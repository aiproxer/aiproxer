#!/usr/bin/env bash
# Workflow lint gate for .github/workflows.
# A workflow GitHub rejects produces no check runs at all, so its required
# checks silently never start; the deliverable state looks merely "blocked" or,
# worse, an older green head answers for the new one. actionlint catches the
# rejected expression before push.
#
# actionlint missing -> warn and skip (CI preflight installs actionlint 1.7.7
# first, so the gate is real there). Install locally with:
#   go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.7
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if ! command -v actionlint >/dev/null 2>&1; then
  echo "WARNING: actionlint not found on PATH; skipping workflow lint (CI installs actionlint 1.7.7 first)." >&2
  exit 0
fi

cd "$ROOT"
actionlint
echo "OK: workflow lint passed"
