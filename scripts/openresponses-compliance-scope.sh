#!/usr/bin/env bash
# Print true when a diff touches a surface covered by the official
# OpenResponses compliance suite; print false for unrelated changes.
set -euo pipefail

# Module files reach the suite only through the gomod-scope probe (see the
# tail loop): resolution-policy changes retain evidence; checksum-only
# removals may bypass. The name match stays so the
# self-test below keeps pinning the path set.
is_module_file() {
  case "$1" in
    go.mod|go.sum|*/go.mod|*/go.sum)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

file_requires_suite() {
  local file="$1"
  case "$file" in
    go.mod|go.sum|*/go.mod|*/go.sum|\
    internal/integration/openresponses/**|\
    internal/archtest/**|\
    internal/qa/openresponses_official_manifest_test.go|\
    .github/actions/go-cache/**|scripts/ci-go-cache.py|\
    internal/plugins/frontends/openresponses/**|\
    internal/plugins/frontends/openairesponses/**|\
    internal/plugins/backends/openresponsescompat/**|\
    internal/plugins/backends/openairesponses/**|\
    internal/plugins/protocols/openresponses/**|\
    internal/plugins/protocols/openairesponsesitem/**|\
    internal/plugins/protocols/openairesponsestream/**|\
    internal/refclient/openresponses/**|\
    internal/refclient/openairesponses/**|\
    internal/refbackend/openresponses/**|\
    internal/refbackend/openairesponses/**|\
    internal/testkit/openresponses/**|\
    internal/testkit/conformance/**|\
    internal/core/**|\
    internal/stdhttp/**|\
    internal/infra/runtimebundle/**|\
    pkg/aipapi/**|\
    pkg/aipsdk/**|\
    tools/openresponses-compliance/**|\
    scripts/test-openresponses-compliance.*|\
    scripts/openresponses-compliance-scope.sh|\
    .github/workflows/openresponses-official-compliance.yml|\
    Makefile)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

self_test() {
  local path tmp script_path base head special result
  for path in \
    internal/integration/openresponses/handler.go \
    internal/archtest/openresponses_js_tool_boundary_test.go \
    internal/qa/openresponses_official_manifest_test.go \
    .github/actions/go-cache/action.yml \
    scripts/ci-go-cache.py \
    internal/plugins/frontends/openresponses/handler.go \
    internal/plugins/frontends/openairesponses/handler.go \
    internal/plugins/backends/openresponsescompat/handler.go \
    internal/plugins/backends/openairesponses/handler.go \
    internal/plugins/protocols/openresponses/wire.go \
    internal/plugins/protocols/openairesponsesitem/wire.go \
    internal/plugins/protocols/openairesponsestream/wire.go \
    internal/refclient/openresponses/client.go \
    internal/refclient/openairesponses/client.go \
    internal/refbackend/openresponses/server.go \
    internal/refbackend/openairesponses/server.go \
    internal/testkit/openresponses/fixture.go \
    internal/testkit/conformance/matrix.go \
    internal/core/runtime.go \
    internal/stdhttp/server.go \
    internal/infra/runtimebundle/build.go \
    pkg/aipapi/request.go \
    pkg/aipsdk/registration.go \
    tools/openresponses-compliance/src/index.ts \
    scripts/test-openresponses-compliance.sh \
    scripts/openresponses-compliance-scope.sh \
    .github/workflows/openresponses-official-compliance.yml \
    Makefile \
    go.mod \
    go.sum \
    connectors/example/go.mod; do
    file_requires_suite "$path" || {
      echo "scope self-test: expected relevant path to run suite: $path" >&2
      return 1
    }
  done
  for path in docs/README.md internal/infra/endpoint/parser.go .github/workflows/ci.yml; do
    if file_requires_suite "$path"; then
      echo "scope self-test: expected unrelated path to bypass suite: $path" >&2
      return 1
    fi
  done
  # Exercise NUL-delimited diff parsing with a filename that contains a newline.
  # Plain line-delimited parsing would split this path and could bypass the suite.
  tmp=$(mktemp -d)
  trap - EXIT
  script_path="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  git -C "$tmp" init -q
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA add -A
  printf 'base\n' > "$tmp/base.txt"
  git -C "$tmp" add base.txt
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm base
  base="$(git -C "$tmp" rev-parse HEAD)"
  special=$'internal/core/scope\nfixture.go'
  mkdir -p "$(dirname "$tmp/$special")"
  printf 'fixture\n' > "$tmp/$special"
  git -C "$tmp" add -A
  git -C "$tmp" -c user.email=qa@example.com -c user.name=QA commit -qm relevant
  head="$(git -C "$tmp" rev-parse HEAD)"
  result="$(cd "$tmp" && bash "$script_path" "$base" "$head")"
  if [[ "$result" != true ]]; then
    rm -rf "$tmp"
    echo "scope self-test: NUL-delimited special-character path was not detected" >&2
    return 1
  fi
  rm -rf "$tmp"

  # A checksum-only removal must bypass the suite; adding a requirement
  # must run it.
  tmp=$(mktemp -d)
  git -C "$tmp" init -q
  git -C "$tmp" config user.email qa@example.com
  git -C "$tmp" config user.name QA
  git -C "$tmp" config commit.gpgsign false
  git -C "$tmp" config maintenance.auto false
  git -C "$tmp" config gc.auto 0
  printf 'module example\n\ngo 1.24\n\nrequire (\n\tgithub.com/a/one v1.0.0\n\tgithub.com/b/two v2.0.0\n)\n' > "$tmp/go.mod"
  printf 'github.com/b/two v2.0.0 h1:bbb=\n' > "$tmp/go.sum"
  git -C "$tmp" add -A
  git -C "$tmp" commit -qm base
  base="$(git -C "$tmp" rev-parse HEAD)"
  sed -i '/b\/two/d' "$tmp/go.sum"
  git -C "$tmp" add -A
  git -C "$tmp" commit -qm removal
  head="$(git -C "$tmp" rev-parse HEAD)"
  result="$(cd "$tmp" && bash "$script_path" "$base" "$head")"
  if [[ "$result" != false ]]; then
    rm -rf "$tmp"
    echo "scope self-test: checksum-only removal ran the suite" >&2
    return 1
  fi
  printf 'module example\n\ngo 1.24\n\nrequire (\n\tgithub.com/a/one v1.0.0\n\tgithub.com/c/three v3.0.0\n)\n' > "$tmp/go.mod"
  git -C "$tmp" add -A
  git -C "$tmp" commit -qm addition
  result="$(cd "$tmp" && bash "$script_path" "$head" HEAD)"
  if [[ "$result" != true ]]; then
    rm -rf "$tmp"
    echo "scope self-test: added requirement bypassed the suite" >&2
    return 1
  fi
  rm -rf "$tmp"

  echo "OK: OpenResponses scope matcher self-test"
}

if [[ "${1:-}" == "--self-test" ]]; then
  self_test
  exit 0
fi

BASE=${1:-}
HEAD=${2:-HEAD}
if [[ -z "$BASE" || "$BASE" =~ ^0{40}$ ]]; then
  printf 'true\n'
  exit 0
fi

# Process substitution does not propagate git's status to the loop. Validate
# both revisions before allowing an unrelated-diff bypass.
git rev-parse --verify "${BASE}^{commit}" >/dev/null
git rev-parse --verify "${HEAD}^{commit}" >/dev/null
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
gomod=false
while IFS= read -r -d '' file; do
  if is_module_file "$file"; then
    gomod=true
    continue
  fi
  if file_requires_suite "$file"; then
    printf 'true\n'
    exit 0
  fi
done < <(git diff --name-only -z "$BASE" "$HEAD")
# The shared probe bypasses checksum-only removals, not resolution changes.
# Probe failures fail open: only an explicit exit 1 bypasses.
if [[ "$gomod" == true ]]; then
  probe_rc=0
  bash "$script_dir/gomod-scope.sh" --relevant "$BASE" "$HEAD" || probe_rc=$?
  if [[ "$probe_rc" != "1" ]]; then
    printf 'true\n'
    exit 0
  fi
fi
printf 'false\n'
