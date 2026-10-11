#!/usr/bin/env bash
# 本地初始化：venv、依赖、env 模板、代理模板。
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> 创建 Python venv"
if [[ ! -d .venv ]]; then
  python3 -m venv .venv
fi
# shellcheck disable=SC1091
source .venv/bin/activate
python -m pip install -U pip
pip install -r requirements.txt

if [[ ! -f .env ]]; then
  cp .env.example .env
  echo "==> 已从 .env.example 生成 .env — 请填写 IMAP_USER / IMAP_PASS / EMAIL_DOMAIN"
else
  echo "==> .env 已存在"
fi

if [[ ! -f config/proxies.ranked.txt ]] || ! grep -qE '^[^#[:space:]]' config/proxies.ranked.txt 2>/dev/null; then
  if [[ -f config/proxies.example.txt ]]; then
    cp config/proxies.example.txt config/proxies.ranked.txt
  fi
  echo "==> 请往 config/proxies.ranked.txt 里填代理（并发 > 3 时建议配置）"
fi

mkdir -p data results
python - <<'PY'
from http_farm import MistralHTTPClient, run_farm
print("import_ok:", MistralHTTPClient.__name__, run_farm.__name__)
PY

cat <<'EOF'

安装完成。
接下来：
  1) 编辑 .env（IMAP_USER / IMAP_PASS / EMAIL_DOMAIN）
  2) 编辑 config/proxies.ranked.txt
  3) make smoke
     # 或者: source .venv/bin/activate && python farm_http.py 2 2
  4) make farm N=20 C=8
EOF
