"""Routing and discovery registration for openai-chatgpt-plan (task 3.2)."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import Mock

import pytest
from src.connectors.base import add_vendor_prefix
from src.core.config.app_config import BackendConfig
from src.core.domain.model_utils import (
    has_explicit_backend_selector,
    parse_model_backend,
)
from src.core.services.model_capability_index import (
    BackendModelEnumeratorRegistry,
    ModelCapabilityDiscoverer,
)

from tests.unit.connectors.test_openai_chatgpt_plan_enumerator import (
    OTHER_PROFILE_ID,
    PROFILE_ID,
    _backend_config,
    _install_owned_models_http,
    _persist_two_ready_profiles,
)

REPO_ROOT = Path(__file__).resolve().parents[3]
ROUTING_PATH = (
    REPO_ROOT / "src" / "core" / "di" / "registrations" / "_backend" / "routing.py"
)
APPLICATION_STAGES_PATH = (
    REPO_ROOT / "src" / "core" / "app" / "stages" / "application_stages.py"
)
STAGES_DIR = REPO_ROOT / "src" / "core" / "app" / "stages"
BACKEND = "openai-chatgpt-plan"


class TestOpenAIChatGPTPlanModelSelector:
    def test_explicit_backend_selector_parses_openai_chatgpt_plan(self) -> None:
        model = f"{BACKEND}:gpt-4o"
        assert has_explicit_backend_selector(model) is True
        parsed = parse_model_backend(model)
        assert parsed.backend_type == BACKEND
        assert parsed.model_name == "gpt-4o"

    def test_vendor_slash_model_is_not_a_backend_selector(self) -> None:
        parsed = parse_model_backend("openai/gpt-4o", default_backend="")
        assert parsed.backend_type == ""
        assert parsed.model_name == "openai/gpt-4o"
        assert has_explicit_backend_selector("openai/gpt-4o") is False


class TestOpenAIChatGPTPlanRoutingRegistration:
    def test_routing_py_registers_chatgpt_plan_enumerator(self) -> None:
        source = ROUTING_PATH.read_text(encoding="utf-8")
        assert "ChatGPTPlanConfiguredModelEnumerator" in source
        assert '"openai-chatgpt-plan"' in source
        assert '"openai_chatgpt_plan"' in source
        assert "CodexModelCatalogStage" not in source
        assert "from src.connectors.openai_chatgpt_plan.enumerator import (" in source

    def test_model_catalog_service_maps_chatgpt_plan_to_openai_family(self) -> None:
        from src.core.services.model_catalog_service import ModelCatalogService

        mapping = ModelCatalogService._BACKEND_TO_CATALOG_PROVIDER
        assert mapping["openai-chatgpt-plan"] == "openai"
        assert mapping["openai-responses"] == "openai"


class TestOpenAIChatGPTPlanApplicationStages:
    def test_application_stages_has_no_new_chatgpt_plan_stage(self) -> None:
        source = APPLICATION_STAGES_PATH.read_text(encoding="utf-8")
        lowered = source.lower()
        assert "chatgpt-plan" not in lowered
        assert "chatgpt_plan" not in lowered
        assert "CodexModelCatalogStage" in source

        chatgpt_stage_files = [
            path.name
            for path in STAGES_DIR.iterdir()
            if path.is_file() and "chatgpt" in path.name.lower()
        ]
        assert chatgpt_stage_files == []


def _mock_config_provider(configs: dict[str, BackendConfig]) -> Mock:
    provider = Mock()
    provider.get_backend_config.side_effect = lambda name: configs.get(name)
    provider.iter_backend_names.side_effect = lambda: list(configs.keys())
    provider.iter_configured_backend_names.side_effect = lambda: list(configs.keys())
    return provider


class TestOpenAIChatGPTPlanCapabilityDiscoverer:
    @pytest.mark.asyncio
    async def test_snapshot_isolates_two_profile_pinned_instances(
        self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        from src.connectors.openai_chatgpt_plan.enumerator import (
            ChatGPTPlanConfiguredModelEnumerator,
        )

        _install_owned_models_http(monkeypatch)
        profiles_path = await _persist_two_ready_profiles(tmp_path)
        configs = {
            "openai-chatgpt-plan.home": _backend_config(
                PROFILE_ID, profiles_path=str(profiles_path)
            ),
            "openai-chatgpt-plan.work": _backend_config(
                OTHER_PROFILE_ID, profiles_path=str(profiles_path)
            ),
        }
        provider = _mock_config_provider(configs)
        lifecycle = Mock()
        lifecycle.get_active_backends.return_value = {}
        registry = BackendModelEnumeratorRegistry()
        registry.register(
            BACKEND,
            ChatGPTPlanConfiguredModelEnumerator(),
            timeout_seconds=None,
        )

        snapshot = await ModelCapabilityDiscoverer(
            config_provider=provider,
            backend_lifecycle_manager=lifecycle,
            enumerator_registry=registry,
        ).discover_snapshot()

        home_models = snapshot.instance_to_models["openai-chatgpt-plan.home"]
        work_models = snapshot.instance_to_models["openai-chatgpt-plan.work"]
        home_unique = add_vendor_prefix("gpt-4o", "openai")
        work_unique = add_vendor_prefix("o3-mini", "openai")
        shared = add_vendor_prefix("gpt-4.1", "openai")

        assert home_unique in home_models
        assert work_unique in work_models
        assert shared in home_models
        assert shared in work_models
        assert home_unique not in work_models
        assert work_unique not in home_models
        assert snapshot.instance_route_policy["openai-chatgpt-plan.home"] == (
            "instance_pinned"
        )
        assert snapshot.instance_route_policy["openai-chatgpt-plan.work"] == (
            "instance_pinned"
        )
        assert (
            snapshot.discovery_status_by_instance["openai-chatgpt-plan.home"].status
            == "available"
        )
        assert (
            snapshot.discovery_status_by_instance["openai-chatgpt-plan.work"].status
            == "available"
        )
