# Phase 6 Task 6.3 blocker — native macOS process-tree evidence

## Status

> [!warning] DEPRECATED
> Resolved on 2026-07-28. GitHub Actions `ACP process-tree` run `30382805122` passed Windows, Ubuntu, and macOS jobs on SHA `e796998cc8f7095bce65e450211cb1aef3b76def`. Tasks `6` and `6.3` are complete. The remainder of this file preserves the historical blocker contract.

## Requirement (not weakened)

Task 6.3 requires process-tree cleanup proof on **Linux, macOS, and Windows** before treating Cursor CLI ACP cutover lifecycle gates as complete.

## What is in place locally (this branch)

| Platform | Evidence kind | Local observation |
|---|---|---|
| Windows | Native runtime `TestKillProcessTree_WindowsDescendants` | Runnable on this Windows host via `make parity-cursorcliacp-plugin` / support module named tests |
| Linux | Native runtime `TestKillProcessTree_UnixProcessGroup` (`//go:build unix`) + Ubuntu CI job in `.github/workflows/acp-process-tree.yml` | Implementation source present; Ubuntu path configured; native Linux runtime not re-observed on this Windows host |
| macOS | Workflow job `macos-latest` in `.github/workflows/acp-process-tree.yml` runs the same named filters with `GOWORK=off` | **Not observed locally.** Cross-compile (`TestProcessTree_CrossCompileSources`) is not a substitute for native Darwin runtime |

Additional wiring (does not close the blocker by itself):

- `make parity-cursorcliacp-plugin` invokes `connector-support/acp` `-run 'KillProcessTree_|ProcessTree_CrossCompile'` then cursor connector `TestParity_|TestDescribe_`.
- Archtest drift guards: `TestACP_processTreeWorkflow_nativeOSMatrix`, `TestACP_parityCursorTarget_includesProcessTree`.

## Exact external action required

1. Push (or open a PR that triggers) `.github/workflows/acp-process-tree.yml` so GitHub Actions runs the matrix including **`macos-latest`**.
2. Confirm the macOS job passes native process-tree filters `KillProcessTree_|ProcessTree_CrossCompile` and independent `connectors/cursorcliacp` `TestParity_|TestDescribe_` on the **same commit** under review.
3. Only then mark tasks `6.3` and parent `6` complete, citing the Actions run URL / job logs for that commit.

## Explicit non-claims

- Configuring the workflow is **not** completion evidence.
- Cross-compiling Darwin targets is **not** native macOS process-tree proof.
- Ubuntu/`backend-plugin-module-checks` Linux success does **not** satisfy the macOS row.

## Human decision (2026-07-27)

Local macOS execution is skipped because no macOS host is available. This is an approved local validation waiver only; implementation semantics are unchanged. CI `macos-latest` in `.github/workflows/acp-process-tree.yml` remains the source of native Darwin process-tree evidence after PR push. This blocker stays open; do not check task `6.3` until that CI job is observed green for the reviewed SHA.
