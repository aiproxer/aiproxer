#!/usr/bin/env bash
# Proves require-ext4-tmpdir.sh accepts a usable ext4 TMPDIR and rejects every
# shape that makes the config-source integrity suites report misleading failures.
# Only a fake toolchain is used: the real filesystem is irrelevant because the
# storage decision is delegated to a stubbed findmnt, and GOOS to a stubbed go.
set -euo pipefail
script_dir=$(cd "$(dirname "$0")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/bin"
cat > "$fixture/bin/go" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "${FAKE_GOOS:-linux}"
STUB
cat > "$fixture/bin/findmnt" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$FAKE_FSTYPE"
STUB
chmod +x "$fixture/bin/go" "$fixture/bin/findmnt"
export PATH="$fixture/bin:$PATH"

fails=0

# run_guard <GOOS> <fstype> <TMPDIR|__unset__> -> sets `status` and `out`
run_guard() {
	local goos="$1" fs="$2" tmpdir="$3"
	if [[ "$tmpdir" == "__unset__" ]]; then
		out="$(env -u TMPDIR FAKE_GOOS="$goos" FAKE_FSTYPE="$fs" \
			bash "$script_dir/require-ext4-tmpdir.sh" 2>&1)" && status=0 || status=$?
	else
		out="$(FAKE_GOOS="$goos" FAKE_FSTYPE="$fs" TMPDIR="$tmpdir" \
			bash "$script_dir/require-ext4-tmpdir.sh" 2>&1)" && status=0 || status=$?
	fi
}

# expect_reject <label> <cause-substring> [GOOS] [fstype] [TMPDIR]
# Defaults use ${v-default}, never ${v:-default}: ":-" also substitutes for an
# EMPTY string, which would silently retest ext4 whenever a case means to
# simulate findmnt reporting nothing.
expect_reject() {
	local label="$1" want="$2" goos="${3-linux}" fs="${4-ext4}" tmpdir="${5-$fixture}"
	run_guard "$goos" "$fs" "$tmpdir"
	if [[ "$status" -eq 0 ]]; then
		echo "FAIL: $label was accepted (GOOS=$goos fstype=${fs:-<empty>} TMPDIR=$tmpdir)" >&2
		fails=$((fails + 1))
	elif ! grep -q -- "$want" <<< "$out"; then
		echo "FAIL: $label rejected without naming the cause; wanted '$want', got:" >&2
		echo "$out" >&2
		fails=$((fails + 1))
	else
		echo "ok: $label rejected, naming the cause"
	fi
}

# expect_accept <label> [GOOS] [fstype] [TMPDIR]
expect_accept() {
	local label="$1" goos="${2-linux}" fs="${3-ext4}" tmpdir="${4-$fixture}"
	run_guard "$goos" "$fs" "$tmpdir"
	if [[ "$status" -eq 0 ]]; then
		echo "ok: $label accepted"
	else
		echo "FAIL: $label was rejected (GOOS=$goos fstype=${fs:-<empty>} TMPDIR=$tmpdir): $out" >&2
		fails=$((fails + 1))
	fi
}

readonly_dir="$fixture/ro"
mkdir -p "$readonly_dir"
chmod 500 "$readonly_dir"

# The real-world trap: /tmp is tmpfs, which is the default when TMPDIR is unset.
expect_reject "unset TMPDIR (defaults to tmpfs /tmp)" "TMPDIR is unset" linux ext4 __unset__
expect_reject "relative TMPDIR" "absolute path" linux ext4 "relative/path"
expect_reject "missing directory" "not an existing directory" linux ext4 "$fixture/absent"
expect_reject "non-writable directory" "not writable" linux ext4 "$readonly_dir"
expect_reject "tmpfs TMPDIR" "must be on ext4" linux tmpfs "$fixture"
expect_reject "TMPDIR on an unnamed filesystem" "on unknown" linux "" "$fixture"
expect_reject "ext3 TMPDIR (near miss, still not ext4)" "must be on ext4" linux ext3 "$fixture"
expect_reject "empty TMPDIR (unset-equivalent)" "TMPDIR is unset" linux ext4 ""

expect_accept "ext4 TMPDIR" linux ext4 "$fixture"
# The requirement is Linux-only, so other hosts must never fail the gate here.
expect_accept "tmpfs TMPDIR on a non-Linux host" darwin tmpfs "$fixture"
expect_accept "unset TMPDIR on a non-Linux host" darwin ext4 __unset__

chmod 700 "$readonly_dir"
if ((fails > 0)); then
	echo "FAIL: $fails case(s) behaved incorrectly" >&2
	exit 1
fi
echo "PASS: ext4 TMPDIR preflight accepts real storage and names every rejection."