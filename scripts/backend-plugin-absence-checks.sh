#!/usr/bin/env bash
# Prove root builds/tests with Phase 7 migrated connector module trees absent.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export GOWORK=off

migrated=(
  connectors/openrouter
  connectors/nvidia
  connectors/huggingface
  connectors/ollama
  connectors/llamacpp
  connectors/lmstudio
  connectors/vllm
  connectors/opencode
  connectors/codex
  connector-support/openaicompat
)

renamed=()
cleanup() {
  for pair in "${renamed[@]+"${renamed[@]}"}"; do
    from="${pair%%|*}"
    to="${pair##*|}"
    if [[ -e "$from" ]]; then
      mv "$from" "$to"
    fi
  done
}
trap cleanup EXIT

echo "== root build/test with Phase 7 modules temporarily renamed =="
for rel in "${migrated[@]}"; do
  if [[ -e "$ROOT/$rel" ]]; then
    mv "$ROOT/$rel" "$ROOT/${rel}.__absent_check"
    renamed+=("$ROOT/${rel}.__absent_check|$ROOT/$rel")
  fi
done
go build -o /dev/null ./cmd/aipstd
go test ./internal/archtest -run 'TestDefaultWireModelOmitsOptionalKinds|TestEssentialOnly_StandardBackendBundleIsEssentialOnly|TestEssentialOnly_AllowlistExact|TestDynamic_NoOptionalKindNamesInGenericPluginreg|TestEssentialBackendBundle_ExactAllowlist|TestRootGoMod_NoConnectorModules'

echo "== package-full metadata includes Phase 7 artifacts =="
dest="$ROOT/.aip-package-staging/absence-full"
rm -rf "$dest"
# restore modules for packaging
cleanup
trap - EXIT
renamed=()
go run ./tools/backendplugin/package_plugins -root "$ROOT" -profile full -dest "$dest"
index="$(cat "$dest/package-index.json")"
for kind in openrouter nvidia huggingface ollama llamacpp lmstudio vllm opencode codex; do
  grep -q "$kind" <<<"$index" || { echo "package index missing $kind"; exit 1; }
done

echo "OK backend-plugin-absence-checks"
