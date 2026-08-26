from collections.abc import AsyncGenerator, AsyncIterator
from types import SimpleNamespace
from typing import cast
from unittest.mock import AsyncMock, Mock, patch

import pytest
from src.core.domain.b2bua_identity import B2buaIdentity
from src.core.domain.chat import ChatRequest
from src.core.domain.request_context import RequestContext
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.interfaces.planning_phase_manager_interface import IPlanningPhaseManager
from src.core.interfaces.resilience_interface import IResilienceCoordinator
from src.core.interfaces.response_processor_interface import ProcessedResponse
from src.core.interfaces.stream_session_id_resolver_interface import (
    IStreamSessionIdResolver,
)
from src.core.interfaces.usage_tracking_interface import IUsageTrackingService
from src.core.interfaces.usage_tracking_wrapper_interface import IUsageTrackingWrapper
from src.core.services.backend_completion_flow.usage_accounting_orchestrator import (
    UsageAccountingOrchestrator,
)


class TestUsageAccountingOrchestrator:
    @pytest.fixture
    def usage_tracking_service(self):
        return Mock(spec=IUsageTrackingService)

    @pytest.fixture
    def usage_tracking_wrapper(self):
        return Mock(spec=IUsageTrackingWrapper)

    @pytest.fixture
    def stream_session_id_resolver(self):
        return Mock(spec=IStreamSessionIdResolver)

    @pytest.fixture
    def planning_phase_manager(self):
        return Mock(spec=IPlanningPhaseManager)

    @pytest.fixture
    def resilience_coordinator(self):
        return Mock(spec=IResilienceCoordinator)

    @pytest.fixture
    def orchestrator(
        self,
        usage_tracking_service,
        usage_tracking_wrapper,
        stream_session_id_resolver,
        planning_phase_manager,
        resilience_coordinator,
    ):
        return UsageAccountingOrchestrator(
            usage_tracking_service=usage_tracking_service,
            usage_tracking_wrapper=usage_tracking_wrapper,
            stream_session_id_resolver=stream_session_id_resolver,
            planning_phase_manager=planning_phase_manager,
            resilience_coordinator=resilience_coordinator,
        )

    @pytest.mark.asyncio
    async def test_calculate_and_record_usage(
        self, orchestrator, usage_tracking_service
    ):
        # Arrange
        usage_tracking_service.record_request = AsyncMock(
            side_effect=["rec_ctp", "rec_ptb"]
        )

        request = Mock(spec=ChatRequest)
        domain_request = Mock(spec=ChatRequest)

        # We need to patch calculate_outbound_tokens because it's imported inside the method usually
        # But if we move logic, it might be imported at top level or dependency injected?
        # The original code imported it inside. I'll patch it.
        with patch(
            "src.core.services.backend_completion_flow.usage_accounting_orchestrator.calculate_outbound_tokens"
        ) as mock_calc:
            mock_calc.return_value = 100

            # Act
            outbound, ctp, ptb = await orchestrator.calculate_and_record_usage(
                domain_request=domain_request,
                request=request,
                backend_type="openai",
                effective_model="gpt-4",
                session=None,
                session_id_for_backend="sess_1",
            )

            # Assert
            assert outbound == 100
            assert ctp == "rec_ctp"
            assert ptb == "rec_ptb"
            assert usage_tracking_service.record_request.call_count == 2

    @pytest.mark.asyncio
    async def test_calculate_and_record_usage_prefers_a_leg_and_b_seq(
        self, orchestrator, usage_tracking_service
    ):
        usage_tracking_service.record_request = AsyncMock(
            side_effect=["rec_ctp", "rec_ptb"]
        )
        request = Mock(spec=ChatRequest)
        domain_request = Mock(spec=ChatRequest)
        context = RequestContext(
            headers={},
            cookies={},
            state={},
            app_state=Mock(),
            session_id="llm-b2bua-a-2020",
            b2bua_identity=B2buaIdentity(
                a_session_id="llm-b2bua-a-2020",
                b_session_id="llm-b2bua-b-2020-4",
                b_seq=4,
            ),
        )

        with patch(
            "src.core.services.backend_completion_flow.usage_accounting_orchestrator.calculate_outbound_tokens"
        ) as mock_calc:
            mock_calc.return_value = 123

            await orchestrator.calculate_and_record_usage(
                domain_request=domain_request,
                request=request,
                backend_type="openai",
                effective_model="gpt-4",
                session=None,
                session_id_for_backend="llm-b2bua-b-2020-4",
                context=context,
            )

        first_call = usage_tracking_service.record_request.call_args_list[0].kwargs
        second_call = usage_tracking_service.record_request.call_args_list[1].kwargs
        assert first_call["session_id"] == "llm-b2bua-a-2020"
        assert first_call["turn_number"] == 1
        assert second_call["session_id"] == "llm-b2bua-a-2020"
        assert second_call["turn_number"] == 4

    @pytest.mark.asyncio
    async def test_wrap_response_for_usage_non_streaming(
        self, orchestrator, usage_tracking_service
    ):
        # Arrange
        from src.core.domain.usage_summary import UsageSummary

        response = ResponseEnvelope(
            content="foo", usage=UsageSummary.from_dict({"completion_tokens": 50})
        )
        usage_tracking_service.record_response = AsyncMock()

        # Act
        result = await orchestrator.wrap_response_for_usage(
            result=response,
            outbound_tokens=100,
            ctp_record_id="rec_ctp",
            ptb_record_id="rec_ptb",
            start_time=1000.0,
        )

        # Assert
        assert result == response
        assert result.metadata["outbound_tokens"] == 100
        assert usage_tracking_service.record_response.call_count == 2

    @pytest.mark.asyncio
    async def test_wrap_response_for_usage_embeds_b2bua_attempt_metadata(
        self, orchestrator, usage_tracking_service
    ):
        from src.core.domain.usage_summary import UsageSummary

        response = ResponseEnvelope(
            content="foo",
            usage=UsageSummary.from_dict({"completion_tokens": 5}),
        )
        usage_tracking_service.record_response = AsyncMock()
        context = RequestContext(
            headers={},
            cookies={},
            state={},
            app_state=Mock(),
            session_id="llm-b2bua-a-3000",
            b2bua_identity=B2buaIdentity(
                a_session_id="llm-b2bua-a-3000",
                b_session_id="llm-b2bua-b-3000-2",
                b_seq=2,
            ),
        )

        await orchestrator.wrap_response_for_usage(
            result=response,
            outbound_tokens=10,
            ctp_record_id="rec_ctp",
            ptb_record_id="rec_ptb",
            start_time=1000.0,
            context=context,
        )

        assert response.metadata is not None
        assert response.metadata["b2bua"]["a_session_id"] == "llm-b2bua-a-3000"
        assert response.metadata["b2bua"]["b_session_id"] == "llm-b2bua-b-3000-2"
        assert response.metadata["b2bua"]["b_seq"] == 2
        backend_usage = usage_tracking_service.record_response.call_args_list[0].kwargs[
            "backend_reported_usage"
        ]
        assert backend_usage["b2bua"]["b_seq"] == 2

    @pytest.mark.asyncio
    async def test_wrap_response_for_usage_streaming(
        self, orchestrator, usage_tracking_wrapper
    ):
        # Arrange
        stream = Mock()
        response = StreamingResponseEnvelope(content=stream)
        usage_tracking_wrapper.wrap_stream_for_usage.return_value = "wrapped_stream"

        # Act
        result = await orchestrator.wrap_response_for_usage(
            result=response,
            outbound_tokens=100,
            ctp_record_id="rec_ctp",
            ptb_record_id="rec_ptb",
            start_time=1000.0,
        )

        # Assert
        assert result.content == "wrapped_stream"
        assert result.metadata["outbound_tokens"] == 100
        usage_tracking_wrapper.wrap_stream_for_usage.assert_called_once()

    @pytest.mark.asyncio
    async def test_wrap_response_for_usage_marks_gemini_oauth_for_recalculation(
        self, orchestrator, usage_tracking_wrapper
    ):
        stream = Mock()
        response = StreamingResponseEnvelope(content=stream)
        usage_tracking_wrapper.wrap_stream_for_usage.return_value = "wrapped_stream"

        result = await orchestrator.wrap_response_for_usage(
            result=response,
            outbound_tokens=321,
            ctp_record_id="rec_ctp",
            ptb_record_id="rec_ptb",
            start_time=1000.0,
            backend_type="gemini-oauth-auto",
        )

        assert result.metadata["outbound_tokens"] == 321
        assert result.metadata["allow_usage_recalculation"] is True

    @pytest.mark.asyncio
    async def test_wrap_response_for_usage_does_not_mark_openai_for_recalculation(
        self, orchestrator, usage_tracking_wrapper
    ):
        stream = Mock()
        response = StreamingResponseEnvelope(content=stream)
        usage_tracking_wrapper.wrap_stream_for_usage.return_value = "wrapped_stream"

        result = await orchestrator.wrap_response_for_usage(
            result=response,
            outbound_tokens=111,
            ctp_record_id="rec_ctp",
            ptb_record_id="rec_ptb",
            start_time=1000.0,
            backend_type="openai",
        )

        assert result.metadata["outbound_tokens"] == 111
        assert "allow_usage_recalculation" not in result.metadata

    @pytest.mark.asyncio
    async def test_handle_streaming_response_success(
        self,
        orchestrator,
        resilience_coordinator,
        planning_phase_manager,
        stream_session_id_resolver,
    ):
        # Arrange
        from src.core.interfaces.response_processor_interface import ProcessedResponse

        async def _ok_stream():
            # Minimal well-formed chunk to exercise the wrapper.
            yield ProcessedResponse(content=b"data: {}\n\n", metadata={})

        response = StreamingResponseEnvelope(content=_ok_stream())
        stream_session_id_resolver.resolve_stream_session_id.return_value = "sess_1"
        context = None

        # Act
        result = await orchestrator.handle_streaming_response(
            result=response,
            backend_type="openai",
            effective_model="gpt-4",
            context=context,
            request=Mock(),
            session_id_for_backend="sess_1",
        )

        # Assert
        assert isinstance(result, StreamingResponseEnvelope)

        # Success is recorded when the stream completes.
        assert result.content is not None
        async for _ in result.content:
            pass

        resilience_coordinator.record_success.assert_called_with("openai", "gpt-4")

    @pytest.mark.asyncio
    async def test_handle_non_streaming_response_success(
        self, orchestrator, resilience_coordinator, planning_phase_manager
    ):
        # Arrange
        response = ResponseEnvelope(content="foo")
        planning_phase_manager.update_counters = AsyncMock()

        # Act
        result = await orchestrator.handle_non_streaming_response(
            result=response,
            backend_type="openai",
            effective_model="gpt-4",
            session_id_for_backend="sess_1",
        )

        # Assert
        assert result == response
        resilience_coordinator.record_success.assert_called_with("openai", "gpt-4")
        planning_phase_manager.update_counters.assert_called_with("sess_1", response)

    @pytest.mark.asyncio
    async def test_handle_non_streaming_response_prefers_a_leg_session_from_context(
        self, orchestrator, planning_phase_manager
    ):
        response = ResponseEnvelope(content="foo")
        planning_phase_manager.update_counters = AsyncMock()
        orchestrator._resilience = None  # type: ignore[attr-defined]
        context = RequestContext(
            headers={},
            cookies={},
            state={},
            app_state=Mock(),
            session_id="llm-b2bua-a-9999",
            b2bua_identity=B2buaIdentity(
                a_session_id="llm-b2bua-a-9999",
                b_session_id="llm-b2bua-b-9999-3",
                b_seq=3,
            ),
        )

        await orchestrator.handle_non_streaming_response(
            result=response,
            backend_type="openai",
            effective_model="gpt-4",
            session_id_for_backend="llm-b2bua-b-9999-3",
            context=context,
        )

        planning_phase_manager.update_counters.assert_called_with(
            "llm-b2bua-a-9999",
            response,
        )

    @pytest.mark.asyncio
    async def test_handle_streaming_response_canonical_uses_usage_embedded_in_content_dict(
        self,
        usage_tracking_service,
        usage_tracking_wrapper,
        stream_session_id_resolver,
        planning_phase_manager,
        resilience_coordinator,
    ) -> None:
        """Terminal OpenAI-style chunks carry usage on content dict; canonical must see it."""
        from src.core.domain.chat import CanonicalChatRequest
        from src.core.domain.request_context import ProcessingContext
        from src.core.domain.usage_canonical_record import CanonicalUsageRecord
        from src.core.domain.usage_summary import UsageSummary
        from src.core.interfaces.usage_normalization_service_interface import (
            IUsageNormalizationService,
        )

        mock_norm = Mock(spec=IUsageNormalizationService)
        mock_norm.build_canonical_record = AsyncMock(
            return_value=CanonicalUsageRecord()
        )
        orch = UsageAccountingOrchestrator(
            usage_tracking_service=usage_tracking_service,
            usage_tracking_wrapper=usage_tracking_wrapper,
            stream_session_id_resolver=stream_session_id_resolver,
            planning_phase_manager=planning_phase_manager,
            resilience_coordinator=resilience_coordinator,
            usage_normalization_service=mock_norm,
        )

        async def _stream() -> AsyncIterator[ProcessedResponse]:
            yield ProcessedResponse(
                content={
                    "choices": [
                        {
                            "index": 0,
                            "delta": {
                                "tool_calls": [
                                    {
                                        "id": "call_1",
                                        "type": "function",
                                        "function": {
                                            "name": "test_fn",
                                            "arguments": "{}",
                                        },
                                    }
                                ]
                            },
                            "finish_reason": None,
                        }
                    ],
                },
                metadata={},
            )
            yield ProcessedResponse(
                content={
                    "id": "resp-terminal",
                    "choices": [
                        {
                            "index": 0,
                            "delta": {},
                            "finish_reason": "tool_calls",
                        }
                    ],
                    "usage": {
                        "prompt_tokens": 20,
                        "completion_tokens": 5,
                        "total_tokens": 25,
                    },
                },
                metadata={},
            )

        response = StreamingResponseEnvelope(content=_stream())
        stream_session_id_resolver.resolve_stream_session_id.return_value = "sess_1"
        context = RequestContext(
            headers={},
            cookies={},
            state={},
            app_state=SimpleNamespace(app_config=None),
            session_id="sess_1",
            processing_context=ProcessingContext(),
        )

        result = await orch.handle_streaming_response(
            result=response,
            backend_type="openai-codex",
            effective_model="gpt-5-codex",
            context=context,
            request=Mock(spec=CanonicalChatRequest),
            session_id_for_backend="sess_1",
        )

        assert result.content is not None
        async for _ in result.content:
            pass

        mock_norm.build_canonical_record.assert_awaited()
        await_info = mock_norm.build_canonical_record.await_args
        assert await_info is not None
        passed_usage = await_info.kwargs["usage"]
        assert passed_usage is not None
        expected = UsageSummary.from_dict(
            {"prompt_tokens": 20, "completion_tokens": 5, "total_tokens": 25}
        )
        assert passed_usage.prompt_tokens == expected.prompt_tokens
        assert passed_usage.completion_tokens == expected.completion_tokens
        assert passed_usage.total_tokens == expected.total_tokens

    @pytest.mark.asyncio
    async def test_client_disconnect_after_meaningful_output_records_success(
        self,
        orchestrator,
        resilience_coordinator,
        stream_session_id_resolver,
    ) -> None:
        """OpenCode-style disconnect after finished stream must heal the breaker."""
        from src.core.services.streaming.stream_recovery_budget import (
            mark_stream_meaningful_output,
        )

        async def _stream() -> AsyncIterator[ProcessedResponse]:
            yield ProcessedResponse(content=b'data: {"choices":[]}\n\n', metadata={})
            yield ProcessedResponse(content=b"data: [DONE]\n\n", metadata={})

        context = RequestContext(
            headers={},
            cookies={},
            state={},
            app_state=SimpleNamespace(app_config=None),
            session_id="sess_disconnect_ok",
        )
        mark_stream_meaningful_output(context)
        stream_session_id_resolver.resolve_stream_session_id.return_value = (
            "sess_disconnect_ok"
        )

        result = await orchestrator.handle_streaming_response(
            result=StreamingResponseEnvelope(content=_stream()),
            backend_type="openai-codex",
            effective_model="gpt-5.6-sol",
            context=context,
            request=Mock(),
            session_id_for_backend="sess_disconnect_ok",
        )
        assert result.content is not None
        agen = cast(AsyncGenerator[ProcessedResponse, None], result.content)
        first = await agen.__anext__()
        assert first is not None
        await agen.aclose()

        resilience_coordinator.record_success.assert_called_once_with(
            "openai-codex:sess_disconnect_ok",
            "gpt-5.6-sol",
        )
        resilience_coordinator.release_circuit_breaker_probe.assert_not_called()
        resilience_coordinator.record_failure.assert_not_called()

    @pytest.mark.asyncio
    async def test_client_disconnect_without_meaningful_output_releases_probe_only(
        self,
        orchestrator,
        resilience_coordinator,
        stream_session_id_resolver,
    ) -> None:
        async def _stream() -> AsyncIterator[ProcessedResponse]:
            yield ProcessedResponse(content=b"data: {}\n\n", metadata={})

        context = RequestContext(
            headers={},
            cookies={},
            state={},
            app_state=SimpleNamespace(app_config=None),
            session_id="sess_disconnect_empty",
        )
        stream_session_id_resolver.resolve_stream_session_id.return_value = (
            "sess_disconnect_empty"
        )

        result = await orchestrator.handle_streaming_response(
            result=StreamingResponseEnvelope(content=_stream()),
            backend_type="openai-codex",
            effective_model="gpt-5.6-sol",
            context=context,
            request=Mock(),
            session_id_for_backend="sess_disconnect_empty",
        )
        assert result.content is not None
        agen = cast(AsyncGenerator[ProcessedResponse, None], result.content)
        await agen.__anext__()
        await agen.aclose()

        resilience_coordinator.release_circuit_breaker_probe.assert_called_once_with(
            "openai-codex:sess_disconnect_empty"
        )
        resilience_coordinator.record_success.assert_not_called()
        resilience_coordinator.record_failure.assert_not_called()

    @pytest.mark.asyncio
    async def test_resilience_recorded_before_usage_normalization_failure(
        self,
        usage_tracking_service,
        usage_tracking_wrapper,
        stream_session_id_resolver,
        planning_phase_manager,
        resilience_coordinator,
    ) -> None:
        from src.core.domain.request_context import ProcessingContext
        from src.core.interfaces.usage_normalization_service_interface import (
            IUsageNormalizationService,
        )
        from src.core.services.streaming.stream_recovery_budget import (
            mark_stream_meaningful_output,
        )

        mock_norm = Mock(spec=IUsageNormalizationService)
        mock_norm.build_canonical_record = AsyncMock(
            side_effect=RuntimeError("usage normalization boom")
        )
        orch = UsageAccountingOrchestrator(
            usage_tracking_service=usage_tracking_service,
            usage_tracking_wrapper=usage_tracking_wrapper,
            stream_session_id_resolver=stream_session_id_resolver,
            planning_phase_manager=planning_phase_manager,
            resilience_coordinator=resilience_coordinator,
            usage_normalization_service=mock_norm,
        )

        async def _stream() -> AsyncIterator[ProcessedResponse]:
            yield ProcessedResponse(content=b"data: {}\n\n", metadata={})

        context = RequestContext(
            headers={},
            cookies={},
            state={},
            app_state=SimpleNamespace(app_config=None),
            session_id="sess_norm_fail",
            processing_context=ProcessingContext(),
        )
        mark_stream_meaningful_output(context)
        stream_session_id_resolver.resolve_stream_session_id.return_value = (
            "sess_norm_fail"
        )

        result = await orch.handle_streaming_response(
            result=StreamingResponseEnvelope(content=_stream()),
            backend_type="openai-codex",
            effective_model="gpt-5.6-sol",
            context=context,
            request=Mock(),
            session_id_for_backend="sess_norm_fail",
        )
        assert result.content is not None
        agen = cast(AsyncGenerator[ProcessedResponse, None], result.content)
        await agen.__anext__()
        await agen.aclose()

        resilience_coordinator.record_success.assert_called_once_with(
            "openai-codex:sess_norm_fail",
            "gpt-5.6-sol",
        )


class TestRecordStreamingResilienceOutcome:
    def test_disconnect_after_meaningful_output_closes_half_open_circuit(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        import src.core.services.resilience.circuit_breaker_state as cb_module
        from src.core.config.models.misc import CircuitBreakerConfig
        from src.core.domain.usage_canonical_record import UsageCompletionOutcome
        from src.core.services.backend_completion_flow.usage_accounting_orchestrator import (
            _record_streaming_resilience_outcome,
        )
        from src.core.services.provider_error_classifier import ProviderErrorClassifier
        from src.core.services.resilience.circuit_breaker_state import (
            CircuitBreakerStateManager,
        )
        from src.core.services.resilience.coordinator import ResilienceCoordinator
        from src.core.services.resilience.handlers import CircuitBreakerErrorHandler
        from src.core.services.resilience.rate_limit_state import RateLimitStateManager
        from src.core.services.streaming.stream_recovery_budget import (
            mark_stream_meaningful_output,
        )

        class _FakeClock:
            def __init__(self) -> None:
                self._now = 1000.0

            def monotonic(self) -> float:
                return self._now

            def advance(self, seconds: float) -> None:
                self._now += seconds

        clock = _FakeClock()
        monkeypatch.setattr(cb_module.time, "monotonic", clock.monotonic)

        circuit_state = CircuitBreakerStateManager(
            CircuitBreakerConfig(
                failure_threshold=1,
                open_cooldown_seconds=5.0,
                half_open_max_inflight=1,
                half_open_success_threshold=1,
            )
        )
        coordinator = ResilienceCoordinator(
            state_manager=RateLimitStateManager(),
            provider_error_classifier=ProviderErrorClassifier(),
            error_handler_chain=CircuitBreakerErrorHandler(circuit_state),
            circuit_breaker_state=circuit_state,
        )
        instance_id = "openai-codex:sess_half_open"

        coordinator.record_failure(instance_id, "gpt-5.6-sol", TimeoutError("boom"))
        assert (
            coordinator.check_availability(instance_id, "gpt-5.6-sol").should_proceed()
            is False
        )
        clock.advance(5.1)
        assert coordinator.try_acquire_circuit_breaker_probe(instance_id) is True
        assert (
            coordinator.check_availability(instance_id, "gpt-5.6-sol").reason
            == "half_open_probe_inflight"
        )

        context = RequestContext(
            headers={},
            cookies={},
            state={},
            app_state=SimpleNamespace(app_config=None),
            session_id="sess_half_open",
        )
        mark_stream_meaningful_output(context)

        _record_streaming_resilience_outcome(
            coordinator,
            instance_id=instance_id,
            effective_model="gpt-5.6-sol",
            completion_outcome=UsageCompletionOutcome.incomplete,
            stream_error=None,
            error_classification=None,
            context=context,
            backend_type="openai-codex",
        )

        decision = coordinator.check_availability(instance_id, "gpt-5.6-sol")
        assert decision.should_proceed() is True
        assert coordinator.try_acquire_circuit_breaker_probe(instance_id) is True

    def test_cancellation_does_not_trip_circuit_breaker(self) -> None:
        import asyncio

        from src.core.common.exceptions import SessionCancelledError
        from src.core.config.models.misc import CircuitBreakerConfig
        from src.core.domain.usage_canonical_record import UsageCompletionOutcome
        from src.core.services.backend_completion_flow.usage_accounting_orchestrator import (
            _record_streaming_resilience_outcome,
        )
        from src.core.services.provider_error_classifier import ProviderErrorClassifier
        from src.core.services.resilience.circuit_breaker_state import (
            CircuitBreakerStateManager,
        )
        from src.core.services.resilience.coordinator import ResilienceCoordinator
        from src.core.services.resilience.handlers import CircuitBreakerErrorHandler
        from src.core.services.resilience.rate_limit_state import RateLimitStateManager

        circuit_state = CircuitBreakerStateManager(
            CircuitBreakerConfig(
                failure_threshold=1,
                open_cooldown_seconds=5.0,
            )
        )
        coordinator = ResilienceCoordinator(
            state_manager=RateLimitStateManager(),
            provider_error_classifier=ProviderErrorClassifier(),
            error_handler_chain=CircuitBreakerErrorHandler(circuit_state),
            circuit_breaker_state=circuit_state,
        )
        instance_id = "agy-cli-acp.default"

        # Stream cancelled via asyncio.CancelledError
        _record_streaming_resilience_outcome(
            coordinator,
            instance_id=instance_id,
            effective_model="google/gemini-3.7-flash",
            completion_outcome=UsageCompletionOutcome.incomplete,
            stream_error=asyncio.CancelledError(),
            error_classification=None,
            context=None,
            backend_type="agy-cli-acp",
        )

        decision = coordinator.check_availability(
            instance_id, "google/gemini-3.7-flash"
        )
        assert decision.should_proceed() is True

        # Stream cancelled via SessionCancelledError
        _record_streaming_resilience_outcome(
            coordinator,
            instance_id=instance_id,
            effective_model="google/gemini-3.7-flash",
            completion_outcome=UsageCompletionOutcome.incomplete,
            stream_error=SessionCancelledError(message="client disconnect"),
            error_classification=None,
            context=None,
            backend_type="agy-cli-acp",
        )

        decision = coordinator.check_availability(
            instance_id, "google/gemini-3.7-flash"
        )
        assert decision.should_proceed() is True
