"""Auto-discovery module for built-in core backend connectors.

This module auto-imports in-repo core connector modules so they self-register
in ``backend_registry``.

OAuth connectors extracted to optional plugin distribution are intentionally
excluded here and are discovered via entry points (``llm_proxy_backends``).
"""

import logging

from src.core.common.backend_discovery_state import (
    get_skipped_oauth_connectors as _get_skipped_oauth_connectors,
)
from src.core.common.backend_discovery_state import (
    is_running_in_multi_user_mode as _is_running_in_multi_user_mode,
)

from .oauth_detector import is_oauth_connector

logger = logging.getLogger(__name__)

# Explicitly import base class first to ensure it's available
from .base import LLMBackend

__all__ = [
    "LLMBackend",
    "ensure_builtin_connectors_discovered",
    "reset_builtin_connector_discovery_state",
]

_discovery_complete = False


def _unregister_personal_auth_backends(
    registered_before: set[str],
    skipped_oauth_connectors: list[str],
    module_name: str,
) -> bool:
    """Unregister backends that declare requires_personal_auth after import.

    Used in Multi User Mode so personal OAuth connectors can be classified from
    capability metadata instead of a backend-name list.
    """
    from src.core.domain.backend_capability_descriptor import (
        source_requires_personal_auth,
    )
    from src.core.services.backend_registry import backend_registry

    newly_registered = (
        set(backend_registry.get_registered_backends()) - registered_before
    )
    blocked = False
    for backend_name in newly_registered:
        factory = backend_registry.get_backend_factory(backend_name)
        if not source_requires_personal_auth(factory):
            continue
        backend_registry.unregister_backend(backend_name)
        blocked = True
        if backend_name not in skipped_oauth_connectors:
            skipped_oauth_connectors.append(backend_name)
    if blocked and module_name not in skipped_oauth_connectors:
        skipped_oauth_connectors.append(module_name)
    return blocked


def reset_builtin_connector_discovery_state() -> None:
    """Reset built-in connector discovery idempotency for isolated test runs."""
    global _discovery_complete
    _discovery_complete = False


def ensure_builtin_connectors_discovered() -> None:
    """Import built-in connector modules once so they self-register."""
    global _discovery_complete
    if _discovery_complete:
        return

    import importlib
    import os
    import pkgutil
    from pathlib import Path

    from src.core.common.backend_discovery_state import (
        get_extracted_connector_module_names,
        replace_skipped_oauth_connectors,
        set_discovery_mode,
    )
    from src.core.services.backend_registry import backend_registry

    current_dir = Path(__file__).parent
    access_mode = os.environ.get("LLM_PROXY_ACCESS_MODE", "single_user")
    is_multi_user_mode = access_mode == "multi_user"
    skipped_oauth_connectors: list[str] = []
    loaded_oauth_connectors: list[str] = []
    extracted_oauth_modules = set(get_extracted_connector_module_names())

    for module_info in pkgutil.iter_modules([str(current_dir)]):
        module_name = module_info.name
        skip_modules = (
            "__init__",
            "base",
            "streaming_utils",
            "mixins",
            "utils",
            "oauth_detector",
        )
        if module_name in skip_modules or module_name.startswith("_"):
            continue

        if module_name in extracted_oauth_modules:
            if is_multi_user_mode and is_oauth_connector(module_name):
                skipped_oauth_connectors.append(module_name)
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Skipping extracted OAuth connector module from core discovery: %s",
                    module_name,
                )
            continue

        if is_multi_user_mode and is_oauth_connector(module_name):
            skipped_oauth_connectors.append(module_name)
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Skipping OAuth connector in Multi User Mode: %s", module_name
                )
            continue

        try:
            registered_before = set(backend_registry.get_registered_backends())
            importlib.import_module(f".{module_name}", package=__package__)
            if is_multi_user_mode and _unregister_personal_auth_backends(
                registered_before, skipped_oauth_connectors, module_name
            ):
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Skipping personal-auth connector in Multi User Mode: %s",
                        module_name,
                    )
                continue
            if not is_multi_user_mode and is_oauth_connector(module_name):
                loaded_oauth_connectors.append(module_name)
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Auto-discovered and imported backend module: %s", module_name
                )
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to import backend module %s: %s",
                    module_name,
                    e,
                    exc_info=True,
                )

    private_connector_modules = (
        "_openai_codex_connector",
        "_openai_codex_v2_connector",
    )
    if is_multi_user_mode:
        for priv_name in private_connector_modules:
            if priv_name not in skipped_oauth_connectors and is_oauth_connector(
                priv_name
            ):
                skipped_oauth_connectors.append(priv_name)
    else:
        for priv_name in private_connector_modules:
            try:
                importlib.import_module(f".{priv_name}", package=__package__)
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug("Imported private backend module: %s", priv_name)
            except Exception as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Failed to import private backend module %s: %s",
                        priv_name,
                        e,
                        exc_info=True,
                    )

    set_discovery_mode(is_multi_user_mode=is_multi_user_mode)
    replace_skipped_oauth_connectors(skipped_oauth_connectors)

    if is_multi_user_mode and skipped_oauth_connectors:
        logger.info(
            "Skipped %d OAuth connector(s) in Multi User Mode (OAuth not allowed in production): %s",
            len(skipped_oauth_connectors),
            ", ".join(sorted(skipped_oauth_connectors)),
        )
    elif loaded_oauth_connectors and logger.isEnabledFor(logging.DEBUG):
        logger.debug(
            "Loaded %d OAuth connector(s) in Single User Mode: %s",
            len(loaded_oauth_connectors),
            ", ".join(sorted(loaded_oauth_connectors)),
        )

    _discovery_complete = True


# Export skipped OAuth connectors for better error messages (Requirement 6.5)
def get_skipped_oauth_connectors() -> list[str]:
    """Get list of OAuth connectors skipped in Multi User Mode.

    Returns:
        List of connector names that were skipped due to OAuth filtering.
        Empty list in Single User Mode or if no connectors were skipped.
    """
    return _get_skipped_oauth_connectors()


def is_running_in_multi_user_mode() -> bool:
    """Check if connector loading happened in Multi User Mode.

    Returns:
        True if Multi User Mode, False if Single User Mode.
    """
    return _is_running_in_multi_user_mode()
