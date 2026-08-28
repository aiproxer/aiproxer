"""
Client simulator for replay-based testing.

Replays client requests against a proxy and validates responses.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from typing import Any

import httpx

from src.core.domain.cbor_capture import (
    CaptureDirection,
    CapturedWireEvent,
    CaptureSession,
)
from src.core.simulation.output_utils import safe_bytes_preview
from src.core.simulation.timing_controller import TimingController

logger = logging.getLogger(__name__)


@dataclass
class ContentMismatch:
    """Details of a content mismatch between expected and actual response."""

    sequence: int
    expected_bytes: int
    actual_bytes: int
    expected_preview: str
    actual_preview: str
    difference_type: str  # "length", "content", "missing"


@dataclass
class TimingDeviation:
    """Details of a timing deviation from expected timing."""

    sequence: int
    expected_delay: float
    actual_delay: float
    deviation_ms: float


@dataclass
class ValidationResult:
    """Result of validating a response against captured expectations."""

    success: bool
    content_mismatches: list[ContentMismatch] = field(default_factory=list)
    timing_deviations: list[TimingDeviation] = field(default_factory=list)
    total_expected_bytes: int = 0
    total_actual_bytes: int = 0
    total_chunks: int = 0
    actual_chunks: int = 0

    @property
    def summary(self) -> str:
        """Get a human-readable summary."""
        if self.success:
            return (
                f"Validation passed: {self.actual_chunks} chunks, "
                f"{self.total_actual_bytes} bytes"
            )
        issues = []
        if self.content_mismatches:
            issues.append(f"{len(self.content_mismatches)} content mismatches")
        if self.timing_deviations:
            issues.append(f"{len(self.timing_deviations)} timing deviations")
        return f"Validation failed: {', '.join(issues)}"


class ClientSimulator:
    """Simulates client requests and validates responses.

    This simulator:
    - Replays captured client requests against a target proxy
    - Validates responses against captured expectations
    - Tracks timing deviations and content mismatches
    """

    def __init__(
        self,
        session: CaptureSession,
        proxy_base_url: str = "http://localhost:8000",
        timing_tolerance_ms: float = 100.0,
    ) -> None:
        """Initialize the client simulator.

        Args:
            session: The capture session to replay
            proxy_base_url: Base URL of the proxy to test
            timing_tolerance_ms: Maximum acceptable timing deviation in milliseconds
        """
        self._session = session
        self._proxy_base_url = proxy_base_url.rstrip("/")
        self._timing_tolerance_ms = timing_tolerance_ms
        self._timing = TimingController()
        self._client: httpx.AsyncClient | None = None

    async def __aenter__(self) -> ClientSimulator:
        """Enter async context."""
        self._client = httpx.AsyncClient(timeout=30.0)
        return self

    async def __aexit__(self, *args: Any) -> None:
        """Exit async context."""
        if self._client:
            await self._client.aclose()
            self._client = None

    def _get_request_entries(self) -> list[CapturedWireEvent]:
        """Get inbound request entries from client."""
        return [
            e
            for e in self._session.entries
            if e.direction == CaptureDirection.CLIENT_TO_PROXY
            and not e.metadata.is_stream_start
            and not e.metadata.is_stream_end
            and e.metadata.chunk_index is None
        ]

    def _get_expected_response_entries(
        self, after_sequence: int
    ) -> list[CapturedWireEvent]:
        """Get expected response entries after a request.

        Args:
            after_sequence: The sequence number of the request

        Returns:
            List of expected response entries
        """
        entries = self._session.entries
        response_entries: list[CapturedWireEvent] = []
        collecting = False

        for entry in entries:
            if entry.sequence == after_sequence:
                collecting = True
                continue
            if collecting:
                if entry.direction == CaptureDirection.PROXY_TO_CLIENT:
                    response_entries.append(entry)
                    if entry.metadata.is_stream_end:
                        break
                elif entry.direction == CaptureDirection.CLIENT_TO_PROXY:
                    # Next request started
                    break

        return response_entries

    async def replay_request(
        self, entry: CapturedWireEvent, endpoint: str = "/v1/chat/completions"
    ) -> httpx.Response:
        """Replay a single request.

        Args:
            entry: The captured request entry
            endpoint: The API endpoint to call

        Returns:
            The response from the proxy
        """
        if not self._client:
            raise RuntimeError("Client not initialized. Use async with.")

        url = f"{self._proxy_base_url}{endpoint}"
        headers = {"Content-Type": "application/json"}

        # Add session ID if available
        if entry.metadata.session_id:
            headers["X-Session-ID"] = entry.metadata.session_id

        response = await self._client.post(
            url,
            content=entry.data,
            headers=headers,
        )
        return response

    async def consume_response_stream(
        self,
        response: httpx.Response,
        expected_entries: list[CapturedWireEvent],
    ) -> ValidationResult:
        """Consume a streaming response and validate against expectations.

        Args:
            response: The httpx response
            expected_entries: Expected response entries from capture

        Returns:
            ValidationResult with mismatches and deviations
        """
        content_mismatches: list[ContentMismatch] = []
        timing_deviations: list[TimingDeviation] = []
        actual_chunks: list[bytes] = []

        # Filter expected entries to only data chunks
        expected_chunks = [
            e
            for e in expected_entries
            if e.data
            and not e.metadata.is_stream_start
            and not e.metadata.is_stream_end
        ]

        # Start timing
        if expected_entries:
            self._timing.start(expected_entries[0].timestamp)

        chunk_idx = 0
        try:
            async for chunk in response.aiter_bytes():
                actual_chunks.append(chunk)
                chunk_idx += 1

                if chunk_idx <= len(expected_chunks):
                    expected = expected_chunks[chunk_idx - 1]

                    # Check content match
                    if chunk != expected.data:
                        mismatch = ContentMismatch(
                            sequence=expected.sequence,
                            expected_bytes=len(expected.data),
                            actual_bytes=len(chunk),
                            expected_preview=safe_bytes_preview(
                                expected.data, max_length=100
                            ),
                            actual_preview=safe_bytes_preview(chunk, max_length=100),
                            difference_type=(
                                "length"
                                if len(chunk) != len(expected.data)
                                else "content"
                            ),
                        )
                        content_mismatches.append(mismatch)

                    # Check timing (if we have timing data)
                    if len(expected_chunks) > 1 and chunk_idx > 1:
                        prev_expected = expected_chunks[chunk_idx - 2]
                        expected_delay = expected.timestamp - prev_expected.timestamp
                        actual_delay = self._timing.get_elapsed_time()

                        deviation_ms = abs(actual_delay - expected_delay) * 1000
                        if deviation_ms > self._timing_tolerance_ms:
                            timing_deviations.append(
                                TimingDeviation(
                                    sequence=expected.sequence,
                                    expected_delay=expected_delay,
                                    actual_delay=actual_delay,
                                    deviation_ms=deviation_ms,
                                )
                            )
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Error consuming stream: %s",
                    e,
                    exc_info=True,
                )

        # Check for missing chunks
        if len(actual_chunks) < len(expected_chunks):
            for i in range(len(actual_chunks), len(expected_chunks)):
                expected = expected_chunks[i]
                content_mismatches.append(
                    ContentMismatch(
                        sequence=expected.sequence,
                        expected_bytes=len(expected.data),
                        actual_bytes=0,
                        expected_preview=safe_bytes_preview(
                            expected.data, max_length=100
                        ),
                        actual_preview="",
                        difference_type="missing",
                    )
                )

        total_expected = sum(len(e.data) for e in expected_chunks)
        total_actual = sum(len(c) for c in actual_chunks)

        return ValidationResult(
            success=len(content_mismatches) == 0 and len(timing_deviations) == 0,
            content_mismatches=content_mismatches,
            timing_deviations=timing_deviations,
            total_expected_bytes=total_expected,
            total_actual_bytes=total_actual,
            total_chunks=len(expected_chunks),
            actual_chunks=len(actual_chunks),
        )

    async def validate_response(
        self,
        response: httpx.Response,
        expected_entries: list[CapturedWireEvent],
    ) -> ValidationResult:
        """Validate a non-streaming response against expectations.

        Args:
            response: The httpx response
            expected_entries: Expected response entries from capture

        Returns:
            ValidationResult with mismatches
        """
        content_mismatches: list[ContentMismatch] = []
        actual_data = response.content

        # For non-streaming, expect a single response entry
        if not expected_entries:
            return ValidationResult(
                success=True,
                total_actual_bytes=len(actual_data),
                actual_chunks=1,
            )

        expected = expected_entries[0]
        if actual_data != expected.data:
            content_mismatches.append(
                ContentMismatch(
                    sequence=expected.sequence,
                    expected_bytes=len(expected.data),
                    actual_bytes=len(actual_data),
                    expected_preview=safe_bytes_preview(expected.data, max_length=100),
                    actual_preview=safe_bytes_preview(actual_data, max_length=100),
                    difference_type=(
                        "length"
                        if len(actual_data) != len(expected.data)
                        else "content"
                    ),
                )
            )

        return ValidationResult(
            success=len(content_mismatches) == 0,
            content_mismatches=content_mismatches,
            total_expected_bytes=len(expected.data),
            total_actual_bytes=len(actual_data),
            total_chunks=1,
            actual_chunks=1,
        )

    async def replay_session(
        self, endpoint: str = "/v1/chat/completions"
    ) -> list[ValidationResult]:
        """Replay all requests in the session.

        Args:
            endpoint: The API endpoint to call

        Returns:
            List of validation results for each request
        """
        results: list[ValidationResult] = []
        requests = self._get_request_entries()

        for req in requests:
            try:
                response = await self.replay_request(req, endpoint)
                expected = self._get_expected_response_entries(req.sequence)

                # Check if streaming based on expected entries
                is_streaming = any(e.metadata.is_stream_start for e in expected)

                if is_streaming:
                    result = await self.consume_response_stream(response, expected)
                else:
                    result = await self.validate_response(response, expected)

                results.append(result)
            except Exception as e:
                if logger.isEnabledFor(logging.ERROR):
                    logger.error(
                        "Error replaying request %s: %s",
                        req.sequence,
                        e,
                        exc_info=True,
                    )
                results.append(
                    ValidationResult(
                        success=False,
                        content_mismatches=[
                            ContentMismatch(
                                sequence=req.sequence,
                                expected_bytes=0,
                                actual_bytes=0,
                                expected_preview="",
                                actual_preview=str(e),
                                difference_type="error",
                            )
                        ],
                    )
                )

        return results
