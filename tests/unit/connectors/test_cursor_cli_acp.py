from __future__ import annotations

import asyncio
import json
import time
from pathlib import Path
from typing import Any
from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest
from pydantic.types import JsonValue
from src.connectors.acp_core.base_connector import BaseAcpConnector
from src.connectors.acp_core.types import ACPError, ACPNotification
from src.connectors.contracts import ConnectorChatCompletionsRequest
from src.connectors.cursor_cli_acp import (
    CursorCliAcpConnector,
    CursorCliConfiguredModelEnumerator,
    CursorCliModelCatalog,
    _cursor_model_fast_tier,
    build_cursor_agent_acp_command,
    discover_cursor_cli_model_catalog,
    parse_agent_models_listing,
    resolve_cursor_agent_executable,
    resolve_cursor_cli_model_id,
)
from src.connectors.cursor_cli_auth import (
    CursorAuthPolicy,
    build_cursor_cli_env,
)
from src.core.common.exceptions import (
    BackendError,
    ConfigurationError,
    ServiceUnavailableError,
)
from src.core.config.app_config import AppConfig, BackendConfig
from src.core.domain.chat import CanonicalChatRequest, ChatMessage
from src.core.domain.responses import (
    ProcessedResponse,
    StreamingResponseEnvelope,
)
from src.core.domain.responses_native_wiring import (
    ACP_RESPONSES_STANDALONE_MODE_KEY,
    ACP_RESPONSES_TEXT_ONLY_MODE_KEY,
)
from src.core.services.translation_service import TranslationService


@pytest.fixture
def temp_workspace(tmp_path: Path) -> Path:
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    return workspace


@pytest.fixture
def connector() -> CursorCliAcpConnector:
    client = AsyncMock(spec=httpx.AsyncClient)
    return CursorCliAcpConnector(client, AppConfig(), TranslationService())


def _make_request(
    *,
    stream: bool = False,
    extra_body: dict[str, JsonValue] | None = None,
    options: dict[str, JsonValue] | None = None,
    processed_messages: list[ChatMessage] | None = None,
    model: str = "cursor/composer-2",
    cancellation_coordinator: Any = None,
    cancellation_token: Any = None,
) -> ConnectorChatCompletionsRequest:
    request = CanonicalChatRequest(
        model=model,
        stream=stream,
        messages=[ChatMessage(role="user", content="hello")],
        extra_body=extra_body,
    )
    return ConnectorChatCompletionsRequest(
        request=request,
        processed_messages=processed_messages
        or [ChatMessage(role="user", content="hello")],
        effective_model=model,
        identity=None,
        cancellation_token=cancellation_token,
        cancellation_coordinator=cancellation_coordinator,
        context=None,
        options=options or {},
    )


class TestCursorCliAcpModelCache:
    async def test_configured_enumerator_does_not_require_workspace(
        self, connector: CursorCliAcpConnector
    ) -> None:
        del connector
        catalog = CursorCliModelCatalog(
            routes=("cursor/glm-5.2-max",),
            cli_ids={"cursor/glm-5.2-max": "glm-5.2-max"},
        )
        enumerator = CursorCliConfiguredModelEnumerator()
        config = BackendConfig(
            connector="cursor-cli-acp",
            extra={
                "cursor_cli_executable": "agent",
                "cursor_cli_extra_args": ["--profile", "work"],
                "cursor_model_discovery_timeout_seconds": 73,
            },
        )

        with (
            patch(
                "src.connectors.cursor_cli_acp.resolve_cursor_agent_executable",
                return_value="agent",
            ),
            patch(
                "src.connectors.cursor_cli_acp.discover_cursor_cli_model_catalog",
                AsyncMock(return_value=catalog),
            ) as discover,
        ):
            result = await enumerator.enumerate("cursor-cli-acp.default", config)

        assert result.models == ("cursor/glm-5.2-max",)
        discover.assert_awaited_once_with(
            executable="agent",
            cursor_api_endpoint=None,
            extra_args=["--profile", "work"],
            workspace=None,
            timeout_seconds=73.0,
        )

    async def test_discovery_uses_list_models_and_configured_cursor_api_endpoint(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._cursor_cli_executable = "agent"
        connector._cursor_api_endpoint = "https://cursor.example.test"
        connector._extra_cli_args = ["--profile", "work"]
        connector._model_discovery_timeout_seconds = 73.0
        connector._default_project_dir = temp_workspace
        completed = MagicMock(
            returncode=0,
            stdout=(
                "Available models\n\n"
                "cursor-grok-4.5-high - Cursor Grok 4.5\n"
                "glm-5.2-max - GLM 5.2 Max\n"
            ),
            stderr="",
        )
        policy = CursorAuthPolicy(
            cookie_usable=True,
            env_key_present=False,
            env_key_invalid=False,
            discovery_mode="cookie_only",
            acp_mode="cookie_only",
        )

        with (
            patch(
                "src.connectors.cursor_cli_acp.resolve_cursor_auth_policy",
                return_value=policy,
            ),
            patch(
                "src.connectors.cursor_cli_acp.subprocess.run", return_value=completed
            ) as run,
        ):
            models = await connector._discover_models()

        assert models == [
            "cursor/grok-4.5-high",
            "cursor/glm-5.2-max",
        ]
        assert connector._model_cli_ids == {
            "cursor/grok-4.5-high": "cursor-grok-4.5-high",
            "cursor/glm-5.2-max": "glm-5.2-max",
        }
        assert connector._acp_auth_mode == "cookie_only"
        run.assert_called_once()
        args, kwargs = run.call_args
        assert args[0] == [
            "agent",
            "-e",
            "https://cursor.example.test",
            "--profile",
            "work",
            "--list-models",
        ]
        assert kwargs["timeout"] == 73.0
        assert kwargs["cwd"] == str(temp_workspace)
        assert "CURSOR_API_KEY" not in kwargs["env"]
        assert "CURSOR_AUTH_TOKEN" not in kwargs["env"]

    async def test_initialize_preserves_model_discovery_timeout_and_extra_args(
        self, connector: CursorCliAcpConnector
    ) -> None:
        with (
            patch.object(connector, "_check_agent_available", return_value=True),
            patch.object(connector, "_ensure_models_discovered", AsyncMock()),
            patch(
                "src.connectors.cursor_cli_acp.resolve_cursor_agent_executable",
                return_value="agent",
            ),
        ):
            await connector.initialize(
                cursor_cli_executable="agent",
                cursor_cli_extra_args=["--profile", "work"],
                cursor_model_discovery_timeout_seconds=91,
            )

        assert connector._extra_cli_args == ["--profile", "work"]
        assert connector._model_discovery_timeout_seconds == 91.0

    async def test_get_available_models_async_refreshes_when_ttl_zero(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._cursor_cli_executable = str(temp_workspace / "agent")
        connector._default_project_dir = temp_workspace
        connector._models_cache_ttl_seconds = 0.0
        connector.is_functional = True
        connector._initialization_failed = False
        connector._validation_errors = []
        calls = 0

        async def fake_discover() -> list[str]:
            nonlocal calls
            calls += 1
            return [f"cursor/model-{calls}"]

        with patch.object(connector, "_discover_models", side_effect=fake_discover):
            first = await connector.get_available_models_async()
            second = await connector.get_available_models_async()

        assert first == ["cursor/model-1"]
        assert second == ["cursor/model-2"]
        assert calls == 2

    async def test_get_available_models_async_skips_within_ttl(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._cursor_cli_executable = str(temp_workspace / "agent")
        connector._default_project_dir = temp_workspace
        connector._models_cache_ttl_seconds = 3600.0
        connector.is_functional = True
        connector._initialization_failed = False
        connector._validation_errors = []
        calls = 0

        async def fake_discover() -> list[str]:
            nonlocal calls
            calls += 1
            return ["cursor/only"]

        with patch.object(connector, "_discover_models", side_effect=fake_discover):
            assert await connector.get_available_models_async() == ["cursor/only"]
            assert await connector.get_available_models_async() == ["cursor/only"]

        assert calls == 1

    async def test_get_available_models_async_caches_empty_discovery_within_ttl(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._cursor_cli_executable = str(temp_workspace / "agent")
        connector._default_project_dir = temp_workspace
        connector._models_cache_ttl_seconds = 3600.0
        connector.is_functional = True
        connector._initialization_failed = False
        connector._validation_errors = []
        calls = 0

        async def fake_discover() -> list[str]:
            nonlocal calls
            calls += 1
            return []

        with patch.object(connector, "_discover_models", side_effect=fake_discover):
            assert await connector.get_available_models_async() == []
            assert await connector.get_available_models_async() == []

        assert calls == 1

    async def test_get_available_models_async_retries_empty_discovery_after_ttl(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._cursor_cli_executable = str(temp_workspace / "agent")
        connector._default_project_dir = temp_workspace
        connector._models_cache_ttl_seconds = 3600.0
        connector.is_functional = True
        connector._initialization_failed = False
        connector._validation_errors = []
        connector._models_cache_fetched_at = time.monotonic() - 3601
        calls = 0

        async def fake_discover() -> list[str]:
            nonlocal calls
            calls += 1
            return ["cursor/model"]

        with patch.object(connector, "_discover_models", side_effect=fake_discover):
            assert await connector.get_available_models_async() == ["cursor/model"]

        assert calls == 1

    async def test_force_refresh_retries_cached_empty_discovery(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._cursor_cli_executable = str(temp_workspace / "agent")
        connector._default_project_dir = temp_workspace
        connector._models_cache_ttl_seconds = 3600.0
        connector._cached_models = []
        connector._models_cache_fetched_at = time.monotonic()
        calls = 0

        async def fake_discover() -> list[str]:
            nonlocal calls
            calls += 1
            return ["cursor/model"]

        with patch.object(connector, "_discover_models", side_effect=fake_discover):
            await connector._ensure_models_discovered(force=True)

        assert calls == 1
        assert connector.get_available_models() == ["cursor/model"]

    async def test_get_available_models_async_caches_discovery_failure_within_ttl(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._cursor_cli_executable = str(temp_workspace / "agent")
        connector._default_project_dir = temp_workspace
        connector._models_cache_ttl_seconds = 3600.0
        connector.is_functional = True
        connector._initialization_failed = False
        connector._validation_errors = []
        calls = 0

        async def failed_discover() -> list[str]:
            nonlocal calls
            calls += 1
            raise RuntimeError("discovery unavailable")

        with patch.object(connector, "_discover_models", side_effect=failed_discover):
            assert await connector.get_available_models_async() == []
            assert await connector.get_available_models_async() == []

        assert calls == 1

    async def test_model_catalog_without_workspace_uses_list_models(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._cursor_cli_executable = str(temp_workspace / "agent")
        connector.is_functional = True
        discover_models = AsyncMock(return_value=["cursor/composer-2"])

        with patch.object(connector, "_discover_models", discover_models):
            assert await connector.get_available_models_async() == ["cursor/composer-2"]

        discover_models.assert_awaited_once_with()
        assert connector._models_cache_fetched_at > 0.0


class TestCursorCliAcpHelpers:
    @pytest.mark.parametrize(
        ("model", "fast_sibling"),
        [
            ("cursor-grok-4.6-low", "cursor-grok-4.6-low-fast"),
            ("cursor-grok-4.6-medium", "cursor-grok-4.6-medium-fast"),
            ("cursor-grok-4.6-high", "cursor-grok-4.6-high-fast"),
            ("cursor-grok-4.6-high", "cursor-grok-4.6-fast"),
            ("cursor-grok-4.6-xhigh", "cursor-grok-4.6-xhigh-fast"),
            ("composer-2.5", "composer-2.5-fast"),
        ],
    )
    def test_paired_standard_model_selects_standard_speed(
        self, model: str, fast_sibling: str
    ) -> None:
        assert (
            _cursor_model_fast_tier(
                model, available_cli_model_ids=[model, fast_sibling]
            )
            is False
        )

    def test_fast_model_selects_fast_speed(self) -> None:
        assert _cursor_model_fast_tier("composer-2.5-fast") is True

    @pytest.mark.parametrize("model", ["cursor-grok-4.6-high", "composer-2.5"])
    def test_known_cursor_standard_models_require_confirmation_without_catalog(
        self, model: str
    ) -> None:
        assert _cursor_model_fast_tier(model) is False

    def test_parse_agent_models_listing(self) -> None:
        raw = """Loading models…
Available models

composer-2 - Composer 2  (current)
gpt-5.2 - GPT-5.2
"""
        ids = parse_agent_models_listing(raw)
        assert ids == ["composer-2", "gpt-5.2"]

    def test_build_cursor_agent_acp_command(self) -> None:
        cmd = build_cursor_agent_acp_command(
            r"C:\agent\agent.cmd",
            model="composer-2",
            trust_workspace=True,
            extra_args=["--foo"],
            cursor_api_endpoint="https://api.example",
            mode="ask",
        )
        assert cmd[0] == r"C:\agent\agent.cmd"
        assert cmd[:4] == [
            r"C:\agent\agent.cmd",
            "-e",
            "https://api.example",
            "--model",
        ]
        assert "composer-2" in cmd
        assert "--trust" in cmd
        assert cmd[cmd.index("--mode") + 1] == "ask"
        assert "--foo" in cmd
        assert cmd[-1] == "acp"

    def test_resolve_cursor_agent_executable_prefers_existing_file(
        self, tmp_path: Path
    ) -> None:
        exe = tmp_path / "my-agent.bat"
        exe.write_text("@echo off\n", encoding="utf-8")
        resolved = resolve_cursor_agent_executable(str(exe))
        assert resolved == str(exe.resolve())


class TestCursorCliAcpInitialization:
    async def test_initialize_with_project_dir(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        fake = str(temp_workspace / "fake-agent")
        Path(fake).write_text("noop", encoding="utf-8")
        with (
            patch.object(connector, "_check_agent_available", return_value=True),
            patch.object(connector, "_discover_models", return_value=["cursor/a"]),
        ):
            await connector.initialize(
                project_dir=str(temp_workspace),
                cursor_cli_executable=fake,
            )

        assert connector.is_backend_functional() is True
        assert connector._default_project_dir == temp_workspace.resolve()

    async def test_initialize_requires_workspace(
        self, connector: CursorCliAcpConnector, tmp_path: Path
    ) -> None:
        with (
            patch.object(connector, "_check_agent_available", return_value=True),
            patch.object(connector, "_discover_models", return_value=["cursor/x"]),
            pytest.raises(ConfigurationError),
        ):
            await connector.initialize(project_dir=str(tmp_path / "missing"))

        assert connector.is_backend_functional() is False

    async def test_legacy_request_uses_resolved_workspace_without_static_config(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        fake = str(temp_workspace / "fake-agent")
        Path(fake).write_text("noop", encoding="utf-8")
        discover_models = AsyncMock(return_value=["cursor/composer-2"])
        with (
            patch.object(connector, "_check_agent_available", return_value=True),
            patch.object(connector, "_discover_models", discover_models),
        ):
            await connector.initialize(cursor_cli_executable=fake)
            runtime = await connector._acquire_runtime(
                _make_request(options={"project_dir": str(temp_workspace)})
            )

        assert connector.is_backend_functional() is True
        assert connector._default_project_dir is None
        assert runtime.project_dir == temp_workspace.resolve()
        discover_models.assert_awaited_once()

    async def test_initialize_without_workspace_leaves_default_unset(
        self,
        connector: CursorCliAcpConnector,
        tmp_path: Path,
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        project = tmp_path / "already-trusted-repo"
        project.mkdir()
        monkeypatch.chdir(project)
        fake = str(project / "fake-agent")
        Path(fake).write_text("noop", encoding="utf-8")
        with (
            patch.object(connector, "_check_agent_available", return_value=True),
            patch.object(connector, "_discover_models", return_value=["cursor/a"]),
        ):
            await connector.initialize(cursor_cli_executable=fake)

        assert connector.is_backend_functional() is True
        assert connector._default_project_dir is None

    async def test_different_workspaces_spawn_separate_runtimes(
        self, connector: CursorCliAcpConnector, tmp_path: Path
    ) -> None:
        first = tmp_path / "worktree-a"
        second = tmp_path / "worktree-b"
        first.mkdir()
        second.mkdir()
        fake = str(tmp_path / "fake-agent")
        Path(fake).write_text("noop", encoding="utf-8")
        discover_models = AsyncMock(return_value=["cursor/composer-2"])
        with (
            patch.object(connector, "_check_agent_available", return_value=True),
            patch.object(connector, "_discover_models", discover_models),
        ):
            await connector.initialize(cursor_cli_executable=fake)
            runtime_a = await connector._acquire_runtime(
                _make_request(options={"project_dir": str(first)})
            )
            runtime_b = await connector._acquire_runtime(
                _make_request(options={"project_dir": str(second)})
            )

        assert runtime_a is not runtime_b
        assert runtime_a.project_dir == first.resolve()
        assert runtime_b.project_dir == second.resolve()
        assert len(connector._runtimes) == 2

    def test_legacy_request_falls_back_to_process_cwd(
        self,
        connector: CursorCliAcpConnector,
        tmp_path: Path,
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        project = tmp_path / "cli-cwd"
        project.mkdir()
        monkeypatch.chdir(project)
        connector._default_project_dir = None

        resolved = connector._resolve_project_dir_for_request(_make_request())

        assert resolved == project.resolve()

    def test_request_workspace_overrides_instance_default(
        self, connector: CursorCliAcpConnector, tmp_path: Path
    ) -> None:
        instance_default = tmp_path / "instance-default"
        request_dir = tmp_path / "worktree"
        instance_default.mkdir()
        request_dir.mkdir()
        connector._default_project_dir = instance_default.resolve()

        resolved = connector._resolve_project_dir_for_request(
            _make_request(options={"project_dir": str(request_dir)})
        )

        assert resolved == request_dir.resolve()

    def test_responses_request_uses_process_cwd_when_unconfigured(
        self,
        connector: CursorCliAcpConnector,
        tmp_path: Path,
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        project = tmp_path / "responses-cwd"
        project.mkdir()
        monkeypatch.chdir(project)
        connector._default_project_dir = None

        resolved = connector._resolve_project_dir_for_request(
            _make_request(extra_body={ACP_RESPONSES_TEXT_ONLY_MODE_KEY: True})
        )

        assert resolved == project.resolve()

    async def test_initialize_rejects_relative_dot_workspace_path(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        fake = str(temp_workspace / "fake-agent")
        Path(fake).write_text("noop", encoding="utf-8")
        with (
            patch.object(connector, "_check_agent_available", return_value=True),
            patch.object(connector, "_discover_models", return_value=["cursor/x"]),
            pytest.raises(ConfigurationError),
        ):
            await connector.initialize(
                cursor_cli_executable=fake,
                workspace_path=".",
            )

        assert connector.is_backend_functional() is False

    async def test_permission_requests_are_always_rejected_for_text_only_mode(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._auto_accept = True
        runtime = connector._create_runtime(temp_workspace, "glm-5.2-max")
        runtime.responses_text_only_mode = True
        runtime.process = MagicMock()
        runtime.process.stdin = MagicMock()
        written: list[bytes] = []

        def capture_write(data: bytes) -> int:
            written.append(data)
            return len(data)

        runtime.process.stdin.write.side_effect = capture_write
        runtime.process.stdin.flush = MagicMock()

        await connector._handle_server_request(
            runtime,
            ACPNotification(method="session/request_permission", id=77, params={}),
        )

        payload = json.loads(written[-1].decode("utf-8"))
        assert payload["result"]["outcome"]["optionId"] == "reject-once"


class TestCursorCliAcpRuntimeReuse:
    def test_responses_request_uses_dynamic_workspace(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._default_project_dir = None
        request = _make_request(
            extra_body={ACP_RESPONSES_TEXT_ONLY_MODE_KEY: True},
            options={"project_dir": str(temp_workspace)},
        )

        resolved = connector._resolve_project_dir_for_request(request)

        assert resolved == temp_workspace.resolve()

    def test_responses_request_uses_static_trusted_workspace(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._default_project_dir = temp_workspace.resolve()

        resolved = connector._resolve_project_dir_for_request(
            _make_request(extra_body={ACP_RESPONSES_TEXT_ONLY_MODE_KEY: True})
        )

        assert resolved == temp_workspace.resolve()

    async def test_failed_standalone_turn_retires_runtime_from_pool(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        request = _make_request(
            extra_body={
                ACP_RESPONSES_TEXT_ONLY_MODE_KEY: True,
                ACP_RESPONSES_STANDALONE_MODE_KEY: True,
            }
        )
        kill_mock = AsyncMock()

        with (
            patch.object(
                BaseAcpConnector,
                "_prepare_turn_request_locked",
                new=AsyncMock(side_effect=BackendError(message="handshake failed")),
            ),
            patch.object(connector, "_kill_runtime", kill_mock),
        ):
            for index in range(3):
                runtime = connector._create_runtime(
                    temp_workspace,
                    "glm-5.2-max",
                    f"acp-responses-failed-{index}",
                )
                key = connector._build_runtime_key(
                    temp_workspace,
                    "glm-5.2-max",
                    runtime.client_session_id,
                    responses_text_only=True,
                )
                runtime.responses_standalone_mode = True
                async with connector._runtime_pool_lock:
                    connector._runtimes[key] = runtime

                with pytest.raises(BackendError, match="handshake failed"):
                    await connector._prepare_turn_request_locked(runtime, request)

                assert key not in connector._runtimes

        assert kill_mock.await_count == 3

    async def test_standalone_responses_runtime_is_expired_after_turn(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        runtime = connector._create_runtime(
            temp_workspace,
            "glm-5.2-max",
            "acp-responses-standalone",
        )
        runtime.responses_text_only_mode = True
        runtime.responses_standalone_mode = True
        key = connector._build_runtime_key(
            temp_workspace,
            "glm-5.2-max",
            runtime.client_session_id,
            responses_text_only=True,
        )
        async with connector._runtime_pool_lock:
            connector._runtimes[key] = runtime

        with patch.object(connector, "_kill_runtime", AsyncMock()) as kill_mock:
            await connector._schedule_stale_kill_after_turn(runtime)

        kill_mock.assert_awaited_once_with(runtime)
        assert key not in connector._runtimes

    def test_runtime_key_ignores_proxy_session_id(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        first = connector._build_runtime_key(temp_workspace, "composer-2", "b-leg-1")
        second = connector._build_runtime_key(temp_workspace, "composer-2", "b-leg-2")

        assert first == second

    def test_runtime_key_is_scoped_to_workspace(
        self, connector: CursorCliAcpConnector, tmp_path: Path
    ) -> None:
        first = tmp_path / "worktree-a"
        second = tmp_path / "worktree-b"
        first.mkdir()
        second.mkdir()

        key_a = connector._build_runtime_key(first, "composer-2", "default")
        key_b = connector._build_runtime_key(second, "composer-2", "default")

        assert key_a != key_b
        assert key_a[0] == str(first)
        assert key_b[0] == str(second)

    def test_responses_text_only_runtime_is_isolated_from_legacy_chat(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        legacy = connector._build_runtime_key(temp_workspace, "glm-5.2-max", "default")
        responses = connector._build_runtime_key(
            temp_workspace,
            "glm-5.2-max",
            "responses-text-only",
            responses_text_only=True,
        )

        assert legacy != responses

    def test_client_session_id_cannot_select_responses_runtime_mode(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        legacy = connector._build_runtime_key(
            temp_workspace,
            "glm-5.2-max",
            "default",
            responses_text_only=False,
        )
        client_collision = connector._build_runtime_key(
            temp_workspace,
            "glm-5.2-max",
            "responses-text-only",
        )

        assert client_collision == legacy

    def test_responses_runtime_key_is_scoped_to_conversation(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        first = connector._build_runtime_key(
            temp_workspace,
            "glm-5.2-max",
            "conversation-a",
            responses_text_only=True,
        )
        first_retry = connector._build_runtime_key(
            temp_workspace,
            "glm-5.2-max",
            "conversation-a",
            responses_text_only=True,
        )
        second = connector._build_runtime_key(
            temp_workspace,
            "glm-5.2-max",
            "conversation-b",
            responses_text_only=True,
        )

        assert first == first_retry
        assert first != second

    async def test_shutdown_terminates_runtimes_for_every_workspace(
        self, connector: CursorCliAcpConnector, tmp_path: Path
    ) -> None:
        first = tmp_path / "worktree-a"
        second = tmp_path / "worktree-b"
        first.mkdir()
        second.mkdir()
        runtime_a = connector._create_runtime(first, "composer-2")
        runtime_b = connector._create_runtime(second, "composer-2")
        proc_a = MagicMock()
        proc_a.poll.return_value = None
        proc_b = MagicMock()
        proc_b.poll.return_value = None
        runtime_a.process = proc_a
        runtime_b.process = proc_b
        key_a = connector._build_runtime_key(first, "composer-2", "default")
        key_b = connector._build_runtime_key(second, "composer-2", "default")
        async with connector._runtime_pool_lock:
            connector._runtimes[key_a] = runtime_a
            connector._runtimes[key_b] = runtime_b

        with patch.object(connector, "_terminate_process", AsyncMock()) as terminate:
            await connector.shutdown()

        assert {call.args[0] for call in terminate.await_args_list} == {proc_a, proc_b}
        assert connector._runtimes == {}
        assert runtime_a.process is None
        assert runtime_b.process is None

    async def test_responses_text_only_runtime_command_uses_ask_mode(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._cursor_cli_executable = "agent"
        runtime = connector._create_runtime(
            temp_workspace,
            "glm-5.2-max",
            "responses-text-only",
        )
        runtime.responses_text_only_mode = True

        command = await connector._build_subprocess_command(runtime)

        assert command[command.index("--mode") + 1] == "ask"
        assert command[command.index("--model") + 1] == "glm-5.2-max"

    async def test_unadvertised_exact_model_is_rejected_without_substitution(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector.is_functional = True
        connector._initialization_failed = False
        connector._validation_errors = []
        connector._default_project_dir = temp_workspace
        connector._cached_models = ["cursor/glm-5.2-max"]
        connector._models_cache_fetched_at = time.monotonic()

        with pytest.raises(BackendError) as exc_info:
            await connector._acquire_runtime(
                _make_request(model="cursor/grok-4.5-xhigh")
            )

        assert exc_info.value.details["code"] == "cursor_model_unavailable"
        assert exc_info.value.details["requested_model"] == "cursor/grok-4.5-xhigh"
        assert "glm-5.2-max" not in exc_info.value.message

    async def test_empty_catalog_with_usable_auth_allows_requested_model(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector.is_functional = True
        connector._initialization_failed = False
        connector._validation_errors = []
        connector._default_project_dir = temp_workspace
        connector._cached_models = []
        connector._model_cli_ids = {}
        connector._models_cache_fetched_at = time.monotonic()
        connector._auth_policy = CursorAuthPolicy(
            cookie_usable=True,
            env_key_present=False,
            env_key_invalid=False,
            discovery_mode="cookie_only",
            acp_mode="cookie_only",
        )
        expected = connector._create_runtime(temp_workspace, "cursor-grok-4.5-high")

        with patch.object(
            BaseAcpConnector,
            "_acquire_runtime",
            AsyncMock(return_value=expected),
        ) as acquire:
            runtime = await connector._acquire_runtime(
                _make_request(model="cursor/grok-4.5-high")
            )

        assert runtime is expected
        assert acquire.await_args is not None
        passed = acquire.await_args.args[0]
        assert passed.effective_model == "cursor-grok-4.5-high"

    async def test_empty_catalog_without_auth_raises_auth_unavailable(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector.is_functional = True
        connector._initialization_failed = False
        connector._validation_errors = []
        connector._default_project_dir = temp_workspace
        connector._cached_models = []
        connector._models_cache_fetched_at = time.monotonic()
        connector._auth_policy = CursorAuthPolicy(
            cookie_usable=False,
            env_key_present=False,
            env_key_invalid=False,
            discovery_mode="cookie_only",
            acp_mode="cookie_only",
        )

        with pytest.raises(BackendError) as exc_info:
            await connector._acquire_runtime(
                _make_request(model="cursor/grok-4.5-high")
            )

        assert exc_info.value.details["code"] == "cursor_cli_auth_unavailable"

    async def test_subprocess_env_strips_key_in_cookie_mode(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._acp_auth_mode = "cookie_only"
        runtime = connector._create_runtime(temp_workspace, "composer-2")
        with patch.dict(
            "os.environ",
            {"CURSOR_API_KEY": "crsr_test", "CURSOR_AUTH_TOKEN": "tok", "PATH": "x"},
            clear=False,
        ):
            env = connector._subprocess_env(runtime)
        assert "CURSOR_API_KEY" not in env
        assert "CURSOR_AUTH_TOKEN" not in env
        assert env.get("PATH") == "x"

    async def test_cursor_cli_model_id_is_preserved_for_runtime(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector.is_functional = True
        connector._initialization_failed = False
        connector._validation_errors = []
        connector._default_project_dir = temp_workspace
        connector._cached_models = ["cursor/grok-4.5-high"]
        connector._model_cli_ids = {"cursor/grok-4.5-high": "cursor-grok-4.5-high"}
        connector._models_cache_fetched_at = time.monotonic()

        runtime = await connector._acquire_runtime(
            _make_request(model="cursor/grok-4.5-high")
        )

        assert runtime.model == "cursor-grok-4.5-high"

    async def test_history_change_does_not_restart_shared_cursor_runtime(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        runtime = connector._create_runtime(temp_workspace, "composer-2")
        first_messages = [ChatMessage(role="user", content="first")]
        second_messages = [ChatMessage(role="user", content="second")]

        first_text, first_state = await connector._compute_history_and_user_message(
            runtime, first_messages
        )
        runtime.history_state = first_state
        second_text, _ = await connector._compute_history_and_user_message(
            runtime, second_messages
        )

        assert "first" in first_text
        assert second_text == "second"

    async def test_instruction_override_is_sent_on_continuation(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        runtime = connector._create_runtime(temp_workspace, "composer-2")
        first_messages = [
            ChatMessage(role="system", content="Use the original instructions."),
            ChatMessage(role="user", content="first"),
        ]
        second_messages = [
            ChatMessage(role="system", content="Use the replacement instructions."),
            ChatMessage(role="assistant", content="first answer"),
            ChatMessage(role="user", content="second"),
        ]

        _, first_state = await connector._compute_history_and_user_message(
            runtime, first_messages
        )
        runtime.history_state = first_state
        second_text, _ = await connector._compute_history_and_user_message(
            runtime, second_messages
        )

        assert "Use the replacement instructions." in second_text
        assert "second" in second_text

    async def test_instruction_override_reaches_session_prompt_payload(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        runtime = connector._create_runtime(temp_workspace, "composer-2")
        runtime.session_id = "cursor-session-1"
        first_messages = [
            ChatMessage(role="system", content="Use the original instructions."),
            ChatMessage(role="user", content="first"),
        ]
        second_messages = [
            ChatMessage(role="system", content="Use the replacement instructions."),
            ChatMessage(role="assistant", content="first answer"),
            ChatMessage(role="user", content="second"),
        ]
        request_one = _make_request(processed_messages=first_messages)
        request_two = _make_request(processed_messages=second_messages)

        with (
            patch.object(connector, "_spawn_process", AsyncMock()),
            patch.object(connector, "_initialize_runtime", AsyncMock()),
            patch.object(
                connector,
                "_send_jsonrpc_message",
                AsyncMock(side_effect=[1, 2]),
            ) as send_jsonrpc,
        ):
            await connector._prepare_turn_request_locked(runtime, request_one)
            await connector._prepare_turn_request_locked(runtime, request_two)

        prompts = [
            call.args[2]["prompt"][0]["text"] for call in send_jsonrpc.await_args_list
        ]
        assert "Use the replacement instructions." in prompts[1]
        assert "second" in prompts[1]


class TestCursorCliAcpProtocol:
    async def test_prepare_prompt_uses_existing_auth_without_interactive_login(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._default_project_dir = temp_workspace
        runtime = connector._create_runtime(temp_workspace, "composer-2")
        runtime.process = MagicMock()

        with (
            patch.object(connector, "_spawn_process", AsyncMock()),
            patch.object(
                connector,
                "_send_jsonrpc_message",
                AsyncMock(side_effect=[1, 2, 3, 4]),
            ) as send_jsonrpc,
            patch.object(
                connector,
                "_await_response",
                AsyncMock(
                    side_effect=[
                        ACPNotification(id=1, result={"protocolVersion": 1}),
                        ACPNotification(
                            id=2,
                            result={
                                "sessionId": "sid-1",
                                "configOptions": [{"id": "fast"}],
                            },
                        ),
                        ACPNotification(
                            id=3,
                            result={
                                "configOptions": [
                                    {"id": "fast", "currentValue": "false"}
                                ]
                            },
                        ),
                    ]
                ),
            ),
        ):
            prompt_request_id, requested_model = (
                await connector._prepare_turn_request_locked(runtime, _make_request())
            )

        assert prompt_request_id == 4
        assert requested_model == "cursor/composer-2"
        assert runtime.session_id == "sid-1"
        assert runtime.initialized is True

        methods = [c.args[1] for c in send_jsonrpc.await_args_list]
        assert methods == [
            "initialize",
            "session/new",
            "session/set_config_option",
            "session/prompt",
        ]
        initialize_params = send_jsonrpc.await_args_list[0].args[2]
        assert initialize_params["clientCapabilities"]["_meta"] == {
            "parameterizedModelPicker": True
        }

    async def test_high_model_selects_standard_speed_before_prompt(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        runtime = connector._create_runtime(
            temp_workspace, "cursor-grok-4.6-high", "speed-standard"
        )
        runtime.process = MagicMock()

        with (
            patch.object(connector, "_spawn_process", AsyncMock()),
            patch.object(
                connector,
                "_send_jsonrpc_message",
                AsyncMock(side_effect=[1, 2, 3, 4]),
            ) as send_jsonrpc,
            patch.object(
                connector,
                "_await_response",
                AsyncMock(
                    side_effect=[
                        ACPNotification(id=1, result={"protocolVersion": 1}),
                        ACPNotification(
                            id=2,
                            result={
                                "sessionId": "sid-standard",
                                "configOptions": [
                                    {"id": "fast", "currentValue": "true"}
                                ],
                            },
                        ),
                        ACPNotification(
                            id=3,
                            result={
                                "configOptions": [
                                    {"id": "fast", "currentValue": "false"}
                                ]
                            },
                        ),
                    ]
                ),
            ),
        ):
            prompt_request_id, _ = await connector._prepare_turn_request_locked(
                runtime, _make_request(model="cursor/grok-4.6-high")
            )

        assert prompt_request_id == 4
        methods = [call.args[1] for call in send_jsonrpc.await_args_list]
        assert methods == [
            "initialize",
            "session/new",
            "session/set_config_option",
            "session/prompt",
        ]
        set_params = send_jsonrpc.await_args_list[2].args[2]
        assert set_params == {
            "sessionId": "sid-standard",
            "configId": "fast",
            "value": "false",
        }

    async def test_fast_model_selects_fast_speed(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        runtime = connector._create_runtime(temp_workspace, "cursor-grok-4.6-fast")
        runtime.process = MagicMock()

        with (
            patch.object(
                connector, "_send_jsonrpc_message", AsyncMock(side_effect=[1, 2, 3])
            ) as send_jsonrpc,
            patch.object(
                connector,
                "_await_response",
                AsyncMock(
                    side_effect=[
                        ACPNotification(id=1, result={"protocolVersion": 1}),
                        ACPNotification(
                            id=2,
                            result={
                                "sessionId": "sid-fast",
                                "configOptions": {"fast": {"id": "fast"}},
                            },
                        ),
                        ACPNotification(
                            id=3,
                            result={
                                "configOptions": {
                                    "fast": {"id": "fast", "currentValue": True}
                                }
                            },
                        ),
                    ]
                ),
            ),
        ):
            await connector._perform_handshake(runtime)

        assert send_jsonrpc.await_args_list[2].args[2]["value"] == "true"
        assert runtime.initialized is True

    async def test_speed_tier_fails_when_fast_option_is_missing(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        runtime = connector._create_runtime(temp_workspace, "cursor-grok-4.6-high")
        runtime.process = MagicMock()

        with (
            patch.object(
                connector, "_send_jsonrpc_message", AsyncMock(side_effect=[1, 2])
            ),
            patch.object(
                connector,
                "_await_response",
                AsyncMock(
                    side_effect=[
                        ACPNotification(id=1, result={"protocolVersion": 1}),
                        ACPNotification(id=2, result={"sessionId": "sid-no-fast"}),
                    ]
                ),
            ),
            pytest.raises(
                BackendError, match="did not expose the parameterized fast option"
            ) as exc_info,
        ):
            await connector._perform_handshake(runtime)

        assert exc_info.value.details["code"] == "cursor_speed_tier_unavailable"
        assert runtime.initialized is False

    async def test_speed_tier_fails_when_cursor_rejects_selection(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        runtime = connector._create_runtime(temp_workspace, "cursor-grok-4.6-high")
        runtime.process = MagicMock()

        with (
            patch.object(
                connector, "_send_jsonrpc_message", AsyncMock(side_effect=[1, 2, 3])
            ),
            patch.object(
                connector,
                "_await_response",
                AsyncMock(
                    side_effect=[
                        ACPNotification(id=1, result={"protocolVersion": 1}),
                        ACPNotification(
                            id=2,
                            result={
                                "sessionId": "sid-reject",
                                "configOptions": [{"id": "fast"}],
                            },
                        ),
                        ACPNotification(
                            id=3,
                            error=ACPError(code=-32602, message="unsupported option"),
                        ),
                    ]
                ),
            ),
            pytest.raises(
                BackendError, match="rejected the requested fast tier"
            ) as exc_info,
        ):
            await connector._perform_handshake(runtime)

        assert exc_info.value.details["code"] == "cursor_speed_tier_rejected"
        assert runtime.initialized is False

    async def test_speed_tier_fails_when_cursor_confirms_mismatch(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        runtime = connector._create_runtime(temp_workspace, "cursor-grok-4.6-high")
        runtime.process = MagicMock()

        with (
            patch.object(
                connector, "_send_jsonrpc_message", AsyncMock(side_effect=[1, 2, 3])
            ),
            patch.object(
                connector,
                "_await_response",
                AsyncMock(
                    side_effect=[
                        ACPNotification(id=1, result={"protocolVersion": 1}),
                        ACPNotification(
                            id=2,
                            result={
                                "sessionId": "sid-mismatch",
                                "configOptions": [
                                    {"id": "fast", "currentValue": "true"}
                                ],
                            },
                        ),
                        ACPNotification(
                            id=3,
                            result={
                                "configOptions": [
                                    {"id": "fast", "currentValue": "true"}
                                ]
                            },
                        ),
                    ]
                ),
            ),
            pytest.raises(
                BackendError, match="did not confirm the requested fast tier"
            ) as exc_info,
        ):
            await connector._perform_handshake(runtime)

        assert exc_info.value.details["code"] == "cursor_speed_tier_mismatch"
        assert exc_info.value.details["effective_value"] == "true"
        assert runtime.initialized is False

    async def test_prepare_prompt_records_standalone_responses_marker(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        runtime = connector._create_runtime(
            temp_workspace, "composer-2", "acp-responses-one-shot"
        )
        runtime.process = MagicMock()
        request = _make_request(
            extra_body={
                ACP_RESPONSES_TEXT_ONLY_MODE_KEY: True,
                ACP_RESPONSES_STANDALONE_MODE_KEY: True,
            }
        )

        with (
            patch.object(connector, "_spawn_process", AsyncMock()),
            patch.object(
                connector,
                "_send_jsonrpc_message",
                AsyncMock(side_effect=[1, 2, 3, 4]),
            ),
            patch.object(
                connector,
                "_await_response",
                AsyncMock(
                    side_effect=[
                        ACPNotification(id=1, result={"protocolVersion": 1}),
                        ACPNotification(
                            id=2,
                            result={
                                "sessionId": "sid-1",
                                "configOptions": [{"id": "fast"}],
                            },
                        ),
                        ACPNotification(
                            id=3,
                            result={
                                "configOptions": [
                                    {"id": "fast", "currentValue": "false"}
                                ]
                            },
                        ),
                    ]
                ),
            ),
        ):
            await connector._prepare_turn_request_locked(runtime, request)

        assert runtime.responses_text_only_mode is True
        assert runtime.responses_standalone_mode is True

    def test_is_server_request(self) -> None:
        assert ACPNotification(
            method="session/request_permission", id=9, params={}
        ).is_server_request
        assert not ACPNotification(id=1, result={"ok": True}).is_server_request
        assert not ACPNotification(method="session/update", params={}).is_server_request

    async def test_handle_server_request_permission_auto_accepts_for_legacy_chat(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._auto_accept = True
        runtime = connector._create_runtime(temp_workspace, "composer-2")
        runtime.process = MagicMock()
        runtime.process.stdin = MagicMock()

        written: list[bytes] = []

        def capture_write(data: bytes) -> int:
            written.append(data)
            return len(data)

        runtime.process.stdin.write.side_effect = capture_write
        runtime.process.stdin.flush = MagicMock()

        msg = ACPNotification(method="session/request_permission", id=42, params={})
        await connector._handle_server_request(runtime, msg)

        line = written[-1].decode("utf-8").strip()
        payload = json.loads(line)
        assert payload["jsonrpc"] == "2.0"
        assert payload["id"] == 42
        assert payload["result"]["outcome"]["optionId"] == "allow-always"

    async def test_handle_cursor_ask_question_skipped(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        runtime = connector._create_runtime(temp_workspace, "composer-2")
        runtime.process = MagicMock()
        runtime.process.stdin = MagicMock()
        written: list[bytes] = []

        def capture_write(data: bytes) -> int:
            written.append(data)
            return len(data)

        runtime.process.stdin.write.side_effect = capture_write
        runtime.process.stdin.flush = MagicMock()

        msg = ACPNotification(method="cursor/ask_question", id=7, params={})
        await connector._handle_server_request(runtime, msg)

        line = written[-1].decode("utf-8").strip()
        payload = json.loads(line)
        assert payload["id"] == 7
        assert payload["result"]["outcome"]["outcome"] == "skipped"


class TestCursorCliAcpChatCompletions:
    async def test_chat_completions_not_initialized(
        self, connector: CursorCliAcpConnector
    ) -> None:
        with pytest.raises(ServiceUnavailableError):
            await connector.chat_completions(_make_request())

    async def test_streaming_yields_done(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector.is_functional = True
        connector._initialization_failed = False
        connector._validation_errors = []
        connector._default_project_dir = temp_workspace

        runtime = connector._create_runtime(temp_workspace, "composer-2")
        runtime.request_lock = asyncio.Lock()

        async def fake_prepare(*_: Any, **__: Any) -> tuple[int, str]:
            return 99, "cursor/composer-2"

        async def fake_stream(*_: Any, **__: Any):
            yield ProcessedResponse(content='data: {"x":1}\n\n')

        with (
            patch.object(
                connector,
                "_acquire_runtime",
                AsyncMock(return_value=runtime),
            ),
            patch.object(
                connector,
                "_prepare_turn_request_locked",
                new=fake_prepare,
            ),
            patch.object(
                connector,
                "_stream_response_with_lock",
                new=fake_stream,
            ),
        ):
            out = await connector.chat_completions(_make_request(stream=True))

        assert isinstance(out, StreamingResponseEnvelope)


class TestCursorCliAcpRuntime:
    def test_runtime_locks(
        self, connector: CursorCliAcpConnector, temp_workspace: Path
    ):
        rt = connector._create_runtime(temp_workspace, "composer-2")
        assert rt.process_lock is not None
        assert rt.request_lock is not None


class TestCursorCliDualAuthDiscovery:
    async def test_discovery_retries_with_env_key_after_cookie_auth_failure(
        self, temp_workspace: Path
    ) -> None:
        del temp_workspace
        cookie_policy = CursorAuthPolicy(
            cookie_usable=True,
            env_key_present=True,
            env_key_invalid=False,
            discovery_mode="cookie_only",
            acp_mode="cookie_only",
        )
        auth_fail = MagicMock(
            returncode=1,
            stdout="",
            stderr=(
                "Error: Authentication required. Run 'agent login', "
                "pass --api-key/--auth-token, or set CURSOR_API_KEY/CURSOR_AUTH_TOKEN."
            ),
        )
        key_ok = MagicMock(
            returncode=0,
            stdout="Available models\n\nauto - Auto\nglm-5.2-max - GLM\n",
            stderr="",
        )

        with (
            patch(
                "src.connectors.cursor_cli_acp.resolve_cursor_auth_policy",
                return_value=cookie_policy,
            ),
            patch(
                "src.connectors.cursor_cli_acp.subprocess.run",
                side_effect=[auth_fail, key_ok],
            ) as run,
            patch.dict(
                "os.environ",
                {"CURSOR_API_KEY": "crsr_valid_test_key"},
                clear=False,
            ),
        ):
            catalog = await discover_cursor_cli_model_catalog(
                executable="agent",
                cursor_api_endpoint=None,
                extra_args=None,
                workspace=None,
                timeout_seconds=10.0,
            )

        assert catalog.routes == ("cursor/auto", "cursor/glm-5.2-max")
        assert catalog.discovery_mode == "with_env_key"
        assert catalog.auth_policy is not None
        assert catalog.auth_policy.acp_mode == "cookie_only"
        assert run.call_count == 2
        assert "CURSOR_API_KEY" not in run.call_args_list[0].kwargs["env"]
        assert (
            run.call_args_list[1].kwargs["env"].get("CURSOR_API_KEY")
            == "crsr_valid_test_key"
        )

    async def test_discovery_retries_with_login_store_key_when_env_key_absent(
        self, temp_workspace: Path
    ) -> None:
        del temp_workspace
        cookie_policy = CursorAuthPolicy(
            cookie_usable=True,
            env_key_present=False,
            env_key_invalid=False,
            discovery_mode="cookie_only",
            acp_mode="cookie_only",
            login_store_key_present=True,
        )
        auth_fail = MagicMock(
            returncode=1,
            stdout="",
            stderr=(
                "Error: Authentication required. Run 'agent login', "
                "pass --api-key/--auth-token, or set CURSOR_API_KEY/CURSOR_AUTH_TOKEN."
            ),
        )
        key_ok = MagicMock(
            returncode=0,
            stdout="Available models\n\ncomposer-2 - Composer 2\n",
            stderr="",
        )
        base_env = {
            k: v
            for k, v in __import__("os").environ.items()
            if k not in ("CURSOR_API_KEY", "CURSOR_AUTH_TOKEN")
        }

        with (
            patch(
                "src.connectors.cursor_cli_acp.resolve_cursor_auth_policy",
                return_value=cookie_policy,
            ),
            patch(
                "src.connectors.cursor_cli_acp.read_cursor_login_store_api_key",
                return_value="crsr_from_auth_json",
            ),
            patch(
                "src.connectors.cursor_cli_acp.subprocess.run",
                side_effect=[auth_fail, key_ok],
            ) as run,
            patch.dict("os.environ", base_env, clear=True),
        ):
            catalog = await discover_cursor_cli_model_catalog(
                executable="agent",
                cursor_api_endpoint=None,
                workspace=None,
                timeout_seconds=10.0,
            )

        assert catalog.routes == ("cursor/composer-2",)
        assert catalog.discovery_mode == "with_env_key"
        assert catalog.auth_policy is not None
        assert catalog.auth_policy.acp_mode == "cookie_only"
        assert "CURSOR_API_KEY" not in run.call_args_list[0].kwargs["env"]
        assert (
            run.call_args_list[1].kwargs["env"].get("CURSOR_API_KEY")
            == "crsr_from_auth_json"
        )

    async def test_cookie_only_list_models_auth_failure_is_debug_when_logged_in(
        self, caplog: pytest.LogCaptureFixture
    ) -> None:
        import logging

        policy = CursorAuthPolicy(
            cookie_usable=True,
            env_key_present=False,
            env_key_invalid=False,
            discovery_mode="cookie_only",
            acp_mode="cookie_only",
            login_store_key_present=False,
        )
        auth_fail = MagicMock(
            returncode=1,
            stdout="",
            stderr="Error: Authentication required. Run 'agent login'",
        )
        with (
            patch(
                "src.connectors.cursor_cli_acp.resolve_cursor_auth_policy",
                return_value=policy,
            ),
            patch(
                "src.connectors.cursor_cli_acp.read_cursor_login_store_api_key",
                return_value=None,
            ),
            patch(
                "src.connectors.cursor_cli_acp.subprocess.run",
                return_value=auth_fail,
            ),
            caplog.at_level(logging.DEBUG, logger="src.connectors.cursor_cli_acp"),
        ):
            catalog = await discover_cursor_cli_model_catalog(
                executable="agent",
                cursor_api_endpoint=None,
                workspace=None,
                timeout_seconds=10.0,
            )

        assert catalog.routes == ()
        assert not any(
            "Authentication required" in r.message and r.levelno >= logging.WARNING
            for r in caplog.records
        )
        assert any(
            "auth remains usable via cookie=" in r.message
            and r.levelno == logging.DEBUG
            for r in caplog.records
        )
        assert any(
            "empty catalog while auth remains usable" in r.message
            and r.levelno == logging.INFO
            for r in caplog.records
        )

    async def test_cookie_only_auth_failure_is_debug_when_env_key_fallback_exists(
        self, caplog: pytest.LogCaptureFixture
    ) -> None:
        import logging

        policy = CursorAuthPolicy(
            cookie_usable=False,
            env_key_present=True,
            env_key_invalid=False,
            discovery_mode="with_env_key",
            acp_mode="with_env_key",
        )
        auth_fail = MagicMock(
            returncode=1,
            stdout="",
            stderr="Error: Authentication required. Run 'agent login'",
        )
        key_ok = MagicMock(
            returncode=0,
            stdout="Available models\n\nauto - Auto\n",
            stderr="",
        )
        with (
            patch(
                "src.connectors.cursor_cli_acp.resolve_cursor_auth_policy",
                return_value=policy,
            ),
            patch(
                "src.connectors.cursor_cli_acp.read_cursor_login_store_api_key",
                return_value=None,
            ),
            patch(
                "src.connectors.cursor_cli_acp.subprocess.run",
                side_effect=[auth_fail, key_ok],
            ),
            patch.dict(
                "os.environ",
                {"CURSOR_API_KEY": "crsr_valid_test_key"},
                clear=False,
            ),
            caplog.at_level(logging.DEBUG, logger="src.connectors.cursor_cli_acp"),
        ):
            catalog = await discover_cursor_cli_model_catalog(
                executable="agent",
                cursor_api_endpoint=None,
                workspace=None,
                timeout_seconds=10.0,
            )

        assert catalog.routes == ("cursor/auto",)
        assert catalog.discovery_mode == "with_env_key"
        assert not any(r.levelno >= logging.WARNING for r in caplog.records)
        assert any(
            "auth remains usable via cookie=" in r.message
            and r.levelno == logging.DEBUG
            for r in caplog.records
        )

    async def test_discovery_cookie_only_success_keeps_acp_cookie_mode(self) -> None:
        policy = CursorAuthPolicy(
            cookie_usable=True,
            env_key_present=True,
            env_key_invalid=False,
            discovery_mode="cookie_only",
            acp_mode="cookie_only",
        )
        ok = MagicMock(
            returncode=0,
            stdout="Available models\n\ncomposer-2 - Composer 2\n",
            stderr="",
        )
        with (
            patch(
                "src.connectors.cursor_cli_acp.resolve_cursor_auth_policy",
                return_value=policy,
            ),
            patch(
                "src.connectors.cursor_cli_acp.subprocess.run",
                return_value=ok,
            ) as run,
            patch.dict(
                "os.environ",
                {"CURSOR_API_KEY": "crsr_valid_test_key"},
                clear=False,
            ),
        ):
            catalog = await discover_cursor_cli_model_catalog(
                executable="agent",
                cursor_api_endpoint=None,
                workspace=None,
                timeout_seconds=10.0,
            )

        assert catalog.discovery_mode == "cookie_only"
        assert catalog.auth_policy is not None
        assert catalog.auth_policy.acp_mode == "cookie_only"
        assert run.call_count == 1
        assert "CURSOR_API_KEY" not in run.call_args.kwargs["env"]

    async def test_invalid_env_key_does_not_block_cookie_discovery(self) -> None:
        policy = CursorAuthPolicy(
            cookie_usable=True,
            env_key_present=True,
            env_key_invalid=False,
            discovery_mode="cookie_only",
            acp_mode="cookie_only",
        )
        cookie_ok = MagicMock(
            returncode=0,
            stdout="Available models\n\ncomposer-2 - Composer 2\n",
            stderr="",
        )
        with (
            patch(
                "src.connectors.cursor_cli_acp.resolve_cursor_auth_policy",
                return_value=policy,
            ),
            patch(
                "src.connectors.cursor_cli_acp.subprocess.run",
                return_value=cookie_ok,
            ),
            patch.dict(
                "os.environ",
                {"CURSOR_API_KEY": "crsr_invalid"},
                clear=False,
            ),
        ):
            catalog = await discover_cursor_cli_model_catalog(
                executable="agent",
                cursor_api_endpoint=None,
                workspace=None,
                timeout_seconds=10.0,
            )

        assert catalog.routes == ("cursor/composer-2",)
        assert catalog.auth_policy is not None
        assert catalog.auth_policy.acp_mode == "cookie_only"

    def test_resolve_cursor_cli_model_id_empty_catalog_uses_cursor_prefix(self) -> None:
        assert (
            resolve_cursor_cli_model_id(
                "cursor/grok-4.5-high",
                cli_ids={},
                requested_model="grok-4.5-high",
            )
            == "cursor-grok-4.5-high"
        )

    def test_resolve_cursor_cli_model_id_uses_catalog_id_when_fast_variant_exists(
        self,
    ) -> None:
        cli_ids = {
            "cursor/composer-2.5": "composer-2.5",
            "cursor/composer-2.5-fast": "composer-2.5-fast",
        }

        assert (
            resolve_cursor_cli_model_id(
                "cursor/composer-2.5",
                cli_ids=cli_ids,
                requested_model="composer-2.5",
            )
            == "composer-2.5"
        )
        assert (
            resolve_cursor_cli_model_id(
                "cursor/composer-2.5-fast",
                cli_ids=cli_ids,
                requested_model="composer-2.5-fast",
            )
            == "composer-2.5-fast"
        )

    def test_build_cursor_cli_env_modes(self) -> None:
        base = {
            "PATH": "/bin",
            "CURSOR_API_KEY": "crsr_x",
            "CURSOR_AUTH_TOKEN": "tok",
        }
        cookie = build_cursor_cli_env("cookie_only", base=base)
        keyed = build_cursor_cli_env("with_env_key", base=base)
        assert "CURSOR_API_KEY" not in cookie
        assert "CURSOR_AUTH_TOKEN" not in cookie
        assert cookie["PATH"] == "/bin"
        assert keyed["CURSOR_API_KEY"] == "crsr_x"
        assert keyed["CURSOR_AUTH_TOKEN"] == "tok"
