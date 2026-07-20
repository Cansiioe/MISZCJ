# Mistral Router / HTTP Farm helpers
#   make setup
#   make smoke
#   make farm N=20 C=8
#   make router

SHELL := /bin/bash
PY    := .venv/bin/python
PIP   := .venv/bin/pip

N ?= 2
C ?= 2

.PHONY: help setup venv deps env proxies smoke farm router build-router clean check

help:
	@echo "Targets:"
	@echo "  make setup          # venv + pip + .env + proxy template"
	@echo "  make smoke          # farm 2 accounts @ concurrency 2"
	@echo "  make farm N=20 C=8  # farm N accounts @ concurrency C"
	@echo "  make check          # import + env presence checks"
	@echo "  make build-router   # go build bin/mistral-router"
	@echo "  make router         # run router on :7623"
	@echo "  make clean          # remove venv / build artifacts (keeps .env)"

setup: venv deps env proxies
	@$(PY) -c 'from http_farm import MistralHTTPClient; print("import_ok", MistralHTTPClient.__name__)'
	@echo
	@echo "Setup done."
	@echo "1) Edit .env  (IMAP_USER / IMAP_PASS / EMAIL_DOMAIN)"
	@echo "2) Edit config/proxies.ranked.txt"
	@echo "3) make smoke"

venv:
	@test -d .venv || python3 -m venv .venv
	@$(PIP) install -q -U pip

deps: venv
	@$(PIP) install -q -r requirements.txt

env:
	@if [[ ! -f .env ]]; then cp .env.example .env; echo "Created .env from .env.example"; else echo ".env exists"; fi

proxies:
	@mkdir -p config data results
	@if [[ ! -f config/proxies.ranked.txt ]] || ! grep -qE '^[^#[:space:]]' config/proxies.ranked.txt 2>/dev/null; then \
	  cp config/proxies.example.txt config/proxies.ranked.txt; \
	  echo "Created config/proxies.ranked.txt — add your proxies"; \
	else \
	  echo "proxies.ranked.txt exists"; \
	fi

check: deps
	@$(PY) -c "from pathlib import Path; from http_farm import config; import sys; print('import_ok');\
vals={'IMAP_USER':config.IMAP_USER,'IMAP_PASS':config.IMAP_PASS,'EMAIL_DOMAIN':config.EMAIL_DOMAIN};\
bad=[k for k,v in vals.items() if (not v) or v.startswith('your-') or v in ('you@your-domain.com','your-imap-password','your-domain.com','x@example.com','dummy')];\
(print('MISSING_OR_PLACEHOLDER_ENV:', ', '.join(bad)) or print('Edit .env then re-run: make check') or sys.exit(1)) if bad else print('env_ok domain=', config.EMAIL_DOMAIN);\
px=Path(config.PROXY_FILE); print('proxy_file', px, 'exists' if px.is_file() else 'MISSING')"

smoke: check
	@$(PY) farm_http.py 2 2

farm: check
	@$(PY) farm_http.py $(N) $(C)

build-router:
	@mkdir -p bin
	go build -o bin/mistral-router .
	@echo "built bin/mistral-router"

router: build-router
	FARM_DIR="$(CURDIR)" DATA_DIR="$(CURDIR)/data" ./bin/mistral-router

clean:
	rm -rf .venv bin __pycache__ http_farm/__pycache__
	@echo "cleaned (.env kept)"
