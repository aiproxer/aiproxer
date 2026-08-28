from __future__ import annotations

import asyncio
from unittest.mock import AsyncMock, Mock

import pytest
from src.core.config.app_config import BackendConfig
from src.core.services.model_capability_index import (
    BackendModelEnumeration,
    BackendModelEnumeratorRegistry,
    ModelCapabilityDiscoverer,
    ModelCapabilityIndex,
    ModelCapabilityRefreshController,
)


def _mock_config_provider(configs: dict[str, BackendConfig]) -> Mock:
    provider = Mock()
    provider.get_backend_config.side_effect = lambda name: configs.get(name)
    provider.iter_backend_names.side_effect = lambda: list(configs.keys())
    provider.iter_configured_backend_names.side_effect = lambda: [
        name
        for name, cfg in configs.items()
        if cfg.connector or cfg.models or cfg.api_key or cfg.api_url or cfg.extra
    ]
    return provider


def test_index_from_config_provider_builds_canonical_and_alias_lookups() -> None:
    provider = _mock_config_provider(
        {
            "openai.1": BackendConfig(models=["openai/gpt-4o"]),
            "openai.2": BackendConfig(models=["gpt-4o"]),
            "anthropic.1": BackendConfig(models=["anthropic/claude-3-haiku"]),
        }
    )

    index = ModelCapabilityIndex.from_config_provider(provider)

    assert index.get_candidates("openai/gpt-4o") == ["openai.1", "openai.2"]
    assert index.get_candidates("gpt-4o") == ["openai.1", "openai.2"]
    assert index.get_candidates("anthropic/claude-3-haiku") == ["anthropic.1"]
    assert index.get_candidates("claude-3-haiku") == ["anthropic.1"]


@pytest.mark.asyncio
async def test_discoverer_prefers_live_enumeration_with_config_hint_fallback() -> None:
    provider = _mock_config_provider(
        {
            "openai.1": BackendConfig(models=["openai/gpt-4o-hint"]),
            "anthropic.1": BackendConfig(models=["anthropic/claude-3-haiku"]),
        }
    )

    openai_backend = Mock()
    openai_backend.get_available_models_async = AsyncMock(
        return_value=["openai/gpt-4o"]
    )

    lifecycle = Mock()
    lifecycle.get_active_backends.return_value = {"openai.1": openai_backend}

    discoverer = ModelCapabilityDiscoverer(
        config_provider=provider,
        backend_lifecycle_manager=lifecycle,
    )
    snapshot = await discoverer.discover_snapshot()

    assert snapshot.model_to_instances["openai/gpt-4o"] == ("openai.1",)
    assert snapshot.model_to_instances["anthropic/claude-3-haiku"] == ("anthropic.1",)


@pytest.mark.asyncio
async def test_discoverer_enumerates_configured_inactive_local_agent_instance() -> None:
    config = BackendConfig(connector="cursor-cli-acp", extra={"model": "auto"})
    provider = _mock_config_provider({"cursor-cli-acp.project": config})
    provider.iter_configured_backend_names.return_value = ["cursor-cli-acp.project"]
    lifecycle = Mock()
    lifecycle.get_active_backends.return_value = {}
    enumerator = Mock()
    enumerator.enumerate = AsyncMock(
        return_value=BackendModelEnumeration.available(
            instance_name="cursor-cli-acp.project",
            connector="cursor-cli-acp",
            models=["cursor/glm-5.2-max"],
            source="cursor_cli",
            instance_pinned=True,
        )
    )
    registry = BackendModelEnumeratorRegistry()
    registry.register("cursor-cli-acp", enumerator)

    discoverer = ModelCapabilityDiscoverer(
        config_provider=provider,
        backend_lifecycle_manager=lifecycle,
        enumerator_registry=registry,
    )
    snapshot = await discoverer.discover_snapshot()

    assert snapshot.instance_to_models["cursor-cli-acp.project"] == (
        "cursor/glm-5.2-max",
    )
    assert snapshot.instance_route_policy["cursor-cli-acp.project"] == (
        "instance_pinned"
    )
    assert snapshot.discovery_status_by_instance["cursor-cli-acp.project"].status == (
        "available"
    )


@pytest.mark.asyncio
async def test_discoverer_can_delegate_timeout_to_enumerator() -> None:
    config = BackendConfig(connector="cursor-cli-acp")
    provider = _mock_config_provider({"cursor-cli-acp.project": config})
    lifecycle = Mock()
    lifecycle.get_active_backends.return_value = {}
    enumerator = Mock()

    async def _enumerate(instance_name: str, backend_config: BackendConfig):
        del instance_name, backend_config
        await asyncio.sleep(0.02)
        return BackendModelEnumeration.available(
            instance_name="cursor-cli-acp.project",
            connector="cursor-cli-acp",
            models=["cursor/glm-5.2-max"],
            source="cursor_cli",
            instance_pinned=True,
        )

    enumerator.enumerate = _enumerate
    registry = BackendModelEnumeratorRegistry()
    registry.register("cursor-cli-acp", enumerator, timeout_seconds=None)

    snapshot = await ModelCapabilityDiscoverer(
        config_provider=provider,
        backend_lifecycle_manager=lifecycle,
        enumerator_registry=registry,
    ).discover_snapshot()

    assert snapshot.instance_to_models["cursor-cli-acp.project"] == (
        "cursor/glm-5.2-max",
    )


@pytest.mark.asyncio
async def test_discoverer_does_not_probe_unconfigured_registered_instance() -> None:
    provider = Mock()
    provider.get_backend_config.return_value = BackendConfig(connector="cursor-cli-acp")
    provider.iter_backend_names.return_value = ["cursor-cli-acp.default"]
    provider.iter_configured_backend_names.return_value = []

    lifecycle = Mock()
    lifecycle.get_active_backends.return_value = {}
    enumerator = Mock()
    enumerator.enumerate = AsyncMock()
    registry = BackendModelEnumeratorRegistry()
    registry.register("cursor-cli-acp", enumerator, timeout_seconds=None)

    snapshot = await ModelCapabilityDiscoverer(
        config_provider=provider,
        backend_lifecycle_manager=lifecycle,
        enumerator_registry=registry,
    ).discover_snapshot()

    enumerator.enumerate.assert_not_awaited()
    assert "cursor-cli-acp.default" not in snapshot.instance_to_models


@pytest.mark.asyncio
async def test_discoverer_omits_only_failed_authoritative_instance() -> None:
    configs = {
        "cursor-cli-acp.failed": BackendConfig(connector="cursor-cli-acp"),
        "agy-cli-acp.configured": BackendConfig(
            connector="agy-cli-acp", models=["google/gemini-3.5-flash-high"]
        ),
    }
    provider = _mock_config_provider(configs)
    provider.iter_configured_backend_names.return_value = list(configs)
    lifecycle = Mock()
    lifecycle.get_active_backends.return_value = {}

    failed = Mock()
    failed.enumerate = AsyncMock(
        return_value=BackendModelEnumeration.unavailable(
            instance_name="cursor-cli-acp.failed",
            connector="cursor-cli-acp",
            source="cursor_cli",
            error_code="command_failed",
            instance_pinned=True,
        )
    )
    explicit = Mock()
    explicit.enumerate = AsyncMock(
        return_value=BackendModelEnumeration.available(
            instance_name="agy-cli-acp.configured",
            connector="agy-cli-acp",
            models=["google/gemini-3.5-flash-high"],
            source="configured",
            instance_pinned=True,
        )
    )
    registry = BackendModelEnumeratorRegistry()
    registry.register("cursor-cli-acp", failed)
    registry.register("agy-cli-acp", explicit)

    snapshot = await ModelCapabilityDiscoverer(
        config_provider=provider,
        backend_lifecycle_manager=lifecycle,
        enumerator_registry=registry,
    ).discover_snapshot()

    assert snapshot.instance_to_models["cursor-cli-acp.failed"] == ()
    assert snapshot.instance_to_models["agy-cli-acp.configured"] == (
        "google/gemini-3.5-flash-high",
    )
    assert "cursor/glm-5.2-max" not in snapshot.model_to_instances
    assert snapshot.discovery_status_by_instance["cursor-cli-acp.failed"].status == (
        "unavailable"
    )


@pytest.mark.asyncio
async def test_refresh_controller_retains_last_known_good_snapshot_on_failure() -> None:
    provider = _mock_config_provider(
        {"openai.1": BackendConfig(models=["openai/gpt-4o"])}
    )
    index = ModelCapabilityIndex.from_config_provider(provider)

    good_snapshot = index.get_snapshot()

    discoverer = Mock()
    discoverer.discover_snapshot = AsyncMock(
        side_effect=[good_snapshot, RuntimeError("boom")]
    )

    controller = ModelCapabilityRefreshController(
        index=index,
        discoverer=discoverer,
        refresh_interval_seconds=0.0,
        failure_backoff_seconds=30.0,
    )

    assert await controller.refresh_now(reason="startup")
    generation_after_success = index.get_snapshot().generation

    assert not await controller.refresh_now(reason="on-demand")
    assert index.get_snapshot().generation == generation_after_success


@pytest.mark.asyncio
async def test_refresh_controller_enforces_backoff_after_failure() -> None:
    provider = _mock_config_provider(
        {"openai.1": BackendConfig(models=["openai/gpt-4o"])}
    )
    index = ModelCapabilityIndex.from_config_provider(provider)

    discoverer = Mock()
    discoverer.discover_snapshot = AsyncMock(side_effect=RuntimeError("boom"))

    controller = ModelCapabilityRefreshController(
        index=index,
        discoverer=discoverer,
        refresh_interval_seconds=0.0,
        failure_backoff_seconds=30.0,
    )

    assert not await controller.refresh_now(reason="startup")
    assert not await controller.refresh_now(reason="on-demand")
    assert discoverer.discover_snapshot.await_count == 1


@pytest.mark.asyncio
async def test_refresh_controller_serializes_concurrent_refreshes() -> None:
    provider = _mock_config_provider(
        {"openai.1": BackendConfig(models=["openai/gpt-4o"])}
    )
    index = ModelCapabilityIndex.from_config_provider(provider)

    current_in_flight = 0
    max_in_flight = 0

    async def _discover_snapshot(*, generation: int = 1):
        nonlocal current_in_flight, max_in_flight
        current_in_flight += 1
        max_in_flight = max(max_in_flight, current_in_flight)
        await asyncio.sleep(0.05)
        current_in_flight -= 1
        return index.get_snapshot()

    discoverer = Mock()
    discoverer.discover_snapshot = AsyncMock(side_effect=_discover_snapshot)

    controller = ModelCapabilityRefreshController(
        index=index,
        discoverer=discoverer,
        refresh_interval_seconds=0.0,
        failure_backoff_seconds=0.0,
    )

    await asyncio.gather(
        controller.refresh_now(reason="a"),
        controller.refresh_now(reason="b"),
        controller.refresh_now(reason="c"),
    )

    assert max_in_flight == 1


@pytest.mark.asyncio
async def test_discoverer_enumerates_configured_opencode_go_instances() -> None:
    from src.connectors.opencode_go import OpencodeGoConfiguredModelEnumerator

    configs = {
        "opencode-go.1": BackendConfig(
            connector="opencode-go",
            api_key="key-1",
            models=["glm-5.1"],
        ),
        "opencode-go.2": BackendConfig(
            connector="opencode-go",
            api_key="key-2",
            models=["glm-5.1", "minimax-m2.7"],
        ),
    }
    provider = _mock_config_provider(configs)
    provider.iter_configured_backend_names.return_value = list(configs)
    lifecycle = Mock()
    lifecycle.get_active_backends.return_value = {}

    registry = BackendModelEnumeratorRegistry()
    registry.register(
        "opencode-go",
        OpencodeGoConfiguredModelEnumerator(),
        timeout_seconds=None,
    )

    discoverer = ModelCapabilityDiscoverer(
        config_provider=provider,
        backend_lifecycle_manager=lifecycle,
        enumerator_registry=registry,
    )
    snapshot = await discoverer.discover_snapshot()

    assert snapshot.instance_to_models["opencode-go.1"] == ("opencode-go/glm-5.1",)
    assert snapshot.instance_to_models["opencode-go.2"] == (
        "opencode-go/glm-5.1",
        "opencode-go/minimax-m2.7",
    )
    # Both instances back opencode-go/glm-5.1 (unpinned for load balancing)
    assert snapshot.model_to_instances["opencode-go/glm-5.1"] == (
        "opencode-go.1",
        "opencode-go.2",
    )
    assert snapshot.model_to_instances["opencode-go/minimax-m2.7"] == ("opencode-go.2",)
    assert snapshot.alias_to_canonical["glm-5.1"] == "opencode-go/glm-5.1"
    assert snapshot.alias_to_canonical["minimax-m2.7"] == "opencode-go/minimax-m2.7"
    assert "opencode-go.1" not in snapshot.instance_route_policy
