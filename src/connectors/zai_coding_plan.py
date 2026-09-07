from __future__ import annotations

import asyncio
import contextlib
import hashlib
import json
import logging
import os
import re
import time
import uuid
from collections.abc import AsyncGenerator, Mapping
from dataclasses import replace
from typing import Any, cast

import httpx
from fastapi import HTTPException

from src.connectors.base import strip_vendor_prefix
from src.connectors.contracts import (
    ConnectorChatCompletionsRequest,
    ConnectorRequestContext,
)
from src.connectors.openai import OpenAIConnector
from src.core.common.env_utils import get_env_value_with_windows_persistent_fallback
from src.core.common.exceptions import (
    AuthenticationError,
    BackendError,
    RateLimitExceededError,
)
from src.core.common.logging_utils import redact_dict
from src.core.domain.chat import CanonicalChatRequest
from src.core.domain.model_utils import parse_model_backend
from src.core.domain.models_listing import ModelInfo, ModelsListingResponse
from src.core.domain.responses import (
    ResponseEnvelope,
    StreamingResponseEnvelope,
    StreamingResponseHandle,
)
from src.core.interfaces.configuration_interface import IAppIdentityConfig
from src.core.interfaces.response_processor_interface import ProcessedResponse
from src.core.services.backend_registry import backend_registry

logger = logging.getLogger(__name__)

_STREAM_HEADERS_OVERRIDE_KEY = "_llm_proxy_stream_headers"

# Pre-compiled regex pattern for MCP tool extraction (performance optimization)
# Module-level constant avoids recompiling on every message processing call
_MCP_TOOL_PATTERN = re.compile(
    r"<(?P<tag>[A-Za-z0-9_\-]+)\b[^>]*>.*?</(?P=tag)>",
    re.DOTALL,
)


class ZaiCodingPlanBackend(OpenAIConnector):
    """
    LLMBackend implementation for ZAI's coding plan API (OpenAI compatible).
    Uses the OpenAI-style API at https://api.z.ai/api/coding/paas/v4
    """

    backend_type: str = "zai-coding-plan"

    # ZAI coding plan serves multiple vendor models, models may already be prefixed
    VENDOR_PREFIX: str | None = None
    _DEFAULT_MODEL: str = "glm-4.6"
    _LEGACY_MODEL: str = "claude-sonnet-4-20250514"
    # Advertised when /models fails; merged ahead of API discovery; always honored in _select_model
    _SUPPORTED_MODELS: tuple[str, ...] = (
        "glm-5.1",
        "glm-5.0",
        _DEFAULT_MODEL,
        _LEGACY_MODEL,
    )
    _KNOWN_CODING_PLAN_MODELS: frozenset[str] = frozenset(_SUPPORTED_MODELS)
    _MODEL_DISCOVERY_SUCCESS_TTL_SECONDS: float = 300.0
    _MODEL_DISCOVERY_FAILURE_TTL_SECONDS: float = 60.0
    _MODEL_DISCOVERY_CACHE: dict[
        tuple[str, str], tuple[float, list[str], set[str], bool]
    ] = {}

    # Kilo-Code fingerprint constants
    _KILO_VERSION: str = "4.111.0"
    _KILO_USER_AGENT: str = f"Kilo-Code/{_KILO_VERSION}"

    _ALLOWED_OUTBOUND_HEADERS: frozenset[str] = frozenset(
        {
            "authorization",
            "content-type",
            "accept",
            "user-agent",
            "referer",
            "origin",
            "http-referer",
            "x-title",
            "x-kilocode-version",
        }
    )

    async def initialize(self, **kwargs: Any) -> None:
        """Initialize the ZAI Coding plan backend."""
        # Get API key from environment or kwargs
        fallback_api_key, fallback_api_key_source = (
            get_env_value_with_windows_persistent_fallback("ZAI_CODING_PLAN_API_KEY")
        )
        self.api_key = kwargs.get("api_key") or fallback_api_key

        if not self.api_key:
            raise AuthenticationError(
                message="ZAI_CODING_PLAN_API_KEY environment variable not set",
                code="missing_api_key",
            )

        # Log masked API key for verification (show first 4 and last 4 chars)
        masked_key = self._mask_api_key(self.api_key)
        if logger.isEnabledFor(logging.INFO):
            logger.info(
                "ZAI Coding Plan backend initialized with API key: %s (source=%s)",
                masked_key,
                "kwargs" if kwargs.get("api_key") else fallback_api_key_source,
            )

        # Set the OpenAI-compatible API base URL for ZAI
        # Note: ZAI API might have non-standard URL structure
        # The parent class will append /chat/completions, so we provide the base
        # Allow override via environment variable for testing
        default_base_url = "https://api.z.ai/api/coding/paas/v4"
        env_base_url = os.environ.get("ZAI_API_BASE_URL")

        self.api_base_url = kwargs.get("api_base_url", env_base_url or default_base_url)

        # Log the base URL for debugging
        logger.info("ZAI Coding Plan base URL: %s", self.api_base_url)
        if env_base_url:
            logger.info(
                "Using custom base URL from ZAI_API_BASE_URL environment variable"
            )

        # For backward compatibility with tests
        self.anthropic_api_base_url = self.api_base_url

        # ZAI supports up to 200K output tokens (plan-specific defaults may be lower)
        self._max_tokens_limit = 200000  # 200K hard ceiling
        self._default_max_tokens = 8192
        self._model_discovery_succeeded = False

        # Refresh the advertised model list from the provider (falls back to defaults on failure)
        self.available_models: list[str] = []
        self._provider_models: set[str] = set()
        await self._refresh_available_models()
        self._health_checked = self._model_discovery_succeeded

    def get_headers(
        self,
        identity: IAppIdentityConfig | None = None,
    ) -> dict[str, str]:
        """Return request headers with consistent Kilo-Code fingerprinting.

        The ZAI coding plan gateway requires specific client identification headers
        (Referer, Origin, X-Title, X-KiloCode-Version) to validate the subscription.
        Always override with the Kilo-Code fingerprint, stripping any client-submitted
        identity headers from identity overrides that would conflict.
        """
        headers = super().get_headers(identity=identity)

        # Always use Kilo-Code fingerprint for ZAI coding plan endpoint.
        # This overrides any identity headers the caller may have set, ensuring
        # the gateway can match the request to the coding plan subscription.
        headers["User-Agent"] = self._KILO_USER_AGENT
        headers["Referer"] = "https://kilocode.ai"
        headers["Origin"] = "https://kilocode.ai"
        headers["HTTP-Referer"] = "https://kilocode.ai"
        headers["X-Title"] = "Kilo Code"
        headers["X-KiloCode-Version"] = self._KILO_VERSION

        # Remove loop guard header for compatibility
        if "x-llmproxy-loop-guard" in headers:
            headers.pop("x-llmproxy-loop-guard", None)

        # Log headers for debugging (redact sensitive headers comprehensively)
        debug_headers = redact_dict(dict(headers) if headers else {})
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "ZAI Coding Plan request headers (fingerprint=kilocode-override): %s",
                debug_headers,
            )

        return self._sanitize_outbound_headers(headers)

    def _sanitize_outbound_headers(
        self, headers: dict[str, str] | None
    ) -> dict[str, str]:
        """Filter outbound headers to a strict allow-list for ZAI gateway compatibility."""
        if not headers:
            return {}

        sanitized: dict[str, str] = {}
        removed_headers: list[str] = []
        for key, value in headers.items():
            key_lower = key.lower()
            if key_lower in self._ALLOWED_OUTBOUND_HEADERS:
                sanitized[key] = value
            else:
                removed_headers.append(key)

        if removed_headers and logger.isEnabledFor(logging.INFO):
            logger.info(
                "Removed non-standard ZAI outbound headers: %s",
                sorted(removed_headers),
            )

        return sanitized

    def _apply_loop_guard_to_outbound_headers(
        self, headers: Mapping[str, str] | None
    ) -> dict[str, str]:
        """Keep ZAI outbound headers on the strict allow-list.

        The generic OpenAI base class re-attaches ``x-llmproxy-loop-guard`` at send time.
        ZAI's coding-plan gateway is stricter than typical OpenAI-compatible backends, so
        we preserve the sanitized header set instead of reintroducing proxy-only headers.
        """

        return self._sanitize_outbound_headers(dict(headers) if headers else None)

    async def stream_completion(
        self, request: CanonicalChatRequest
    ) -> AsyncGenerator[object, None]:
        """Force ZAI streaming requests to use SSE-friendly sanitized headers."""

        extra_body = dict(getattr(request, "extra_body", None) or {})
        raw_stream_headers = extra_body.get(_STREAM_HEADERS_OVERRIDE_KEY)

        if isinstance(raw_stream_headers, dict):
            stream_headers = {str(k): str(v) for k, v in raw_stream_headers.items()}
        else:
            identity = getattr(request, "identity", None)
            stream_headers = self.get_headers(identity=identity)

        stream_headers["Accept"] = "text/event-stream"
        extra_body[_STREAM_HEADERS_OVERRIDE_KEY] = self._sanitize_outbound_headers(
            stream_headers
        )

        if hasattr(request, "model_copy"):
            request = request.model_copy(update={"extra_body": extra_body})
        else:
            request.extra_body = extra_body

        async for chunk in super().stream_completion(request):
            yield chunk

    async def list_models(
        self, api_base_url: str | None = None, **kwargs: Any
    ) -> ModelsListingResponse:
        """Return available models for ZAI coding plan."""
        models = self.available_models or list(self._SUPPORTED_MODELS)
        model_infos = [
            ModelInfo(
                id=model,
                name=model,
                object="model",
                created=index,
                owned_by="zai",
            )
            for index, model in enumerate(models, start=1)
        ]
        return ModelsListingResponse(object="list", data=model_infos)

    async def get_available_models_async(self) -> list[str]:
        """Return list of available model IDs."""
        return list(self.available_models or self._SUPPORTED_MODELS)

    def get_available_models(self) -> list[str]:
        """Return list of available model IDs."""
        return list(self.available_models or self._SUPPORTED_MODELS)

    @staticmethod
    def _mask_api_key(api_key: str) -> str:
        """Mask API key for logging, showing only first 4 and last 4 characters.

        Args:
            api_key: The API key to mask

        Returns:
            Masked API key string
        """
        if not api_key:
            return "[empty]"
        if len(api_key) <= 8:
            return "***" + api_key[-2:] if len(api_key) > 2 else "***"
        return f"{api_key[:4]}...{api_key[-4:]}"

    def _get_model_discovery_cache_key(self) -> tuple[str, str]:
        normalized_base_url = self.api_base_url.rstrip("/")
        api_key = self.api_key or ""
        api_key_hash = hashlib.sha256(api_key.encode("utf-8")).hexdigest()
        return normalized_base_url, api_key_hash

    @classmethod
    def _get_cached_model_discovery(
        cls, cache_key: tuple[str, str]
    ) -> tuple[list[str], set[str], bool] | None:
        cached_entry = cls._MODEL_DISCOVERY_CACHE.get(cache_key)
        if cached_entry is None:
            return None

        expires_at, cached_models, cached_provider_models, cache_success = cached_entry
        if time.monotonic() >= expires_at:
            cls._MODEL_DISCOVERY_CACHE.pop(cache_key, None)
            return None

        return list(cached_models), set(cached_provider_models), cache_success

    @classmethod
    def _store_model_discovery_cache(
        cls,
        cache_key: tuple[str, str],
        *,
        models: list[str],
        provider_models: set[str],
        success: bool,
    ) -> None:
        ttl_seconds = (
            cls._MODEL_DISCOVERY_SUCCESS_TTL_SECONDS
            if success
            else cls._MODEL_DISCOVERY_FAILURE_TTL_SECONDS
        )
        cls._MODEL_DISCOVERY_CACHE[cache_key] = (
            time.monotonic() + ttl_seconds,
            list(models),
            set(provider_models),
            success,
        )

    async def _refresh_available_models(self) -> None:
        """Probe provider for accessible models and merge with local defaults."""
        fallback_models = list(self._SUPPORTED_MODELS)
        cache_key = self._get_model_discovery_cache_key()
        cached_discovery = self._get_cached_model_discovery(cache_key)
        if cached_discovery is not None:
            cached_models, cached_provider_models, cache_success = cached_discovery
            self.available_models = cached_models
            self._provider_models = cached_provider_models
            self._model_discovery_succeeded = cache_success
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "ZAI Coding Plan using cached model discovery: success=%s models=%s",
                    cache_success,
                    cached_models,
                )
            return

        self._provider_models = set()
        self._model_discovery_succeeded = False
        headers: dict[str, str]
        try:
            headers = self.get_headers()
        except Exception as exc:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "ZAI Coding Plan unable to build headers for model discovery: %s",
                    exc,
                    exc_info=True,
                )
            self.available_models = fallback_models
            self._store_model_discovery_cache(
                cache_key,
                models=self.available_models,
                provider_models=self._provider_models,
                success=False,
            )
            return

        discovered_models: list[str] = []
        try:
            response = await self.client.get(
                f"{self.api_base_url.rstrip('/')}/models", headers=headers
            )
            if hasattr(response, "raise_for_status"):
                response.raise_for_status()
            data = response.json()
            if asyncio.iscoroutine(data):
                data = await data
            if isinstance(data, dict):
                for entry in data.get("data", []):
                    if isinstance(entry, dict):
                        model_id = entry.get("id")
                        if isinstance(model_id, str):
                            discovered_models.append(model_id)
        except (httpx.TransportError, TimeoutError, ConnectionError) as exc:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Unable to fetch ZAI Coding Plan models from API: %s",
                    exc,
                )
        except Exception as exc:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Unable to fetch ZAI Coding Plan models from API: %s",
                    exc,
                    exc_info=True,
                )

        if discovered_models:
            self._provider_models = {name for name in discovered_models if name}
            # Prefer stable static ordering for known Coding Plan models, then append provider-only IDs
            seen: set[str] = set()
            unique_models: list[str] = []
            for name in self._SUPPORTED_MODELS:
                if name and name not in seen:
                    seen.add(name)
                    unique_models.append(name)
            for name in discovered_models:
                if name and name not in seen:
                    seen.add(name)
                    unique_models.append(name)
            self.available_models = unique_models
            self._model_discovery_succeeded = True
            self._store_model_discovery_cache(
                cache_key,
                models=self.available_models,
                provider_models=self._provider_models,
                success=True,
            )
            if logger.isEnabledFor(logging.INFO):
                logger.info(
                    "ZAI Coding Plan available models discovered from provider: %s",
                    self.available_models,
                )
        else:
            self.available_models = fallback_models
            self._store_model_discovery_cache(
                cache_key,
                models=self.available_models,
                provider_models=self._provider_models,
                success=False,
            )
            if logger.isEnabledFor(logging.INFO):
                logger.info(
                    "ZAI Coding Plan using fallback model list: %s",
                    self.available_models,
                )

    async def _perform_health_check(self) -> bool:
        """Reuse model discovery for health checks to avoid duplicate /models probes."""

        await self._refresh_available_models()
        return bool(self._model_discovery_succeeded)

    _VENDOR_PREFIXES: tuple[str, ...] = ("zai", "z-ai", "zhipu")

    def _strip_known_vendor_prefixes(self, model: str) -> str:
        stripped = model
        for prefix in self._VENDOR_PREFIXES:
            stripped = strip_vendor_prefix(stripped, prefix)
        return stripped

    def _select_model(self, requested_model: str | None) -> str:
        """Pick an appropriate provider model, honoring availability."""
        candidate = requested_model or self._DEFAULT_MODEL
        parsed = parse_model_backend(str(candidate), default_backend=self.backend_type)
        normalized = self._strip_known_vendor_prefixes(
            parsed.model_name or self._DEFAULT_MODEL
        )

        # ZAI coding-plan accepts model IDs outside static discovery results.
        # Preserve explicit user model choice (for example glm-4.7) instead of
        # silently falling back to the first discovered model.
        if requested_model:
            return normalized

        available = self.available_models or list(self._SUPPORTED_MODELS)
        if normalized in available:
            return normalized
        if normalized in self._KNOWN_CODING_PLAN_MODELS:
            return normalized
        if available:
            return available[0]
        return normalized

    @staticmethod
    def _detail_to_text(detail: Any) -> str:
        if isinstance(detail, dict):
            for key in ("message", "detail", "error"):
                value = detail.get(key)
                if value:
                    if isinstance(value, dict):
                        inner = value.get("message") or value.get("detail")
                        if inner:
                            return str(inner)
                    return str(value)
            return str(detail)
        return str(detail)

    @staticmethod
    def _provider_error_details(detail: Any) -> dict[str, Any]:
        if not isinstance(detail, dict):
            return {}

        provider_details: dict[str, Any] = {}
        headers = detail.get("headers")
        if isinstance(headers, dict):
            retry_after = headers.get("retry-after") or headers.get("Retry-After")
            if retry_after is not None:
                provider_details["headers"] = {"retry-after": str(retry_after)}
                with contextlib.suppress(TypeError, ValueError):
                    provider_details["retry_after_seconds"] = float(retry_after)
        retry_after_seconds = detail.get("retry_after_seconds")
        if retry_after_seconds is not None:
            with contextlib.suppress(TypeError, ValueError):
                provider_details["retry_after_seconds"] = float(retry_after_seconds)
        return provider_details

    async def chat_completions(  # type: ignore[override]
        self,
        request: ConnectorChatCompletionsRequest,
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        """Route chat completions with canonical request processing only.

        Implements :class:`ICanonicalChatCompletionsBackend` using
        :class:`ConnectorChatCompletionsRequest` only.
        """
        # Update model in canonical request if needed
        selected_model = self._select_model(request.effective_model)
        if request.effective_model != selected_model:
            request = replace(request, effective_model=selected_model)

        try:
            return await self._chat_completions_canonical(request)
        except HTTPException as exc:
            detail_text = self._detail_to_text(exc.detail)
            provider_details = {
                "provider_error": exc.detail,
                "attempted_model": selected_model,
                **self._provider_error_details(exc.detail),
            }
            if exc.status_code == 429:
                raise RateLimitExceededError(
                    message=detail_text
                    or "ZAI Coding Plan reported insufficient quota",
                    details=provider_details,
                ) from exc

            raise BackendError(
                message=detail_text or "ZAI Coding Plan request failed",
                backend_name=self.backend_type,
                details=provider_details,
                status_code=getattr(exc, "status_code", 502),
                code=f"zai_error_{getattr(exc, 'status_code', 'unknown')}",
            ) from exc
        except BackendError:
            # RateLimitExceededError and other mapped domain errors must propagate
            # to resilience / HTTP mapping; do not wrap as zai_error_unexpected.
            raise
        except Exception as exc:
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Unexpected error in ZAI Coding Plan backend: %s",
                    exc,
                    exc_info=True,
                )
            raise BackendError(
                message=f"ZAI Coding Plan request failed: {exc!s}",
                backend_name=self.backend_type,
                details={"attempted_model": selected_model, "error": str(exc)},
                status_code=502,
                code="zai_error_unexpected",
            ) from exc

    async def _handle_non_streaming_response(
        self,
        url: str,
        payload: dict[str, Any],
        headers: dict[str, str] | None,
        session_id: str,
        context: ConnectorRequestContext | None = None,
    ) -> ResponseEnvelope:
        """Override to add detailed logging for debugging."""
        # Remove loop guard header for ZAI API (might be causing issues)
        if headers and "x-llmproxy-loop-guard" in headers:
            headers = dict(headers)
            del headers["x-llmproxy-loop-guard"]
            if logger.isEnabledFor(logging.INFO):
                logger.info(
                    "Removed x-llmproxy-loop-guard header for ZAI API compatibility"
                )
        headers = self._sanitize_outbound_headers(headers)

        # Log request details with redacted headers
        debug_headers = redact_dict(dict(headers) if headers else {})

        logger.info("ZAI API Request (non-streaming): POST %s", url)
        logger.info("ZAI API Headers: %s", debug_headers)
        logger.debug("ZAI API Payload model: %s", payload.get("model", "N/A"))

        # Call parent implementation
        return await super()._handle_non_streaming_response(
            url, payload, headers, session_id, context
        )

    async def _handle_streaming_response(
        self,
        url: str,
        payload: dict[str, Any],
        headers: dict[str, str] | None,
        session_id: str,
        stream_format: str,
        context: ConnectorRequestContext | None = None,
    ) -> StreamingResponseHandle:
        """Override to add detailed logging for debugging.

        Also handles potential non-standard URL structure for ZAI API.
        """
        # Remove loop guard header for ZAI API (might be causing issues)
        if headers and "x-llmproxy-loop-guard" in headers:
            headers = dict(headers)
            del headers["x-llmproxy-loop-guard"]
            if logger.isEnabledFor(logging.INFO):
                logger.info(
                    "Removed x-llmproxy-loop-guard header for ZAI API compatibility"
                )
        headers = self._sanitize_outbound_headers(headers)

        # Log request details with redacted headers
        debug_headers = redact_dict(dict(headers) if headers else {})

        logger.info("ZAI API Request (streaming): POST %s", url)
        logger.info("ZAI API Headers: %s", debug_headers)
        logger.debug("ZAI API Payload model: %s", payload.get("model", "N/A"))

        # Log key payload fields for debugging
        payload_summary = {
            "model": payload.get("model"),
            "message_count": len(payload.get("messages", [])),
            "temperature": payload.get("temperature"),
            "top_p": payload.get("top_p"),
        }
        logger.info("ZAI API Payload summary: %s", payload_summary)

        # Log first message for debugging (truncated)
        messages = payload.get("messages", [])
        if messages:
            first_msg = messages[0]
            content_preview = str(first_msg.get("content", ""))[:100]
            logger.debug(
                "First message: role=%s, content=%s...",
                first_msg.get("role"),
                content_preview,
            )

        # Log the actual Authorization header format for verification
        if headers and "Authorization" in headers:
            auth_header = headers["Authorization"]
            if auth_header.startswith("Bearer "):
                token = auth_header[7:]
                logger.info(
                    "Authorization format: Bearer %s (length: %d)",
                    self._mask_api_key(token),
                    len(token),
                )
            else:
                logger.warning(
                    "Authorization header does not start with 'Bearer ': %s...",
                    auth_header[:20],
                )

        # Call parent implementation with potentially corrected URL
        base_handle = await super()._handle_streaming_response(
            url, payload, headers, session_id, stream_format
        )

        # Post-process streaming iterator to normalize ZAI attempt_completion output
        async def _zai_stream_wrapper() -> AsyncGenerator[ProcessedResponse, None]:
            collected_content: list[str] = []
            sanitized_emitted = False
            selected_model = str(payload.get("model") or self._DEFAULT_MODEL)

            async for chunk in base_handle.iterator:
                parsed_json: dict[str, Any] | None = None
                chunk_content = chunk.content

                if isinstance(chunk_content, bytes):
                    try:
                        chunk_str = chunk_content.decode("utf-8")
                    except UnicodeDecodeError:
                        chunk_str = None
                elif isinstance(chunk_content, str):
                    chunk_str = chunk_content
                else:
                    chunk_str = None

                if chunk_str and "data:" in chunk_str:
                    for line in chunk_str.splitlines():
                        line = line.strip()
                        if not line.startswith("data:"):
                            continue
                        data_part = line[len("data:") :].strip()
                        if not data_part or data_part == "[DONE]":
                            continue
                        try:
                            payload_obj = json.loads(data_part)
                        except json.JSONDecodeError:
                            continue
                        if isinstance(payload_obj, dict):
                            parsed_json = payload_obj
                            break

                if parsed_json:
                    parsed_model = parsed_json.get("model")
                    if isinstance(parsed_model, str) and parsed_model.strip():
                        selected_model = parsed_model
                    choices = parsed_json.get("choices") or []
                    if choices:
                        delta = choices[0].get("delta") or {}
                        content_piece = delta.get("content")
                        if isinstance(content_piece, str):
                            collected_content.append(content_piece)

                        finish_reason = choices[0].get("finish_reason")
                        if (
                            finish_reason == "stop"
                            and not sanitized_emitted
                            and collected_content
                        ):
                            sanitized_text = self._extract_attempt_result(
                                "".join(collected_content)
                            )
                            if sanitized_text:
                                synthetic_chunk = self._build_synthetic_stream_chunk(
                                    sanitized_text,
                                    selected_model,
                                )
                                yield synthetic_chunk
                                sanitized_emitted = True

                yield chunk

            # Safety: emit sanitized chunk even if finish_reason missing
            if not sanitized_emitted and collected_content:
                sanitized_text = self._extract_attempt_result(
                    "".join(collected_content)
                )
                if sanitized_text:
                    synthetic_chunk = self._build_synthetic_stream_chunk(
                        sanitized_text, selected_model
                    )
                    yield synthetic_chunk

        handle = StreamingResponseHandle(
            iterator=_zai_stream_wrapper(),
            cancel_callback=base_handle.cancel_callback,
            headers=base_handle.headers,
        )

        return handle

    @staticmethod
    def _extract_attempt_result(raw_text: str) -> str:
        """Extract the textual result from an attempt_completion XML payload."""
        import re

        if not raw_text:
            return ""

        match = re.search(
            r"<attempt_completion>.*?<result>(?P<body>.*)</result>.*?</attempt_completion>",
            raw_text,
            flags=re.DOTALL | re.IGNORECASE,
        )
        if not match:
            return ""

        body = match.group("body")
        body = body.strip()
        return body

    @staticmethod
    def _build_synthetic_stream_chunk(content: str, model: str) -> ProcessedResponse:
        """Create a synthetic SSE chunk that delivers plain assistant content."""
        payload = {
            "id": f"hybrid-sanitized-{uuid.uuid4().hex}",
            "object": "chat.completion.chunk",
            "created": int(time.time()),
            "model": model,
            "choices": [
                {
                    "index": 0,
                    "delta": {
                        "role": "assistant",
                        "content": content,
                    },
                    "finish_reason": None,
                }
            ],
        }
        payload_str = json.dumps(payload, ensure_ascii=False)
        return ProcessedResponse(content=f"data: {payload_str}\n\n")

    def _extract_mcp_tool_calls_from_messages(self, messages: list[Any]) -> list[Any]:
        """Extract MCP tool calls from message content and convert to tool_calls format.

        This method processes messages to find XML-style MCP tool invocations in the
        content and converts them to proper OpenAI-style tool_calls, removing the XML
        from the content.

        Only processes new messages (those without a processing marker). Historical
        messages that have already been processed are skipped to avoid redundant
        processing and excessive logging.

        Args:
            messages: List of messages to process

        Returns:
            List of messages with tool calls extracted
        """
        from src.core.utils.message_processing_utils import (
            find_last_assistant_message,
            is_message_processed,
            mark_message_processed,
        )

        processed_messages = []

        # Import here to avoid circular dependency
        from src.core.di.services import get_service_provider
        from src.core.services.tool_call_repair_service import ToolCallRepairService

        service_provider = get_service_provider()
        repair_service = service_provider.get_required_service(ToolCallRepairService)
        # Use module-level pre-compiled pattern for performance
        tool_pattern = _MCP_TOOL_PATTERN

        # Find last assistant message for fallback logic
        last_assistant_idx = find_last_assistant_message(messages)

        for idx, message in enumerate(messages):
            # Check if message has already been processed
            if is_message_processed(message):
                logger.log(
                    5,  # TRACE level
                    "Skipping already processed message at index %d",
                    idx,
                )
                processed_messages.append(message)
                continue

            # Get message attributes
            if isinstance(message, dict):
                role = message.get("role", "")
                content = message.get("content", "")
                existing_tool_calls = message.get("tool_calls", [])
            else:
                role = getattr(message, "role", "")
                content = getattr(message, "content", "")
                existing_tool_calls = getattr(message, "tool_calls", [])

            # Only process assistant messages with string content
            if role != "assistant" or not isinstance(content, str):
                processed_messages.append(message)
                continue

            # Fallback: Only process last assistant message if no marker present
            if idx != last_assistant_idx:
                logger.log(
                    5,  # TRACE level
                    "Skipping historical assistant message at index %d (last is %d)",
                    idx,
                    last_assistant_idx,
                )
                processed_messages.append(message)
                continue

            # Check if there are already tool_calls - if so, skip extraction
            if existing_tool_calls:
                processed_messages.append(message)
                continue

            matches = list(tool_pattern.finditer(content))
            if not matches:
                # No XML tool calls found
                processed_messages.append(message)
                continue

            tool_calls = []
            cleaned_content = content

            for match in matches:
                xml_block = match.group(0)
                # Note: Using protected method _extract_xml_tool_call here
                # (despite LSP warning) because the public repair_tool_calls
                # has different semantics and would break existing tests/behavior
                repair_result = cast(Any, repair_service)._extract_xml_tool_call(
                    xml_block
                )
                if not repair_result:
                    continue

                # Extract the tool_call dict from the ToolCallRepairResult
                tool_calls.append(repair_result.tool_call)
                cleaned_content = cleaned_content.replace(xml_block, "", 1).strip()

            if not tool_calls:
                processed_messages.append(message)
                continue

            # Create updated message with tool_calls
            if isinstance(message, dict):
                updated_message = message.copy()
                updated_message["tool_calls"] = tool_calls
                # Keep any remaining text content
                if cleaned_content:
                    updated_message["content"] = cleaned_content
                else:
                    # OpenAI requires content for assistant messages with tool_calls
                    updated_message["content"] = ""
            else:
                # Handle Pydantic models
                update_dict: dict[str, Any] = {"tool_calls": tool_calls}
                if cleaned_content:
                    update_dict["content"] = cleaned_content
                else:
                    update_dict["content"] = ""

                if hasattr(message, "model_copy"):
                    updated_message = message.model_copy(update=update_dict)
                else:
                    # Fallback for non-Pydantic objects
                    updated_message = message

            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    f"Extracted {len(tool_calls)} XML tool call(s) from assistant message"
                )

            # Mark message as processed
            mark_message_processed(updated_message)
            processed_messages.append(updated_message)

        return processed_messages

    async def _prepare_payload(
        self,
        request_data: Any,
        processed_messages: Any = None,
        effective_model: str | None = None,
        context: Any = None,
    ) -> dict[str, Any]:
        """Prepare request payload for ZAI API.

        Args:
            request_data: The request data
            processed_messages: Processed messages (for compatibility)
            effective_model: The effective model name (for compatibility)
            context: Connector request context (optional, for logging correlation)
        """
        # Extract MCP tool calls from message content before preparing payload
        if processed_messages:
            processed_messages = self._extract_mcp_tool_calls_from_messages(
                processed_messages
            )

        # Use OpenAI-style payload preparation while preserving the requested model
        selected_model = self._select_model(
            effective_model or getattr(request_data, "model", None)
        )
        payload = await super()._prepare_payload(
            request_data, processed_messages, selected_model, context
        )

        # Ensure stream flag is preserved for compatibility with Anthropic routing
        if hasattr(request_data, "stream"):
            payload["stream"] = bool(request_data.stream)

        payload["model"] = selected_model

        # Handle max_tokens with ZAI's limits
        if hasattr(request_data, "max_tokens") and request_data.max_tokens is not None:
            requested_max_tokens = request_data.max_tokens
            if requested_max_tokens > 0:
                # Preserve user intent for small token budgets and clamp only at provider max.
                if requested_max_tokens > self._max_tokens_limit:
                    payload["max_tokens"] = self._max_tokens_limit
                else:
                    payload["max_tokens"] = requested_max_tokens
            else:
                # Zero or negative -> conservative default
                payload["max_tokens"] = self._default_max_tokens
        # If client omitted max_tokens entirely, let provider defaults apply

        # Copy other optional parameters
        # Note: Only include parameters that are explicitly set to avoid issues
        if (
            hasattr(request_data, "temperature")
            and request_data.temperature is not None
        ):
            try:
                payload["temperature"] = float(request_data.temperature)
                logger.debug("Including temperature: %s", payload["temperature"])
            except (ValueError, TypeError):
                logger.warning(
                    "Invalid temperature value '%s' ignored.",
                    request_data.temperature,
                    exc_info=True,
                )
        if hasattr(request_data, "top_p") and request_data.top_p is not None:
            payload["top_p"] = request_data.top_p
            logger.debug("Including top_p: %s", request_data.top_p)
        if hasattr(request_data, "tools") and request_data.tools:
            payload["tools"] = request_data.tools
            logger.debug("Including tools: %d tools", len(request_data.tools))
        if hasattr(request_data, "tool_choice") and request_data.tool_choice:
            tool_choice = request_data.tool_choice
            if isinstance(tool_choice, dict) and tool_choice.get("type") == "function":
                # ZAI coding-plan rejects function-enforced tool_choice objects;
                # use OpenAI-compatible automatic tool selection instead.
                tool_choice = "auto"
            payload["tool_choice"] = tool_choice
            logger.debug("Including tool_choice: %s", tool_choice)

        # ZAI coding-plan gateway is strict about accepted payload shape.
        # Only forward a minimal set of known-safe keys to avoid WAF/429 triggers.
        allowed_keys = {
            "model",
            "messages",
            "stream",
            "max_tokens",
            "temperature",
            "top_p",
            "tools",
            "tool_choice",
        }
        cleaned_payload: dict[str, Any] = {}
        removed_keys: list[str] = []
        for key, value in payload.items():
            if key not in allowed_keys:
                removed_keys.append(key)
                continue
            if value is None:
                removed_keys.append(key)
                continue
            if key in {"tools", "tool_choice"} and not value:
                removed_keys.append(key)
                continue
            cleaned_payload[key] = value

        # Log payload sanitization for debugging WAF/429 issues
        if removed_keys and logger.isEnabledFor(logging.INFO):
            logger.info(
                "ZAI Coding Plan stripped payload keys: %s",
                sorted(removed_keys),
            )
        logger.info(
            "ZAI Coding Plan final payload keys: %s",
            sorted(cleaned_payload.keys()),
        )

        return cleaned_payload


backend_registry.register_backend("zai-coding-plan", ZaiCodingPlanBackend)
