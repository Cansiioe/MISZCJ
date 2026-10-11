"""验证码获取入口：按 OTP_PROVIDER 分发到 邮件 API 服务 / 自建邮箱服务 / IMAP。"""
from __future__ import annotations

from . import config


def create_mailbox(email_addr: str) -> str | None:
    """按当前通道预建收件邮箱。

    返回 mailbox_id —— 只有 邮件 API 服务 需要（它是按 id 查邮件的），
    其余通道返回 None。
    """
    if config.OTP_PROVIDER == "mailapi":
        from .mailapi_otp import create_mailbox as _create
        return _create(email_addr)
    if config.OTP_PROVIDER == "selfhost":
        from .selfhost_otp import add_user
        add_user([email_addr])
        return None
    return None


def needs_precreate() -> bool:
    """注册前是否需要先建邮箱。"""
    if config.OTP_PROVIDER == "mailapi":
        return True
    if config.OTP_PROVIDER == "selfhost":
        return config.SELFHOST_CREATE_USER
    return False


def wait_otp(
    email_addr: str,
    *,
    mailbox_id: str | None = None,
    timeout: int | None = None,
    poll: float | None = None,
) -> str:
    if config.OTP_PROVIDER == "mailapi":
        from .mailapi_otp import wait_otp as _wait
        return _wait(email_addr, mailbox_id=mailbox_id, timeout=timeout, poll=poll)
    if config.OTP_PROVIDER == "selfhost":
        from .selfhost_otp import wait_otp as _wait
        return _wait(email_addr, timeout=timeout, poll=poll)
    from .imap_otp import wait_otp as _wait
    return _wait(email_addr, timeout=timeout, poll=poll)
