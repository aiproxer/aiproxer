from __future__ import annotations

import asyncio
import logging
from contextlib import suppress
from typing import Any

from fastapi import FastAPI

from src.core.interfaces.session_service_interface import ISessionService
from src.core.interfaces.wire_capture_interface import IWireCapture

logger = logging.getLogger(__name__)

# Maximum number of background tasks to prevent unbounded memory growth
# If tasks are created faster than they complete, this limit prevents memory leaks
# 1,000 tasks is roughly ~50-100 KB of memory (assuming ~50-100 bytes per task reference)
_MAX_BACKGROUND_TASKS = 1_000


class AppLifecycle:
    """Handles application lifecycle events.

    This class manages startup and shutdown tasks for the application.
    """

    def __init__(self, app: FastAPI, config: dict[str, Any]) -> None:
        """Initialize the lifecycle manager.

        Args:
            app: The FastAPI application
            config: The application configuration
        """
        self.app = app
        self.config = config
        self._background_tasks: list[asyncio.Task] = []

    def _remove_completed_task(self, task: asyncio.Task) -> None:
        """Remove a completed task from the background tasks list.

        This callback is registered on each task to prevent memory leaks.
        """
        with suppress(ValueError):
            # Task already removed (shouldn't happen, but safe to ignore)
            self._background_tasks.remove(task)

    def _cleanup_completed_tasks(self) -> None:
        """Remove all completed tasks from the background tasks list.

        This prevents unbounded memory growth from accumulating completed tasks.
        """
        # Remove completed tasks in reverse order to avoid index shifting issues
        for i in range(len(self._background_tasks) - 1, -1, -1):
            if self._background_tasks[i].done():
                self._background_tasks.pop(i)

        # Enforce max limit to prevent unbounded growth
        # If we're at the limit, cancel oldest tasks (FIFO eviction)
        if len(self._background_tasks) >= _MAX_BACKGROUND_TASKS:
            excess_count = len(self._background_tasks) - _MAX_BACKGROUND_TASKS + 1
            for i in range(excess_count):
                if i < len(self._background_tasks):
                    task = self._background_tasks[i]
                    if not task.done():
                        task.cancel()
                    self._background_tasks.pop(i)
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Evicted %d oldest background tasks (max=%d reached)",
                    excess_count,
                    _MAX_BACKGROUND_TASKS,
                )

    async def startup(self) -> None:
        """Perform startup tasks.

        This method is called during application startup.
        """
        if logger.isEnabledFor(logging.INFO):
            logger.info("Starting application lifecycle...")

        # Start health check services
        await self._start_health_checks()

        # Start ProxyMem services (analysis worker, maintenance)
        await self._start_memory_services()

        # Start EoS subscribers
        await self._start_eos_subscribers()

        # Start usage tracking services (async write queue)
        await self._start_usage_tracking_services()

        # Start model catalog updater
        await self._start_model_catalog_updater()

        # Start background tasks

        self._start_background_tasks()

    async def shutdown(self) -> None:
        """Perform shutdown tasks.

        This method is called during application shutdown.
        """
        if logger.isEnabledFor(logging.INFO):
            logger.info("Shutting down application lifecycle...")

        # Stop EoS subscribers
        await self._stop_eos_subscribers()

        # Stop ProxyMem services
        await self._stop_memory_services()

        # Stop usage tracking services (drain pending records)
        await self._stop_usage_tracking_services()

        # Stop model catalog updater
        await self._stop_model_catalog_updater()

        # Stop background tasks

        await self._stop_background_tasks()

        # Close any remaining connections
        await self._close_connections()

        # Dispose service provider to clean up singleton resources
        await self._dispose_service_provider()

    async def _start_memory_services(self) -> None:
        """Start ProxyMem services (analysis worker, maintenance).

        Per Req 10.2: Start periodic cleanup on proxy startup.
        Per Req 6.6: Start analysis queue processor.
        """
        provider = getattr(self.app.state, "service_provider", None)
        if not provider:
            return

        try:
            from src.core.memory.analysis_worker import AnalysisWorker
            from src.core.memory.completion_detector import SessionCompletionDetector
            from src.core.memory.config import MemoryConfiguration
            from src.core.memory.maintenance import DatabaseMaintenance

            # Check if memory feature is available
            memory_config = provider.get_service(MemoryConfiguration)
            if not memory_config or not memory_config.available:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug("ProxyMem not available, skipping service startup")
                return

            # Start analysis worker (processes queue, generates summaries)
            analysis_worker = provider.get_service(AnalysisWorker)
            if analysis_worker:
                await analysis_worker.start()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("ProxyMem analysis worker started")

            # Start session completion detector (timeout checker)
            completion_detector = provider.get_service(SessionCompletionDetector)
            if completion_detector:
                await completion_detector.start_timeout_checker()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("ProxyMem session timeout checker started")

            # Start database maintenance (retention cleanup)
            maintenance = provider.get_service(DatabaseMaintenance)
            if maintenance:
                await maintenance.start_periodic_cleanup(interval_hours=24)
                if logger.isEnabledFor(logging.INFO):
                    logger.info("ProxyMem database maintenance started")

        except ImportError:
            # Memory services not available
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning("Error starting ProxyMem services: %s", e, exc_info=True)

    async def _start_eos_subscribers(self) -> None:
        """Start End-of-Session event subscribers.

        Starts all EoS subscribers that listen for RemoteBackendConnectionEndOfSessionEvent.
        """
        provider = getattr(self.app.state, "service_provider", None)
        if not provider:
            return

        # Start ProxyMemEosSubscriber
        try:
            from src.core.memory.eos_subscriber import ProxyMemEosSubscriber

            subscriber = provider.get_service(ProxyMemEosSubscriber)
            if subscriber:
                await subscriber.start()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("ProxyMemEosSubscriber started")
        except ImportError:
            # ProxyMemEosSubscriber not available (memory disabled)
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to start ProxyMemEosSubscriber: %s", e, exc_info=True
                )

        # Start UsageTrackingEosSubscriber
        try:
            from src.core.services.usage_tracking_eos_subscriber import (
                UsageTrackingEosSubscriber,
            )

            subscriber = provider.get_service(UsageTrackingEosSubscriber)
            if subscriber:
                await subscriber.start()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("UsageTrackingEosSubscriber started")
        except ImportError:
            # UsageTrackingEosSubscriber not available
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to start UsageTrackingEosSubscriber: %s", e, exc_info=True
                )

        # Start WireCaptureEosSubscriber
        try:
            from src.core.services.wire_capture_eos_subscriber import (
                WireCaptureEosSubscriber,
            )

            subscriber = provider.get_service(WireCaptureEosSubscriber)
            if subscriber:
                await subscriber.start()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("WireCaptureEosSubscriber started")
        except ImportError:
            # WireCaptureEosSubscriber not available
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to start WireCaptureEosSubscriber: %s", e, exc_info=True
                )

        # Start TestExecutionReminderEosSubscriber
        # Note: This subscriber is created inline in provider_lifecycle and stored
        # in provider._test_execution_reminder_eos_subscriber
        try:
            from src.services.test_execution_reminder.eos_subscriber import (
                TestExecutionReminderEosSubscriber,
            )

            # Try to get the subscriber from provider (stored in provider_lifecycle)
            subscriber = getattr(
                provider, "_test_execution_reminder_eos_subscriber", None
            )
            if subscriber and isinstance(
                subscriber, TestExecutionReminderEosSubscriber
            ):
                await subscriber.start()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("TestExecutionReminderEosSubscriber started")
            else:
                # Fallback: try to get from DI if registered as service
                subscriber = provider.get_service(TestExecutionReminderEosSubscriber)
                if subscriber:
                    await subscriber.start()
                    if logger.isEnabledFor(logging.INFO):
                        logger.info("TestExecutionReminderEosSubscriber started")
        except ImportError:
            # TestExecutionReminderEosSubscriber not available
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to start TestExecutionReminderEosSubscriber: %s",
                    e,
                    exc_info=True,
                )

        # Start SessionCancellationCleanupEosSubscriber
        try:
            from src.core.services.session_cancellation_cleanup_eos_subscriber import (
                SessionCancellationCleanupEosSubscriber,
            )

            subscriber = provider.get_service(SessionCancellationCleanupEosSubscriber)
            if subscriber:
                await subscriber.start()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("SessionCancellationCleanupEosSubscriber started")
        except ImportError:
            # SessionCancellationCleanupEosSubscriber not available
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to start SessionCancellationCleanupEosSubscriber: %s",
                    e,
                    exc_info=True,
                )

        # Start ModelReplacementEosSubscriber
        try:
            from src.core.services.model_replacement_eos_subscriber import (
                ModelReplacementEosSubscriber,
            )

            subscriber = provider.get_service(ModelReplacementEosSubscriber)
            if subscriber:
                await subscriber.start()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("ModelReplacementEosSubscriber started")
        except ImportError:
            # ModelReplacementEosSubscriber not available
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to start ModelReplacementEosSubscriber: %s",
                    e,
                    exc_info=True,
                )

        # Start RedactionCacheEosSubscriber
        try:
            from src.core.services.redaction_cache_eos_subscriber import (
                RedactionCacheEosSubscriber,
            )

            subscriber = provider.get_service(RedactionCacheEosSubscriber)
            if subscriber:
                await subscriber.start()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("RedactionCacheEosSubscriber started")
        except ImportError:
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to start RedactionCacheEosSubscriber: %s",
                    e,
                    exc_info=True,
                )

    async def _stop_eos_subscribers(self) -> None:
        """Stop End-of-Session event subscribers.

        Stops all EoS subscribers that were started during application startup.
        Ensures all subscribers are attempted even if some fail, preventing resource leaks.
        """
        provider = getattr(self.app.state, "service_provider", None)
        if not provider:
            return

        # Collect all subscribers first to ensure we attempt to stop all of them
        # even if one fails partway through
        subscribers_to_stop: list[tuple[str, Any]] = []

        # Collect ProxyMemEosSubscriber
        try:
            from src.core.memory.eos_subscriber import ProxyMemEosSubscriber

            subscriber = provider.get_service(ProxyMemEosSubscriber)
            if subscriber:
                subscribers_to_stop.append(("ProxyMemEosSubscriber", subscriber))
        except ImportError:
            pass

        # Collect UsageTrackingEosSubscriber
        try:
            from src.core.services.usage_tracking_eos_subscriber import (
                UsageTrackingEosSubscriber,
            )

            subscriber = provider.get_service(UsageTrackingEosSubscriber)
            if subscriber:
                subscribers_to_stop.append(("UsageTrackingEosSubscriber", subscriber))
        except ImportError:
            pass

        # Collect WireCaptureEosSubscriber
        try:
            from src.core.services.wire_capture_eos_subscriber import (
                WireCaptureEosSubscriber,
            )

            subscriber = provider.get_service(WireCaptureEosSubscriber)
            if subscriber:
                subscribers_to_stop.append(("WireCaptureEosSubscriber", subscriber))
        except ImportError:
            pass

        # Collect TestExecutionReminderEosSubscriber
        try:
            from src.services.test_execution_reminder.eos_subscriber import (
                TestExecutionReminderEosSubscriber,
            )

            subscriber = getattr(
                provider, "_test_execution_reminder_eos_subscriber", None
            )
            if subscriber and isinstance(
                subscriber, TestExecutionReminderEosSubscriber
            ):
                subscribers_to_stop.append(
                    ("TestExecutionReminderEosSubscriber", subscriber)
                )
            else:
                subscriber = provider.get_service(TestExecutionReminderEosSubscriber)
                if subscriber:
                    subscribers_to_stop.append(
                        ("TestExecutionReminderEosSubscriber", subscriber)
                    )
        except ImportError:
            pass

        # Collect SessionCancellationCleanupEosSubscriber
        try:
            from src.core.services.session_cancellation_cleanup_eos_subscriber import (
                SessionCancellationCleanupEosSubscriber,
            )

            subscriber = provider.get_service(SessionCancellationCleanupEosSubscriber)
            if subscriber:
                subscribers_to_stop.append(
                    ("SessionCancellationCleanupEosSubscriber", subscriber)
                )
        except ImportError:
            pass

        # Collect ModelReplacementEosSubscriber
        try:
            from src.core.services.model_replacement_eos_subscriber import (
                ModelReplacementEosSubscriber,
            )

            subscriber = provider.get_service(ModelReplacementEosSubscriber)
            if subscriber:
                subscribers_to_stop.append(
                    ("ModelReplacementEosSubscriber", subscriber)
                )
        except ImportError:
            pass

        # Collect RedactionCacheEosSubscriber
        try:
            from src.core.services.redaction_cache_eos_subscriber import (
                RedactionCacheEosSubscriber,
            )

            subscriber = provider.get_service(RedactionCacheEosSubscriber)
            if subscriber:
                subscribers_to_stop.append(("RedactionCacheEosSubscriber", subscriber))
        except ImportError:
            pass

        # Now stop all subscribers, ensuring each is attempted even if others fail
        for subscriber_name, subscriber in subscribers_to_stop:
            try:
                await subscriber.stop()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("%s stopped", subscriber_name)
            except Exception as e:
                # Log error but continue to stop remaining subscribers
                # This prevents resource leaks if one subscriber fails
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Failed to stop %s: %s", subscriber_name, e, exc_info=True
                    )
                # Attempt direct unsubscribe as fallback if stop() failed
                try:
                    from src.core.domain.events.end_of_session_events import (
                        RemoteBackendConnectionEndOfSessionEvent,
                    )
                    from src.core.interfaces.event_bus_interface import IEventBus

                    event_bus = provider.get_service(IEventBus)
                    if event_bus and hasattr(subscriber, "_handle_eos_event"):
                        event_bus.unsubscribe(
                            RemoteBackendConnectionEndOfSessionEvent,
                            subscriber._handle_eos_event,
                        )
                        if logger.isEnabledFor(logging.DEBUG):
                            logger.debug(
                                "Fallback unsubscribe succeeded for %s", subscriber_name
                            )
                except Exception as fallback_error:
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug(
                            "Fallback unsubscribe failed for %s: %s",
                            subscriber_name,
                            fallback_error,
                        )

    async def _stop_memory_services(self) -> None:
        """Stop ProxyMem services."""
        provider = getattr(self.app.state, "service_provider", None)
        if not provider:
            return

        try:
            from src.core.memory.analysis_worker import AnalysisWorker
            from src.core.memory.completion_detector import SessionCompletionDetector
            from src.core.memory.maintenance import DatabaseMaintenance

            # Stop analysis worker
            analysis_worker = provider.get_service(AnalysisWorker)
            if analysis_worker:
                await analysis_worker.stop()

            # Stop session completion detector
            completion_detector = provider.get_service(SessionCompletionDetector)
            if completion_detector:
                await completion_detector.stop_timeout_checker()

            # Stop database maintenance
            maintenance = provider.get_service(DatabaseMaintenance)
            if maintenance:
                await maintenance.stop_periodic_cleanup()

            # Clean up MemoryService cleanup tasks
            from src.core.memory.service import MemoryService

            memory_service = provider.get_service(MemoryService)
            if memory_service:
                await memory_service.cleanup()

            if logger.isEnabledFor(logging.INFO):
                logger.info("ProxyMem services stopped")

        except ImportError:
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning("Error stopping ProxyMem services: %s", e, exc_info=True)

    async def _start_usage_tracking_services(self) -> None:
        """Start usage tracking services (async write queue).

        Starts the AsyncUsageWriteQueue background task for batched
        database writes when database persistence is enabled.
        """
        provider = getattr(self.app.state, "service_provider", None)
        if not provider:
            return

        try:
            from src.core.services.async_usage_write_queue import (
                AsyncUsageWriteQueue,
            )

            write_queue = provider.get_service(AsyncUsageWriteQueue)
            if write_queue:
                await write_queue.start()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("Usage write queue started")
        except ImportError:
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning("Error starting usage write queue: %s", e, exc_info=True)

    async def _stop_usage_tracking_services(self) -> None:
        """Stop usage tracking services and drain pending records.

        Gracefully stops the AsyncUsageWriteQueue, ensuring all pending
        usage records are flushed to the database before shutdown.
        Also stops InMemoryUsageStore persistence thread to prevent thread leaks.
        """
        provider = getattr(self.app.state, "service_provider", None)
        if not provider:
            return

        try:
            from src.core.services.async_usage_write_queue import (
                AsyncUsageWriteQueue,
            )

            write_queue = provider.get_service(AsyncUsageWriteQueue)
            if write_queue:
                await write_queue.stop()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("Usage write queue stopped and drained")
        except ImportError:
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning("Error stopping usage write queue: %s", e, exc_info=True)

        # Stop InMemoryUsageStore persistence thread to prevent thread leaks
        try:
            from src.core.services.in_memory_usage_store import InMemoryUsageStore

            usage_store = provider.get_service(InMemoryUsageStore)
            if usage_store:
                usage_store.stop_persistence_thread()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("InMemoryUsageStore persistence thread stopped")
        except ImportError:
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Error stopping InMemoryUsageStore persistence thread: %s",
                    e,
                    exc_info=True,
                )

    async def _start_model_catalog_updater(self) -> None:
        """Start the model catalog updater."""
        provider = getattr(self.app.state, "service_provider", None)
        if not provider:
            return

        try:
            from src.core.services.model_catalog_updater import ModelCatalogUpdater

            updater = provider.get_service(ModelCatalogUpdater)
            if updater:
                await updater.start()
        except ImportError:
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Error starting model catalog updater: %s", e, exc_info=True
                )

    async def _stop_model_catalog_updater(self) -> None:
        """Stop the model catalog updater."""
        provider = getattr(self.app.state, "service_provider", None)
        if not provider:
            return

        try:
            from src.core.services.model_catalog_updater import ModelCatalogUpdater

            updater = provider.get_service(ModelCatalogUpdater)
            if updater:
                await updater.stop()
        except ImportError:
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Error stopping model catalog updater: %s", e, exc_info=True
                )

    async def _start_health_checks(self) -> None:
        """Start health check services if enabled."""
        provider = getattr(self.app.state, "service_provider", None)
        if not provider:
            return

        try:
            from src.core.services.health.backend_notifier import (
                BackendHealthNotifier,
            )
            from src.core.services.health.health_check_scheduler import (
                HealthCheckScheduler,
            )
            from src.core.services.health.logging_handler import HealthLoggingHandler
            from src.core.services.health.state_manager import HealthStateManager

            # Start state manager (subscribes to check events)
            state_manager = provider.get_service(HealthStateManager)
            if state_manager:
                await state_manager.start()

            # Start logging handler (subscribes to transition events)
            logging_handler = provider.get_service(HealthLoggingHandler)
            if logging_handler:
                await logging_handler.start()

            # Start backend notifier (routes health events to backends)
            backend_notifier = provider.get_service(BackendHealthNotifier)
            if backend_notifier:
                await backend_notifier.start()

            # Start scheduler (runs background check loops)
            scheduler = provider.get_service(HealthCheckScheduler)
            if scheduler:
                await scheduler.start()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("Health check services started")

            # Start connection tracker cleanup scheduler
            try:
                from src.core.services.connection_tracker_cleanup_scheduler import (
                    ConnectionTrackerCleanupScheduler,
                )

                cleanup_scheduler = provider.get_service(
                    ConnectionTrackerCleanupScheduler
                )
                if cleanup_scheduler:
                    await cleanup_scheduler.start()
                    if logger.isEnabledFor(logging.INFO):
                        logger.info("Connection tracker cleanup scheduler started")
            except ImportError:
                # Connection tracker cleanup not available
                pass
            except Exception as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Error starting connection tracker cleanup scheduler: %s",
                        e,
                        exc_info=True,
                    )

        except ImportError:
            # Health check services not available
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Error starting health check services: %s", e, exc_info=True
                )

    def _start_background_tasks(self) -> None:
        """Start background tasks."""
        # Start session cleanup task
        if self.config.get("session_cleanup_enabled", True):
            interval = self.config.get(
                "session_cleanup_interval", 3600
            )  # 1 hour default
            max_age = self.config.get("session_max_age", 86400)  # 1 day default

            task = asyncio.create_task(
                self._session_cleanup_task(interval, max_age), name="session_cleanup"
            )
            # Clean up completed tasks before adding new one (lazy cleanup)
            self._cleanup_completed_tasks()
            # Add task and register callback to remove it when done
            self._background_tasks.append(task)
            task.add_done_callback(self._remove_completed_task)
            if logger.isEnabledFor(logging.INFO):
                logger.info(
                    "Started session cleanup task (interval: %ds, max age: %ds)",
                    interval,
                    max_age,
                )

    async def _stop_background_tasks(self) -> None:
        """Stop background tasks."""
        for task in self._background_tasks:
            if not task.done():
                task.cancel()
                try:
                    await task
                except asyncio.CancelledError:
                    if logger.isEnabledFor(logging.INFO):
                        logger.info("Cancelled background task: %s", task.get_name())

    async def _close_connections(self) -> None:
        """Close any remaining connections."""
        # Get service provider
        provider = getattr(self.app.state, "service_provider", None)
        if not provider:
            return

        # Shutdown health check services
        await self._shutdown_health_checks(provider)

        # Shutdown all cached backends to prevent subprocess leaks
        # This ensures backends with subprocesses clean up properly
        await self._shutdown_all_backends(provider)

        # Get wire capture service and shut it down
        wire_capture_service = provider.get_service(IWireCapture)
        if wire_capture_service and hasattr(wire_capture_service, "shutdown"):
            await wire_capture_service.shutdown()

        # Shutdown quota status service
        from src.core.services.quota_status_service import QuotaStatusService

        quota_status_service = provider.get_service(QuotaStatusService)
        if quota_status_service and hasattr(quota_status_service, "shutdown"):
            await quota_status_service.shutdown()

    async def _dispose_service_provider(self) -> None:
        """Dispose service provider to clean up singleton resources.

        This ensures that singleton services with dispose() methods
        (such as DatabaseEngine) are properly cleaned up, preventing
        connection termination errors during shutdown.
        """
        provider: Any = getattr(self.app.state, "service_provider", None)
        if not provider:
            return

        try:
            if hasattr(provider, "dispose") and callable(provider.dispose):
                # We use cast to Any to avoid pyright awaitability issues on object
                from typing import cast

                await cast(Any, provider).dispose()
                if logger.isEnabledFor(logging.INFO):
                    logger.info("Service provider disposed successfully")
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning("Error disposing service provider: %s", e, exc_info=True)

    async def _shutdown_all_backends(self, provider: Any) -> None:
        """Shutdown all cached backends to prevent resource leaks.

        This method ensures that all backends (including those with subprocesses)
        are properly shut down during app shutdown, preventing subprocess leaks.

        Args:
            provider: The service provider.
        """
        try:
            from typing import cast

            from src.core.interfaces.backend_lifecycle_manager_interface import (
                IBackendLifecycleManager,
            )

            lifecycle_manager = provider.get_service(
                cast(type, IBackendLifecycleManager)
            )
            if not lifecycle_manager:
                return

            # Get all active backends
            active_backends = lifecycle_manager.get_active_backends()
            if not active_backends:
                return

            if logger.isEnabledFor(logging.INFO):
                logger.info(
                    "Shutting down %d cached backend(s)...", len(active_backends)
                )

            # Shutdown each backend
            shutdown_tasks = []
            for cache_key, backend in active_backends.items():
                try:
                    shutdown_method = getattr(backend, "shutdown", None)
                    if shutdown_method:
                        if asyncio.iscoroutinefunction(shutdown_method):
                            shutdown_tasks.append(
                                (
                                    cache_key,
                                    asyncio.create_task(shutdown_method()),
                                )
                            )
                        else:
                            # Synchronous shutdown
                            shutdown_method()
                            if logger.isEnabledFor(logging.DEBUG):
                                logger.debug("Shut down backend: %s", cache_key)
                except Exception as e:
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            "Error shutting down backend %s: %s",
                            cache_key,
                            e,
                            exc_info=True,
                        )

            # Wait for all async shutdowns to complete
            if shutdown_tasks:
                results = await asyncio.gather(
                    *[task for _, task in shutdown_tasks],
                    return_exceptions=True,
                )
                for (cache_key, _), result in zip(
                    shutdown_tasks, results, strict=False
                ):
                    if isinstance(result, Exception):
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning(
                                "Error shutting down backend %s: %s",
                                cache_key,
                                result,
                                exc_info=True,
                            )
                    elif logger.isEnabledFor(logging.DEBUG):
                        logger.debug("Shut down backend: %s", cache_key)

            # Await any pending shutdown tasks created by discard() operations
            # This prevents resource leaks from untracked shutdown tasks
            try:
                await lifecycle_manager.await_pending_shutdown_tasks(timeout=5.0)
            except Exception as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Error awaiting pending backend shutdown tasks: %s",
                        e,
                        exc_info=True,
                    )

            if logger.isEnabledFor(logging.INFO):
                logger.info("All cached backends shut down")

        except ImportError:
            # Backend lifecycle manager not available
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning("Error shutting down backends: %s", e, exc_info=True)

    async def _shutdown_health_checks(self, provider: Any) -> None:
        """Shutdown health check services.

        Args:
            provider: The service provider.
        """
        try:
            from src.core.services.health.backend_notifier import (
                BackendHealthNotifier,
            )
            from src.core.services.health.health_check_scheduler import (
                HealthCheckScheduler,
            )
            from src.core.services.health.logging_handler import HealthLoggingHandler
            from src.core.services.health.state_manager import HealthStateManager

            # Stop scheduler first
            scheduler = provider.get_service(HealthCheckScheduler)
            if scheduler:
                await scheduler.shutdown()

            # Stop connection tracker cleanup scheduler
            try:
                from src.core.services.connection_tracker_cleanup_scheduler import (
                    ConnectionTrackerCleanupScheduler,
                )

                cleanup_scheduler = provider.get_service(
                    ConnectionTrackerCleanupScheduler
                )
                if cleanup_scheduler:
                    await cleanup_scheduler.stop()
            except ImportError:
                # Connection tracker cleanup not available
                pass
            except Exception as e:
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "Error stopping connection tracker cleanup scheduler: %s",
                        e,
                        exc_info=True,
                    )

            # Stop backend notifier
            backend_notifier = provider.get_service(BackendHealthNotifier)
            if backend_notifier:
                await backend_notifier.stop()

            # Stop state manager
            state_manager = provider.get_service(HealthStateManager)
            if state_manager:
                await state_manager.stop()

            # Stop logging handler
            logging_handler = provider.get_service(HealthLoggingHandler)
            if logging_handler:
                await logging_handler.stop()

            # Shutdown event bus
            from src.core.interfaces.event_bus_interface import IEventBus

            event_bus = provider.get_service(IEventBus)
            if event_bus:
                await event_bus.shutdown()

        except ImportError:
            # Health check services not available
            pass
        except Exception as e:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Error shutting down health check services: %s", e, exc_info=True
                )

    async def _session_cleanup_task(self, interval: int, max_age: int) -> None:
        """Background task for cleaning up expired sessions.

        Args:
            interval: The interval in seconds between cleanup runs
            max_age: The maximum age in seconds for sessions
        """
        try:
            while True:
                await asyncio.sleep(interval)

                try:
                    # Get service provider
                    provider = self.app.state.service_provider
                    if not provider:
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning(
                                "Service provider not available for session cleanup"
                            )
                        continue

                    # Get session service
                    session_service = provider.get_service(ISessionService)
                    if not session_service:
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning("Session service not available for cleanup")
                        continue

                    # Perform cleanup
                    deleted_count = 0
                    with suppress(AttributeError):
                        deleted_count = await session_service.cleanup_expired(max_age)

                    if deleted_count > 0 and logger.isEnabledFor(logging.INFO):
                        logger.info("Cleaned up %d expired sessions", deleted_count)

                except Exception as e:
                    if logger.isEnabledFor(logging.ERROR):
                        logger.error(
                            "Error during session cleanup: %s", e, exc_info=True
                        )

        except asyncio.CancelledError:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug("Session cleanup task cancelled")
            raise
