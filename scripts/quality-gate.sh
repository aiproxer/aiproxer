#!/usr/bin/env bash
# Pre-commit quality gate. The default is fast: build, vet, tests and lint of
# the staged packages only. CI (ci.yml "Go suite" and
# "Lint") runs the complete tagged suite and lint on every PR, so the hook does
# not repeat them. AIP_PRECOMMIT_FULL=1 or `make precommit-full` restores the
# complete local gate: whole root suite, certification, full lint, govulncheck.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/dev-cpu-defaults.sh"

echo "=== Pre-Commit Quality Gate ==="
echo ""

staged_files="$(git diff --cached --no-renames --name-only --diff-filter=ACMRD)"
if grep -qE '^(Makefile$|scripts/|\.github/workflows/|\.githooks/)' <<< "$staged_files"; then
	echo "Checking known automation self-tests and consumer contracts..."
	go run ./tools/devcheck -task=contracts -scope=changed -base=HEAD -automation-only
fi
if ! grep -qE '\.go$' <<< "$staged_files"; then
	if grep -qE '(^|/)(go\.mod|go\.sum)$' <<< "$staged_files"; then
		echo "No staged Go source files detected; checking module metadata."
	else
		# Spec bookkeeping can still break a repository invariant on its own:
		# marking a spec completed without archiving it fails the Kiro
		# lifecycle contract, and no Go test would notice. Always validate the
		# cheap spec/ownership contracts here rather than letting a
		# docs-only commit bypass every gate.
		if grep -qE '^\.kiro/specs/' <<< "$staged_files"; then
			echo "No staged Go files; validating Kiro spec and ownership contracts."
			echo ""
			# go run is not a heavy Go-guard command, so this never queues for a slot.
			go run ./tools/kiro/kirocheck
			# speccheck only certifies the specs it registers; skip it otherwise.
			speccheck_specs="$(sed -nE 's/.*\{name: "([^"]+)".*/\1/p' tools/kiro/speccheck/*_test.go | paste -sd'|' -)"
			if grep -qE "^tools/kiro/speccheck/|^\.kiro/specs/(archive/)?(${speccheck_specs:-^})/" <<< "$staged_files"; then
				go test -count=1 ./tools/kiro/speccheck/
			fi
			exit 0
		fi
		echo "No staged Go files or module metadata detected; skipping quality gate checks."
		exit 0
	fi
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Fail fast on the one environmental precondition that makes the suites below
# report misleading failures. On a tmpfs TMPDIR the config-source integrity tests
# fail with 'source-integrity-failed' / 'source_non_atomic_update' across several
# unrelated packages, which reads as a regression in the staged change. Checking
# here costs milliseconds and names the real cause once.
bash "$SCRIPT_DIR/require-ext4-tmpdir.sh"

if grep -Eq '(^|/)(go\.mod|go\.sum)$' <<< "$staged_files"; then
	# Root dependencies can affect every independent module. Nested metadata
	# belongs to that module and is checked by the staged quality pass below.
	if [[ "${AIP_PRECOMMIT_FULL:-}" == "1" ]] || grep -Eq '^(go\.mod|go\.sum)$' <<< "$staged_files"; then
		echo "Checking all independent Go module metadata..."
		bash "$SCRIPT_DIR/tidy-all-modules.sh" --check
		echo ""
	fi
fi

if [[ "${AIP_PRECOMMIT_FULL:-}" != "1" ]]; then
	# Normalize root-module source paths only for local fast archtest scopes so
	# compiler entries can be reused between developer worktrees. Keep this off
	# in CI and full precommit, and honor an explicit local opt-out.
	local_ci=false
	for value in "${CI:-}" "${GITHUB_ACTIONS:-}"; do
		case "$value" in
			1|true|TRUE|yes|YES|on|ON) local_ci=true ;;
		esac
	done
	if [[ "$local_ci" == false && -z "${AIP_LOCAL_ARCH_TRIMPATH+x}" ]]; then
		export AIP_LOCAL_ARCH_TRIMPATH=1
	fi

	echo "Running quality checks on staged packages..."
	# Lint runs once, scoped, at the end of this gate.
	AIP_SKIP_LINT=1 AIP_SKIP_ARCHTEST=1 bash "$SCRIPT_DIR/quality-checks.sh" --staged
	if grep -qE '^connectors/.*\.go$' <<< "$staged_files"; then
		echo "Checking connector host import boundary (including test-only changes)..."
		bash "$SCRIPT_DIR/run-named-go-tests.sh" ./internal/archtest '^TestConnectorSubtree_CannotImportHostSecurityAuthority$' -mod=readonly -trimpath
	fi

	echo ""
	# Test only the packages whose files are staged. Nearly every package
	# reaches the repository-wide suites (archtest, qa, runtime, runtimebundle)
	# through reverse dependencies, so a reverse-dependency scope costs about
	# as much as the full suite; CI's Go suite owns those consumers.
	declare -A test_packages=()
	# Staged tests run in devcheck summary mode: the full `go test -json`
	# stream (thousands of === RUN lines for broad packages) stays in the
	# evidence logs under the dev temp root, while the hook prints only a
	# per-module summary line and the evidence path. A failure still prints
	# the failing package/test names and an output excerpt, never just a path.
	aip_dev_scratch="${AIP_DEV_TMPDIR:-$HOME/.cache/aip-tmp}"
	mkdir -p "$aip_dev_scratch"
	shared_test_scope="$(go run -buildvcs=false ./tools/localscope -mode staged -metadata=false -format lines)"
	while IFS=$'\t' read -r module packages; do
		[[ -n "$module" && -n "$packages" ]] || continue
		test_packages["$module"]="$packages"
	done <<< "$shared_test_scope"
	if [[ ${#test_packages[@]} -eq 0 ]]; then
		echo "No staged Go packages to test."
	fi

	# Hook-local Git pins must not bind temporary-repository fixtures to the
	# caller, so the tests run in a subshell without them.
	(
		mapfile -t git_local_env < <(git rev-parse --local-env-vars)
		for name in "${git_local_env[@]}"; do
			unset "$name"
		done
		for module in "${!test_packages[@]}"; do
			packages=$(tr ' ' '\n' <<< "${test_packages[$module]}" | sed '/^$/d' | sort -u | tr '\n' ' ')
			echo "Testing staged packages in module $module (CI runs the complete suite): $packages"
			skip_test_args=()
			if [[ "$module" == "." && " $packages " == *" ./internal/infra/runtimebundle "* ]]; then
				skip_test_args=(-skip-test=TestRuntimebundle_NoCompleteOwnerCallbackEscapes)
				echo "Fast staged hook excludes TestRuntimebundle_NoCompleteOwnerCallbackEscapes; full precommit and dedicated CI still run the whole-module owner-callback gate."
			fi
			module_slug="$(printf '%s' "$module" | tr -c '[:alnum:]' '_')"
			evidence_file="${aip_dev_scratch}/precommit-staged-test-$(date +%Y%m%d-%H%M%S)-$$-${module_slug}.json"
			if go run -buildvcs=false ./tools/devcheck -task=test -module="$module" -packages="$packages" "${skip_test_args[@]}" -output=summary -evidence="$evidence_file"; then
				echo "Staged tests passed in module $module; evidence: $evidence_file"
			else
				test_status=$?
				echo "Staged tests FAILED in module $module; evidence: $evidence_file" >&2
				exit "$test_status"
			fi
		done
	)

	if [[ "$(go env GOOS)" == "linux" ]] && grep -qE '^(internal/infra/(configsource|runtimehost)/|scripts/(test-)?configsource-)' <<< "$staged_files"; then
		echo ""
		echo "Running ext4 source-lifetime certification (config-source change staged)..."
		bash "$SCRIPT_DIR/configsource-certify.sh"
		bash "$SCRIPT_DIR/test-configsource-fault-check.sh"
		bash "$SCRIPT_DIR/configsource-fault-check.sh"
	fi
else
	echo "Running quality checks..."
	# The following cached root test owns compilation, curated vet, and the
	# architecture package; avoid repeating those expensive Go phases in the hook.
	AIP_SKIP_GO_COMPILE_CHECKS=1 AIP_SKIP_ARCHTEST=1 bash "$SCRIPT_DIR/quality-checks.sh"

	echo ""
	echo "Running complete root test suite with precommit tags (Go cache enabled)..."
	env AIP_TEST_PRECOMMIT=1 bash "$SCRIPT_DIR/test-staged.sh"

	if [[ "$(go env GOOS)" == "linux" ]]; then
		echo ""
		echo "Running mandatory ext4 source-lifetime certification..."
		bash "$SCRIPT_DIR/configsource-certify.sh"
		echo ""
		echo "Running mandatory source-ownership fault lifecycle tests..."
		bash "$SCRIPT_DIR/test-configsource-fault-check.sh"
		bash "$SCRIPT_DIR/configsource-fault-check.sh"
	fi
fi

# The race detector is owned by remote CI: nightly race/fuzz
# (.github/workflows/race-fuzz-nightly.yml), the connector race workflows, and
# the release gate. Local commits do not run it, because a full -race scan of
# the billing, runtime, and runtimebundle suites takes hours on a developer
# machine and has repeatedly starved the rest of this gate of its time budget.
# Set AIP_PRECOMMIT_RACE=1 to opt a local commit back into the staged scan.
if [[ "${AIP_PRECOMMIT_RACE:-}" == "1" ]]; then
	echo ""
	echo "Running race detector scan..."
	bash "$SCRIPT_DIR/race-check.sh" --staged
else
	echo ""
	echo "Skipping race detector scan (remote CI owns it; AIP_PRECOMMIT_RACE=1 to opt in)."
fi

if [[ "${AIP_SKIP_LINT:-}" != "1" ]]; then
	echo ""
	if [[ "${AIP_PRECOMMIT_FULL:-}" == "1" ]]; then
		echo "Running complete multi-module linter across all modules (precommit-full)..."
		bash "$SCRIPT_DIR/lint-all-modules.sh"
	else
		# Staged packages only: consumers are linted by CI's Lint job.
		echo "Running multi-module linter on staged packages (CI lints their consumers)..."
		bash "$SCRIPT_DIR/lint-all-modules.sh" --staged --direct
	fi
else
	echo ""
	echo "Skipping linter (AIP_SKIP_LINT=1)."
fi

if [[ "${AIP_PRECOMMIT_FULL:-}" == "1" && "${AIP_SKIP_VULN:-}" != "1" ]]; then
	echo ""
	echo "Running govulncheck (precommit-full)..."
	if command -v govulncheck >/dev/null 2>&1; then
		govulncheck ./...
	else
		go tool govulncheck ./...
	fi
else
	echo ""
	echo "Skipping govulncheck in fast pre-commit mode (opt in: AIP_PRECOMMIT_FULL=1 or 'make precommit-full'; CI runs it anyway)."
fi

echo ""
echo "=== Quality Gate Passed ==="
exit 0
