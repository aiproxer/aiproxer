#!/usr/bin/env bash
# Protobuf contract gate for api/backendplugin/v1 (see issue #715).
# Runs buf lint, buf breaking against origin/main, and generation freshness.
# Generation is hermetic: pinned `go install tool` plugins are installed to a
# scratch GOBIN prepended to PATH, and output is regenerated into a scratch
# copy of api/ and byte-compared, so the worktree is never modified and
# ambient plugin state cannot shadow the pinned toolchain.
# buf missing -> warn and skip (CI installs buf first, so the gate is real there).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if ! command -v buf >/dev/null 2>&1; then
  echo "WARNING: buf not found on PATH; skipping protobuf contract gate (CI installs buf 1.66.0 first)." >&2
  exit 0
fi

echo "== buf lint =="
(cd "$ROOT/api" && buf lint)

echo "== buf breaking =="
if base="$(git -C "$ROOT" merge-base HEAD origin/main 2>/dev/null)"; then
  (cd "$ROOT" && buf breaking --config api/buf.yaml --against ".git#commit=$base")
else
  echo "WARNING: origin/main not resolvable; skipping breaking-change detection." >&2
fi

echo "== buf generate freshness =="
SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/aip-proto-check.XXXXXX")"
cleanup() { rm -rf "$SCRATCH"; }
trap cleanup EXIT

mkdir -p "$SCRATCH/api" "$SCRATCH/plugins"
cp "$ROOT/api/buf.yaml" "$ROOT/api/buf.gen.yaml" "$SCRATCH/api/"
(cd "$ROOT/api" && find . -name '*.proto') | while IFS= read -r proto; do
  mkdir -p "$SCRATCH/api/$(dirname "$proto")"
  cp "$ROOT/api/$proto" "$SCRATCH/api/$proto"
done

(cd "$ROOT" && GOBIN="$SCRATCH/plugins" go install tool)
(cd "$SCRATCH/api" && PATH="$SCRATCH/plugins:$PATH" buf generate --template buf.gen.yaml)

stale=0
while IFS= read -r generated; do
  rel="api/${generated#./}"
  if ! cmp -s "$SCRATCH/api/$generated" "$ROOT/$rel"; then
    echo "STALE: $rel differs from buf generate output" >&2
    stale=1
  fi
done < <(cd "$SCRATCH/api" && find . -name '*.pb.go' | sort)
if [ "$stale" -ne 0 ]; then
  echo "ERROR: generated protobuf output is stale. Regenerate with pinned tools:" >&2
  echo "  go install tool && cd api && buf generate --template buf.gen.yaml" >&2
  exit 1
fi

echo "OK: protobuf contract gate passed"
