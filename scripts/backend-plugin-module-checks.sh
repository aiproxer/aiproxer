#!/usr/bin/env bash
# Dynamic multi-module checks for connectors/* and connector-support/*.
# Avoids recursive make: this script is invoked by Make once.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export GOWORK=off
# Local worktrees may not have VCS metadata usable by Go's build stamping.
# Preserve caller/CI GOFLAGS so release checks retain normal stamping behavior.
if [[ "${AIP_DISABLE_VCS_STAMPING:-}" == "1" && -z "${GOFLAGS:-}" ]]; then
  export GOFLAGS=-buildvcs=false
fi

TOOL_TMP="$(mktemp -d "${TMPDIR:-/tmp}/aip-module-tools.XXXXXX")"
SYN=""
TMP=""
cleanup_all() {
  [[ -z "$SYN" ]] || rm -rf "$SYN"
  [[ -z "$TMP" ]] || rm -rf "$TMP"
  rm -rf "$TOOL_TMP"
}
trap cleanup_all EXIT

DISCOVER_MODULES_BIN="$TOOL_TMP/discover_modules"
go build -o "$DISCOVER_MODULES_BIN" ./tools/backendplugin/discover_modules
export AIP_DISCOVER_MODULES_BIN="$DISCOVER_MODULES_BIN"

echo "== root go list/build/module graph =="
go list ./... >/dev/null
go build -o /dev/null ./cmd/aipstd
if go list -m all | grep -E 'connectors/|connector-support/' >/dev/null; then
  echo "root go list -m all must not contain connector modules" >&2
  go list -m all | grep -E 'connectors/|connector-support/' || true
  exit 1
fi

echo "== root go test ./... =="
go test ./...

echo "== discovered module tidy/tests/builds =="
AIP_MODULE_CHECK_JOBS="${AIP_MODULE_CHECK_JOBS:-4}" bash "$ROOT/scripts/check-all-modules.sh"

echo "== synthetic connector discovery =="
SYN="$ROOT/connectors/_synthetic_ci_probe"
mkdir -p "$SYN"
printf 'module github.com/aiproxer/aiproxer/connectors/_synthetic_ci_probe\n\ngo 1.26.5\n' >"$SYN/go.mod"
FOUND="$("$DISCOVER_MODULES_BIN" -root "$ROOT")"
echo "$FOUND" | grep -q 'connectors/_synthetic_ci_probe'

rm -rf "$SYN"
SYN=""

echo "== root build with connectors/ absent (temp copy) =="
TMP="$(mktemp -d "${TMPDIR:-/tmp}/aip-root-no-connectors.XXXXXX")"
if command -v rsync >/dev/null 2>&1; then
  rsync -a --exclude '.git' --exclude 'connectors' --exclude 'connector-support' \
    --exclude '.aip-package-staging' --exclude '.aip-plugins' \
    "$ROOT/" "$TMP/"
else
  tar -C "$ROOT" --exclude='.git' --exclude='connectors' --exclude='connector-support' \
    --exclude='.aip-package-staging' --exclude='.aip-plugins' -cf - . | tar -C "$TMP" -xf -
fi
(
  cd "$TMP"
  export GOWORK=off
  go build -o /dev/null ./cmd/aipstd
)

echo "OK backend-plugin-module-checks"
