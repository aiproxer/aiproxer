#!/usr/bin/env bash
# Bounded CLI fixtures for dual-artifact feature-plane generation (Task 2).
# Scratch destinations only: repository defaults are read or checked, never
# overwritten with custom content. Fails fast on the first mismatch.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
MANIFEST="$REPO_ROOT/pkg/aipsdk/feature/plane_manifest.go"
SDK_DEFAULT="$REPO_ROOT/pkg/aipsdk/feature/plane_generated.go"
CORE_DEFAULT="$REPO_ROOT/internal/core/largebody/plane_catalog_generated.go"

TMP="$(mktemp -d "${TMPDIR:-/tmp}/generate-feature-planes-test.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

expect_pass() {
  local desc="$1"
  shift
  if ! "$@" >/dev/null 2>"$TMP/pass.err"; then
    cat "$TMP/pass.err" >&2
    fail "expected success: $desc ($*)"
  fi
  echo "ok: $desc"
}

expect_fail() {
  local desc="$1"
  shift
  if "$@" >/dev/null 2>"$TMP/fail.err"; then
    fail "expected failure but passed: $desc ($*)"
  fi
  echo "ok: $desc (rejected as expected)"
}

# 1. Default -check covers both artifacts and is read-only.
sdk_before="$(sha256sum "$SDK_DEFAULT" | awk '{print $1}')"
core_before="$(sha256sum "$CORE_DEFAULT" | awk '{print $1}')"
expect_pass "default -check covers both artifacts" \
  go run ./scripts/generate-feature-planes.go -check
[[ "$(sha256sum "$SDK_DEFAULT" | awk '{print $1}')" == "$sdk_before" ]] || fail "-check modified the SDK artifact"
[[ "$(sha256sum "$CORE_DEFAULT" | awk '{print $1}')" == "$core_before" ]] || fail "-check modified the core artifact"
echo "ok: default -check is read-only"

# 2. Custom two-output regeneration matches the repository defaults.
expect_pass "custom two-output generation" \
  go run ./scripts/generate-feature-planes.go \
    -manifest "$MANIFEST" -out "$TMP/sdk.go" -eligibility-out "$TMP/core.go"
cmp -s "$TMP/sdk.go" "$SDK_DEFAULT" || fail "custom SDK bytes differ from repository default"
cmp -s "$TMP/core.go" "$CORE_DEFAULT" || fail "custom core bytes differ from repository default"
echo "ok: custom two-output bytes match repository defaults"

# 3. Legacy explicit SDK output does not touch the repository core output.
expect_pass "legacy explicit -out stays SDK-only" \
  go run ./scripts/generate-feature-planes.go \
    -manifest "$MANIFEST" -out "$TMP/legacy.go"
cmp -s "$TMP/legacy.go" "$SDK_DEFAULT" || fail "legacy SDK bytes differ from repository default"
[[ "$(sha256sum "$CORE_DEFAULT" | awk '{print $1}')" == "$core_before" ]] || fail "legacy -out touched the repository core artifact"
[[ "$(sha256sum "$SDK_DEFAULT" | awk '{print $1}')" == "$sdk_before" ]] || fail "legacy -out touched the repository SDK artifact"
echo "ok: legacy explicit SDK output leaves repository artifacts unchanged"

# 4. Stale core is rejected; missing core is rejected (path-specific, read-only).
cp "$TMP/sdk.go" "$TMP/check-sdk.go"
cp "$TMP/core.go" "$TMP/check-core.go"
printf '\n// stale\n' >>"$TMP/check-core.go"
expect_fail "stale core -check" \
  go run ./scripts/generate-feature-planes.go -check \
    -manifest "$MANIFEST" -out "$TMP/check-sdk.go" -eligibility-out "$TMP/check-core.go"
rm "$TMP/check-core.go"
expect_fail "missing core -check" \
  go run ./scripts/generate-feature-planes.go -check \
    -manifest "$MANIFEST" -out "$TMP/check-sdk.go" -eligibility-out "$TMP/check-core.go"
# Stale SDK is rejected symmetrically.
cp "$TMP/sdk.go" "$TMP/check-sdk-stale.go"
cp "$TMP/core.go" "$TMP/check-core-fresh.go"
printf '\n// stale\n' >>"$TMP/check-sdk-stale.go"
expect_fail "stale SDK -check" \
  go run ./scripts/generate-feature-planes.go -check \
    -manifest "$MANIFEST" -out "$TMP/check-sdk-stale.go" -eligibility-out "$TMP/check-core-fresh.go"
echo "ok: stale/missing core and stale SDK fail read-only checks"

# 5. Invalid input installs neither target.
cat >"$TMP/invalid-manifest.go" <<'EOF'
package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"

var PlaneDupA = Plane[toolpolicy.Policy]{
	ID: "dup_plane", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, RequestAccess: RequestBodyCanonicalRequired,
	Combine: func(s SourceKind, c, in toolpolicy.Policy) (toolpolicy.Policy, error) { return in, nil },
}
var PlaneDupB = Plane[toolpolicy.Policy]{
	ID: "dup_plane", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, RequestAccess: RequestBodyMetadataOnly,
	Combine: func(s SourceKind, c, in toolpolicy.Policy) (toolpolicy.Policy, error) { return in, nil },
}

var StandardPlanes = []any{PlaneDupA, PlaneDupB}
EOF
printf 'sdk-sentinel\n' >"$TMP/invalid-sdk.go"
printf 'core-sentinel\n' >"$TMP/invalid-core.go"
expect_fail "invalid manifest generation" \
  go run ./scripts/generate-feature-planes.go \
    -manifest "$TMP/invalid-manifest.go" -out "$TMP/invalid-sdk.go" -eligibility-out "$TMP/invalid-core.go"
[[ "$(cat "$TMP/invalid-sdk.go")" == "sdk-sentinel" ]] || fail "invalid input modified the SDK target"
[[ "$(cat "$TMP/invalid-core.go")" == "core-sentinel" ]] || fail "invalid input modified the core target"
echo "ok: invalid input leaves both targets unchanged"

# 6. Installation failure exits nonzero (no multi-file transaction promised).
expect_fail "installation failure on missing directory" \
  go run ./scripts/generate-feature-planes.go \
    -manifest "$MANIFEST" -out "$TMP/install-sdk.go" -eligibility-out "$TMP/no-such-dir/core.go"
echo "ok: incomplete installation fails nonzero"

# 7. Custom manifest without explicit -out must not overwrite the repository
# SDK default: install mode rejects, repository defaults stay untouched.
cp "$MANIFEST" "$TMP/custom-manifest.go"
expect_fail "custom manifest with only -eligibility-out" \
  go run ./scripts/generate-feature-planes.go \
    -manifest "$TMP/custom-manifest.go" -eligibility-out "$TMP/custom-core-only.go"
[[ "$(sha256sum "$SDK_DEFAULT" | awk '{print $1}')" == "$sdk_before" ]] || fail "rejected custom-manifest install touched the repository SDK artifact"
[[ "$(sha256sum "$CORE_DEFAULT" | awk '{print $1}')" == "$core_before" ]] || fail "rejected custom-manifest install touched the repository core artifact"
echo "ok: custom manifest without explicit -out leaves repository artifacts unchanged"

# 8. Custom manifest with default invocation (both destinations implicit) is
# rejected in install mode; read-only -check stays allowed.
expect_fail "custom manifest with implicit defaults (install)" \
  go run ./scripts/generate-feature-planes.go \
    -manifest "$TMP/custom-manifest.go"
[[ "$(sha256sum "$SDK_DEFAULT" | awk '{print $1}')" == "$sdk_before" ]] || fail "rejected implicit-default install touched the repository SDK artifact"
[[ "$(sha256sum "$CORE_DEFAULT" | awk '{print $1}')" == "$core_before" ]] || fail "rejected implicit-default install touched the repository core artifact"
echo "ok: custom manifest with implicit defaults is rejected in install mode"

# 9. Identical -out and -eligibility-out destinations are rejected in both
# install and check modes, including alias spellings of one file.
expect_fail "same destination for both outputs (install)" \
  go run ./scripts/generate-feature-planes.go \
    -manifest "$MANIFEST" -out "$TMP/same.go" -eligibility-out "$TMP/same.go"
[[ "$(sha256sum "$SDK_DEFAULT" | awk '{print $1}')" == "$sdk_before" ]] || fail "rejected same-destination install touched the repository SDK artifact"
[[ "$(sha256sum "$CORE_DEFAULT" | awk '{print $1}')" == "$core_before" ]] || fail "rejected same-destination install touched the repository core artifact"
expect_fail "alias same destination for both outputs (install)" \
  go run ./scripts/generate-feature-planes.go \
    -manifest "$MANIFEST" -out "$TMP/same.go" -eligibility-out "$TMP/./same.go"
cp "$TMP/sdk.go" "$TMP/same-check.go"
expect_fail "same destination for both outputs (check)" \
  go run ./scripts/generate-feature-planes.go -check \
    -manifest "$MANIFEST" -out "$TMP/same-check.go" -eligibility-out "$TMP/same-check.go"
[[ "$(sha256sum "$SDK_DEFAULT" | awk '{print $1}')" == "$sdk_before" ]] || fail "rejected same-destination check touched the repository SDK artifact"
[[ "$(sha256sum "$CORE_DEFAULT" | awk '{print $1}')" == "$core_before" ]] || fail "rejected same-destination check touched the repository core artifact"
echo "ok: shared -out/-eligibility-out destination is rejected"

echo 'OK: dual-artifact generator CLI fixtures'
