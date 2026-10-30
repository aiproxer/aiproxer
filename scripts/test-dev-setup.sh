#!/usr/bin/env bash
# Bootstrap/doctor contracts use a private home and fake tools, never host services.
set -euo pipefail
source_dir=$(cd "$(dirname "$0")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
export HOME="$fixture/home" XDG_CONFIG_HOME="$fixture/home/.config"
export AIP_DEV_HOST_POLICY_FILE="$XDG_CONFIG_HOME/aip/dev-host-policy"
export TMPDIR="$fixture/tmp" AIP_DEV_TMPDIR="$fixture/tmp"
mkdir -p "$HOME" "$TMPDIR" "$fixture/bin" "$fixture/repo/scripts/hooks"
cp "$source_dir/"{dev-setup.sh,dev-host-policy.sh,go-dev-guard.sh,setup-hooks.sh,install-hooks.sh,check-worktree-layout.sh,require-ext4-tmpdir.sh} "$fixture/repo/scripts/"
cp "$source_dir/hooks/"{pre-commit,commit-msg,pre-push} "$fixture/repo/scripts/hooks/"
printf 'module example\n\ngo 1.26.6\n\ntoolchain go1.26.10\n' > "$fixture/repo/go.mod"
export TEST_REPO="$fixture/repo" TEST_GIT_CONFIG="$fixture/hooks-path"
cat > "$fixture/bin/git" <<'SH'
#!/usr/bin/env bash
if [[ ${1:-} == -C ]]; then shift 2; fi
case "$*" in
  'rev-parse --show-toplevel') echo "$TEST_REPO" ;;
  'config core.hooksPath scripts/hooks') echo scripts/hooks > "$TEST_GIT_CONFIG" ;;
  'config --get core.hooksPath') cat "$TEST_GIT_CONFIG" 2>/dev/null ;;
  'worktree list --porcelain') printf 'worktree %s\n' "${TEST_LAYOUT_PATH:-$TEST_REPO}" ;;
  *) exit 1 ;;
esac
SH
cat > "$fixture/bin/psql" <<'SH'
#!/usr/bin/env bash
[[ ${PGPASSWORD:-} == secret && ${PGDATABASE:-} == test ]] || exit 3
[[ ${PGCONNECT_TIMEOUT:-} == 5 ]] || exit 4
if [[ ${TEST_DB_DOWN:-0} == 1 ]]; then echo "$PGPASSWORD" >&2; exit 2; fi
echo 1
SH
cat > "$fixture/bin/go" <<'SH'
#!/usr/bin/env bash
case "$*" in
  'env GOOS') echo linux ;;
  'version') if [[ -n ${TEST_GO_VERSION:-} ]]; then echo "go version $TEST_GO_VERSION linux/amd64"; else echo 'go version fake'; fi ;;
  *) exit 1 ;;
esac
SH
cat > "$fixture/bin/findmnt" <<'SH'
#!/usr/bin/env bash
echo "${TEST_FS:-ext4}"
SH
for tool in golangci-lint buf actionlint gitleaks rg; do
  printf '#!/usr/bin/env bash\nexit 0\n' > "$fixture/bin/$tool"
done
chmod +x "$fixture/bin/"*
export PATH="$fixture/bin:$PATH"
export GO_DEV_GUARD_REAL_GO="$fixture/bin/go"
setup="$fixture/repo/scripts/dev-setup.sh"
bash "$setup" > "$fixture/plan"
[[ ! -e "$HOME/.local/bin/go" && ! -e "$TEST_GIT_CONFIG" && ! -e "$AIP_DEV_HOST_POLICY_FILE" ]]
bash "$setup" --apply > "$fixture/apply"
cmp "$fixture/repo/scripts/go-dev-guard.sh" "$HOME/.local/bin/go"
cmp "$fixture/repo/scripts/dev-host-policy.sh" "$HOME/.local/lib/aip/dev-host-policy.sh"
[[ $(cat "$TEST_GIT_CONFIG") == scripts/hooks && $(cat "$AIP_DEV_HOST_POLICY_FILE") == development ]]
[[ -x "$fixture/repo/scripts/hooks/pre-commit" && -x "$fixture/repo/scripts/hooks/commit-msg" && -x "$fixture/repo/scripts/hooks/pre-push" ]]
bash "$setup" --apply > "$fixture/reapply"
# A host that stands in for remote CI keeps that policy; re-applying the setup
# must preserve it instead of refusing to run.
printf 'ci-stand-in\n' > "$AIP_DEV_HOST_POLICY_FILE"
bash "$setup" --apply > "$fixture/reapply-stand-in"
[[ $(cat "$AIP_DEV_HOST_POLICY_FILE") == ci-stand-in ]]
printf 'development\n' > "$AIP_DEV_HOST_POLICY_FILE"
bash "$fixture/repo/scripts/install-hooks.sh" > "$fixture/legacy"
[[ $(cat "$TEST_GIT_CONFIG") == scripts/hooks ]]
export PATH="$HOME/.local/bin:$PATH"
bash "$setup" --doctor > "$fixture/doctor"
# The doctor derives the declared toolchain from go.mod and reports the
# effective one: a host that builds with a different toolchain than the
# repository declares is reported, not silently accepted.
grep -q 'declared go1.26.10 (go.mod); effective version not reported' "$fixture/doctor"
TEST_GO_VERSION=go1.26.10 bash "$setup" --doctor > "$fixture/doctor-declared"
grep -q 'OK: Go toolchain go1.26.10 matches the repository declaration (go.mod)' "$fixture/doctor-declared"
TEST_GO_VERSION=go1.27.2 bash "$setup" --doctor > "$fixture/doctor-other"
grep -q 'NOTE: Go toolchain go1.27.2 differs from the declared go1.26.10 (go.mod)' "$fixture/doctor-other"
grep -q 'Set GOTOOLCHAIN=go1.26.10' "$fixture/doctor-other"
mv "$fixture/bin/golangci-lint" "$fixture/bin/golangci-lint.off"
if PATH="$HOME/.local/bin:$fixture/bin:/usr/bin:/bin" bash "$setup" --doctor > "$fixture/missing-lint" 2>&1; then exit 1; fi
grep -q 'BLOCKED: install golangci-lint' "$fixture/missing-lint"
mv "$fixture/bin/golangci-lint.off" "$fixture/bin/golangci-lint"
mkdir -p "$fixture/container/worktrees/task"
TEST_REPO="$fixture/container/worktrees/task" bash "$fixture/repo/scripts/check-worktree-layout.sh" > "$fixture/layout"
if TEST_REPO="$fixture/container/worktrees/task" TEST_LAYOUT_PATH="$fixture/outside" bash "$fixture/repo/scripts/check-worktree-layout.sh" > "$fixture/bad-layout" 2>&1; then exit 1; fi
export AIP_TEST_POSTGRES_DSN='postgres://user:secret@localhost/test'
export AIP_TEST_POSTGRES_ADMIN_DSN="$AIP_TEST_POSTGRES_DSN"
bash "$setup" --doctor --postgres > "$fixture/postgres"
if TEST_DB_DOWN=1 bash "$setup" --doctor --postgres > "$fixture/down" 2>&1; then exit 1; fi
if grep -q secret "$fixture/down"; then echo 'FAIL: doctor leaked a DSN' >&2; exit 1; fi
if TEST_FS=tmpfs bash "$setup" --doctor > "$fixture/tmpfs" 2>&1; then exit 1; fi
printf '#!/usr/bin/env bash\necho user-shim\n' > "$HOME/.local/bin/go"
cp "$HOME/.local/bin/go" "$fixture/original-shim"
if bash "$setup" --apply > "$fixture/refused" 2>&1; then exit 1; fi
cmp "$fixture/original-shim" "$HOME/.local/bin/go"
echo 'PASS: setup is dry-first/idempotent; hooks are canonical; doctor checks storage and SQL without exposing DSNs; unrelated shims are preserved.'
