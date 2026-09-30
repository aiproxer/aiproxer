"""First-party ChatGPT-plan connector package.

Importing this package registers backend type ``openai-chatgpt-plan``.
Registration is import-safe: it performs no browser flow, token refresh,
network I/O, or credential prompt.
"""

from .config import ChatGPTPlanConfig
from .connector import OpenAIChatGPTPlanConnector

__all__ = ["ChatGPTPlanConfig", "OpenAIChatGPTPlanConnector"]
