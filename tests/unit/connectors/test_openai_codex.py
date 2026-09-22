import asyncio
import contextlib
import json
import threading
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest
from fastapi import HTTPException
from src.connectors.contracts import (
    ConnectorChatCompletionsRequest,
    ConnectorRequestContext,
)
from src.connectors.openai import OpenAIConnector
from src.connectors.openai_codex import (
    OpenAICodexConnector,
    OpenAICredentialsFileHandler,
)
from src.core.common.exceptions import BackendError
from src.core.domain.chat import CanonicalChatRequest, ChatMessage, ChatRequest


@pytest.mark.asyncio
async def test_openai_codex_routes_supported_model_through_codex_api(
    openai_codex_backend: OpenAICodexConnector,
):
    req = ChatRequest(
        model="openai-codex:gpt-5.5",
        messages=[ChatMessage(role="user", content="hi")],
        max_tokens=16,
        stream=False,
    )

    expected = MagicMock()

    with (
        patch.object(
            openai_codex_backend,
            "_validate_runtime_credentials",
            AsyncMock(return_value=True),
        ),
        patch.object(
            openai_codex_backend,
            "_call_codex_responses_api",
            AsyncMock(return_value=expected),
        ) as codex_call,
        patch.object(OpenAIConnector, "chat_completions", AsyncMock()) as base_call,
    ):
        domain = CanonicalChatRequest.model_validate(req.model_dump())
        connector_req = ConnectorChatCompletionsRequest(
            request=domain,
            processed_messages=[ChatMessage(role="user", content="hi")],
            effective_model="gpt-5.5",
            identity=None,
            cancellation_token=None,
            cancellation_coordinator=None,
            context=None,
            options={},
        )
        result = await openai_codex_backend.chat_completions(connector_req)

    assert result is expected
    codex_call.assert_awaited_once()
    base_call.assert_not_awaited()


@pytest.mark.asyncio
async def test_openai_codex_rejects_unsupported_models_without_openai_fallback(
    openai_codex_backend: OpenAICodexConnector,
):
    req = ChatRequest(
        model="openai-codex:gpt-4o-mini",
        messages=[ChatMessage(role="user", content="hi")],
        max_tokens=16,
        stream=False,
    )

    with (
        patch.object(
            openai_codex_backend,
            "_validate_runtime_credentials",
            AsyncMock(return_value=True),
        ),
        patch.object(
            openai_codex_backend,
            "_call_codex_responses_api",
            AsyncMock(),
        ) as codex_call,
        patch.object(OpenAIConnector, "chat_completions", AsyncMock()) as base_call,
        pytest.raises(HTTPException) as exc_info,
    ):
        domain = CanonicalChatRequest.model_validate(req.model_dump())
        connector_req = ConnectorChatCompletionsRequest(
            request=domain,
            processed_messages=[ChatMessage(role="user", content="hi")],
            effective_model="gpt-4o-mini",
            identity=None,
            cancellation_token=None,
            cancellation_coordinator=None,
            context=None,
            options={},
        )
        await openai_codex_backend.chat_completions(connector_req)

    assert exc_info.value.status_code == 400
    detail = exc_info.value.detail
    assert isinstance(detail, dict)
    assert detail["error"] == "openai_codex_model_not_supported"
    assert detail["details"]["requested_model"] == "gpt-4o-mini"
    codex_call.assert_not_awaited()
    base_call.assert_not_awaited()


@pytest.mark.asyncio
async def test_usage_window_warmup_targeted_bind_failure_raises_backend_error(
    openai_codex_backend: OpenAICodexConnector,
):
    req = ChatRequest(
        model="openai-codex:gpt-5.5",
        messages=[ChatMessage(role="user", content="warmup probe")],
        max_tokens=16,
        stream=False,
    )
    domain = CanonicalChatRequest.model_validate(req.model_dump())
    connector_req = ConnectorChatCompletionsRequest(
        request=domain,
        processed_messages=[ChatMessage(role="user", content="warmup probe")],
        effective_model="gpt-5.5",
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=ConnectorRequestContext(
            request_id="req-1",
            session_id="warmup-session",
            client_host="127.0.0.1",
            extensions={
                "usage_window_warmup": True,
                "warmup_target_account_id": "acct-missing",
            },
        ),
        options={},
    )

    with (
        patch.object(
            openai_codex_backend._credential_manager,
            "ensure_usage_window_warmup_managed_account",
            AsyncMock(return_value=False),
        ),
        pytest.raises(BackendError) as exc_info,
    ):
        await openai_codex_backend._prepare_usage_window_warmup_credentials(
            connector_req
        )

    assert exc_info.value.message == (
        "Usage window warm-up could not bind targeted managed account"
    )


@pytest.mark.asyncio
async def test_usage_window_warmup_restores_credentials_after_success(
    openai_codex_backend: OpenAICodexConnector,
):
    req = ChatRequest(
        model="openai-codex:gpt-5.5",
        messages=[ChatMessage(role="user", content="warmup probe")],
        max_tokens=16,
        stream=False,
    )
    domain = CanonicalChatRequest.model_validate(req.model_dump())
    connector_req = ConnectorChatCompletionsRequest(
        request=domain,
        processed_messages=[ChatMessage(role="user", content="warmup probe")],
        effective_model="gpt-5.5",
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=ConnectorRequestContext(
            request_id="req-2",
            session_id="warmup-session",
            client_host="127.0.0.1",
            extensions={"usage_window_warmup": True},
        ),
        options={},
    )

    openai_codex_backend._credential_manager._auth_credentials = {  # type: ignore[reportPrivateUsage]
        "tokens": {"access_token": "baseline_token"}
    }
    openai_codex_backend.api_key = "baseline_token"

    async def fake_warmup_override(
        managed_account_id: str | None,
        *,
        session_id: str | None = None,
    ) -> bool:
        assert managed_account_id is None
        assert session_id is None
        openai_codex_backend._credential_manager._auth_credentials = {  # type: ignore[reportPrivateUsage]
            "tokens": {"access_token": "warmup_token"}
        }
        return True

    async def fake_codex_call(**kwargs):
        assert openai_codex_backend.api_key == "warmup_token"
        return MagicMock()

    with (
        patch.object(
            openai_codex_backend._credential_manager,
            "ensure_usage_window_warmup_managed_account",
            side_effect=fake_warmup_override,
        ),
        patch.object(
            openai_codex_backend,
            "_validate_runtime_credentials",
            AsyncMock(return_value=True),
        ),
        patch.object(
            openai_codex_backend,
            "_call_codex_responses_api",
            AsyncMock(side_effect=fake_codex_call),
        ),
    ):
        await openai_codex_backend.chat_completions(connector_req)

    assert openai_codex_backend.api_key == "baseline_token"
    assert (
        openai_codex_backend._credential_manager.get_access_token() == "baseline_token"
    )


@pytest.mark.asyncio
async def test_usage_window_warmup_restores_credentials_after_failure(
    openai_codex_backend: OpenAICodexConnector,
):
    req = ChatRequest(
        model="openai-codex:gpt-5.5",
        messages=[ChatMessage(role="user", content="warmup probe")],
        max_tokens=16,
        stream=False,
    )
    domain = CanonicalChatRequest.model_validate(req.model_dump())
    connector_req = ConnectorChatCompletionsRequest(
        request=domain,
        processed_messages=[ChatMessage(role="user", content="warmup probe")],
        effective_model="gpt-5.5",
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=ConnectorRequestContext(
            request_id="req-3",
            session_id="warmup-session",
            client_host="127.0.0.1",
            extensions={"usage_window_warmup": True},
        ),
        options={},
    )

    openai_codex_backend._credential_manager._auth_credentials = {  # type: ignore[reportPrivateUsage]
        "tokens": {"access_token": "baseline_token"}
    }
    openai_codex_backend.api_key = "baseline_token"

    async def fake_warmup_override(
        managed_account_id: str | None,
        *,
        session_id: str | None = None,
    ) -> bool:
        assert managed_account_id is None
        assert session_id is None
        openai_codex_backend._credential_manager._auth_credentials = {  # type: ignore[reportPrivateUsage]
            "tokens": {"access_token": "warmup_token"}
        }
        return True

    with (
        patch.object(
            openai_codex_backend._credential_manager,
            "ensure_usage_window_warmup_managed_account",
            side_effect=fake_warmup_override,
        ),
        patch.object(
            openai_codex_backend,
            "_validate_runtime_credentials",
            AsyncMock(return_value=True),
        ),
        patch.object(
            openai_codex_backend,
            "_call_codex_responses_api",
            AsyncMock(side_effect=RuntimeError("probe failed")),
        ),
        pytest.raises(RuntimeError, match="probe failed"),
    ):
        await openai_codex_backend.chat_completions(connector_req)

    assert openai_codex_backend.api_key == "baseline_token"
    assert (
        openai_codex_backend._credential_manager.get_access_token() == "baseline_token"
    )


@pytest.mark.asyncio
async def test_usage_window_warmup_concurrent_targeted_probes_use_matching_credentials(
    openai_codex_backend: OpenAICodexConnector,
):
    def make_request(
        request_id: str, account_id: str
    ) -> ConnectorChatCompletionsRequest:
        req = ChatRequest(
            model="openai-codex:gpt-5.5",
            messages=[ChatMessage(role="user", content=f"warmup {account_id}")],
            max_tokens=16,
            stream=False,
        )
        domain = CanonicalChatRequest.model_validate(req.model_dump())
        return ConnectorChatCompletionsRequest(
            request=domain,
            processed_messages=[
                ChatMessage(role="user", content=f"warmup {account_id}")
            ],
            effective_model="gpt-5.5",
            identity=None,
            cancellation_token=None,
            cancellation_coordinator=None,
            context=ConnectorRequestContext(
                request_id=request_id,
                session_id="warmup-session",
                client_host="127.0.0.1",
                extensions={
                    "usage_window_warmup": True,
                    "warmup_target_account_id": account_id,
                },
            ),
            options={},
        )

    openai_codex_backend._credential_manager._auth_credentials = {  # type: ignore[reportPrivateUsage]
        "tokens": {"access_token": "baseline_token"}
    }
    openai_codex_backend.api_key = "baseline_token"

    active_account: str | None = None
    snapshots: dict[int, str | None] = {}
    account_tokens = {
        "acct-A": "token-A",
        "acct-B": "token-B",
        None: "baseline_token",
    }
    a_marked = asyncio.Event()
    b_marked = asyncio.Event()

    def begin_override() -> dict[str, str | None]:
        snapshot = {"active_account": active_account}
        snapshots[id(snapshot)] = active_account
        return snapshot

    def end_override(snapshot: dict[str, str | None]) -> None:
        nonlocal active_account
        active_account = snapshots[id(snapshot)]

    async def ensure_account(
        managed_account_id: str | None,
        *,
        session_id: str | None = None,
    ) -> bool:
        nonlocal active_account
        active_account = managed_account_id
        if managed_account_id == "acct-A":
            a_marked.set()
            with contextlib.suppress(asyncio.TimeoutError):
                await asyncio.wait_for(b_marked.wait(), timeout=0.2)
        elif managed_account_id == "acct-B":
            with contextlib.suppress(asyncio.TimeoutError):
                await asyncio.wait_for(a_marked.wait(), timeout=0.2)
            b_marked.set()
        return True

    def get_access_token() -> str:
        return account_tokens[active_account]

    openai_codex_backend._credential_manager.begin_usage_window_warmup_override = begin_override  # type: ignore[attr-defined]
    openai_codex_backend._credential_manager.end_usage_window_warmup_override = end_override  # type: ignore[attr-defined]
    openai_codex_backend._credential_manager.ensure_usage_window_warmup_managed_account = ensure_account  # type: ignore[attr-defined]
    openai_codex_backend._credential_manager.get_access_token = get_access_token  # type: ignore[method-assign]

    observed_api_keys: dict[str, str] = {}

    async def fake_codex_call(**kwargs):
        request_data = kwargs.get("request_data")
        if isinstance(request_data, dict):
            payload = request_data
        else:
            payload = request_data.model_dump()
        text = payload["messages"][0]["content"]
        request_label = "A" if text.endswith("acct-A") else "B"
        observed_api_keys[request_label] = openai_codex_backend.api_key
        await asyncio.sleep(0)
        return MagicMock()

    req_a = make_request("req-A", "acct-A")
    req_b = make_request("req-B", "acct-B")

    with (
        patch.object(
            openai_codex_backend,
            "_validate_runtime_credentials",
            AsyncMock(return_value=True),
        ),
        patch.object(
            openai_codex_backend,
            "_call_codex_responses_api",
            AsyncMock(side_effect=fake_codex_call),
        ),
    ):
        await asyncio.gather(
            openai_codex_backend.chat_completions(req_a),
            openai_codex_backend.chat_completions(req_b),
        )

    assert observed_api_keys == {"A": "token-A", "B": "token-B"}


@pytest.mark.asyncio
async def test_openai_codex_reload_scheduled_from_thread(
    openai_codex_backend: OpenAICodexConnector,
):
    backend = openai_codex_backend

    reload_event = asyncio.Event()

    async def fake_load(force_reload: bool = False) -> bool:
        reload_event.set()
        return True

    with (
        patch.object(
            backend, "_load_auth", AsyncMock(side_effect=fake_load)
        ) as load_mock,
        patch.object(
            backend, "_validate_credentials_structure", return_value=(True, [])
        ),
    ):

        def trigger() -> None:
            backend._schedule_credentials_reload()

        thread = threading.Thread(target=trigger)
        thread.start()

        # The thread needs to complete its work, but thread.join() is a blocking
        # call that will stall the event loop. Instead, we wait for the event
        # that is set by the coroutine that the thread schedules.
        # A generous timeout is used to prevent flakes on slow systems.
        await asyncio.wait_for(reload_event.wait(), timeout=5.0)
        await asyncio.to_thread(thread.join)
        load_mock.assert_awaited()

        # Allow callbacks to run so the pending task/future clears
        await asyncio.sleep(0)
        pending = backend._pending_reload_task
        assert pending is None or pending.done()


# --------------------------------------------------------------------------------
# Tests for file watching and force reload functionality
# --------------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_start_file_watching_success(auth_dir: Path):
    """Test that file watching starts successfully."""
    import os

    async with httpx.AsyncClient() as client:
        from src.core.config.app_config import AppConfig
        from src.core.services.translation_service import TranslationService

        cfg = AppConfig()
        ts = TranslationService()
        backend = OpenAICodexConnector(client, cfg, translation_service=ts)

        # Enable file watching for this test
        with (
            patch.dict(os.environ, {"ENABLE_CODEX_FILE_WATCH": "1"}),
            patch.object(
                backend, "_validate_credentials_file_exists", return_value=(True, [])
            ),
            patch.object(
                backend, "_validate_credentials_structure", return_value=(True, [])
            ),
        ):
            await backend.initialize(openai_codex_path=str(auth_dir))

        # Verify file observer was started
        assert backend._file_observer is not None
        assert backend._file_observer.is_alive()

        # Clean up - run blocking _stop_file_watching() in thread pool
        # to avoid blocking the event loop. Add timeout to prevent stalling
        # if the observer's join() call hangs.
        await asyncio.wait_for(
            asyncio.to_thread(backend._stop_file_watching), timeout=5.0
        )
        # Verify it's been cleaned up
        assert backend._file_observer is None


@pytest.mark.asyncio
async def test_start_file_watching_no_credentials_path():
    """Test that file watching doesn't start if credentials path is not set."""
    async with httpx.AsyncClient() as client:
        from src.core.config.app_config import AppConfig
        from src.core.services.translation_service import TranslationService

        cfg = AppConfig()
        ts = TranslationService()
        backend = OpenAICodexConnector(client, cfg, translation_service=ts)

        # Try to start file watching without setting credentials path
        backend._start_file_watching()

        # Verify file observer was not started
        assert backend._file_observer is None


@pytest.mark.asyncio
async def test_stop_file_watching_success(auth_dir: Path):
    """Test that file watching stops successfully."""
    import os

    async with httpx.AsyncClient() as client:
        from src.core.config.app_config import AppConfig
        from src.core.services.translation_service import TranslationService

        cfg = AppConfig()
        ts = TranslationService()
        backend = OpenAICodexConnector(client, cfg, translation_service=ts)

        # Enable file watching for this test
        with (
            patch.dict(os.environ, {"ENABLE_CODEX_FILE_WATCH": "1"}),
            patch.object(
                backend, "_validate_credentials_file_exists", return_value=(True, [])
            ),
            patch.object(
                backend, "_validate_credentials_structure", return_value=(True, [])
            ),
        ):
            await backend.initialize(openai_codex_path=str(auth_dir))

        assert backend._file_observer is not None

        # Stop file watching - run blocking _stop_file_watching() in thread pool
        # to avoid blocking the event loop. Add timeout to prevent stalling
        # if the observer's join() call hangs.
        await asyncio.wait_for(
            asyncio.to_thread(backend._stop_file_watching), timeout=5.0
        )

        # Verify observer is stopped and cleaned up
        assert backend._file_observer is None


@pytest.mark.asyncio
async def test_stop_file_watching_no_observer():
    """Test that stopping file watching when no observer exists doesn't raise errors."""
    async with httpx.AsyncClient() as client:
        from src.core.config.app_config import AppConfig
        from src.core.services.translation_service import TranslationService

        cfg = AppConfig()
        ts = TranslationService()
        backend = OpenAICodexConnector(client, cfg, translation_service=ts)

        # Should not raise an error
        backend._stop_file_watching()
        assert backend._file_observer is None


@pytest.mark.asyncio
async def test_schedule_credentials_reload_valid_update(auth_dir: Path):
    """Test that credentials are reloaded when file changes with valid data."""
    async with httpx.AsyncClient() as client:
        from src.core.config.app_config import AppConfig
        from src.core.services.translation_service import TranslationService

        cfg = AppConfig()
        ts = TranslationService()
        backend = OpenAICodexConnector(client, cfg, translation_service=ts)

        with (
            patch.object(
                backend, "_validate_credentials_file_exists", return_value=(True, [])
            ),
            patch.object(
                backend, "_validate_credentials_structure", return_value=(True, [])
            ),
            patch.object(backend, "_start_file_watching"),
        ):
            await backend.initialize(openai_codex_path=str(auth_dir))

        try:
            # Update credentials file with new token
            new_data = {"tokens": {"access_token": "new_token_123"}}
            (auth_dir / "auth.json").write_text(json.dumps(new_data), encoding="utf-8")

            # Mock _load_auth to return success
            with patch.object(backend, "_load_auth", return_value=True) as mock_load:
                backend._auth_credentials = new_data

                # Call the reload method
                backend._schedule_credentials_reload()

                # Wait for the task to complete
                if backend._pending_reload_task:
                    await backend._pending_reload_task

                # Verify load_auth was called with force_reload=True
                mock_load.assert_called_once_with(force_reload=True)
        finally:
            await backend.shutdown()


@pytest.mark.asyncio
async def test_schedule_credentials_reload_invalid_file(auth_dir: Path):
    """Test that credentials reload degrades backend when file becomes invalid."""
    async with httpx.AsyncClient() as client:
        from src.core.config.app_config import AppConfig
        from src.core.services.translation_service import TranslationService

        cfg = AppConfig()
        ts = TranslationService()
        backend = OpenAICodexConnector(client, cfg, translation_service=ts)

        with (
            patch.object(
                backend, "_validate_credentials_file_exists", return_value=(True, [])
            ),
            patch.object(
                backend, "_validate_credentials_structure", return_value=(True, [])
            ),
            patch.object(backend, "_start_file_watching"),
        ):
            await backend.initialize(openai_codex_path=str(auth_dir))

        try:
            # Mock validation to return failure
            from src.core.domain.validation import ValidationResult

            with (
                patch.object(backend, "_load_auth", return_value=True),
                patch.object(
                    backend,
                    "_validate_credentials_structure",
                    return_value=ValidationResult.failure(["Missing required fields"]),
                ),
            ):
                backend._auth_credentials = {}

                # Call the reload method
                backend._schedule_credentials_reload()

                # Wait for the task to complete
                if backend._pending_reload_task:
                    await backend._pending_reload_task

                # Verify backend was degraded
                assert not backend.is_functional
                assert len(backend._credential_validation_errors) > 0
        finally:
            await backend.shutdown()


@pytest.mark.asyncio
async def test_schedule_credentials_reload_load_failure(auth_dir: Path):
    """Test that credentials reload degrades backend when load fails."""
    async with httpx.AsyncClient() as client:
        from src.core.config.app_config import AppConfig
        from src.core.services.translation_service import TranslationService

        cfg = AppConfig()
        ts = TranslationService()
        backend = OpenAICodexConnector(client, cfg, translation_service=ts)

        with (
            patch.object(
                backend, "_validate_credentials_file_exists", return_value=(True, [])
            ),
            patch.object(
                backend, "_validate_credentials_structure", return_value=(True, [])
            ),
            patch.object(backend, "_start_file_watching"),
        ):
            await backend.initialize(openai_codex_path=str(auth_dir))

        try:
            # Mock _load_auth to fail
            with patch.object(backend, "_load_auth", return_value=False):
                # Call the reload method
                backend._schedule_credentials_reload()

                # Wait for the task to complete
                if backend._pending_reload_task:
                    await backend._pending_reload_task

                # Verify backend was degraded
                assert not backend.is_functional
                assert "Failed to reload credentials from file" in str(
                    backend._credential_validation_errors
                )
        finally:
            await backend.shutdown()


@pytest.mark.asyncio
async def test_load_auth_with_force_reload(auth_dir: Path):
    """Test that force_reload bypasses the timestamp cache."""
    async with httpx.AsyncClient() as client:
        from src.core.config.app_config import AppConfig
        from src.core.services.translation_service import TranslationService

        cfg = AppConfig()
        ts = TranslationService()
        backend = OpenAICodexConnector(client, cfg, translation_service=ts)
        backend._oauth_dir_override = auth_dir

        try:
            # First load
            result1 = await backend._load_auth()
            assert result1 is True
            token1 = backend.api_key
            last_modified1 = backend._last_modified

            # Update the file with new token but keep same timestamp
            new_data = {"tokens": {"access_token": "force_reload_token"}}
            (auth_dir / "auth.json").write_text(json.dumps(new_data), encoding="utf-8")

            # Set the timestamp back to simulate no change
            import os

            os.utime(auth_dir / "auth.json", (last_modified1, last_modified1))

            # Load without force_reload - should use cache
            result2 = await backend._load_auth(force_reload=False)
            assert result2 is True
            token2 = backend.api_key
            assert token2 == token1  # Should be cached

            # Load with force_reload - should reload from file
            result3 = await backend._load_auth(force_reload=True)
            assert result3 is True
            token3 = backend.api_key
            assert token3 == "force_reload_token"  # Should be new token
        finally:
            # Clean up backend to prevent test isolation issues
            await backend.shutdown()


@pytest.mark.asyncio
async def test_file_handler_on_modified_path_comparison(auth_dir: Path):
    """Test that file handler correctly compares paths across platforms."""
    async with httpx.AsyncClient() as client:
        from src.core.config.app_config import AppConfig
        from src.core.services.translation_service import TranslationService

        cfg = AppConfig()
        ts = TranslationService()
        backend = OpenAICodexConnector(client, cfg, translation_service=ts)
        backend._oauth_dir_override = auth_dir

        try:
            await backend._load_auth()

            handler = OpenAICredentialsFileHandler(backend)

            # Create a mock event with the same path
            mock_event = MagicMock()
            mock_event.is_directory = False
            mock_event.src_path = str(auth_dir / "auth.json")

            # Mock the schedule method to verify it was called
            with patch.object(backend, "_schedule_credentials_reload") as mock_schedule:
                handler.on_modified(mock_event)
                mock_schedule.assert_called_once()
        finally:
            # Clean up backend to prevent test isolation issues
            await backend.shutdown()


@pytest.mark.asyncio
async def test_file_handler_on_modified_different_file(auth_dir: Path):
    """Test that file handler ignores changes to different files."""
    async with httpx.AsyncClient() as client:
        from src.core.config.app_config import AppConfig
        from src.core.services.translation_service import TranslationService

        cfg = AppConfig()
        ts = TranslationService()
        backend = OpenAICodexConnector(client, cfg, translation_service=ts)
        backend._oauth_dir_override = auth_dir

        try:
            await backend._load_auth()

            handler = OpenAICredentialsFileHandler(backend)

            # Create a mock event for a different file
            mock_event = MagicMock()
            mock_event.is_directory = False
            mock_event.src_path = str(auth_dir / "other_file.json")

            # Mock the schedule method to verify it was NOT called
            with patch.object(backend, "_schedule_credentials_reload") as mock_schedule:
                handler.on_modified(mock_event)
                mock_schedule.assert_not_called()
        finally:
            # Clean up backend to prevent test isolation issues
            await backend.shutdown()
