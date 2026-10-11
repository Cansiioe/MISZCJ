"""Mistral HTTP 注册机 — 纯 HTTP 完成注册 / 验证 / 取密钥（无需浏览器）。"""

from .client import MistralHTTPClient
from .farm import main as run_farm

__version__ = "0.2.0"
__all__ = ["MistralHTTPClient", "run_farm", "__version__"]
