#!/usr/bin/env bash
# fuzz-smoke.sh — concurrent Tier-1 fuzz smoke over the canonical target list
# in scripts/fuzz-targets.tsv (shared with the Windows runner).
#
# Replaces the sequential per-line Makefile loop: every `go test -fuzz`
# invocation pays a fixed link/spawn cost (~1.3s measured) before its fuzz
# budget even starts, so short smoke budgets ran mostly on overhead.
#
# Env:
#   FUZZTIME        per-target budget passed to go test -fuzztime (default 500ms)
#   AIP_FUZZ_JOBS   concurrent fuzz processes (default: cores/2 clamped to 2..8)
#
# Each target still routes through scripts/fuzz-run.sh so the spurious
# golang/go#75804 deadline flake at -fuzztime expiry stays tolerated.
# Failure semantics differ from the old sequential loop on purpose: all
# targets now run and every failure is reported before exiting non-zero.

set -u

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/.." && pwd)
targets_file="$script_dir/fuzz-targets.tsv"
fuzztime="${FUZZTIME:-500ms}"

if [ ! -f "$targets_file" ]; then
	echo "ERROR: fuzz target list not found: $targets_file" >&2
	exit 1
fi

cores=$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4)
jobs_allowed=$((cores / 2))
[ "$jobs_allowed" -lt 2 ] && jobs_allowed=2
[ "$jobs_allowed" -gt 8 ] && jobs_allowed=8
case "${AIP_FUZZ_JOBS:-}" in
	'' | *[!0-9]*) ;;
	*) if [ "$AIP_FUZZ_JOBS" -ge 1 ]; then jobs_allowed=$AIP_FUZZ_JOBS; fi ;;
esac

# Workers inside each `go test -fuzz` process. The pool already occupies the
# machine with several targets, so each one gets a small worker slice instead
# of the GOMAXPROCS default.
worker_parallel=2

run_tmp=$(mktemp -d "${TMPDIR:-/tmp}/aip-fuzz-smoke.XXXXXX")
cleanup() { rm -rf "$run_tmp"; }
trap cleanup EXIT

echo "Fuzz smoke (FUZZTIME=$fuzztime): $jobs_allowed concurrent targets x $worker_parallel workers each"

index=0
while IFS=$'\t' read -r name package module; do
	# Tolerate editors saving the TSV with CRLF line endings.
	module=${module%$'\r'}
	case "$name" in '' | '#'*) continue ;; esac
	index=$((index + 1))
	cwd="$repo_root"
	module_env=()
	if [ -n "$module" ]; then
		cwd="$repo_root/$module"
		module_env=(GOWORK=off)
	fi
	log_file="$run_tmp/$index.log"
	status_file="$run_tmp/$index.status"
	printf '%s\n' "$name" >"$run_tmp/$index.name"
	{
		(
			cd "$cwd" || exit 99
			env "${module_env[@]}" bash "$script_dir/fuzz-run.sh" \
				-fuzz="^${name}\$" \
				-fuzztime="$fuzztime" \
				-run='^$' \
				-parallel="$worker_parallel" \
				"$package"
		)
		echo $? >"$status_file"
	} >"$log_file" 2>&1 &
	echo "  [$index] $name ($package${module:+ @ $module})"
	while [ "$(jobs -rp | wc -l)" -ge "$jobs_allowed" ]; do
		wait -n || true
	done
done < <(tail -n +2 "$targets_file")

wait || true

echo ""
status=0
failures=0
for i in $(seq 1 "$index"); do
	name=$(cat "$run_tmp/$i.name")
	target_status=$(cat "$run_tmp/$i.status" 2>/dev/null || printf 'missing')
	if [ "$target_status" != "0" ]; then
		status=1
		failures=$((failures + 1))
		echo "--- FAIL $name (exit ${target_status}) ---"
		[ -f "$run_tmp/$i.log" ] && cat "$run_tmp/$i.log"
	else
		echo "PASS $name"
	fi
done

echo ""
if [ "$status" -ne 0 ]; then
	echo "Fuzz smoke FAILED ($failures of $index targets)"
else
	echo "Fuzz smoke passed ($index targets)"
fi
exit "$status"
