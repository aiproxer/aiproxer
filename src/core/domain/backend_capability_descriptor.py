from __future__ import annotations

from typing import Any, Literal

from pydantic import BaseModel, Field, ValidationError

ProtocolFamily = Literal["openai", "anthropic", "gemini"]


class BackendCapabilityDescriptor(BaseModel):
    """Typed capability descriptor for a backend instance.

    Declared in config under each backend's capability_descriptor key.
    Routing and validation read these flags instead of inferring from
    implicit backend attributes or hard-coded provider names.
    """

    protocol_family: ProtocolFamily = Field(
        default="openai",
        description="Wire protocol family this backend speaks",
    )
    supports_streaming: bool = Field(
        default=True,
        description="Backend supports SSE streaming responses",
    )
    supports_tool_calls: bool = Field(
        default=True,
        description="Backend supports tool/function calling",
    )
    supports_vision: bool = Field(
        default=False,
        description="Backend accepts image inputs",
    )
    supports_json_mode: bool = Field(
        default=False,
        description="Backend supports structured JSON output mode",
    )
    max_context_tokens: int | None = Field(
        default=None,
        description="Maximum context window in tokens (None = unknown)",
    )
    is_oauth_based: bool = Field(
        default=False,
        description="Backend authenticates through an OAuth/OIDC flow",
    )
    requires_personal_auth: bool = Field(
        default=False,
        description="Backend requires personal user credentials and is unsafe in shared deployments",
    )

    @classmethod
    def from_dict(cls, data: dict) -> BackendCapabilityDescriptor:
        return cls.model_validate(data)


def get_declared_capability_descriptor(
    source: Any,
) -> BackendCapabilityDescriptor | None:
    """Read a capability descriptor declared on config or a connector class."""
    if source is None:
        return None
    raw = getattr(source, "capability_descriptor", None)
    if raw is None:
        return None
    if isinstance(raw, BackendCapabilityDescriptor):
        return raw
    if isinstance(raw, dict):
        try:
            return BackendCapabilityDescriptor.from_dict(raw)
        except (ValidationError, TypeError, ValueError):
            return None
    return None


def source_is_oauth_based(source: Any) -> bool:
    """Return True when *source* declares ``is_oauth_based``."""
    descriptor = get_declared_capability_descriptor(source)
    return descriptor is not None and descriptor.is_oauth_based


def source_requires_personal_auth(source: Any) -> bool:
    """Return True when *source* declares ``requires_personal_auth``."""
    descriptor = get_declared_capability_descriptor(source)
    return descriptor is not None and descriptor.requires_personal_auth
