from __future__ import annotations

import fnmatch
import logging
import re
from threading import Lock
from typing import Any

from src.core.common.exceptions import RoutingError
from src.core.config.app_config import RoutingConfig
from src.core.config.constrained_backend_policy import (
    collapse_constrained_backend_candidates,
    match_constrained_connector_family,
)
from src.core.interfaces.backend_config_provider_interface import IBackendConfigProvider
from src.core.interfaces.backend_lifecycle_manager_interface import (
    IBackendLifecycleManager,
)
from src.core.interfaces.resilience_interface import IResilienceCoordinator
from src.core.services.model_capability_index import (
    ModelCapabilityDiscoverer,
    ModelCapabilityIndex,
    ModelCapabilityRefreshController,
    ModelCapabilitySnapshot,
)

logger = logging.getLogger(__name__)


class BackendRoutingService:
    """Service for routing requests to appropriate backend instances.

    Handles:
    1. Variant 1: Explicit instance routing (e.g. "openai.1")
    2. Variant 2: Load balancing across instances (e.g. "openai" -> "openai.1", "openai.2")
    3. Variant 3: Model-based discovery (e.g. "gpt-4" -> "openai.1")
    """

    def __init__(
        self,
        config_provider: IBackendConfigProvider,
        routing_config: RoutingConfig | None = None,
        capability_index: ModelCapabilityIndex | None = None,
        capability_discoverer: ModelCapabilityDiscoverer | None = None,
        capability_refresh_controller: ModelCapabilityRefreshController | None = None,
        backend_lifecycle_manager: IBackendLifecycleManager | None = None,
        resilience_coordinator: IResilienceCoordinator | None = None,
    ) -> None:
        self._config_provider = config_provider
        self._routing_config = routing_config or RoutingConfig()
        self._backend_lifecycle_manager = backend_lifecycle_manager
        self._resilience_coordinator = resilience_coordinator
        self._rr_counters: dict[str, int] = {}
        self._rr_lock = Lock()
        self._capability_index = (
            capability_index
            or ModelCapabilityIndex.from_config_provider(config_provider)
        )
        self._capability_discoverer = (
            capability_discoverer
            or ModelCapabilityDiscoverer(config_provider=config_provider)
        )
        self._capability_refresh_controller = capability_refresh_controller or ModelCapabilityRefreshController(
            index=self._capability_index,
            discoverer=self._capability_discoverer,
            refresh_interval_seconds=self._routing_config.capability_refresh_interval_seconds,
            failure_backoff_seconds=self._routing_config.capability_refresh_backoff_seconds,
        )

    def resolve_backend_instance(
        self,
        backend_type: str | None,
        model: str,
        excluded_backends: set[str] | None = None,
    ) -> str | None:
        """Resolve the specific backend instance to use.

        Args:
            backend_type: The requested backend type (e.g. "openai", "openai.1", or None)
            model: The requested model name
            excluded_backends: Backend instance names that must be skipped (e.g., permanently disabled)

        Returns:
            The resolved backend instance name (e.g. "openai.1"), or None if resolution failed.

        Raises:
            RoutingError: If the requested routing method is disabled by policy.
        """
        excluded = excluded_backends or set()

        # Case 1: Specific instance requested (contains dot)
        if backend_type and "." in backend_type:
            if (
                self._routing_config.disable_backend_ids
                or self._routing_config.disable_backend_names
            ):
                raise RoutingError(
                    message=f"Routing by explicit backend instance ID ('{backend_type}') is disabled by policy.",
                    details={
                        "code": "policy_rejected",
                        "backend_type": backend_type,
                        "model": model,
                    },
                )
            return None if backend_type in excluded else backend_type

        # Case 2: Generic backend requested (e.g. "openai")
        if backend_type:
            if self._routing_config.disable_backend_names:
                raise RoutingError(
                    message=f"Routing by backend name ('{backend_type}') is disabled by policy.",
                    details={
                        "code": "policy_rejected",
                        "backend_type": backend_type,
                        "model": model,
                    },
                )
            return self._resolve_generic_backend(backend_type, model, excluded)

        # Case 3: Only model provided, discover backend
        if self._routing_config.disable_model_names:
            raise RoutingError(
                message=f"Routing by model name only ('{model}') is disabled by policy.",
                details={"code": "policy_rejected", "model": model},
            )
        return self._discover_backend_for_model(model, excluded)

    def resolve_model_only_backend(
        self,
        model: str,
        excluded_backends: set[str] | None = None,
    ) -> str:
        """Resolve model-only selector and raise structured routing errors."""
        if self._routing_config.disable_model_names:
            raise RoutingError(
                message=f"Routing by model name only ('{model}') is disabled by policy.",
                details={"code": "policy_rejected", "model": model},
            )

        excluded = excluded_backends or set()
        candidates = self._discover_model_candidates(model)
        if not candidates:
            raise RoutingError(
                message=self._build_unknown_model_message(model),
                details=self._build_routing_error_details(
                    code="unknown_model",
                    model=model,
                    retryable=False,
                ),
            )

        eligible = self._filter_eligible_candidates(
            model=model,
            candidates=candidates,
            excluded=excluded,
        )
        if not eligible:
            raise RoutingError(
                message=(
                    f"Model '{model}' is temporarily unavailable. "
                    f"All candidates are currently excluded."
                ),
                details=self._build_routing_error_details(
                    code="temporarily_unavailable",
                    model=model,
                    candidates=sorted(candidates),
                    reason="all_candidates_filtered",
                ),
            )

        ranked_buckets = self._rank_model_candidates(model=model, candidates=eligible)
        if not ranked_buckets:
            raise RoutingError(
                message=f"Model '{model}' is temporarily unavailable.",
                details=self._build_routing_error_details(
                    code="temporarily_unavailable",
                    model=model,
                    candidates=sorted(candidates),
                    reason="no_ranked_candidates",
                ),
            )

        top_bucket = ranked_buckets[0]
        return self._select_instance(f"model:{model}", top_bucket)

    def _build_unknown_model_message(self, model: str) -> str:
        message = f"Unknown model '{model}'. No backend candidates discovered."
        alias_hint = self._build_reserved_selector_hint(model)
        if alias_hint:
            return f"{message} {alias_hint}"
        return message

    def _build_reserved_selector_hint(self, model: str) -> str | None:
        route_portion, _, _ = model.partition("?")
        if ":" not in route_portion:
            return None

        namespace, _, alias_name = route_portion.partition(":")
        normalized_namespace = namespace.strip().lower()
        if normalized_namespace not in {"alias", "auto"}:
            return None

        alias_rules = self._get_model_alias_rules()
        if not alias_rules:
            return (
                f"The `{normalized_namespace}:` selector namespace uses model alias rules, "
                "but no `model_aliases` are loaded. If you expected YAML aliases, verify "
                "the server was started with the intended `--config` file."
            )

        if alias_name and self._matches_any_alias_rule(route_portion, alias_rules):
            return None

        return (
            f"The `{normalized_namespace}:` selector namespace uses model alias rules, "
            f"but no configured alias matched '{route_portion}'."
        )

    def _get_model_alias_rules(self) -> list[Any]:
        app_config = getattr(self._config_provider, "_app_config", None)
        alias_rules = getattr(app_config, "model_aliases", None)
        if isinstance(alias_rules, list):
            return alias_rules
        return []

    @staticmethod
    def _matches_any_alias_rule(model: str, alias_rules: list[Any]) -> bool:
        for alias_rule in alias_rules:
            pattern = getattr(alias_rule, "pattern", None)
            if not isinstance(pattern, str) or not pattern:
                continue
            try:
                if re.search(pattern, model):
                    return True
            except re.error:
                continue
        return False

    def _resolve_generic_backend(
        self, backend_type: str, model: str, excluded: set[str]
    ) -> str | None:
        """Resolve a generic backend type to a specific instance using Round Robin."""
        instances = self._filter_eligible_candidates(
            model=model,
            candidates=self._find_instances_for_backend(backend_type),
            excluded=excluded,
        )

        if not instances:
            # If no specific instances found, fall back to the generic name
            # This handles cases where only "openai" is configured without "openai.1"
            if backend_type in excluded:
                return None
            if not self._is_candidate_eligible(backend_type, model, excluded):
                return None
            return backend_type

        return self._select_instance(backend_type, instances, excluded)

    def _discover_backend_for_model(self, model: str, excluded: set[str]) -> str | None:
        """Find a backend that supports the given model."""
        candidates = self._filter_eligible_candidates(
            model=model,
            candidates=self._discover_model_candidates(model),
            excluded=excluded,
        )
        if not candidates:
            return None

        ranked_buckets = self._rank_model_candidates(model=model, candidates=candidates)
        if not ranked_buckets:
            return None

        return self._select_instance(f"model:{model}", ranked_buckets[0], excluded)

    def _discover_model_candidates(self, model: str) -> list[str]:
        candidates = self._capability_index.get_candidates(model)
        if not candidates:
            candidates = self._discover_model_candidates_from_configs(model)
        return sorted(set(candidates))

    def _discover_model_candidates_from_configs(self, model: str) -> list[str]:
        candidates: list[str] = []

        model_variants = {model}
        if "/" in model:
            _, tail = model.split("/", 1)
            if tail:
                model_variants.add(tail)

        if hasattr(self._config_provider, "iter_backend_names"):
            for backend_name in self._config_provider.iter_backend_names():
                cfg = self._config_provider.get_backend_config(backend_name)
                models = getattr(cfg, "models", None) if cfg else None
                if (
                    cfg
                    and models
                    and any(variant in models for variant in model_variants)
                ):
                    candidates.append(backend_name)

        return candidates

    def _discover_all_backend_candidates(self) -> list[str]:
        candidates: list[str] = []
        if hasattr(self._config_provider, "iter_backend_names"):
            candidates = list(self._config_provider.iter_backend_names())
        return sorted(set(candidates))

    def _is_model_catalog_unavailable(self) -> bool:
        snapshot = self._capability_index.get_snapshot()
        if snapshot.model_to_instances:
            return False

        if not hasattr(self._config_provider, "iter_backend_names"):
            return True

        for backend_name in self._config_provider.iter_backend_names():
            cfg = self._config_provider.get_backend_config(backend_name)
            models = getattr(cfg, "models", None) if cfg else None
            if models:
                return False
        return True

    def _find_instances_for_backend(self, backend_type: str) -> list[str]:
        """Find all configured instances for a given backend type."""
        instances = []

        if hasattr(self._config_provider, "iter_backend_names"):
            for name in self._config_provider.iter_backend_names():
                # Check if name is like "{backend_type}.{id}"
                if name.startswith(f"{backend_type}."):
                    instances.append(name)

        # Sort to ensure consistent order for Round Robin
        instances.sort()
        collapsed = collapse_constrained_backend_candidates(instances)
        if len(collapsed) < len(instances) and logger.isEnabledFor(logging.WARNING):
            family = match_constrained_connector_family(backend_type) or backend_type
            logger.warning(
                "Constrained backend family '%s' has multiple configured instances %s; "
                "proxy routing will use deterministic single instance %s",
                family,
                instances,
                collapsed,
            )
        return collapsed

    def _filter_eligible_candidates(
        self,
        *,
        model: str,
        candidates: list[str],
        excluded: set[str],
    ) -> list[str]:
        filtered = [
            candidate
            for candidate in sorted(set(candidates))
            if self._is_candidate_eligible(candidate, model, excluded)
        ]
        return collapse_constrained_backend_candidates(filtered)

    def _is_candidate_eligible(
        self,
        candidate: str,
        model: str,
        excluded: set[str],
    ) -> bool:
        return self._candidate_ineligibility_reason(candidate, model, excluded) is None

    def _candidate_ineligibility_reason(
        self,
        candidate: str,
        model: str,
        excluded: set[str],
    ) -> str | None:
        if candidate in excluded:
            return "excluded_disabled_backend"

        if self._backend_lifecycle_manager is not None:
            disabled_backends = self._backend_lifecycle_manager.get_disabled_backends()
            if candidate in disabled_backends:
                disabled_info = disabled_backends.get(candidate)
                reason = getattr(disabled_info, "reason", None)
                return f"lifecycle_disabled:{reason or 'unknown'}"

        if self._resilience_coordinator is not None:
            decision = self._resilience_coordinator.check_availability(candidate, model)
            if not decision.should_proceed():
                return decision.reason or "resilience_reject"

        return None

    def describe_ineligibility(
        self,
        backend_type: str,
        model: str,
        excluded_backends: set[str] | None = None,
    ) -> str | None:
        excluded = excluded_backends or set()
        instances = self._find_instances_for_backend(backend_type)
        if instances:
            reasons = [
                f"{instance}:{self._candidate_ineligibility_reason(instance, model, excluded) or 'eligible'}"
                for instance in instances
            ]
            generic_reason = self._candidate_ineligibility_reason(
                backend_type, model, excluded
            )
            return (
                f"instances={';'.join(reasons)};generic={generic_reason or 'eligible'}"
            )
        return self._candidate_ineligibility_reason(backend_type, model, excluded)

    def _select_instance(
        self, key: str, instances: list[str], excluded: set[str] | None = None
    ) -> str:
        """Select an instance from the list using Round Robin."""
        if excluded:
            instances = [i for i in instances if i not in excluded]
        if not instances:
            raise ValueError("No instances provided for selection")

        with self._rr_lock:
            current_index = self._rr_counters.get(key, 0)
            selected = instances[current_index % len(instances)]
            self._rr_counters[key] = current_index + 1

        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                f"Routing '{key}' to instance '{selected}' (RR index {current_index})"
            )

        return selected

    def _rank_model_candidates(
        self, model: str, candidates: list[str]
    ) -> list[list[str]]:
        if not candidates:
            return []

        policy = self._select_preference_policy(model=model, candidates=candidates)
        if policy == "round_robin":
            return [sorted(candidates)]

        scored: dict[float, list[str]] = {}
        for candidate in sorted(candidates):
            score = self._score_candidate(candidate=candidate, policy=policy)
            scored.setdefault(score, []).append(candidate)

        return [sorted(scored[score]) for score in sorted(scored.keys(), reverse=True)]

    def _select_preference_policy(self, model: str, candidates: list[str]) -> str:
        model_overrides = self._routing_config.model_only_model_overrides
        matched_patterns = [
            pattern
            for pattern in model_overrides
            if pattern == model or fnmatch.fnmatch(model, pattern)
        ]
        if matched_patterns:
            selected_pattern = sorted(
                matched_patterns,
                key=lambda pattern: (
                    -len(pattern.replace("*", "").replace("?", "")),
                    pattern.count("*") + pattern.count("?"),
                    pattern,
                ),
            )[0]
            return model_overrides[selected_pattern]

        family_overrides = self._routing_config.model_only_backend_family_overrides
        if family_overrides:
            family_policies = {
                family_overrides[self._extract_backend_family(candidate)]
                for candidate in candidates
                if self._extract_backend_family(candidate) in family_overrides
            }
            if len(family_policies) == 1:
                return next(iter(family_policies))

        return self._routing_config.model_only_preference_policy

    @staticmethod
    def _extract_backend_family(backend_name: str) -> str:
        if "." in backend_name:
            return backend_name.split(".", 1)[0]
        return backend_name

    def _score_candidate(self, *, candidate: str, policy: str) -> float:
        cfg = self._config_provider.get_backend_config(candidate)
        cfg_extra = getattr(cfg, "extra", None)
        extra: dict[str, Any] = cfg_extra if isinstance(cfg_extra, dict) else {}

        if policy == "cost":
            raw_cost = extra.get("routing_cost", extra.get("cost"))
            try:
                numeric_cost = (
                    float(raw_cost)
                    if raw_cost is not None
                    else float(self._routing_config.model_only_missing_cost)
                )
            except (TypeError, ValueError):
                numeric_cost = float(self._routing_config.model_only_missing_cost)
            return -numeric_cost

        if policy == "priority":
            raw_priority = extra.get("routing_priority", extra.get("priority"))
            try:
                numeric_priority = (
                    float(raw_priority)
                    if raw_priority is not None
                    else float(self._routing_config.model_only_missing_priority)
                )
            except (TypeError, ValueError):
                numeric_priority = float(
                    self._routing_config.model_only_missing_priority
                )
            return numeric_priority

        return 0.0

    @staticmethod
    def _build_routing_error_details(
        *,
        code: str,
        model: str,
        candidates: list[str] | None = None,
        reason: str | None = None,
        retryable: bool | None = None,
    ) -> dict[str, Any]:
        category = "validation" if code == "unknown_model" else "availability"
        resolved_retryable = (
            retryable if retryable is not None else code == "temporarily_unavailable"
        )

        details: dict[str, Any] = {
            "code": code,
            "category": category,
            "retryable": resolved_retryable,
            "model": model,
        }
        if candidates is not None:
            details["candidates"] = candidates
        if reason:
            details["reason"] = reason
        return details

    async def refresh_model_capabilities(self, *, reason: str = "on-demand") -> bool:
        """Refresh capability snapshot (startup, periodic, or on-demand)."""
        return await self._capability_refresh_controller.refresh_now(reason=reason)

    async def start_model_capability_refresh(self) -> None:
        await self._capability_refresh_controller.start_periodic_refresh()

    async def stop_model_capability_refresh(self) -> None:
        await self._capability_refresh_controller.stop_periodic_refresh()

    def get_model_capability_snapshot(self) -> ModelCapabilitySnapshot:
        """Return the current capability snapshot for observability surfaces."""
        return self._capability_index.get_snapshot()

    def build_model_eligibility_diagnostics(
        self,
        *,
        model_limit: int = 200,
        instances_per_model_limit: int = 20,
    ) -> dict[str, Any]:
        """Build bounded model-eligibility diagnostics for observability."""
        safe_model_limit = max(1, int(model_limit))
        safe_instances_limit = max(1, int(instances_per_model_limit))

        snapshot = self._capability_index.get_snapshot()
        canonical_models = sorted(set(snapshot.alias_to_canonical.values()))
        selected_models = canonical_models[:safe_model_limit]
        models_omitted = max(0, len(canonical_models) - len(selected_models))

        model_eligibility: list[dict[str, Any]] = []
        for model in selected_models:
            candidates = sorted(set(self._capability_index.get_candidates(model)))
            eligible = self._filter_eligible_candidates(
                model=model, candidates=candidates, excluded=set()
            )
            applied_policy = self._select_preference_policy(
                model=model,
                candidates=eligible or candidates,
            )
            ranked_buckets = self._rank_model_candidates(
                model=model, candidates=eligible
            )
            tie_sets = [bucket for bucket in ranked_buckets if len(bucket) > 1]

            limited_eligible = eligible[:safe_instances_limit]
            omitted_instances = max(0, len(eligible) - len(limited_eligible))

            model_eligibility.append(
                {
                    "model": model,
                    "eligible_instances": limited_eligible,
                    "eligible_instance_count": len(eligible),
                    "instances_truncated": omitted_instances > 0,
                    "instances_omitted": omitted_instances,
                    "applied_preference_policy": applied_policy,
                    "equivalent_score_tie_sets": tie_sets,
                }
            )

        return {
            "default_preference_policy": self._routing_config.model_only_preference_policy,
            "proxy_selection_scope": "proxy_instance_model_selection",
            "connector_scheduling_scope": "connector_internal_and_opaque",
            "truncation": {
                "model_limit": safe_model_limit,
                "instances_per_model_limit": safe_instances_limit,
                "models_truncated": models_omitted > 0,
                "models_omitted": models_omitted,
            },
            "model_eligibility": model_eligibility,
        }

    def find_alternative_instances(
        self,
        model: str,
        exclude: list[str],
    ) -> list[str]:
        """Find backend instances that can serve the given model.

        This method is used by the failure handling strategy to find
        alternative backend instances when one fails.

        Args:
            model: Fully qualified model name (e.g., "openai/gpt-4o" or "gpt-4o").
            exclude: List of backend instance names to exclude (already tried).

        Returns:
            List of backend instance names that can serve the model,
            ordered by preference bucket (top bucket first).
        """
        excluded_set = set(exclude)
        candidates = self._filter_eligible_candidates(
            model=model,
            candidates=self._discover_model_candidates(model),
            excluded=excluded_set,
        )
        ranked_buckets = self._rank_model_candidates(model=model, candidates=candidates)
        ordered_candidates: list[str] = []
        for bucket in ranked_buckets:
            ordered_candidates.extend(sorted(bucket))

        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Found %d alternative instances for model '%s' (excluding %s): %s",
                len(ordered_candidates),
                model,
                exclude,
                ordered_candidates,
            )

        return ordered_candidates
