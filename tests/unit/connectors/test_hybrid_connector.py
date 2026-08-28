"""Unit tests for hybrid connector core functionality."""

import contextlib
import json
from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock, Mock, patch

import pytest
from src.connectors.contracts import ConnectorChatCompletionsRequest
from src.connectors.hybrid import HybridConnector, ReasoningPhaseResult
from src.core.common.exceptions import (
    BackendError,
    ConfigurationError,
    ServiceResolutionError,
)
from src.core.config.app_config import AppConfig
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.interfaces.configuration_interface import IAppIdentityConfig
from src.core.interfaces.response_processor_interface import ProcessedResponse


@pytest.fixture
def app_config():
    """Create a basic app config for testing."""
    config = AppConfig()
    return config.model_copy(
        update={
            "backends": config.backends.model_copy(
                update={"disable_hybrid_backend": False}
            )
        }
    )


@pytest.fixture
def hybrid_connector(app_config):
    """Create a hybrid connector instance for testing."""
    connector = HybridConnector(
        client=Mock(),
        config=app_config,
        translation_service=Mock(),
        backend_registry=Mock(),
    )
    return connector


class TestHybridModelSpecificationParsing:
    """Test hybrid model specification parsing (Task 8.1)."""

    def test_valid_format_basic(self, hybrid_connector):
        """Test valid format: hybrid:[backend:model,backend:model]."""
        model_spec = "hybrid:[openai:gpt-4,openai:gpt-3.5-turbo]"

        spec = hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert spec.reasoning_backend == "openai"
        assert spec.reasoning_model == "gpt-4"
        assert spec.reasoning_params == {}
        assert spec.execution_backend == "openai"
        assert spec.execution_model == "gpt-3.5-turbo"
        assert spec.execution_params == {}

    def test_valid_format_without_hybrid_prefix(self, hybrid_connector):
        """Test valid format without 'hybrid:' prefix."""
        model_spec = "[openai:gpt-4,anthropic:claude-3]"

        spec = hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert spec.reasoning_backend == "openai"
        assert spec.reasoning_model == "gpt-4"
        assert spec.reasoning_params == {}
        assert spec.execution_backend == "anthropic"
        assert spec.execution_model == "claude-3"
        assert spec.execution_params == {}

    def test_valid_example_minimax_qwen(self, hybrid_connector):
        """Test valid example: hybrid:[minimax:MiniMax-M2,qwen-oauth:qwen3-coder-plus]."""
        model_spec = "hybrid:[minimax:MiniMax-M2,qwen-oauth:qwen3-coder-plus]"

        spec = hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert spec.reasoning_backend == "minimax"
        assert spec.reasoning_model == "MiniMax-M2"
        assert spec.reasoning_params == {}
        assert spec.execution_backend == "qwen-oauth"
        assert spec.execution_model == "qwen3-coder-plus"
        assert spec.execution_params == {}

    def test_valid_format_with_whitespace(self, hybrid_connector):
        """Test valid format with whitespace around components."""
        model_spec = "hybrid:[ openai : gpt-4 , anthropic : claude-3 ]"

        spec = hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert spec.reasoning_backend == "openai"
        assert spec.reasoning_model == "gpt-4"
        assert spec.reasoning_params == {}
        assert spec.execution_backend == "anthropic"
        assert spec.execution_model == "claude-3"
        assert spec.execution_params == {}

    def test_invalid_format_missing_brackets(self, hybrid_connector):
        """Test invalid format: missing brackets."""
        model_spec = "hybrid:openai:gpt-4,anthropic:claude-3"

        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert "Invalid hybrid model format" in str(exc_info.value)
        assert (
            "Expected: hybrid:[reasoning-backend:reasoning-model,execution-backend:execution-model]"
            in str(exc_info.value)
        )
        assert (
            "Example: hybrid:[minimax:MiniMax-M2,qwen-oauth:qwen3-coder-plus]"
            in str(exc_info.value)
        )

    def test_invalid_format_missing_opening_bracket(self, hybrid_connector):
        """Test invalid format: missing opening bracket."""
        model_spec = "hybrid:openai:gpt-4,anthropic:claude-3]"

        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert "Invalid hybrid model format" in str(exc_info.value)

    def test_invalid_format_missing_closing_bracket(self, hybrid_connector):
        """Test invalid format: missing closing bracket."""
        model_spec = "hybrid:[openai:gpt-4,anthropic:claude-3"

        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert "Invalid hybrid model format" in str(exc_info.value)

    def test_invalid_format_missing_comma(self, hybrid_connector):
        """Test invalid format: missing comma separator."""
        model_spec = "hybrid:[openai:gpt-4 anthropic:claude-3]"

        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(model_spec)

        # Should fail because it expects exactly 2 parts
        assert "Invalid" in str(exc_info.value)

    def test_invalid_format_extra_commas(self, hybrid_connector):
        """Test invalid format: extra commas."""
        model_spec = "hybrid:[openai:gpt-4,anthropic:claude-3,extra:model]"

        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert "Expected exactly 2 models separated by comma, got 3" in str(
            exc_info.value
        )

    def test_incomplete_spec_missing_reasoning_model(self, hybrid_connector):
        """Test incomplete spec: missing reasoning model."""
        model_spec = "hybrid:[openai:,anthropic:claude-3]"

        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert "Incomplete reasoning model specification" in str(exc_info.value)
        assert "Both backend and model must be non-empty" in str(exc_info.value)

    def test_incomplete_spec_missing_reasoning_backend(self, hybrid_connector):
        """Test incomplete spec: missing reasoning backend."""
        model_spec = "hybrid:[:gpt-4,anthropic:claude-3]"

        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert "Incomplete reasoning model specification" in str(exc_info.value)

    def test_incomplete_spec_missing_execution_model(self, hybrid_connector):
        """Test incomplete spec: missing execution model."""
        model_spec = "hybrid:[openai:gpt-4,anthropic:]"

        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert "Incomplete execution model specification" in str(exc_info.value)

    def test_incomplete_spec_missing_execution_backend(self, hybrid_connector):
        """Test incomplete spec: missing execution backend."""
        model_spec = "hybrid:[openai:gpt-4,:claude-3]"

        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert "Incomplete execution model specification" in str(exc_info.value)

    def test_incomplete_spec_missing_colon_in_reasoning(self, hybrid_connector):
        """Test incomplete spec: missing colon in reasoning part."""
        model_spec = "hybrid:[openai-gpt-4,anthropic:claude-3]"

        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(model_spec)

        message = str(exc_info.value)
        assert "Invalid hybrid model format" in message
        assert "Expected exactly 2 models" in message

    def test_incomplete_spec_missing_colon_in_execution(self, hybrid_connector):
        """Test incomplete spec: missing colon in execution part."""
        model_spec = "hybrid:[openai:gpt-4,anthropic-claude-3]"

        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert "Incomplete execution model specification" in str(exc_info.value)

    def test_edge_case_empty_string(self, hybrid_connector):
        """Test edge case: empty string."""
        model_spec = ""

        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert "Invalid hybrid model format" in str(exc_info.value)

    def test_edge_case_only_brackets(self, hybrid_connector):
        """Test edge case: only brackets."""
        model_spec = "hybrid:[]"

        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(model_spec)

        # Should fail when trying to split by comma
        assert "Invalid" in str(exc_info.value)

    def test_edge_case_special_characters_in_model_name(self, hybrid_connector):
        """Test edge case: special characters in model name."""
        model_spec = (
            "hybrid:[openai:gpt-4-turbo-preview,anthropic:claude-3-opus-20240229]"
        )

        spec = hybrid_connector._parse_hybrid_model_spec(model_spec)

        assert spec.reasoning_backend == "openai"
        assert spec.reasoning_model == "gpt-4-turbo-preview"
        assert spec.reasoning_params == {}
        assert spec.execution_backend == "anthropic"
        assert spec.execution_model == "claude-3-opus-20240229"
        assert spec.execution_params == {}

    def test_error_message_includes_format_example(self, hybrid_connector):
        """Test that error messages include format examples."""
        model_spec = "invalid"

        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(model_spec)

        error_message = str(exc_info.value)
        assert (
            "Example: hybrid:[minimax:MiniMax-M2,qwen-oauth:qwen3-coder-plus]"
            in error_message
        )


class TestHybridInitializationWiring:
    """Test runtime dependency wiring performed during initialize()."""

    @pytest.mark.asyncio
    async def test_initialize_propagates_default_registry_into_phase_executor(
        self, app_config
    ):
        """When backend registry is resolved lazily, phase executor must be updated."""
        from src.core.services.backend_registry import (
            backend_registry as global_registry,
        )

        connector = HybridConnector(
            client=Mock(),
            config=app_config,
            translation_service=Mock(),
            backend_registry=None,
        )

        phase_executor = connector._orchestrator.phase_executor
        assert getattr(phase_executor, "backend_registry", None) is None

        await connector.initialize()

        assert connector._backend_registry is global_registry
        assert getattr(phase_executor, "backend_registry", None) is global_registry


class TestAdaptiveMessageAugmentation:
    """Test adaptive message augmentation (Task 8.2)."""

    def test_system_message_injection_for_openai(self, hybrid_connector):
        """Test system message injection for OpenAI (supports system messages)."""
        messages = [{"role": "user", "content": "Hello"}]
        reasoning_output = "This is my reasoning"

        augmented = hybrid_connector._augment_messages(
            messages, reasoning_output, "openai"
        )

        # Should have system message at the beginning
        assert len(augmented) == 2
        assert augmented[0]["role"] == "system"
        assert "Consider this reasoning" in augmented[0]["content"]
        assert "<thinking>" in augmented[0]["content"]
        assert reasoning_output in augmented[0]["content"]
        assert "</thinking>" in augmented[0]["content"]

        # Original user message should be preserved
        assert augmented[1]["role"] == "user"
        assert augmented[1]["content"] == "Hello"

    def test_system_message_injection_for_anthropic(self, hybrid_connector):
        """Test system message injection for Anthropic (supports system messages)."""
        messages = [{"role": "user", "content": "Hello"}]
        reasoning_output = "This is my reasoning"

        augmented = hybrid_connector._augment_messages(
            messages, reasoning_output, "anthropic"
        )

        # Should have system message at the beginning
        assert len(augmented) == 2
        assert augmented[0]["role"] == "system"
        assert reasoning_output in augmented[0]["content"]

    def test_user_message_prefix_injection_for_gemini(self, hybrid_connector):
        """Test user message prefix injection for Gemini (no system message support)."""
        messages = [{"role": "user", "content": "Hello"}]
        reasoning_output = "This is my reasoning"

        augmented = hybrid_connector._augment_messages(
            messages, reasoning_output, "gemini"
        )

        # Should still have 1 message (user message modified)
        assert len(augmented) == 1
        assert augmented[0]["role"] == "user"

        # Reasoning should be prepended to user message
        assert "<thinking>" in augmented[0]["content"]
        assert reasoning_output in augmented[0]["content"]
        assert "</thinking>" in augmented[0]["content"]
        assert "Hello" in augmented[0]["content"]

        # Reasoning should come before original content
        content = augmented[0]["content"]
        reasoning_index = content.index(reasoning_output)
        hello_index = content.index("Hello")
        assert reasoning_index < hello_index

    def test_model_capability_detection_openai(self, hybrid_connector):
        """Test model capability detection for OpenAI."""
        assert hybrid_connector._supports_system_messages("openai") is True

    def test_model_capability_detection_anthropic(self, hybrid_connector):
        """Test model capability detection for Anthropic."""
        assert hybrid_connector._supports_system_messages("anthropic") is True

    def test_model_capability_detection_gemini(self, hybrid_connector):
        """Test model capability detection for Gemini."""
        assert hybrid_connector._supports_system_messages("gemini") is False

    def test_model_capability_detection_unknown_backend(self, hybrid_connector):
        """Test model capability detection for unknown backend (defaults to True)."""
        assert hybrid_connector._supports_system_messages("unknown-backend") is True

    def test_reasoning_format_adaptation_thinking_tags(self, hybrid_connector):
        """Test reasoning format adaptation: thinking tags for OpenAI."""
        reasoning_output = "My reasoning"
        formatted = hybrid_connector._format_reasoning_for_model(
            reasoning_output, "openai"
        )

        assert formatted.startswith("<thinking>")
        assert formatted.endswith("</thinking>")
        assert reasoning_output in formatted

    def test_reasoning_format_adaptation_think_tags_deepseek(self, hybrid_connector):
        """Test reasoning format adaptation: think tags for DeepSeek."""
        reasoning_output = "My reasoning"
        formatted = hybrid_connector._format_reasoning_for_model(
            reasoning_output, "deepseek"
        )

        assert formatted.startswith("<think>")
        assert formatted.endswith("</think>")
        assert reasoning_output in formatted

    def test_reasoning_format_adaptation_reasoning_tags_default(self, hybrid_connector):
        """Test reasoning format adaptation: reasoning tags for unknown backend."""
        reasoning_output = "My reasoning"
        formatted = hybrid_connector._format_reasoning_for_model(
            reasoning_output, "unknown"
        )

        assert formatted.startswith("<reasoning>")
        assert formatted.endswith("</reasoning>")
        assert reasoning_output in formatted

    def test_handling_existing_system_message_augment(self, hybrid_connector):
        """Test handling of existing system messages (augment existing)."""
        messages = [
            {"role": "system", "content": "You are a helpful assistant"},
            {"role": "user", "content": "Hello"},
        ]
        reasoning_output = "This is my reasoning"

        augmented = hybrid_connector._augment_messages(
            messages, reasoning_output, "openai"
        )

        # Should still have 2 messages
        assert len(augmented) == 2

        # System message should be augmented, not replaced
        assert augmented[0]["role"] == "system"
        assert "You are a helpful assistant" in augmented[0]["content"]
        assert "Consider this reasoning" in augmented[0]["content"]
        assert reasoning_output in augmented[0]["content"]

    def test_handling_existing_system_message_create_new(self, hybrid_connector):
        """Test creating new system message when none exists."""
        messages = [{"role": "user", "content": "Hello"}]
        reasoning_output = "This is my reasoning"

        augmented = hybrid_connector._augment_messages(
            messages, reasoning_output, "openai"
        )

        # Should have new system message
        assert len(augmented) == 2
        assert augmented[0]["role"] == "system"

    def test_empty_message_list_handling(self, hybrid_connector):
        """Test handling of empty message list."""
        messages = []
        reasoning_output = "This is my reasoning"

        # Should handle gracefully and return empty list
        augmented = hybrid_connector._augment_messages(
            messages, reasoning_output, "openai"
        )

        assert augmented == []

    def test_messages_with_no_user_messages(self, hybrid_connector):
        """Test messages with no user messages (only system/assistant)."""
        messages = [
            {"role": "system", "content": "System message"},
            {"role": "assistant", "content": "Assistant message"},
        ]
        reasoning_output = "This is my reasoning"

        # For system message support: should augment system message
        augmented = hybrid_connector._augment_messages(
            messages, reasoning_output, "openai"
        )

        assert len(augmented) == 2
        assert augmented[0]["role"] == "system"
        assert reasoning_output in augmented[0]["content"]

    def test_messages_with_no_user_messages_gemini(self, hybrid_connector):
        """Test messages with no user messages for Gemini (no system support)."""
        messages = [{"role": "assistant", "content": "Assistant message"}]
        reasoning_output = "This is my reasoning"

        # For no system message support: should try to inject to user message
        # but if no user message exists, should return original messages
        augmented = hybrid_connector._augment_messages(
            messages, reasoning_output, "gemini"
        )

        # Should return messages unchanged since no user message to inject into
        assert len(augmented) == 1
        assert augmented[0]["role"] == "assistant"

    def test_message_deep_copy_no_mutation(self, hybrid_connector):
        """Test that original messages are not mutated."""
        messages = [{"role": "user", "content": "Hello"}]
        original_content = messages[0]["content"]
        reasoning_output = "This is my reasoning"

        augmented = hybrid_connector._augment_messages(
            messages, reasoning_output, "openai"
        )

        # Original messages should not be modified
        assert messages[0]["content"] == original_content
        assert len(messages) == 1

        # Augmented messages should contain the reasoning
        assert len(augmented) > len(messages)

    def test_repeat_reasoning_as_artificial_message(self, app_config):
        """Test that reasoning is repeated as an artificial message when enabled."""
        # Enable the feature in the config
        app_config = app_config.model_copy(
            update={
                "backends": app_config.backends.model_copy(
                    update={"hybrid_backend_repeat_messages": True}
                )
            }
        )

        hybrid_connector = HybridConnector(
            client=Mock(),
            config=app_config,
            translation_service=Mock(),
            backend_registry=Mock(),
        )

        messages = [{"role": "user", "content": "Hello"}]
        reasoning_output = "This is my reasoning"

        augmented = hybrid_connector._augment_messages(
            messages, reasoning_output, "openai"
        )

        # Should have system message, user message, and artificial assistant message
        assert len(augmented) == 3
        assert augmented[0]["role"] == "system"
        assert "Consider this reasoning" in augmented[0]["content"]
        assert reasoning_output in augmented[0]["content"]

        assert augmented[1]["role"] == "user"
        assert augmented[1]["content"] == "Hello"

        assert augmented[2]["role"] == "assistant"
        assert augmented[2]["content"] == ""
        assert augmented[2].get("reasoning_content") == reasoning_output


class TestReasoningExposure:
    """Tests for exposing reasoning output to clients."""

    def test_truncate_after_reasoning_close(self, hybrid_connector):
        raw_output = "<think>Plan</think>Final answer"

        truncated = hybrid_connector._truncate_after_reasoning_close(raw_output)

        assert truncated == "<think>Plan</think>"

    def test_format_reasoning_for_client_wraps_missing_tags(self, hybrid_connector):
        formatted = hybrid_connector._format_reasoning_for_client(
            "Consider alternatives", "minimax"
        )

        assert formatted == "Consider alternatives"

    def test_format_reasoning_for_client_normalizes_gemini_tags(self, hybrid_connector):
        raw = "<reasoning>Outline the approach</reasoning>"
        formatted = hybrid_connector._format_reasoning_for_client(
            raw, "gemini-oauth-plan"
        )

        assert formatted == "Outline the approach"

    def test_format_reasoning_for_client_does_not_double_wrap(self, hybrid_connector):
        raw = "<think>Existing reasoning</think>"
        formatted = hybrid_connector._format_reasoning_for_client(raw, "minimax")

        assert formatted == "Existing reasoning"

    def test_format_reasoning_for_model_avoids_double_wrapping(self, hybrid_connector):
        raw = "<think>Already tagged</think>"
        formatted = hybrid_connector._format_reasoning_for_model(raw, "qwen-oauth")

        assert formatted == "<thinking>Already tagged</thinking>"

    def test_build_reasoning_stream_chunk_contains_metadata(self, hybrid_connector):
        chunk = hybrid_connector._build_reasoning_stream_chunk(
            "<think>Plan</think>", "minimax", "MiniMax-M2"
        )

        assert chunk is not None
        assert chunk.metadata == {
            "hybrid_phase": "reasoning",
            "reasoning_backend": "minimax",
            "reasoning_model": "MiniMax-M2",
        }
        assert isinstance(chunk.content, str)
        assert "MiniMax-M2" in chunk.content

        payload = json.loads(chunk.content.removeprefix("data: ").strip())
        delta = payload["choices"][0]["delta"]
        assert delta["reasoning"] == "<think>Plan</think>"
        assert delta["content"] == ""
        assert delta["reasoning_content"] == "Plan"

    def test_format_reasoning_for_model_converts_reasoning_tags(self, hybrid_connector):
        raw = "<reasoning>Plan steps</reasoning>"
        formatted = hybrid_connector._format_reasoning_for_model(raw, "qwen-oauth")

        assert formatted.startswith("<thinking>")
        assert formatted.endswith("</thinking>")
        assert "Plan steps" in formatted

    def test_format_reasoning_for_model_adds_missing_closing(self, hybrid_connector):
        raw = "<think>Plan without close"
        formatted = hybrid_connector._format_reasoning_for_model(raw, "qwen-oauth")

        assert formatted.startswith("<thinking>")
        assert formatted.endswith("</thinking>")
        assert "Plan without close" in formatted

    def test_build_reasoning_stream_chunk_ignores_empty_reasoning(
        self, hybrid_connector
    ):
        chunk = hybrid_connector._build_reasoning_stream_chunk(
            "   ",
            "minimax",
            "MiniMax-M2",
        )

        assert chunk is None

    @pytest.mark.asyncio
    async def test_prepend_reasoning_chunk_to_stream(self, hybrid_connector):
        async def original_stream():
            yield ProcessedResponse(
                content='data: {"choices": [{"delta": {"content": "Result"}}]}\n\n'
            )

        envelope = StreamingResponseEnvelope(content=original_stream())

        combined = hybrid_connector._prepend_reasoning_chunk_to_stream(
            envelope,
            "<think>Plan</think>",
            "minimax",
            "MiniMax-M2",
        )

        assert combined.content is not None

        chunks: list[ProcessedResponse] = []
        async for chunk in combined.content:
            chunks.append(chunk)
            if len(chunks) >= 2:
                break

        assert chunks
        first_json = json.loads(str(chunks[0].content).removeprefix("data: ").strip())
        first_delta = first_json["choices"][0]["delta"]
        assert first_delta["reasoning"] == "<think>Plan</think>"
        assert first_delta["reasoning_content"] == "Plan"
        assert first_delta["content"] == ""
        assert any("Result" in str(chunk.content) for chunk in chunks[1:])

    def test_prepend_reasoning_to_non_streaming_text(self, hybrid_connector):
        combined = hybrid_connector._prepend_reasoning_to_non_streaming_content(
            "Final response.",
            "<think>Plan</think>",
            "minimax",
            "MiniMax-M2",
        )

        assert combined == "Final response."

    def test_prepend_reasoning_to_non_streaming_choice_message(self, hybrid_connector):
        content = {
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": "Final answer."},
                }
            ]
        }

        updated = hybrid_connector._prepend_reasoning_to_non_streaming_content(
            content,
            "<think>Plan</think>",
            "minimax",
            "MiniMax-M2",
        )

        assert updated["choices"][0]["message"]["content"] == "Final answer."
        assert updated["choices"][0]["message"]["reasoning"] == "<think>Plan</think>"
        assert updated["choices"][0]["message"]["reasoning_content"] == "Plan"

    def test_prepend_reasoning_to_non_streaming_dict_metadata(self, hybrid_connector):
        content = {"content": "Final answer"}

        updated = hybrid_connector._prepend_reasoning_to_non_streaming_content(
            content,
            "<think>Plan</think>",
            "minimax",
            "MiniMax-M2",
        )

        assert updated["content"] == "Final answer"
        assert updated["metadata"]["reasoning"] == "<think>Plan</think>"
        assert updated["metadata"]["reasoning_content"] == "Plan"
        assert updated["metadata"]["reasoning_format"] == "hybrid_injected"


class TestIdentityResolution:
    """Tests for backend identity resolution logic."""

    def test_backend_identity_preferred(self, hybrid_connector):

        backend_identity = MagicMock(spec=IAppIdentityConfig)
        request_identity = MagicMock(spec=IAppIdentityConfig)

        resolved = hybrid_connector._resolve_backend_identity(
            "minimax",
            request_identity,
            SimpleNamespace(identity=backend_identity),
        )

        assert resolved is backend_identity

    def test_request_identity_fallback(self, hybrid_connector):

        request_identity = MagicMock(spec=IAppIdentityConfig)

        resolved = hybrid_connector._resolve_backend_identity(
            "minimax",
            request_identity,
            SimpleNamespace(identity=None),
        )

        assert resolved is request_identity

    def test_app_identity_fallback(self, hybrid_connector):
        app_identity = hybrid_connector.config.identity

        resolved = hybrid_connector._resolve_backend_identity(
            "minimax",
            None,
            None,
        )

        assert resolved is app_identity


class TestReasoningParameterOverrides:
    """Test reasoning parameter overrides (Task 8.3)."""

    def test_reasoning_phase_high_effort_openai(self, hybrid_connector):
        """Test reasoning phase gets high reasoning effort for OpenAI."""
        request_data = {"model": "gpt-4", "temperature": 0.5}

        overridden = hybrid_connector._apply_reasoning_params(
            request_data, "openai", enable_reasoning=True
        )

        assert overridden["reasoning_effort"] == "high"
        # Original data should not be mutated
        assert "reasoning_effort" not in request_data

    def test_reasoning_phase_high_thinking_budget_qwen(self, hybrid_connector):
        """Test reasoning phase gets high thinking budget for Qwen."""
        request_data = {"model": "qwen-model"}

        overridden = hybrid_connector._apply_reasoning_params(
            request_data, "qwen", enable_reasoning=True
        )

        assert overridden["thinking_budget"] == 10000

    def test_execution_phase_low_effort_openai(self, hybrid_connector):
        """Test execution phase gets low reasoning effort for OpenAI."""
        request_data = {"model": "gpt-4"}

        overridden = hybrid_connector._apply_reasoning_params(
            request_data, "openai", enable_reasoning=False
        )

        assert overridden["reasoning_effort"] == "low"

    def test_execution_phase_disabled_thinking_qwen(self, hybrid_connector):
        """Test execution phase gets disabled thinking for Qwen."""
        request_data = {"model": "qwen-model"}

        overridden = hybrid_connector._apply_reasoning_params(
            request_data, "qwen", enable_reasoning=False
        )

        assert overridden["thinking_budget"] == 0

    def test_backend_specific_parameter_names_reasoning_effort(self, hybrid_connector):
        """Test backend-specific parameter names: reasoning_effort."""
        request_data = {}

        # OpenAI uses reasoning_effort
        overridden = hybrid_connector._apply_reasoning_params(
            request_data, "openai", enable_reasoning=True
        )
        assert "reasoning_effort" in overridden

        # DeepSeek also uses reasoning_effort
        overridden = hybrid_connector._apply_reasoning_params(
            request_data, "deepseek", enable_reasoning=True
        )
        assert "reasoning_effort" in overridden

    def test_backend_specific_parameter_names_thinking_budget(self, hybrid_connector):
        """Test backend-specific parameter names: thinking_budget."""
        request_data = {}

        # Qwen uses thinking_budget
        overridden = hybrid_connector._apply_reasoning_params(
            request_data, "qwen", enable_reasoning=True
        )
        assert "thinking_budget" in overridden

        # Qwen-oauth also uses thinking_budget
        overridden = hybrid_connector._apply_reasoning_params(
            request_data, "qwen-oauth", enable_reasoning=True
        )
        assert "thinking_budget" in overridden

    def test_default_parameters_for_unknown_backend(self, hybrid_connector):
        """Test default parameters for unknown backend."""
        request_data = {}

        # Unknown backend should get default parameters
        overridden = hybrid_connector._apply_reasoning_params(
            request_data, "unknown-backend", enable_reasoning=True
        )

        # Default reasoning phase has temperature
        assert "temperature" in overridden

    def test_parameter_override_preserves_other_params(self, hybrid_connector):
        """Test that parameter override preserves other parameters."""
        request_data = {
            "model": "gpt-4",
            "temperature": 0.8,
            "max_tokens": 1000,
            "stream": True,
        }

        overridden = hybrid_connector._apply_reasoning_params(
            request_data, "openai", enable_reasoning=True
        )

        # Should preserve other parameters
        assert overridden["model"] == "gpt-4"
        assert overridden["max_tokens"] == 1000
        assert overridden["stream"] is True

        # Should add reasoning_effort
        assert overridden["reasoning_effort"] == "high"

    @patch("src.connectors.hybrid_backend.compatibility.logger")
    def test_parameter_override_logging(self, mock_logger, hybrid_connector):
        """Test parameter override logging."""
        request_data = {"reasoning_effort": "medium"}

        hybrid_connector._apply_reasoning_params(
            request_data, "openai", enable_reasoning=True
        )

        # Should log the override
        mock_logger.debug.assert_called()
        call_args = str(mock_logger.debug.call_args)
        assert "reasoning_effort" in call_args


class TestErrorHandling:
    """Test error handling (Task 8.4)."""

    def test_disabled_hybrid_backend_error(self, app_config):
        """Test disabled hybrid backend error."""
        # Configure backend as disabled
        app_config = app_config.model_copy(
            update={
                "backends": app_config.backends.model_copy(
                    update={"disable_hybrid_backend": True}
                )
            }
        )

        connector = HybridConnector(
            client=Mock(),
            config=app_config,
            translation_service=Mock(),
            backend_registry=Mock(),
        )

        # Attempt to call chat_completions
        with pytest.raises(ConfigurationError) as exc_info:
            # Use asyncio to run the async method
            import asyncio

            from src.core.domain.chat import CanonicalChatRequest, ChatMessage

            domain = CanonicalChatRequest(
                model="hybrid:[openai:gpt-4,openai:gpt-3.5]",
                messages=[ChatMessage(role="user", content="test")],
            )
            asyncio.run(
                connector.chat_completions(
                    ConnectorChatCompletionsRequest(
                        request=domain,
                        processed_messages=[ChatMessage(role="user", content="test")],
                        effective_model="hybrid:[openai:gpt-4,openai:gpt-3.5]",
                        identity=None,
                        cancellation_token=None,
                        cancellation_coordinator=None,
                        context=None,
                    )
                )
            )

        assert "Hybrid backend is disabled" in str(exc_info.value)

    def test_invalid_model_specification_error(self, hybrid_connector):
        """Test invalid model specification error."""
        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec("invalid-format")

        assert "Invalid hybrid model format" in str(exc_info.value)

    def test_incomplete_model_specification_error(self, hybrid_connector):
        """Test incomplete model specification error."""
        with pytest.raises(ValueError) as exc_info:
            hybrid_connector._parse_hybrid_model_spec(
                "hybrid:[openai:,anthropic:claude]"
            )

        assert "Incomplete" in str(exc_info.value)

    @pytest.mark.asyncio
    async def test_reasoning_phase_api_failure(self, hybrid_connector):
        """Test reasoning phase API failure."""
        # Mock backend service to return a failing completion
        mock_backend_service = AsyncMock()
        mock_backend_service.call_completion = AsyncMock(
            side_effect=Exception("API Error")
        )

        # Patch get_required_service to return our mock service
        with (
            patch(
                "src.core.di.services.get_required_service",
                return_value=mock_backend_service,
            ),
            pytest.raises(BackendError) as exc_info,
        ):
            await hybrid_connector._execute_reasoning_phase(
                messages=[{"role": "user", "content": "test"}],
                reasoning_backend="openai",
                reasoning_model="gpt-4",
                request_data={"model": "gpt-4"},
                identity=None,
            )

        assert "reasoning" in str(exc_info.value).lower()

    @pytest.mark.asyncio
    async def test_execution_phase_api_failure(self, hybrid_connector):
        """Test execution phase API failure."""
        # Mock BackendService to raise an error when call_completion is called
        mock_backend_service = AsyncMock()

        # Make call_completion an async function that raises BackendError with execution context
        async def failing_call_completion(*args, **kwargs):
            raise BackendError(
                "Execution phase failed: API Error", backend_name="openai"
            )

        mock_backend_service.call_completion = failing_call_completion

        # Patch get_required_service at the module where it's imported from
        with (
            patch(
                "src.core.di.services.get_required_service",
                return_value=mock_backend_service,
            ),
            pytest.raises(BackendError) as exc_info,
        ):
            await hybrid_connector._execute_execution_phase(
                request_data={"model": "gpt-4"},
                augmented_messages=[{"role": "user", "content": "test"}],
                execution_backend="openai",
                execution_model="gpt-4",
                identity=None,
            )

        assert "execution" in str(exc_info.value).lower()
        assert "api error" in str(exc_info.value).lower()

    @pytest.mark.asyncio
    async def test_timeout_scenario_reasoning_phase(self, hybrid_connector):
        """Test timeout scenario in reasoning phase."""
        import asyncio

        # Reduce timeout to make test fast (minimum allowed is 1 second).
        # PhaseExecutor keeps the AppConfig reference from construction; keep it in sync.
        cfg = hybrid_connector.config
        new_cfg = cfg.model_copy(
            update={
                "backends": cfg.backends.model_copy(
                    update={"hybrid_reasoning_model_timeout": 1}
                )
            }
        )
        hybrid_connector.config = new_cfg
        hybrid_connector._orchestrator.config = new_cfg
        hybrid_connector._orchestrator.phase_executor.config = new_cfg

        # Mock backend service that takes too long
        mock_backend_service = AsyncMock()

        async def slow_completion(*args, **kwargs):
            await asyncio.sleep(2.0)  # Longer than 1s timeout
            return Mock()

        mock_backend_service.call_completion = slow_completion

        with patch(
            "src.core.di.services.get_required_service",
            return_value=mock_backend_service,
        ):
            # Should NOT raise, but return partial result (graceful degradation)
            result = await hybrid_connector._execute_reasoning_phase(
                messages=[{"role": "user", "content": "test"}],
                reasoning_backend="openai",
                reasoning_model="gpt-4",
                request_data={"model": "gpt-4"},
                identity=None,
            )

        assert result.text == ""
        assert result.complete is False

    def test_backend_not_found_error(self, hybrid_connector):
        """Test backend not found error."""
        with patch(
            "src.core.di.services.get_required_service",
            side_effect=ServiceResolutionError(
                "No service registered for BackendFactory",
                service_name="BackendFactory",
            ),
        ):
            import asyncio

            with pytest.raises(BackendError) as exc_info:
                asyncio.run(
                    hybrid_connector._execute_reasoning_phase(
                        messages=[{"role": "user", "content": "test"}],
                        reasoning_backend="nonexistent",
                        reasoning_model="model",
                        request_data={"model": "model"},
                        identity=None,
                    )
                )

        assert exc_info.value.code == "reasoning_backend_init_failed"
        assert "failed to initialize reasoning backend" in str(exc_info.value).lower()


class TestBackendServiceIntegration:
    """Test integration with BackendService to prevent model format regression."""

    @pytest.mark.asyncio
    async def test_reasoning_phase_passes_correct_model_format_to_backend_service(
        self, app_config
    ):
        """Test that reasoning phase passes 'backend:model' format to backend service.

        This test prevents regression of the bug where only the model name was passed,
        causing backend_service.call_completion to fail parsing the model.

        The bug occurred when:
        1. Hybrid connector parsed 'hybrid:[minimax:MiniMax-M2,zai:glm-4.6]'
        2. It extracted reasoning_backend='minimax', reasoning_model='MiniMax-M2'
        3. But only passed 'MiniMax-M2' to _prepare_backend_request
        4. backend_service.call_completion couldn't determine the backend

        The fix ensures the full 'minimax:MiniMax-M2' format is passed.
        """
        from src.core.domain.chat import CanonicalChatRequest

        # Track what model was passed to _prepare_backend_request
        captured_model = None

        def mock_prepare_backend_request(
            self, request_data, target_model, stream, messages=None
        ):
            nonlocal captured_model
            captured_model = target_model
            # Return a minimal CanonicalChatRequest
            return CanonicalChatRequest(
                model=target_model,
                messages=messages or [],
                stream=stream,
            )

        # Create connector with mocked translation service
        mock_translation_service = Mock()
        hybrid_connector = HybridConnector(
            client=Mock(),
            config=app_config,
            translation_service=mock_translation_service,
            backend_registry=Mock(),
        )

        # Mock the backend service
        mock_backend_service = AsyncMock()
        mock_response = StreamingResponseEnvelope(
            content=AsyncMock(),
            media_type="text/event-stream",
        )
        mock_backend_service.call_completion = AsyncMock(return_value=mock_response)

        # Mock the stream to return some reasoning output
        async def mock_stream():
            yield ProcessedResponse(
                content='data: {"choices":[{"delta":{"content":"<think>reasoning</think>"}}]}\n\n',
                usage=None,
                metadata={},
            )
            yield ProcessedResponse(
                content='data: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\n',
                usage=None,
                metadata={},
            )
            yield ProcessedResponse(
                content="data: [DONE]\n\n",
                usage=None,
                metadata={},
            )

        mock_response.content = mock_stream()

        # Patch both the service and the prepare method
        with (
            patch(
                "src.core.di.services.get_required_service",
                return_value=mock_backend_service,
            ),
            patch.object(
                HybridConnector,
                "_prepare_backend_request",
                mock_prepare_backend_request,
            ),
            contextlib.suppress(Exception),
        ):
            # Execute reasoning phase with a specific backend:model combination
            await hybrid_connector._execute_reasoning_phase(
                messages=[{"role": "user", "content": "test"}],
                reasoning_backend="minimax",
                reasoning_model="MiniMax-M2",
                request_data={"model": "minimax:MiniMax-M2", "messages": []},
                identity=None,
            )
        # The critical assertion: the model passed to _prepare_backend_request
        # should be in 'backend:model' format, not just the model name
        assert (
            captured_model is not None
        ), "_prepare_backend_request should have been called"
        assert captured_model == "minimax:MiniMax-M2", (
            f"Model should be 'minimax:MiniMax-M2' (full backend:model format), "
            f"but got '{captured_model}'. This indicates the bug has been reintroduced. "
            f"The hybrid connector should pass 'backend:model' format to ensure "
            f"backend_service.call_completion can correctly identify the backend."
        )

    @pytest.mark.asyncio
    async def test_reasoning_phase_model_format_with_different_backends(
        self, app_config
    ):
        """Test that various backend:model combinations are correctly formatted.

        This ensures the fix works for different backend types, not just minimax.
        """
        from src.core.domain.chat import CanonicalChatRequest

        test_cases = [
            ("openai", "gpt-4", "openai:gpt-4"),
            ("anthropic", "claude-3-opus", "anthropic:claude-3-opus"),
            ("qwen-oauth", "qwen3-coder-plus", "qwen-oauth:qwen3-coder-plus"),
            ("zai-coding-plan", "glm-4.6", "zai-coding-plan:glm-4.6"),
        ]

        for backend, model, expected_format in test_cases:
            # Track what model was passed
            captured_model = None

            def mock_prepare_backend_request(
                self, request_data, target_model, stream, messages=None
            ):
                nonlocal captured_model
                captured_model = target_model
                return CanonicalChatRequest(
                    model=target_model,
                    messages=messages or [],
                    stream=stream,
                )

            # Create connector
            mock_translation_service = Mock()
            hybrid_connector = HybridConnector(
                client=Mock(),
                config=app_config,
                translation_service=mock_translation_service,
                backend_registry=Mock(),
            )

            # Mock the backend service
            mock_backend_service = AsyncMock()
            mock_response = StreamingResponseEnvelope(
                content=AsyncMock(),
                media_type="text/event-stream",
            )
            mock_backend_service.call_completion = AsyncMock(return_value=mock_response)

            # Mock minimal stream
            async def mock_stream():
                yield ProcessedResponse(
                    content='data: {"choices":[{"delta":{"content":"test"}}]}\n\n',
                    usage=None,
                    metadata={},
                )

            mock_response.content = mock_stream()

            with (
                patch(
                    "src.core.di.services.get_required_service",
                    return_value=mock_backend_service,
                ),
                patch.object(
                    HybridConnector,
                    "_prepare_backend_request",
                    mock_prepare_backend_request,
                ),
                contextlib.suppress(Exception),
            ):
                await hybrid_connector._execute_reasoning_phase(
                    messages=[{"role": "user", "content": "test"}],
                    reasoning_backend=backend,
                    reasoning_model=model,
                    request_data={"model": f"{backend}:{model}", "messages": []},
                    identity=None,
                )

            # Verify the model format
            assert captured_model == expected_format, (
                f"For backend '{backend}' and model '{model}', "
                f"expected format '{expected_format}' but got '{captured_model}'"
            )

    def test_prepare_backend_request_strips_backend_type_from_extra_body(
        self, app_config
    ):
        """Hybrid connector must not leak hybrid backend_type into nested requests."""
        from src.core.domain.chat import CanonicalChatRequest

        def to_domain_request(payload, _backend):
            if isinstance(payload, CanonicalChatRequest):
                return payload
            if isinstance(payload, dict):
                return CanonicalChatRequest(
                    model=payload["model"],
                    messages=payload.get("messages", []),
                    stream=payload.get("stream"),
                    extra_body=payload.get("extra_body"),
                )
            return payload

        translation_service = Mock()
        translation_service.to_domain_request.side_effect = to_domain_request

        connector = HybridConnector(
            client=Mock(),
            config=app_config,
            translation_service=translation_service,
            backend_registry=Mock(),
        )

        request_data = {
            "model": "hybrid:[minimax:MiniMax-M2,qwen-oauth:qwen3-coder-plus]",
            "messages": [{"role": "user", "content": "test"}],
            "stream": False,
            "extra_body": {"session_id": "sess-123", "backend_type": "hybrid"},
        }

        canonical_request = connector._prepare_backend_request(
            request_data=request_data,
            target_model="minimax:MiniMax-M2",
            stream=True,
            messages=[{"role": "user", "content": "test"}],
        )

        assert canonical_request.model == "minimax:MiniMax-M2"
        assert canonical_request.stream is True
        assert canonical_request.extra_body is None or (
            "backend_type" not in canonical_request.extra_body
            and "session_id" not in canonical_request.extra_body
            and "model" not in canonical_request.extra_body
        )

    def test_apply_reasoning_params_drops_hybrid_routing_hints(self, hybrid_connector):
        """_apply_reasoning_params should strip hybrid routing keys from extra_body."""
        request_data = {
            "model": "hybrid:[minimax:MiniMax-M2,qwen-oauth:qwen3-coder-plus]",
            "messages": [],
            "extra_body": {
                "model": "hybrid:[minimax:MiniMax-M2,qwen-oauth:qwen3-coder-plus]",
                "backend_type": "hybrid",
                "keep": "true",
            },
        }

        updated = hybrid_connector._apply_reasoning_params(
            request_data=request_data,
            backend_or_params={"temperature": 0.2},
        )

        assert isinstance(updated, dict)
        assert updated["temperature"] == 0.2
        assert updated["extra_body"] == {"keep": "true", "temperature": 0.2}


class TestHybridToolCallShortCircuit:
    """Tests for scenarios where execution phase is skipped."""

    @pytest.mark.asyncio
    async def test_streaming_skip_execution_on_tool_call(
        self, hybrid_connector
    ) -> None:
        from src.core.domain.chat import CanonicalChatRequest, ChatMessage

        request_payload = CanonicalChatRequest(
            model="hybrid:[minimax:MiniMax-M2,qwen-oauth:qwen3-coder-plus]",
            messages=[ChatMessage(role="user", content="Plan the steps")],
            stream=True,
        )
        processed_messages = [ChatMessage(role="user", content="Plan the steps")]
        tool_calls = [
            {
                "id": "call_123",
                "type": "function",
                "function": {
                    "name": "read_file",
                    "arguments": '{"path": "src/app.py"}',
                },
            }
        ]
        reasoning_result = ReasoningPhaseResult(
            text="   ",
            complete=True,
            tool_calls=tool_calls,
            raw_chunks=[],
            media_type="text/event-stream",
            headers=None,
        )

        with (
            patch.object(
                HybridConnector,
                "_execute_reasoning_phase",
                AsyncMock(return_value=reasoning_result),
            ),
            patch.object(
                HybridConnector,
                "_execute_execution_phase",
                AsyncMock(),
            ) as execution_mock,
        ):
            response = await hybrid_connector.chat_completions(
                ConnectorChatCompletionsRequest(
                    request=request_payload,
                    processed_messages=processed_messages,
                    effective_model=request_payload.model,
                    identity=None,
                    cancellation_token=None,
                    cancellation_coordinator=None,
                    context=None,
                )
            )

        assert isinstance(response, StreamingResponseEnvelope)
        assert response.content is not None
        chunks = [chunk async for chunk in response.content]
        assert chunks, "Expected at least one streamed chunk"
        payload = json.loads(str(chunks[0].content).removeprefix("data: ").strip())
        delta = payload["choices"][0]["delta"]
        assert delta["tool_calls"] == tool_calls
        assert delta["content"] == ""
        execution_mock.assert_not_called()

    @pytest.mark.asyncio
    async def test_non_streaming_skip_execution_on_tool_call(
        self, hybrid_connector
    ) -> None:
        from src.core.domain.chat import CanonicalChatRequest, ChatMessage

        request_payload = CanonicalChatRequest(
            model="hybrid:[minimax:MiniMax-M2,qwen-oauth:qwen3-coder-plus]",
            messages=[ChatMessage(role="user", content="Plan the steps")],
            stream=False,
        )
        processed_messages = [ChatMessage(role="user", content="Plan the steps")]
        tool_calls = [
            {
                "id": "call_456",
                "type": "function",
                "function": {
                    "name": "read_file",
                    "arguments": '{"path": "tests/test_file.py"}',
                },
            }
        ]
        reasoning_result = ReasoningPhaseResult(
            text="",
            complete=True,
            tool_calls=tool_calls,
            raw_chunks=[],
            media_type=None,
            headers=None,
        )

        with (
            patch.object(
                HybridConnector,
                "_execute_reasoning_phase",
                AsyncMock(return_value=reasoning_result),
            ),
            patch.object(
                HybridConnector,
                "_execute_execution_phase",
                AsyncMock(),
            ) as execution_mock,
        ):
            response = await hybrid_connector.chat_completions(
                ConnectorChatCompletionsRequest(
                    request=request_payload,
                    processed_messages=processed_messages,
                    effective_model=request_payload.model,
                    identity=None,
                    cancellation_token=None,
                    cancellation_coordinator=None,
                    context=None,
                )
            )

        assert isinstance(response, ResponseEnvelope)
        assert isinstance(response.content, dict)
        assert "choices" in response.content
        assert isinstance(response.content["choices"], list)
        assert len(response.content["choices"]) > 0
        choice = response.content["choices"][0]
        assert isinstance(choice, dict)
        assert "message" in choice
        assert isinstance(choice["message"], dict)
        assert choice["message"]["tool_calls"] == tool_calls
        assert choice["message"]["content"] == ""
        assert choice["finish_reason"] == "tool_calls"
        execution_mock.assert_not_called()
