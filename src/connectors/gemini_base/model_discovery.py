"""
Model discovery strategies for Gemini OAuth connectors.

This module provides different model discovery implementations:
- ApiModelDiscovery: Calls fetchAvailableModels API endpoint
- FallbackModelDiscovery: Returns hardcoded model list (for sandboxes)
"""

import logging
from typing import TYPE_CHECKING, Any

from src.connectors.gemini_base.config import DEFAULT_AVAILABLE_MODELS

if TYPE_CHECKING:
    import httpx

logger = logging.getLogger(__name__)


class ApiModelDiscovery:
    """Model discovery strategy that uses fetchAvailableModels API.

    Used by gemini-oauth-plan and gemini-oauth-free backends.
    """

    def __init__(
        self,
        fallback_models: list[str] | None = None,
    ) -> None:
        """Initialize the API model discovery strategy.

        Args:
            fallback_models: Models to use when API fails (default: DEFAULT_AVAILABLE_MODELS).
        """
        self._fallback_models = list(fallback_models or DEFAULT_AVAILABLE_MODELS)
        self._cached_models: list[str] | None = None

    def get_fallback_models(self) -> list[str]:
        """Get the fallback model list.

        Returns:
            List of fallback model names.
        """
        return self._fallback_models.copy()

    async def discover(
        self,
        client: "httpx.AsyncClient",
        headers: dict[str, str],
        base_url: str,
    ) -> list[str]:
        """Return hardcoded model list (API discovery is deprecated).

        The v1internal:fetchAvailableModels endpoint is no longer supported on
        standard Code Assist API endpoints. This method now returns the hardcoded
        fallback list to avoid 404 errors.

        Args:
            client: The HTTP client (unused).
            headers: HTTP headers (unused).
            base_url: The API base URL (unused).

        Returns:
            List of available model names.
        """
        if self._cached_models:
            return self._cached_models.copy()

        if logger.isEnabledFor(logging.INFO):
            logger.info(
                "Skipping fetchAvailableModels API (deprecated); using hardcoded model list."
            )

        self._cached_models = self.get_fallback_models()
        return self._cached_models.copy()

    def _extract_models_from_response(self, data: dict[str, Any]) -> list[str]:
        """Extract model names from the API response.

        Args:
            data: The JSON response from fetchAvailableModels.

        Returns:
            List of model names.
        """
        models: list[str] = []

        # Handle the format: {"models": {"model_name": {...}, ...}}
        models_dict = data.get("models", {})
        if isinstance(models_dict, dict):
            models.extend(models_dict.keys())

        # Handle the format: {"models": [{"name": "models/model_name"}, ...]}
        if isinstance(models_dict, list):
            for item in models_dict:
                if isinstance(item, dict):
                    name = item.get("name", "")
                    if name.startswith("models/"):
                        name = name[7:]  # Remove "models/" prefix
                    if name:
                        models.append(name)

        return models

    def clear_cache(self) -> None:
        """Clear the cached models list."""
        self._cached_models = None


class FallbackModelDiscovery:
    """Model discovery strategy that returns a hardcoded model list.

    Used by antigravity-oauth backend where the sandbox
    doesn't expose fetchAvailableModels endpoint.
    """

    # Extended model list for Antigravity (includes Claude and OpenAI models)
    # Public names use "vendor/model" format (e.g., "google/gemini-3-pro")
    # Internal mapping (handled by connector):
    # - google/gemini-3-pro -> gemini-3-pro-high/low based on reasoning_effort
    # - anthropic/claude-opus-4.5 -> claude-opus-4-5-thinking (always)
    # - anthropic/claude-sonnet-4.5 -> claude-sonnet-4-5 or claude-sonnet-4-5-thinking
    # - openai/gpt-oss-120b -> gpt-oss-120b-medium (always)
    ANTIGRAVITY_MODELS = [
        # Gemini models (google/ vendor prefix)
        "google/gemini-3-pro",  # Maps to gemini-3-pro-high/low internally
        "google/gemini-3-flash-preview",
        "google/gemini-2.5-pro",
        "google/gemini-2.5-flash",
        "google/gemini-2.5-flash-lite",
        "google/gemini-2.5-pro-preview-05-06",
        "google/gemini-2.5-pro-preview-06-05",
        "google/gemini-2.5-flash-preview-05-20",
        "google/gemini-2.0-flash",
        "google/gemini-2.0-flash-thinking-exp-1219",
        "google/gemini-1.5-pro",
        "google/gemini-1.5-flash",
        # Anthropic models (anthropic/ vendor prefix)
        "anthropic/claude-opus-4.5",  # Always maps to claude-opus-4-5-thinking
        "anthropic/claude-sonnet-4.5",  # Maps based on reasoning_effort
        # OpenAI models (openai/ vendor prefix)
        "openai/gpt-oss-120b",  # Always maps to gpt-oss-120b-medium
    ]

    def __init__(
        self,
        models: list[str] | None = None,
    ) -> None:
        """Initialize the fallback model discovery strategy.

        Args:
            models: Custom model list (default: ANTIGRAVITY_MODELS).
        """
        self._models = list(models or self.ANTIGRAVITY_MODELS)

    def get_fallback_models(self) -> list[str]:
        """Get the fallback model list.

        Returns:
            List of model names.
        """
        return self._models.copy()

    async def discover(
        self,
        client: "httpx.AsyncClient",
        headers: dict[str, str],
        base_url: str,
    ) -> list[str]:
        """Return the hardcoded model list without API calls.

        This method skips API discovery since the Antigravity sandbox
        doesn't expose fetchAvailableModels endpoint.

        Args:
            client: The HTTP client (unused).
            headers: HTTP headers (unused).
            base_url: The API base URL (unused).

        Returns:
            List of available model names.
        """
        logger.info(
            "Skipping fetchAvailableModels for sandbox endpoint; using fallback model list."
        )
        return self.get_fallback_models()


__all__ = [
    "ApiModelDiscovery",
    "FallbackModelDiscovery",
]
