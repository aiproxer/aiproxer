#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || -z "${1:-}" ]]; then
  echo "usage: $0 <SPEC>" >&2
  exit 2
fi

if ! grep -qF "{name: \"$1\"" tools/kiro/speccheck/*_test.go; then
  echo "kiro-spec-check: $1 is not registered in tools/kiro/speccheck; run 'go run ./tools/kiro/kirocheck' for lifecycle and budget checks" >&2
  exit 2
fi

export KIRO_SPEC="$1"
exec go test ${GO_TEST_FLAGS:--parallel=8 -timeout=10m} ./tools/kiro/speccheck/ -run '^TestKiroSpec$' -count=1
