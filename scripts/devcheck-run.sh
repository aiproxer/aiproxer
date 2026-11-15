#!/usr/bin/env bash
# Run the development checker from a per-worktree cached binary.
#
# `go run` links devcheck on every invocation. Building once into a stable cache
# path and executing that binary keeps the same source-derived tool while
# avoiding the link cost on repeated iterations. The build always runs so Go's
# build cache decides staleness for every dependency, not a timestamp heuristic.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cache_root=${AIP_DEVCHECK_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/aip-devcheck}
key=$(printf '%s' "$root" | cksum | awk '{print $1}')
cache_dir=$cache_root/$key
bin=$cache_dir/devcheck
read -r -a go_argv <<<"${AIP_DEVCHECK_GO:-go}"

mkdir -p "$cache_dir"
tmp=$(mktemp "$cache_dir/devcheck.XXXXXX")
trap 'rm -f "$tmp"' EXIT
(cd "$root" && "${go_argv[@]}" build -buildvcs=false -o "$tmp" ./tools/devcheck)
chmod +x "$tmp"
mv -f "$tmp" "$bin"
trap - EXIT
exec "$bin" "$@"
