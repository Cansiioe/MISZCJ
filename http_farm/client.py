"""HTTP client for Mistral Ory auth + console/admin billing key APIs."""
from __future__ import annotations

import random
import string
import time
from typing import Any
from urllib.parse import parse_qs, urlparse

import requests
from requests.adapters import HTTPAdapter

from . import config
from .imap_otp import wait_otp

FIRST_NAMES = [
    "John", "Jane", "Alex", "Sam", "Chris", "Emma", "Noah", "Mia",
    "Lucas", "Lily", "Ryan", "Sofia", "Ethan", "Zoe", "Oliver",
    "Ava", "Liam", "Isabella", "Mason", "Charlotte",
]
LAST_NAMES = [
    "Smith", "Johnson", "Brown", "Davis", "Wilson", "Moore",
    "Taylor", "Anderson", "Thomas", "Jackson", "White", "Harris",
    "Martin", "Thompson", "Garcia", "Martinez", "Robinson", "Clark",
]

_TRANSIENT = ("429", "rate", "timeout", "proxy", "connection", "connect", "timed out")


def random_email(domain: str | None = None) -> str:
    domain = domain or config.EMAIL_DOMAIN
    ts = str(int(time.time() * 1000))[-10:]
    name = ts + "".join(random.choices(string.ascii_lowercase, k=5))
    return f"{name}@{domain}"


def gen_password() -> str:
    return "Aa1!" + "".join(random.choices(string.ascii_letters + string.digits, k=12))


def gen_name() -> tuple[str, str]:
    return random.choice(FIRST_NAMES), random.choice(LAST_NAMES)


def _flow_messages(data: dict) -> str:
    msgs: list[str] = []
    ui = data.get("ui") or {}
    for n in ui.get("nodes") or []:
        for m in n.get("messages") or []:
            msgs.append(m.get("text") or str(m))
    for m in ui.get("messages") or []:
        msgs.append(m.get("text") or str(m))
    return "; ".join(msgs)


def _is_rate_limited(resp: requests.Response) -> bool:
    if resp.status_code == 429:
        return True
    try:
        t = resp.text.lower()
    except Exception:
        return False
    return "too many" in t and ("request" in t or "api" in t)


def _looks_unauthenticated(status: int, text: str) -> bool:
    if status in (301, 302, 303, 307, 308, 401, 403):
        return True
    return "choose_method" in text or "self-service/login" in text


class MistralHTTPClient:
    def __init__(
        self,
        proxy: str | None = None,
        timeout: float | None = None,
        pool=None,
    ):
        self.timeout = timeout or config.HTTP_TIMEOUT_S
        self.pool = pool
        if proxy is not None:
            self.proxy = proxy
        elif pool is not None:
            self.proxy = pool.next()
        else:
            self.proxy = config.PROXY_URL or None

        self.s = requests.Session()
        self.s.headers.update({"User-Agent": config.UA, "Accept": "application/json"})
        adapter = HTTPAdapter(pool_connections=20, pool_maxsize=20, max_retries=0)
        self.s.mount("https://", adapter)
        self.s.mount("http://", adapter)
        self._apply_proxy(self.proxy)

        self.session_token: str | None = None
        self.email: str | None = None
        self.password: str | None = None
        self.first_name: str | None = None
        self.last_name: str | None = None
        self.api_key: str | None = None
        self.key_id: str | None = None

    # ---- proxy ----
    def _apply_proxy(self, proxy: str | None) -> None:
        self.proxy = proxy
        if proxy:
            self.s.proxies.update({"http": proxy, "https": proxy})
        else:
            self.s.proxies.clear()

    def rotate_proxy(self, reason: str = "") -> str | None:
        if self.pool is None:
            return self.proxy
        if self.proxy:
            self.pool.mark_fail(self.proxy)
        nxt = self.pool.next()
        self.s.cookies.clear()
        self._apply_proxy(nxt)
        return nxt

    def mark_proxy_ok(self) -> None:
        if self.pool is not None:
            self.pool.mark_ok(self.proxy)

    def _has_ory_session(self) -> bool:
        return any(c.name.startswith("ory_session") for c in self.s.cookies)

    def _csrf_cookie(self, host_substr: str = "") -> str:
        for c in self.s.cookies:
            if c.name == "csrftoken" and (not host_substr or host_substr in (c.domain or "")):
                return c.value
        for c in self.s.cookies:
            if c.name == "csrftoken":
                return c.value
        return ""

    def _seed_console_cookies(self) -> None:
        self.s.get(f"{config.CONSOLE_BASE}/", headers={"Accept": "text/html"}, timeout=self.timeout)
        self.s.get(f"{config.ADMIN_BASE}/join", headers={"Accept": "text/html"}, timeout=self.timeout)

    @staticmethod
    def _csrf_from_flow(flow: dict) -> str:
        for n in (flow.get("ui") or {}).get("nodes") or []:
            attrs = n.get("attributes") or {}
            if attrs.get("name") == "csrf_token":
                return attrs.get("value") or ""
        return ""

    # ---- HTTP ----
    def _get(self, url: str, **kw) -> requests.Response:
        kw.setdefault("timeout", self.timeout)
        return self.s.get(url, **kw)

    def _post(self, url: str, **kw) -> requests.Response:
        kw.setdefault("timeout", self.timeout)
        return self.s.post(url, **kw)

    def _request_with_rotate(self, method: str, url: str, *, attempts: int = 3, **kw) -> requests.Response:
        last: Exception | None = None
        for i in range(1, attempts + 1):
            try:
                r = self.s.request(method, url, timeout=self.timeout, **kw)
                if _is_rate_limited(r) and i < attempts:
                    self.rotate_proxy(f"{method} {r.status_code}")
                    time.sleep(config.RETRY_429_BASE_S * i)
                    continue
                return r
            except (requests.RequestException, OSError) as e:
                last = e
                if i >= attempts:
                    break
                self.rotate_proxy(str(e)[:60])
                time.sleep(config.RETRY_429_BASE_S * i)
        raise RuntimeError(f"{method} {url} failed: {last}")

    # ---- registration ----
    def register(
        self,
        email: str | None = None,
        password: str | None = None,
        first: str | None = None,
        last: str | None = None,
        retries: int = 3,
    ) -> dict[str, Any]:
        email = email or random_email()
        password = password or gen_password()
        if not first or not last:
            f, l = gen_name()
            first = first or f
            last = last or l
        self.email, self.password = email, password
        self.first_name, self.last_name = first, last

        last_err: Exception | None = None
        for attempt in range(1, retries + 1):
            try:
                r = self._request_with_rotate("GET", f"{config.AUTH_BASE}/self-service/registration/api")
                if r.status_code != 200:
                    raise RuntimeError(f"registration/api {r.status_code}: {r.text[:200]}")
                flow = r.json()
                action = (flow.get("ui") or {}).get("action") or (
                    f"{config.AUTH_BASE}/self-service/registration?flow={flow['id']}"
                )
                body = {
                    "method": "password",
                    "traits": {"email": email, "name": {"first": first, "last": last}},
                    "password": password,
                }
                r2 = self._request_with_rotate(
                    "POST", action, json=body, headers={"Content-Type": "application/json"}
                )
                if r2.status_code not in (200, 201, 422):
                    raise RuntimeError(f"register failed {r2.status_code}: {r2.text[:300]}")
                data = r2.json()

                st = data.get("session_token")
                if not st:
                    for cw in data.get("continue_with") or []:
                        if cw.get("action") == "set_ory_session_token":
                            st = cw.get("ory_session_token")
                            break
                if r2.status_code == 422 and not st:
                    raise RuntimeError(f"register validation: {_flow_messages(data) or data}")
                if not st:
                    raise RuntimeError(f"register: no session_token: {list(data.keys())}")

                self.session_token = st
                ver_flow = None
                for cw in data.get("continue_with") or []:
                    if cw.get("action") == "show_verification_ui":
                        ver_flow = (cw.get("flow") or {}).get("id")
                        break
                self.mark_proxy_ok()
                return {
                    "email": email,
                    "password": password,
                    "session_token": st,
                    "verification_flow_id": ver_flow,
                    "raw": data,
                }
            except Exception as e:
                last_err = e
                if attempt >= retries:
                    break
                if any(x in str(e).lower() for x in _TRANSIENT):
                    self.rotate_proxy(str(e)[:80])
                    time.sleep(config.RETRY_429_BASE_S * attempt)
                    continue
                raise
        raise RuntimeError(f"register failed after {retries}: {last_err}")

    def whoami(self) -> dict[str, Any]:
        if not self.session_token:
            raise RuntimeError("no session_token")
        r = self._get(
            f"{config.AUTH_BASE}/sessions/whoami",
            headers={"Authorization": f"Bearer {self.session_token}"},
        )
        r.raise_for_status()
        return r.json()

    def verify_email(self, code: str, flow_id: str | None = None) -> dict[str, Any]:
        if not self.session_token:
            raise RuntimeError("verify_email: no session_token")
        headers = {"Authorization": f"Bearer {self.session_token}"}

        flow = None
        if flow_id:
            r = self._get(
                f"{config.AUTH_BASE}/self-service/verification/flows?id={flow_id}",
                headers=headers,
            )
            if r.status_code == 200:
                flow = r.json()
        if flow is None:
            r = self._get(f"{config.AUTH_BASE}/self-service/verification/api", headers=headers)
            r.raise_for_status()
            flow = r.json()
            flow_id = flow["id"]

        action = (flow.get("ui") or {}).get("action") or (
            f"{config.AUTH_BASE}/self-service/verification?flow={flow_id}"
        )
        r2 = self._post(
            action,
            json={"method": "code", "code": code},
            headers={**headers, "Content-Type": "application/json"},
        )
        if r2.status_code not in (200, 201, 422):
            raise RuntimeError(f"verify failed {r2.status_code}: {r2.text[:300]}")
        data = r2.json()
        addrs = (self.whoami().get("identity") or {}).get("verifiable_addresses") or []
        if not any(a.get("verified") for a in addrs):
            raise RuntimeError(f"email still unverified: state={data.get('state')}")
        return data

    def login_browser_session(self, retries: int | None = None) -> None:
        """Establish ory_session cookie for console/admin APIs."""
        if not self.email or not self.password:
            raise RuntimeError("login_browser_session: email/password required")
        attempts = retries if retries is not None else max(1, config.LOGIN_RETRIES)
        last_err: Exception | None = None

        for attempt in range(1, attempts + 1):
            try:
                r = self.s.get(
                    f"{config.AUTH_BASE}/self-service/login/browser",
                    params={"return_to": f"{config.CONSOLE_BASE}/"},
                    headers={"Accept": "text/html"},
                    allow_redirects=False,
                    timeout=self.timeout,
                )
                if _is_rate_limited(r):
                    raise RuntimeError(f"login/browser rate-limited {r.status_code}: {r.text[:200]}")
                loc = r.headers.get("Location") or ""
                if "flow=" not in loc:
                    raise RuntimeError(
                        f"login/browser no flow redirect: {r.status_code} {loc or r.text[:80]}"
                    )
                flow_id = parse_qs(urlparse(loc).query).get("flow", [None])[0]
                if not flow_id:
                    raise RuntimeError(f"login/browser bad location: {loc}")

                fr = self._get(f"{config.AUTH_BASE}/self-service/login/flows?id={flow_id}")
                if _is_rate_limited(fr):
                    raise RuntimeError(f"login/flows rate-limited: {fr.text[:200]}")
                fr.raise_for_status()
                flow = fr.json()
                csrf = self._csrf_from_flow(flow)
                action = (flow.get("ui") or {}).get("action") or (
                    f"{config.AUTH_BASE}/self-service/login?flow={flow_id}"
                )
                form_headers = {
                    "Accept": "application/json",
                    "Content-Type": "application/x-www-form-urlencoded",
                }
                r1 = self.s.post(
                    action,
                    data={
                        "method": "identifier_first",
                        "identifier": self.email,
                        "csrf_token": csrf,
                    },
                    headers=form_headers,
                    timeout=self.timeout,
                )
                if _is_rate_limited(r1):
                    raise RuntimeError(f"identifier_first rate-limited: {r1.text[:200]}")
                f2 = r1.json()
                action2 = (f2.get("ui") or {}).get("action") or action
                r2 = self.s.post(
                    action2,
                    data={
                        "method": "password",
                        "identifier": self.email,
                        "password": self.password,
                        "csrf_token": self._csrf_from_flow(f2),
                    },
                    headers=form_headers,
                    timeout=self.timeout,
                )
                if _is_rate_limited(r2):
                    raise RuntimeError(f"password login rate-limited {r2.status_code}: {r2.text[:200]}")
                if r2.status_code not in (200, 201, 422):
                    raise RuntimeError(f"password login failed {r2.status_code}: {r2.text[:300]}")
                data = r2.json()
                if "session" not in data and "session_token" not in data:
                    raise RuntimeError(f"password login no session: {list(data.keys())}")
                if data.get("session_token"):
                    self.session_token = data["session_token"]

                self._seed_console_cookies()
                self.mark_proxy_ok()
                return
            except Exception as e:
                last_err = e
                if attempt >= attempts:
                    break
                msg = str(e).lower()
                if any(x in msg for x in _TRANSIENT) or "rate-limited" in msg:
                    self.rotate_proxy(str(e)[:80])
                time.sleep(config.RETRY_429_BASE_S * attempt)
        raise RuntimeError(f"login_browser_session failed after {attempts} tries: {last_err}")

    def _ensure_session(self) -> str:
        """Ensure ory cookie + csrf exist; return csrf token."""
        if not self._has_ory_session():
            self.login_browser_session(retries=2)
        csrf = self._csrf_cookie("admin") or self._csrf_cookie("console")
        if not csrf:
            self._seed_console_cookies()
            csrf = self._csrf_cookie("admin") or self._csrf_cookie("console")
        return csrf

    def create_api_key(self, name: str = "farm-key", retries: int = 3) -> dict[str, Any]:
        last_err: Exception | None = None
        for attempt in range(1, retries + 1):
            try:
                csrf = self._ensure_session()
            except Exception as e:
                last_err = e
                csrf = ""

            for base in (config.ADMIN_BASE, config.CONSOLE_BASE):
                host = (urlparse(base).hostname or "").split(".")[0]
                try:
                    r = self.s.post(
                        f"{base}/api/billing/api-keys",
                        json={"name": name},
                        headers={
                            "Content-Type": "application/json",
                            "Accept": "application/json",
                            "X-CSRFToken": self._csrf_cookie(host) or csrf or "",
                            "Referer": f"{base}/",
                        },
                        timeout=self.timeout,
                        allow_redirects=False,  # unauth redirects to Ory login JSON
                    )
                except (requests.RequestException, OSError) as e:
                    last_err = e
                    continue

                text = r.text or ""
                if _looks_unauthenticated(r.status_code, text):
                    last_err = RuntimeError(f"create_key unauthenticated {r.status_code}")
                    try:
                        self.login_browser_session(retries=2)
                        self._seed_console_cookies()
                    except Exception as e:
                        last_err = e
                    continue

                if r.status_code not in (200, 201):
                    last_err = RuntimeError(f"{r.status_code}: {text[:200]}")
                    continue
                try:
                    data = r.json()
                except Exception:
                    last_err = RuntimeError(f"create_key non-json {r.status_code}: {text[:120]}")
                    continue
                key = data.get("key") or data.get("api_key")
                if not key:
                    last_err = RuntimeError(f"create key missing secret keys={list(data.keys())[:8]}")
                    try:
                        self.login_browser_session(retries=2)
                    except Exception as e:
                        last_err = e
                    continue
                self.api_key = key
                self.key_id = data.get("key_id") or data.get("id")
                self.mark_proxy_ok()
                return data

            if attempt < retries:
                self.rotate_proxy(str(last_err)[:60] if last_err else "create_key")
                time.sleep(config.RETRY_429_BASE_S * attempt)
                try:
                    self.login_browser_session(retries=2)
                except Exception as e:
                    last_err = e
        raise RuntimeError(f"create api key failed: {last_err}")

    def smoke(self, api_key: str | None = None) -> int:
        key = api_key or self.api_key
        if not key:
            raise RuntimeError("smoke: no api_key")
        r = requests.get(
            f"{config.API_BASE}/v1/models",
            headers={"Authorization": f"Bearer {key}", "User-Agent": config.UA},
            timeout=self.timeout,
            proxies=self.s.proxies or None,
        )
        r.raise_for_status()
        return len((r.json()).get("data") or [])

    def farm_one(self) -> dict[str, Any]:
        """register → OTP → verify → cookie session → create key → optional smoke."""
        t0 = time.monotonic()
        stages: dict[str, float] = {}

        def timed(name: str, fn):
            t = time.monotonic()
            out = fn()
            stages[name] = time.monotonic() - t
            return out

        reg = timed("register", self.register)
        code = timed("otp", lambda: wait_otp(self.email))
        timed("verify", lambda: self.verify_email(code, flow_id=reg.get("verification_flow_id")))
        timed("login_cookie", self.login_browser_session)
        timed("create_key", lambda: self.create_api_key(name=f"farm-{int(time.time()) % 100000}"))

        models = 0
        if config.SMOKE_MODELS:
            models = timed("smoke", self.smoke)

        self.mark_proxy_ok()
        return {
            "email": self.email,
            "password": self.password,
            "api_key": self.api_key,
            "key_id": self.key_id,
            "models": models,
            "elapsed_s": round(time.monotonic() - t0, 2),
            "stages": {k: round(v, 2) for k, v in stages.items()},
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "source": "http_farm",
            "proxy": (self.proxy or "")[:48],
        }
