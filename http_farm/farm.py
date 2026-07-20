"""Mistral HTTP farm runner — concurrent pure-HTTP account creation.

Usage:
  set -a; . ./.env; set +a
  python farm_http.py 20 8
  python -m http_farm.farm 20 8
"""
from __future__ import annotations

import json
import os
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

from . import config
from .client import MistralHTTPClient
from .proxy_pool import ProxyPool, load_proxies

_results_lock = threading.Lock()
_print_lock = threading.Lock()
_stats = {"ok": 0, "fail": 0, "start": 0.0}
_pool: ProxyPool | None = None


def log(msg: str) -> None:
    with _print_lock:
        print(msg, flush=True)


def build_pool() -> ProxyPool | None:
    candidates: list[Path | str] = []
    if config.PROXY_LIST_FILE:
        candidates.append(config.PROXY_LIST_FILE)
    candidates.append(config.PROXY_FILE)
    candidates.append(config.PROXY_FILE.parent / "proxies.txt")

    proxies: list[str] = []
    used: Path | str | None = None
    for p in candidates:
        if not p:
            continue
        path = Path(p)
        proxies = load_proxies(path)
        if proxies:
            used = path
            break
    if not proxies and config.PROXY_URL:
        proxies = [config.PROXY_URL]
        used = "PROXY_URL"
    if not proxies:
        log("WARN: no proxies loaded — bare IP (429 risk under concurrency)")
        return None
    log(f"proxy pool: {len(proxies)} from {used} ban_after={config.PROXY_BAN_AFTER}")
    return ProxyPool(proxies, ban_after=config.PROXY_BAN_AFTER)


def append_result(entry: dict, results_file: str) -> int:
    with _results_lock:
        try:
            with open(results_file) as f:
                data = json.load(f)
                if not isinstance(data, list):
                    data = []
        except Exception:
            data = []
        data.append({
            "email": entry.get("email"),
            "api_key": entry.get("api_key"),
            "password": entry.get("password"),
            "key_id": entry.get("key_id"),
            "timestamp": entry.get("timestamp"),
            "source": "http_farm",
            "elapsed_s": entry.get("elapsed_s"),
            "stages": entry.get("stages"),
            "models": entry.get("models"),
            "proxy": entry.get("proxy"),
        })
        tmp = results_file + ".tmp"
        with open(tmp, "w") as f:
            json.dump(data, f, indent=2)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, results_file)
        return len(data)


def write_batch(batch_dir: Path, ok: list, failed: list) -> Path:
    batch_dir.mkdir(parents=True, exist_ok=True)
    d = batch_dir / time.strftime("batch_%Y%m%d_%H%M%S")
    d.mkdir(parents=True, exist_ok=True)
    (d / "accounts.json").write_text(json.dumps(ok, indent=2))
    (d / "failed.json").write_text(json.dumps(failed, indent=2))
    total = max(1, len(ok) + len(failed))
    (d / "summary.json").write_text(json.dumps({
        "ok": len(ok),
        "fail": len(failed),
        "success_rate": round(len(ok) / total, 4),
        "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "proxy_alive": len(_pool) if _pool else 0,
        "proxy_total": _pool.total() if _pool else 0,
    }, indent=2))
    return d


def run_one(idx: int, total: int) -> dict:
    tag = f"[{idx}/{total}]"
    t0 = time.monotonic()
    attempts = max(1, config.ACCOUNT_RETRIES)
    last_err: Exception | None = None

    for attempt in range(1, attempts + 1):
        try:
            result = MistralHTTPClient(pool=_pool).farm_one()
            with _results_lock:
                _stats["ok"] += 1
                ok, fail = _stats["ok"], _stats["fail"]
            rate = ok / max((time.monotonic() - _stats["start"]) / 60.0, 1e-6)
            extra = f" try={attempt}" if attempt > 1 else ""
            px = (result.get("proxy") or "")[-20:]
            log(
                f"{tag} OK{extra} {result['email']} key=…{result['api_key'][-4:]} "
                f"{result['elapsed_s']}s stages={result['stages']} "
                f"models={result.get('models')} px=…{px} | total ok={ok} fail={fail} rate={rate:.2f}/min"
            )
            result["_results_count"] = append_result(result, config.RESULTS_FILE)
            result["ok"] = True
            return result
        except Exception as e:
            last_err = e
            permanent = "validation" in str(e).lower() and "email" in str(e).lower()
            if permanent or attempt >= attempts:
                break
            log(f"{tag} retry {attempt}/{attempts} after {type(e).__name__}: {e}")
            time.sleep(0.5 * attempt)

    with _results_lock:
        _stats["fail"] += 1
        ok, fail = _stats["ok"], _stats["fail"]
    dt = round(time.monotonic() - t0, 2)
    log(f"{tag} FAIL {type(last_err).__name__}: {last_err} ({dt}s) | ok={ok} fail={fail}")
    return {
        "ok": False,
        "error": f"{type(last_err).__name__}: {last_err}",
        "elapsed_s": dt,
        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }


def main(argv: list[str] | None = None) -> int:
    global _pool
    config.require_imap()
    max_n = config.MAX_ACCOUNTS
    conc = config.CONCURRENCY
    if argv:
        if len(argv) >= 1:
            max_n = int(argv[0])
        if len(argv) >= 2:
            conc = max(1, min(int(argv[1]), 32))

    _pool = build_pool()
    log(f"mistral-http-farm start max={max_n} concurrency={conc} domain={config.EMAIL_DOMAIN}")
    log(f"results={config.RESULTS_FILE} batch_dir={config.BATCH_DIR}")
    _stats.update(ok=0, fail=0, start=time.monotonic())

    ok_list: list[dict] = []
    fail_list: list[dict] = []
    with ThreadPoolExecutor(max_workers=conc) as ex:
        futs = {}
        for i in range(1, max_n + 1):
            futs[ex.submit(run_one, i, max_n)] = i
            if conc > 1 and i < max_n:
                time.sleep(config.SPAWN_DELAY_S)
        for fut in as_completed(futs):
            res = fut.result()
            (ok_list if res.get("ok") else fail_list).append(res)

    elapsed = time.monotonic() - _stats["start"]
    batch = write_batch(Path(config.BATCH_DIR), ok_list, fail_list)
    rate = _stats["ok"] / max(elapsed / 60.0, 1e-6)
    sr = _stats["ok"] / max(1, _stats["ok"] + _stats["fail"])
    log(
        f"DONE ok={_stats['ok']} fail={_stats['fail']} "
        f"success_rate={sr:.1%} elapsed={elapsed:.1f}s rate={rate:.2f}/min "
        f"batch={batch} proxies_alive={len(_pool) if _pool else 0}"
    )
    return 0 if _stats["ok"] > 0 or max_n == 0 else 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
