# ACP Runtime Deduplication Closeout

- **Spec:** `acp-runtime-deduplication`
- **Status:** completed and archived
- **Implementation PR:** [#242](https://github.com/matdev83/go-llm-interactive-proxy/pull/242), merge `70d500d9935363da31c4368b7e320590bc6c9f6a`
- **Prerequisite PR:** [#239](https://github.com/matdev83/go-llm-interactive-proxy/pull/239), merge `79605bb8e783399c424f3c00cf865459360c302f`
- **Closeout head:** `8eed2636549f4c6bdf1783a6d747ee94815a7135`
- **Closeout date:** 2026-08-08

## Verification

The canonical support unit, seed, RPC-error, and three active 30-second fuzz campaigns passed with `GOWORK=off` in `connector-support/acp`. The executable ACP parity and CLI ACP parity targets passed. Root architecture tests and `make quality-checks` passed.

The local Windows race command is not executable because the Windows cgo toolchain exits before tests start. Strict Linux workflow [31265106123](https://github.com/matdev83/go-llm-interactive-proxy/actions/runs/31265106123) failed on unrelated root-module findings in `internal/refclient/openresponses` and `tools/backendplugin`; it did not report an ACP failure. The merged PR's cross-platform, process-tree, platform-smoke, test, QA, CodeQL, and vulnerability checks passed. The dedicated ACP Linux race follow-up is recorded in `phase3-task31-race-blocker.md`.

The Windows `make backend-plugin-module-checks` target reached root discovery/build/module checks but stopped at unrelated root test failures in `TestDocs_Architecture_OneRuntimeOwnershipContract` and `TestProcessTree_WindowsJobObjectDirect`. These environment/repository maintenance limitations are recorded explicitly; no ACP production regression was observed.

## Scope protection

This closeout moves and completes ACP bookkeeping only. It does not modify the ongoing `openai-codex-native-compaction` / encrypted-reasoning state-preservation specification or implementation.
