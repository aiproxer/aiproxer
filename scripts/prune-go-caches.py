#!/usr/bin/env python3
"""Select obsolete repository-owned Go cache snapshots; never delete directly.

Owns the per-lane compiler/module snapshots and the golangci-lint analysis
caches: keep the newest of each, and let cross-platform main baselines yield
before the Linux ones the per-PR lanes restore.
"""

import argparse
import json
import re
from collections import defaultdict

SNAPSHOT = re.compile(
    r"^go-cache-(ci|qa|backend-plugin|acp|cost)-(Linux|Windows|macOS)"
    r"-v2-([^-]+)-go[^-]+-(.+)-[0-9a-f]{64}-[0-9a-f]{40}$"
)
LEGACY = re.compile(
    r"^go-cache-(ci|qa|backend-plugin|acp)-(Linux|Windows|macOS)-[0-9a-f]{64}$"
)
PR_REF = re.compile(r"^refs/pull/(\d+)/merge$")
BUILD_V3 = re.compile(r"^go-build-v3-([a-z0-9-]+)-(Linux|Windows|macOS)-([^-]+)-go[^-]+-[0-9a-f]{64}-[0-9a-f]{40}$")
MODULE_V3 = re.compile(r"^go-mod-v3-([a-z0-9-]+)-(Linux|Windows|macOS)-[0-9a-f]{64}-[0-9a-f]{40}$")
MODULE_SHARED = re.compile(r"^go-mod-v3-shared-[0-9a-f]{64}-[0-9a-f]{40}$")
SETUP_GO = re.compile(r"^setup-go-(Linux|Windows|macOS)-(?:x64|arm64)(?:-ubuntu[0-9]+)?-go-[0-9.]+-[0-9a-f]{64}$")
# Per-commit linter analysis caches. Every restore is a version-prefix restore,
# so only the newest entry per version is reachable; unbounded growth here
# (1.35 GiB across ten entries on 2026-10-12) fills the repository cap and
# pushes GitHub's own last-access eviction into lane snapshots.
GOLANGCI_LINT = re.compile(r"^golangci-lint-([0-9]+\.[0-9]+\.[0-9]+)-[0-9a-f]{40}$")
# Retain the newest snapshot of every lane: the 27 lane/OS groups summed to
# ~9.2 GiB on 2026-10-12, but the Linux and portable ones the per-PR lanes
# restore are ~7.7 GiB. Cross-platform baselines yield first, and the 10 GiB
# repository cap must still cover the non-Go owners. The usage report names any
# lane left without a main snapshot when this is too small.
DEFAULT_BYTE_BUDGET = 8704 * 1024**2  # 8.5 GiB


def cache_os(cache):
    """OS of a repository-owned snapshot; empty for portable or shared keys."""
    match = SETUP_GO.fullmatch(cache["key"])
    if match:
        return match[1]
    for pattern in (SNAPSHOT, LEGACY, BUILD_V3, MODULE_V3):
        match = pattern.fullmatch(cache["key"])
        if match:
            return match[2]
    return ""


def obsolete_snapshots(caches, open_prs, byte_budget=DEFAULT_BYTE_BUDGET):
    """Keep newest snapshot per family/OS/arch/job/ref, including active PRs.

    Retire legacy dependency-only snapshots only after a replacement exists in
    the same family/OS/ref. Unknown namespaces/formats and non-Go caches survive.
    """
    groups = defaultdict(list)
    replacements = set()
    obsolete = set()
    legacy = []
    linters = []
    owned = []
    setup_go = []
    for cache in caches:
        key = cache["key"]
        linter = GOLANGCI_LINT.fullmatch(key)
        if linter:
            linters.append((cache, linter[1]))
            continue
        match = SNAPSHOT.fullmatch(key)
        old = LEGACY.fullmatch(key)
        build = BUILD_V3.fullmatch(key)
        modules = MODULE_V3.fullmatch(key)
        shared_modules = MODULE_SHARED.fullmatch(key)
        setup = SETUP_GO.fullmatch(key)
        if not match and not old and not build and not modules and not shared_modules and not setup:
            continue
        owned.append(cache)
        ref = cache["ref"]
        pr = PR_REF.fullmatch(ref)
        if pr and int(pr[1]) not in open_prs:
            obsolete.add(cache["id"])
            continue
        if old:
            legacy.append((cache, (old[1], old[2], ref)))
            continue
        if setup:
            setup_go.append((cache, setup[1]))
            continue
        if shared_modules:
            groups[("modules-shared-v3", "portable", "any", "modules", ref)].append(cache)
            continue
        if build or modules:
            if build:
                lane, os_name, arch = build.groups()
                family, job = "build-v3", lane
            else:
                lane, os_name = modules.groups()
                family, arch, job = "modules-v3", "any", lane
            groups[(family, os_name, arch, job, ref)].append(cache)
            # An advancing v3 producer supersedes the old combined snapshots
            # of this workload. Never retire a v2 lane before its replacement.
            old_family = {"ci-unit": "ci", "ci-db": "ci", "cost": "cost"}.get(lane, lane)
            replacements.add((old_family, os_name, ref))
            continue
        family, os_name, arch, job = match.groups()
        replacements.add((family, os_name, ref))
        groups[(family, os_name, arch, job, ref)].append(cache)
    for entries in groups.values():
        # Creation time, not last access: a frequently restored stale snapshot
        # must not displace the newest completed build progress.
        entries.sort(key=lambda c: (c["created_at"], c["id"]), reverse=True)
        obsolete.update(c["id"] for c in entries[1:])
    obsolete.update(c["id"] for c, lane in legacy if lane in replacements)
    shared_refs = {c["ref"] for c in owned if MODULE_SHARED.fullmatch(c["key"])}
    obsolete.update(c["id"] for c in owned if MODULE_V3.fullmatch(c["key"]) and c["ref"] in shared_refs)
    # Retire superseded v2 snapshots after a v3 producer exists on the same ref.
    v3_lanes = set()
    for cache in owned:
        key_match = BUILD_V3.fullmatch(cache["key"])
        if key_match:
            lane, os_name, _ = key_match.groups()
            family = {"ci-unit": "ci", "ci-db": "ci"}.get(lane, lane)
            v3_lanes.add((family, os_name, cache["ref"]))
    # Every setup-go writer has migrated. Retire its combined archive only
    # after a bounded compiler producer exists on the same OS and ref.
    v3_os_refs = {(os_name, ref) for _, os_name, ref in v3_lanes}
    obsolete.update(c["id"] for c, os_name in setup_go if (os_name, c["ref"]) in v3_os_refs)
    for cache in owned:
        match = SNAPSHOT.fullmatch(cache["key"])
        if match and (match[1], match[2], cache["ref"]) in v3_lanes:
            obsolete.add(cache["id"])
    # Keep only the newest analysis cache per linter version: older entries are
    # unreachable behind the version-prefix restore keys.
    by_linter_version = defaultdict(list)
    for cache, version in linters:
        by_linter_version[version].append(cache)
    for entries in by_linter_version.values():
        entries.sort(key=lambda c: (c["created_at"], c["id"]), reverse=True)
        obsolete.update(c["id"] for c in entries[1:])
    survivors = [c for c in owned if c["id"] not in obsolete]
    total = sum(c.get("size_in_bytes", 0) for c in survivors)
    # Main baselines serve every PR. Evict private branch caches first, then
    # cross-platform main baselines: per-PR CI is Linux-only, so the Windows and
    # macOS snapshots only serve their own daily runs, which re-save them. Linux
    # and portable snapshots yield last, by least recent access.
    survivors.sort(key=lambda c: (
        c["ref"] == "refs/heads/main",
        cache_os(c) in ("Linux", ""),
        c.get("last_accessed_at", c["created_at"]),
        c["id"],
    ))
    for cache in survivors:
        if total <= byte_budget:
            break
        obsolete.add(cache["id"])
        total -= cache.get("size_in_bytes", 0)
    return sorted(obsolete)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--caches", required=True, help="gh api --paginate --slurp cache pages")
    parser.add_argument("--open-prs", required=True, help="complete gh api --paginate --slurp open PR pages")
    args = parser.parse_args()
    with open(args.caches, encoding="utf-8") as stream:
        pages = json.load(stream)
    with open(args.open_prs, encoding="utf-8") as stream:
        pr_pages = json.load(stream)
    caches = [cache for page in pages for cache in page["actions_caches"]]
    open_prs = {pr["number"] for page in pr_pages for pr in page}
    print(json.dumps(obsolete_snapshots(caches, open_prs)))


if __name__ == "__main__":
    main()
