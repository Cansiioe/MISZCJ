"""Mistral HTTP 注册机的环境变量 / 配置。"""
from __future__ import annotations

import os
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

try:
    from dotenv import load_dotenv

    load_dotenv(ROOT / ".env")
    load_dotenv(ROOT / ".env.farm")
except Exception:
    pass


def _env(key: str, default: str = "") -> str:
    return (os.environ.get(key) or default).strip()


def _env_bool(key: str, default: bool) -> bool:
    raw = _env(key, "1" if default else "0").lower()
    if raw in ("1", "true", "yes", "on"):
        return True
    if raw in ("0", "false", "no", "off"):
        return False
    return default


def _env_int(key: str, default: int) -> int:
    try:
        return int(_env(key, str(default)) or default)
    except ValueError:
        return default


def _env_float(key: str, default: float) -> float:
    try:
        return float(_env(key, str(default)) or default)
    except ValueError:
        return default


AUTH_BASE = _env("MISTRAL_AUTH_BASE", "https://auth.mistral.ai").rstrip("/")
CONSOLE_BASE = _env("MISTRAL_CONSOLE_BASE", "https://console.mistral.ai").rstrip("/")
ADMIN_BASE = _env("MISTRAL_ADMIN_BASE", "https://admin.mistral.ai").rstrip("/")
API_BASE = _env("MISTRAL_API_BASE", "https://api.mistral.ai").rstrip("/")

IMAP_USER = _env("IMAP_USER")
IMAP_PASS = _env("IMAP_PASS")
IMAP_HOST = _env("IMAP_HOST", "imap.example.com")
IMAP_PORT = _env_int("IMAP_PORT", 993)
EMAIL_DOMAIN = _env("EMAIL_DOMAIN").lstrip("@")

# --- 取码通道: mailapi | selfhost | imap ---
# 不显式设置 OTP_PROVIDER 时，按已填的凭据自动推断
def _guess_provider() -> str:
    if _env("MAILAPI_KEY"):
        return "mailapi"
    if _env("SELFHOST_BASE"):
        return "selfhost"
    return "imap"


OTP_PROVIDER = _env("OTP_PROVIDER").lower() or _guess_provider()

# --- 邮件 API 服务 ---
MAILAPI_BASE = _env("MAILAPI_BASE", "https://em.example.com/api/v1").rstrip("/")
MAILAPI_KEY = _env("MAILAPI_KEY")
MAILAPI_DOMAIN_ID = _env_int("MAILAPI_DOMAIN_ID", 1)
MAILAPI_EXPIRES_HOURS = _env_int("MAILAPI_EXPIRES_HOURS", 24)
# 设 1 则创建永久邮箱（此时不传 expiresInHours）
MAILAPI_PERMANENT = _env_bool("MAILAPI_PERMANENT", False)

# --- 自建邮箱服务 ---
SELFHOST_BASE = _env("SELFHOST_BASE").rstrip("/")
SELFHOST_ADMIN = _env("SELFHOST_ADMIN")
SELFHOST_PASSWORD = _env("SELFHOST_PASSWORD")
# 注册前先调 addUser 建好邮箱（部署关闭了「无收件人仍收信」时必须开）
SELFHOST_CREATE_USER = _env_bool("SELFHOST_CREATE_USER", True)

MAX_ACCOUNTS = _env_int("MAX_ATTEMPTS", _env_int("MAX_ACCOUNTS", 10))
CONCURRENCY = max(1, min(_env_int("CONCURRENCY", 8), 32))
SPAWN_DELAY_S = _env_float("SPAWN_DELAY_S", 0.25)
OTP_TIMEOUT_S = _env_int("OTP_TIMEOUT_S", 100)
OTP_POLL_S = _env_float("OTP_POLL_S", 0.6)
HTTP_TIMEOUT_S = _env_float("HTTP_TIMEOUT_S", 25)
ACCOUNT_RETRIES = max(1, _env_int("ACCOUNT_RETRIES", 2))
LOGIN_RETRIES = _env_int("LOGIN_RETRIES", 4)
RETRY_429_BASE_S = _env_float("RETRY_429_BASE_S", 1.5)

# 优先读 SKIP_SMOKE；SMOKE_MODELS 仅为向后兼容保留。默认跳过冒烟测试。
if _env("SKIP_SMOKE"):
    SMOKE_MODELS = not _env_bool("SKIP_SMOKE", True)
else:
    SMOKE_MODELS = _env_bool("SMOKE_MODELS", False)

RESULTS_FILE = _env("RESULTS_FILE", str(ROOT / "results.json"))
BATCH_DIR = _env("BATCH_DIR", str(ROOT / "results"))

PROXY_URL = _env("PROXY_URL")
PROXY_FILE = Path(_env("PROXY_FILE", str(ROOT / "config" / "proxies.ranked.txt")))
if not PROXY_FILE.is_absolute():
    PROXY_FILE = ROOT / PROXY_FILE
PROXY_LIST_FILE = _env("PROXY_LIST_FILE")
PROXY_BAN_AFTER = _env_int("PROXY_BAN_AFTER", 2)

UA = _env(
    "HTTP_UA",
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
)


def require_otp() -> None:
    if OTP_PROVIDER == "mailapi":
        required = {
            "MAILAPI_BASE": MAILAPI_BASE,
            "MAILAPI_KEY": MAILAPI_KEY,
            "EMAIL_DOMAIN": EMAIL_DOMAIN,
        }
    elif OTP_PROVIDER == "selfhost":
        required = {
            "SELFHOST_BASE": SELFHOST_BASE,
            "SELFHOST_ADMIN": SELFHOST_ADMIN,
            "SELFHOST_PASSWORD": SELFHOST_PASSWORD,
            "EMAIL_DOMAIN": EMAIL_DOMAIN,
        }
    else:
        required = {
            "IMAP_USER": IMAP_USER,
            "IMAP_PASS": IMAP_PASS,
            "EMAIL_DOMAIN": EMAIL_DOMAIN,
        }
    missing = [k for k, v in required.items() if not v]
    if missing:
        raise SystemExit(f"错误: 缺少必需的环境变量: {', '.join(missing)}")


# 兼容旧调用名
require_imap = require_otp
