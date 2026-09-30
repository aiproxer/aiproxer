"""Tests for Codex CLI latest-version discovery (GitHub releases/latest)."""

from __future__ import annotations

from pathlib import Path

import httpx
import pytest
from src.connectors.openai_codex.catalog.cli_version import (
    CODEX_LATEST_RELEASE_URL,
    CodexCliLatestVersionClient,
    CodexCliLatestVersionResolver,
    extract_version_from_latest_tag,
)


def _client(
    responses: list[httpx.Response],
    *,
    requests: list[httpx.Request] | None = None,
) -> httpx.AsyncClient:
    remaining = list(responses)

    def handle(request: httpx.Request) -> httpx.Response:
        if requests is not None:
            requests.append(request)
        if not remaining:
            raise AssertionError("unexpected extra HTTP request")
        return remaining.pop(0)

    return httpx.AsyncClient(transport=httpx.MockTransport(handle))


class TestExtractVersionFromLatestTag:
    def test_strips_rust_v_prefix(self) -> None:
        assert extract_version_from_latest_tag("rust-v0.159.2") == "0.159.2"

    def test_rejects_v_prefix(self) -> None:
        assert extract_version_from_latest_tag("v0.159.2") is None

    def test_rejects_empty_and_non_string(self) -> None:
        assert extract_version_from_latest_tag("") is None
        assert extract_version_from_latest_tag("   ") is None
        assert extract_version_from_latest_tag("rust-v") is None


class TestCodexCliLatestVersionClient:
    @pytest.mark.asyncio
    async def test_fetches_github_latest_and_extracts_tag(self) -> None:
        requests: list[httpx.Request] = []
        client = _client(
            [httpx.Response(200, json={"tag_name": "rust-v0.159.2"})],
            requests=requests,
        )
        fetched = await CodexCliLatestVersionClient(http_client=client).fetch()

        assert fetched == "0.159.2"
        assert str(requests[0].url) == CODEX_LATEST_RELEASE_URL
        assert requests[0].headers["Accept"] == "application/vnd.github+json"
        assert "User-Agent" in requests[0].headers
        await client.aclose()

    @pytest.mark.asyncio
    async def test_returns_none_on_http_error(self) -> None:
        client = _client([httpx.Response(403, json={"message": "rate limited"})])
        fetched = await CodexCliLatestVersionClient(http_client=client).fetch()
        assert fetched is None
        await client.aclose()

    @pytest.mark.asyncio
    async def test_returns_none_on_malformed_tag(self) -> None:
        client = _client([httpx.Response(200, json={"tag_name": "v0.159.2"})])
        fetched = await CodexCliLatestVersionClient(http_client=client).fetch()
        assert fetched is None
        await client.aclose()


class TestCodexCliLatestVersionResolver:
    @pytest.mark.asyncio
    async def test_fetch_success_writes_cache_and_returns_version(
        self, tmp_path: Path
    ) -> None:
        cache_path = tmp_path / "version.json"
        http_client = _client([httpx.Response(200, json={"tag_name": "rust-v0.160.0"})])
        resolver = CodexCliLatestVersionResolver(
            cache_path=cache_path,
            client=CodexCliLatestVersionClient(http_client=http_client),
        )

        version = await resolver.resolve()

        assert version == "0.160.0"
        assert cache_path.is_file()
        await http_client.aclose()

        cached_client = _client([])
        cached_resolver = CodexCliLatestVersionResolver(
            cache_path=cache_path,
            client=CodexCliLatestVersionClient(http_client=cached_client),
        )
        assert cached_resolver.read_cache() == "0.160.0"
        await cached_client.aclose()

    @pytest.mark.asyncio
    async def test_fetch_failure_falls_back_to_cache(self, tmp_path: Path) -> None:
        cache_path = tmp_path / "version.json"
        cache_path.write_text('{"latest_version": "0.159.2"}\n', encoding="utf-8")
        http_client = _client([httpx.Response(500, json={"message": "boom"})])
        resolver = CodexCliLatestVersionResolver(
            cache_path=cache_path,
            client=CodexCliLatestVersionClient(http_client=http_client),
        )

        version = await resolver.resolve()

        assert version == "0.159.2"
        await http_client.aclose()

    @pytest.mark.asyncio
    async def test_fetch_failure_without_cache_returns_none(
        self, tmp_path: Path
    ) -> None:
        http_client = _client([httpx.Response(500, json={"message": "boom"})])
        resolver = CodexCliLatestVersionResolver(
            cache_path=tmp_path / "missing.json",
            client=CodexCliLatestVersionClient(http_client=http_client),
        )

        version = await resolver.resolve()

        assert version is None
        await http_client.aclose()
