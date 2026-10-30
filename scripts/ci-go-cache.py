#!/usr/bin/env python3
"""Bound disposable CI snapshots by bytes; missing Go entries rebuild normally."""
import argparse
import json
import os
from pathlib import Path
import shutil


def resolve_lane(policy, lane, phase, workflow, job):
    if lane not in policy or phase not in {"restore", "save"}:
        raise ValueError("unknown cache lane or phase")
    owner = policy[lane]
    if phase == "save" and (workflow, job) != (owner["workflow"], owner["job"]):
        raise ValueError(f"only {owner['workflow']}/{owner['job']} may publish {lane}")
    return owner["build_mib"]


def copy_downloads(source, destination):
    """Stage portable download files; exclude links outside the cache tree."""
    for directory, _, names in os.walk(source, followlinks=False):
        for name in names:
            path = Path(directory) / name
            if path.is_symlink() or not path.is_file():
                continue
            target = destination / path.relative_to(source)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, target)


def bound_snapshot(root, budget):
    if budget <= 0:
        raise ValueError("cache budget must be positive")
    files = []
    for directory, _, names in os.walk(root, followlinks=False):
        for name in names:
            path = Path(directory) / name
            if path.is_symlink() or not path.is_file():
                continue
            stat = path.stat()
            files.append((stat.st_mtime_ns, str(path), stat.st_size, path))
    before = sum(entry[2] for entry in files)
    retained = 0
    for _, _, size, path in sorted(files, reverse=True):
        if retained + size <= budget:
            retained += size
        else:
            path.unlink()
    return before, retained


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--path", type=Path)
    parser.add_argument("--mib", type=int)
    parser.add_argument("--copy-from", type=Path)
    parser.add_argument("--copy-to", type=Path)
    parser.add_argument("--lane")
    parser.add_argument("--phase", choices=["restore", "save"])
    args = parser.parse_args()
    # Only the disposable hosted-runner cache is modified. Workflow callers
    # resolve this path with go env, after all build/test commands finish.
    if os.environ.get("GITHUB_ACTIONS") != "true":
        parser.error("snapshot preparation is restricted to GitHub Actions")
    if args.lane is not None or args.phase is not None:
        if args.lane is None or args.phase is None or any(v is not None for v in [args.path, args.mib, args.copy_from, args.copy_to]):
            parser.error("lane and phase must be specified together without snapshot operations")
        with open(".github/actions/go-cache/policy.json", encoding="utf-8") as stream:
            policy = json.load(stream)
        budget = resolve_lane(policy, args.lane, args.phase, os.environ["GITHUB_WORKFLOW"], os.environ["GITHUB_JOB"])
        print(f"build-mib={budget}")
        return
    if args.copy_from is not None or args.copy_to is not None:
        if args.copy_from is None or args.copy_to is None or args.path is not None or args.mib is not None:
            parser.error("copy-from and copy-to must be specified together, without snapshot bounds")
        copy_downloads(args.copy_from, args.copy_to)
        return
    if args.path is None or args.mib is None:
        parser.error("snapshot bounds require path and mib")
    before, after = bound_snapshot(args.path, args.mib * 1024 * 1024)
    summary = f"Go snapshot {args.path}: {before // 1024 // 1024} -> {after // 1024 // 1024} MiB (budget {args.mib} MiB)"
    print(summary)
    if summary_path := os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(summary_path, "a", encoding="utf-8") as stream:
            stream.write(summary + "\n")


if __name__ == "__main__":
    main()
