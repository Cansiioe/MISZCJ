# Mistral Router + HTTP Farm

OpenAI-compatible **Mistral multi-key router** with a pure-HTTP account farm.

Default farm path is pure HTTP (no browser/Camoufox/xvfb).

## Features

- HTTP farm: Ory signup → IMAP OTP → API key mint
- Concurrent workers + residential proxy pool
- OpenAI-compatible reverse proxy (`/v1/*`)
- Admin UI for keys, farm control, and status

## Requirements

| Component | Need |
|-----------|------|
| OS | Linux / macOS / WSL2 |
| Python | **3.10+** |
| Network | HTTPS to `auth.mistral.ai`, `admin.mistral.ai`, `api.mistral.ai` |
| IMAP | catch-all inbox for verification codes |
| Proxies | recommended when concurrency > 3 |
| Go (optional) | **1.22+** to build the router |

Python deps:

```text
requests
python-dotenv
```

## Quick start

```bash
git clone https://<你的仓库地址>.git
cd mistral-router

make setup
# edit .env and config/proxies.ranked.txt
make smoke          # 2 accounts @ concurrency 2
make farm N=20 C=8  # larger batch
```

One-liner after clone:

```bash
make setup && ${EDITOR:-nano} .env config/proxies.ranked.txt && make smoke
```

Manual:

```bash
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt

cp .env.example .env
# set IMAP_HOST, IMAP_USER, IMAP_PASS, EMAIL_DOMAIN

cp config/proxies.example.txt config/proxies.ranked.txt
# add proxies, one per line:
#   http://user:pass@host:port

python farm_http.py 2 2
```

## Configuration

| File | Fields |
|------|--------|
| `.env` | `IMAP_USER`, `IMAP_PASS`, `EMAIL_DOMAIN` (required) |
| `config/proxies.ranked.txt` | proxy list (recommended for scale) |

Useful knobs:

```bash
CONCURRENCY=8
MAX_ATTEMPTS=20
SPAWN_DELAY_S=0.25
SKIP_SMOKE=1
OTP_TIMEOUT_S=100
LOGIN_RETRIES=4
```

## Outputs

- `results.json`
- `results/batch_YYYYMMDD_HHMMSS/{accounts,failed,summary}.json`

## Router (optional)

```bash
make router
# Admin UI: http://127.0.0.1:7623/admin/
```

Or:

```bash
go build -o bin/mistral-router .
FARM_DIR=$PWD DATA_DIR=$PWD/data ./bin/mistral-router
```

## Project layout

```text
.
  http_farm/           # HTTP farm library
  farm_http.py         # CLI entry
  Makefile             # setup / smoke / farm / router
  requirements.txt
  .env.example
  config/proxies.example.txt
  *.go web/            # router + admin UI
  data/ results/       # runtime (gitignored)
```

## Make targets

```bash
make setup
make check
make smoke
make farm N=20 C=8
make router
make clean
```

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| `required env missing: IMAP_*` | copy/edit `.env` |
| many `429` | add proxies, lower concurrency |
| `OTP timeout` | check catch-all + IMAP credentials |
| `create_key unauthenticated` | transient proxy/session issue; retries usually recover |
| Go build fails | install Go 1.22+ or use Python farm only |

## Notes

- OTP delivery is the main time bottleneck.
- High concurrency without proxies will hit auth rate limits.
- Use at your own risk; comply with Mistral ToS and local law.
