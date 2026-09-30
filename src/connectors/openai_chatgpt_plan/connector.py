"""Thin ChatGPT-plan facade over the public OpenAI Responses transport."""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import replace
from typing import Any, Protocol

from src.connectors.contracts import ConnectorResponsesRequest
from src.connectors.openai_responses import OpenAIResponsesConnector
from src.core.common.exceptions import AuthenticationError, LLMProxyError
from src.core.domain.backend_capability_descriptor import BackendCapabilityDescriptor
from src.core.domain.responses import ResponseEnvelope, StreamingResponseEnvelope
from src.core.domain.responses_native_wiring import (
    RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY,
)
from src.core.interfaces.configuration_interface import IAppIdentityConfig
from src.core.services.backend_registry import backend_registry

CHATGPT_PLAN_CAPABILITY_DESCRIPTOR = BackendCapabilityDescriptor(
    protocol_family="openai",
    supports_streaming=True,
    supports_tool_calls=True,
    is_oauth_based=True,
    requires_personal_auth=True,
)

PUBLIC_OPENAI_API_BASE = "https://api.openai.com/v1"

_CODEX_OUTBOUND_HEADER_NAMES = frozenset(
    {
        "originator",
        "codex-task-type",
        "chatgpt-account-id",
        "openai-beta",
        "conversation_id",
        "session_id",
        "version",
    }
)


class _ChatGPTPlanModelLister(Protocol):
    async def list_models(
        self, profile_id: str, *, force_refresh: bool = False
    ) -> list[str]: ...


class _ChatGPTPlanAccessTokenSource(Protocol):
    async def get_access_token(self, profile_id: str) -> str: ...

    async def force_refresh(self, profile_id: str) -> Any: ...

    async def mark_needs_reauth(self, profile_id: str, reason: str) -> None: ...


def _looks_like_codex_user_agent(value: str) -> bool:
    folded = value.casefold()
    return "codex_cli_rs" in folded or folded.strip() == "originator"


def _strip_codex_outbound_headers(headers: Mapping[str, str]) -> dict[str, str]:
    cleaned: dict[str, str] = {}
    for name, value in headers.items():
        lowered = name.lower()
        if lowered in _CODEX_OUTBOUND_HEADER_NAMES:
            continue
        if lowered == "user-agent" and _looks_like_codex_user_agent(value):
            continue
        cleaned[name] = value
    return cleaned


class OpenAIChatGPTPlanConnector(OpenAIResponsesConnector):
    """Public Responses-based backend for official ChatGPT-plan usage.

    Thin registration and routing facade over ``OpenAIResponsesConnector``.
    Importing this module registers the backend and does not start
    authorization, token refresh, network I/O, or a credential prompt.
    """

    backend_type: str = "openai-chatgpt-plan"
    VENDOR_PREFIX: str | None = "openai"
    capability_descriptor = CHATGPT_PLAN_CAPABILITY_DESCRIPTOR
    _chatgpt_plan_model_catalog: _ChatGPTPlanModelLister | None = None
    _chatgpt_plan_profile_id: str | None = None
    _chatgpt_plan_token_manager: _ChatGPTPlanAccessTokenSource | None = None
    _chatgpt_plan_identity_fingerprint: str | None = None

    @property
    def has_static_credentials(self) -> bool:
        return False

    def bind_chatgpt_plan_model_catalog(
        self, catalog: _ChatGPTPlanModelLister, profile_id: str
    ) -> None:
        """Attach a per-profile catalog used by ``get_available_models_async``.

        Catalog construction is lazy (initialize / enumerator). This hook does
        not register an application initialization stage.
        """

        self._chatgpt_plan_model_catalog = catalog
        self._chatgpt_plan_profile_id = profile_id
        token_manager = getattr(catalog, "_token_manager", None)
        if token_manager is not None:
            self._chatgpt_plan_token_manager = token_manager

    def _bind_chatgpt_plan_catalog_from_init_kwargs(
        self, kwargs: Mapping[str, Any]
    ) -> None:
        from src.connectors.openai_chatgpt_plan.enumerator import (
            build_chatgpt_plan_model_catalog,
            chatgpt_plan_config_from_init_kwargs,
            profile_id_from_init_kwargs,
        )

        profile_id = profile_id_from_init_kwargs(kwargs)
        if profile_id is None:
            return
        plan_config = chatgpt_plan_config_from_init_kwargs(kwargs)
        catalog = build_chatgpt_plan_model_catalog(plan_config)
        self.bind_chatgpt_plan_model_catalog(catalog, profile_id)

    async def initialize(self, **kwargs: Any) -> None:
        await super().initialize(**kwargs)
        self.api_base_url = PUBLIC_OPENAI_API_BASE
        self._use_websocket = False
        self._bind_chatgpt_plan_catalog_from_init_kwargs(kwargs)

    def get_headers(self, identity: IAppIdentityConfig | None = None) -> dict[str, str]:
        headers = super().get_headers(identity=identity)
        return _strip_codex_outbound_headers(headers)

    def set_chatgpt_plan_identity_fingerprint(self, fingerprint: str) -> None:
        """Attach a non-secret profile identity fingerprint for diagnostics."""

        stripped = fingerprint.strip()
        if stripped:
            self._chatgpt_plan_identity_fingerprint = stripped

    def _get_log_extra(self, context: Any) -> dict[str, str]:
        extra = super()._get_log_extra(context)
        profile_id = self._chatgpt_plan_profile_id
        if profile_id:
            extra["profile_id"] = profile_id
        fingerprint = self._chatgpt_plan_identity_fingerprint
        if fingerprint:
            extra["profile_identity_fingerprint"] = fingerprint
        return extra

    def _require_token_manager(self) -> _ChatGPTPlanAccessTokenSource:
        manager = self._chatgpt_plan_token_manager
        if manager is None:
            catalog = self._chatgpt_plan_model_catalog
            nested = getattr(catalog, "_token_manager", None)
            if nested is not None:
                manager = nested
                self._chatgpt_plan_token_manager = nested
        if manager is None:
            raise AuthenticationError(
                message="ChatGPT-plan token manager is not bound.",
            )
        return manager

    async def _chatgpt_plan_access_token(self) -> str:
        profile_id = self._chatgpt_plan_profile_id
        if not profile_id:
            raise AuthenticationError(
                message="ChatGPT-plan profile is not selected.",
            )
        manager = self._require_token_manager()
        return await manager.get_access_token(profile_id)

    async def _chatgpt_plan_force_refresh(self) -> None:
        profile_id = self._chatgpt_plan_profile_id
        if not profile_id:
            raise AuthenticationError(
                message="ChatGPT-plan profile is not selected.",
            )
        manager = self._require_token_manager()
        await manager.force_refresh(profile_id)

    async def _chatgpt_plan_mark_needs_reauth(self, reason: str) -> None:
        profile_id = self._chatgpt_plan_profile_id
        if not profile_id:
            return
        try:
            manager = self._require_token_manager()
        except AuthenticationError:
            return
        await manager.mark_needs_reauth(profile_id, reason)

    def _projected_generic_payload(
        self, request: ConnectorResponsesRequest, extra_body: dict[str, Any]
    ) -> dict[str, Any]:
        native_raw = extra_body.get(RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY)
        if isinstance(native_raw, dict):
            return native_raw
        domain_request = self.translation_service.to_domain_request(
            request.request, "responses"
        )
        return self.translation_service.from_domain_to_responses_request(domain_request)

    async def _chatgpt_plan_run_projected(
        self,
        request: ConnectorResponsesRequest,
        *,
        downstream_stream_requested: bool,
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        from src.connectors.openai_chatgpt_plan.stream_accumulator import (
            ChatGPTPlanStreamAccumulator,
        )

        result = await super().responses(request)
        if downstream_stream_requested:
            return result
        if isinstance(result, StreamingResponseEnvelope):
            accumulator = ChatGPTPlanStreamAccumulator(backend_type=self.backend_type)
            return await accumulator.accumulate(result)
        return result

    async def responses(
        self, request: ConnectorResponsesRequest
    ) -> ResponseEnvelope | StreamingResponseEnvelope:
        from src.connectors.openai_chatgpt_plan.errors import ChatGPTPlanErrorMapper
        from src.connectors.openai_chatgpt_plan.request_policy import (
            ChatGPTPlanRequestPolicy,
        )

        request_data = request.request
        raw_extra = getattr(request_data, "extra_body", None)
        extra_body = dict(raw_extra) if isinstance(raw_extra, dict) else {}
        generic_payload = self._projected_generic_payload(request, extra_body)
        projected = ChatGPTPlanRequestPolicy().project(
            request=request,
            generic_payload=generic_payload,
        )
        extra_body[RESPONSES_NATIVE_PROJECTED_PAYLOAD_KEY] = projected.payload
        # SIWC requires upstream stream=true for every HTTP inference request.
        # Force the shared Responses transport onto the streaming path even when
        # the downstream client asked for a non-streaming response.
        forwarded_request = request_data.model_copy(
            update={"extra_body": extra_body, "stream": True}
        )

        self.api_key = await self._chatgpt_plan_access_token()
        self.api_base_url = PUBLIC_OPENAI_API_BASE
        self._use_websocket = False

        options = dict(request.options) if request.options else {}
        options["openai_url"] = PUBLIC_OPENAI_API_BASE
        options["use_websocket"] = False

        forwarded = replace(
            request,
            request=forwarded_request,
            options=options,
        )

        mapper = ChatGPTPlanErrorMapper()
        profile_id = self._chatgpt_plan_profile_id
        try:
            return await self._chatgpt_plan_run_projected(
                forwarded,
                downstream_stream_requested=projected.downstream_stream_requested,
            )
        except Exception as first_exc:
            mapped = mapper.map_exception(first_exc, profile_id=profile_id)
            if not mapper.should_refresh_and_retry(mapped, already_retried=False):
                raise mapped from first_exc
            try:
                await self._chatgpt_plan_force_refresh()
                self.api_key = await self._chatgpt_plan_access_token()
                return await self._chatgpt_plan_run_projected(
                    forwarded,
                    downstream_stream_requested=projected.downstream_stream_requested,
                )
            except Exception as second_exc:
                mapped_second = mapper.map_exception(second_exc, profile_id=profile_id)
                if isinstance(mapped_second, LLMProxyError) and int(
                    getattr(mapped_second, "status_code", 401) or 401
                ) in {401, 403}:
                    await self._chatgpt_plan_mark_needs_reauth(
                        str(getattr(mapped_second, "code", None) or "auth_retry_failed")
                    )
                raise mapped_second from second_exc

    async def get_available_models_async(self) -> list[str]:
        catalog = self._chatgpt_plan_model_catalog
        profile_id = self._chatgpt_plan_profile_id
        if catalog is not None and profile_id:
            listed = await catalog.list_models(profile_id)
            self.available_models = [str(model) for model in listed]
        return self.get_available_models()


backend_registry.register_backend("openai-chatgpt-plan", OpenAIChatGPTPlanConnector)
