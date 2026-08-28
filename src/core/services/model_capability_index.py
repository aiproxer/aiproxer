from __future__ import annotations

import asyncio
import inspect
import logging
import time
from collections import defaultdict
from collections.abc import Awaitable, Iterable, Mapping
from dataclasses import dataclass, field
from threading import RLock
from typing import TYPE_CHECKING, Any, cast

from src.core.common.model_catalog import BackendModelEnumeration
from src.core.domain.model_utils import (
    has_explicit_backend_selector,
    parse_model_backend,
)
from src.core.interfaces.backend_config_provider_interface import IBackendConfigProvider

if TYPE_CHECKING:
    from src.core.interfaces.backend_lifecycle_manager_interface import (
        IBackendLifecycleManager,
    )

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class BackendModelDiscoveryStatus:
    status: str
    source: str
    model_count: int
    error_code: str | None = None


class BackendModelEnumeratorRegistry:
    """Configured-instance model sources that do not require backend activation."""

    def __init__(self) -> None:
        self._entries: dict[str, tuple[Any, float | None]] = {}

    def register(
        self,
        connector: str,
        enumerator: Any,
        *,
        timeout_seconds: float | None = 15.0,
    ) -> None:
        deadline = (
            max(0.1, float(timeout_seconds)) if timeout_seconds is not None else None
        )
        self._entries[connector] = (enumerator, deadline)

    def get(self, connector: str) -> tuple[Any, float | None] | None:
        return self._entries.get(connector)


def _normalize_model_selector(selector: str) -> str:
    route, _, _ = selector.partition("?")
    candidate = route.strip()
    if not candidate:
        return ""
    if has_explicit_backend_selector(candidate):
        parsed = parse_model_backend(candidate, "")
        return parsed.model_name.strip()
    return candidate


def _build_aliases(canonical_model: str) -> set[str]:
    aliases = {canonical_model}
    if "/" in canonical_model:
        _, tail = canonical_model.split("/", 1)
        if tail:
            aliases.add(tail)
    return aliases


@dataclass(frozen=True)
class ModelCapabilitySnapshot:
    generation: int
    model_to_instances: dict[str, tuple[str, ...]]
    instance_to_models: dict[str, tuple[str, ...]]
    alias_to_canonical: dict[str, str]
    created_at_monotonic: float
    instance_route_policy: dict[str, str] = field(default_factory=dict)
    discovery_status_by_instance: dict[str, BackendModelDiscoveryStatus] = field(
        default_factory=dict
    )


class ModelCapabilityIndex:
    """Read-optimized model capability snapshot index."""

    def __init__(self, snapshot: ModelCapabilitySnapshot | None = None) -> None:
        self._lock = RLock()
        self._snapshot = snapshot or ModelCapabilitySnapshot(
            generation=0,
            model_to_instances={},
            instance_to_models={},
            alias_to_canonical={},
            created_at_monotonic=time.monotonic(),
        )

    @classmethod
    def from_config_provider(
        cls, config_provider: IBackendConfigProvider
    ) -> ModelCapabilityIndex:
        instance_to_models: dict[str, list[str]] = {}
        for backend_name in sorted(set(config_provider.iter_backend_names())):
            cfg = config_provider.get_backend_config(backend_name)
            models = list(getattr(cfg, "models", []) or [])
            instance_to_models[backend_name] = models
        snapshot = cls.build_snapshot(instance_to_models, generation=1)
        return cls(snapshot=snapshot)

    @classmethod
    def build_snapshot(
        cls,
        instance_to_models: Mapping[str, Iterable[str]],
        generation: int,
        *,
        instance_route_policy: Mapping[str, str] | None = None,
        discovery_status_by_instance: (
            Mapping[str, BackendModelDiscoveryStatus] | None
        ) = None,
    ) -> ModelCapabilitySnapshot:
        model_to_instances: dict[str, set[str]] = defaultdict(set)
        normalized_instance_to_models: dict[str, tuple[str, ...]] = {}
        alias_candidates: dict[str, set[str]] = defaultdict(set)

        for instance in sorted(instance_to_models.keys()):
            canonical_models: set[str] = set()
            for raw_model in instance_to_models.get(instance, []):
                canonical = _normalize_model_selector(str(raw_model))
                if not canonical:
                    continue
                canonical_models.add(canonical)
                for alias in _build_aliases(canonical):
                    alias_candidates[alias].add(canonical)

            ordered_models = tuple(sorted(canonical_models))
            normalized_instance_to_models[instance] = ordered_models
            for canonical in ordered_models:
                model_to_instances[canonical].add(instance)

        normalized_model_to_instances: dict[str, tuple[str, ...]] = {
            model: tuple(sorted(instances))
            for model, instances in model_to_instances.items()
        }

        alias_to_canonical: dict[str, str] = {}
        for alias, canonical_set in alias_candidates.items():
            alias_to_canonical[alias] = cls._select_canonical_for_alias(
                alias=alias,
                canonical_candidates=canonical_set,
                model_to_instances=normalized_model_to_instances,
            )

        # Canonical key must always resolve to itself.
        for canonical in normalized_model_to_instances:
            alias_to_canonical.setdefault(canonical, canonical)

        return ModelCapabilitySnapshot(
            generation=max(0, int(generation)),
            model_to_instances=normalized_model_to_instances,
            instance_to_models=normalized_instance_to_models,
            alias_to_canonical=alias_to_canonical,
            created_at_monotonic=time.monotonic(),
            instance_route_policy=dict(instance_route_policy or {}),
            discovery_status_by_instance=dict(discovery_status_by_instance or {}),
        )

    @staticmethod
    def _select_canonical_for_alias(
        *,
        alias: str,
        canonical_candidates: set[str],
        model_to_instances: Mapping[str, tuple[str, ...]],
    ) -> str:
        # Deterministic tie-break:
        # 1) Prefer candidate with more backing instances
        # 2) Prefer shortest canonical identifier
        # 3) Lexicographic fallback
        ranked = sorted(
            canonical_candidates,
            key=lambda candidate: (
                -len(model_to_instances.get(candidate, ())),
                len(candidate),
                candidate,
            ),
        )
        if not ranked:
            return alias
        return ranked[0]

    def get_snapshot(self) -> ModelCapabilitySnapshot:
        with self._lock:
            return self._snapshot

    def replace_snapshot(self, snapshot: ModelCapabilitySnapshot) -> None:
        with self._lock:
            self._snapshot = snapshot

    def publish_discovered(
        self,
        instance_to_models: Mapping[str, Iterable[str]],
        *,
        generation: int | None = None,
    ) -> ModelCapabilitySnapshot:
        current_generation = self.get_snapshot().generation
        next_generation = (
            max(0, int(generation))
            if generation is not None
            else max(1, current_generation + 1)
        )
        snapshot = self.build_snapshot(
            instance_to_models=instance_to_models,
            generation=next_generation,
        )
        self.replace_snapshot(snapshot)
        return snapshot

    def get_candidates(self, model_selector: str) -> list[str]:
        snapshot = self.get_snapshot()
        normalized = _normalize_model_selector(model_selector)
        if not normalized:
            return []
        canonical = snapshot.alias_to_canonical.get(normalized, normalized)
        candidates: set[str] = set(snapshot.model_to_instances.get(canonical, ()))
        candidates.update(snapshot.model_to_instances.get(normalized, ()))

        if "/" in normalized:
            _, tail = normalized.split("/", 1)
            if tail:
                candidates.update(snapshot.model_to_instances.get(tail, ()))
        else:
            suffix = f"/{normalized}"
            for model_key, model_instances in snapshot.model_to_instances.items():
                if model_key.endswith(suffix):
                    candidates.update(model_instances)

        return sorted(candidates)

    def get_models_for_instance(self, backend_instance: str) -> list[str]:
        snapshot = self.get_snapshot()
        return list(snapshot.instance_to_models.get(backend_instance, ()))


class ModelCapabilityDiscoverer:
    """Builds capability snapshots from live backends with config-hint fallback."""

    def __init__(
        self,
        *,
        config_provider: IBackendConfigProvider,
        backend_lifecycle_manager: IBackendLifecycleManager | None = None,
        enumerator_registry: BackendModelEnumeratorRegistry | None = None,
    ) -> None:
        self._config_provider = config_provider
        self._backend_lifecycle_manager = backend_lifecycle_manager
        self._enumerator_registry = (
            enumerator_registry or BackendModelEnumeratorRegistry()
        )

    async def discover_snapshot(self, generation: int = 1) -> ModelCapabilitySnapshot:
        active_backends: Mapping[str, Any] = {}
        if self._backend_lifecycle_manager is not None:
            try:
                active_backends = self._backend_lifecycle_manager.get_active_backends()
            except Exception as exc:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Failed to read active backends for capability discovery: %s",
                        exc,
                        exc_info=True,
                    )

        configured_backend_names = set(
            self._config_provider.iter_configured_backend_names()
        )
        active_backend_names = {name for name in active_backends if ":" not in name}
        all_backend_names = configured_backend_names | active_backend_names

        enumerations: dict[str, BackendModelEnumeration] = {}

        async def _enumerate_configured(
            backend_name: str,
        ) -> tuple[str, BackendModelEnumeration] | None:
            cfg = self._config_provider.get_backend_config(backend_name)
            if cfg is None:
                return None
            connector = str(getattr(cfg, "connector", "") or "").strip()
            if not connector:
                connector = backend_name.split(".", 1)[0]
            entry = self._enumerator_registry.get(connector)
            if entry is None:
                return None
            enumerator, timeout_seconds = entry
            try:
                enumeration = enumerator.enumerate(backend_name, cfg)
                if timeout_seconds is None:
                    result = await enumeration
                else:
                    result = await asyncio.wait_for(
                        enumeration,
                        timeout=timeout_seconds,
                    )
                if isinstance(result, BackendModelEnumeration):
                    return backend_name, result
            except asyncio.TimeoutError:
                return backend_name, BackendModelEnumeration.unavailable(
                    instance_name=backend_name,
                    connector=connector,
                    source=connector,
                    error_code="timeout",
                    instance_pinned=True,
                )
            except Exception:
                logger.warning(
                    "Configured model enumeration failed for %s",
                    backend_name,
                    exc_info=True,
                )
                return backend_name, BackendModelEnumeration.unavailable(
                    instance_name=backend_name,
                    connector=connector,
                    source=connector,
                    error_code="enumeration_failed",
                    instance_pinned=True,
                )
            return None

        # Configured-instance enumerators are intentionally restricted to
        # explicitly configured backend entries. Registered connector names
        # without configuration must not trigger local CLI discovery.
        tasks = [
            _enumerate_configured(name) for name in sorted(configured_backend_names)
        ]
        if tasks:
            for item in await asyncio.gather(*tasks):
                if item is not None:
                    enumerations[item[0]] = item[1]

        instance_to_models: dict[str, list[str]] = {}
        route_policy: dict[str, str] = {}
        discovery_status: dict[str, BackendModelDiscoveryStatus] = {}
        for backend_name in sorted(all_backend_names):
            enumeration = enumerations.get(backend_name)
            if enumeration is not None:
                instance_to_models[backend_name] = list(enumeration.models)
                if enumeration.instance_pinned:
                    route_policy[backend_name] = "instance_pinned"
                discovery_status[backend_name] = BackendModelDiscoveryStatus(
                    status=enumeration.status,
                    source=enumeration.source,
                    model_count=len(enumeration.models),
                    error_code=enumeration.error_code,
                )
                continue
            backend = active_backends.get(backend_name)
            discovered_models = await self._enumerate_live_models(backend)
            if not discovered_models:
                cfg = self._config_provider.get_backend_config(backend_name)
                discovered_models = list(getattr(cfg, "models", []) or [])
            instance_to_models[backend_name] = discovered_models

        return ModelCapabilityIndex.build_snapshot(
            instance_to_models=instance_to_models,
            generation=generation,
            instance_route_policy=route_policy,
            discovery_status_by_instance=discovery_status,
        )

    async def _enumerate_live_models(self, backend: Any | None) -> list[str]:
        if backend is None:
            return []

        get_models_async = getattr(backend, "get_available_models_async", None)
        if callable(get_models_async):
            try:
                result = get_models_async()
                if inspect.isawaitable(result):
                    result = await cast(Awaitable[Any], result)
                if isinstance(result, list):
                    return [str(item) for item in result if isinstance(item, str)]
            except Exception as exc:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Async model enumeration failed; falling back to hints: %s",
                        exc,
                        exc_info=True,
                    )

        get_models = getattr(backend, "get_available_models", None)
        if callable(get_models):
            try:
                result = get_models()
                if inspect.isawaitable(result):
                    result = await cast(Awaitable[Any], result)
                if isinstance(result, list):
                    return [str(item) for item in result if isinstance(item, str)]
            except Exception as exc:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Sync model enumeration failed; falling back to hints: %s",
                        exc,
                        exc_info=True,
                    )

        return []


class ModelCapabilityRefreshController:
    """Refresh lifecycle policy for capability snapshots."""

    def __init__(
        self,
        *,
        index: ModelCapabilityIndex,
        discoverer: ModelCapabilityDiscoverer,
        refresh_interval_seconds: float = 0.0,
        failure_backoff_seconds: float = 30.0,
    ) -> None:
        self._index = index
        self._discoverer = discoverer
        self._refresh_interval_seconds = max(0.0, float(refresh_interval_seconds))
        self._failure_backoff_seconds = max(0.0, float(failure_backoff_seconds))
        self._refresh_lock = asyncio.Lock()
        self._periodic_task: asyncio.Task[None] | None = None
        self._next_refresh_not_before = 0.0

    async def startup_refresh(self) -> bool:
        return await self.refresh_now(reason="startup")

    async def refresh_now(self, *, reason: str = "on-demand") -> bool:
        now = time.monotonic()
        if now < self._next_refresh_not_before:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Capability refresh skipped (%s): backoff active for %.2fs",
                    reason,
                    self._next_refresh_not_before - now,
                )
            return False

        async with self._refresh_lock:
            now = time.monotonic()
            if now < self._next_refresh_not_before:
                return False

            generation = self._index.get_snapshot().generation + 1
            try:
                snapshot = await self._discoverer.discover_snapshot(
                    generation=generation
                )
                self._index.replace_snapshot(snapshot)
                self._next_refresh_not_before = 0.0
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Capability refresh succeeded (%s), generation=%d",
                        reason,
                        snapshot.generation,
                    )
                return True
            except Exception as exc:
                self._next_refresh_not_before = (
                    time.monotonic() + self._failure_backoff_seconds
                )
                logger.warning(
                    "Capability refresh failed (%s): %s",
                    reason,
                    exc,
                    exc_info=True,
                )
                return False

    async def start_periodic_refresh(self) -> None:
        if self._refresh_interval_seconds <= 0:
            return
        if self._periodic_task is not None and not self._periodic_task.done():
            return
        self._periodic_task = asyncio.create_task(self._run_periodic_refresh_loop())

    async def stop_periodic_refresh(self) -> None:
        if self._periodic_task is None:
            return
        self._periodic_task.cancel()
        try:
            await self._periodic_task
        except asyncio.CancelledError:
            pass
        finally:
            self._periodic_task = None

    async def _run_periodic_refresh_loop(self) -> None:
        try:
            while True:
                await asyncio.sleep(self._refresh_interval_seconds)
                await self.refresh_now(reason="periodic")
        except asyncio.CancelledError:
            raise
