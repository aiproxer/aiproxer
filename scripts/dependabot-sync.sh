#!/usr/bin/env bash
# Bring a checked-out Dependabot Go branch up to date with main and synchronize
# every module with the root graph, committing the result (nothing is pushed).
#
# Dependabot bumps only direct requirements, one directory at a time, so modules
# that carry the dependency as indirect or only in go.sum drift. When main has
# moved go.mod/go.sum underneath the branch, main's files are taken and the
# bump commit's requirements are re-applied on top; conflicts in any other file
# fail the run.
#
# usage: scripts/dependabot-sync.sh <dependabot-bump-commit> [base-ref]
set -euo pipefail

BUMP="${1:?usage: $0 <dependabot-bump-commit> [base-ref]}"
BASE="${2:-origin/main}"
ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

module_file() { [[ "$1" =~ (^|/)go\.(mod|sum)$ ]]; }

# newer A B: true when version B sorts after A.
newer() { [[ "$1" != "$2" && "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -1)" == "$2" ]]; }

reapply_bump() {
  local mod dir path version current
  while read -r mod; do
    dir="$(dirname "$mod")"
    [[ -f "$dir/go.mod" ]] || continue
    while read -r path version; do
      current="$(cd "$dir" && go mod edit -json | jq -r --arg p "$path" '.Require[]? | select(.Path == $p) | .Version')"
      # Only raise a requirement main still has; never downgrade past main.
      if [[ -n "$current" ]] && newer "$current" "$version"; then
        echo "re-applying $path $version in $dir (main has $current)"
        (cd "$dir" && go mod edit -require="$path@$version")
      fi
    done < <(git show --format= "$BUMP" -- "$mod" |
      sed -nE 's/^\+(require)?[[:space:]]+([^[:space:]]+)[[:space:]]+(v[0-9][^[:space:]]*).*/\2 \3/p')
  done < <(git show --name-only --format= "$BUMP" | grep -E '(^|/)go\.mod$')
}

if ! git merge --no-edit -m "Merge ${BASE#origin/} into Dependabot branch" "$BASE" >/dev/null; then
  mapfile -t conflicted < <(git diff --name-only --diff-filter=U)
  for file in "${conflicted[@]}"; do
    if ! module_file "$file"; then
      echo "conflict outside go.mod/go.sum: $file" >&2
      git merge --abort
      exit 1
    fi
  done
  echo "resolving ${#conflicted[@]} module file conflicts from $BASE"
  git checkout "$BASE" -- "${conflicted[@]}"
  reapply_bump
fi

GOWORK=off bash scripts/tidy-all-modules.sh

git add -A
if [[ -f "$(git rev-parse --git-dir)/MERGE_HEAD" ]]; then
  git commit -qm "Merge $BASE and re-apply Dependabot bump"
elif ! git diff --cached --quiet; then
  git commit -qm "build(deps): synchronize module graph after Dependabot bump"
fi
