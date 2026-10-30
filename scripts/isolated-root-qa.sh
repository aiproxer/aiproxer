#!/usr/bin/env bash
# Phase 8.5: copy root excluding connectors/connector-support/Node/artifacts; GOWORK=off QA.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export GOWORK=off
go run ./tools/backendplugin/isolated_root_qa "$ROOT"
