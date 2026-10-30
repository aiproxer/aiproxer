# shellcheck shell=bash
# Shared by repo scripts and the installed Go wrapper. Plain-data configuration:
# 'development' opts this machine in; 'auto' retains legacy host detection;
# 'ci-stand-in' keeps this machine a development host but records that remote
# CI cannot run, so the lanes remote CI would own run here instead.
# Known development hosts cannot opt out.
aip_policy_file=${AIP_DEV_HOST_POLICY_FILE:-${XDG_CONFIG_HOME:-$HOME/.config}/aip/dev-host-policy}
aip_policy=auto
if [[ -e "$aip_policy_file" ]]; then aip_policy=$(cat "$aip_policy_file") || return 2; fi
case "$aip_policy" in
  auto|development|ci-stand-in) ;;
  *) echo "Invalid development host policy: expected auto, development or ci-stand-in in $aip_policy_file" >&2; return 2 ;;
esac

aip_development_host() {
  [[ "$aip_policy" == development ]] && return 0
  # A stand-in host is still a development host: it keeps the resource
  # defaults, the temporary-directory redirect and the slot accounting.
  [[ "$aip_policy" == ci-stand-in ]] && return 0
  local host
  for host in "${HOSTNAME:-}" "${COMPUTERNAME:-}" "$(hostname -s 2>/dev/null || true)"; do
    case "${host,,}" in agent-dev|agent-dev.*|desktop-i2caj6v|desktop-i2caj6v.*) return 0 ;; esac
  done
  return 1
}

# aip_remote_ci_unavailable reports that the remote CI lanes cannot run on this
# host, so a lane that only remote CI could normally own has to be executed
# here. A GitHub Actions billing block is the motivating case: every remote job
# fails before it starts, which would leave the race lanes unproven.
aip_remote_ci_unavailable() {
  [[ "$aip_policy" == ci-stand-in ]] && return 0
  return 1
}

aip_local_development_host() {
  [[ ${OS:-} != Windows_NT && $(uname -s) == Linux ]] || return 1
  [[ -z ${CI:-} && -z ${GITHUB_ACTIONS:-} ]] || return 1
  aip_development_host
}

# Config-source tests require ext4 scratch; avoid RAM-backed /tmp.
aip_dev_tmpdir() {
  if [[ -z ${TMPDIR:-} || $(stat -f -c %T "$TMPDIR" 2>/dev/null) != ext2/ext3 ]]; then
    TMPDIR=${AIP_DEV_TMPDIR:-$HOME/.cache/aip-tmp}
    mkdir -p "$TMPDIR"
    export TMPDIR
  fi
  local stamp=$TMPDIR/.go-build-pruned
  if [[ ! -e $stamp || -n $(find "$stamp" -mmin +60 2>/dev/null) ]]; then
    touch "$stamp"
    find "$TMPDIR" -mindepth 1 -maxdepth 1 -type d -name 'go-build*' -mmin +360 -exec rm -rf {} + 2>/dev/null || true
  fi
}
