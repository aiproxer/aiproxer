"""Tests for auxiliary request router."""

from src.core.domain.chat import ChatMessage, ChatRequest
from src.core.services.auxiliary_request_router import (
    AuxiliaryRequestDetector,
    AuxiliaryRequestRouter,
    AuxiliaryRoutingConfig,
)


class TestAuxiliaryRequestDetector:
    """Tests for AuxiliaryRequestDetector."""

    def test_disabled_config_returns_false(self) -> None:
        """When disabled, always returns False."""
        config = AuxiliaryRoutingConfig(enabled=False, backend="test-backend")
        detector = AuxiliaryRequestDetector(config)

        request = ChatRequest(
            model="test-model",
            messages=[
                ChatMessage(
                    role="user", content="The following is the text to summarize: hello"
                ),
            ],
        )

        assert detector.is_auxiliary_request(request) is False

    def test_no_backend_returns_false(self) -> None:
        """When no backend configured, returns False."""
        config = AuxiliaryRoutingConfig(enabled=True, backend=None)
        detector = AuxiliaryRequestDetector(config)

        request = ChatRequest(
            model="test-model",
            messages=[
                ChatMessage(
                    role="user", content="The following is the text to summarize: hello"
                ),
            ],
        )

        assert detector.is_auxiliary_request(request) is False

    def test_model_only_selector_with_colon_suffix_is_not_treated_as_fqn(self) -> None:
        """vendor/model:variant should not count as explicit backend:model target."""
        config = AuxiliaryRoutingConfig(
            enabled=True,
            backend=None,
            model="openrouter/anthropic/claude-3-haiku:free",
        )
        detector = AuxiliaryRequestDetector(config)

        request = ChatRequest(
            model="test-model",
            messages=[
                ChatMessage(
                    role="user", content="The following is the text to summarize: hello"
                ),
            ],
        )

        assert detector.is_auxiliary_request(request) is False

    def test_detects_summarize_pattern(self) -> None:
        """Detects 'The following is the text to summarize' pattern."""
        config = AuxiliaryRoutingConfig(enabled=True, backend="aux-backend")
        detector = AuxiliaryRequestDetector(config)

        request = ChatRequest(
            model="test-model",
            messages=[
                ChatMessage(role="system", content="You are a helpful assistant."),
                ChatMessage(
                    role="user",
                    content="The following is the text to summarize:\n<text>\nHello world\n</text>",
                ),
            ],
        )

        assert detector.is_auxiliary_request(request) is True

    def test_detects_title_generation_pattern(self) -> None:
        """Detects 'Generate a title' pattern."""
        config = AuxiliaryRoutingConfig(enabled=True, backend="aux-backend")
        detector = AuxiliaryRequestDetector(config)

        request = ChatRequest(
            model="test-model",
            messages=[
                ChatMessage(
                    role="user", content="Generate a short title for this conversation"
                ),
            ],
        )

        assert detector.is_auxiliary_request(request) is True

    def test_detects_title_generation_when_last_user_message_is_topic(self) -> None:
        """Regression: detect auxiliary title requests that end with the topic text."""

        config = AuxiliaryRoutingConfig(enabled=True, backend="aux-backend")
        detector = AuxiliaryRequestDetector(config)

        request = ChatRequest(
            model="test-model",
            messages=[
                ChatMessage(role="system", content="You are a title generator."),
                ChatMessage(
                    role="user", content="Generate a title for this conversation:"
                ),
                ChatMessage(
                    role="user", content="What are the latest commits all about?"
                ),
            ],
        )

        assert detector.is_auxiliary_request(request) is True

    def test_does_not_detect_normal_conversation(self) -> None:
        """Does not detect normal conversation as auxiliary."""
        config = AuxiliaryRoutingConfig(enabled=True, backend="aux-backend")
        detector = AuxiliaryRequestDetector(config)

        request = ChatRequest(
            model="test-model",
            messages=[
                ChatMessage(role="user", content="How can I fix this bug?"),
            ],
        )

        assert detector.is_auxiliary_request(request) is False

    def test_respects_max_message_count(self) -> None:
        """Requests with too many messages are not considered auxiliary."""
        config = AuxiliaryRoutingConfig(
            enabled=True,
            backend="aux-backend",
            max_message_count=2,
        )
        detector = AuxiliaryRequestDetector(config)

        # 4 messages - exceeds max_message_count of 2
        request = ChatRequest(
            model="test-model",
            messages=[
                ChatMessage(role="user", content="Hello"),
                ChatMessage(role="assistant", content="Hi"),
                ChatMessage(role="user", content="How are you?"),
                ChatMessage(
                    role="user",
                    content="The following is the text to summarize: test",
                ),
            ],
        )

        assert detector.is_auxiliary_request(request) is False

    def test_detects_auxiliary_request_even_with_explicit_backend_selector(
        self,
    ) -> None:
        """Auxiliary title requests should still be routed when client sets backend:model."""

        config = AuxiliaryRoutingConfig(enabled=True, backend="aux-backend")
        detector = AuxiliaryRequestDetector(config)

        request = ChatRequest(
            model="gemini-oauth-auto:google/gemini-3-flash-preview",
            messages=[
                ChatMessage(role="system", content="You are a title generator."),
                ChatMessage(
                    role="user", content="Generate a title for this conversation:"
                ),
                ChatMessage(role="user", content="Some topic"),
            ],
        )

        assert detector.is_auxiliary_request(request) is True

    def test_ignores_tool_and_assistant_messages_for_message_count_threshold(
        self,
    ) -> None:
        """Tool-title requests should be allowed when only system/user messages are few."""

        config = AuxiliaryRoutingConfig(
            enabled=True,
            backend="aux-backend",
            max_message_count=3,
        )
        detector = AuxiliaryRequestDetector(config)

        request = ChatRequest(
            model="qwen-oauth:qwen/coder-model",
            messages=[
                ChatMessage(role="system", content="You are a title generator."),
                ChatMessage(
                    role="user", content="Generate a title for this tool execution:"
                ),
                ChatMessage(role="assistant", content="I will inspect git status."),
                ChatMessage(role="tool", content="On branch dev\nmodified: foo.py"),
                ChatMessage(role="user", content="Show working tree status"),
            ],
        )

        assert detector.is_auxiliary_request(request) is True

    def test_still_rejects_when_system_and_user_messages_exceed_threshold(self) -> None:
        """Long user/system conversations should not be treated as auxiliary."""

        config = AuxiliaryRoutingConfig(
            enabled=True,
            backend="aux-backend",
            max_message_count=3,
        )
        detector = AuxiliaryRequestDetector(config)

        request = ChatRequest(
            model="qwen-oauth:qwen/coder-model",
            messages=[
                ChatMessage(role="system", content="You are a title generator."),
                ChatMessage(
                    role="user", content="Generate a title for this conversation:"
                ),
                ChatMessage(role="user", content="Topic one"),
                ChatMessage(role="assistant", content="Interim response"),
                ChatMessage(role="user", content="Topic two"),
                ChatMessage(role="user", content="Topic three"),
            ],
        )

        assert detector.is_auxiliary_request(request) is False

    def test_detects_title_generator_system_prompt_with_topic_only_user_message(
        self,
    ) -> None:
        """OpenCode may put the title intent in system prompt and only the topic in user."""

        config = AuxiliaryRoutingConfig(enabled=True, backend="aux-backend")
        detector = AuxiliaryRequestDetector(config)

        request = ChatRequest(
            model="qwen-oauth:qwen/coder-model",
            messages=[
                ChatMessage(role="system", content="You are a title generator."),
                ChatMessage(role="user", content="Show working tree status"),
            ],
        )

        assert detector.is_auxiliary_request(request) is True


class TestAuxiliaryRequestRouter:
    """Tests for AuxiliaryRequestRouter."""

    def test_enabled_property(self) -> None:
        """enabled property reflects config state."""
        config_disabled = AuxiliaryRoutingConfig(enabled=False)
        router_disabled = AuxiliaryRequestRouter(config_disabled)
        assert router_disabled.enabled is False

        config_enabled_no_backend = AuxiliaryRoutingConfig(enabled=True, backend=None)
        router_no_backend = AuxiliaryRequestRouter(config_enabled_no_backend)
        assert router_no_backend.enabled is False

        config_enabled = AuxiliaryRoutingConfig(enabled=True, backend="test")
        router_enabled = AuxiliaryRequestRouter(config_enabled)
        assert router_enabled.enabled is True

    def test_get_auxiliary_backend(self) -> None:
        """get_auxiliary_backend returns configured backend."""
        config = AuxiliaryRoutingConfig(enabled=True, backend="openrouter")
        router = AuxiliaryRequestRouter(config)

        assert router.get_auxiliary_backend() == "openrouter"

    def test_get_auxiliary_model(self) -> None:
        """get_auxiliary_model returns configured model."""
        config = AuxiliaryRoutingConfig(
            enabled=True,
            backend="openrouter",
            model="google/gemini-flash-1.5",
        )
        router = AuxiliaryRequestRouter(config)

        assert router.get_auxiliary_model() == "google/gemini-flash-1.5"

    def test_model_only_selector_with_colon_suffix_stays_model_only(self) -> None:
        """vendor/model:variant should not be split into backend/model."""
        config = AuxiliaryRoutingConfig(
            enabled=True,
            backend=None,
            model="openrouter/anthropic/claude-3-haiku:free",
        )
        router = AuxiliaryRequestRouter(config)

        assert router.get_auxiliary_backend() == ""
        assert (
            router.get_auxiliary_model() == "openrouter/anthropic/claude-3-haiku:free"
        )

    def test_stats_tracking(self) -> None:
        """Router tracks request statistics."""
        config = AuxiliaryRoutingConfig(enabled=True, backend="aux")
        router = AuxiliaryRequestRouter(config)

        # Normal request
        normal_request = ChatRequest(
            model="test",
            messages=[ChatMessage(role="user", content="Hello")],
        )
        router.should_route_to_auxiliary(normal_request)

        # Auxiliary request
        aux_request = ChatRequest(
            model="test",
            messages=[
                ChatMessage(
                    role="user",
                    content="The following is the text to summarize: test",
                ),
            ],
        )
        router.should_route_to_auxiliary(aux_request)

        stats = router.get_stats()
        assert stats["total_request_count"] == 2
        assert stats["auxiliary_request_count"] == 1
        assert stats["auxiliary_percentage"] == 50.0
