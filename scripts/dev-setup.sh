#!/usr/bin/env bash
# Repository instrumentation bootstrap, dry-run by default; no sudo or downloads.
set -euo pipefail
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$script_dir/.." && pwd)
mode=plan
postgres=false
for arg in "$@"; do
  case "$arg" in
    --apply) mode=apply ;;
    --doctor) mode=doctor ;;
    --postgres) postgres=true ;;
    *) echo 'Usage: dev-setup.sh [--apply|--doctor] [--postgres]' >&2; exit 2 ;;
  esac
done
# shellcheck source=scripts/dev-host-policy.sh
source "$script_dir/dev-host-policy.sh"
guard="$HOME/.local/bin/go"
installed_policy="$HOME/.local/lib/aip/dev-host-policy.sh"
scratch=${AIP_DEV_TMPDIR:-$HOME/.cache/aip-tmp}

if [[ $mode == plan ]]; then
  printf 'Plan (no changes):\n  enable development policy: %s\n  install Go guard: %s\n  install shared policy: %s\n  create ext4 scratch: %s\n  install canonical pre-commit/commit-msg/pre-push hooks\n' "$aip_policy_file" "$guard" "$installed_policy" "$scratch"
  echo 'Apply with make dev-setup SETUP_APPLY=1; diagnose with make dev-doctor.'
  exit 0
fi

if [[ $mode == apply ]]; then
  # Validate before changing anything. Preserve unknown shims and symlinks.
  if [[ -L "$guard" ]] || { [[ -e "$guard" ]] && ! grep -qF "Installed as the user's PATH-level go command" "$guard"; }; then
    echo "Refusing to replace unfamiliar Go shim: $guard" >&2; exit 1
  fi
  if [[ -e "$aip_policy_file" && $aip_policy != development && $aip_policy != ci-stand-in ]]; then
    echo "Existing policy preserved; set $aip_policy_file to development or ci-stand-in before applying." >&2; exit 1
  fi
  [[ -x /usr/local/bin/go || -n ${GO_DEV_GUARD_REAL_GO:-} ]] || {
    echo 'Install the real Go toolchain at /usr/local/bin/go before applying.' >&2; exit 1;
  }
  [[ "$scratch" == /* ]] || { echo 'AIP_DEV_TMPDIR must be absolute.' >&2; exit 1; }
  cd "$root"
  git rev-parse --show-toplevel >/dev/null
  mkdir -p "$scratch"
  TMPDIR="$scratch" bash "$script_dir/require-ext4-tmpdir.sh"
  mkdir -p "$(dirname "$guard")" "$(dirname "$installed_policy")" "$(dirname "$aip_policy_file")"
  if [[ -f "$guard" ]] && ! cmp -s "$guard" "$script_dir/go-dev-guard.sh"; then
    backup=$(mktemp "$guard.backup.XXXXXX")
    cp -p "$guard" "$backup"
    echo "Previous guard backed up: $backup"
  fi
  install -m 644 "$script_dir/dev-host-policy.sh" "$installed_policy"
  install -m 755 "$script_dir/go-dev-guard.sh" "$guard"
  if [[ ! -e "$aip_policy_file" ]]; then
    (umask 077; printf 'development\n' > "$aip_policy_file")
  fi
  bash "$script_dir/setup-hooks.sh"
  echo "Setup applied. Put $HOME/.local/bin first on PATH; run make dev-doctor."
  exit 0
fi

failures=0
missing() { echo "BLOCKED: $*"; failures=$((failures + 1)); }
printf 'Development policy: %s (%s)\n' "$aip_policy" "$aip_policy_file"
if ! aip_development_host; then missing 'development host policy inactive; run make dev-setup SETUP_APPLY=1'; fi
for tool in go git make rg golangci-lint buf actionlint gitleaks; do
  if path=$(command -v "$tool"); then echo "OK: $tool ($path)"; else missing "install $tool"; fi
done
for tool in psql pwsh node gremlins goreleaser; do
  if path=$(command -v "$tool"); then echo "AVAILABLE: optional $tool ($path)"; else echo "OPTIONAL: $tool unavailable"; fi
done
if [[ $(git -C "$root" config --get core.hooksPath || true) != scripts/hooks || ! -x "$script_dir/hooks/pre-commit" || ! -x "$script_dir/hooks/commit-msg" || ! -x "$script_dir/hooks/pre-push" ]]; then
  missing 'canonical hooks; run make hooks-install'
else echo 'OK: canonical pre-commit/commit-msg/pre-push hooks'; fi
if [[ $(command -v go || true) != "$guard" ]] || ! cmp -s "$guard" "$script_dir/go-dev-guard.sh" || ! cmp -s "$installed_policy" "$script_dir/dev-host-policy.sh"; then
  missing 'Go guard installation/version/PATH; run make dev-setup SETUP_APPLY=1'
else echo 'OK: installed Go guard and shared policy match this checkout'; fi
# The repository declares the toolchain it builds and releases with. Go prefers a
# newer local toolchain over that declaration, so a development host can build
# with a different standard library than the release does; report both, and
# derive the declaration from go.mod so it follows the repository.
declared_toolchain=$(awk '$1 == "toolchain" { print $2; exit }' "$root/go.mod" 2>/dev/null || true)
effective_toolchain=$(cd "$root" && go version 2>/dev/null | awk '{ for (i = 1; i <= NF; i++) if ($i ~ /^go1\./) { print $i; exit } }' || true)
if [[ -z $declared_toolchain ]]; then
  printf 'Go toolchain: none declared in go.mod\n'
elif [[ -z $effective_toolchain ]]; then
  printf 'Go toolchain: declared %s (go.mod); effective version not reported\n' "$declared_toolchain"
elif [[ $effective_toolchain == "$declared_toolchain" ]]; then
  printf 'OK: Go toolchain %s matches the repository declaration (go.mod)\n' "$declared_toolchain"
else
  printf 'NOTE: Go toolchain %s differs from the declared %s (go.mod); Go prefers the newer local toolchain, so this host does not build with the release toolchain. Set GOTOOLCHAIN=%s to build with the declared one.\n' "$effective_toolchain" "$declared_toolchain" "$declared_toolchain"
fi
if ! TMPDIR=${TMPDIR:-$scratch} bash "$script_dir/require-ext4-tmpdir.sh"; then missing 'ext4 TMPDIR'; else echo 'OK: ext4 scratch'; fi
if $postgres; then
  for endpoint in runtime admin; do
    dsn=${AIP_TEST_POSTGRES_DSN:-}
    if [[ $endpoint == admin ]]; then dsn=${AIP_TEST_POSTGRES_ADMIN_DSN:-$dsn}; fi
    if [[ -z "$dsn" ]]; then missing "PostgreSQL $endpoint DSN is unset"; continue; fi
    # Translate a URI to libpq environment fields; PGDATABASE itself does not
    # expand URIs. Credentials never enter argv or diagnostics.
    if result=$(AIP_DOCTOR_DSN="$dsn" python3 - 2>/dev/null <<'PY'
import os
import urllib.parse

uri = urllib.parse.urlsplit(os.environ['AIP_DOCTOR_DSN'])
if uri.scheme not in ('postgres', 'postgresql') or not uri.hostname:
    raise ValueError('expected PostgreSQL URI')
env = {k: v for k, v in os.environ.items() if not k.startswith('PG') and k != 'AIP_DOCTOR_DSN'}
env.update(PGHOST=uri.hostname, PGPORT=str(uri.port or 5432), PGDATABASE=urllib.parse.unquote(uri.path.lstrip('/')))
if uri.username is not None:
    env['PGUSER'] = urllib.parse.unquote(uri.username)
if uri.password is not None:
    env['PGPASSWORD'] = urllib.parse.unquote(uri.password)
parameters = {'sslmode': 'PGSSLMODE', 'sslrootcert': 'PGSSLROOTCERT', 'sslcert': 'PGSSLCERT',
              'sslkey': 'PGSSLKEY', 'options': 'PGOPTIONS', 'application_name': 'PGAPPNAME',
              'connect_timeout': 'PGCONNECT_TIMEOUT'}
for key, value in urllib.parse.parse_qsl(uri.query, strict_parsing=True):
    env[parameters[key]] = value
env['PGCONNECT_TIMEOUT'] = '5'
os.execvpe('psql', ['psql', '-X', '-w', '-Atqc', 'SELECT 1'], env)
PY
    ) && [[ $result == 1 ]]; then
      echo "OK: PostgreSQL $endpoint SELECT 1"
    else missing "PostgreSQL $endpoint query failed (credentials/details withheld)"; fi
  done
else echo 'OPTIONAL: PostgreSQL not requested; use make dev-doctor DOCTOR_POSTGRES=1 with DSNs configured'; fi
[[ $failures == 0 ]]
