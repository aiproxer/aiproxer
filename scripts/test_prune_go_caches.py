"""Offline retention tests; no GitHub credentials or deletions."""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("prune", Path(__file__).with_name("prune-go-caches.py"))
prune = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prune)


def snapshot(number, job="test", ref="refs/heads/main", version="go1.26.6"):
    return {
        "id": number,
        "key": f"go-cache-ci-Linux-v2-X64-{version}-{job}-{'a' * 64}-{'b' * 40}",
        "ref": ref,
        "created_at": f"2026-09-{number:02d}T00:00:00Z",
    }


class RetentionTests(unittest.TestCase):
    def test_setup_go_survives_until_same_os_main_replacement(self):
        legacy, build = snapshot(1), snapshot(2)
        legacy["key"] = f"setup-go-Linux-x64-ubuntu24-go-1.26.6-{'a' * 64}"
        build["key"] = f"go-build-v3-qa-Windows-X64-go1.26.6-{'a' * 64}-{'b' * 40}"
        self.assertEqual(prune.obsolete_snapshots([legacy, build], set()), [])
        build["key"] = build["key"].replace("Windows", "Linux")
        self.assertEqual(prune.obsolete_snapshots([legacy, build], set()), [1])

    def test_portable_module_producer_supersedes_per_lane_copies(self):
        old, first, newest = snapshot(1), snapshot(2), snapshot(3)
        old["key"] = f"go-mod-v3-qa-Linux-{'a' * 64}-{'b' * 40}"
        for entry in [first, newest]:
            entry["key"] = f"go-mod-v3-shared-{'a' * 64}-{'b' * 40}"
        self.assertEqual(prune.obsolete_snapshots([old, first, newest], set()), [1, 2])

    def test_v3_keeps_latest_main_producer_and_enforces_byte_budget(self):
        caches = [snapshot(1), snapshot(2), snapshot(3, ref="refs/pull/10/merge")]
        for entry in caches:
            entry["key"] = f"go-build-v3-ci-unit-Linux-X64-go1.26.6-{'a' * 64}-{'b' * 40}"
            entry["size_in_bytes"] = 6
        self.assertEqual(prune.obsolete_snapshots(caches, {10}, byte_budget=6), [1, 3])

    def test_modules_and_builds_have_separate_retention(self):
        build, modules = snapshot(1), snapshot(2)
        build["key"] = f"go-build-v3-qa-Linux-X64-go1.26.6-{'a' * 64}-{'b' * 40}"
        modules["key"] = f"go-mod-v3-qa-Linux-{'a' * 64}-{'b' * 40}"
        self.assertEqual(prune.obsolete_snapshots([build, modules], set()), [])

    def test_keep_newest_per_job_and_ref_across_toolchain_updates(self):
        caches = [snapshot(1), snapshot(2, version="go1.26.7"), snapshot(3, job="db-parity"), snapshot(4, ref="refs/pull/10/merge")]
        self.assertEqual(prune.obsolete_snapshots(caches, {10}), [1])

    def test_closed_pr_only_go_namespaces(self):
        unrelated = snapshot(2, ref="refs/pull/11/merge")
        unrelated["key"] = "node-cache-important"
        self.assertEqual(prune.obsolete_snapshots([snapshot(1, ref="refs/pull/11/merge"), unrelated], set()), [1])

    def test_legacy_survives_until_same_ref_replacement(self):
        legacy = snapshot(1)
        legacy["key"] = "go-cache-ci-Linux-" + "a" * 64
        self.assertEqual(prune.obsolete_snapshots([legacy], set()), [])
        self.assertEqual(prune.obsolete_snapshots([legacy, snapshot(2, ref="refs/pull/10/merge")], {10}), [])
        self.assertEqual(prune.obsolete_snapshots([legacy, snapshot(3)], set()), [1])

    def test_unknown_key_is_never_selected(self):
        cache = snapshot(1)
        cache["key"] += "-custom"
        self.assertEqual(prune.obsolete_snapshots([cache], set()), [])

    def test_superseded_linter_caches_retire_per_version(self):
        old, new, other_version = snapshot(1), snapshot(2), snapshot(3)
        old["key"] = f"golangci-lint-2.14.0-{'a' * 40}"
        new["key"] = f"golangci-lint-2.14.0-{'b' * 40}"
        other_version["key"] = f"golangci-lint-2.13.0-{'c' * 40}"
        self.assertEqual(prune.obsolete_snapshots([old, new, other_version], set()), [1])

    def test_cross_platform_main_snapshots_yield_before_linux(self):
        linux, windows = snapshot(1), snapshot(2)
        linux["key"] = f"go-build-v3-qa-Linux-X64-go1.26.6-{'a' * 64}-{'b' * 40}"
        windows["key"] = f"go-build-v3-qa-Windows-X64-go1.26.6-{'a' * 64}-{'b' * 40}"
        for entry in (linux, windows):
            entry["size_in_bytes"] = 8
        # 16 bytes against an 8-byte budget: plain last-access order would evict
        # the older Linux snapshot; the cross-platform tier must yield first.
        self.assertEqual(prune.obsolete_snapshots([linux, windows], set(), byte_budget=8), [2])

    def test_private_branch_and_cross_platform_yield_before_linux(self):
        pr, linux, windows = snapshot(1, ref="refs/pull/10/merge"), snapshot(2), snapshot(3)
        for entry in (pr, linux, windows):
            entry["key"] = f"go-build-v3-qa-{'Windows' if entry is windows else 'Linux'}-X64-go1.26.6-{'a' * 64}-{'b' * 40}"
            entry["size_in_bytes"] = 8
        # 24 bytes against an 8-byte budget: the PR ref yields first and the most
        # recent main baseline (Windows) next; the Linux baseline survives even
        # though plain last-access order would evict it before Windows.
        self.assertEqual(prune.obsolete_snapshots([pr, linux, windows], {10}, byte_budget=8), [1, 3])


if __name__ == "__main__":
    unittest.main()
