# Mistral 路由 / HTTP 注册机 辅助命令
#   make setup
#   make smoke
#   make farm N=20 C=8
#   make router

SHELL := /bin/bash
PY    := .venv/bin/python
PIP   := .venv/bin/pip

N ?= 2
C ?= 2

.PHONY: help setup venv deps env proxies smoke farm router build-router clean check test docker docker-up docker-down

help:
	@echo "可用目标:"
	@echo "  make setup          # 建 venv + 装依赖 + 生成 .env 和代理模板"
	@echo "  make smoke          # 注册 2 个账号，并发 2"
	@echo "  make farm N=20 C=8  # 注册 N 个账号，并发 C"
	@echo "  make check          # 检查依赖导入和环境变量"
	@echo "  make test           # 跑 Go 单元测试"
	@echo "  make build-router   # 编译 bin/mistral-router"
	@echo "  make router         # 在 :7623 启动路由器"
	@echo "  make docker         # 构建镜像"
	@echo "  make docker-up      # docker compose 起服务"
	@echo "  make docker-down    # 停掉服务"
	@echo "  make clean          # 清理 venv 和构建产物（保留 .env）"

setup: venv deps env proxies
	@$(PY) -c 'from http_farm import MistralHTTPClient; print("import_ok", MistralHTTPClient.__name__)'
	@echo
	@echo "安装完成。"
	@echo "1) 编辑 .env（IMAP_USER / IMAP_PASS / EMAIL_DOMAIN）"
	@echo "2) 编辑 config/proxies.ranked.txt"
	@echo "3) 运行 make smoke"

venv:
	@test -d .venv || python3 -m venv .venv
	@$(PIP) install -q -U pip

deps: venv
	@$(PIP) install -q -r requirements.txt

env:
	@if [[ ! -f .env ]]; then cp .env.example .env; echo "已从 .env.example 生成 .env"; else echo ".env 已存在"; fi

proxies:
	@mkdir -p config data results
	@if [[ ! -f config/proxies.ranked.txt ]] || ! grep -qE '^[^#[:space:]]' config/proxies.ranked.txt 2>/dev/null; then \
	  cp config/proxies.example.txt config/proxies.ranked.txt; \
	  echo "已生成 config/proxies.ranked.txt — 请填入你的代理"; \
	else \
	  echo "proxies.ranked.txt 已存在"; \
	fi

check: deps
	@$(PY) -c "from pathlib import Path; from http_farm import config; import sys; print('import_ok');\
print('取码通道:', config.OTP_PROVIDER);\
vals=({'MAILAPI_BASE':config.MAILAPI_BASE,'MAILAPI_KEY':config.MAILAPI_KEY,'EMAIL_DOMAIN':config.EMAIL_DOMAIN} if config.OTP_PROVIDER=='mailapi' else {'SELFHOST_BASE':config.SELFHOST_BASE,'SELFHOST_ADMIN':config.SELFHOST_ADMIN,'SELFHOST_PASSWORD':config.SELFHOST_PASSWORD,'EMAIL_DOMAIN':config.EMAIL_DOMAIN} if config.OTP_PROVIDER=='selfhost' else {'IMAP_USER':config.IMAP_USER,'IMAP_PASS':config.IMAP_PASS,'EMAIL_DOMAIN':config.EMAIL_DOMAIN});\
bad=[k for k,v in vals.items() if (not v) or v.startswith('your-') or v.startswith('sk_live_xxx') or v in ('you@your-domain.com','your-imap-password','your-domain.com','x@example.com','dummy','your-admin-password','admin@example.com')];\
(print('环境变量缺失或仍是占位符:', ', '.join(bad)) or print('请编辑 .env 后重新运行: make check') or sys.exit(1)) if bad else print('环境变量正常 domain=', config.EMAIL_DOMAIN);\
px=Path(config.PROXY_FILE); print('代理文件', px, '存在' if px.is_file() else '缺失')"

smoke: check
	@$(PY) farm_http.py 2 2

farm: check
	@$(PY) farm_http.py $(N) $(C)

build-router:
	@mkdir -p bin
	go build -o bin/mistral-router .
	@echo "已编译 bin/mistral-router"

router: build-router
	FARM_DIR="$(CURDIR)" DATA_DIR="$(CURDIR)/data" ./bin/mistral-router

test:
	@go test ./...
	@echo "单元测试通过"

docker:
	docker build -t mistral-router:latest .
	@echo "已构建 mistral-router:latest"

docker-up:
	docker compose up -d
	@echo "已启动；访问 http://127.0.0.1:7623/admin/"

docker-down:
	docker compose down

clean:
	rm -rf .venv bin __pycache__ http_farm/__pycache__
	@echo "已清理（.env 保留）"
