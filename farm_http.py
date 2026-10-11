#!/usr/bin/env python3
"""命令行入口: python farm_http.py [账号数量] [并发数]"""
from __future__ import annotations

import sys

from http_farm.farm import main

if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
