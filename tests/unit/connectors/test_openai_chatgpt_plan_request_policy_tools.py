"""Unit tests for ChatGPT-plan SIWC tool and input preservation (task 4.2).

Covers function/custom tool schemas, tool_choice, call/output linkage,
text/image/file content parts, absence of Codex built-in injection and
client-family adapters, and the official SIWC preview tool matrix
(fetched 2026-09-30): preserve web search; reject unsupported hosted
capabilities with ResponsesProviderLimitationError.
"""

from __future__ import annotations

import ast
import re
from typing import Any

import pytest
from src.core.common.exceptions import ResponsesProviderLimitationError

from tests.unit.connectors.test_openai_chatgpt_plan_request_policy import (
    REQUEST_POLICY_PATH,
    SOURCE_KEY,
    _imported_names_from_source,
    _project,
    _responses_request,
)

PROVIDER = "openai-chatgpt-plan"

CODEX_BUILTIN_TOOL_NAMES = (
    "apply_patch",
    "shell",
    "update_plan",
    "exec_command",
    "view_image",
)

# Official SIWC preview limitations (2026-09-30) plus the design hosted-tool
# matrix. These constants are the test-side source of truth; they must not be
# derived from ChatGPTPlanRequestPolicy._UNSUPPORTED_HOSTED_TOOL_TYPES.
# https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations
OFFICIAL_SUPPORTED_WEB_SEARCH_TYPES = frozenset(
    {
        "web_search",
        "web_search_preview",
        "web_search_call",
    }
)

OFFICIAL_UNSUPPORTED_BY_CATEGORY: dict[str, tuple[str, ...]] = {
    "image_generation": ("image_generation", "image_generation_call"),
    "file_search": ("file_search", "file_search_call"),
    "code_interpreter": ("code_interpreter", "code_interpreter_call"),
    "native_computer_use": (
        "computer",
        "computer_call",
        "computer_use",
        "computer_use_preview",
    ),
    "hosted_mcp_connectors": ("mcp", "mcp_call", "mcp_list_tools", "connectors"),
    "tool_search": ("tool_search", "tool_search_call"),
    "programmatic_tool_calling": ("programmatic_tool_calling",),
}

OFFICIAL_UNSUPPORTED_HOSTED_TOOL_TYPES = frozenset(
    tool_type
    for types in OFFICIAL_UNSUPPORTED_BY_CATEGORY.values()
    for tool_type in types
)

FORBIDDEN_CLIENT_FAMILY_SUBSTRINGS = (
    "opencode",
    "kilo",
    "droid",
    "letta",
    "xml_parser",
    "xml-parser",
    "xmlparser",
    "openai_codex",
)

FORBIDDEN_CLIENT_FAMILY_IDENTS = frozenset(
    {
        "opencode",
        "kilo",
        "droid",
        "letta",
        "pi",
        "client_family",
        "xml_parser",
        "xmlparser",
    }
)

_FUNCTION_PARAMETERS: dict[str, Any] = {
    "type": "object",
    "properties": {
        "location": {"type": "string"},
        "unit": {"type": "string", "enum": ["celsius", "fahrenheit"]},
    },
    "required": ["location"],
    "additionalProperties": False,
}


def _function_tool() -> dict[str, Any]:
    return {
        "type": "function",
        "name": "get_weather",
        "description": "Return the weather for a location.",
        "parameters": _FUNCTION_PARAMETERS,
        "strict": True,
    }


def _custom_tool() -> dict[str, Any]:
    return {
        "type": "custom",
        "name": "submit_patch",
        "description": "Submit a unified diff.",
        "format": {
            "type": "grammar",
            "syntax": "lark",
            "definition": "start: patch",
        },
    }


class TestFunctionAndCustomToolSchemasPreserved:
    def test_function_tool_schema_and_tool_choice_preserved(self) -> None:
        tool_choice = {"type": "function", "name": "get_weather"}
        generic = {
            "model": "gpt-4o",
            "instructions": "Use get_weather when the user asks about weather.",
            "tools": [_function_tool()],
            "tool_choice": tool_choice,
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Weather in Paris?"}],
                }
            ],
        }
        projected = _project(generic, _responses_request(stream=False))
        tools = projected.payload["tools"]
        assert tools == [_function_tool()]
        assert tools[0]["name"] == "get_weather"
        assert tools[0]["parameters"] == _FUNCTION_PARAMETERS
        assert tools[0]["strict"] is True
        assert projected.payload["tool_choice"] == tool_choice
        assert projected.payload["instructions"] == generic["instructions"]

    def test_custom_tool_schema_preserved(self) -> None:
        tool_choice = {"type": "custom", "name": "submit_patch"}
        generic = {
            "model": "gpt-4o",
            "tools": [_custom_tool()],
            "tool_choice": tool_choice,
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Apply the change."}],
                }
            ],
        }
        projected = _project(generic)
        assert projected.payload["tools"] == [_custom_tool()]
        assert projected.payload["tools"][0]["type"] == "custom"
        assert projected.payload["tools"][0]["name"] == "submit_patch"
        assert projected.payload["tools"][0]["format"]["syntax"] == "lark"
        assert projected.payload["tool_choice"] == tool_choice


class TestToolCallOutputLinkagePreserved:
    def test_function_call_and_output_keep_call_id_arguments_output_and_order(
        self,
    ) -> None:
        generic = {
            "model": "gpt-4o",
            "tools": [_function_tool()],
            "tool_choice": "auto",
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Weather in Lyon?"}],
                },
                {
                    "type": "function_call",
                    "call_id": "call_weather_1",
                    "name": "get_weather",
                    "arguments": '{"location":"Lyon","unit":"celsius"}',
                },
                {
                    "type": "function_call_output",
                    "call_id": "call_weather_1",
                    "output": '{"temp":18,"summary":"cloudy"}',
                },
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Thanks, continue."}],
                },
            ],
        }
        projected = _project(generic)
        items = projected.payload["input"]
        assert [item["type"] for item in items] == [
            "message",
            "function_call",
            "function_call_output",
            "message",
        ]
        call_item = items[1]
        output_item = items[2]
        assert call_item["call_id"] == "call_weather_1"
        assert call_item["name"] == "get_weather"
        assert call_item["arguments"] == '{"location":"Lyon","unit":"celsius"}'
        assert output_item["call_id"] == "call_weather_1"
        assert output_item["output"] == '{"temp":18,"summary":"cloudy"}'
        assert projected.payload["tool_choice"] == "auto"

    def test_custom_tool_call_and_output_linkage_preserved(self) -> None:
        generic = {
            "model": "gpt-4o",
            "tools": [_custom_tool()],
            "input": [
                {
                    "type": "custom_tool_call",
                    "call_id": "call_patch_1",
                    "name": "submit_patch",
                    "arguments": "*** Begin Patch\n*** End Patch",
                },
                {
                    "type": "custom_tool_call_output",
                    "call_id": "call_patch_1",
                    "output": "ok",
                },
            ],
        }
        projected = _project(generic)
        items = projected.payload["input"]
        assert [item["type"] for item in items] == [
            "custom_tool_call",
            "custom_tool_call_output",
        ]
        assert items[0]["call_id"] == items[1]["call_id"] == "call_patch_1"
        assert items[0]["name"] == "submit_patch"
        assert items[0]["arguments"] == "*** Begin Patch\n*** End Patch"
        assert items[1]["output"] == "ok"


class TestTextImageFileContentPreserved:
    def test_text_image_and_file_content_parts_preserved_on_user_item(self) -> None:
        generic = {
            "model": "gpt-4o",
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [
                        {"type": "input_text", "text": "Describe the attachment."},
                        {
                            "type": "input_image",
                            "image_url": "https://example.com/diagram.png",
                        },
                        {
                            "type": "input_file",
                            "file_id": "file-diagram-001",
                        },
                    ],
                }
            ],
        }
        projected = _project(generic)
        content = projected.payload["input"][0]["content"]
        assert content[0] == {"type": "input_text", "text": "Describe the attachment."}
        assert content[1]["type"] == "input_image"
        assert content[1]["image_url"] == "https://example.com/diagram.png"
        assert content[2]["type"] == "input_file"
        assert content[2]["file_id"] == "file-diagram-001"
        assert projected.payload["input"][0]["role"] == "user"


class TestNoCodexBuiltinInjection:
    def test_projected_tools_do_not_gain_codex_builtins_absent_from_generic_payload(
        self,
    ) -> None:
        generic = {
            "model": "gpt-4o",
            "tools": [_function_tool()],
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Status?"}],
                }
            ],
        }
        projected = _project(generic)
        tools = projected.payload.get("tools")
        assert isinstance(tools, list)
        names = [tool.get("name") for tool in tools if isinstance(tool, dict)]
        for builtin in CODEX_BUILTIN_TOOL_NAMES:
            assert builtin not in names
        assert names == ["get_weather"]
        assert len(tools) == 1
        assert (
            "tools"
            not in _project(
                {
                    "model": "gpt-4o",
                    "input": [{"type": "message", "role": "user", "content": "Hi"}],
                }
            ).payload
        )


class TestNoClientFamilyAdapterBranches:
    def test_request_policy_source_has_no_client_family_or_codex_adapter_branches(
        self,
    ) -> None:
        source = REQUEST_POLICY_PATH.read_text(encoding="utf-8")
        imported = _imported_names_from_source(source)
        forbidden_imports = sorted(
            name
            for name in imported
            if "openai_codex" in name
            or name.endswith(("xml.etree.ElementTree", "xml.etree"))
        )
        assert forbidden_imports == []

        lowered = source.lower()
        for marker in FORBIDDEN_CLIENT_FAMILY_SUBSTRINGS:
            assert marker not in lowered

        tree = ast.parse(source)
        for node in ast.walk(tree):
            if isinstance(node, ast.Name):
                assert node.id.lower() not in FORBIDDEN_CLIENT_FAMILY_IDENTS
            elif isinstance(node, ast.Attribute):
                assert node.attr.lower() not in FORBIDDEN_CLIENT_FAMILY_IDENTS
            elif isinstance(node, ast.Constant) and isinstance(node.value, str):
                token = node.value.strip().lower()
                assert token not in FORBIDDEN_CLIENT_FAMILY_IDENTS
                for marker in FORBIDDEN_CLIENT_FAMILY_SUBSTRINGS:
                    assert marker not in token

        assert re.search(r"\bpi\b", source, flags=re.IGNORECASE) is None


class TestOfficialSiwcPreviewToolMatrixPinnedIndependently:
    """Fail if production clones a wrong extra or drops a required category type."""

    def test_official_categories_match_preview_docs_and_design_matrix(self) -> None:
        assert set(OFFICIAL_UNSUPPORTED_BY_CATEGORY) == {
            "image_generation",
            "file_search",
            "code_interpreter",
            "native_computer_use",
            "hosted_mcp_connectors",
            "tool_search",
            "programmatic_tool_calling",
        }
        assert (
            frozenset({"web_search", "web_search_preview", "web_search_call"})
            == OFFICIAL_SUPPORTED_WEB_SEARCH_TYPES
        )
        assert (
            "computer_use_preview"
            in OFFICIAL_UNSUPPORTED_BY_CATEGORY["native_computer_use"]
        )
        assert (
            "programmatic_tool_calling"
            in OFFICIAL_UNSUPPORTED_BY_CATEGORY["programmatic_tool_calling"]
        )
        overlap = (
            OFFICIAL_SUPPORTED_WEB_SEARCH_TYPES & OFFICIAL_UNSUPPORTED_HOSTED_TOOL_TYPES
        )
        assert overlap == frozenset()

    def test_production_unsupported_set_matches_official_categories_not_hosted_looking(
        self,
    ) -> None:
        from src.connectors.openai_chatgpt_plan.request_policy import (
            _UNSUPPORTED_HOSTED_TOOL_TYPES,
        )

        extra = _UNSUPPORTED_HOSTED_TOOL_TYPES - OFFICIAL_UNSUPPORTED_HOSTED_TOOL_TYPES
        missing = (
            OFFICIAL_UNSUPPORTED_HOSTED_TOOL_TYPES - _UNSUPPORTED_HOSTED_TOOL_TYPES
        )
        assert extra == frozenset(), (
            "production rejects types outside the official/design matrix "
            f"(wrong extras: {sorted(extra)})"
        )
        assert missing == frozenset(), (
            "production omits official/design unsupported types "
            f"(missing: {sorted(missing)})"
        )
        leaked_web_search = (
            _UNSUPPORTED_HOSTED_TOOL_TYPES & OFFICIAL_SUPPORTED_WEB_SEARCH_TYPES
        )
        assert leaked_web_search == frozenset()


class TestWebSearchToolsPreserved:
    @pytest.mark.parametrize(
        "web_search_type", sorted(OFFICIAL_SUPPORTED_WEB_SEARCH_TYPES)
    )
    def test_project_preserves_web_search_tool_in_tools(
        self, web_search_type: str
    ) -> None:
        tool = {"type": web_search_type}
        generic = {
            "model": "gpt-4o",
            "tools": [_function_tool(), tool],
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Search the web."}],
                }
            ],
        }
        projected = _project(generic)
        assert projected.payload["tools"] == [_function_tool(), tool]
        assert projected.payload["tools"][1]["type"] == web_search_type

    @pytest.mark.parametrize(
        "web_search_type", sorted(OFFICIAL_SUPPORTED_WEB_SEARCH_TYPES)
    )
    def test_project_preserves_web_search_item_in_input(
        self, web_search_type: str
    ) -> None:
        item = {"type": web_search_type, "id": "ws_1"}
        generic = {
            "model": "gpt-4o",
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Search the web."}],
                },
                item,
            ],
        }
        projected = _project(generic)
        assert projected.payload["input"][1] == item
        assert projected.payload["input"][1]["type"] == web_search_type

    def test_project_preserves_web_search_tool_choice(self) -> None:
        tool_choice = {"type": "web_search"}
        generic = {
            "model": "gpt-4o",
            "tools": [{"type": "web_search"}],
            "tool_choice": tool_choice,
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Search."}],
                }
            ],
        }
        projected = _project(generic)
        assert projected.payload["tools"] == [{"type": "web_search"}]
        assert projected.payload["tool_choice"] == tool_choice


class TestExplicitHostedToolsRejected:
    def test_explicit_file_search_tool_raises_provider_limitation(self) -> None:
        generic = {
            "model": "gpt-4o",
            "tools": [{"type": "file_search", "vector_store_ids": ["vs_demo"]}],
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Search the files."}],
                }
            ],
        }
        with pytest.raises(ResponsesProviderLimitationError) as exc_info:
            _project(generic)
        error = exc_info.value
        assert error.feature == "file_search"
        assert error.provider == PROVIDER
        assert "file_search" in str(error)
        assert error.details["feature"] == "file_search"
        assert error.details["provider"] == PROVIDER

    @pytest.mark.parametrize(
        "hosted_type", sorted(OFFICIAL_UNSUPPORTED_HOSTED_TOOL_TYPES)
    )
    def test_explicit_hosted_tool_type_raises_naming_capability(
        self, hosted_type: str
    ) -> None:
        generic = {
            "model": "gpt-4o",
            "tools": [{"type": hosted_type}],
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Use hosted tool."}],
                }
            ],
        }
        with pytest.raises(ResponsesProviderLimitationError) as exc_info:
            _project(generic)
        assert exc_info.value.feature == hosted_type
        assert exc_info.value.provider == PROVIDER
        assert exc_info.value.details["feature"] == hosted_type
        assert exc_info.value.details["provider"] == PROVIDER

    def test_programmatic_tool_calling_in_tools_raises_naming_capability(self) -> None:
        generic = {
            "model": "gpt-4o",
            "tools": [{"type": "programmatic_tool_calling"}],
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Call tools."}],
                }
            ],
        }
        with pytest.raises(ResponsesProviderLimitationError) as exc_info:
            _project(generic)
        assert exc_info.value.feature == "programmatic_tool_calling"
        assert exc_info.value.provider == PROVIDER

    def test_computer_use_preview_in_tools_raises_naming_capability(self) -> None:
        generic = {
            "model": "gpt-4o",
            "tools": [{"type": "computer_use_preview"}],
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Use the computer."}],
                }
            ],
        }
        with pytest.raises(ResponsesProviderLimitationError) as exc_info:
            _project(generic)
        assert exc_info.value.feature == "computer_use_preview"
        assert exc_info.value.provider == PROVIDER

    @pytest.mark.parametrize(
        "call_type",
        (
            "image_generation_call",
            "file_search_call",
            "code_interpreter_call",
            "computer_call",
            "mcp_call",
            "tool_search_call",
        ),
    )
    def test_hosted_tool_call_item_in_input_raises_provider_limitation(
        self, call_type: str
    ) -> None:
        generic = {
            "model": "gpt-4o",
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Search docs."}],
                },
                {"type": call_type, "id": "hosted_call_1"},
            ],
        }
        with pytest.raises(ResponsesProviderLimitationError) as exc_info:
            _project(generic)
        assert exc_info.value.feature == call_type
        assert exc_info.value.provider == PROVIDER


class TestInstructionProjectionNotRegressedWithTools:
    def test_tools_payload_still_rewrites_residual_system_and_keeps_instructions(
        self,
    ) -> None:
        generic = {
            "model": "gpt-4o",
            "instructions": "Harness-owned native instructions.",
            "tools": [_function_tool()],
            "tool_choice": "required",
            "input": [
                {
                    "type": "message",
                    "role": "system",
                    "content": "Residual system.",
                    SOURCE_KEY: "canonical_system_prompt",
                },
                {
                    "type": "message",
                    "role": "system",
                    "content": "Independent residual system.",
                },
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Go."}],
                },
            ],
        }
        projected = _project(
            generic,
            _responses_request(system_prompt="Harness-owned native instructions."),
        )
        assert projected.payload["instructions"] == "Harness-owned native instructions."
        items = projected.payload["input"]
        roles = [item.get("role") for item in items if isinstance(item, dict)]
        assert roles == ["developer", "user"]
        assert "system" not in roles
        assert any(
            item.get("role") == "developer"
            and item.get("content") == "Independent residual system."
            for item in items
        )
        assert projected.payload["tools"] == [_function_tool()]
        assert projected.payload["tool_choice"] == "required"
        for item in items:
            assert SOURCE_KEY not in item


class TestChatShapedToolsFlattenedToResponses:
    """Compatibility: Chat Completions nested function tools -> flat SIWC tools."""

    def _chat_shaped_bash_tool(self) -> dict[str, Any]:
        return {
            "type": "function",
            "function": {
                "name": "bash",
                "description": "Run a shell command.",
                "parameters": {
                    "type": "object",
                    "properties": {"command": {"type": "string"}},
                    "required": ["command"],
                },
                "strict": None,
            },
        }

    def test_chat_shaped_tools_gain_top_level_name_and_drop_nested_function(
        self,
    ) -> None:
        generic = {
            "model": "gpt-4o",
            "tools": [self._chat_shaped_bash_tool()],
            "tool_choice": {
                "type": "function",
                "function": {"name": "bash"},
            },
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "run ls"}],
                }
            ],
        }
        projected = _project(generic)
        tools = projected.payload["tools"]
        assert len(tools) == 1
        tool = tools[0]
        assert tool["type"] == "function"
        assert tool["name"] == "bash"
        assert tool["description"] == "Run a shell command."
        assert tool["parameters"]["required"] == ["command"]
        assert "function" not in tool
        assert "name" in tool
        assert projected.payload["tool_choice"] == {
            "type": "function",
            "name": "bash",
        }

    def test_missing_type_with_nested_function_blob_flattened(self) -> None:
        generic = {
            "model": "gpt-4o",
            "tools": [
                {
                    "function": {
                        "name": "read_file",
                        "description": "Read a file.",
                        "parameters": {
                            "type": "object",
                            "properties": {"path": {"type": "string"}},
                        },
                    }
                }
            ],
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "read"}],
                }
            ],
        }
        projected = _project(generic)
        tool = projected.payload["tools"][0]
        assert tool["type"] == "function"
        assert tool["name"] == "read_file"
        assert tool["description"] == "Read a file."
        assert "function" not in tool

    def test_nested_additional_tools_chat_shaped_entries_flattened(self) -> None:
        generic = {
            "model": "gpt-4o",
            "tools": [
                {
                    "type": "function",
                    "name": "orchestrator",
                    "parameters": {"type": "object", "properties": {}},
                    "additional_tools": [
                        {
                            "type": "function",
                            "function": {
                                "name": "nested_bash",
                                "description": "Nested shell.",
                                "parameters": {
                                    "type": "object",
                                    "properties": {"cmd": {"type": "string"}},
                                },
                            },
                        }
                    ],
                }
            ],
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "go"}],
                }
            ],
        }
        projected = _project(generic)
        tool = projected.payload["tools"][0]
        assert tool["name"] == "orchestrator"
        nested = tool["additional_tools"][0]
        assert nested["type"] == "function"
        assert nested["name"] == "nested_bash"
        assert nested["description"] == "Nested shell."
        assert "function" not in nested

    def test_already_flat_responses_tools_unchanged(self) -> None:
        flat = _function_tool()
        generic = {
            "model": "gpt-4o",
            "tools": [flat],
            "tool_choice": {"type": "function", "name": "get_weather"},
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "weather"}],
                }
            ],
        }
        projected = _project(generic)
        assert projected.payload["tools"] == [flat]
        assert projected.payload["tool_choice"] == {
            "type": "function",
            "name": "get_weather",
        }

    def test_chat_shaped_custom_tool_flattened(self) -> None:
        generic = {
            "model": "gpt-4o",
            "tools": [
                {
                    "type": "custom",
                    "function": {
                        "name": "submit_patch",
                        "description": "Submit a unified diff.",
                        "format": {
                            "type": "grammar",
                            "syntax": "lark",
                            "definition": "start: patch",
                        },
                    },
                }
            ],
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "patch"}],
                }
            ],
        }
        projected = _project(generic)
        tool = projected.payload["tools"][0]
        assert tool["type"] == "custom"
        assert tool["name"] == "submit_patch"
        assert tool["format"]["syntax"] == "lark"
        assert "function" not in tool

    def test_hosted_tools_still_rejected_alongside_chat_shaped_tools(self) -> None:
        generic = {
            "model": "gpt-4o",
            "tools": [
                self._chat_shaped_bash_tool(),
                {"type": "file_search", "vector_store_ids": ["vs_demo"]},
            ],
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "search"}],
                }
            ],
        }
        with pytest.raises(ResponsesProviderLimitationError) as exc_info:
            _project(generic)
        assert exc_info.value.feature == "file_search"
        assert exc_info.value.provider == PROVIDER


class TestChatShapedToolHistoryProjectedToResponses:
    """Compatibility: Chat Completions tool history -> Responses input items."""

    def test_assistant_nested_tool_calls_become_function_call_items(self) -> None:
        generic = {
            "model": "gpt-4o",
            "tools": [_function_tool()],
            "input": [
                {
                    "role": "user",
                    "content": "Weather in Lyon?",
                },
                {
                    "role": "assistant",
                    "content": None,
                    "tool_calls": [
                        {
                            "id": "call_weather_1",
                            "type": "function",
                            "function": {
                                "name": "get_weather",
                                "arguments": '{"location":"Lyon"}',
                            },
                        }
                    ],
                },
            ],
        }
        projected = _project(generic)
        items = projected.payload["input"]
        assert [item.get("type") or item.get("role") for item in items] == [
            "user",
            "function_call",
        ]
        call_item = items[1]
        assert call_item == {
            "type": "function_call",
            "call_id": "call_weather_1",
            "name": "get_weather",
            "arguments": '{"location":"Lyon"}',
        }
        for item in items:
            assert "tool_calls" not in item
            assert "tool_call_id" not in item

    def test_tool_role_message_becomes_function_call_output(self) -> None:
        generic = {
            "model": "gpt-4o",
            "input": [
                {
                    "role": "tool",
                    "tool_call_id": "call_weather_1",
                    "content": '{"temp":18,"summary":"cloudy"}',
                }
            ],
        }
        projected = _project(generic)
        items = projected.payload["input"]
        assert items == [
            {
                "type": "function_call_output",
                "call_id": "call_weather_1",
                "output": '{"temp":18,"summary":"cloudy"}',
            }
        ]
        assert "tool_call_id" not in items[0]
        assert items[0].get("role") != "tool"

    def test_no_tool_calls_key_remains_on_any_input_item(self) -> None:
        generic = {
            "model": "gpt-4o",
            "messages": [
                {"role": "user", "content": "run tools"},
                {
                    "role": "assistant",
                    "content": "Calling tools.",
                    "tool_calls": [
                        {
                            "id": "call_a",
                            "type": "function",
                            "function": {
                                "name": "get_weather",
                                "arguments": '{"location":"Paris"}',
                            },
                        },
                        {
                            "id": "call_b",
                            "type": "function",
                            "function": {
                                "name": "get_weather",
                                "arguments": '{"location":"Lyon"}',
                            },
                        },
                    ],
                },
                {
                    "role": "tool",
                    "tool_call_id": "call_a",
                    "content": "sunny",
                },
                {
                    "role": "tool",
                    "tool_call_id": "call_b",
                    "content": "cloudy",
                },
                {"role": "user", "content": "thanks"},
            ],
        }
        projected = _project(generic)
        items = projected.payload["input"]
        assert "messages" not in projected.payload
        for item in items:
            assert isinstance(item, dict)
            assert "tool_calls" not in item
            assert "tool_call_id" not in item
            assert item.get("role") != "tool"

    def test_multi_tool_turn_preserves_call_id_linkage_and_order(self) -> None:
        generic = {
            "model": "gpt-4o",
            "tools": [_function_tool()],
            "input": [
                {"role": "user", "content": "Weather in Paris and Lyon?"},
                {
                    "role": "assistant",
                    "content": None,
                    "tool_calls": [
                        {
                            "id": "call_paris",
                            "type": "function",
                            "function": {
                                "name": "get_weather",
                                "arguments": '{"location":"Paris"}',
                            },
                        },
                        {
                            "id": "call_lyon",
                            "type": "function",
                            "function": {
                                "name": "get_weather",
                                "arguments": '{"location":"Lyon"}',
                            },
                        },
                    ],
                },
                {
                    "role": "tool",
                    "tool_call_id": "call_paris",
                    "content": '{"temp":20}',
                },
                {
                    "role": "tool",
                    "tool_call_id": "call_lyon",
                    "content": '{"temp":18}',
                },
                {"role": "user", "content": "Summarize."},
            ],
        }
        projected = _project(generic)
        items = projected.payload["input"]
        assert [item.get("type") or item.get("role") for item in items] == [
            "user",
            "function_call",
            "function_call",
            "function_call_output",
            "function_call_output",
            "user",
        ]
        assert items[1]["call_id"] == "call_paris"
        assert items[1]["name"] == "get_weather"
        assert items[1]["arguments"] == '{"location":"Paris"}'
        assert items[2]["call_id"] == "call_lyon"
        assert items[2]["arguments"] == '{"location":"Lyon"}'
        assert items[3]["call_id"] == "call_paris"
        assert items[3]["output"] == '{"temp":20}'
        assert items[4]["call_id"] == "call_lyon"
        assert items[4]["output"] == '{"temp":18}'
        for item in items:
            assert "tool_calls" not in item
            assert "tool_call_id" not in item

    def test_already_responses_shaped_function_call_history_unchanged(self) -> None:
        generic = {
            "model": "gpt-4o",
            "input": [
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "Weather?"}],
                },
                {
                    "type": "function_call",
                    "call_id": "call_weather_1",
                    "name": "get_weather",
                    "arguments": '{"location":"Lyon"}',
                },
                {
                    "type": "function_call_output",
                    "call_id": "call_weather_1",
                    "output": '{"temp":18}',
                },
            ],
        }
        projected = _project(generic)
        items = projected.payload["input"]
        assert [item["type"] for item in items] == [
            "message",
            "function_call",
            "function_call_output",
        ]
        assert items[1]["call_id"] == items[2]["call_id"] == "call_weather_1"
        assert items[1]["name"] == "get_weather"
        assert items[2]["output"] == '{"temp":18}'
