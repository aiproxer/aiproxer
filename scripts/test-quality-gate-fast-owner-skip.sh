#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
QUALITY_GATE_SOURCE="$SCRIPT_DIR/quality-gate.sh"
OWNER_TEST=TestRuntimebundle_NoCompleteOwnerCallbackEscapes
tmp="$(mktemp -d)"
trap 'rm -rf -- "$tmp"' EXIT

mapfile -t git_local_env < <(git -C "$SCRIPT_DIR/.." rev-parse --local-env-vars)
for name in "${git_local_env[@]}"; do
	unset "$name"
done
SHARED_SCOPE_BINARY="$tmp/localscope"
export SHARED_SCOPE_BINARY
(cd "$SCRIPT_DIR/.." && go build -o "$SHARED_SCOPE_BINARY" ./tools/localscope)

# create_fixture builds one throwaway repository with a single staged path.
# Case paths become the fixture's names, so the gate log and Go capture stay
# separate per case.
create_fixture() {
	local case_name="$1" path="$2" module_dir="$3"
	REPO="$tmp/$case_name"
	FAKEBIN="$tmp/$case_name-bin"
	CAPTURE="$tmp/$case_name-go.log"
	GATE_LOG="$tmp/$case_name-gate.log"
	mkdir -p "$REPO/scripts" "$FAKEBIN" "$REPO/$(dirname "$path")"
	printf 'module example.test/root\n\ngo 1.26.9\n' >"$REPO/go.mod"
	if [[ "$module_dir" != "." ]]; then
		mkdir -p "$REPO/$module_dir"
		printf 'module example.test/%s\n\ngo 1.25.0\n' "$case_name" >"$REPO/$module_dir/go.mod"
	fi
	printf 'package fixture\n' >"$REPO/$path"
	git -C "$REPO" -c init.defaultBranch=main init -q
	git -C "$REPO" add -- "$path"
}

# install_gate_helpers copies the gate and stubs every helper it invokes. The
# list comes from the gate itself, so a new dependency in quality-gate.sh
# cannot leave the fixture incomplete and break this test with a missing
# script. ci-scope.sh is copied because the gate sources it for classifiers.
install_gate_helpers() {
	local repo="$1" helper
	while IFS= read -r helper; do
		if [[ ! -f "$SCRIPT_DIR/$helper" ]]; then
			printf 'gate helper does not exist: %s\n' "$helper" >&2
			return 1
		fi
		case "$helper" in
			dev-cpu-defaults.sh)
				printf '#!/usr/bin/env bash\n:\n' >"$repo/scripts/$helper"
				;;
			test-staged.sh)
				cat >"$repo/scripts/$helper" <<'EOF'
#!/usr/bin/env bash
printf 'test-staged\n' >>"$GO_CAPTURE"
EOF
				;;
			ci-scope.sh)
				cp "$SCRIPT_DIR/$helper" "$repo/scripts/$helper"
				;;
			*)
				printf '#!/usr/bin/env bash\nexit 0\n' >"$repo/scripts/$helper"
				;;
		esac
	done < <(grep -E '^[[:space:]]*(source|bash|AIP_[A-Z_]+=.*bash|env .*bash) ' "$QUALITY_GATE_SOURCE" | grep -oE '[A-Za-z0-9._/-]+\.sh' | sed 's|.*/||' | sort -u)
	# Copy the gate last: it keeps its own name even if it yet mentions it.
	cp "$QUALITY_GATE_SOURCE" "$repo/scripts/quality-gate.sh"
}

install_fake_tools() {
	local fakebin="$1"
	cat >"$fakebin/go" <<'EOF'
#!/usr/bin/env bash
printf '%s\t' "$@" >>"$GO_CAPTURE"
printf '\n' >>"$GO_CAPTURE"
if [[ " $* " == *" ./tools/localscope "* ]]; then
  shift 3
  exec "$SHARED_SCOPE_BINARY" "$@"
fi
if [[ "${1:-}" == "env" && "${2:-}" == "GOOS" ]]; then
	printf 'linux\n'
fi
EOF
	cat >"$fakebin/govulncheck" <<'EOF'
#!/usr/bin/env bash
printf 'govulncheck\n' >>"$GO_CAPTURE"
EOF
	chmod +x "$fakebin/go" "$fakebin/govulncheck"
}

run_fixture_gate() {
	local precommit_full="$1"
	: >"$CAPTURE"
	(
		cd "$REPO"
		export PATH="$FAKEBIN:$PATH" GO_CAPTURE="$CAPTURE" AIP_PRECOMMIT_FULL="$precommit_full"
		bash scripts/quality-gate.sh
	) >"$GATE_LOG" 2>&1
}

run_case() {
	local case_name="$1" path="$2" module_dir="$3" want_skip="$4" precommit_full="${5:-}"
	create_fixture "$case_name" "$path" "$module_dir"
	install_gate_helpers "$REPO" || return 1
	install_fake_tools "$FAKEBIN"
	if ! run_fixture_gate "$precommit_full"; then
		cat "$GATE_LOG" >&2
		return 1
	fi
	if [[ "$precommit_full" == "1" ]]; then
		if grep -Fq -- "-skip-test=$OWNER_TEST" "$CAPTURE" || ! grep -Fxq -- "test-staged" "$CAPTURE"; then
			printf 'full precommit did not retain its whole-suite test path in %s\n' "$case_name" >&2
			cat "$CAPTURE" >&2
			cat "$GATE_LOG" >&2
			return 1
		fi
		return 0
	fi
	if [[ "$want_skip" == "yes" ]]; then
		if ! grep -Fq -- "-skip-test=$OWNER_TEST" "$CAPTURE" || ! grep -Fq -- "$OWNER_TEST; full precommit and dedicated CI" "$GATE_LOG"; then
			printf 'owner package did not receive the explicit skip in %s\n' "$case_name" >&2
			cat "$CAPTURE" >&2
			cat "$GATE_LOG" >&2
			return 1
		fi
	elif grep -Fq -- "-skip-test=$OWNER_TEST" "$CAPTURE" || grep -Fq -- "Fast staged hook excludes $OWNER_TEST" "$GATE_LOG"; then
		printf 'unexpected owner skip in %s\n' "$case_name" >&2
		cat "$CAPTURE" >&2
		cat "$GATE_LOG" >&2
		return 1
	fi
}

# assert_policy_checks states which policy gate a policy-only commit must reach.
assert_policy_checks() {
	local case_name="$1" want_checker="$2" unwanted_checker="$3"
	if [[ -n "$want_checker" ]] && ! grep -Fq -- $'run\t'"$want_checker" "$CAPTURE"; then
		printf '%s did not run %s\n' "$case_name" "$want_checker" >&2
		cat "$CAPTURE" >&2
		cat "$GATE_LOG" >&2
		return 1
	fi
	if [[ -n "$unwanted_checker" ]] && grep -Fq -- $'run\t'"$unwanted_checker" "$CAPTURE"; then
		printf '%s unexpectedly ran %s\n' "$case_name" "$unwanted_checker" >&2
		cat "$CAPTURE" >&2
		cat "$GATE_LOG" >&2
		return 1
	fi
}

# A policy-only commit runs the kirocheck policy checker; the spec-only path
# keeps the offline, dependency-free checker.
run_policy_case() {
	local case_name="$1" path="$2" want_checker="$3" unwanted_checker="$4" setup="${5:-}"
	create_fixture "$case_name" "$path" .
	if [[ "$setup" == "speccheck" ]]; then
		mkdir -p "$REPO/tools/kiro/speccheck"
		printf 'var registry = struct{ name string }{name: "example"}\n' >"$REPO/tools/kiro/speccheck/fixture_test.go"
	fi
	install_gate_helpers "$REPO" || return 1
	install_fake_tools "$FAKEBIN"
	if ! run_fixture_gate ""; then
		cat "$GATE_LOG" >&2
		return 1
	fi
	assert_policy_checks "$case_name" "$want_checker" "$unwanted_checker"
}

# A policy-only commit must never fall through to the staged Go gate.
assert_policy_only_exit() {
	if grep -Fq -- "No staged Go files or module metadata detected" "$GATE_LOG"; then
		printf 'policy commit was skipped entirely\n' >&2
		cat "$GATE_LOG" >&2
		return 1
	fi
}

cp "$QUALITY_GATE_SOURCE" "$tmp/broken-gate.sh"
# shellcheck disable=SC2016 # Keep the gate's runtime SCRIPT_DIR reference literal.
printf '\nbash "$SCRIPT_DIR/nonexistent-owner-skip-helper.sh"\n' >>"$tmp/broken-gate.sh"
if (QUALITY_GATE_SOURCE="$tmp/broken-gate.sh"; run_case missing-helper internal/infra/other/other_test.go . no) >"$tmp/missing-helper.log" 2>&1; then
	echo 'fixture accepted a nonexistent gate helper' >&2
	exit 1
fi
grep -Fxq 'gate helper does not exist: nonexistent-owner-skip-helper.sh' "$tmp/missing-helper.log"

run_case root-owner internal/infra/runtimebundle/owner_test.go . yes
run_case root-other internal/infra/other/other_test.go . no
run_case nested-owner connectors/local/internal/infra/runtimebundle/owner_test.go connectors/local no
run_case full-owner internal/infra/runtimebundle/owner_test.go . no 1

run_policy_case spec-only .kiro/specs/example/requirements.md ./tools/kiro/kirospec-check ./tools/kiro/kirocheck speccheck
assert_policy_only_exit
run_policy_case steering-only .kiro/steering/testing.md ./tools/kiro/kirocheck ./tools/kiro/kirospec-check
assert_policy_only_exit
run_policy_case makefile-only Makefile ./tools/kiro/kirocheck ./tools/kiro/kirospec-check
assert_policy_only_exit
run_policy_case unrelated-doc notes/README.md "" ./tools/kiro/kirocheck
if ! grep -Fq -- "No staged Go files or module metadata detected" "$GATE_LOG"; then
	printf 'unrelated documentation did not stay skipped\n' >&2
	cat "$GATE_LOG" >&2
	exit 1
fi
printf 'quality-gate fast owner-skip scope tests passed\n'
