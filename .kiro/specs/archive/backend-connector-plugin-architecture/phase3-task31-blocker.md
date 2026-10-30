# Phase status notes (backend-connector-plugin-architecture)

## Phase 3–5

Completed earlier on this branch (localstub externalization, dynamic module checks, packaging, docs).

## Phase 6 (ACP family)

In progress — parent task `6` and `6.3` remain open for native macOS process-tree CI evidence (see `phase6-task63-macos-process-tree-blocker.md`).

Done so far:

1. `connector-support/acp` — independent module with protocol/runtime support, caller-owned HTTP, instance-owned `ExecutableCache`, public cancel DTOs via `pkg/lipapi`.
2. External connectors: `connectors/acp`, `connectors/cursorcliacp`, `connectors/geminicliacp`, `connectors/agycliacp` with manifests/`release.yaml`.
3. Static cutover: `acp` / `cursorcliacp` / `geminicliacp` / `agycliacp` removed from migration/essential tables and from `StandardDistributionRequirements`.
4. Product packages `internal/plugins/backends/{cursor,gemini,agy}cliacp` deleted.
5. Packaging discovers ACP artifacts structurally (`package-full` index includes all four kinds + localstub).
6. Make targets: `parity-acp-plugin`, `parity-cursorcliacp-plugin` (includes process-tree), `parity-cli-acp-plugins`; workflow `.github/workflows/acp-process-tree.yml` matrix ubuntu/macos/windows.

Residual honesty:

- `internal/plugins/backends/acp` remains as a temporary shared runtime dependency for **codexappserver** until Codex extraction (Phase 7+). Root `go list -m all` still has **no** `connector-support/*` / `connectors/*` requires.
- Parity evidence is module/descriptor/config + support-module protocol tests and packaging; **not** live Cursor/Gemini/Agy CLI binary runs.
- `-race` unavailable on Windows; support module tests run without race here.
- Native macOS process-tree runtime proof is an **external CI blocker** until `macos-latest` job logs are observed for the reviewed commit.

## Remaining (Phase 7+)

1. Migrate OpenAI-compatible provider family (OpenRouter, NVIDIA, Hugging Face, …).
2. Migrate local OpenAI-compatible runtimes.
3. Extract Codex / OpenCode families and delete remaining `internal/plugins/backends/acp` once codexappserver no longer needs it.
