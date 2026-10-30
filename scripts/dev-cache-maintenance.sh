#!/usr/bin/env bash
# Explicit cache maintenance, sharing the Go/analyzer cache exclusion lock.
set -euo pipefail
script_dir=$(cd "$(dirname "$0")" && pwd)
case "${1:-}" in
"")
	echo 'Dry run: would run go clean -cache and golangci-lint cache clean under an exclusive resource lock.'
	echo 'Use --apply only for deliberate maintenance, not routine troubleshooting.'
	exit 0
	;;
--apply) [[ $# == 1 ]] || { echo 'usage: dev-cache-maintenance.sh [--apply]' >&2; exit 2; } ;;
*) echo 'usage: dev-cache-maintenance.sh [--apply]' >&2; exit 2 ;;
esac
command -v go >/dev/null
command -v golangci-lint >/dev/null
exec bash "$script_dir/go-dev-guard.sh" --aip-resource-exclusive bash -ec '
echo "Exclusive cache maintenance: clearing Go build cache"
go clean -cache
echo "Exclusive cache maintenance: clearing analyzer cache"
golangci-lint cache clean
echo "Cache maintenance complete"
'
