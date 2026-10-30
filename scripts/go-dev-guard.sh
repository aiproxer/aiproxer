#!/usr/bin/env bash
# Installed as the user's PATH-level go command; never replaces the toolchain.
set -euo pipefail

# Repository analyzers reuse this standalone guard's admission mechanism.
# Exclusive mode is for explicitly requested cache maintenance only.
resource_mode=go
case "${1:-}" in
--aip-resource-run) resource_mode=analyzer; shift ;;
--aip-resource-exclusive) resource_mode=maintenance; shift ;;
esac
if [[ $resource_mode != go && $# == 0 ]]; then
	echo 'go guard: resource mode requires a command.' >&2
	exit 2
fi
delegate=("${GO_DEV_GUARD_REAL_GO:-/usr/local/bin/go}")
if [[ $resource_mode != go ]]; then delegate=("$1"); shift; fi
resource_args=("$@")

policy_script="$(dirname "${BASH_SOURCE[0]}")/dev-host-policy.sh"
if [[ ! -f "$policy_script" ]]; then policy_script="$HOME/.local/lib/aip/dev-host-policy.sh"; fi
if [[ ! -f "$policy_script" ]]; then
	echo 'Go guard policy missing; run make dev-setup SETUP_APPLY=1.' >&2
	exit 2
fi
# shellcheck source=scripts/dev-host-policy.sh
source "$policy_script"

# Build inside a checkout of this repository with the toolchain the repository
# declares. Go prefers a newer local toolchain over the `toolchain` directive, so
# a development host would otherwise build with a different standard library
# than CI and the release use. A checkout is recognized by its own guard
# script, so the pin follows the checkout the command runs in and leaves other
# projects on the host untouched: this guard is a host-wide shim. An explicit
# GOTOOLCHAIN in the environment wins, so a developer can opt out per command.
aip_declared_toolchain() {
	local dir="$PWD"
	while [[ -n $dir && $dir != / ]]; do
		if [[ -f $dir/go.mod && -f $dir/scripts/go-dev-guard.sh ]]; then
			awk '$1 == "toolchain" { print $2; exit }' "$dir/go.mod" 2>/dev/null
			return 0
		fi
		dir=${dir%/*}
	done
}
declared_toolchain=$(aip_declared_toolchain || true)
if [[ -n ${declared_toolchain:-} && -z ${GOTOOLCHAIN:-} ]]; then
	export GOTOOLCHAIN="$declared_toolchain"
fi

blocked_host=false
if aip_development_host; then blocked_host=true; fi
# The race admission follows remote availability, not the host class alone.
# While remote CI cannot start its race lanes, this host runs them, so the race
# flag is admitted instead of refused; the guard's premise (remote CI owns the
# race evidence) no longer holds. Every other development-host guard below
# (resource defaults, temporary directory, slot accounting) stays in force.
race_blocked_host=$blocked_host
if aip_remote_ci_unavailable; then race_blocked_host=false; fi

race_flag() {
	case "$1" in
	-race|--race|-race=true|--race=true|-race=True|--race=True|-race=TRUE|--race=TRUE|-race=t|--race=t|-race=T|--race=T|-race=1|--race=1) return 0 ;;
	*) return 1 ;;
	esac
}

reject_race() {
	echo 'ERROR: race-enabled Go commands are disabled on development hosts. Run race checks in remote GitHub CI. The PATH guard must not be bypassed with a toolchain path or override. When remote CI cannot run at all, record that state in the development host policy (ci-stand-in) instead of bypassing this guard.' >&2
	exit 2
}

go_flags=${GOFLAGS:-}
if [[ -z "$go_flags" && "${GOENV:-}" != off ]]; then
	go_env=${GOENV:-${XDG_CONFIG_HOME:-$HOME/.config}/go/env}
	if [[ -f "$go_env" ]]; then
		while IFS= read -r entry || [[ -n "$entry" ]]; do
			case "$entry" in GOFLAGS=*) go_flags=${entry#GOFLAGS=} ;; esac
		done < "$go_env"
	fi
fi

if [[ "$race_blocked_host" == true && $resource_mode == go ]]; then
	args=("$@")
	index=0
	# Go's global -C precedes its subcommand.
	case "${args[0]:-}" in
	-C|--C) index=2 ;;
	-C=*|--C=*) index=1 ;;
	esac
	command_name=${args[index]:-}
	case "$command_name" in
	build|test|run|install|list|vet|generate|tool)
		# GOFLAGS uses whitespace-separated, optionally quoted flags. Inspect
		# tokens without evaluating shell expansions or executing their contents.
		inspected_flags=${go_flags//\"/}
		inspected_flags=${inspected_flags//\'/}
		inspected_flags=${inspected_flags//$'\n'/ }
		inspected_flags=${inspected_flags//$'\r'/ }
		read -r -a env_flags <<< "$inspected_flags"
		for flag in "${env_flags[@]}"; do
			race_flag "$flag" && reject_race
		done
		needs_value=false
		for ((index+=1; index<${#args[@]}; index++)); do
			arg=${args[index]}
			[[ "$arg" == -args || "$arg" == -- ]] && break
			if [[ "$needs_value" == true ]]; then needs_value=false; continue; fi
			race_flag "$arg" && reject_race
			# go run's program arguments are not Go flags. Its flags precede
			# the package/file; separate values belong to the preceding flag.
			if [[ "$command_name" == run ]]; then
				case "$arg" in
				-*=*|-a|-n|-v|-work|-x|-trimpath|-cover|-race|-msan|-asan) ;;
				-*) needs_value=true ;;
				*) break ;;
				esac
			fi
		done
		;;
	esac
fi
# The limits above are per command; sessions sharing this host still stack.
# Heavy commands (build, test, vet, install) take one of AIP_GO_SLOTS
# host-wide slots (default 2) and wait for a free one, so concurrent agents
# queue instead of slowing each other into timeouts and memory kills. Nested
# commands inherit the parent's slot. AIP_GO_SLOT_MODE=advisory (default)
# runs anyway after AIP_GO_SLOT_WAIT seconds (default 900), preserving the
# escape for children that scrub their environment. Hard mode exits 75 instead
# without starting the toolchain. A zero wait attempts each slot once.
# Print one line per held slot so a waiting agent can see what it queues
# behind (pid, elapsed time, worktree, command) without inspecting processes.
aip_go_slot_holders() {
	local dir=$1 pid etime args slot cmd cwd
	while read -r pid etime args; do
		[[ $args == flock\ * && $args == *" $dir/slot"* ]] || continue
		slot=${args#*" $dir/"}
		slot=${slot%% *}
		cmd=${args#*" $dir/$slot "}
		cmd=${cmd#* }
		cwd=$(readlink "/proc/$pid/cwd" 2>/dev/null || echo '?')
		echo "go guard:   $slot held for $etime by pid $pid in $cwd: go ${cmd:0:160}" >&2
	done < <(ps -eo pid=,etime=,args= 2>/dev/null || true)
}

aip_go_slot_run() {
	[[ -z ${AIP_GO_SLOT_HELD:-} ]] || return 0
	local index=0 command_name
	case "${1:-}" in -C|--C) index=2 ;; -C=*|--C=*) index=1 ;; esac
	local args=("$@")
	command_name=${args[index]:-}
	if [[ $resource_mode == go ]]; then
		case "$command_name" in build|test|vet|install) ;; *) return 0 ;; esac
	fi
	local slots=${AIP_GO_SLOTS:-2} wait=${AIP_GO_SLOT_WAIT:-900}
	local mode=${AIP_GO_SLOT_MODE:-advisory}
	case "$mode" in
	advisory|hard) ;;
	*) echo "go guard: invalid AIP_GO_SLOT_MODE: $mode (expected advisory or hard)." >&2; exit 2 ;;
	esac
	[[ $slots =~ ^[1-9][0-9]*$ ]] || slots=2
	[[ $wait =~ ^[0-9]+$ ]] || wait=900
	local dir=${AIP_GO_SLOT_DIR:-$HOME/.cache/aip-go-slots} slot status announced=false
	local deadline=$((SECONDS + wait))
	mkdir -p "$dir"
	while :; do
		for ((slot = 0; slot < slots; slot++)); do
			# flock -o keeps the lock in flock itself, so a leaked test
			# subprocess cannot hold the slot after go exits.
			status=0
			AIP_GO_SLOT_HELD=$slot flock -n -o -E 211 "$dir/slot$slot" "${delegate[@]}" "$@" || status=$?
			((status == 211)) || exit "$status"
		done
		((SECONDS < deadline)) || break
		if [[ $announced == false ]]; then
			echo "go guard: $slots heavy Go commands are already running on this host; waiting for a slot (AIP_GO_SLOTS=$slots)." >&2
			aip_go_slot_holders "$dir"
			announced=true
		fi
		sleep 2
	done
	if [[ $mode == hard ]]; then
		echo "go guard: resource-blocked: no Go slot freed within ${wait}s (AIP_GO_SLOT_MODE=hard); toolchain not started." >&2
		aip_go_slot_holders "$dir"
		exit 75
	fi
	echo "go guard: no Go slot freed within ${wait}s; running without one (AIP_GO_SLOT_MODE=advisory)." >&2
	export AIP_GO_SLOT_HELD=none
}

# Cache exclusion never fails open: advisory slot overflow still holds a shared
# cache lock. flock owns the lock, without leaking its descriptor to workers.
aip_go_cache_run() {
	local dir=${AIP_GO_SLOT_DIR:-$HOME/.cache/aip-go-slots}
	local wait=${AIP_GO_SLOT_WAIT:-900} status=0
	[[ $wait =~ ^[0-9]+$ ]] || wait=900
	mkdir -p "$dir"
	if [[ $resource_mode == maintenance ]]; then
		if [[ -n ${AIP_GO_SLOT_HELD:-} || -n ${AIP_GO_CACHE_HELD:-} ]]; then
			echo 'go guard: resource-blocked: maintenance cannot upgrade an inherited resource lock.' >&2
			exit 75
		fi
		flock -x -w "$wait" -o -E 211 "$dir/cache" env AIP_GO_CACHE_HELD=exclusive AIP_GO_SLOT_HELD=maintenance "${delegate[@]}" "${resource_args[@]}" || status=$?
	else
		[[ -z ${AIP_GO_SLOT_HELD:-} && -z ${AIP_GO_CACHE_HELD:-} ]] || return 0
		local command_args=("${resource_args[@]}")
		if [[ $resource_mode == analyzer ]]; then command_args=(--aip-resource-run "${delegate[@]}" "${resource_args[@]}"); fi
		flock -s -w "$wait" -o -E 211 "$dir/cache" env AIP_GO_CACHE_HELD=shared bash "$0" "${command_args[@]}" || status=$?
	fi
	if ((status == 211)); then
		echo "go guard: resource-blocked: cache lock unavailable within ${wait}s; command not started." >&2
		exit 75
	fi
	exit "$status"
}

if [[ $resource_mode == maintenance ]]; then aip_go_cache_run; fi

# TMPDIR correctness is filesystem reality, not hostname: any local Linux host
# whose temp directory is not ext4 breaks the config-source integrity tests,
# so the redirect applies wherever it is needed. Resource controls below stay
# scoped to development hosts.
if [[ ${OS:-} != Windows_NT && $(uname -s) == Linux && -z ${CI:-} && -z ${GITHUB_ACTIONS:-} ]]; then
	aip_dev_tmpdir
fi
# Resource defaults apply locally; race policy also applies with CI markers.
if aip_local_development_host; then
		index=0
		case "${1:-}" in -C|--C) index=2 ;; -C=*|--C=*) index=1 ;; esac
		if [[ $resource_mode == analyzer || ${resource_args[index]:-} =~ ^(build|test|vet|install)$ ]]; then
			aip_go_cache_run
		fi
		: "${GOMAXPROCS:=2}"
		export GOMAXPROCS
		# Preserve persisted and quoted flags verbatim; explicit -p wins.
		package_flags=${go_flags//\"/}
		package_flags=${package_flags//\'/}
		if [[ $resource_mode == go && ! $package_flags =~ (^|[[:space:]])-p(=|[[:space:]]|$) ]]; then
			GOFLAGS="${go_flags:+$go_flags }-p=1"
			export GOFLAGS
		fi
		current_nice=$(ps -o ni= -p "$$")
		if (( current_nice < 10 )); then
			renice --priority 10 --pid "$$" >/dev/null
		fi
		aip_go_slot_run "$@"
fi
exec "${delegate[@]}" "$@"
