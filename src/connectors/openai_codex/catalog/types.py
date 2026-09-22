"""Codex model catalog data types.

Pure data models for the Codex model catalog. Behavior (query/clamp logic) is
defined on ``CodexModelCatalog`` but intentionally left unimplemented in this
module during the TDD interface phase; implementations land in a later phase
after the tests are written.

The catalog is sourced at runtime from the authenticated Codex backend catalog
endpoint (raw JSON) and parsed by
:class:`src.connectors.openai_codex.catalog.parser.CodexCatalogParser`.
"""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass, field

# Reasoning effort levels assumed for models not present in the catalog.
# Unknown models do not support the extended ``xhigh``/``max``/``ultra`` tiers,
# matching the legacy behavior of downgrading ``xhigh`` to ``high`` for them.
_FALLBACK_REASONING_LEVELS: tuple[str, ...] = ("low", "medium", "high")


@dataclass(frozen=True, slots=True)
class CodexModelReasoningProfile:
    """Per-model reasoning profile sourced from the Codex CLI catalog.

    Attributes:
        slug: Model slug as accepted by the Codex backend.
        default_reasoning_level: Default reasoning effort for the model.
        supported_reasoning_levels: Reasoning efforts the model accepts,
            ordered from lowest to highest depth.
        visibility: Codex CLI picker visibility (``list`` or ``hide``).
        supported_in_api: Whether the Codex Responses API accepts the slug.
        context_window: Nominal context window in tokens.
        max_context_window: Maximum context window in tokens.
        legacy: True for slugs still accepted by the backend but absent from
            the current Codex CLI model picker.
        extra: Additional raw fields from the CLI catalog (display_name, etc.).
    """

    slug: str
    default_reasoning_level: str
    supported_reasoning_levels: tuple[str, ...]
    visibility: str = "list"
    supported_in_api: bool = True
    context_window: int | None = None
    max_context_window: int | None = None
    support_verbosity: bool = False
    default_verbosity: str | None = None
    legacy: bool = False
    extra: dict[str, object] = field(default_factory=dict)

    @property
    def api_accepted(self) -> bool:
        """True when the slug is selectable through the Codex Responses API."""
        return self.supported_in_api and self.visibility != "hide"

    def supports(self, effort: str) -> bool:
        return effort in self.supported_reasoning_levels


@dataclass(slots=True)
class CodexModelCatalog:
    """Runtime Codex model catalog with per-model reasoning-effort mappings.

    The catalog is built by the parser from the Codex backend catalog JSON (or
    the shipped fallback snapshot). Connectors query it through
    :class:`src.connectors.openai_codex.catalog.interfaces.ICodexModelCatalog`.

    Attributes:
        profiles: Mapping of lowercased slug -> profile, in catalog priority
            order (the parser preserves the CLI's model ordering).
        reasoning_effort_order: Reasoning efforts ordered from lowest to highest
            depth (derived from the widest model's ``supported_reasoning_levels``).
        default_reasoning_effort: Global default effort (constant ``"medium"``,
            not a model slug).
        reasoning_effort_descriptions: Verbatim effort descriptions from the CLI.
    """

    profiles: Mapping[str, CodexModelReasoningProfile]
    reasoning_effort_order: tuple[str, ...] = ()
    default_reasoning_effort: str = "medium"
    reasoning_effort_descriptions: Mapping[str, str] = field(default_factory=dict)

    def routable_slugs(self) -> tuple[str, ...]:
        """Return API-accepted slugs in catalog priority order."""
        return tuple(p.slug for p in self.profiles.values() if p.api_accepted)

    def is_supported(self, slug: str) -> bool:
        """True when ``slug`` is an API-accepted routable model."""
        profile = self.get_profile(slug)
        return profile is not None and profile.api_accepted

    def get_profile(self, slug: str) -> CodexModelReasoningProfile | None:
        """Return the profile for ``slug`` (case-insensitive) or None."""
        if not isinstance(slug, str):
            return None
        return self.profiles.get(slug.lower())

    def supported_reasoning_levels(self, slug: str) -> tuple[str, ...]:
        """Return reasoning efforts supported by ``slug``.

        Unknown models fall back to the baseline ``low/medium/high`` set so that
        extended tiers (``xhigh``/``max``/``ultra``) are downgraded for them,
        preserving the legacy ``xhigh`` -> ``high`` downgrade behavior.
        """
        profile = self.get_profile(slug)
        if profile is None:
            return _FALLBACK_REASONING_LEVELS
        return profile.supported_reasoning_levels

    def default_reasoning_level(self, slug: str) -> str:
        """Return the default reasoning effort for ``slug``.

        Unknown models fall back to the global default reasoning effort.
        """
        profile = self.get_profile(slug)
        if profile is None:
            return self.default_reasoning_effort
        return profile.default_reasoning_level

    def is_valid_effort(self, effort: str) -> bool:
        """True when ``effort`` is a known reasoning effort level."""
        if not isinstance(effort, str):
            return False
        return effort.lower().strip() in self.reasoning_effort_order

    def clamp_reasoning_effort(self, slug: str, effort: str) -> str:
        """Clamp ``effort`` to a level supported by ``slug``.

        If ``effort`` is supported by the model, it is returned unchanged.
        Otherwise the highest supported effort whose depth is at or below the
        requested effort is returned. If no supported effort qualifies (the
        requested effort is shallower than every supported level, or the effort
        is unknown), the model's default reasoning level is returned.
        """
        if not isinstance(effort, str):
            return self.default_reasoning_level(slug)
        requested = effort.lower().strip()
        levels = self.supported_reasoning_levels(slug)
        if requested in levels:
            return requested
        order = self.reasoning_effort_order
        try:
            requested_index = order.index(requested)
        except ValueError:
            return self.default_reasoning_level(slug)
        candidates = [
            lvl
            for lvl in levels
            if lvl in order and order.index(lvl) <= requested_index
        ]
        if candidates:
            return max(candidates, key=order.index)
        return self.default_reasoning_level(slug)

    def models_supporting(self, effort: str) -> tuple[str, ...]:
        """Return routable slugs whose profile supports ``effort``."""
        if not isinstance(effort, str):
            return ()
        wanted = effort.lower().strip()
        return tuple(
            p.slug
            for p in self.profiles.values()
            if p.api_accepted and p.supports(wanted)
        )

    def supports_verbosity(self, slug: str) -> bool:
        """True when ``slug`` advertises Responses ``text.verbosity`` support."""
        profile = self.get_profile(slug)
        if profile is None:
            return False
        return bool(profile.support_verbosity)

    def default_verbosity_for(self, slug: str) -> str | None:
        """Return the catalog default verbosity for ``slug``, if any."""
        profile = self.get_profile(slug)
        if profile is None:
            return None
        return profile.default_verbosity


def parse_client_version(value: object) -> tuple[int, ...] | None:
    """Parse a ``major.minor.patch`` client version into comparable ints.

    Returns ``None`` when the value is missing or unparsable so callers treat
    the entry as compatible rather than dropping it on unexpected input.
    """
    if not isinstance(value, str):
        return None
    text = value.strip()
    if not text:
        return None
    if text[:1].lower() == "v":
        text = text[1:].strip()
    parts: list[int] = []
    for chunk in text.split("."):
        chunk = chunk.strip()
        if not chunk:
            return None
        digits = ""
        for ch in chunk:
            if ch.isdigit():
                digits += ch
            else:
                break
        if not digits:
            return None
        try:
            parts.append(int(digits))
        except ValueError:
            return None
        # Stop at build metadata / pre-release suffixes (e.g. ``0.155.0-beta``).
        if len(digits) != len(chunk):
            break
    if not parts:
        return None
    return tuple(parts)


def _version_lte(left: tuple[int, ...], right: tuple[int, ...]) -> bool:
    width = max(len(left), len(right))
    padded_left = tuple(left) + (0,) * (width - len(left))
    padded_right = tuple(right) + (0,) * (width - len(right))
    return padded_left <= padded_right


def filter_catalog_by_client_version(
    catalog: CodexModelCatalog, client_version: object
) -> CodexModelCatalog:
    """Return a catalog with models requiring a newer client removed.

    Entries without a parsable ``minimal_client_version`` stay eligible. The
    effective client version is the outbound ``version`` header / catalog
    ``client_version`` query value, so a lower configured version must not
    advertise models the backend gates behind a newer client.
    """
    configured = parse_client_version(client_version)
    if configured is None:
        return catalog
    kept: dict[str, CodexModelReasoningProfile] = {}
    for key, profile in catalog.profiles.items():
        minimal_raw = profile.extra.get("minimal_client_version")
        minimal = parse_client_version(minimal_raw)
        if minimal is not None and not _version_lte(minimal, configured):
            continue
        kept[key] = profile
    if len(kept) == len(catalog.profiles):
        return catalog
    widest: tuple[str, ...] = ()
    for profile in kept.values():
        levels = profile.supported_reasoning_levels
        if len(levels) > len(widest):
            widest = levels
    return CodexModelCatalog(
        profiles=kept,
        reasoning_effort_order=widest,
        default_reasoning_effort=catalog.default_reasoning_effort,
        reasoning_effort_descriptions=dict(catalog.reasoning_effort_descriptions),
    )


__all__ = [
    "CodexModelCatalog",
    "CodexModelReasoningProfile",
    "filter_catalog_by_client_version",
    "parse_client_version",
]
