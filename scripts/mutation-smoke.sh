#!/usr/bin/env bash
# Optional mutation smoke: requires gremlins on PATH.
# Not part of default CI.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"
if ! command -v gremlins >/dev/null 2>&1; then
  echo "mutation-smoke: gremlins not on PATH; install with: go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0" >&2
  echo "mutation-smoke: skipping" >&2
  exit 0
fi
gremlins unleash --path "./pkg/aipapi" --timeout=5m
gremlins unleash --path "./internal/core/routing" --timeout=5m
