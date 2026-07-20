#!/usr/bin/env bash
# Build a source tarball WITHOUT secrets / runtime data.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STAMP="$(date +%Y%m%d_%H%M%S)"
OUT_DIR="$ROOT/dist"
NAME="mistral-router-$STAMP"
STAGE="$OUT_DIR/$NAME"
mkdir -p "$STAGE"

rsync -a \
  --exclude '.env' \
  --exclude '.env.*' \
  --exclude '!.env.example' \
  --exclude 'data/' \
  --exclude 'results/' \
  --exclude 'results.json' \
  --exclude 'results.json.tmp' \
  --exclude 'bin/' \
  --exclude 'mistral-router' \
  --exclude '__pycache__/' \
  --exclude '.venv/' \
  --exclude 'venv/' \
  --exclude 'dist/' \
  --exclude 'share-out/' \
  --exclude 'config/proxies.txt' \
  --exclude 'config/proxies.ranked.txt' \
  --exclude '*.db' \
  --exclude '*.log' \
  --exclude '.git/' \
  "$ROOT/" "$STAGE/"

# ensure examples exist
cp -n "$ROOT/.env.example" "$STAGE/.env.example" 2>/dev/null || true
cp -n "$ROOT/config/proxies.example.txt" "$STAGE/config/proxies.example.txt" 2>/dev/null || true

# scrub any accidental secret files
rm -f "$STAGE/.env" "$STAGE/.env.farm" \
  "$STAGE/config/proxies.txt" "$STAGE/config/proxies.ranked.txt" \
  "$STAGE/results.json" 2>/dev/null || true
rm -rf "$STAGE/data" "$STAGE/results" "$STAGE/bin" 2>/dev/null || true

# double-check no private keys / long secrets in staged tree (heuristic)
if rg -n --hidden -g '!.git' -g '!*.example' \
  -e 'ory_st_[A-Za-z0-9]{10,}' \
  -e 'IMAP_PASS=.+' \
  -e 'http://[^:]+:[^@]+@' \
  "$STAGE" 2>/dev/null | head -20; then
  echo "WARN: possible secrets still in stage — review before sharing" >&2
fi

mkdir -p "$OUT_DIR"
TAR="$OUT_DIR/$NAME.tar.gz"
tar -C "$OUT_DIR" -czf "$TAR" "$NAME"
rm -rf "$STAGE"
echo "PACKAGE_TARBALL=$TAR"
ls -lh "$TAR"
