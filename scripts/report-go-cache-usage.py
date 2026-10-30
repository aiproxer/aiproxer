#!/usr/bin/env python3
"""Summarize Go cache retention: usage against the cap, lanes left without a main snapshot."""

import argparse
import importlib.util
import json
from pathlib import Path

_spec = importlib.util.spec_from_file_location("prune", Path(__file__).with_name("prune-go-caches.py"))
prune = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(prune)

MAIN = "refs/heads/main"
GITHUB_CAP = 10 * 1024**3  # Per-repository limit; GitHub then evicts the oldest entries.
# The Go retention budget already owns the first ~8.5 GiB, so warn only within
# 1 GiB of GitHub's cap: that is when an unmanaged eviction can take a lane's
# snapshot, and it keeps the alarm meaningful instead of always-on.
WARN_AT = 0.9
GIB = 1024**3


def main_build_snapshots(caches):
    """Newest main build snapshot per (lane, os)."""
    newest = {}
    for cache in caches:
        match = prune.BUILD_V3.fullmatch(cache["key"])
        if not match or cache["ref"] != MAIN:
            continue
        group = (match[1], match[2])
        if group not in newest or cache["created_at"] > newest[group]["created_at"]:
            newest[group] = cache
    return newest


def report(caches, obsolete, lanes):
    """Return (markdown summary lines, warning messages)."""
    obsolete = set(obsolete)
    retained = [c for c in caches if c["id"] not in obsolete]
    size = lambda items: sum(c.get("size_in_bytes", 0) for c in items)  # noqa: E731
    owned = [c for c in retained if prune_owned(c["key"])]
    kept = main_build_snapshots(retained)
    dropped = main_build_snapshots([c for c in caches if c["id"] in obsolete])

    warnings = []
    total = size(retained)
    if total >= WARN_AT * GITHUB_CAP:
        warnings.append(
            f"Repository caches use {total / GIB:.1f} of {GITHUB_CAP // GIB} GiB; "
            "GitHub evicts the oldest entries beyond the cap, which turns a lane cold."
        )
    evicted = sorted(set(dropped) - set(kept))
    tiered = []
    for lane, os_name in evicted:
        if os_name == "Linux":
            warnings.append(f"Retention evicted the only main snapshot of lane {lane} (Linux); the byte budget is too small for the PR lanes.")
        else:
            tiered.append(f"{lane} ({os_name})")
    present = {lane for lane, _ in kept} | {lane for lane, _ in evicted}
    for lane in sorted(set(lanes) - present):
        warnings.append(f"Lane {lane} has no main build snapshot; its jobs run cold until a main push saves one.")

    linters = size([c for c in retained if prune.GOLANGCI_LINT.fullmatch(c["key"])])
    lines = [
        f"Repository caches: {total / GIB:.2f} GiB of {GITHUB_CAP // GIB} GiB "
        f"(Go-owned {size(owned) / GIB:.2f} GiB, retention budget {prune.DEFAULT_BYTE_BUDGET / GIB:.1f} GiB, "
        f"golangci-lint {linters / 1024**2:.0f} MiB).",
        "",
        "| Lane | OS | Compressed MiB | Created |",
        "| --- | --- | ---: | --- |",
    ]
    for (lane, os_name), cache in sorted(kept.items()):
        lines.append(f"| {lane} | {os_name} | {cache.get('size_in_bytes', 0) // 1024**2} | {cache['created_at']} |")
    if tiered:
        lines.append("")
        lines.append(f"Cross-platform tier yielded {', '.join(tiered)}; the next main run re-saves each (per-PR CI is Linux-only).")
    return lines, warnings


def prune_owned(key):
    patterns = (prune.SNAPSHOT, prune.LEGACY, prune.BUILD_V3, prune.MODULE_V3, prune.MODULE_SHARED, prune.SETUP_GO)
    return any(p.fullmatch(key) for p in patterns)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--caches", required=True, help="gh api --paginate --slurp cache pages (before pruning)")
    parser.add_argument("--obsolete", required=True, help="JSON list of cache ids retention is deleting")
    parser.add_argument("--policy", required=True, help="go-cache policy.json; its keys are the expected lanes")
    args = parser.parse_args()
    with open(args.caches, encoding="utf-8") as stream:
        caches = [cache for page in json.load(stream) for cache in page["actions_caches"]]
    with open(args.obsolete, encoding="utf-8") as stream:
        obsolete = json.load(stream)
    with open(args.policy, encoding="utf-8") as stream:
        lanes = list(json.load(stream))
    lines, warnings = report(caches, obsolete, lanes)
    print("\n".join(lines))
    for message in warnings:
        print(f"::warning::{message}")


if __name__ == "__main__":
    main()
