"""OpenCode Go backend connector implementation."""

from __future__ import annotations

import logging
import os
import time
from collections.abc import AsyncGenerator
from dataclasses import replace
from typing import Any

import httpx

from src.connectors.anthropic import AnthropicBackend
from src.connectors.base import add_vendor_prefix, strip_vendor_prefix
from src.connectors.contracts import (
    ConnectorChatCompletionsRequest,
)
from src.connectors.openai import OpenAIConnector
from src.core.common.exceptions import ConfigurationError, RoutingError
from src.core.common.model_catalog import BackendModelEnumeration
from src.core.config.app_config import AppConfig, BackendConfig
from src.core.domain.models_listing import ModelInfo, ModelsListingResponse
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.services.backend_registry import backend_registry
from src.core.services.translation_service import TranslationService

logger = logging.getLogger(__name__)


def _strip_opencode_go_anthropic_extra_body(
    connector_req: ConnectorChatCompletionsRequest,
) -> ConnectorChatCompletionsRequest:
    """Drop Anthropic-beta knobs OpenCode Go /messages does not accept (HTTP 400)."""
    inner = connector_req.request
    eb = getattr(inner, "extra_body", None)
    if not isinstance(eb, dict):
        return connector_req
    drop_keys = frozenset({"thinking", "anthropic_beta"})
    if not drop_keys.intersection(eb.keys()):
        return connector_req
    cleaned = {k: v for k, v in eb.items() if k not in drop_keys}
    new_inner = inner.model_copy(update={"extra_body": cleaned})
    return replace(connector_req, request=new_inner)


def _opencode_go_params_to_input_schema(parameters: Any) -> dict[str, Any]:
    """Build Anthropic ``input_schema`` from OpenAI JSON-schema ``parameters``."""

    if not isinstance(parameters, dict) or not parameters:
        return {"type": "object", "properties": {}}
    schema: dict[str, Any] = {
        "type": parameters.get("type") or "object",
        "properties": (
            parameters.get("properties")
            if isinstance(parameters.get("properties"), dict)
            else {}
        ),
    }
    req = parameters.get("required")
    if isinstance(req, list) and req:
        schema["required"] = req
    return schema


def _opencode_go_coerce_input_schema(schema: Any) -> dict[str, Any]:
    if not isinstance(schema, dict) or not schema:
        return {"type": "object", "properties": {}}
    out = dict(schema)
    out.setdefault("type", "object")
    props = out.get("properties")
    out["properties"] = props if isinstance(props, dict) else {}
    return out


def _opencode_go_normalize_tool_for_messages_api(
    tool: dict[str, Any],
) -> dict[str, Any] | None:
    """OpenCode Go MiniMax ``/messages`` expects Anthropic flat tools, not OpenAI wrappers."""

    fn = tool.get("function")
    if isinstance(fn, dict):
        name = str(fn.get("name") or "").strip()
        if not name:
            return None
        if "input_schema" in fn:
            isc = _opencode_go_coerce_input_schema(fn.get("input_schema"))
        else:
            isc = _opencode_go_params_to_input_schema(fn.get("parameters"))
        out: dict[str, Any] = {"name": name, "input_schema": isc}
        desc = fn.get("description")
        if isinstance(desc, str) and desc.strip():
            out["description"] = desc.strip()
        return out

    root_name = tool.get("name")
    if isinstance(root_name, str) and root_name.strip():
        isc = _opencode_go_coerce_input_schema(tool.get("input_schema"))
        out_flat: dict[str, Any] = {
            "name": root_name.strip(),
            "input_schema": isc,
        }
        desc2 = tool.get("description")
        if isinstance(desc2, str) and desc2.strip():
            out_flat["description"] = desc2.strip()
        return out_flat

    return None


def _opencode_go_normalize_payload_tools(payload: dict[str, Any]) -> None:
    raw = payload.get("tools")
    if raw is None:
        return
    if not isinstance(raw, list):
        payload.pop("tools", None)
        payload.pop("tool_choice", None)
        return
    normalized: list[dict[str, Any]] = []
    for item in raw:
        if isinstance(item, dict):
            conv = _opencode_go_normalize_tool_for_messages_api(item)
            if conv is not None:
                normalized.append(conv)
    if normalized:
        payload["tools"] = normalized
    else:
        payload.pop("tools", None)
        payload.pop("tool_choice", None)


def _opencode_go_sanitize_openai_payload(
    payload: dict[str, Any],
    model_name: str,
) -> None:
    """Remove fields rejected by OpenCode Go's OpenAI-compatible endpoint."""

    normalized_model = _normalize_model_name(model_name).lower()
    if normalized_model.startswith("glm-"):
        for key in ("reasoning", "reasoning_effort", "thinking"):
            payload.pop(key, None)


_OPENCODE_GO_DEFAULT_BASE_URL = "https://opencode.ai/zen/go/v1"
_OPENCODE_GO_VENDOR_PREFIX = "opencode-go"
_OPENCODE_GO_OPENAI_MODELS: tuple[str, ...] = (
    "glm-5",
    "glm-5.1",
    "glm-5.2",
    "kimi-k2.5",
    "kimi-k2.6",
    "kimi-k2.7-code",
    "deepseek-v4-pro",
    "deepseek-v4-flash",
    "mimo-v2.5",
    "mimo-v2.5-pro",
    "mimo-v2-pro",
    "mimo-v2-omni",
)
# Legacy aliases for model ids the gateway used to advertise. The live
# /models endpoint is authoritative at runtime; this map only rescues user
# configs that still reference the older slugs.
_OPENCODE_GO_LEGACY_MODEL_ALIASES: dict[str, str] = {
    "kimi-k2.7": "kimi-k2.7-code",
}
_OPENCODE_GO_ANTHROPIC_MODELS: tuple[str, ...] = (
    "minimax-m3",
    "minimax-m2.5",
    "minimax-m2.7",
    "qwen3.7-max",
    "qwen3.7-plus",
    "qwen3.6-plus",
    "qwen3.5-plus",
)
_OPENCODE_GO_SUPPORTED_PROTOCOLS = {"openai", "anthropic"}
_MODELS_CACHE_TTL: int = 60  # seconds


def _normalize_opencode_go_api_key(api_key: str) -> str:
    """Strip accidental ``Bearer `` prefix from config or env (OpenAI adds it again)."""

    trimmed = api_key.strip()
    lower = trimmed.lower()
    if lower.startswith("bearer "):
        return trimmed[7:].strip()
    return trimmed


def _normalize_model_name(model: str) -> str:
    """Normalize a public or backend-prefixed model identifier to its raw model id."""

    candidate = model.strip()
    if not candidate:
        return candidate
    if candidate.startswith(f"{_OPENCODE_GO_VENDOR_PREFIX}/"):
        return strip_vendor_prefix(candidate, _OPENCODE_GO_VENDOR_PREFIX)
    if candidate.startswith(f"{_OPENCODE_GO_VENDOR_PREFIX}:"):
        return _normalize_model_name(candidate.split(":", 1)[1])
    return candidate


def _validate_protocol_name(protocol: Any) -> str | None:
    if not isinstance(protocol, str):
        return None
    normalized = protocol.strip().lower()
    if normalized in _OPENCODE_GO_SUPPORTED_PROTOCOLS:
        return normalized
    return None


def _normalize_openai_base_url(api_base_url: str) -> str:
    """Accept either a base URL or a full chat completions endpoint."""

    normalized = api_base_url.rstrip("/")
    suffix = "/chat/completions"
    if normalized.endswith(suffix):
        return normalized[: -len(suffix)]
    return normalized


def _normalize_anthropic_base_url(api_base_url: str) -> str:
    """Accept either a base URL or a full messages endpoint."""

    normalized = api_base_url.rstrip("/")
    suffix = "/messages"
    if normalized.endswith(suffix):
        return normalized[: -len(suffix)]
    return normalized


def _infer_protocol_from_model_info(model: ModelInfo) -> str | None:
    """Infer OpenCode Go wire protocol from upstream /models metadata.

    Reads only the canonical fields OpenCode documents (``endpoint`` and
    ``ai_sdk_package``) so unrelated fields such as ``id`` or future
    extras cannot accidentally flip the protocol classification.
    """

    raw = model.model_dump(mode="python")
    endpoint = str(raw.get("endpoint", "")).lower()
    sdk_pkg = str(raw.get("ai_sdk_package", "")).lower()
    if "anthropic" in sdk_pkg or endpoint.endswith("/messages"):
        return "anthropic"
    if "openai" in sdk_pkg or endpoint.endswith("/chat/completions"):
        return "openai"
    return None


class _OpencodeGoAnthropicDelegate(AnthropicBackend):
    """Anthropic Messages delegate: OpenCode Go expects ``x-api-key`` on ``/messages`` (not Bearer)."""

    backend_type: str = _OPENCODE_GO_VENDOR_PREFIX

    async def initialize(self, **kwargs: Any) -> None:
        kwargs = dict(kwargs)
        kwargs.setdefault("key_name", _OPENCODE_GO_VENDOR_PREFIX)
        kwargs.setdefault("anthropic_api_base_url", _OPENCODE_GO_DEFAULT_BASE_URL)
        kwargs.setdefault("auth_header_name", "x-api-key")
        await super().initialize(**kwargs)

    def _prepare_anthropic_payload(
        self,
        request_data: Any,
        processed_messages: list[Any],
        effective_model: str,
        project: str | None,
        context: Any | None = None,
    ) -> dict[str, Any]:
        normalized_model = _normalize_model_name(effective_model)
        payload = super()._prepare_anthropic_payload(
            request_data,
            processed_messages,
            normalized_model,
            project,
            context,
        )
        # extra_body may reintroduce config-style ids (opencode-go/...); upstream rejects
        # those on the wire with 401 (see dev/scripts/opencode_go_probe.py).
        wire_model = _normalize_model_name(
            str(payload.get("model") or normalized_model)
        )
        if wire_model:
            payload["model"] = wire_model
        for k in ("thinking", "reasoning_effort"):
            payload.pop(k, None)
        _opencode_go_normalize_payload_tools(payload)
        return payload

    async def stream_completion(self, request: Any) -> AsyncGenerator[object, None]:
        normalized_request = request.model_copy(
            update={"model": _normalize_model_name(getattr(request, "model", ""))}
        )
        async for chunk in super().stream_completion(normalized_request):
            yield chunk


class OpencodeGoBackend(OpenAIConnector):
    """Hybrid OpenCode Go backend with OpenAI and Anthropic protocol routing."""

    backend_type: str = _OPENCODE_GO_VENDOR_PREFIX
    VENDOR_PREFIX: str | None = _OPENCODE_GO_VENDOR_PREFIX

    def __init__(
        self,
        client: httpx.AsyncClient,
        config: AppConfig,
        translation_service: TranslationService | None = None,
    ) -> None:
        super().__init__(client, config, translation_service=translation_service)
        self.api_key: str | None = None
        self.api_base_url = _OPENCODE_GO_DEFAULT_BASE_URL
        self.available_models = list(self._default_raw_models())
        self._model_protocol_overrides: dict[str, str] = {}
        self._models_cache: tuple[float, ModelsListingResponse] | None = None
        self._anthropic_delegate = _OpencodeGoAnthropicDelegate(
            client=client,
            config=config,
            translation_service=translation_service or self.translation_service,
        )

    @staticmethod
    def _default_raw_models() -> tuple[str, ...]:
        return _OPENCODE_GO_OPENAI_MODELS + _OPENCODE_GO_ANTHROPIC_MODELS

    @classmethod
    def _build_advertised_raw_models(
        cls, overrides: dict[str, str] | None = None
    ) -> list[str]:
        ordered_models: list[str] = list(cls._default_raw_models())
        override_keys: list[str] = []
        if overrides:
            override_keys = [
                _normalize_model_name(model_name)
                for model_name in overrides
                if _normalize_model_name(model_name)
            ]

        for model_name in override_keys:
            if model_name not in ordered_models:
                ordered_models.append(model_name)
        return ordered_models

    @staticmethod
    def _build_protocol_override_map(
        overrides: Any,
    ) -> dict[str, str]:
        if overrides is None:
            return {}
        if not isinstance(overrides, dict):
            raise ConfigurationError(
                message="model_protocol_overrides must be a mapping of model -> protocol",
                code="invalid_config",
            )

        normalized: dict[str, str] = {}
        for raw_model, raw_protocol in overrides.items():
            model_name = _normalize_model_name(str(raw_model))
            if not model_name:
                continue
            protocol = _validate_protocol_name(raw_protocol)
            if protocol is None:
                raise ConfigurationError(
                    message=(
                        "model_protocol_overrides values must be 'openai' or 'anthropic'"
                    ),
                    details={
                        "model": model_name,
                        "protocol": raw_protocol,
                    },
                    code="invalid_config",
                )
            normalized[model_name] = protocol
        return normalized

    @classmethod
    def _resolve_protocol_for_model(
        cls,
        model_name: str,
        protocol_overrides: dict[str, str],
    ) -> str | None:
        normalized = _normalize_model_name(model_name)
        if not normalized:
            return None

        override = protocol_overrides.get(normalized)
        if override:
            return override

        if normalized in _OPENCODE_GO_ANTHROPIC_MODELS:
            return "anthropic"
        return "openai"

    def _resolve_wire_model_id(self, raw_model: str) -> str:
        """Map an incoming model id to the gateway-advertised wire id.

        Falls back to a legacy alias map for ids the gateway no longer lists
        (e.g. ``kimi-k2.7`` -> ``kimi-k2.7-code``) so existing user aliases
        and configs keep working.
        """
        normalized = _normalize_model_name(raw_model)
        if not normalized:
            return normalized
        return _OPENCODE_GO_LEGACY_MODEL_ALIASES.get(normalized, normalized)

    def _normalize_request(
        self, request: ConnectorChatCompletionsRequest, raw_model: str
    ) -> ConnectorChatCompletionsRequest:
        wire_model = self._resolve_wire_model_id(raw_model)
        normalized_request = request.request.model_copy(update={"model": wire_model})
        return replace(
            request,
            request=normalized_request,
            effective_model=wire_model,
        )

    async def _prepare_payload(
        self,
        request_data: Any,
        processed_messages: list[Any],
        effective_model: str,
        context: Any | None = None,
    ) -> dict[str, Any]:
        wire_model = self._resolve_wire_model_id(effective_model)
        payload = await super()._prepare_payload(
            request_data,
            processed_messages,
            wire_model,
            context,
        )
        canonical_wire_model = _normalize_model_name(
            str(payload.get("model") or wire_model)
        )
        if canonical_wire_model:
            payload["model"] = canonical_wire_model
        _opencode_go_sanitize_openai_payload(
            payload, canonical_wire_model or wire_model
        )
        return payload

    def _build_unknown_model_error(self, model_name: str) -> RoutingError:
        supported_models = [
            f"{self.backend_type}/{model}"
            for model in self._build_advertised_raw_models(
                self._model_protocol_overrides
            )
        ]
        return RoutingError(
            message=f"Unsupported opencode-go model '{model_name}'.",
            details={
                "code": "unknown_model",
                "category": "validation",
                "retryable": False,
                "backend_type": self.backend_type,
                "model": model_name,
                "supported_models": supported_models,
            },
        )

    async def initialize(self, **kwargs: Any) -> None:
        raw_key = kwargs.get("api_key")
        if not raw_key or not isinstance(raw_key, str):
            raise ConfigurationError(
                message="api_key is required for OpencodeGoBackend",
                code="missing_api_key",
            )
        self.api_key = _normalize_opencode_go_api_key(raw_key)

        shared_api_base_url = (
            kwargs.get("api_base_url") or _OPENCODE_GO_DEFAULT_BASE_URL
        )
        openai_api_base_url = _normalize_openai_base_url(
            str(kwargs.get("openai_api_base_url") or shared_api_base_url)
        )
        anthropic_api_base_url = _normalize_anthropic_base_url(
            str(kwargs.get("anthropic_api_base_url") or shared_api_base_url)
        )

        self.api_base_url = openai_api_base_url
        self._model_protocol_overrides = self._build_protocol_override_map(
            kwargs.get("model_protocol_overrides")
        )

        configured_models = kwargs.get("models")
        if isinstance(configured_models, list | tuple) and configured_models:
            self.available_models = [
                _normalize_model_name(str(m))
                for m in configured_models
                if _normalize_model_name(str(m))
            ]
        else:
            self.available_models = self._build_advertised_raw_models(
                self._model_protocol_overrides
            )

        # When no explicit ``models`` list is configured, attempt a live
        # ``GET /models`` so new OpenCode Go model additions are picked up
        # without a connector change. Failure is non-fatal: we keep the
        # curated static list and log a warning. Operators who want to
        # avoid the startup HTTP call (e.g. air-gapped deployments) can
        # pin ``models`` in their backend config to short-circuit it.
        if not configured_models:
            await self._refresh_available_models_from_upstream()

        self._anthropic_delegate.api_key = self.api_key
        await self._anthropic_delegate.initialize(
            api_key=self.api_key,
            key_name=kwargs.get("key_name", self.backend_type),
            anthropic_api_base_url=anthropic_api_base_url,
            auth_header_name="x-api-key",
        )

        if logger.isEnabledFor(logging.INFO):
            logger.info(
                "Initialized opencode-go backend with openai_base=%s anthropic_base=%s",
                self.api_base_url,
                anthropic_api_base_url,
            )

    def get_available_models(self) -> list[str]:
        return [f"{self.backend_type}/{model}" for model in self.available_models]

    async def get_available_models_async(self) -> list[str]:
        return self.get_available_models()

    async def list_models(
        self, api_base_url: str | None = None
    ) -> ModelsListingResponse:
        now = time.monotonic()
        if self._models_cache is not None:
            cached_at, cached_response = self._models_cache
            if now - cached_at < _MODELS_CACHE_TTL:
                return cached_response

        response = await super().list_models(api_base_url=api_base_url)
        self._models_cache = (now, response)
        return response

    async def _refresh_available_models_from_upstream(self) -> None:
        try:
            response = await self.list_models()
        except Exception as exc:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Failed to refresh OpenCode Go model catalog; using fallback models: %s",
                    exc,
                )
            return

        upstream_models: list[str] = []
        upstream_protocols: dict[str, str] = {}
        for model in response.data:
            model_id = _normalize_model_name(model.id)
            if not model_id:
                continue
            upstream_models.append(model_id)
            protocol = _infer_protocol_from_model_info(model)
            if protocol is not None:
                upstream_protocols[model_id] = protocol

        if not upstream_models:
            return

        seen: set[str] = set()
        self.available_models = []
        for model_id in upstream_models:
            if model_id not in seen:
                self.available_models.append(model_id)
                seen.add(model_id)
        for override_model in self._model_protocol_overrides:
            normalized_model = _normalize_model_name(override_model)
            if normalized_model and normalized_model not in seen:
                self.available_models.append(normalized_model)
                seen.add(normalized_model)
        self._model_protocol_overrides = {
            **upstream_protocols,
            **self._model_protocol_overrides,
        }

    def get_provider_name(self) -> str:
        # The outer connector routes OpenAI-compatible requests through the
        # OpenAI streaming stack; Anthropic requests use the private delegate.
        return "openai"

    async def chat_completions(  # type: ignore[override]
        self,
        request: ConnectorChatCompletionsRequest,
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        if not isinstance(request, ConnectorChatCompletionsRequest):
            raise TypeError(
                "OpencodeGoBackend.chat_completions requires ConnectorChatCompletionsRequest."
            )

        raw_model = _normalize_model_name(request.effective_model)
        if not raw_model:
            raw_model = _normalize_model_name(request.request.model)

        protocol = self._resolve_protocol_for_model(
            raw_model, self._model_protocol_overrides
        )

        normalized_request = self._normalize_request(request, raw_model)
        if protocol == "openai":
            return await super().chat_completions(normalized_request)

        anthropic_req = _strip_opencode_go_anthropic_extra_body(normalized_request)
        return await self._anthropic_delegate.chat_completions(anthropic_req)


class OpencodeGoConfiguredModelEnumerator:
    """Enumerate canonical OpenCode Go routes for configured backend instances."""

    async def enumerate(
        self, instance_name: str, config: BackendConfig
    ) -> BackendModelEnumeration:
        extra = config.extra or {}
        configured_models = config.models or extra.get("models")
        if isinstance(configured_models, list | tuple) and configured_models:
            models = [
                add_vendor_prefix(
                    strip_vendor_prefix(str(m).strip(), _OPENCODE_GO_VENDOR_PREFIX),
                    _OPENCODE_GO_VENDOR_PREFIX,
                )
                for m in configured_models
                if str(m).strip()
            ]
            if models:
                return BackendModelEnumeration.available(
                    instance_name=instance_name,
                    connector=_OPENCODE_GO_VENDOR_PREFIX,
                    models=models,
                    source="opencode_go_configured",
                    instance_pinned=False,
                )

        suffix = instance_name.split(".", 1)[-1] if "." in instance_name else None
        api_key = (
            config.api_key
            or extra.get("api_key")
            or (os.environ.get(f"OPENCODE_GO_API_KEY_{suffix}") if suffix else None)
            or os.environ.get("OPENCODE_GO_API_KEY")
        )

        if not api_key:
            return BackendModelEnumeration.unavailable(
                instance_name=instance_name,
                connector=_OPENCODE_GO_VENDOR_PREFIX,
                source="opencode_go",
                error_code="missing_api_key",
                instance_pinned=False,
            )

        overrides_raw = extra.get("model_protocol_overrides")
        overrides: dict[str, str] = {}
        if isinstance(overrides_raw, dict):
            for k, v in overrides_raw.items():
                norm_k = _normalize_model_name(str(k))
                norm_v = _validate_protocol_name(v)
                if norm_k and norm_v:
                    overrides[norm_k] = norm_v

        curated_models = [
            add_vendor_prefix(m, _OPENCODE_GO_VENDOR_PREFIX)
            for m in OpencodeGoBackend._build_advertised_raw_models(overrides)
        ]

        shared_api_base_url = (
            config.api_url or extra.get("api_base_url") or _OPENCODE_GO_DEFAULT_BASE_URL
        )
        openai_api_base_url = _normalize_openai_base_url(
            str(extra.get("openai_api_base_url") or shared_api_base_url)
        )
        timeout_seconds = float(extra.get("model_discovery_timeout_seconds", 5.0))

        clean_key = _normalize_opencode_go_api_key(str(api_key))
        if clean_key:
            try:
                headers = {
                    "Authorization": f"Bearer {clean_key}",
                    "Accept": "application/json",
                }
                async with httpx.AsyncClient(timeout=timeout_seconds) as client:
                    response = await client.get(
                        f"{openai_api_base_url.rstrip('/')}/models",
                        headers=headers,
                    )
                    if response.status_code == 200:
                        data = response.json()
                        raw_items = data.get("data") if isinstance(data, dict) else None
                        if isinstance(raw_items, list) and raw_items:
                            upstream_models: list[str] = []
                            seen: set[str] = set()
                            for item in raw_items:
                                model_id = ""
                                if isinstance(item, dict):
                                    model_id = str(item.get("id") or "")
                                elif isinstance(item, str):
                                    model_id = item
                                norm_id = _normalize_model_name(model_id)
                                if norm_id and norm_id not in seen:
                                    seen.add(norm_id)
                                    upstream_models.append(
                                        add_vendor_prefix(
                                            norm_id, _OPENCODE_GO_VENDOR_PREFIX
                                        )
                                    )
                            for override_model in overrides:
                                norm_override = _normalize_model_name(override_model)
                                if norm_override and norm_override not in seen:
                                    seen.add(norm_override)
                                    upstream_models.append(
                                        add_vendor_prefix(
                                            norm_override, _OPENCODE_GO_VENDOR_PREFIX
                                        )
                                    )
                            if upstream_models:
                                return BackendModelEnumeration.available(
                                    instance_name=instance_name,
                                    connector=_OPENCODE_GO_VENDOR_PREFIX,
                                    models=upstream_models,
                                    source="opencode_go_upstream",
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

        return BackendModelEnumeration.available(
            instance_name=instance_name,
            connector=_OPENCODE_GO_VENDOR_PREFIX,
            models=curated_models,
            source="opencode_go_curated",
            instance_pinned=False,
        )


backend_registry.register_backend("opencode-go", OpencodeGoBackend)

__all__ = [
    "OpencodeGoBackend",
    "OpencodeGoConfiguredModelEnumerator",
]
