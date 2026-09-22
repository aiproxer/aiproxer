#!/usr/bin/env python3
"""List the Codex model slugs and reasoning-effort settings accepted by the
``openai-codex``, ``openai-codex-v2`` and ``openai-codex-app-server`` connectors.

The catalog is auto-discovered at proxy startup from the authenticated Codex
backend catalog endpoint; this script prints the shipped fallback snapshot
(``src/resources/codex/codex_model_catalog.json``) that the connectors fall
back to when discovery is unavailable. Run
``scripts/refresh_codex_model_catalog.py`` to refresh that snapshot.

Usage::

    ./.venv/Scripts/python.exe scripts/list_codex_models.py
    ./.venv/Scripts/python.exe scripts/list_codex_models.py --json
    ./.venv/Scripts/python.exe scripts/list_codex_models.py --matrix
"""

from __future__ import annotations

import argparse
import json
import sys
from dataclasses import asdict
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parents[1]
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(PROJECT_ROOT))

from src.connectors.openai_codex.catalog.fallback_loader import (
    CodexCatalogFallbackLoader,
)
from src.connectors.openai_codex.catalog.types import CodexModelCatalog


def _load_catalog() -> CodexModelCatalog:
    return CodexCatalogFallbackLoader().load()


def _render_table(catalog: CodexModelCatalog) -> None:
    try:
        from rich.console import Console
        from rich.table import Table
    except ImportError:
        _render_plain(catalog)
        return

    console = Console()
    console.print(
        "[bold]Codex model catalog[/bold] "
        "(openai-codex / v2 / app-server, shipped fallback snapshot)\n"
    )

    table = Table(title="Supported Codex models", show_lines=False)
    table.add_column("Slug", style="bold cyan")
    table.add_column("Default")
    table.add_column("Supported reasoning efforts")
    table.add_column("Notes", style="dim")

    for slug in catalog.routable_slugs():
        profile = catalog.get_profile(slug)
        assert profile is not None
        notes = []
        if profile.legacy:
            notes.append("legacy")
        if "ultra" in profile.supported_reasoning_levels:
            notes.append("ultra")
        elif "max" in profile.supported_reasoning_levels:
            notes.append("max")
        table.add_row(
            slug,
            profile.default_reasoning_level,
            " ".join(profile.supported_reasoning_levels),
            ", ".join(notes),
        )
    console.print(table)

    efforts = Table(title="Reasoning effort levels (low -> highest depth)")
    efforts.add_column("Effort", style="bold")
    efforts.add_column("Description")
    for effort in catalog.reasoning_effort_order:
        efforts.add_row(effort, catalog.reasoning_effort_descriptions.get(effort, ""))
    console.print(efforts)

    console.print(
        f"\nDefault reasoning effort: [bold]{catalog.default_reasoning_effort}[/bold]"
    )
    xhigh = catalog.models_supporting("xhigh")
    max_models = catalog.models_supporting("max")
    ultra = catalog.models_supporting("ultra")
    console.print(
        f"xhigh supported by {len(xhigh)} models, "
        f"max by {len(max_models)} ({', '.join(max_models)}), "
        f"ultra by {len(ultra)} ({', '.join(ultra)})."
    )


def _render_plain(catalog: CodexModelCatalog) -> None:
    print("Codex model catalog (openai-codex / v2 / app-server)")
    print("Reasoning effort order:", " ".join(catalog.reasoning_effort_order))
    print(f"Default reasoning effort: {catalog.default_reasoning_effort}")
    print("\nSupported Codex models:")
    for slug in catalog.routable_slugs():
        profile = catalog.get_profile(slug)
        assert profile is not None
        print(
            f"  {slug:<22} default={profile.default_reasoning_level:<6} "
            f"levels={' '.join(profile.supported_reasoning_levels)}"
        )


def _render_matrix(catalog: CodexModelCatalog) -> None:
    levels = catalog.reasoning_effort_order
    header = f"  {'model':<22}" + "".join(f"{e:<8}" for e in levels)
    print("Reasoning effort downgrade matrix (requested effort -> resolved):")
    print(header)
    for slug in catalog.routable_slugs():
        row = f"  {slug:<22}"
        for effort in levels:
            row += f"{catalog.clamp_reasoning_effort(slug, effort):<8}"
        print(row)


def _render_json(catalog: CodexModelCatalog) -> None:
    payload = {
        "source": "Codex backend catalog (shipped fallback snapshot)",
        "reasoning_effort_order": list(catalog.reasoning_effort_order),
        "reasoning_effort_descriptions": dict(catalog.reasoning_effort_descriptions),
        "default_reasoning_effort": catalog.default_reasoning_effort,
        "supported_models": [
            {
                "slug": slug,
                **asdict(catalog.get_profile(slug)),  # type: ignore[arg-type]
            }
            for slug in catalog.routable_slugs()
        ],
        "xhigh_supported_models": list(catalog.models_supporting("xhigh")),
        "max_supported_models": list(catalog.models_supporting("max")),
        "ultra_supported_models": list(catalog.models_supporting("ultra")),
    }
    print(json.dumps(payload, indent=2, default=str))


def main() -> int:
    parser = argparse.ArgumentParser(
        description="List Codex model slugs and reasoning-effort settings."
    )
    parser.add_argument(
        "--json",
        action="store_true",
        help="Emit the catalog as JSON.",
    )
    parser.add_argument(
        "--matrix",
        action="store_true",
        help="Also print the per-model reasoning effort downgrade matrix.",
    )
    args = parser.parse_args()

    catalog = _load_catalog()
    if args.json:
        _render_json(catalog)
    else:
        _render_table(catalog)
    if args.matrix and not args.json:
        print()
        _render_matrix(catalog)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
