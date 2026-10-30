#!/usr/bin/env bash
# Run a named Go test gate, refusing a renamed or empty selector.
# Usage (from the owning module): bash scripts/run-named-go-tests.sh PACKAGE REGEXP [go test flags...]
set -euo pipefail
if [[ $# -lt 2 || -z "$1" || -z "$2" ]]; then
  echo 'usage: run-named-go-tests.sh PACKAGE REGEXP [go test flags...]' >&2
  exit 2
fi
package="$1" pattern="$2"
shift 2
listed="$("${GO:-go}" test "$@" -list "$pattern" "$package")"
if ! grep -qE '^Test[^[:space:]]+$' <<<"$listed"; then
  echo "named test gate matched zero tests: $package $pattern" >&2
  exit 1
fi
"${GO:-go}" test "$@" -run "$pattern" "$package"
