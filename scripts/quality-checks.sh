#!/usr/bin/env bash
# quality-checks.sh
# Fast quality checks before tests. Order: fastest to slowest, fail-fast.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/dev-cpu-defaults.sh"

QUALITY_MODE=full
if [[ $# -eq 1 && "$1" == "--staged" ]]; then
	QUALITY_MODE=staged
elif [[ $# -ne 0 ]]; then
	echo "usage: $0 [--staged]" >&2
	exit 2
fi

QUALITY_TMPDIR="$(mktemp -d "${TMPDIR:-/tmp}/aip-quality.XXXXXX")"
cleanup_quality_tmp() {
	if [[ -n "${QUALITY_TMPDIR:-}" ]]; then
		rm -rf "$QUALITY_TMPDIR"
	fi
}
trap cleanup_quality_tmp EXIT

# Test parallelism defaults to the machine's logical core count; override with
# AIP_TEST_PARALLEL=<n> (mirrors GO_TEST_FLAGS in the Makefile).
test_parallel="${AIP_TEST_PARALLEL:-}"
if [ -z "$test_parallel" ]; then
	test_parallel=$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 8)
fi
test_package_flags=()
if [ -n "${AIP_TEST_PACKAGES:-}" ]; then
	if [[ ! "$AIP_TEST_PACKAGES" =~ ^[1-9][0-9]*$ ]]; then
		echo "AIP_TEST_PACKAGES must be a positive integer" >&2
		exit 2
	fi
	test_package_flags=("-p=$AIP_TEST_PACKAGES")
fi

is_agent_skill_path() {
	case "$1" in
		.agents/skills/*|.codex/skills/*|.cursor/skills/*|.kiro/skills/*|.opencode/skills/*|.pi/skills/*)
			return 0
			;;
	esac
	return 1
}

collect_quality_packages() {
	local plan module packages package found=false
	plan="$(go run -buildvcs=false ./tools/localscope -mode changed -metadata=false -format lines)" || return 1
	while IFS=$'\t' read -r module packages; do
		[[ "$module" == . ]] || continue
		for package in $packages; do
			found=true
			if [[ "$package" == . ]]; then printf './...\n'; return 0; fi
			printf '%s/...\n' "$package"
		done
	done <<< "$plan"
	if [[ "$found" == false ]]; then printf './...\n'; fi
}

declare -a STAGED_PATHS=() STAGED_GO_FILES_EXISTING=() STAGED_MODULES=() STAGED_TIDY_MODULES=()
declare -A STAGED_MODULE_INDEX=() STAGED_TIDY_SEEN=() STAGED_MODULE_ALL=() STAGED_DELETED_MODULES=()
STAGED_FEATURE_PLANES=false
STAGED_PROTOBUF=false
STAGED_WORKFLOWS=false

add_staged_tidy_module() {
	local module="$1"
	[[ -f "$module/go.mod" ]] || return 0
	if [[ -z "${STAGED_TIDY_SEEN[$module]+x}" ]]; then
		STAGED_TIDY_SEEN["$module"]=1
		STAGED_TIDY_MODULES+=("$module")
	fi
}

add_staged_package() {
	local module="$1" package="$2" module_index package_file existing
	if [[ -n "${STAGED_MODULE_ALL[$module]+x}" ]]; then
		return 0
	fi
	if [[ -z "${STAGED_MODULE_INDEX[$module]+x}" ]]; then
		module_index=${#STAGED_MODULES[@]}
		STAGED_MODULE_INDEX["$module"]="$module_index"
		STAGED_MODULES+=("$module")
		package_file="$QUALITY_TMPDIR/packages.$module_index"
		: >"$package_file"
	fi
	module_index="${STAGED_MODULE_INDEX[$module]}"
	package_file="$QUALITY_TMPDIR/packages.$module_index"
	while IFS= read -r -d '' existing; do
		[[ "$existing" == "$package" ]] && return 0
	done <"$package_file"
	printf '%s\0' "$package" >>"$package_file"
}

collect_staged_scope() {
	local file kind first second
	if ! go run -buildvcs=false ./tools/localscope -mode staged -format nul >"$QUALITY_TMPDIR/shared-scope"; then
		echo "ERROR: unable to read staged paths from Git or plan quality scope" >&2
		return 1
	fi
	while IFS= read -r -d '' kind && IFS= read -r -d '' first && IFS= read -r -d '' second; do
		case "$kind" in
			path) STAGED_PATHS+=("$first") ;;
			file) STAGED_GO_FILES_EXISTING+=("$first") ;;
			tidy) add_staged_tidy_module "$first" ;;
			package) add_staged_package "$first" "$second"; if [[ "$second" == ./... ]]; then STAGED_MODULE_ALL["$first"]=1; fi ;;
			*) echo "ERROR: invalid shared scope record $kind" >&2; return 1 ;;
		esac
	done <"$QUALITY_TMPDIR/shared-scope"
	for file in "${STAGED_PATHS[@]}"; do
		case "$file" in
			pkg/aipsdk/feature/*|internal/archtest/*|internal/featureplanegen/*|internal/core/largebody/plane_catalog_generated.go|scripts/generate-feature-planes.go|go.mod|go.sum|scripts/quality-checks.sh)
				STAGED_FEATURE_PLANES=true
				;;
		esac
		case "$file" in
			api/*|go.mod|go.sum|scripts/proto-check.sh|scripts/quality-checks.sh|.github/workflows/ci.yml)
				STAGED_PROTOBUF=true
				;;
		esac
		case "$file" in
			.github/workflows/*|scripts/check-workflows.sh|scripts/quality-checks.sh)
				STAGED_WORKFLOWS=true
				;;
		esac

	done
}

if [[ "$QUALITY_MODE" == "staged" ]]; then
	REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || {
		echo "ERROR: staged checks require a Git worktree" >&2
		exit 1
	}
	cd "$REPO_ROOT"
	if ! collect_staged_scope; then
		exit 1
	fi
	QUALITY_PACKAGES=()
	for module in "${STAGED_MODULES[@]}"; do
		module_index="${STAGED_MODULE_INDEX[$module]}"
		mapfile -d '' -t packages <"$QUALITY_TMPDIR/packages.$module_index"
		QUALITY_PACKAGES+=("$module:${packages[*]}")
	done
	if [[ ${#QUALITY_PACKAGES[@]} -eq 0 ]]; then
		QUALITY_SCOPE="no staged Go packages"
	else
		QUALITY_SCOPE="${QUALITY_PACKAGES[*]}"
	fi
else
	if ! collect_quality_packages >"$QUALITY_TMPDIR/quality-packages"; then
		exit 1
	fi
	mapfile -t QUALITY_PACKAGES <"$QUALITY_TMPDIR/quality-packages"
	QUALITY_SCOPE="${QUALITY_PACKAGES[*]}"
fi

echo "Quality scope: $QUALITY_SCOPE"
echo ""
echo "=== Quality Checks ==="
echo ""

staged_root_arch_scope_allows_generator_trimpath() {
	[[ "$QUALITY_MODE" == "staged" && "${AIP_LOCAL_ARCH_TRIMPATH:-}" == "1" ]] || return 1
	local module_index="${STAGED_MODULE_INDEX[.]:-}" package
	[[ -n "$module_index" ]] || return 1
	local -a packages=()
	mapfile -d '' -t packages <"$QUALITY_TMPDIR/packages.$module_index"
	local has_arch_scope=false
	for package in "${packages[@]}"; do
		case "$package" in
			./...|./internal/...)
				return 1
				;;
			./internal/archtest|./internal/archtest/*)
				has_arch_scope=true
				;;
		esac
	done
	[[ "$has_arch_scope" == true ]]
}

if [[ "$QUALITY_MODE" != "staged" || "$STAGED_FEATURE_PLANES" == true ]]; then
	echo "[1/8] Checking generated feature planes..."
	# The generator and archtest share featureplanegen, so match the scoped arch
	# build variant when staged packages explicitly include that root package.
	feature_generator_trim_flags=()
	if staged_root_arch_scope_allows_generator_trimpath; then
		feature_generator_trim_flags=(-trimpath)
	fi
	if ! go run "${feature_generator_trim_flags[@]}" ./scripts/generate-feature-planes.go -check; then
		echo "ERROR: Feature planes generation check failed"
		exit 1
	fi
	echo "OK: Generated feature planes check passed"
else
	echo "Skipping generated feature planes: no staged generator inputs changed."
fi
echo ""

echo "[2/8] Checking Go formatting..."
if [[ "$QUALITY_MODE" == "staged" ]]; then
	if [[ ${#STAGED_GO_FILES_EXISTING[@]} -eq 0 ]]; then
		unformatted=""
		echo "Skipping gofmt: no staged Go files exist in the worktree."
	else
		if ! unformatted=$(gofmt -l "${STAGED_GO_FILES_EXISTING[@]}" 2>"$QUALITY_TMPDIR/gofmt.err"); then
			cat "$QUALITY_TMPDIR/gofmt.err" >&2
			echo "ERROR: gofmt failed on staged Go files" >&2
			exit 1
		fi
	fi
else
	unformatted=$(gofmt -l . 2>/dev/null | while IFS= read -r file; do
		file=${file//\\//}
		if ! is_agent_skill_path "$file"; then
			printf '%s\n' "$file"
		fi
	done || true)
fi
if [ -n "$unformatted" ]; then
	echo "Unformatted files:"
	echo "$unformatted"
	echo "Run: gofmt -w <files> or go fmt ./..."
	exit 1
fi
echo "OK: Format check passed"
echo ""

echo "[3/8] Checking Go modules..."
if [[ "$QUALITY_MODE" == "staged" ]]; then
	if [[ ${#STAGED_TIDY_MODULES[@]} -eq 0 ]]; then
		echo "Skipping module tidy: no staged Go source or module metadata changes."
	else
		for module in "${STAGED_TIDY_MODULES[@]}"; do
			echo "Checking module tidy in $module..."
			if ! (cd "$module" && GOWORK=off go mod tidy -diff); then
				echo "ERROR: go.mod/go.sum drift detected in module $module" >&2
				exit 1
			fi
		done
	fi
else
	pre_tidy_mod=$(git hash-object go.mod 2>/dev/null || printf 'missing-go-mod')
	pre_tidy_sum=$(git hash-object go.sum 2>/dev/null || printf 'missing-go-sum')
	go mod tidy
	post_tidy_mod=$(git hash-object go.mod 2>/dev/null || printf 'missing-go-mod')
	post_tidy_sum=$(git hash-object go.sum 2>/dev/null || printf 'missing-go-sum')
	if [ "$pre_tidy_mod" != "$post_tidy_mod" ] || [ "$pre_tidy_sum" != "$post_tidy_sum" ]; then
		tidy_changes=$(git diff --name-only go.mod go.sum 2>/dev/null || true)
		echo "ERROR: go.mod/go.sum modified by 'go mod tidy'"
		if [ -n "$tidy_changes" ]; then
			echo "Changes detected:"
			echo "$tidy_changes"
		fi
		echo "Run: go mod tidy && git add go.mod go.sum"
		exit 1
	fi
fi
should_verify_module_cache=false
case "${AIP_VERIFY_MODULE_CACHE:-}" in
	1|true|TRUE|yes|YES|on|ON)
		should_verify_module_cache=true
		;;
esac
if [ "$should_verify_module_cache" = false ]; then
	case "${CI:-}" in
		1|true|TRUE|yes|YES|on|ON)
			should_verify_module_cache=true
			;;
	esac
fi
if [ "$should_verify_module_cache" = true ]; then
	echo "Verifying module checksums..."
	if ! go mod verify; then
		echo "ERROR: go mod verify failed (checksum mismatch or corrupt module cache)"
		exit 1
	fi
else
	echo "Skipping module cache verification locally (set AIP_VERIFY_MODULE_CACHE=1 to enable)."
fi
echo "OK: Module check passed"
echo ""

script_dir="$SCRIPT_DIR"

check_staged_module_packages() {
	local module="$1" module_index="$2" group_name="$3" trim_arch="$4"
	shift 4
	local -a packages=("$@")
	local -a trim_flags=()
	if [[ "$trim_arch" == true ]]; then
		trim_flags=(-trimpath)
	fi

	if [[ ${#packages[@]} -eq 1 && "${packages[0]}" == "./..." ]]; then
		local listed_file="$QUALITY_TMPDIR/wildcard-packages.$module_index.$group_name"
		if ! (cd "$module" && GOWORK=off go list -f '{{.ImportPath}}' ./...) >"$listed_file"; then
			echo "ERROR: unable to list staged module $module" >&2
			exit 1
		fi
		local -a listed_packages=() wildcard_output=()
		mapfile -t listed_packages <"$listed_file"
		if [[ ${#listed_packages[@]} -eq 1 ]]; then
			# A wildcard resolving to one main package otherwise writes a binary
			# into the checked worktree. Library archives accept this file too.
			wildcard_output=(-o "$QUALITY_TMPDIR/build.$module_index.$group_name")
		fi
		if ! (cd "$module" && GOWORK=off go build -buildvcs=false "${trim_flags[@]}" "${wildcard_output[@]}" ./...); then
			echo "ERROR: build failed in staged module $module" >&2
			exit 1
		fi
	else
		local build_packages_file="$QUALITY_TMPDIR/build-packages.$module_index.$group_name" build_output_path
		if ! (cd "$module" && GOWORK=off go list -f '{{if or .GoFiles .CgoFiles}}{{.ImportPath}}{{end}}' "${packages[@]}") >"$build_packages_file"; then
			echo "ERROR: unable to list staged packages in module $module" >&2
			exit 1
		fi
		local -a build_packages=()
		mapfile -t build_packages <"$build_packages_file"
		local -a filtered_build_packages=()
		local package
		for package in "${build_packages[@]}"; do
			if [[ -n "$package" ]]; then
				filtered_build_packages+=("$package")
			fi
		done
		if [[ ${#filtered_build_packages[@]} -eq 0 ]]; then
			echo "Skipping build: staged package has no production Go files."
		else
			# A file accepts either a library archive or one executable.
			# With multiple packages Go discards outputs; an output directory
			# would instead force linking all main packages and reject libraries.
			local -a build_output=()
			if [[ ${#filtered_build_packages[@]} -eq 1 ]]; then
				build_output_path="$QUALITY_TMPDIR/build.$module_index"
				if [[ "$group_name" != all ]]; then
					build_output_path+=".$group_name"
				fi
				build_output=(-o "$build_output_path")
			fi
			if ! (cd "$module" && GOWORK=off go build -buildvcs=false "${trim_flags[@]}" "${build_output[@]}" "${filtered_build_packages[@]}"); then
				echo "ERROR: build failed in staged module $module" >&2
				exit 1
			fi
		fi
	fi

	if ! (cd "$module" && GOWORK=off go vet "${trim_flags[@]}" "${packages[@]}"); then
		echo "ERROR: vet failed in staged module $module" >&2
		exit 1
	fi
}

if [ "${AIP_SKIP_GO_COMPILE_CHECKS:-}" = "1" ]; then
	echo "Skipping standalone build/vet: the following go test target owns compilation and curated vet checks."
elif [[ "$QUALITY_MODE" == "staged" ]]; then
	if [[ ${#STAGED_MODULES[@]} -eq 0 ]]; then
		echo "Skipping build/vet: no staged Go package or module metadata scope."
	else
		for module in "${STAGED_MODULES[@]}"; do
			module_index="${STAGED_MODULE_INDEX[$module]}"
			mapfile -d '' -t packages <"$QUALITY_TMPDIR/packages.$module_index"
			printf '=== Staged module %s: %s ===\n' "$module" "${packages[*]}"
			if [[ "$module" == "." && "${AIP_LOCAL_ARCH_TRIMPATH:-}" == "1" ]]; then
				broad_arch_scope=false
				for package in "${packages[@]}"; do
					if [[ "$package" == "./..." || "$package" == "./internal/..." ]]; then
						broad_arch_scope=true
						break
					fi
				done
				if [[ "$broad_arch_scope" == true ]]; then
					check_staged_module_packages "$module" "$module_index" all false "${packages[@]}"
					continue
				fi
				declare -a arch_packages=() other_packages=()
				for package in "${packages[@]}"; do
					case "$package" in
						./internal/archtest|./internal/archtest/*)
							arch_packages+=("$package")
							;;
						*)
							other_packages+=("$package")
							;;
					esac
				done
				if [[ ${#arch_packages[@]} -gt 0 ]]; then
					if [[ "${packages[0]}" == "./internal/archtest" || "${packages[0]}" == ./internal/archtest/* ]]; then
						check_staged_module_packages "$module" "$module_index" arch true "${arch_packages[@]}"
						if [[ ${#other_packages[@]} -gt 0 ]]; then
							check_staged_module_packages "$module" "$module_index" other false "${other_packages[@]}"
						fi
					else
						if [[ ${#other_packages[@]} -gt 0 ]]; then
							check_staged_module_packages "$module" "$module_index" other false "${other_packages[@]}"
						fi
						check_staged_module_packages "$module" "$module_index" arch true "${arch_packages[@]}"
					fi
				else
					check_staged_module_packages "$module" "$module_index" all false "${packages[@]}"
				fi
			else
				check_staged_module_packages "$module" "$module_index" all false "${packages[@]}"
			fi
		done
	fi
else
	echo "[4/8] Checking build..."
	if ! go build "${QUALITY_PACKAGES[@]}"; then
		echo "ERROR: Build failed"
		exit 1
	fi
	echo "OK: Build check passed"
	echo ""

	echo "[5/8] Running go vet..."
	if ! go vet "${QUALITY_PACKAGES[@]}"; then
		echo "ERROR: go vet failed"
		exit 1
	fi
	echo "OK: Vet check passed"
	echo ""
fi

echo "[6-8/8] Running independent guardrails in parallel..."
guard_tmp="$QUALITY_TMPDIR/guards"
mkdir -p "$guard_tmp"

declare -A guard_pids=( )
run_guard() {
	local name="$1"
	shift
	"$@" >"$guard_tmp/${name}.log" 2>&1 &
	guard_pids["$name"]=$!
}

run_guard adhoc bash "$script_dir/check-adhoc-goroutines.sh"
run_guard regex bash "$script_dir/regex-hotpath-check.sh"
if [[ "$QUALITY_MODE" != "staged" || "$STAGED_PROTOBUF" == true ]]; then
	run_guard protobuf bash "$script_dir/proto-check.sh"
else
	echo "Skipping protobuf checks: no staged protobuf or pinned-tool inputs changed."
fi
if [[ "$QUALITY_MODE" != "staged" || "$STAGED_WORKFLOWS" == true ]]; then
	run_guard workflows bash "$script_dir/check-workflows.sh"
else
	echo "Skipping workflow lint: no staged workflow files changed."
fi
if [ "${AIP_SKIP_LINT:-}" != "1" ]; then
	run_guard lint bash "$script_dir/lint-all-modules.sh" --changed
fi
if [ "${AIP_SKIP_ARCHTEST:-}" != "1" ]; then
	# Match the test-unit flags (make GO_TEST_FLAGS) so the standalone
	# quality-checks archtest run shares Go's build/test cache with
	# subsequent `make test`/`make qa` executions (see #291).
	run_guard archtest go test "-parallel=$test_parallel" -timeout=10m "${test_package_flags[@]}" ./internal/archtest/...
fi

status=0
for name in "${!guard_pids[@]}"; do
	if ! wait "${guard_pids[$name]}"; then
		status=1
	fi
done

for name in "${!guard_pids[@]}"; do
	echo "--- ${name} ---"
	cat "$guard_tmp/${name}.log"
done
if [ "$status" -ne 0 ]; then
	echo "ERROR: one or more parallel guardrails failed"
	exit "$status"
fi
echo ""

echo "=== All Quality Checks Passed ==="
exit 0
