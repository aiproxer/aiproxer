"""
Session enricher implementation.

Handles session resolution and client context enrichment including:
- Session ID resolution and loading
- Agent normalization
- Client OS detection
- VTC detection and enablement
- Project directory auto-resolution
"""

from __future__ import annotations

import logging
import re
from dataclasses import dataclass
from typing import Any, cast

from src.core.domain.chat import CanonicalChatRequest, ChatRequest
from src.core.domain.request_context import RequestContext
from src.core.domain.responses_native_wiring import ACP_RESPONSES_TEXT_ONLY_MODE_KEY
from src.core.interfaces.application_state_interface import IApplicationState
from src.core.interfaces.request_processor_internal import ISessionEnricher
from src.core.interfaces.session_manager_interface import ISessionManager

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class MessageRoleAndContent:
    """Extracted role and content from a message."""

    role: Any
    content: Any


class SessionEnricher(ISessionEnricher):
    """
    Handles session resolution and client context enrichment.

    This component is responsible for enriching the request with session-specific
    context including agent normalization, OS detection, VTC enablement, and
    project directory resolution.
    """

    def __init__(
        self,
        session_manager: ISessionManager,
        app_state: IApplicationState | None = None,
    ) -> None:
        """
        Initialize the session enricher.

        Args:
            session_manager: Session manager for session operations
            app_state: Application state for configuration and service access (optional)
        """
        self._session_manager = session_manager
        self._app_state = app_state

    async def enrich(
        self, context: RequestContext, request: ChatRequest
    ) -> tuple[object, ChatRequest]:
        """
        Resolve session and enrich client context.

        Args:
            context: Request context containing headers, cookies, etc.
            request: Chat request to enrich

        Returns:
            tuple[session, possibly_updated_request]: The resolved session object
            and the request, potentially updated with session-specific values
            (agent, VTC flag, etc.).

        This method handles:
        - Session ID resolution
        - Agent normalization (incoming agent vs session agent)
        - Client OS detection and propagation
        - VTC detection and enablement
        - Project directory auto-resolution
        """
        # Attach domain_request to context for intelligent session resolution
        context.domain_request = cast(CanonicalChatRequest, request)

        # Resolve session and update agent if needed
        session_id = await self._session_manager.resolve_session_id(context)
        session = await self._session_manager.get_session(session_id)

        # Agent normalization: prefer request agent, fallback to context agent
        incoming_agent = getattr(request, "agent", None) or getattr(
            context, "agent", None
        )
        session = await self._session_manager.update_session_agent(
            session, incoming_agent
        )
        session_agent = getattr(session, "agent", None)
        if session_agent:
            request = request.model_copy(update={"agent": session_agent})

        # Auto-detect client OS if not yet detected
        if hasattr(session, "state") and not getattr(session.state, "client_os", None):
            client_os = self._detect_client_os(request)
            if client_os:
                new_state = session.state.with_client_os(client_os)
                session.update_state(new_state)
                if logger.isEnabledFor(logging.INFO):
                    logger.info(
                        f"Detected client OS for session {session_id}: {client_os}"
                    )

        # Ensure client_os is available in processing context for downstream middleware
        effective_client_os = getattr(session.state, "client_os", None)
        if effective_client_os:
            context.ensure_processing_context().update(
                {"client_os": effective_client_os}
            )

        # Detect VTC (Virtual Tool Calling) client mode
        if not session.state.vtc_enabled and self._app_state is not None:
            from src.core.services.vtc_detection import detect_vtc_client

            app_config = self._app_state.get_setting("app_config")
            if app_config is not None:
                # Safely get vtc_client_patterns with fallback for mock configs
                vtc_patterns = getattr(app_config, "vtc_client_patterns", None)
                if vtc_patterns:
                    agent_for_vtc = incoming_agent or session_agent
                    if detect_vtc_client(agent_for_vtc, vtc_patterns):
                        new_state = session.state.with_vtc_enabled(True)
                        session.update_state(new_state)
                        if logger.isEnabledFor(logging.INFO):
                            logger.info(
                                "VTC mode enabled for session %s (agent: %s)",
                                session_id,
                                agent_for_vtc,
                            )

        # Propagate VTC flag to request for downstream processors
        if session.state.vtc_enabled:
            request = request.model_copy(update={"vtc_enabled": True})

        extra_body = getattr(request, "extra_body", None)
        uses_static_acp_workspace = bool(
            isinstance(extra_body, dict)
            and extra_body.get(ACP_RESPONSES_TEXT_ONLY_MODE_KEY) is True
        )
        raw_project_dir = (
            getattr(session.state, "project_dir", None)
            if hasattr(session, "state")
            else None
        )
        project_dir_already_resolved = isinstance(raw_project_dir, str) and bool(
            raw_project_dir.strip()
        )

        # Auto-detect project directory if needed. Responses-to-ACP requests use
        # the connector's validated static workspace and must never infer a host
        # path from prompt text. A failed first attempt (for example an OpenCode
        # title request with no cwd) must not block a later request that does
        # include an authoritative workspace line.
        if (
            self._app_state is not None
            and hasattr(session, "state")
            and not project_dir_already_resolved
            and not uses_static_acp_workspace
        ):
            try:
                from src.core.services.project_directory_resolution_service import (
                    ProjectDirectoryResolutionService,
                )

                project_dir_service = self._app_state.get_service(
                    ProjectDirectoryResolutionService
                )
                if project_dir_service:
                    await project_dir_service.maybe_resolve_project_directory(
                        session, request
                    )
                    if logger.isEnabledFor(logging.DEBUG):
                        logger.debug("Project directory auto-detection completed")
            except Exception as e:
                # Don't fail the request if project directory detection fails
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        f"Project directory auto-detection failed: {e}", exc_info=True
                    )

        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(f"Session enrichment completed for session {session_id}")

        return session, request

    def _detect_client_os(self, request: ChatRequest) -> str | None:
        """
        Detect client OS from request messages.

        Args:
            request: Chat request containing messages

        Returns:
            Detected OS ("windows", "macos", "linux") or None if not detected
        """
        if not hasattr(request, "messages"):
            return None

        for message in request.messages:
            # Check user messages for system info
            extracted = self._get_message_role_and_content(message)
            role, content = extracted.role, extracted.content

            # Normalize content to string if it's a list of text blocks (multimodal)
            if isinstance(content, list):
                text_parts = []
                for part in content:
                    part_type = None
                    part_text = None

                    if isinstance(part, dict):
                        part_type = part.get("type")
                        part_text = part.get("text")
                    else:
                        part_type = getattr(part, "type", None)
                        part_text = getattr(part, "text", None)

                    if part_type == "text" and isinstance(part_text, str):
                        text_parts.append(part_text)

                if text_parts:
                    content = "\n".join(text_parts)

            if role in ("user", "system") and isinstance(content, str):
                # Look for "User system info (win32 10.0.19045)"
                # The regex captures the content inside parentheses
                match = re.search(r"User system info \((.*?)\)", content)
                if match:
                    os_info = match.group(1).lower()
                    if "win32" in os_info or "windows" in os_info:
                        return "windows"
                    if "darwin" in os_info or "macos" in os_info:
                        return "macos"
                    if "linux" in os_info:
                        return "linux"

                # Secondary heuristic: File paths
                # Windows path: C:\Users\... (case-insensitive drive letter)
                if re.search(r"[a-zA-Z]:\\[^\s]+", content):
                    return "windows"
                # Unix path: /Users/... or /home/...
                # Note: This is less reliable as URLs also use /
                # but typically absolute paths start with / and don't have protocol://

        return None

    def _get_message_role_and_content(self, raw_message: Any) -> MessageRoleAndContent:
        """
        Extract role and content from dicts or objects uniformly.

        Args:
            raw_message: Message as dict or object

        Returns:
            MessageRoleAndContent with extracted role and content
        """
        if isinstance(raw_message, dict):
            return MessageRoleAndContent(
                role=raw_message.get("role"), content=raw_message.get("content")
            )
        return MessageRoleAndContent(
            role=getattr(raw_message, "role", None),
            content=getattr(raw_message, "content", None),
        )
