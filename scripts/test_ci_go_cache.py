"""Exercise snapshot bounds without deleting real Go caches."""
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("cache", Path(__file__).with_name("ci-go-cache.py"))
cache = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cache)


class SnapshotTests(unittest.TestCase):
    def test_snapshot_cli_with_windows_console_encoding(self):
        # Save runs only on main. Exercise its real CLI under the Windows
        # runner's cp1252 stdout even when this preflight runs on Linux.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            snapshot = root / "snapshot"
            snapshot.mkdir()
            (snapshot / "oversized").write_bytes(b"x" * (2 * 1024 * 1024))
            (snapshot / "keep").write_bytes(b"keep")
            summary = root / "summary.md"
            result = subprocess.run(
                [sys.executable, str(Path(__file__).with_name("ci-go-cache.py")),
                 "--path", str(snapshot), "--mib", "1"],
                env={**os.environ, "GITHUB_ACTIONS": "true",
                     "PYTHONIOENCODING": "cp1252", "GITHUB_STEP_SUMMARY": str(summary)},
                capture_output=True, timeout=10,
            )
            self.assertEqual(result.returncode, 0, result.stderr.decode("cp1252", errors="replace"))
            self.assertIn("2 -> 0 MiB (budget 1 MiB)", result.stdout.decode("cp1252"))
            self.assertEqual(sorted(p.name for p in snapshot.iterdir()), ["keep"])
            self.assertIn("2 -> 0 MiB (budget 1 MiB)", summary.read_text(encoding="utf-8"))

    def test_only_designated_producer_can_publish_borrowed_lane(self):
        policy = {"race": {"workflow": "Connector pool race", "job": "connector-pool-race", "build_mib": 1536}}
        self.assertEqual(cache.resolve_lane(policy, "race", "restore", "Release", "verify"), 1536)
        self.assertEqual(cache.resolve_lane(policy, "race", "save", "Connector pool race", "connector-pool-race"), 1536)
        with self.assertRaises(ValueError):
            cache.resolve_lane(policy, "race", "save", "Release", "verify")
        with self.assertRaises(ValueError):
            cache.resolve_lane(policy, "unknown", "restore", "Release", "verify")

    def test_portable_downloads_round_trip_and_ignore_links(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            downloads, snapshot, restored = (root / name for name in ["downloads", "snapshot", "restored"])
            downloads.mkdir()
            archive = downloads / "example.com" / "module" / "@v" / "v1.0.0.zip"
            archive.parent.mkdir(parents=True)
            archive.write_bytes(b"validated module archive")
            (downloads / "link").symlink_to(archive)
            cache.copy_downloads(downloads, snapshot)
            cache.copy_downloads(snapshot, restored)
            self.assertEqual((restored / archive.relative_to(downloads)).read_bytes(), b"validated module archive")
            self.assertFalse((restored / "link").exists())

    def test_keep_recent_files_within_budget(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, age in [("old", 1), ("new", 2), ("oversized", 3)]:
                path = root / name
                path.write_bytes(b"x" * (20 if name == "oversized" else 4))
                os.utime(path, (age, age))
            before, after = cache.bound_snapshot(root, 4)
            self.assertEqual((before, after), (28, 4))
            self.assertEqual(sorted(p.name for p in root.iterdir()), ["new"])

    def test_never_follow_symlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            outside = root / "outside"
            outside.write_bytes(b"keep")
            snapshot = root / "snapshot"
            snapshot.mkdir()
            (snapshot / "link").symlink_to(outside)
            cache.bound_snapshot(snapshot, 1)
            self.assertEqual(outside.read_bytes(), b"keep")

    def test_reject_nonpositive_budget(self):
        with self.assertRaises(ValueError):
            cache.bound_snapshot(Path("unused"), 0)


if __name__ == "__main__":
    unittest.main()
