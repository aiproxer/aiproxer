#!/usr/bin/env bash
# Point this repository at the versioned hooks under scripts/hooks/.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

git config core.hooksPath scripts/hooks
# The hooks themselves must be executable, but they invoke the gate scripts
# through `bash` rather than relying on those scripts' executable bits.
chmod +x scripts/hooks/pre-commit scripts/hooks/commit-msg scripts/hooks/pre-push

echo "Installed pre-commit, commit-msg, and pre-push hooks via core.hooksPath=scripts/hooks"
