import json
import logging
from pathlib import Path, PureWindowsPath
from typing import Literal
from unittest.mock import AsyncMock

import pytest
from src.core.config.app_config import AppConfig, SessionConfig
from src.core.config.models.access_mode import AccessMode, AccessModeConfig
from src.core.domain.chat import ChatMessage, ChatRequest, FunctionCall, ToolCall
from src.core.domain.responses import ResponseEnvelope
from src.core.domain.session import Session, SessionState
from src.core.services.project_directory_resolution_service import (
    ProjectDirectoryResolutionService,
)


@pytest.fixture
def mock_backend_service() -> AsyncMock:
    """Fixture for a mocked IBackendService."""
    return AsyncMock()


@pytest.fixture
def mock_session_service() -> AsyncMock:
    """Fixture for a mocked ISessionService."""
    return AsyncMock()


@pytest.fixture
def session() -> Session:
    """Fixture for a new session."""
    return Session(session_id="test-session", state=SessionState())


def create_app_config(
    resolution_mode: str,
    model_spec: str | None = "openai:gpt-4",
    filesystem_mode: Literal["auto", "enabled", "disabled"] = "auto",
    access_mode: AccessMode = AccessMode.SINGLE_USER,
    disable_default_openrouter_fallback: bool = False,
) -> AppConfig:
    """Helper to create AppConfig with specific resolution settings."""
    session_config = SessionConfig(
        project_dir_resolution_mode=resolution_mode,
        project_dir_resolution_model=model_spec,
        project_dir_resolution_filesystem_mode=filesystem_mode,
        disable_default_openrouter_project_dir_resolution_fallback=disable_default_openrouter_fallback,
    )
    return AppConfig(
        session=session_config, access_mode=AccessModeConfig(mode=access_mode)
    )


@pytest.mark.asyncio
class TestProjectDirectoryResolutionService:
    # Deterministic Tests
    @pytest.mark.parametrize(
        "prompt, expected_path",
        [
            ("Work on C:\\Users\\Test\\Project", "C:\\Users\\Test\\Project"),
            (
                "My project is at /home/user/dev/project-x, please help",
                "/home/user/dev/project-x",
            ),
            (
                "Use project \\\\server\\share\\folder\\src\\main",
                "\\\\server\\share\\folder\\src\\main",
            ),
        ],
    )
    async def test_deterministic_finds_path(
        self, mock_backend_service, mock_session_service, session, prompt, expected_path
    ):
        request = ChatRequest(
            model="test-model", messages=[ChatMessage(role="user", content=prompt)]
        )
        config = create_app_config(
            "deterministic", disable_default_openrouter_fallback=True
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == expected_path
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_called_once_with(session)

    async def test_deterministic_uses_marker_backed_root_for_file_mentions(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ):
        project_root = tmp_path / "project-root"
        module_a = project_root / "src" / "module1"
        module_b = project_root / "src" / "module2"
        docs_dir = project_root / "docs"
        tests_dir = project_root / "tests" / "unit"
        module_a.mkdir(parents=True)
        module_b.mkdir(parents=True)
        docs_dir.mkdir(parents=True)
        tests_dir.mkdir(parents=True)
        (project_root / ".git").mkdir()
        (module_a / "abc.py").write_text("pass\n")
        (module_b / "utils.py").write_text("pass\n")
        (docs_dir / "README.md").write_text("docs\n")
        (tests_dir / "test_sample.py").write_text("pass\n")

        prompt = (
            f'"{module_a / "abc.py"}", '
            f"'{module_b / 'utils.py'}', "
            f"`{docs_dir / 'README.md'}`, "
            f"and {tests_dir / 'test_sample.py'}."
        )
        request = ChatRequest(
            model="test-model", messages=[ChatMessage(role="user", content=prompt)]
        )
        config = create_app_config("deterministic", filesystem_mode="enabled")
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == str(project_root.resolve())
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_called_once_with(session)

    async def test_deterministic_uses_developer_metadata_cwd_hint(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        project_root = tmp_path / "project-root"
        project_root.mkdir(parents=True)

        request = ChatRequest(
            model="test-model",
            messages=[
                ChatMessage(
                    role="system", content="Generic startup instructions only."
                ),
                ChatMessage(
                    role="developer",
                    content="Session metadata",
                    metadata={"cwd": str(project_root)},
                ),
                ChatMessage(role="user", content="Please inspect the project root."),
            ],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            access_mode=AccessMode.MULTI_USER,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == str(project_root)
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_called_once_with(session)

    async def test_deterministic_uses_request_metadata_cwd_hint(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        project_root = tmp_path / "project-root"
        project_root.mkdir(parents=True)

        request = ChatRequest(
            model="test-model",
            request_metadata={"cwd": str(project_root)},
            messages=[
                ChatMessage(
                    role="system", content="Generic startup instructions only."
                ),
                ChatMessage(role="user", content="Please inspect the project root."),
            ],
        )
        config = create_app_config("deterministic")
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == str(project_root)
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_called_once_with(session)

    async def test_deterministic_uses_tool_call_arguments_cwd_hint(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        project_root = tmp_path / "project-root"
        project_root.mkdir(parents=True)

        request = ChatRequest(
            model="test-model",
            messages=[
                ChatMessage(
                    role="system", content="Generic startup instructions only."
                ),
                ChatMessage(
                    role="developer",
                    tool_calls=[
                        ToolCall(
                            function=FunctionCall(
                                name="bash", arguments=f"cwd: {project_root}"
                            )
                        )
                    ],
                ),
                ChatMessage(role="user", content="Please inspect the project root."),
            ],
        )
        config = create_app_config("deterministic")
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == str(project_root)
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_called_once_with(session)

    async def test_deterministic_ignores_untrusted_tool_call_arguments(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        project_root = tmp_path / "project-root"
        project_root.mkdir(parents=True)

        request = ChatRequest(
            model="test-model",
            messages=[
                ChatMessage(
                    role="user",
                    content="Please inspect the project root.",
                    tool_calls=[
                        ToolCall(
                            function=FunctionCall(
                                name="bash", arguments=f"cwd: {project_root}"
                            )
                        )
                    ],
                )
            ],
        )
        config = create_app_config(
            "deterministic", disable_default_openrouter_fallback=True
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir is None
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_called_once_with(session)

    async def test_deterministic_uses_json_tool_call_arguments_cwd_hint(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        project_root = tmp_path / "project-root"
        project_root.mkdir(parents=True)

        request = ChatRequest(
            model="test-model",
            messages=[
                ChatMessage(
                    role="developer",
                    content="Startup tool call metadata.",
                    tool_calls=[
                        ToolCall(
                            function=FunctionCall(
                                name="exec_command",
                                arguments=json.dumps({"cwd": str(project_root)}),
                            )
                        )
                    ],
                ),
                ChatMessage(role="user", content="Please inspect the project root."),
            ],
        )
        config = create_app_config("deterministic")
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == str(project_root)
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_called_once_with(session)

    async def test_deterministic_no_path(
        self,
        mock_backend_service,
        mock_session_service,
        session,
        caplog,
        tmp_path,
        monkeypatch,
    ):
        # Ensure we don't accidentally set project_dir via deterministic fallback-to-cwd.
        # The fallback is dot-based, so use an empty temp directory without dot entries.
        original_cwd = Path.cwd()
        monkeypatch.chdir(tmp_path)
        request = ChatRequest(
            model="test-model",
            messages=[ChatMessage(role="user", content="Hello world")],
        )
        config = create_app_config(
            "deterministic", disable_default_openrouter_fallback=True
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir is None
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_not_called()
        assert "did not identify a directory (deterministic mode)" in caplog.text
        monkeypatch.chdir(original_cwd)

    async def test_deterministic_auto_fallbacks_to_openrouter_in_single_user_mode(
        self, mock_backend_service, mock_session_service, session
    ) -> None:
        request = ChatRequest(
            model="test-model",
            messages=[ChatMessage(role="user", content="Hello world")],
        )
        config = create_app_config("deterministic", model_spec=None)
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )
        service._openrouter_api_key_available = True
        mock_backend_service.call_completion.return_value = ResponseEnvelope(
            content=(
                "<directory-resolution-response>"
                "<project-absolute-directory>/home/user/project</project-absolute-directory>"
                "</directory-resolution-response>"
            )
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == "/home/user/project"
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_called_once()
        llm_request = mock_backend_service.call_completion.await_args.args[0]
        assert llm_request.model == "openrouter:openrouter/free"

    async def test_deterministic_does_not_fallback_to_cwd_when_candidate_is_ambiguous(
        self, mock_backend_service, mock_session_service, session, tmp_path, monkeypatch
    ):
        workspace = tmp_path / "workspace"
        workspace.mkdir()
        (workspace / ".git").mkdir()
        original_cwd = Path.cwd()
        monkeypatch.chdir(workspace)

        non_project_dir = tmp_path / "non_project_dir"
        non_project_dir.mkdir()

        file_a = non_project_dir / "a.py"
        file_b = non_project_dir / "b.py"
        file_a.write_text("print('a')\n")
        file_b.write_text("print('b')\n")

        request = ChatRequest(
            model="test-model",
            messages=[
                ChatMessage(
                    role="user", content=f"Use {file_a} and also inspect {file_b}"
                )
            ],
        )
        config = create_app_config(
            "deterministic", disable_default_openrouter_fallback=True
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir is None
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_not_called()
        monkeypatch.chdir(original_cwd)

    # LLM Mode Tests
    async def test_llm_mode_success(
        self, mock_backend_service, mock_session_service, session, caplog
    ):
        request = ChatRequest(
            model="test-model",
            messages=[ChatMessage(role="user", content="I want to work on my project")],
        )
        config = create_app_config("llm")

        llm_response = ResponseEnvelope(
            content="<directory-resolution-response><project-absolute-directory>/home/user/Desktop</project-absolute-directory></directory-resolution-response>"
        )
        mock_backend_service.call_completion.return_value = llm_response

        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )
        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == "/home/user/Desktop"
        mock_backend_service.call_completion.assert_called_once()
        assert mock_backend_service.call_completion.await_args is not None
        assert (
            mock_backend_service.call_completion.await_args.kwargs["allow_failover"]
            is True
        )
        assert (
            "Project directory auto-detected (LLM): /home/user/Desktop" in caplog.text
        )

    async def test_llm_mode_preserves_runtime_failover_for_composite_selector(
        self, mock_backend_service, mock_session_service, session
    ) -> None:
        request = ChatRequest(
            model="test-model",
            messages=[ChatMessage(role="user", content="I want to work on my project")],
        )
        config = create_app_config(
            "llm", model_spec="openai:gpt-4o-mini|anthropic:claude-3-5-sonnet"
        )
        mock_backend_service.call_completion.return_value = ResponseEnvelope(
            content=(
                "<directory-resolution-response>"
                "<project-absolute-directory>/home/user/Desktop</project-absolute-directory>"
                "</directory-resolution-response>"
            )
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        mock_backend_service.call_completion.assert_called_once()
        assert mock_backend_service.call_completion.await_args is not None
        assert (
            mock_backend_service.call_completion.await_args.kwargs["allow_failover"]
            is True
        )
        llm_request = mock_backend_service.call_completion.await_args.args[0]
        assert llm_request.model == "openai:gpt-4o-mini|anthropic:claude-3-5-sonnet"

    async def test_llm_mode_llm_fails(
        self, mock_backend_service, mock_session_service, session, caplog
    ):
        request = ChatRequest(
            model="test-model",
            messages=[ChatMessage(role="user", content="my project is on the desktop")],
        )
        config = create_app_config("llm")

        llm_response = ResponseEnvelope(
            content="<directory-resolution-response><error>Cannot determine</error></directory-resolution-response>"
        )
        mock_backend_service.call_completion.return_value = llm_response

        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )
        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir is None
        mock_backend_service.call_completion.assert_called_once()
        assert "did not identify a directory (Cannot determine)" in caplog.text

    # Hybrid Mode Tests
    async def test_hybrid_mode_deterministic_wins(
        self, mock_backend_service, mock_session_service, session, caplog
    ):
        prompt = "Path is C:\\Users\\Test\\MyProject"
        request = ChatRequest(
            model="test-model", messages=[ChatMessage(role="user", content=prompt)]
        )
        config = create_app_config("hybrid")
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == "C:\\Users\\Test\\MyProject"
        mock_backend_service.call_completion.assert_not_called()
        assert (
            "Project directory auto-detected (deterministic/user): C:\\Users\\Test\\MyProject"
            in caplog.text
        )

    async def test_hybrid_mode_fallback_to_llm(
        self, mock_backend_service, mock_session_service, session, caplog
    ):
        request = ChatRequest(
            model="test-model",
            messages=[ChatMessage(role="user", content="my project is on the desktop")],
        )
        config = create_app_config("hybrid")

        llm_response = ResponseEnvelope(
            content="<directory-resolution-response><project-absolute-directory>/home/user/Desktop</project-absolute-directory></directory-resolution-response>"
        )
        mock_backend_service.call_completion.return_value = llm_response

        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )
        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == "/home/user/Desktop"
        mock_backend_service.call_completion.assert_called_once()
        assert (
            "Project directory auto-detected (LLM): /home/user/Desktop" in caplog.text
        )

    async def test_hybrid_mode_fallback_to_llm_when_filesystem_probe_disabled(
        self, mock_backend_service, mock_session_service, session, tmp_path, caplog
    ):
        project_root = tmp_path / "project-root"
        component_dir = project_root / "src" / "feature" / "component"
        component_dir.mkdir(parents=True)
        (project_root / ".git").mkdir()
        file_a = component_dir / "a.py"
        file_b = component_dir / "b.py"
        file_a.write_text("print('a')\n")
        file_b.write_text("print('b')\n")

        request = ChatRequest(
            model="test-model",
            messages=[
                ChatMessage(role="user", content=f"Inspect {file_a} and {file_b}")
            ],
        )
        config = create_app_config(
            "hybrid", filesystem_mode="disabled", access_mode=AccessMode.MULTI_USER
        )

        llm_response = ResponseEnvelope(
            content="<directory-resolution-response><project-absolute-directory>/home/user/Desktop</project-absolute-directory></directory-resolution-response>"
        )
        mock_backend_service.call_completion.return_value = llm_response

        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )
        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == "/home/user/Desktop"
        mock_backend_service.call_completion.assert_called_once()
        assert (
            "Project directory auto-detected (LLM): /home/user/Desktop" in caplog.text
        )

    async def test_deterministic_mode_auto_fallbacks_to_openrouter_in_single_user_mode(
        self, mock_backend_service, mock_session_service, session, monkeypatch, caplog
    ) -> None:
        monkeypatch.setenv("OPENROUTER_API_KEY", "test-openrouter-key")
        request = ChatRequest(
            model="test-model",
            messages=[ChatMessage(role="user", content="my project is on the desktop")],
        )
        config = create_app_config("deterministic", model_spec=None)
        mock_backend_service.call_completion.return_value = ResponseEnvelope(
            content=(
                "<directory-resolution-response>"
                "<project-absolute-directory>/home/user/Desktop</project-absolute-directory>"
                "</directory-resolution-response>"
            )
        )

        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )
        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == "/home/user/Desktop"
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_called_once()
        llm_request = mock_backend_service.call_completion.await_args.args[0]
        assert llm_request.model == "openrouter:openrouter/free"
        assert (
            "Project directory auto-detected (LLM): /home/user/Desktop" in caplog.text
        )

    async def test_deterministic_mode_ignores_user_override_model_in_deterministic_mode(
        self, mock_backend_service, mock_session_service, session, monkeypatch
    ) -> None:
        monkeypatch.setenv("OPENROUTER_API_KEY", "test-openrouter-key")
        request = ChatRequest(
            model="test-model",
            messages=[ChatMessage(role="user", content="my project is on the desktop")],
        )
        config = create_app_config("deterministic", model_spec="openai:gpt-4.1-mini")
        mock_backend_service.call_completion.return_value = ResponseEnvelope(
            content=(
                "<directory-resolution-response>"
                "<project-absolute-directory>/home/user/Desktop</project-absolute-directory>"
                "</directory-resolution-response>"
            )
        )

        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )
        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == "/home/user/Desktop"
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_called_once()
        llm_request = mock_backend_service.call_completion.await_args.args[0]
        assert llm_request.model == "openrouter:openrouter/free"

    async def test_deterministic_mode_does_not_fallback_when_disable_flag_is_set(
        self, mock_backend_service, mock_session_service, session, monkeypatch, caplog
    ) -> None:
        monkeypatch.setenv("OPENROUTER_API_KEY", "test-openrouter-key")
        request = ChatRequest(
            model="test-model",
            messages=[ChatMessage(role="user", content="my project is on the desktop")],
        )
        config = create_app_config(
            "deterministic", model_spec=None, disable_default_openrouter_fallback=True
        )

        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )
        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir is None
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_not_called()
        assert "did not identify a directory (deterministic mode)" in caplog.text

    # Edge cases
    async def test_skips_if_dir_already_set(
        self, mock_backend_service, mock_session_service, caplog
    ):
        session = Session(
            session_id="test", state=SessionState().with_project_dir("/already/set")
        )
        request = ChatRequest(
            model="test-model", messages=[ChatMessage(role="user", content="...")]
        )
        config = create_app_config("hybrid")
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        mock_backend_service.call_completion.assert_not_called()
        assert "skipped: directory already set" in caplog.text

    async def test_skips_if_history_not_empty(
        self, mock_backend_service, mock_session_service, session
    ):
        session.history.append(ChatMessage(role="user", content="previous message"))
        request = ChatRequest(
            model="test-model",
            messages=[ChatMessage(role="user", content="current message")],
        )
        config = create_app_config("hybrid")
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_not_called()

    async def test_llm_mode_no_model_configured(
        self, mock_backend_service, mock_session_service, session, caplog
    ):
        request = ChatRequest(
            model="test-model",
            messages=[ChatMessage(role="user", content="some prompt")],
        )
        config = create_app_config("llm", model_spec=None)
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir is None
        mock_backend_service.call_completion.assert_not_called()
        assert (
            "LLM project directory resolution is enabled but no model is configured"
            in caplog.text
        )

    async def test_hybrid_mode_no_model_configured_fallback(
        self, mock_backend_service, mock_session_service, session, caplog
    ):
        request = ChatRequest(
            model="test-model",
            messages=[ChatMessage(role="user", content="some prompt without a path")],
        )
        config = create_app_config(
            "hybrid", model_spec=None, disable_default_openrouter_fallback=True
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir is None
        mock_backend_service.call_completion.assert_not_called()
        assert (
            "did not identify a directory (hybrid mode, no LLM configured)"
            in caplog.text
        )

    async def test_no_call_when_feature_disabled(
        self, mock_backend_service, mock_session_service, session
    ) -> None:
        config = create_app_config("disabled")
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )
        request = ChatRequest(
            model="test-model",
            messages=[ChatMessage(role="user", content="some prompt")],
        )

        await service.maybe_resolve_project_directory(session, request)

        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_not_called()

    async def test_opencode_like_tools_and_routed_model_still_resolve_path(
        self, mock_backend_service, mock_session_service, session
    ) -> None:
        """Coding agents send tools on every turn; routed models use backend:model syntax."""
        win_path = "C:\\Users\\Dev\\opencode-app"
        request = ChatRequest(
            model="cursor-cli-acp:cursor/composer-2",
            messages=[
                ChatMessage(role="user", content=f"Read the README under {win_path}")
            ],
            tools=[
                {
                    "type": "function",
                    "function": {
                        "name": "bash",
                        "description": "Run shell",
                        "parameters": {"type": "object", "properties": {}},
                    },
                }
            ],
        )
        config = create_app_config(
            "deterministic", disable_default_openrouter_fallback=True
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_path
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_called_once_with(session)

    async def test_opencode_working_directory_line_in_system_prompt(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """OpenCode injects ``Working directory: <abs>`` (not ``current working directory``)."""
        project_root = tmp_path / "turbodom"
        project_root.mkdir(parents=True)
        win_path = str(project_root.resolve())
        request = ChatRequest(
            model="cursor-cli-acp:cursor/composer-2",
            agent="opencode/1.2.26 ai-sdk/provider-utils/3.0.20 runtime/bun/1.3.10",
            messages=[
                ChatMessage(
                    role="system",
                    content=(
                        "You are a coding agent.\n"
                        f"Working directory: {win_path}\n"
                        "Use absolute paths."
                    ),
                ),
                ChatMessage(role="user", content="Say hello."),
            ],
            tools=[{"type": "function", "function": {"name": "bash"}}],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_path
        mock_backend_service.call_completion.assert_not_called()

    async def test_retries_opencode_working_directory_after_failed_first_attempt(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """A short title prompt must not freeze an empty workspace for later OpenCode turns."""
        project_root = tmp_path / "later-turn-root"
        project_root.mkdir(parents=True)
        win_path = str(project_root.resolve())
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        first_request = ChatRequest(
            model="cursor-cli-acp:cursor/grok-4.6-high",
            agent="opencode/1.18.32 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14",
            messages=[ChatMessage(role="user", content="Generate a brief title")],
        )
        await service.maybe_resolve_project_directory(session, first_request)
        assert session.state.project_dir is None
        assert session.state.project_dir_resolution_attempted is True

        later_request = ChatRequest(
            model="cursor-cli-acp:cursor/grok-4.6-high",
            agent="opencode/1.18.32 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14",
            messages=[
                ChatMessage(
                    role="system",
                    content=f"Working directory: {win_path}\n",
                ),
                ChatMessage(role="user", content="Continue in this repo."),
            ],
            tools=[{"type": "function", "function": {"name": "bash"}}],
        )
        await service.maybe_resolve_project_directory(session, later_request)

        assert session.state.project_dir == win_path
        mock_backend_service.call_completion.assert_not_called()

    async def test_opencode_working_directory_uses_session_agent_when_request_has_no_agent(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """OpenCode patterns apply when agent is only on session (e.g. prior enricher path)."""
        project_root = tmp_path / "session-agent-root"
        project_root.mkdir(parents=True)
        win_path = str(project_root.resolve())
        session.agent = (
            "opencode/1.2.26 ai-sdk/provider-utils/3.0.20 runtime/bun/1.3.10"
        )
        request = ChatRequest(
            model="cursor-cli-acp:cursor/composer-2",
            messages=[
                ChatMessage(
                    role="system",
                    content=(
                        "You are a coding agent.\n" f"Working directory: {win_path}\n"
                    ),
                ),
                ChatMessage(role="user", content="Say hello."),
            ],
            tools=[{"type": "function", "function": {"name": "bash"}}],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_path
        mock_backend_service.call_completion.assert_not_called()

    async def test_factory_droid_pwd_transcript_wins_over_other_absolute_paths(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """Factory Droid puts cwd on the line after ``% pwd`` in the user transcript."""
        repo_a = tmp_path / "repo-a"
        repo_b = tmp_path / "repo-b"
        repo_a.mkdir(parents=True)
        repo_b.mkdir(parents=True)
        path_a = str(repo_a.resolve())
        path_b = str(repo_b.resolve())
        user_blob = (
            "Context from shell (not part of the user question):\n\n"
            "% pwd\n"
            f"{path_a}\n\n"
            f"Documentation mentions sibling checkout at `{path_b}`.\n"
        )
        request = ChatRequest(
            model="test-model",
            agent="factory-cli/0.99.0",
            messages=[ChatMessage(role="user", content=user_blob)],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == path_a
        mock_backend_service.call_completion.assert_not_called()

    async def test_pi_harness_developer_forward_slash_cwd_resolves(
        self, mock_backend_service, mock_session_service, session
    ) -> None:
        """Pi puts ``Current working directory: C:/...`` in a ``developer`` message."""

        cwd = str(PureWindowsPath("C:/Users/Mateusz/tmp"))
        request = ChatRequest(
            model="alias:minimax",
            agent="OpenAI/JS 6.26.0",
            messages=[
                ChatMessage(
                    role="developer",
                    content=(
                        "You are an expert coding assistant operating inside pi.\n"
                        "Current date: 2026-04-16\n"
                        "Current working directory: C:/Users/Mateusz/tmp\n"
                    ),
                ),
                ChatMessage(role="user", content="Are there any local changes?"),
            ],
            tools=[{"type": "function", "function": {"name": "bash"}}],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == cwd
        mock_backend_service.call_completion.assert_not_called()

    async def test_pi_developer_cwd_wins_when_tools_carry_many_user_paths(
        self, mock_backend_service, mock_session_service, session
    ) -> None:
        """Aggregated startup paths must not hide Pi's cwd line (see trusted bodies pass)."""

        cwd = str(PureWindowsPath("C:/Users/Mateusz/tmp"))
        request = ChatRequest(
            model="alias:minimax",
            agent="OpenAI/JS 6.26.0",
            messages=[
                ChatMessage(
                    role="developer",
                    content=(
                        "You are an expert coding assistant operating inside pi.\n"
                        "Also see C:\\Users\\Mateusz\\other and "
                        "C:\\Users\\Mateusz\\source\\repos\\unrelated for context.\n"
                        "Current working directory: C:/Users/Mateusz/tmp\n"
                    ),
                ),
                ChatMessage(role="user", content="status"),
            ],
            tools=[
                {
                    "type": "function",
                    "function": {
                        "name": "read_file",
                        "description": (
                            "Reads C:\\Users\\Mateusz\\AppData\\x and "
                            "C:\\Users\\Mateusz\\source\\repos\\y\\z"
                        ),
                    },
                }
            ],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == cwd
        mock_backend_service.call_completion.assert_not_called()

    async def test_claude_code_working_directory_in_system_wins_over_api_doc_paths(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """Claude Code injects ``Working directory:``; system prompt also cites ``/v1/...`` API paths."""

        repo = tmp_path / "llm-interactive-proxy"
        repo.mkdir()
        win_path = str(repo.resolve())
        system_blob = (
            "You are Claude Code, Anthropic's official CLI for Claude.\n"
            "The API supports POST /v1/code/triggers and GET /v1/messages.\n"
            f"Working directory: {win_path}\n"
        )
        request = ChatRequest(
            model="qwen-oauth:qwen/coder-model",
            agent="claude-cli/2.1.92 (external, cli)",
            messages=[
                ChatMessage(role="system", content=system_blob),
                ChatMessage(role="user", content="Hello"),
            ],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_path
        mock_backend_service.call_completion.assert_not_called()

    async def test_claude_code_working_directory_in_first_user_message(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """When dynamic sections are moved out of system, cwd can appear only on the first user turn."""

        repo = tmp_path / "proj"
        repo.mkdir()
        win_path = str(repo.resolve())
        request = ChatRequest(
            model="test-model",
            agent="claude-cli/2.1.0",
            messages=[
                ChatMessage(
                    role="system",
                    content="You are Claude Code. Docs mention /v1/code/triggers.",
                ),
                ChatMessage(
                    role="user",
                    content=(
                        "Context:\n"
                        f"Working directory: {win_path}\n\n"
                        "Please summarize the repo."
                    ),
                ),
            ],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_path
        mock_backend_service.call_completion.assert_not_called()

    async def test_cline_workspace_path_in_first_user_turn_is_authoritative(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """Cline puts ``Workspace Path:`` in the first user message; short startup must not win first."""

        repo = tmp_path / "llm-interactive-proxy"
        repo.mkdir()
        parent = tmp_path / "repos"
        parent.mkdir()
        win_repo = str(repo.resolve())
        win_parent = str(parent.resolve())
        request = ChatRequest(
            model="test-model",
            agent="Cline/3.78.0",
            messages=[
                ChatMessage(role="system", content="You are a helpful assistant."),
                ChatMessage(
                    role="user",
                    content=(
                        f"Workspace Path: {win_repo}\n\n"
                        f"Context also references tools under {win_parent}.\n"
                    ),
                ),
            ],
            tools=[
                {
                    "type": "function",
                    "function": {"name": "x", "description": f"Runs in {win_parent}"},
                }
            ],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_repo
        mock_backend_service.call_completion.assert_not_called()

    async def test_cline_workspace_folder_label_in_user_turn_is_authoritative(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """Cline may emit ``Workspace folder:`` (vscode_fork hint), not only ``Workspace path``."""

        repo = tmp_path / "cline-ws-folder"
        repo.mkdir()
        win_repo = str(repo.resolve())
        request = ChatRequest(
            model="test-model",
            agent="Cline/3.78.0",
            messages=[
                ChatMessage(role="user", content=f"Workspace folder: {win_repo}\n")
            ],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_repo
        mock_backend_service.call_completion.assert_not_called()

    async def test_roo_code_workspace_path_in_first_user_turn_is_authoritative(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """Roo Code (VS Code) matches Cline-style environment lines on the first user turn."""

        repo = tmp_path / "llm-interactive-proxy"
        repo.mkdir()
        noise = tmp_path / "research-volatility"
        noise.mkdir()
        win_repo = str(repo.resolve())
        win_noise = str(noise.resolve())
        request = ChatRequest(
            model="test-model",
            agent="RooCode/3.52.1",
            messages=[
                ChatMessage(role="system", content="You are a helpful assistant."),
                ChatMessage(
                    role="user",
                    content=(
                        f"Workspace Path: {win_repo}\n\n"
                        f"See also sibling work under {win_noise} for examples.\n"
                    ),
                ),
            ],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_repo
        mock_backend_service.call_completion.assert_not_called()

    async def test_roo_code_workspace_folder_label_in_second_user_message(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """Roo may use ``Workspace folder:`` and split stub + environment across user turns."""

        repo = tmp_path / "llm-interactive-proxy"
        repo.mkdir()
        win_repo = str(repo.resolve())
        request = ChatRequest(
            model="test-model",
            agent="RooCode/3.52.1",
            messages=[
                ChatMessage(role="user", content="(task stub)"),
                ChatMessage(
                    role="user", content=f"Workspace folder: {win_repo}\n\nProceed.\n"
                ),
            ],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_repo
        mock_backend_service.call_completion.assert_not_called()

    async def test_kilo_code_workspace_path_in_user_turn_is_authoritative(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """Kilo Code uses the same Cline-family ``Workspace Path:`` style user preamble."""

        repo = tmp_path / "kilo-sandbox"
        repo.mkdir()
        win_repo = str(repo.resolve())
        ua = "Kilo-Code/7.2.10 ai-sdk/provider-utils/4.0.21 runtime/bun/1.3.11"
        request = ChatRequest(
            model="test-model",
            agent=ua,
            messages=[
                ChatMessage(role="system", content="You are a helpful assistant."),
                ChatMessage(
                    role="user", content=f"Workspace Path: {win_repo}\n\nHello.\n"
                ),
            ],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_repo
        mock_backend_service.call_completion.assert_not_called()

    async def test_kilo_working_directory_line_in_user_turn_is_authoritative(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """Kilo gets ``Working directory:`` hint patterns like other vscode forks."""

        repo = tmp_path / "kilo-wd"
        repo.mkdir()
        win_repo = str(repo.resolve())
        ua = "Kilo-Code/7.2.10 ai-sdk/provider-utils/4.0.21 runtime/bun/1.3.11"
        request = ChatRequest(
            model="test-model",
            agent=ua,
            messages=[
                ChatMessage(role="user", content=f"Working directory: {win_repo}\n")
            ],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_repo
        mock_backend_service.call_completion.assert_not_called()

    async def test_kilo_code_workspace_folder_in_second_user_message(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """Kilo may split a short first user stub from the environment block on a later user turn."""

        repo = tmp_path / "kilo-second-user"
        repo.mkdir()
        win_repo = str(repo.resolve())
        ua = "Kilo-Code/7.2.10 ai-sdk/provider-utils/4.0.21 runtime/bun/1.3.11"
        request = ChatRequest(
            model="test-model",
            agent=ua,
            messages=[
                ChatMessage(role="user", content="(task stub)"),
                ChatMessage(
                    role="user", content=f"Workspace folder: {win_repo}\n\nProceed.\n"
                ),
            ],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_repo
        mock_backend_service.call_completion.assert_not_called()

    async def test_non_opencode_working_directory_line_not_trusted_in_system(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """Generic clients: ``Working directory:`` alone is not a trusted hint line."""
        project_root = tmp_path / "other-root"
        project_root.mkdir(parents=True)
        win_path = str(project_root.resolve())
        request = ChatRequest(
            model="test-model",
            agent="some-other-cli/1.0",
            messages=[
                ChatMessage(role="system", content=f"Working directory: {win_path}\n"),
                ChatMessage(role="user", content="noop"),
            ],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir is None

    async def test_extra_body_workspace_fields_set_project_dir(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        workspace = tmp_path / "from-extra"
        workspace.mkdir()
        request = ChatRequest(
            model="cursor-cli-acp:cursor/composer-2",
            messages=[ChatMessage(role="user", content="hello")],
            tools=[{"type": "function", "function": {"name": "bash"}}],
            extra_body={"project_dir": str(workspace)},
        )
        config = create_app_config(
            "deterministic", disable_default_openrouter_fallback=True
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == str(workspace.resolve())
        mock_backend_service.call_completion.assert_not_called()

    async def test_acp_model_uri_params_still_detects_trusted_cwd_line(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """First ACP turn with ``?reasoning_effort=`` must not skip workspace detection."""
        project_root = tmp_path / "llm-interactive-proxy"
        project_root.mkdir(parents=True)
        win_path = str(project_root.resolve())
        request = ChatRequest(
            model="cursor-cli-acp:cursor/composer-2.5?reasoning_effort=high",
            agent="opencode/1.2.26 ai-sdk/provider-utils/3.0.20 runtime/bun/1.3.10",
            messages=[
                ChatMessage(
                    role="system",
                    content=(
                        "You are a coding agent.\n" f"Working directory: {win_path}\n"
                    ),
                ),
                ChatMessage(role="user", content="Say hello."),
            ],
            tools=[{"type": "function", "function": {"name": "bash"}}],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_path
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_called_once_with(session)

    async def test_single_backend_model_uri_params_still_detects_path(
        self, mock_backend_service, mock_session_service, session
    ) -> None:
        """Normal ``backend:model?param=value`` selectors must not skip detection."""
        win_path = "C:\\Users\\Dev\\my-app"
        request = ChatRequest(
            model="nvidia:minimaxai/minimax-m3?reasoning_effort=high",
            messages=[ChatMessage(role="user", content=f"Work in {win_path}")],
            tools=[{"type": "function", "function": {"name": "bash"}}],
        )
        config = create_app_config(
            "deterministic", disable_default_openrouter_fallback=True
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_path
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_called_once_with(session)

    async def test_composite_model_uri_params_still_detects_trusted_cwd_line(
        self, mock_backend_service, mock_session_service, session, tmp_path: Path
    ) -> None:
        """Composite selectors with per-leaf URI params must not skip detection."""
        project_root = tmp_path / "composite-ws"
        project_root.mkdir(parents=True)
        win_path = str(project_root.resolve())
        request = ChatRequest(
            model=(
                "[handicap=10]nvidia:minimaxai/minimax-m3?reasoning_effort=high!"
                "nvidia:deepseek-ai/deepseek-v4-pro?reasoning_effort=max"
            ),
            agent="opencode/1.2.26 ai-sdk/provider-utils/3.0.20 runtime/bun/1.3.10",
            messages=[
                ChatMessage(role="system", content=f"Working directory: {win_path}\n"),
                ChatMessage(role="user", content="Proceed."),
            ],
            tools=[{"type": "function", "function": {"name": "bash"}}],
        )
        config = create_app_config(
            "deterministic",
            filesystem_mode="disabled",
            disable_default_openrouter_fallback=True,
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir == win_path
        assert session.state.project_dir_resolution_attempted is True
        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_called_once_with(session)

    async def test_vendor_model_selector_still_skips_with_tools(
        self, mock_backend_service, mock_session_service, session
    ) -> None:
        """Model-only ``provider/model`` selectors remain skipped (ambiguous routing)."""
        request = ChatRequest(
            model="openai/gpt-4o",
            messages=[
                ChatMessage(role="user", content="Work in C:\\Users\\Dev\\my-app")
            ],
            tools=[
                {
                    "type": "function",
                    "function": {
                        "name": "read",
                        "description": "Read file",
                        "parameters": {"type": "object", "properties": {}},
                    },
                }
            ],
        )
        config = create_app_config(
            "deterministic", disable_default_openrouter_fallback=True
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir is None
        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_not_called()

    async def test_vendor_model_selector_with_uri_params_still_skips(
        self, mock_backend_service, mock_session_service, session
    ) -> None:
        """URI params do not make model-only ``provider/model`` selectors safe."""
        request = ChatRequest(
            model="openai/gpt-4o?reasoning_effort=high",
            messages=[
                ChatMessage(role="user", content="Work in C:\\Users\\Dev\\my-app")
            ],
            tools=[{"type": "function", "function": {"name": "read"}}],
        )
        config = create_app_config(
            "deterministic", disable_default_openrouter_fallback=True
        )
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )

        await service.maybe_resolve_project_directory(session, request)

        assert session.state.project_dir is None
        mock_backend_service.call_completion.assert_not_called()
        mock_session_service.update_session.assert_not_called()


@pytest.mark.asyncio
class TestProjectDirectoryValidation:
    @pytest.mark.parametrize(
        "invalid_path",
        [
            "C:\\",
            "D:\\",
            "/",
            "C:\\Users",
            "/home",
            "C:\\Windows\\System32",
            "/usr/bin",
            "\\\\server\\share",  # Shallow UNC
        ],
    )
    def test_rejects_invalid_paths(
        self, invalid_path, mock_backend_service, mock_session_service
    ):
        config = create_app_config("deterministic")
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )
        path_type = service._detect_path_type(invalid_path)
        assert path_type is not None, f"Path type for {invalid_path} should be detected"
        assert not service._is_valid_project_directory_candidate(
            invalid_path, path_type
        )

    @pytest.mark.parametrize(
        "valid_path",
        [
            "C:\\Users\\test\\project",
            "/home/user/project",
            "\\\\server\\share\\team\\project\\src",
            "C:\\Users\\some-user\\Desktop\\my-project",
        ],
    )
    def test_accepts_valid_paths(
        self, valid_path, mock_backend_service, mock_session_service
    ):
        config = create_app_config("deterministic")
        service = ProjectDirectoryResolutionService(
            config, mock_backend_service, mock_session_service
        )
        path_type = service._detect_path_type(valid_path)
        assert path_type is not None, f"Path type for {valid_path} should be detected"
        assert service._is_valid_project_directory_candidate(valid_path, path_type)


@pytest.mark.asyncio
async def test_deterministic_scoring_prefers_deeper_paths(
    mock_backend_service, mock_session_service, session
):
    prompt = (
        "We have C:\\Users\\Test and also C:\\Users\\Test\\ProjectA. "
        "And another one at C:\\Users\\Test\\ProjectA\\src"
    )
    request = ChatRequest(
        model="test-model", messages=[ChatMessage(role="user", content=prompt)]
    )
    config = create_app_config("deterministic")
    service = ProjectDirectoryResolutionService(
        config, mock_backend_service, mock_session_service
    )

    await service.maybe_resolve_project_directory(session, request)

    # The deepest common path should be preferred
    assert session.state.project_dir == "C:\\Users\\Test\\ProjectA"


@pytest.mark.asyncio
async def test_deterministic_ignores_system_and_root_paths(
    mock_backend_service, mock_session_service, session
):
    prompt = (
        "My project is at C:\\Users\\Test\\Project, but I also have "
        "C:\\Windows and /etc/hosts mentioned."
    )
    request = ChatRequest(
        model="test-model", messages=[ChatMessage(role="user", content=prompt)]
    )
    config = create_app_config("deterministic")
    service = ProjectDirectoryResolutionService(
        config, mock_backend_service, mock_session_service
    )

    await service.maybe_resolve_project_directory(session, request)

    assert session.state.project_dir == "C:\\Users\\Test\\Project"


class TestExtractXmlFromResponse:
    def _build_service(self) -> ProjectDirectoryResolutionService:
        config = create_app_config("deterministic")
        mock = AsyncMock()
        mock.update_session = AsyncMock()
        return ProjectDirectoryResolutionService(config, AsyncMock(), mock)

    def test_strips_thinking_tags(self) -> None:
        service = self._build_service()
        response = (
            "I need to think about this.\n"
            "</think>\n"
            "<directory-resolution-response>"
            "<project-absolute-directory>/home/user/project</project-absolute-directory>"
            "</directory-resolution-response>"
        )
        result = service._extract_xml_from_response(response)
        assert result.startswith("<directory-resolution-response>")
        assert "/home/user/project" in result

    def test_strips_thinking_tags_before_xml(self) -> None:
        service = self._build_service()
        response = (
            "Let me reason.\n"
            "The path is probably /somewhere.\n"
            "</think>\n"
            "<directory-resolution-response>"
            "<project-absolute-directory>/home/user/project</project-absolute-directory>"
            "</directory-resolution-response>"
        )
        result = service._extract_xml_from_response(response)
        assert result.startswith("<directory-resolution-response>")

    def test_strips_reasoning_tags(self) -> None:
        service = self._build_service()
        response = (
            "<reasoning>The user wants a path.</reasoning>\n"
            "<directory-resolution-response>"
            "<project-absolute-directory>/home/user/project</project-absolute-directory>"
            "</directory-resolution-response>"
        )
        result = service._extract_xml_from_response(response)
        assert result.startswith("<directory-resolution-response>")

    def test_extracts_from_xml_code_block(self) -> None:
        service = self._build_service()
        response = (
            "Here is the response:\n\n"
            "```xml\n"
            "<directory-resolution-response>"
            "<project-absolute-directory>/home/user/project</project-absolute-directory>"
            "</directory-resolution-response>\n"
            "```\n\n"
            "Let me know if this helps."
        )
        result = service._extract_xml_from_response(response)
        assert result == (
            "<directory-resolution-response>"
            "<project-absolute-directory>/home/user/project</project-absolute-directory>"
            "</directory-resolution-response>"
        )

    def test_extracts_from_plain_code_block(self) -> None:
        service = self._build_service()
        response = (
            "```\n"
            "<directory-resolution-response>"
            "<project-absolute-directory>/home/user/project</project-absolute-directory>"
            "</directory-resolution-response>\n"
            "```"
        )
        result = service._extract_xml_from_response(response)
        assert result == (
            "<directory-resolution-response>"
            "<project-absolute-directory>/home/user/project</project-absolute-directory>"
            "</directory-resolution-response>"
        )

    def test_extracts_xml_from_surrounding_prose(self) -> None:
        service = self._build_service()
        response = (
            "Based on your instructions, the project directory is:\n"
            "<directory-resolution-response>"
            "<project-absolute-directory>/home/user/project</project-absolute-directory>"
            "</directory-resolution-response>\n"
            "Hope that helps!"
        )
        result = service._extract_xml_from_response(response)
        assert result.startswith("<directory-resolution-response>")
        assert "/home/user/project" in result

    def test_returns_original_when_no_xml_found(self) -> None:
        service = self._build_service()
        response = "I don't know what directory you mean."
        result = service._extract_xml_from_response(response)
        assert result == response

    def test_returns_clean_xml_when_already_correct(self) -> None:
        service = self._build_service()
        response = (
            "<directory-resolution-response>"
            "<project-absolute-directory>/home/user/project</project-absolute-directory>"
            "</directory-resolution-response>"
        )
        result = service._extract_xml_from_response(response)
        assert result == response


class TestParseDirectoryResponseWithNoisyInput:
    def _build_service(self) -> ProjectDirectoryResolutionService:
        config = create_app_config("deterministic")
        mock = AsyncMock()
        mock.update_session = AsyncMock()
        return ProjectDirectoryResolutionService(config, AsyncMock(), mock)

    def test_parses_xml_with_thinking_block(self) -> None:
        service = self._build_service()
        response = (
            "</think>\n"
            "<directory-resolution-response>"
            "<project-absolute-directory>/home/user/project</project-absolute-directory>"
            "</directory-resolution-response>"
        )
        directory, error = service._parse_directory_response(response)
        assert directory == "/home/user/project"
        assert error is None

    def test_parses_xml_from_markdown_code_block(self) -> None:
        service = self._build_service()
        response = (
            "```xml\n"
            "<directory-resolution-response>"
            "<project-absolute-directory>/home/user/project</project-absolute-directory>"
            "</directory-resolution-response>\n"
            "```"
        )
        directory, error = service._parse_directory_response(response)
        assert directory == "/home/user/project"
        assert error is None

    def test_parses_error_response_with_thinking_block(self) -> None:
        service = self._build_service()
        response = (
            "I'm not sure about this.\n"
            "</think>\n"
            "<directory-resolution-response>"
            "<error>Cannot determine the project directory from the prompt.</error>"
            "</directory-resolution-response>"
        )
        directory, error = service._parse_directory_response(response)
        assert directory is None
        assert error is not None
        assert "Cannot determine" in error

    def test_parses_xml_with_trailing_prose_after_block(self) -> None:
        service = self._build_service()
        response = (
            "<directory-resolution-response>"
            "<project-absolute-directory>/home/user/project</project-absolute-directory>"
            "</directory-resolution-response>\n"
            "Extra prose that should be ignored."
        )
        directory, error = service._parse_directory_response(response)
        assert directory == "/home/user/project"
        assert error is None

    def test_rejects_non_xml_response(self) -> None:
        service = self._build_service()
        response = "Sorry, I cannot help with that."
        directory, error = service._parse_directory_response(response)
        assert directory is None
        assert error is not None
        assert "invalid XML" in error

    def test_rejects_non_xml_response_without_traceback(self, caplog) -> None:
        service = self._build_service()
        with caplog.at_level(logging.WARNING):
            directory, error = service._parse_directory_response(
                "Sorry, I cannot help with that."
            )
        assert directory is None
        assert error is not None
        records = [
            record
            for record in caplog.records
            if "Failed to parse XML in directory response" in record.getMessage()
        ]
        assert records
        assert all(record.exc_info is None for record in records)
