"""代理池 — 轮询 + 封禁 + 从文件加载。"""
from __future__ import annotations

import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import Iterable

import requests


def load_proxies(path: Path) -> list[str]:
    if not path.is_file():
        return []
    out: list[str] = []
    for line in path.read_text().splitlines():
        s = line.strip()
        if not s or s.startswith("#"):
            continue
        if "://" not in s:
            if "@" in s:
                s = "http://" + s
            else:
                parts = s.split(":")
                if len(parts) == 4:
                    host, port, user, pw = parts
                    s = f"http://{user}:{pw}@{host}:{port}"
                elif len(parts) == 2:
                    s = f"http://{s}"
                else:
                    continue
        out.append(s)
    return out


class ProxyPool:
    """Round-robin + ban. Reset bans if pool empties."""

    def __init__(self, proxies: Iterable[str], *, ban_after: int = 3):
        self._proxies = [p for p in proxies if p]
        self._ban_after = max(0, ban_after)
        self._fails: dict[str, int] = {}
        self._banned: set[str] = set()
        self._lock = threading.Lock()
        self._rr_i = 0

    def __len__(self) -> int:
        with self._lock:
            return sum(1 for p in self._proxies if p not in self._banned)

    def total(self) -> int:
        return len(self._proxies)

    def alive(self) -> list[str]:
        with self._lock:
            return [p for p in self._proxies if p not in self._banned]

    def next(self) -> str | None:
        with self._lock:
            alive = [p for p in self._proxies if p not in self._banned]
            if not alive:
                if not self._proxies:
                    return None
                self._banned.clear()
                self._fails.clear()
                alive = list(self._proxies)
            p = alive[self._rr_i % len(alive)]
            self._rr_i += 1
            return p

    def mark_ok(self, proxy: str | None) -> None:
        if not proxy:
            return
        with self._lock:
            self._fails[proxy] = 0
            self._banned.discard(proxy)

    def mark_fail(self, proxy: str | None) -> None:
        if not proxy or self._ban_after <= 0:
            return
        with self._lock:
            n = self._fails.get(proxy, 0) + 1
            self._fails[proxy] = n
            if n >= self._ban_after:
                self._banned.add(proxy)


def rank_proxies_quick(
    proxies: list[str],
    *,
    target: str = "https://auth.mistral.ai/self-service/registration/api",
    workers: int = 20,
    timeout: float = 8.0,
    limit: int = 0,
) -> list[tuple[str, float, int]]:
    """Return [(proxy, ms, status)] with 2xx/3xx first by latency."""
    sample = proxies[: limit or len(proxies)]
    results: list[tuple[str, float, int]] = []

    def one(p: str) -> tuple[str, float, int]:
        t0 = time.perf_counter()
        try:
            r = requests.get(
                target,
                proxies={"http": p, "https": p},
                timeout=timeout,
                headers={"User-Agent": "Mozilla/5.0", "Accept": "application/json"},
            )
            return p, (time.perf_counter() - t0) * 1000, int(r.status_code)
        except Exception:
            return p, (time.perf_counter() - t0) * 1000, 0

    with ThreadPoolExecutor(max_workers=workers) as ex:
        futs = [ex.submit(one, p) for p in sample]
        for f in as_completed(futs):
            results.append(f.result())

    good = sorted([r for r in results if 200 <= r[2] < 400], key=lambda x: x[1])
    bad = sorted([r for r in results if not (200 <= r[2] < 400)], key=lambda x: x[1])
    return good + bad


def write_ranked(path: Path, ranked: list[tuple[str, float, int]]) -> None:
    lines = [p for p, _, st in ranked if 200 <= st < 400]
    path.write_text("\n".join(lines) + ("\n" if lines else ""))
