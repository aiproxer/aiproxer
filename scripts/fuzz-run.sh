#!/usr/bin/env bash
# fuzz-run.sh — run a single `go test -fuzz=...` invocation and tolerate the
# Go fuzz engine's known spurious "context deadline exceeded" failure that
# occurs when -fuzztime expires (golang/go#75804, Go 1.25-1.26.x).
#
# At the -fuzztime boundary the fuzz coordinator cancels in-flight iterations
# and may emit:
#     --- FAIL: FuzzX (Ns)
#         context deadline exceeded
# with NO `file:line` reference and NO "Failing input written to" corpus entry.
# That is the time budget expiring, not a real test failure.
#
# Real failures always include either a `..._test.go:<line>:` (or any
# `*.go:<line>:`) reference or a "Failing input written to" line; those are
# still surfaced as failures. Only the bare deadline message is tolerated.
#
# Usage: fuzz-run.sh <args passed to `go test -fuzz=...`>
# Env:  GO — go binary (default: go)

set -u

GO_BIN="${GO:-go}"

# Go interprets -fuzz as a regular expression. Repository fuzz targets are
# plain function identifiers, so anchor only that narrow form. Preserve
# caller-supplied regular expressions unchanged because this wrapper forwards
# general `go test` arguments as well.
normalized_args=()
for arg in "$@"; do
	if [[ "$arg" == -fuzz=* ]]; then
		pattern="${arg#-fuzz=}"
		if [[ "$pattern" =~ ^\^?[A-Za-z_][A-Za-z0-9_]*\$?$ ]]; then
			[[ "$pattern" == ^* ]] || pattern="^${pattern}"
			[[ "$pattern" == *'$' ]] || pattern="${pattern}$"
			arg="-fuzz=$pattern"
		fi
	fi
	normalized_args+=("$arg")
done

out="$(mktemp)"
trap 'rm -f "$out"' EXIT

# Note: no `set -e`/pipefail here — a nonzero `go test` exit is classified below.
"$GO_BIN" test "${normalized_args[@]}" >"$out" 2>&1
status=$?
cat "$out"

# Clean exit.
if [ "$status" -eq 0 ]; then
	exit 0
fi

# Real finding: a new failing corpus entry was written.
if grep -q 'Failing input written to' "$out"; then
	exit "$status"
fi

# Real assertion/panic failure: a source file:line reference is present.
if grep -qE '\.go:[0-9]+:' "$out"; then
	exit "$status"
fi

# Spurious engine deadline flake at the -fuzztime boundary — tolerate.
if grep -q 'context deadline exceeded' "$out"; then
	echo "fuzz-run: tolerated spurious Go fuzz engine 'context deadline exceeded' at -fuzztime boundary (golang/go#75804)" >&2
	exit 0
fi

# Any other non-zero exit is a real error (build failure, panic w/o location, etc.).
exit "$status"
