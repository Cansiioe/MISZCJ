"""邮件 API 服务 邮箱取码器。

接口（除 domains 外均按 mailboxId 操作，全部需要 Authorization: Bearer <key>）:
  GET  /domains                               → 域名列表（拿 domainId）
  POST /mailboxes                             {localPart, domainId, expiresInHours} → {id, address}
  GET  /mailboxes/{id}/messages               → {items:[...]}
  GET  /mailboxes/{id}/messages/{mid}/raw        邮件原文
  GET  /mailboxes/{id}/messages/{mid}/raw/parsed 解析后的结构化内容
  POST /mailboxes/{id}/release                释放
  POST /mailboxes/{id}/extend                 {expiresInHours} 续期

与 自建邮箱服务 的差别：取码是按 **mailboxId** 查，不是按收件地址，
所以 wait_otp 必须带上 create_mailbox() 返回的 mailbox_id。
"""
from __future__ import annotations

import json
import re
import threading
import time
from typing import Any

import requests

from . import config

_CODE_RE = re.compile(r"\b(\d{6})\b")
_TAG_RE = re.compile(r"<[^>]+>")

_seen_lock = threading.Lock()
_seen_ids: set[tuple[str, str]] = set()
_SEEN_CAP = 5000


class MailAPIError(RuntimeError):
    """邮件 API 服务 接口返回的错误。"""


def _base() -> str:
    return (config.MAILAPI_BASE or "").rstrip("/")


def _check_config() -> None:
    missing = [k for k, v in {
        "MAILAPI_BASE": config.MAILAPI_BASE,
        "MAILAPI_KEY": config.MAILAPI_KEY,
    }.items() if not v]
    if missing:
        raise MailAPIError(f"缺少配置: {', '.join(missing)}")


def _request(method: str, path: str, **kw) -> Any:
    """发请求并统一处理错误码。"""
    _check_config()
    headers = {
        "Authorization": f"Bearer {config.MAILAPI_KEY}",
        "Accept": "application/json",
    }
    if "json" in kw:
        headers["Content-Type"] = "application/json"
    try:
        resp = requests.request(
            method, f"{_base()}{path}", headers=headers,
            timeout=config.HTTP_TIMEOUT_S, **kw,
        )
    except requests.RequestException as e:
        raise MailAPIError(f"{method} {path} 请求失败: {e}") from e

    if resp.status_code == 401:
        raise MailAPIError(f"{method} {path} 鉴权失败(401): API Key 无效")
    if resp.status_code == 404:
        raise MailAPIError(f"{method} {path} 资源不存在(404): {resp.text[:160]}")
    if resp.status_code >= 400:
        raise MailAPIError(f"{method} {path} HTTP {resp.status_code}: {resp.text[:200]}")
    if not resp.content:
        return None
    try:
        return resp.json()
    except Exception:
        return resp.text


def create_mailbox(email_addr: str) -> str:
    """创建收件邮箱，返回 mailboxId（字符串）。"""
    local = email_addr.split("@")[0]
    body: dict[str, Any] = {
        "localPart": local,
        "domainId": config.MAILAPI_DOMAIN_ID,
    }
    if config.MAILAPI_PERMANENT:
        body["permanent"] = True
    else:
        # 创建时 expiresInHours 是必填项
        body["expiresInHours"] = config.MAILAPI_EXPIRES_HOURS

    data = _request("POST", "/mailboxes", json=body)
    if not isinstance(data, dict) or data.get("id") is None:
        raise MailAPIError(f"创建邮箱未返回 id: {data}")
    return str(data["id"])


def release_mailbox(mailbox_id: str | int) -> None:
    """释放邮箱（释放后可用 extend 重新激活同一地址）。"""
    _request("POST", f"/mailboxes/{mailbox_id}/release")


def extend_mailbox(mailbox_id: str | int, hours: int | None = None) -> None:
    _request(
        "POST",
        f"/mailboxes/{mailbox_id}/extend",
        json={"expiresInHours": hours or config.MAILAPI_EXPIRES_HOURS},
    )


def list_messages(mailbox_id: str | int, *, limit: int = 20) -> list[dict]:
    """列出邮箱里的邮件。"""
    data = _request("GET", f"/mailboxes/{mailbox_id}/messages", params={"limit": limit})
    if isinstance(data, dict):
        items = data.get("items")
        if isinstance(items, list):
            return items
    if isinstance(data, list):
        return data
    return []


def fetch_message_text(mailbox_id: str | int, message_id: Any) -> str:
    """取邮件正文：优先用解析后的结构化内容，失败再回退原始 MIME。"""
    # 1) /raw/parsed —— 服务端已解析，拿 text / html 字段
    try:
        parsed = _request("GET", f"/mailboxes/{mailbox_id}/messages/{message_id}/raw/parsed")
    except MailAPIError:
        parsed = None

    text = _flatten_parsed(parsed)
    if text.strip():
        return text

    # 2) /raw —— 原始 MIME 原文
    raw = _request("GET", f"/mailboxes/{mailbox_id}/messages/{message_id}/raw")
    if isinstance(raw, str):
        return raw
    return json.dumps(raw, ensure_ascii=False)


def _flatten_parsed(parsed: Any) -> str:
    """把 parsed 结构里的文本内容拼成一段纯文本。"""
    if parsed is None:
        return ""
    if isinstance(parsed, str):
        return parsed
    if isinstance(parsed, list):
        return "\n".join(_flatten_parsed(x) for x in parsed)
    if not isinstance(parsed, dict):
        return str(parsed)

    chunks: list[str] = []
    for key in ("subject", "text", "textBody", "plain", "body", "html", "htmlBody"):
        v = parsed.get(key)
        if isinstance(v, str) and v:
            chunks.append(_strip_html(v) if "html" in key.lower() else v)
    # 常见的嵌套结构
    for key in ("data", "message", "parsed", "content"):
        if key in parsed:
            chunks.append(_flatten_parsed(parsed[key]))
    return "\n".join(c for c in chunks if c)


def _strip_html(html: str) -> str:
    return _TAG_RE.sub(" ", html or "")


def _extract_code(text: str) -> str | None:
    """从邮件文本里提取 6 位验证码。"""
    if not text:
        return None
    low = text.lower()
    mistralish = "mistral" in low or "verif" in low
    if not mistralish:
        return None
    codes = _CODE_RE.findall(text)
    return codes[0] if codes else None


def _remember(mailbox_id: str | int, message_id: Any) -> bool:
    """标记 (mailboxId, messageId) 已处理；返回 True 表示首次见到。"""
    key = (str(mailbox_id), str(message_id))
    with _seen_lock:
        if key in _seen_ids:
            return False
        _seen_ids.add(key)
        if len(_seen_ids) > _SEEN_CAP:
            _seen_ids.clear()
        return True


def _message_id(row: dict) -> Any:
    for k in ("id", "messageId", "message_id"):
        if row.get(k) is not None:
            return row[k]
    return None


def wait_otp(
    email_addr: str,
    *,
    mailbox_id: str | int | None = None,
    timeout: int | None = None,
    poll: float | None = None,
) -> str:
    """轮询 邮件 API 服务，等待 email_addr 收到 Mistral 的 6 位验证码。"""
    _check_config()
    if mailbox_id is None:
        raise MailAPIError("邮件 API 服务 取码需要 mailbox_id（请先 create_mailbox）")

    timeout = timeout if timeout is not None else config.OTP_TIMEOUT_S
    poll = max(0.5, float(poll if poll is not None else config.OTP_POLL_S))
    deadline = time.monotonic() + timeout

    while time.monotonic() < deadline:
        try:
            rows = list_messages(mailbox_id, limit=20)
        except MailAPIError:
            time.sleep(poll)
            continue

        for row in rows:
            mid = _message_id(row)
            if mid is None or not _remember(mailbox_id, mid):
                continue

            # 先看列表里就带的主题
            subject = row.get("subject") or ""
            code = _extract_code(subject)
            if code:
                return code

            try:
                body = fetch_message_text(mailbox_id, mid)
            except MailAPIError:
                continue
            code = _extract_code(body)
            if code:
                return code

        time.sleep(poll)

    raise TimeoutError(f"验证码超时：{email_addr} 等待 {timeout}s 仍未收到")
