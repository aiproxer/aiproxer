#!/usr/bin/env bash
# Fail when a commit or branch range changes more than 100 Go files.
# Admin override: AIP_ALLOW_LARGE_CHANGE=1 or `git config aip.allowLargeChange true`.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$repo_root"

if [[ $# -eq 0 ]]; then
	set -- --staged
fi

exec go run ./tools/changesize "$@"
