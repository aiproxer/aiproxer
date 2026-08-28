from __future__ import annotations

from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest
from pydantic.types import JsonValue
from src.connectors.acp_core.types import ACPNotification
from src.connectors.contracts import ConnectorChatCompletionsRequest
from src.connectors.freebuff_cli_acp import (
    DEFAULT_FREEBUFF_PROCESS_TIMEOUT_SECONDS,
    FreebuffCliAcpConnector,
    FreebuffCliConfiguredModelEnumerator,
    build_freebuff_acp_wrapper_command,
    build_freebuff_model_catalog_command,
    parse_freebuff_wrapper_model_catalog,
    resolve_freebuff_acp_wrapper_executable,
    run_freebuff_wrapper_probe,
)
from src.core.common.exceptions import ConfigurationError
from src.core.config.app_config import AppConfig, BackendConfig
from src.core.domain.chat import CanonicalChatRequest, ChatMessage
from src.core.services.model_capability_index import ModelCapabilityIndex
from src.core.services.translation_service import TranslationService


@pytest.fixture
def temp_workspace(tmp_path: Path) -> Path:
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    return workspace


@pytest.fixture
def connector() -> FreebuffCliAcpConnector:
    client = AsyncMock(spec=httpx.AsyncClient)
    return FreebuffCliAcpConnector(client, AppConfig(), TranslationService())


def _make_request(
    *,
    stream: bool = False,
    options: dict[str, JsonValue] | None = None,
    model: str = "mimo/mimo-v2.5",
) -> ConnectorChatCompletionsRequest:
    request = CanonicalChatRequest(
        model=model,
        stream=stream,
        messages=[ChatMessage(role="user", content="hello")],
    )
    return ConnectorChatCompletionsRequest(
        request=request,
        processed_messages=[ChatMessage(role="user", content="hello")],
        effective_model=model,
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=None,
        options=options or {},
    )


class TestFreebuffCliAcpHelpers:
    async def test_wrapper_probe_does_not_require_asyncio_subprocess_support(
        self,
    ) -> None:
        process = MagicMock()
        process.communicate.return_value = (b"dev\n", b"")
        process.returncode = 0

        with (
            patch(
                "asyncio.create_subprocess_exec",
                AsyncMock(side_effect=NotImplementedError),
            ),
            patch(
                "src.connectors.freebuff_cli_acp.subprocess.Popen",
                return_value=process,
            ) as popen,
        ):
            result = await run_freebuff_wrapper_probe(
                ["go-freebuff-acp-wrapper.exe", "--version"],
                timeout=1,
            )

        assert result == (0, b"dev\n", b"")
        popen.assert_called_once_with(
            ["go-freebuff-acp-wrapper.exe", "--version"],
            stdout=-1,
            stderr=-1,
            shell=False,
        )

    def test_build_freebuff_acp_wrapper_command_full(self) -> None:
        cmd = build_freebuff_acp_wrapper_command(
            r"C:\tools\go-freebuff-acp-wrapper.exe",
            model="mimo/mimo-v2.5",
            termctrl_binary=r"C:\tools\termctrl.exe",
            freebuff_binary=r"C:\tools\freebuff.exe",
            lock_dir=r"C:\locks",
            timeout_seconds=300,
            pace_ms=35,
            extra_args=["--foo", "bar"],
        )
        assert cmd == [
            r"C:\tools\go-freebuff-acp-wrapper.exe",
            "--default-model",
            "mimo/mimo-v2.5",
            "--termctrl-binary",
            r"C:\tools\termctrl.exe",
            "--freebuff-binary",
            r"C:\tools\freebuff.exe",
            "--lock-dir",
            r"C:\locks",
            "--timeout",
            "300",
            "--pace-ms",
            "35",
            "--foo",
            "bar",
        ]

    def test_build_freebuff_acp_wrapper_command_minimal(self) -> None:
        cmd = build_freebuff_acp_wrapper_command("go-freebuff-acp-wrapper")
        assert cmd == ["go-freebuff-acp-wrapper"]

    def test_build_freebuff_model_catalog_command(self) -> None:
        assert build_freebuff_model_catalog_command("go-freebuff-acp-wrapper.exe") == [
            "go-freebuff-acp-wrapper.exe",
            "--list-models",
        ]

    def test_parse_freebuff_wrapper_model_catalog(self) -> None:
        sample = (
            "mimo/mimo-v2.5\n"
            "deepseek/deepseek-v4-flash\n"
            "openai/gpt-5.6-luna\n"
            "deepseek/deepseek-v4-pro\n"
            "google/gemini-3.7-flash\n"
            "glm/glm-5.2\n"
            "invalid_non_canonical\n"
        )
        models = parse_freebuff_wrapper_model_catalog(sample)
        assert models == [
            "mimo/mimo-v2.5",
            "deepseek/deepseek-v4-flash",
            "openai/gpt-5.6-luna",
            "deepseek/deepseek-v4-pro",
            "google/gemini-3.7-flash",
            "glm/glm-5.2",
        ]

    def test_resolve_wrapper_prefers_existing_file(self, tmp_path: Path) -> None:
        exe = tmp_path / "go-freebuff-acp-wrapper.exe"
        exe.write_text("noop", encoding="utf-8")
        assert resolve_freebuff_acp_wrapper_executable(str(exe)) == str(exe.resolve())

    def test_connector_default_process_timeout(
        self, connector: FreebuffCliAcpConnector
    ) -> None:
        assert connector._process_timeout == DEFAULT_FREEBUFF_PROCESS_TIMEOUT_SECONDS
        assert DEFAULT_FREEBUFF_PROCESS_TIMEOUT_SECONDS == 300.0


class TestFreebuffCliAcpInitialization:
    async def test_initialize_with_project_dir(
        self, connector: FreebuffCliAcpConnector, temp_workspace: Path
    ) -> None:
        fake = str(temp_workspace / "go-freebuff-acp-wrapper.exe")
        Path(fake).write_text("noop", encoding="utf-8")
        with (
            patch.object(connector, "_check_wrapper_available", return_value=True),
            patch.object(
                connector,
                "_discover_models",
                AsyncMock(
                    return_value=["mimo/mimo-v2.5", "deepseek/deepseek-v4-flash"]
                ),
            ),
        ):
            await connector.initialize(
                project_dir=str(temp_workspace),
                wrapper_executable=fake,
                model="mimo/mimo-v2.5",
            )
        assert connector.is_backend_functional() is True
        assert connector._default_project_dir == temp_workspace.resolve()
        assert connector._model == "mimo/mimo-v2.5"
        assert connector._process_timeout == DEFAULT_FREEBUFF_PROCESS_TIMEOUT_SECONDS

    async def test_initialize_requires_existing_workspace(
        self, connector: FreebuffCliAcpConnector, tmp_path: Path
    ) -> None:
        with (
            patch.object(connector, "_check_wrapper_available", return_value=True),
            pytest.raises(ConfigurationError),
        ):
            await connector.initialize(project_dir=str(tmp_path / "missing"))
        assert connector.is_backend_functional() is False

    async def test_initialize_without_workspace_uses_runtime_project_dir(
        self, connector: FreebuffCliAcpConnector, temp_workspace: Path
    ) -> None:
        fake = str(temp_workspace / "go-freebuff-acp-wrapper.exe")
        Path(fake).write_text("noop", encoding="utf-8")
        with (
            patch.object(connector, "_check_wrapper_available", return_value=True),
            patch.object(
                connector,
                "_discover_models",
                AsyncMock(return_value=["mimo/mimo-v2.5"]),
            ),
        ):
            await connector.initialize(
                wrapper_executable=fake,
                models=["mimo/mimo-v2.5"],
            )
        assert connector.is_backend_functional() is True
        assert connector._default_project_dir is None


class TestFreebuffCliAcpProtocol:
    async def test_prepare_prompt_sends_initialize_authenticate_session(
        self, connector: FreebuffCliAcpConnector, temp_workspace: Path
    ) -> None:
        connector._default_project_dir = temp_workspace
        runtime = connector._create_runtime(temp_workspace, "mimo/mimo-v2.5")
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
                        ACPNotification(id=2, result={}),
                        ACPNotification(id=3, result={"sessionId": "sess-fb-1"}),
                    ]
                ),
            ),
        ):
            prompt_request_id, requested_model = (
                await connector._prepare_turn_request_locked(
                    runtime,
                    _make_request(model="mimo/mimo-v2.5"),
                )
            )

        assert prompt_request_id == 4
        assert requested_model == "mimo/mimo-v2.5"
        assert runtime.session_id == "sess-fb-1"
        assert runtime.initialized is True
        assert [c.args[1] for c in send_jsonrpc.await_args_list] == [
            "initialize",
            "authenticate",
            "session/new",
            "session/prompt",
        ]
        assert send_jsonrpc.await_args_list[1].args[2] == {"methodId": "freebuff"}


class TestFreebuffCliConfiguredModelEnumerator:
    async def test_enumerates_wrapper_models_via_probe(self, tmp_path: Path) -> None:
        wrapper = tmp_path / "go-freebuff-acp-wrapper.exe"
        wrapper.write_text("fixture", encoding="utf-8")
        enumerator = FreebuffCliConfiguredModelEnumerator()

        with patch(
            "src.connectors.freebuff_cli_acp.run_freebuff_wrapper_probe",
            AsyncMock(
                return_value=(
                    0,
                    b"mimo/mimo-v2.5\ndeepseek/deepseek-v4-flash\nopenai/gpt-5.6-luna\n",
                    b"",
                )
            ),
        ):
            result = await enumerator.enumerate(
                "freebuff-cli-acp.default",
                BackendConfig(
                    connector="freebuff-cli-acp",
                    extra={
                        "wrapper_executable": str(wrapper),
                    },
                ),
            )

        assert result.models == (
            "mimo/mimo-v2.5",
            "deepseek/deepseek-v4-flash",
            "openai/gpt-5.6-luna",
        )
        assert result.source == "freebuff_wrapper"
        assert result.instance_pinned is True

        index = ModelCapabilityIndex(
            ModelCapabilityIndex.build_snapshot(
                {"freebuff-cli-acp.default": result.models},
                generation=1,
                instance_route_policy={
                    "freebuff-cli-acp.default": "instance_pinned",
                },
            )
        )
        assert index.get_candidates("freebuff-cli-acp:deepseek/deepseek-v4-flash") == [
            "freebuff-cli-acp.default"
        ]
