#!/usr/bin/env bash
# verify-product.sh - POSIX outer supervisor for `make verify-product`.
#
# The 600-second command budget must include the `go run` compilation, and the
# supervisor must be able to terminate every process the controller starts even
# when the controller is unresponsive. taskrunner deliberately starts each
# child in its own process group, so a process-group signal from `timeout`
# would miss those groups. This supervisor starts the whole command in a new
# session and, on timeout, kills every process in that session.
#
# Usage: verify-product.sh <timeout-seconds> <evidence-dir> <command...>
# AIP_PRODUCT_SMOKE_BOOTSTRAP_SECONDS bounds the session-identity handshake
# (default 30). If the handshake does not complete while the command is still
# running, the supervisor terminates the command tree rather than supervising
# without a session identity.
set -euo pipefail

if [ "$#" -lt 3 ]; then
	echo "verify-product: usage: verify-product.sh <timeout-seconds> <evidence-dir> <command...>" >&2
	exit 2
fi
timeout_seconds=$1
evidence=$2
shift 2
for value in "$timeout_seconds" "${AIP_PRODUCT_SMOKE_BOOTSTRAP_SECONDS:-30}"; do
	case "$value" in
	'' | *[!0-9]*)
		echo "verify-product: timeout values must be whole seconds, got '$value'" >&2
		exit 2
		;;
	esac
done
if [ "$#" -eq 0 ]; then
	echo "verify-product: missing command" >&2
	exit 2
fi
for tool in setsid pkill pgrep ps realpath; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "verify-product: $tool is required by the POSIX session supervisor" >&2
		exit 2
	fi
done

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

deadline=$(( $(date +%s) + timeout_seconds ))
export AIP_PRODUCT_SMOKE_DEADLINE_UNIX=$deadline

# Invalidate a previous verdict before compilation can consume the budget. The
# controller clears it too, but it cannot do so if the toolchain never starts
# it. Never modify a path that resolves inside the source tree: the controller
# rejects that destination, and deleting a repository file first would be a
# side effect on an invalid input.
if [ -n "$evidence" ]; then
	root_real=$(realpath -e -- "$root" 2>/dev/null || true)
	evidence_real=$(realpath -m -- "$evidence" 2>/dev/null || true)
	if [ -n "$root_real" ] && [ -n "$evidence_real" ]; then
		case "$evidence_real" in
		"$root_real" | "$root_real"/*)
			echo "verify-product: evidence directory is inside the source tree; leaving any existing summary untouched" >&2
			;;
		*)
			rm -f -- "$evidence_real/summary.json"
			;;
		esac
	fi
fi

sidfile=$(mktemp "${TMPDIR:-/tmp}/product-smoke-sid.XXXXXX")
supervisor_pid=
sid=
own_sid=
kill_tree() {
	local parent=$1 child
	for child in $(pgrep -P "$parent" 2>/dev/null || true); do
		kill_tree "$child"
	done
	kill -KILL "$parent" 2>/dev/null || true
}
# kill_supervised_tree covers the handshake-failure path, where the session id
# is unknown. It kills the known parent/child sessions that differ from this
# script's session, then the remaining parent/child tree.
kill_supervised_tree() {
	local root=$1 pid session
	for pid in "$root" $(pgrep -P "$root" 2>/dev/null || true); do
		session=$(ps -o sid= -p "$pid" 2>/dev/null | tr -d ' ' || true)
		if [ -n "$session" ] && [ -n "$own_sid" ] && [ "$session" != "$own_sid" ]; then
			pkill -KILL -s "$session" 2>/dev/null || true
		fi
	done
	kill_tree "$root"
}
cleanup() {
	if [ -n "${sid:-}" ]; then
		pkill -KILL -s "$sid" 2>/dev/null || true
	fi
	if [ -n "${supervisor_pid:-}" ]; then
		kill_supervised_tree "$supervisor_pid"
	fi
	rm -f "$sidfile"
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

own_sid=$(ps -o sid= -p $$ 2>/dev/null | tr -d ' ' || true)
# setsid --wait keeps the wrapper as the script's child and propagates the
# command's exit status. The inner shell records the session leader pid before
# exec, so the supervisor never has to guess which pid owns the new session.
setsid --wait bash -c 'echo $$ > "$1"; shift; exec "$@"' _ "$sidfile" "$@" &
supervisor_pid=$!

bootstrap_seconds=${AIP_PRODUCT_SMOKE_BOOTSTRAP_SECONDS:-30}
bootstrap_deadline=$(( $(date +%s) + bootstrap_seconds ))
if [ "$bootstrap_deadline" -gt "$deadline" ]; then
	bootstrap_deadline=$deadline
fi
while [ "$(date +%s)" -lt "$bootstrap_deadline" ]; do
	if [ -s "$sidfile" ]; then
		break
	fi
	if ! kill -0 "$supervisor_pid" 2>/dev/null; then
		break
	fi
	sleep 0.01
done
if [ -s "$sidfile" ]; then
	sid=$(cat "$sidfile")
	actual_sid=$(ps -o sid= -p "$sid" 2>/dev/null | tr -d ' ' || true)
	# An empty actual_sid means the leader already exited; the wait below
	# reports its status. A different sid means setsid did not isolate it.
	if [ -n "$actual_sid" ] && [ "$actual_sid" != "$sid" ]; then
		# Do not run cleanup against a session id that is not isolated: it
		# could be this script's own session.
		bad_sid=$sid
		sid=
		kill_supervised_tree "$supervisor_pid"
		echo "verify-product: failed to create an isolated session for pid $bad_sid (sid=${actual_sid:-unknown})" >&2
		exit 2
	fi
fi
if [ -z "$sid" ]; then
	# A command that exited before publishing its identity is ordinary
	# completion. A command still running without an identity cannot be
	# supervised safely; terminate it rather than lose ownership.
	if kill -0 "$supervisor_pid" 2>/dev/null; then
		kill_supervised_tree "$supervisor_pid"
		echo "verify-product: could not acquire the supervised session id within ${bootstrap_seconds}s; terminated the command" >&2
		if [ "$(date +%s)" -ge "$deadline" ]; then
			exit 124
		fi
		exit 2
	fi
	status=0
	wait "$supervisor_pid" || status=$?
	exit "$status"
fi

while kill -0 "$supervisor_pid" 2>/dev/null; do
	if [ "$(date +%s)" -ge "$deadline" ]; then
		cleanup
		echo "verify-product: hard timeout of ${timeout_seconds}s expired; killed session ${sid:-unknown}" >&2
		exit 124
	fi
	sleep 0.05
done
status=0
wait "$supervisor_pid" || status=$?
exit "$status"
