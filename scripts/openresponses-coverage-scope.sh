#!/usr/bin/env bash
# Refine the coarse CI scope using the five measured packages' actual Linux
# test dependency graph. Unknown inputs and graph failures run coverage.
set -euo pipefail
base=${1:-}
head=${2:-HEAD}
if [[ -z "$base" || "$base" =~ ^0{40}$ ]]; then echo true; exit 0; fi
if ! git rev-parse --verify "$base^{commit}" >/dev/null 2>&1 \
  || ! git rev-parse --verify "$head^{commit}" >/dev/null 2>&1; then
  echo true; exit 0
fi
# go list inspects the checked-out tree, never an arbitrary different head.
if [[ $(git rev-parse HEAD) != $(git rev-parse "$head^{commit}") ]]; then echo true; exit 0; fi
files=$(mktemp)
trap 'rm -f "$files"' EXIT
if ! git diff --name-only -z "$base" "$head" > "$files"; then echo true; exit 0; fi
packages=(
  ./internal/plugins/protocols/openresponses
  ./internal/refclient/openresponses
  ./internal/refbackend/openresponses
  ./internal/plugins/frontends/openresponses
  ./internal/plugins/backends/openresponsescompat
)
root=$(git rev-parse --show-toplevel)
# -test includes external test packages and their imports. No compilation or
# execution; -mod=readonly prevents the classifier from rewriting metadata.
if ! dirs=$(go list -mod=readonly -deps -test -f '{{.Dir}}' "${packages[@]}"); then
  echo true; exit 0
fi
if [[ -z "$dirs" ]]; then echo true; exit 0; fi
while IFS= read -r -d '' file; do
  case "$file" in
    go.sum|*/go.sum|go.work.sum|*/go.work.sum)
      probe_rc=0
      bash "$(dirname "${BASH_SOURCE[0]}")/gomod-scope.sh" --relevant "$base" "$head" || probe_rc=$?
      if [[ "$probe_rc" != 1 ]]; then echo true; exit 0; fi
      ;;
    internal/*|pkg/*)
      # Package assets (including embedded files and local testdata) count too.
      while IFS= read -r dir; do
        [[ "$dir" == "$root/"* ]] || continue
        if [[ "$root/$file" == "$dir/"* ]]; then echo true; exit 0; fi
      done <<< "$dirs"
      ;;
    *) echo true; exit 0 ;; # Shared policy, module inputs or external fixtures.
  esac
done < "$files"
echo false
