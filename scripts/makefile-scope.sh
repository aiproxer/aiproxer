#!/usr/bin/env bash
# makefile-scope.sh
# Decide whether a Makefile diff is relevant to a CI matrix workflow scope.
#
# Usage:
#   makefile-scope.sh --relevant BASE_SHA HEAD_SHA SCOPE
#   makefile-scope.sh --self-test
#
# Exit codes (--relevant):
#   0  relevant     - the Makefile diff touches lines matching SCOPE
#   1  not relevant - Makefile unchanged or no matching added/removed lines
#   2  usage/error  - unknown scope, invalid base, missing Makefile revision;
#                     callers treat anything other than exit 1 as relevant
#
# Scopes (case-insensitive match over added/removed Makefile lines):
#   acp            acp | cursorcliacp
#   backend-plugin backend-plugin
#
# The matrix workflows still trigger on any Makefile change; this probe only
# decides whether the expensive 3-OS matrix must actually run. Non-PR events,
# unknown bases, and missing Makefile revisions fail open so coverage is never
# skipped: only an explicit "diff has no scope-relevant lines" answer returns 1.

set -euo pipefail

scope_pattern() {
  case "$1" in
    acp)            printf '%s' 'acp|cursorcliacp' ;;
    backend-plugin) printf '%s' 'backend-plugin' ;;
    *)              return 2 ;;
  esac
}

relevant() {
  local base="$1" head="$2" scope="$3" pattern
  if ! pattern=$(scope_pattern "$scope"); then
    echo "makefile-scope.sh: unknown scope: $scope" >&2
    return 2
  fi

  # Non-PR events and manual dispatches have no base SHA: fail open.
  if [[ -z "$base" ]]; then
    return 0
  fi
  # Invalid base or a missing Makefile at either revision: fail open.
  if ! git cat-file -e "$base^{commit}" 2>/dev/null; then
    echo "makefile-scope.sh: base $base not available; treating as relevant" >&2
    return 0
  fi
  if ! git cat-file -e "$base:Makefile" 2>/dev/null || ! git cat-file -e "$head:Makefile" 2>/dev/null; then
    echo "makefile-scope.sh: Makefile missing at base or head; treating as relevant" >&2
    return 0
  fi

  # Makefile unchanged: the workflow path trigger is the only signal.
  if git diff --quiet "$base" "$head" -- Makefile; then
    return 1
  fi

  # Consider only actual added/removed lines (skip hunk headers and the
  # +/- line prefixes themselves). Recipe and target names carry the scope.
  # The .PHONY mega-line lists every target, so a change to it would match
  # every scope; exclude it so single-line target additions do not force the
  # full 3-OS matrices. Real target/recipe changes are still detected.
  if git diff --unified=0 "$base" "$head" -- Makefile \
    | grep -E '^[+-]' \
    | grep -vE '^(---|\+\+\+)' \
    | grep -vE '^[+-]\.PHONY:' \
    | grep -qiE "$pattern"; then
    return 0
  fi
  return 1
}

self_test() {
  local tmp base head script_path
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' RETURN
  script_path="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"

  git -C "$tmp" init -q
  git -C "$tmp" config user.email qa@example.com
  git -C "$tmp" config user.name QA
  git -C "$tmp" config commit.gpgsign false

  # Realistic fixture: .PHONY mega-line is always first, as in the repo.
  printf '.PHONY: help parity-acp-plugin\n\nparity-acp-plugin:\n\t@echo acp\n\nhelp:\n\t@echo usage\n' > "$tmp/Makefile"
  git -C "$tmp" add Makefile
  git -C "$tmp" commit -qm base
  base=$(git -C "$tmp" rev-parse HEAD)

  printf '\nparity-acp-extra:\n\t@echo acp\n' >> "$tmp/Makefile"
  git -C "$tmp" add Makefile
  git -C "$tmp" commit -qm acpextra
  acpextra=$(git -C "$tmp" rev-parse HEAD)

  (cd "$tmp" && bash "$script_path" --relevant "$base" "$acpextra" acp) \
    || { echo "acp scope missed added parity-acp-extra target" >&2; return 1; }
  (cd "$tmp" && bash "$script_path" --relevant "$base" "$acpextra" backend-plugin) \
    && { echo "backend-plugin scope wrongly matched an acp-only change" >&2; return 1; }

  printf '\nbackend-plugin-cross-platform-qa:\n\t@echo plugin\n' >> "$tmp/Makefile"
  git -C "$tmp" add Makefile
  git -C "$tmp" commit -qm plugin
  plugin=$(git -C "$tmp" rev-parse HEAD)
  (cd "$tmp" && bash "$script_path" --relevant "$acpextra" "$plugin" backend-plugin) \
    || { echo "backend-plugin scope missed added cross-platform target" >&2; return 1; }
  (cd "$tmp" && bash "$script_path" --relevant "$acpextra" "$plugin" acp) \
    && { echo "acp scope wrongly matched a plugin-only Makefile change" >&2; return 1; }

  printf '\nhelp:\n\t@echo updated usage\n' >> "$tmp/Makefile"
  git -C "$tmp" add Makefile
  git -C "$tmp" commit -qm helponly
  help=$(git -C "$tmp" rev-parse HEAD)

  # A .PHONY mega-line change alone lists every target and must not match any
  # scope; only real target/recipe lines carry the signal.
  sed -i '1s/.*/.PHONY: help parity-acp-plugin parity-acp-extra backend-plugin-cross-platform-qa/' "$tmp/Makefile"
  git -C "$tmp" add Makefile
  git -C "$tmp" commit -qm phonyonly
  phony=$(git -C "$tmp" rev-parse HEAD)
  (cd "$tmp" && bash "$script_path" --relevant "$help" "$phony" acp) \
    && { echo "acp scope matched a .PHONY-only change" >&2; return 1; }
  (cd "$tmp" && bash "$script_path" --relevant "$help" "$phony" backend-plugin) \
    && { echo "backend-plugin scope matched a .PHONY-only change" >&2; return 1; }
  (cd "$tmp" && bash "$script_path" --relevant "$plugin" "$help" acp) \
    && { echo "acp scope matched a help-only Makefile change" >&2; return 1; }
  (cd "$tmp" && bash "$script_path" --relevant "$plugin" "$help" backend-plugin) \
    && { echo "backend-plugin scope matched a help-only Makefile change" >&2; return 1; }

  if (cd "$tmp" && bash "$script_path" --relevant "$plugin" "$help" nope) 2>/dev/null; then
    echo "unknown scope did not fail closed" >&2
    return 1
  fi
  if ! (cd "$tmp" && bash "$script_path" --relevant "" "$phony" acp); then
    echo "empty base did not fail open" >&2
    return 1
  fi

  echo 'OK: makefile-scope self-test'
}

case "${1:-}" in
  --self-test)
    self_test
    ;;
  --relevant)
    if [[ $# -ne 4 ]]; then
      echo "usage: $0 --relevant BASE_SHA HEAD_SHA SCOPE | --self-test" >&2
      exit 2
    fi
    relevant "$2" "$3" "$4"
    ;;
  *)
    echo "usage: $0 --relevant BASE_SHA HEAD_SHA SCOPE | --self-test" >&2
    exit 2
    ;;
esac
