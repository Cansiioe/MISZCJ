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
  --exclude '._*' \
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

# 再清一次资源叉：rsync 遍历过程中 macOS 会为新读到的文件生成 ._ 文件，
# 单靠 --exclude '._*' 挡不住（它们在 rsync 跑到一半时才出现）
find "$STAGE" -name '._*' -delete 2>/dev/null || true

# 清掉任何误入的敏感文件
rm -f "$STAGE/.env" "$STAGE/.env.farm" \
  "$STAGE/config/proxies.txt" "$STAGE/config/proxies.ranked.txt" \
  "$STAGE/results.json" 2>/dev/null || true
rm -rf "$STAGE/data" "$STAGE/results" "$STAGE/bin" 2>/dev/null || true

# 二次检查：暂存目录里不应出现任何真实凭据
# 排除：*.example / *.md（本来就是写占位符的地方）、脚本自身（含规则文本）
if command -v rg >/dev/null 2>&1; then
  # 注意末尾的 || true：rg 没匹配到任何东西会返回 1，
  # 在 set -e 下会让整个赋值语句失败并中止脚本
  FOUND=$(rg -n --hidden \
    -g '!.git' -g '!*example*' -g '!*.md' -g '!go.sum' -g '!package.sh' \
    -e 'sk_live_[A-Za-z0-9_-]{8,}' \
    -e 'mstrl_[A-Za-z0-9_-]{8,}' \
    -e 'nm-[a-f0-9]{20,}' \
    -e 'ory_st_[A-Za-z0-9]{10,}' \
    -e '^MAILAPI_KEY=\S+' \
    -e '^SELFHOST_PASSWORD=\S+' \
    -e '^IMAP_PASS=\S+' \
    -e '^ADMIN_PASSWORD=\S+' \
    -e 'http://[a-zA-Z0-9_.-]+:[^@[:space:]]{3,}@[a-zA-Z0-9.-]+:[0-9]+' \
    -e '[a-zA-Z0-9-]+\.(xyz|top|icu|cn|cc|ml|ga|tk|shop|site)' \
    -e 'imap\.hostinger\.com' \
    -e '43\.133\.149\.136' \
    "$STAGE" 2>/dev/null | head -20 || true)
  if [ -n "$FOUND" ]; then
    echo "警告: 暂存目录里可能仍有敏感信息 — 分享前请先检查：" >&2
    echo "$FOUND" >&2
  else
    echo "凭据检查: 未发现真实密钥或私有域名"
  fi
else
  echo "提示: 未安装 ripgrep，跳过了凭据二次检查"
fi

mkdir -p "$OUT_DIR"
TAR="$OUT_DIR/$NAME.tar.gz"
tar -C "$OUT_DIR" -czf "$TAR" "$NAME"
rm -rf "$STAGE"
echo "PACKAGE_TARBALL=$TAR"
ls -lh "$TAR"
