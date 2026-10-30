#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/named-go-tests.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT
cat >"$scratch/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$CALLS"
if [[ " $* " == *' -list '* ]]; then
  printf '%s\n' "${LISTED:-ok example [no tests to run]}"
  exit "${GO_EXIT:-0}"
fi
exit "${RUN_EXIT:-0}"
EOF
chmod +x "$scratch/go"
export PATH="$scratch:$PATH" GO="$scratch/go" CALLS="$scratch/calls"
if bash "$root/scripts/run-named-go-tests.sh" ./example '^TestMissing$'; then
  echo 'empty selection accepted' >&2; exit 1
fi
test "$(wc -l <"$CALLS")" -eq 1
LISTED=TestPresent bash "$root/scripts/run-named-go-tests.sh" ./example '^TestPresent$' -count=1
grep -q -- '-run \^TestPresent\$' "$CALLS"
if LISTED=TestPresent GO_EXIT=7 bash "$root/scripts/run-named-go-tests.sh" ./example '^TestPresent$'; then
  echo 'Go listing failure swallowed' >&2; exit 1
fi
if LISTED=TestPresent RUN_EXIT=8 bash "$root/scripts/run-named-go-tests.sh" ./example '^TestPresent$'; then
  echo 'Go test failure swallowed' >&2; exit 1
fi
echo 'named Go test selection checks passed'
if env -u AIP_GATEWAY_CERT_RUNTIME make -s -C "$root" claude-gateway-certify; then
  echo 'explicit certification accepted a missing runtime' >&2; exit 1
fi
