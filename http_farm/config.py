"""Env/config for mistral HTTP farm."""
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

MAX_ACCOUNTS = _env_int("MAX_ATTEMPTS", _env_int("MAX_ACCOUNTS", 10))
CONCURRENCY = max(1, min(_env_int("CONCURRENCY", 8), 32))
SPAWN_DELAY_S = _env_float("SPAWN_DELAY_S", 0.25)
OTP_TIMEOUT_S = _env_int("OTP_TIMEOUT_S", 100)
OTP_POLL_S = _env_float("OTP_POLL_S", 0.6)
HTTP_TIMEOUT_S = _env_float("HTTP_TIMEOUT_S", 25)
ACCOUNT_RETRIES = max(1, _env_int("ACCOUNT_RETRIES", 2))
LOGIN_RETRIES = _env_int("LOGIN_RETRIES", 4)
RETRY_429_BASE_S = _env_float("RETRY_429_BASE_S", 1.5)

# Prefer SKIP_SMOKE; SMOKE_MODELS kept for back-compat. Default: skip smoke.
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


def require_imap() -> None:
    missing = [k for k, v in {
        "IMAP_USER": IMAP_USER,
        "IMAP_PASS": IMAP_PASS,
        "EMAIL_DOMAIN": EMAIL_DOMAIN,
    }.items() if not v]
    if missing:
        raise SystemExit(f"ERROR: required env missing: {', '.join(missing)}")
