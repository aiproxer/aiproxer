"""Backend and model target resolution service.

This service isolates the logic for determining which backend and model to use
for a given request, including model alias resolution, backend prefix parsing,
URI parameter extraction, and static routing overrides.
"""

from __future__ import annotations

import logging
from typing import TYPE_CHECKING, Any, NoReturn, cast

from pydantic.types import JsonValue

from src.connectors.acp_core.workspace_policy import (
    ACP_BACKEND_TYPES,
    ACP_MISSING_PROJECT_WORKSPACE_CODE,
    first_usable_workspace_dir,
    is_usable_workspace_directory,
)
from src.core.common.exceptions import ConfigurationError, RoutingError
from src.core.config.app_config import AppConfig
from src.core.domain.backend_target import BackendTarget
from src.core.domain.chat import ChatRequest
from src.core.domain.composite_routing import CompositeRoutingInput, RoutingSurface
from src.core.domain.configuration.backend_config import BackendConfiguration
from src.core.domain.model_utils import (
    RESOLVED_URI_PARAMS_EXTRA_BODY_KEY,
    has_explicit_backend_selector,
    parse_model_with_params,
)
from src.core.domain.request_context import RequestContext
from src.core.domain.session import SessionState
from src.core.interfaces.backend_lifecycle_manager_interface import (
    IBackendLifecycleManager,
)
from src.core.interfaces.backend_model_resolver_interface import (
    IBackendModelResolver,
)
from src.core.interfaces.configuration_interface import IConfig
from src.core.interfaces.model_alias_resolver_interface import IModelAliasResolver
from src.core.interfaces.planning_phase_manager_interface import IPlanningPhaseManager
from src.core.interfaces.session_service_interface import ISessionService
from src.core.services.backend_routing_service import BackendRoutingService
from src.core.services.composite_routing_state import (
    COMPOSITE_LEAF_PARSED_BACKEND_EXTRA_BODY_KEY,
    COMPOSITE_LEAF_PARSED_MODEL_EXTRA_BODY_KEY,
    COMPOSITE_LEAF_RESOLUTION_EXTRA_BODY_KEY,
    COMPOSITE_LEAF_RESOLUTION_FLAG,
    COMPOSITE_LEAF_SELECTOR_EXTRA_BODY_KEY,
    INTERLEAVED_THINKING_SUPPRESS_THINKER_SELECTION_KEY,
    INTERLEAVED_THINKING_WEIGHTED_CYCLE_STATE_KEY,
    resolve_composite_routing_surface,
)
from src.core.services.replacement_compatibility_bridge import (
    ReplacementCompatibilityBridge,
)

if TYPE_CHECKING:
    from src.core.services.composite_routing_service import CompositeRoutingService

logger = logging.getLogger(__name__)

_RESOLVED_URI_PARAMS_CONTEXT_KEY = "resolved_uri_params"
_SKIP_STATIC_ROUTE_CONTEXT_KEY = "skip_static_route"


class BackendModelResolver(IBackendModelResolver):
    """Resolver for backend and model targets from requests.

    This implementation consolidates target resolution logic including:
    - Session-based backend configuration
    - Model alias resolution (applied before backend parsing)
    - Backend prefix parsing from model strings (e.g., "anthropic:claude-3-5-sonnet")
    - URI parameter extraction (e.g., "?temperature=0.5&max_tokens=100")
    - Static routing overrides from configuration
    - Backend discovery and routing via BackendRoutingService
    """

    def __init__(
        self,
        session_service: ISessionService,
        model_alias_resolver: IModelAliasResolver,
        planning_phase_manager: IPlanningPhaseManager,
        backend_lifecycle_manager: IBackendLifecycleManager,
        config: IConfig,
        routing_service: BackendRoutingService,
        composite_routing_service: CompositeRoutingService | None = None,
        replacement_compatibility_bridge: ReplacementCompatibilityBridge | None = None,
    ):
        """Initialize the backend model resolver.

        Args:
            session_service: Service for session lookups
            model_alias_resolver: Resolver for model aliases
            planning_phase_manager: Manager for planning phase application
            backend_lifecycle_manager: Manager for backend lifecycle
            config: Application configuration
            routing_service: Shared dynamic routing service
        """
        self._session_service = session_service
        self._model_alias_resolver = model_alias_resolver
        self._planning_phase_manager = planning_phase_manager
        self._backend_lifecycle_manager = backend_lifecycle_manager
        self._config = config
        self._routing_service = routing_service
        self._composite_routing_service = (
            composite_routing_service or self._build_default_composite_routing_service()
        )
        self._replacement_compatibility_bridge = (
            replacement_compatibility_bridge or ReplacementCompatibilityBridge()
        )

    async def resolve_target(
        self, request: ChatRequest, context: RequestContext | None = None
    ) -> BackendTarget:
        """Resolve backend, model, and URI parameters from request.

        Resolution order:
        1. Session backend configuration (if available)
        2. Request extra_body backend_type (if available)
        3. Model alias resolution (MUST happen before backend parsing)
        4. Backend prefix parsing from model string
        5. URI parameter extraction
        6. Backend discovery via routing service
        7. Static routing overrides

        Args:
            request: The chat completion request
            context: Optional request context (currently unused)

        Returns:
            ResolvedTarget with backend, model, and URI parameters
        """
        effective_model = self._model_alias_resolver.resolve(request.model)
        request_has_explicit_backend_selector = has_explicit_backend_selector(
            request.model
        )
        routing_surface = resolve_composite_routing_surface(context)

        if not self._is_composite_leaf_resolution(context=context, request=request):
            selector_for_routing = effective_model
            if routing_surface is RoutingSurface.REPLACEMENT_BRIDGE:
                selector_for_routing = (
                    self._replacement_compatibility_bridge.translate_selector(
                        selector=effective_model,
                        context=context,
                    )
                )
            should_route_via_composite = bool(selector_for_routing.strip())
            if routing_surface is RoutingSurface.REPLACEMENT_BRIDGE:
                # Keep replacement bridge validation strict, even for degenerate input.
                should_route_via_composite = True

            if should_route_via_composite:
                session, prefer_first_weighted_branch = (
                    await self._resolve_weighted_first_session_state(
                        request=request,
                        context=context,
                    )
                )
                self._apply_interleaved_thinking_suppression_from_session(
                    context=context,
                    session=session,
                )
                self._apply_interleaved_thinking_suppression_for_first_turn(
                    context=context,
                    is_first_turn=prefer_first_weighted_branch,
                )
                should_consume_weighted_first_flag = bool(
                    session is not None
                    and not bool(
                        getattr(session.state, "weighted_first_request_consumed", False)
                    )
                )
                base_session_state = (
                    self._session_state_as_session_state(cast(Any, session).state)
                    if session is not None
                    else None
                )
                cycle_state = (
                    base_session_state.interleaved_thinking_weighted_cycle_state
                    if base_session_state is not None
                    else None
                )
                composite_input = CompositeRoutingInput(
                    selector=selector_for_routing,
                    surface=routing_surface,
                    require_explicit_backend=self._require_explicit_backend_for_surface(
                        routing_surface
                    ),
                    configured_max_hops=self._resolve_max_hops_from_config(),
                    default_backend=self._resolve_default_backend(),
                    prefer_first_weighted_branch=prefer_first_weighted_branch,
                    request_context_tokens=self._extract_request_context_tokens(
                        request
                    ),
                    interleaved_thinking_weighted_cycle_state=cycle_state,
                )
                try:
                    return await self._composite_routing_service.resolve_target(
                        routing_input=composite_input,
                        request=request,
                        context=context,
                    )
                finally:
                    raw_cycle_state = (
                        context.extensions.get(
                            INTERLEAVED_THINKING_WEIGHTED_CYCLE_STATE_KEY
                        )
                        if context is not None
                        else None
                    )
                    should_persist_cycle_state = session is not None and isinstance(
                        raw_cycle_state, dict
                    )
                    if should_consume_weighted_first_flag or should_persist_cycle_state:
                        session_for_update = cast(Any, session)
                        base_state = self._session_state_as_session_state(
                            session_for_update.state
                        )
                        if base_state is not None:
                            next_state = base_state
                            if should_consume_weighted_first_flag:
                                next_state = (
                                    next_state.with_weighted_first_request_consumed(
                                        True
                                    )
                                )
                            if should_persist_cycle_state:
                                next_state = next_state.with_interleaved_thinking_weighted_cycle_state(
                                    cast(dict[str, Any], raw_cycle_state)
                                )
                            session_for_update.update_state(next_state)
                            await self._session_service.update_session(
                                session_for_update
                            )

        # Extract session ID and fetch session
        session_id = getattr(context, "session_id", None) if context else None
        if not session_id and isinstance(request.extra_body, dict):
            session_id = request.extra_body.get("session_id")
        session = (
            await self._session_service.get_session(session_id) if session_id else None
        )

        # Get default backend from configuration
        app_config = cast(AppConfig, self._config)
        default_backend = self._resolve_default_backend()

        # Apply planning phase if needed
        await self._planning_phase_manager.apply_if_needed(session, default_backend)

        # Get disabled backends for filtering
        excluded_backends = set(
            self._backend_lifecycle_manager.get_disabled_backends().keys()
        )

        composite_precheck = self._try_read_and_clear_composite_leaf_precheck(request)

        # Priority 1: Explicit backend requested in model or composite precheck
        # Priority 2: Session backend configuration
        # Priority 3: Request extra_body backend_type
        backend_type: str | None = None
        has_explicit_target_backend = (
            composite_precheck is not None and bool(composite_precheck[0])
        ) or request_has_explicit_backend_selector
        if not has_explicit_target_backend:
            if session and session.state and session.state.backend_config:
                backend_type = cast(
                    BackendConfiguration, session.state.backend_config
                ).backend_type

            if not backend_type:
                backend_type = (
                    request.extra_body.get("backend_type")
                    if request.extra_body
                    else None
                )

        # Parse model string with URI parameters
        uri_params: dict[str, JsonValue] = {}
        preserved_uri_params = self._extract_preserved_uri_params(request, context)

        if not backend_type:
            # No backend type set yet - parse from model string
            # Pass empty string as default to detect if backend was specified
            if composite_precheck is not None:
                pre_backend, pre_model, pre_uri = composite_precheck
                backend_selected_by_model_only = False
                explicit_backend_requested = bool(pre_backend)

                if not pre_backend:
                    pre_backend = self._routing_service.resolve_model_only_backend(
                        pre_model,
                        excluded_backends=excluded_backends,
                    )
                    backend_selected_by_model_only = True

                backend_type = pre_backend
                effective_model = pre_model
                uri_params = pre_uri if pre_uri else dict(preserved_uri_params)
            else:
                parsed = parse_model_with_params(effective_model, "")
                backend_selected_by_model_only = False
                explicit_backend_requested = bool(parsed.backend_type)

                # Resolve model-only selectors via the shared routing path.
                if not parsed.backend_type:
                    parsed.backend_type = (
                        self._routing_service.resolve_model_only_backend(
                            parsed.model_name,
                            excluded_backends=excluded_backends,
                        )
                    )
                    backend_selected_by_model_only = True

                backend_type = parsed.backend_type
                effective_model = parsed.model_name
                uri_params = (
                    parsed.uri_params
                    if parsed.uri_params
                    else dict(preserved_uri_params)
                )

            # Route the backend type (either parsed or default)
            should_route_backend_type = (
                backend_selected_by_model_only
                or explicit_backend_requested
                or backend_type != default_backend
            )
            if should_route_backend_type:
                resolved = self._routing_service.resolve_backend_instance(
                    backend_type, effective_model, excluded_backends
                )
                if resolved:
                    if logger.isEnabledFor(logging.DEBUG) and resolved != backend_type:
                        logger.debug(
                            f"RoutingService resolved '{backend_type}' -> '{resolved}'"
                        )
                    backend_type = resolved
                else:
                    self._raise_no_available_instance(
                        backend_type=backend_type,
                        effective_model=effective_model,
                        excluded_backends=excluded_backends,
                    )

        else:
            # Backend type already set (from session or extra_body)
            # Still need to parse URI parameters from the model string
            # Parse with empty default backend since we already have backend_type
            if composite_precheck is not None:
                _, pre_model, pre_uri = composite_precheck
                uri_params = pre_uri if pre_uri else dict(preserved_uri_params)
                effective_model = pre_model
            else:
                parsed = parse_model_with_params(effective_model, "")
                parsed_model = parsed.model_name
                uri_params = (
                    parsed.uri_params
                    if parsed.uri_params
                    else dict(preserved_uri_params)
                )
                effective_model = parsed_model

            # Route the explicitly set backend.
            resolved = self._routing_service.resolve_backend_instance(
                backend_type, effective_model, excluded_backends
            )
            if resolved:
                if logger.isEnabledFor(logging.DEBUG) and resolved != backend_type:
                    logger.debug(
                        f"RoutingService resolved '{backend_type}' -> '{resolved}'"
                    )
                backend_type = resolved
            else:
                self._raise_no_available_instance(
                    backend_type=backend_type,
                    effective_model=effective_model,
                    excluded_backends=excluded_backends,
                )

        skip_static_route = False
        if context is not None:
            extensions = getattr(context, "extensions", None)
            if isinstance(extensions, dict):
                skip_static_route = bool(
                    extensions.get(_SKIP_STATIC_ROUTE_CONTEXT_KEY, False)
                )

        # Apply static_route override if configured.
        # Auxiliary request routing can set a context flag to bypass this global
        # override for explicitly rerouted requests.
        if (
            not skip_static_route
            and not request_has_explicit_backend_selector
            and hasattr(app_config, "backends")
            and hasattr(app_config.backends, "static_route")
            and app_config.backends.static_route
        ):
            static_route = app_config.backends.static_route
            # Parse static route via canonical parser so URI-like params are handled
            # the same way as request model selectors.
            parsed_static = parse_model_with_params(static_route, "")

            if has_explicit_backend_selector(static_route):
                forced_backend = parsed_static.backend_type
                forced_model = parsed_static.model_name
                if logger.isEnabledFor(logging.INFO):
                    logger.info(
                        f"Applying static_route override: {backend_type}:{effective_model} -> {forced_backend}:{forced_model}"
                    )
                backend_type = forced_backend
                effective_model = forced_model
            else:
                raise ConfigurationError(
                    message=(
                        "Invalid runtime static_route. "
                        "Expected explicit backend:model selector."
                    ),
                    details={
                        "error_code": "invalid_static_route_format",
                        "static_route": static_route,
                        "expected_format": "<backend_name>:<model_name>",
                    },
                )

            if parsed_static.uri_params:
                # Static-route parameters override request-provided URI params.
                uri_params = {**uri_params, **parsed_static.uri_params}
        elif logger.isEnabledFor(logging.DEBUG):
            if skip_static_route:
                logger.debug(
                    "Skipping static_route override due to request context flag"
                )
            elif request_has_explicit_backend_selector:
                logger.debug(
                    "Skipping static_route override because request uses explicit backend selector"
                )

        self._persist_uri_params_in_context(context, uri_params)

        if backend_type in ACP_BACKEND_TYPES:
            extra = request.extra_body if isinstance(request.extra_body, dict) else None
            session_workspace: dict[str, str] | None = None
            if session is not None:
                pd = getattr(session.state, "project_dir", None)
                if isinstance(pd, str) and pd.strip():
                    session_workspace = {"project_dir": pd.strip()}
            workspace = first_usable_workspace_dir(
                extra,
                session_workspace,
                is_usable=is_usable_workspace_directory,
            )
            if workspace is None:
                raise RoutingError(
                    message=(
                        "ACP backends require a usable workspace directory "
                        "(session project_dir or request extra_body "
                        "project_dir/workspace_path/cwd/project)."
                    ),
                    details={
                        "category": "validation",
                        "code": ACP_MISSING_PROJECT_WORKSPACE_CODE,
                        "backend_type": backend_type,
                    },
                )

        return BackendTarget(
            backend=backend_type,
            model=effective_model,
            uri_params=uri_params,
        )

    def synchronize_request_with_target(
        self, request: ChatRequest, resolved: BackendTarget
    ) -> ChatRequest:
        """Update request to match resolved backend and model.

        Ensures the request object and its extra_body reflect the resolved
        backend and model information, preserving the original format where
        appropriate.

        Args:
            request: Original chat request
            resolved: Resolved target information

        Returns:
            Updated request with synchronized backend/model
        """
        backend_type = resolved.backend
        effective_model = resolved.model

        updates: dict[str, Any] = {}

        # Preserve the original model format if it contains a backend prefix that matches
        # the resolved backend. This allows connectors to see the original client request.
        # However, if the backend was overridden (e.g., via static_route), update the model.
        should_update_model = False
        if request.model != effective_model:
            if has_explicit_backend_selector(request.model):
                # Model has backend prefix - check if it matches the resolved backend
                request_backend, _ = request.model.split(":", 1)
                if request_backend != backend_type:
                    # Backend was overridden, update the model
                    should_update_model = True
                # else: Backend matches, preserve original format
            else:
                # No backend prefix, update to effective model
                should_update_model = True

        if should_update_model:
            updates["model"] = effective_model

        extra_body = getattr(request, "extra_body", None)
        resolved_uri_params = dict(resolved.uri_params)
        updated_extra_body: dict[str, Any] = (
            dict(extra_body) if isinstance(extra_body, dict) else {}
        )
        extra_changed = False
        existing_uri_params = updated_extra_body.get(RESOLVED_URI_PARAMS_EXTRA_BODY_KEY)
        if resolved_uri_params:
            if existing_uri_params != resolved_uri_params:
                updated_extra_body[RESOLVED_URI_PARAMS_EXTRA_BODY_KEY] = (
                    resolved_uri_params
                )
                extra_changed = True
        elif RESOLVED_URI_PARAMS_EXTRA_BODY_KEY in updated_extra_body:
            updated_extra_body.pop(RESOLVED_URI_PARAMS_EXTRA_BODY_KEY)
            extra_changed = True

        if isinstance(extra_body, dict):
            if updated_extra_body.get("model") != effective_model:
                updated_extra_body["model"] = effective_model
                extra_changed = True

            if backend_type:
                if updated_extra_body.get("backend_type") != backend_type:
                    updated_extra_body["backend_type"] = backend_type
                    extra_changed = True
            elif "backend_type" in updated_extra_body:
                # Remove stale backend_type when backend resolution is empty
                updated_extra_body.pop("backend_type")
                extra_changed = True

        if extra_changed:
            updates["extra_body"] = updated_extra_body

        if not updates:
            return request

        return request.model_copy(update=updates)

    def _build_default_composite_routing_service(self) -> CompositeRoutingService:
        from src.core.services.composite_diagnostics_publisher import (
            CompositeDiagnosticsPublisher,
        )
        from src.core.services.composite_leaf_target_resolver_adapter import (
            CompositeLeafTargetResolverAdapter,
        )
        from src.core.services.composite_routing_coordinator import (
            CompositeRoutingCoordinator,
        )
        from src.core.services.composite_routing_service import CompositeRoutingService
        from src.core.services.composite_selector_parser import CompositeSelectorParser
        from src.core.services.weighted_branch_selector import WeightedBranchSelector

        leaf_resolver = CompositeLeafTargetResolverAdapter(
            backend_model_resolver=self,
        )
        diagnostics_publisher = CompositeDiagnosticsPublisher()
        coordinator = CompositeRoutingCoordinator(
            weighted_branch_selector=WeightedBranchSelector(),
            leaf_target_resolver=leaf_resolver,
            diagnostics_publisher=diagnostics_publisher,
        )
        return CompositeRoutingService(
            parser=CompositeSelectorParser(),
            coordinator=coordinator,
            diagnostics_publisher=diagnostics_publisher,
        )

    @staticmethod
    def _session_state_as_session_state(state_obj: Any) -> SessionState | None:
        if isinstance(state_obj, SessionState):
            return state_obj

        to_dict_fn = getattr(state_obj, "to_dict", None)
        if callable(to_dict_fn):
            raw_state = to_dict_fn()
            if isinstance(raw_state, dict):
                try:
                    return cast(SessionState, SessionState.from_dict(raw_state))
                except Exception:
                    return None
        return None

    async def _resolve_weighted_first_session_state(
        self,
        *,
        request: ChatRequest,
        context: RequestContext | None,
    ) -> tuple[Any | None, bool]:
        session_id: str | None = None
        if context is not None and isinstance(context.session_id, str):
            candidate = context.session_id.strip()
            if candidate:
                session_id = candidate
        if session_id is None and isinstance(request.extra_body, dict):
            extra_session_id = request.extra_body.get("session_id")
            if isinstance(extra_session_id, str):
                candidate = extra_session_id.strip()
                if candidate:
                    session_id = candidate

        if session_id is None:
            return None, True

        session = cast(Any, await self._session_service.get_session(session_id))
        if session is None:
            return None, True

        return session, not bool(
            getattr(session.state, "weighted_first_request_consumed", False)
        )

    def _resolve_default_backend(self) -> str:
        app_config = cast(AppConfig, self._config)
        if hasattr(app_config, "backends"):
            return app_config.backends.default_backend
        return "openai"

    def _resolve_max_hops_from_config(self) -> int | None:
        app_config = cast(AppConfig, self._config)
        failure_handling = getattr(app_config, "failure_handling", None)
        max_hops = getattr(failure_handling, "max_failover_hops", None)
        if isinstance(max_hops, int) and max_hops > 0:
            return max_hops
        return None

    @classmethod
    def _apply_interleaved_thinking_suppression_from_session(
        cls,
        *,
        context: RequestContext | None,
        session: Any | None,
    ) -> None:
        if context is None or session is None:
            return
        base_state = cls._session_state_as_session_state(
            getattr(session, "state", None)
        )
        if base_state is None:
            return
        raw_state = base_state.interleaved_thinking_state
        if not isinstance(raw_state, dict):
            return
        remaining = raw_state.get("regular_turns_remaining", 0)
        if isinstance(remaining, int) and remaining > 0:
            context.extensions[INTERLEAVED_THINKING_SUPPRESS_THINKER_SELECTION_KEY] = (
                True
            )

    @staticmethod
    def _apply_interleaved_thinking_suppression_for_first_turn(
        *,
        context: RequestContext | None,
        is_first_turn: bool,
    ) -> None:
        if context is None or not is_first_turn:
            return
        context.extensions[INTERLEAVED_THINKING_SUPPRESS_THINKER_SELECTION_KEY] = True

    @staticmethod
    def _require_explicit_backend_for_surface(surface: RoutingSurface) -> bool:
        """Return whether composite leaf parsing must enforce backend:model leaves."""
        return surface is RoutingSurface.REPLACEMENT_BRIDGE

    @staticmethod
    def _is_composite_leaf_resolution(
        *,
        context: RequestContext | None,
        request: ChatRequest,
    ) -> bool:
        if context is not None and bool(
            context.extensions.get(COMPOSITE_LEAF_RESOLUTION_FLAG)
        ):
            return True

        extra_body = request.extra_body
        if not isinstance(extra_body, dict):
            return False
        if not bool(extra_body.get(COMPOSITE_LEAF_RESOLUTION_EXTRA_BODY_KEY)):
            return False
        leaf_selector = extra_body.get(COMPOSITE_LEAF_SELECTOR_EXTRA_BODY_KEY)
        return isinstance(leaf_selector, str) and leaf_selector == request.model

    @staticmethod
    def _try_read_and_clear_composite_leaf_precheck(
        request: ChatRequest,
    ) -> tuple[str, str, dict[str, JsonValue]] | None:
        """Consume composite-leaf bridge fields produced by CompositeLeafTargetResolverAdapter.

        Composite routing already parsed the leaf once in :class:`CompositeSelectorParser`.
        The adapter forwards ``backend_type``, ``model_name``, and URI params via
        ``extra_body`` so this resolver can avoid calling :func:`parse_model_with_params`
        again for the same selector string.
        """
        extra_body = getattr(request, "extra_body", None)
        if not isinstance(extra_body, dict):
            return None
        if not bool(extra_body.get(COMPOSITE_LEAF_RESOLUTION_EXTRA_BODY_KEY)):
            return None
        leaf_selector = extra_body.get(COMPOSITE_LEAF_SELECTOR_EXTRA_BODY_KEY)
        if not isinstance(leaf_selector, str) or leaf_selector != request.model:
            return None
        backend = extra_body.get(COMPOSITE_LEAF_PARSED_BACKEND_EXTRA_BODY_KEY)
        model_name = extra_body.get(COMPOSITE_LEAF_PARSED_MODEL_EXTRA_BODY_KEY)
        if not isinstance(backend, str) or not isinstance(model_name, str):
            return None
        extra_body.pop(COMPOSITE_LEAF_PARSED_BACKEND_EXTRA_BODY_KEY, None)
        extra_body.pop(COMPOSITE_LEAF_PARSED_MODEL_EXTRA_BODY_KEY, None)
        uri_params = BackendModelResolver._normalize_uri_params(
            extra_body.get(RESOLVED_URI_PARAMS_EXTRA_BODY_KEY)
        )
        return (backend, model_name, uri_params)

    @staticmethod
    def _normalize_uri_params(raw_value: Any) -> dict[str, JsonValue]:
        if not isinstance(raw_value, dict):
            return {}
        normalized: dict[str, JsonValue] = {}
        for key, value in raw_value.items():
            if not isinstance(key, str):
                continue
            if isinstance(value, dict):
                if all(isinstance(nested_key, str) for nested_key in value):
                    normalized[key] = cast(dict[str, JsonValue], value)
                continue
            if isinstance(value, list):
                normalized[key] = cast(list[JsonValue], value)
                continue
            if isinstance(value, str | int | float | bool) or value is None:
                normalized[key] = value
        return normalized

    @classmethod
    def _extract_preserved_uri_params(
        cls,
        request: ChatRequest,
        context: RequestContext | None,
    ) -> dict[str, JsonValue]:
        extra_body = getattr(request, "extra_body", None)
        if (
            isinstance(extra_body, dict)
            and RESOLVED_URI_PARAMS_EXTRA_BODY_KEY in extra_body
        ):
            # Respect explicit request-scoped URI params first.
            # An explicit empty map means "clear previously resolved params".
            extra_value = extra_body.get(RESOLVED_URI_PARAMS_EXTRA_BODY_KEY)
            return cls._normalize_uri_params(extra_value)

        if context is not None:
            context_value = context.extensions.get(_RESOLVED_URI_PARAMS_CONTEXT_KEY)
            normalized_context = cls._normalize_uri_params(context_value)
            if normalized_context:
                return normalized_context

        return {}

    def _raise_no_available_instance(
        self,
        *,
        backend_type: str,
        effective_model: str,
        excluded_backends: set[str],
    ) -> NoReturn:
        availability_reason = None
        describe = getattr(self._routing_service, "describe_ineligibility", None)
        if callable(describe):
            availability_reason = describe(
                backend_type, effective_model, excluded_backends
            )
        raise RoutingError(
            message=(
                f"No available backend instance for '{backend_type}:{effective_model}'."
            ),
            details={
                **self._build_temporarily_unavailable_details(
                    backend_type=backend_type,
                    model=effective_model,
                ),
                "availability_reason": availability_reason,
            },
        )

    @staticmethod
    def _extract_request_context_tokens(request: ChatRequest) -> int | None:
        extra_body = getattr(request, "extra_body", None)
        if not isinstance(extra_body, dict):
            return None

        raw_value = extra_body.get("request_context_tokens")
        if isinstance(raw_value, bool):
            return None
        if isinstance(raw_value, int):
            return raw_value if raw_value >= 0 else None
        if isinstance(raw_value, str):
            stripped = raw_value.strip()
            if stripped.isdigit():
                return int(stripped)
        return None

    @staticmethod
    def _persist_uri_params_in_context(
        context: RequestContext | None,
        uri_params: dict[str, JsonValue],
    ) -> None:
        if context is None:
            return
        context.extensions[_RESOLVED_URI_PARAMS_CONTEXT_KEY] = dict(uri_params)

    @staticmethod
    def _build_unknown_model_details(*, model: str) -> dict[str, Any]:
        return {
            "code": "unknown_model",
            "category": "validation",
            "retryable": False,
            "model": model,
        }

    @staticmethod
    def _build_temporarily_unavailable_details(
        *,
        backend_type: str,
        model: str,
    ) -> dict[str, Any]:
        return {
            "code": "temporarily_unavailable",
            "category": "availability",
            "retryable": True,
            "backend_type": backend_type,
            "model": model,
        }
