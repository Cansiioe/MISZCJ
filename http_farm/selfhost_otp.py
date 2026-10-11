"""自建邮箱服务 邮箱取码器（对接 maillab/cloud-mail 的公开 API）。

用 HTTP 接口替代 IMAP 收 Mistral 验证码。

接口（均为 POST，除 genToken 外都需要 Authorization 头）:
  POST /api/public/genToken   {email, password}                 → {token}
  POST /api/public/emailList  {toEmail, size, num, timeSort...}  → 邮件列表
  POST /api/public/addUser    {list: [{email, password?}]}       → 批量建邮箱

重要：服务端把 token 存在 KV 的**单个全局键** `public_key:` 上，
每次 genToken 都会覆盖旧值，因此本模块在进程内只取一次并复用，
只在收到 401 时才强制刷新。
"""
from __future__ import annotations

import re
import threading
import time
from typing import Any

import requests

from . import config

_CODE_RE = re.compile(r"\b(\d{6})\b")
_TAG_RE = re.compile(r"<[^>]+>")

_token_lock = threading.Lock()
_cached_token: str | None = None

_seen_lock = threading.Lock()
_seen_ids: set[int] = set()
_SEEN_CAP = 5000


class SelfHostError(RuntimeError):
    """自建邮箱服务 接口返回的业务错误。"""


def _base() -> str:
    return (config.SELFHOST_BASE or "").rstrip("/")


def _check_config() -> None:
    missing = [k for k, v in {
        "SELFHOST_BASE": config.SELFHOST_BASE,
        "SELFHOST_ADMIN": config.SELFHOST_ADMIN,
        "SELFHOST_PASSWORD": config.SELFHOST_PASSWORD,
    }.items() if not v]
    if missing:
        raise SelfHostError(f"缺少配置: {', '.join(missing)}")


def gen_token(force: bool = False) -> str:
    """取得公开 API token。进程内缓存；force=True 时强制重新获取。"""
    global _cached_token
    with _token_lock:
        if _cached_token and not force:
            return _cached_token
        _check_config()
        try:
            resp = requests.post(
                f"{_base()}/api/public/genToken",
                json={"email": config.SELFHOST_ADMIN, "password": config.SELFHOST_PASSWORD},
                timeout=config.HTTP_TIMEOUT_S,
            )
        except requests.RequestException as e:
            raise SelfHostError(f"genToken 请求失败: {e}") from e
        data = _unwrap(resp)
        token = data.get("token") if isinstance(data, dict) else None
        if not token:
            raise SelfHostError(f"genToken 未返回 token: {data}")
        _cached_token = str(token)
        return _cached_token


def _unwrap(resp: requests.Response) -> Any:
    """解析统一响应体 {code, message, data}；code 非 200 抛异常。"""
    try:
        body = resp.json()
    except Exception:
        raise SelfHostError(f"返回非 JSON: HTTP {resp.status_code} {resp.text[:200]}")
    code = body.get("code")
    if code != 200:
        raise SelfHostError(f"接口错误 code={code}: {body.get('message')}")
    return body.get("data")


def _post(path: str, payload: dict, *, _retry: bool = True) -> Any:
    """带 token 的 POST；token 失效时自动刷新一次。"""
    token = gen_token()
    try:
        resp = requests.post(
            f"{_base()}/api/public/{path}",
            json=payload,
            headers={"Authorization": token, "Content-Type": "application/json"},
            timeout=config.HTTP_TIMEOUT_S,
        )
    except requests.RequestException as e:
        raise SelfHostError(f"{path} 请求失败: {e}") from e

    try:
        body = resp.json()
    except Exception:
        raise SelfHostError(f"{path} 返回非 JSON: HTTP {resp.status_code} {resp.text[:200]}")

    # token 被其他进程顶掉时，服务端返回 HTTP 200 + code=401
    if body.get("code") == 401 and _retry:
        gen_token(force=True)
        return _post(path, payload, _retry=False)

    code = body.get("code")
    if code != 200:
        raise SelfHostError(f"{path} 错误 code={code}: {body.get('message')}")
    return body.get("data")


def add_user(emails: list[str], password: str | None = None) -> None:
    """批量创建邮箱。已存在的邮箱会被服务端拒绝，这里静默忽略。"""
    if not emails:
        return
    rows: list[dict[str, Any]] = []
    for e in emails:
        row: dict[str, Any] = {"email": e}
        if password:
            row["password"] = password
        rows.append(row)
    try:
        _post("addUser", {"list": rows})
    except SelfHostError as e:
        msg = str(e)
        low = msg.lower()
        # 服务端对已存在邮箱返回 501「有邮箱已存在数据库中」/ SQLITE_CONSTRAINT
        if "exist" in low or "constraint" in low or "已存在" in msg:
            return
        raise


def email_list(to_email: str, *, size: int = 20, num: int = 1) -> list[dict]:
    """按收件地址查邮件（服务端用 LIKE，传完整地址即精确匹配）。"""
    data = _post("emailList", {"toEmail": to_email, "size": size, "num": num})
    return data if isinstance(data, list) else []


def _strip_html(html: str) -> str:
    return _TAG_RE.sub(" ", html or "")


def _extract_code(row: dict) -> str | None:
    """从一封邮件里提取 6 位验证码（先看主题，再看正文）。"""
    subject = row.get("subject") or ""
    subject_l = subject.lower()
    send_email = (row.get("sendEmail") or "").lower()
    send_name = (row.get("sendName") or "").lower()

    mistralish = (
        "mistral" in send_email
        or "mistral" in send_name
        or "mistral" in subject_l
        or "verif" in subject_l
    )
    if not mistralish:
        return None

    codes = _CODE_RE.findall(subject)
    if codes:
        return codes[0]

    body = row.get("text") or _strip_html(row.get("content") or "")
    codes = _CODE_RE.findall(body)
    if codes:
        return codes[0]
    return None


def _remember(email_id: int) -> bool:
    """标记 emailId 已处理；返回 True 表示首次见到。"""
    with _seen_lock:
        if email_id in _seen_ids:
            return False
        _seen_ids.add(email_id)
        if len(_seen_ids) > _SEEN_CAP:
            _seen_ids.clear()
        return True


def wait_otp(
    email_addr: str,
    *,
    timeout: int | None = None,
    poll: float | None = None,
) -> str:
    """轮询 自建邮箱服务，等待 email_addr 收到 Mistral 的 6 位验证码。"""
    _check_config()
    timeout = timeout if timeout is not None else config.OTP_TIMEOUT_S
    poll = max(0.5, float(poll if poll is not None else config.OTP_POLL_S))
    deadline = time.monotonic() + timeout
    target = email_addr.lower()

    while time.monotonic() < deadline:
        try:
            rows = email_list(target, size=20)
        except SelfHostError:
            time.sleep(poll)
            continue

        for row in rows:
            if (row.get("toEmail") or "").lower() != target:
                continue
            email_id = int(row.get("emailId") or 0)
            if not email_id or not _remember(email_id):
                continue
            code = _extract_code(row)
            if code:
                return code

        time.sleep(poll)

    raise TimeoutError(f"验证码超时：{email_addr} 等待 {timeout}s 仍未收到")
