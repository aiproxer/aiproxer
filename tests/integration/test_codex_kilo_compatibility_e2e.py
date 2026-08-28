"""End-to-end integration tests for Codex-KiloCode compatibility layer.

This test suite verifies complete workflows including:
- Read → Edit → Completion flow
- Search → Replace → Verify flow
- MCP tool usage
- Non-KiloCode client compatibility
- Codex with other clients
"""

from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import MagicMock, patch

import httpx
import pytest
import pytest_asyncio
from src.connectors.openai_codex import OpenAICodexConnector
from src.core.config.app_config import AppConfig
from src.core.services.translation_service import TranslationService


@pytest_asyncio.fixture(name="auth_dir")
async def auth_dir_tmp(tmp_path: Path):
    """Create temporary auth directory with credentials."""
    data = {"tokens": {"access_token": "test_token"}}
    tmp_path.mkdir(parents=True, exist_ok=True)
    (tmp_path / "auth.json").write_text(json.dumps(data), encoding="utf-8")
    return tmp_path


@pytest_asyncio.fixture(name="mock_file_system")
async def mock_file_system_fixture(tmp_path: Path):
    """Create a mock file system for testing."""
    # Create test files
    test_file = tmp_path / "test.py"
    test_file.write_text("def hello():\n    pass\n", encoding="utf-8")

    test_dir = tmp_path / "src"
    test_dir.mkdir()
    (test_dir / "main.py").write_text("print('hello')\n", encoding="utf-8")
    (test_dir / "utils.py").write_text("def util():\n    return 42\n", encoding="utf-8")

    return tmp_path


@pytest_asyncio.fixture(name="codex_connector")
async def codex_connector_fixture(auth_dir: Path, mock_file_system: Path):
    """Create connector with compatibility layer enabled."""
    async with httpx.AsyncClient() as client:
        cfg = AppConfig()
        ts = TranslationService()
        backend = OpenAICodexConnector(client, cfg, translation_service=ts)

        # Enable compatibility layer
        backend._connector_settings["compatibility_layer"]["enabled"] = True

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
            backend._auth_credentials = {"tokens": {"access_token": "test_token"}}

            # Initialize session detector
            from src.connectors._openai_codex_session_detector import SessionDetector

            detection_cfg = backend._connector_settings["compatibility_layer"][
                "detection"
            ]
            backend._session_detector = SessionDetector(
                cache_ttl_seconds=detection_cfg["cache_ttl_seconds"],
                heuristic_threshold=detection_cfg["heuristic_threshold"],
            )
            backend._compatibility_layer_enabled = True

            # Set working directory for file operations
            backend._working_directory = str(mock_file_system)

            yield backend


@pytest_asyncio.fixture(name="mock_codex_api")
async def mock_codex_api_fixture():
    """Create mock Codex API responses."""

    class MockCodexAPI:
        """Mock Codex API for testing."""

        def __init__(self):
            self.call_history = []
            self.responses = []

        def add_response(self, content: str, tool_calls: list | None = None):
            """Add a mock response."""
            response = {
                "id": f"codex-{len(self.responses)}",
                "object": "chat.completion",
                "created": 1234567890,
                "model": "gpt-5-codex",
                "choices": [
                    {
                        "index": 0,
                        "message": {
                            "role": "assistant",
                            "content": content,
                        },
                        "finish_reason": "stop",
                    }
                ],
                "usage": {
                    "prompt_tokens": 10,
                    "completion_tokens": 20,
                    "total_tokens": 30,
                },
            }

            if tool_calls:
                response["choices"][0]["message"]["tool_calls"] = tool_calls

            self.responses.append(response)

        async def chat_completions(self, *args, **kwargs):
            """Mock chat completions."""
            self.call_history.append({"args": args, "kwargs": kwargs})

            if self.responses:
                response = self.responses.pop(0)
                from src.core.domain.responses import ResponseEnvelope

                return ResponseEnvelope(
                    content=response,
                    status_code=200,
                    headers={"content-type": "application/json"},
                )

            # Default response
            from src.core.domain.responses import ResponseEnvelope

            return ResponseEnvelope(
                content={
                    "id": "codex-default",
                    "object": "chat.completion",
                    "created": 1234567890,
                    "model": "gpt-5-codex",
                    "choices": [
                        {
                            "index": 0,
                            "message": {
                                "role": "assistant",
                                "content": "OK",
                            },
                            "finish_reason": "stop",
                        }
                    ],
                    "usage": {
                        "prompt_tokens": 10,
                        "completion_tokens": 5,
                        "total_tokens": 15,
                    },
                },
                status_code=200,
                headers={"content-type": "application/json"},
            )

    return MockCodexAPI()


class TestReadEditCompletionFlow:
    """Test end-to-end read → edit → completion flow."""

    @pytest.mark.asyncio
    async def test_kilocode_read_edit_completion_workflow(
        self, codex_connector: OpenAICodexConnector, mock_file_system: Path
    ):
        """Test complete workflow: read file, edit it, complete task."""
        from src.connectors._openai_codex_kilo_tool_translator import KiloToolTranslator
        from src.core.services.universal_tool_executor import UniversalToolExecutor

        # Create translator and executor
        translator = KiloToolTranslator(codex_connector)
        executor = UniversalToolExecutor(
            working_directory=str(mock_file_system), result_format="kilo_standard"
        )

        # Step 1: Read file
        read_xml = '<read_file path="test.py" />'
        read_result = await translator.translate_tool_invocation(
            read_xml, session_id="test_session"
        )

        assert read_result is not None
        tool_name, arguments = read_result
        assert tool_name == "read_file"

        read_output = await executor.execute_tool(tool_name, arguments)
        assert read_output["exit_code"] == 0
        assert "def hello():" in read_output["output"]
        assert "[read_file] Result:" in read_output["output"]

        # Step 2: Edit file (search_and_replace; proxy write_to_file disabled)
        edit_xml = """<search_and_replace>
<path>test.py</path>
<search>    pass</search>
<replace>    print("world")</replace>
</search_and_replace>"""

        edit_result = await translator.translate_tool_invocation(
            edit_xml, session_id="test_session"
        )

        assert edit_result is not None
        tool_name, arguments = edit_result
        assert tool_name == "__proxy_search_and_replace"

        edit_output = await executor.execute_tool(tool_name, arguments)
        assert edit_output["exit_code"] == 0

        # Verify file was edited
        edited_content = (mock_file_system / "test.py").read_text(encoding="utf-8")
        assert 'print("world")' in edited_content

        # Step 3: Complete task
        completion_xml = (
            '<attempt_completion result="Successfully updated hello function" />'
        )

        completion_result = await translator.translate_tool_invocation(
            completion_xml, session_id="test_session"
        )

        assert completion_result is not None
        tool_name, arguments = completion_result
        assert tool_name == "__proxy_attempt_completion"

        # Execute the completion marker
        completion_output = await executor.execute_tool(tool_name, arguments)
        assert completion_output["exit_code"] == 0
        assert "[COMPLETION]" in completion_output["output"]
        assert "marker_type" in completion_output
        assert completion_output["marker_type"] == "completion"

    @pytest.mark.asyncio
    async def test_read_file_not_found_error(
        self, codex_connector: OpenAICodexConnector, mock_file_system: Path
    ):
        """Test read file with non-existent file returns error."""
        from src.connectors._openai_codex_kilo_tool_translator import KiloToolTranslator
        from src.core.services.universal_tool_executor import UniversalToolExecutor

        translator = KiloToolTranslator(codex_connector)
        executor = UniversalToolExecutor(
            working_directory=str(mock_file_system), result_format="kilo_standard"
        )

        read_xml = '<read_file path="nonexistent.py" />'
        read_result = await translator.translate_tool_invocation(
            read_xml, session_id="test_session"
        )

        assert read_result is not None
        tool_name, arguments = read_result

        read_output = await executor.execute_tool(tool_name, arguments)
        assert read_output["exit_code"] == 1
        assert "error" in read_output
        assert "nonexistent.py" in read_output["output"].lower()


class TestSearchReplaceVerifyFlow:
    """Test end-to-end search → replace → verify flow."""

    @pytest.mark.asyncio
    async def test_kilocode_search_replace_verify_workflow(
        self, codex_connector: OpenAICodexConnector, mock_file_system: Path
    ):
        """Test complete workflow: search for pattern, replace, verify."""
        from src.connectors._openai_codex_kilo_tool_translator import KiloToolTranslator
        from src.core.services.universal_tool_executor import UniversalToolExecutor

        translator = KiloToolTranslator(codex_connector)
        executor = UniversalToolExecutor(
            working_directory=str(mock_file_system), result_format="kilo_standard"
        )

        # Step 1: Search for pattern
        search_xml = '<codebase_search query="def " />'

        search_result = await translator.translate_tool_invocation(
            search_xml, session_id="test_session"
        )

        assert search_result is not None
        tool_name, arguments = search_result
        # codebase_search maps to grep_files in Codex
        assert tool_name == "grep_files"

        search_output = await executor.execute_tool(tool_name, arguments)
        assert search_output["exit_code"] == 0
        assert (
            "test.py" in search_output["output"]
            or "utils.py" in search_output["output"]
        )

        # Step 2: Replace content
        replace_xml = """<search_and_replace>
            <path>src/utils.py</path>
            <search>def util():</search>
            <replace>def utility():</replace>
        </search_and_replace>"""

        replace_result = await translator.translate_tool_invocation(
            replace_xml, session_id="test_session"
        )

        assert replace_result is not None
        tool_name, arguments = replace_result
        assert tool_name == "__proxy_search_and_replace"

        replace_output = await executor.execute_tool(tool_name, arguments)
        assert replace_output["exit_code"] == 0

        # Step 3: Verify the change
        verify_xml = '<read_file path="src/utils.py" />'

        verify_result = await translator.translate_tool_invocation(
            verify_xml, session_id="test_session"
        )

        assert verify_result is not None
        tool_name, arguments = verify_result

        verify_output = await executor.execute_tool(tool_name, arguments)
        assert verify_output["exit_code"] == 0
        assert "def utility():" in verify_output["output"]
        assert "def util():" not in verify_output["output"]

    @pytest.mark.asyncio
    async def test_search_with_include_pattern(
        self, codex_connector: OpenAICodexConnector, mock_file_system: Path
    ):
        """Test search with include pattern filters correctly."""
        from src.connectors._openai_codex_kilo_tool_translator import KiloToolTranslator
        from src.core.services.universal_tool_executor import UniversalToolExecutor

        translator = KiloToolTranslator(codex_connector)
        executor = UniversalToolExecutor(
            working_directory=str(mock_file_system), result_format="kilo_standard"
        )

        # Search only in src directory
        search_xml = '<search_files query="def " include="src/**/*.py" />'

        search_result = await translator.translate_tool_invocation(
            search_xml, session_id="test_session"
        )

        assert search_result is not None
        tool_name, arguments = search_result

        search_output = await executor.execute_tool(tool_name, arguments)
        # Search should complete successfully (exit_code 0 even if no matches)
        assert search_output["exit_code"] == 0
        # Verify the search was executed (output contains result marker)
        assert (
            "[grep_files]" in search_output["output"].lower()
            or "result" in search_output["output"].lower()
        )


class TestMcpXmlRejectedAtProxy:
    """MCP XML must not be translated into proxy-side MCP execution."""

    @pytest.mark.asyncio
    async def test_use_mcp_tool_raises_unsupported(self, codex_connector):
        from src.connectors._openai_codex_compatibility_errors import (
            CompatibilityErrorCode,
        )
        from src.connectors._openai_codex_kilo_tool_translator import (
            KiloToolTranslator,
            TranslationError,
        )

        translator = KiloToolTranslator(codex_connector)
        mcp_xml = """<use_mcp_tool name="custom_tool">
            <arguments>
                <param1>value1</param1>
            </arguments>
        </use_mcp_tool>"""

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(
                mcp_xml, session_id="test_session"
            )
        assert (
            exc_info.value.error_code == CompatibilityErrorCode.UNSUPPORTED_TOOL.value
        )

    @pytest.mark.asyncio
    async def test_access_mcp_resource_raises_unsupported(self, codex_connector):
        from src.connectors._openai_codex_compatibility_errors import (
            CompatibilityErrorCode,
        )
        from src.connectors._openai_codex_kilo_tool_translator import (
            KiloToolTranslator,
            TranslationError,
        )

        translator = KiloToolTranslator(codex_connector)
        resource_xml = '<access_mcp_resource uri="file://test/resource.txt" />'

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(
                resource_xml, session_id="test_session"
            )
        assert (
            exc_info.value.error_code == CompatibilityErrorCode.UNSUPPORTED_TOOL.value
        )


class TestNonKiloCodeClientCompatibility:
    """Test that non-KiloCode clients are unaffected by compatibility layer."""

    @pytest.mark.asyncio
    async def test_non_kilocode_client_bypasses_translation(
        self, codex_connector: OpenAICodexConnector
    ):
        """Test that non-KiloCode clients bypass the compatibility layer."""
        from src.connectors._openai_codex_session_detector import SessionDetector

        detector = codex_connector._session_detector
        assert isinstance(detector, SessionDetector)

        # Test with Cline client
        request_data = MagicMock()
        metadata = {"agent": "cline"}

        result = await detector.detect(
            request_data=request_data,
            metadata=metadata,
            session_id="cline_session",
            backend="openai-codex",
        )

        assert result.is_kilocode is False
        assert result.detection_method in ("metadata", "cached", "none")

    @pytest.mark.asyncio
    async def test_cursor_client_not_detected_as_kilocode(
        self, codex_connector: OpenAICodexConnector
    ):
        """Test that Cursor client is not detected as KiloCode."""
        from src.connectors._openai_codex_session_detector import SessionDetector

        detector = codex_connector._session_detector
        assert isinstance(detector, SessionDetector)

        request_data = MagicMock()
        metadata = {"agent": "cursor"}

        result = await detector.detect(
            request_data=request_data,
            metadata=metadata,
            session_id="cursor_session",
            backend="openai-codex",
        )

        assert result.is_kilocode is False

    @pytest.mark.asyncio
    async def test_non_kilocode_xml_not_translated(
        self, codex_connector: OpenAICodexConnector
    ):
        """Test that XML from non-KiloCode clients is not translated."""
        from src.connectors._openai_codex_kilo_tool_translator import KiloToolTranslator

        translator = KiloToolTranslator(codex_connector)

        # Non-KiloCode XML format (different structure)
        non_kilo_xml = '<tool name="read_file"><path>test.py</path></tool>'

        result = await translator.translate_tool_invocation(
            non_kilo_xml, session_id="test_session"
        )

        # Should return None as it doesn't match KiloCode patterns
        assert result is None


class TestCodexWithOtherClients:
    """Test that Codex backend works correctly with other clients."""

    @pytest.mark.asyncio
    async def test_codex_with_cline_client(self, auth_dir: Path):
        """Test Codex backend with Cline client."""
        async with httpx.AsyncClient() as client:
            cfg = AppConfig()
            ts = TranslationService()
            backend = OpenAICodexConnector(client, cfg, translation_service=ts)

            # Enable compatibility layer
            backend._connector_settings["compatibility_layer"]["enabled"] = True

            with (
                patch.object(
                    backend,
                    "_validate_credentials_file_exists",
                    return_value=(True, []),
                ),
                patch.object(
                    backend, "_validate_credentials_structure", return_value=(True, [])
                ),
                patch.object(backend, "_start_file_watching"),
            ):
                await backend.initialize(openai_codex_path=str(auth_dir))
                backend._auth_credentials = {"tokens": {"access_token": "test_token"}}

                # Initialize session detector
                from src.connectors._openai_codex_session_detector import (
                    SessionDetector,
                )

                detection_cfg = backend._connector_settings["compatibility_layer"][
                    "detection"
                ]
                backend._session_detector = SessionDetector(
                    cache_ttl_seconds=detection_cfg["cache_ttl_seconds"],
                    heuristic_threshold=detection_cfg["heuristic_threshold"],
                )
                backend._compatibility_layer_enabled = True

                # Detect Cline client
                request_data = MagicMock()
                metadata = {"agent": "cline"}

                result = await backend._session_detector.detect(
                    request_data=request_data,
                    metadata=metadata,
                    session_id="cline_session",
                    backend="openai-codex",
                )

                # Cline should not trigger compatibility layer
                assert result.is_kilocode is False

    @pytest.mark.asyncio
    async def test_codex_canonical_instructions_preserved_for_all_clients(
        self, codex_connector: OpenAICodexConnector
    ):
        """Test that canonical instructions are preserved for all clients."""
        # This is a critical requirement - Codex requires exact canonical instructions
        # The compatibility layer should not modify them for any client

        # Verify the connector has the capability resolver
        assert hasattr(codex_connector, "_capability_resolver")
        assert codex_connector._capability_resolver is not None

        # The actual content verification is done in snapshot tests
        # Here we just verify the connector is properly initialized

    @pytest.mark.asyncio
    async def test_compatibility_layer_disabled_affects_no_clients(
        self, auth_dir: Path
    ):
        """Test that disabling compatibility layer doesn't affect any clients."""
        async with httpx.AsyncClient() as client:
            cfg = AppConfig()
            ts = TranslationService()
            backend = OpenAICodexConnector(client, cfg, translation_service=ts)

            # Explicitly disable compatibility layer
            backend._connector_settings["compatibility_layer"]["enabled"] = False

            with (
                patch.object(
                    backend,
                    "_validate_credentials_file_exists",
                    return_value=(True, []),
                ),
                patch.object(
                    backend, "_validate_credentials_structure", return_value=(True, [])
                ),
                patch.object(backend, "_start_file_watching"),
            ):
                await backend.initialize(openai_codex_path=str(auth_dir))
                backend._auth_credentials = {"tokens": {"access_token": "test_token"}}

                # Verify compatibility layer is disabled
                assert backend._compatibility_layer_enabled is False
                assert backend._session_detector is None


class TestEndToEndWithMockCodexAPI:
    """Test complete end-to-end flows with mocked Codex API."""

    @pytest.mark.asyncio
    async def test_complete_kilocode_session_with_mock_api(
        self, codex_connector: OpenAICodexConnector, mock_file_system: Path
    ):
        """Test a complete KiloCode session with mocked Codex API responses."""
        from src.connectors._openai_codex_kilo_tool_translator import KiloToolTranslator
        from src.core.services.universal_tool_executor import UniversalToolExecutor

        translator = KiloToolTranslator(codex_connector)
        executor = UniversalToolExecutor(
            working_directory=str(mock_file_system), result_format="kilo_standard"
        )

        # Simulate a complete session
        session_id = "complete_session"

        # 1. List files
        list_xml = '<list_files path="." />'
        list_result = await translator.translate_tool_invocation(list_xml, session_id)
        assert list_result is not None
        list_output = await executor.execute_tool(*list_result)
        assert list_output["exit_code"] == 0

        # 2. Read a file
        read_xml = '<read_file path="test.py" />'
        read_result = await translator.translate_tool_invocation(read_xml, session_id)
        assert read_result is not None
        read_output = await executor.execute_tool(*read_result)
        assert read_output["exit_code"] == 0

        # 3. Search for pattern
        search_xml = '<codebase_search query="def" />'
        search_result = await translator.translate_tool_invocation(
            search_xml, session_id
        )
        assert search_result is not None
        search_output = await executor.execute_tool(*search_result)
        assert search_output["exit_code"] == 0

        # 4. Edit file
        edit_xml = """<search_and_replace>
<path>test.py</path>
<search>    pass</search>
<replace>    print("updated")</replace>
</search_and_replace>"""
        edit_result = await translator.translate_tool_invocation(edit_xml, session_id)
        assert edit_result is not None
        edit_output = await executor.execute_tool(*edit_result)
        assert edit_output["exit_code"] == 0

        # 5. Complete
        complete_xml = '<attempt_completion result="Task completed successfully" />'
        complete_result = await translator.translate_tool_invocation(
            complete_xml, session_id
        )
        assert complete_result is not None
        complete_output = await executor.execute_tool(*complete_result)
        assert complete_output["exit_code"] == 0

        # Verify all operations succeeded
        assert all(
            [
                list_output["exit_code"] == 0,
                read_output["exit_code"] == 0,
                search_output["exit_code"] == 0,
                edit_output["exit_code"] == 0,
                complete_output["exit_code"] == 0,
            ]
        )

    @pytest.mark.asyncio
    async def test_error_handling_in_workflow(
        self, codex_connector: OpenAICodexConnector, mock_file_system: Path
    ):
        """Test error handling throughout a workflow."""
        from src.connectors._openai_codex_kilo_tool_translator import KiloToolTranslator
        from src.core.services.universal_tool_executor import UniversalToolExecutor

        translator = KiloToolTranslator(codex_connector)
        executor = UniversalToolExecutor(
            working_directory=str(mock_file_system), result_format="kilo_standard"
        )

        # Try to read non-existent file
        read_xml = '<read_file path="nonexistent.py" />'
        read_result = await translator.translate_tool_invocation(
            read_xml, "error_session"
        )
        assert read_result is not None
        read_output = await executor.execute_tool(*read_result)
        assert read_output["exit_code"] == 1
        assert "error" in read_output

        # Try to search with invalid pattern (should still work but return no results)
        search_xml = '<codebase_search query="nonexistent_pattern_xyz" />'
        search_result = await translator.translate_tool_invocation(
            search_xml, "error_session"
        )
        assert search_result is not None
        search_output = await executor.execute_tool(*search_result)
        # Search should succeed even with no results
        assert search_output["exit_code"] == 0


class TestConversationControlTools:
    """Test that conversation control tools are not forwarded to Codex."""

    @pytest.mark.asyncio
    async def test_attempt_completion_not_sent_to_codex(
        self, codex_connector: OpenAICodexConnector, mock_file_system: Path
    ):
        """Verify attempt_completion tool is translated with __proxy_ prefix."""
        from src.connectors._openai_codex_kilo_tool_translator import KiloToolTranslator

        # Create translator
        translator = KiloToolTranslator(codex_connector)

        # Translate attempt_completion tool
        completion_xml = (
            "<attempt_completion>Task completed successfully</attempt_completion>"
        )
        result = await translator.translate_tool_invocation(
            completion_xml, session_id="test_session"
        )

        assert result is not None
        tool_name, arguments = result

        # Verify tool has __proxy_ prefix (indicating proxy-side execution, not sent to Codex)
        assert tool_name == "__proxy_attempt_completion"
        assert arguments["result"] == "Task completed successfully"

        # Verify the tool can be handled by conversation control handler
        formatted_result = await translator.handle_conversation_control(
            tool_name, arguments, "test_session"
        )
        assert "Task completion acknowledged" in formatted_result

    @pytest.mark.asyncio
    async def test_ask_followup_question_not_sent_to_codex(
        self, codex_connector: OpenAICodexConnector, mock_file_system: Path
    ):
        """Verify ask_followup_question tool is translated with __proxy_ prefix."""
        from src.connectors._openai_codex_kilo_tool_translator import KiloToolTranslator

        # Create translator
        translator = KiloToolTranslator(codex_connector)

        # Translate ask_followup_question tool
        followup_xml = "<ask_followup_question>Do you need any clarification?</ask_followup_question>"
        result = await translator.translate_tool_invocation(
            followup_xml, session_id="test_session"
        )

        assert result is not None
        tool_name, arguments = result

        # Verify tool has __proxy_ prefix (indicating proxy-side execution, not sent to Codex)
        assert tool_name == "__proxy_ask_followup_question"
        assert arguments["question"] == "Do you need any clarification?"

        # Verify the tool can be handled by conversation control handler
        formatted_result = await translator.handle_conversation_control(
            tool_name, arguments, "test_session"
        )
        assert "Question received" in formatted_result

    @pytest.mark.asyncio
    async def test_conversation_control_tools_have_proxy_prefix(
        self, codex_connector: OpenAICodexConnector, mock_file_system: Path
    ):
        """Verify conversation control tools have __proxy_ prefix."""
        from src.connectors._openai_codex_kilo_tool_translator import KiloToolTranslator

        translator = KiloToolTranslator(codex_connector)

        # Test attempt_completion
        completion_result = await translator.translate_tool_invocation(
            "<attempt_completion>Done</attempt_completion>", "test_session"
        )
        assert completion_result is not None
        # KiloTranslationResult can be unpacked as tuple for backward compatibility
        tool_name, _ = completion_result
        assert tool_name == "__proxy_attempt_completion"

        # Test ask_followup_question
        followup_result = await translator.translate_tool_invocation(
            "<ask_followup_question>Any questions?</ask_followup_question>",
            "test_session",
        )
        assert followup_result is not None
        tool_name, _ = followup_result
        assert tool_name == "__proxy_ask_followup_question"

        # Test regular tool (read_file) does NOT have __proxy_ prefix
        read_result = await translator.translate_tool_invocation(
            '<read_file path="test.py" />', "test_session"
        )
        assert read_result is not None
        tool_name, _ = read_result
        assert tool_name == "read_file"
        assert not tool_name.startswith("__proxy_")
