#!/usr/bin/env bash
# gomod-scope.sh
# Decide whether a module-metadata diff (go.mod, go.sum, go.work, go.work.sum
# at any depth) justifies a subject CI lane (billing certification, OS-native
# legs, protobuf contract gate, connector/taskrunner matrices, no-Node lane).
#
# Usage:
#   gomod-scope.sh --relevant BASE_SHA HEAD_SHA
#   gomod-scope.sh --self-test
#
# Exit codes (--relevant):
#   0  relevant     - a module file changed with added lines: a dependency was
#                     added or updated, so new code may reach the lane subject.
#   1  not relevant - untouched metadata or checksum-only removals.
#                     go.mod/go.work deletions can change MVS, replacement or
#                     workspace resolution; they always retain subject evidence.
#   0 on uncertainty (fail open): empty base, invalid base, or a git error runs
#                     the lane. Only an explicit "removal-only or untouched"
#                     answer returns 1. Callers treat any exit other than 1 as
#                     relevant, mirroring scripts/makefile-scope.sh.
#
# The probe scans the union of every caller's module set (root files plus
# nested fixture/connector modules). Each caller still decides which paths
# consult the probe, so a caller that only cares about the root module keeps
# that boundary; a nested addition alongside a root removal fires
# conservatively rather than risking a missed lane.

set -euo pipefail

is_module_file() {
  case "$1" in
    go.mod|go.sum|go.work|go.work.sum|*/go.mod|*/go.sum|*/go.work|*/go.work.sum)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

relevant() {
  local base="$1" head="$2"

  # No predecessor (manual dispatches, initial pushes): fail open.
  if [[ -z "$base" || "$base" =~ ^0{40}$ ]]; then
    return 0
  fi
  # Invalid revisions: fail open so coverage is never skipped.
  if ! git cat-file -e "$base^{commit}" 2>/dev/null; then
    echo "gomod-scope.sh: base $base not available; treating as relevant" >&2
    return 0
  fi
  if ! git cat-file -e "$head^{commit}" 2>/dev/null; then
    echo "gomod-scope.sh: head $head not available; treating as relevant" >&2
    return 0
  fi

  if ! git diff --quiet "$base" "$head" -- go.mod go.work \
    ':(glob)**/go.mod' ':(glob)**/go.work'; then
    return 0
  fi
  local diff
  if ! diff=$(git diff --unified=0 "$base" "$head" -- go.sum go.work.sum \
    ':(glob)**/go.sum' ':(glob)**/go.work.sum'); then
    return 0
  fi
  # Do not swallow git errors or use grep -q in a pipefail pipeline.
  if grep -E '^\+[^+]' <<< "$diff" >/dev/null; then return 0; fi
  return 1
}

self_test() {
  local tmp base head script_path
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' RETURN
  script_path="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  # Hooks pin their index/repository location. Fixture Git must never inherit
  # those pins, or it reads the caller's index against unrelated objects.
  local git_var
  while IFS= read -r git_var; do
    unset "$git_var"
  done < <(git rev-parse --local-env-vars)
  git -C "$tmp" init -q
  git -C "$tmp" config user.email qa@example.com
  git -C "$tmp" config user.name QA
  git -C "$tmp" config commit.gpgsign false
  git -C "$tmp" config maintenance.auto false
  git -C "$tmp" config gc.auto 0

  printf 'module example\n\ngo 1.24\n\nrequire (\n\tgithub.com/a/one v1.0.0\n\tgithub.com/b/two v2.0.0\n)\n' > "$tmp/go.mod"
  printf 'github.com/a/one v1.0.0 h1:aaa=\n' > "$tmp/go.sum"
  mkdir -p "$tmp/testdata/fixture"
  printf 'module fixture\n\ngo 1.24\n\nrequire github.com/c/three v3.0.0\n' > "$tmp/testdata/fixture/go.mod"
  printf 'github.com/c/three v3.0.0 h1:ccc=\n' > "$tmp/testdata/fixture/go.sum"
  printf 'package main\n' > "$tmp/notes.txt"
  git -C "$tmp" add -A
  git -C "$tmp" commit -qm base
  base="$(git -C "$tmp" rev-parse HEAD)"

  # A removal-only root module diff must not fire a subject lane.
  sed -i '/a\/one/d' "$tmp/go.sum"
  git -C "$tmp" add -A
  git -C "$tmp" commit -qm removal
  head="$(git -C "$tmp" rev-parse HEAD)"
  if (cd "$tmp" && bash "$script_path" --relevant "$base" "$head"); then
    echo "removal-only go.mod/go.sum diff was treated as relevant" >&2
    return 1
  fi

  # A removal-only nested fixture module diff must not fire a subject lane.
  sed -i '/c\/three/d' "$tmp/testdata/fixture/go.sum"
  git -C "$tmp" add -A
  git -C "$tmp" commit -qm nested-removal
  nested_head="$(git -C "$tmp" rev-parse HEAD)"
  if (cd "$tmp" && bash "$script_path" --relevant "$head" "$nested_head"); then
    echo "removal-only nested go.mod diff was treated as relevant" >&2
    return 1
  fi

  # An unrelated file change touches no module file.
  printf 'more\n' >> "$tmp/notes.txt"
  git -C "$tmp" add -A
  git -C "$tmp" commit -qm notes
  notes_head="$(git -C "$tmp" rev-parse HEAD)"
  if (cd "$tmp" && bash "$script_path" --relevant "$nested_head" "$notes_head"); then
    echo "non-module diff was treated as relevant" >&2
    return 1
  fi

  # An added requirement fires the lane.
  printf '\trequire github.com/d/four v4.0.0\n' >> "$tmp/testdata/fixture/go.mod"
  git -C "$tmp" add -A
  git -C "$tmp" commit -qm addition
  addition_head="$(git -C "$tmp" rev-parse HEAD)"
  (cd "$tmp" && bash "$script_path" --relevant "$notes_head" "$addition_head") \
    || { echo "added requirement did not fire the lane" >&2; return 1; }

  # A version bump (paired removal/addition) fires the lane.
  sed -i 's/a\/one v1.0.0/a\/one v1.0.1/' "$tmp/go.mod"
  git -C "$tmp" add -A
  git -C "$tmp" commit -qm bump
  bump_head="$(git -C "$tmp" rev-parse HEAD)"
  (cd "$tmp" && bash "$script_path" --relevant "$addition_head" "$bump_head") \
    || { echo "version bump did not fire the lane" >&2; return 1; }

  # Empty and invalid bases fail open: the lane runs rather than being
  # skipped on uncertainty.
  (cd "$tmp" && bash "$script_path" --relevant "" "$bump_head") \
    || { echo "empty base did not fail open" >&2; return 1; }
  if ! (cd "$tmp" && bash "$script_path" --relevant missing-revision "$bump_head") 2>/dev/null; then
    echo "invalid base did not fail open" >&2
    return 1
  fi

  echo 'OK: gomod scope self-test'
}

if [[ "${1:-}" == "--self-test" ]]; then
  self_test
  exit 0
fi

if [[ "${1:-}" != "--relevant" || $# -ne 3 ]]; then
  echo "usage: $0 --relevant BASE_SHA HEAD_SHA | --self-test" >&2
  exit 2
fi
relevant "$2" "$3"
