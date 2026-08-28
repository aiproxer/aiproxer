from unittest.mock import Mock

import pytest
from src.core.config.app_config import BackendConfig
from src.core.domain.chat import ChatRequest
from src.core.domain.configuration.app_identity_config import AppIdentityConfig
from src.core.domain.request_context import RequestContext
from src.core.interfaces.backend_config_provider_interface import IBackendConfigProvider
from src.core.interfaces.configuration_interface import IConfig
from src.core.interfaces.wire_capture_interface import IWireCapture
from src.core.services.backend_completion_flow.wire_capture_orchestrator import (
    WireCaptureOrchestrator,
)


class TestWireCaptureOrchestrator:
    @pytest.fixture
    def wire_capture(self):
        return Mock(spec=IWireCapture)

    @pytest.fixture
    def config(self):
        # Mock AppConfig structure
        conf = Mock(spec=IConfig)
        conf.backends = {}
        conf.identity = "default_identity"
        return conf

    @pytest.fixture
    def backend_config_service(self):
        return Mock(spec=IBackendConfigProvider)

    @pytest.fixture
    def orchestrator(self, wire_capture, config, backend_config_service):
        return WireCaptureOrchestrator(
            wire_capture=wire_capture,
            config=config,
            backend_config_service=backend_config_service,
        )

    @pytest.mark.asyncio
    async def test_prepare_wire_capture_context_uses_backend_config(
        self, orchestrator, backend_config_service, config
    ):
        # Arrange
        identity = AppIdentityConfig()
        backend_config = BackendConfig(identity=identity)
        backend_config_service.get_backend_config.return_value = backend_config

        # Act
        result_identity = await orchestrator.prepare_wire_capture_context(
            "openai", None
        )

        # Assert
        assert result_identity == identity

    @pytest.mark.asyncio
    async def test_prepare_wire_capture_context_updates_turn_count(
        self, orchestrator, backend_config_service, config
    ):
        # Arrange
        identity = AppIdentityConfig()
        backend_config = BackendConfig(identity=identity)
        backend_config_service.get_backend_config.return_value = backend_config

        session = Mock()
        session.history = [1, 2, 3]  # length 3

        # Act
        result_identity = await orchestrator.prepare_wire_capture_context(
            "openai", session
        )

        # Assert
        assert result_identity.session_turn_count == 3
        assert result_identity.title == identity.title

    @pytest.mark.asyncio
    async def test_capture_wire_outbound_calls_wire_capture(
        self, orchestrator, wire_capture
    ):
        # Arrange
        wire_capture.enabled.return_value = True
        orchestrator.detect_key_name = Mock(return_value="OPENAI_API_KEY")

        request = Mock(spec=ChatRequest)
        context = Mock(spec=RequestContext)
        context.session_id = "sess_123"
        context.extensions = {}

        # Act
        await orchestrator.capture_wire_outbound(
            backend_type="openai",
            effective_model="gpt-4",
            domain_request=request,
            context=context,
        )

        # Assert
        wire_capture.capture_outbound_request.assert_called_once()
        call_args = wire_capture.capture_outbound_request.call_args[1]
        assert call_args["backend"] == "openai"
        assert call_args["model"] == "gpt-4"
        assert call_args["key_name"] == "OPENAI_API_KEY"
        assert call_args["session_id"] == "sess_123"

    @pytest.mark.asyncio
    async def test_capture_wire_outbound_swallows_errors(
        self, orchestrator, wire_capture
    ):
        # Arrange
        wire_capture.enabled.return_value = True
        wire_capture.capture_outbound_request.side_effect = Exception("Boom")

        request = Mock(spec=ChatRequest)
        context = Mock(spec=RequestContext)

        # Act & Assert (Should not raise)
        await orchestrator.capture_wire_outbound(
            backend_type="openai",
            effective_model="gpt-4",
            domain_request=request,
            context=context,
        )

    def test_detect_key_name_fallback(self, orchestrator):
        # Test fallback when no key found
        key = orchestrator.detect_key_name("unknown_backend")
        assert key == "unknown_backend"

    @pytest.mark.asyncio
    async def test_capture_inbound_response_calls_wire_capture(
        self, orchestrator, wire_capture
    ):
        # Arrange
        wire_capture.enabled.return_value = True

        context = Mock(spec=RequestContext)
        context.extensions = {}
        response_content = {"foo": "bar"}

        # Act
        await orchestrator.capture_inbound_response(
            context=context,
            session_id="sess_123",
            backend_type="openai",
            effective_model="gpt-4",
            key_name="OPENAI_API_KEY",
            response_content=response_content,
        )

        # Assert
        wire_capture.capture_inbound_response.assert_called_once()
        call_args = wire_capture.capture_inbound_response.call_args[1]
        assert call_args["backend"] == "openai"
        assert call_args["model"] == "gpt-4"
        assert call_args["response_content"] == response_content

    @pytest.mark.asyncio
    async def test_capture_inbound_response_with_canonical_usage(
        self, orchestrator, wire_capture
    ):
        """Test that canonical_usage is passed through to wire capture."""
        # Arrange
        wire_capture.enabled.return_value = True

        context = Mock(spec=RequestContext)
        context.extensions = {}
        response_content = {"foo": "bar"}

        canonical_usage = {
            "provider_id": "openai",
            "model_id": "gpt-4",
            "prompt_tokens": 10,
            "completion_tokens": 20,
            "total_tokens": 30,
        }

        # Act
        await orchestrator.capture_inbound_response(
            context=context,
            session_id="sess_123",
            backend_type="openai",
            effective_model="gpt-4",
            key_name="OPENAI_API_KEY",
            response_content=response_content,
            canonical_usage=canonical_usage,
        )

        # Assert
        wire_capture.capture_inbound_response.assert_called_once()
        call_args = wire_capture.capture_inbound_response.call_args[1]
        assert call_args["backend"] == "openai"
        assert call_args["model"] == "gpt-4"
        assert call_args["canonical_usage"] == canonical_usage

    @pytest.mark.asyncio
    async def test_wrap_inbound_stream_calls_wire_capture(
        self, orchestrator, wire_capture
    ):
        # Arrange
        wire_capture.enabled.return_value = True
        mock_stream = Mock()  # AsyncIterator
        wire_capture.wrap_inbound_stream.return_value = mock_stream

        context = Mock(spec=RequestContext)

        # Act
        result = orchestrator.wrap_inbound_stream(
            context=context,
            session_id="sess_123",
            backend_type="openai",
            effective_model="gpt-4",
            key_name="OPENAI_API_KEY",
            stream=mock_stream,
        )

        # Assert
        assert result == mock_stream
        wire_capture.wrap_inbound_stream.assert_called_once()
