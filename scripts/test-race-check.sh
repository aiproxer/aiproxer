#!/usr/bin/env bash
# Exercise race scan coverage and failure propagation without running Go tests.
set -euo pipefail
script_dir="$(cd "$(dirname "$0")" && pwd)"
bash "$script_dir/test-go-dev-guard.sh"
# Bypass the development-host guard in scripts/race-check.sh: this self-test
# exercises the scan logic with a stubbed toolchain and must run identically
# on every host, including blocked dev machines.
export AIP_ALLOW_RACE_ON_DEV=1
fixture="$(mktemp -d)"
export AIP_DEV_HOST_POLICY_FILE="$fixture/host-policy"
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/bin"
cat > "$fixture/bin/go" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  'env CGO_ENABLED') echo 1; exit 0 ;;
  'env CC') echo bash; exit 0 ;;
  'list ./...')
    [[ -n "${LIST_STDERR:-}" ]] && echo 'go: downloading golang.org/x/sys v0.49.0' >&2
    printf '%s\n' example/enterprise/internal/core/billing example/enterprise/internal/infra/billingstore example/internal/core/runtime example/internal/archtest example/pkg/aipapi
    exit 0 ;;
esac
[[ " $* " == *' -c '* ]] && exit 0
printf '%s\n' "$*" >> "$SCAN_CALLS"
[[ -n "${FAIL_MATCH:-}" && " $* " == *"$FAIL_MATCH"* ]] && exit 17
exit 0
STUB
chmod +x "$fixture/bin/go"
export PATH="$fixture/bin:$PATH"
export SCAN_CALLS="$fixture/calls"
cd "$fixture"

verify_coverage() {
  local calls
  calls="$(cat "$SCAN_CALLS")"
  [[ "$(wc -l < "$SCAN_CALLS")" -eq 6 ]]
  for package in example/pkg/aipapi ./enterprise/internal/infra/billingstore ./internal/core/runtime ./internal/archtest/...; do
    [[ "$(grep -F -- " $package" "$SCAN_CALLS" | wc -l)" -eq 1 ]]
  done
  [[ "$(grep -F -- ' ./enterprise/internal/core/billing' "$SCAN_CALLS" | wc -l)" -eq 2 ]]
  [[ "$(grep -F -- ' -skip ^TestSupportAgreementShadowPredicate$' "$SCAN_CALLS" | wc -l)" -eq 1 ]]
  [[ "$(grep -F -- ' -run ^TestSupportAgreementShadowPredicate$' "$SCAN_CALLS" | wc -l)" -eq 1 ]]
  [[ "$(grep -F -- ' -timeout=60m -skip ^TestSupportAgreementShadowPredicate$' "$SCAN_CALLS" | wc -l)" -eq 1 ]]
  [[ "$(grep -F -- ' -timeout=60m ./enterprise/internal/infra/billingstore' "$SCAN_CALLS" | wc -l)" -eq 1 ]]
  while IFS= read -r call; do
    [[ " $call " == *' -race '* && " $call " == *' -tags=precommit,integration '* && " $call " == *' -count=1 '* ]]
    [[ " $call " == *' -p=4 '* && " $call " == *' -parallel=4 '* ]]
  done <<< "$calls"
}

bash "$script_dir/race-check.sh" --strict > "$fixture/success.log" 2>&1
verify_coverage
cp "$SCAN_CALLS" "$fixture/all-calls"
for match in ' example/pkg/aipapi ' ' -skip ^TestSupportAgreementShadowPredicate$ ' ' -run ^TestSupportAgreementShadowPredicate$ ' ' ./enterprise/internal/infra/billingstore ' ' ./internal/core/runtime ' ' ./internal/archtest/... '; do
  : > "$SCAN_CALLS"
  export FAIL_MATCH="$match"
  status=0
  bash "$script_dir/race-check.sh" --strict > "$fixture/failure.log" 2>&1 || status=$?
  [[ "$status" -eq 17 ]]
  verify_coverage
done
unset FAIL_MATCH
# A cold module cache makes `go list` print download progress on stderr. That
# notice is not a package: when it was merged into the package list it reached
# `go test` as an argument and reddened the whole lane with
# `malformed import path "go: downloading ...": invalid char ':'`
# (race-fuzz broad, run 38100651810).
export LIST_STDERR=1
: > "$SCAN_CALLS"
bash "$script_dir/race-check.sh" --strict > "$fixture/list-stderr.log" 2>&1
if grep -Fq 'go: downloading' "$SCAN_CALLS"; then
  echo 'go list stderr (module download notice) leaked into the race scan package set' >&2
  exit 1
fi
verify_coverage
unset LIST_STDERR
# The development-host guard skips the race scan while remote CI can own the
# race lane. When remote CI cannot run, the host records that state and the
# lane executes here, one package at a time because the host is still shared;
# skipping in that state would leave the lane unproven.
printf 'development' > "$AIP_DEV_HOST_POLICY_FILE"
: > "$SCAN_CALLS"
unset AIP_ALLOW_RACE_ON_DEV
bash "$script_dir/race-check.sh" --strict > "$fixture/guard-skip.log" 2>&1
grep -Fq 'race detector scan is disabled on development host' "$fixture/guard-skip.log"
[[ ! -s "$SCAN_CALLS" ]]
printf 'ci-stand-in' > "$AIP_DEV_HOST_POLICY_FILE"
: > "$SCAN_CALLS"
bash "$script_dir/race-check.sh" --strict > "$fixture/ci-stand-in.log" 2>&1
grep -Fq 'remote CI race lanes are unavailable' "$fixture/ci-stand-in.log"
grep -Fq 'back to development' "$fixture/ci-stand-in.log"
[[ -s "$SCAN_CALLS" ]]
while IFS= read -r call; do
  [[ " $call " == *' -race '* && " $call " == *' -p=1 '* && " $call " == *' -parallel=2 '* ]]
done < "$SCAN_CALLS"
# A stand-in host with spare capacity raises the parallelism explicitly.
: > "$SCAN_CALLS"
AIP_RACE_P=4 AIP_RACE_PARALLEL=8 bash "$script_dir/race-check.sh" --strict > "$fixture/ci-stand-in-wide.log" 2>&1
[[ -s "$SCAN_CALLS" ]]
while IFS= read -r call; do
  [[ " $call " == *' -p=4 '* && " $call " == *' -parallel=8 '* ]]
done < "$SCAN_CALLS"
: > "$SCAN_CALLS"
status=0
AIP_RACE_P=0 bash "$script_dir/race-check.sh" --strict > "$fixture/ci-stand-in-invalid.log" 2>&1 || status=$?
[[ $status -eq 2 && ! -s "$SCAN_CALLS" ]]
grep -Fq 'Invalid AIP_RACE_P/AIP_RACE_PARALLEL' "$fixture/ci-stand-in-invalid.log"
rm -f "$AIP_DEV_HOST_POLICY_FILE"
export AIP_ALLOW_RACE_ON_DEV=1
# The overrides belong to stand-in hosts only; the remote lane values stay fixed.
: > "$SCAN_CALLS"
AIP_RACE_P=1 AIP_RACE_PARALLEL=1 bash "$script_dir/race-check.sh" --strict > "$fixture/override-ignored.log" 2>&1
verify_coverage
# Every workflow that partitions the strict race scan must select the complete
# lane partition. The nightly selects different JSON arrays per schedule;
# release.yml and backend-plugin-release-gates.yml each declare one lane array.
# Validate each workflow's union on its own: sharing one union across files
# would hide a lane dropped from one workflow while another still runs it, the
# way the billing-store lane disappeared from both release matrices in #856.
race_workflows=(
  .github/workflows/race-fuzz-nightly.yml
  .github/workflows/release.yml
  .github/workflows/backend-plugin-release-gates.yml
)
for race_workflow in "${race_workflows[@]}"; do
  mapfile -t workflow_matrices < <(
    sed -n 's/^[[:space:]]*lane: //p' "$script_dir/../$race_workflow" |
      grep -oE '\[[^]]*\]'
  )
  if (( ${#workflow_matrices[@]} == 0 )); then
    echo "$race_workflow contains no recognizable race lanes" >&2
    exit 1
  fi
  : > "$fixture/workflow-lanes"
  for matrix in "${workflow_matrices[@]}"; do
    lanes="$(printf '%s\n' "$matrix" | tr -d '\[\]" ' | tr ',' '\n')"
    if [[ -z "$lanes" || "$(printf '%s\n' "$lanes" | sort)" != "$(printf '%s\n' "$lanes" | sort -u)" ]]; then
      echo "$race_workflow matrix contains empty or duplicate lanes: $matrix" >&2
      exit 1
    fi
    printf '%s\n' "$lanes" >> "$fixture/workflow-lanes"
  done
  mapfile -t workflow_lanes < <(sort -u "$fixture/workflow-lanes")
  : > "$fixture/lane-calls"
  for lane in "${workflow_lanes[@]}"; do
    : > "$SCAN_CALLS"
    bash "$script_dir/race-check.sh" --strict --lane "$lane" > "$fixture/lane.log" 2>&1
    [[ "$(wc -l < "$SCAN_CALLS")" -eq 1 ]]
    grep -Fx -- "$(cat "$SCAN_CALLS")" "$fixture/all-calls" >/dev/null
    cat "$SCAN_CALLS" >> "$fixture/lane-calls"
  done
  sort "$fixture/all-calls" > "$fixture/all-sorted"
  sort "$fixture/lane-calls" > "$fixture/lanes-sorted"
  if ! diff "$fixture/all-sorted" "$fixture/lanes-sorted"; then
    echo "$race_workflow race lane partition does not cover the full strict scan" >&2
    exit 1
  fi
done
if bash "$script_dir/race-check.sh" --strict --lane invalid > "$fixture/invalid.log" 2>&1; then
  echo 'Invalid lane unexpectedly succeeded' >&2
  exit 1
fi
echo 'Race scan coverage and failure propagation passed.'
