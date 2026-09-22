"""Service interfaces for OpenAI Codex connector.

This module defines abstract interfaces for connector components following
the dependency inversion principle. All interfaces extend ABC and document
preconditions and postconditions.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from collections.abc import Mapping
from pathlib import Path
from typing import Any, Protocol

from src.connectors._openai_codex_capabilities import CodexClientCapabilities
from src.connectors.contracts import ConnectorRequestContext
from src.connectors.openai_codex.contracts import (
    CodexConnectorSettings,
    CodexInputItem,
    CodexPayload,
    CodexRequestContext,
    CodexToolSchema,
    CompatibilityResult,
    CompatibilityState,
    ProcessedMessage,
    ProviderStreamChunk,
    ToolArguments,
    ToolCall,
    ToolExecutionResult,
)
from src.core.config.app_config import AppConfig
from src.core.domain.openai_function_schema import OpenAIFunctionSchema
from src.core.domain.responses import (
    ResponseEnvelope,
    StreamingResponseEnvelope,
    StreamingResponseHandle,
)
from src.core.domain.validation import ValidationResult


class ISettingsLoader(ABC):
    """Interface for loading and normalizing connector settings.

    Preconditions:
    - app_config is validated by core config loader

    Postconditions:
    - Settings include defaults and environment overrides
    - Normalized types are applied
    """

    @abstractmethod
    def load(self, app_config: AppConfig) -> CodexConnectorSettings:
        """Return normalized settings for the connector.

        Args:
            app_config: Application configuration

        Returns:
            Normalized connector settings with defaults and overrides applied
        """
        ...


class ICredentialManager(ABC):
    """Interface for managing credential lifecycle and concurrency.

    Preconditions:
    - initialize called once per connector lifecycle

    Postconditions:
    - Refresh never leaves credentials in a partially written state
    - Shutdown ensures watcher is stopped and is_watcher_running() returns False
    """

    @abstractmethod
    async def initialize(
        self, auth_path: Path | None, *, start_watcher: bool = True
    ) -> None:
        """Load initial credentials and optionally start the file watcher.

        Args:
            auth_path: Optional path to auth.json file
            start_watcher: When True (default), start the credential file
                watcher. Catalog discovery passes False so it does not spawn a
                long-lived watcher during startup.
        """
        ...

    @abstractmethod
    async def refresh_access_token(self) -> bool:
        """Refresh the access token in a concurrency-safe manner.

        Returns:
            True if refresh succeeded, False otherwise
        """
        ...

    @abstractmethod
    def get_access_token(self) -> str | None:
        """Return current access token if available.

        Returns:
            Access token string or None if not available
        """
        ...

    @abstractmethod
    async def shutdown(self) -> None:
        """Stop the file watcher and release resources.

        This method ensures clean shutdown by:
        - Stopping the credential file watcher
        - Cancelling any pending reload tasks
        - Releasing concurrency locks

        Safe to call multiple times; subsequent calls are no-ops.
        """
        ...

    @abstractmethod
    def is_watcher_running(self) -> bool:
        """Return True if the credential file watcher is active.

        Returns:
            True if watcher is running, False otherwise
        """
        ...

    @abstractmethod
    async def reload_credentials(self, force: bool = False) -> bool:
        """Reload credentials from file, optionally bypassing cache.

        This method allows external components (e.g., file watchers) to trigger
        credential reloads without accessing private implementation details.

        Args:
            force: If True, bypass cache and force reload from file even if
                file modification time hasn't changed

        Returns:
            True if reload succeeded, False otherwise
        """
        ...

    @abstractmethod
    def validate_current_credentials(self) -> ValidationResult:
        """Validate currently loaded credentials structure.

        This method validates the credentials that are currently in memory,
        without reloading from file. Useful for checking credentials after
        a reload operation.

        Returns:
            ValidationResult with success/error status. If no credentials
            are loaded, returns a failure result.
        """
        ...

    @abstractmethod
    def get_account_id(self) -> str | None:
        """Return ChatGPT account ID from loaded credentials.

        Returns:
            Account ID string or None if not available
        """
        ...

    @abstractmethod
    async def list_managed_oauth_account_ids(self) -> list[str]:
        """Return managed OAuth account IDs for fan-out usage-window warm-up calls.

        When at least one account is not on local rate-limit cooldown, return only
        those. When the whole pool is on cooldown, implementations may return every
        otherwise-usable id so warm-up can still probe with local limits ignored.
        """
        ...


class IPayloadBuilder(ABC):
    """Interface for building Codex payloads.

    Preconditions:
    - CodexRequestContext contains resolved model and messages

    Postconditions:
    - Payload is compatible with Codex Responses API
    - Passthrough rules are preserved
    """

    @abstractmethod
    def build_payload(self, context: CodexRequestContext) -> CodexPayload:
        """Build a Codex payload preserving passthrough rules.

        Args:
            context: Request context with processed messages and capabilities

        Returns:
            Codex API payload ready for submission
        """
        ...

    @abstractmethod
    def convert_dict_to_payload(
        self, payload_dict: dict[str, Any], context: CodexRequestContext
    ) -> CodexPayload:
        """Convert dictionary payload to CodexPayload model.

        This method handles passthrough format conversion, ensuring that
        dictionary payloads (e.g., from passthrough requests) are properly
        converted to CodexPayload instances with correct field types.

        Preconditions:
        - payload_dict contains valid Codex payload fields
        - context provides necessary metadata for conversion

        Postconditions:
        - Returns validated CodexPayload instance
        - Input items, tools, and other fields are properly converted

        Args:
            payload_dict: Dictionary containing Codex payload fields
            context: Request context for conversion metadata

        Returns:
            Validated CodexPayload instance
        """
        ...


class ICodexContinuationCoordinator(ABC):
    """Interface for Codex continuation lineage handling.

    This is an internal contract for future delta-path orchestration.
    """

    @abstractmethod
    async def resolve_previous_response_id(
        self, context: CodexRequestContext
    ) -> str | None:
        """Return the upstream response id to continue from, if any."""
        ...

    @abstractmethod
    async def record_response_id(
        self, context: CodexRequestContext, response_id: str
    ) -> None:
        """Persist the latest upstream response id for later turns."""
        ...

    @abstractmethod
    async def invalidate(
        self, context: CodexRequestContext, *, reason: str | None = None
    ) -> None:
        """Clear continuation lineage for the given context."""
        ...


class IRequestTranslator(ABC):
    """Interface for translating canonical requests to Codex format.

    Preconditions:
    - Messages are pre-processed and validated

    Postconditions:
    - Output items match current Codex input schema
    - Parameter transformation semantics are preserved
    """

    @abstractmethod
    def translate_messages(
        self,
        messages: list[ProcessedMessage],
        context: CodexRequestContext | None = None,
    ) -> list[CodexInputItem]:
        """Convert processed messages to Codex input items.

        Args:
            messages: List of processed messages
            context: Optional request context for environment context and capabilities

        Returns:
            List of Codex input items
        """
        ...

    @abstractmethod
    def translate_tool_calls(self, tool_calls: list[ToolCall]) -> list[CodexInputItem]:
        """Convert tool calls to Codex function_call input items.

        Args:
            tool_calls: List of tool calls

        Returns:
            List of Codex input items representing function calls
        """
        ...


class IPromptResolver(ABC):
    """Interface for resolving and sanitizing system prompts.

    Preconditions:
    - Settings and capabilities are validated

    Postconditions:
    - Prompt content is sanitized and ready for API submission
    """

    @abstractmethod
    def resolve_system_prompt(
        self,
        settings: CodexConnectorSettings,
        capabilities: CodexClientCapabilities,
    ) -> str:
        """Return the resolved system prompt for the request.

        Args:
            settings: Connector settings
            capabilities: Client capabilities

        Returns:
            Resolved and sanitized system prompt
        """
        ...

    @abstractmethod
    def resolve_instructions(
        self,
        settings: CodexConnectorSettings,
        user_instructions: str | None,
    ) -> str | None:
        """Return merged instructions or None if not applicable.

        Args:
            settings: Connector settings
            user_instructions: Optional user-provided instructions

        Returns:
            Merged instructions or None
        """
        ...


class IToolSchemaResolver(ABC):
    """Interface for resolving tool schemas and handling collisions.

    Preconditions:
    - Request context contains tool definitions

    Postconditions:
    - Tool schemas are merged with collision handling
    - Output matches current tool schema merge behavior
    """

    @abstractmethod
    def resolve_tool_schema(
        self, context: CodexRequestContext
    ) -> list[CodexToolSchema]:
        """Resolve tool schemas and handle collisions.

        Args:
            context: Request context

        Returns:
            List of resolved tool schemas
        """
        ...


class IResponseExecutor(ABC):
    """Interface for executing Codex API requests with retry.

    Preconditions:
    - Payload and headers are already validated

    Postconditions:
    - Error shapes and statuses match current behavior
    - Streaming retry behavior is preserved
    """

    @abstractmethod
    async def execute(
        self, payload: CodexPayload, context: CodexRequestContext
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        """Execute Codex request with retry and compatibility handling.

        Args:
            payload: Codex API payload
            context: Request context

        Returns:
            Response envelope (streaming or non-streaming)
        """
        ...


class ICompatibilityLayer(ABC):
    """Interface for handling KiloCode and Droid compatibility flows.

    Preconditions:
    - Request context has processed messages

    Postconditions:
    - Tool translations and results match current behavior
    - Cleanup ensures state resources are released
    """

    @abstractmethod
    async def apply(self, context: CodexRequestContext) -> CompatibilityResult:
        """Detect and translate compatibility tool calls.

        Args:
            context: Request context

        Returns:
            Compatibility result with tool lists and state
        """
        ...

    @abstractmethod
    async def translate_stream_chunk(
        self, chunk: ProviderStreamChunk, state: CompatibilityState
    ) -> ProviderStreamChunk:
        """Apply streaming tool-call translations with owned state.

        Args:
            chunk: Provider stream chunk
            state: Per-request compatibility state

        Returns:
            Translated stream chunk
        """
        ...

    @abstractmethod
    async def cleanup_state(self, state: CompatibilityState) -> None:
        """Release per-request state after streaming completes or on error.

        This method MUST be called after streaming ends (success or failure) to:
        - Clear tool-call caches and translation buffers
        - Release any pending tool-call references
        - Reset detection flags

        The state object should not be reused after cleanup.

        Args:
            state: Compatibility state to clean up
        """
        ...

    @abstractmethod
    def create_state(self) -> CompatibilityState:
        """Create a new per-request compatibility state instance.

        Returns a fresh state object for tracking compatibility flows
        during a single request lifecycle.

        Returns:
            New compatibility state instance
        """
        ...

    @abstractmethod
    def detect_incompatible_tool_calls(
        self,
        tool_calls: list[dict[str, object]],
        context: CodexRequestContext,
    ) -> list[str]:
        """Return incompatible tool names that should be rejected server-side."""
        ...

    @abstractmethod
    def append_incompatible_tool_steering(
        self,
        payload_dict: dict[str, object],
        incompatible_tool_names: list[str],
        context: CodexRequestContext,
    ) -> dict[str, object]:
        """Append family-specific steering for incompatible tool retry."""
        ...


class IToolExecutionService(ABC):
    """Interface for executing proxy-side tools (MCP runs in the agent, not here)."""

    @abstractmethod
    async def execute_proxy_tool(
        self, tool_name: str, arguments: ToolArguments, session_id: str | None = None
    ) -> ToolExecutionResult:
        """Execute a proxy tool and return formatted result.

        Args:
            tool_name: Name of the tool to execute
            arguments: Tool arguments
            session_id: Optional session ID for telemetry and conversation control

        Returns:
            Tool execution result with success/error status
        """
        ...

    @abstractmethod
    def get_available_tool_schemas(self) -> list[OpenAIFunctionSchema]:
        """Get schemas for tools advertised by this service (may be empty)."""

        ...


# Protocol definitions for compatibility layer collaborators
# These define the public boundary for test substitution (Requirement 2.4, 9.3, 9.6)


class ISessionDetectionResult(Protocol):
    """Protocol for session detection result."""

    is_kilocode: bool
    detection_method: str
    confidence: float


class ISessionDetector(Protocol):
    """Protocol for KiloCode session detector.

    Public boundary for session detection used by compatibility layer.
    """

    async def detect(
        self,
        *,
        request_data: object,
        metadata: Mapping[str, object] | None,
        session_id: str,
        backend: str,
    ) -> ISessionDetectionResult:
        """Detect if request is from KiloCode client.

        Args:
            request_data: Request data object
            metadata: Optional request metadata
            session_id: Session identifier
            backend: Backend type identifier

        Returns:
            Detection result with is_kilocode flag and metadata
        """
        ...


class IDroidDetectionResult(Protocol):
    """Protocol for Droid detection result."""

    is_droid: bool
    detection_method: str
    confidence: float


class IDroidDetector(Protocol):
    """Protocol for Droid session detector.

    Public boundary for Droid detection used by compatibility layer.
    """

    def detect(
        self,
        *,
        headers: Mapping[str, str] | None,
        messages: list[Mapping[str, object]] | None,
        tools: list[Mapping[str, object]] | None,
    ) -> IDroidDetectionResult:
        """Detect if request is from Droid client.

        Args:
            headers: HTTP headers
            messages: Request messages
            tools: Request tools

        Returns:
            Detection result with is_droid flag and metadata
        """
        ...


class IKiloTranslationResult(Protocol):
    """Protocol for KiloCode tool translation result."""

    tool_name: str
    arguments: Mapping[str, object]


class IKiloToolTranslator(Protocol):
    """Protocol for KiloCode tool translator.

    Public boundary for tool translation used by compatibility layer.
    """

    async def translate_tool_invocation(
        self, xml_text: str, session_id: str | None = None
    ) -> IKiloTranslationResult | None:
        """Translate XML tool invocation to Codex format.

        Args:
            xml_text: XML text containing tool invocation
            session_id: Optional session identifier

        Returns:
            Translation result with tool_name and arguments, or None if translation fails
        """
        ...

    def get_xml_parser(self) -> Any | None:
        """Return the XML parser instance.

        Returns:
            XMLToolParser instance or None if not yet initialized
        """
        ...

    def ensure_xml_parser(self) -> Any:
        """Return an initialized XML parser instance."""
        ...


class IDroidReverseTranslationResult(Protocol):
    """Protocol for Droid reverse translation result."""

    droid_tool_name: str
    droid_arguments: Mapping[str, object]


class IDroidToolTranslator(Protocol):
    """Protocol for Droid tool translator.

    Public boundary for Droid translation used by compatibility layer.
    """

    def translate_codex_to_droid(
        self, codex_tool_name: str, codex_arguments: Mapping[str, object]
    ) -> IDroidReverseTranslationResult:
        """Translate Codex tool name/arguments to Droid format.

        Args:
            codex_tool_name: Codex tool name
            codex_arguments: Codex tool arguments

        Returns:
            Translation result with droid_tool_name and droid_arguments
        """
        ...


class ICodexTransport(Protocol):
    """Protocol for Codex streaming transport.

    Public boundary for streaming HTTP requests used by response executor.
    """

    async def initiate_streaming_request(
        self,
        url: str,
        payload: dict[str, Any],
        headers: dict[str, str],
        session_id: str,
        *,
        context: ConnectorRequestContext | None = None,
        backend: str = "openai-codex",
        model: str = "unknown",
        key_name: str | None = None,
    ) -> StreamingResponseHandle:
        """Initiate a streaming request to Codex API.

        Args:
            url: Codex API endpoint URL
            payload: Request payload as dictionary
            headers: HTTP headers including Authorization
            session_id: Session identifier for logging and cancellation
            context: Optional proxy request context for wire capture
            backend: Backend key used for websocket capture metadata
            model: Effective model name for websocket capture metadata
            key_name: Optional capture key name override

        Returns:
            StreamingResponseHandle with iterator and cancel callback

        Raises:
            HTTPException: For 4xx/5xx responses
            ServiceUnavailableError: For network failures
            AuthenticationError: For missing credentials
        """
        ...
