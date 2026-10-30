#!/usr/bin/env bash
# Emit changed-file scope for CI jobs without skipping required workflow jobs.
# Usage: ci-scope.sh --outputs BASE_SHA HEAD_SHA
#        ci-scope.sh --self-test
set -euo pipefail

is_kiro_spec_path() {
  case "$1" in
    .kiro/specs/*) return 0 ;;
    *) return 1 ;;
  esac
}

# Agent policy surface: steering, skills, and root agent instructions. Prose
# edits here are validated by the cheap QA policy preflight, never by the
# platform test matrix.
is_agent_policy_path() {
  case "$1" in
    .kiro/*|.agents/*|AGENTS.md|CLAUDE.md) return 0 ;;
    *) return 1 ;;
  esac
}

is_documentation_path() {
  case "$1" in
    docs/*)
      case "$1" in
        *.md|*.txt|*.rst|*.adoc|*.png|*.jpg|*.jpeg|*.gif|*.svg|*.drawio)
          return 0
          ;;
        *)
          return 1
          ;;
      esac
      ;;
    .kiro/specs/*)
      # Canonical Kiro SDD artifacts are specification inputs, not runtime/test
      # inputs. Keep executable or otherwise unexpected files fail-closed so
      # the spec tree can never become a generic CI bypass bucket.
      case "$1" in
        *.md|*.markdown|*.json|*.txt|*.rst|*.adoc|*.png|*.jpg|*.jpeg|*.gif|*.svg|*.drawio)
          return 0
          ;;
        *)
          return 1
          ;;
      esac
      ;;
    .kiro/*|.agents/*)
      # Steering and skill catalogs: prose and data only. Scripts, Go and
      # workflow files under these trees stay code-relevant.
      case "$1" in
        *.md|*.markdown|*.json|*.txt|*.rst|*.adoc|*.png|*.jpg|*.jpeg|*.gif|*.svg|*.drawio)
          return 0
          ;;
        *)
          return 1
          ;;
      esac
      ;;
    README.md|README.*.md|CHANGELOG.md|CHANGELOG.*.md|AGENTS.md|CLAUDE.md|LICENSE|LICENSE.*)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

# Some shared files (Makefile, ci.yml) say nothing about a lane by name: a one-line
# edit to them must not fire an expensive lane. They count only when the added or
# removed lines mention the lane's subject. The Makefile .PHONY mega-line lists every
# target, so it is excluded like in scripts/makefile-scope.sh.
diff_mentions() {
  local base="$1" head="$2" pattern="$3"
  shift 3
  git diff --unified=0 "$base" "$head" -- "$@" \
    | grep -E '^[+-]' \
    | grep -vE '^(---|\+\+\+)' \
    | grep -vE '^[+-]\.PHONY:' \
    | grep -iE "$pattern" >/dev/null
}

file_matches() {
  local scope="$1"
  local file="$2"
  case "$scope" in
    code)
      if is_documentation_path "$file"; then
        return 1
      fi
      return 0
      ;;
    test)
      case "$file" in
        scripts/ci-scope.sh|scripts/openresponses-compliance-scope.sh)
          return 1
          ;;
      esac
      if is_documentation_path "$file"; then
        return 1
      fi
      return 0
      ;;
    kiro)
      is_kiro_spec_path "$file" || is_agent_policy_path "$file"
      ;;
    proto)
      # Protobuf contract gate inputs: the api/ tree, the gate script, and
      # this workflow. Module files use the checksum-removal probe below.
      case "$file" in
        api/*|scripts/proto-check.sh|.github/workflows/ci.yml)
          return 0 ;;
        *) return 1 ;;
      esac
      ;;
    os_sensitive)
      # Paths whose behaviour differs per OS or per filesystem. Only these (or
      # the daily schedule / full-ci label) justify Windows and macOS legs.
      # Module files use the checksum-removal probe below.
      case "$file" in
        cmd/*|connectors/*|connector-support/*|tools/taskrunner/*|\
        internal/infra/configsource/*|internal/infra/runtimehost/*|internal/infra/backendplugins/*|\
        scripts/configsource-*|scripts/require-ext4-tmpdir.sh|scripts/check-merge-receiver-branch.*|\
        scripts/test-configsource-*|scripts/test-go-dev-guard.sh|scripts/test-require-ext4-tmpdir.sh|scripts/test-check-merge-receiver-branch.sh|\
        scripts/ci-go-cache.py|.github/actions/go-cache/*|.github/workflows/ci.yml)
          return 0 ;;
        *) return 1 ;;
      esac
      ;;
    go)
      case "$file" in
        scripts/ci-scope.sh|scripts/openresponses-compliance-scope.sh)
          return 1
          ;;
      esac
      case "$file" in
        *.go|go.mod|go.sum|*/go.mod|*/go.sum|go.work|go.work.sum|Makefile|\
        .golangci.yml|.golangci.yaml|.goreleaser.yml|.goreleaser.yaml|\
        .github/workflows/*|\
        scripts/quality-checks.*|scripts/test-*|scripts/windows-task.*|\
        scripts/race-check.*|scripts/tidy-all-modules.*|scripts/check-all-modules.*)
        return 0
        ;;
        *) return 1 ;;
      esac
      ;;
    openresponses_coverage)
      case "$file" in
        internal/**|pkg/**|tools/coverage-gate/**|testdata/**|\
        .github/workflows/openresponses-coverage.yml|.github/actions/go-cache/**|scripts/ci-go-cache.py|scripts/ci-scope.sh|scripts/openresponses-coverage-scope.sh|Makefile|\
        go.mod|go.sum|*/go.mod|*/go.sum)
        return 0
        ;;
        *) return 1 ;;
      esac
      ;;
    billing_schema)
      # Module files use the checksum-removal probe below.
      case "$file" in
        enterprise/internal/core/billing/*|internal/infra/billing*/*|internal/testkit/billsem/*|pkg/aipsdk/metering/*|pkg/aipsdk/economics/*|pkg/aipsdk/billing/*|pkg/aipsdk/scope/*|\
        scripts/ci-scope.sh|scripts/test-billing-*)
          return 0 ;;
        *) return 1 ;;
      esac
      ;;
    test_cost)
      case "$file" in
        scripts/test-cost-*|tools/testcost/**|internal/qa/test_cost_policy_test.go)
          return 0
          ;;
        *) return 1 ;;
      esac
      ;;
    *)
      echo "unknown scope: $scope" >&2
      return 2
      ;;
  esac
}

classify_diff() {
  local base="$1"
  local head="$2"
  local code=false
  local go=false
  local test=false
  local kiro=false
  local coverage=false
  local test_cost=false
  local billing_schema=false
  local os_sensitive=false
  local proto=false
  local root_gomod=false
  local probe_rc script_dir
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  local file diff_file

  # Events without a base SHA (initial pushes or manual dispatches) run
  # every scope rather than risking a false bypass.
  if [[ -z "$base" || "$base" =~ ^0{40}$ ]]; then
    printf 'code=true\ngo=true\ntest=true\nkiro=true\nopenresponses_coverage=true\ntest_cost=true\nbilling_schema=true\nos_sensitive=true\nproto=true\n'
    return 0
  fi

  diff_file=$(mktemp)
  if ! git diff --name-only -z "$base" "$head" > "$diff_file"; then
    rm -f "$diff_file"
    echo "unable to classify changes between $base and $head" >&2
    return 1
  fi
  while IFS= read -r -d '' file; do
    file_matches code "$file" && code=true
    file_matches go "$file" && go=true
    file_matches test "$file" && test=true
    file_matches kiro "$file" && kiro=true
    file_matches openresponses_coverage "$file" && coverage=true
    file_matches test_cost "$file" && test_cost=true
    file_matches billing_schema "$file" && billing_schema=true
    file_matches os_sensitive "$file" && os_sensitive=true
    file_matches proto "$file" && proto=true
    case "$file" in
      go.mod|go.sum|go.work|go.work.sum) root_gomod=true ;;
    esac
  done < "$diff_file"
  rm -f "$diff_file"

  # Root module metadata reaches the billing, OS-native and protobuf lanes
  # only through the shared probe: checksum-only removals can bypass, but
  # all resolution-policy changes retain evidence. A probe failure fails open so a
  # broken probe never silently drops a lane: only an explicit exit 1 skips.
  if [[ "$billing_schema" == false && "$root_gomod" == true ]]; then
    probe_rc=0
    bash "$script_dir/gomod-scope.sh" --relevant "$base" "$head" || probe_rc=$?
    if [[ "$probe_rc" != "1" ]]; then
      billing_schema=true
    fi
  fi
  if [[ "$os_sensitive" == false && "$root_gomod" == true ]]; then
    probe_rc=0
    bash "$script_dir/gomod-scope.sh" --relevant "$base" "$head" || probe_rc=$?
    if [[ "$probe_rc" != "1" ]]; then
      os_sensitive=true
    fi
  fi
  if [[ "$proto" == false && "$root_gomod" == true ]]; then
    probe_rc=0
    bash "$script_dir/gomod-scope.sh" --relevant "$base" "$head" || probe_rc=$?
    if [[ "$probe_rc" != "1" ]]; then
      proto=true
    fi
  fi

  # Makefile, ci.yml and the shared Go-cache plumbing reach the billing
  # certification only when the change itself concerns billing (its targets, its
  # CI job or its cache lane); a budget tweak for another lane does not.
  if [[ "$billing_schema" == false ]] \
    && diff_mentions "$base" "$head" 'billing' Makefile .github/workflows/ci.yml .github/actions/go-cache scripts/ci-go-cache.py; then
    billing_schema=true
  fi

  for value in "$code" "$go" "$test" "$kiro" "$coverage" "$test_cost" "$billing_schema" "$os_sensitive" "$proto"; do
    case "$value" in
      true|false) ;;
      *) echo "invalid CI scope value: $value" >&2; return 1 ;;
    esac
  done
  printf 'code=%s\ngo=%s\ntest=%s\nkiro=%s\nopenresponses_coverage=%s\ntest_cost=%s\nbilling_schema=%s\nos_sensitive=%s\nproto=%s\n' "$code" "$go" "$test" "$kiro" "$coverage" "$test_cost" "$billing_schema" "$os_sensitive" "$proto"
}

# Fixture repositories disable background maintenance: a commit otherwise
# spawns a detached "git maintenance run --auto" that can still be writing
# into .git when the fixture is removed ("rm: cannot remove .git: Directory
# not empty").
init_fixture_repo() {
  git -C "$1" init -q
  git -C "$1" config maintenance.auto false
  git -C "$1" config gc.auto 0
}

self_test() {
  local relevant unrelated output tmp base head script_path
  # Hooks pin their index/repository location. Fixture Git must never inherit
  # those pins, or it reads the caller's index against unrelated fixture objects.
  local git_var
  while IFS= read -r git_var; do
    unset "$git_var"
  done < <(git rev-parse --local-env-vars)

  for relevant in \
    internal/core/runtime.go \
    go.mod \
    connectors/example/go.mod \
    .github/workflows/ci.yml \
    scripts/quality-checks.sh; do
    file_matches code "$relevant" || { echo "code scope missed $relevant" >&2; return 1; }
    file_matches go "$relevant" || { echo "go scope missed $relevant" >&2; return 1; }
    file_matches test "$relevant" || { echo "test scope missed $relevant" >&2; return 1; }
  done

  # Preserve the existing conservative policy outside canonical documentation
  # locations, while treating canonical Kiro SDD artifacts as non-runtime.
  for unrelated in \
    docs/README.md \
    README.md \
    CHANGELOG.md \
    .kiro/specs/example/requirements.md \
    .kiro/specs/example/design.md \
    .kiro/specs/example/research.md \
    .kiro/specs/example/tasks.md \
    .kiro/specs/example/spec.json \
    AGENTS.md \
    .kiro/steering/testing.md \
    .agents/skills/golang-testing/SKILL.md \
    .agents/catalog.json; do
    file_matches code "$unrelated" && { echo "code scope included $unrelated" >&2; return 1; }
    file_matches test "$unrelated" && { echo "test scope included $unrelated" >&2; return 1; }
  done

  # Agent policy prose keeps the cheap policy preflight, but scripts inside the
  # same trees stay code-relevant.
  for relevant in AGENTS.md .kiro/steering/delivery.md .agents/skills/x/SKILL.md; do
    file_matches kiro "$relevant" || { echo "kiro scope missed $relevant" >&2; return 1; }
  done
  for relevant in .agents/skills/x/scripts/run.sh .agents/skills/x/check.go; do
    file_matches code "$relevant" || { echo "code scope missed $relevant" >&2; return 1; }
  done
  for unrelated in Makefile scripts/helper.sh tools/devcheck/main.go; do
    file_matches os_sensitive "$unrelated" && { echo "os_sensitive scope included $unrelated" >&2; return 1; }
  done
  for unrelated in Makefile .github/workflows/ci.yml go.mod go.sum go.work; do
    file_matches billing_schema "$unrelated" && { echo "billing_schema matched $unrelated by name" >&2; return 1; }
  done
  for relevant in internal/infra/configsource/source.go cmd/aipstd/main.go; do
    file_matches os_sensitive "$relevant" || { echo "os_sensitive scope missed $relevant" >&2; return 1; }
  done
  # Root module files reach os_sensitive only through the gomod-scope probe
  # (added/updated requirements), never by name; the diff-level fixture below
  # pins both directions.
  for unrelated in go.mod go.sum go.work; do
    file_matches os_sensitive "$unrelated" && { echo "os_sensitive scope included $unrelated by name" >&2; return 1; }
  done
  for relevant in api/backendplugin/v1/plugin.proto api/buf.yaml scripts/proto-check.sh; do
    file_matches proto "$relevant" || { echo "proto scope missed $relevant" >&2; return 1; }
  done
  # Root module files reach the proto gate only through the gomod-scope
  # probe; the diff-level fixture below pins both directions.
  for unrelated in go.mod go.sum; do
    file_matches proto "$unrelated" && { echo "proto scope included $unrelated by name" >&2; return 1; }
  done
  for unrelated in internal/core/runtime/exec.go pkg/aipapi/request.go docs/README.md AGENTS.md; do
    file_matches proto "$unrelated" && { echo "proto scope included $unrelated" >&2; return 1; }
  done
  for unrelated in internal/core/runtime/exec.go internal/plugins/features/x/y.go docs/README.md AGENTS.md; do
    file_matches os_sensitive "$unrelated" && { echo "os_sensitive scope included $unrelated" >&2; return 1; }
  done

  for relevant in docs/backend-plugins/docs_test.go notes/README.md assets/example.txt testdata/fixture.json scripts/helper.sh; do
    file_matches test "$relevant" || { echo "test scope missed $relevant" >&2; return 1; }
  done

  for relevant in \
    .kiro/specs/example/requirements.md \
    .kiro/specs/example/spec.json \
    .kiro/specs/archive/example/tasks.md; do
    file_matches kiro "$relevant" || { echo "kiro scope missed $relevant" >&2; return 1; }
  done
  for unrelated in docs/README.md internal/core/runtime.go notes/README.md; do
    file_matches kiro "$unrelated" && { echo "kiro scope included $unrelated" >&2; return 1; }
  done

  # Unexpected executable content beneath .kiro/specs must still trigger the
  # ordinary code/test/Go path in addition to the Kiro policy scope.
  relevant=.kiro/specs/example/check.go
  file_matches code "$relevant" || { echo "code scope missed executable Kiro artifact $relevant" >&2; return 1; }
  file_matches test "$relevant" || { echo "test scope missed executable Kiro artifact $relevant" >&2; return 1; }
  file_matches go "$relevant" || { echo "go scope missed executable Kiro artifact $relevant" >&2; return 1; }
  file_matches kiro "$relevant" || { echo "kiro scope missed executable Kiro artifact $relevant" >&2; return 1; }

  for safe_scope in scripts/ci-scope.sh scripts/openresponses-compliance-scope.sh; do
    file_matches test "$safe_scope" && { echo "safe scope script was classified as test-relevant: $safe_scope" >&2; return 1; }
  done
  for relevant in \
    internal/plugins/protocols/openresponses/wire.go \
    internal/refclient/openresponses/client.go \
    pkg/aipapi/request.go \
    internal/core/runtime.go \
    tools/coverage-gate/main.go; do
    file_matches openresponses_coverage "$relevant" || {
      echo "coverage scope missed $relevant" >&2
      return 1
    }
  done
  file_matches openresponses_coverage scripts/openresponses-compliance-scope.sh && {
    echo "coverage scope included unrelated matcher scripts/openresponses-compliance-scope.sh" >&2
    return 1
  }
  for relevant in \
    scripts/test-cost-ratchet.ps1 \
    scripts/test-cost-budget.json \
    tools/testcost/measure.go \
    internal/qa/test_cost_policy_test.go; do
    file_matches test_cost "$relevant" || {
      echo "test_cost scope missed $relevant" >&2
      return 1
    }
  done
  for unrelated in docs/README.md internal/core/runtime.go; do
    file_matches test_cost "$unrelated" && {
      echo "test_cost scope included $unrelated" >&2
      return 1
    }
  done
  if classify_diff invalid-base HEAD >/dev/null 2>&1; then
    echo "invalid base revision did not fail closed" >&2
    return 1
  fi

  # Confirm NUL-delimited parsing does not split a newline-containing path.
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' RETURN
  script_path="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  init_fixture_repo "$tmp"
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit --allow-empty -qm base
  base="$(git -C "$tmp" rev-parse HEAD)"
  relevant=$'internal/plugins/protocols/openresponses/scope\nfixture.go'
  mkdir -p "$(dirname "$tmp/$relevant")"
  printf 'fixture\n' > "$tmp/$relevant"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm relevant
  head="$(git -C "$tmp" rev-parse HEAD)"
  output="$(cd "$tmp" && bash "$script_path" --outputs "$base" "$head")"
  grep -qx 'openresponses_coverage=true' <<< "$output" || {
    echo "NUL-delimited coverage path was not detected" >&2
    return 1
  }
  rm -rf "$tmp"
  trap - RETURN

  # Lock the regression that motivated this scope: a canonical Kiro SDD-only
  # diff must retain Kiro policy validation without enabling runtime/test work.
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' RETURN
  init_fixture_repo "$tmp"
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit --allow-empty -qm base
  base="$(git -C "$tmp" rev-parse HEAD)"
  mkdir -p "$tmp/.kiro/specs/example"
  printf '# Requirements\n' > "$tmp/.kiro/specs/example/requirements.md"
  printf '{"phase":"requirements-generated"}\n' > "$tmp/.kiro/specs/example/spec.json"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm spec-only
  head="$(git -C "$tmp" rev-parse HEAD)"
  output="$(cd "$tmp" && bash "$script_path" --outputs "$base" "$head")"
  for relevant in \
    code=false \
    go=false \
    test=false \
    kiro=true \
    openresponses_coverage=false \
    test_cost=false; do
    grep -qx "$relevant" <<< "$output" || {
      echo "spec-only scope regression: expected $relevant, got: $output" >&2
      return 1
    }
  done
  rm -rf "$tmp"
  trap - RETURN

  # A checksum-only removal keeps the ordinary code/test/Go
  # scopes but must not fire the billing, OS-native or protobuf lanes, while
  # adding a requirement fires all three.
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' RETURN
  init_fixture_repo "$tmp"
  printf 'module example\n\ngo 1.24\n\nrequire (\n\tgithub.com/a/one v1.0.0\n\tgithub.com/b/two v2.0.0\n)\n' > "$tmp/go.mod"
  printf 'github.com/a/one v1.0.0 h1:aaa=\n' > "$tmp/go.sum"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm base
  base="$(git -C "$tmp" rev-parse HEAD)"
  sed -i '/a\/one/d' "$tmp/go.sum"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm removal
  head="$(git -C "$tmp" rev-parse HEAD)"
  output="$(cd "$tmp" && bash "$script_path" --outputs "$base" "$head")"
  for expected in \
    code=true \
    go=true \
    test=true \
    billing_schema=false \
    os_sensitive=false \
    proto=false; do
    grep -qx "$expected" <<< "$output" || {
      echo "removal-only module scope regression: expected $expected, got: $output" >&2
      return 1
    }
  done
  removal_head="$head"
  printf 'module example\n\ngo 1.24\n\nrequire (\n\tgithub.com/a/one v1.0.0\n\tgithub.com/c/three v3.0.0\n)\n' > "$tmp/go.mod"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm addition
  output="$(cd "$tmp" && bash "$script_path" --outputs "$removal_head" HEAD)"
  for expected in \
    billing_schema=true \
    os_sensitive=true \
    proto=true; do
    grep -qx "$expected" <<< "$output" || {
      echo "added-requirement module scope regression: expected $expected, got: $output" >&2
      return 1
    }
  done
  rm -rf "$tmp"
  trap - RETURN

  # Content-judged files: a Makefile, ci.yml or Go-cache edit fires the billing
  # certification only when the changed lines mention billing.
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' RETURN
  init_fixture_repo "$tmp"
  mkdir -p "$tmp/.github/workflows"
  printf '.PHONY: help\nhelp:\n\t@echo usage\n' > "$tmp/Makefile"
  printf 'jobs:\n  test:\n    name: t\n' > "$tmp/.github/workflows/ci.yml"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm base
  base="$(git -C "$tmp" rev-parse HEAD)"
  printf 'lint:\n\t@echo lint\n' >> "$tmp/Makefile"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm unrelated-makefile
  makefile_head="$(git -C "$tmp" rev-parse HEAD)"
  output="$(cd "$tmp" && bash "$script_path" --outputs "$base" "$makefile_head")"
  grep -qx 'billing_schema=false' <<< "$output" || { echo "unrelated Makefile edit fired billing: $output" >&2; return 1; }
  grep -qx 'os_sensitive=false' <<< "$output" || { echo "Makefile edit fired os_sensitive: $output" >&2; return 1; }
  printf '    timeout-minutes: 5\n' >> "$tmp/.github/workflows/ci.yml"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm unrelated-ci
  unrelated_head="$(git -C "$tmp" rev-parse HEAD)"
  output="$(cd "$tmp" && bash "$script_path" --outputs "$makefile_head" "$unrelated_head")"
  grep -qx 'billing_schema=false' <<< "$output" || { echo "unrelated ci.yml edit fired billing: $output" >&2; return 1; }
  mkdir -p "$tmp/.github/actions/go-cache"
  printf '{\n  "ci-suite": {\n    "build_mib": 3072\n  }\n}\n' > "$tmp/.github/actions/go-cache/policy.json"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm cache-policy-base
  cache_base="$(git -C "$tmp" rev-parse HEAD)"
  sed -i 's/3072/4096/' "$tmp/.github/actions/go-cache/policy.json"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm unrelated-cache-budget
  output="$(cd "$tmp" && bash "$script_path" --outputs "$cache_base" HEAD)"
  grep -qx 'billing_schema=false' <<< "$output" || { echo "unrelated cache budget edit fired billing: $output" >&2; return 1; }
  unrelated_head="$(git -C "$tmp" rev-parse HEAD)"
  sed -i 's/"ci-suite"/"billing-schema"/' "$tmp/.github/actions/go-cache/policy.json"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm billing-cache-lane
  output="$(cd "$tmp" && bash "$script_path" --outputs "$unrelated_head" HEAD)"
  grep -qx 'billing_schema=true' <<< "$output" || { echo "billing cache lane edit did not fire billing: $output" >&2; return 1; }
  unrelated_head="$(git -C "$tmp" rev-parse HEAD)"
  printf 'test-billing-schema:\n\t@echo billing\n' >> "$tmp/Makefile"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm makefile-billing
  output="$(cd "$tmp" && bash "$script_path" --outputs "$unrelated_head" HEAD)"
  grep -qx 'billing_schema=true' <<< "$output" || { echo "billing Makefile target did not fire billing: $output" >&2; return 1; }
  billing_head="$(git -C "$tmp" rev-parse HEAD)"
  printf '  billing-schema:\n    name: Billing schema certification\n' >> "$tmp/.github/workflows/ci.yml"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm ci-billing
  output="$(cd "$tmp" && bash "$script_path" --outputs "$billing_head" HEAD)"
  grep -qx 'billing_schema=true' <<< "$output" || { echo "billing ci.yml job edit did not fire billing: $output" >&2; return 1; }
  rm -rf "$tmp"
  trap - RETURN

  echo 'OK: CI scope self-test'
}

if [[ "${1:-}" == "--self-test" ]]; then
  self_test
  exit 0
fi

if [[ "${1:-}" != "--outputs" || $# -ne 3 ]]; then
  echo "usage: $0 --outputs BASE_SHA HEAD_SHA | --self-test" >&2
  exit 2
fi
classify_diff "$2" "$3"
