"""IMAP 验证码轮询器，用于收取 Mistral 验证邮件（兼容 catch-all）。

- 复用同一个 IMAP 连接
- 先只拉邮件头，命中候选才拉完整正文
- 睡眠放在锁外，方便多个 worker 交错执行
"""
from __future__ import annotations

import email as email_lib
import imaplib
import re
import threading
import time
from email.header import decode_header, make_header

from . import config

_CODE_RE = re.compile(r"\b(\d{6})\b")
_imap_lock = threading.Lock()
_mail: imaplib.IMAP4_SSL | None = None
_mail_until = 0.0
_REUSE_S = 60.0


def _decode_subj(raw) -> str:
    if raw is None:
        return ""
    try:
        return str(make_header(decode_header(raw)))
    except Exception:
        return raw.decode("utf-8", "replace") if isinstance(raw, bytes) else str(raw)


def _body(msg: email_lib.message.Message) -> str:
    parts: list[str] = []
    if msg.is_multipart():
        for part in msg.walk():
            if "text" not in (part.get_content_type() or ""):
                continue
            try:
                payload = part.get_payload(decode=True) or b""
                charset = part.get_content_charset() or "utf-8"
                parts.append(payload.decode(charset, "replace"))
            except Exception:
                continue
    else:
        try:
            payload = msg.get_payload(decode=True) or b""
            charset = msg.get_content_charset() or "utf-8"
            parts.append(payload.decode(charset, "replace"))
        except Exception:
            pass
    return "\n".join(parts)


def _close_mail() -> None:
    global _mail, _mail_until
    if _mail is not None:
        try:
            _mail.logout()
        except Exception:
            try:
                _mail.shutdown()
            except Exception:
                pass
    _mail = None
    _mail_until = 0.0


def _get_mail() -> imaplib.IMAP4_SSL:
    """返回一个可用的 IMAP 连接。调用方必须先持有 _imap_lock。"""
    global _mail, _mail_until
    now = time.monotonic()
    if _mail is not None and now < _mail_until:
        return _mail
    _close_mail()
    m = imaplib.IMAP4_SSL(config.IMAP_HOST, config.IMAP_PORT)
    m.login(config.IMAP_USER, config.IMAP_PASS)
    typ, _ = m.select("INBOX")
    if typ != "OK":
        raise RuntimeError("IMAP SELECT INBOX 失败")
    _mail = m
    _mail_until = now + _REUSE_S
    return m


def wait_otp(
    email_addr: str,
    *,
    timeout: int | None = None,
    poll: float | None = None,
) -> str:
    """轮询 IMAP，等待 email_addr 对应的 6 位 Mistral 验证码。"""
    if not config.IMAP_USER or not config.IMAP_PASS:
        raise RuntimeError("需要配置 IMAP_USER / IMAP_PASS")
    timeout = timeout if timeout is not None else config.OTP_TIMEOUT_S
    poll = max(0.3, float(poll if poll is not None else config.OTP_POLL_S))
    deadline = time.monotonic() + timeout
    target = email_addr.lower()
    local = target.split("@")[0]
    seen: set[bytes] = set()

    while time.monotonic() < deadline:
        code: str | None = None
        with _imap_lock:
            try:
                mail = _get_mail()
                ids: list[bytes] = []
                for criterion in (
                    f'(TO "{email_addr}")',
                    f'(TO "{local}")',
                    '(OR FROM "mistral" SUBJECT "verify")',
                    "UNSEEN",
                    "ALL",
                ):
                    typ, data = mail.search(None, criterion)
                    if typ == "OK" and data and data[0]:
                        ids = data[0].split()
                        if criterion != "ALL" and ids:
                            break

                for msg_id in reversed(ids[-60:]):
                    if msg_id in seen:
                        continue
                    typ, msg_data = mail.fetch(
                        msg_id,
                        "(BODY.PEEK[HEADER.FIELDS (FROM SUBJECT TO DELIVERED-TO X-ORIGINAL-TO CC)])",
                    )
                    if typ != "OK" or not msg_data or not msg_data[0]:
                        seen.add(msg_id)
                        continue
                    raw_hdr = msg_data[0][1]
                    if not isinstance(raw_hdr, (bytes, bytearray)):
                        seen.add(msg_id)
                        continue

                    hdr = email_lib.message_from_bytes(raw_hdr)
                    fr = (hdr.get("From") or "").lower()
                    subj = _decode_subj(hdr.get("Subject"))
                    subj_l = subj.lower()
                    to_blob = " ".join(filter(None, [
                        hdr.get("To", ""),
                        hdr.get("Delivered-To", ""),
                        hdr.get("X-Original-To", ""),
                        hdr.get("Cc", ""),
                    ])).lower()
                    to_hit = target in to_blob or local in to_blob
                    mistralish = (
                        "mistral" in fr
                        or "mistral" in subj_l
                        or "verify" in subj_l
                        or "verification" in subj_l
                    )
                    if not to_hit and not mistralish:
                        seen.add(msg_id)
                        continue

                    codes = _CODE_RE.findall(subj)
                    if codes and (to_hit or local in subj_l):
                        code = codes[0]
                        break

                    typ2, body_data = mail.fetch(msg_id, "(BODY.PEEK[])")
                    if typ2 != "OK" or not body_data or not body_data[0]:
                        seen.add(msg_id)
                        continue
                    raw = body_data[0][1]
                    if not isinstance(raw, (bytes, bytearray)):
                        seen.add(msg_id)
                        continue
                    body = _body(email_lib.message_from_bytes(raw))
                    blob = f"{to_blob}\n{body}".lower()
                    if target not in blob and local not in blob:
                        seen.add(msg_id)
                        continue
                    if not mistralish and "mistral" not in body.lower():
                        seen.add(msg_id)
                        continue
                    codes = _CODE_RE.findall(body) or _CODE_RE.findall(subj)
                    if codes:
                        code = codes[0]
                        break
                    seen.add(msg_id)
            except Exception:
                _close_mail()
        if code:
            return code
        time.sleep(poll)
    raise TimeoutError(f"验证码超时：{email_addr} 等待 {timeout}s 仍未收到")
