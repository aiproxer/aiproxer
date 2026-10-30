#!/usr/bin/env bash
# Host no-Node verification lane (spec cursor-sdk-standalone, task 5.1).
# Delegates to the Go tool so the lane needs no interpreter beyond Go itself and
# so it can re-exec itself inside a private mount namespace.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
exec go run ./tools/backendplugin/node_independence --root "$ROOT" "$@"