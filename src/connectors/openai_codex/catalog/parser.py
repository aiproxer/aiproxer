"""Catalog parser.

Parses raw Codex backend catalog JSON (a dict with a ``models`` list) into a
:class:`CodexModelCatalog`. Each model entry is mapped to a
:class:`CodexModelReasoningProfile`; entries missing required fields or with no
usable reasoning levels are skipped. The global reasoning-effort order is
derived from the widest model's ``supported_reasoning_levels`` (the per-model
list is depth-ordered in the backend response), so no effort hierarchy is
hardcoded.
"""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any

from src.connectors.openai_codex.catalog.types import (
    CodexModelCatalog,
    CodexModelReasoningProfile,
)

# Model entry keys consumed by the parser; everything else goes into ``extra``.
_KNOWN_KEYS = frozenset(
    {
        "slug",
        "default_reasoning_level",
        "supported_reasoning_levels",
        "visibility",
        "supported_in_api",
        "context_window",
        "max_context_window",
        "support_verbosity",
        "default_verbosity",
    }
)

# Known-huge raw keys that are never consumed downstream and must not be
# retained in ``extra``. ``model_messages`` (incl. ``persistent_instructions``)
# runs to tens of KB per model and ``base_instructions`` to ~20KB per model;
# keeping them would pin megabytes of dead payload on every parsed catalog.
_EXCLUDED_FROM_EXTRA = frozenset({"model_messages", "base_instructions"})


class CodexCatalogParser:
    """Parse the raw Codex backend catalog payload into a catalog."""

    def parse(self, raw: Mapping[str, Any]) -> CodexModelCatalog:
        models_raw = raw.get("models") if isinstance(raw, Mapping) else None
        if not isinstance(models_raw, list):
            models_raw = []

        profiles: dict[str, CodexModelReasoningProfile] = {}
        descriptions: dict[str, str] = {}
        widest_levels: tuple[str, ...] = ()

        for entry in models_raw:
            profile, levels = self._parse_entry(entry, descriptions)
            if profile is None:
                continue
            profiles[profile.slug.lower()] = profile
            if len(levels) > len(widest_levels):
                widest_levels = levels

        return CodexModelCatalog(
            profiles=profiles,
            reasoning_effort_order=widest_levels,
            default_reasoning_effort="medium",
            reasoning_effort_descriptions=descriptions,
        )

    @staticmethod
    def _parse_entry(
        entry: Any, descriptions: dict[str, str]
    ) -> tuple[CodexModelReasoningProfile | None, tuple[str, ...]]:
        if not isinstance(entry, Mapping):
            return None, ()

        slug = entry.get("slug")
        if not isinstance(slug, str) or not slug.strip():
            return None, ()
        slug = slug.strip()

        levels = CodexCatalogParser._parse_levels(
            entry.get("supported_reasoning_levels"), descriptions
        )
        if not levels:
            # Entries without usable reasoning levels are not actionable.
            return None, ()

        default_level = entry.get("default_reasoning_level")
        if not isinstance(default_level, str) or not default_level.strip():
            default_level = "medium"
        else:
            default_level = default_level.strip()

        visibility = entry.get("visibility")
        if not isinstance(visibility, str) or not visibility.strip():
            visibility = "list"
        else:
            visibility = visibility.strip()

        supported_in_api = entry.get("supported_in_api")
        if not isinstance(supported_in_api, bool):
            supported_in_api = True

        context_window = entry.get("context_window")
        if not isinstance(context_window, int):
            context_window = None

        max_context_window = entry.get("max_context_window")
        if not isinstance(max_context_window, int):
            max_context_window = None

        support_verbosity = entry.get("support_verbosity")
        if not isinstance(support_verbosity, bool):
            support_verbosity = False

        default_verbosity_raw = entry.get("default_verbosity")
        default_verbosity: str | None
        if isinstance(default_verbosity_raw, str) and default_verbosity_raw.strip():
            default_verbosity = default_verbosity_raw.strip()
        else:
            default_verbosity = None

        extra = {
            str(k): v
            for k, v in entry.items()
            if str(k) not in _KNOWN_KEYS and str(k) not in _EXCLUDED_FROM_EXTRA
        }

        profile = CodexModelReasoningProfile(
            slug=slug,
            default_reasoning_level=default_level,
            supported_reasoning_levels=levels,
            visibility=visibility,
            supported_in_api=supported_in_api,
            context_window=context_window,
            max_context_window=max_context_window,
            support_verbosity=support_verbosity,
            default_verbosity=default_verbosity,
            extra=extra,
        )
        return profile, levels

    @staticmethod
    def _parse_levels(raw_levels: Any, descriptions: dict[str, str]) -> tuple[str, ...]:
        if not isinstance(raw_levels, list):
            return ()
        levels: list[str] = []
        for item in raw_levels:
            if not isinstance(item, Mapping):
                continue
            effort = item.get("effort")
            if not isinstance(effort, str) or not effort.strip():
                continue
            effort = effort.strip()
            levels.append(effort)
            description = item.get("description")
            if (
                isinstance(description, str)
                and description.strip()
                and effort not in descriptions
            ):
                descriptions[effort] = description.strip()
        return tuple(levels)


__all__ = ["CodexCatalogParser"]
