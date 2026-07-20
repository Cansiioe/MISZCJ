#!/usr/bin/env bash
# Local setup: venv, deps, env template, proxy template.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> Python venv"
if [[ ! -d .venv ]]; then
  python3 -m venv .venv
fi
# shellcheck disable=SC1091
source .venv/bin/activate
python -m pip install -U pip
pip install -r requirements.txt

if [[ ! -f .env ]]; then
  cp .env.example .env
  echo "==> Created .env from .env.example — set IMAP_USER / IMAP_PASS / EMAIL_DOMAIN"
else
  echo "==> .env already exists"
fi

if [[ ! -f config/proxies.ranked.txt ]] || ! grep -qE '^[^#[:space:]]' config/proxies.ranked.txt 2>/dev/null; then
  if [[ -f config/proxies.example.txt ]]; then
    cp config/proxies.example.txt config/proxies.ranked.txt
  fi
  echo "==> Add proxies to config/proxies.ranked.txt (recommended for concurrency > 3)"
fi

mkdir -p data results
python - <<'PY'
from http_farm import MistralHTTPClient, run_farm
print("import_ok:", MistralHTTPClient.__name__, run_farm.__name__)
PY

cat <<'EOF'

Setup done.
Next:
  1) Edit .env  (IMAP_USER / IMAP_PASS / EMAIL_DOMAIN)
  2) Edit config/proxies.ranked.txt
  3) make smoke
     # or: source .venv/bin/activate && python farm_http.py 2 2
  4) make farm N=20 C=8
EOF
