"""Tests for ``CodexCatalogEndpointClient`` — direct authenticated catalog GET."""

from __future__ import annotations

from pathlib import Path
from typing import Any

import httpx
import pytest
from src.connectors.openai_codex import credentials as credentials_module
from src.connectors.openai_codex.catalog import endpoint_client as endpoint_module
from src.connectors.openai_codex.catalog.endpoint_client import (
    CodexCatalogEndpointClient,
)


@pytest.fixture()
def patch_credential_manager(monkeypatch):
    """Patch the concrete credential manager the endpoint client creates."""

    def _patch(manager: _FakeCredentialManager) -> None:
        monkeypatch.setattr(
            credentials_module, "CredentialManager", lambda client: manager
        )

    return _patch


class _FakeCredentialManager:
    """Minimal ``ICredentialManager`` stand-in for endpoint-client tests."""

    def __init__(
        self,
        *,
        token: str | None = "token-1",
        account_id: str | None = "acct-1",
        refreshed_token: str | None = "token-2",
        refresh_result: bool = True,
    ) -> None:
        self._token = token
        self._account_id = account_id
        self._refreshed_token = refreshed_token
        self._refresh_result = refresh_result
        self.initialize_calls: list[dict[str, Any]] = []
        self.refresh_calls = 0
        self.shutdown_calls = 0
        self.managed_configs: list[Any] = []

    def configure_managed_oauth(self, config: Any) -> None:
        self.managed_configs.append(config)

    async def initialize(
        self, auth_path: Path | None, *, start_watcher: bool = True
    ) -> None:
        self.initialize_calls.append(
            {"auth_path": auth_path, "start_watcher": start_watcher}
        )

    async def refresh_access_token(self) -> bool:
        self.refresh_calls += 1
        if self._refresh_result and self._refreshed_token is not None:
            self._token = self._refreshed_token
        return self._refresh_result

    def get_access_token(self) -> str | None:
        return self._token

    def get_account_id(self) -> str | None:
        return self._account_id

    async def shutdown(self) -> None:
        self.shutdown_calls += 1


def _handler(
    responses: list[httpx.Response],
    *,
    requests: list[httpx.Request] | None = None,
):
    remaining = list(responses)

    def handle(request: httpx.Request) -> httpx.Response:
        if requests is not None:
            requests.append(request)
        if not remaining:
            raise AssertionError("unexpected extra HTTP request")
        return remaining.pop(0)

    return handle


def _client(
    responses: list[httpx.Response],
    *,
    requests: list[httpx.Request] | None = None,
) -> httpx.AsyncClient:
    return httpx.AsyncClient(
        transport=httpx.MockTransport(_handler(responses, requests=requests))
    )


def _json_response(payload: object, status_code: int = 200) -> httpx.Response:
    return httpx.Response(status_code, json=payload)


def _raw_response(text: str, status_code: int = 200) -> httpx.Response:
    return httpx.Response(status_code, text=text)


_CATALOG = {"models": [{"slug": "gpt-6-sol"}, {"slug": "gpt-6-luna"}]}


class TestRequestShape:
    @pytest.mark.asyncio
    async def test_success_returns_raw_mapping(self) -> None:
        requests: list[httpx.Request] = []
        client = _client([_json_response(_CATALOG)], requests=requests)
        manager = _FakeCredentialManager()
        endpoint = CodexCatalogEndpointClient(
            http_client=client, credential_manager=manager
        )

        result = await endpoint.fetch()

        assert result == _CATALOG
        await client.aclose()

    @pytest.mark.asyncio
    async def test_exact_client_version_query(self) -> None:
        requests: list[httpx.Request] = []
        client = _client([_json_response(_CATALOG)], requests=requests)
        endpoint = CodexCatalogEndpointClient(
            http_client=client, credential_manager=_FakeCredentialManager()
        )

        await endpoint.fetch()

        assert requests[0].url.params["client_version"] == "0.156.0"
        assert requests[0].url.path == "/backend-api/codex/models"
        assert requests[0].url.host == "chatgpt.com"
        await client.aclose()

    @pytest.mark.asyncio
    async def test_authorization_accept_and_user_agent_headers(self) -> None:
        requests: list[httpx.Request] = []
        client = _client([_json_response(_CATALOG)], requests=requests)
        endpoint = CodexCatalogEndpointClient(
            http_client=client, credential_manager=_FakeCredentialManager()
        )

        await endpoint.fetch()

        headers = requests[0].headers
        assert headers["Authorization"] == "Bearer token-1"
        assert headers["Accept"] == "application/json"
        assert headers["User-Agent"] == "codex_cli_rs/0.156.0"
        await client.aclose()

    @pytest.mark.asyncio
    async def test_account_header_included_when_available(self) -> None:
        requests: list[httpx.Request] = []
        client = _client([_json_response(_CATALOG)], requests=requests)
        endpoint = CodexCatalogEndpointClient(
            http_client=client,
            credential_manager=_FakeCredentialManager(account_id="acct-42"),
        )

        await endpoint.fetch()

        assert requests[0].headers["chatgpt-account-id"] == "acct-42"
        await client.aclose()

    @pytest.mark.asyncio
    async def test_account_header_omitted_when_absent(self) -> None:
        requests: list[httpx.Request] = []
        client = _client([_json_response(_CATALOG)], requests=requests)
        endpoint = CodexCatalogEndpointClient(
            http_client=client,
            credential_manager=_FakeCredentialManager(account_id=None),
        )

        await endpoint.fetch()

        assert "chatgpt-account-id" not in requests[0].headers
        await client.aclose()

    @pytest.mark.asyncio
    async def test_timeout_is_forwarded(self) -> None:
        requests: list[httpx.Request] = []
        client = _client([_json_response(_CATALOG)], requests=requests)
        endpoint = CodexCatalogEndpointClient(
            http_client=client,
            credential_manager=_FakeCredentialManager(),
            timeout_seconds=7.5,
        )
        await endpoint.fetch()

        # httpx stores the per-request timeout extension on the request.
        timeout = requests[0].extensions.get("timeout")
        assert timeout is not None
        assert timeout["connect"] == 7.5
        await client.aclose()


class TestCredentials:
    @pytest.mark.asyncio
    async def test_initialize_disables_watcher(self) -> None:
        client = _client([_json_response(_CATALOG)])
        manager = _FakeCredentialManager()
        endpoint = CodexCatalogEndpointClient(
            http_client=client,
            credential_manager=manager,
            auth_path=Path("/x/auth.json"),
        )

        await endpoint.fetch()

        assert manager.initialize_calls == [
            {"auth_path": Path("/x/auth.json"), "start_watcher": False}
        ]
        await client.aclose()

    @pytest.mark.asyncio
    async def test_auth_path_expands_user(self, monkeypatch) -> None:
        """``~/.codex/auth.json`` must reach the credential manager expanded."""
        monkeypatch.setenv("HOME", "/home/testuser")
        monkeypatch.setenv("USERPROFILE", "/home/testuser")
        client = _client([_json_response(_CATALOG)])
        manager = _FakeCredentialManager()
        endpoint = CodexCatalogEndpointClient(
            http_client=client,
            credential_manager=manager,
            auth_path=Path("~/.codex/auth.json"),
        )

        await endpoint.fetch()

        assert len(manager.initialize_calls) == 1
        passed = manager.initialize_calls[0]["auth_path"]
        assert passed is not None
        assert "~" not in str(passed)
        assert passed.name == "auth.json"
        assert passed.parent.name == ".codex"
        await client.aclose()

    @pytest.mark.asyncio
    async def test_missing_credentials_returns_none_without_request(self) -> None:
        requests: list[httpx.Request] = []
        client = _client([], requests=requests)
        endpoint = CodexCatalogEndpointClient(
            http_client=client,
            credential_manager=_FakeCredentialManager(token=None),
        )

        result = await endpoint.fetch()

        assert result is None
        assert requests == []
        await client.aclose()


class TestAuthRetry:
    @pytest.mark.asyncio
    async def test_first_401_refreshes_and_retries_once(self) -> None:
        requests: list[httpx.Request] = []
        client = _client(
            [
                _json_response({"error": "expired"}, status_code=401),
                _json_response(_CATALOG),
            ],
            requests=requests,
        )
        manager = _FakeCredentialManager(token="old", refreshed_token="new")
        endpoint = CodexCatalogEndpointClient(
            http_client=client, credential_manager=manager
        )

        result = await endpoint.fetch()

        assert result == _CATALOG
        assert manager.refresh_calls == 1
        assert requests[0].headers["Authorization"] == "Bearer old"
        assert requests[1].headers["Authorization"] == "Bearer new"
        await client.aclose()

    @pytest.mark.asyncio
    async def test_second_401_returns_none(self) -> None:
        requests: list[httpx.Request] = []
        client = _client(
            [
                _json_response({"error": "expired"}, status_code=401),
                _json_response({"error": "expired"}, status_code=401),
            ],
            requests=requests,
        )
        manager = _FakeCredentialManager()
        endpoint = CodexCatalogEndpointClient(
            http_client=client, credential_manager=manager
        )

        assert await endpoint.fetch() is None
        assert manager.refresh_calls == 1
        assert len(requests) == 2
        await client.aclose()

    @pytest.mark.asyncio
    async def test_refresh_failure_returns_none(self) -> None:
        client = _client([_json_response({"error": "expired"}, status_code=401)])
        manager = _FakeCredentialManager(refresh_result=False)
        endpoint = CodexCatalogEndpointClient(
            http_client=client, credential_manager=manager
        )

        assert await endpoint.fetch() is None
        assert manager.refresh_calls == 1
        await client.aclose()


class TestFailuresReturnNone:
    @pytest.mark.asyncio
    @pytest.mark.parametrize("status", [403, 429, 500, 503])
    async def test_non_2xx_returns_none_without_refresh(self, status: int) -> None:
        client = _client([_json_response({"error": "nope"}, status_code=status)])
        manager = _FakeCredentialManager()
        endpoint = CodexCatalogEndpointClient(
            http_client=client, credential_manager=manager
        )

        assert await endpoint.fetch() is None
        assert manager.refresh_calls == 0
        await client.aclose()

    @pytest.mark.asyncio
    async def test_timeout_returns_none(self) -> None:
        def handler(request: httpx.Request) -> httpx.Response:
            raise httpx.ReadTimeout("timed out", request=request)

        client = httpx.AsyncClient(transport=httpx.MockTransport(handler))
        endpoint = CodexCatalogEndpointClient(
            http_client=client, credential_manager=_FakeCredentialManager()
        )

        assert await endpoint.fetch() is None
        await client.aclose()

    @pytest.mark.asyncio
    async def test_connection_failure_returns_none(self) -> None:
        def handler(request: httpx.Request) -> httpx.Response:
            raise httpx.ConnectError("no route", request=request)

        client = httpx.AsyncClient(transport=httpx.MockTransport(handler))
        endpoint = CodexCatalogEndpointClient(
            http_client=client, credential_manager=_FakeCredentialManager()
        )

        assert await endpoint.fetch() is None
        await client.aclose()

    @pytest.mark.asyncio
    async def test_invalid_json_returns_none(self) -> None:
        client = _client([_raw_response("not json", status_code=200)])
        endpoint = CodexCatalogEndpointClient(
            http_client=client, credential_manager=_FakeCredentialManager()
        )

        assert await endpoint.fetch() is None
        await client.aclose()

    @pytest.mark.asyncio
    async def test_missing_models_returns_none(self) -> None:
        client = _client([_json_response({"data": []})])
        endpoint = CodexCatalogEndpointClient(
            http_client=client, credential_manager=_FakeCredentialManager()
        )

        assert await endpoint.fetch() is None
        await client.aclose()

    @pytest.mark.asyncio
    async def test_non_mapping_response_returns_none(self) -> None:
        client = _client([_json_response([1, 2, 3])])
        endpoint = CodexCatalogEndpointClient(
            http_client=client, credential_manager=_FakeCredentialManager()
        )

        assert await endpoint.fetch() is None
        await client.aclose()

    @pytest.mark.asyncio
    async def test_models_not_list_returns_none(self) -> None:
        client = _client([_json_response({"models": "nope"})])
        endpoint = CodexCatalogEndpointClient(
            http_client=client, credential_manager=_FakeCredentialManager()
        )

        assert await endpoint.fetch() is None
        await client.aclose()


class _RecordingAsyncClient:
    """Stand-in ``httpx.AsyncClient`` that records close calls and responses."""

    instances: list[_RecordingAsyncClient] = []

    def __init__(self, *args: Any, **kwargs: Any) -> None:
        self.closed = False
        self.requests: list[httpx.Request] = []
        self._responses = _RecordingAsyncClient._responses
        type(self).instances.append(self)

    _responses: list[httpx.Response] = []

    async def get(
        self,
        url: str,
        *,
        params: Any = None,
        headers: Any = None,
        timeout: Any = None,
    ) -> httpx.Response:
        request = httpx.Request("GET", url, params=params, headers=headers)
        self.requests.append(request)
        return self._responses.pop(0)

    async def aclose(self) -> None:
        self.closed = True


class TestOwnership:
    @pytest.mark.asyncio
    async def test_injected_http_client_is_not_closed(self) -> None:
        client = _client([_json_response(_CATALOG)])
        endpoint = CodexCatalogEndpointClient(
            http_client=client, credential_manager=_FakeCredentialManager()
        )

        await endpoint.fetch()

        assert client.is_closed is False
        await client.aclose()

    @pytest.mark.asyncio
    async def test_internally_created_client_is_closed(self, monkeypatch) -> None:
        _RecordingAsyncClient.instances = []
        _RecordingAsyncClient._responses = [_json_response(_CATALOG)]
        monkeypatch.setattr(endpoint_module.httpx, "AsyncClient", _RecordingAsyncClient)

        endpoint = CodexCatalogEndpointClient(
            credential_manager=_FakeCredentialManager()
        )
        result = await endpoint.fetch()

        assert result == _CATALOG
        created = _RecordingAsyncClient.instances[-1]
        assert created.closed is True

    @pytest.mark.asyncio
    async def test_internally_created_manager_is_shutdown(
        self, monkeypatch, patch_credential_manager
    ) -> None:
        _RecordingAsyncClient.instances = []
        _RecordingAsyncClient._responses = [_json_response(_CATALOG)]
        monkeypatch.setattr(endpoint_module.httpx, "AsyncClient", _RecordingAsyncClient)
        manager = _FakeCredentialManager()
        patch_credential_manager(manager)

        endpoint = CodexCatalogEndpointClient()
        await endpoint.fetch()

        assert manager.shutdown_calls == 1

    @pytest.mark.asyncio
    async def test_created_manager_disables_managed_oauth(
        self, monkeypatch, patch_credential_manager
    ) -> None:
        _RecordingAsyncClient.instances = []
        _RecordingAsyncClient._responses = [_json_response(_CATALOG)]
        monkeypatch.setattr(endpoint_module.httpx, "AsyncClient", _RecordingAsyncClient)
        manager = _FakeCredentialManager()
        patch_credential_manager(manager)

        endpoint = CodexCatalogEndpointClient()
        await endpoint.fetch()

        assert len(manager.managed_configs) == 1
        assert manager.managed_configs[0].enabled is False


def test_endpoint_client_satisfies_protocol() -> None:
    from src.connectors.openai_codex.catalog.interfaces import (
        ICodexCatalogEndpointClient,
    )

    assert isinstance(CodexCatalogEndpointClient(), ICodexCatalogEndpointClient)
