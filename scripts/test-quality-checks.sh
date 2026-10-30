#!/usr/bin/env bash
# Regression checks for quality-checks.sh --staged. Fake tools capture the
# hook commands; a final local-only Go probe validates -o behavior.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
QUALITY_CHECKS_SOURCE="${QUALITY_CHECKS_SOURCE:-$SCRIPT_DIR/quality-checks.sh}"
QUALITY_GATE_SOURCE="${QUALITY_GATE_SOURCE:-$SCRIPT_DIR/quality-gate.sh}"
REAL_GIT="$(command -v git)"
# A commit hook may export an absolute staged index. Fixture git operations
# must use their own repositories, never that inherited index/object store.
mapfile -t fixture_git_vars < <("$REAL_GIT" rev-parse --local-env-vars)
for fixture_git_var in "${fixture_git_vars[@]}"; do unset "$fixture_git_var"; done
REAL_GO="$(command -v go)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/quality-checks-test.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/bin"
mkdir -p "$BIN"
SHARED_SCOPE_BINARY="$TMP/localscope"
export SHARED_SCOPE_BINARY
(cd "$SCRIPT_DIR/.." && "$REAL_GO" build -o "$SHARED_SCOPE_BINARY" ./tools/localscope)

fail() {
	echo "FAIL: $*" >&2
	if [[ -f "$TMP/output" ]]; then
		cat "$TMP/output" >&2
	fi
	if [[ -f "$TMP/go.log" ]]; then
		cat "$TMP/go.log" >&2
	fi
	if [[ -f "$TMP/gate.log" ]]; then
		cat "$TMP/gate.log" >&2
	fi
	if [[ -f "$TMP/lint.log" ]]; then
		cat "$TMP/lint.log" >&2
	fi
	exit 1
}

assert_contains() {
	local file="$1" needle="$2"
	grep -Fq -- "$needle" "$file" || fail "expected '$needle' in $file"
}

assert_not_contains() {
	local file="$1" needle="$2"
	if grep -Fq -- "$needle" "$file"; then
		fail "did not expect '$needle' in $file"
	fi
}

assert_count() {
	local file="$1" needle="$2" want="$3" got
	got=$(grep -Fc -- "$needle" "$file" || true)
	[[ "$got" == "$want" ]] || fail "expected $want matches for '$needle' in $file, got $got"
}

cat >"$BIN/go" <<'FAKE_GO'
#!/usr/bin/env bash
set -euo pipefail
{
	printf 'PWD=%q GOWORK=%q GO' "$PWD" "${GOWORK:-unset}"
	for arg in "$@"; do printf ' %q' "$arg"; done
	printf '\n'
} >>"$FAKE_GO_LOG"
if [[ -n "${FAKE_GO_ENV_LOG:-}" ]]; then
	printf 'AIP_LOCAL_ARCH_TRIMPATH=%s GO' "${AIP_LOCAL_ARCH_TRIMPATH:-unset}" >>"$FAKE_GO_ENV_LOG"
	for arg in "$@"; do printf ' %q' "$arg" >>"$FAKE_GO_ENV_LOG"; done
	printf '\n' >>"$FAKE_GO_ENV_LOG"
fi
case "${GO_FAIL:-}:$*" in
	build:build\ *) exit 41 ;;
	vet:vet\ *) exit 42 ;;
	tidy:mod\ tidy\ *) exit 43 ;;
esac
if [[ " $* " == *' -list '* ]]; then
	printf '%s\n' TestConnectorSubtree_CannotImportHostSecurityAuthority
	exit 0
fi
if [[ " $* " == *" ./tools/localscope "* ]]; then
	shift 3
	exec "$SHARED_SCOPE_BINARY" "$@"
fi
if [[ "${1:-}" == list ]]; then
	shift
	if [[ "${1:-}" == -f ]]; then shift 2; fi
	module=$(awk '$1 == "module" { print $2; exit }' go.mod)
	for package in "$@"; do
    if [[ "$package" == "./..." && "${FAKE_GO_WILDCARD_SINGLE:-}" != "1" ]]; then
      printf '%s\n' "$module/first" "$module/second"
    elif [[ "$package" == "${FAKE_GO_LIST_EMPTY:-}" && -n "${FAKE_GO_LIST_EMPTY:-}" ]]; then
			printf '\n'
		elif [[ "$package" == "." ]]; then
			printf '%s\n' "$module"
		else
			printf '%s/%s\n' "$module" "${package#./}"
		fi
	done
	exit 0
fi
exit 0
FAKE_GO
chmod +x "$BIN/go"

cat >"$BIN/gofmt" <<'FAKE_GOFMT'
#!/usr/bin/env bash
set -euo pipefail
{
	printf 'PWD=%q GOFMT' "$PWD"
	for arg in "$@"; do printf ' %q' "$arg"; done
	printf '\n'
} >>"$FAKE_GOFMT_LOG"
if [[ "${GOFMT_FAIL:-}" == 1 ]]; then
	echo "fake gofmt failure" >&2
	exit 51
fi
if [[ -n "${GOFMT_UNFORMATTED:-}" ]]; then
	for arg in "$@"; do
		if [[ "$arg" == "$GOFMT_UNFORMATTED" ]]; then
			printf '%s\n' "$arg"
		fi
	done
fi
FAKE_GOFMT
chmod +x "$BIN/gofmt"

cat >"$BIN/buf" <<'FAKE_BUF'
#!/usr/bin/env bash
set -euo pipefail
{
	printf 'BUF'
	for arg in "$@"; do printf ' %q' "$arg"; done
	printf '\n'
} >>"$FAKE_BUF_LOG"
FAKE_BUF
chmod +x "$BIN/buf"

cat >"$BIN/git" <<'FAKE_GIT'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${FAKE_GIT_FAIL_DIFF:-}" == 1 && "${1:-}" == diff && "${2:-}" == --cached ]]; then
	exit 61
fi
exec "$REAL_GIT" "$@"
FAKE_GIT
chmod +x "$BIN/git"

new_repo() {
	local name="$1" repo="$TMP/$1"
	mkdir -p "$repo/scripts" "$repo/internal/rootpkg" "$repo/internal/secondpkg" "$repo/internal/testonly" "$repo/internal/archtest" "$repo/internal/featureplanegen" \
		"$repo/internal/core/largebody" \
		"$repo/connectors/nested/pkg" "$repo/connectors/untouched/pkg" \
		"$repo/pkg/aipsdk/feature" "$repo/api" "$repo/.github/workflows"
	cp "$QUALITY_CHECKS_SOURCE" "$repo/scripts/quality-checks.sh"
	cp "$QUALITY_GATE_SOURCE" "$repo/scripts/quality-gate.sh"
	cp "$SCRIPT_DIR/dev-cpu-defaults.sh" "$repo/scripts/dev-cpu-defaults.sh"
	cp "$SCRIPT_DIR/dev-host-policy.sh" "$repo/scripts/dev-host-policy.sh"
	cat >"$repo/go.mod" <<'EOF'
module example.test/root

go 1.24
EOF
	cat >"$repo/connectors/nested/go.mod" <<'EOF'
module example.test/nested

go 1.24
EOF
	cat >"$repo/connectors/untouched/go.mod" <<'EOF'
module example.test/untouched

go 1.24
EOF
	cat >"$repo/internal/rootpkg/another.go" <<'EOF'
package rootpkg
EOF
	cat >"$repo/internal/rootpkg/file with space.go" <<'EOF'
package rootpkg
EOF
	cat >"$repo/internal/secondpkg/second.go" <<'EOF'
package secondpkg
EOF
	cat >"$repo/internal/testonly/only_test.go" <<'EOF'
package testonly
EOF
	cat >"$repo/internal/archtest/scan.go" <<'EOF'
package archtest
EOF
	cat >"$repo/connectors/nested/pkg/service.go" <<'EOF'
package service
EOF
	cat >"$repo/connectors/nested/pkg/keep.go" <<'EOF'
package service
EOF
	cat >"$repo/connectors/nested/pkg/delete.go" <<'EOF'
package service
EOF
	cat >"$repo/connectors/untouched/pkg/untouched.go" <<'EOF'
package untouched
EOF
	printf 'feature input\n' >"$repo/pkg/aipsdk/feature/base.txt"
	printf 'feature to delete\n' >"$repo/pkg/aipsdk/feature/delete.txt"
	printf 'package largebody\n' >"$repo/internal/core/largebody/plane_catalog_generated.go"
	printf 'syntax = "proto3";\n' >"$repo/api/base.proto"
	printf 'syntax = "proto3";\n' >"$repo/api/delete.proto"
	printf 'name: CI\n' >"$repo/.github/workflows/ci.yml"
	cat >"$repo/scripts/check-adhoc-goroutines.sh" <<'EOF'
#!/usr/bin/env bash
printf 'adhoc\n' >>"$FAKE_GUARD_LOG"
EOF
	cat >"$repo/scripts/regex-hotpath-check.sh" <<'EOF'
#!/usr/bin/env bash
printf 'regex\n' >>"$FAKE_GUARD_LOG"
EOF
	cat >"$repo/scripts/proto-check.sh" <<'EOF'
#!/usr/bin/env bash
printf 'proto\n' >>"$FAKE_GUARD_LOG"
buf lint
EOF
	cat >"$repo/scripts/check-workflows.sh" <<'EOF'
#!/usr/bin/env bash
printf 'workflows\n' >>"$FAKE_GUARD_LOG"
EOF
	cat >"$repo/scripts/lint-all-modules.sh" <<'EOF'
#!/usr/bin/env bash
printf 'lint\n' >>"$FAKE_GUARD_LOG"
EOF
	chmod +x "$repo/scripts/"*.sh
	git -C "$repo" init -q
	git -C "$repo" config user.email quality-checks-test@example.com
	git -C "$repo" config user.name quality-checks-test
	git -C "$repo" add -A
	git -C "$repo" commit -qm baseline
	REPO="$repo"
}

run_gate() {
	local repo="$1" mode="$2" go_fail="${3:-}" fmt_fail="${4:-}" fmt_unformatted="${5:-}" git_fail="${6:-}" subdir="${7:-}" arch_trimpath="${8:-}"
	local -a args=()
	[[ -z "$mode" ]] || args=("$mode")
	: >"$TMP/go.log"
	: >"$TMP/gofmt.log"
	: >"$TMP/buf.log"
	: >"$TMP/guards.log"
	(
		local script_path="$repo/scripts/quality-checks.sh"
		if [[ -n "$subdir" ]]; then
			cd "$repo/$subdir"
			script_path='../../scripts/quality-checks.sh'
		else
			cd "$repo"
		fi
		env PATH="$BIN:$PATH" REAL_GIT="$REAL_GIT" \
			FAKE_GO_LOG="$TMP/go.log" FAKE_GOFMT_LOG="$TMP/gofmt.log" \
			FAKE_BUF_LOG="$TMP/buf.log" FAKE_GUARD_LOG="$TMP/guards.log" \
			FAKE_GO_LIST_EMPTY="${FAKE_GO_LIST_EMPTY:-}" \
			GO_FAIL="$go_fail" GOFMT_FAIL="$fmt_fail" GOFMT_UNFORMATTED="$fmt_unformatted" \
			FAKE_GIT_FAIL_DIFF="$git_fail" AIP_SKIP_LINT=1 AIP_SKIP_ARCHTEST=1 \
			AIP_TEST_PARALLEL=1 AIP_VERIFY_MODULE_CACHE=0 AIP_LOCAL_ARCH_TRIMPATH="$arch_trimpath" CI=0 \
			bash "$script_path" "${args[@]}"
	) >"$TMP/output" 2>&1
}

run_quality_gate() {
	local repo="$1" ci_value="$2" github_actions="${3:-unset}" full="${4:-0}" arch_trimpath="${5:-unset}"
	local -a env_args=(
		"PATH=$BIN:$PATH"
		"REAL_GIT=$REAL_GIT"
		"FAKE_GO_LOG=$TMP/go.log"
		"FAKE_GO_ENV_LOG=$TMP/go-env.log"
		"FAKE_GATE_LOG=$TMP/gate.log"
		"FAKE_GUARD_LOG=$TMP/guards.log"
		"AIP_SKIP_LINT=1"
		"AIP_SKIP_VULN=1"
		"CI=$ci_value"
	)
	if [[ "$github_actions" != unset ]]; then
		env_args+=("GITHUB_ACTIONS=$github_actions")
	fi
	if [[ "$full" == "1" ]]; then
		env_args+=(AIP_PRECOMMIT_FULL=1)
	fi
	if [[ "$arch_trimpath" != unset ]]; then
		env_args+=("AIP_LOCAL_ARCH_TRIMPATH=$arch_trimpath")
	fi
	: >"$TMP/go.log"
	: >"$TMP/go-env.log"
	: >"$TMP/gate.log"
	: >"$TMP/guards.log"
	(
		cd "$repo"
		env -u AIP_LOCAL_ARCH_TRIMPATH -u AIP_PRECOMMIT_FULL -u GITHUB_ACTIONS "${env_args[@]}" bash "$repo/scripts/quality-gate.sh"
	) >"$TMP/gate-output" 2>&1
}

expect_pass() {
	if ! run_gate "$@"; then fail "gate unexpectedly failed for $1"; fi
}

expect_fail() {
	if run_gate "$@"; then fail "gate unexpectedly passed for $1"; fi
}

assert_cheap_guards() {
	assert_contains "$TMP/guards.log" adhoc
	assert_contains "$TMP/guards.log" regex
}

new_lint_fixture() {
	LINT_REPO="$TMP/lint-fixture"
	LINT_BIN="$TMP/lint-bin"
	mkdir -p "$LINT_REPO/scripts" "$LINT_REPO/internal/archtest" "$LINT_REPO/internal/rootpkg" \
		"$LINT_REPO/connectors/local" "$LINT_BIN"
	cp "$SCRIPT_DIR/lint-all-modules.sh" "$LINT_REPO/scripts/lint-all-modules.sh"
	cp "$SCRIPT_DIR/go-dev-guard.sh" "$LINT_REPO/scripts/go-dev-guard.sh"
	cp "$SCRIPT_DIR/dev-host-policy.sh" "$LINT_REPO/scripts/dev-host-policy.sh"
	printf 'module example.test/root\n\ngo 1.24\n' >"$LINT_REPO/go.mod"
	printf 'module example.test/local\n\ngo 1.24\n' >"$LINT_REPO/connectors/local/go.mod"
	cat >"$LINT_BIN/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'PWD=%s GO' "$PWD" >>"$FAKE_LINT_GO_LOG"
for arg in "$@"; do printf ' %s' "$arg" >>"$FAKE_LINT_GO_LOG"; done
printf '\n' >>"$FAKE_LINT_GO_LOG"
if [[ " $* " == *" ./tools/lintscope "* ]]; then
	printf '%s' "$FAKE_LINT_PLAN"
	 exit 0
fi
if [[ "${1:-}" == env && "${2:-}" == GOFLAGS ]]; then
	if [[ "${FAKE_GO_ENV_FAIL:-}" == 1 ]]; then
		exit 72
	fi
	printf '%s\n' "$FAKE_GO_ENV_FLAGS"
	 exit 0
fi
exit 0
EOF
	cat >"$LINT_BIN/golangci-lint" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'PWD=%s|GOFLAGS=%s|CMD=golangci-lint' "$PWD" "${GOFLAGS-<unset>}" >>"$FAKE_LINT_LOG"
for arg in "$@"; do printf ' %s' "$arg" >>"$FAKE_LINT_LOG"; done
printf '\n' >>"$FAKE_LINT_LOG"
if [[ -n "${FAKE_LINT_FAIL_MATCH:-}" && " $* " == *"$FAKE_LINT_FAIL_MATCH"* ]]; then
	exit 71
fi
EOF
	chmod +x "$LINT_BIN/go" "$LINT_BIN/golangci-lint"
}

run_lint_fixture() {
	local plan="$1" trim="$2" fail_match="$3"
	shift 3
	: >"$TMP/lint.log"
	: >"$TMP/lint-go.log"
	(
		cd "$LINT_REPO"
		env PATH="$LINT_BIN:$PATH" FAKE_LINT_GO_LOG="$TMP/lint-go.log" \
			FAKE_LINT_LOG="$TMP/lint.log" FAKE_LINT_PLAN="$plan" \
			FAKE_GO_ENV_FLAGS='-tags=persisted -mod=readonly' FAKE_GO_ENV_FAIL="${FAKE_GO_ENV_FAIL:-}" \
			FAKE_LINT_FAIL_MATCH="$fail_match" \
			GOFLAGS='-tags=caller' AIP_LOCAL_ARCH_TRIMPATH="$trim" \
			AIP_LINT_JOBS=1 AIP_LINT_CONCURRENCY=2 \
			AIP_GO_SLOT_DIR="$TMP/lint-slots" AIP_DEV_TMPDIR="$TMP/lint-tmp" \
			bash scripts/lint-all-modules.sh "$@"
	) >"$TMP/lint-output" 2>&1
}

# A mixed root and nested-module change checks direct package grouping, NUL-safe
# path handling, and isolation from an unstaged root file.
new_repo mixed-scope
printf 'package rootpkg\n' >"$REPO/internal/rootpkg/unstaged.go"
printf '// staged\n' >>"$REPO/internal/rootpkg/another.go"
printf '// staged\n' >>"$REPO/internal/rootpkg/file with space.go"
printf '// staged\n' >>"$REPO/internal/secondpkg/second.go"
printf '// staged\n' >>"$REPO/connectors/nested/pkg/service.go"
git -C "$REPO" add 'internal/rootpkg/another.go' 'internal/rootpkg/file with space.go' \
	'internal/secondpkg/second.go' 'connectors/nested/pkg/service.go'
expect_pass "$REPO" --staged
assert_contains "$TMP/gofmt.log" 'file\ with\ space.go'
assert_not_contains "$TMP/gofmt.log" unstaged.go
assert_count "$TMP/go.log" 'GO build ' 2
assert_count "$TMP/go.log" 'GO vet ' 2
assert_contains "$TMP/go.log" './internal/rootpkg ./internal/secondpkg'
assert_contains "$TMP/go.log" "PWD=$REPO/connectors/nested GOWORK=off GO build -buildvcs=false -o "
assert_not_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO build -buildvcs=false -o "
grep -Eq "PWD=$REPO/connectors/nested GOWORK=off GO build -buildvcs=false -o [^[:space:]]+/build\\.[0-9]+ " "$TMP/go.log" || \
	fail "single-package build should write its archive to a scratch file"
assert_contains "$TMP/go.log" './pkg'
assert_not_contains "$TMP/go.log" './...'
assert_count "$TMP/go.log" 'GO mod tidy -diff' 2
assert_not_contains "$TMP/go.log" 'connectors/untouched'
assert_not_contains "$TMP/go.log" 'generate-feature-planes.go'
assert_not_contains "$TMP/buf.log" BUF
assert_cheap_guards

# The local opt-in trims only explicit root archtest scopes, including when
# ordinary root packages are checked in the same staged module.
new_repo arch-trimpath-mixed
printf '// staged arch change\n' >>"$REPO/internal/archtest/scan.go"
printf '// staged ordinary change\n' >>"$REPO/internal/rootpkg/another.go"
git -C "$REPO" add internal/archtest/scan.go internal/rootpkg/another.go
expect_pass "$REPO" --staged '' '' '' '' '' 1
assert_count "$TMP/go.log" 'GO build ' 2
assert_count "$TMP/go.log" 'GO vet ' 2
assert_contains "$TMP/go.log" 'GO build -buildvcs=false -trimpath -o '
assert_contains "$TMP/go.log" 'GO vet -trimpath ./internal/archtest'
assert_contains "$TMP/go.log" 'GO run -trimpath ./scripts/generate-feature-planes.go -check'
assert_contains "$TMP/go.log" 'GO build -buildvcs=false -o '
assert_contains "$TMP/go.log" 'GO vet ./internal/rootpkg'
assert_not_contains "$TMP/go.log" 'GO vet -trimpath ./internal/rootpkg'

# Without the local opt-in the arch scope and ordinary package behavior stay
# identical to the default command.
new_repo arch-trimpath-default-off
printf '// staged arch change\n' >>"$REPO/internal/archtest/scan.go"
git -C "$REPO" add internal/archtest/scan.go
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" 'GO build -buildvcs=false -o '
assert_contains "$TMP/go.log" 'GO vet ./internal/archtest'
assert_contains "$TMP/go.log" 'GO run ./scripts/generate-feature-planes.go -check'
assert_not_contains "$TMP/go.log" 'GO build -buildvcs=false -trimpath'
assert_not_contains "$TMP/go.log" 'GO vet -trimpath '
assert_not_contains "$TMP/go.log" 'GO run -trimpath ./scripts/generate-feature-planes.go'

# A same-named package in a nested module stays untrimmed; a root ./... scope
# also remains unchanged even when the local opt-in is enabled.
new_repo nested-arch-trimpath
mkdir -p "$REPO/connectors/nested/internal/archtest"
printf 'package archtest\n' >"$REPO/connectors/nested/internal/archtest/scan.go"
git -C "$REPO" add connectors/nested/internal/archtest/scan.go
expect_pass "$REPO" --staged '' '' '' '' '' 1
assert_contains "$TMP/go.log" 'GO vet ./internal/archtest'
assert_not_contains "$TMP/go.log" 'GO vet -trimpath '

new_repo root-broad-trimpath
printf '// staged arch change\n' >>"$REPO/internal/archtest/scan.go"
printf '\n// changed metadata\n' >>"$REPO/go.mod"
git -C "$REPO" add go.mod internal/archtest/scan.go
expect_pass "$REPO" --staged '' '' '' '' '' 1
assert_count "$TMP/go.log" 'GO build ' 1
assert_count "$TMP/go.log" 'GO vet ' 1
assert_contains "$TMP/go.log" 'GO build -buildvcs=false ./...'
assert_contains "$TMP/go.log" 'GO vet ./...'
assert_contains "$TMP/go.log" 'GO run ./scripts/generate-feature-planes.go -check'
assert_not_contains "$TMP/go.log" 'GO build -buildvcs=false -trimpath'
assert_not_contains "$TMP/go.log" 'GO vet -trimpath '
assert_not_contains "$TMP/go.log" 'GO run -trimpath ./scripts/generate-feature-planes.go'

# The gate enables the arch-only cache variant by default for local fast runs,
# while CI, full mode, and an explicit opt-out keep it disabled.
new_repo local-fast-gate-trimpath
printf '// staged arch change\n' >>"$REPO/internal/archtest/scan.go"
git -C "$REPO" add internal/archtest/scan.go
cat >"$REPO/scripts/require-ext4-tmpdir.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat >"$REPO/scripts/quality-checks.sh" <<'EOF'
#!/usr/bin/env bash
printf 'quality-checks AIP_LOCAL_ARCH_TRIMPATH=%s args=%s\n' "${AIP_LOCAL_ARCH_TRIMPATH:-unset}" "$*" >>"$FAKE_GATE_LOG"
EOF
cat >"$REPO/scripts/test-staged.sh" <<'EOF'
#!/usr/bin/env bash
printf 'test-staged AIP_LOCAL_ARCH_TRIMPATH=%s args=%s\n' "${AIP_LOCAL_ARCH_TRIMPATH:-unset}" "$*" >>"$FAKE_GATE_LOG"
EOF
chmod +x "$REPO/scripts/require-ext4-tmpdir.sh" "$REPO/scripts/quality-checks.sh" "$REPO/scripts/test-staged.sh"
if ! run_quality_gate "$REPO" 0; then
	fail "local fast quality gate failed"
fi
assert_contains "$TMP/gate.log" 'quality-checks AIP_LOCAL_ARCH_TRIMPATH=1 args=--staged'
assert_contains "$TMP/go-env.log" 'AIP_LOCAL_ARCH_TRIMPATH=1 GO run'
if ! run_quality_gate "$REPO" true; then
	fail "CI fast quality gate failed"
fi
assert_contains "$TMP/gate.log" 'quality-checks AIP_LOCAL_ARCH_TRIMPATH=unset args=--staged'
if ! run_quality_gate "$REPO" 0 true; then
	fail "GitHub Actions fast quality gate failed"
fi
assert_contains "$TMP/gate.log" 'quality-checks AIP_LOCAL_ARCH_TRIMPATH=unset args=--staged'
if ! run_quality_gate "$REPO" 0 unset 0 0; then
	fail "opt-out fast quality gate failed"
fi
assert_contains "$TMP/gate.log" 'quality-checks AIP_LOCAL_ARCH_TRIMPATH=0 args=--staged'
if ! run_quality_gate "$REPO" 0 unset 1; then
	fail "full quality gate failed"
fi
assert_contains "$TMP/gate.log" 'quality-checks AIP_LOCAL_ARCH_TRIMPATH=unset args='
assert_contains "$TMP/gate.log" 'test-staged AIP_LOCAL_ARCH_TRIMPATH=unset'

# Test-only connector changes must not bypass the existing host import rule.
printf '// staged connector test\n' >"$REPO/connectors/nested/pkg/routing_test.go"
git -C "$REPO" add connectors/nested/pkg/routing_test.go
cp "$SCRIPT_DIR/run-named-go-tests.sh" "$REPO/scripts/run-named-go-tests.sh"
if ! run_quality_gate "$REPO" 0; then
	fail "connector boundary gate failed"
fi
assert_contains "$TMP/go.log" 'TestConnectorSubtree_CannotImportHostSecurityAuthority'

# Relative invocation from a package directory still finds its guard scripts.
new_repo relative-invocation
printf '// staged\n' >>"$REPO/internal/rootpkg/another.go"
git -C "$REPO" add internal/rootpkg/another.go
expect_pass "$REPO" --staged '' '' '' '' internal/rootpkg
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO build -buildvcs=false -o "
grep -Eq "PWD=$REPO GOWORK=off GO build -buildvcs=false -o [^[:space:]]+/build\\.[0-9]+ " "$TMP/go.log" || \
	fail "single-package build should write its archive to a scratch file"

# Test-only packages remain in vet and later test scope; build skips packages
# with no production files.
new_repo test-only-package
printf '// staged\n' >>"$REPO/internal/testonly/only_test.go"
git -C "$REPO" add internal/testonly/only_test.go
FAKE_GO_LIST_EMPTY=./internal/testonly expect_pass "$REPO" --staged
unset FAKE_GO_LIST_EMPTY
assert_contains "$TMP/go.log" 'GO list -f'
assert_not_contains "$TMP/go.log" 'GO build '
assert_contains "$TMP/go.log" 'GO vet ./internal/testonly'

# A deleted Go file still checks its package while a sibling source file remains.
new_repo deleted-file
git -C "$REPO" rm -q connectors/nested/pkg/delete.go
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" "PWD=$REPO/connectors/nested GOWORK=off GO build -buildvcs=false -o "
grep -Eq "PWD=$REPO/connectors/nested GOWORK=off GO build -buildvcs=false -o [^[:space:]]+/build\\.[0-9]+ " "$TMP/go.log" || \
	fail "single-package build should write its archive to a scratch file"
assert_contains "$TMP/go.log" './pkg'
assert_contains "$TMP/go.log" 'GO vet ./pkg'
assert_contains "$TMP/go.log" 'GO mod tidy -diff'
assert_not_contains "$TMP/gofmt.log" delete.go

# A removed module contributes no packages. Surviving sources widen checks to
# their parent module, including when go.mod was renamed away.
new_repo removed-module
git -C "$REPO" rm -qr connectors/nested
expect_pass "$REPO" --staged
assert_not_contains "$TMP/go.log" 'connectors/nested'
assert_not_contains "$TMP/go.log" 'GO build '
assert_not_contains "$TMP/go.log" 'GO mod tidy'

new_repo removed-module-metadata-only
git -C "$REPO" rm -q connectors/nested/go.mod
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO mod tidy -diff"
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO build -buildvcs=false ./..."
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO vet ./..."
assert_count "$TMP/go.log" 'GO mod tidy -diff' 1
expect_fail "$REPO" --staged build
assert_contains "$TMP/output" 'build failed in staged module .'

new_repo renamed-module-metadata
git -C "$REPO" mv connectors/nested/go.mod connectors/nested/old.go.mod
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO mod tidy -diff"
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO build -buildvcs=false ./..."
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO vet ./..."

new_repo removed-root-module
git -C "$REPO" rm -q go.mod
expect_fail "$REPO" --staged
assert_contains "$TMP/output" 'Go source has no surviving parent module'

# Metadata-only commits check all packages in the affected module. Root metadata
# retains root build/vet safety and triggers both global contract generators.
new_repo nested-metadata
printf '\n// changed metadata\n' >>"$REPO/connectors/nested/go.mod"
git -C "$REPO" add connectors/nested/go.mod
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" "PWD=$REPO/connectors/nested GOWORK=off GO build -buildvcs=false ./..."
assert_contains "$TMP/go.log" 'GO vet ./...'
assert_contains "$TMP/go.log" 'GO mod tidy -diff'
assert_not_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO build"
assert_not_contains "$TMP/buf.log" BUF

new_repo root-metadata
printf '\n// changed metadata\n' >>"$REPO/go.mod"
git -C "$REPO" add go.mod
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" "PWD=$REPO GOWORK=off GO build -buildvcs=false ./..."
assert_contains "$TMP/go.log" 'GO vet ./...'
assert_contains "$TMP/go.log" 'GO mod tidy -diff'
assert_contains "$TMP/go.log" 'GO run ./scripts/generate-feature-planes.go -check'
assert_contains "$TMP/buf.log" 'BUF lint'

# A wildcard over a single main package must use scratch output, not dirty its module.
new_repo single-main-metadata
printf '\n// changed metadata\n' >>"$REPO/connectors/nested/go.mod"
git -C "$REPO" add connectors/nested/go.mod
export FAKE_GO_WILDCARD_SINGLE=1
expect_pass "$REPO" --staged
unset FAKE_GO_WILDCARD_SINGLE
assert_contains "$TMP/go.log" "PWD=$REPO/connectors/nested GOWORK=off GO build -buildvcs=false -o "
grep -Eq "GO build -buildvcs=false -o [^[:space:]]+/build\\.[0-9]+\\.all ./\\.\\.\\." "$TMP/go.log" || fail 'wildcard binary output escaped scratch'

# Feature-plane and protobuf checks run for changed inputs, including deletions.
new_repo feature-add
printf 'added feature input\n' >"$REPO/pkg/aipsdk/feature/added.txt"
git -C "$REPO" add pkg/aipsdk/feature/added.txt
expect_pass "$REPO" --staged '' '' '' '' '' 1
assert_contains "$TMP/go.log" 'GO run ./scripts/generate-feature-planes.go -check'
assert_not_contains "$TMP/go.log" 'GO run -trimpath ./scripts/generate-feature-planes.go'
assert_not_contains "$TMP/buf.log" BUF

new_repo feature-delete
git -C "$REPO" rm -q pkg/aipsdk/feature/delete.txt
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" 'GO run ./scripts/generate-feature-planes.go -check'

new_repo feature-generator-source
printf 'package featureplanegen\n' >"$REPO/internal/featureplanegen/generator.go"
git -C "$REPO" add internal/featureplanegen/generator.go
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" 'GO run ./scripts/generate-feature-planes.go -check'

new_repo feature-core-generated
printf '// staged core catalog\n' >>"$REPO/internal/core/largebody/plane_catalog_generated.go"
git -C "$REPO" add internal/core/largebody/plane_catalog_generated.go
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" 'GO run ./scripts/generate-feature-planes.go -check'

new_repo proto-add
printf 'syntax = "proto3";\n' >"$REPO/api/added.proto"
git -C "$REPO" add api/added.proto
expect_pass "$REPO" --staged
assert_contains "$TMP/buf.log" 'BUF lint'
assert_not_contains "$TMP/go.log" 'generate-feature-planes.go'

new_repo proto-delete
git -C "$REPO" rm -q api/delete.proto
expect_pass "$REPO" --staged
assert_contains "$TMP/buf.log" 'BUF lint'

new_repo ci-policy
printf 'name: changed CI\n' >"$REPO/.github/workflows/ci.yml"
git -C "$REPO" add .github/workflows/ci.yml
expect_pass "$REPO" --staged
assert_contains "$TMP/buf.log" 'BUF lint'
assert_contains "$TMP/guards.log" workflows

new_repo script-policy
printf '\n# staged policy trigger\n' >>"$REPO/scripts/quality-checks.sh"
git -C "$REPO" add scripts/quality-checks.sh
expect_pass "$REPO" --staged
assert_contains "$TMP/go.log" 'GO run ./scripts/generate-feature-planes.go -check'
assert_contains "$TMP/buf.log" 'BUF lint'
assert_contains "$TMP/guards.log" workflows

# Empty staged scope does not widen to the root module, while the two cheap
# source guardrails continue to run.
new_repo empty-scope
expect_pass "$REPO" --staged
assert_not_contains "$TMP/gofmt.log" GOFMT
assert_not_contains "$TMP/go.log" 'GO build '
assert_not_contains "$TMP/go.log" 'GO vet '
assert_not_contains "$TMP/go.log" 'GO mod tidy'
assert_not_contains "$TMP/go.log" 'generate-feature-planes.go'
assert_not_contains "$TMP/buf.log" BUF
assert_not_contains "$TMP/guards.log" workflows
assert_cheap_guards

# Formatting, build, vet, tidy, and Git-scope failures must fail the staged gate.
new_repo failures
printf '// staged\n' >>"$REPO/internal/rootpkg/another.go"
git -C "$REPO" add internal/rootpkg/another.go
expect_fail "$REPO" --staged '' 1
assert_contains "$TMP/output" 'gofmt failed on staged Go files'
expect_fail "$REPO" --staged '' '' 'internal/rootpkg/another.go'
assert_contains "$TMP/output" 'Unformatted files:'
expect_fail "$REPO" --staged build
assert_contains "$TMP/output" 'build failed in staged module'
expect_fail "$REPO" --staged vet
assert_contains "$TMP/output" 'vet failed in staged module'
expect_fail "$REPO" --staged tidy
assert_contains "$TMP/output" 'go.mod/go.sum drift detected'
expect_fail "$REPO" --staged '' '' '' 1
assert_contains "$TMP/output" 'unable to read staged paths from Git'

# No-argument invocation retains the comprehensive whole-tree checks.
new_repo standalone
expect_pass "$REPO" ""
assert_contains "$TMP/gofmt.log" 'GOFMT -l .'
assert_contains "$TMP/go.log" 'GO mod tidy'
assert_contains "$TMP/go.log" 'GO build ./...'
assert_contains "$TMP/go.log" 'GO vet ./...'
assert_contains "$TMP/go.log" 'GO run ./scripts/generate-feature-planes.go -check'
assert_contains "$TMP/buf.log" 'BUF lint'
assert_contains "$TMP/guards.log" workflows

# Mandatory staged/direct lint shares the archtest trimpath compiler variant
# with the preceding local build and test checks, while other scopes retain the
# caller's GOFLAGS and the same analyzer settings.
new_lint_fixture
LINT_MIXED_PLAN=$'.\t./internal/archtest ./internal/rootpkg\n'
if ! run_lint_fixture "$LINT_MIXED_PLAN" 1 '' --staged --direct; then
	fail "trimpath lint fixture unexpectedly failed"
fi
assert_count "$TMP/lint.log" 'CMD=golangci-lint' 2
assert_contains "$TMP/lint.log" "PWD=$LINT_REPO|GOFLAGS=-tags=persisted -mod=readonly -trimpath=true|CMD=golangci-lint run --allow-parallel-runners --concurrency=2 --disable=modernize,paralleltest,thelper,gocognit ./internal/archtest"
assert_contains "$TMP/lint.log" "PWD=$LINT_REPO|GOFLAGS=-tags=caller|CMD=golangci-lint run --allow-parallel-runners --concurrency=2 --disable=modernize,paralleltest,thelper,gocognit ./internal/rootpkg"
assert_count "$TMP/lint-go.log" 'env GOFLAGS' 1

# The advisory report is uncapped, includes integration/precommit-tagged
# files, and stays non-blocking when it finds style debt.
if ! run_lint_fixture "$LINT_MIXED_PLAN" 1 '' --staged --advisory; then
	fail "advisory report fixture unexpectedly failed"
fi
assert_count "$TMP/lint.log" 'CMD=golangci-lint' 1
assert_contains "$TMP/lint.log" 'CMD=golangci-lint run --allow-parallel-runners --concurrency=2 --build-tags=integration,precommit --max-issues-per-linter=0 --max-same-issues=0 --issues-exit-code=0 ./internal/archtest ./internal/rootpkg'
assert_not_contains "$TMP/lint.log" '--disable='

# The changed-lines ratchet keeps the same uncapped/tagged report but limits it
# to lines changed since the supplied base and fails on a new finding.
if ! run_lint_fixture "$LINT_MIXED_PLAN" 1 '' --advisory --ratchet --base HEAD; then
	fail "advisory ratchet fixture unexpectedly failed"
fi
assert_count "$TMP/lint.log" 'CMD=golangci-lint' 1
assert_contains "$TMP/lint.log" 'CMD=golangci-lint run --allow-parallel-runners --concurrency=2 --build-tags=integration,precommit --max-issues-per-linter=0 --max-same-issues=0 --new-from-rev=HEAD ./internal/archtest ./internal/rootpkg'
assert_not_contains "$TMP/lint.log" '--issues-exit-code=0'
assert_contains "$TMP/lint-go.log" '-mode base -base HEAD'
if run_lint_fixture "$LINT_MIXED_PLAN" 1 new-from-rev --advisory --ratchet --base HEAD; then
	fail "advisory ratchet unexpectedly passed with a new finding"
fi
assert_contains "$TMP/lint.log" '--new-from-rev=HEAD'

if ! run_lint_fixture "$LINT_MIXED_PLAN" 1 '' --staged --advisory --ratchet --ratchet-base HEAD; then
	fail "staged advisory ratchet fixture unexpectedly failed"
fi
assert_count "$TMP/lint.log" 'CMD=golangci-lint' 1
assert_contains "$TMP/lint.log" '--new-from-rev=HEAD'
assert_contains "$TMP/lint-go.log" '-mode staged'

if run_lint_fixture "$LINT_MIXED_PLAN" 1 '' --advisory --ratchet; then
	fail "advisory ratchet unexpectedly passed without a base"
fi
assert_contains "$TMP/lint-output" '--ratchet requires --ratchet-base'

# Broad, nested, non-direct, and non-opted-in scopes keep one original linter
# invocation without reading or changing GOFLAGS.
if ! run_lint_fixture $'.\t./internal/... ./internal/archtest\n' 1 '' --staged --direct; then
	fail "broad lint fixture unexpectedly failed"
fi
assert_count "$TMP/lint.log" 'CMD=golangci-lint' 1
assert_contains "$TMP/lint.log" 'GOFLAGS=-tags=caller|CMD=golangci-lint'
assert_not_contains "$TMP/lint.log" 'GOFLAGS=-tags=persisted'
assert_not_contains "$TMP/lint-go.log" 'env GOFLAGS'

if ! run_lint_fixture $'connectors/local\t./internal/archtest\n' 1 '' --staged --direct; then
	fail "nested lint fixture unexpectedly failed"
fi
assert_count "$TMP/lint.log" 'CMD=golangci-lint' 1
assert_contains "$TMP/lint.log" 'GOFLAGS=-tags=caller|CMD=golangci-lint'
assert_not_contains "$TMP/lint-go.log" 'env GOFLAGS'

if ! run_lint_fixture "$LINT_MIXED_PLAN" 1 '' --changed --direct; then
	fail "non-staged lint fixture unexpectedly failed"
fi
assert_count "$TMP/lint.log" 'CMD=golangci-lint' 1
assert_contains "$TMP/lint.log" 'GOFLAGS=-tags=caller|CMD=golangci-lint'
assert_not_contains "$TMP/lint-go.log" 'env GOFLAGS'

if ! run_lint_fixture "$LINT_MIXED_PLAN" 0 '' --staged --direct; then
	fail "default lint fixture unexpectedly failed"
fi
assert_count "$TMP/lint.log" 'CMD=golangci-lint' 1
assert_contains "$TMP/lint.log" 'GOFLAGS=-tags=caller|CMD=golangci-lint'
assert_not_contains "$TMP/lint-go.log" 'env GOFLAGS'

# A failure in the first group propagates through xargs' exported function
# subprocess and prevents the second group from running.
if run_lint_fixture "$LINT_MIXED_PLAN" 1 archtest --staged --direct; then
	fail "failing first lint group unexpectedly passed"
fi
assert_count "$TMP/lint.log" 'CMD=golangci-lint' 1
assert_contains "$TMP/lint.log" './internal/archtest'
assert_not_contains "$TMP/lint.log" './internal/rootpkg'

if run_lint_fixture $'.\t./internal/rootpkg ./internal/archtest\n' 1 rootpkg --staged --direct; then
	fail "failing first ordinary lint group unexpectedly passed"
fi
assert_count "$TMP/lint.log" 'CMD=golangci-lint' 1
assert_contains "$TMP/lint.log" './internal/rootpkg'
assert_not_contains "$TMP/lint.log" './internal/archtest'

if FAKE_GO_ENV_FAIL=1 run_lint_fixture "$LINT_MIXED_PLAN" 1 '' --staged --direct; then
	fail "GOFLAGS lookup failure unexpectedly passed"
fi
assert_contains "$TMP/lint-output" 'unable to read effective GOFLAGS for trimmed archtest lint'
assert_count "$TMP/lint.log" 'CMD=golangci-lint' 0

# Verify the exact Go build semantics used by staged scope: a single non-main
# package accepts -o to a scratch file, while multiple packages build without
# -o and therefore create no executables in the module. GOPROXY=off keeps this
# probe independent of network access.
PROBE="$TMP/real-go-build-probe"
mkdir -p "$PROBE/first" "$PROBE/second"
cat >"$PROBE/go.mod" <<'EOF'
module example.test/build-probe

go 1.24
EOF
printf 'package first\nconst Value = 1\n' >"$PROBE/first/first.go"
printf 'package second\nconst Value = 2\n' >"$PROBE/second/second.go"
(
	cd "$PROBE"
	GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off \
		"$REAL_GO" build -buildvcs=false -o "$PROBE/single-package.a" ./first
	test -f "$PROBE/single-package.a"
	GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off \
		"$REAL_GO" build -buildvcs=false ./first ./second
)

mkdir -p "$PROBE/single-main"
printf 'module example.test/single-main\n\ngo 1.24\n' >"$PROBE/single-main/go.mod"
printf 'package main\nfunc main() {}\n' >"$PROBE/single-main/main.go"
(
	cd "$PROBE/single-main"
	GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off \
		"$REAL_GO" build -buildvcs=false -o "$TMP/single-main-output" ./...
	test -f "$TMP/single-main-output"
	test ! -e single-main
)

echo 'OK: staged quality-check behavior and standalone compatibility'
