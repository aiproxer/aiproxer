"""
Model registry for Gemini OAuth connectors.

This module provides GeminiModelRegistry which handles model discovery, caching,
validation, and name mapping.
"""

import asyncio
import logging
from typing import TYPE_CHECKING

from src.connectors.base import add_vendor_prefix
from src.connectors.gemini_base.interfaces import (
    ICredentialCoordinator,
    IEndpointConfig,
    IModelDiscoveryStrategy,
    IModelRegistry,
)
from src.connectors.gemini_base.model_validation import GOOGLE_VENDOR_PREFIX
from src.core.common.exceptions import BackendError

if TYPE_CHECKING:
    import httpx

logger = logging.getLogger(__name__)


class GeminiModelRegistry(IModelRegistry):
    """Handles model discovery, caching, validation, and name mapping.

    This class maintains cached model lists for fast lookups and provides
    public-to-internal name translation.
    """

    # Global cache of successfully loaded model lists to prevent redundant discovery
    # across connector instances (e.g. during parallel request bursts).
    _global_loaded_models: dict[str, list[str]] = {}
    _load_lock: asyncio.Lock | None = None

    @classmethod
    def _get_load_lock(cls) -> asyncio.Lock:
        if cls._load_lock is None:
            cls._load_lock = asyncio.Lock()
        return cls._load_lock

    def __init__(
        self,
        model_discovery: IModelDiscoveryStrategy,
        endpoint_config: IEndpointConfig,
        credential_coordinator: ICredentialCoordinator,
        http_client: "httpx.AsyncClient",
        public_to_internal_map: dict[str, str] | None = None,
        backend_name: str = "gemini-oauth",
    ) -> None:
        """Initialize the model registry.

        Args:
            model_discovery: Strategy for discovering models via API.
            endpoint_config: Configuration for API endpoints and headers.
            credential_coordinator: Coordinator for credential access.
            http_client: HTTP client for API calls.
            public_to_internal_map: Optional mapping from public aliases to internal names.
            backend_name: Backend name for error messages (default: "gemini-oauth").
        """
        self._model_discovery = model_discovery
        self._endpoint_config = endpoint_config
        self._credential_coordinator = credential_coordinator
        self._http_client = http_client
        self._public_to_internal_map = public_to_internal_map or {}
        self._backend_name = backend_name

        # Cache state
        self._available_models: list[str] = []
        self._available_models_set: set[str] = set()
        self._models_from_api: bool = False
        self._loaded: bool = False

    async def ensure_loaded(self) -> None:
        """Load models if not already cached.

        This method performs lazy loading of models via API discovery or fallback list.
        It is safe to call multiple times; subsequent calls are no-ops if already loaded.
        """
        if self._loaded:
            return

        # Check global cache first
        if self._backend_name in self._global_loaded_models:
            models = self._global_loaded_models[self._backend_name]
            self._available_models = sorted(models)
            self._available_models_set = set(models)
            self._models_from_api = True
            self._loaded = True
            return

        async with self._get_load_lock():
            # Re-check after acquiring lock
            if self._loaded or self._backend_name in self._global_loaded_models:
                if self._backend_name in self._global_loaded_models:
                    models = self._global_loaded_models[self._backend_name]
                    self._available_models = sorted(models)
                    self._available_models_set = set(models)
                    self._models_from_api = True
                self._loaded = True
                return

            # Check if credentials are available
            credentials = self._credential_coordinator.credentials
            if not credentials or not credentials.access_token:
                logger.debug("No credentials available, using fallback model list")
                self._available_models = self._model_discovery.get_fallback_models()
                self._available_models_set = set(self._available_models)
                self._models_from_api = False
                self._loaded = True
                return

            # Try to load models from API
            try:
                base_url = self._endpoint_config.get_base_url()
                headers = self._endpoint_config.get_api_headers(credentials.to_dict())

                models = await self._model_discovery.discover(
                    self._http_client, headers, base_url
                )

                if models:
                    self._available_models = sorted(models)
                    self._available_models_set = set(models)
                    self._models_from_api = True
                    # Populate global cache
                    self._global_loaded_models[self._backend_name] = models
                    if logger.isEnabledFor(logging.INFO):
                        logger.info(
                            "Loaded %d models from API discovery",
                            len(self._available_models),
                        )
                else:
                    # API returned empty, use fallback
                    self._available_models = self._model_discovery.get_fallback_models()
                    self._available_models_set = set(self._available_models)
                    self._models_from_api = False
                    if logger.isEnabledFor(logging.INFO):
                        logger.info(
                            "API discovery returned no models, using %d fallback models",
                            len(self._available_models),
                        )
            except Exception as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Failed to load models from API: %s", e, exc_info=True
                    )
                # Fallback to hardcoded list
                self._available_models = self._model_discovery.get_fallback_models()
                self._available_models_set = set(self._available_models)
                self._models_from_api = False
                if logger.isEnabledFor(logging.INFO):
                    logger.info(
                        "Using %d fallback models due to API error",
                        len(self._available_models),
                    )

            self._loaded = True

    def validate(self, model_name: str) -> None:
        """Raise if the model is unavailable for this backend.

        Args:
            model_name: The model name to validate.

        Raises:
            BackendError: If the model is not available.
        """
        # Only validate if models were loaded from the API
        # Skip validation when using hardcoded fallback (may be outdated)
        if not self._models_from_api:
            logger.debug(
                "Model validation skipped - using hardcoded fallback model list"
            )
            return

        if not self._available_models_set:
            # Models not loaded yet or empty - skip validation
            logger.debug(
                "Model validation skipped - available models list not loaded yet"
            )
            return

        if model_name not in self._available_models_set:
            available_list = sorted(self._available_models_set)[
                :10
            ]  # Show first 10 models
            suffix = (
                f"... and {len(self._available_models_set) - 10} more"
                if len(self._available_models_set) > 10
                else ""
            )
            raise BackendError(
                message=f"Model '{model_name}' is not available on this backend. "
                f"Available models: {', '.join(available_list)}{suffix}",
                code="model_not_found",
                status_code=400,
                backend_name=self._backend_name,
                details={
                    "requested_model": model_name,
                    "available_count": len(self._available_models_set),
                },
            )

    def to_public_name(self, model_name: str) -> str:
        """Map internal names to public aliases when required.

        Args:
            model_name: Internal model name.

        Returns:
            Public alias or original name if no mapping exists.
        """
        # Create reverse mapping
        internal_to_public = {v: k for k, v in self._public_to_internal_map.items()}
        return internal_to_public.get(model_name, model_name)

    def to_internal_name(self, model_name: str) -> str:
        """Map public aliases to internal names when required.

        Args:
            model_name: Public model alias.

        Returns:
            Internal name or original name if no mapping exists.
        """
        return self._public_to_internal_map.get(model_name, model_name)

    def list_public_models(self) -> list[str]:
        """Return vendor-prefixed models for routing.

        Returns:
            List of public model names with vendor prefixes.
        """
        # Ensure models are loaded
        if not self._loaded:
            # This shouldn't happen in practice, but handle it gracefully
            logger.warning("list_public_models called before ensure_loaded")
            return []

        # Create reverse mapping for exposure
        internal_to_public = {v: k for k, v in self._public_to_internal_map.items()}

        models = []
        for m in self._available_models:
            # Map internal name to public alias if exists
            public_name = internal_to_public.get(m, m)
            models.append(add_vendor_prefix(public_name, GOOGLE_VENDOR_PREFIX))

        return models
