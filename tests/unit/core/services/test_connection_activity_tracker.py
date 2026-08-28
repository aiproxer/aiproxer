"""Unit tests for ConnectionActivityTracker service."""

from __future__ import annotations

import contextlib
import time
from concurrent.futures import ThreadPoolExecutor
from unittest.mock import patch

import pytest
from src.core.domain.connection_activity import (
    BackendActivitySnapshot,
    ConnectionActivity,
    ConnectionType,
)
from src.core.services.connection_activity_tracker import (
    ConnectionActivityTracker,
    get_activity_tracker,
    reset_activity_tracker,
)


class TestConnectionActivity:
    """Tests for ConnectionActivity domain model."""

    def test_connection_activity_defaults(self) -> None:
        """Test ConnectionActivity has correct defaults."""
        activity = ConnectionActivity(
            session_id="test-session",
            backend_name="openai.1",
            connection_type=ConnectionType.STREAMING,
        )

        assert activity.session_id == "test-session"
        assert activity.backend_name == "openai.1"
        assert activity.connection_type == ConnectionType.STREAMING
        assert activity.model is None
        assert activity.bytes_rx == 0
        assert activity.bytes_tx == 0
        assert activity.started_at > 0

    def test_connection_activity_duration(self) -> None:
        """Test duration_seconds property."""
        base_time = 1000.0
        with patch("time.time", return_value=base_time):
            start_time = time.time() - 5.0  # 5 seconds ago
            activity = ConnectionActivity(
                session_id="test",
                backend_name="test",
                connection_type=ConnectionType.NON_STREAMING,
                started_at=start_time,
            )

            # Duration should be approximately 5 seconds
            assert 4.9 <= activity.duration_seconds <= 5.5

    def test_connection_activity_to_dict(self) -> None:
        """Test to_dict serialization."""
        activity = ConnectionActivity(
            session_id="session-123",
            backend_name="anthropic.1",
            connection_type=ConnectionType.STREAMING,
            model="claude-3-sonnet",
            bytes_rx=1000,
            bytes_tx=500,
        )

        data = activity.to_dict()

        assert data.session_id == "session-123"
        assert data.backend_name == "anthropic.1"
        assert data.connection_type == "streaming"
        assert data.model == "claude-3-sonnet"
        assert data.bytes_rx == 1000
        assert data.bytes_tx == 500
        assert "duration_seconds" in data.model_dump()
        assert "started_at" in data.model_dump()


class TestBackendActivitySnapshot:
    """Tests for BackendActivitySnapshot domain model."""

    def test_snapshot_defaults(self) -> None:
        """Test BackendActivitySnapshot has correct defaults."""
        snapshot = BackendActivitySnapshot(backend_name="openai.1")

        assert snapshot.backend_name == "openai.1"
        assert snapshot.active_connections == 0
        assert snapshot.connections == []
        assert snapshot.total_bytes_rx == 0
        assert snapshot.total_bytes_tx == 0

    def test_snapshot_to_dict(self) -> None:
        """Test to_dict serialization."""
        conn = ConnectionActivity(
            session_id="s1",
            backend_name="openai.1",
            connection_type=ConnectionType.STREAMING,
            bytes_rx=100,
            bytes_tx=50,
        )
        snapshot = BackendActivitySnapshot(
            backend_name="openai.1",
            active_connections=1,
            connections=[conn],
            total_bytes_rx=100,
            total_bytes_tx=50,
        )

        data = snapshot.to_dict()

        assert data.backend_name == "openai.1"
        assert data.active_connections == 1
        assert len(data.connections) == 1
        assert data.total_bytes_rx == 100
        assert data.total_bytes_tx == 50


class TestConnectionActivityTracker:
    """Tests for ConnectionActivityTracker service."""

    def setup_method(self) -> None:
        """Reset tracker before each test."""
        reset_activity_tracker()
        self.tracker = ConnectionActivityTracker()

    def teardown_method(self) -> None:
        """Clean up after each test."""
        reset_activity_tracker()

    def test_track_connection_context_manager(self) -> None:
        """Test connection tracking via context manager."""
        assert self.tracker.get_connection_count() == 0

        with self.tracker.track_connection(
            session_id="test-session",
            backend_name="openai.1",
            connection_type=ConnectionType.STREAMING,
            model="gpt-4",
        ):
            assert self.tracker.get_connection_count() == 1

            # Verify connection details
            snapshot = self.tracker.get_backend_snapshot("openai.1")
            assert snapshot.active_connections == 1
            assert len(snapshot.connections) == 1
            conn = snapshot.connections[0]
            assert conn.session_id == "test-session"
            assert conn.model == "gpt-4"

        # Connection should be removed after context exits
        assert self.tracker.get_connection_count() == 0

    def test_track_connection_cleanup_on_exception(self) -> None:
        """Test connection is cleaned up even when exception occurs."""
        assert self.tracker.get_connection_count() == 0

        with (
            pytest.raises(ValueError),
            self.tracker.track_connection(
                session_id="test",
                backend_name="test",
                connection_type=ConnectionType.NON_STREAMING,
            ),
        ):
            assert self.tracker.get_connection_count() == 1
            raise ValueError("Test exception")

        # Connection should be removed despite exception
        assert self.tracker.get_connection_count() == 0

    def test_increment_rx(self) -> None:
        """Test incrementing received bytes counter."""
        with self.tracker.track_connection(
            session_id="s1",
            backend_name="openai.1",
            connection_type=ConnectionType.STREAMING,
        ):
            self.tracker.increment_rx("s1", "openai.1", 100)
            self.tracker.increment_rx("s1", "openai.1", 50)

            snapshot = self.tracker.get_backend_snapshot("openai.1")
            assert snapshot.total_bytes_rx == 150
            assert snapshot.connections[0].bytes_rx == 150

    def test_increment_tx(self) -> None:
        """Test incrementing transmitted bytes counter."""
        with self.tracker.track_connection(
            session_id="s1",
            backend_name="openai.1",
            connection_type=ConnectionType.STREAMING,
        ):
            self.tracker.increment_tx("s1", "openai.1", 200)
            self.tracker.increment_tx("s1", "openai.1", 100)

            snapshot = self.tracker.get_backend_snapshot("openai.1")
            assert snapshot.total_bytes_tx == 300
            assert snapshot.connections[0].bytes_tx == 300

    def test_increment_ignores_non_positive_values(self) -> None:
        """Test that non-positive byte counts are ignored."""
        with self.tracker.track_connection(
            session_id="s1",
            backend_name="test",
            connection_type=ConnectionType.STREAMING,
        ):
            self.tracker.increment_rx("s1", "test", 0)
            self.tracker.increment_rx("s1", "test", -10)
            self.tracker.increment_tx("s1", "test", 0)
            self.tracker.increment_tx("s1", "test", -5)

            snapshot = self.tracker.get_backend_snapshot("test")
            assert snapshot.total_bytes_rx == 0
            assert snapshot.total_bytes_tx == 0

    def test_increment_ignores_unknown_connection(self) -> None:
        """Test that increments for unknown connections are ignored."""
        # Should not raise
        self.tracker.increment_rx("unknown", "unknown", 100)
        self.tracker.increment_tx("unknown", "unknown", 100)

        # No connections should exist
        assert self.tracker.get_connection_count() == 0

    def test_multiple_connections_same_backend(self) -> None:
        """Test multiple connections to the same backend."""
        with self.tracker.track_connection(
            session_id="s1",
            backend_name="openai.1",
            connection_type=ConnectionType.STREAMING,
        ):
            with self.tracker.track_connection(
                session_id="s2",
                backend_name="openai.1",
                connection_type=ConnectionType.STREAMING,
            ):
                assert self.tracker.get_connection_count() == 2

                snapshot = self.tracker.get_backend_snapshot("openai.1")
                assert snapshot.active_connections == 2

            # After s2 exits
            assert self.tracker.get_connection_count() == 1

        # After both exit
        assert self.tracker.get_connection_count() == 0

    def test_multiple_backends(self) -> None:
        """Test connections to multiple backends."""
        with (
            self.tracker.track_connection(
                session_id="s1",
                backend_name="openai.1",
                connection_type=ConnectionType.STREAMING,
            ),
            self.tracker.track_connection(
                session_id="s2",
                backend_name="anthropic.1",
                connection_type=ConnectionType.NON_STREAMING,
            ),
        ):
            assert self.tracker.get_connection_count() == 2

            openai_snapshot = self.tracker.get_backend_snapshot("openai.1")
            anthropic_snapshot = self.tracker.get_backend_snapshot("anthropic.1")

            assert openai_snapshot.active_connections == 1
            assert anthropic_snapshot.active_connections == 1

    def test_get_global_snapshot(self) -> None:
        """Test global snapshot aggregation."""
        with self.tracker.track_connection(
            session_id="s1",
            backend_name="openai.1",
            connection_type=ConnectionType.STREAMING,
        ):
            self.tracker.increment_rx("s1", "openai.1", 100)
            self.tracker.increment_tx("s1", "openai.1", 50)

            with self.tracker.track_connection(
                session_id="s2",
                backend_name="anthropic.1",
                connection_type=ConnectionType.NON_STREAMING,
            ):
                self.tracker.increment_rx("s2", "anthropic.1", 200)
                self.tracker.increment_tx("s2", "anthropic.1", 100)

                snapshot = self.tracker.get_global_snapshot()

                assert snapshot.total_active_connections == 2
                assert snapshot.total_bytes_rx == 300
                assert snapshot.total_bytes_tx == 150
                assert len(snapshot.backends) == 2

    def test_get_backend_snapshot_empty(self) -> None:
        """Test getting snapshot for backend with no connections."""
        snapshot = self.tracker.get_backend_snapshot("nonexistent")

        assert snapshot.backend_name == "nonexistent"
        assert snapshot.active_connections == 0
        assert snapshot.connections == []
        assert snapshot.total_bytes_rx == 0
        assert snapshot.total_bytes_tx == 0

    def test_thread_safety(self) -> None:
        """Test thread-safe concurrent access."""
        num_threads = 3
        iterations_per_thread = 20
        errors: list[Exception] = []

        def worker(thread_id: int) -> None:
            try:
                for i in range(iterations_per_thread):
                    session_id = f"thread-{thread_id}-iter-{i}"
                    with self.tracker.track_connection(
                        session_id=session_id,
                        backend_name=f"backend-{thread_id % 3}",
                        connection_type=ConnectionType.STREAMING,
                    ):
                        self.tracker.increment_rx(
                            session_id, f"backend-{thread_id % 3}", 10
                        )
                        self.tracker.increment_tx(
                            session_id, f"backend-{thread_id % 3}", 5
                        )
            except Exception as e:
                errors.append(e)

        with ThreadPoolExecutor(max_workers=num_threads) as executor:
            futures = [executor.submit(worker, i) for i in range(num_threads)]
            for f in futures:
                f.result()

        assert len(errors) == 0, f"Thread errors: {errors}"
        # All connections should be cleaned up
        assert self.tracker.get_connection_count() == 0

    def test_cleanup_stale_connections(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test cleanup of stale connections."""
        current_time = {"value": 1000.0}

        def fake_time() -> float:
            return current_time["value"]

        monkeypatch.setattr(time, "time", fake_time)
        monkeypatch.setattr(
            "src.core.services.connection_activity_tracker.time.time", fake_time
        )

        # Create tracker with very short timeout for testing
        tracker = ConnectionActivityTracker(stale_timeout_seconds=0.1)

        # Manually add a connection without using context manager
        # (simulating orphaned connection)
        from src.core.domain.connection_activity import ConnectionActivity

        stale_conn = ConnectionActivity(
            session_id="stale",
            backend_name="test",
            connection_type=ConnectionType.STREAMING,
            started_at=current_time["value"] - 1.0,  # Started 1 second ago
        )
        with tracker._lock:
            tracker._connections[("test", "stale")] = stale_conn

        assert tracker.get_connection_count() == 1

        # Advance time beyond timeout
        current_time["value"] += 0.15

        # Cleanup should remove the stale connection
        removed = tracker.cleanup_stale_connections()
        assert removed == 1
        assert tracker.get_connection_count() == 0

    def test_clear(self) -> None:
        """Test clearing all connections."""
        with self.tracker.track_connection(
            session_id="s1",
            backend_name="test",
            connection_type=ConnectionType.STREAMING,
        ):
            assert self.tracker.get_connection_count() == 1
            self.tracker.clear()
            assert self.tracker.get_connection_count() == 0

    def test_max_connections_limit_eviction(self) -> None:
        """Test that tracker enforces maximum connection limits."""
        import src.core.services.connection_activity_tracker as cat_module

        # Temporarily lower the max limit for testing
        original_max = cat_module._MAX_CONNECTIONS
        cat_module._MAX_CONNECTIONS = 5

        try:
            tracker = cat_module.ConnectionActivityTracker()

            # Add exactly MAX_CONNECTIONS
            contexts = []
            for i in range(cat_module._MAX_CONNECTIONS):
                ctx = tracker.track_connection(
                    session_id=f"sess-{i}",
                    backend_name="test-be",
                    connection_type=ConnectionType.STREAMING,
                )
                contexts.append(ctx)
                ctx.__enter__()
                # Small sleep to ensure different started_at times
                time.sleep(0.01)

            assert tracker.get_connection_count() == 5

            # Add one more, should evict the oldest
            with tracker.track_connection(
                session_id="sess-overflow",
                backend_name="test-be",
                connection_type=ConnectionType.STREAMING,
            ):
                # The total count should remain at MAX_CONNECTIONS
                assert tracker.get_connection_count() == 5

                # Verify 'sess-0' (the first one) was evicted
                snapshot = tracker.get_backend_snapshot("test-be")
                sessions = [c.session_id for c in snapshot.connections]
                assert "sess-0" not in sessions
                assert "sess-overflow" in sessions

            # Cleanup
            for ctx in contexts:
                with contextlib.suppress(Exception):
                    ctx.__exit__(None, None, None)
        finally:
            cat_module._MAX_CONNECTIONS = original_max


class TestGlobalTrackerSingleton:
    """Tests for global tracker singleton functions."""

    def setup_method(self) -> None:
        """Reset tracker before each test."""
        reset_activity_tracker()

    def teardown_method(self) -> None:
        """Reset tracker after each test."""
        reset_activity_tracker()

    def test_get_activity_tracker_returns_singleton(self) -> None:
        """Test that get_activity_tracker returns the same instance."""
        tracker1 = get_activity_tracker()
        tracker2 = get_activity_tracker()

        assert tracker1 is tracker2

    def test_reset_activity_tracker(self) -> None:
        """Test that reset creates a new instance."""
        tracker1 = get_activity_tracker()

        with tracker1.track_connection(
            session_id="test",
            backend_name="test",
            connection_type=ConnectionType.STREAMING,
        ):
            assert tracker1.get_connection_count() == 1

            reset_activity_tracker()

            tracker2 = get_activity_tracker()
            assert tracker2 is not tracker1
            assert tracker2.get_connection_count() == 0
