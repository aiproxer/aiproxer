"""Property tests for ConfigurationApplicator.

**Feature: cli-god-object-refactoring, Task 5: ConfigurationApplicator (TDD)**

Property 1: Argument Parsing Round-Trip Consistency
*For any* valid combination of CLI arguments, parsing with ArgumentParserBuilder
and applying with ConfigurationApplicator SHALL produce an AppConfig equivalent
to the original apply_cli_args function.

**Validates: Requirements 1.1, 1.2, 7.1**

Property 2: Parameter Source Recording Completeness
*For any* CLI argument that modifies AppConfig, the ParameterResolution SHALL
contain an entry recording the parameter path, value, and CLI flag origin.

**Validates: Requirements 1.3**

Requirements:
- 1.1: ArgumentParser is constructed by a dedicated ArgumentParserBuilder class
- 1.2: CLI module delegates to ConfigurationApplicator for applying arguments
- 1.3: ConfigurationApplicator records parameter sources via ParameterResolution
- 7.1: Backward compatibility with existing apply_cli_args behavior
- 9.3: Property-based tests for correctness properties
"""

from __future__ import annotations

import argparse
from typing import Any
from unittest.mock import MagicMock, patch

from hypothesis import given, settings
from hypothesis import strategies as st
from src.core.config.parameter_resolution import ParameterSource

# Strategy for generating valid port numbers
st_port = st.integers(min_value=1024, max_value=65535)

# Strategy for generating valid hostnames
st_host = st.sampled_from(["127.0.0.1", "0.0.0.0", "localhost", "192.168.1.1"])

# Strategy for generating valid log levels
st_log_level = st.sampled_from(["DEBUG", "INFO", "WARNING", "ERROR"])

# Strategy for generating valid backend names
st_backend = st.sampled_from(
    ["openai", "gemini", "openrouter", "anthropic", "gemini-oauth-plan"]
)


def create_mock_cfg() -> MagicMock:
    """Create a mock AppConfig for testing."""
    mock_cfg = MagicMock()
    mock_cfg.model_dump.return_value = {}
    mock_cfg.logging = MagicMock(log_file="./logs/test.log")
    mock_cfg.command_prefix = "/proxy"
    mock_cfg.model_copy.return_value = mock_cfg
    return mock_cfg


class TestArgumentParsingRoundTripConsistency:
    """Property 1: Argument Parsing Round-Trip Consistency.

    **Feature: cli-god-object-refactoring, Property 1**

    Validates: Requirements 1.1, 1.2, 7.1
    """

    @given(
        host=st_host,
        port=st_port,
    )
    @settings(max_examples=50, deadline=30000)
    def test_host_port_round_trip(self, host: str, port: int) -> None:
        """Test that host and port arguments round-trip correctly."""
        from src.core.cli_support.applicators.server_applicator import ServerApplicator
        from src.core.cli_support.configuration_applicator import (
            ConfigurationApplicator,
        )

        applicator = ConfigurationApplicator(domain_applicators=[ServerApplicator()])

        # Create args with host and port
        args = argparse.Namespace(
            config_file=None,
            log_file=None,
            host=host,
            port=port,
            anthropic_port=None,
            timeout=None,
            command_prefix=None,
            force_context_window=None,
            enable_activity_tracking=None,
            request_dedup_window=None,
            disable_request_dedup=None,
            thinking_budget=None,
        )

        with patch("src.core.config.app_config.load_config") as mock_load_config:
            mock_cfg = MagicMock()
            # Start with different values
            mock_cfg.model_dump.return_value = {"host": "0.0.0.0", "port": 8080}
            mock_cfg.logging = MagicMock(log_file="./logs/test.log")
            mock_cfg.command_prefix = "/proxy"
            mock_load_config.return_value = mock_cfg

            with patch("src.core.config.app_config.AppConfig") as mock_app_config:
                captured_data: list[dict[str, Any]] = []

                def capture_validate(data: dict[str, Any]) -> MagicMock:
                    captured_data.append(data.copy())
                    result_cfg = MagicMock()
                    result_cfg.command_prefix = "/proxy"
                    result_cfg.model_copy.return_value = result_cfg
                    return result_cfg

                mock_app_config.model_validate.side_effect = capture_validate

                applicator.apply(args)

        # Property: The final config should have the CLI-provided values
        assert len(captured_data) == 1
        assert captured_data[0]["host"] == host
        assert captured_data[0]["port"] == port

    @given(log_level=st_log_level)
    @settings(max_examples=30, deadline=30000)
    def test_log_level_round_trip(self, log_level: str) -> None:
        """Test that log level argument round-trips correctly."""
        from src.core.cli_support.applicators.logging_applicator import (
            LoggingApplicator,
        )
        from src.core.cli_support.configuration_applicator import (
            ConfigurationApplicator,
        )

        applicator = ConfigurationApplicator(domain_applicators=[LoggingApplicator()])

        args = argparse.Namespace(
            config_file=None,
            log_file=None,
            log_level=log_level,
            log_use_colors=None,
            capture_file=None,
            capture_max_bytes=None,
            capture_truncate_bytes=None,
            capture_max_files=None,
            capture_rotate_interval_seconds=None,
            capture_total_max_bytes=None,
            cbor_capture_dir=None,
            cbor_capture_session_id=None,
        )

        with patch("src.core.config.app_config.load_config") as mock_load_config:
            mock_cfg = MagicMock()
            mock_cfg.model_dump.return_value = {"logging": {"level": "INFO"}}
            mock_cfg.logging = MagicMock(log_file="./logs/test.log")
            mock_cfg.command_prefix = "/proxy"
            mock_load_config.return_value = mock_cfg

            with patch("src.core.config.app_config.AppConfig") as mock_app_config:
                captured_data: list[dict[str, Any]] = []

                def capture_validate(data: dict[str, Any]) -> MagicMock:
                    captured_data.append(data.copy())
                    result_cfg = MagicMock()
                    result_cfg.command_prefix = "/proxy"
                    result_cfg.model_copy.return_value = result_cfg
                    return result_cfg

                mock_app_config.model_validate.side_effect = capture_validate

                applicator.apply(args)

        # Property: The final config should have the CLI-provided log level
        assert len(captured_data) == 1
        assert "logging" in captured_data[0]
        # The LoggingApplicator stores the level as a LogLevel enum value
        from src.core.config.app_config import LogLevel

        assert captured_data[0]["logging"]["level"] == LogLevel[log_level]

    @given(backend=st_backend)
    @settings(max_examples=30, deadline=30000)
    def test_backend_round_trip(self, backend: str) -> None:
        """Test that default_backend argument round-trips correctly."""
        from src.core.cli_support.applicators.backend_applicator import (
            BackendApplicator,
        )
        from src.core.cli_support.configuration_applicator import (
            ConfigurationApplicator,
        )

        applicator = ConfigurationApplicator(domain_applicators=[BackendApplicator()])

        args = argparse.Namespace(
            config_file=None,
            log_file=None,
            default_backend=backend,
            static_route=None,
            disable_gemini_oauth_fallback=False,
            disable_hybrid_backend=False,
            hybrid_backend_repeat_messages=False,
            reasoning_injection_probability=None,
            hybrid_reasoning_model_timeout=None,
            hybrid_reasoning_force_initial_turns=None,
            interleaved_thinking_instructions_file=None,
            openrouter_api_key=None,
            openrouter_api_base_url=None,
            gemini_api_key=None,
            gemini_api_base_url=None,
            zai_api_key=None,
            zai_coding_plan_api_key=None,
            zenmux_api_base_url=None,
            model_aliases=None,
            enable_antigravity_backend_debugging_override=False,
            enable_cline_backend_debugging_override=False,
            enable_gemini_oauth_free_backend_debugging_override=False,
            enable_gemini_oauth_plan_backend_debugging_override=False,
            enable_qwen_oauth_backend_debugging_override=False,
            enable_openai_codex_backend_debugging_override=False,
        )

        with patch("src.core.config.app_config.load_config") as mock_load_config:
            mock_cfg = MagicMock()
            mock_cfg.model_dump.return_value = {
                "backends": {"default_backend": "openai"}
            }
            mock_cfg.logging = MagicMock(log_file="./logs/test.log")
            mock_cfg.command_prefix = "/proxy"
            mock_load_config.return_value = mock_cfg

            with patch("src.core.config.app_config.AppConfig") as mock_app_config:
                captured_data: list[dict[str, Any]] = []

                def capture_validate(data: dict[str, Any]) -> MagicMock:
                    captured_data.append(data.copy())
                    result_cfg = MagicMock()
                    result_cfg.command_prefix = "/proxy"
                    result_cfg.model_copy.return_value = result_cfg
                    return result_cfg

                mock_app_config.model_validate.side_effect = capture_validate

                applicator.apply(args)

        # Property: The final config should have the CLI-provided backend
        assert len(captured_data) == 1
        assert "backends" in captured_data[0]
        assert captured_data[0]["backends"]["default_backend"] == backend


class TestParameterSourceRecordingCompleteness:
    """Property 2: Parameter Source Recording Completeness.

    **Feature: cli-god-object-refactoring, Property 2**

    Validates: Requirements 1.3
    """

    @given(
        host=st_host,
        port=st_port,
    )
    @settings(max_examples=50, deadline=30000)
    def test_records_cli_source_for_applied_arguments(
        self, host: str, port: int
    ) -> None:
        """Test that CLI arguments are recorded with CLI source."""
        from src.core.cli_support.applicators.server_applicator import ServerApplicator
        from src.core.cli_support.configuration_applicator import (
            ConfigurationApplicator,
        )

        applicator = ConfigurationApplicator(domain_applicators=[ServerApplicator()])

        args = argparse.Namespace(
            config_file=None,
            log_file=None,
            host=host,
            port=port,
            anthropic_port=None,
            timeout=None,
            command_prefix=None,
            force_context_window=None,
            enable_activity_tracking=None,
            request_dedup_window=None,
            disable_request_dedup=None,
            thinking_budget=None,
        )

        with patch("src.core.config.app_config.load_config") as mock_load_config:
            mock_cfg = create_mock_cfg()
            mock_load_config.return_value = mock_cfg

            with patch("src.core.config.app_config.AppConfig") as mock_app_config:
                mock_app_config.model_validate.return_value = mock_cfg

                _, resolution = applicator.apply(args, return_resolution=True)

        # Property: Each CLI argument should have a CLI source record
        cli_entries = resolution.latest_by_source(ParameterSource.CLI)

        # Host and port should be recorded
        assert resolution.is_set("host"), "host should be recorded in resolution"
        assert resolution.is_set("port"), "port should be recorded in resolution"

        # Their source should be CLI
        assert (
            "host" in cli_entries
        ), f"host should have CLI source, got sources for: {list(cli_entries.keys())}"
        assert (
            "port" in cli_entries
        ), f"port should have CLI source, got sources for: {list(cli_entries.keys())}"

    @given(log_level=st_log_level)
    @settings(max_examples=30, deadline=30000)
    def test_records_origin_flag_for_cli_arguments(self, log_level: str) -> None:
        """Test that CLI arguments record the flag name as origin."""
        from src.core.cli_support.applicators.logging_applicator import (
            LoggingApplicator,
        )
        from src.core.cli_support.configuration_applicator import (
            ConfigurationApplicator,
        )

        applicator = ConfigurationApplicator(domain_applicators=[LoggingApplicator()])

        args = argparse.Namespace(
            config_file=None,
            log_file=None,
            log_level=log_level,
            log_use_colors=None,
            capture_file=None,
            capture_max_bytes=None,
            capture_truncate_bytes=None,
            capture_max_files=None,
            capture_rotate_interval_seconds=None,
            capture_total_max_bytes=None,
            cbor_capture_dir=None,
            cbor_capture_session_id=None,
        )

        with patch("src.core.config.app_config.load_config") as mock_load_config:
            mock_cfg = create_mock_cfg()
            mock_load_config.return_value = mock_cfg

            with patch("src.core.config.app_config.AppConfig") as mock_app_config:
                mock_app_config.model_validate.return_value = mock_cfg

                _, resolution = applicator.apply(args, return_resolution=True)

        # Property: CLI arguments should record their origin flag
        cli_entries = resolution.latest_by_source(ParameterSource.CLI)
        assert "logging.level" in cli_entries

        # Check that origin contains the flag information
        level_record = cli_entries["logging.level"]
        assert level_record.origin is not None
        assert "--log-level" in level_record.origin

    @given(
        host=st_host,
        port=st_port,
        backend=st_backend,
    )
    @settings(max_examples=30, deadline=30000)
    def test_multiple_args_all_recorded(
        self, host: str, port: int, backend: str
    ) -> None:
        """Test that multiple CLI arguments are all recorded."""
        from src.core.cli_support.applicators.backend_applicator import (
            BackendApplicator,
        )
        from src.core.cli_support.applicators.server_applicator import ServerApplicator
        from src.core.cli_support.configuration_applicator import (
            ConfigurationApplicator,
        )

        applicator = ConfigurationApplicator(
            domain_applicators=[ServerApplicator(), BackendApplicator()]
        )

        # Combine server and backend args
        args = argparse.Namespace(
            config_file=None,
            log_file=None,
            # Server args
            host=host,
            port=port,
            anthropic_port=None,
            timeout=None,
            command_prefix=None,
            force_context_window=None,
            enable_activity_tracking=None,
            request_dedup_window=None,
            disable_request_dedup=None,
            thinking_budget=None,
            # Backend args
            default_backend=backend,
            static_route=None,
            disable_gemini_oauth_fallback=False,
            disable_hybrid_backend=False,
            hybrid_backend_repeat_messages=False,
            reasoning_injection_probability=None,
            hybrid_reasoning_model_timeout=None,
            hybrid_reasoning_force_initial_turns=None,
            interleaved_thinking_instructions_file=None,
            openrouter_api_key=None,
            openrouter_api_base_url=None,
            gemini_api_key=None,
            gemini_api_base_url=None,
            zai_api_key=None,
            zai_coding_plan_api_key=None,
            zenmux_api_base_url=None,
            model_aliases=None,
            enable_antigravity_backend_debugging_override=False,
            enable_cline_backend_debugging_override=False,
            enable_gemini_oauth_free_backend_debugging_override=False,
            enable_gemini_oauth_plan_backend_debugging_override=False,
            enable_qwen_oauth_backend_debugging_override=False,
            enable_openai_codex_backend_debugging_override=False,
        )

        with patch("src.core.config.app_config.load_config") as mock_load_config:
            mock_cfg = create_mock_cfg()
            mock_load_config.return_value = mock_cfg

            with patch("src.core.config.app_config.AppConfig") as mock_app_config:
                mock_app_config.model_validate.return_value = mock_cfg

                _, resolution = applicator.apply(args, return_resolution=True)

        # Property: All CLI arguments should be recorded
        cli_entries = resolution.latest_by_source(ParameterSource.CLI)

        assert "host" in cli_entries
        assert "port" in cli_entries
        assert "backends.default_backend" in cli_entries


class TestConfigurationApplicatorIdempotency:
    """Property tests for idempotency of configuration application."""

    @given(host=st_host, port=st_port)
    @settings(max_examples=20, deadline=30000)
    def test_applying_same_args_twice_produces_same_result(
        self, host: str, port: int
    ) -> None:
        """Test that applying the same args twice produces equivalent configs."""
        from src.core.cli_support.applicators.server_applicator import ServerApplicator
        from src.core.cli_support.configuration_applicator import (
            ConfigurationApplicator,
        )

        args = argparse.Namespace(
            config_file=None,
            log_file=None,
            host=host,
            port=port,
            anthropic_port=None,
            timeout=None,
            command_prefix=None,
            force_context_window=None,
            enable_activity_tracking=None,
            request_dedup_window=None,
            disable_request_dedup=None,
            thinking_budget=None,
        )

        captured_data_1: list[dict[str, Any]] = []
        captured_data_2: list[dict[str, Any]] = []

        for captured_data in [captured_data_1, captured_data_2]:
            applicator = ConfigurationApplicator(
                domain_applicators=[ServerApplicator()]
            )

            with patch("src.core.config.app_config.load_config") as mock_load_config:
                mock_cfg = MagicMock()
                mock_cfg.model_dump.return_value = {"host": "0.0.0.0", "port": 8080}
                mock_cfg.logging = MagicMock(log_file="./logs/test.log")
                mock_cfg.command_prefix = "/proxy"
                mock_load_config.return_value = mock_cfg

                with patch("src.core.config.app_config.AppConfig") as mock_app_config:

                    def capture_validate(
                        data: dict[str, Any],
                        captured_data: list[dict[str, Any]] = captured_data,
                    ) -> MagicMock:
                        captured_data.append(data.copy())
                        result_cfg = MagicMock()
                        result_cfg.command_prefix = "/proxy"
                        result_cfg.model_copy.return_value = result_cfg
                        return result_cfg

                    mock_app_config.model_validate.side_effect = capture_validate

                    applicator.apply(args)

        # Property: Both applications should produce the same config data
        assert captured_data_1[0] == captured_data_2[0]
