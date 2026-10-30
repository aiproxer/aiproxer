#!/usr/bin/env bash
# Selects the packages of one CI Go-suite shard from a package list on stdin
# (one import path per line, as printed by `go list`).
#
#   go list -tags=precommit ./... | scripts/ci-suite-shard.sh rest-2
#
# Shards: heavy (the few slowest packages), rest and rest-2 (everything else,
# split by a stable hash so adding a package never moves the others). Every
# package lands in exactly one shard, so a stale list costs balance, never
# coverage.
set -euo pipefail

heavy='/internal/infra/runtimebundle$|/internal/plugins/features/secretguard$|/internal/plugins/frontends/frontendpipe$|/internal/core/runtime$'
# Slow packages pinned to a half so the hash cannot put them all in one (test
# seconds from a CI run: billingstore 23, stdhttp 12, qa 10, release_gates 9,
# billing 9).
rest_pinned='/internal/stdhttp$|/tools/backendplugin/release_gates$'
rest2_pinned='/enterprise/internal/infra/billingstore$|/internal/qa$|/enterprise/internal/core/billing$'

shard_of() {
  local pkg="$1" sum
  if [[ "$pkg" =~ $heavy ]]; then
    echo heavy
  elif [[ "$pkg" =~ $rest_pinned ]]; then
    echo rest
  elif [[ "$pkg" =~ $rest2_pinned ]]; then
    echo rest-2
  else
    sum=$(printf '%s' "$pkg" | cksum | cut -d' ' -f1)
    if (( sum % 2 == 1 )); then echo rest-2; else echo rest; fi
  fi
}

select_shard() {
  local want="$1" pkg
  case "$want" in heavy|rest|rest-2) ;; *) echo "unknown shard: $want" >&2; return 2 ;; esac
  while IFS= read -r pkg; do
    [[ -n "$pkg" ]] || continue
    [[ "$(shard_of "$pkg")" == "$want" ]] && printf '%s\n' "$pkg"
  done
}

self_test() {
  local list shard count total=0 seen
  list=$(printf '%s\n' \
    m/internal/infra/runtimebundle m/internal/core/runtime m/internal/core/runtime/sub \
    m/enterprise/internal/infra/billingstore m/internal/qa m/enterprise/internal/core/billing m/internal/core/billingx \
    m/pkg/a m/pkg/b m/pkg/c m/pkg/d m/pkg/e m/pkg/f m/cmd/aipstd)
  seen=$(mktemp)
  for shard in heavy rest rest-2; do
    count=0
    while IFS= read -r pkg; do
      printf '%s\n' "$pkg" >> "$seen"
      count=$((count + 1))
    done < <(select_shard "$shard" <<<"$list")
    total=$((total + count))
  done
  [[ "$total" == "$(wc -l <<<"$list")" ]] || { echo "shards do not cover the list exactly: $total" >&2; rm -f "$seen"; return 1; }
  [[ -z "$(sort "$seen" | uniq -d)" ]] || { echo "a package is in two shards" >&2; rm -f "$seen"; return 1; }
  rm -f "$seen"
  [[ "$(shard_of m/internal/infra/runtimebundle)" == heavy ]] || { echo "runtimebundle must be heavy" >&2; return 1; }
  [[ "$(shard_of m/internal/core/runtime/sub)" != heavy ]] || { echo "heavy match must be anchored" >&2; return 1; }
  [[ "$(shard_of m/enterprise/internal/core/billing)" == rest-2 ]] || { echo "billing must be pinned to rest-2" >&2; return 1; }
  [[ "$(shard_of m/internal/stdhttp)" == rest ]] || { echo "stdhttp must be pinned to rest" >&2; return 1; }
  [[ "$(shard_of m/internal/core/billingx)" != heavy ]] || { echo "billingx must not be heavy" >&2; return 1; }
  # Same input, same answer on every runner.
  [[ "$(shard_of m/pkg/a)" == "$(shard_of m/pkg/a)" ]]
  echo "OK: ci-suite-shard self-test"
}

if [[ "${1:-}" == "--self-test" ]]; then
  self_test
else
  select_shard "${1:?usage: ci-suite-shard.sh heavy|rest|rest-2 < packages}"
fi
