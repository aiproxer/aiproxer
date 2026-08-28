from __future__ import annotations

from pathlib import Path
from typing import Any
from unittest.mock import Mock

import httpx
import pytest
from src.core.config.app_config import BackendConfig
from src.core.services.configured_backend_model_enumerators import (
    CodexAppServerConfiguredModelEnumerator,
    ExplicitConfiguredModelEnumerator,
    OpenAICodexConfiguredModelEnumerator,
    OpencodeZenConfiguredModelEnumerator,
)


@pytest.mark.asyncio
async def test_explicit_enumerator_returns_only_configured_models() -> None:
    enumerator = ExplicitConfiguredModelEnumerator(
        connector="agy-cli-acp", source="configured"
    )

    result = await enumerator.enumerate(
        "agy-cli-acp.project",
        BackendConfig(
            connector="agy-cli-acp",
            models=["google/gemini-3.5-flash-high"],
        ),
    )

    assert result.models == ("google/gemini-3.5-flash-high",)
    assert result.instance_pinned is True


@pytest.mark.asyncio
async def test_explicit_enumerator_omits_empty_configuration() -> None:
    enumerator = ExplicitConfiguredModelEnumerator(
        connector="gemini-cli-acp", source="configured"
    )

    result = await enumerator.enumerate(
        "gemini-cli-acp.project",
        BackendConfig(connector="gemini-cli-acp"),
    )

    assert result.models == ()
    assert result.status == "unavailable"
    assert result.error_code == "models_not_configured"


@pytest.mark.asyncio
async def test_codex_app_server_uses_only_live_discovered_catalog() -> None:
    catalog = Mock()
    catalog.routable_slugs.return_value = ["gpt-5.6-sol", "gpt-5.6-luna"]
    enumerator = CodexAppServerConfiguredModelEnumerator(
        catalog=catalog,
        catalog_source="discovery",
    )

    result = await enumerator.enumerate(
        "openai-codex-app-server.default",
        BackendConfig(connector="openai-codex-app-server"),
    )

    assert result.models == (
        "openai/auto",
        "openai/gpt-5.6-sol",
        "openai/gpt-5.6-luna",
    )


@pytest.mark.asyncio
async def test_codex_app_server_fallback_catalog_advertises_only_auto() -> None:
    catalog = Mock()
    catalog.routable_slugs.return_value = ["stale-model"]
    enumerator = CodexAppServerConfiguredModelEnumerator(
        catalog=catalog,
        catalog_source="fallback",
    )

    result = await enumerator.enumerate(
        "openai-codex-app-server.default",
        BackendConfig(connector="openai-codex-app-server"),
    )

    assert result.models == ("openai/auto",)


@pytest.mark.asyncio
async def test_opencode_zen_enumerator_returns_configured_models() -> None:
    enumerator = OpencodeZenConfiguredModelEnumerator()
    config = BackendConfig(
        connector="opencode-zen",
        api_key="test-key",
        models=["claude-sonnet-4-5", "opencode-zen/gpt-5.1"],
    )

    result = await enumerator.enumerate("opencode-zen", config)

    assert result.status == "available"
    assert result.source == "opencode_zen_configured"
    assert result.models == (
        "opencode-zen/claude-sonnet-4-5",
        "opencode-zen/gpt-5.1",
    )
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_opencode_zen_enumerator_falls_back_to_curated_on_remote_failure(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    enumerator = OpencodeZenConfiguredModelEnumerator()
    config = BackendConfig(
        connector="opencode-zen",
        api_key="test-key",
        api_url="http://127.0.0.1:1/invalid",
        extra={"model_discovery_timeout_seconds": 0.1},
    )

    result = await enumerator.enumerate("opencode-zen", config)

    assert result.status == "available"
    assert result.source == "opencode_zen_curated"
    assert "opencode-zen/anthropic/claude-sonnet-4-5" in result.models
    assert "opencode-zen/openai/gpt-5.1" in result.models
    assert "opencode-zen/z-ai/glm-5.1" in result.models
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_opencode_zen_enumerator_returns_live_upstream_models(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    enumerator = OpencodeZenConfiguredModelEnumerator()
    config = BackendConfig(
        connector="opencode-zen",
        api_key="test-key",
    )

    class MockTransport(httpx.AsyncBaseTransport):
        async def handle_async_request(self, request: httpx.Request) -> httpx.Response:
            return httpx.Response(
                200,
                json={"data": [{"id": "claude-sonnet-4-5"}, {"id": "glm-5.1"}]},
            )

    real_async_client = httpx.AsyncClient

    def _mock_client(*args: Any, **kwargs: Any) -> httpx.AsyncClient:
        kwargs["transport"] = MockTransport()
        return real_async_client(*args, **kwargs)

    monkeypatch.setattr(httpx, "AsyncClient", _mock_client)

    result = await enumerator.enumerate("opencode-zen", config)

    assert result.status == "available"
    assert result.source == "opencode_zen_upstream"
    assert result.models == (
        "opencode-zen/anthropic/claude-sonnet-4-5",
        "opencode-zen/z-ai/glm-5.1",
    )
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_opencode_zen_enumerator_returns_unavailable_when_missing_credentials(
    monkeypatch: pytest.MonkeyPatch, tmp_path: pytest.TempPathFactory
) -> None:
    monkeypatch.delenv("OPENCODE_ZEN_API_KEY", raising=False)
    monkeypatch.delenv("OPENCODE_AUTH_PATH", raising=False)
    monkeypatch.setenv("LOCALAPPDATA", str(tmp_path))
    monkeypatch.setenv("XDG_DATA_HOME", str(tmp_path))
    monkeypatch.setattr(Path, "home", lambda: Path(str(tmp_path)))

    enumerator = OpencodeZenConfiguredModelEnumerator()
    config = BackendConfig(connector="opencode-zen")

    result = await enumerator.enumerate("opencode-zen", config)

    assert result.status == "unavailable"
    assert result.error_code == "missing_credentials"
    assert result.models == ()


@pytest.mark.asyncio
async def test_openai_codex_enumerator_returns_configured_models() -> None:
    enumerator = OpenAICodexConfiguredModelEnumerator()
    config = BackendConfig(
        connector="openai-codex",
        models=["gpt-5.5", "openai-codex/gpt-5.4", "openai/gpt-5.3-codex"],
    )

    result = await enumerator.enumerate("openai-codex", config)

    assert result.status == "available"
    assert result.source == "openai-codex_configured"
    assert result.models == (
        "openai/gpt-5.5",
        "openai/gpt-5.4",
        "openai/gpt-5.3-codex",
    )
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_openai_codex_enumerator_returns_unavailable_when_missing_credentials(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    monkeypatch.delenv("OPENAI_API_KEY", raising=False)
    monkeypatch.delenv("OPENAI_CODEX_API_KEY", raising=False)
    monkeypatch.delenv("OPENAI_CODEX_AUTH_PATH", raising=False)
    monkeypatch.delenv("OPENAI_AUTH_PATH", raising=False)
    monkeypatch.setenv("USERPROFILE", str(tmp_path / "empty_userprofile"))
    monkeypatch.setenv("LOCALAPPDATA", str(tmp_path / "empty_localappdata"))
    monkeypatch.setattr(Path, "home", lambda: tmp_path / "empty_home")

    enumerator = OpenAICodexConfiguredModelEnumerator()
    config = BackendConfig(
        connector="openai-codex",
        extra={"storage_path": str(tmp_path / "empty_storage")},
    )

    result = await enumerator.enumerate("openai-codex", config)

    assert result.status == "unavailable"
    assert result.error_code == "missing_credentials"
    assert result.models == ()


@pytest.mark.asyncio
async def test_openai_codex_enumerator_with_api_key_discovers_catalog() -> None:
    catalog = Mock()
    catalog.routable_slugs.return_value = ["gpt-5.5", "gpt-5.4"]
    enumerator = OpenAICodexConfiguredModelEnumerator(
        catalog=catalog,
        catalog_source="discovery",
    )
    config = BackendConfig(
        connector="openai-codex",
        api_key="test-key",
    )

    result = await enumerator.enumerate("openai-codex", config)

    assert result.status == "available"
    assert result.source == "codex_discovery"
    assert result.models == ("openai/gpt-5.5", "openai/gpt-5.4")
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_openai_codex_enumerator_with_auth_json_discovers_catalog(
    tmp_path: Path,
) -> None:
    auth_file = tmp_path / "auth.json"
    auth_file.write_text('{"tokens": {"access_token": "test-token"}}', encoding="utf-8")

    catalog = Mock()
    catalog.routable_slugs.return_value = ["gpt-5.5", "gpt-5.3-codex"]
    enumerator = OpenAICodexConfiguredModelEnumerator(
        catalog=catalog,
        catalog_source="fallback",
    )
    config = BackendConfig(
        connector="openai-codex",
        credentials_path=str(auth_file),
    )

    result = await enumerator.enumerate("openai-codex", config)

    assert result.status == "available"
    assert result.source == "codex_fallback"
    assert result.models == ("openai/gpt-5.5", "openai/gpt-5.3-codex")
    assert not result.instance_pinned


@pytest.mark.asyncio
async def test_openai_codex_enumerator_with_managed_oauth_storage(
    tmp_path: Path,
) -> None:
    oauth_storage = tmp_path / "oauth_accounts"
    oauth_storage.mkdir(parents=True, exist_ok=True)
    (oauth_storage / "acc_1.json").write_text(
        '{"account_id": "acc-1", "access_token": "token-1"}', encoding="utf-8"
    )

    catalog = Mock()
    catalog.routable_slugs.return_value = ["gpt-5.5"]
    enumerator = OpenAICodexConfiguredModelEnumerator(
        catalog=catalog,
        catalog_source="discovery",
    )
    config = BackendConfig(
        connector="openai-codex-v2",
        extra={"managed_oauth": {"storage_path": str(oauth_storage)}},
    )

    result = await enumerator.enumerate("openai-codex-v2", config)

    assert result.status == "available"
    assert result.source == "codex_discovery"
    assert result.models == ("openai/gpt-5.5",)
    assert not result.instance_pinned
