"""OAuth connector detection utilities.

Provides multi-layered OAuth connector detection:
1. Naming patterns: -oauth- or -oauth suffix
2. Property check: has_static_credentials = False
3. Explicit known list: documented OAuth connectors

This module is used during connector auto-discovery to filter OAuth connectors
in Multi User Mode.
"""

from __future__ import annotations

from typing import Any

from src.core.common.backend_discovery_state import get_extracted_backend_names

# OAuth connector naming patterns (converted to underscore for module filenames)
OAUTH_CONNECTOR_PATTERNS: list[str] = [
    "_oauth_",  # Matches: gemini_oauth_auto, gemini_oauth_free, kiro_oauth_auto
    "_oauth",  # Matches: qwen_oauth, cursor_oauth, antigravity_oauth
]

# Keep extracted OAuth family names sourced from shared discovery state.
_EXTRACTED_OAUTH_CONNECTORS: set[str] = set(get_extracted_backend_names())

# Known OAuth connectors (explicit list for clarity and documentation).
# Module names use underscores, but logical connector names use dashes.
# Backends that don't follow the -oauth naming convention must be listed
# explicitly so they are treated as OAuth during Multi User Mode filtering.
KNOWN_OAUTH_CONNECTORS: set[str] = _EXTRACTED_OAUTH_CONNECTORS.union(
    {
        "gemini-cli-acp",  # Uses local gemini-cli personal auth via ACP
        "cursor-cli-acp",  # Uses Cursor CLI (agent acp) with cursor_login
        "agy-cli-acp",  # Uses local Antigravity CLI auth through ACP wrapper
        "eve-acp",  # Uses local Vercel Eve CLI via native ACP
        "workbuddy-acp",  # Uses local WorkBuddy AI CLI via native ACP
        "openai-codex",  # Uses OAuth via auth.json (special case)
        "openai-codex-v2",  # Managed OAuth stack shared with openai-codex
        "openai-codex-app-server",  # Local Codex CLI app-server (personal login, stdio)
        "opencode-zen",  # Extracted plugin; name doesn't follow -oauth convention
        "cline",  # Extracted plugin; uses OAuth-style token management
        "kiro-oauth-auto",  # Extracted OAuth backend; logical name uses dashes
    }
)


def is_oauth_connector(
    module_name: str | None, connector_class: type | None = None
) -> bool:
    """Detect if a connector is OAuth-based using multi-layered approach.

    Detection layers (in order of precedence):
    1. Check if module name is in KNOWN_OAUTH_CONNECTORS (explicit list)
    2. Check if module name matches OAUTH_CONNECTOR_PATTERNS (naming convention)
    3. Check connector_class.has_static_credentials property if available

    Args:
        module_name: The connector module name (e.g., "gemini_oauth_auto", "_openai_codex_connector")
        connector_class: Optional connector class to check has_static_credentials property

    Returns:
        True if connector is OAuth-based, False otherwise

    Examples:
        >>> is_oauth_connector("gemini_oauth_auto")
        True
        >>> is_oauth_connector("cursor_oauth")
        True
        >>> is_oauth_connector("openai")
        False
        >>> is_oauth_connector("_openai_codex_connector")
        True
    """
    if not module_name:
        return False

    # Normalize module name: convert underscores to dashes for matching
    # Module files use underscores (gemini_oauth_auto.py) but logical names use dashes
    normalized_name = module_name.replace("_", "-")

    # Remove leading underscore from private modules for matching
    if normalized_name.startswith("-"):
        normalized_name = normalized_name[1:]

    # Layer 1: Check explicit known OAuth connectors list
    if normalized_name in KNOWN_OAUTH_CONNECTORS:
        return True

    # Check if module name contains any known OAuth connector as substring
    # This handles cases like "_openai_codex_connector" matching "openai-codex"
    for known_oauth in KNOWN_OAUTH_CONNECTORS:
        if known_oauth in normalized_name:
            return True

    # Layer 2: Check naming patterns (module names use underscores)
    for pattern in OAUTH_CONNECTOR_PATTERNS:
        if pattern in module_name:
            return True

    # Layer 3: Check has_static_credentials property if connector class provided
    if connector_class is not None:
        try:
            # Check if class has the property
            if hasattr(connector_class, "has_static_credentials"):
                # Get the property value (handle both class and instance properties)
                prop_value = _get_property_value(
                    connector_class, "has_static_credentials"
                )
                # OAuth connectors have has_static_credentials = False
                if prop_value is False:
                    return True
        except Exception:
            # If property check fails, fall back to pattern/list detection only
            pass

    return False


def _get_property_value(cls: type, property_name: str) -> Any:
    """Get property value from class, handling both class and instance properties.

    Args:
        cls: The class to get property from
        property_name: Name of the property

    Returns:
        The property value if available, None otherwise
    """
    try:
        # Check if it's a property descriptor on the class
        for base in cls.__mro__:
            if property_name in base.__dict__:
                attr = base.__dict__[property_name]
                if isinstance(attr, property):
                    # It's a property descriptor, try to instantiate to get value
                    try:
                        instance = cls()
                        return getattr(instance, property_name)
                    except Exception:
                        # Can't instantiate, can't determine value
                        return None
                else:
                    # It's a class attribute, return directly
                    return attr

        # Try direct getattr as fallback (for simple class attributes)
        return getattr(cls, property_name, None)
    except Exception:
        return None
