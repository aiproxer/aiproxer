#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

if ! command -v go >/dev/null 2>&1; then
	echo "configsource fault checks require the Go toolchain" >&2
	exit 1
fi
if [[ "$(go env GOOS)" != "linux" ]]; then
	echo "configsource fault checks require Linux" >&2
	exit 1
fi
if [[ -z "${TMPDIR:-}" || "$TMPDIR" != /* || ! -d "$TMPDIR" || ! -w "$TMPDIR" ]]; then
	echo "configsource fault checks require an explicit writable TMPDIR on ext4" >&2
	exit 1
fi
if ! command -v findmnt >/dev/null 2>&1; then
	echo "configsource fault checks require findmnt to verify TMPDIR storage" >&2
	exit 1
fi
tmpdir_fs="$(findmnt --noheadings --output FSTYPE --target "$TMPDIR" 2>/dev/null || true)"
if [[ "$tmpdir_fs" != "ext4" ]]; then
	echo "configsource fault checks require TMPDIR on ext4 (found: ${tmpdir_fs:-unknown})" >&2
	exit 1
fi
if [[ -n "${GOTMPDIR:-}" && ( ! -d "$GOTMPDIR" || ! -w "$GOTMPDIR" || ! -x "$GOTMPDIR" ) ]]; then
	echo "GOTMPDIR must be an existing writable, executable directory when set" >&2
	exit 1
fi
if ! command -v python3 >/dev/null 2>&1; then
	echo "configsource fault checks require python3 to validate go test JSON" >&2
	exit 1
fi

coordinator_test='TestCoordinator_PublishedStartFailureRetainsAdoptedSourceAndCanQuiesce'
host_close_test='TestHostCloseCachedSourceCloseFailure'
host_close_lifecycle_test='TestHostCloseLiveSourceDeadlineAndFinalRelease'
runtimehost_pkg='./internal/infra/runtimehost'
runtimebundle_pkg='./internal/infra/runtimebundle'

discover_exact_test() {
	local name="$1"
	local package="$2"
	local output
	if ! output="$(go test -tags=configsource_faulttest -list "^${name}$" "$package")"; then
		echo "failed to discover required configsource fault test $name in $package" >&2
		return 1
	fi
	if ! grep -Fqx -- "$name" <<<"$output"; then
		echo "required configsource fault test $name was not discovered in $package" >&2
		return 1
	fi
	echo "discovered $name in $package"
}

# Discover both exact tests before running either one so a partial lane cannot
# report success when one tagged regression disappears.
discover_exact_test "$coordinator_test" "$runtimehost_pkg"
discover_exact_test "$host_close_test" "$runtimebundle_pkg"
discover_exact_test "$host_close_lifecycle_test" "$runtimebundle_pkg"

report="$(mktemp "$TMPDIR/configsource-fault-check.XXXXXX.json")"
trap 'rm -f "$report"' EXIT
# The same fault scenarios remain mandatory locally, but race evidence belongs
# exclusively to remote GitHub CI. CI metadata cannot override the dev-host guard.
race_flags=()
blocked_host=false
# shellcheck source=scripts/dev-host-policy.sh
source "$(dirname "${BASH_SOURCE[0]}")/dev-host-policy.sh"
if aip_development_host; then blocked_host=true; fi
if [[ "${GITHUB_ACTIONS:-}" == true && "$blocked_host" == false ]]; then
	race_flags=(-race)
	echo "Running configsource fault checks with race instrumentation (GitHub CI)."
else
	echo "Running configsource fault checks without race instrumentation (local host)."
fi
if ! go test -json "${race_flags[@]}" -count=1 -tags=configsource_faulttest \
	-run "^${coordinator_test}$|^TestHostClose.*Source" \
	"$runtimehost_pkg" "$runtimebundle_pkg" >"$report"; then
	cat "$report" >&2
	echo "tagged configsource fault test execution failed" >&2
	exit 1
fi

python3 - "$report" <<'PY'
import json
import sys

required = {
    "TestCoordinator_PublishedStartFailureRetainsAdoptedSourceAndCanQuiesce": {
        "TestCoordinator_PublishedStartFailureRetainsAdoptedSourceAndCanQuiesce": None,
        "TestCoordinator_PublishedStartFailureRetainsAdoptedSourceAndCanQuiesce/returned_error": None,
        "TestCoordinator_PublishedStartFailureRetainsAdoptedSourceAndCanQuiesce/panic": None,
    },
    "TestHostCloseCachedSourceCloseFailure": {
        "TestHostCloseCachedSourceCloseFailure": None,
        "TestHostCloseCachedSourceCloseFailure/initially-idle": None,
        "TestHostCloseCachedSourceCloseFailure/admitted-attempt": None,
    },
    "TestHostCloseLiveSourceDeadlineAndFinalRelease": {
        "TestHostCloseLiveSourceDeadlineAndFinalRelease": None,
        "TestHostCloseLiveSourceDeadlineAndFinalRelease/initially-idle": None,
        "TestHostCloseLiveSourceDeadlineAndFinalRelease/admitted-attempt": None,
    },
}
seen = {name: {"run": False, "pass": False} for cases in required.values() for name in cases}
failed = []

with open(sys.argv[1], encoding="utf-8") as stream:
    for line in stream:
        try:
            event = json.loads(line)
        except json.JSONDecodeError as exc:
            raise SystemExit(f"invalid go test JSON event: {exc}")
        name = event.get("Test")
        action = event.get("Action")
        if not isinstance(name, str):
            continue
        parent = next((test for test in required if name == test or name.startswith(test + "/")), None)
        if parent is None:
            continue
        if action == "run" and name in seen:
            seen[name]["run"] = True
        elif action == "pass" and name in seen:
            seen[name]["pass"] = True
        elif action in {"skip", "fail"}:
            failed.append(f"{name}: {action}")

missing = [name for name, state in seen.items() if not state["run"] or not state["pass"]]
if failed or missing:
    details = failed + [f"{name}: missing run/pass event" for name in missing]
    raise SystemExit("configsource fault checks did not execute cleanly: " + "; ".join(details))

print("configsource fault checks passed: coordinator adoption and Host.Close tests plus all required subtests ran without skips")
PY
