"""Offline tests for the cache usage report; no GitHub credentials."""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("usage", Path(__file__).with_name("report-go-cache-usage.py"))
usage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(usage)

MIB = 1024**2


def build(number, lane, os_name="Linux", ref="refs/heads/main", size=100 * MIB):
    return {
        "id": number,
        "key": f"go-build-v3-{lane}-{os_name}-X64-go1.26.6-{'a' * 64}-{'b' * 40}",
        "ref": ref,
        "size_in_bytes": size,
        "created_at": f"2026-10-{number:02d}T00:00:00Z",
    }


class UsageReportTests(unittest.TestCase):
    def test_healthy_lanes_produce_no_warning(self):
        caches = [build(1, "ci-suite"), build(2, "ci-suite-2")]
        lines, warnings = usage.report(caches, [], ["ci-suite", "ci-suite-2"])
        self.assertEqual(warnings, [])
        self.assertTrue(any("| ci-suite-2 | Linux | 100 |" in line for line in lines))

    def test_hyphenated_lane_is_not_confused_with_its_prefix(self):
        caches = [build(1, "ci-suite")]
        _, warnings = usage.report(caches, [], ["ci-suite", "ci-suite-2"])
        self.assertEqual(len(warnings), 1)
        self.assertIn("ci-suite-2", warnings[0])

    def test_lane_whose_only_main_snapshot_is_removed_is_reported(self):
        caches = [build(1, "ci-lint"), build(2, "ci-suite")]
        _, warnings = usage.report(caches, [1], ["ci-suite"])
        self.assertEqual(len(warnings), 1)
        self.assertIn("only main snapshot of lane ci-lint", warnings[0])

    def test_superseded_snapshot_removal_is_not_an_eviction(self):
        caches = [build(1, "ci-suite"), build(2, "ci-suite")]
        _, warnings = usage.report(caches, [1], ["ci-suite"])
        self.assertEqual(warnings, [])

    def test_pull_request_snapshots_do_not_count_as_a_main_baseline(self):
        caches = [build(1, "ci-suite", ref="refs/pull/7/merge")]
        _, warnings = usage.report(caches, [], ["ci-suite"])
        self.assertEqual(len(warnings), 1)
        self.assertIn("no main build snapshot", warnings[0])

    def test_usage_near_the_cap_warns(self):
        caches = [build(1, "ci-suite", size=9 * 1024**3)]
        _, warnings = usage.report(caches, [], ["ci-suite"])
        self.assertEqual(len(warnings), 1)
        self.assertIn("of 10 GiB", warnings[0])

    def test_usage_at_the_retention_budget_does_not_warn(self):
        """The Go budget plus reserve must stay below the warning threshold,
        or the report would warn on every run instead of on drift."""
        caches = [build(1, "ci-suite", size=usage.prune.DEFAULT_BYTE_BUDGET)]
        _, warnings = usage.report(caches, [], ["ci-suite"])
        self.assertEqual(warnings, [])

    def test_cross_platform_eviction_is_tiering_not_a_warning(self):
        caches = [build(1, "ci-unit", os_name="Windows"), build(2, "ci-unit")]
        lines, warnings = usage.report(caches, [1], ["ci-unit"])
        self.assertEqual(warnings, [])
        self.assertIn("Cross-platform tier yielded ci-unit (Windows)", "\n".join(lines))

    def test_deleted_caches_do_not_count_toward_usage(self):
        caches = [build(1, "ci-suite"), build(2, "ci-suite", size=9 * 1024**3)]
        _, warnings = usage.report(caches, [2], ["ci-suite"])
        self.assertEqual(warnings, [])


if __name__ == "__main__":
    unittest.main()
