#!/usr/bin/env bash
# Validate every independent Go module with bounded parallelism.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# Module checks validate isolated builds; local VCS metadata must not affect them.
# CI/release stamping remains enabled in release workflows.
if [[ "${AIP_DISABLE_VCS_STAMPING:-}" == "1" && -z "${GOFLAGS:-}" ]]; then
  export GOFLAGS=-buildvcs=false
fi
JOBS="${AIP_MODULE_CHECK_JOBS:-${CI_NODE_TOTAL:-4}}"
[[ "$JOBS" =~ ^[1-9][0-9]*$ ]] || { echo "AIP_MODULE_CHECK_JOBS must be a positive integer" >&2; exit 2; }

TOOL_TMP="$(mktemp -d "${TMPDIR:-/tmp}/aip-module-tools.XXXXXX")"
trap 'rm -rf "$TOOL_TMP"' EXIT
DISCOVER_MODULES_BIN="${AIP_DISCOVER_MODULES_BIN:-}"
if [[ -z "$DISCOVER_MODULES_BIN" ]]; then
  DISCOVER_MODULES_BIN="$TOOL_TMP/discover_modules"
  go build -o "$DISCOVER_MODULES_BIN" ./tools/backendplugin/discover_modules
fi

mapfile -t MODULES < <(
  {
    printf '%s\n' "testdata/enterprise_module" "testdata/external_connector" "testdata/external_feature_sdk" "testdata/external_billing_binding"
    "$DISCOVER_MODULES_BIN" -root "$ROOT"
  } | awk 'NF && !seen[$0]++'
)

run_module() {
  local module="$1"
  local dir="$ROOT/$module"
  [[ -f "$dir/go.mod" ]] || return 0
  echo "== $module =="
  (
    # This subshell runs inside a fresh `bash -c`, which does not inherit the
    # parent script's errexit. Re-enable it here so a failing tidy, test, or
    # command build is not absorbed by the trailing `if [[ -d cmd ]]`.
    set -euo pipefail
    cd "$dir"
    GOWORK=off go mod tidy -diff
    # Bound each module's suite explicitly. The Go default is ten minutes, so a
    # single module that blocks in teardown used to hold the job for the full
    # default before reporting. The ACP cross-compilation suite can take several
    # minutes on a cold runner; five minutes leaves room for that work while
    # keeping a stuck module bounded.
    GOWORK=off go test -timeout=5m ./...
    if [[ -d cmd ]]; then
      for command_dir in cmd/*/; do
        [[ -d "$command_dir" ]] || continue
        # Build into the scratch dir: a bare `go build ./cmd/x` writes the binary
        # into the module root, where it is one `git add` away from a commit.
        GOWORK=off go build -o "$TOOL_TMP/$module/" "./${command_dir%/}"
      done
    fi
  )
}

export ROOT TOOL_TMP
export -f run_module
printf '%s\n' "Checking ${#MODULES[@]} modules with up to $JOBS workers"
printf '%s\n' "${MODULES[@]}" | xargs -r -P"$JOBS" -I{} bash -c 'run_module "$1"' _ {}
echo "OK: all discovered Go modules passed tidy, tests, and command builds"
