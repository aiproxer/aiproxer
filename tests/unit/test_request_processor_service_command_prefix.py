from types import SimpleNamespace

# Tests updated for refactored RequestProcessor architecture
from unittest.mock import AsyncMock, MagicMock

import pytest
from src.core.domain.chat import ChatMessage, ChatRequest
from src.core.domain.processed_result import ProcessedResult
from src.core.domain.request_context import RequestContext
from src.core.domain.responses import ResponseEnvelope
from src.core.domain.session import Session
from src.core.services.application_state_service import ApplicationStateService
from src.core.services.request_processor_service import RequestProcessor


@pytest.mark.asyncio
async def test_request_processor_uses_app_state_command_prefix(monkeypatch) -> None:
    app_state = ApplicationStateService()
    app_state.set_command_prefix("$/")
    app_state.set_setting(
        "app_config",
        SimpleNamespace(
            auth=SimpleNamespace(redact_api_keys_in_prompts=True),
            command_prefix="!/",
        ),
    )

    class DummyCommandProcessor:
        async def process_messages(self, messages, session_id, context):
            return ProcessedResult(
                modified_messages=messages,
                command_executed=False,
                command_results=[],
            )

    class DummySessionManager:
        async def resolve_session_id(self, context):
            return "session-123"

        async def get_session(self, session_id):
            return Session(session_id=session_id)

        async def update_session_agent(self, session, agent):
            return session

        async def record_command_in_session(self, request, session_id):
            return None

        async def update_session_history(
            self, request_data, backend_request, backend_response, session_id
        ):
            return None

        async def apply_openai_codex_history_compaction_gate(
            self, session, resolved_backend
        ):
            return session

    class DummyBackendRequestManager:
        async def prepare_backend_request(
            self, request_data, command_result, **_kwargs
        ):
            return request_data

        async def process_backend_request(self, backend_request, session_id, context):
            return ResponseEnvelope(content={"ok": True})

    class DummyResponseManager:
        async def process_command_result(self, command_result, session):
            return ResponseEnvelope(content={"command": True})

    captured_prefix: dict[str, str] = {}

    async def _echo_process(request, _context):
        return request

    # Store transform_pipeline reference for accessing command_prefix
    transform_pipeline_ref = [None]

    def fake_redaction(*, api_keys):
        # Get command prefix from transform pipeline for testing
        # This will be set after transform_pipeline is created
        if transform_pipeline_ref[0] is not None:
            # Create a dummy session to get command prefix
            dummy_session = Session(session_id="test")
            command_prefix = transform_pipeline_ref[0]._get_command_prefix(
                dummy_session
            )
        else:
            command_prefix = app_state.get_command_prefix()
        captured_prefix["value"] = command_prefix
        middleware = MagicMock()
        middleware.process = AsyncMock(side_effect=_echo_process)
        return middleware

    monkeypatch.setattr(
        "src.core.services.redaction_middleware.RedactionMiddleware",
        fake_redaction,
    )
    monkeypatch.setattr(
        "src.core.common.logging_utils.discover_api_keys_from_config_and_env",
        lambda cfg: [],
    )
    monkeypatch.setattr(
        "src.core.config.edit_precision_temperatures.load_edit_precision_temperatures_config",
        dict,
    )

    class DummyEditPrecision:
        async def process(self, request, context):
            return request

    monkeypatch.setattr(
        "src.core.services.edit_precision_middleware.EditPrecisionTuningMiddleware",
        lambda *args, **kwargs: DummyEditPrecision(),
    )

    from src.core.interfaces.request_processor_internal import (
        IBackendExecutor,
        IBackendPreparer,
        ICommandHandler,
        IRequestSideEffects,
        ISessionEnricher,
    )

    # Create mocks for new required dependencies
    session_enricher = AsyncMock(spec=ISessionEnricher)
    session_enricher.enrich.return_value = (
        Session(session_id="session-123"),
        ChatRequest(
            model="gpt-test", messages=[ChatMessage(role="user", content="Hello")]
        ),
    )

    request_side_effects = AsyncMock(spec=IRequestSideEffects)
    request_side_effects.apply.return_value = ChatRequest(
        model="gpt-test", messages=[ChatMessage(role="user", content="Hello")]
    )

    command_handler = AsyncMock(spec=ICommandHandler)
    command_handler.handle.return_value = ProcessedResult(
        modified_messages=[ChatMessage(role="user", content="Hello")],
        command_executed=False,
        command_results=[],
    )

    backend_preparer = AsyncMock(spec=IBackendPreparer)
    backend_preparer.prepare.return_value = ChatRequest(
        model="gpt-test", messages=[ChatMessage(role="user", content="Hello")]
    )

    # Use real transform pipeline to test command prefix
    from src.core.services.request_transform_pipeline import RequestTransformPipeline

    transform_pipeline = RequestTransformPipeline(app_state=app_state)

    backend_executor = AsyncMock(spec=IBackendExecutor)
    backend_executor.execute.return_value = ResponseEnvelope(content={"ok": True})

    processor = RequestProcessor(
        command_processor=DummyCommandProcessor(),
        session_manager=DummySessionManager(),
        backend_request_manager=DummyBackendRequestManager(),
        response_manager=DummyResponseManager(),
        session_enricher=session_enricher,
        request_side_effects=request_side_effects,
        command_handler=command_handler,
        backend_preparer=backend_preparer,
        transform_pipeline=transform_pipeline,
        backend_executor=backend_executor,
        app_state=app_state,
    )

    request = ChatRequest(
        model="gpt-test",
        messages=[ChatMessage(role="user", content="Hello")],
    )
    context = RequestContext(headers={}, cookies={}, state={}, app_state=app_state)

    await processor.process_request(context, request)

    assert captured_prefix.get("value") == "$/"


@pytest.mark.asyncio
async def test_request_processor_prefers_session_command_prefix(monkeypatch) -> None:
    session_override = Session(session_id="session-override")
    session_override.state = session_override.state.with_command_prefix_override("#/")

    app_state = ApplicationStateService()
    app_state.set_command_prefix("!/")
    app_state.set_setting(
        "app_config",
        SimpleNamespace(
            auth=SimpleNamespace(redact_api_keys_in_prompts=True),
            command_prefix="!/",
        ),
    )

    class DummyCommandProcessor:
        async def process_messages(self, messages, session_id, context):
            return ProcessedResult(
                modified_messages=messages,
                command_executed=False,
                command_results=[],
            )

    class DummySessionManager:
        async def resolve_session_id(self, context):
            return session_override.session_id

        async def get_session(self, session_id):
            return session_override

        async def update_session_agent(self, session, agent):
            return session

        async def record_command_in_session(self, request, session_id):
            return None

        async def update_session_history(
            self, request_data, backend_request, backend_response, session_id
        ):
            return None

        async def apply_openai_codex_history_compaction_gate(
            self, session, resolved_backend
        ):
            return session

    class DummyBackendRequestManager:
        async def prepare_backend_request(
            self, request_data, command_result, **_kwargs
        ):
            return request_data

        async def process_backend_request(self, backend_request, session_id, context):
            return ResponseEnvelope(content={"ok": True})

    class DummyResponseManager:
        async def process_command_result(self, command_result, session):
            return ResponseEnvelope(content={"command": True})

    captured_prefix: dict[str, str] = {}

    async def _echo_process(request, _context):
        return request

    # Store transform_pipeline reference for accessing command_prefix
    transform_pipeline_ref = [None]

    def fake_redaction(*, api_keys):
        # Get command prefix from transform pipeline for testing
        # This will be set after transform_pipeline is created
        if transform_pipeline_ref[0] is not None:
            command_prefix = transform_pipeline_ref[0]._get_command_prefix(
                session_override
            )
        else:
            command_prefix = app_state.get_command_prefix()
        captured_prefix["value"] = command_prefix
        middleware = MagicMock()
        middleware.process = AsyncMock(side_effect=_echo_process)
        return middleware

    monkeypatch.setattr(
        "src.core.services.redaction_middleware.RedactionMiddleware",
        fake_redaction,
    )
    monkeypatch.setattr(
        "src.core.common.logging_utils.discover_api_keys_from_config_and_env",
        lambda cfg: [],
    )
    monkeypatch.setattr(
        "src.core.config.edit_precision_temperatures.load_edit_precision_temperatures_config",
        dict,
    )

    class DummyEditPrecision:
        async def process(self, request, context):
            return request

    monkeypatch.setattr(
        "src.core.services.edit_precision_middleware.EditPrecisionTuningMiddleware",
        lambda *args, **kwargs: DummyEditPrecision(),
    )

    from src.core.interfaces.request_processor_internal import (
        IBackendExecutor,
        IBackendPreparer,
        ICommandHandler,
        IRequestSideEffects,
        ISessionEnricher,
    )

    # Create mocks for new required dependencies
    session_enricher = AsyncMock(spec=ISessionEnricher)
    session_enricher.enrich.return_value = (
        session_override,
        ChatRequest(
            model="gpt-test", messages=[ChatMessage(role="user", content="Hello")]
        ),
    )

    request_side_effects = AsyncMock(spec=IRequestSideEffects)
    request_side_effects.apply.return_value = ChatRequest(
        model="gpt-test", messages=[ChatMessage(role="user", content="Hello")]
    )

    command_handler = AsyncMock(spec=ICommandHandler)
    command_handler.handle.return_value = ProcessedResult(
        modified_messages=[ChatMessage(role="user", content="Hello")],
        command_executed=False,
        command_results=[],
    )

    backend_preparer = AsyncMock(spec=IBackendPreparer)
    backend_preparer.prepare.return_value = ChatRequest(
        model="gpt-test", messages=[ChatMessage(role="user", content="Hello")]
    )

    # Use real transform pipeline to test command prefix
    from src.core.services.request_transform_pipeline import RequestTransformPipeline

    transform_pipeline = RequestTransformPipeline(app_state=app_state)
    transform_pipeline_ref[0] = transform_pipeline

    backend_executor = AsyncMock(spec=IBackendExecutor)
    backend_executor.execute.return_value = ResponseEnvelope(content={"ok": True})

    processor = RequestProcessor(
        command_processor=DummyCommandProcessor(),
        session_manager=DummySessionManager(),
        backend_request_manager=DummyBackendRequestManager(),
        response_manager=DummyResponseManager(),
        session_enricher=session_enricher,
        request_side_effects=request_side_effects,
        command_handler=command_handler,
        backend_preparer=backend_preparer,
        transform_pipeline=transform_pipeline,
        backend_executor=backend_executor,
        app_state=app_state,
    )

    request = ChatRequest(
        model="gpt-test",
        messages=[ChatMessage(role="user", content="Hello")],
    )
    context = RequestContext(headers={}, cookies={}, state={}, app_state=app_state)

    await processor.process_request(context, request)

    assert captured_prefix.get("value") == "#/"
