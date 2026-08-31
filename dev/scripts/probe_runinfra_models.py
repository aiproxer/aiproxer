#!/usr/bin/env python
"""
Probe runinfra.ai gateway for its available model catalog.

Hits ``https://api.runinfra.ai/v1/models`` directly with ``Authorization: Bearer $RUNINFRA_API_KEY``
so the slugs printed match the upstream model IDs.

Usage:

    ./.venv/Scripts/python.exe dev/scripts/probe_runinfra_models.py
    ./.venv/Scripts/python.exe dev/scripts/probe_runinfra_models.py --raw
    ./.venv/Scripts/python.exe dev/scripts/probe_runinfra_models.py --api-key <custom_key>
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os

import httpx

MODELS_ENDPOINT = "https://api.runinfra.ai/v1/models"
API_KEY_ENV = "RUNINFRA_API_KEY"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Enumerate models exposed by runinfra.ai."
    )
    parser.add_argument(
        "--raw",
        action="store_true",
        help="Print the full raw JSON model listing response.",
    )
    parser.add_argument(
        "--api-key",
        type=str,
        default=None,
        help="Explicit Runinfra API key (overrides RUNINFRA_API_KEY environment variable).",
    )
    parser.add_argument(
        "--base-url",
        type=str,
        default="https://api.runinfra.ai/v1",
        help="Base URL for runinfra.ai (default: https://api.runinfra.ai/v1).",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=30.0,
        help="HTTP timeout in seconds (default: 30.0).",
    )
    return parser.parse_args()


async def async_main(args: argparse.Namespace) -> int:
    api_key = args.api_key or os.getenv(API_KEY_ENV)
    if not api_key:
        print(
            f"ERROR: {API_KEY_ENV} environment variable is not set and --api-key was not provided."
        )
        print("Set RUNINFRA_API_KEY or pass --api-key <key>.")
        return 2

    clean_key = api_key.strip()
    if clean_key.lower().startswith("bearer "):
        clean_key = clean_key[7:].lstrip()

    endpoint = f"{args.base_url.rstrip('/')}/models"
    headers = {
        "Authorization": f"Bearer {clean_key}",
        "Accept": "application/json",
    }

    print(f"Connecting to {endpoint} ...")
    async with httpx.AsyncClient(timeout=args.timeout) as client:
        try:
            response = await client.get(endpoint, headers=headers)
        except Exception as exc:
            print(f"ERROR: Failed to connect to {endpoint}: {exc}")
            return 1

        print(f"Status: {response.status_code} {response.reason_phrase}")
        print(
            "Response headers: " + json.dumps(dict(response.headers.items()), indent=2)
        )

        if response.status_code >= 400:
            print("\nError body:")
            print(response.text[:4000])
            return 1

        payload = response.json()

        if args.raw:
            print("\nRaw payload:")
            print(json.dumps(payload, indent=2))
            return 0

        data = payload.get("data", []) if isinstance(payload, dict) else []
        print(f"\nTotal models reported by runinfra.ai: {len(data)}")
        print("\nModel IDs (usable with proxy prefix runinfra/<model_id>):")
        for entry in data:
            if isinstance(entry, dict):
                entry_id = entry.get("id")
                owned_by = entry.get("owned_by") or ""
            elif isinstance(entry, str):
                entry_id = entry
                owned_by = ""
            else:
                continue

            if not entry_id:
                continue

            suffix = f"  [owned_by: {owned_by}]" if owned_by else ""
            print(f"  - runinfra/{entry_id}{suffix}")

        return 0


def main() -> None:
    args = parse_args()
    try:
        exit_code = asyncio.run(async_main(args))
    except KeyboardInterrupt:
        exit_code = 130
    raise SystemExit(exit_code)


if __name__ == "__main__":
    main()
