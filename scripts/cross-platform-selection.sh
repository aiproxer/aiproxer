#!/usr/bin/env bash
# Print a comma-separated connector selection for cross-platform QA.
# Empty output intentionally means "run the complete matrix".
set -euo pipefail

BASE=${1:-}
HEAD=${2:-HEAD}
if [[ -z "$BASE" || "$BASE" =~ ^0{40}$ ]]; then
  exit 0
fi

git rev-parse --verify "${BASE}^{commit}" >/dev/null
git rev-parse --verify "${HEAD}^{commit}" >/dev/null
mapfile -d '' -t FILES < <(git diff --name-only -z "$BASE" "$HEAD")
declare -A SELECTED=()
full=0

for file in "${FILES[@]}"; do
  case "$file" in
    go.mod|go.sum|*/go.mod|*/go.sum)
      full=1
      ;;
    pkg/aipsdk/backendplugin/**|.github/actions/go-cache/**|scripts/ci-go-cache.py|scripts/cross-platform-selection.sh)
      # Shared contracts and cache/selector policy require a complete baseline,
      # even when the same commit also changes one particular connector.
      full=1
      ;;
    connectors/*/*)
      connector=${file#connectors/}
      connector=${connector%%/*}
      if [[ -f "connectors/$connector/release.yaml" ]]; then
        SELECTED["$connector"]=1
      else
        # Unknown/new connector paths are unsafe to narrow; run the full matrix.
        full=1
      fi
      ;;
    connector-support/acp/**)
      for connector in acp agycliacp claudeagentacp codex cursorcliacp geminicliacp; do
        SELECTED["$connector"]=1
      done
      ;;
    connector-support/openaicompat/**)
      for connector in huggingface llamacpp lmstudio nvidia ollama openrouter vllm; do
        SELECTED["$connector"]=1
      done
      ;;
    tools/backendplugin/**|internal/infra/backendplugins/**|internal/archtest/backend_plugin_cross_platform_ci_test.go|Makefile|.github/workflows/backend-plugin-cross-platform.yml)
      full=1
      ;;
  esac
done

if (( full )) || (( ${#SELECTED[@]} == 0 )); then
  exit 0
fi

printf '%s\n' "${!SELECTED[@]}" | sort | paste -sd, -
