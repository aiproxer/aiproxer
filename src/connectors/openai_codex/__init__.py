"""OpenAI Codex connector module.

This module contains the refactored OpenAI Codex connector with separated
responsibilities and clear interfaces. The model catalog is auto-discovered at
startup (``codex debug models``) with a shipped fallback snapshot — see
:mod:`src.connectors.openai_codex.catalog`.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from src.connectors.openai_codex.catalog import (
    CodexModelCatalog,
    CodexModelCatalogConfig,
    CodexModelCatalogProvider,
    CodexModelReasoningProfile,
    ICodexModelCatalog,
)

if TYPE_CHECKING:
    from src.connectors._openai_codex_connector import (
        OPENAI_VENDOR_PREFIX as OPENAI_VENDOR_PREFIX,
    )
    from src.connectors._openai_codex_connector import (
        OpenAICodexConfiguredModelEnumerator as OpenAICodexConfiguredModelEnumerator,
    )
    from src.connectors._openai_codex_connector import (
        OpenAICodexConnector as OpenAICodexConnector,
    )

__all__ = [
    "OPENAI_VENDOR_PREFIX",
    "OpenAICodexConnector",
    "OpenAICodexConfiguredModelEnumerator",
    "CodexModelCatalog",
    "CodexModelCatalogConfig",
    "CodexModelCatalogProvider",
    "CodexModelReasoningProfile",
    "ICodexModelCatalog",
]

# Also export OpenAICredentialsFileHandler from credentials module
try:
    from .credentials import (
        OpenAICredentialsFileHandler as OpenAICredentialsFileHandler,
    )

    __all__.append("OpenAICredentialsFileHandler")
except ImportError:
    pass


def __getattr__(name: str) -> Any:
    if name in (
        "OPENAI_VENDOR_PREFIX",
        "OpenAICodexConnector",
        "OpenAICodexConfiguredModelEnumerator",
    ):
        from src.connectors import _openai_codex_connector as _mod

        return getattr(_mod, name)
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
