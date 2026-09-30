from __future__ import annotations

from collections.abc import Iterable

from src.core.config.app_config import AppConfig, BackendConfig
from src.core.domain.chat import ChatRequest
from src.core.interfaces.backend_config_provider_interface import IBackendConfigProvider
from src.core.services.backend_registry import backend_registry


def _backend_config_completeness_score(cfg: BackendConfig) -> tuple[int, ...]:
    """Rank BackendConfig richness for dash/underscore alias disambiguation.

    Higher tuples win. Prefer credentials first, then non-empty ``extra`` and
    other fields that differ from a bare discovery default.
    """
    defaults = BackendConfig()
    has_api_key = 1 if cfg.api_key else 0
    has_extra = 1 if cfg.extra else 0
    extra_size = len(cfg.extra) if cfg.extra else 0

    semantic_signals = 0
    if cfg.api_url is not None:
        semantic_signals += 1
    if cfg.models:
        semantic_signals += 1
    if cfg.credentials_path is not None:
        semantic_signals += 1
    if cfg.connector is not None:
        semantic_signals += 1
    if cfg.timeout != defaults.timeout:
        semantic_signals += 1
    if cfg.identity is not None:
        semantic_signals += 1
    if cfg.supported_input_types is not None:
        semantic_signals += 1
    if cfg.capability_descriptor is not None:
        semantic_signals += 1
    if cfg.allow_concurrent_use != defaults.allow_concurrent_use:
        semantic_signals += 1

    return (has_api_key, has_extra, extra_size, semantic_signals)


class BackendConfigProvider(IBackendConfigProvider):
    """Adapter that exposes AppConfig.backends as a canonical provider.

    This ensures consumers receive `BackendConfig` objects regardless of
    whether the original config was a dict or a BackendSettings instance.
    """

    def __init__(self, app_config: AppConfig) -> None:
        self._app_config = app_config

    def get_backend_config(self, name: str) -> BackendConfig | None:
        """Return the BackendConfig for the given backend name or None."""
        import logging

        logger = logging.getLogger(__name__)

        # If an instance name is provided (e.g. "openai.1"), fall back to the base
        # backend config when the instance is not explicitly configured.
        base_name: str | None = None
        if "." in name:
            base_name = name.split(".", 1)[0]
        name_for_fallback = base_name or name

        # Handle dash-to-underscore mapping for backend names
        attr_name = name.replace(
            "-", "_"
        )  # Convert dashes to underscores for attribute access
        dash_name = name.replace("_", "-")  # Convert underscores to dashes

        # List of all possible names to try
        possible_names = [name, attr_name, dash_name]
        if base_name:
            possible_names.extend(
                [
                    base_name,
                    base_name.replace("-", "_"),
                    base_name.replace("_", "-"),
                ]
            )
        # Remove duplicates while preserving order
        possible_names = list(dict.fromkeys(possible_names))

        found_configs: list[BackendConfig] = []

        lookup_method = getattr(self._app_config.backends, "lookup", None)
        for lookup_name in possible_names:
            cfg: BackendConfig | dict | None = None
            try:
                if callable(lookup_method):
                    cfg = lookup_method(lookup_name)  # type: ignore[assignment]
                else:
                    cfg = self._app_config.backends.get(lookup_name, None)  # type: ignore[arg-type]
            except (AttributeError, TypeError, KeyError):
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Failed to lookup backend config for name %s",
                        lookup_name,
                        exc_info=True,
                    )
                cfg = None

            if cfg is None:
                continue
            if isinstance(cfg, dict):
                found_configs.append(BackendConfig(**cfg))
            else:
                # BackendSettings.lookup returns BackendConfig; treat any non-dict as BackendConfig.
                found_configs.append(cfg)

        if found_configs:
            # Prefer the most complete alias match (e.g. filled YAML under the
            # underscore key over an empty discovery default under the hyphen).
            best = max(found_configs, key=_backend_config_completeness_score)
            resolved = BackendConfig(**best.model_dump())

            # Credential reuse fallbacks.
            # Some connectors are just protocol variants and should reuse the base backend's credentials
            # when not explicitly configured.
            if not resolved.api_key and name_for_fallback in {
                "openai-responses",
                "openai_responses",
            }:
                base = self.get_backend_config("openai")
                if base and base.api_key:
                    return BackendConfig(**base.model_dump())

            return resolved

        # No config found; return an empty BackendConfig for compatibility.
        # (Some call sites expect a BackendConfig object rather than None.)
        if name_for_fallback in {"openai-responses", "openai_responses"}:
            base = self.get_backend_config("openai")
            if base and base.api_key:
                return BackendConfig(**base.model_dump())

        return BackendConfig()

    def iter_backend_names(self) -> Iterable[str]:
        """Iterate over known backend names."""
        # Include both registered backends and any backends explicitly configured
        registered = set(backend_registry.get_registered_backends())

        backends_config = self._app_config.backends

        # Add any backends that are explicitly configured.
        try:
            for key in backends_config.get_named_backend_configs():
                if not key or key == "default_backend" or key.startswith("_"):
                    continue
                registered.add(key)
            for raw in backends_config:
                iter_key: str | None = None
                if isinstance(raw, str):
                    iter_key = raw
                elif isinstance(raw, tuple) and raw and isinstance(raw[0], str):
                    iter_key = raw[0]

                if (
                    not iter_key
                    or iter_key == "default_backend"
                    or iter_key.startswith("_")
                ):
                    continue
                registered.add(iter_key)
        except Exception as e:
            import logging

            logging.getLogger(__name__).debug(
                "iter_backend_names failed while inspecting backend configuration: %s",
                e,
                exc_info=True,
            )

        return registered

    def iter_configured_backend_names(self) -> Iterable[str]:
        """Return backend entries carrying explicit routing or connector settings."""

        configured: list[str] = []
        for name, cfg in self._app_config.backends.get_named_backend_configs().items():
            if not name or name == "default_backend" or name.startswith("_"):
                continue
            if not isinstance(cfg, BackendConfig):
                continue
            if (
                cfg.connector
                or cfg.models
                or cfg.api_key
                or cfg.api_url
                or cfg.credentials_path
                or cfg.extra
            ):
                configured.append(name)
        return configured

    def get_default_backend(self) -> str:
        """Return the configured default backend name."""
        try:
            default = self._app_config.backends.default_backend
            if default:
                return str(default)
        except Exception as e:
            import logging

            logging.getLogger(__name__).debug(
                "get_default_backend failed to access default_backend: %s",
                e,
                exc_info=True,
            )
        return "openai"

    def get_functional_backends(self) -> set[str]:
        """Return a set of backend names that are considered functional (e.g. have API keys)."""
        try:
            # If backends has a functional_backends property, use it
            if hasattr(self._app_config.backends, "functional_backends"):
                backends = self._app_config.backends.functional_backends
                if backends:  # Non-empty set
                    return backends
        except Exception as e:
            import logging

            logging.getLogger(__name__).debug(
                "get_functional_backends failed reading property: %s", e, exc_info=True
            )

        # Build a set of backends with api_key present
        result = set()

        # First check explicitly configured backend entries
        if hasattr(self._app_config.backends, "get_named_backend_configs"):
            for (
                name,
                value,
            ) in self._app_config.backends.get_named_backend_configs().items():
                # Skip non-backend attributes
                if (
                    name == "default_backend"
                    or name.startswith("_")
                    or not isinstance(value, BackendConfig)
                ):
                    continue

                if value.api_key:  # Non-empty api_key
                    result.add(name)

        # Then check backends via get_backend_config for any we missed
        for name in self.iter_backend_names():
            if name in result:
                continue  # Already processed

            cfg = self.get_backend_config(name)
            if cfg and cfg.api_key:
                result.add(name)

        return result

    def apply_backend_config(
        self, request: ChatRequest, backend_type: str, config: AppConfig
    ) -> ChatRequest:
        """Apply backend-specific configuration to a request.

        Args:
            request: The chat completion request
            backend_type: The backend type
            config: The application configuration

        Returns:
            The updated request with backend-specific configuration applied
        """
        # For now, return the request unchanged
        # This method can be extended to apply backend-specific configurations
        # similar to the BackendConfigService.apply_backend_configmodel method
        return request

    # Alias for backward compatibility with the interface
    def functional_backends(self) -> set[str]:
        """Alias for get_functional_backends for backward compatibility."""
        return self.get_functional_backends()
