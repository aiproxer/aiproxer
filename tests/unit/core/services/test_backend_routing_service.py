from unittest.mock import Mock

import pytest
from pydantic import ValidationError
from src.core.common.exceptions import RoutingError
from src.core.config.app_config import BackendConfig, ModelAliasRule, RoutingConfig
from src.core.interfaces.resilience_interface import ActionType, ResilienceDecision
from src.core.services.backend_routing_service import BackendRoutingService
from src.core.services.model_capability_index import ModelCapabilityIndex


@pytest.fixture
def mock_config_provider():
    provider = Mock()
    provider.configs = {
        "openai.1": BackendConfig(api_key="k1", models=["gpt-4"]),
        "openai.2": BackendConfig(api_key="k2", models=["gpt-4", "gpt-3.5"]),
        "anthropic.1": BackendConfig(api_key="k3", models=["claude-3"]),
    }

    def get_config(name):
        return provider.configs.get(name)

    def iter_names():
        return provider.configs.keys()

    provider.get_backend_config.side_effect = get_config
    provider.iter_backend_names.side_effect = iter_names
    return provider


@pytest.fixture
def mock_config_provider_without_model_hints():
    provider = Mock()
    provider.configs = {
        "openai": BackendConfig(api_key="k1"),
        "anthropic": BackendConfig(api_key="k2"),
    }

    def get_config(name):
        return provider.configs.get(name)

    def iter_names():
        return provider.configs.keys()

    provider.get_backend_config.side_effect = get_config
    provider.iter_backend_names.side_effect = iter_names
    return provider


class TestBackendRoutingService:

    def test_explicit_routing_success(self, mock_config_provider):
        service = BackendRoutingService(mock_config_provider, RoutingConfig())
        result = service.resolve_backend_instance("openai.1", "gpt-4")
        assert result == "openai.1"

    def test_backend_instance_model_routes_to_concrete_instance_no_load_balancing(
        self, mock_config_provider
    ) -> None:
        """Req 1.3: backend-instance:model selects concrete instance without load balancing.

        When backend_type contains a dot (e.g. openai.1 from 'openai.1:gpt-4'),
        the routing service returns that instance directly and never round-robins
        to other instances (e.g. openai.2).
        """
        service = BackendRoutingService(mock_config_provider, RoutingConfig())
        for _ in range(10):
            result = service.resolve_backend_instance("openai.1", "gpt-4")
            assert result == "openai.1", (
                "backend-instance selector must always return the same instance, "
                "never load-balance to openai.2"
            )

    def test_generic_routing_round_robin(self, mock_config_provider):
        service = BackendRoutingService(mock_config_provider, RoutingConfig())

        # Should alternate between openai.1 and openai.2
        results = set()
        for _ in range(10):
            res = service.resolve_backend_instance("openai", "gpt-4")
            results.add(res)

        assert "openai.1" in results
        assert "openai.2" in results
        assert len(results) == 2

    def test_model_routing_discovery(self, mock_config_provider):
        service = BackendRoutingService(mock_config_provider, RoutingConfig())

        # gpt-4 is on openai.1 and openai.2
        results_gpt4 = set()
        for _ in range(10):
            res = service.resolve_backend_instance(None, "gpt-4")
            results_gpt4.add(res)
        assert "openai.1" in results_gpt4
        assert "openai.2" in results_gpt4

        # vendor/model should match plain model entries too
        results_vendor_gpt4 = set()
        for _ in range(10):
            res = service.resolve_backend_instance(None, "openai/gpt-4")
            results_vendor_gpt4.add(res)
        assert "openai.1" in results_vendor_gpt4
        assert "openai.2" in results_vendor_gpt4

        # claude-3 is only on anthropic.1
        res_claude = service.resolve_backend_instance(None, "claude-3")
        assert res_claude == "anthropic.1"

        res_vendor_claude = service.resolve_backend_instance(None, "anthropic/claude-3")
        assert res_vendor_claude == "anthropic.1"

    def test_policy_disable_backend_ids(self, mock_config_provider):
        config = RoutingConfig(disable_backend_ids=True)
        service = BackendRoutingService(mock_config_provider, config)

        # Explicit ID should fail
        with pytest.raises(RoutingError) as exc:
            service.resolve_backend_instance("openai.1", "gpt-4")
        assert "explicit backend instance ID" in str(exc.value)
        assert exc.value.details.get("code") == "policy_rejected"

        # Generic name should succeed
        assert service.resolve_backend_instance("openai", "gpt-4") in [
            "openai.1",
            "openai.2",
        ]

        # Model name should succeed
        assert service.resolve_backend_instance(None, "gpt-4") in [
            "openai.1",
            "openai.2",
        ]

    def test_policy_disable_backend_names(self, mock_config_provider):
        config = RoutingConfig(disable_backend_names=True)
        service = BackendRoutingService(mock_config_provider, config)

        # Explicit ID should fail (implied)
        with pytest.raises(RoutingError) as exc:
            service.resolve_backend_instance("openai.1", "gpt-4")
        assert "explicit backend instance ID" in str(exc.value)
        assert exc.value.details.get("code") == "policy_rejected"

        # Generic name should fail
        with pytest.raises(RoutingError) as exc:
            service.resolve_backend_instance("openai", "gpt-4")
        assert "backend name" in str(exc.value)
        assert exc.value.details.get("code") == "policy_rejected"

        # Model name should succeed
        assert service.resolve_backend_instance(None, "gpt-4") in [
            "openai.1",
            "openai.2",
        ]

    def test_policy_disable_model_names(self, mock_config_provider):
        config = RoutingConfig(disable_model_names=True)
        service = BackendRoutingService(mock_config_provider, config)

        # Explicit ID should succeed
        assert service.resolve_backend_instance("openai.1", "gpt-4") == "openai.1"

        # Generic name should succeed
        assert service.resolve_backend_instance("openai", "gpt-4") in [
            "openai.1",
            "openai.2",
        ]

        # Model name should fail
        with pytest.raises(RoutingError) as exc:
            service.resolve_backend_instance(None, "gpt-4")
        assert "model name only" in str(exc.value)
        assert exc.value.details.get("code") == "policy_rejected"

    def test_generic_routing_fallback_if_no_instances(self, mock_config_provider):
        # Scenario where "custom" backend exists in config but has no "custom.1" instances
        # The service should return "custom" as is (legacy behavior compatibility)
        service = BackendRoutingService(mock_config_provider, RoutingConfig())

        # Mock provider returns no instances for "custom"
        # But resolve_generic_backend should fall back to the name itself if no instances found
        res = service.resolve_backend_instance("custom", "model")
        assert res == "custom"

    def test_excluded_backends_are_skipped(self, mock_config_provider):
        service = BackendRoutingService(mock_config_provider, RoutingConfig())

        # Exclude openai.1 and ensure round-robin sticks to openai.2
        excluded = {"openai.1"}
        for _ in range(3):
            res = service.resolve_backend_instance(
                "openai", "gpt-4", excluded_backends=excluded
            )
            assert res == "openai.2"

        # Exclude the only provider for claude-3 -> returns None
        res = service.resolve_backend_instance(
            None, "claude-3", excluded_backends={"anthropic.1"}
        )
        assert res is None

    def test_model_only_unknown_raises_structured_routing_error(
        self, mock_config_provider
    ):
        """Req 3.3: unknown model-only selectors fail before dispatch."""
        service = BackendRoutingService(mock_config_provider, RoutingConfig())

        with pytest.raises(RoutingError) as exc:
            service.resolve_model_only_backend("vendor/unknown-model")

        assert exc.value.details is not None
        assert exc.value.details.get("code") == "unknown_model"
        assert exc.value.details.get("model") == "vendor/unknown-model"

    def test_model_only_cline_free_virtual_route_resolves_to_cline(self) -> None:
        """The synthetic cline-free/free route must be model-only discoverable."""
        snapshot = ModelCapabilityIndex.build_snapshot(
            {"cline": ["cline-free/free", "cline-free/gemini-3.8-flash"]},
            generation=1,
        )
        index = ModelCapabilityIndex(snapshot=snapshot)
        provider = Mock()
        provider.iter_backend_names.return_value = ["cline"]
        provider.get_backend_config.return_value = BackendConfig(connector="cline")
        service = BackendRoutingService(
            provider, RoutingConfig(), capability_index=index
        )

        assert service.resolve_model_only_backend("cline-free/free") == "cline"
        assert service.resolve_model_only_backend("cline:cline-free/free") == "cline"

    def test_model_only_unknown_raises_when_model_catalog_unavailable(
        self, mock_config_provider_without_model_hints
    ) -> None:
        """Req 3.3: unknown model-only selectors fail even without model metadata."""
        service = BackendRoutingService(
            mock_config_provider_without_model_hints,
            RoutingConfig(),
        )

        with pytest.raises(RoutingError) as exc:
            service.resolve_model_only_backend("test-model")

        assert exc.value.details is not None
        assert exc.value.details.get("code") == "unknown_model"
        assert exc.value.details.get("model") == "test-model"

    def test_model_only_unknown_alias_mentions_missing_alias_config(
        self, mock_config_provider_without_model_hints
    ) -> None:
        """Alias-style selectors should hint when model_aliases are not loaded."""
        mock_config_provider_without_model_hints._app_config = Mock(model_aliases=[])
        service = BackendRoutingService(
            mock_config_provider_without_model_hints,
            RoutingConfig(),
        )

        with pytest.raises(RoutingError) as exc:
            service.resolve_model_only_backend("alias:oss-code-medium")

        message = str(exc.value)
        assert "No backend candidates discovered" in message
        assert "no `model_aliases` are loaded" in message
        assert "`--config` file" in message

    def test_model_only_unknown_alias_mentions_unmatched_alias_rule(
        self, mock_config_provider_without_model_hints
    ) -> None:
        """Alias-style selectors should hint when aliases are loaded but no rule matches."""
        mock_config_provider_without_model_hints._app_config = Mock(
            model_aliases=[
                ModelAliasRule(
                    pattern=r"^alias:verifier$",
                    replacement="openai:gpt-4o-mini",
                )
            ]
        )
        service = BackendRoutingService(
            mock_config_provider_without_model_hints,
            RoutingConfig(),
        )

        with pytest.raises(RoutingError) as exc:
            service.resolve_model_only_backend("alias:oss-code-medium")

        message = str(exc.value)
        assert "No backend candidates discovered" in message
        assert "no configured alias matched 'alias:oss-code-medium'" in message

    def test_model_only_unknown_auto_mentions_missing_alias_config(
        self, mock_config_provider_without_model_hints
    ) -> None:
        """Auto-style selectors should share alias hinting semantics."""
        mock_config_provider_without_model_hints._app_config = Mock(model_aliases=[])
        service = BackendRoutingService(
            mock_config_provider_without_model_hints,
            RoutingConfig(),
        )

        with pytest.raises(RoutingError) as exc:
            service.resolve_model_only_backend("auto:reasoning")

        message = str(exc.value)
        assert "The `auto:` selector namespace uses model alias rules" in message
        assert "`--config` file" in message

    def test_model_only_all_candidates_unavailable_raises_temporarily_unavailable(
        self, mock_config_provider
    ) -> None:
        """Req 2.4/6.3: candidate set exists but all are unavailable."""
        service = BackendRoutingService(mock_config_provider, RoutingConfig())

        with pytest.raises(RoutingError) as exc:
            service.resolve_model_only_backend(
                "gpt-4", excluded_backends={"openai.1", "openai.2"}
            )

        assert exc.value.details is not None
        assert exc.value.details.get("code") == "temporarily_unavailable"
        assert sorted(exc.value.details.get("candidates", [])) == [
            "openai.1",
            "openai.2",
        ]

    def test_model_only_filters_candidates_rejected_by_resilience(
        self, mock_config_provider
    ) -> None:
        """Req 4.1/4.5: model-only routing excludes resilience-unavailable pairs."""
        resilience = Mock()

        def _decision(instance_id: str, model: str) -> ResilienceDecision:
            if instance_id == "openai.1":
                return ResilienceDecision(
                    action=ActionType.REJECT,
                    reason="Model unsupported on instance",
                    instance_id=instance_id,
                    model=model,
                )
            return ResilienceDecision(
                action=ActionType.PROCEED,
                instance_id=instance_id,
                model=model,
            )

        resilience.check_availability.side_effect = _decision
        service = BackendRoutingService(
            mock_config_provider,
            RoutingConfig(),
            resilience_coordinator=resilience,
        )

        for _ in range(5):
            assert service.resolve_model_only_backend("gpt-4") == "openai.2"

    def test_model_only_cost_policy_prefers_lower_cost_candidates(
        self, mock_config_provider
    ) -> None:
        """Req 14.2/14.3: cost policy + RR for equivalent score candidates."""
        # Make openai.2 strictly cheaper than openai.1
        mock_config_provider.configs["openai.1"] = mock_config_provider.configs[
            "openai.1"
        ].model_copy(update={"extra": {"routing_cost": 1.2}})
        mock_config_provider.configs["openai.2"] = mock_config_provider.configs[
            "openai.2"
        ].model_copy(update={"extra": {"routing_cost": 0.7}})
        config = RoutingConfig(model_only_preference_policy="cost")
        service = BackendRoutingService(mock_config_provider, config)

        for _ in range(6):
            assert service.resolve_model_only_backend("gpt-4") == "openai.2"

    def test_model_only_cost_policy_round_robins_equal_score_candidates(
        self, mock_config_provider
    ) -> None:
        """Req 14.3: equal-score candidates use deterministic Round Robin."""
        mock_config_provider.configs["openai.1"] = mock_config_provider.configs[
            "openai.1"
        ].model_copy(update={"extra": {"routing_cost": 1.0}})
        mock_config_provider.configs["openai.2"] = mock_config_provider.configs[
            "openai.2"
        ].model_copy(update={"extra": {"routing_cost": 1.0}})
        config = RoutingConfig(model_only_preference_policy="cost")
        service = BackendRoutingService(mock_config_provider, config)

        selections = [service.resolve_model_only_backend("gpt-4") for _ in range(6)]
        assert selections == [
            "openai.1",
            "openai.2",
            "openai.1",
            "openai.2",
            "openai.1",
            "openai.2",
        ]

    def test_model_only_cost_policy_uses_missing_cost_fallback(
        self, mock_config_provider
    ) -> None:
        """Req 14.5: missing metadata falls back deterministically."""
        mock_config_provider.configs["openai.1"] = BackendConfig(
            api_key="k1",
            models=["gpt-4"],
        )
        mock_config_provider.configs["openai.2"] = BackendConfig(
            api_key="k2",
            models=["gpt-4"],
            extra={"routing_cost": 0.3},
        )
        config = RoutingConfig(
            model_only_preference_policy="cost",
            model_only_missing_cost=5.0,
        )
        service = BackendRoutingService(mock_config_provider, config)

        for _ in range(4):
            assert service.resolve_model_only_backend("gpt-4") == "openai.2"

    def test_model_only_priority_policy_prefers_higher_priority(
        self, mock_config_provider
    ) -> None:
        """Req 14.2: priority policy should pick highest ranked backend."""
        mock_config_provider.configs["openai.1"] = mock_config_provider.configs[
            "openai.1"
        ].model_copy(update={"extra": {"routing_priority": 5}})
        mock_config_provider.configs["openai.2"] = mock_config_provider.configs[
            "openai.2"
        ].model_copy(update={"extra": {"routing_priority": 20}})
        config = RoutingConfig(model_only_preference_policy="priority")
        service = BackendRoutingService(mock_config_provider, config)

        for _ in range(6):
            assert service.resolve_model_only_backend("gpt-4") == "openai.2"

    def test_model_override_policy_wins_over_global_policy(
        self, mock_config_provider
    ) -> None:
        """Req 14.7: model override > global default."""
        mock_config_provider.configs["openai.1"] = mock_config_provider.configs[
            "openai.1"
        ].model_copy(update={"extra": {"routing_priority": 1}})
        mock_config_provider.configs["openai.2"] = mock_config_provider.configs[
            "openai.2"
        ].model_copy(update={"extra": {"routing_priority": 10}})
        config = RoutingConfig(
            model_only_preference_policy="round_robin",
            model_only_model_overrides={"gpt-4": "priority"},
        )
        service = BackendRoutingService(mock_config_provider, config)

        for _ in range(6):
            assert service.resolve_model_only_backend("gpt-4") == "openai.2"

    def test_failover_candidates_walk_top_bucket_before_lower_bucket(
        self, mock_config_provider
    ) -> None:
        """Req 14.4: failover order stays in top equivalent set first."""
        mock_config_provider.configs["openai.1"] = mock_config_provider.configs[
            "openai.1"
        ].model_copy(update={"extra": {"routing_cost": 0.5}})
        mock_config_provider.configs["openai.2"] = mock_config_provider.configs[
            "openai.2"
        ].model_copy(update={"extra": {"routing_cost": 0.5}})
        mock_config_provider.configs["anthropic.1"] = mock_config_provider.configs[
            "anthropic.1"
        ].model_copy(
            update={"models": ["claude-3", "gpt-4"], "extra": {"routing_cost": 2.0}}
        )
        config = RoutingConfig(model_only_preference_policy="cost")
        service = BackendRoutingService(mock_config_provider, config)

        alternatives = service.find_alternative_instances("gpt-4", exclude=["openai.1"])

        assert alternatives == ["openai.2", "anthropic.1"]

    def test_constrained_family_does_not_round_robin_across_instances(
        self, mock_config_provider
    ) -> None:
        """Req 12.4: constrained connector families use single proxy instance."""
        mock_config_provider.configs["qwen-oauth.1"] = BackendConfig(
            api_key="k4",
            models=["qwen-plus"],
        )
        mock_config_provider.configs["qwen-oauth.2"] = BackendConfig(
            api_key="k5",
            models=["qwen-plus"],
        )
        service = BackendRoutingService(mock_config_provider, RoutingConfig())

        selected = {
            service.resolve_backend_instance("qwen-oauth", "qwen-plus")
            for _ in range(8)
        }

        assert selected == {"qwen-oauth.1"}


class TestRoutingConfigValidation:
    """Tests for RoutingConfig validation rules."""

    def test_valid_config_all_enabled(self):
        """Default config with all methods enabled should be valid."""
        config = RoutingConfig()
        assert config.disable_backend_ids is False
        assert config.disable_backend_names is False
        assert config.disable_model_names is False

    def test_valid_config_disable_backend_ids_only(self):
        """Disabling only backend IDs is valid."""
        config = RoutingConfig(disable_backend_ids=True)
        assert config.disable_backend_ids is True

    def test_valid_config_disable_backend_names_only(self):
        """Disabling backend names (implies IDs) is valid if model names enabled."""
        config = RoutingConfig(disable_backend_names=True)
        assert config.disable_backend_names is True

    def test_valid_config_disable_model_names_only(self):
        """Disabling model names is valid if backend names enabled."""
        config = RoutingConfig(disable_model_names=True)
        assert config.disable_model_names is True

    def test_valid_config_disable_ids_and_model_names(self):
        """Disabling IDs and model names is valid (backend names still work)."""
        config = RoutingConfig(disable_backend_ids=True, disable_model_names=True)
        assert config.disable_backend_ids is True
        assert config.disable_model_names is True

    def test_invalid_config_disable_backend_names_and_model_names(self):
        """Disabling both backend names and model names is invalid."""
        with pytest.raises(ValidationError) as exc:
            RoutingConfig(disable_backend_names=True, disable_model_names=True)
        assert "cannot disable both backend names and model-only routing" in str(
            exc.value
        )

    def test_invalid_config_all_disabled(self):
        """Disabling all routing methods is invalid."""
        with pytest.raises(ValidationError) as exc:
            RoutingConfig(
                disable_backend_ids=True,
                disable_backend_names=True,
                disable_model_names=True,
            )
        assert "cannot disable both backend names and model-only routing" in str(
            exc.value
        )

    def test_model_eligibility_diagnostics_exposes_policy_and_tie_sets(
        self, mock_config_provider
    ) -> None:
        mock_config_provider.configs["openai.1"] = mock_config_provider.configs[
            "openai.1"
        ].model_copy(update={"extra": {"routing_cost": 1}})
        mock_config_provider.configs["openai.2"] = mock_config_provider.configs[
            "openai.2"
        ].model_copy(update={"extra": {"routing_cost": 1}})
        mock_config_provider.configs["anthropic.1"] = mock_config_provider.configs[
            "anthropic.1"
        ].model_copy(update={"extra": {"routing_cost": 5}})

        service = BackendRoutingService(
            mock_config_provider,
            RoutingConfig(model_only_preference_policy="cost"),
        )

        diagnostics = service.build_model_eligibility_diagnostics(
            model_limit=20,
            instances_per_model_limit=20,
        )

        gpt4_entry = next(
            item
            for item in diagnostics["model_eligibility"]
            if item["model"] == "gpt-4"
        )
        assert diagnostics["default_preference_policy"] == "cost"
        assert gpt4_entry["applied_preference_policy"] == "cost"
        assert gpt4_entry["equivalent_score_tie_sets"] == [["openai.1", "openai.2"]]

    def test_model_eligibility_diagnostics_applies_deterministic_truncation(
        self, mock_config_provider
    ) -> None:
        service = BackendRoutingService(mock_config_provider, RoutingConfig())

        diagnostics = service.build_model_eligibility_diagnostics(
            model_limit=3,
            instances_per_model_limit=1,
        )

        truncation = diagnostics["truncation"]
        assert truncation["model_limit"] == 3
        assert truncation["instances_per_model_limit"] == 1
        assert truncation["models_truncated"] is False
        assert truncation["models_omitted"] == 0

        assert [item["model"] for item in diagnostics["model_eligibility"]] == [
            "claude-3",
            "gpt-3.5",
            "gpt-4",
        ]
        gpt4_entry = diagnostics["model_eligibility"][-1]
        assert gpt4_entry["instances_truncated"] is True
        assert gpt4_entry["instances_omitted"] == 1
