#!/usr/bin/env bash
# All delegated commands use a fake toolchain, including simulated CI race runs.
set -euo pipefail
# The self-test asserts what the guard itself exports, so it must not inherit
# guard state from its caller. The go wrapper exports the lock markers while it
# holds the real slot and cache locks (the guard would then skip the fixture
# locks), and `go run` exports GOTOOLCHAIN to its children (the test asserts the
# guard leaves it unset); the pre-commit hook runs this script under both.
unset AIP_GO_CACHE_HELD AIP_GO_SLOT_HELD GOTOOLCHAIN
script_dir=$(cd "$(dirname "$0")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/bin" "$fixture/config/go"
cat > "$fixture/bin/hostname" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$GUARD_TEST_HOST"
STUB
cat > "$fixture/bin/real-go" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$GUARD_TEST_CALLS"
resource_nice=
if [[ $(uname -s) == Linux ]]; then resource_nice=$(ps -o ni= -p "$$" | tr -d ' '); fi
printf '%s\n' "${GOMAXPROCS:-}" "${GOFLAGS:-}" "$resource_nice" > "$GUARD_TEST_RESOURCES"
printf '%s\n' "${TMPDIR:-}" > "$GUARD_TEST_TMPDIR"
printf '%s\n' "${AIP_GO_SLOT_HELD:-}" > "$GUARD_TEST_SLOT"
printf '%s\n' "${GOTOOLCHAIN:-}" > "$GUARD_TEST_TOOLCHAIN"
if [[ ${GUARD_TEST_PROBE_CACHE:-} == 1 ]]; then
	if flock -n -x "$AIP_GO_SLOT_DIR/cache" true; then exit 97; fi
fi
printf 'fake toolchain output\n'
exit "${GUARD_TEST_EXIT:-0}"
STUB
# Filesystem type is faked so the result does not depend on the runner's /tmp.
cat > "$fixture/bin/stat" <<'STUB'
#!/usr/bin/env bash
case "${!#}" in "$GUARD_TEST_EXT4"*) printf 'ext2/ext3\n' ;; *) printf 'tmpfs\n' ;; esac
STUB
chmod +x "$fixture/bin/hostname" "$fixture/bin/real-go" "$fixture/bin/stat"
export PATH="$fixture/bin:$PATH"
export GO_DEV_GUARD_REAL_GO="$fixture/bin/real-go"
export GUARD_TEST_CALLS="$fixture/calls"
export GUARD_TEST_RESOURCES="$fixture/resources"
export GUARD_TEST_TMPDIR="$fixture/tmpdir"
export GUARD_TEST_SLOT="$fixture/slot"
export GUARD_TEST_TOOLCHAIN="$fixture/toolchain"
export AIP_GO_SLOT_DIR="$fixture/slots"
export GUARD_TEST_EXT4="$fixture/ext4"
export AIP_DEV_TMPDIR="$GUARD_TEST_EXT4/aip-tmp"
export GUARD_TEST_HOST=agent-dev HOSTNAME=agent-dev
export XDG_CONFIG_HOME="$fixture/config"
unset GOFLAGS GOENV GOMAXPROCS COMPUTERNAME CI GITHUB_ACTIONS AIP_ALLOW_RACE_ON_DEV AIP_GO_SLOTS AIP_GO_SLOT_WAIT AIP_GO_SLOT_HELD AIP_GO_SLOT_MODE AIP_DEV_HOST_POLICY_FILE

blocked() {
	rm -f "$GUARD_TEST_CALLS"
	local status=0
	bash "$script_dir/go-dev-guard.sh" "$@" > "$fixture/out" 2> "$fixture/err" || status=$?
	if [[ "$status" != 2 || -e "$GUARD_TEST_CALLS" ]]; then
		echo "FAIL: blocked invocation reached toolchain or returned status $status: $*" >&2
		exit 1
	fi
	grep -q 'GitHub CI' "$fixture/err"
}

allowed() {
	rm -f "$GUARD_TEST_CALLS"
	bash "$script_dir/go-dev-guard.sh" "$@" > "$fixture/out" 2> "$fixture/err"
	[[ -e "$GUARD_TEST_CALLS" && ! -s "$fixture/err" ]]
	grep -qx 'fake toolchain output' "$fixture/out"
	printf '%s\n' "$@" > "$fixture/expected"
	cmp "$fixture/expected" "$GUARD_TEST_CALLS"
}

blocked test -race ./...
blocked test ./... -race=true
blocked -C module test --race=1 ./...
blocked build -race ./cmd/app
blocked run -race main.go
blocked install -race ./cmd/app
blocked tool compile -race input.go
GOFLAGS='-p=2 -race' blocked test ./...
GOFLAGS="'-race=true' -p=2" blocked test ./...
GOFLAGS=$'-p=2\n-race' blocked test ./...
printf 'GOFLAGS=-race\n' > "$fixture/config/go/env"
blocked test ./...
GOFLAGS=-p=2 allowed test ./...
GOENV=off allowed test ./...
GOENV="$fixture/config/go/env" blocked test ./...
allowed env -u GOFLAGS
rm "$fixture/config/go/env"
CI=true GITHUB_ACTIONS=true AIP_ALLOW_RACE_ON_DEV=1 blocked test -race ./...
GUARD_TEST_HOST=ci-runner HOSTNAME=ci-runner COMPUTERNAME=AGENT-DEV.example blocked test -race ./...
GUARD_TEST_HOST=Desktop-I2caj6v.example HOSTNAME=ci-runner blocked test -race ./...
GUARD_TEST_HOST=ci-runner HOSTNAME=ci-runner allowed test -race ./...
mkdir -p "$XDG_CONFIG_HOME/aip"
printf 'development\n' > "$XDG_CONFIG_HOME/aip/dev-host-policy"
GUARD_TEST_HOST=devbox-local HOSTNAME=devbox-local blocked test -race ./...
GUARD_TEST_HOST=devbox-local HOSTNAME=devbox-local CI=true blocked test -race ./...
GUARD_TEST_HOST=devbox-local HOSTNAME=devbox-local allowed version
GUARD_TEST_HOST=devbox-local HOSTNAME=devbox-local allowed test ./...
[[ $(sed -n '1p' "$GUARD_TEST_RESOURCES") == 2 && $(cat "$GUARD_TEST_TMPDIR") == "$AIP_DEV_TMPDIR" ]]
# When remote CI cannot start its race lanes (for example while the GitHub
# Actions account is billing-blocked), the race lane it would normally own runs
# on this host instead. The host records that state, and the race flag is admitted
# because the guard's premise (remote CI owns the race evidence) no longer
# holds. Every other development-host guard stays in force: the machine is
# still a development host, so the resource defaults still apply.
printf 'ci-stand-in\n' > "$XDG_CONFIG_HOME/aip/dev-host-policy"
GUARD_TEST_HOST=stand-in-host HOSTNAME=stand-in-host allowed test -race ./...
[[ $(sed -n '1p' "$GUARD_TEST_RESOURCES") == 2 && $(sed -n '2p' "$GUARD_TEST_RESOURCES") == '-p=1' ]]
printf 'invalid\n' > "$XDG_CONFIG_HOME/aip/dev-host-policy"
rm -f "$GUARD_TEST_CALLS"
status=0
bash "$script_dir/go-dev-guard.sh" version > "$fixture/invalid-policy" 2>&1 || status=$?
[[ $status == 2 && ! -e "$GUARD_TEST_CALLS" ]]
rm "$XDG_CONFIG_HOME/aip/dev-host-policy"
allowed version
allowed test -race=false ./...
allowed test ./... -args -race
allowed run ./cmd/app -race
allowed test -run Contains-race ./...
allowed test './path with spaces/...'
status=0
GUARD_TEST_EXIT=17 bash "$script_dir/go-dev-guard.sh" test ./... > /dev/null || status=$?
[[ "$status" == 17 ]]
# Observe the delegated fake process, independently of installed Go wrappers.
if [[ $(uname -s) == Linux ]]; then
	inherited_nice=$(ps -o ni= -p "$$" | tr -d ' ')
	expected_nice=$(( inherited_nice > 10 ? inherited_nice : 10 ))
	retained_nice=$(( inherited_nice > 15 ? inherited_nice : 15 ))
	allowed test ./...
	printf '2\n-p=1\n%s\n' "$expected_nice" > "$fixture/expected-resources"
	cmp "$fixture/expected-resources" "$GUARD_TEST_RESOURCES"
	GOFLAGS='-p=3 -tags="two words"' GOMAXPROCS=4 allowed test ./...
	printf '4\n-p=3 -tags="two words"\n%s\n' "$expected_nice" > "$fixture/expected-resources"
	cmp "$fixture/expected-resources" "$GUARD_TEST_RESOURCES"
	printf 'GOFLAGS=-tags="two words"\n' > "$fixture/config/go/env"
	allowed test ./...
	printf '2\n-tags="two words" -p=1\n%s\n' "$expected_nice" > "$fixture/expected-resources"
	cmp "$fixture/expected-resources" "$GUARD_TEST_RESOURCES"
	rm "$fixture/config/go/env"
	CI=true allowed test ./...
	[[ $(sed -n '1p' "$GUARD_TEST_RESOURCES") == '' && $(sed -n '2p' "$GUARD_TEST_RESOURCES") == '' ]]
	OS=Windows_NT allowed test ./...
	[[ $(sed -n '1p' "$GUARD_TEST_RESOURCES") == '' && $(sed -n '2p' "$GUARD_TEST_RESOURCES") == '' ]]
	bash -c 'renice --priority "$2" --pid "$$" >/dev/null; exec bash "$1" test ./...' _ "$script_dir/go-dev-guard.sh" "$retained_nice" >/dev/null
	[[ $(sed -n '3p' "$GUARD_TEST_RESOURCES") == "$retained_nice" ]]
	POSIXLY_CORRECT=1 allowed test ./...
	[[ $(sed -n '3p' "$GUARD_TEST_RESOURCES") == "$expected_nice" ]]
	# Bash gate defaults reach all children, including non-Go linters.
	unset AIP_TEST_PACKAGES AIP_TEST_PARALLEL
	bash -c 'source "$1/dev-cpu-defaults.sh"; printf "%s\n" "$GOMAXPROCS" "$AIP_TEST_PACKAGES" "$AIP_TEST_PARALLEL" "$(ps -o ni= -p "$$" | tr -d " ")"' _ "$script_dir" > "$fixture/gate-resources"
	printf '2\n1\n2\n%s\n' "$expected_nice" > "$fixture/expected-resources"
	cmp "$fixture/expected-resources" "$fixture/gate-resources"
	GOMAXPROCS=4 AIP_TEST_PACKAGES=3 AIP_TEST_PARALLEL=5 bash -c 'source "$1/dev-cpu-defaults.sh"; printf "%s\n" "$GOMAXPROCS" "$AIP_TEST_PACKAGES" "$AIP_TEST_PARALLEL"' _ "$script_dir" > "$fixture/gate-resources"
	printf '4\n3\n5\n' > "$fixture/expected-resources"
	cmp "$fixture/expected-resources" "$fixture/gate-resources"
	# A tmpfs or unset TMPDIR moves to ext4; an ext4 TMPDIR is kept; CI is untouched.
	env -u TMPDIR bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ $(cat "$GUARD_TEST_TMPDIR") == "$AIP_DEV_TMPDIR" && -d $AIP_DEV_TMPDIR ]]
	TMPDIR="$fixture/ram" bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ $(cat "$GUARD_TEST_TMPDIR") == "$AIP_DEV_TMPDIR" ]]
	mkdir -p "$GUARD_TEST_EXT4/own"
	TMPDIR="$GUARD_TEST_EXT4/own" bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ $(cat "$GUARD_TEST_TMPDIR") == "$GUARD_TEST_EXT4/own" ]]
	TMPDIR="$fixture/ram" CI=true bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ $(cat "$GUARD_TEST_TMPDIR") == "$fixture/ram" ]]
	TMPDIR="$fixture/ram" bash -c 'source "$1/dev-cpu-defaults.sh"; printf "%s\n" "$TMPDIR"' _ "$script_dir" > "$fixture/gate-tmpdir"
	[[ $(cat "$fixture/gate-tmpdir") == "$AIP_DEV_TMPDIR" ]]
	# Filesystem need, not hostname, decides the TMPDIR redirect: a non-agent-dev
	# Linux host with a non-ext temp directory gets the same redirect, an ext one
	# is kept, and CI stays untouched. Resource limits stay hostname-gated.
	GUARD_TEST_HOST=devbox-local HOSTNAME=devbox-local env -u TMPDIR bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ $(cat "$GUARD_TEST_TMPDIR") == "$AIP_DEV_TMPDIR" ]]
	GUARD_TEST_HOST=devbox-local HOSTNAME=devbox-local TMPDIR="$fixture/ram" bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ $(cat "$GUARD_TEST_TMPDIR") == "$AIP_DEV_TMPDIR" ]]
	mkdir -p "$GUARD_TEST_EXT4/devbox"
	GUARD_TEST_HOST=devbox-local HOSTNAME=devbox-local TMPDIR="$GUARD_TEST_EXT4/devbox" bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ $(cat "$GUARD_TEST_TMPDIR") == "$GUARD_TEST_EXT4/devbox" ]]
	GUARD_TEST_HOST=devbox-local HOSTNAME=devbox-local TMPDIR="$fixture/ram" CI=true bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ $(cat "$GUARD_TEST_TMPDIR") == "$fixture/ram" ]]
	GUARD_TEST_HOST=devbox-local HOSTNAME=devbox-local env -u TMPDIR bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ $(sed -n '1p' "$GUARD_TEST_RESOURCES") == '' ]]
	GUARD_TEST_HOST=devbox-local HOSTNAME=devbox-local TMPDIR="$fixture/ram" bash -c 'source "$1/dev-cpu-defaults.sh"; printf "%s\n" "$TMPDIR"' _ "$script_dir" > "$fixture/gate-tmpdir"
	[[ $(cat "$fixture/gate-tmpdir") == "$AIP_DEV_TMPDIR" ]]
	# Orphaned work dirs older than six hours are pruned at most hourly; live ones stay.
	mkdir -p "$AIP_DEV_TMPDIR/go-build-old" "$AIP_DEV_TMPDIR/go-build-live" "$AIP_DEV_TMPDIR/keep-old"
	touch -d '7 hours ago' "$AIP_DEV_TMPDIR/go-build-old" "$AIP_DEV_TMPDIR/keep-old"
	rm -f "$AIP_DEV_TMPDIR/.go-build-pruned"
	env -u TMPDIR bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ ! -e $AIP_DEV_TMPDIR/go-build-old && -d $AIP_DEV_TMPDIR/go-build-live && -d $AIP_DEV_TMPDIR/keep-old ]]
	mkdir -p "$AIP_DEV_TMPDIR/go-build-old" && touch -d '7 hours ago' "$AIP_DEV_TMPDIR/go-build-old"
	env -u TMPDIR bash "$script_dir/go-dev-guard.sh" test ./... >/dev/null
	[[ -d $AIP_DEV_TMPDIR/go-build-old ]]
	# Heavy commands take a host-wide slot; the slot is visible to children.
	allowed test ./...
	[[ $(cat "$GUARD_TEST_SLOT") == 0 ]]
	status=0
	GUARD_TEST_EXIT=17 bash "$script_dir/go-dev-guard.sh" test ./... > /dev/null || status=$?
	[[ "$status" == 17 ]]
	# With every slot busy, heavy commands wait; light and nested ones do not.
	flock -n -o "$AIP_GO_SLOT_DIR/slot0" bash -c 'touch "$1"; sleep 30' _ "$fixture/held" &
	holder=$!
	for _ in $(seq 100); do [[ -e $fixture/held ]] && break; sleep 0.1; done
	[[ -e $fixture/held ]]
	AIP_GO_SLOTS=1 allowed env GOPATH
	AIP_GO_SLOTS=1 AIP_GO_SLOT_HELD=7 allowed test ./...
	[[ $(cat "$GUARD_TEST_SLOT") == 7 ]]
	AIP_GO_SLOTS=2 allowed vet ./...
	[[ $(cat "$GUARD_TEST_SLOT") == 1 ]]
	AIP_GO_SLOTS=1 AIP_GO_SLOT_WAIT=1 bash "$script_dir/go-dev-guard.sh" build ./... > "$fixture/out" 2> "$fixture/err"
	grep -q 'waiting for a slot' "$fixture/err" && grep -q 'running without one' "$fixture/err"
	grep -q 'slot0 held for .* by pid' "$fixture/err"
	[[ $(cat "$GUARD_TEST_SLOT") == none ]]
	GUARD_TEST_PROBE_CACHE=1 AIP_GO_SLOTS=1 AIP_GO_SLOT_WAIT=0 AIP_GO_SLOT_MODE=advisory bash "$script_dir/go-dev-guard.sh" build ./... > "$fixture/out" 2> "$fixture/err"
	grep -q 'AIP_GO_SLOT_MODE=advisory' "$fixture/err"
	[[ $(cat "$GUARD_TEST_SLOT") == none ]]
	# Hard limits never start the toolchain when every slot remains busy.
	rm -f "$GUARD_TEST_CALLS"
	status=0
	AIP_GO_SLOTS=1 AIP_GO_SLOT_WAIT=0 AIP_GO_SLOT_MODE=hard bash "$script_dir/go-dev-guard.sh" build ./... > "$fixture/out" 2> "$fixture/err" || status=$?
	[[ $status == 75 && ! -e "$GUARD_TEST_CALLS" ]]
	grep -q 'resource-blocked' "$fixture/err"
	status=0
	AIP_GO_SLOTS=1 AIP_GO_SLOT_WAIT=0 AIP_GO_SLOT_MODE=hard bash "$script_dir/go-dev-guard.sh" --aip-resource-run "$fixture/bin/real-go" analyzer > "$fixture/out" 2> "$fixture/err" || status=$?
	[[ $status == 75 && ! -e "$GUARD_TEST_CALLS" ]]
	# Nested commands retain the parent's slot even under hard policy.
	AIP_GO_SLOTS=1 AIP_GO_SLOT_WAIT=0 AIP_GO_SLOT_MODE=hard AIP_GO_SLOT_HELD=7 allowed test ./...
	[[ $(cat "$GUARD_TEST_SLOT") == 7 ]]
	# A zero wait still attempts a free slot once.
	AIP_GO_SLOTS=2 AIP_GO_SLOT_WAIT=0 AIP_GO_SLOT_MODE=hard allowed vet ./...
	[[ $(cat "$GUARD_TEST_SLOT") == 1 ]]
	# Misspelled policy must not silently weaken a requested hard limit.
	rm -f "$GUARD_TEST_CALLS"
	status=0
	AIP_GO_SLOT_MODE=hrad bash "$script_dir/go-dev-guard.sh" build ./... > "$fixture/out" 2> "$fixture/err" || status=$?
	[[ $status == 2 && ! -e "$GUARD_TEST_CALLS" ]]
	grep -q 'invalid AIP_GO_SLOT_MODE' "$fixture/err"
	kill "$holder"; wait "$holder" 2>/dev/null || true
	# A freed slot is taken without the timeout fallback.
	flock -n -o "$AIP_GO_SLOT_DIR/slot0" sleep 1 &
	holder=$!
	sleep 0.3
	AIP_GO_SLOTS=1 bash "$script_dir/go-dev-guard.sh" -C . test ./... > "$fixture/out" 2> "$fixture/err"
	wait "$holder" 2>/dev/null || true
	[[ $(cat "$GUARD_TEST_SLOT") == 0 ]] && ! grep -q 'running without one' "$fixture/err"
	# Analyzer work uses the same slot budget, not an independent semaphore.
	GUARD_TEST_PROBE_CACHE=1 AIP_GO_SLOT_MODE=hard AIP_GO_SLOT_WAIT=0 bash "$script_dir/go-dev-guard.sh" --aip-resource-run "$fixture/bin/real-go" analyzer > "$fixture/out" 2> "$fixture/err"
	[[ $(cat "$GUARD_TEST_SLOT") == 0 ]]
	grep -qx analyzer "$GUARD_TEST_CALLS"
	GUARD_TEST_PROBE_CACHE=1 allowed build ./...
	# Real flock contention: maintenance cannot overlap active compiler/analyzer
	# cache readers, even when the slot policy would otherwise fail open.
	exec {cache_fd}>"$AIP_GO_SLOT_DIR/cache"
	flock -s "$cache_fd"
	rm -f "$GUARD_TEST_CALLS"
	status=0
	AIP_GO_SLOT_WAIT=0 bash "$script_dir/go-dev-guard.sh" --aip-resource-exclusive "$fixture/bin/real-go" maintenance > "$fixture/out" 2> "$fixture/err" || status=$?
	[[ $status == 75 && ! -e "$GUARD_TEST_CALLS" ]]
	grep -q 'resource-blocked' "$fixture/err"
	flock -u "$cache_fd"
	flock -x "$cache_fd"
	for invocation in go analyzer; do
		rm -f "$GUARD_TEST_CALLS"
		status=0
		if [[ $invocation == go ]]; then
			AIP_GO_SLOT_WAIT=0 bash "$script_dir/go-dev-guard.sh" build ./... > "$fixture/out" 2> "$fixture/err" || status=$?
		else
			AIP_GO_SLOT_WAIT=0 bash "$script_dir/go-dev-guard.sh" --aip-resource-run "$fixture/bin/real-go" analyzer > "$fixture/out" 2> "$fixture/err" || status=$?
		fi
		[[ $status == 75 && ! -e "$GUARD_TEST_CALLS" ]]
	done
	flock -u "$cache_fd"
	exec {cache_fd}>&-
	# Exclusive owners can invoke nested guarded Go without self-deadlock.
	AIP_GO_SLOT_WAIT=0 bash "$script_dir/go-dev-guard.sh" --aip-resource-exclusive bash "$script_dir/go-dev-guard.sh" build ./... > "$fixture/out" 2> "$fixture/err"
	[[ $(cat "$GUARD_TEST_SLOT") == maintenance ]]
	# Exit status and lock release survive a failed maintenance command.
	status=0
	GUARD_TEST_EXIT=17 bash "$script_dir/go-dev-guard.sh" --aip-resource-exclusive "$fixture/bin/real-go" maintenance >/dev/null || status=$?
	[[ $status == 17 ]]
	flock -n -x "$AIP_GO_SLOT_DIR/cache" true
	# Maintenance's real entry point is dry by default. --apply delegates only
	# to fake tools here; never clear the development host's caches in a test.
	cat > "$fixture/bin/go" <<'STUB'
#!/usr/bin/env bash
printf 'go %s\n' "$*" >> "$GUARD_TEST_MAINTENANCE"
[[ ${AIP_GO_CACHE_HELD:-} == exclusive && ${AIP_GO_SLOT_HELD:-} == maintenance ]]
STUB
	cat > "$fixture/bin/golangci-lint" <<'STUB'
#!/usr/bin/env bash
printf 'lint %s\n' "$*" >> "$GUARD_TEST_MAINTENANCE"
[[ ${AIP_GO_CACHE_HELD:-} == exclusive && ${AIP_GO_SLOT_HELD:-} == maintenance ]]
STUB
	chmod +x "$fixture/bin/go" "$fixture/bin/golangci-lint"
	export GUARD_TEST_MAINTENANCE="$fixture/maintenance"
	bash "$script_dir/dev-cache-maintenance.sh" > "$fixture/out"
	[[ ! -e $GUARD_TEST_MAINTENANCE ]]
	bash "$script_dir/dev-cache-maintenance.sh" --apply > "$fixture/out"
	printf 'go clean -cache\nlint cache clean\n' > "$fixture/expected-maintenance"
	cmp "$fixture/expected-maintenance" "$GUARD_TEST_MAINTENANCE"
fi
# The guard builds inside a checkout of this repository with the toolchain the
# repository declares: Go prefers a newer local toolchain over the declaration,
# so the host would otherwise build with a different standard library than CI
# and the release use. Every other project on the host keeps its own
# toolchain, and an explicit GOTOOLCHAIN wins so a developer can opt out.
mkdir -p "$fixture/checkout/scripts" "$fixture/checkout/nested" "$fixture/other"
printf 'module example\n\ngo 1.26.6\n\ntoolchain go1.26.10\n' > "$fixture/checkout/go.mod"
: > "$fixture/checkout/scripts/go-dev-guard.sh"
(cd "$fixture/checkout" && bash "$script_dir/go-dev-guard.sh" version > /dev/null 2>&1)
[[ $(cat "$GUARD_TEST_TOOLCHAIN") == go1.26.10 ]]
(cd "$fixture/checkout/nested" && bash "$script_dir/go-dev-guard.sh" version > /dev/null 2>&1)
[[ $(cat "$GUARD_TEST_TOOLCHAIN") == go1.26.10 ]]
(cd "$fixture/other" && bash "$script_dir/go-dev-guard.sh" version > /dev/null 2>&1)
[[ -z $(cat "$GUARD_TEST_TOOLCHAIN") ]]
printf 'module example\n\ngo 1.26.6\n' > "$fixture/checkout/go.mod"
(cd "$fixture/checkout" && bash "$script_dir/go-dev-guard.sh" version > /dev/null 2>&1)
[[ -z $(cat "$GUARD_TEST_TOOLCHAIN") ]]
printf 'module example\n\ngo 1.26.6\n\ntoolchain go1.26.10\n' > "$fixture/checkout/go.mod"
(cd "$fixture/checkout" && GOTOOLCHAIN=local bash "$script_dir/go-dev-guard.sh" version > /dev/null 2>&1)
[[ $(cat "$GUARD_TEST_TOOLCHAIN") == local ]]
echo 'PASS: Go development race guard blocks before toolchain execution; normal/CI delegation preserved; agent-dev TMPDIR stays on ext4; heavy commands share host-wide slots; the repository toolchain is pinned inside a checkout only.'
