#!/usr/bin/env bash
# Phase 8.5: one aipstd binary; package via release.yaml; same-binary inspect/doctor/invoke.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export GOWORK=off
go run ./tools/backendplugin/installed_plugin_smoke "$ROOT"
