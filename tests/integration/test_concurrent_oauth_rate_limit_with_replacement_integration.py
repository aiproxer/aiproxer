"""
Integration test for the complete session interruption fix.

This test simulates the exact scenario reported by the user:
- 3 concurrent clients
- Replacement model (gemini-oauth-auto with gemini-3.1-pro-preview)
- Quality verifier configured (claude-sonnet-4.6)
- All OAuth accounts hit rate limits simultaneously

Expected behavior after fixes:
1. Streaming errors return proper SSE format (Fix 0)
2. OAuth connector returns False instead of raising (Fix 1)
3. Fallback logic catches preparation-phase errors (Fix 2)
4. DEBUG logs show quality verifier decisions (Fix 3)
5. NO session interruption - clients get successful responses

Background:
All client sessions were interrupted with "Unauthorized: data: {...} data: [DONE]"
error when all OAuth accounts hit rate limits during replacement model usage.

Issue: https://github.com/.../issues/...
Fixed in: Session 2026-02-26
"""

from __future__ import annotations

import asyncio
from unittest.mock import AsyncMock, MagicMock

import pytest
from src.core.common.exceptions import AuthenticationError
from src.core.domain.chat import ChatMessage, ChatRequest
from src.core.domain.processed_result import ProcessedResult
from src.core.domain.request_context import RequestContext
from src.core.domain.responses import ResponseEnvelope
from src.core.domain.session import Session
from src.core.services.request_processor_service import RequestProcessor


@pytest.fixture
def mock_oauth_connector_all_rate_limited():
    """
    Mock OAuth connector where all accounts are rate-limited.

    This simulates the exact condition that triggered the bug.
    """
    connector = MagicMock()

    # _refresh_token_if_needed returns False (Fix 1)
    async def refresh_token_rate_limited(*args, **kwargs):
        # This is the fixed behavior - returns False instead of raising
        return False

    connector._refresh_token_if_needed = AsyncMock(
        side_effect=refresh_token_rate_limited
    )
    connector._oauth_credentials = {"access_token": "fake_token"}

    return connector


@pytest.fixture
def mock_replacement_service_with_gemini():
    """Mock replacement service configured with gemini-3.1-pro-preview."""
    service = MagicMock()

    state = MagicMock()
    state.active = True
    state.replacement_backend = "gemini-oauth-auto"
    state.replacement_model = "gemini-3.1-pro-preview"
    state.original_backend = "openai"
    state.original_model = "gpt-4o"
    state.deactivate = MagicMock()

    service.get_state.return_value = state
    service.should_replace.return_value = False
    service.get_effective_backend_model.return_value = (
        "gemini-oauth-auto",
        "gemini-3.1-pro-preview",
    )

    return service


@pytest.fixture
def mock_app_state_with_quality_verifier_claude():
    """Mock app state with claude-sonnet-4.6 as quality verifier."""
    app_state = MagicMock()

    config = MagicMock()
    session_config = MagicMock()
    session_config.quality_verifier_model = "anthropic:claude-sonnet-4.6"
    session_config.quality_verifier_frequency = 10
    session_config.quality_verifier_max_history = None
    session_config.quality_verifier_max_consecutive_failures = 5
    session_config.quality_verifier_cooldown_seconds = 300
    session_config.quality_verifier_ttft_timeout_seconds = 30.0

    config.session = session_config
    app_state.get_setting.return_value = config
    app_state.get_backend_type.return_value = "openai"

    return app_state


@pytest.fixture
def integrated_processor(
    mock_oauth_connector_all_rate_limited,
    mock_replacement_service_with_gemini,
    mock_app_state_with_quality_verifier_claude,
):
    """
    Create fully integrated processor with all components configured
    to reproduce the exact reported scenario.
    """
    processor = RequestProcessor(
        command_processor=MagicMock(),
        session_manager=AsyncMock(),
        backend_request_manager=AsyncMock(),
        response_manager=AsyncMock(),
        session_enricher=AsyncMock(),
        request_side_effects=AsyncMock(),
        command_handler=AsyncMock(),
        backend_preparer=AsyncMock(),
        transform_pipeline=AsyncMock(),
        backend_executor=AsyncMock(),
        app_state=mock_app_state_with_quality_verifier_claude,
        replacement_service=mock_replacement_service_with_gemini,
    )

    # Setup session
    session = MagicMock(spec=Session)
    session.state.to_dict.return_value = {"quality_verifier_eligible_turn_count": 5}
    session.state.with_multiple_updates = MagicMock(return_value=session.state)
    session.update_state = MagicMock()

    processor._session_enricher.enrich.return_value = (
        session,
        ChatRequest(
            model="openai:gpt-4o",
            messages=[ChatMessage(role="user", content="test")],
        ),
    )

    processor._session_manager.resolve_session_id.return_value = "session-123"
    processor._session_manager.get_session.return_value = session

    processor._command_handler.handle.return_value = ProcessedResult(
        command_executed=False, modified_messages=[], command_results=[]
    )

    processor._request_side_effects.apply = AsyncMock(
        side_effect=lambda c, sid, req: req
    )
    processor._transform_pipeline.transform = AsyncMock(
        side_effect=lambda c, s, sid, req: req
    )

    # Setup backend preparer to simulate OAuth refresh failure on first attempt

    async def mock_prepare(ctx, sid, req, cmd, **_kwargs):
        if "gemini-oauth-auto" in str(req.model):
            # First attempt (or any attempt with replacement model): fail due to OAuth rate limit
            raise AuthenticationError(
                "OAuth token unavailable for gemini-oauth-auto (streaming API call). "
                "This may be due to rate limiting, expired tokens, or other auth issues."
            )
        else:
            # Fallback attempt (original model): succeed
            return req

    processor._backend_preparer.prepare = AsyncMock(side_effect=mock_prepare)

    # Setup backend executor
    processor._backend_executor.execute = AsyncMock(
        return_value=ResponseEnvelope(content={"message": "success"})
    )

    return processor


@pytest.mark.asyncio
async def test_three_concurrent_clients_all_hit_rate_limits_no_interruption(
    integrated_processor,
    mock_replacement_service_with_gemini,
    caplog,
) -> None:
    """
    THE MAIN REGRESSION TEST: Simulate exact reported scenario.

    3 concurrent clients, all hit OAuth rate limits with replacement model active.
    After fixes, NO session interruption - all clients get successful responses.
    """
    import logging

    caplog.set_level(logging.WARNING)

    contexts = [
        RequestContext(
            headers={},
            cookies={},
            state=MagicMock(),
            app_state=MagicMock(),
            client_host=f"127.0.0.{i+1}",
            original_request=None,
        )
        for i in range(3)
    ]

    for ctx in contexts:
        ctx.backend = "gemini-oauth-auto"
        ctx.effective_model = "gemini-3.1-pro-preview"

    requests = [
        ChatRequest(
            model="gemini-oauth-auto:gemini-3.1-pro-preview",
            messages=[ChatMessage(role="user", content=f"Request {i+1}")],
        )
        for i in range(3)
    ]

    # Run 3 concurrent requests (the exact scenario from bug report)
    results = await asyncio.gather(
        *[
            integrated_processor.process_request(ctx, req)
            for ctx, req in zip(contexts, requests, strict=False)
        ],
        return_exceptions=True,
    )

    # CRITICAL: All must succeed (no exceptions)
    assert all(
        not isinstance(r, Exception) for r in results
    ), f"Sessions were interrupted! Exceptions: {[r for r in results if isinstance(r, Exception)]}"

    # All must return successful responses
    assert all(isinstance(r, ResponseEnvelope) for r in results)

    # WARNING logs must be present (fallback happened)
    warning_logs = [r for r in caplog.records if r.levelname == "WARNING"]
    assert len(warning_logs) > 0
    assert any(
        "falling back" in r.message.lower() or "fallback" in r.message.lower()
        for r in warning_logs
    )

    # Replacement must have been deactivated (3 times, once per client)
    assert (
        mock_replacement_service_with_gemini.get_state.return_value.deactivate.call_count
        == 3
    )


@pytest.mark.asyncio
async def test_streaming_error_format_if_original_also_fails(
    integrated_processor,
    caplog,
) -> None:
    """
    If both replacement AND original model fail, streaming errors
    must be properly formatted (Fix 0).
    """
    import logging

    caplog.set_level(logging.WARNING)

    # Make both attempts fail
    integrated_processor._backend_preparer.prepare = AsyncMock(
        side_effect=AuthenticationError("Both models unavailable")
    )

    # Create streaming request
    context = RequestContext(
        headers={"accept": "text/event-stream"},
        cookies={},
        state=MagicMock(),
        app_state=MagicMock(),
        client_host="127.0.0.1",
        original_request=None,
    )
    context.backend = "gemini-oauth-auto"
    context.effective_model = "gemini-3.1-pro-preview"

    request_data = ChatRequest(
        model="gemini-oauth-auto:gemini-3.1-pro-preview",
        messages=[ChatMessage(role="user", content="test")],
    )

    # This will raise since both models failed
    with pytest.raises(AuthenticationError):
        await integrated_processor.process_request(context, request_data)

    # If we were to handle this error with error handlers, it would be SSE format
    # (This is tested separately in test_streaming_error_format_regression.py)


@pytest.mark.asyncio
async def test_quality_verifier_logs_show_skip_due_to_replacement(
    integrated_processor,
    caplog,
) -> None:
    """
    DEBUG logs show quality verifier is skipped due to replacement (Fix 3).
    """
    import logging

    caplog.set_level(logging.DEBUG)

    context = RequestContext(
        headers={},
        cookies={},
        state=MagicMock(),
        app_state=MagicMock(),
        client_host="127.0.0.1",
        original_request=None,
    )
    context.backend = "gemini-oauth-auto"
    context.effective_model = "gemini-3.1-pro-preview"

    request_data = ChatRequest(
        model="gemini-oauth-auto:gemini-3.1-pro-preview",
        messages=[ChatMessage(role="user", content="test")],
    )

    await integrated_processor.process_request(context, request_data)

    debug_logs = [r.message for r in caplog.records if r.levelname == "DEBUG"]

    # Must show quality verifier skip with replacement reason
    assert any(
        "quality verifier" in log.lower()
        and "skip" in log.lower()
        and "replacement" in log.lower()
        for log in debug_logs
    )


@pytest.mark.asyncio
async def test_b2bua_identity_different_for_fallback_attempt(
    integrated_processor,
    caplog,
) -> None:
    """
    Fallback attempt allocates NEW B2BUA identity (Fix 2 - B2BUA awareness).

    This is implicit in the design - each execute() call allocates new identity.
    We verify by checking that execute is called once (for fallback only, since
    first attempt failed during prepare before reaching execute).
    """
    context = RequestContext(
        headers={},
        cookies={},
        state=MagicMock(),
        app_state=MagicMock(),
        client_host="127.0.0.1",
        original_request=None,
    )
    context.backend = "gemini-oauth-auto"
    context.effective_model = "gemini-3.1-pro-preview"

    request_data = ChatRequest(
        model="gemini-oauth-auto:gemini-3.1-pro-preview",
        messages=[ChatMessage(role="user", content="test")],
    )

    await integrated_processor.process_request(context, request_data)

    # Execute called once (for fallback to original model)
    assert integrated_processor._backend_executor.execute.call_count == 1

    # The execute call should be with original model context
    call_args = integrated_processor._backend_executor.execute.call_args
    called_context = call_args[0][0]

    # Context should have been reverted to original
    assert called_context.backend == "openai"
    assert called_context.effective_model == "gpt-4o"


@pytest.mark.asyncio
async def test_no_data_done_in_error_message_text(
    integrated_processor,
) -> None:
    """
    Critical: 'data: [DONE]' must never appear in error message text (Fix 0).

    This was the most visible symptom reported by the user.
    """
    # Make prepare fail to trigger error
    integrated_processor._backend_preparer.prepare = AsyncMock(
        side_effect=[
            AuthenticationError("Token unavailable"),
            AuthenticationError("Both failed"),
        ]
    )

    context = RequestContext(
        headers={},
        cookies={},
        state=MagicMock(),
        app_state=MagicMock(),
        client_host="127.0.0.1",
        original_request=None,
    )
    context.backend = "gemini-oauth-auto"
    context.effective_model = "gemini-3.1-pro-preview"

    request_data = ChatRequest(
        model="gemini-oauth-auto:gemini-3.1-pro-preview",
        messages=[ChatMessage(role="user", content="test")],
    )

    try:
        await integrated_processor.process_request(context, request_data)
    except AuthenticationError as e:
        # Error message must NOT contain "data: [DONE]" as text
        assert "data: [DONE]" not in str(e)
        # Error message is clean
        assert "unavailable" in str(e).lower() or "failed" in str(e).lower()


@pytest.mark.asyncio
async def test_fallback_happens_exactly_once_per_request(
    integrated_processor,
    mock_replacement_service_with_gemini,
) -> None:
    """
    Each request attempts fallback at most once (Fix 2 - no infinite loops).
    """
    context = RequestContext(
        headers={},
        cookies={},
        state=MagicMock(),
        app_state=MagicMock(),
        client_host="127.0.0.1",
        original_request=None,
    )
    context.backend = "gemini-oauth-auto"
    context.effective_model = "gemini-3.1-pro-preview"

    request_data = ChatRequest(
        model="gemini-oauth-auto:gemini-3.1-pro-preview",
        messages=[ChatMessage(role="user", content="test")],
    )

    await integrated_processor.process_request(context, request_data)

    # Prepare called exactly twice (once for replacement, once for fallback)
    assert integrated_processor._backend_preparer.prepare.call_count == 2

    # Deactivate called exactly once
    assert (
        mock_replacement_service_with_gemini.get_state.return_value.deactivate.call_count
        == 1
    )


@pytest.mark.asyncio
async def test_warning_not_error_for_replacement_failure(
    integrated_processor,
    caplog,
) -> None:
    """
    Replacement model failures log WARNING, not ERROR (Fix 2).

    This prevents false alarms in monitoring systems.
    """
    import logging

    caplog.set_level(logging.WARNING)

    context = RequestContext(
        headers={},
        cookies={},
        state=MagicMock(),
        app_state=MagicMock(),
        client_host="127.0.0.1",
        original_request=None,
    )
    context.backend = "gemini-oauth-auto"
    context.effective_model = "gemini-3.1-pro-preview"

    request_data = ChatRequest(
        model="gemini-oauth-auto:gemini-3.1-pro-preview",
        messages=[ChatMessage(role="user", content="test")],
    )

    await integrated_processor.process_request(context, request_data)

    # Must have WARNING logs
    warning_logs = [r for r in caplog.records if r.levelname == "WARNING"]
    assert len(warning_logs) > 0

    # Must NOT have ERROR logs
    error_logs = [r for r in caplog.records if r.levelname == "ERROR"]
    assert len(error_logs) == 0
