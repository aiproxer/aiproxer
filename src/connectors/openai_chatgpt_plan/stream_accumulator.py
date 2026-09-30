"""SIWC stream accumulation requiring response.completed terminal success."""

from __future__ import annotations

import logging
from collections.abc import AsyncIterator
from typing import Any, cast

from src.connectors.gemini_base.response_accumulator import StreamingResponseAccumulator
from src.core.common.exceptions import BackendError
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.interfaces.response_processor_interface import ProcessedResponse

logger = logging.getLogger(__name__)


class ChatGPTPlanStreamError(BackendError):
    """Terminal non-success SIWC stream outcome (failed/incomplete/missing completed)."""

    def __init__(
        self,
        message: str,
        *,
        terminal_event: str,
        details: dict[str, Any] | None = None,
        status_code: int = 502,
        **kwargs: Any,
    ) -> None:
        merged = dict(details or {})
        merged.setdefault("terminal_event", terminal_event)
        super().__init__(
            message,
            backend_name="openai-chatgpt-plan",
            details=merged,
            status_code=status_code,
            code=terminal_event,
            **kwargs,
        )
        self.terminal_event = terminal_event


class ChatGPTPlanStreamAccumulator:
    """Accumulate a forced upstream Responses SSE stream for non-streaming clients.

    Success is emitted only after a completed terminal (``response.completed`` /
    ``response.done`` mapped to finish_reason stop). ``response.failed`` and
    ``response.incomplete`` (and streams that end early) raise
    :class:`ChatGPTPlanStreamError` instead of synthesizing success.
    """

    def __init__(self, backend_type: str = "openai-chatgpt-plan") -> None:
        self.backend_type = backend_type
        self._inner = StreamingResponseAccumulator(backend_type=backend_type)

    async def accumulate(
        self, streaming_response: StreamingResponseEnvelope
    ) -> ResponseEnvelope:
        saw_completed = False
        terminal_error: dict[str, Any] | None = None
        terminal_event = "response.failed"

        if streaming_response.content is None:
            raise ChatGPTPlanStreamError(
                "SIWC upstream stream produced no content before completion.",
                terminal_event="missing_response_completed",
                details={"reason": "empty_stream"},
            )

        source = streaming_response.content

        async def _watched() -> AsyncIterator[ProcessedResponse]:
            nonlocal saw_completed, terminal_error, terminal_event
            async for chunk in source:
                event, err = _classify_chunk(chunk)
                if event == "response.completed":
                    saw_completed = True
                elif event in {"response.failed", "response.incomplete"}:
                    terminal_event = event
                    terminal_error = err or {"message": f"SIWC stream {event}"}
                yield cast(ProcessedResponse, chunk)

        watched_content = cast(AsyncIterator[ProcessedResponse], _watched())
        watched_envelope = StreamingResponseEnvelope(
            content=watched_content,
            media_type=streaming_response.media_type,
            headers=streaming_response.headers,
            cancel_callback=streaming_response.cancel_callback,
            status_code=streaming_response.status_code,
        )
        result = await self._inner.accumulate(watched_envelope)

        if terminal_error is not None:
            message = str(
                terminal_error.get("message")
                or f"SIWC upstream stream ended with {terminal_event}."
            )
            raise ChatGPTPlanStreamError(
                message,
                terminal_event=terminal_event,
                details={
                    "provider_error": terminal_error,
                    "terminal_event": terminal_event,
                },
                status_code=502 if terminal_event == "response.failed" else 400,
            )

        content = result.content
        if isinstance(content, dict) and content.get("error"):
            err = content.get("error")
            err_dict = err if isinstance(err, dict) else {"message": str(err)}
            code = str(err_dict.get("code") or "")
            event = (
                "response.incomplete"
                if code == "response_incomplete"
                else "response.failed"
            )
            raise ChatGPTPlanStreamError(
                str(err_dict.get("message") or "SIWC upstream stream failed."),
                terminal_event=event,
                details={"provider_error": err_dict, "terminal_event": event},
                status_code=400 if event == "response.incomplete" else 502,
            )

        if not saw_completed:
            raise ChatGPTPlanStreamError(
                "SIWC upstream stream ended before response.completed.",
                terminal_event="missing_response_completed",
                details={"reason": "missing_terminal_completed"},
            )

        return result


def _classify_chunk(chunk: Any) -> tuple[str | None, dict[str, Any] | None]:
    data = _chunk_as_mapping(chunk)
    if data is None:
        return None, None

    err = data.get("error")
    if isinstance(err, dict):
        code = str(err.get("code") or "")
        if code == "response_incomplete":
            return "response.incomplete", dict(err)
        return "response.failed", dict(err)

    choices = data.get("choices")
    if isinstance(choices, list) and choices:
        first = choices[0] if isinstance(choices[0], dict) else {}
        finish = first.get("finish_reason")
        if finish == "stop" or finish == "tool_calls":
            return "response.completed", None
        if finish == "error":
            return "response.failed", {"message": "stream finish_reason=error"}

    return None, None


def _chunk_as_mapping(chunk: Any) -> dict[str, Any] | None:
    content = getattr(chunk, "content", chunk)
    if hasattr(content, "model_dump") and not isinstance(content, dict):
        try:
            dumped = content.model_dump(exclude_none=False)
        except TypeError:
            dumped = content.model_dump()
        return dumped if isinstance(dumped, dict) else None
    if isinstance(content, dict):
        return content
    return None
