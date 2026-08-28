"""OpenAI Codex connector module.

This module contains the refactored OpenAI Codex connector with separated
responsibilities and clear interfaces. The model catalog is auto-discovered at
startup (``codex debug models``) with a shipped fallback snapshot — see
:mod:`src.connectors.openai_codex.catalog`.
"""

from src.connectors._openai_codex_connector import (
    OPENAI_VENDOR_PREFIX,
    OpenAICodexConfiguredModelEnumerator,
    OpenAICodexConnector,
)
from src.connectors.openai_codex.catalog import (
    CodexModelCatalog,
    CodexModelCatalogConfig,
    CodexModelCatalogProvider,
    CodexModelReasoningProfile,
    ICodexModelCatalog,
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
