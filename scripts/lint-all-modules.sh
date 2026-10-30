#!/usr/bin/env bash
# Runs golangci-lint across all or scoped Go modules in parallel.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STAGED=0
CHANGED=0
BASE=""
DIRECT=0
ADVISORY=0
RATCHET=0
RATCHET_BASE=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --staged)
      STAGED=1
      shift
      ;;
    --changed)
      CHANGED=1
      shift
      ;;
    --base)
      BASE="${2:?--base needs a commit}"
      shift 2
      ;;
    --direct)
      DIRECT=1
      shift
      ;;
    --advisory)
      ADVISORY=1
      shift
      ;;
    --ratchet)
      RATCHET=1
      shift
      ;;
    --ratchet-base)
      RATCHET_BASE="${2:?--ratchet-base needs a commit}"
      shift 2
      ;;
    *)
      echo "usage: $0 [--staged|--changed|--base <commit>] [--direct] [--advisory] [--ratchet [--ratchet-base <commit>]]" >&2
      exit 2
      ;;
  esac
done

# This gate is mandatory: `make lint`, the staged pre-commit lint and CI all
# run it. Skipping when the analyzer is absent turns the gate into a silent
# no-op, and substituting staticcheck hides most of the analyzer set CI
# enforces. tools/devcheck applies the same rule for `make dev-lint`.
if ! command -v golangci-lint >/dev/null 2>&1; then
  {
    echo "ERROR: golangci-lint is required for the repository lint gate but is not on PATH."
    echo "Install the version pinned in .github/golangci-lint.version:"
    echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@<pinned-version>"
  } >&2
  exit 3
fi

if (( RATCHET )); then
  if (( ! ADVISORY )); then
    echo "ERROR: --ratchet requires --advisory" >&2
    exit 2
  fi
  # The comparison base may differ from the package scope: a staged local run
  # compares new lines against HEAD while still scoping to staged files.
  if [[ -z "$RATCHET_BASE" ]]; then
    RATCHET_BASE="$BASE"
  fi
  if [[ -z "$RATCHET_BASE" ]]; then
    echo "ERROR: --ratchet requires --ratchet-base <commit> (or --base)" >&2
    exit 2
  fi
fi

declare -A MODULE_SET=()

declare -A MODULE_PACKAGES=()
FULL=0
if (( STAGED || CHANGED )) || [[ -n "$BASE" ]]; then
  mode=changed
  if (( STAGED )); then mode=staged; fi
  if [[ -n "$BASE" ]]; then mode=base; fi
  plan="$(go -C "$ROOT" run -buildvcs=false ./tools/lintscope -mode "$mode" -base "$BASE" -direct="$(( DIRECT ))" -format=lines)"
  if [[ "$plan" == "FULL" ]]; then
    FULL=1
  elif [[ -n "$plan" ]]; then
    while IFS=$'\t' read -r module packages; do
      MODULE_SET["$module"]=1
      MODULE_PACKAGES["$module"]="$packages"
    done <<< "$plan"
  fi
else
  FULL=1
fi
if (( FULL )); then
  # Discover all modules
  MODULE_SET["."]=1
  MODULE_SET["testdata/enterprise_module"]=1
  MODULE_SET["testdata/external_billing_binding"]=1
  MODULE_SET["testdata/external_connector"]=1
  MODULE_SET["testdata/external_feature_sdk"]=1
  for base in connectors connector-support; do
    if [[ -d "$ROOT/$base" ]]; then
      for d in "$ROOT/$base"/*; do
        if [[ -d "$d" && -f "$d/go.mod" ]]; then
          rel="${base}/$(basename "$d")"
          MODULE_SET["$rel"]=1
        fi
      done
    fi
  done
fi

MODULES=("${!MODULE_SET[@]}")
if [[ ${#MODULES[@]} -eq 0 ]]; then
  exit 0
fi

# Each analyzer already schedules parallel work. Avoid cores x cores workers
# and competing whole-module loads; both platforms share this budget.
JOBS="${AIP_LINT_JOBS:-2}"
LINT_CONCURRENCY="${AIP_LINT_CONCURRENCY:-2}"
for budget in "$JOBS" "$LINT_CONCURRENCY"; do
  if [[ ! "$budget" =~ ^[1-9][0-9]*$ ]]; then
    echo "AIP_LINT_JOBS and AIP_LINT_CONCURRENCY must be positive integers" >&2
    exit 2
  fi
done
echo "Lint budget: modules=$JOBS analyzers/module=$LINT_CONCURRENCY"

run_module_lint() {
  local module="$1"
  local dir="$ROOT/$module"
  [[ -f "$dir/go.mod" ]] || return 0
  local packages
  read -r -a packages <<< "${2:-./...}"
  local trim_arch="${3:-false}"

  if [[ "$trim_arch" != true && "$STAGED" == 1 && "$DIRECT" == 1 && "$module" == . && "${AIP_LOCAL_ARCH_TRIMPATH:-}" == 1 ]]; then
    local broad_arch_scope=false
    local package
    local -a arch_packages=() other_packages=()
    for package in "${packages[@]}"; do
      if [[ "$package" == "./..." || "$package" == "./internal/..." ]]; then
        broad_arch_scope=true
        break
      fi
      case "$package" in
        ./internal/archtest|./internal/archtest/*)
          arch_packages+=("$package")
          ;;
        *)
          other_packages+=("$package")
          ;;
      esac
    done
    if [[ "$broad_arch_scope" == false && ${#arch_packages[@]} -gt 0 ]]; then
      if [[ "${packages[0]}" == ./internal/archtest || "${packages[0]}" == ./internal/archtest/* ]]; then
        if ! run_module_lint "$module" "${arch_packages[*]}" true; then
          return 1
        fi
        if [[ ${#other_packages[@]} -gt 0 ]] && ! run_module_lint "$module" "${other_packages[*]}"; then
          return 1
        fi
      else
        if [[ ${#other_packages[@]} -gt 0 ]] && ! run_module_lint "$module" "${other_packages[*]}"; then
          return 1
        fi
        if ! run_module_lint "$module" "${arch_packages[*]}" true; then
          return 1
        fi
      fi
      return 0
    fi
  fi

  echo "== Linting $module: ${packages[*]} =="
  local effective_go_flags=""
  if [[ "$trim_arch" == true ]]; then
    if ! effective_go_flags="$(cd "$dir" && go env GOFLAGS)"; then
      echo "ERROR: unable to read effective GOFLAGS for trimmed archtest lint" >&2
      return 1
    fi
    effective_go_flags="${effective_go_flags:+$effective_go_flags }-trimpath=true"
  fi
  local -a lint_args=(run --allow-parallel-runners --concurrency="$LINT_CONCURRENCY")
  if (( ADVISORY )); then
    # The advisory report is deliberately uncapped: max-same-issues=3 hid 12
    # of 15 findings in a previous delivery, so a capped report is not evidence.
    # Tagged files are part of the repository's authored Go surface and are
    # otherwise invisible to the default-tag advisory pass.
    lint_args+=(--build-tags=integration,precommit --max-issues-per-linter=0 --max-same-issues=0)
    if (( RATCHET )); then
      lint_args+=(--new-from-rev="$RATCHET_BASE")
    else
      lint_args+=(--issues-exit-code=0)
    fi
  else
    lint_args+=(--disable=modernize,paralleltest,thelper,gocognit)
  fi
  (
    cd "$dir" || exit 1
    if [[ "$trim_arch" == true ]]; then
      export GOFLAGS="$effective_go_flags"
    fi
    bash "$ROOT/scripts/go-dev-guard.sh" --aip-resource-run golangci-lint "${lint_args[@]}" "${packages[@]}"
  )
}

if (( ADVISORY )); then
  if (( RATCHET )); then
    echo "Mode: ADVISORY RATCHET (full set incl. modernize, paralleltest, thelper; integration+precommit tags; only findings on lines changed since $RATCHET_BASE; fails on new findings)."
  else
    echo "Mode: ADVISORY (full set incl. modernize, paralleltest, thelper; integration+precommit tags; uncapped; findings do not fail this report)."
  fi
else
  echo "Mode: MANDATORY correctness gate (--disable=modernize,paralleltest,thelper,gocognit); style and complexity debt via 'make lint-advisory'."
fi

export ROOT ADVISORY RATCHET BASE RATCHET_BASE LINT_CONCURRENCY STAGED DIRECT
export -f run_module_lint
for module in "${MODULES[@]}"; do
  printf '%s\0%s\0' "$module" "${MODULE_PACKAGES[$module]:-./...}"
done | xargs -0 -r -n2 -P"$JOBS" bash -c 'run_module_lint "$1" "$2"' _
if (( ADVISORY )); then
  if (( RATCHET )); then
    echo "OK: no advisory findings on lines changed since $RATCHET_BASE."
  else
    echo "OK: advisory report complete (uncapped; findings do not fail this report)."
  fi
else
  echo "OK: All checked Go modules passed linting."
fi
