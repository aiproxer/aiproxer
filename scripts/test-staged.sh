#!/usr/bin/env bash
# test-staged.sh
# Run the complete root-module test graph through Go's native build/test cache.
# Set AIP_TEST_PRECOMMIT=1 (e.g. from quality-gate) to include the precommit-only
# regression and hygiene tests. The full graph is intentional: staged-package
# filtering can miss reverse dependencies affected by a changed package.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/dev-cpu-defaults.sh"

pre_flags=()
if [[ "${AIP_TEST_PRECOMMIT:-}" =~ ^(1|true|yes|on)$ ]]; then
	pre_flags=( -tags=precommit )
fi

echo "Testing complete root-module package graph (Go build/test cache enabled)"
if [ ${#pre_flags[@]} -gt 0 ]; then
	echo "Test tags: ${pre_flags[*]}"
fi
echo ""

# Deliberately omit -count=1. Go can reuse both package builds and successful
# test results, while ./... catches downstream packages that staged-only
# selection would miss after a shared-package change.
test_parallel="${AIP_TEST_PARALLEL:-}"
if [ -z "$test_parallel" ]; then
	test_parallel=$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 8)
fi
test_package_flags=()
if [[ -n ${AIP_TEST_PACKAGES:-} ]]; then
	if [[ ! $AIP_TEST_PACKAGES =~ ^[1-9][0-9]*$ ]]; then
		echo "AIP_TEST_PACKAGES must be a positive integer" >&2
		exit 2
	fi
	test_package_flags=("-p=$AIP_TEST_PACKAGES")
fi
# Hook-local Git pins must not bind temporary-repository fixtures to the caller.
git_env_names=$(git rev-parse --local-env-vars)
mapfile -t git_local_env <<< "$git_env_names"
for name in "${git_local_env[@]}"; do
	unset "$name"
done

# Keep the complete graph, but avoid making expensive architecture scans
# compete with ordinary tests. Both lanes retain the same tags and budgets.
package_list=$(go list "${pre_flags[@]}" ./...)
mapfile -t ordinary_packages < <(printf '%s\n' "$package_list" | grep -vE '/internal/archtest(/|$)' || true)
mapfile -t architecture_packages < <(printf '%s\n' "$package_list" | grep -E '/internal/archtest(/|$)' || true)
if [ ${#ordinary_packages[@]} -gt 0 ]; then
	go test "-parallel=$test_parallel" "${test_package_flags[@]}" "${pre_flags[@]}" "${ordinary_packages[@]}"
fi
if [ ${#architecture_packages[@]} -gt 0 ]; then
	go test "-parallel=$test_parallel" "${test_package_flags[@]}" "${pre_flags[@]}" "${architecture_packages[@]}"
fi
exit $?
