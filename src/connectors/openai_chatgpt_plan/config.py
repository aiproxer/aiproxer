"""Normalized settings for the openai-chatgpt-plan connector.

Precedence for connector-local values: ENV > YAML extra.chatgpt_plan > defaults.
Core AppConfig loading records OPENAI_CHATGPT_PLAN_* onto
``backends.openai_chatgpt_plan.extra.chatgpt_plan`` so YAML and ENV merge with
the project-wide CLI > ENV > YAML > defaults order. Connector-specific
argparse flags are not registered on the main CLI in this task.
"""

from __future__ import annotations

import os
from collections.abc import Mapping
from typing import Any

from pydantic import BaseModel, ConfigDict, Field, field_validator

from src.core.config.env.util import get_env_value
from src.core.config.parameter_resolution import ParameterResolution

YAML_BACKEND_KEY = "openai_chatgpt_plan"
REGISTRY_BACKEND_KEY = "openai-chatgpt-plan"
CHATGPT_PLAN_EXTRA_KEY = "chatgpt_plan"

DEFAULT_CHATGPT_PLAN_PROFILES_PATH = "var/openai_chatgpt_plan/profiles"
DEFAULT_CHATGPT_PLAN_HOST_STATE_PATH = "var/openai_chatgpt_plan/host.json"
DEFAULT_CHATGPT_PLAN_OAUTH_CALLBACK_PORT = 1455
DEFAULT_CHATGPT_PLAN_MODEL_CATALOG_TTL_SECONDS = 300

ENV_PROFILE_ID = "OPENAI_CHATGPT_PLAN_PROFILE_ID"
ENV_PROFILES_PATH = "OPENAI_CHATGPT_PLAN_PROFILES_PATH"
ENV_HOST_STATE_PATH = "OPENAI_CHATGPT_PLAN_HOST_STATE_PATH"
ENV_OAUTH_CALLBACK_PORT = "OPENAI_CHATGPT_PLAN_OAUTH_CALLBACK_PORT"
ENV_MODEL_CATALOG_TTL_SECONDS = "OPENAI_CHATGPT_PLAN_MODEL_CATALOG_TTL_SECONDS"

_ENV_PATH_PREFIX = f"backends.{YAML_BACKEND_KEY}.extra.{CHATGPT_PLAN_EXTRA_KEY}"


class ChatGPTPlanOAuthConfig(BaseModel):
    """Loopback OAuth callback settings for Sign in with ChatGPT."""

    model_config = ConfigDict(frozen=True, extra="ignore")

    callback_port: int = Field(
        default=DEFAULT_CHATGPT_PLAN_OAUTH_CALLBACK_PORT,
        ge=1,
        le=65535,
        description="Loopback callback port advertised as 127.0.0.1:<port>/auth/callback.",
    )


class ChatGPTPlanModelCatalogConfig(BaseModel):
    """Per-profile public /v1/models cache settings."""

    model_config = ConfigDict(frozen=True, extra="ignore")

    ttl_seconds: int = Field(
        default=DEFAULT_CHATGPT_PLAN_MODEL_CATALOG_TTL_SECONDS,
        ge=0,
        description="How long a profile's discovered model list may be reused.",
    )


class ChatGPTPlanConfig(BaseModel):
    """Typed openai-chatgpt-plan settings (profile selection and local paths)."""

    model_config = ConfigDict(frozen=True, extra="ignore")

    profile_id: str | None = Field(
        default=None,
        description="Explicit local profile alias used by this backend instance.",
    )
    profiles_path: str = Field(default=DEFAULT_CHATGPT_PLAN_PROFILES_PATH)
    host_state_path: str = Field(default=DEFAULT_CHATGPT_PLAN_HOST_STATE_PATH)
    oauth: ChatGPTPlanOAuthConfig = Field(default_factory=ChatGPTPlanOAuthConfig)
    model_catalog: ChatGPTPlanModelCatalogConfig = Field(
        default_factory=ChatGPTPlanModelCatalogConfig
    )

    @field_validator("profile_id", mode="before")
    @classmethod
    def _normalize_profile_id(cls, value: object) -> str | None:
        if value is None:
            return None
        if isinstance(value, str):
            stripped = value.strip()
            return stripped or None
        return str(value)

    @field_validator("profiles_path", "host_state_path", mode="before")
    @classmethod
    def _normalize_path(cls, value: object) -> object:
        if isinstance(value, str):
            return value.strip()
        return value


def _deep_merge(base: Mapping[str, Any], override: Mapping[str, Any]) -> dict[str, Any]:
    merged: dict[str, Any] = dict(base)
    for key, value in override.items():
        existing = merged.get(key)
        if isinstance(existing, dict) and isinstance(value, Mapping):
            merged[key] = _deep_merge(existing, value)
        else:
            merged[key] = value
    return merged


def _ensure_mapping(parent: dict[str, Any], key: str) -> dict[str, Any]:
    current = parent.get(key)
    if not isinstance(current, dict):
        current = {}
        parent[key] = current
    return current


def chatgpt_plan_env_overrides(
    env: Mapping[str, str],
    *,
    resolution: ParameterResolution | None = None,
) -> dict[str, Any]:
    """Return a nested extra.chatgpt_plan mapping for set OPENAI_CHATGPT_PLAN_* vars."""

    overrides: dict[str, Any] = {}

    if ENV_PROFILE_ID in env:
        overrides["profile_id"] = get_env_value(
            env,
            ENV_PROFILE_ID,
            None,
            path=f"{_ENV_PATH_PREFIX}.profile_id",
            resolution=resolution,
            transform=lambda value: value.strip() or None,
        )

    if ENV_PROFILES_PATH in env:
        profiles_path = get_env_value(
            env,
            ENV_PROFILES_PATH,
            None,
            path=f"{_ENV_PATH_PREFIX}.profiles_path",
            resolution=resolution,
            transform=lambda value: value.strip(),
        )
        if profiles_path:
            overrides["profiles_path"] = profiles_path

    if ENV_HOST_STATE_PATH in env:
        host_state_path = get_env_value(
            env,
            ENV_HOST_STATE_PATH,
            None,
            path=f"{_ENV_PATH_PREFIX}.host_state_path",
            resolution=resolution,
            transform=lambda value: value.strip(),
        )
        if host_state_path:
            overrides["host_state_path"] = host_state_path

    if ENV_OAUTH_CALLBACK_PORT in env:
        callback_port = get_env_value(
            env,
            ENV_OAUTH_CALLBACK_PORT,
            None,
            path=f"{_ENV_PATH_PREFIX}.oauth.callback_port",
            resolution=resolution,
            transform=lambda value: int(value.strip()),
        )
        if callback_port is not None:
            overrides["oauth"] = {"callback_port": callback_port}

    if ENV_MODEL_CATALOG_TTL_SECONDS in env:
        ttl_seconds = get_env_value(
            env,
            ENV_MODEL_CATALOG_TTL_SECONDS,
            None,
            path=f"{_ENV_PATH_PREFIX}.model_catalog.ttl_seconds",
            resolution=resolution,
            transform=lambda value: int(value.strip()),
        )
        if ttl_seconds is not None:
            overrides["model_catalog"] = {"ttl_seconds": ttl_seconds}

    return overrides


def apply_openai_chatgpt_plan_env_to_backends(
    config_backends: dict[str, Any],
    env: Mapping[str, str],
    resolution: ParameterResolution | None = None,
) -> None:
    """Merge OPENAI_CHATGPT_PLAN_* into backends.openai_chatgpt_plan.extra."""

    overrides = chatgpt_plan_env_overrides(env, resolution=resolution)
    if not overrides:
        return

    backend = _ensure_mapping(config_backends, YAML_BACKEND_KEY)
    extra = _ensure_mapping(backend, "extra")
    chatgpt_plan = _ensure_mapping(extra, CHATGPT_PLAN_EXTRA_KEY)
    extra[CHATGPT_PLAN_EXTRA_KEY] = _deep_merge(chatgpt_plan, overrides)


def load_chatgpt_plan_config(
    extra: Mapping[str, Any] | None = None,
    *,
    environ: Mapping[str, str] | None = None,
) -> ChatGPTPlanConfig:
    """Build ChatGPTPlanConfig from backend extra plus ENV overlays."""

    env = os.environ if environ is None else environ
    yaml_section: Mapping[str, Any] = {}
    if extra is not None:
        candidate = extra.get(CHATGPT_PLAN_EXTRA_KEY)
        if isinstance(candidate, Mapping):
            yaml_section = candidate

    merged: dict[str, Any] = _deep_merge({}, yaml_section)
    merged = _deep_merge(merged, chatgpt_plan_env_overrides(env))
    return ChatGPTPlanConfig.model_validate(merged)


def _backend_extra(backends: Any, name: str) -> Mapping[str, Any] | None:
    lookup = getattr(backends, "lookup", None)
    backend = lookup(name) if callable(lookup) else None
    if backend is None:
        return None
    extra = getattr(backend, "extra", None)
    return extra if isinstance(extra, Mapping) else None


def chatgpt_plan_config_from_app_config(
    app_config: Any,
    *,
    environ: Mapping[str, str] | None = None,
) -> ChatGPTPlanConfig:
    """Load ChatGPTPlanConfig from AppConfig.backends extra.chatgpt_plan."""

    backends = getattr(app_config, "backends", None)
    extra: Mapping[str, Any] | None = None
    if backends is not None:
        extra = _backend_extra(backends, YAML_BACKEND_KEY)
        if extra is None:
            extra = _backend_extra(backends, REGISTRY_BACKEND_KEY)
    return load_chatgpt_plan_config(extra, environ=environ)


__all__ = [
    "CHATGPT_PLAN_EXTRA_KEY",
    "ChatGPTPlanConfig",
    "ChatGPTPlanModelCatalogConfig",
    "ChatGPTPlanOAuthConfig",
    "DEFAULT_CHATGPT_PLAN_HOST_STATE_PATH",
    "DEFAULT_CHATGPT_PLAN_MODEL_CATALOG_TTL_SECONDS",
    "DEFAULT_CHATGPT_PLAN_OAUTH_CALLBACK_PORT",
    "DEFAULT_CHATGPT_PLAN_PROFILES_PATH",
    "ENV_HOST_STATE_PATH",
    "ENV_MODEL_CATALOG_TTL_SECONDS",
    "ENV_OAUTH_CALLBACK_PORT",
    "ENV_PROFILE_ID",
    "ENV_PROFILES_PATH",
    "REGISTRY_BACKEND_KEY",
    "YAML_BACKEND_KEY",
    "apply_openai_chatgpt_plan_env_to_backends",
    "chatgpt_plan_config_from_app_config",
    "chatgpt_plan_env_overrides",
    "load_chatgpt_plan_config",
]
