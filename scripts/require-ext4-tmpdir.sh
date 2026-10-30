#!/usr/bin/env bash
# Single source of truth for the ext4 TMPDIR precondition.
#
# The config-source integrity tests assert atomic rename and inode-reuse
# behaviour. tmpfs does not provide those semantics, so on a host whose TMPDIR
# is tmpfs (a /tmp mount is the common case) they fail with
# `source-integrity-failed` or `source_non_atomic_update` no matter what the
# change under test actually did.
#
# Call this BEFORE the expensive suite so the cause is reported once, up front,
# instead of as unrelated-looking failures in six packages after many minutes.
# Exits 0 and prints nothing when the precondition holds.
set -euo pipefail

# The requirement is Linux-specific: only there does this repo gate on ext4
# storage for these tests.
if [[ "$(go env GOOS 2>/dev/null || true)" != "linux" ]]; then
	echo "ext4 TMPDIR preflight: not Linux, skipping." >&2
	exit 0
fi

fail() {
	echo "error: $1" >&2
	echo "" >&2
	echo "  The config-source integrity tests assert atomic rename and inode-reuse" >&2
	echo "  behaviour. tmpfs does not provide those semantics, so on a tmpfs TMPDIR" >&2
	echo "  they fail with 'source-integrity-failed' or 'source_non_atomic_update'" >&2
	echo "  regardless of what your change did. Those failures are environmental." >&2
	echo "" >&2
	echo "  Point TMPDIR at an ext4-backed directory, then re-run:" >&2
	echo "    export TMPDIR=/path/on/ext4" >&2
	echo "  Find one with:" >&2
	echo "    findmnt --noheadings --output FSTYPE --target /home" >&2
	exit 1
}

if [[ -z "${TMPDIR:-}" ]]; then
	fail "TMPDIR is unset, so it defaults to /tmp."
fi
if [[ "$TMPDIR" != /* ]]; then
	fail "TMPDIR must be an absolute path (got: $TMPDIR)."
fi
if [[ ! -d "$TMPDIR" ]]; then
	fail "TMPDIR is not an existing directory: $TMPDIR"
fi
if [[ ! -w "$TMPDIR" ]]; then
	fail "TMPDIR is not writable: $TMPDIR"
fi
if ! command -v findmnt >/dev/null 2>&1; then
	fail "findmnt is required to verify TMPDIR storage."
fi

tmpdir_fs="$(findmnt --noheadings --output FSTYPE --target "$TMPDIR" 2>/dev/null || true)"
if [[ "$tmpdir_fs" != "ext4" ]]; then
	fail "TMPDIR must be on ext4, but $TMPDIR is on ${tmpdir_fs:-unknown}."
fi