#!/usr/bin/env bash
# 打包源码压缩包，自动剔除密钥和运行时数据。
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

# 确保示例文件存在
cp -n "$ROOT/.env.example" "$STAGE/.env.example" 2>/dev/null || true
cp -n "$ROOT/config/proxies.example.txt" "$STAGE/config/proxies.example.txt" 2>/dev/null || true

# 清掉任何误入的敏感文件
rm -f "$STAGE/.env" "$STAGE/.env.farm" \
  "$STAGE/config/proxies.txt" "$STAGE/config/proxies.ranked.txt" \
  "$STAGE/results.json" 2>/dev/null || true
rm -rf "$STAGE/data" "$STAGE/results" "$STAGE/bin" 2>/dev/null || true

# 二次检查：暂存目录里不应出现私钥 / 长密钥 / 个人标识
if command -v rg >/dev/null 2>&1; then
  if rg -n --hidden -g '!.git' -g '!*.example' -g '!go.sum' \
    -e 'ory_st_[A-Za-z0-9]{10,}' \
    -e 'IMAP_PASS=.+' \
    -e 'http://[^:]+:[^@]+@' \
    -e '43\.133\.149\.136' \
    -e 'imap\.hostinger\.com' \
    -e 'pool-proxy-secret' \
    -e '\"123456\"' \
    "$STAGE" 2>/dev/null | head -20; then
    echo "警告: 暂存目录里可能仍有敏感信息 — 分享前请先检查" >&2
  fi
fi

mkdir -p "$OUT_DIR"
TAR="$OUT_DIR/$NAME.tar.gz"
tar -C "$OUT_DIR" -czf "$TAR" "$NAME"
rm -rf "$STAGE"
echo "PACKAGE_TARBALL=$TAR"
ls -lh "$TAR"
