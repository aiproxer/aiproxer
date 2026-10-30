#!/usr/bin/env bash
# test-openresponses-compliance.sh - OpenResponses full-path compliance suite (Task 8.5).
#
# Proves the ACTUAL pinned official OpenResponses compliance suite on the full
# independent deployment (official JS client -> OpenResponses frontend -> core ->
# generic OpenResponses backend -> independent refbackend origin), separately
# from the Go-native mirrors, plus the direct independent-emulator wire suites,
# the 45-cell FE*BE conformance matrix (Task 8.5), and the emulator boundary
# architecture gates.
#
# With `-static` it runs only the fast Task 8.5 wiring/evidence gate used by
# `make qa`: it verifies the compliance scripts, the Makefile target wiring, and
# the docs reference exist, then runs the default-build evidence validators and
# emulator boundary gates. It deliberately does NOT re-run the huge tagged
# conformance/integration suites (which `make qa`'s qa-tests already covers) and
# does NOT require a JavaScript runtime, so the release gate is wired into qa
# without recursively duplicating test work.
#
# Usage: bash scripts/test-openresponses-compliance.sh [-static]
# Env:   GO          - go binary (default "go")
#        GO_TEST_FLAGS - extra go test flags (default "-parallel=8 -timeout=10m")
set -euo pipefail

GO="${GO:-go}"
GO_TEST_FLAGS="${GO_TEST_FLAGS:--parallel=8 -timeout=10m}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPLIANCE_TOOL="$ROOT/tools/openresponses-compliance"

# prepare_official_compliance_tooling ensures Node and the pinned tool
# dependencies are available before the ACTUAL official suite runs. `npm ci` is
# a SETUP step (exact versions + integrity from package-lock.json); no test run
# downloads.
prepare_official_compliance_tooling() {
  if ! command -v node >/dev/null 2>&1; then
    echo "openresponses-compliance: official suite requires Node.js >= 20 in PATH (npm ci in $COMPLIANCE_TOOL is setup)" >&2
    exit 1
  fi
  if [[ ! -d "$COMPLIANCE_TOOL/node_modules" ]]; then
    echo "openresponses-compliance: installing pinned tool dependencies (setup, npm ci)..."
    (cd "$COMPLIANCE_TOOL" && npm ci --no-audit --no-fund)
  fi
}

# invoke_official_compliance_suite runs the ACTUAL pinned official suite through
# the Go harness, which deploys the full independent path and invokes the
# vendored runner. The harness FAILS (never silently skips) when the suite is
# requested but tooling is missing, and fails when any official case fails or is
# skipped.
invoke_official_compliance_suite() {
  prepare_official_compliance_tooling
  AIP_RUN_OFFICIAL_COMPLIANCE=1 \
    "$GO" test $GO_TEST_FLAGS -run 'TestOfficialComplianceSuite_FullDeployment' -timeout 15m ./internal/integration/openresponses/
}

STATIC=0
if [[ "${1:-}" == "-static" ]]; then
  STATIC=1
fi

if [[ "$STATIC" == "1" ]]; then
  if [[ ! -f "$ROOT/scripts/test-openresponses-compliance.ps1" ]] || [[ ! -f "$ROOT/scripts/test-openresponses-compliance.sh" ]]; then
    echo "openresponses-compliance-static: compliance scripts missing" >&2
    exit 1
  fi
  if ! grep -q '^test-openresponses-compliance:' "$ROOT/Makefile" || ! grep -q '^test-openresponses-compliance-static:' "$ROOT/Makefile"; then
    echo "openresponses-compliance-static: Makefile compliance targets missing" >&2
    exit 1
  fi
  if ! grep -Eq '^qa:.*test-openresponses-compliance-static' "$ROOT/Makefile"; then
    echo "openresponses-compliance-static: Makefile qa does not wire the compliance gate" >&2
    exit 1
  fi
  if ! grep -q 'test-openresponses-compliance' "$ROOT/docs/release-gates.md"; then
    echo "openresponses-compliance-static: docs do not reference the compliance gate" >&2
    exit 1
  fi
  # Fast Task 8.5 evidence validators (default build; the tagged matrix loops are
  # run by make qa's qa-tests and by the full standalone compliance suite).
  "$GO" test $GO_TEST_FLAGS ./internal/testkit/conformance/
  "$GO" test $GO_TEST_FLAGS ./internal/archtest/ -run 'OpenResponses|EmulatorBoundary'
  echo "openresponses-compliance-static: Task 8.5 wiring and evidence verified"
  exit 0
fi

# 1. Authoritative 5x9 = 45-cell matrix, OpenResponses frontend row, and
#    OpenResponses backend column (Task 8.5 evidence).
"$GO" test $GO_TEST_FLAGS -tags=precommit,integration ./internal/testkit/conformance/...

# 2. Independent OpenResponses refclient <-> refbackend direct wire suites
#    (official fixtures, adversarial streams, required presence).
"$GO" test $GO_TEST_FLAGS ./internal/refclient/openresponses/... ./internal/refbackend/openresponses/...

# 3. Production OpenResponses frontend + generic backend adapter suites.
"$GO" test $GO_TEST_FLAGS ./internal/plugins/frontends/openresponses/... ./internal/plugins/backends/openresponsescompat/...

# 4. Full black-box deployment harness (client -> frontend -> core -> backend ->
#    independent provider origin) across JSON/SSE/compact/WebSocket.
"$GO" test $GO_TEST_FLAGS ./internal/integration/openresponses/...

# 5. OpenResponses profile/source/license pinning and emulator boundary gates.
"$GO" test $GO_TEST_FLAGS ./internal/archtest/... -run 'OpenResponses|EmulatorBoundary'
"$GO" test $GO_TEST_FLAGS ./internal/plugins/protocols/openresponses/... -run 'Profile|Source|License|Manifest'

# 6. ACTUAL pinned official compliance suite on the full independent deployment
#    (separate from the Go-native mirrors above). FAILS when any official case
#    fails or is skipped, or when Node/tooling is unavailable.
invoke_official_compliance_suite

echo "openresponses-compliance: all suites passed"
