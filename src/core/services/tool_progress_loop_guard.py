"""Session-level guard for cross-request tool-progress loops."""

from __future__ import annotations

from collections import OrderedDict
from dataclasses import dataclass, field
from typing import Any

from src.core.domain.chat import ChatMessage, ChatRequest
from src.core.domain.tool_progress_loop import (
    DEFAULT_TOOL_PROGRESS_LOOP_GUARD_ACTION,
    DEFAULT_TOOL_PROGRESS_LOOP_STEERING_MESSAGE,
    ToolProgressLoopAction,
    ToolProgressLoopDecision,
    ToolProgressLoopGuardActionMode,
    fingerprint_tool_call,
    fingerprint_tool_output,
)
from src.core.interfaces.tool_progress_loop_guard_interface import (
    IToolProgressLoopGuard,
)


@dataclass(slots=True)
class _SessionLoopState:
    consecutive_tool_followups: int = 0
    call_counts: OrderedDict[str, int] = field(default_factory=OrderedDict)
    output_counts: OrderedDict[str, int] = field(default_factory=OrderedDict)
    pending_steer_call_fingerprint: str | None = None
    pending_steer_reason: str | None = None
    pending_steer_score: int = 0
    pending_steer_repeated_call_count: int = 0
    pending_steer_repeated_output_count: int = 0
    last_counted_identity: str | None = None


class ToolProgressLoopGuard(IToolProgressLoopGuard):
    """Detects repeated tool calls/results before dispatching another backend request."""

    def __init__(
        self,
        *,
        max_consecutive_tool_followups: int = 50,
        max_repeated_tool_call_signature: int = 7,
        max_repeated_tool_output: int = 7,
        max_counts_per_session: int = 256,
        max_cached_sessions: int = 1000,
        enabled: bool = True,
        action_mode: ToolProgressLoopGuardActionMode = DEFAULT_TOOL_PROGRESS_LOOP_GUARD_ACTION,
        steering_message: str | None = None,
    ) -> None:
        self._max_consecutive_tool_followups = max(1, max_consecutive_tool_followups)
        self._max_repeated_tool_call_signature = max(
            1, max_repeated_tool_call_signature
        )
        self._max_repeated_tool_output = max(1, max_repeated_tool_output)
        self._max_counts_per_session = max(1, max_counts_per_session)
        self._max_cached_sessions = max(1, max_cached_sessions)
        self._enabled = enabled
        self._action_mode = action_mode
        self._steering_message = (
            steering_message or DEFAULT_TOOL_PROGRESS_LOOP_STEERING_MESSAGE
        )
        self._sessions: OrderedDict[str, _SessionLoopState] = OrderedDict()

    async def evaluate_request(
        self,
        *,
        session_id: str,
        request: ChatRequest,
    ) -> ToolProgressLoopDecision:
        if not self._enabled:
            return ToolProgressLoopDecision(action=ToolProgressLoopAction.ALLOW)

        state = self._state_for(session_id)
        messages = list(getattr(request, "messages", []) or [])
        if self._has_new_user_message_after_last_tool(messages):
            state = _SessionLoopState()
            self._sessions[session_id] = state
            return ToolProgressLoopDecision(action=ToolProgressLoopAction.ALLOW)

        tool_outputs = self._extract_tool_outputs(messages)
        assistant_tool_calls = self._extract_recent_assistant_tool_calls(messages)
        current_call_fingerprint = self._fingerprint_tool_calls(assistant_tool_calls)
        turn_identity = self._turn_identity(
            message_count=len(messages),
            call_fingerprint=current_call_fingerprint,
            tool_outputs=tool_outputs,
        )
        if (
            tool_outputs
            and turn_identity
            and turn_identity == state.last_counted_identity
        ):
            return ToolProgressLoopDecision(
                action=ToolProgressLoopAction.ALLOW,
                score=state.consecutive_tool_followups,
                repeated_call_count=max(state.call_counts.values(), default=0),
                repeated_output_count=max(state.output_counts.values(), default=0),
            )

        if state.pending_steer_call_fingerprint is not None:
            if (
                current_call_fingerprint
                and current_call_fingerprint == state.pending_steer_call_fingerprint
            ):
                decision = ToolProgressLoopDecision(
                    action=ToolProgressLoopAction.BLOCK,
                    reason="repeated_tool_call_after_steer",
                    score=state.pending_steer_score,
                    repeated_call_count=state.pending_steer_repeated_call_count,
                    repeated_output_count=state.pending_steer_repeated_output_count,
                )
                # One-shot block: clear sticky pending-steer state so the session
                # can recover on subsequent requests instead of returning 409 forever.
                self._sessions[session_id] = _SessionLoopState()
                return decision
            self._clear_pending_steer(state)

        if not tool_outputs:
            state.consecutive_tool_followups = 0
            return ToolProgressLoopDecision(action=ToolProgressLoopAction.ALLOW)

        state.consecutive_tool_followups += 1
        max_call_count = 0
        for tool_call in assistant_tool_calls:
            fp = fingerprint_tool_call(tool_call)
            key = fp.arguments_hash
            state.call_counts[key] = state.call_counts.get(key, 0) + 1
            max_call_count = max(max_call_count, state.call_counts[key])
            self._evict_oldest_if_needed(state.call_counts)

        call_args_hashes = [
            fingerprint_tool_call(tc).arguments_hash for tc in assistant_tool_calls
        ]
        can_pair = len(call_args_hashes) == len(tool_outputs) and len(tool_outputs) > 0

        max_output_count = 0
        for idx, output in enumerate(tool_outputs):
            output_fp = fingerprint_tool_output(output)
            call_hash = call_args_hashes[idx] if can_pair else ""
            key = (
                f"{call_hash}::{output_fp.output_hash}"
                if can_pair
                else output_fp.output_hash
            )
            state.output_counts[key] = state.output_counts.get(key, 0) + 1
            max_output_count = max(max_output_count, state.output_counts[key])
            self._evict_oldest_if_needed(state.output_counts)

        state.last_counted_identity = turn_identity or None

        if max_output_count >= self._max_repeated_tool_output:
            return self._resolve_loop_detection(
                state=state,
                reason="repeated_tool_output",
                score=max_output_count,
                max_call_count=max_call_count,
                max_output_count=max_output_count,
                current_call_fingerprint=current_call_fingerprint,
            )

        if max_call_count >= self._max_repeated_tool_call_signature:
            return self._resolve_loop_detection(
                state=state,
                reason="repeated_tool_call",
                score=max_call_count,
                max_call_count=max_call_count,
                max_output_count=max_output_count,
                current_call_fingerprint=current_call_fingerprint,
            )

        if state.consecutive_tool_followups >= self._max_consecutive_tool_followups:
            return self._resolve_loop_detection(
                state=state,
                reason="consecutive_tool_followups",
                score=state.consecutive_tool_followups,
                max_call_count=max_call_count,
                max_output_count=max_output_count,
                current_call_fingerprint=current_call_fingerprint,
            )

        return ToolProgressLoopDecision(
            action=ToolProgressLoopAction.ALLOW,
            score=state.consecutive_tool_followups,
            repeated_call_count=max_call_count,
            repeated_output_count=max_output_count,
        )

    def _resolve_loop_detection(
        self,
        *,
        state: _SessionLoopState,
        reason: str,
        score: int,
        max_call_count: int,
        max_output_count: int,
        current_call_fingerprint: str,
    ) -> ToolProgressLoopDecision:
        if self._action_mode == "error":
            return ToolProgressLoopDecision(
                action=ToolProgressLoopAction.BLOCK,
                reason=reason,
                score=score,
                repeated_call_count=max_call_count,
                repeated_output_count=max_output_count,
            )

        state.pending_steer_call_fingerprint = current_call_fingerprint or None
        state.pending_steer_reason = reason
        state.pending_steer_score = score
        state.pending_steer_repeated_call_count = max_call_count
        state.pending_steer_repeated_output_count = max_output_count
        # Back off consecutive counter after steering so a productive changed
        # tool call does not immediately re-trigger consecutive_tool_followups.
        state.consecutive_tool_followups = 0
        return ToolProgressLoopDecision(
            action=ToolProgressLoopAction.STEER,
            reason=reason,
            score=score,
            repeated_call_count=max_call_count,
            repeated_output_count=max_output_count,
            steering_message=self._steering_message,
        )

    @staticmethod
    def _clear_pending_steer(state: _SessionLoopState) -> None:
        state.pending_steer_call_fingerprint = None
        state.pending_steer_reason = None
        state.pending_steer_score = 0
        state.pending_steer_repeated_call_count = 0
        state.pending_steer_repeated_output_count = 0

    def _state_for(self, session_id: str) -> _SessionLoopState:
        state = self._sessions.get(session_id)
        if state is not None:
            self._sessions.move_to_end(session_id)
            return state
        while len(self._sessions) >= self._max_cached_sessions:
            self._sessions.popitem(last=False)
        state = _SessionLoopState()
        self._sessions[session_id] = state
        return state

    def _evict_oldest_if_needed(self, counter: OrderedDict[str, int]) -> None:
        while len(counter) > self._max_counts_per_session:
            counter.popitem(last=False)

    @staticmethod
    def _turn_identity(
        *,
        message_count: int,
        call_fingerprint: str,
        tool_outputs: list[Any],
    ) -> str:
        if not call_fingerprint and not tool_outputs:
            return ""
        output_part = "|".join(
            fingerprint_tool_output(output).output_hash for output in tool_outputs
        )
        return f"{message_count}::{call_fingerprint}::{output_part}"

    @staticmethod
    def _fingerprint_tool_calls(tool_calls: list[Any]) -> str:
        if not tool_calls:
            return ""
        hashes = sorted(
            fingerprint_tool_call(tool_call).arguments_hash for tool_call in tool_calls
        )
        return "|".join(hashes)

    @staticmethod
    def _has_new_user_message_after_last_tool(messages: list[ChatMessage]) -> bool:
        last_tool_idx = -1
        last_user_idx = -1
        for idx, message in enumerate(messages):
            role = getattr(message, "role", None)
            if role == "tool":
                last_tool_idx = idx
            elif role == "user":
                last_user_idx = idx
        return last_tool_idx >= 0 and last_user_idx > last_tool_idx

    @staticmethod
    def _extract_tool_outputs(messages: list[ChatMessage]) -> list[Any]:
        outputs: list[Any] = []
        for message in reversed(messages):
            if getattr(message, "role", None) == "tool":
                outputs.append(getattr(message, "content", "") or "")
                continue
            if outputs:
                break
        outputs.reverse()
        return outputs

    @staticmethod
    def _extract_recent_assistant_tool_calls(messages: list[ChatMessage]) -> list[Any]:
        for message in reversed(messages):
            if getattr(message, "role", None) != "assistant":
                continue
            tool_calls = getattr(message, "tool_calls", None)
            if tool_calls:
                return list(tool_calls)
        return []
