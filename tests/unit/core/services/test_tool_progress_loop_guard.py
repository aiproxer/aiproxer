from src.core.domain.chat import ChatMessage, ChatRequest, FunctionCall, ToolCall
from src.core.domain.tool_progress_loop import (
    DEFAULT_TOOL_PROGRESS_LOOP_STEERING_MESSAGE,
    ToolProgressLoopAction,
)
from src.core.services.tool_progress_loop_guard import ToolProgressLoopGuard

_DEFAULT_READ_ARGS = '{"filePath":"var/logs/proxy.log","limit":20}'


def _request_with_tool_result(output: str, *, tool_name: str = "read") -> ChatRequest:
    return _request_with_repeated_turns([output], tool_name=tool_name)


def _request_with_repeated_turns(
    outputs: list[str],
    *,
    tool_name: str = "read",
    arguments: str = _DEFAULT_READ_ARGS,
) -> ChatRequest:
    messages: list[ChatMessage] = [
        ChatMessage(role="user", content="inspect logs"),
    ]
    for idx, output in enumerate(outputs, start=1):
        call_id = f"call_{idx}"
        messages.append(
            ChatMessage(
                role="assistant",
                content=None,
                tool_calls=[
                    ToolCall(
                        id=call_id,
                        function=FunctionCall(name=tool_name, arguments=arguments),
                    )
                ],
            )
        )
        messages.append(
            ChatMessage(role="tool", content=output, tool_call_id=call_id),
        )
    return ChatRequest(model="gpt-4", messages=messages)


async def test_guard_blocks_repeated_same_tool_output() -> None:
    guard = ToolProgressLoopGuard(
        max_repeated_tool_output=3,
        action_mode="error",
    )

    for count in range(1, 3):
        decision = await guard.evaluate_request(
            session_id="stable-session",
            request=_request_with_repeated_turns(["same output"] * count),
        )
        assert decision.action == ToolProgressLoopAction.ALLOW

    decision = await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_repeated_turns(["same output"] * 3),
    )

    assert decision.action == ToolProgressLoopAction.BLOCK
    assert decision.repeated_output_count == 3


async def test_guard_does_not_count_identical_http_retries() -> None:
    guard = ToolProgressLoopGuard(
        max_repeated_tool_call_signature=3,
        max_repeated_tool_output=3,
        action_mode="steer_then_error",
    )
    request = _request_with_tool_result("same output")

    for _ in range(8):
        decision = await guard.evaluate_request(
            session_id="stable-session",
            request=request,
        )
        assert decision.action == ToolProgressLoopAction.ALLOW
        assert decision.repeated_call_count <= 1
        assert decision.repeated_output_count <= 1


async def test_guard_blocks_repeated_same_tool_parameters() -> None:
    guard = ToolProgressLoopGuard(
        max_repeated_tool_call_signature=3,
        action_mode="error",
    )

    for _ in range(2):
        decision = await guard.evaluate_request(
            session_id="stable-session",
            request=_request_with_tool_result(f"output {_}"),
        )
        assert decision.action == ToolProgressLoopAction.ALLOW

    decision = await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_tool_result("different output"),
    )

    assert decision.action == ToolProgressLoopAction.BLOCK
    assert decision.repeated_call_count == 3


async def test_guard_blocks_consecutive_tool_followups_even_when_values_change() -> (
    None
):
    guard = ToolProgressLoopGuard(
        max_consecutive_tool_followups=3,
        max_repeated_tool_call_signature=99,
        max_repeated_tool_output=99,
        action_mode="error",
    )

    for idx in range(2):
        decision = await guard.evaluate_request(
            session_id="stable-session",
            request=_request_with_tool_result(f"output {idx}"),
        )
        assert decision.action == ToolProgressLoopAction.ALLOW

    decision = await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_tool_result("output 3"),
    )

    assert decision.action == ToolProgressLoopAction.BLOCK
    assert decision.reason == "consecutive_tool_followups"


async def test_guard_resets_on_new_user_message_after_tool_result() -> None:
    guard = ToolProgressLoopGuard(max_consecutive_tool_followups=2)

    assert (
        await guard.evaluate_request(
            session_id="stable-session",
            request=_request_with_tool_result("first"),
        )
    ).allow

    new_user_request = ChatRequest(
        model="gpt-4",
        messages=[
            *_request_with_tool_result("first").messages,
            ChatMessage(role="user", content="stop and summarize"),
        ],
    )

    assert (
        await guard.evaluate_request(
            session_id="stable-session",
            request=new_user_request,
        )
    ).allow
    assert (
        await guard.evaluate_request(
            session_id="stable-session",
            request=_request_with_tool_result("second"),
        )
    ).allow


async def test_guard_finds_assistant_tool_calls_when_last_assistant_has_none() -> None:
    guard = ToolProgressLoopGuard()
    request = ChatRequest(
        model="gpt-4",
        messages=[
            ChatMessage(role="user", content="inspect logs"),
            ChatMessage(
                role="assistant",
                content=None,
                tool_calls=[
                    ToolCall(
                        id="call_1",
                        function=FunctionCall(
                            name="read", arguments='{"filePath":"one.log"}'
                        ),
                    )
                ],
            ),
            ChatMessage(role="tool", content="result 1", tool_call_id="call_1"),
            ChatMessage(role="assistant", content="thinking...", tool_calls=None),
        ],
    )

    decision = await guard.evaluate_request(
        session_id="stable-session", request=request
    )

    assert decision.repeated_call_count == 1


async def test_guard_counts_only_latest_tool_result_batch() -> None:
    guard = ToolProgressLoopGuard(
        max_repeated_tool_output=3,
        action_mode="error",
    )
    prefix = [
        ChatMessage(role="user", content="inspect logs"),
        ChatMessage(
            role="assistant",
            content=None,
            tool_calls=[
                ToolCall(
                    id="call_1",
                    function=FunctionCall(
                        name="read", arguments='{"filePath":"one.log"}'
                    ),
                )
            ],
        ),
        ChatMessage(role="tool", content="old repeated", tool_call_id="call_1"),
    ]

    async def _evaluate(latest_count: int) -> ToolProgressLoopAction:
        messages = list(prefix)
        for idx in range(latest_count):
            call_id = f"call_latest_{idx}"
            messages.append(
                ChatMessage(
                    role="assistant",
                    content=None,
                    tool_calls=[
                        ToolCall(
                            id=call_id,
                            function=FunctionCall(
                                name="read", arguments='{"filePath":"two.log"}'
                            ),
                        )
                    ],
                )
            )
            messages.append(
                ChatMessage(role="tool", content="latest", tool_call_id=call_id)
            )
        request = ChatRequest(model="gpt-4", messages=messages)
        return (
            await guard.evaluate_request(session_id="stable-session", request=request)
        ).action

    assert await _evaluate(1) == ToolProgressLoopAction.ALLOW
    assert await _evaluate(2) == ToolProgressLoopAction.ALLOW
    assert await _evaluate(3) == ToolProgressLoopAction.BLOCK


async def test_guard_does_not_block_different_calls_same_output() -> None:
    """Different tool calls producing same output text should not trigger repeated_tool_output."""
    guard = ToolProgressLoopGuard(
        max_repeated_tool_output=3,
        max_repeated_tool_call_signature=99,
        max_consecutive_tool_followups=99,
    )

    for idx in range(5):
        request = ChatRequest(
            model="gpt-4",
            messages=[
                ChatMessage(role="user", content="find files"),
                ChatMessage(
                    role="assistant",
                    content=None,
                    tool_calls=[
                        ToolCall(
                            id=f"call_{idx}",
                            function=FunctionCall(
                                name="glob",
                                arguments=f'{{"pattern":"*.py{idx}"}}',
                            ),
                        )
                    ],
                ),
                ChatMessage(
                    role="tool",
                    content="No files found",
                    tool_call_id=f"call_{idx}",
                ),
            ],
        )
        decision = await guard.evaluate_request(
            session_id="stable-session", request=request
        )
        assert decision.action == ToolProgressLoopAction.ALLOW, (
            f"Request {idx} should be allowed (different tool call args), "
            f"got {decision.action} reason={decision.reason}"
        )


async def test_guard_still_blocks_same_call_same_output() -> None:
    """Same tool call + same output should still block (regression check)."""
    guard = ToolProgressLoopGuard(
        max_repeated_tool_output=3,
        action_mode="error",
    )

    for count in range(1, 3):
        decision = await guard.evaluate_request(
            session_id="stable-session",
            request=_request_with_repeated_turns(["same output"] * count),
        )
        assert decision.action == ToolProgressLoopAction.ALLOW

    decision = await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_repeated_turns(["same output"] * 3),
    )
    assert decision.action == ToolProgressLoopAction.BLOCK
    assert decision.repeated_output_count == 3


async def test_guard_falls_back_when_call_output_count_mismatch() -> None:
    """If call count != output count, use output_hash-only key (no crash)."""
    guard = ToolProgressLoopGuard(
        max_repeated_tool_output=5,
        max_repeated_tool_call_signature=99,
        max_consecutive_tool_followups=99,
    )

    request = ChatRequest(
        model="gpt-4",
        messages=[
            ChatMessage(role="user", content="find files"),
            ChatMessage(
                role="assistant",
                content=None,
                tool_calls=[
                    ToolCall(
                        id="call_a",
                        function=FunctionCall(
                            name="glob", arguments='{"pattern":"*.py"}'
                        ),
                    ),
                    ToolCall(
                        id="call_b",
                        function=FunctionCall(
                            name="glob", arguments='{"pattern":"*.ts"}'
                        ),
                    ),
                ],
            ),
            ChatMessage(role="tool", content="No files found", tool_call_id="call_a"),
        ],
    )

    for _ in range(4):
        decision = await guard.evaluate_request(
            session_id="stable-session", request=request
        )
        assert decision.action == ToolProgressLoopAction.ALLOW


async def test_guard_default_action_mode_steers_on_first_loop() -> None:
    guard = ToolProgressLoopGuard(max_repeated_tool_output=3)

    for count in range(1, 3):
        assert (
            await guard.evaluate_request(
                session_id="stable-session",
                request=_request_with_repeated_turns(["same output"] * count),
            )
        ).action == ToolProgressLoopAction.ALLOW

    decision = await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_repeated_turns(["same output"] * 3),
    )

    assert decision.action == ToolProgressLoopAction.STEER


async def test_guard_error_action_mode_blocks_on_loop() -> None:
    guard = ToolProgressLoopGuard(
        max_repeated_tool_output=3,
        action_mode="error",
    )

    for count in range(1, 3):
        assert (
            await guard.evaluate_request(
                session_id="stable-session",
                request=_request_with_repeated_turns(["same output"] * count),
            )
        ).action == ToolProgressLoopAction.ALLOW

    decision = await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_repeated_turns(["same output"] * 3),
    )

    assert decision.action == ToolProgressLoopAction.BLOCK


async def test_guard_steer_then_error_returns_steer_on_first_loop() -> None:
    guard = ToolProgressLoopGuard(
        max_repeated_tool_output=3,
        action_mode="steer_then_error",
    )

    for count in range(1, 3):
        assert (
            await guard.evaluate_request(
                session_id="stable-session",
                request=_request_with_repeated_turns(["same output"] * count),
            )
        ).action == ToolProgressLoopAction.ALLOW

    decision = await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_repeated_turns(["same output"] * 3),
    )

    assert decision.action == ToolProgressLoopAction.STEER
    assert decision.reason == "repeated_tool_output"
    assert decision.steering_message == DEFAULT_TOOL_PROGRESS_LOOP_STEERING_MESSAGE


async def test_guard_steer_then_error_uses_custom_steering_message() -> None:
    custom_message = "Stop looping and change strategy."
    guard = ToolProgressLoopGuard(
        max_repeated_tool_output=2,
        action_mode="steer_then_error",
        steering_message=custom_message,
    )

    await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_repeated_turns(["same output"]),
    )
    decision = await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_repeated_turns(["same output", "same output"]),
    )

    assert decision.action == ToolProgressLoopAction.STEER
    assert decision.steering_message == custom_message


def test_default_steering_message_warns_repeat_will_stop_session() -> None:
    lowered = DEFAULT_TOOL_PROGRESS_LOOP_STEERING_MESSAGE.lower()
    assert "same tool call" in lowered
    assert "same arguments" in lowered
    assert "stop" in lowered


async def test_guard_steer_then_error_blocks_repeat_same_call_after_steer() -> None:
    guard = ToolProgressLoopGuard(
        max_repeated_tool_output=3,
        action_mode="steer_then_error",
    )

    for count in range(1, 3):
        assert (
            await guard.evaluate_request(
                session_id="stable-session",
                request=_request_with_repeated_turns(["same output"] * count),
            )
        ).action == ToolProgressLoopAction.ALLOW

    steered_request = _request_with_repeated_turns(["same output"] * 3)
    assert (
        await guard.evaluate_request(
            session_id="stable-session",
            request=steered_request,
        )
    ).action == ToolProgressLoopAction.STEER

    retry = await guard.evaluate_request(
        session_id="stable-session",
        request=steered_request,
    )
    assert retry.action == ToolProgressLoopAction.ALLOW

    decision = await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_repeated_turns(["same output"] * 4),
    )

    assert decision.action == ToolProgressLoopAction.BLOCK
    assert decision.reason == "repeated_tool_call_after_steer"


async def test_guard_block_after_steer_is_one_shot_not_sticky() -> None:
    """After BLOCK, the same retry must not keep returning 409 forever."""
    guard = ToolProgressLoopGuard(
        max_repeated_tool_output=3,
        max_repeated_tool_call_signature=99,
        max_consecutive_tool_followups=99,
        action_mode="steer_then_error",
    )

    for count in range(1, 3):
        assert (
            await guard.evaluate_request(
                session_id="stable-session",
                request=_request_with_repeated_turns(["same output"] * count),
            )
        ).action == ToolProgressLoopAction.ALLOW

    assert (
        await guard.evaluate_request(
            session_id="stable-session",
            request=_request_with_repeated_turns(["same output"] * 3),
        )
    ).action == ToolProgressLoopAction.STEER

    fourth_turn = _request_with_repeated_turns(["same output"] * 4)
    assert (
        await guard.evaluate_request(session_id="stable-session", request=fourth_turn)
    ).action == ToolProgressLoopAction.BLOCK

    # One-shot block: session counters/pending steer are cleared so retries recover.
    retry = await guard.evaluate_request(
        session_id="stable-session", request=fourth_turn
    )
    assert retry.action == ToolProgressLoopAction.ALLOW


async def test_guard_steer_on_consecutive_resets_counter_for_changed_call() -> None:
    """Changing tool call after consecutive STEER must not immediately re-steer."""
    guard = ToolProgressLoopGuard(
        max_consecutive_tool_followups=3,
        max_repeated_tool_call_signature=99,
        max_repeated_tool_output=99,
        action_mode="steer_then_error",
    )

    for idx in range(2):
        assert (
            await guard.evaluate_request(
                session_id="stable-session",
                request=_request_with_tool_result(f"output {idx}", tool_name="read"),
            )
        ).action == ToolProgressLoopAction.ALLOW

    steer = await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_tool_result("output 2", tool_name="read"),
    )
    assert steer.action == ToolProgressLoopAction.STEER
    assert steer.reason == "consecutive_tool_followups"

    changed_call = ChatRequest(
        model="gpt-4",
        messages=[
            ChatMessage(role="user", content="inspect logs"),
            ChatMessage(
                role="assistant",
                content=None,
                tool_calls=[
                    ToolCall(
                        id="call_2",
                        function=FunctionCall(
                            name="read",
                            arguments='{"filePath":"var/logs/other.log","limit":20}',
                        ),
                    )
                ],
            ),
            ChatMessage(role="tool", content="different output", tool_call_id="call_2"),
        ],
    )
    after_steer = await guard.evaluate_request(
        session_id="stable-session",
        request=changed_call,
    )
    assert after_steer.action == ToolProgressLoopAction.ALLOW


async def test_guard_steer_then_error_block_preserves_steering_metadata() -> None:
    guard = ToolProgressLoopGuard(
        max_repeated_tool_output=3,
        action_mode="steer_then_error",
    )

    for count in range(1, 3):
        assert (
            await guard.evaluate_request(
                session_id="stable-session",
                request=_request_with_repeated_turns(["same output"] * count),
            )
        ).action == ToolProgressLoopAction.ALLOW

    steer_decision = await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_repeated_turns(["same output"] * 3),
    )
    assert steer_decision.action == ToolProgressLoopAction.STEER
    assert steer_decision.reason == "repeated_tool_output"
    assert steer_decision.score == 3
    assert steer_decision.repeated_output_count == 3

    block_decision = await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_repeated_turns(["same output"] * 4),
    )

    assert block_decision.action == ToolProgressLoopAction.BLOCK
    assert block_decision.reason == "repeated_tool_call_after_steer"
    assert block_decision.score == steer_decision.score
    assert block_decision.repeated_call_count == steer_decision.repeated_call_count
    assert block_decision.repeated_output_count == steer_decision.repeated_output_count
    assert block_decision.score >= 3
    assert block_decision.repeated_output_count >= 3


async def test_guard_steer_then_error_clears_pending_when_call_changes() -> None:
    guard = ToolProgressLoopGuard(
        max_repeated_tool_output=3,
        max_repeated_tool_call_signature=99,
        action_mode="steer_then_error",
    )

    for count in range(1, 4):
        await guard.evaluate_request(
            session_id="stable-session",
            request=_request_with_repeated_turns(["same output"] * count),
        )

    changed_call_request = ChatRequest(
        model="gpt-4",
        messages=[
            ChatMessage(role="user", content="inspect logs"),
            ChatMessage(
                role="assistant",
                content=None,
                tool_calls=[
                    ToolCall(
                        id="call_2",
                        function=FunctionCall(
                            name="read",
                            arguments='{"filePath":"var/logs/other.log","limit":20}',
                        ),
                    )
                ],
            ),
            ChatMessage(role="tool", content="different output", tool_call_id="call_2"),
        ],
    )

    decision = await guard.evaluate_request(
        session_id="stable-session",
        request=changed_call_request,
    )

    assert decision.action == ToolProgressLoopAction.ALLOW


async def test_guard_error_mode_never_steers() -> None:
    guard = ToolProgressLoopGuard(
        max_repeated_tool_output=2,
        action_mode="error",
    )

    await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_repeated_turns(["same output"]),
    )
    decision = await guard.evaluate_request(
        session_id="stable-session",
        request=_request_with_repeated_turns(["same output", "same output"]),
    )

    assert decision.action == ToolProgressLoopAction.BLOCK
