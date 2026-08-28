"""
SSO Web Interface for authentication flows.

This module provides FastAPI endpoints for the SSO authentication flow:
- /auth/login: Provider selection and SSO initiation
- /auth/callback: OAuth2/SAML callback handling
- /auth/confirm: Confirmation code entry (single-user mode)
- /auth/success: Token display after successful authorization
"""

import asyncio
import logging
import secrets
import time
from typing import Annotated, Any

from fastapi import APIRouter, Form, HTTPException, Query, Request, Response
from fastapi.responses import HTMLResponse, RedirectResponse

from src.core.auth.sso.authorization_service import (
    AuthorizationMode,
    AuthorizationService,
)
from src.core.auth.sso.captcha_service import CaptchaService
from src.core.auth.sso.config import SSOConfig
from src.core.auth.sso.database import DatabaseManager, TokenRepository
from src.core.auth.sso.exceptions import (
    AuthenticationError,
    AuthorizationError,
    ConfigurationError,
    SSOException,
)
from src.core.auth.sso.rate_limit_service import RateLimitService
from src.core.auth.sso.sso_service import SSOService
from src.core.auth.sso.token_service import TokenService

logger = logging.getLogger(__name__)


def create_sso_router(
    sso_config: SSOConfig,
    sso_service: SSOService,
    token_service: TokenService,
    authorization_service: AuthorizationService,
    database_manager: DatabaseManager,
    rate_limit_service: RateLimitService,
    base_url: str,
    captcha_service: CaptchaService | None = None,
) -> APIRouter:
    """
    Create FastAPI router for SSO authentication endpoints.

    Args:
        sso_config: SSO configuration
        sso_service: SSO service for OAuth2/SAML flows
        token_service: Token generation and verification service
        authorization_service: Authorization service (confirmation code or API)
        database_manager: Database manager for token storage
        rate_limit_service: Rate limiting service
        base_url: Base URL for the proxy (e.g., "http://localhost:8000")
        captcha_service: Service used to validate captcha responses

    Returns:
        FastAPI router with SSO endpoints
    """
    router = APIRouter(prefix="/auth", tags=["sso"])

    # Lock for protecting state stores from concurrent access
    _state_lock = asyncio.Lock()
    # Store state -> provider mapping for callback validation
    # In production, this should be in Redis or database
    # Each entry includes '_created_at' timestamp for TTL cleanup
    _state_store: dict[str, str | dict[str, Any]] = {}
    _login_sessions: dict[str, dict[str, Any]] = {}
    # TTL for OAuth state entries (15 minutes - OAuth flows should complete quickly)
    _state_ttl_seconds: int = 900
    # Maximum entries to prevent memory exhaustion from abandoned flows
    _max_state_entries: int = 1000
    captcha_service = captcha_service or CaptchaService(sso_config.captcha)

    async def _cleanup_expired_state() -> None:
        """Remove expired entries from state stores to prevent memory leaks.

        This is called before adding new entries to ensure abandoned OAuth flows
        don't accumulate indefinitely.
        """
        now = time.time()

        async with _state_lock:
            # Cleanup _state_store
            expired_states = [
                key
                for key, value in _state_store.items()
                if isinstance(value, dict)
                and now - value.get("_created_at", 0) > _state_ttl_seconds
            ]
            for key in expired_states:
                del _state_store[key]
            if expired_states and logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Cleaned up %d expired OAuth state entries", len(expired_states)
                )

            # Cleanup _login_sessions
            expired_sessions = [
                key
                for key, value in _login_sessions.items()
                if now - value.get("_created_at", 0) > _state_ttl_seconds
            ]
            for key in expired_sessions:
                del _login_sessions[key]
            if expired_sessions and logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "Cleaned up %d expired login session entries", len(expired_sessions)
                )

            # Enforce max entries limit (remove oldest first)
            if len(_state_store) > _max_state_entries:
                # Sort by creation time and remove oldest
                sorted_states = sorted(
                    [
                        (k, v)
                        for k, v in _state_store.items()
                        if isinstance(v, dict) and "_created_at" in v
                    ],
                    key=lambda x: x[1].get("_created_at", 0),
                )
                to_remove = len(_state_store) - _max_state_entries
                for key, _ in sorted_states[:to_remove]:
                    del _state_store[key]
                if to_remove > 0 and logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Evicted %d oldest OAuth state entries due to capacity limit",
                        to_remove,
                    )

            if len(_login_sessions) > _max_state_entries:
                sorted_sessions = sorted(
                    _login_sessions.items(),
                    key=lambda x: x[1].get("_created_at", 0),
                )
                to_remove = len(_login_sessions) - _max_state_entries
                for key, _ in sorted_sessions[:to_remove]:
                    del _login_sessions[key]
                if to_remove > 0 and logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Evicted %d oldest login session entries due to capacity limit",
                        to_remove,
                    )

    async def _get_request_value(request: Request, key: str) -> str | None:
        """Extract a value from form data or query parameters."""
        if request.method in {"POST", "PUT", "PATCH"}:
            form = await request.form()
            if key in form:
                return str(form[key])
        return request.query_params.get(key)

    @router.get("/login", response_class=HTMLResponse, response_model=None)
    async def login(
        request: Request, token: Annotated[str | None, Query()] = None
    ) -> Response:
        """
        Display provider selection or redirect to configured IdP.

        If only one provider is configured, redirects directly to that provider.
        If multiple providers are configured, displays a selection page.

        Validates one-off login token if present. Returns 403 if invalid.

        Requirements: 2.1
        """
        try:
            # Verify login token
            if not token:
                # No token provided - reject
                return HTMLResponse(status_code=403)

            token_repo = TokenRepository(database_manager.database_path)
            is_valid, agent_token_id = await token_repo.verify_and_consume_login_token(
                token
            )

            if not is_valid:
                # Invalid or expired token - reject
                return HTMLResponse(status_code=403)

            # Store agent_token_id in state for re-authentication flow
            # This will be retrieved in callback to update existing token

            # Get only enabled providers (not disabled ones)
            providers = sso_service.get_enabled_providers()

            if not providers:
                return HTMLResponse(
                    content=_render_error_page(
                        "No Identity Providers Configured",
                        "The SSO authentication system is not properly configured. "
                        "Please contact your administrator.",
                    ),
                    status_code=500,
                )

            captcha_enabled = captcha_service.is_enabled

            # If only one provider and no captcha is required, redirect directly
            if len(providers) == 1 and not captcha_enabled:
                provider = providers[0]
                state = secrets.token_urlsafe(32)
                await _cleanup_expired_state()
                async with _state_lock:
                    _state_store[state] = {
                        "provider": provider,
                        "agent_token_id": agent_token_id,
                        "_created_at": time.time(),
                    }

                redirect_uri = f"{base_url}/auth/callback"
                auth_url = await sso_service.create_authorization_url(
                    provider, state, redirect_uri
                )

                return RedirectResponse(url=auth_url, status_code=302)

            login_session = secrets.token_urlsafe(16)
            await _cleanup_expired_state()
            async with _state_lock:
                _login_sessions[login_session] = {
                    "providers": providers,
                    "captcha_required": captcha_enabled,
                    "agent_token_id": agent_token_id,
                    "_created_at": time.time(),
                }

            captcha_config = sso_config.captcha if captcha_enabled else None

            # Multiple providers or captcha required: show selection page
            return HTMLResponse(
                content=_render_provider_selection_page(
                    providers=providers,
                    base_url=base_url,
                    login_session=login_session,
                    captcha_site_key=(
                        captcha_config.site_key if captcha_config else None
                    ),
                    captcha_mode=captcha_config.widget_mode if captcha_config else None,
                )
            )

        except (KeyboardInterrupt, SystemExit):
            # Don't interfere with system shutdown signals
            raise
        except ConfigurationError as e:
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "SSO configuration error during login: %s", e, exc_info=True
                )
            return HTMLResponse(
                content=_render_error_page(
                    "Configuration Error",
                    "The SSO authentication system is not properly configured.",
                ),
                status_code=500,
            )
        except (ValueError, TypeError, KeyError, AttributeError) as e:
            # Catch common data validation/access errors
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Data validation error during login: %s",
                    e,
                    exc_info=True,
                )
            return HTMLResponse(
                content=_render_error_page(
                    "Authentication Error",
                    "Failed to initialize authentication due to invalid data.",
                ),
                status_code=500,
            )
        except Exception as e:
            logger.exception("Failed to render login page")
            return HTMLResponse(
                content=_render_error_page(
                    "Authentication Error",
                    f"Failed to initialize authentication: {e!s}",
                ),
                status_code=500,
            )

    @router.api_route(
        "/login/{provider}", methods=["GET", "POST"], response_class=Response
    )
    async def login_provider(request: Request, provider: str) -> Response:
        """
        Initiate SSO flow for a specific provider.

        Args:
            provider: Provider name (e.g., 'google', 'github')

        Returns:
            Redirect to provider's authorization URL
        """
        try:
            login_session = await _get_request_value(request, "login_session")
            captcha_token = await _get_request_value(request, "captcha_token")

            # Get session info under lock, then copy to local variable
            async with _state_lock:
                session_info = (
                    _login_sessions.get(login_session) if login_session else None
                )

            if not session_info:
                return HTMLResponse(
                    content=_render_error_page(
                        "Session Invalid",
                        "Your sign-in session could not be validated. Please start over.",
                    ),
                    status_code=403,
                )

            agent_token_id = session_info.get("agent_token_id")

            if provider not in session_info.get("providers", []):
                return HTMLResponse(
                    content=_render_error_page(
                        "Invalid Provider",
                        "The requested identity provider is not available for this session.",
                    ),
                    status_code=400,
                )

            if captcha_service.is_enabled:

                verification = await captcha_service.verify(
                    captcha_token, request.client.host if request.client else None
                )
                if not verification.success:
                    error_detail = (
                        f" ({', '.join(verification.error_codes)})"
                        if verification.error_codes
                        else ""
                    )
                    return HTMLResponse(
                        content=_render_error_page(
                            "Verification Failed",
                            f"Captcha verification failed{error_detail}. Please try again.",
                        ),
                        status_code=403,
                    )

                if login_session is not None:
                    async with _state_lock:
                        _login_sessions.pop(login_session, None)
            else:
                if login_session is not None:
                    async with _state_lock:
                        _login_sessions.pop(login_session, None)

            # Generate state for CSRF protection
            state = secrets.token_urlsafe(32)
            await _cleanup_expired_state()
            async with _state_lock:
                _state_store[state] = {
                    "provider": provider,
                    "agent_token_id": agent_token_id,
                    "_created_at": time.time(),
                }

            redirect_uri = f"{base_url}/auth/callback"
            auth_url = await sso_service.create_authorization_url(
                provider, state, redirect_uri
            )

            return RedirectResponse(url=auth_url, status_code=302)

        except ConfigurationError as e:
            if logger.isEnabledFor(logging.ERROR):
                logger.error("Provider configuration error: %s", e, exc_info=True)
            raise HTTPException(status_code=400, detail=str(e)) from e
        except (ValueError, TypeError, KeyError, AttributeError) as e:
            # Catch common data validation/access errors
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Data validation error during SSO initiation for provider %s: %s",
                    provider,
                    e,
                    exc_info=True,
                )
            raise HTTPException(
                status_code=400, detail=f"Invalid request data: {e!s}"
            ) from e
        except Exception as e:
            if logger.isEnabledFor(logging.ERROR):
                logger.exception("Failed to initiate SSO for provider %s", provider)
            raise HTTPException(
                status_code=500, detail=f"Failed to initiate authentication: {e!s}"
            ) from e

    @router.api_route(
        "/callback",
        methods=["GET", "POST"],
        response_model=None,
    )
    async def callback(
        request: Request,
        code: Annotated[str | None, Query()] = None,
        state: Annotated[str | None, Query()] = None,
        error: Annotated[str | None, Query()] = None,
        error_description: Annotated[str | None, Query()] = None,
        saml_response: Annotated[str | None, Query(alias="SAMLResponse")] = None,
    ) -> Response:
        """
        Handle OAuth2/SAML callbacks.

        This endpoint receives the authorization code from the IdP and:
        1. Validates the state parameter (CSRF protection)
        2. Exchanges the code for user information
        3. Initiates the authorization flow (confirmation code or API)
        4. Redirects to appropriate next step

        Requirements: 11.4
        """
        # Capture SAML POST body if present
        if request.method == "POST":
            try:

                form = await request.form()
                form_saml = form.get("SAMLResponse")
                if isinstance(form_saml, str):
                    saml_response = saml_response or form_saml

                if not state:
                    form_relay = form.get("RelayState")
                    if isinstance(form_relay, str):
                        state = form_relay
            except Exception as e:
                # Continue with query params if form parsing fails
                if logger.isEnabledFor(logging.WARNING):
                    logger.warning(
                        "SAML POST form parsing failed, continuing with query params: %s",
                        e,
                        exc_info=True,
                    )

        relay_state = request.query_params.get("RelayState") or state

        # Handle OAuth2 errors from provider
        if error:
            error_msg = error_description or error
            if logger.isEnabledFor(logging.WARNING):
                logger.warning("OAuth2 error from provider: %s", error_msg)
            return HTMLResponse(
                content=_render_error_page(
                    "Authentication Failed",
                    f"The identity provider returned an error: {error_msg}",
                ),
                status_code=400,
            )

        # Validate required parameters
        if not relay_state and not state and not saml_response:
            return HTMLResponse(
                content=_render_error_page(
                    "Invalid Callback",
                    "Missing required parameters. Please try again.",
                ),
                status_code=400,
            )

        # Validate state (CSRF protection)
        state_key = relay_state or state
        if not state_key:
            return HTMLResponse(
                content=_render_error_page(
                    "Invalid Callback",
                    "Missing state parameter.",
                ),
                status_code=400,
            )

        async with _state_lock:
            state_data = _state_store.pop(state_key, None)
        if not state_data:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Invalid or expired state parameter: %s...", (state_key or "")[:8]
                )
            return HTMLResponse(
                content=_render_error_page(
                    "Invalid Session",
                    "Your authentication session has expired or is invalid. Please try again.",
                ),
                status_code=400,
            )

        # Extract provider and agent_token_id from state
        if isinstance(state_data, dict):
            provider = state_data.get("provider")
            agent_token_id = state_data.get("agent_token_id")
        else:
            # Backward compatibility: if state_data is just a string
            provider = state_data
            agent_token_id = None

        if not provider:
            if logger.isEnabledFor(logging.WARNING):
                logger.warning("Invalid state data: %s...", (state_key or "")[:8])
            return HTMLResponse(
                content=_render_error_page(
                    "Invalid Session",
                    "Your authentication session has expired or is invalid. Please try again.",
                ),
                status_code=400,
            )

        try:
            # Get client IP for authorization
            client_ip = request.client.host if request.client else "unknown"

            # Exchange code for user info
            redirect_uri = f"{base_url}/auth/callback"
            sso_result = await sso_service.handle_callback(
                provider,
                code,
                state_key,
                redirect_uri,
                saml_response=saml_response,
            )

            if not sso_result.success:
                if logger.isEnabledFor(logging.ERROR):
                    logger.error(
                        "SSO callback failed: %s", sso_result.error, exc_info=True
                    )
                return HTMLResponse(
                    content=_render_error_page(
                        "Authentication Failed",
                        f"Failed to authenticate with {provider}: {sso_result.error}",
                    ),
                    status_code=401,
                )

            if not sso_result.user_id or not sso_result.user_email:
                if logger.isEnabledFor(logging.ERROR):
                    logger.error(
                        "SSO callback missing user info: %s", sso_result, exc_info=True
                    )
                return HTMLResponse(
                    content=_render_error_page(
                        "Authentication Failed",
                        "Failed to retrieve user information from identity provider.",
                    ),
                    status_code=401,
                )

            user_id = sso_result.user_id
            user_email = sso_result.user_email

            # Now handle authorization based on mode
            if authorization_service.mode == AuthorizationMode.SINGLE_USER:
                # Create pending authorization and log confirmation code
                await authorization_service.create_pending_authorization(
                    sso_state=state_key,
                    user_email=user_email,
                    user_id=user_id,
                    provider=provider,
                    client_ip=client_ip,
                )

                # Redirect to confirmation page
                return RedirectResponse(
                    url=f"/auth/confirm?state={state_key}", status_code=302
                )

            elif authorization_service.mode == AuthorizationMode.ENTERPRISE:
                # Query authorization API
                auth_result = await authorization_service.query_authorization_api(
                    user_id=user_id,
                    user_email=user_email,
                    client_ip=client_ip,
                )

                if not auth_result.authorized:
                    if logger.isEnabledFor(logging.WARNING):
                        logger.warning(
                            "Authorization denied for user %s: %s",
                            user_email,
                            auth_result.error,
                        )
                    return HTMLResponse(
                        content=_render_error_page(
                            "Access Denied",
                            "You are not authorized to use this service. "
                            "Please contact your administrator if you believe this is an error.",
                        ),
                        status_code=403,
                    )

                # Authorization successful - check for existing token (re-authentication)
                from datetime import datetime, timedelta, timezone

                from src.core.auth.sso.models import TokenRecord

                token_repo = TokenRepository(database_manager.database_path)

                # First check if this is a re-auth flow (agent_token_id provided)
                if agent_token_id:
                    # This is re-authentication - update the specified token
                    existing_token = await token_repo.get_by_id(agent_token_id)
                    if existing_token and existing_token.user_id == user_id:
                        # Security check: ensure the token belongs to the same user
                        # Re-authentication: update existing token's auth status
                        # Requirements: 5.1, 5.3, 9.3
                        await token_repo.update_auth_status(
                            existing_token.id,
                            authenticated=True,
                            expiry=datetime.now(timezone.utc)
                            + timedelta(hours=sso_config.session_lifetime_hours),
                        )

                        if logger.isEnabledFor(logging.INFO):
                            logger.info(
                                "Re-authenticated token %s for user %s",
                                existing_token.id,
                                user_email,
                            )

                        # Redirect to success page indicating re-authentication
                        # Note: We don't show the token again for security
                        return HTMLResponse(
                            content=_render_reauth_success_page(),
                            status_code=200,
                        )
                    else:
                        # Token doesn't exist or belongs to different user
                        if logger.isEnabledFor(logging.WARNING):
                            logger.warning(
                                "Re-auth attempted with invalid agent_token_id: %s",
                                agent_token_id,
                            )
                        # Fall through to check for existing token by user_id
                        agent_token_id = None

                # Check for existing token by user_id (not via re-auth flow)
                if not agent_token_id:
                    existing_token = await token_repo.find_by_user_id(user_id)

                    if existing_token:
                        # User has existing token - update it (implicit re-auth)
                        # Requirements: 5.1, 5.3
                        await token_repo.update_auth_status(
                            existing_token.id,
                            authenticated=True,
                            expiry=datetime.now(timezone.utc)
                            + timedelta(hours=sso_config.session_lifetime_hours),
                        )

                        if logger.isEnabledFor(logging.INFO):
                            logger.info(
                                "Implicitly re-authenticated token %s for user %s",
                                existing_token.id,
                                user_email,
                            )

                        # Redirect to success page indicating re-authentication
                        # Note: We don't show the token again for security
                        return HTMLResponse(
                            content=_render_reauth_success_page(),
                            status_code=200,
                        )
                    # First-time authentication: generate new token
                generated = token_service.generate_token()

                # Store token in database
                token_record = TokenRecord(
                    id=secrets.token_hex(16),
                    token_hash=generated.hash,
                    user_id=user_id,
                    user_email=user_email,
                    provider=provider,
                    is_authenticated=True,
                    is_active=True,
                    created_at=datetime.now(timezone.utc),
                    last_authenticated_at=datetime.now(timezone.utc),
                    auth_expires_at=datetime.now(timezone.utc)
                    + timedelta(hours=sso_config.session_lifetime_hours),
                )

                await token_repo.store_token(token_record)

                # Redirect to success page with token
                return RedirectResponse(
                    url=f"/auth/success?token={generated.plaintext}", status_code=302
                )

            else:
                raise ValueError(
                    f"Unknown authorization mode: {authorization_service.mode}"
                )

        except AuthenticationError as e:
            if logger.isEnabledFor(logging.ERROR):
                logger.error("Authentication error: %s", e, exc_info=True)
            return HTMLResponse(
                content=_render_error_page(
                    "Authentication Error", f"Authentication failed: {e!s}"
                ),
                status_code=401,
            )
        except AuthorizationError as e:
            if logger.isEnabledFor(logging.ERROR):
                logger.error("Authorization error: %s", e, exc_info=True)
            return HTMLResponse(
                content=_render_error_page(
                    "Authorization Error", f"Authorization failed: {e!s}"
                ),
                status_code=403,
            )
        except (KeyboardInterrupt, SystemExit):
            # Don't interfere with system shutdown signals
            raise
        except HTTPException:
            # Re-raise FastAPI HTTP exceptions to let FastAPI handle them
            raise
        except (ValueError, TypeError, KeyError, AttributeError) as e:
            # Catch common data validation/access errors
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Data validation error during callback processing: %s",
                    e,
                    exc_info=True,
                )
            return HTMLResponse(
                content=_render_error_page(
                    "Internal Error",
                    "An unexpected error occurred. Please try again or contact support.",
                ),
                status_code=500,
            )
        except (RuntimeError, OSError, asyncio.TimeoutError) as e:
            # Catch runtime/system-level errors that might occur during async operations
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Runtime error during callback processing: %s",
                    e,
                    exc_info=True,
                )
            return HTMLResponse(
                content=_render_error_page(
                    "Internal Error",
                    "An unexpected error occurred. Please try again or contact support.",
                ),
                status_code=500,
            )
        except asyncio.CancelledError:
            # Propagate cancellation - don't suppress it
            raise
        except SSOException as e:
            # Known SSO authentication errors - log with context
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "SSO error during callback processing: %s",
                    e,
                    exc_info=True,
                )
            return HTMLResponse(
                content=_render_error_page(
                    "Authentication Error",
                    f"Failed to complete authentication: {e!s}",
                ),
                status_code=500,
            )
        except Exception:
            # Fallback for truly unexpected errors - log with full context
            logger.exception("Unexpected error during callback processing")
            return HTMLResponse(
                content=_render_error_page(
                    "Internal Error",
                    "An unexpected error occurred. Please try again or contact support.",
                ),
                status_code=500,
            )

    @router.get("/confirm", response_class=HTMLResponse)
    async def confirm_get(
        request: Request, state: Annotated[str | None, Query()] = None
    ) -> HTMLResponse:
        """
        Display confirmation code form (single-user mode).

        Requirements: 6.2
        """
        if not state:
            return HTMLResponse(
                content=_render_error_page(
                    "Invalid Request", "Missing session state. Please try again."
                ),
                status_code=400,
            )

        return HTMLResponse(content=_render_confirmation_form(state, base_url))

    @router.post("/confirm", response_model=None)
    async def confirm_post(
        request: Request,
        state: Annotated[str, Form()],
        code: Annotated[str, Form()],
    ) -> Response:
        """
        Handle confirmation code submission (single-user mode).

        Requirements: 6.2
        """
        try:
            # Get client IP for rate limiting
            client_ip = request.client.host if request.client else "unknown"

            # Verify confirmation code
            result = await authorization_service.verify_confirmation_code(
                sso_state=state, code=code, client_ip=client_ip
            )

            if result.success:
                # Code verified! Now we need to get user info from pending auth
                import aiosqlite

                async with aiosqlite.connect(database_manager.database_path) as db:
                    db.row_factory = aiosqlite.Row
                    cursor = await db.execute(
                        """
                        SELECT user_id, user_email, provider
                        FROM pending_authorizations
                        WHERE sso_state = ?
                        """,
                        (state,),
                    )
                    row = await cursor.fetchone()

                if not row:
                    # This shouldn't happen if verify succeeded, but handle it
                    return HTMLResponse(
                        content=_render_error_page(
                            "Session Error",
                            "Could not retrieve session information. Please try again.",
                        ),
                        status_code=500,
                    )

                # Check for existing token (re-authentication)
                from datetime import datetime, timedelta, timezone

                from src.core.auth.sso.models import TokenRecord

                token_repo = TokenRepository(database_manager.database_path)
                existing_token = await token_repo.find_by_user_id(row["user_id"])

                if existing_token:
                    # Re-authentication: update existing token's auth status
                    # Requirements: 5.1, 5.3
                    await token_repo.update_auth_status(
                        existing_token.id,
                        authenticated=True,
                        expiry=datetime.now(timezone.utc)
                        + timedelta(hours=sso_config.session_lifetime_hours),
                    )

                    # Redirect to success page indicating re-authentication
                    # Note: We don't show the token again for security
                    return HTMLResponse(
                        content=_render_reauth_success_page(),
                        status_code=200,
                    )
                else:
                    # First-time authentication: generate new token
                    generated = token_service.generate_token()

                    # Store token in database
                    token_record = TokenRecord(
                        id=secrets.token_hex(16),
                        token_hash=generated.hash,
                        user_id=row["user_id"],
                        user_email=row["user_email"],
                        provider=row["provider"],
                        is_authenticated=True,
                        is_active=True,
                        created_at=datetime.now(timezone.utc),
                        last_authenticated_at=datetime.now(timezone.utc),
                        auth_expires_at=datetime.now(timezone.utc)
                        + timedelta(hours=sso_config.session_lifetime_hours),
                    )

                    await token_repo.store_token(token_record)

                    # Redirect to success page with token
                    return RedirectResponse(
                        url=f"/auth/success?token={generated.plaintext}",
                        status_code=302,
                    )

            else:
                # Code verification failed
                if result.must_reauthenticate:
                    return HTMLResponse(
                        content=_render_error_page(
                            "Authentication Required",
                            "Your confirmation code has expired or you have exceeded "
                            "the maximum number of attempts. Please authenticate again.",
                        ),
                        status_code=401,
                    )
                else:
                    # Show form again with error
                    return HTMLResponse(
                        content=_render_confirmation_form(
                            state,
                            base_url,
                            error=f"Incorrect code. {result.attempts_remaining} attempts remaining.",
                        )
                    )

        except AuthorizationError as e:
            # Rate limit or other authorization error
            if "Rate limit" in str(e):
                return HTMLResponse(
                    content=_render_error_page(
                        "Too Many Attempts",
                        str(e) + " Please wait before trying again.",
                    ),
                    status_code=429,
                )
            else:
                return HTMLResponse(
                    content=_render_error_page("Authorization Error", str(e)),
                    status_code=403,
                )
        except (KeyboardInterrupt, SystemExit):
            # Don't interfere with system shutdown signals
            raise
        except HTTPException:
            # Re-raise FastAPI HTTP exceptions to let FastAPI handle them
            raise
        except (ValueError, TypeError, KeyError, AttributeError) as e:
            # Catch common data validation/access errors
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Data validation error during confirmation code processing: %s",
                    e,
                    exc_info=True,
                )
            return HTMLResponse(
                content=_render_error_page(
                    "Internal Error",
                    "An unexpected error occurred. Please try again.",
                ),
                status_code=500,
            )
        except (RuntimeError, OSError, asyncio.TimeoutError) as e:
            # Catch runtime/system-level errors that might occur during async operations
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "Runtime error during confirmation code processing: %s",
                    e,
                    exc_info=True,
                )
            return HTMLResponse(
                content=_render_error_page(
                    "Internal Error",
                    "An unexpected error occurred. Please try again.",
                ),
                status_code=500,
            )
        except asyncio.CancelledError:
            # Propagate cancellation - don't suppress it
            raise
        except SSOException as e:
            # Known SSO authentication errors - log with context
            if logger.isEnabledFor(logging.ERROR):
                logger.error(
                    "SSO error during confirmation code processing: %s",
                    e,
                    exc_info=True,
                )
            return HTMLResponse(
                content=_render_error_page(
                    "Authentication Error",
                    f"Failed to process confirmation: {e!s}",
                ),
                status_code=500,
            )
        except Exception:
            # Fallback for truly unexpected errors - log with full context
            logger.exception("Error processing confirmation code")
            return HTMLResponse(
                content=_render_error_page(
                    "Internal Error",
                    "An unexpected error occurred. Please try again.",
                ),
                status_code=500,
            )

    @router.get("/success", response_class=HTMLResponse)
    async def success(
        request: Request, token: Annotated[str | None, Query()] = None
    ) -> HTMLResponse:
        """
        Display generated token with copy button and configuration instructions.

        Requirements: 3.3, 3.6
        """
        if not token:
            return HTMLResponse(
                content=_render_error_page(
                    "Invalid Request", "Missing token. Please try again."
                ),
                status_code=400,
            )

        return HTMLResponse(content=_render_success_page(token))

    return router


# =============================================================================
# HTML Templates
# =============================================================================


def _render_provider_selection_page(
    providers: list[str],
    base_url: str,
    login_session: str,
    captcha_site_key: str | None = None,
    captcha_mode: str | None = None,
) -> str:
    """
    Render provider selection page.

    Args:
        providers: List of provider names
        base_url: Base URL for the proxy
        login_session: One-time login session identifier
        captcha_site_key: Optional captcha site key to render invisible widget
        captcha_mode: Captcha widget mode

    Returns:
        HTML content
    """
    requires_captcha = bool(captcha_site_key)
    provider_buttons = []
    for provider in providers:
        button_attributes = (
            f'type="button" onclick="handleProviderClick(\'provider-{provider}\')"'
            if requires_captcha
            else 'type="submit"'
        )
        provider_buttons.append(f"""
            <form id="provider-{provider}" class="provider-form" method="POST" action="{base_url}/auth/login/{provider}">
                <input type="hidden" name="login_session" value="{login_session}">
                <input type="hidden" name="captcha_token" value="">
                <button {button_attributes} class="provider-button">
                    <span class="provider-icon">{_get_provider_icon(provider)}</span>
                    <span class="provider-name">{_get_provider_display_name(provider)}</span>
                </button>
            </form>
            """)

    captcha_html = ""
    if requires_captcha:
        captcha_size = captcha_mode or "invisible"
        captcha_html = f"""
        <div id="captcha-panel" class="captcha-panel">
            <div id="turnstile-container" class="turnstile-container"></div>
            <p class="captcha-hint">Additional verification is required to start SSO.</p>
        </div>
        <script src="https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit" async defer></script>
        <script>
            let turnstileWidgetId = null;
            let pendingFormId = null;

            function ensureTurnstile() {{
                if (turnstileWidgetId !== null) {{
                    return turnstileWidgetId;
                }}
                turnstileWidgetId = turnstile.render("#turnstile-container", {{
                    sitekey: "{captcha_site_key}",
                    callback: onCaptchaComplete,
                    size: "{captcha_size}",
                    retry: "auto",
                }});
                return turnstileWidgetId;
            }}

            function handleProviderClick(formId) {{
                pendingFormId = formId;
                const widgetId = ensureTurnstile();
                turnstile.execute(widgetId);
            }}

            function onCaptchaComplete(token) {{
                if (!pendingFormId) {{
                    return;
                }}
                const targetForm = document.getElementById(pendingFormId);
                if (!targetForm) {{
                    return;
                }}
                const tokenInput = targetForm.querySelector('input[name="captcha_token"]');
                if (tokenInput) {{
                    tokenInput.value = token;
                }}
                targetForm.submit();
            }}
        </script>
        """

    return f"""
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Sign In - LLM Proxy</title>
    <style>
        * {{
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }}
        body {{
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            padding: 20px;
        }}
        .container {{
            background: white;
            border-radius: 12px;
            box-shadow: 0 20px 60px rgba(0, 0, 0, 0.3);
            padding: 40px;
            max-width: 450px;
            width: 100%;
        }}
        h1 {{
            color: #333;
            font-size: 28px;
            margin-bottom: 10px;
            text-align: center;
        }}
        .subtitle {{
            color: #666;
            font-size: 14px;
            text-align: center;
            margin-bottom: 30px;
        }}
        .provider-list {{
            display: flex;
            flex-direction: column;
            gap: 12px;
        }}
        .provider-form {{
            margin: 0;
        }}
        .provider-button {{
            display: flex;
            align-items: center;
            padding: 16px 20px;
            border: 2px solid #e0e0e0;
            border-radius: 8px;
            text-decoration: none;
            color: #333;
            transition: all 0.2s ease;
            background: white;
            width: 100%;
        }}
        .provider-button:hover {{
            border-color: #667eea;
            background: #f8f9ff;
            transform: translateY(-2px);
            box-shadow: 0 4px 12px rgba(102, 126, 234, 0.2);
        }}
        .provider-icon {{
            font-size: 24px;
            margin-right: 16px;
            width: 32px;
            text-align: center;
        }}
        .provider-name {{
            font-size: 16px;
            font-weight: 500;
        }}
        .footer {{
            margin-top: 30px;
            text-align: center;
            color: #999;
            font-size: 12px;
        }}
        .captcha-panel {{
            margin-top: 18px;
            padding: 14px;
            border: 1px dashed #cfd2ff;
            border-radius: 8px;
            background: #f7f8ff;
        }}
        .captcha-hint {{
            margin-top: 8px;
            font-size: 13px;
            color: #4a4f6f;
            text-align: center;
        }}
    </style>
</head>
<body>
    <div class="container">
        <h1>Sign In</h1>
        <p class="subtitle">Choose your identity provider to continue</p>
        <div class="provider-list">
            {''.join(provider_buttons)}
        </div>
        {captcha_html}
        <div class="footer">
            Secure authentication powered by SSO
        </div>
    </div>
</body>
</html>
"""


def _render_error_page(title: str, message: str) -> str:
    """
    Render error page.

    Args:
        title: Error title
        message: Error message

    Returns:
        HTML content
    """
    return f"""
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{title} - LLM Proxy</title>
    <style>
        * {{
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }}
        body {{
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            padding: 20px;
        }}
        .container {{
            background: white;
            border-radius: 12px;
            box-shadow: 0 20px 60px rgba(0, 0, 0, 0.3);
            padding: 40px;
            max-width: 500px;
            width: 100%;
            text-align: center;
        }}
        .error-icon {{
            font-size: 64px;
            margin-bottom: 20px;
        }}
        h1 {{
            color: #d32f2f;
            font-size: 24px;
            margin-bottom: 16px;
        }}
        p {{
            color: #666;
            font-size: 16px;
            line-height: 1.6;
            margin-bottom: 24px;
        }}
        .button {{
            display: inline-block;
            padding: 12px 24px;
            background: #667eea;
            color: white;
            text-decoration: none;
            border-radius: 6px;
            font-weight: 500;
            transition: background 0.2s ease;
        }}
        .button:hover {{
            background: #5568d3;
        }}
    </style>
</head>
<body>
    <div class="container">
        <div class="error-icon">!</div>
        <h1>{title}</h1>
        <p>{message}</p>
        <a href="/auth/login" class="button">Try Again</a>
    </div>
</body>
</html>
"""


def _get_provider_icon(provider: str) -> str:
    """
    Get icon for provider.

    Args:
        provider: Provider name

    Returns:
        Short ASCII icon label
    """
    icons = {
        "google": "[G]",
        "microsoft": "[MS]",
        "github": "[GH]",
        "linkedin": "[IN]",
        "aws": "[AWS]",
    }
    return icons.get(provider.lower(), "[SSO]")


def _get_provider_display_name(provider: str) -> str:
    """
    Get display name for provider.

    Args:
        provider: Provider name

    Returns:
        Human-readable provider name
    """
    names = {
        "google": "Google",
        "microsoft": "Microsoft",
        "github": "GitHub",
        "linkedin": "LinkedIn",
        "aws": "AWS IAM Identity Center",
    }
    return names.get(provider.lower(), provider.title())


def _render_confirmation_form(
    state: str, base_url: str, error: str | None = None
) -> str:
    """
    Render confirmation code entry form.

    Args:
        state: Session state
        base_url: Base URL for the proxy
        error: Optional error message to display

    Returns:
        HTML content
    """
    error_html = ""
    if error:
        error_html = f"""
        <div class="error-message">
            {error}
        </div>
        """

    return f"""
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Enter Confirmation Code - LLM Proxy</title>
    <style>
        * {{
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }}
        body {{
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            padding: 20px;
        }}
        .container {{
            background: white;
            border-radius: 12px;
            box-shadow: 0 20px 60px rgba(0, 0, 0, 0.3);
            padding: 40px;
            max-width: 450px;
            width: 100%;
        }}
        h1 {{
            color: #333;
            font-size: 28px;
            margin-bottom: 10px;
            text-align: center;
        }}
        .subtitle {{
            color: #666;
            font-size: 14px;
            text-align: center;
            margin-bottom: 30px;
        }}
        .info-box {{
            background: #e3f2fd;
            border-left: 4px solid #2196f3;
            padding: 16px;
            margin-bottom: 24px;
            border-radius: 4px;
        }}
        .info-box p {{
            color: #1565c0;
            font-size: 14px;
            line-height: 1.6;
        }}
        .error-message {{
            background: #ffebee;
            border-left: 4px solid #f44336;
            padding: 16px;
            margin-bottom: 24px;
            border-radius: 4px;
            color: #c62828;
            font-size: 14px;
        }}
        form {{
            display: flex;
            flex-direction: column;
            gap: 20px;
        }}
        .form-group {{
            display: flex;
            flex-direction: column;
            gap: 8px;
        }}
        label {{
            color: #333;
            font-size: 14px;
            font-weight: 500;
        }}
        input[type="text"] {{
            padding: 14px 16px;
            border: 2px solid #e0e0e0;
            border-radius: 8px;
            font-size: 18px;
            letter-spacing: 4px;
            text-align: center;
            font-family: 'Courier New', monospace;
            transition: border-color 0.2s ease;
        }}
        input[type="text"]:focus {{
            outline: none;
            border-color: #667eea;
        }}
        button {{
            padding: 14px 24px;
            background: #667eea;
            color: white;
            border: none;
            border-radius: 8px;
            font-size: 16px;
            font-weight: 500;
            cursor: pointer;
            transition: background 0.2s ease;
        }}
        button:hover {{
            background: #5568d3;
        }}
        button:active {{
            transform: translateY(1px);
        }}
        .footer {{
            margin-top: 24px;
            text-align: center;
            color: #999;
            font-size: 12px;
        }}
    </style>
</head>
<body>
    <div class="container">
        <h1>Enter Confirmation Code</h1>
        <p class="subtitle">Check your server console for the 6-digit code</p>
        
        <div class="info-box">
            <p>
                A 6-digit confirmation code has been logged to your server console.
                Please check the server logs and enter the code below to complete authorization.
            </p>
        </div>
        
        {error_html}
        
        <form method="POST" action="{base_url}/auth/confirm">
            <input type="hidden" name="state" value="{state}">
            <div class="form-group">
                <label for="code">Confirmation Code</label>
                <input 
                    type="text" 
                    id="code" 
                    name="code" 
                    maxlength="6" 
                    pattern="[0-9]{{6}}"
                    placeholder="000000"
                    required
                    autofocus
                    autocomplete="off"
                >
            </div>
            <button type="submit">Verify Code</button>
        </form>
        
        <div class="footer">
            The code expires in 10 minutes
        </div>
    </div>
    
    <script>
        // Auto-format code input
        const codeInput = document.getElementById('code');
        codeInput.addEventListener('input', function(e) {{
            this.value = this.value.replace(/[^0-9]/g, '');
        }});
    </script>
</body>
</html>
"""


def _render_reauth_success_page() -> str:
    """
    Render success page for re-authentication.

    This page is shown when a user with an existing token completes
    SSO re-authentication. It confirms that their session has been
    restored without showing the token again.

    Returns:
        HTML content
    """
    return """
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Re-authentication Successful - LLM Proxy</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            padding: 20px;
        }
        .container {
            background: white;
            border-radius: 12px;
            box-shadow: 0 20px 60px rgba(0, 0, 0, 0.3);
            padding: 40px;
            max-width: 600px;
            width: 100%;
        }
        .success-icon {
            font-size: 64px;
            text-align: center;
            margin-bottom: 20px;
        }
        h1 {
            color: #2e7d32;
            font-size: 28px;
            margin-bottom: 10px;
            text-align: center;
        }
        .subtitle {
            color: #666;
            font-size: 14px;
            text-align: center;
            margin-bottom: 30px;
        }
        .info-box {
            background: #e3f2fd;
            border-left: 4px solid #2196f3;
            padding: 20px;
            margin-bottom: 24px;
            border-radius: 4px;
        }
        .info-box h2 {
            color: #1565c0;
            font-size: 18px;
            margin-bottom: 12px;
        }
        .info-box p {
            color: #1976d2;
            font-size: 14px;
            line-height: 1.6;
            margin-bottom: 8px;
        }
        .info-box p:last-child {
            margin-bottom: 0;
        }
        .info-box strong {
            font-weight: 600;
        }
        .footer {
            text-align: center;
            color: #999;
            font-size: 12px;
            padding-top: 20px;
            border-top: 1px solid #e0e0e0;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="success-icon">OK</div>
        <h1>Re-authentication Successful!</h1>
        <p class="subtitle">Your session has been restored</p>
        
        <div class="info-box">
            <h2>What This Means</h2>
            <p>
                <strong>Your existing agent token is now active again.</strong>
                You don't need to reconfigure your AI agent - it will continue
                working with the same token you configured previously.
            </p>
            <p>
                Your session has been extended and you can now continue using
                the proxy service without any interruption.
            </p>
        </div>
        
        <div class="footer">
            You can now close this window and continue using your AI agent
        </div>
    </div>
</body>
</html>
"""


def _render_success_page(token: str) -> str:
    """
    Render success page with token and configuration instructions.

    Args:
        token: Generated agent token

    Returns:
        HTML content
    """
    return f"""
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Authentication Successful - LLM Proxy</title>
    <style>
        * {{
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }}
        body {{
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            padding: 20px;
        }}
        .container {{
            background: white;
            border-radius: 12px;
            box-shadow: 0 20px 60px rgba(0, 0, 0, 0.3);
            padding: 40px;
            max-width: 600px;
            width: 100%;
        }}
        .success-icon {{
            font-size: 64px;
            text-align: center;
            margin-bottom: 20px;
        }}
        h1 {{
            color: #2e7d32;
            font-size: 28px;
            margin-bottom: 10px;
            text-align: center;
        }}
        .subtitle {{
            color: #666;
            font-size: 14px;
            text-align: center;
            margin-bottom: 30px;
        }}
        .warning-box {{
            background: #fff3cd;
            border-left: 4px solid #ff9800;
            padding: 16px;
            margin-bottom: 24px;
            border-radius: 4px;
        }}
        .warning-box p {{
            color: #f57c00;
            font-size: 14px;
            line-height: 1.6;
            margin-bottom: 8px;
        }}
        .warning-box p:last-child {{
            margin-bottom: 0;
        }}
        .token-section {{
            margin-bottom: 30px;
        }}
        .token-label {{
            color: #333;
            font-size: 14px;
            font-weight: 500;
            margin-bottom: 8px;
        }}
        .token-container {{
            display: flex;
            gap: 8px;
            align-items: stretch;
        }}
        .token-display {{
            flex: 1;
            padding: 14px 16px;
            background: #f5f5f5;
            border: 2px solid #e0e0e0;
            border-radius: 8px;
            font-family: 'Courier New', monospace;
            font-size: 13px;
            word-break: break-all;
            color: #333;
        }}
        .copy-button {{
            padding: 14px 24px;
            background: #667eea;
            color: white;
            border: none;
            border-radius: 8px;
            font-size: 14px;
            font-weight: 500;
            cursor: pointer;
            transition: all 0.2s ease;
            white-space: nowrap;
        }}
        .copy-button:hover {{
            background: #5568d3;
        }}
        .copy-button:active {{
            transform: translateY(1px);
        }}
        .copy-button.copied {{
            background: #2e7d32;
        }}
        .instructions {{
            background: #f5f5f5;
            border-radius: 8px;
            padding: 24px;
            margin-bottom: 24px;
        }}
        .instructions h2 {{
            color: #333;
            font-size: 18px;
            margin-bottom: 16px;
        }}
        .instructions ol {{
            margin-left: 20px;
            color: #666;
            line-height: 1.8;
        }}
        .instructions li {{
            margin-bottom: 12px;
        }}
        .instructions code {{
            background: #e0e0e0;
            padding: 2px 6px;
            border-radius: 3px;
            font-family: 'Courier New', monospace;
            font-size: 13px;
        }}
        .agent-examples {{
            margin-top: 20px;
        }}
        .agent-examples h3 {{
            color: #333;
            font-size: 16px;
            margin-bottom: 12px;
        }}
        .agent-examples ul {{
            list-style: none;
            margin-left: 0;
        }}
        .agent-examples li {{
            padding: 8px 0;
            color: #666;
        }}
        .agent-examples li::before {{
            content: "•";
            color: #667eea;
            font-weight: bold;
            display: inline-block;
            width: 1em;
            margin-left: -1em;
        }}
        .footer {{
            text-align: center;
            color: #999;
            font-size: 12px;
            padding-top: 20px;
            border-top: 1px solid #e0e0e0;
        }}
    </style>
</head>
<body>
    <div class="container">
        <div class="success-icon">OK</div>
        <h1>Authentication Successful!</h1>
        <p class="subtitle">Your agent token has been generated</p>
        
        <div class="warning-box">
            <p><strong>Important:</strong> This token will only be shown once.</p>
            <p>Copy it now and store it securely. You will need to configure your AI agent with this token.</p>
        </div>
        
        <div class="token-section">
            <div class="token-label">Your Agent Token:</div>
            <div class="token-container">
                <div class="token-display" id="token">{token}</div>
                <button class="copy-button" id="copyButton" onclick="copyToken()">
                    Copy
                </button>
            </div>
        </div>
        
        <div class="instructions">
            <h2>Configuration Instructions</h2>
            <ol>
                <li>Copy the token above using the "Copy" button</li>
                <li>Open your AI agent's settings or configuration</li>
                <li>Find the API Key or Bearer Token field</li>
                <li>Paste the token into that field</li>
                <li>Save your configuration</li>
            </ol>
            
            <div class="agent-examples">
                <h3>Common AI Agents:</h3>
                <ul>
                    <li><strong>Cursor:</strong> Settings → Models → API Key</li>
                    <li><strong>Continue:</strong> Config → API Key</li>
                    <li><strong>Cline:</strong> Settings → API Configuration</li>
                    <li><strong>Aider:</strong> Use <code>--api-key</code> flag or set <code>OPENAI_API_KEY</code> environment variable</li>
                </ul>
            </div>
        </div>
        
        <div class="footer">
            You can now close this window and start using your AI agent
        </div>
    </div>
    
    <script>
        function copyToken() {{
            const tokenText = document.getElementById('token').textContent;
            const button = document.getElementById('copyButton');
            
            navigator.clipboard.writeText(tokenText).then(function() {{
                button.textContent = 'Copied!';
                button.classList.add('copied');
                
                setTimeout(function() {{
                    button.textContent = 'Copy';
                    button.classList.remove('copied');
                }}, 2000);
            }}).catch(function(err) {{
                console.error('Failed to copy:', err);
                alert('Failed to copy token. Please select and copy manually.');
            }});
        }}
    </script>
</body>
</html>
"""
