"""Unit tests for OAuth connector detection utilities.

Tests OAuth connector detection using three-layer approach:
1. Naming patterns (-oauth-, -oauth suffix)
2. has_static_credentials property check
3. Explicit known OAuth connector list

Requirements satisfied:
- 6.1: OAuth connector detection by naming patterns and property
- 6.2: Maintain explicit list of known OAuth connectors
"""

from __future__ import annotations

from src.connectors.oauth_detector import (
    KNOWN_OAUTH_CONNECTORS,
    OAUTH_CONNECTOR_PATTERNS,
    is_oauth_connector,
)
from src.core.domain.backend_capability_descriptor import BackendCapabilityDescriptor


class MockConnectorWithProperty:
    """Mock connector class for property-based detection tests."""

    def __init__(self, has_static_creds: bool = False):
        self._has_static_creds = has_static_creds

    @property
    def has_static_credentials(self) -> bool:
        return self._has_static_creds


class TestOAuthConnectorPatterns:
    """Tests for OAuth connector naming pattern constants."""

    def test_oauth_connector_patterns_defined(self) -> None:
        """Test that OAUTH_CONNECTOR_PATTERNS is defined and non-empty."""
        assert OAUTH_CONNECTOR_PATTERNS is not None
        assert len(OAUTH_CONNECTOR_PATTERNS) > 0

    def test_oauth_connector_patterns_includes_underscore_oauth_underscore(
        self,
    ) -> None:
        """Test that patterns include _oauth_ for middle pattern matching (module filename convention)."""
        assert "_oauth_" in OAUTH_CONNECTOR_PATTERNS

    def test_oauth_connector_patterns_includes_underscore_oauth(self) -> None:
        """Test that patterns include _oauth for suffix matching (module filename convention)."""
        assert "_oauth" in OAUTH_CONNECTOR_PATTERNS


class TestKnownOAuthConnectors:
    """Tests for known OAuth connector list."""

    def test_known_oauth_connectors_defined(self) -> None:
        """Test that KNOWN_OAUTH_CONNECTORS is defined and non-empty."""
        assert KNOWN_OAUTH_CONNECTORS is not None
        assert len(KNOWN_OAUTH_CONNECTORS) > 0

    def test_known_oauth_connectors_includes_openai_codex(self) -> None:
        """Test that known connectors include openai-codex (special case)."""
        assert "openai-codex" in KNOWN_OAUTH_CONNECTORS

    def test_known_oauth_connectors_includes_kiro_oauth_auto(self) -> None:
        """Test that known connectors include kiro-oauth-auto."""
        assert "kiro-oauth-auto" in KNOWN_OAUTH_CONNECTORS

    def test_known_oauth_connectors_includes_gemini_cli_acp(self) -> None:
        """Test that known connectors include gemini-cli-acp."""
        assert "gemini-cli-acp" in KNOWN_OAUTH_CONNECTORS
        assert "cursor-cli-acp" in KNOWN_OAUTH_CONNECTORS


class TestIsOAuthConnectorNamingPatterns:
    """Tests for OAuth connector detection by naming patterns."""

    def test_detects_oauth_middle_pattern_gemini_oauth_auto(self) -> None:
        """Test -oauth- pattern: gemini-oauth-auto."""
        assert is_oauth_connector("gemini_oauth_auto") is True

    def test_detects_oauth_middle_pattern_gemini_oauth_plan(self) -> None:
        """Test -oauth- pattern: gemini-oauth-plan."""
        assert is_oauth_connector("gemini_oauth_plan") is True

    def test_detects_oauth_middle_pattern_gemini_oauth_free(self) -> None:
        """Test -oauth- pattern: gemini-oauth-free."""
        assert is_oauth_connector("gemini_oauth_free") is True

    def test_detects_oauth_middle_pattern_kiro_oauth_auto(self) -> None:
        """Test -oauth- pattern: kiro-oauth-auto."""
        assert is_oauth_connector("kiro_oauth_auto") is True

    def test_detects_oauth_suffix_pattern_cursor_oauth(self) -> None:
        """Test -oauth suffix: cursor-oauth (module filename convention)."""
        assert is_oauth_connector("cursor_oauth") is True

    def test_detects_oauth_suffix_pattern_qwen_oauth(self) -> None:
        """Test -oauth suffix: qwen-oauth."""
        assert is_oauth_connector("qwen_oauth") is True

    def test_detects_oauth_suffix_pattern_antigravity_oauth(self) -> None:
        """Test -oauth suffix: antigravity-oauth."""
        assert is_oauth_connector("antigravity_oauth") is True

    def test_non_oauth_connector_openai_returns_false(self) -> None:
        """Test non-OAuth connector: openai."""
        assert is_oauth_connector("openai") is False

    def test_non_oauth_connector_gemini_returns_false(self) -> None:
        """Test non-OAuth connector: gemini."""
        assert is_oauth_connector("gemini") is False

    def test_non_oauth_connector_anthropic_returns_false(self) -> None:
        """Test non-OAuth connector: anthropic."""
        assert is_oauth_connector("anthropic") is False

    def test_non_oauth_connector_minimax_returns_false(self) -> None:
        """Test non-OAuth connector: minimax."""
        assert is_oauth_connector("minimax") is False

    def test_module_name_with_underscores_converted_to_dashes(self) -> None:
        """Test that module names with underscores are handled (module uses underscore, pattern uses dash)."""
        # Module filenames use underscores, but logical names use dashes
        assert is_oauth_connector("gemini_oauth_auto") is True
        assert is_oauth_connector("cursor_oauth") is True


class TestIsOAuthConnectorKnownList:
    """Tests for OAuth connector detection via known list."""

    def test_openai_codex_detected_via_known_list(self) -> None:
        """Test openai-codex is detected (doesn't match naming pattern but in known list)."""
        # openai_codex doesn't match -oauth- or -oauth patterns
        # but should be detected via KNOWN_OAUTH_CONNECTORS
        assert is_oauth_connector("_openai_codex_connector") is True

    def test_opencode_zen_detected_if_in_known_list(self) -> None:
        """Test opencode-zen detection if it's in known list."""
        # This tests the known list fallback mechanism
        if "opencode-zen" in KNOWN_OAUTH_CONNECTORS:
            assert is_oauth_connector("opencode_zen") is True

    def test_gemini_cli_acp_detected_via_known_list(self) -> None:
        """Test gemini-cli-acp is detected via known list."""
        assert is_oauth_connector("gemini_cli_acp") is True

    def test_cursor_cli_acp_detected_via_known_list(self) -> None:
        """Test cursor-cli-acp is detected via known list."""
        assert is_oauth_connector("cursor_cli_acp") is True

    def test_openai_codex_app_server_detected_via_known_list(self) -> None:
        """Test openai-codex-app-server is detected via known list."""
        assert is_oauth_connector("openai_codex_app_server") is True


class TestIsOAuthConnectorPropertyBased:
    """Tests for OAuth connector detection via has_static_credentials property."""

    def test_detects_oauth_when_has_static_credentials_false(self) -> None:
        """Test connector with has_static_credentials=False is detected as OAuth."""
        mock_class = type("MockOAuthConnector", (), {"has_static_credentials": False})
        result = is_oauth_connector("test_connector", connector_class=mock_class)
        assert result is True

    def test_does_not_detect_oauth_when_has_static_credentials_true(self) -> None:
        """Test connector with has_static_credentials=True is NOT detected as OAuth."""
        mock_class = type("MockStaticConnector", (), {"has_static_credentials": True})
        # Module name doesn't match patterns and property says static
        result = is_oauth_connector("test_connector", connector_class=mock_class)
        assert result is False

    def test_property_check_with_instance_property(self) -> None:
        """Test property check works with instance property via mock."""
        connector_instance = MockConnectorWithProperty(has_static_creds=False)
        mock_class = type(connector_instance)
        result = is_oauth_connector("test_connector", connector_class=mock_class)
        assert result is True

    def test_property_check_requires_connector_class(self) -> None:
        """Test property check is skipped if connector_class is None."""
        # If module name doesn't match patterns and no class provided,
        # should return False (unless in known list)
        result = is_oauth_connector("unknown_connector", connector_class=None)
        assert result is False


class TestIsOAuthConnectorEdgeCases:
    """Tests for edge cases in OAuth connector detection."""

    def test_empty_module_name_returns_false(self) -> None:
        """Test empty module name returns False."""
        assert is_oauth_connector("") is False

    def test_none_module_name_returns_false(self) -> None:
        """Test None module name returns False."""
        assert is_oauth_connector(None) is False  # type: ignore[arg-type]

    def test_module_name_only_without_class(self) -> None:
        """Test detection works with module name only (no class)."""
        assert is_oauth_connector("cursor_oauth") is True
        assert is_oauth_connector("openai") is False

    def test_module_name_with_class_both_used(self) -> None:
        """Test detection uses both module name and class if provided."""
        # If module name doesn't match but class property says OAuth
        mock_class = type("MockOAuth", (), {"has_static_credentials": False})
        assert is_oauth_connector("custom_backend", connector_class=mock_class) is True

    def test_module_name_pattern_overrides_property(self) -> None:
        """Test module name pattern match takes precedence."""
        # Even if class says has_static_credentials=True, naming pattern should detect OAuth
        mock_class = type("MockStatic", (), {"has_static_credentials": True})
        result = is_oauth_connector("gemini_oauth_auto", connector_class=mock_class)
        # Naming pattern should match regardless of property
        assert result is True

    def test_private_module_name_handled(self) -> None:
        """Test private module names (starting with _) are handled."""
        # _openai_codex_connector should be detected
        assert is_oauth_connector("_openai_codex_connector") is True

    def test_connector_class_without_property_falls_back_to_patterns(self) -> None:
        """Test that missing has_static_credentials property falls back to patterns."""
        # Class without has_static_credentials property
        mock_class = type("MockNoProperty", (), {})
        result = is_oauth_connector("gemini_oauth_auto", connector_class=mock_class)
        # Should still detect via naming pattern
        assert result is True


class TestIsOAuthConnectorCombinedLogic:
    """Tests for combined detection logic (all three layers)."""

    def test_detection_precedence_known_list_highest(self) -> None:
        """Test known list detection works even without pattern match."""
        # openai_codex doesn't match patterns but is in known list
        assert is_oauth_connector("_openai_codex_connector") is True

    def test_detection_precedence_pattern_second(self) -> None:
        """Test pattern detection works without known list."""
        # Novel OAuth connector not in known list but matches pattern
        assert is_oauth_connector("future_oauth_provider") is True

    def test_detection_precedence_property_third(self) -> None:
        """Test property detection works when patterns and known list don't match."""
        mock_class = type("FutureOAuth", (), {"has_static_credentials": False})
        result = is_oauth_connector("future_provider", connector_class=mock_class)
        assert result is True

    def test_all_detection_methods_agree_on_oauth(self) -> None:
        """Test all three methods agree on OAuth connector."""
        mock_class = type("GeminiOAuth", (), {"has_static_credentials": False})
        # gemini-oauth-auto: matches pattern, in known list, property false
        result = is_oauth_connector("gemini_oauth_auto", connector_class=mock_class)
        assert result is True

    def test_all_detection_methods_agree_on_non_oauth(self) -> None:
        """Test all three methods agree on non-OAuth connector."""
        mock_class = type("OpenAI", (), {"has_static_credentials": True})
        # openai: no pattern match, not in known list, property true
        result = is_oauth_connector("openai", connector_class=mock_class)
        assert result is False


class TestIsOAuthConnectorCapabilityDescriptor:
    """Generic capability flags classify personal OAuth without name lists."""

    def test_openai_chatgpt_plan_name_alone_is_not_oauth(self) -> None:
        assert "openai-chatgpt-plan" not in KNOWN_OAUTH_CONNECTORS
        assert "openai_chatgpt_plan" not in KNOWN_OAUTH_CONNECTORS
        assert is_oauth_connector("openai_chatgpt_plan") is False
        assert is_oauth_connector("openai-chatgpt-plan") is False

    def test_capability_flags_detect_oauth_without_name_heuristics(self) -> None:
        mock_class = type(
            "CapabilityOAuth",
            (),
            {
                "has_static_credentials": True,
                "capability_descriptor": BackendCapabilityDescriptor(
                    is_oauth_based=True,
                    requires_personal_auth=True,
                ),
            },
        )
        assert is_oauth_connector("custom_backend", connector_class=mock_class) is True

    def test_default_capability_flags_do_not_force_oauth(self) -> None:
        mock_class = type(
            "StaticBackend",
            (),
            {
                "has_static_credentials": True,
                "capability_descriptor": BackendCapabilityDescriptor(),
            },
        )
        assert is_oauth_connector("custom_backend", connector_class=mock_class) is False

    def test_chatgpt_plan_connector_class_is_oauth_via_capability(self) -> None:
        from src.connectors.openai_chatgpt_plan import OpenAIChatGPTPlanConnector

        assert is_oauth_connector("openai_chatgpt_plan") is False
        assert (
            is_oauth_connector(
                "openai_chatgpt_plan",
                connector_class=OpenAIChatGPTPlanConnector,
            )
            is True
        )
