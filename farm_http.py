#!/usr/bin/env python3
"""CLI entry: python farm_http.py [max_accounts] [concurrency]"""
from __future__ import annotations

import sys

from http_farm.farm import main

if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
