"""Helpers for gpt-5.5 + ChatGPT-account Codex limitations (e.g. free plan downgrades)."""

from __future__ import annotations

import re
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any

# Observed upstream JSON: {"detail":"The 'gpt-5.5' model is not supported when using Codex with a ChatGPT account."}
_GPT55_REJECTION_PHRASE = (
    "The 'gpt-5.5' model is not supported when using Codex with a ChatGPT account."
)
# Tolerate provider changing quote style around the model id.
_GPT55_REJECTION_PATTERN = re.compile(
    r"The\s+['\"](?P<model>gpt-5\.5)['\"]\s+model\s+is\s+not\s+supported\s+"
    r"when\s+using\s+Codex\s+with\s+a\s+ChatGPT\s+account\.",
    re.IGNORECASE | re.DOTALL,
)


@dataclass(frozen=True, slots=True)
class Gpt55FreePlanDowngradeConfig:
    """Configuration for proactive/reactive gpt-5.5 downgrades on Codex.

    ``target_model`` is intentionally allowed to be a legacy-accepted slug
    that is absent from the advertised catalog: absence from the catalog
    proves only that the model is not advertised, not that the backend
    rejects it. Operators should verify the target against their account.
    """

    enabled: bool = True
    proactive_enabled: bool = True
    reactive_enabled: bool = True
    source_model: str = "gpt-5.5"
    target_model: str = "gpt-5.4"
    free_plan_types: frozenset[str] = frozenset({"free"})


DEFAULT_GPT55_DOWNGRADE = Gpt55FreePlanDowngradeConfig()


def gpt55_config_from_mapping(
    raw: Mapping[str, Any] | None,
) -> Gpt55FreePlanDowngradeConfig:
    """Build config from YAML / ``CodexConnectorSettings`` dict."""
    if not raw:
        return DEFAULT_GPT55_DOWNGRADE

    def _bool(key: str, default: bool) -> bool:
        v = raw.get(key)
        if v is None:
            return default
        if isinstance(v, bool):
            return v
        if isinstance(v, str):
            return v.strip().lower() in {"1", "true", "yes", "on"}
        return bool(v)

    free_raw = raw.get("free_plan_types")
    free_list: list[str]
    if isinstance(free_raw, list):
        free_list = [str(x).strip().lower() for x in free_raw if str(x).strip()]
    elif isinstance(free_raw, str) and free_raw.strip():
        free_list = [p.strip().lower() for p in free_raw.split(",") if p.strip()]
    else:
        free_list = list(DEFAULT_GPT55_DOWNGRADE.free_plan_types)

    sm = raw.get("source_model")
    tm = raw.get("target_model")
    source_model = (
        str(sm).strip()
        if isinstance(sm, str) and sm.strip()
        else DEFAULT_GPT55_DOWNGRADE.source_model
    )
    target_model = (
        str(tm).strip()
        if isinstance(tm, str) and tm.strip()
        else DEFAULT_GPT55_DOWNGRADE.target_model
    )

    return Gpt55FreePlanDowngradeConfig(
        enabled=_bool("enabled", DEFAULT_GPT55_DOWNGRADE.enabled),
        proactive_enabled=_bool(
            "proactive_enabled", DEFAULT_GPT55_DOWNGRADE.proactive_enabled
        ),
        reactive_enabled=_bool(
            "reactive_enabled", DEFAULT_GPT55_DOWNGRADE.reactive_enabled
        ),
        source_model=source_model,
        target_model=target_model,
        free_plan_types=frozenset(free_list) if free_list else frozenset({"free"}),
    )


def extract_codex_error_detail_string(detail: Any) -> str | None:
    """Return human-facing ``detail`` string from a FastAPI/JSON error body."""
    if isinstance(detail, str):
        return detail.strip() or None
    if not isinstance(detail, dict):
        return None
    raw = detail.get("detail")
    if isinstance(raw, str) and raw.strip():
        return raw
    # Wrapped instruction errors keep the upstream body under original_error
    orig = detail.get("original_error")
    if isinstance(orig, dict):
        inner = orig.get("detail")
        if isinstance(inner, str) and inner.strip():
            return inner
    return None


def is_upstream_gpt55_chatgpt_rejection(detail: Any) -> bool:
    """True when upstream rejects gpt-5.5 for ChatGPT-account Codex (HTTP 400)."""
    text = extract_codex_error_detail_string(detail)
    if not text:
        return False
    if _GPT55_REJECTION_PHRASE in text:
        return True
    return _GPT55_REJECTION_PATTERN.search(text) is not None


def plan_hint_is_free(
    plan_hint: object, free_plan_types: frozenset[str] | set[str]
) -> bool:
    if plan_hint is None or not isinstance(plan_hint, str):
        return False
    stripped = plan_hint.strip()
    if not stripped:
        return False
    return stripped.lower() in {f.lower() for f in free_plan_types}


def codex_plan_type_hint_from_account_payloads(
    last_codex_quota_headers: dict[str, str] | None,
    last_codex_usage_limit: dict[str, Any] | None,
) -> str | None:
    """Prefer x-codex-plan-type, then last usage_limit plan_type."""
    if isinstance(last_codex_quota_headers, dict) and last_codex_quota_headers:
        # Keys are lowercased at persistence time (see record_codex_quota_headers).
        for key in ("x-codex-plan-type", "X-Codex-Plan-Type"):
            val = last_codex_quota_headers.get(key)
            if isinstance(val, str) and val.strip():
                return val.strip()
        for k, v in last_codex_quota_headers.items():
            if (
                str(k).lower() == "x-codex-plan-type"
                and isinstance(v, str)
                and v.strip()
            ):
                return v.strip()
    if isinstance(last_codex_usage_limit, dict):
        pt = last_codex_usage_limit.get("plan_type")
        if isinstance(pt, str) and pt.strip():
            return pt.strip()
    return None


def should_downgrade_source_model(
    *,
    current_model: str,
    config: Gpt55FreePlanDowngradeConfig,
) -> bool:
    if not config.enabled or not config.proactive_enabled:
        return False
    if not current_model or not str(current_model).strip():
        return False
    return str(current_model).strip() == config.source_model


def maybe_reactive_gpt55_downgrade(
    *,
    current_model: str,
    config: Gpt55FreePlanDowngradeConfig,
    recovery_already_used: bool,
) -> str | None:
    """If reactive recovery should apply, return target model; else None."""
    if (
        not config.enabled
        or not config.reactive_enabled
        or recovery_already_used
        or not str(current_model).strip()
    ):
        return None
    if str(current_model).strip() != config.source_model:
        return None
    return str(config.target_model).strip() or None


def is_gpt55_downgrade_target_advertised(
    config: Gpt55FreePlanDowngradeConfig,
    is_supported: Any | None,
) -> bool | None:
    """Check whether the downgrade target is in the advertised catalog.

    Returns ``None`` when no catalog predicate is available. A ``False``
    result is only observability (the target may still be legacy-accepted),
    so callers keep attempting the configured target but log a warning.
    """
    if is_supported is None or not callable(is_supported):
        return None
    try:
        target = str(config.target_model).strip()
    except Exception:
        return None
    if not target:
        return False
    try:
        return bool(is_supported(target))
    except Exception:
        return None
