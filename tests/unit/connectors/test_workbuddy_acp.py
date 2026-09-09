"""Unit tests for WorkBuddy ACP connector."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import AsyncMock, patch

import pytest
from src.connectors.acp_core.types import ACPNotification
from src.connectors.workbuddy_acp import (
    DEFAULT_WORKBUDDY_MODEL,
    WorkBuddyAcpConnector,
    WorkBuddyConfiguredModelEnumerator,
    build_workbuddy_acp_command,
    canonicalize_workbuddy_model_id,
    resolve_workbuddy_cli_path,
    resolve_workbuddy_node_executable,
)
from src.core.common.exceptions import ConfigurationError
from src.core.config.app_config import BackendConfig


class TestWorkBuddyAcpHelpers:
    def test_canonicalize_workbuddy_model_id_aliases(self) -> None:
        assert canonicalize_workbuddy_model_id("primary-model") == "primary-model"
        assert (
            canonicalize_workbuddy_model_id("workbuddy/primary-model")
            == "primary-model"
        )
        assert canonicalize_workbuddy_model_id("hy4") == "primary-model"
        assert canonicalize_workbuddy_model_id("workbuddy/hy4") == "primary-model"
        assert canonicalize_workbuddy_model_id("hy4-preview") == "primary-model"
        assert canonicalize_workbuddy_model_id("hunyuan") == "primary-model"
        assert canonicalize_workbuddy_model_id("hunyuan-4") == "primary-model"
        assert canonicalize_workbuddy_model_id("auto") == "primary-model"
        assert canonicalize_workbuddy_model_id("default") == "primary-model"
        assert canonicalize_workbuddy_model_id("deep") == "deep-model"
        assert canonicalize_workbuddy_model_id("reasoning") == "deep-model"
        assert canonicalize_workbuddy_model_id("balanced") == "balanced-model"
        assert canonicalize_workbuddy_model_id("fast") == "fast-model"
        assert canonicalize_workbuddy_model_id("minimax") == "fast-model"
        assert canonicalize_workbuddy_model_id("") == DEFAULT_WORKBUDDY_MODEL

    def test_build_workbuddy_acp_command(self) -> None:
        cmd = build_workbuddy_acp_command(
            r"C:\bin\node.exe",
            r"C:\bin\codebuddy",
            extra_args=["--verbose"],
        )
        assert cmd == [
            r"C:\bin\node.exe",
            r"C:\bin\codebuddy",
            "--acp",
            "--acp-transport",
            "stdio",
            "--verbose",
        ]

    def test_resolve_workbuddy_node_executable_configured(self, tmp_path: Path) -> None:
        dummy_node = tmp_path / "node.exe"
        dummy_node.write_text("")
        resolved = resolve_workbuddy_node_executable(str(dummy_node))
        assert resolved == str(dummy_node.resolve())

    def test_resolve_workbuddy_cli_path_configured(self, tmp_path: Path) -> None:
        dummy_cli = tmp_path / "codebuddy"
        dummy_cli.write_text("")
        resolved = resolve_workbuddy_cli_path(str(dummy_cli))
        assert resolved == str(dummy_cli.resolve())


class TestWorkBuddyConfiguredModelEnumerator:
    @pytest.mark.asyncio
    async def test_enumerate_available_default(self, tmp_path: Path) -> None:
        dummy_node = tmp_path / "node.exe"
        dummy_node.write_text("")
        dummy_cli = tmp_path / "codebuddy"
        dummy_cli.write_text("")

        enumerator = WorkBuddyConfiguredModelEnumerator()
        cfg = BackendConfig(
            extra={
                "node_executable": str(dummy_node),
                "cli_path": str(dummy_cli),
            }
        )
        res = await enumerator.enumerate("workbuddy_acp", cfg)
        assert res.status == "available"
        assert "workbuddy/primary-model" in res.models
        assert "workbuddy/hy4" in res.models

    @pytest.mark.asyncio
    async def test_enumerate_unavailable_when_executables_missing(self) -> None:
        enumerator = WorkBuddyConfiguredModelEnumerator()
        cfg = BackendConfig(
            extra={
                "node_executable": r"C:\non_existent\node.exe",
                "cli_path": r"C:\non_existent\codebuddy",
            }
        )
        with patch(
            "src.connectors.workbuddy_acp.resolve_workbuddy_node_executable",
            return_value=None,
        ):
            res = await enumerator.enumerate("workbuddy_acp", cfg)
            assert res.status == "unavailable"
            assert res.error_code == "executable_not_found"


class TestWorkBuddyAcpConnector:
    async def _create_mock_connector(self, tmp_path: Path) -> WorkBuddyAcpConnector:
        dummy_node = tmp_path / "node.exe"
        dummy_node.write_text("")
        dummy_cli = tmp_path / "codebuddy"
        dummy_cli.write_text("")

        cfg = BackendConfig(
            extra={
                "node_executable": str(dummy_node),
                "cli_path": str(dummy_cli),
                "model": "hy4",
                "permission_mode": "bypassPermissions",
                "thought_level": "enabled",
            }
        )
        connector = WorkBuddyAcpConnector(cfg)
        with patch.object(
            WorkBuddyAcpConnector, "_check_available_sync", return_value=True
        ):
            await connector.initialize(
                node_executable=str(dummy_node),
                cli_path=str(dummy_cli),
                model="hy4",
                project_dir=tmp_path,
            )
        return connector

    @pytest.mark.asyncio
    async def test_init_success(self, tmp_path: Path) -> None:
        conn = await self._create_mock_connector(tmp_path)
        assert conn.is_functional is True
        assert conn.name == "workbuddy-acp"
        assert conn._model == "primary-model"

    @pytest.mark.asyncio
    async def test_init_fails_when_executables_missing(self, tmp_path: Path) -> None:
        cfg = BackendConfig(
            extra={
                "node_executable": r"C:\non_existent\node.exe",
                "cli_path": r"C:\non_existent\codebuddy",
            }
        )
        conn = WorkBuddyAcpConnector(cfg)
        with (
            patch(
                "src.connectors.workbuddy_acp.resolve_workbuddy_node_executable",
                return_value=None,
            ),
            pytest.raises(ConfigurationError),
        ):
            await conn.initialize(project_dir=tmp_path)

    @pytest.mark.asyncio
    async def test_perform_handshake(self, tmp_path: Path) -> None:
        conn = await self._create_mock_connector(tmp_path)
        runtime = conn._create_runtime(tmp_path, "hy4")

        sent_messages: list[tuple[str, dict]] = []

        async def mock_send(rt, method, params):
            sent_messages.append((method, params))
            return len(sent_messages)

        async def mock_await(rt, req_id):
            if req_id == 1:
                return ACPNotification(
                    jsonrpc="2.0", id=1, result={"protocolVersion": 1}
                )
            if req_id == 2:
                return ACPNotification(
                    jsonrpc="2.0", id=2, result={"sessionId": "test-session-123"}
                )
            if req_id in (3, 4, 5):
                return ACPNotification(
                    jsonrpc="2.0", id=req_id, result={"currentValue": "ok"}
                )
            return ACPNotification(jsonrpc="2.0", id=req_id, result={})

        with (
            patch.object(conn, "_send_jsonrpc_message", side_effect=mock_send),
            patch.object(conn, "_await_response", side_effect=mock_await),
        ):
            await conn._perform_handshake(runtime)

        assert runtime.initialized is True
        assert runtime.session_id == "test-session-123"

        methods = [m[0] for m in sent_messages]
        assert methods == [
            "initialize",
            "session/new",
            "session/set_config_option",
            "session/set_config_option",
            "session/set_config_option",
        ]
        # Check model config was set to canonical primary-model
        assert sent_messages[2][1]["configId"] == "model"
        assert sent_messages[2][1]["value"] == "primary-model"

        # Check mode was set to bypassPermissions
        assert sent_messages[3][1]["configId"] == "mode"
        assert sent_messages[3][1]["value"] == "bypassPermissions"

    @pytest.mark.asyncio
    async def test_handle_server_request_permission(self, tmp_path: Path) -> None:
        conn = await self._create_mock_connector(tmp_path)
        runtime = conn._create_runtime(tmp_path, "primary-model")

        permission_req = ACPNotification(
            jsonrpc="2.0",
            id=42,
            method="session/request_permission",
            params={"toolCall": {"name": "read_file"}},
        )

        with patch.object(
            conn, "_send_jsonrpc_result", new_callable=AsyncMock
        ) as mock_send_res:
            await conn._handle_server_request(runtime, permission_req)

            mock_send_res.assert_awaited_once_with(
                runtime,
                42,
                {"outcome": {"outcome": "selected", "optionId": "allow-always"}},
            )
