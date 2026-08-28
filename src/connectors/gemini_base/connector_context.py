"""
Connector context interface for ChatRequestPreparer.

This module defines a narrow interface that captures only what the
ChatRequestPreparer actually needs, avoiding tight coupling to the
full connector class.
"""

from typing import TYPE_CHECKING, Any, Protocol, runtime_checkable

if TYPE_CHECKING:
    from src.core.domain.chat import CanonicalChatRequest


@runtime_checkable
class IConnectorContext(Protocol):
    """Narrow interface for ChatRequestPreparer dependencies.

    This protocol defines the minimal contract needed by ChatRequestPreparer,
    avoiding coupling to the full connector class. This enables:
    - Testing with mock implementations
    - Reuse across different connector types
    - Clear dependency boundaries
    """

    @property
    def _oauth_credentials(self) -> dict[str, Any] | None:
        """Get current OAuth credentials."""
        ...

    async def _refresh_token_if_needed(
        self,
        *,
        force_reload: bool = False,
        session_id: str | None = None,
        retry_after_seconds: float | None = None,
    ) -> bool:
        """Ensure a valid access token is available.

        Args:
            force_reload: If True, bypasses local caches and forces a reload/refresh.
            session_id: Optional session identifier for telemetry and affinity.
            retry_after_seconds: Optional explicit retry delay suggested by the API.
        """
        ...

    async def record_rate_limit(self, *, retry_after_seconds: float | None) -> None:
        """Record a rate limit event for the current credentials.

        Args:
            retry_after_seconds: Optional explicit retry delay suggested by the API.
        """
        ...

    def _get_session_headers(self) -> dict[str, str]:
        """Get headers for AuthorizedSession requests."""
        ...

    async def _discover_project_id(self, auth_session: Any) -> str:
        """Discover the project ID for Code Assist API."""
        ...


@runtime_checkable
class IRequestCounter(Protocol):
    """Interface for request counting."""

    def increment(self) -> None:
        """Increment the request counter."""
        ...


@runtime_checkable
class IThoughtSignatureService(Protocol):
    """Interface for thought signature management."""

    def inject_signatures(
        self, canonical_request: "CanonicalChatRequest", session_id: str
    ) -> None:
        """Inject stored thought_signatures into tool_calls that are missing them."""
        ...

    def store_signatures_from_tool_calls(
        self, tool_calls: list[dict[str, Any]], session_id: str | None
    ) -> None:
        """Store thought_signatures from streaming tool call responses."""
        ...

    def log_signature_state(
        self,
        canonical_request: "CanonicalChatRequest",
        session_id: str,
        effective_model: str,
    ) -> None:
        """Log presence/absence of thought signatures on assistant tool calls."""
        ...

    def get_cached_signature(self, session_id: str, tool_call_id: str) -> str | None:
        """Return cached signature for a session/tool call pair."""
        ...


@runtime_checkable
class IMessageConverter(Protocol):
    """Interface for message conversion operations.

    Note: Method names use underscores to match existing connector implementation.
    This maintains backward compatibility while enabling interface-based testing.
    """

    def _convert_system_messages_for_code_assist(
        self, gemini_request: dict[str, Any]
    ) -> list[dict[str, Any]]:
        """Convert system messages for Code Assist API format."""
        ...

    def _build_code_assist_request(
        self, gemini_request: dict[str, Any], final_contents: list[dict[str, Any]]
    ) -> dict[str, Any]:
        """Build Code Assist API request from Gemini format."""
        ...

    def _sanitize_code_assist_tools(
        self,
        canonical_request: "CanonicalChatRequest",
        code_assist_request: dict[str, Any],
    ) -> None:
        """Sanitize tool definitions for Code Assist API."""
        ...


@runtime_checkable
class IPromptLimiter(Protocol):
    """Interface for prompt limit enforcement.

    Note: Method names use underscores to match existing connector implementation.
    """

    def _estimate_prompt_tokens(
        self, code_assist_request: dict[str, Any]
    ) -> int | None:
        """Estimate the number of prompt tokens in the request."""
        ...

    def _enforce_prompt_limit(
        self,
        prompt_tokens: int | None,
        effective_model: str,
        *,
        request_id: str | None = None,
    ) -> None:
        """Enforce prompt token limits, raising InvalidRequestError if exceeded."""
        ...


@runtime_checkable
class IRequestBodyBuilder(Protocol):
    """Interface for building Code Assist request bodies.

    Note: Method names use underscores to match existing connector implementation.
    """

    def _build_code_assist_request_body(
        self,
        effective_model: str,
        project_id: str,
        request_data: Any,
        code_assist_request: dict[str, Any],
    ) -> dict[str, Any]:
        """Build the final request body for the Code Assist API."""
        ...


__all__ = [
    "IConnectorContext",
    "IMessageConverter",
    "IPromptLimiter",
    "IRequestBodyBuilder",
    "IRequestCounter",
    "IThoughtSignatureService",
]
