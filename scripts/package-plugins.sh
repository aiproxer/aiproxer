#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PROFILE="${1:?profile minimal|full}"
DEST="${2:?dest path}"
cd "$ROOT"
go run ./tools/backendplugin/package_plugins -root "$ROOT" -profile "$PROFILE" -dest "$DEST"
echo "OK package-$PROFILE -> $DEST"
