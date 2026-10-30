#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT/connectors/localstub"
export GOWORK=off
go test ${GO_TEST_FLAGS:--parallel=8 -timeout=10m} ./... -count=1
echo "OK backend-plugin-example-check"
