#!/usr/bin/env bash
set -euo pipefail

run() {
  local started=$SECONDS
  printf '\n==> '
  printf '%q ' "$@"
  printf '\n'
  if "$@"; then
    printf '<== PASS (%ss): ' "$((SECONDS - started))"
    printf '%q ' "$@"
    printf '\n'
  else
    local status=$?
    printf '<== FAIL (%ss, exit %s): ' "$((SECONDS - started))" "$status" >&2
    printf '%q ' "$@" >&2
    printf '\n' >&2
    return "$status"
  fi
}

run go test ./enterprise/internal/core/billing ./internal/core/runtime ./internal/core/usageauthority/... ./enterprise/internal/infra/billingstore ./enterprise/internal/infra/billingspool ./enterprise/internal/infra/billingcompose ./enterprise/internal/infra/billingadmission ./internal/infra/runtimebundle ./internal/archtest ./enterprise/internal/infra/usageauthority/... ./enterprise/internal/archtest -run 'Billing|UsageAuthority|Usage|Spool|Compose|Admission|Runtime|Migration|Outbox|Wiring|Construction|Deletion|LOC|Symbol' -count=1
run go test ./enterprise/internal/core/billing ./internal/core/runtime ./enterprise/internal/infra/billingstore ./internal/infra/runtimebundle -count=1

if [[ "${AIP_REQUIRE_POSTGRES:-0}" == "1" ]]; then
  postgres_dsn="${AIP_TEST_POSTGRES_DSN:-${AIP_TEST_POSTGRES_ADMIN_DSN:-}}"
  if [[ -z "$postgres_dsn" ]]; then
    echo "FAIL: AIP_REQUIRE_POSTGRES=1 but no PostgreSQL DSN is configured" >&2
    exit 1
  fi
  AIP_REQUIRE_POSTGRES=1 AIP_TEST_POSTGRES_DSN="$postgres_dsn" run go test -tags=integration ./enterprise/internal/infra/billingstore -run 'Postgres' -count=1
else
  echo "SKIP: PostgreSQL billingstore integration (set AIP_REQUIRE_POSTGRES=1 to require it)"
fi

run make quality-checks
# This is the repository-wide unit gate. Do not also invoke make test-unit:
# that target runs the same ./... graph and would execute the full suite twice.
run go test -parallel=8 -timeout=10m ./... -count=1
run make docs-check

case "$(go env GOOS)" in
  windows)
    echo "SKIP: race certification is unavailable on Windows in this repository"
    ;;
  *)
    run make test-race
    ;;
esac

run git diff --check
changed_go_files_list="$( { git diff --name-only --diff-filter=ACM -- '*.go'; git ls-files --others --exclude-standard -- '*.go'; } | sort -u )"
if [[ -n "$changed_go_files_list" ]] && [[ -n "$(gofmt -l $changed_go_files_list)" ]]; then
  echo "FAIL: changed Go files are not gofmt-clean" >&2
  exit 1
fi
changed_go_files="$(printf '%s\n' "$changed_go_files_list" | sed '/^$/d' | wc -l | tr -d ' ')"
changed_files="$( { git diff --name-only; git ls-files --others --exclude-standard; } | sort -u | wc -l | tr -d ' ')"
if (( changed_go_files > 100 || changed_files > 100 )); then
  echo "FAIL: changed files exceed the 100-file checkpoint limit (Go=$changed_go_files total=$changed_files)" >&2
  exit 1
fi

echo "PASS: billing convergence certification completed; PostgreSQL/race status was reported explicitly"
