#!/usr/bin/env bash
set -euo pipefail

if [[ "$(go env GOOS)" != "linux" ]]; then
	echo "configsource certification requires Linux" >&2
	exit 1
fi
# Shared precondition, so the actionable message lives in exactly one place.
# quality-gate.sh runs it up front, before the expensive suite.
bash "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/require-ext4-tmpdir.sh"

report="$(mktemp "$TMPDIR/configsource-certify.XXXXXX.json")"
trap 'rm -f "$report"' EXIT
# Surface the test output when execution fails. Without this guard a failing
# `go test` aborts under `set -e` before the report is parsed, and the EXIT trap
# then deletes the only record of the cause. Matches configsource-fault-check.sh.
if ! go test -json -count=1 -tags=configsource_cert \
	-run '^TestFixedSource_(PinnedAtomicRecovery|PinPreventsAcceptedInodeReuse)$' \
	./internal/infra/configsource/... >"$report"; then
	cat "$report" >&2
	echo "tagged configsource certification test execution failed" >&2
	exit 1
fi

python3 - "$report" <<'PY'
import json
import sys

required = {
    "TestFixedSource_PinnedAtomicRecovery",
    "TestFixedSource_PinPreventsAcceptedInodeReuse",
}
seen = {name: {"run": False, "pass": False} for name in required}
failed = []
with open(sys.argv[1], encoding="utf-8") as stream:
    for line in stream:
        try:
            event = json.loads(line)
        except json.JSONDecodeError as exc:
            raise SystemExit(f"invalid go test JSON event: {exc}")
        name = event.get("Test")
        action = event.get("Action")
        if name not in seen:
            continue
        if action == "run":
            seen[name]["run"] = True
        elif action == "pass":
            seen[name]["pass"] = True
        elif action in {"skip", "fail"}:
            failed.append(f"{name}: {action}")

missing = [name for name, state in seen.items() if not state["run"] or not state["pass"]]
if failed or missing:
    details = failed + [f"{name}: missing run/pass event" for name in missing]
    raise SystemExit("configsource certification did not execute cleanly: " + "; ".join(details))
print("configsource certification passed: both required ext4 tests ran and passed")
PY
