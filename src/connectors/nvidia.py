"""Nvidia NIM OpenAI-compatible backend connector."""

from __future__ import annotations

import logging
import os
from typing import TYPE_CHECKING, Any

import httpx

from src.connectors.base import add_vendor_prefix, strip_vendor_prefix
from src.connectors.contracts import ConnectorChatCompletionsRequest
from src.connectors.openai import OpenAIConnector
from src.core.common.model_catalog import BackendModelEnumeration
from src.core.config.app_config import AppConfig, BackendConfig
from src.core.domain.chat import CanonicalChatRequest
from src.core.domain.models_listing import ModelsListingResponse
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.services.backend_registry import backend_registry
from src.core.services.streaming.processed_stream_idle_keepalive import (
    wrap_processed_stream_with_idle_keepalive,
)

if TYPE_CHECKING:
    from src.connectors.contracts import ConnectorRequestContext
    from src.core.services.translation_service import TranslationService

logger = logging.getLogger(__name__)

NVIDIA_DEFAULT_BASE_URL = "https://integrate.api.nvidia.com/v1"

_NVIDIA_FALLBACK_MODELS: tuple[str, ...] = (
    "meta/llama-3.3-70b-instruct",
    "meta/llama-3.1-405b-instruct",
    "meta/llama-3.1-70b-instruct",
    "meta/llama-3.1-8b-instruct",
    "mistralai/mistral-large-2407",
    "mistralai/mixtral-8x22b-instruct-v0.1",
    "mistralai/mistral-nemo-12b-instruct",
    "google/gemma-2-27b-it",
    "google/gemma-2-9b-it",
    "deepseek-ai/deepseek-r1",
    "deepseek-ai/deepseek-v3",
    "nvidia/llama-3.1-nemotron-70b-instruct",
    "nvidia/nemotron-4-340b-instruct",
    "nvidia/nemotron-4-340b-reward",
    "qwen/qwen2.5-72b-instruct",
    "qwen/qwen2.5-coder-32b-instruct",
    "writer/palmyra-med-70b-32k",
)

_NVIDIA_HTTP_LIMITS = httpx.Limits(max_connections=100, max_keepalive_connections=20)
# Minimum ``read`` timeout between SSE body chunks. The hosted integrator (and some
# NIM models) may pause far longer than the shared pool default (60s) during extended
# reasoning; shorter values abort mid-generation with ReadTimeout / truncated output.
_NVIDIA_MIN_INTER_CHUNK_READ_S = 300.0


class _NvidiaTransportProxy(httpx.AsyncBaseTransport):
    """Delegate NVIDIA traffic to the shared transport without owning it."""

    def __init__(self, transport: httpx.AsyncBaseTransport) -> None:
        self._transport = transport

    async def handle_async_request(self, request: httpx.Request) -> httpx.Response:
        return await self._transport.handle_async_request(request)

    async def aclose(self) -> None:
        # The shared client owns the underlying transport lifecycle.
        return None


def _nvidia_dedicated_timeout(
    base: httpx.AsyncClient, app_config: AppConfig
) -> httpx.Timeout:
    """Build httpx timeouts for the NVIDIA-only HTTP/1.1 client."""

    bc = app_config.backends.lookup("nvidia")
    configured = (
        float(bc.timeout)
        if bc is not None and getattr(bc, "timeout", None) not in (None, 0)
        else 120.0
    )
    if configured <= 0:
        configured = 120.0

    base_t = getattr(base, "timeout", None)
    if isinstance(base_t, httpx.Timeout):
        if base_t.read is None:
            return base_t
        fields = dict(base_t.as_dict())
        base_read = fields.get("read")
        br = float(base_read) if isinstance(base_read, int | float) else 60.0
        fields["read"] = max(br, configured, _NVIDIA_MIN_INTER_CHUNK_READ_S)
        return httpx.Timeout(**fields)

    return httpx.Timeout(
        connect=10.0,
        read=max(configured, _NVIDIA_MIN_INTER_CHUNK_READ_S),
        write=60.0,
        pool=60.0,
    )


def _copy_event_hooks(
    base: httpx.AsyncClient,
) -> dict[str, list[Any]] | None:
    """Preserve any shared-client hooks on the NVIDIA-only client.

    The dedicated client must stay HTTP/1.1-only, but it should not silently drop
    request/response hooks that may carry observability or capture instrumentation.
    """

    hooks = getattr(base, "event_hooks", None)
    if not isinstance(hooks, dict):
        return None
    return {name: list(callbacks) for name, callbacks in hooks.items()}


def _proxy_shared_transport(
    base: httpx.AsyncClient,
) -> httpx.AsyncBaseTransport | None:
    """Reuse the shared client's transport so dedicated-client tests and hooks still work."""

    transport = getattr(base, "_transport", None)
    if isinstance(transport, httpx.AsyncBaseTransport):
        return _NvidiaTransportProxy(transport)
    return None


def _normalize_nvidia_api_key(value: str) -> str:
    """Strip whitespace and a leading ``Bearer `` prefix (common copy-paste mistake)."""

    s = value.strip()
    lower = s.lower()
    if lower.startswith("bearer "):
        s = s[7:].lstrip()
    return s


class NvidiaConnector(OpenAIConnector):
    """Connector for NVIDIA-hosted NIM OpenAI-compatible inference API."""

    backend_type: str = "nvidia"

    # Multi-vendor gateway: upstream model ids are already vendor-qualified
    VENDOR_PREFIX: str | None = None

    def __init__(
        self,
        client: httpx.AsyncClient,
        config: AppConfig,
        translation_service: TranslationService | None = None,
    ) -> None:
        super().__init__(client, config, translation_service=translation_service)
        self.api_base_url = NVIDIA_DEFAULT_BASE_URL
        # Dedicated client: NVIDIA integrator often drops HTTP/2 mid-stream
        # (``RemoteProtocolError: Server disconnected`` in httpcore's HTTP/2 stack)
        # on large chat bodies; HTTP/1.1 is stable for the same traffic.
        self._nvidia_http11_client: httpx.AsyncClient | None = None

    async def _chat_completions_canonical(
        self,
        request: ConnectorChatCompletionsRequest,
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        """Attach client-side SSE keepalives; NIM often goes silent during reasoning."""

        result = await super()._chat_completions_canonical(request)
        if isinstance(result, StreamingResponseEnvelope) and result.content is not None:
            interval = 8.0
            fh = getattr(self.config, "failure_handling", None)
            raw_iv = getattr(fh, "keepalive_interval", None) if fh is not None else None
            if isinstance(raw_iv, int | float) and raw_iv > 0:
                interval = float(raw_iv)
            stream_id: str | None = None
            if request.context is not None:
                stream_id = getattr(request.context, "session_id", None)
            if not stream_id:
                stream_id = getattr(request.request, "session_id", None)
            result.content = wrap_processed_stream_with_idle_keepalive(
                result.content,
                keepalive_interval=interval,
                idle_timeout=None,
                stream_id=stream_id,
                model_name=request.effective_model,
                on_idle_timeout=None,
            )
        return result

    async def initialize(self, **kwargs: Any) -> None:
        """Initialize connector, falling back to NVIDIA_API_KEY when no key in kwargs."""

        raw = kwargs.get("api_key")
        if isinstance(raw, str):
            norm = _normalize_nvidia_api_key(raw)
            kwargs["api_key"] = norm if norm else None

        if not kwargs.get("api_key"):
            env_key = os.getenv("NVIDIA_API_KEY")
            if env_key:
                kwargs["api_key"] = _normalize_nvidia_api_key(env_key)

        kwargs.setdefault("api_base_url", self.api_base_url)
        self._ensure_nvidia_http11_client()
        await super().initialize(**kwargs)

    def _ensure_nvidia_http11_client(self) -> None:
        """Use HTTP/1.1 for NVIDIA only; keep timeouts/limits aligned with the shared client."""

        if not isinstance(self.client, httpx.AsyncClient):
            return
        dedicated = self._nvidia_http11_client
        if dedicated is not None and not dedicated.is_closed:
            self.client = dedicated
            return
        if dedicated is not None:
            self._nvidia_http11_client = None

        base = self.client
        timeout = _nvidia_dedicated_timeout(base, self.config)
        limits = getattr(base, "limits", None) or _NVIDIA_HTTP_LIMITS
        event_hooks = _copy_event_hooks(base)
        transport = _proxy_shared_transport(base)
        self._nvidia_http11_client = httpx.AsyncClient(
            http2=False,
            timeout=timeout,
            limits=limits,
            transport=transport,
            trust_env=False,
            event_hooks=event_hooks,
        )
        self.client = self._nvidia_http11_client

    async def close(self) -> None:
        """Close the NVIDIA-only client in addition to OpenAI connector resources."""

        nvidia_client = self._nvidia_http11_client
        self._nvidia_http11_client = None
        if nvidia_client is not None and not nvidia_client.is_closed:
            await nvidia_client.aclose()
        await super().close()

    async def list_models(
        self, api_base_url: str | None = None
    ) -> ModelsListingResponse:
        """Ensure HTTP/1.1 client before ``GET /models`` (may run outside ``initialize`` in tests)."""

        self._ensure_nvidia_http11_client()
        return await super().list_models(api_base_url)

    async def _prepare_payload(
        self,
        request_data: CanonicalChatRequest,
        processed_messages: list[Any],
        effective_model: str,
        context: ConnectorRequestContext | None = None,
    ) -> dict[str, Any]:
        """Build JSON body for NVIDIA NIM; strict schema rejects some OpenAI Chat fields."""

        payload = await super()._prepare_payload(
            request_data, processed_messages, effective_model, context
        )
        # Hosted integrator returns 400 extra_forbidden for max_completion_tokens (Pydantic strict).
        mct = payload.pop("max_completion_tokens", None)
        if mct is not None and payload.get("max_tokens") is None:
            payload["max_tokens"] = mct
        # Strict OpenAI-compat schema on hosted NIM often rejects Chat Completions extensions
        # that the generic OpenAI connector adds (unknown keys -> 422/extra_forbidden).
        payload.pop("reasoning", None)
        payload.pop("stream_options", None)
        return payload


backend_registry.register_backend("nvidia", NvidiaConnector)


class NvidiaConfiguredModelEnumerator:
    """Enumerate canonical NVIDIA NIM routes for configured backend instances."""

    async def enumerate(
        self, instance_name: str, config: BackendConfig
    ) -> BackendModelEnumeration:
        extra = config.extra or {}
        configured_models = config.models or extra.get("models")
        if isinstance(configured_models, list | tuple) and configured_models:
            models = [
                add_vendor_prefix(
                    strip_vendor_prefix(str(m).strip(), "nvidia"),
                    "nvidia",
                )
                for m in configured_models
                if str(m).strip()
            ]
            if models:
                return BackendModelEnumeration.available(
                    instance_name=instance_name,
                    connector="nvidia",
                    models=models,
                    source="nvidia_configured",
                    instance_pinned=False,
                )

        suffix = instance_name.split(".", 1)[-1] if "." in instance_name else None
        raw_key = (
            config.api_key
            or extra.get("api_key")
            or (os.environ.get(f"NVIDIA_API_KEY_{suffix}") if suffix else None)
            or os.environ.get("NVIDIA_API_KEY")
        )

        api_key = _normalize_nvidia_api_key(str(raw_key)) if raw_key else None

        if not api_key:
            return BackendModelEnumeration.unavailable(
                instance_name=instance_name,
                connector="nvidia",
                source="nvidia",
                error_code="missing_api_key",
                instance_pinned=False,
            )

        shared_api_base_url = (
            config.api_url or extra.get("api_base_url") or NVIDIA_DEFAULT_BASE_URL
        )
        api_base_url = str(shared_api_base_url).rstrip("/")
        timeout_seconds = float(extra.get("model_discovery_timeout_seconds", 5.0))

        clean_key = str(api_key).strip()
        if clean_key:
            try:
                headers = {
                    "Authorization": f"Bearer {clean_key}",
                    "Accept": "application/json",
                }
                async with httpx.AsyncClient(timeout=timeout_seconds) as client:
                    response = await client.get(
                        f"{api_base_url}/models", headers=headers
                    )
                    if response.status_code == 200:
                        data = response.json()
                        rows = data.get("data") if isinstance(data, dict) else None
                        if isinstance(rows, list) and rows:
                            live_models: list[str] = []
                            seen: set[str] = set()
                            for r in rows:
                                raw_id = ""
                                if isinstance(r, dict):
                                    raw_id = str(r.get("id") or "")
                                elif isinstance(r, str):
                                    raw_id = r
                                raw_id = raw_id.strip()
                                if raw_id and raw_id not in seen:
                                    seen.add(raw_id)
                                    live_models.append(
                                        add_vendor_prefix(raw_id, "nvidia")
                                    )
                            if live_models:
                                return BackendModelEnumeration.available(
                                    instance_name=instance_name,
                                    connector="nvidia",
                                    models=live_models,
                                    source="nvidia_upstream",
                                    instance_pinned=False,
                                )
            except (httpx.TransportError, TimeoutError, ConnectionError) as exc:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Live model discovery failed for %s, falling back to curated list: %s",
                        instance_name,
                        exc,
                    )
            except Exception as exc:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Live model discovery failed for %s, falling back to curated list: %s",
                        instance_name,
                        exc,
                        exc_info=True,
                    )

        curated_models = [
            add_vendor_prefix(m, "nvidia") for m in _NVIDIA_FALLBACK_MODELS
        ]
        return BackendModelEnumeration.available(
            instance_name=instance_name,
            connector="nvidia",
            models=curated_models,
            source="nvidia_curated",
            instance_pinned=False,
        )


__all__ = [
    "NVIDIA_DEFAULT_BASE_URL",
    "NvidiaConfiguredModelEnumerator",
    "NvidiaConnector",
]
