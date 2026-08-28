"""
Utilities for manipulating and normalizing chat history.
"""

from __future__ import annotations

import logging
from collections.abc import Callable

from src.core.domain.chat import ChatMessage, ToolCall
from src.core.utils.token_count import extract_prompt_text

logger = logging.getLogger(__name__)


def stringify_tool_calls_and_results(
    messages: list[ChatMessage],
    *,
    max_tool_result_chars: int = 2000,
    max_converted_tool_messages: int = 50,
    include_ids: bool = True,
    include_descriptions: bool = True,
    signature_checker: Callable[[ToolCall], bool] | None = None,
) -> list[ChatMessage]:
    """Convert tool calls and results into plain text within chat history.

    This is essential for backends that require session-specific metadata (like
    Gemini thought signatures) when such signatures are unavailable (e.g.,
    passing history to a different model or after a session restart).

    Args:
        messages: The chat history to process.
        max_tool_result_chars: Maximum length of converted tool output text.
        max_converted_tool_messages: Maximum number of recent tool messages to keep.
        include_ids: Whether to include tool_call_id in the text transcript.
        include_descriptions: Whether to include text descriptions of tool calls
                             in assistant messages.
        signature_checker: Optional callback to determine if a tool call has a valid
                          signature. If provided, tool calls with signatures will
                          be preserved as actual tool calls. If None, all tool
                          calls are stringified.

    Returns:
        A new list of ChatMessages with tool calls/results converted to text.
    """
    if not messages:
        return []

    downgraded: list[ChatMessage] = []

    # 1. Identify which tool calls we can keep
    kept_tool_call_ids: set[str] = set()
    if signature_checker:
        for msg in messages:
            if msg.role == "assistant" and msg.tool_calls:
                for tc in msg.tool_calls:
                    if signature_checker(tc) and tc.id:
                        kept_tool_call_ids.add(tc.id)

    # 2. Count convertible tool messages to handle skipping old ones if over limit
    convertible_tool_message_count = 0
    for m in messages:
        if m.role == "tool":
            if m.tool_call_id and m.tool_call_id in kept_tool_call_ids:
                continue
            convertible_tool_message_count += 1

    convertible_tool_message_skip_before = max(
        0, convertible_tool_message_count - max_converted_tool_messages
    )
    convertible_tool_message_seen = 0

    for msg in messages:
        # 3. Handle Assistant Tool Calls
        if msg.role == "assistant" and msg.tool_calls:
            kept_in_msg: list[ToolCall] = []
            descriptions: list[str] = []

            for tc in msg.tool_calls:
                if signature_checker and signature_checker(tc):
                    kept_in_msg.append(tc)
                elif include_descriptions:
                    name = tc.function.name if tc.function else "unknown"
                    args = tc.function.arguments if tc.function else "{}"
                    desc = f"[Tool Call: {name}({args})]"
                    if include_ids and tc.id:
                        desc = f"{desc} (id: {tc.id})"
                    descriptions.append(desc)

            # If we kept some tool calls, we must yield a message with THEM.
            # If we also have descriptions, we should ideally put them in content.

            content_text = ""
            if isinstance(msg.content, str):
                content_text = msg.content
            elif msg.content:
                content_text = extract_prompt_text([msg])
                if content_text.startswith("assistant: "):
                    content_text = content_text[len("assistant: ") :].lstrip()

            if descriptions:
                desc_text = "\n".join(descriptions)
                if content_text:
                    content_text = f"{content_text}\n\n{desc_text}"
                else:
                    content_text = desc_text

            if kept_in_msg:
                # If we have both text and kept tool calls, split them if requested or
                # to follow specific backend conventions (like Gemini surgical downgrade).
                if content_text:
                    downgraded.append(
                        ChatMessage(
                            role="assistant",
                            content=content_text,
                            reasoning_content=msg.reasoning_content,
                            name=msg.name,
                        )
                    )
                    downgraded.append(
                        ChatMessage(
                            role="assistant",
                            content=None,
                            tool_calls=kept_in_msg,
                            name=msg.name,
                        )
                    )
                else:
                    downgraded.append(
                        ChatMessage(
                            role="assistant",
                            content=None,
                            tool_calls=kept_in_msg,
                            reasoning_content=msg.reasoning_content,
                            name=msg.name,
                        )
                    )
            else:
                # All tool calls in this message were stringified.
                downgraded.append(
                    ChatMessage(
                        role="assistant",
                        content=content_text or None,
                        reasoning_content=msg.reasoning_content,
                        name=msg.name,
                    )
                )
            continue

        # 4. Handle Tool Results
        if msg.role == "tool":
            if msg.tool_call_id and msg.tool_call_id in kept_tool_call_ids:
                downgraded.append(
                    ChatMessage(
                        role="tool",
                        content=msg.content,
                        tool_call_id=msg.tool_call_id,
                        name=msg.name,
                        metadata=msg.metadata.copy() if msg.metadata else None,
                    )
                )
                continue

            convertible_tool_message_seen += 1
            if convertible_tool_message_seen <= convertible_tool_message_skip_before:
                continue

            tool_text = extract_prompt_text([msg])
            if tool_text.startswith("tool:"):
                tool_text = tool_text[len("tool:") :].lstrip()

            if len(tool_text) > max_tool_result_chars:
                tool_text = tool_text[:max_tool_result_chars] + "... (truncated)"

            prefix = "Tool result"
            if include_ids and msg.tool_call_id:
                prefix = f"{prefix} (tool_call_id={msg.tool_call_id})"

            downgraded.append(
                ChatMessage(
                    role="user",
                    content=f"{prefix}: {tool_text}",
                )
            )
            continue

        # 5. Keep other messages as-is
        downgraded.append(
            ChatMessage(
                role=msg.role,
                content=msg.content,
                reasoning_content=msg.reasoning_content,
                name=msg.name,
                metadata=msg.metadata.copy() if msg.metadata else None,
            )
        )

    return downgraded
