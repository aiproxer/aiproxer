#!/usr/bin/env bash
# Source from Bash entry points; --local lets Make select the same host defaults.
# shellcheck source=scripts/dev-host-policy.sh
source "$(dirname "${BASH_SOURCE[0]}")/dev-host-policy.sh"
aip_local_agent_dev() {
	aip_local_development_host
}

if [[ ${1:-} == --local ]]; then
	aip_local_agent_dev && printf 'yes\n'
	exit 0
fi
# TMPDIR correctness is filesystem reality, not hostname: any local Linux host
# whose temp directory is not ext4 breaks the config-source integrity tests,
# so the redirect applies wherever it is needed. Resource limits below stay
# scoped to the shared agent-dev host.
if [[ ${OS:-} != Windows_NT && $(uname -s) == Linux && -z ${CI:-} && -z ${GITHUB_ACTIONS:-} ]]; then
	aip_dev_tmpdir
fi
if aip_local_agent_dev; then
	: "${GOMAXPROCS:=2}" "${AIP_TEST_PACKAGES:=1}" "${AIP_TEST_PARALLEL:=2}"
	export GOMAXPROCS AIP_TEST_PACKAGES AIP_TEST_PARALLEL
	# Absolute floor: inherited lower priority is retained, never compounded.
	aip_current_nice=$(ps -o ni= -p "$$")
	if (( aip_current_nice < 10 )); then
		renice --priority 10 --pid "$$" >/dev/null
	fi
	unset aip_current_nice
fi
