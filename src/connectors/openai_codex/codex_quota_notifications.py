"""Desktop notifications for OpenAI Codex managed-OAuth quota / rate limits."""

from __future__ import annotations

import logging
import math
import time
from collections.abc import Mapping
from typing import TYPE_CHECKING, Any

from src.connectors.openai_codex.codex_rate_limit_logging import (
    classify_usage_limit_window,
    compute_codex_quota_until_iso_utc,
)

if TYPE_CHECKING:
    from src.core.interfaces.notification_service_interface import (
        INotificationService,
    )

logger = logging.getLogger(__name__)

_CODEX_QUOTA_NOTIFICATION_TITLE = "OpenAI Codex: Quota reached"
_CODEX_QUOTA_LOW_REMAINING_TITLE = "OpenAI Codex: Low quota remaining"

# Dedupe key component when every managed account is simultaneously unavailable:
# one desktop alert per quota window, not one per account that hits 429 last.
_CODEX_QUOTA_ALL_ACCOUNTS_DEDUPE_ID = "__all_managed_accounts__"

_EXTENDED_WINDOW = "extended (~weekly_or_plan_quota)"


def _format_account_label(
    email: str | None,
    *,
    managed_account_id: str | None,
    chatgpt_account_id: str | None,
) -> str:
    """Best-effort human-readable account reference for desktop notifications."""
    if isinstance(email, str) and email.strip():
        return email.strip()
    if isinstance(chatgpt_account_id, str) and chatgpt_account_id.strip():
        return chatgpt_account_id.strip()
    if isinstance(managed_account_id, str) and managed_account_id.strip():
        return managed_account_id.strip()
    return "(unknown)"


def user_facing_quota_type(resets_in_seconds: float | None) -> str:
    """Map heuristic window to the two primary Codex limit labels (plus unknown)."""
    window = classify_usage_limit_window(resets_in_seconds)
    if window == "unknown":
        return "unknown"
    if window == _EXTENDED_WINDOW:
        return "weekly limit"
    return "sliding 5h window"


def build_codex_quota_notification_message(
    *,
    email: str | None,
    managed_account_id: str | None = None,
    chatgpt_account_id: str | None = None,
    quota_type: str,
    until_display: str,
    pool_exhaustion_confirmed: bool,
) -> str:
    """Body text for a Codex quota desktop notification."""
    account = _format_account_label(
        email,
        managed_account_id=managed_account_id,
        chatgpt_account_id=chatgpt_account_id,
    )
    msg = (
        f"Codex quota reached. Account: {account}, type: {quota_type}, "
        f"until: {until_display}"
    )
    if pool_exhaustion_confirmed:
        msg += "\n\nQuotas exhausted on all available accounts"
    return msg


def _effective_resets_in_seconds(
    usage_limit_fields: Mapping[str, Any] | None,
    retry_after_seconds: float | None,
) -> float | None:
    if usage_limit_fields:
        raw = usage_limit_fields.get("resets_in_seconds")
        if isinstance(raw, int | float) and float(raw) > 0:
            return float(raw)
        rat_unix = usage_limit_fields.get("resets_at_unix")
        if isinstance(rat_unix, int) and rat_unix > 1_000_000_000:
            derived = float(rat_unix) - time.time()
            if derived > 0:
                return derived
    if retry_after_seconds is not None and retry_after_seconds > 0:
        return float(retry_after_seconds)
    return None


def _resets_at_unix_from_fields(
    usage_limit_fields: Mapping[str, Any] | None,
) -> int | None:
    if not usage_limit_fields:
        return None
    rat = usage_limit_fields.get("resets_at_unix")
    return rat if isinstance(rat, int) else None


async def maybe_notify_codex_quota_reached(
    notification_service: INotificationService | None,
    dedupe_keys: set[tuple[str, str, str]],
    *,
    managed_account_id: str,
    email: str | None,
    chatgpt_account_id: str | None = None,
    usage_limit_fields: dict[str, Any] | None,
    retry_after_seconds: float | None,
    pool_exhaustion_confirmed: bool,
) -> None:
    """Send at most one desktop notification per dedupe key while service is enabled.

    Args:
        pool_exhaustion_confirmed: If True, uses global dedupe key and adds "all accounts exhausted"
            message. If False, uses per-account dedupe for notifications.
    """
    if notification_service is None or not notification_service.is_enabled:
        return

    resets_in = _effective_resets_in_seconds(usage_limit_fields, retry_after_seconds)
    quota_type = user_facing_quota_type(resets_in)
    until_iso = compute_codex_quota_until_iso_utc(
        resets_at_unix=_resets_at_unix_from_fields(usage_limit_fields),
        retry_after_seconds=retry_after_seconds,
    )
    until_display = until_iso if until_iso else "unknown"
    dedupe_until = until_iso if until_iso else "none"

    dedupe_account_id = (
        _CODEX_QUOTA_ALL_ACCOUNTS_DEDUPE_ID
        if pool_exhaustion_confirmed
        else managed_account_id
    )
    key = (dedupe_account_id, quota_type, dedupe_until)
    if key in dedupe_keys:
        return

    message = build_codex_quota_notification_message(
        email=email,
        managed_account_id=managed_account_id,
        chatgpt_account_id=chatgpt_account_id,
        quota_type=quota_type,
        until_display=until_display,
        pool_exhaustion_confirmed=pool_exhaustion_confirmed,
    )
    try:
        await notification_service.send_notification(
            title=_CODEX_QUOTA_NOTIFICATION_TITLE,
            message=message,
        )
    except Exception as exc:
        logger.warning("Codex quota notification failed: %s", exc, exc_info=True)
        return

    dedupe_keys.add(key)


def codex_limit_kind_display(limit_kind: str) -> str:
    """Human-readable Codex rolling window label for remaining-quota alerts."""
    if limit_kind == "secondary":
        return "weekly rolling window"
    return "5 hour rolling window"


def parse_codex_used_percent_from_headers(
    headers: Mapping[str, Any],
    *,
    header_key: str,
) -> float | None:
    """Return used-percent (0-100) from headers, or None if missing/invalid."""
    target = header_key.lower()
    for raw_k, raw_v in headers.items():
        if str(raw_k).lower() != target:
            continue
        try:
            used = float(str(raw_v).strip())
        except (TypeError, ValueError):
            return None
        if not math.isfinite(used):
            return None
        return used
    return None


def codex_remaining_percent_from_used(used_percent: float) -> float:
    """Remaining quota percent from upstream ``*-used-percent`` (clamped)."""
    remaining = 100.0 - float(used_percent)
    if remaining < 0.0:
        return 0.0
    if remaining > 100.0:
        return 100.0
    return remaining


def collect_codex_remaining_pairs(
    headers: Mapping[str, Any],
) -> list[tuple[str, float]]:
    """Build (limit_kind, remaining_percent) pairs from Codex quota headers."""
    pairs: list[tuple[str, float]] = []
    used_primary = parse_codex_used_percent_from_headers(
        headers,
        header_key="x-codex-primary-used-percent",
    )
    if used_primary is not None:
        pairs.append(
            ("primary", codex_remaining_percent_from_used(used_primary)),
        )
    used_secondary = parse_codex_used_percent_from_headers(
        headers,
        header_key="x-codex-secondary-used-percent",
    )
    if used_secondary is not None:
        pairs.append(
            ("secondary", codex_remaining_percent_from_used(used_secondary)),
        )
    return pairs


def build_codex_low_remaining_notification_message(
    *,
    email: str | None,
    managed_account_id: str | None = None,
    chatgpt_account_id: str | None = None,
    limit_kind: str,
    threshold_percent: float,
    remaining_percent: float,
) -> str:
    """Body text for a Codex low remaining-quota desktop notification."""
    account = _format_account_label(
        email,
        managed_account_id=managed_account_id,
        chatgpt_account_id=chatgpt_account_id,
    )
    window = codex_limit_kind_display(limit_kind)
    rem_rounded = round(float(remaining_percent), 1)
    thr = float(threshold_percent)
    thr_display = int(thr) if thr == int(thr) else thr
    return (
        f"Codex quota running low. Account: {account}, limit: {window}. "
        f"Remaining is below your {thr_display}% threshold "
        f"(about {rem_rounded}% of this window still available)."
    )


def _normalize_threshold_latch_key(threshold_percent: float) -> float:
    return round(float(threshold_percent), 6)


_CODEX_TOKEN_INVALIDATED_TITLE = "OpenAI Codex: Token invalidated"


async def maybe_notify_codex_token_invalidated(
    notification_service: INotificationService | None,
    dedupe_keys: set[str],
    *,
    managed_account_id: str,
    email: str | None,
) -> None:
    """Send at most one desktop notification per account when its token is invalidated.

    Args:
        notification_service: Desktop notification service.
        dedupe_keys: Mutable set of already-notified account keys.
        managed_account_id: The account whose token was rejected.
        email: Optional email for human-readable labeling.
    """
    if notification_service is None or not notification_service.is_enabled:
        return

    account = _format_account_label(
        email,
        managed_account_id=managed_account_id,
        chatgpt_account_id=None,
    )
    key = f"{managed_account_id}:{email or ''}"
    if key in dedupe_keys:
        return

    message = (
        f"Codex access token was invalidated for account: {account}. "
        "Re-authentication required."
    )
    try:
        await notification_service.send_notification(
            title=_CODEX_TOKEN_INVALIDATED_TITLE,
            message=message,
        )
    except Exception as exc:
        logger.warning(
            "Codex token invalidated notification failed: %s", exc, exc_info=True
        )
        return

    dedupe_keys.add(key)


async def maybe_notify_codex_quota_remaining_low(
    notification_service: INotificationService | None,
    latch_keys: set[tuple[str, str, float]],
    *,
    managed_account_id: str,
    email: str | None,
    chatgpt_account_id: str | None = None,
    threshold_percents: list[float],
    remaining_by_limit: list[tuple[str, float]],
) -> None:
    """Notify when remaining quota drops strictly below configured thresholds.

    ``latch_keys`` uses tuples ``(managed_account_id, limit_kind, threshold)``.
    A key is cleared when remaining rises back to ``>=`` that threshold so alerts
    can re-fire after recovery.

    When several thresholds are violated in one evaluation (e.g. remaining 8%
    with alerts at 25% and 10%), at most one desktop notification is sent for
    that limit kind, using the tightest newly crossed threshold; all violated
    thresholds are latched so they do not each emit a separate bubble.
    """
    if notification_service is None or not notification_service.is_enabled:
        return
    if not threshold_percents or not remaining_by_limit:
        return

    thresholds = sorted(
        {_normalize_threshold_latch_key(t) for t in threshold_percents if t > 0},
        reverse=True,
    )
    if not thresholds:
        return

    for limit_kind, remaining in remaining_by_limit:
        if not isinstance(limit_kind, str) or not limit_kind.strip():
            continue
        kind = limit_kind.strip().lower()
        if kind not in ("primary", "secondary"):
            continue
        if not math.isfinite(float(remaining)):
            continue
        rem = float(remaining)

        for thr in thresholds:
            key = (managed_account_id, kind, thr)
            if rem >= thr:
                latch_keys.discard(key)

        violated = [thr for thr in thresholds if rem < thr]
        if not violated:
            continue

        newly_violated = [
            thr for thr in violated if (managed_account_id, kind, thr) not in latch_keys
        ]
        if not newly_violated:
            continue

        worst_new = min(newly_violated)
        message = build_codex_low_remaining_notification_message(
            email=email,
            managed_account_id=managed_account_id,
            chatgpt_account_id=chatgpt_account_id,
            limit_kind=kind,
            threshold_percent=worst_new,
            remaining_percent=rem,
        )
        try:
            await notification_service.send_notification(
                title=_CODEX_QUOTA_LOW_REMAINING_TITLE,
                message=message,
            )
        except Exception as exc:
            logger.warning(
                "Codex low remaining quota notification failed: %s",
                exc,
                exc_info=True,
            )
            continue

        for thr in violated:
            latch_keys.add((managed_account_id, kind, thr))
