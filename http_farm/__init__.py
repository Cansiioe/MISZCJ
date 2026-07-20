"""Mistral HTTP farm — pure HTTP signup / verify / API key (no browser)."""

from .client import MistralHTTPClient
from .farm import main as run_farm

__version__ = "0.2.0"
__all__ = ["MistralHTTPClient", "run_farm", "__version__"]
