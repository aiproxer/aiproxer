#!/usr/bin/env bash
# Canonical staged checks; the hook wraps this whole sequence in a source guard.
set -euo pipefail
repo_root="$(git rev-parse --show-toplevel)"
bash "$repo_root/scripts/check-merge-receiver-branch.sh"
bash "$repo_root/scripts/check-worktree-layout.sh"
bash "$repo_root/scripts/check-change-size.sh" --staged
bash "$repo_root/scripts/check-staged-secrets.sh"
bash "$repo_root/scripts/check-local-specifics.sh" --staged
bash "$repo_root/scripts/check-release-clean.sh" --staged
bash "$repo_root/scripts/quality-gate.sh"
