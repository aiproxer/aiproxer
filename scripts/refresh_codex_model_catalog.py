#!/usr/bin/env python3
"""Refresh the shipped Codex model catalog fallback snapshot.

Fetches the model catalog directly from the authenticated Codex backend
(``GET https://chatgpt.com/backend-api/codex/models``) and writes the raw
response, normalized as pretty JSON, to
``src/resources/codex/codex_model_catalog.json``. This snapshot is the fallback
used by the ``openai-codex``, ``openai-codex-v2`` and
``openai-codex-app-server`` connectors when startup auto-discovery fails or is
disabled.

Usage::

    ./.venv/Scripts/python.exe scripts/refresh_codex_model_catalog.py
    ./.venv/Scripts/python.exe scripts/refresh_codex_model_catalog.py --output path/to/catalog.json
    ./.venv/Scripts/python.exe scripts/refresh_codex_model_catalog.py --auth-path ~/.codex/auth.json
"""

from __future__ import annotations

import argparse
import asyncio
import json
import sys
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any

PROJECT_ROOT = Path(__file__).resolve().parents[1]
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(PROJECT_ROOT))

from src.connectors.openai_codex.catalog.endpoint_client import (
    DEFAULT_CLIENT_VERSION,
    CodexCatalogEndpointClient,
)
from src.connectors.openai_codex.catalog.fallback_loader import (
    SHIPPED_RESOURCE_NAME,
)

DEFAULT_OUTPUT = PROJECT_ROOT / "src" / "resources" / "codex" / SHIPPED_RESOURCE_NAME
DEFAULT_TIMEOUT = 30.0


async def _fetch_raw(
    *,
    client_version: str,
    auth_path: Path | None,
    timeout: float,
) -> Mapping[str, Any] | None:
    endpoint = CodexCatalogEndpointClient(
        client_version=client_version,
        auth_path=auth_path,
        timeout_seconds=timeout,
    )
    return await endpoint.fetch()


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Refresh the shipped Codex model catalog fallback snapshot."
    )
    parser.add_argument(
        "--output",
        type=Path,
        default=DEFAULT_OUTPUT,
        help=f"Output path (default: {DEFAULT_OUTPUT}).",
    )
    parser.add_argument(
        "--client-version",
        type=str,
        default=DEFAULT_CLIENT_VERSION,
        help=f"Codex client protocol version (default: {DEFAULT_CLIENT_VERSION}).",
    )
    parser.add_argument(
        "--auth-path",
        type=str,
        default=None,
        help="Explicit path to auth.json (else discovered by the credential manager).",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=DEFAULT_TIMEOUT,
        help=f"HTTP request timeout in seconds (default: {DEFAULT_TIMEOUT}).",
    )
    args = parser.parse_args(argv)

    auth_path = Path(args.auth_path).expanduser() if args.auth_path else None
    try:
        data = asyncio.run(
            _fetch_raw(
                client_version=args.client_version,
                auth_path=auth_path,
                timeout=args.timeout,
            )
        )
    except Exception as exc:  # - surface unexpected failures to the operator
        print(f"ERROR: catalog fetch failed: {exc}", file=sys.stderr)
        return 5

    if data is None:
        print(
            "ERROR: catalog fetch failed (missing credentials, non-2xx response, "
            "timeout, or malformed response).",
            file=sys.stderr,
        )
        return 5

    output_path: Path = args.output.expanduser()
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(
        json.dumps(data, indent=2, ensure_ascii=False) + "\n",
        encoding="utf-8",
    )

    models = data.get("models")
    routable: list[str] = []
    for model in models or []:
        if not isinstance(model, Mapping):
            continue
        slug = model.get("slug")
        if not isinstance(slug, str):
            continue
        if (
            model.get("supported_in_api", True)
            and model.get("visibility", "list") != "hide"
        ):
            routable.append(slug)
    print(f"Wrote catalog snapshot to {output_path}")
    print(f"  models: {len(models or [])} total, {len(routable)} routable")
    print(f"  routable slugs: {', '.join(routable)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
