from collections.abc import Sequence
from typing import Any, TypeVar

from pydantic import ConfigDict, Field, field_validator, model_validator

from src.core.domain.base import ValueObject
from src.core.domain.usage_summary import UsageSummary
from src.core.interfaces.model_bases import DomainModel

# Define a type variable for generic methods
T = TypeVar("T", bound=DomainModel)


# For multimodal content parts
class MessageContentPartText(DomainModel):
    """Represents a text content part in a multimodal message."""

    model_config = ConfigDict(extra="forbid")

    type: str = "text"
    text: str
    cache_control: dict[str, Any] | None = Field(default=None, exclude=True)

    def model_dump(self, **kwargs: Any) -> dict[str, Any]:
        """Override to include cache_control only when set."""
        result = super().model_dump(**kwargs)
        if self.cache_control is not None:
            result["cache_control"] = self.cache_control
        return result


class ImageURL(DomainModel):
    """Specifies the URL and optional detail for an image in a multimodal message."""

    # Should be a data URI (e.g., "data:image/jpeg;base64,...") or public URL
    url: str
    detail: str | None = Field(None, examples=["auto", "low", "high"])


class MessageContentPartImage(DomainModel):
    """Represents an image content part in a multimodal message."""

    model_config = ConfigDict(extra="forbid")

    type: str = "image_url"
    image_url: ImageURL
    cache_control: dict[str, Any] | None = Field(default=None, exclude=True)

    def model_dump(self, **kwargs: Any) -> dict[str, Any]:
        """Override to include cache_control only when set."""
        result = super().model_dump(**kwargs)
        if self.cache_control is not None:
            result["cache_control"] = self.cache_control
        return result


class InputAudio(DomainModel):
    """Specifies the audio data for an audio input in a multimodal message."""

    data: str  # Base64-encoded audio data
    format: str  # Audio format (wav, mp3, etc.)


class MessageContentPartAudio(DomainModel):
    """Represents an audio content part in a multimodal message."""

    type: str = "input_audio"
    input_audio: InputAudio


# Extended multimodal types for text, image, and audio
MessageContentPart = (
    MessageContentPartText | MessageContentPartImage | MessageContentPartAudio
)
"""Type alias for possible content parts in a multimodal message."""


class FunctionCall(DomainModel):
    """Represents a function call within a tool call."""

    model_config = ConfigDict(extra="allow")

    name: str | None = None
    arguments: str | None = None


class ToolCall(DomainModel):
    """Represents a tool call in a chat completion response."""

    model_config = ConfigDict(
        # Allow extra fields for backward compatibility
        extra="allow",
    )

    id: str | None = None
    type: str = "function"
    function: FunctionCall

    # Extra content for provider-specific metadata (e.g., Gemini thought_signature)
    # We don't use Field(exclude=True) because we want to include it when it has a value
    extra_content: dict[str, Any] | None = None

    def model_dump(self, **kwargs: Any) -> dict[str, Any]:
        """Override to exclude extra_content when None for backward compatibility."""
        # Get the default serialization
        result = super().model_dump(**kwargs)
        # Remove extra_content if it's None to maintain backward compatibility
        # with code that doesn't expect this field
        if result.get("extra_content") is None:
            result.pop("extra_content", None)
        return result


class FunctionDefinition(DomainModel):
    """Represents a function definition for tool calling."""

    name: str
    description: str | None = None
    parameters: dict[str, Any] | None = None
    strict: bool | None = None  # OpenAI strict mode for function parameters


def _coerce_properties_list(items: list[Any]) -> dict[str, Any] | None:
    mapped: dict[str, Any] = {}
    for item in items:
        if isinstance(item, dict):
            key = item.get("key") or item.get("name")
            if "value" in item:
                value = item.get("value")
            else:
                value = item.get("schema")
        elif isinstance(item, list | tuple) and len(item) == 2:
            key, value = item
        else:
            return None
        if not isinstance(key, str) or not key:
            return None
        mapped[key] = value
    return mapped


def _normalize_schema_properties(schema: Any) -> Any:
    if isinstance(schema, dict):
        normalized: dict[str, Any] = {}
        for key, value in schema.items():
            if key == "properties" and isinstance(value, list):
                coerced = _coerce_properties_list(value)
                if coerced is not None:
                    normalized[key] = {
                        prop_key: _normalize_schema_properties(prop_val)
                        for prop_key, prop_val in coerced.items()
                    }
                else:
                    normalized[key] = {}
                continue
            normalized[key] = _normalize_schema_properties(value)
        return normalized
    if isinstance(schema, list):
        return [_normalize_schema_properties(item) for item in schema]
    return schema


def _normalize_tool_schema(tool: dict[str, Any]) -> dict[str, Any]:
    tool_copy = dict(tool)
    function = tool_copy.get("function")
    if isinstance(function, dict):
        params = function.get("parameters")
        if isinstance(params, dict):
            function_copy = dict(function)
            function_copy["parameters"] = _normalize_schema_properties(params)
            tool_copy["function"] = function_copy
        return tool_copy
    if isinstance(tool_copy.get("parameters"), dict):
        tool_copy["parameters"] = _normalize_schema_properties(tool_copy["parameters"])
    return tool_copy


class ToolDefinition(DomainModel):
    """Represents a tool definition in a chat completion request."""

    type: str = "function"
    function: FunctionDefinition

    @field_validator("function", mode="before")
    @classmethod
    def ensure_function_is_dict(cls, v: Any) -> dict[str, Any] | FunctionDefinition:
        # Accept either a FunctionDefinition or a ToolDefinition/FunctionDefinition instance
        # and normalize to a dict for ChatRequest validation
        if isinstance(v, FunctionDefinition):
            return v.model_dump()
        # If v is already a dict, return it as is
        if isinstance(v, dict):
            return v
        # If v is something else, try to convert it to a dict
        # This should handle cases where v is a dict-like object
        try:
            return dict(v)  # type: ignore
        except (TypeError, ValueError):
            # If we can't convert to dict, raise a ValueError to let Pydantic handle the error properly
            raise ValueError(f"Cannot convert {type(v)} to dict or FunctionDefinition")


class ChatMessage(DomainModel):
    """
    A chat message in a conversation.
    """

    role: str
    content: str | Sequence[MessageContentPart] | None = None
    reasoning_content: str | None = None
    name: str | None = None
    tool_calls: list[ToolCall] | None = None
    tool_call_id: str | None = None
    metadata: dict[str, Any] | None = None

    @model_validator(mode="before")
    @classmethod
    def map_reasoning_fields(cls, data: Any) -> Any:
        """Map alternative field names to standard ChatMessage fields."""
        if not isinstance(data, dict):
            return data

        # Map reasoning fields
        if "reasoning_content" not in data:
            if "reasoning" in data:
                data["reasoning_content"] = data["reasoning"]
            elif "reasoning_details" in data:
                data["reasoning_content"] = data["reasoning_details"]

        # Map alternative field names
        if "content" not in data:
            if "text" in data:
                data["content"] = data["text"]
            elif "message" in data and isinstance(data["message"], dict):
                msg_data = data["message"]
                if "role" in msg_data and "role" not in data:
                    data["role"] = msg_data["role"]
                if "content" in msg_data:
                    data["content"] = msg_data["content"]

        if "role" not in data and "type" in data:
            # Map type to role for some legacy formats
            if data["type"] in ["user", "assistant", "system"]:
                data["role"] = data["type"]
            if "content" not in data:
                data["content"] = data.get("text", "")

        # Default role to 'user' if still missing but we have content
        if "role" not in data and ("content" in data or "text" in data):
            data["role"] = "user"

        return data

    def to_dict(self) -> dict[str, Any]:
        """Convert the message to a dictionary."""
        result: dict[str, Any] = {"role": self.role}
        if self.content is not None:
            result["content"] = self._serialize_content(self.content)
        if self.reasoning_content is not None:
            result["reasoning_content"] = self.reasoning_content
        if self.name:
            result["name"] = self.name
        if self.tool_calls:
            serialized_tool_calls: list[Any] = []
            for tool_call in self.tool_calls:
                dumper = getattr(tool_call, "model_dump", None)
                if callable(dumper):
                    serialized_tool_calls.append(dumper())
                elif isinstance(tool_call, dict):
                    # ``model_copy(update=...)`` bypasses validation, so callers
                    # (e.g. MCP XML extraction) may inject plain dicts here.
                    serialized_tool_calls.append(dict(tool_call))
                else:
                    serialized_tool_calls.append(tool_call)
            result["tool_calls"] = serialized_tool_calls
        if self.tool_call_id:
            result["tool_call_id"] = self.tool_call_id
        return result

    @staticmethod
    def _serialize_content(
        content: str | Sequence[MessageContentPart] | DomainModel | None,
    ) -> Any:
        """Normalize message content so downstream callers receive plain data structures."""

        if content is None:
            return None

        if isinstance(content, str):
            return content

        if isinstance(content, DomainModel):
            return content.model_dump()

        # content is Sequence[MessageContentPart]
        return [part.model_dump() for part in content]


class ChatRequest(ValueObject):
    """
    A request for a chat completion.
    """

    model: str
    messages: list[ChatMessage]
    system_prompt: str | None = None  # Add system_prompt field
    temperature: float | None = None
    top_p: float | None = None
    top_k: int | None = None
    n: int | None = None
    stream: bool | None = None
    stop: list[str] | str | None = None
    max_tokens: int | None = None  # Deprecated, use max_completion_tokens
    max_completion_tokens: int | None = None  # OpenAI standard token limit
    presence_penalty: float | None = None
    frequency_penalty: float | None = None
    logit_bias: dict[str, float] | None = None
    logprobs: bool | None = None  # Whether to return log probabilities
    top_logprobs: int | None = None  # Number of most likely tokens (0-20)
    user: str | None = None
    seed: int | None = None
    tools: list[dict[str, Any]] | None = None
    tool_choice: str | dict[str, Any] | None = None
    parallel_tool_calls: bool | None = None  # Enable parallel function calling
    response_format: dict[str, Any] | None = None  # Structured output format
    service_tier: str | None = (
        None  # OpenAI service tier (auto, default, flex, priority)
    )
    store: bool | None = None  # Store for distillation/evals (OpenAI API parity)
    request_metadata: dict[str, str] | None = (
        None  # Key-value metadata (OpenAI API parity)
    )
    prediction: dict[str, Any] | None = None  # Predicted output optimization
    modalities: list[str] | None = None  # Output modalities (text, audio)
    audio: dict[str, Any] | None = None  # Audio output configuration
    session_id: str | None = None
    agent: str | None = None  # Add agent field
    extra_body: dict[str, Any] | None = None
    compression_diagnostics: dict[str, Any] | None = Field(
        default=None,
        exclude=True,
    )
    vtc_enabled: bool | None = None  # Virtual Tool Calling mode for Cline-like clients
    # When set, overrides AppConfig.session.streaming_loop_detection_enabled for the
    # ports-layer streaming pipeline (connector integrate_streaming_pipeline path).
    streaming_loop_detection_enabled: bool | None = None

    # Reasoning parameters for o1, o3, o4-mini and other reasoning models
    reasoning_effort: str | None = None
    reasoning: dict[str, Any] | None = None

    # Output verbosity for GPT-5 family models (low|medium|high)
    verbosity: str | None = None

    # Gemini-specific reasoning parameters
    thinking_budget: int | None = None
    generation_config: dict[str, Any] | None = None

    @field_validator("messages")
    @classmethod
    def validate_messages(cls, v: list[Any]) -> list[ChatMessage]:
        """Validate and convert messages."""
        if not v:
            raise ValueError("At least one message is required")
        return [m if isinstance(m, ChatMessage) else ChatMessage(**m) for m in v]

    @field_validator("tools", mode="before")
    @classmethod
    def validate_tools(cls, v: Any) -> list[dict[str, Any]] | None:
        """Allow passing ToolDefinition instances or dicts for tools."""
        if v is None:
            return None
        result: list[dict[str, Any]] = []
        for item in v:
            if isinstance(item, ToolDefinition):
                result.append(_normalize_tool_schema(item.model_dump()))
            elif isinstance(item, dict):
                result.append(_normalize_tool_schema(item))
            else:
                # Attempt to coerce
                try:
                    td = ToolDefinition(**item)
                    result.append(_normalize_tool_schema(td.model_dump()))
                except Exception as e:
                    from src.core.common.exceptions import ToolCallParsingError

                    raise ToolCallParsingError(
                        message="Invalid tool definition provided",
                        details={"original_error": str(e), "invalid_item": str(item)},
                    ) from e
        return result


class ChatCompletionChoiceMessage(DomainModel):
    """Represents the message content within a chat completion choice."""

    role: str
    content: str | None = None
    reasoning_content: str | None = None
    tool_calls: list[ToolCall] | None = None
    tool_call_id: str | None = None
    refusal: str | None = None  # Model refusal message (OpenAI API parity)
    annotations: list[dict[str, Any]] | None = None  # Response annotations
    metadata: dict[str, Any] | None = None


class ChatCompletionChoice(DomainModel):
    """Represents a single choice in a chat completion response."""

    index: int
    message: ChatCompletionChoiceMessage
    finish_reason: str | None = None
    logprobs: dict[str, Any] | None = None  # Log probability information


# ChatUsage class is defined elsewhere in this file


class ChatResponse(ValueObject):
    """
    A response from a chat completion.
    """

    id: str
    created: int
    model: str
    choices: list[ChatCompletionChoice]
    usage: UsageSummary | None = None
    system_fingerprint: str | None = None
    service_tier: str | None = None  # Actual service tier used for the request
    object: str = "chat.completion"


class StreamingChatResponse(ValueObject):
    """
    A streaming chunk of a chat completion response.
    """

    content: str | None
    model: str
    finish_reason: str | None = None
    tool_calls: list[dict[str, Any]] | None = None
    delta: dict[str, Any] | None = None
    system_fingerprint: str | None = None
    done: bool | None = None
    metadata: dict[str, Any] | None = None

    @classmethod
    def from_legacy_chunk(cls, chunk: dict[str, Any]) -> "StreamingChatResponse":
        """
        Create a StreamingChatResponse from a legacy chunk format.

        Args:
            chunk: A legacy streaming chunk

        Returns:
            A new StreamingChatResponse
        """
        # Extract the response content and other fields from the chunk
        content: str | None = None
        if chunk.get("choices"):
            choice: dict[str, Any] = chunk["choices"][0]
            if "delta" in choice:
                delta: dict[str, Any] = choice["delta"]
                if "content" in delta:
                    content = delta["content"]

                # Might have tool calls in delta
                tool_calls: list[dict[str, Any]] | None = delta.get("tool_calls")

                # The delta is the actual delta object
                delta_obj: dict[str, Any] | None = delta
            else:
                # Simpler format
                content = choice.get("text", "")
                tool_calls = None
                delta_obj = None

            # Extract finish reason if present
            finish_reason: str | None = choice.get("finish_reason")
        else:
            # Anthropic format
            if "content" in chunk:
                if isinstance(chunk["content"], list):
                    content_parts: list[str] = [
                        p["text"] for p in chunk["content"] if p.get("type") == "text"
                    ]
                    content = "".join(content_parts)
                else:
                    content = chunk["content"]

            tool_calls = chunk.get("tool_calls")
            delta_obj = None
            finish_reason = chunk.get("stop_reason")

        # Extract model
        model: str = chunk.get("model", "unknown")

        # Extract system fingerprint
        system_fingerprint: str | None = chunk.get("system_fingerprint")

        return cls(
            content=content,
            model=model,
            finish_reason=finish_reason,
            tool_calls=tool_calls,
            delta=delta_obj,
            system_fingerprint=system_fingerprint,
        )


# ChatUsage class is defined elsewhere in this file


class CanonicalChatRequest(ChatRequest):
    """
    A canonical chat request model that is used internally throughout the application.
    """


class CanonicalChatResponse(ChatResponse):
    """
    A canonical chat response model that is used internally throughout the application.
    """


class StreamingFunctionCall(DomainModel):
    """Represents a partial function call within a streaming tool call."""

    model_config = ConfigDict(extra="allow")

    name: str | None = None
    arguments: str | None = None


class StreamingToolCall(DomainModel):
    """Represents a partial tool call in a streaming chat completion chunk."""

    model_config = ConfigDict(extra="allow")

    index: int
    id: str | None = None
    type: str | None = "function"
    function: StreamingFunctionCall | None = None

    def __getitem__(self, key: str) -> Any:
        """Support dict-style access for extra fields."""
        try:
            return getattr(self, key)
        except AttributeError:
            raise KeyError(key) from None

    def get(self, key: str, default: Any = None) -> Any:
        """Support dict-style get() for extra fields."""
        try:
            return getattr(self, key)
        except AttributeError:
            return default


class StreamingChatCompletionChoiceDelta(DomainModel):
    """Represents the delta content within a streaming chat completion choice."""

    model_config = ConfigDict(extra="allow")

    role: str | None = None
    content: str | None = None
    reasoning_content: str | None = None
    reasoning: Any = None
    reasoning_summary: Any = None
    tool_calls: list[StreamingToolCall] | None = None
    refusal: str | None = None

    def __getitem__(self, key: str) -> Any:
        """Support dict-style access for extra fields."""
        try:
            return getattr(self, key)
        except AttributeError:
            raise KeyError(key) from None

    def __setitem__(self, key: str, value: Any) -> None:
        """Support dict-style assignment for extra fields."""
        setattr(self, key, value)

    def __contains__(self, key: str) -> bool:
        """Support 'in' operator for checking field existence."""
        return hasattr(self, key)


class StreamingChatCompletionChoice(DomainModel):
    """Represents a single choice in a streaming chat completion response."""

    index: int
    delta: StreamingChatCompletionChoiceDelta
    finish_reason: str | None = None
    logprobs: dict[str, Any] | None = None  # Log probability information


class CanonicalStreamChunk(ValueObject):
    """
    A canonical streaming chunk model that is used internally throughout the application.
    """

    id: str | None = None
    object: str = "chat.completion.chunk"
    created: int | None = None
    model: str | None = None
    choices: list[StreamingChatCompletionChoice]
    usage: UsageSummary | None = None
    system_fingerprint: str | None = None
