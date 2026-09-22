import json
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from typing import Any, cast
from unittest.mock import AsyncMock

import httpx
import pytest
import pytest_asyncio
import src.connectors  # noqa: F401 — register backends for default BackendSettings
from fastapi import HTTPException
from pytest_mock import MockerFixture
from src.connectors._openai_codex_capabilities import CodexClientCapabilities
from src.connectors.contracts import ConnectorChatCompletionsRequest
from src.connectors.openai_codex import OpenAICodexConnector
from src.connectors.openai_codex.catalog.interfaces import ICodexModelCatalog
from src.connectors.openai_codex.catalog.parser import CodexCatalogParser
from src.connectors.openai_codex.contracts import (
    CodexPayload,
)
from src.core.config.app_config import AppConfig
from src.core.domain.chat import (
    CanonicalChatRequest,
    ChatMessage,
    ChatRequest,
    FunctionCall,
    ToolCall,
)
from src.core.domain.responses import (
    ResponseEnvelope,
    StreamingResponseEnvelope,
    StreamingResponseHandle,
)
from src.core.domain.usage_summary import UsageSummary
from src.core.interfaces.response_processor_interface import ProcessedResponse
from src.core.services.tool_text_renderer import (
    OverrideRenderer,
    render_tool_call,
    reset_renderer_registry,
)

from tests.unit.connectors.openai_codex.catalog.conftest import make_raw_catalog


def _connector_chat_request(
    chat_request: ChatRequest, *, effective_model: str
) -> ConnectorChatCompletionsRequest:
    domain = CanonicalChatRequest.model_validate(chat_request.model_dump())
    return ConnectorChatCompletionsRequest(
        request=domain,
        processed_messages=list(chat_request.messages),
        effective_model=effective_model,
        identity=None,
        cancellation_token=None,
        cancellation_coordinator=None,
        context=None,
        options={},
    )


@asynccontextmanager
async def _build_connector_with_streaming_settings(
    *,
    max_retries: int,
    retry_backoff_seconds: tuple[float, ...],
) -> AsyncIterator[OpenAICodexConnector]:
    reset_renderer_registry()
    client = httpx.AsyncClient()
    config = AppConfig()

    config.backends["openai-codex"].extra.setdefault("codex", {})["streaming"] = {
        "max_retries": max_retries,
        "retry_backoff_seconds": list(retry_backoff_seconds),
    }

    from src.core.di.container import ServiceCollection
    from src.core.di.registrations import backend
    from src.core.di.services import set_service_provider

    services = ServiceCollection()
    backend.register(services, config)
    provider = services.build_service_provider()
    set_service_provider(provider)

    instance = OpenAICodexConnector(client=client, config=config)
    try:
        yield instance
    finally:
        await client.aclose()


@pytest_asyncio.fixture()  # type: ignore[reportUntypedFunctionDecorator]
async def connector() -> AsyncIterator[OpenAICodexConnector]:
    reset_renderer_registry()
    client = httpx.AsyncClient()
    config = AppConfig()

    # Register TranslationService before creating connector (required by connector DI)
    from src.core.di.container import ServiceCollection
    from src.core.di.registrations import backend
    from src.core.di.services import set_service_provider

    services = ServiceCollection()
    backend.register(services, config)
    # Register a fake discovered catalog (mirrors CodexModelCatalogStage) so the
    # connector resolves a catalog from DI instead of the shipped fallback file.
    services.add_instance(
        cast(type, ICodexModelCatalog),
        CodexCatalogParser().parse(make_raw_catalog()),
    )
    provider = services.build_service_provider()
    set_service_provider(provider)

    instance = OpenAICodexConnector(client=client, config=config)
    try:
        yield instance
    finally:
        await client.aclose()


def test_is_codex_model_detection(connector: OpenAICodexConnector) -> None:
    """_is_codex_model recognizes catalog-routable slugs (auto-discovered).

    The injected catalog mirrors the authenticated backend catalog response:
    routable slugs gpt-5.6-sol, gpt-5.6-luna, gpt-5.5; CLI-only
    gpt-5.3-codex-spark and hidden codex-auto-review are NOT routable. Legacy
    slugs absent from the discovered catalog are no longer routable.
    """
    catalog = connector._catalog
    # Routable slugs come from the discovered catalog (no hardcoded list).
    assert catalog.routable_slugs() == ("gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.5")

    # Reasoning effort hierarchy is data-driven (derived from the widest model).
    assert catalog.reasoning_effort_order == (
        "low",
        "medium",
        "high",
        "xhigh",
        "max",
        "ultra",
    )
    assert catalog.reasoning_effort_order == connector.REASONING_EFFORT_LEVELS
    assert connector.DEFAULT_REASONING_EFFORT == "medium"

    # Extended tier support sets are derived from the catalog.
    assert catalog.models_supporting("ultra") == ("gpt-5.6-sol",)
    assert catalog.models_supporting("max") == ("gpt-5.6-sol", "gpt-5.6-luna")
    assert catalog.models_supporting("xhigh") == (
        "gpt-5.6-sol",
        "gpt-5.6-luna",
        "gpt-5.5",
    )

    # _is_codex_model recognizes routable slugs (with/without vendor prefix,
    # case-insensitive).
    assert connector._is_codex_model("gpt-5.6-sol") is True
    assert connector._is_codex_model("gpt-5.6-luna") is True
    assert connector._is_codex_model("gpt-5.5") is True
    assert connector._is_codex_model("openai/gpt-5.6-sol") is True
    assert connector._is_codex_model("GPT-5.6-SOL") is True

    # CLI-only / hidden catalog entries are NOT routable.
    assert connector._is_codex_model("gpt-5.3-codex-spark") is False
    assert connector._is_codex_model("codex-auto-review") is False

    # Legacy slugs absent from the discovered catalog are no longer routable.
    assert connector._is_codex_model("gpt-5.1-codex") is False
    assert connector._is_codex_model("gpt-5.3-codex") is False
    assert connector._is_codex_model("gpt-5-codex") is False

    # Unknown / non-Codex models.
    assert connector._is_codex_model("gpt-4.1") is False
    assert connector._is_codex_model("gpt-4") is False
    assert connector._is_codex_model("claude-3") is False

    # get_available_models advertises the routable slugs (vendor-prefixed).
    assert connector.get_available_models() == [
        "openai/gpt-5.6-sol",
        "openai/gpt-5.6-luna",
        "openai/gpt-5.5",
    ]


@pytest.mark.asyncio
async def test_build_codex_payload_structure(connector: OpenAICodexConnector) -> None:
    chat_request = ChatRequest(
        messages=[ChatMessage(role="user", content="Hello Codex!")],
        model="gpt-5.1-codex",
        stream=True,
        extra_body={
            "codex_capabilities": {
                "include_environment_context": True,
                "tool_schema_mode": "codex_default",
            }
        },
    )

    payload, conversation_id = connector._build_codex_payload(
        chat_request, chat_request.messages, "gpt-5.1-codex"
    )

    assert payload.model == "gpt-5.1-codex"
    assert payload.stream is True
    assert payload.prompt_cache_key == conversation_id

    # With the refactoring, the main system prompt is in the `instructions` field
    assert payload.instructions is not None
    expected_prompt = connector._sanitize_codex_instructions(
        connector._codex_system_prompt()
    ).rstrip()
    assert payload.instructions.rstrip() == expected_prompt

    # The input items should contain the environment context and the user message
    input_items = cast(list[Any], payload.input)
    assert len(input_items) == 2
    # Access content from CodexInputItem model
    env_parts = cast(list[dict[str, Any]], input_items[0].content)
    env_block = env_parts[0]["text"]
    assert env_block.startswith("<environment_context>")
    assert "<model>" not in env_block
    assert "<approval_policy>never</approval_policy>" in env_block
    assert "<sandbox_mode>read-only</sandbox_mode>" in env_block
    assert "<network_access>restricted</network_access>" in env_block
    assert input_items[1].role == "user"
    user_parts = cast(list[dict[str, Any]], input_items[1].content)
    assert user_parts[0]["type"] == "input_text"
    assert user_parts[0]["text"] == "Hello Codex!"
    assert payload.reasoning is not None
    assert payload.reasoning.effort == "medium"
    assert payload.reasoning.summary == "auto"
    assert payload.include == ["reasoning.encrypted_content"]
    tools = payload.tools
    names_by_type = {tool.name: tool.type for tool in tools}
    assert names_by_type["shell"] == "function"
    assert names_by_type["apply_patch"] == "custom"


@pytest.mark.asyncio
async def test_build_codex_payload_custom_prompt_mode(
    connector: OpenAICodexConnector,
) -> None:
    chat_request = ChatRequest(
        messages=[
            ChatMessage(role="system", content="Stay curious"),
            ChatMessage(role="user", content="hello"),
        ],
        model="gpt-5.1-codex",
        extra_body={
            "codex_capabilities": {
                "prompt_mode": "custom_only",
                "include_environment_context": False,
            }
        },
    )

    payload, _ = connector._build_codex_payload(
        chat_request, chat_request.messages, "gpt-5.1-codex"
    )

    assert payload.instructions == "Stay curious"
    input_items = cast(list[Any], payload.input)
    # There should only be one message, the user message
    assert len(input_items) == 1
    # First entry is the system message passed through as-is
    assert input_items[0].role == "user"
    parts = cast(list[dict[str, Any]], input_items[0].content)
    assert parts[0]["text"] == "hello"
    # No environment block injected
    for item in input_items:
        item_parts = (
            cast(list[dict[str, Any]], item.content)
            if isinstance(item.content, list)
            else []
        )
        for part in item_parts:
            if part.get("type") == "input_text":
                assert "<environment_context>" not in part["text"]


@pytest.mark.asyncio
async def test_build_codex_payload_merge_custom_prompt(
    connector: OpenAICodexConnector,
) -> None:
    custom_prompt = "Behave like an expert pair programmer."
    chat_request = ChatRequest(
        messages=[ChatMessage(role="user", content="Hi!")],
        model="gpt-5.1-codex",
        extra_body={
            "codex_capabilities": {"prompt_mode": "merge_custom"},
            "codex_system_prompt": custom_prompt,
        },
    )

    payload, _ = connector._build_codex_payload(
        chat_request, chat_request.messages, "gpt-5.1-codex"
    )

    instructions = payload.instructions or ""

    assert custom_prompt in instructions
    assert "You are Codex" in instructions


@pytest.mark.asyncio
async def test_codex_default_mode_merges_client_system_prompt(
    connector: OpenAICodexConnector,
) -> None:
    chat_request = ChatRequest(
        messages=[
            ChatMessage(role="system", content="Prioritize security fixes."),
            ChatMessage(role="user", content="hello"),
        ],
        model="gpt-5.1-codex",
        extra_body={"codex_capabilities": {"include_environment_context": True}},
    )

    payload, _ = connector._build_codex_payload(
        chat_request, chat_request.messages, "gpt-5.1-codex"
    )

    instructions = (payload.instructions or "").rstrip()
    expected_prompt = connector._sanitize_codex_instructions(
        connector._codex_system_prompt()
    ).rstrip()
    assert instructions == expected_prompt

    input_items = cast(list[Any], payload.input)
    assert len(input_items) == 3
    user_block = input_items[0]
    assert user_block.role == "user"
    user_content = cast(list[dict[str, Any]], user_block.content)
    assert user_content[0]["type"] == "input_text"
    assert user_content[0]["text"].startswith("<user_instructions>")
    assert "Prioritize security fixes." in user_content[0]["text"]
    env_block = input_items[1]
    env_content = cast(list[dict[str, Any]], env_block.content)
    assert env_content[0]["text"].startswith("<environment_context>")


@pytest.mark.asyncio
async def test_codex_xml_mode_handles_structured_tool_calls(
    connector: OpenAICodexConnector,
) -> None:
    tool_call = ToolCall(
        id="call_structured",
        function=FunctionCall(name="shell", arguments='{"command":["ls"]}'),
    )
    assistant_msg = ChatMessage(role="assistant", tool_calls=[tool_call])
    tool_msg = ChatMessage(
        role="tool",
        content='{"output": "files", "exit_code": 0}',
        tool_call_id="call_structured",
    )
    user_msg = ChatMessage(role="user", content="List files")
    chat_request = ChatRequest(
        messages=[user_msg, assistant_msg, tool_msg],
        model="gpt-5.1-codex",
        extra_body={"codex_capabilities": {"tool_text_format": "codex_xml"}},
    )

    items = connector._build_codex_input_items(
        chat_request, chat_request.messages, "gpt-5.1-codex"
    )

    function_calls = [item for item in items if item["type"] == "function_call"]
    outputs = [item for item in items if item["type"] == "function_call_output"]

    assert len(function_calls) == 1
    assert len(outputs) == 1

    call_entry = function_calls[0]
    output_entry = outputs[0]

    assert call_entry["call_id"] == "call_structured"
    assert call_entry["name"] == "shell"
    assert json.loads(call_entry["arguments"])["command"] == ["ls"]

    parsed_output = json.loads(output_entry["output"])
    assert parsed_output["output"] == '{"output": "files", "exit_code": 0}'


@pytest.mark.asyncio
async def test_config_default_capabilities_from_backend_extra() -> None:
    reset_renderer_registry()
    config = AppConfig()
    config.backends["openai-codex"].extra.setdefault("codex", {}).update(
        {
            "default_capabilities": {
                "tool_text_format": "codex_xml",
                "include_environment_context": False,
            }
        }
    )
    async with httpx.AsyncClient() as client:
        connector = OpenAICodexConnector(client=client, config=config)
        chat_request = ChatRequest(
            messages=[ChatMessage(role="user", content="hello")],
            model="gpt-5.1-codex",
        )
        capabilities = connector._resolve_capabilities(chat_request)
        assert capabilities.tool_text_format == "codex_xml"
        assert capabilities.include_environment_context is False
    reset_renderer_registry()


@pytest.mark.asyncio
async def test_prompt_configuration_applies_prepend_append() -> None:
    reset_renderer_registry()
    config = AppConfig()
    config.backends["openai-codex"].extra.setdefault("codex", {}).update(
        {
            "prompt": {
                "prepend": ["<environment constraints>"],
                "append": ["<end of rules>"],
            }
        }
    )
    async with httpx.AsyncClient() as client:
        connector = OpenAICodexConnector(client=client, config=config)
        chat_request = ChatRequest(
            messages=[ChatMessage(role="user", content="hello")],
            model="gpt-5.1-codex",
        )
        payload, _ = connector._build_codex_payload(
            chat_request, chat_request.messages, "gpt-5.1-codex"
        )
    instructions = payload.instructions or ""

    assert instructions.startswith("<environment constraints>")
    assert instructions.endswith("<end of rules>")

    reset_renderer_registry()


@pytest.mark.asyncio
async def test_tool_schema_configuration_overrides_default() -> None:
    reset_renderer_registry()
    config = AppConfig()
    config.backends["openai-codex"].extra.setdefault("codex", {}).update(
        {
            "tool_schema": {
                "base_tools": [
                    {
                        "type": "function",
                        "name": "echo",
                        "description": "Echo text back",
                        "parameters": {
                            "type": "object",
                            "properties": {"text": {"type": "string"}},
                            "required": ["text"],
                        },
                    }
                ]
            }
        }
    )
    async with httpx.AsyncClient() as client:
        connector = OpenAICodexConnector(client=client, config=config)
        tools = connector._default_codex_tools()
        assert len(tools) == 1
        assert tools[0]["name"] == "echo"
    reset_renderer_registry()


@pytest.mark.asyncio
async def test_tool_schema_custom_only_uses_config_defaults() -> None:
    reset_renderer_registry()
    config = AppConfig()
    config.backends["openai-codex"].extra.setdefault("codex", {}).update(
        {
            "tool_schema": {
                "custom_tools": [
                    {
                        "type": "function",
                        "name": "workspace_info",
                        "description": "Returns workspace metadata",
                        "parameters": {"type": "object", "properties": {}},
                    }
                ]
            }
        }
    )
    async with httpx.AsyncClient() as client:
        connector = OpenAICodexConnector(client=client, config=config)
        chat_request = ChatRequest(
            messages=[ChatMessage(role="user", content="hello")],
            model="gpt-5.1-codex",
            extra_body={"codex_capabilities": {"tool_schema_mode": "custom_only"}},
        )
        payload, _ = connector._build_codex_payload(
            chat_request, chat_request.messages, "gpt-5.1-codex"
        )
        tools = payload.tools
        assert len(tools) == 1
        assert tools[0].name == "workspace_info"

    reset_renderer_registry()


@pytest.mark.asyncio
async def test_renderer_configuration_alias_and_default() -> None:
    reset_renderer_registry()
    config = AppConfig()
    config.backends["openai-codex"].extra.setdefault("codex", {}).update(
        {"renderer": {"aliases": {"cli": "xml"}, "default": "cli"}}
    )
    async with httpx.AsyncClient() as client:
        connector = OpenAICodexConnector(client=client, config=config)
        chat_request = ChatRequest(
            messages=[ChatMessage(role="user", content="hello")],
            model="gpt-5.1-codex",
        )
        capabilities = connector._resolve_capabilities(chat_request)
        renderer_key = connector._select_renderer_key(capabilities)
        assert renderer_key == "cli"
        tool_call = ToolCall(
            id="call-1",
            function=FunctionCall(name="shell", arguments='{"command":["ls"]}'),
        )
        with OverrideRenderer(renderer_key):
            rendered = render_tool_call(tool_call)
        assert rendered and rendered.startswith("<execute_command>")
    reset_renderer_registry()


@pytest.mark.asyncio
async def test_codex_passthrough_skips_translation(
    connector: OpenAICodexConnector, mocker: MockerFixture
) -> None:
    """Verify that native-like payloads bypass the translation method."""
    # This payload is structurally similar to a native Codex/Responses payload
    native_payload = {
        "model": "gpt-5.1-codex",
        "input": [{"role": "user", "content": [{"type": "input_text", "text": "hi"}]}],
        "stream": True,
    }

    # Mock the method that would be called if translation were to occur
    build_input_items_mock = mocker.patch.object(
        connector, "_build_codex_input_items", return_value=[]
    )

    # Simulate a passthrough scenario by directly passing the native payload
    # and setting the capabilities.
    capabilities = CodexClientCapabilities(codex_passthrough=True)
    payload, _ = connector._build_codex_payload(
        native_payload, [], "gpt-5.1-codex", capabilities=capabilities
    )

    # The payload should be the native one, with minor adjustments
    assert payload.model == "gpt-5.1-codex"
    assert payload.stream is True
    input_items = cast(list[Any], payload.input)
    assert input_items[0].role == "user"

    # The key assertion: translation was bypassed
    build_input_items_mock.assert_not_called()


@pytest.mark.asyncio
async def test_codex_passthrough_preserves_previous_response_id(
    connector: OpenAICodexConnector,
) -> None:
    native_payload = {
        "model": "gpt-5.1-codex",
        "input": [{"role": "user", "content": [{"type": "input_text", "text": "hi"}]}],
        "previous_response_id": "resp-123",
        "stream": True,
    }

    capabilities = CodexClientCapabilities(codex_passthrough=True)
    payload, _ = connector._build_codex_payload(
        native_payload,
        [],
        "gpt-5.1-codex",
        capabilities=capabilities,
    )

    assert payload.previous_response_id == "resp-123"


@pytest.mark.asyncio
async def test_codex_headers_include_expected_fields() -> None:
    client = httpx.AsyncClient()
    config = AppConfig()
    connector = OpenAICodexConnector(client=client, config=config)
    headers = connector._build_codex_headers("conversation-id")
    assert headers["OpenAI-Beta"] == "responses=experimental"
    assert headers["conversation_id"] == "conversation-id"
    assert headers["session_id"] == "conversation-id"
    assert headers["Codex-Task-Type"] == "standard"
    assert headers["originator"] == connector.CODEX_ORIGINATOR
    assert headers["version"] == connector.CODEX_VERSION_HEADER
    assert headers["version"] == "0.156.0"
    assert "User-Agent" in headers
    await client.aclose()


@pytest.mark.asyncio
async def test_codex_client_version_uses_configured_model_catalog_version() -> None:
    client = httpx.AsyncClient()
    config = AppConfig()
    connector = OpenAICodexConnector(client=client, config=config)
    connector._connector_settings = {"model_catalog": {"client_version": "0.155.0"}}  # type: ignore[reportPrivateUsage]

    assert connector.codex_client_version == "0.155.0"
    assert connector._build_codex_headers("c-id")["version"] == "0.155.0"
    assert "codex_cli_rs/0.155.0" in connector._codex_user_agent()
    await client.aclose()


@pytest.mark.asyncio
async def test_streaming_refresh_rebuilds_authorization_header(
    mocker: MockerFixture,
) -> None:
    async with _build_connector_with_streaming_settings(
        max_retries=2, retry_backoff_seconds=(0.0, 0.0, 0.0)
    ) as connector:
        request = ChatRequest(
            messages=[ChatMessage(role="user", content="hi")],
            model="gpt-5.1-codex",
            stream=True,
        )
        payload = CodexPayload(
            model="gpt-5.1-codex",
            input=[],
            tools=[],
            tool_choice="auto",
            parallel_tool_calls=False,
            store=False,
            prompt_cache_key="conv-123",
            stream=True,
            include=[],
        )

        mocker.patch.object(
            connector, "_build_codex_payload", return_value=(payload, "conv-123")
        )
        mocker.patch.object(
            connector, "_resolve_capabilities", return_value=CodexClientCapabilities()
        )

        connector.api_key = "token_old"

        refresh_count = 0

        async def refresh_stub() -> bool:
            nonlocal refresh_count
            refresh_count += 1
            connector.api_key = f"token_new_{refresh_count}"
            return True

        refresh_mock = mocker.patch.object(
            connector, "_refresh_access_token", side_effect=refresh_stub
        )

        headers_seen: list[str | None] = []
        call_count = 0

        async def _successful_event_iterator() -> AsyncIterator[ProcessedResponse]:
            yield ProcessedResponse(content={"event": "ok"})

        success_handle = StreamingResponseHandle(
            iterator=_successful_event_iterator(),
            cancel_callback=AsyncMock(),
            headers={"Authorization": "Bearer token_new_2"},
        )

        async def streaming_side_effect(
            url: str,
            request_payload: dict[str, Any],
            request_headers: dict[str, str],
            request_session_id: str,
            stream_format: str,
            **kwargs: Any,
        ) -> StreamingResponseHandle:
            nonlocal call_count
            headers_seen.append(request_headers.get("Authorization"))
            call_count += 1
            if call_count <= 2:
                raise HTTPException(status_code=401, detail="expired")
            return success_handle

        mocker.patch.object(
            connector,
            "_handle_streaming_response",
            side_effect=streaming_side_effect,
        )

        result = await connector._call_codex_responses_api(
            request_data=request,
            processed_messages=request.messages,
            effective_model="gpt-5.1-codex",
            domain_request=request,
        )

        assert isinstance(result, StreamingResponseEnvelope)
        assert result.content is not None
        chunk = await result.content.__anext__()
        assert isinstance(chunk, ProcessedResponse)
        assert chunk.content == {"event": "ok"}
        assert headers_seen == [
            "Bearer token_old",
            "Bearer token_new_1",
            "Bearer token_new_2",
        ]
        assert refresh_mock.await_count == 2


@pytest.mark.asyncio
async def test_streaming_auth_failure_chunk_triggers_retry(
    mocker: MockerFixture,
) -> None:
    async with _build_connector_with_streaming_settings(
        max_retries=2, retry_backoff_seconds=(0.0, 0.0, 0.0)
    ) as connector:
        request = ChatRequest(
            messages=[ChatMessage(role="user", content="hi")],
            model="gpt-5.1-codex",
            stream=True,
        )
        payload = CodexPayload(
            model="gpt-5.1-codex",
            input=[],
            tools=[],
            tool_choice="auto",
            parallel_tool_calls=False,
            store=False,
            prompt_cache_key="conv-123",
            stream=True,
            include=[],
        )

        mocker.patch.object(
            connector, "_build_codex_payload", return_value=(payload, "conv-123")
        )
        mocker.patch.object(
            connector, "_resolve_capabilities", return_value=CodexClientCapabilities()
        )

        connector.api_key = "token_old"

        refresh_count = 0

        async def refresh_stub() -> bool:
            nonlocal refresh_count
            refresh_count += 1
            connector.api_key = f"token_new_{refresh_count}"
            return True

        refresh_mock = mocker.patch.object(
            connector, "_refresh_access_token", side_effect=refresh_stub
        )

        async def failing_iterator(
            status: int, code: str
        ) -> AsyncIterator[ProcessedResponse]:
            yield ProcessedResponse(
                content={
                    "error": "Responses stream reported failure",
                    "details": {"status": status, "code": code},
                }
            )

        async def success_iterator() -> AsyncIterator[ProcessedResponse]:
            yield ProcessedResponse(
                content={
                    "choices": [
                        {
                            "index": 0,
                            "delta": {"content": "hello"},
                            "finish_reason": None,
                        }
                    ]
                }
            )

        cancel_first = AsyncMock()
        cancel_second = AsyncMock()
        cancel_third = AsyncMock()
        first_handle = StreamingResponseHandle(
            iterator=failing_iterator(401, "authentication_error"),
            cancel_callback=cancel_first,
            headers={"Authorization": "Bearer token_old"},
        )
        second_handle = StreamingResponseHandle(
            iterator=failing_iterator(401, "token_expired"),
            cancel_callback=cancel_second,
            headers={"Authorization": "Bearer token_new_1"},
        )
        success_handle = StreamingResponseHandle(
            iterator=success_iterator(),
            cancel_callback=cancel_third,
            headers={"Authorization": "Bearer token_new_2"},
        )

        stream_handles = [first_handle, second_handle, success_handle]
        headers_seen: list[str | None] = []

        def handle_side_effect(
            url: str,
            request_payload: dict[str, Any],
            request_headers: dict[str, str],
            request_session_id: str,
            stream_format: str,
            **kwargs: Any,
        ) -> StreamingResponseHandle:
            headers_seen.append(request_headers.get("Authorization"))
            return stream_handles.pop(0)

        handle_mock = mocker.patch.object(
            connector, "_handle_streaming_response", side_effect=handle_side_effect
        )

        result = await connector._call_codex_responses_api(
            request_data=request,
            processed_messages=request.messages,
            effective_model="gpt-5.1-codex",
            domain_request=request,
        )

        assert isinstance(result, StreamingResponseEnvelope)
        assert result.content is not None
        chunk = await result.content.__anext__()
        assert isinstance(chunk, ProcessedResponse)
        assert chunk.content is not None
        assert isinstance(chunk.content, dict)
        content_dict = cast(dict[str, Any], chunk.content)
        assert content_dict["choices"][0]["delta"]["content"] == "hello"
        assert headers_seen == [
            "Bearer token_old",
            "Bearer token_new_1",
            "Bearer token_new_2",
        ]
        assert refresh_mock.await_count == 2
        cancel_first.assert_awaited_once()
        cancel_second.assert_awaited_once()
        cancel_third.assert_not_called()
        assert handle_mock.call_count == 3
        with pytest.raises(StopAsyncIteration):
            await result.content.__anext__()
        assert result.headers is not None
        assert dict(result.headers) == {"Authorization": "Bearer token_new_2"}


@pytest.mark.asyncio
async def test_streaming_handshake_exceeds_retry_limit(
    mocker: MockerFixture,
) -> None:
    async with _build_connector_with_streaming_settings(
        max_retries=1, retry_backoff_seconds=(0.0,)
    ) as connector:
        request = ChatRequest(
            messages=[ChatMessage(role="user", content="hi")],
            model="gpt-5.1-codex",
            stream=True,
        )
        payload = CodexPayload(
            model="gpt-5.1-codex",
            input=[],
            tools=[],
            tool_choice="auto",
            parallel_tool_calls=False,
            store=False,
            prompt_cache_key="conv-123",
            stream=True,
            include=[],
        )
        mocker.patch.object(
            connector, "_build_codex_payload", return_value=(payload, "conv-123")
        )

        mocker.patch.object(
            connector, "_resolve_capabilities", return_value=CodexClientCapabilities()
        )

        connector.api_key = "token_old"

        async def refresh_stub() -> bool:
            connector.api_key = "token_new_1"
            return True

        refresh_mock = mocker.patch.object(
            connector, "_refresh_access_token", side_effect=refresh_stub
        )
        degrade_mock = mocker.patch.object(connector, "_degrade")

        # Managed OAuth can raise the effective rotation budget above ``max_retries``;
        # this test pins the floor so handshake exhaustion matches connector streaming config.
        mocker.patch.object(
            connector._response_executor._credential_manager,
            "effective_max_rate_limit_retries",
            AsyncMock(return_value=1),
        )

        headers_seen: list[str | None] = []

        async def streaming_side_effect(
            url: str,
            request_payload: dict[str, Any],
            request_headers: dict[str, str],
            request_session_id: str,
            stream_format: str,
            **kwargs: Any,
        ) -> StreamingResponseHandle:
            headers_seen.append(request_headers.get("Authorization"))
            raise HTTPException(status_code=401, detail="expired")

        mocker.patch.object(
            connector, "_handle_streaming_response", side_effect=streaming_side_effect
        )

        result = await connector._call_codex_responses_api(
            request_data=request,
            processed_messages=request.messages,
            effective_model="gpt-5.1-codex",
            domain_request=request,
        )

        assert isinstance(result, StreamingResponseEnvelope)
        assert result.content is not None
        with pytest.raises(HTTPException) as exc_info:
            await result.content.__anext__()

        assert exc_info.value.status_code == 401
        assert refresh_mock.await_count == 1
        degrade_mock.assert_called_once()
        degrade_messages = degrade_mock.call_args[0][0]
        assert any("handshake" in msg for msg in degrade_messages)
        assert headers_seen == ["Bearer token_old", "Bearer token_new_1"]


@pytest.mark.asyncio
async def test_streaming_auth_failure_chunk_unrecoverable(
    mocker: MockerFixture,
) -> None:
    async with _build_connector_with_streaming_settings(
        max_retries=2, retry_backoff_seconds=(0.0, 0.0)
    ) as connector:
        request = ChatRequest(
            messages=[ChatMessage(role="user", content="hi")],
            model="gpt-5.1-codex",
            stream=True,
        )
        payload = CodexPayload(
            model="gpt-5.1-codex",
            input=[],
            tools=[],
            tool_choice="auto",
            parallel_tool_calls=False,
            store=False,
            prompt_cache_key="conv-123",
            stream=True,
            include=[],
        )
        mocker.patch.object(
            connector, "_build_codex_payload", return_value=(payload, "conv-123")
        )

        mocker.patch.object(
            connector, "_resolve_capabilities", return_value=CodexClientCapabilities()
        )

        connector.api_key = "stale"

        mocker.patch.object(
            connector, "_refresh_access_token", AsyncMock(return_value=False)
        )
        degrade_mock = mocker.patch.object(connector, "_degrade")

        async def failing_iterator() -> AsyncIterator[ProcessedResponse]:
            yield ProcessedResponse(
                content={
                    "error": "Responses stream reported failure",
                    "details": {"status": 401, "code": "invalid_token"},
                }
            )

        cancel_cb = AsyncMock()
        stream_handle = StreamingResponseHandle(
            iterator=failing_iterator(),
            cancel_callback=cancel_cb,
            headers={"Authorization": "Bearer stale"},
        )

        def handle_side_effect(
            url: str,
            request_payload: dict[str, Any],
            request_headers: dict[str, str],
            request_session_id: str,
            stream_format: str,
            **kwargs: Any,
        ) -> StreamingResponseHandle:
            return stream_handle

        mocker.patch.object(
            connector, "_handle_streaming_response", side_effect=handle_side_effect
        )

        result = await connector._call_codex_responses_api(
            request_data=request,
            processed_messages=request.messages,
            effective_model="gpt-5.1-codex",
            domain_request=request,
        )

        assert isinstance(result, StreamingResponseEnvelope)
        assert result.content is not None
        with pytest.raises(HTTPException) as exc_info:
            await result.content.__anext__()

        assert exc_info.value.status_code == 401
        degrade_mock.assert_called_once()
        degrade_messages = degrade_mock.call_args[0][0]
        assert any("token refresh" in msg for msg in degrade_messages)
        cancel_cb.assert_awaited_once()


@pytest.mark.asyncio
async def test_chat_completions_routes_to_codex_api(
    connector: OpenAICodexConnector, mocker: MockerFixture
) -> None:
    mocker.patch.object(
        connector, "_validate_runtime_credentials", return_value=(True, [])
    )
    mocker.patch.object(connector, "_load_auth", AsyncMock(return_value=True))
    connector.api_key = "Bearer test-token"
    codex_mock = mocker.patch.object(
        connector, "_call_codex_responses_api", AsyncMock(return_value="codex-result")
    )
    super_cls = type(connector).__mro__[1]
    super_mock = mocker.patch.object(super_cls, "chat_completions", AsyncMock())

    chat_request = ChatRequest(
        messages=[ChatMessage(role="user", content="Hello Codex!")],
        model="gpt-5.6-sol",
        stream=True,
    )

    result = await connector.chat_completions(
        _connector_chat_request(chat_request, effective_model="gpt-5.6-sol")
    )

    assert result == "codex-result"
    codex_mock.assert_awaited_once()
    super_mock.assert_not_called()


@pytest.mark.asyncio
async def test_chat_completions_non_codex_falls_back_to_parent(
    connector: OpenAICodexConnector, mocker: MockerFixture
) -> None:
    mocker.patch.object(
        connector, "_validate_runtime_credentials", return_value=(True, [])
    )
    mocker.patch.object(connector, "_load_auth", AsyncMock(return_value=True))
    # Mock the authentication method to provide valid headers
    mocker.patch.object(
        connector,
        "get_headers",
        return_value={"Authorization": "Bearer test-token"},
    )
    connector.api_key = "Bearer test-token"
    codex_mock = mocker.patch.object(
        connector, "_call_codex_responses_api", AsyncMock(return_value="codex-result")
    )
    super_cls = type(connector).__mro__[1]
    super_mock = mocker.patch.object(
        super_cls, "chat_completions", AsyncMock(return_value="openai-result")
    )

    chat_request = ChatRequest(
        messages=[ChatMessage(role="user", content="Hello classic OpenAI!")],
        model="gpt-4.1-mini",
        stream=False,
    )

    result = await connector.chat_completions(
        _connector_chat_request(chat_request, effective_model="gpt-4.1-mini")
    )

    assert result == "openai-result"
    codex_mock.assert_not_called()
    super_mock.assert_awaited_once()


def test_resolve_capabilities_defaults(connector: OpenAICodexConnector) -> None:
    chat_request = ChatRequest(
        messages=[ChatMessage(role="user", content="hello")],
        model="gpt-5.1-codex",
    )

    capabilities = connector._resolve_capabilities(chat_request)

    # Defaults from settings.py
    expected = CodexClientCapabilities(
        tool_schema_mode="custom_only",
        bypass_tool_call_reactor=True,
        include_environment_context=False,
    )
    assert capabilities == expected


def test_resolve_capabilities_from_extra_body(
    connector: OpenAICodexConnector,
) -> None:
    chat_request = ChatRequest(
        messages=[ChatMessage(role="user", content="hello")],
        model="gpt-5.1-codex",
        extra_body={
            "client_capabilities": {
                "protocol": "openai-responses",
                "codex_passthrough": True,
                "tool_text_format": "none",
            }
        },
    )

    capabilities = connector._resolve_capabilities(chat_request)

    assert capabilities.protocol == "openai-responses"
    assert capabilities.codex_passthrough is True
    # Fields not overridden should keep defaults
    assert capabilities.prompt_mode == CodexClientCapabilities().prompt_mode
    assert capabilities.tool_schema_mode == "custom_only"
    # Explicit override respected
    assert capabilities.tool_text_format == "none"


def test_resolve_capabilities_for_cline_agent(
    connector: OpenAICodexConnector,
) -> None:
    chat_request = ChatRequest(
        messages=[ChatMessage(role="user", content="hello")],
        model="gpt-5.1-codex",
        agent="cline",
    )

    capabilities = connector._resolve_capabilities(chat_request)

    assert capabilities.tool_text_format == "codex_xml"


@pytest.mark.asyncio
async def test_codex_retries_after_token_refresh(
    connector: OpenAICodexConnector, mocker: MockerFixture
) -> None:
    chat_request = ChatRequest(
        messages=[ChatMessage(role="user", content="hello")],
        model="gpt-5.1-codex",
        stream=False,
    )

    mocker.patch.object(
        connector,
        "_build_codex_payload",
        return_value=(
            CodexPayload(
                model="gpt-5.1-codex",
                input=[],
                tools=[],
                tool_choice="auto",
                parallel_tool_calls=False,
                store=False,
                prompt_cache_key="cid-1",
                stream=False,
                include=[],
            ),
            "cid-1",
        ),
    )

    # Set api_key so get_headers() returns Authorization header
    connector.api_key = "test_token"

    # Mock streaming handshake to return 401 first, then a successful stream handle
    call_count = 0

    async def success_iterator() -> AsyncIterator[ProcessedResponse]:
        yield ProcessedResponse(
            content={
                "choices": [
                    {
                        "index": 0,
                        "delta": {"content": "ok"},
                        "finish_reason": None,
                    }
                ]
            }
        )
        yield ProcessedResponse(
            content={
                "choices": [
                    {
                        "index": 0,
                        "delta": {},
                        "finish_reason": "stop",
                    }
                ],
                "usage": {
                    "prompt_tokens": 10,
                    "completion_tokens": 2,
                    "total_tokens": 12,
                },
            },
            usage=UsageSummary(
                prompt_tokens=10,
                completion_tokens=2,
                total_tokens=12,
            ),
            metadata={"done": True},
        )

    success_handle = StreamingResponseHandle(
        iterator=success_iterator(),
        cancel_callback=AsyncMock(),
        headers={"x-request-id": "req-refresh"},
    )

    async def streaming_side_effect(*args, **kwargs):
        nonlocal call_count
        call_count += 1
        if call_count == 1:
            raise HTTPException(status_code=401, detail="Unauthorized")
        return success_handle

    streaming_mock = mocker.patch.object(
        connector,
        "_handle_streaming_response",
        AsyncMock(side_effect=streaming_side_effect),
    )
    refresh_mock = mocker.patch.object(
        connector,
        "_refresh_access_token",
        AsyncMock(return_value=True),
    )

    result = await connector._call_codex_responses_api(
        chat_request,
        chat_request.messages,
        "gpt-5.1-codex",
        chat_request,
    )

    # Verify result is a ResponseEnvelope with the expected content
    assert isinstance(result, ResponseEnvelope)
    if isinstance(result.content, dict):
        assert result.content.get("choices") is not None
    else:
        assert "ok" in str(result.content)
    # Verify streaming handshake retried once and then succeeded
    assert streaming_mock.await_count == 2
    refresh_mock.assert_awaited_once()


@pytest.mark.asyncio
async def test_call_codex_responses_api_accumulates_stream_for_non_stream_clients(
    mocker: MockerFixture,
) -> None:
    async with _build_connector_with_streaming_settings(
        max_retries=1, retry_backoff_seconds=(0.0,)
    ) as connector:
        chat_request = ChatRequest(
            messages=[ChatMessage(role="user", content="hi")],
            model="gpt-5.1-codex",
            stream=False,
        )
        payload = CodexPayload(
            model="gpt-5.1-codex",
            input=[],
            tools=[],
            tool_choice="auto",
            parallel_tool_calls=False,
            store=False,
            prompt_cache_key="cid-acc",
            stream=True,
            include=[],
        )
        mocker.patch.object(
            connector, "_build_codex_payload", return_value=(payload, "cid-acc")
        )
        mocker.patch.object(
            connector, "_resolve_capabilities", return_value=CodexClientCapabilities()
        )

        async def iterator() -> AsyncIterator[ProcessedResponse]:
            yield ProcessedResponse(
                content={
                    "choices": [
                        {
                            "index": 0,
                            "delta": {"content": "hello"},
                            "finish_reason": None,
                        }
                    ]
                }
            )
            yield ProcessedResponse(
                content={
                    "choices": [
                        {
                            "index": 0,
                            "delta": {},
                            "finish_reason": "stop",
                        }
                    ],
                    "usage": {
                        "prompt_tokens": 10,
                        "completion_tokens": 2,
                        "total_tokens": 12,
                    },
                },
                usage=UsageSummary(
                    prompt_tokens=10,
                    completion_tokens=2,
                    total_tokens=12,
                ),
                metadata={"done": True},
            )

        mock_handle = StreamingResponseHandle(
            iterator=iterator(),
            cancel_callback=AsyncMock(),
            headers={"x-request-id": "req-acc"},
        )
        mocker.patch.object(
            connector._response_executor._base_connector,
            "_handle_streaming_response",
            AsyncMock(return_value=mock_handle),
        )

        result = await connector._call_codex_responses_api(
            chat_request,
            chat_request.messages,
            "gpt-5.1-codex",
            chat_request,
        )

        assert isinstance(result, ResponseEnvelope)
        assert result.headers == {"x-request-id": "req-acc"}
        assert result.usage is not None
        assert result.usage.total_tokens == 12
        assert isinstance(result.content, dict)
        assert result.content["choices"][0]["message"]["content"] == "hello"
        assert result.content["choices"][0]["finish_reason"] == "stop"


@pytest.mark.asyncio
async def test_build_codex_input_items_function_call_and_output(
    connector: OpenAICodexConnector,
) -> None:
    tool_call = ToolCall(
        id="call_123",
        function=FunctionCall(name="shell", arguments='{"command":["ls"]}'),
    )
    assistant_message = ChatMessage(role="assistant", tool_calls=[tool_call])
    tool_message = ChatMessage(
        role="tool",
        content="exit code: 0",
        tool_call_id="call_123",
    )
    user_message = ChatMessage(role="user", content="List files")

    items = connector._build_codex_input_items(
        ChatRequest(
            messages=[user_message, assistant_message, tool_message],
            model="gpt-5.1-codex",
            extra_body={"codex_capabilities": {"include_environment_context": True}},
        ),
        [user_message, assistant_message, tool_message],
        "gpt-5.1-codex",
    )

    # env context + user + function call + output
    assert len(items) == 4
    assert items[0]["content"][0]["text"].startswith("<environment_context>")
    assert items[1]["role"] == "user"
    assert items[2]["type"] == "function_call"
    assert items[2]["call_id"] == "call_123"
    assert items[2]["name"] == "shell"
    assert items[2]["arguments"] == '{"command":["ls"]}'
    assert items[3]["type"] == "function_call_output"
    assert items[3]["call_id"] == "call_123"
    assert items[3]["output"] == '{"output": "exit code: 0"}'


@pytest.mark.asyncio
async def test_build_codex_input_items_textual_tool_flow(
    connector: OpenAICodexConnector,
) -> None:
    assistant_text = (
        "<execute_command>"
        "<command>bash -lc ls</command>"
        "<cwd>/workspace</cwd>"
        "</execute_command>"
    )
    user_text = (
        "[execute_command for 'bash -lc ls'] Result:\n"
        "Command executed in terminal  within working directory '/workspace'. Exit code: 0\n"
        "Output:\n\nfile_one\nfile_two\n"
    )
    messages = [
        ChatMessage(role="user", content="List project files"),
        ChatMessage(role="assistant", content=assistant_text),
        ChatMessage(role="user", content=user_text),
    ]

    chat_request = ChatRequest(
        messages=messages,
        model="gpt-5.1-codex",
        extra_body={"codex_capabilities": {"tool_text_format": "codex_xml"}},
    )

    items = connector._build_codex_input_items(
        chat_request,
        messages,
        "gpt-5.1-codex",
    )

    function_calls = [item for item in items if item["type"] == "function_call"]
    outputs = [item for item in items if item["type"] == "function_call_output"]

    assert len(function_calls) == 1
    assert len(outputs) == 1

    call_entry = function_calls[0]
    output_entry = outputs[0]

    assert call_entry["name"] == "shell"
    parsed_args = json.loads(call_entry["arguments"])
    assert parsed_args["command"] == ["bash", "-lc", "ls"]
    assert parsed_args["workdir"] == "/workspace"

    assert call_entry["call_id"] == output_entry["call_id"]
    parsed_output = json.loads(output_entry["output"])
    assert parsed_output["output"].startswith("file_one")
    assert parsed_output["exit_code"] == 0
    assert parsed_output["workdir"] == "/workspace"


@pytest.mark.asyncio
async def test_codex_refresh_failure_propagates(
    connector: OpenAICodexConnector, mocker: MockerFixture
) -> None:
    chat_request = ChatRequest(
        messages=[ChatMessage(role="user", content="hello")],
        model="gpt-5.1-codex",
        stream=False,
    )

    mocker.patch.object(
        connector,
        "_build_codex_payload",
        return_value=(
            CodexPayload(
                model="gpt-5.1-codex",
                input=[],
                tools=[],
                tool_choice="auto",
                parallel_tool_calls=False,
                store=False,
                prompt_cache_key="cid-1",
                stream=False,
                include=[],
            ),
            "cid-1",
        ),
    )

    # Set api_key so get_headers() returns Authorization header
    connector.api_key = "test_token"

    # Mock streaming handshake to fail auth immediately
    mocker.patch.object(
        connector,
        "_handle_streaming_response",
        AsyncMock(side_effect=HTTPException(status_code=401, detail="Unauthorized")),
    )
    refresh_mock = mocker.patch.object(
        connector,
        "_refresh_access_token",
        AsyncMock(return_value=False),
    )

    result = await connector._call_codex_responses_api(
        chat_request,
        chat_request.messages,
        "gpt-5.1-codex",
        chat_request,
    )

    assert isinstance(result, ResponseEnvelope)
    assert result.status_code == 401
    assert isinstance(result.content, dict)
    assert result.content["error"]["error"] == "openai_codex_stream_auth_failed"
    refresh_mock.assert_awaited_once()


@pytest.mark.asyncio
async def test_codex_api_http_error_propagation(
    mocker: MockerFixture,
) -> None:
    async with _build_connector_with_streaming_settings(
        max_retries=0, retry_backoff_seconds=(0.0,)
    ) as connector:
        chat_request = ChatRequest(
            messages=[ChatMessage(role="user", content="hello")],
            model="gpt-5.6-sol",
            stream=False,
        )
        mocker.patch.object(
            connector, "_validate_runtime_credentials", return_value=(True, [])
        )
        mocker.patch.object(connector, "_load_auth", AsyncMock(return_value=True))
        mocker.patch.object(
            connector,
            "get_headers",
            return_value={"Authorization": "Bearer valid-token"},
        )
        connector.api_key = "Bearer valid-token"

        # Codex backend is streamed under the hood even for non-streaming requests; errors can
        # surface during stream handshake/consumption and are converted into an error envelope.
        mocker.patch.object(
            connector._response_executor._base_connector,
            "_handle_streaming_response",
            AsyncMock(
                side_effect=HTTPException(
                    status_code=429, detail={"error": "rate limit exceeded"}
                )
            ),
        )

        result = await connector.chat_completions(
            _connector_chat_request(chat_request, effective_model="gpt-5.6-sol")
        )

        assert isinstance(result, ResponseEnvelope)
        assert result.status_code == 429
        assert isinstance(result.content, dict)
        assert result.content.get("error", {}).get("error") == "rate limit exceeded"
