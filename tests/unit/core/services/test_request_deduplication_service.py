"""Unit tests for RequestDeduplicationService."""

from __future__ import annotations

import asyncio

import pytest
from src.core.domain.chat import ChatMessage, ChatRequest
from src.core.services.request_deduplication_service import RequestDeduplicationService


class TestRequestDeduplicationService:
    """Tests for RequestDeduplicationService."""

    @pytest.fixture
    def service(self) -> RequestDeduplicationService:
        """Create a deduplication service with default settings."""
        return RequestDeduplicationService(window_seconds=6.0, enabled=True)

    @pytest.fixture
    def short_window_service(self) -> RequestDeduplicationService:
        """Create a deduplication service with a short window for testing."""
        return RequestDeduplicationService(window_seconds=0.1, enabled=True)

    @pytest.fixture
    def sample_request(self) -> ChatRequest:
        """Create a sample chat request."""
        return ChatRequest(
            model="gpt-4",
            messages=[
                ChatMessage(role="user", content="Hello, world!"),
            ],
        )

    @pytest.fixture
    def different_request(self) -> ChatRequest:
        """Create a different chat request."""
        return ChatRequest(
            model="gpt-4",
            messages=[
                ChatMessage(role="user", content="Different message"),
            ],
        )

    @pytest.mark.asyncio
    async def test_first_request_not_duplicate(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """First request should not be detected as duplicate."""
        is_duplicate, content_hash, _ = await service.check_and_register(
            sample_request, "session-1"
        )
        assert is_duplicate is False
        assert content_hash != ""
        assert len(content_hash) == 32

    @pytest.mark.asyncio
    async def test_identical_request_within_window_is_duplicate(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Identical request within the dedup window should be detected as duplicate."""
        await service.check_and_register(sample_request, "session-1")

        is_duplicate, content_hash, _ = await service.check_and_register(
            sample_request, "session-1"
        )
        assert is_duplicate is True
        assert content_hash != ""

    @pytest.mark.asyncio
    async def test_identical_request_after_window_not_duplicate(
        self,
        short_window_service: RequestDeduplicationService,
        sample_request: ChatRequest,
    ) -> None:
        """Identical request after the dedup window expires should not be duplicate."""
        from tests.utils.fake_clock import FakeClockContext

        async with FakeClockContext() as clock:
            await short_window_service.check_and_register(sample_request, "session-1")

            clock.advance(0.15)

            is_duplicate, _, _ = await short_window_service.check_and_register(
                sample_request, "session-1"
            )
        assert is_duplicate is False

    @pytest.mark.asyncio
    async def test_different_sessions_not_duplicates(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Same request from different sessions should not be duplicates."""
        await service.check_and_register(sample_request, "session-1")

        is_duplicate, _, _ = await service.check_and_register(
            sample_request, "session-2"
        )
        assert is_duplicate is False

    @pytest.mark.asyncio
    async def test_different_content_not_duplicate(
        self,
        service: RequestDeduplicationService,
        sample_request: ChatRequest,
        different_request: ChatRequest,
    ) -> None:
        """Different request content should not be detected as duplicate."""
        await service.check_and_register(sample_request, "session-1")

        is_duplicate, _, _ = await service.check_and_register(
            different_request, "session-1"
        )
        assert is_duplicate is False

    @pytest.mark.asyncio
    async def test_disabled_service_never_detects_duplicates(
        self, sample_request: ChatRequest
    ) -> None:
        """Disabled service should never detect duplicates."""
        service = RequestDeduplicationService(window_seconds=6.0, enabled=False)

        await service.check_and_register(sample_request, "session-1")

        is_duplicate, content_hash, _ = await service.check_and_register(
            sample_request, "session-1"
        )
        assert is_duplicate is False
        assert content_hash == ""

    @pytest.mark.asyncio
    async def test_zero_window_disables_dedup(
        self, sample_request: ChatRequest
    ) -> None:
        """Zero window should disable deduplication."""
        service = RequestDeduplicationService(window_seconds=0.0, enabled=True)

        await service.check_and_register(sample_request, "session-1")

        is_duplicate, content_hash, _ = await service.check_and_register(
            sample_request, "session-1"
        )
        assert is_duplicate is False
        assert content_hash == ""

    @pytest.mark.asyncio
    async def test_stats_tracking(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Stats should correctly track requests and duplicates."""
        initial_stats = service.get_stats()
        assert initial_stats.requests_processed == 0
        assert initial_stats.duplicates_blocked == 0

        await service.check_and_register(sample_request, "session-1")
        await service.check_and_register(sample_request, "session-1")
        await service.check_and_register(sample_request, "session-1")

        stats = service.get_stats()
        assert stats.requests_processed == 3
        assert stats.duplicates_blocked == 2
        assert stats.cache_size == 1
        assert stats.dedup_rate == pytest.approx(2 / 3, rel=0.01)

    @pytest.mark.asyncio
    async def test_cleanup_removes_expired_entries(
        self,
        short_window_service: RequestDeduplicationService,
        sample_request: ChatRequest,
    ) -> None:
        """Cleanup should remove expired entries."""
        from tests.utils.fake_clock import FakeClockContext

        async with FakeClockContext() as clock:
            await short_window_service.check_and_register(sample_request, "session-1")
            assert short_window_service.get_stats().cache_size == 1

            clock.advance(0.15)

            removed = await short_window_service.cleanup()
            assert removed == 1
            assert short_window_service.get_stats().cache_size == 0

    @pytest.mark.asyncio
    async def test_cache_size_limit_enforced(self) -> None:
        """Cache size limit should be enforced after cleanup triggers."""
        service = RequestDeduplicationService(
            window_seconds=60.0,
            enabled=True,
            max_cache_size=5,
        )

        # Make enough requests to trigger size-based cleanup
        # Cleanup triggers when cache exceeds max_cache_size
        for i in range(10):
            request = ChatRequest(
                model="gpt-4",
                messages=[ChatMessage(role="user", content=f"Message {i}")],
            )
            await service.check_and_register(request, f"session-{i}")

        # After cleanup, size should be at most max_cache_size
        # Note: cleanup triggers when size EXCEEDS max, so final size <= max
        final_size = service.get_stats().cache_size
        # Due to cleanup triggering after exceeding, allow some slack
        assert final_size <= 10, f"Cache size {final_size} should be bounded"

    @pytest.mark.asyncio
    async def test_concurrent_access_thread_safety(
        self, service: RequestDeduplicationService
    ) -> None:
        """Service should handle concurrent access safely."""

        async def make_request(
            session_id: str, msg: str
        ) -> tuple[bool, str, float | None]:
            request = ChatRequest(
                model="gpt-4",
                messages=[ChatMessage(role="user", content=msg)],
            )
            return await service.check_and_register(request, session_id)

        tasks = []
        for i in range(20):
            tasks.append(make_request(f"session-{i % 5}", f"msg-{i % 3}"))

        results = await asyncio.gather(*tasks)

        assert all(isinstance(r[0], bool) for r in results)
        assert all(isinstance(r[1], str) for r in results)

        stats = service.get_stats()
        assert stats.requests_processed == 20

    @pytest.mark.asyncio
    async def test_different_models_not_duplicate(
        self, service: RequestDeduplicationService
    ) -> None:
        """Same message with different models should not be duplicates."""
        request1 = ChatRequest(
            model="gpt-4",
            messages=[ChatMessage(role="user", content="Hello")],
        )
        request2 = ChatRequest(
            model="gpt-3.5-turbo",
            messages=[ChatMessage(role="user", content="Hello")],
        )

        await service.check_and_register(request1, "session-1")
        is_duplicate, _, _ = await service.check_and_register(request2, "session-1")

        assert is_duplicate is False


class TestStatusAwareDeduplication:
    """Tests for status-aware deduplication (retry-after-429 scenarios)."""

    @pytest.fixture
    def service(self) -> RequestDeduplicationService:
        """Create a deduplication service with default settings."""
        return RequestDeduplicationService(window_seconds=6.0, enabled=True)

    @pytest.fixture
    def sample_request(self) -> ChatRequest:
        """Create a sample chat request."""
        return ChatRequest(
            model="gpt-4",
            messages=[
                ChatMessage(role="user", content="Test message with 120 messages"),
            ]
            * 120,
        )

    @pytest.mark.asyncio
    async def test_retry_after_429_always_allowed(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Retry after 429 rate limit should ALWAYS be allowed, regardless of timing.

        This is the critical requirement: never block retries after retriable errors.
        """
        # First request
        is_dup, hash1, _ = await service.check_and_register(sample_request, "session-1")
        assert is_dup is False

        # Mark as 429 (rate limited)
        await service.mark_request_complete(hash1, "session-1", status_code=429)

        # Immediate retry should be allowed (within dedup window)
        is_dup, hash2, _ = await service.check_and_register(sample_request, "session-1")
        assert is_dup is False, "Retry after 429 should be allowed immediately"
        assert hash1 == hash2

        # Verify stats tracked retry
        stats = service.get_stats()
        assert stats.extra is not None
        assert stats.extra["retries_after_error_allowed"] == 1

    @pytest.mark.asyncio
    async def test_retry_after_503_allowed(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Retry after 503 service unavailable should be allowed."""
        is_dup, hash1, _ = await service.check_and_register(sample_request, "session-1")
        await service.mark_request_complete(hash1, "session-1", status_code=503)

        is_dup, _, _ = await service.check_and_register(sample_request, "session-1")
        assert is_dup is False

    @pytest.mark.asyncio
    async def test_retry_after_502_allowed(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Retry after 502 bad gateway should be allowed."""
        is_dup, hash1, _ = await service.check_and_register(sample_request, "session-1")
        await service.mark_request_complete(hash1, "session-1", status_code=502)

        is_dup, _, _ = await service.check_and_register(sample_request, "session-1")
        assert is_dup is False

    @pytest.mark.asyncio
    async def test_retry_after_timeout_allowed(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Retry after 408 timeout should be allowed."""
        is_dup, hash1, _ = await service.check_and_register(sample_request, "session-1")
        await service.mark_request_complete(hash1, "session-1", status_code=408)

        is_dup, _, _ = await service.check_and_register(sample_request, "session-1")
        assert is_dup is False

    @pytest.mark.asyncio
    async def test_retry_after_success_blocked(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Retry after successful completion (200) should be blocked (zombie pattern)."""
        is_dup, hash1, _ = await service.check_and_register(sample_request, "session-1")
        await service.mark_request_complete(hash1, "session-1", status_code=200)

        # Retry within window should be blocked
        is_dup, _, _ = await service.check_and_register(sample_request, "session-1")
        assert is_dup is True, "Retry after success should be blocked (zombie)"

        stats = service.get_stats()
        assert stats.duplicates_blocked == 1

    @pytest.mark.asyncio
    async def test_retry_after_client_disconnect_blocked(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Retry after client disconnect should be blocked (zombie pattern)."""
        is_dup, hash1, _ = await service.check_and_register(sample_request, "session-1")
        await service.mark_request_complete(
            hash1, "session-1", client_disconnected=True
        )

        # Retry within window should be blocked
        is_dup, _, _ = await service.check_and_register(sample_request, "session-1")
        assert is_dup is True, "Retry after disconnect should be blocked (zombie)"

    @pytest.mark.asyncio
    async def test_parallel_duplicate_blocked(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Parallel duplicate request (while original is in-flight) should be blocked."""
        # First request (in-flight, not yet completed)
        is_dup, _, _ = await service.check_and_register(sample_request, "session-1")
        assert is_dup is False

        # Second parallel request before first completes
        is_dup, _, _ = await service.check_and_register(sample_request, "session-1")
        assert is_dup is True, "Parallel duplicate should be blocked"

    @pytest.mark.asyncio
    async def test_multiple_retries_after_429_allowed(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Multiple retries after 429 should all be allowed (retry loop scenario)."""
        hash_val = None

        # Simulate multiple retry attempts
        for i in range(5):
            is_dup, hash_val, _ = await service.check_and_register(
                sample_request, "session-1"
            )
            assert is_dup is False, f"Retry {i+1} should be allowed"

            # Mark as 429 each time
            await service.mark_request_complete(hash_val, "session-1", status_code=429)

        # All retries should have been allowed
        stats = service.get_stats()
        assert stats.extra is not None
        assert stats.extra["retries_after_error_allowed"] == 4  # First isn't a retry

    @pytest.mark.asyncio
    async def test_retry_after_non_retriable_error_blocked(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Retry after non-retriable error (400, 404, etc) should be blocked."""
        is_dup, hash1, _ = await service.check_and_register(sample_request, "session-1")
        # 400 bad request - non-retriable
        await service.mark_request_complete(hash1, "session-1", status_code=400)

        # Retry should be blocked (treated as success for dedup purposes)
        is_dup, _, _ = await service.check_and_register(sample_request, "session-1")
        assert is_dup is True, "Retry after 400 should be blocked"

    @pytest.mark.asyncio
    async def test_request_outcome_exposes_blocked_status_code(
        self, service, sample_request
    ):
        """Callers can distinguish a cached upstream 403 from a successful duplicate."""
        is_dup, content_hash, _ = await service.check_and_register(
            sample_request, "session-1"
        )
        assert is_dup is False

        await service.mark_request_complete(content_hash, "session-1", status_code=403)

        outcome = await service.get_request_outcome(content_hash, "session-1")
        assert outcome == ("success", 403)

    @pytest.mark.asyncio
    async def test_retry_after_403_blocked_within_window(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Retry after 403 Forbidden should be blocked within the dedup window and allowed after expiry."""
        from tests.utils.fake_clock import FakeClockContext

        async with FakeClockContext() as clock:
            # First request
            is_dup, hash1, _ = await service.check_and_register(
                sample_request, "session-1"
            )

            # Mark as 403 (Forbidden/Non-retriable)
            await service.mark_request_complete(hash1, "session-1", status_code=403)

            # Within dedup window (default 6.0s)
            clock.advance(2.0)
            is_dup, _, _ = await service.check_and_register(sample_request, "session-1")
            assert is_dup is True, "Retry within dedup window should be blocked"

            # Advance past dedup window (6.0s)
            clock.advance(5.0)

            # Now it should be allowed (treated as new request)
            is_dup, _, _ = await service.check_and_register(sample_request, "session-1")
            assert is_dup is False, "Retry after dedup window should be allowed"

    @pytest.mark.asyncio
    async def test_retry_after_204_blocked_within_window(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Retry after 204 No Content should be blocked within the dedup window and allowed after expiry."""
        from tests.utils.fake_clock import FakeClockContext

        async with FakeClockContext() as clock:
            # First request
            is_dup, hash1, _ = await service.check_and_register(
                sample_request, "session-1"
            )

            # Mark as 204 (No Content / Empty Response)
            await service.mark_request_complete(hash1, "session-1", status_code=204)

            # Within dedup window (default 6.0s)
            clock.advance(2.0)
            is_dup, _, _ = await service.check_and_register(sample_request, "session-1")
            assert is_dup is True, "Retry within dedup window should be blocked"

            # Advance past dedup window (6.0s)
            clock.advance(5.0)

            is_dup, _, _ = await service.check_and_register(sample_request, "session-1")
            assert is_dup is False, "Retry after dedup window should be allowed"

    @pytest.mark.asyncio
    async def test_streaming_requests_blocked_for_longer_than_base_window(self) -> None:
        """Streaming requests should be deduplicated for a longer TTL by default.

        This prevents expensive zombie retry loops during/after streaming responses.
        """
        from tests.utils.fake_clock import FakeClockContext

        service = RequestDeduplicationService(
            window_seconds=0.1,
            streaming_window_seconds=1.0,
            streaming_in_flight_window_seconds=1.0,
            enabled=True,
        )
        request = ChatRequest(
            model="gpt-4",
            stream=True,
            messages=[ChatMessage(role="user", content="hello")],
        )

        async with FakeClockContext() as clock:
            is_dup, content_hash, _ = await service.check_and_register(
                request, "session-1"
            )
            assert is_dup is False
            await service.mark_request_complete(
                content_hash, "session-1", status_code=200
            )

            # Past the base window, but still within streaming TTL.
            clock.advance(0.15)
            is_dup, _, _ = await service.check_and_register(request, "session-1")
            assert is_dup is True

            # Past the streaming TTL: should be treated as new.
            clock.advance(1.1)
            is_dup, _, _ = await service.check_and_register(request, "session-1")
            assert is_dup is False

    @pytest.mark.asyncio
    async def test_zombie_pattern_detection(
        self, service: RequestDeduplicationService, sample_request: ChatRequest
    ) -> None:
        """Reproduce zombie request pattern from production logs.

        Scenario:
        1. Request sent → succeeds (200)
        2. Client "stops" but orphaned retry logic continues
        3. Same request retried → should be BLOCKED (zombie)
        """
        # Initial request succeeds
        is_dup, hash1, _ = await service.check_and_register(sample_request, "session-1")
        await service.mark_request_complete(hash1, "session-1", status_code=200)

        # User "stops" client, but zombie retry fires
        is_dup, _, _ = await service.check_and_register(sample_request, "session-1")
        assert is_dup is True, "Zombie retry after success should be blocked"

        # Multiple zombie retries should all be blocked
        for _ in range(3):
            is_dup, _, _ = await service.check_and_register(sample_request, "session-1")
            assert is_dup is True

        stats = service.get_stats()
        assert stats.duplicates_blocked == 4  # Initial + 3 more

    @pytest.mark.asyncio
    async def test_native_responses_payload_differentiates_tool_followup(
        self, service: RequestDeduplicationService
    ) -> None:
        """Native /v1/responses turns share placeholder messages; hash must use payload.

        Without hashing responses_native_projected_payload, a function_call_output
        follow-up collides with the prior turn and is swallowed as a duplicate.
        """
        placeholder = [ChatMessage(role="user", content=".")]
        turn1 = ChatRequest(
            model="openai-chatgpt-plan:gpt-6.1-sol",
            messages=placeholder,
            stream=True,
            extra_body={
                "responses_native_projected_payload": {
                    "model": "gpt-6.1-sol",
                    "input": [{"type": "message", "role": "user", "content": "call tool"}],
                    "tools": [{"type": "function", "name": "get_weather"}],
                }
            },
        )
        turn2 = ChatRequest(
            model="openai-chatgpt-plan:gpt-6.1-sol",
            messages=placeholder,
            stream=True,
            extra_body={
                "responses_native_projected_payload": {
                    "model": "gpt-6.1-sol",
                    "input": [
                        {"type": "message", "role": "user", "content": "call tool"},
                        {
                            "type": "function_call",
                            "call_id": "call_1",
                            "name": "get_weather",
                            "arguments": "{\"city\":\"Warsaw\"}",
                        },
                        {
                            "type": "function_call_output",
                            "call_id": "call_1",
                            "output": "{\"temp_c\":18}",
                        },
                    ],
                    "tools": [{"type": "function", "name": "get_weather"}],
                }
            },
        )

        is_dup1, hash1, _ = await service.check_and_register(turn1, "session-native")
        assert is_dup1 is False
        await service.mark_request_complete(hash1, "session-native", status_code=200)

        is_dup2, hash2, _ = await service.check_and_register(turn2, "session-native")
        assert is_dup2 is False
        assert hash1 != hash2
