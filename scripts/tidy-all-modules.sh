#!/usr/bin/env bash
# Keep every independent Go module synchronized with the root dependency graph.
# Update mode rewrites module metadata; --check reports drift without changing files.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHECK=0
if [[ "${1:-}" == "--check" ]]; then
  CHECK=1
elif [[ "${1:-}" != "" ]]; then
  echo "usage: $0 [--check]" >&2
  exit 2
fi

JOBS="${AIP_MODULE_CHECK_JOBS:-${CI_NODE_TOTAL:-4}}"
[[ "$JOBS" =~ ^[1-9][0-9]*$ ]] || {
  echo "AIP_MODULE_CHECK_JOBS must be a positive integer" >&2
  exit 2
}

TOOL_TMP="$(mktemp -d "${TMPDIR:-/tmp}/aip-module-tools.XXXXXX")"
cleanup() { rm -rf "$TOOL_TMP"; }
trap cleanup EXIT

# Build discovery once rather than compiling it through go run for every
# parent script invocation. The binary is intentionally temporary.
DISCOVER_MODULES_BIN="$TOOL_TMP/discover_modules"
(
  cd "$ROOT"
  go build -o "$DISCOVER_MODULES_BIN" ./tools/backendplugin/discover_modules
)

mapfile -t MODULES < <(
  {
    printf '%s\n' "." "testdata/enterprise_module" "testdata/external_connector" "testdata/external_feature_sdk" "testdata/external_billing_binding"
    "$DISCOVER_MODULES_BIN" -root "$ROOT"
  } | awk 'NF && !seen[$0]++'
)

run_module() {
  local module="$1"
  local dir="$ROOT/$module"
  [[ -f "$dir/go.mod" ]] || return 0
  echo "== $module =="
  if (( CHECK )); then
    (cd "$dir" && GOWORK=off go mod tidy -diff)
  else
    (cd "$dir" && GOWORK=off go mod tidy)
  fi
}

export ROOT CHECK
export -f run_module
printf '%s\n' "Tidying ${#MODULES[@]} modules with up to $JOBS workers"
printf '%s\n' "${MODULES[@]}" | xargs -r -P"$JOBS" -I{} bash -c 'run_module "$1"' _ {}
echo "OK: all discovered Go modules $([[ $CHECK -eq 1 ]] && echo are tidy || echo synchronized)"
