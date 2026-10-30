#!/usr/bin/env bash
# Simulated race invocations use only a fake toolchain.
set -euo pipefail
script_dir=$(cd "$(dirname "$0")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/bin"
cat > "$fixture/bin/hostname" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$FAULT_TEST_HOST"
STUB
cat > "$fixture/bin/findmnt" <<'STUB'
#!/usr/bin/env bash
printf 'ext4\n'
STUB
cat > "$fixture/bin/go" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == env ]]; then printf 'linux\n'; exit 0; fi
for arg in "$@"; do
  if [[ "$arg" == -list ]]; then
    for name in TestCoordinator_PublishedStartFailureRetainsAdoptedSourceAndCanQuiesce TestHostCloseCachedSourceCloseFailure TestHostCloseLiveSourceDeadlineAndFinalRelease; do
      printf '%s\n' "$name"
    done
    exit 0
  fi
done
printf '%s\n' "$@" > "$FAULT_TEST_CALLS"
python3 - <<'PY'
import json
import os
tests = {
    'TestCoordinator_PublishedStartFailureRetainsAdoptedSourceAndCanQuiesce': ['returned_error', 'panic'],
    'TestHostCloseCachedSourceCloseFailure': ['initially-idle', 'admitted-attempt'],
    'TestHostCloseLiveSourceDeadlineAndFinalRelease': ['initially-idle', 'admitted-attempt'],
}
for parent, cases in tests.items():
    for name in [parent] + [parent + '/' + case for case in cases]:
        for action in ['run', 'skip' if os.environ.get('FAULT_TEST_SKIP') == '1' else 'pass']:
            print(json.dumps({'Test': name, 'Action': action}))
PY
STUB
chmod +x "$fixture/bin/hostname" "$fixture/bin/findmnt" "$fixture/bin/go"
export PATH="$fixture/bin:$PATH" TMPDIR="$fixture" GOENV=off GOFLAGS=
export FAULT_TEST_CALLS="$fixture/calls"
export AIP_DEV_HOST_POLICY_FILE="$fixture/host-policy"
unset COMPUTERNAME AIP_ALLOW_RACE_ON_DEV CI GITHUB_ACTIONS

check_mode() {
  local host="$1" github="$2" want_race="$3"
  rm -f "$FAULT_TEST_CALLS"
  FAULT_TEST_HOST="$host" HOSTNAME="$host" GITHUB_ACTIONS="$github" \
    bash "$script_dir/configsource-fault-check.sh" > "$fixture/out"
  [[ -s "$FAULT_TEST_CALLS" ]]
  if [[ "$want_race" == true ]]; then
    grep -qx -- '-race' "$FAULT_TEST_CALLS"
  elif grep -qx -- '-race' "$FAULT_TEST_CALLS"; then
    echo "FAIL: local host $host received race instrumentation" >&2
    exit 1
  fi
  grep -qx -- '-tags=configsource_faulttest' "$FAULT_TEST_CALLS"
  grep -qx -- './internal/infra/runtimehost' "$FAULT_TEST_CALLS"
  grep -qx -- './internal/infra/runtimebundle' "$FAULT_TEST_CALLS"
  grep -q 'all required subtests ran without skips' "$fixture/out"
}

check_mode agent-dev false false
check_mode ordinary-laptop false false
check_mode ci-runner true true
# Claiming CI must not re-enable race on a blocked development host.
check_mode agent-dev true false
check_mode DESKTOP-I2CAJ6V.example true false
printf 'development\n' > "$AIP_DEV_HOST_POLICY_FILE"
check_mode devbox-local true false

if FAULT_TEST_HOST=agent-dev HOSTNAME=agent-dev FAULT_TEST_SKIP=1 \
  bash "$script_dir/configsource-fault-check.sh" > "$fixture/out" 2>&1; then
  echo 'FAIL: skipped fault tests were accepted' >&2
  exit 1
fi
grep -q 'did not execute cleanly' "$fixture/out"
echo 'PASS: local fault checks omit race, GitHub CI retains race, skipped tests fail.'
