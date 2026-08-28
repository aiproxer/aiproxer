"""Unit tests for OpenAI Codex KiloToolTranslator."""

from unittest.mock import MagicMock

import pytest
from src.connectors._openai_codex_compatibility_errors import CompatibilityErrorCode
from src.connectors._openai_codex_kilo_tool_translator import (
    KiloToolTranslator,
    TranslationError,
)


@pytest.fixture
def mock_connector():
    """Create a mock OpenAI Codex connector."""
    connector = MagicMock()
    connector._get_universal_executor = MagicMock()
    return connector


@pytest.fixture
def translator(mock_connector):
    """Create a KiloToolTranslator instance."""
    return KiloToolTranslator(mock_connector)


class TestTranslateReadFile:
    """Test translation of <read_file> tags."""

    @pytest.mark.asyncio
    async def test_translate_read_file_simple_path(self, translator):
        """Test translating read_file with simple path."""
        xml = "<read_file>src/main.py</read_file>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "read_file"
        assert arguments["path"] == "src/main.py"
        assert arguments["file_path"] == "src/main.py"
        assert "start_line" not in arguments
        assert "end_line" not in arguments

    @pytest.mark.asyncio
    async def test_translate_read_file_with_line_range(self, translator):
        """Test translating read_file with line range."""
        xml = """<read_file>
            <path>src/utils.py</path>
            <start_line>10</start_line>
            <end_line>20</end_line>
        </read_file>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "read_file"
        assert arguments["path"] == "src/utils.py"
        assert arguments["file_path"] == "src/utils.py"
        assert arguments["start_line"] == 10
        assert arguments["end_line"] == 20

    @pytest.mark.asyncio
    async def test_translate_read_file_with_path_attribute(self, translator):
        """Test translating read_file with path as attribute."""
        xml = '<read_file path="config/settings.yaml" />'

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "read_file"
        assert arguments["path"] == "config/settings.yaml"
        assert arguments["file_path"] == "config/settings.yaml"

    @pytest.mark.asyncio
    async def test_translate_read_file_nested_path(self, translator):
        """Test translating read_file with nested path tag."""
        xml = "<read_file><path>tests/test_file.py</path></read_file>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "read_file"
        assert arguments["path"] == "tests/test_file.py"
        assert arguments["file_path"] == "tests/test_file.py"

    @pytest.mark.asyncio
    async def test_translate_read_file_with_relative_path(self, translator):
        """Test translating read_file with relative path."""
        xml = "<read_file>../parent/file.txt</read_file>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "read_file"
        assert arguments["path"] == "../parent/file.txt"
        assert arguments["file_path"] == "../parent/file.txt"

    @pytest.mark.asyncio
    async def test_translate_read_file_with_absolute_path(self, translator):
        """Test translating read_file with absolute path."""
        xml = "<read_file>/usr/local/bin/script.sh</read_file>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "read_file"
        assert arguments["path"] == "/usr/local/bin/script.sh"
        assert arguments["file_path"] == "/usr/local/bin/script.sh"


class TestTranslateListFiles:
    """Test translation of <list_files> tags."""

    @pytest.mark.asyncio
    async def test_translate_list_files_simple_path(self, translator):
        """Test translating list_files with simple path."""
        xml = "<list_files>src/</list_files>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "list_dir"
        assert arguments["path"] == "src/"
        assert arguments["dir_path"] == "src/"
        assert "depth" not in arguments

    @pytest.mark.asyncio
    async def test_translate_list_files_with_recursive_true(self, translator):
        """Test translating list_files with recursive=true."""
        xml = '<list_files path="src/" recursive="true" />'

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "list_dir"
        assert arguments["path"] == "src/"
        assert arguments["dir_path"] == "src/"
        assert arguments["depth"] == 3  # Default depth for recursive

    @pytest.mark.asyncio
    async def test_translate_list_files_with_recursive_false(self, translator):
        """Test translating list_files with recursive=false."""
        xml = '<list_files path="src/" recursive="false" />'

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "list_dir"
        assert arguments["path"] == "src/"
        assert arguments["dir_path"] == "src/"
        assert "depth" not in arguments

    @pytest.mark.asyncio
    async def test_translate_list_files_with_explicit_depth(self, translator):
        """Test translating list_files with explicit depth."""
        xml = """<list_files>
            <path>src/</path>
            <recursive>true</recursive>
            <depth>5</depth>
        </list_files>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "list_dir"
        assert arguments["path"] == "src/"
        assert arguments["dir_path"] == "src/"
        assert arguments["depth"] == 5

    @pytest.mark.asyncio
    async def test_translate_list_files_default_path(self, translator):
        """Test translating list_files with no path (defaults to current dir)."""
        xml = "<list_files></list_files>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "list_dir"
        assert arguments["path"] == "."
        assert arguments["dir_path"] == "."

    @pytest.mark.asyncio
    async def test_translate_list_files_nested_tags(self, translator):
        """Test translating list_files with nested tags."""
        xml = """<list_files>
            <path>tests/</path>
            <recursive>true</recursive>
        </list_files>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "list_dir"
        assert arguments["path"] == "tests/"
        assert arguments["dir_path"] == "tests/"
        assert arguments["depth"] == 3


class TestTranslateExecuteCommand:
    """Test translation of <execute_command> tags."""

    @pytest.mark.asyncio
    async def test_translate_execute_command_simple(self, translator):
        """Test translating execute_command with simple command."""
        xml = "<execute_command>ls -la</execute_command>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "shell"
        assert arguments["command"] == ["ls", "-la"]
        assert "working_dir" not in arguments
        assert "timeout" not in arguments

    @pytest.mark.asyncio
    async def test_translate_execute_command_with_working_dir(self, translator):
        """Test translating execute_command with working directory."""
        xml = '<execute_command command="npm test" working_dir="/app/frontend" />'

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "shell"
        assert arguments["command"] == ["npm", "test"]
        assert arguments["workdir"] == "/app/frontend"
        assert arguments["working_dir"] == "/app/frontend"

    @pytest.mark.asyncio
    async def test_translate_execute_command_with_timeout(self, translator):
        """Test translating execute_command with timeout."""
        xml = """<execute_command>
            <command>python script.py</command>
            <timeout>30</timeout>
        </execute_command>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "shell"
        assert arguments["command"] == ["python", "script.py"]
        assert arguments["timeout"] == 30

    @pytest.mark.asyncio
    async def test_translate_execute_command_with_all_params(self, translator):
        """Test translating execute_command with all parameters."""
        xml = """<execute_command>
            <command>cargo build --release</command>
            <working_dir>/home/user/project</working_dir>
            <timeout>120</timeout>
        </execute_command>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "shell"
        assert arguments["command"] == ["cargo", "build", "--release"]
        assert arguments["workdir"] == "/home/user/project"
        assert arguments["working_dir"] == "/home/user/project"
        assert arguments["timeout"] == 120

    @pytest.mark.asyncio
    async def test_translate_execute_command_complex_command(self, translator):
        """Test translating execute_command with complex command string."""
        xml = "<execute_command>git log --oneline --graph --all | head -n 20</execute_command>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "shell"
        assert arguments["command"] == [
            "git",
            "log",
            "--oneline",
            "--graph",
            "--all",
            "|",
            "head",
            "-n",
            "20",
        ]

    @pytest.mark.asyncio
    async def test_translate_execute_command_with_quotes(self, translator):
        """Test translating execute_command with quoted arguments."""
        xml = """<execute_command>echo "Hello, World!"</execute_command>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "shell"
        assert arguments["command"] == ["echo", "Hello, World!"]


class TestResultFormatting:
    """Test formatting of tool execution results."""

    def test_format_tool_result_simple_output(self, translator):
        """Test formatting result with simple output."""
        result = {
            "output": "File contents here",
        }

        formatted = translator.format_tool_result("read_file", result)

        assert formatted.startswith("[read_file] Result:")
        assert "File contents here" in formatted

    def test_format_tool_result_with_exit_code(self, translator):
        """Test formatting result with exit code for shell command."""
        result = {
            "output": "Command output",
            "exit_code": 0,
        }

        formatted = translator.format_tool_result("shell", result)

        assert formatted.startswith("[shell] Result:")
        assert "Command output" in formatted
        assert "Exit code: 0" in formatted

    def test_format_tool_result_with_error(self, translator):
        """Test formatting result with error."""
        result = {
            "output": "",
            "error": "File not found",
        }

        formatted = translator.format_tool_result("read_file", result)

        assert formatted.startswith("[read_file] Result:")
        assert "Error: File not found" in formatted

    def test_format_tool_result_empty_output(self, translator):
        """Test formatting result with empty output."""
        result = {
            "output": "",
        }

        formatted = translator.format_tool_result("list_dir", result)

        assert formatted.startswith("[list_dir] Result:")

    def test_format_tool_result_multiline_output(self, translator):
        """Test formatting result with multiline output."""
        result = {
            "output": "Line 1\nLine 2\nLine 3",
        }

        formatted = translator.format_tool_result("read_file", result)

        assert formatted.startswith("[read_file] Result:")
        assert "Line 1" in formatted
        assert "Line 2" in formatted
        assert "Line 3" in formatted

    def test_format_tool_result_command_with_nonzero_exit(self, translator):
        """Test formatting result for command with non-zero exit code."""
        result = {
            "output": "Error: command failed",
            "exit_code": 1,
        }

        formatted = translator.format_tool_result("shell", result)

        assert formatted.startswith("[shell] Result:")
        assert "Error: command failed" in formatted
        assert "Exit code: 1" in formatted


class TestErrorHandling:
    """Test error handling in translation."""

    @pytest.mark.asyncio
    async def test_translate_invalid_xml_returns_none(self, translator):
        """Test that invalid XML returns None (no supported tags found)."""
        xml = "<read_file>unclosed tag"

        # Invalid XML that doesn't match any supported tags returns None
        result = await translator.translate_tool_invocation(xml)

        assert result is None

    @pytest.mark.asyncio
    async def test_translate_empty_string_returns_none(self, translator):
        """Test that empty string returns None."""
        result = await translator.translate_tool_invocation("")

        assert result is None

    @pytest.mark.asyncio
    async def test_translate_none_returns_none(self, translator):
        """Test that None returns None."""
        result = await translator.translate_tool_invocation(None)  # type: ignore

        assert result is None

    @pytest.mark.asyncio
    async def test_translate_unsupported_tool_returns_none(self, translator):
        """Test that unsupported tool returns None (not an error)."""
        xml = "<browser_action>navigate to https://example.com</browser_action>"

        result = await translator.translate_tool_invocation(xml)

        # Unsupported tools return None, not an error
        assert result is None

    @pytest.mark.asyncio
    async def test_translate_whitespace_only_returns_none(self, translator):
        """Test that whitespace-only string returns None."""
        result = await translator.translate_tool_invocation("   \n\t  ")

        assert result is None


class TestParameterValidation:
    """Test parameter validation during translation."""

    @pytest.mark.asyncio
    async def test_read_file_missing_path_raises_error(self, translator):
        """Test that read_file without path raises error during translation."""
        # This should be caught by the parser, but test the translator's handling
        xml = "<read_file></read_file>"

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)

        # The parser will raise an error first
        assert exc_info.value.error_code in ("COMPAT_E002", "COMPAT_E003")

    @pytest.mark.asyncio
    async def test_execute_command_missing_command_raises_error(self, translator):
        """Test that execute_command without command raises error."""
        xml = "<execute_command></execute_command>"

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)

        # The parser will raise an error first
        assert exc_info.value.error_code in ("COMPAT_E002", "COMPAT_E003")


class TestTranslateSearch:
    """Test translation of <codebase_search> and <search_files> tags."""

    @pytest.mark.asyncio
    async def test_translate_codebase_search_simple_query(self, translator):
        """Test translating codebase_search with simple query."""
        xml = "<codebase_search>def main</codebase_search>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["pattern"] == "def main"
        assert arguments["path"] == "."
        assert arguments["recursive"] is True
        assert arguments["case_sensitive"] is True

    @pytest.mark.asyncio
    async def test_translate_codebase_search_with_nested_query(self, translator):
        """Test translating codebase_search with nested query tag."""
        xml = """<codebase_search>
            <query>import asyncio</query>
        </codebase_search>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["pattern"] == "import asyncio"

    @pytest.mark.asyncio
    async def test_translate_search_files_with_pattern(self, translator):
        """Test translating search_files with glob pattern."""
        xml = '<search_files query="TODO" pattern="*.py" />'

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["pattern"] == "TODO"
        assert arguments["include"] == "*.py"

    @pytest.mark.asyncio
    async def test_translate_search_files_with_include_pattern(self, translator):
        """Test translating search_files with include pattern."""
        xml = """<search_files>
            <query>class \w+</query>
            <include>*.py</include>
        </search_files>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["pattern"] == "class \\w+"
        assert arguments["include"] == "*.py"

    @pytest.mark.asyncio
    async def test_translate_search_files_with_exclude_pattern(self, translator):
        """Test translating search_files with exclude pattern."""
        xml = """<search_files>
            <query>error</query>
            <exclude>*.log</exclude>
        </search_files>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["pattern"] == "error"
        assert arguments["exclude"] == "*.log"

    @pytest.mark.asyncio
    async def test_translate_search_files_with_include_and_exclude(self, translator):
        """Test translating search_files with both include and exclude patterns."""
        xml = """<search_files>
            <query>function</query>
            <include>*.js</include>
            <exclude>*.test.js</exclude>
        </search_files>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["pattern"] == "function"
        assert arguments["include"] == "*.js"
        assert arguments["exclude"] == "*.test.js"

    @pytest.mark.asyncio
    async def test_translate_search_with_path(self, translator):
        """Test translating search with specific path."""
        xml = """<codebase_search>
            <query>import</query>
            <path>src/</path>
        </codebase_search>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["pattern"] == "import"
        assert arguments["path"] == "src/"

    @pytest.mark.asyncio
    async def test_translate_search_with_recursive_false(self, translator):
        """Test translating search with recursive=false."""
        xml = '<search_files query="test" recursive="false" />'

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["pattern"] == "test"
        assert arguments["recursive"] is False

    @pytest.mark.asyncio
    async def test_translate_search_with_recursive_true(self, translator):
        """Test translating search with recursive=true."""
        xml = '<search_files query="test" recursive="true" />'

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["pattern"] == "test"
        assert arguments["recursive"] is True

    @pytest.mark.asyncio
    async def test_translate_search_complex_regex_pattern(self, translator):
        """Test translating search with complex regex pattern."""
        xml = "<codebase_search>async def \\w+\\(.*\\):</codebase_search>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["pattern"] == "async def \\w+\\(.*\\):"

    @pytest.mark.asyncio
    async def test_translate_search_with_all_parameters(self, translator):
        """Test translating search with all parameters."""
        xml = """<search_files>
            <query>TODO|FIXME</query>
            <path>src/</path>
            <include>*.py</include>
            <exclude>*_test.py</exclude>
            <recursive>true</recursive>
        </search_files>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["pattern"] == "TODO|FIXME"
        assert arguments["path"] == "src/"
        assert arguments["include"] == "*.py"
        assert arguments["exclude"] == "*_test.py"
        assert arguments["recursive"] is True

    @pytest.mark.asyncio
    async def test_translate_search_missing_query_raises_error(self, translator):
        """Test that search without query raises error."""
        xml = "<codebase_search></codebase_search>"

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)

        assert exc_info.value.error_code in ("COMPAT_E002", "COMPAT_E003")

    @pytest.mark.asyncio
    async def test_translate_search_with_pattern_and_query(self, translator):
        """Test that pattern parameter is used as include when query is separate."""
        xml = """<search_files>
            <query>class</query>
            <pattern>*.py</pattern>
        </search_files>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["pattern"] == "class"
        assert arguments["include"] == "*.py"

    @pytest.mark.asyncio
    async def test_translate_search_defaults_to_current_directory(self, translator):
        """Test that search defaults to current directory when no path specified."""
        xml = "<codebase_search>search term</codebase_search>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["path"] == "."

    @pytest.mark.asyncio
    async def test_translate_search_defaults_to_recursive(self, translator):
        """Test that search defaults to recursive=true."""
        xml = "<codebase_search>search term</codebase_search>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["recursive"] is True

    @pytest.mark.asyncio
    async def test_translate_search_defaults_to_case_sensitive(self, translator):
        """Test that search defaults to case_sensitive=true."""
        xml = "<codebase_search>search term</codebase_search>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "grep_files"
        assert arguments["case_sensitive"] is True


class TestSearchResultFormatting:
    """Test formatting of search tool results."""

    def test_format_search_result_with_matches(self, translator):
        """Test formatting search result with matches."""
        result = {
            "output": "src/main.py:10:def main():\nsrc/utils.py:25:def main_helper():",
            "exit_code": 0,
            "matches_count": 2,
        }

        formatted = translator.format_tool_result("grep_files", result)

        assert formatted.startswith("[grep_files] Result:")
        assert "src/main.py:10:def main():" in formatted
        assert "Matches found: 2" in formatted

    def test_format_search_result_no_matches(self, translator):
        """Test formatting search result with no matches."""
        result = {
            "output": "No matches found for pattern: nonexistent",
            "exit_code": 0,
            "matches_count": 0,
        }

        formatted = translator.format_tool_result("grep_files", result)

        assert formatted.startswith("[grep_files] Result:")
        assert "No matches found" in formatted
        assert "Matches found: 0" in formatted

    def test_format_search_result_with_error(self, translator):
        """Test formatting search result with error."""
        result = {
            "output": "Error: Invalid regex pattern",
            "exit_code": 1,
            "error": "Invalid regex",
        }

        formatted = translator.format_tool_result("grep_files", result)

        assert formatted.startswith("[grep_files] Result:")
        assert "Error: Invalid regex" in formatted

    def test_format_codebase_search_result(self, translator):
        """Test formatting codebase_search result (alias for grep_files)."""
        result = {
            "output": "file.py:1:match",
            "exit_code": 0,
            "matches_count": 1,
        }

        formatted = translator.format_tool_result("codebase_search", result)

        assert formatted.startswith("[codebase_search] Result:")
        assert "file.py:1:match" in formatted
        assert "Matches found: 1" in formatted

    def test_format_search_files_result(self, translator):
        """Test formatting search_files result (alias for grep_files)."""
        result = {
            "output": "test.py:5:test case",
            "exit_code": 0,
            "matches_count": 1,
        }

        formatted = translator.format_tool_result("search_files", result)

        assert formatted.startswith("[search_files] Result:")
        assert "test.py:5:test case" in formatted
        assert "Matches found: 1" in formatted


class TestConversationControl:
    """Test conversation control handlers (attempt_completion, ask_followup_question)."""

    @pytest.mark.asyncio
    async def test_translate_attempt_completion_with_result(self, translator):
        """Test translating attempt_completion with result message."""
        xml = """<attempt_completion>
            <result>Task completed successfully</result>
        </attempt_completion>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "__proxy_attempt_completion"
        assert arguments["result"] == "Task completed successfully"

    @pytest.mark.asyncio
    async def test_translate_attempt_completion_simple_content(self, translator):
        """Test translating attempt_completion with simple content."""
        xml = "<attempt_completion>All tests passed</attempt_completion>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "__proxy_attempt_completion"
        assert arguments["result"] == "All tests passed"

    @pytest.mark.asyncio
    async def test_translate_attempt_completion_empty(self, translator):
        """Test translating attempt_completion with no content."""
        xml = "<attempt_completion></attempt_completion>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "__proxy_attempt_completion"
        assert arguments["result"] == ""

    @pytest.mark.asyncio
    async def test_translate_ask_followup_question_simple(self, translator):
        """Test translating ask_followup_question with simple question."""
        xml = "<ask_followup_question>What should I do next?</ask_followup_question>"

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "__proxy_ask_followup_question"
        assert arguments["question"] == "What should I do next?"

    @pytest.mark.asyncio
    async def test_translate_ask_followup_question_with_nested_tag(self, translator):
        """Test translating ask_followup_question with nested question tag."""
        xml = """<ask_followup_question>
            <question>Should I proceed with deployment?</question>
        </ask_followup_question>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "__proxy_ask_followup_question"
        assert arguments["question"] == "Should I proceed with deployment?"

    @pytest.mark.asyncio
    async def test_translate_ask_followup_question_missing_question_raises_error(
        self, translator
    ):
        """Test that ask_followup_question without question raises error."""
        xml = "<ask_followup_question></ask_followup_question>"

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)

        # The parser will raise an error first
        assert exc_info.value.error_code in ("COMPAT_E002", "COMPAT_E003")

    @pytest.mark.asyncio
    async def test_handle_attempt_completion_proxy_side(self, translator):
        """Test proxy-side handling of attempt_completion."""
        tool_name = "__proxy_attempt_completion"
        arguments = {"result": "Task completed successfully"}

        response = await translator.handle_conversation_control(
            tool_name, arguments, session_id="test-session-123"
        )

        assert "[attempt_completion]" in response
        assert "Task completion acknowledged" in response
        assert "Task completed successfully" in response

    @pytest.mark.asyncio
    async def test_handle_attempt_completion_empty_result(self, translator):
        """Test proxy-side handling of attempt_completion with empty result."""
        tool_name = "__proxy_attempt_completion"
        arguments = {"result": ""}

        response = await translator.handle_conversation_control(
            tool_name, arguments, session_id="test-session-456"
        )

        assert "[attempt_completion]" in response
        assert "Task completion acknowledged" in response

    @pytest.mark.asyncio
    async def test_handle_ask_followup_question_proxy_side(self, translator):
        """Test proxy-side handling of ask_followup_question."""
        tool_name = "__proxy_ask_followup_question"
        arguments = {"question": "What should I do next?"}

        response = await translator.handle_conversation_control(
            tool_name, arguments, session_id="test-session-789"
        )

        assert "[ask_followup_question]" in response
        assert "Question received" in response
        assert "What should I do next?" in response

    @pytest.mark.asyncio
    async def test_handle_conversation_control_without_session_id(self, translator):
        """Test conversation control handling without session ID."""
        tool_name = "__proxy_attempt_completion"
        arguments = {"result": "Done"}

        # Should work without session_id
        response = await translator.handle_conversation_control(tool_name, arguments)

        assert "[attempt_completion]" in response
        assert "Done" in response

    @pytest.mark.asyncio
    async def test_handle_conversation_control_unknown_tool_raises_error(
        self, translator
    ):
        """Test that unknown conversation control tool raises error."""
        tool_name = "__proxy_unknown_tool"
        arguments = {}

        with pytest.raises(TranslationError) as exc_info:
            await translator.handle_conversation_control(tool_name, arguments)

        assert exc_info.value.error_code == "COMPAT_E001"
        assert "Unknown conversation control tool" in str(exc_info.value)

    @pytest.mark.asyncio
    async def test_conversation_control_tags_not_forwarded_to_codex(self, translator):
        """Test that conversation control tags return proxy markers, not Codex tools.

        This ensures that attempt_completion and ask_followup_question are handled
        proxy-side and never forwarded to Codex backend.
        """
        # Test attempt_completion
        xml_completion = "<attempt_completion>Task done</attempt_completion>"
        result_completion = await translator.translate_tool_invocation(xml_completion)

        assert result_completion is not None
        tool_name_completion, _ = result_completion
        # Should return proxy marker, not a Codex tool name
        assert tool_name_completion == "__proxy_attempt_completion"
        assert not tool_name_completion.startswith("codex_")
        assert tool_name_completion.startswith("__proxy_")

        # Test ask_followup_question
        xml_question = "<ask_followup_question>What next?</ask_followup_question>"
        result_question = await translator.translate_tool_invocation(xml_question)

        assert result_question is not None
        tool_name_question, _ = result_question
        # Should return proxy marker, not a Codex tool name
        assert tool_name_question == "__proxy_ask_followup_question"
        assert not tool_name_question.startswith("codex_")
        assert tool_name_question.startswith("__proxy_")

    @pytest.mark.asyncio
    async def test_acknowledgment_response_format(self, translator):
        """Test that acknowledgment responses follow expected format."""
        # Test attempt_completion acknowledgment
        response_completion = await translator.handle_conversation_control(
            "__proxy_attempt_completion",
            {"result": "All tests passed"},
            session_id="test-123",
        )

        assert response_completion.startswith("[attempt_completion]")
        assert "Task completion acknowledged" in response_completion
        assert "All tests passed" in response_completion

        # Test ask_followup_question acknowledgment
        response_question = await translator.handle_conversation_control(
            "__proxy_ask_followup_question",
            {"question": "Should I continue?"},
            session_id="test-456",
        )

        assert response_question.startswith("[ask_followup_question]")
        assert "Question received" in response_question
        assert "Should I continue?" in response_question


class TestMcpXmlRejected:
    """<use_mcp_tool> / <access_mcp_resource> are not executed by the proxy."""

    @pytest.mark.asyncio
    @pytest.mark.parametrize(
        "xml",
        [
            """<use_mcp_tool name="patch_file">
            <arguments>
                <diff>--- a/file.py
+++ b/file.py
@@ -1,3 +1,3 @@
-old line
+new line
</diff>
            </arguments>
        </use_mcp_tool>""",
            """<use_mcp_tool name="custom_tool">
            <arguments>
                <param1>value1</param1>
            </arguments>
        </use_mcp_tool>""",
            '<use_mcp_tool name="simple_tool"></use_mcp_tool>',
        ],
    )
    async def test_use_mcp_tool_always_unsupported(self, translator, xml):
        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)
        assert (
            exc_info.value.error_code == CompatibilityErrorCode.UNSUPPORTED_TOOL.value
        )
        assert exc_info.value.tool_name == "use_mcp_tool"

    @pytest.mark.asyncio
    async def test_access_mcp_resource_unsupported(self, translator):
        xml = '<access_mcp_resource uri="file://test/resource.txt" />'
        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)
        assert (
            exc_info.value.error_code == CompatibilityErrorCode.UNSUPPORTED_TOOL.value
        )
        assert exc_info.value.tool_name == "access_mcp_resource"


class TestTranslateSearchAndReplace:
    """Test translation of <search_and_replace> tags."""

    @pytest.mark.asyncio
    async def test_translate_search_and_replace_basic(self, translator):
        """Test translating search_and_replace with all required parameters."""
        xml = """<search_and_replace>
            <path>src/main.py</path>
            <search>old_function</search>
            <replace>new_function</replace>
        </search_and_replace>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "__proxy_search_and_replace"
        assert arguments["path"] == "src/main.py"
        assert arguments["search"] == "old_function"
        assert arguments["replace"] == "new_function"

    @pytest.mark.asyncio
    async def test_translate_search_and_replace_multiline(self, translator):
        """Test translating search_and_replace with multiline content."""
        xml = """<search_and_replace>
            <path>config.yaml</path>
            <search>old:
  value: 1</search>
            <replace>new:
  value: 2</replace>
        </search_and_replace>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "__proxy_search_and_replace"
        assert arguments["path"] == "config.yaml"
        assert "old:" in arguments["search"]
        assert "new:" in arguments["replace"]

    @pytest.mark.asyncio
    async def test_translate_search_and_replace_missing_path_raises_error(
        self, translator
    ):
        """Test that search_and_replace without path raises error."""
        xml = """<search_and_replace>
            <search>old</search>
            <replace>new</replace>
        </search_and_replace>"""

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)

        assert exc_info.value.error_code in ("COMPAT_E002", "COMPAT_E003")

    @pytest.mark.asyncio
    async def test_translate_search_and_replace_missing_search_raises_error(
        self, translator
    ):
        """Test that search_and_replace without search raises error."""
        xml = """<search_and_replace>
            <path>file.py</path>
            <replace>new</replace>
        </search_and_replace>"""

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)

        assert exc_info.value.error_code in ("COMPAT_E002", "COMPAT_E003")

    @pytest.mark.asyncio
    async def test_translate_search_and_replace_missing_replace_raises_error(
        self, translator
    ):
        """Test that search_and_replace without replace raises error."""
        xml = """<search_and_replace>
            <path>file.py</path>
            <search>old</search>
        </search_and_replace>"""

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)

        assert exc_info.value.error_code in ("COMPAT_E002", "COMPAT_E003")


class TestTranslateWriteToFile:
    """<write_to_file> is rejected at the proxy (not translated to a proxy tool)."""

    @pytest.mark.asyncio
    async def test_translate_write_to_file_rejected(self, translator):
        xml = """<write_to_file>
            <path>output.txt</path>
            <content>Hello, World!</content>
        </write_to_file>"""

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)

        assert (
            exc_info.value.error_code == CompatibilityErrorCode.UNSUPPORTED_TOOL.value
        )
        assert exc_info.value.tool_name == "write_to_file"

    @pytest.mark.asyncio
    async def test_translate_write_to_file_multiline_rejected(self, translator):
        xml = """<write_to_file>
            <path>script.py</path>
            <content>line1
line2</content>
        </write_to_file>"""

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)

        assert (
            exc_info.value.error_code == CompatibilityErrorCode.UNSUPPORTED_TOOL.value
        )

    @pytest.mark.asyncio
    async def test_translate_write_to_file_missing_path_still_rejected(
        self, translator
    ):
        xml = """<write_to_file>
            <content>content</content>
        </write_to_file>"""

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)

        assert exc_info.value.error_code in (
            CompatibilityErrorCode.UNSUPPORTED_TOOL.value,
            CompatibilityErrorCode.INVALID_XML_SYNTAX.value,
        )

    @pytest.mark.asyncio
    async def test_translate_write_to_file_missing_content_still_rejected(
        self, translator
    ):
        xml = """<write_to_file>
            <path>file.txt</path>
        </write_to_file>"""

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)

        assert exc_info.value.error_code in (
            CompatibilityErrorCode.UNSUPPORTED_TOOL.value,
            CompatibilityErrorCode.INVALID_XML_SYNTAX.value,
        )


class TestTranslateInsertContent:
    """Test translation of <insert_content> tags."""

    @pytest.mark.asyncio
    async def test_translate_insert_content_basic(self, translator):
        """Test translating insert_content with path and content."""
        xml = """<insert_content>
            <path>file.py</path>
            <content>new_line_content</content>
        </insert_content>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "__proxy_insert_content"
        assert arguments["path"] == "file.py"
        assert arguments["content"] == "new_line_content"
        assert "position" not in arguments

    @pytest.mark.asyncio
    async def test_translate_insert_content_with_position(self, translator):
        """Test translating insert_content with position parameter."""
        xml = """<insert_content>
            <path>file.py</path>
            <content>import os</content>
            <position>5</position>
        </insert_content>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "__proxy_insert_content"
        assert arguments["path"] == "file.py"
        assert arguments["content"] == "import os"
        assert arguments["position"] == 5

    @pytest.mark.asyncio
    async def test_translate_insert_content_multiline(self, translator):
        """Test translating insert_content with multiline content."""
        xml = """<insert_content>
            <path>module.py</path>
            <content>def new_function():
    pass
</content>
            <position>10</position>
        </insert_content>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "__proxy_insert_content"
        assert arguments["path"] == "module.py"
        assert "def new_function():" in arguments["content"]
        assert arguments["position"] == 10

    @pytest.mark.asyncio
    async def test_translate_insert_content_missing_path_raises_error(self, translator):
        """Test that insert_content without path raises error."""
        xml = """<insert_content>
            <content>content</content>
        </insert_content>"""

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)

        assert exc_info.value.error_code in ("COMPAT_E002", "COMPAT_E003")

    @pytest.mark.asyncio
    async def test_translate_insert_content_missing_content_raises_error(
        self, translator
    ):
        """Test that insert_content without content raises error."""
        xml = """<insert_content>
            <path>file.py</path>
        </insert_content>"""

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)

        # Error can be COMPAT_E002/E003 (parsing/validation) or COMPAT_E007 (wrapped)
        assert exc_info.value.error_code in (
            "COMPAT_E002",
            "COMPAT_E003",
            "COMPAT_E007",
        )


class TestTranslateEditFile:
    """Test translation of <edit_file> tags."""

    @pytest.mark.asyncio
    async def test_translate_edit_file_with_content(self, translator):
        """Test translating edit_file with path and content."""
        xml = """<edit_file>
            <path>config.json</path>
            <content>{"key": "value"}</content>
        </edit_file>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "__proxy_edit_file"
        assert arguments["path"] == "config.json"
        assert arguments["content"] == '{"key": "value"}'

    @pytest.mark.asyncio
    async def test_translate_edit_file_without_content(self, translator):
        """Test translating edit_file with only path (no content)."""
        xml = """<edit_file>
            <path>file.txt</path>
        </edit_file>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "__proxy_edit_file"
        assert arguments["path"] == "file.txt"
        assert "content" not in arguments

    @pytest.mark.asyncio
    async def test_translate_edit_file_multiline_content(self, translator):
        """Test translating edit_file with multiline content."""
        xml = """<edit_file>
            <path>README.md</path>
            <content># Project Title

## Description
This is a test project.
</content>
        </edit_file>"""

        result = await translator.translate_tool_invocation(xml)

        assert result is not None
        tool_name, arguments = result
        assert tool_name == "__proxy_edit_file"
        assert arguments["path"] == "README.md"
        assert "# Project Title" in arguments["content"]
        assert "## Description" in arguments["content"]

    @pytest.mark.asyncio
    async def test_translate_edit_file_missing_path_raises_error(self, translator):
        """Test that edit_file without path raises error."""
        xml = """<edit_file>
            <content>content</content>
        </edit_file>"""

        with pytest.raises(TranslationError) as exc_info:
            await translator.translate_tool_invocation(xml)

        assert exc_info.value.error_code in ("COMPAT_E002", "COMPAT_E003")


class TestEditingToolResultFormatting:
    """Test formatting of editing tool results."""

    def test_format_search_and_replace_result(self, translator):
        """Test formatting search_and_replace result."""
        result = {
            "output": "Successfully replaced 3 occurrence(s) in file.py",
            "exit_code": 0,
            "occurrences": 3,
        }

        formatted = translator.format_tool_result("search_and_replace", result)

        assert formatted.startswith("[search_and_replace] Result:")
        assert "Successfully replaced 3 occurrence(s)" in formatted

    def test_format_write_to_file_result(self, translator):
        """Test formatting write_to_file result."""
        result = {
            "output": "Successfully wrote 1024 bytes to output.txt",
            "exit_code": 0,
            "size": 1024,
        }

        formatted = translator.format_tool_result("write_to_file", result)

        assert formatted.startswith("[write_to_file] Result:")
        assert "Successfully wrote 1024 bytes" in formatted

    def test_format_insert_content_result(self, translator):
        """Test formatting insert_content result."""
        result = {
            "output": "Successfully inserted content at line 5 in file.py",
            "exit_code": 0,
            "position": 5,
        }

        formatted = translator.format_tool_result("insert_content", result)

        assert formatted.startswith("[insert_content] Result:")
        assert "Successfully inserted content at line 5" in formatted

    def test_format_edit_file_result(self, translator):
        """Test formatting edit_file result."""
        result = {
            "output": "Successfully edited config.json (256 bytes)",
            "exit_code": 0,
        }

        formatted = translator.format_tool_result("edit_file", result)

        assert formatted.startswith("[edit_file] Result:")
        assert "Successfully edited config.json" in formatted

    def test_format_editing_tool_error(self, translator):
        """Test formatting editing tool error result."""
        result = {
            "output": "Error: File not found: missing.txt",
            "exit_code": 1,
            "error": "File does not exist",
        }

        formatted = translator.format_tool_result("write_to_file", result)

        assert formatted.startswith("[write_to_file] Result:")
        assert "Error: File not found" in formatted
        assert "Error: File does not exist" in formatted
