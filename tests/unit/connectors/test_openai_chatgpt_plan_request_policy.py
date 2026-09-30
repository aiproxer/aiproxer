"""Unit tests for ChatGPT-plan SIWC instruction projection (task 4.1).

Covers native ``instructions``, canonical ``system_prompt``, residual
``role=system`` rewrite to ``developer``, provenance (not text-equality),
and negative Codex/client-family prompt fingerprints.
"""

from __future__ import annotations

import ast
import importlib
import json
import sys
from pathlib import Path
from typing import Any

from src.connectors.contracts import (
    ConnectorChatCompletionsRequest,
    ConnectorResponsesRequest,
)
from src.core.domain.chat import CanonicalChatRequest, ChatMessage

PACKAGE_NAME = "src.connectors.openai_chatgpt_plan"
PACKAGE_DIR = (
    Path(__file__).resolve().parents[3] / "src" / "connectors" / "openai_chatgpt_plan"
)
REQUEST_POLICY_PATH = PACKAGE_DIR / "request_policy.py"
INIT_PATH = PACKAGE_DIR / "__init__.py"

SOURCE_KEY = "siwc_instruction_source"
SOURCE_CANONICAL_SYSTEM_PROMPT = "canonical_system_prompt"

FORBIDDEN_MODULE_PREFIXES = (
    "src.connectors.openai_codex",
    "src.connectors.openai_codex_v2",
    "src.connectors._openai_codex_connector",
    "src.connectors._openai_codex_v2_connector",
    "src.connectors.openai_codex_app_server",
    "src.resources.codex",
)

FORBIDDEN_PAYLOAD_SUBSTRINGS = (
    "<user_instructions>",
    "</user_instructions>",
    "<environment_context>",
    "codex_cli_rs",
    "You are ChatGPT, a large language model trained by OpenAI",
    "You are Codex, based on GPT-5",
    "OpenCode compatibility mode",
    "Cline-family XML compatibility mode",
    "Factory Droid compatibility mode",
    "Pi compatibility mode",
    "Letta Code compatibility mode",
)

FORBIDDEN_SOURCE_SUBSTRINGS = (
    "openai_codex",
    "src.resources.codex",
    "gpt_5_codex_prompt.md",
    "<user_instructions>",
    "codex_cli_rs",
)


def _imported_names_from_source(source: str) -> set[str]:
    tree = ast.parse(source)
    names: set[str] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            names.update(alias.name for alias in node.names)
        elif isinstance(node, ast.ImportFrom) and node.module:
            names.add(node.module)
    return names


def _unload_chatgpt_plan_modules() -> None:
    for key in list(sys.modules):
        if key == PACKAGE_NAME or key.startswith(f"{PACKAGE_NAME}."):
            sys.modules.pop(key, None)


def _canonical(
    *,
    stream: bool | None = False,
    system_prompt: str | None = None,
    messages: list[ChatMessage] | None = None,
) -> CanonicalChatRequest:
    return CanonicalChatRequest(
        model="gpt-4o",
        messages=messages or [ChatMessage(role="user", content="What is the status?")],
        system_prompt=system_prompt,
        stream=stream,
    )


def _responses_request(
    *,
    stream: bool | None = False,
    system_prompt: str | None = None,
    messages: list[ChatMessage] | None = None,
) -> ConnectorResponsesRequest:
    canonical = _canonical(
        stream=stream, system_prompt=system_prompt, messages=messages
    )
    return ConnectorResponsesRequest(
        request=canonical,
        processed_messages=list(canonical.messages),
        effective_model="gpt-4o",
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=None,
    )


def _chat_completions_request(
    *,
    stream: bool | None = False,
    system_prompt: str | None = None,
    messages: list[ChatMessage] | None = None,
) -> ConnectorChatCompletionsRequest:
    canonical = _canonical(
        stream=stream, system_prompt=system_prompt, messages=messages
    )
    return ConnectorChatCompletionsRequest(
        request=canonical,
        processed_messages=list(canonical.messages),
        effective_model="gpt-4o",
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=None,
    )


def _project(
    generic_payload: dict[str, Any],
    request: ConnectorResponsesRequest | ConnectorChatCompletionsRequest | None = None,
) -> Any:
    from src.connectors.openai_chatgpt_plan.request_policy import (
        ChatGPTPlanRequestPolicy,
    )

    policy = ChatGPTPlanRequestPolicy()
    return policy.project(
        request=request or _responses_request(),
        generic_payload=generic_payload,
    )


def _roles(items: Any) -> list[str | None]:
    if not isinstance(items, list):
        return []
    roles: list[str | None] = []
    for item in items:
        if isinstance(item, dict):
            role = item.get("role")
            roles.append(str(role) if role is not None else None)
        else:
            roles.append(None)
    return roles


def _payload_text(payload: dict[str, Any]) -> str:
    return json.dumps(payload, sort_keys=True, default=str)


def _assert_no_system_roles(payload: dict[str, Any]) -> None:
    assert "messages" not in payload
    for key in ("input", "messages"):
        items = payload.get(key)
        if not isinstance(items, list):
            continue
        for item in items:
            if isinstance(item, dict):
                assert item.get("role") != "system"


def _assert_responses_input_shape(payload: dict[str, Any]) -> None:
    assert "messages" not in payload
    assert isinstance(payload.get("input"), list)


def _assert_no_forbidden_fingerprints(payload: dict[str, Any]) -> None:
    blob = _payload_text(payload)
    for marker in FORBIDDEN_PAYLOAD_SUBSTRINGS:
        assert marker not in blob


class TestChatGPTPlanRequestPolicySourceBoundary:
    def test_request_policy_module_exists_and_avoids_codex_imports(self) -> None:
        assert REQUEST_POLICY_PATH.is_file()
        source = REQUEST_POLICY_PATH.read_text(encoding="utf-8")
        assert "sk-" not in source
        imported = _imported_names_from_source(source)
        forbidden = sorted(
            name
            for name in imported
            if any(
                name == prefix or name.startswith(f"{prefix}.")
                for prefix in FORBIDDEN_MODULE_PREFIXES
            )
            or name
            in {
                "src.connectors.openai_chatgpt_plan.catalog",
                "src.connectors.openai_chatgpt_plan.oauth",
                "src.connectors.openai_chatgpt_plan.tokens",
            }
        )
        assert forbidden == []
        for marker in FORBIDDEN_SOURCE_SUBSTRINGS:
            assert marker not in source

    def test_package_init_does_not_import_request_policy(self) -> None:
        init_source = INIT_PATH.read_text(encoding="utf-8")
        imported = _imported_names_from_source(init_source)
        assert "src.connectors.openai_chatgpt_plan.request_policy" not in imported
        assert ".request_policy" not in init_source
        assert "request_policy" not in imported

    def test_package_import_does_not_load_request_policy_or_catalog(self) -> None:
        _unload_chatgpt_plan_modules()
        importlib.import_module(PACKAGE_NAME)
        assert f"{PACKAGE_NAME}.request_policy" not in sys.modules
        assert f"{PACKAGE_NAME}.catalog" not in sys.modules


class TestNativeInstructionsPreserved:
    def test_native_instructions_preserved_verbatim_without_codex_prompt(self) -> None:
        native = "Harness-owned native instructions. Prioritize the user's repo."
        generic = {
            "model": "gpt-4o",
            "instructions": native,
            "input": [
                {"type": "message", "role": "user", "content": "Ship it."},
            ],
        }
        projected = _project(
            generic,
            _responses_request(system_prompt=native, stream=True),
        )
        assert projected.payload["instructions"] is native
        assert projected.payload["instructions"] == native
        assert projected.downstream_stream_requested is True
        assert projected.explicit_profile_id is None
        _assert_no_system_roles(projected.payload)
        _assert_no_forbidden_fingerprints(projected.payload)


class TestCanonicalSystemPromptProjection:
    def test_canonical_system_prompt_becomes_instructions_when_empty(
        self,
    ) -> None:
        prompt = "You are the harness agent. Follow repository conventions."
        generic = {
            "model": "gpt-4o",
            "messages": [
                {"role": "user", "content": "List files."},
            ],
        }
        projected = _project(
            generic,
            _responses_request(system_prompt=prompt, stream=False),
        )
        assert projected.payload["instructions"] == prompt
        assert projected.downstream_stream_requested is False
        _assert_responses_input_shape(projected.payload)
        assert _roles(projected.payload["input"]) == ["user"]
        _assert_no_system_roles(projected.payload)
        _assert_no_forbidden_fingerprints(projected.payload)

    def test_empty_string_instructions_filled_from_system_prompt(self) -> None:
        prompt = "Canonical high-priority system prompt."
        generic = {
            "model": "gpt-4o",
            "instructions": "",
            "messages": [{"role": "user", "content": "Hi."}],
        }
        projected = _project(generic, _responses_request(system_prompt=prompt))
        assert projected.payload["instructions"] == prompt
        _assert_responses_input_shape(projected.payload)
        assert projected.payload["input"][0]["content"] == "Hi."


class TestResidualSystemRewrite:
    def test_residual_system_input_items_rewritten_to_developer_order_preserved(
        self,
    ) -> None:
        generic = {
            "model": "gpt-4o",
            "input": [
                {
                    "type": "message",
                    "role": "system",
                    "content": [
                        {"type": "input_text", "text": "First residual system."}
                    ],
                },
                {
                    "type": "message",
                    "role": "user",
                    "content": [{"type": "input_text", "text": "User turn."}],
                },
                {
                    "type": "message",
                    "role": "assistant",
                    "content": [{"type": "input_text", "text": "Assistant turn."}],
                },
                {
                    "type": "message",
                    "role": "system",
                    "content": [
                        {"type": "input_text", "text": "Second residual system."}
                    ],
                },
            ],
        }
        projected = _project(generic)
        items = projected.payload["input"]
        assert _roles(items) == ["developer", "user", "assistant", "developer"]
        assert items[0]["content"][0]["text"] == "First residual system."
        assert items[3]["content"][0]["text"] == "Second residual system."
        assert items[0]["type"] == "message"
        _assert_no_system_roles(projected.payload)

    def test_residual_system_messages_rewritten_on_generic_serializer_path(
        self,
    ) -> None:
        generic = {
            "model": "gpt-4o",
            "messages": [
                {"role": "system", "content": "Translated system item."},
                {"role": "user", "content": "Hello from chat completions."},
                {"role": "assistant", "content": "Prior reply."},
            ],
        }
        request = _chat_completions_request(
            messages=[
                ChatMessage(role="system", content="Translated system item."),
                ChatMessage(role="user", content="Hello from chat completions."),
                ChatMessage(role="assistant", content="Prior reply."),
            ]
        )
        projected = _project(generic, request)
        _assert_responses_input_shape(projected.payload)
        items = projected.payload["input"]
        assert _roles(items) == ["developer", "user", "assistant"]
        assert items[0]["content"] == "Translated system item."
        _assert_no_system_roles(projected.payload)

    def test_existing_developer_items_keep_role_and_relative_order(self) -> None:
        generic = {
            "model": "gpt-4o",
            "instructions": "Top-level native instructions.",
            "input": [
                {
                    "type": "message",
                    "role": "developer",
                    "content": "Existing developer A.",
                },
                {"type": "message", "role": "user", "content": "Question."},
                {
                    "type": "message",
                    "role": "assistant",
                    "content": "Answer.",
                },
                {
                    "type": "message",
                    "role": "developer",
                    "content": "Existing developer B.",
                },
            ],
        }
        projected = _project(
            generic, _responses_request(system_prompt="Top-level native instructions.")
        )
        items = projected.payload["input"]
        assert _roles(items) == ["developer", "user", "assistant", "developer"]
        assert items[0]["content"] == "Existing developer A."
        assert items[3]["content"] == "Existing developer B."
        assert projected.payload["instructions"] == "Top-level native instructions."


class TestInstructionProvenance:
    def test_tagged_carrier_not_duplicated_untagged_same_text_becomes_developer(
        self,
    ) -> None:
        shared = "Identical high-priority instruction text."
        generic = {
            "model": "gpt-4o",
            "instructions": shared,
            "input": [
                {
                    "type": "message",
                    "role": "system",
                    "content": shared,
                    SOURCE_KEY: SOURCE_CANONICAL_SYSTEM_PROMPT,
                },
                {
                    "type": "message",
                    "role": "system",
                    "content": shared,
                },
                {
                    "type": "message",
                    "role": "user",
                    "content": "Continue.",
                },
            ],
        }
        projected = _project(
            generic,
            _responses_request(system_prompt=shared),
        )
        assert projected.payload["instructions"] == shared
        items = projected.payload["input"]
        assert _roles(items) == ["developer", "user"]
        assert items[0]["content"] == shared
        assert SOURCE_KEY not in items[0]
        developer_count = sum(
            1
            for item in items
            if isinstance(item, dict) and item.get("role") == "developer"
        )
        assert developer_count == 1
        _assert_no_system_roles(projected.payload)

    def test_messages_tagged_carrier_skipped_untagged_independent_kept(self) -> None:
        shared = "Same text, different provenance."
        generic = {
            "model": "gpt-4o",
            "instructions": shared,
            "messages": [
                {
                    "role": "system",
                    "content": shared,
                    SOURCE_KEY: SOURCE_CANONICAL_SYSTEM_PROMPT,
                },
                {"role": "system", "content": shared},
                {"role": "developer", "content": "Pre-existing developer."},
                {"role": "user", "content": "Ask."},
            ],
        }
        projected = _project(generic, _responses_request(system_prompt=shared))
        _assert_responses_input_shape(projected.payload)
        items = projected.payload["input"]
        assert _roles(items) == ["developer", "developer", "user"]
        assert items[0]["content"] == shared
        assert items[1]["content"] == "Pre-existing developer."
        _assert_no_system_roles(projected.payload)


class TestNegativeCodexAndClientFamilyFingerprints:
    def test_projected_payload_contains_no_codex_or_client_family_wrappers(
        self,
    ) -> None:
        generic = {
            "model": "gpt-4o",
            "instructions": "Native harness instructions only.",
            "input": [
                {"type": "message", "role": "system", "content": "Residual system."},
                {"type": "message", "role": "developer", "content": "Keep me."},
                {"type": "message", "role": "user", "content": "Go."},
            ],
            "messages": [
                {"role": "system", "content": "Generic serializer system."},
                {"role": "user", "content": "Also go."},
            ],
        }
        projected = _project(
            generic,
            _responses_request(
                system_prompt="Canonical prompt distinct from Codex.",
                stream=True,
            ),
        )
        _assert_responses_input_shape(projected.payload)
        # Both former input and messages item lists are merged into input.
        assert _roles(projected.payload["input"]) == [
            "developer",
            "developer",
            "user",
            "developer",
            "user",
        ]
        _assert_no_forbidden_fingerprints(projected.payload)
        blob = _payload_text(projected.payload)
        assert "src/resources/codex" not in blob
        assert "gpt_5_codex_prompt.md" not in blob
        _assert_no_system_roles(projected.payload)

    def test_policy_does_not_mutate_caller_generic_payload(self) -> None:
        generic: dict[str, Any] = {
            "model": "gpt-4o",
            "instructions": "Caller owned.",
            "input": [
                {"role": "system", "content": "Residual."},
                {"role": "user", "content": "Hi."},
            ],
        }
        original_input = generic["input"]
        projected = _project(generic)
        assert generic["input"] is original_input
        caller_items = generic["input"]
        assert isinstance(caller_items, list)
        assert caller_items[0]["role"] == "system"
        projected_items = projected.payload["input"]
        assert isinstance(projected_items, list)
        assert projected_items[0]["role"] == "developer"
        assert projected.payload is not generic

class TestMessagesNormalizedToInput:
    def test_translator_shaped_messages_become_input_and_messages_removed(self) -> None:
        """Chat Completions translator keys must map onto Responses input."""
        generic = {
            "model": "gpt-4o",
            "stream": True,
            "messages": [
                {"role": "user", "content": "What is the status?"},
            ],
        }
        projected = _project(generic, _responses_request(stream=True))
        assert "messages" not in projected.payload
        assert isinstance(projected.payload.get("input"), list)
        assert _roles(projected.payload["input"]) == ["user"]
        assert projected.payload["input"][0]["content"] == "What is the status?"
        assert projected.payload["model"] == "gpt-4o"
        assert projected.payload["stream"] is True
        assert projected.payload["store"] is False

    def test_messages_merged_after_existing_input(self) -> None:
        generic = {
            "model": "gpt-4o",
            "input": [
                {"type": "message", "role": "user", "content": "from-input"},
            ],
            "messages": [
                {"role": "user", "content": "from-messages"},
            ],
        }
        projected = _project(generic)
        assert "messages" not in projected.payload
        items = projected.payload["input"]
        assert len(items) == 2
        assert items[0]["content"] == "from-input"
        assert items[1]["content"] == "from-messages"

    def test_non_list_messages_popped_without_clobbering_input(self) -> None:
        generic = {
            "model": "gpt-4o",
            "input": [{"role": "user", "content": "keep-me"}],
            "messages": "not-a-list",
        }
        projected = _project(generic)
        assert "messages" not in projected.payload
        assert projected.payload["input"][0]["content"] == "keep-me"

    def test_empty_input_replaced_by_messages(self) -> None:
        generic = {
            "model": "gpt-4o",
            "input": [],
            "messages": [{"role": "user", "content": "only-messages"}],
        }
        projected = _project(generic)
        assert "messages" not in projected.payload
        assert _roles(projected.payload["input"]) == ["user"]
        assert projected.payload["input"][0]["content"] == "only-messages"

