# Phase 9 Task 9.4 blocker — native multi-OS cross-platform QA evidence

## Status

> [!warning] DEPRECATED
> Resolved on 2026-07-28. GitHub Actions `Backend plugin cross-platform` run `30382807401` passed Ubuntu, macOS, and Windows jobs on SHA `e796998cc8f7095bce65e450211cb1aef3b76def`; ACP process-tree run `30382805122` also passed all three OS runners. Task `9.4` is complete. Darwin runtime support remains fail-closed and unclaimed. The remainder of this file preserves the historical blocker contract.

Existing Phase 6.3 (macOS process-tree) and Phase 8.2 (Linux race) blockers remain
open unless their dedicated workflows are likewise observed green for that SHA.
This document does not close them.

## Requirement (not weakened)

Task 9.4 requires architecture-appropriate compile/package gates for claimed
Linux/Windows amd64/arm64 artifacts, rejection of false Darwin host-channel
claims, machine-readable unsupported connector-platform pairs, root package
independence from optional connectors, and native lifecycle/IPC tests wherever
runners exist (discovery, strict manifest, digest/exact launch, unauthorized
peer, approved local transport, config secrecy, stream/cancel, process tree,
hard kill/reap, upgrade/rollback/uninstall survivability).

## What is in place locally (this branch)

| Gate | Local observation |
|---|---|
| Structural discovery (`connectors/` + `connector-support/` via `go.mod`, `release.yaml`) | `tools/backendplugin/crossplatform_qa` (no hardcoded connector name list) |
| Host secure profile inventory | `processhost.HostSecureProfiles` — Darwin `compile_unverified` + fail-closed channel |
| False Darwin manifest claims removed | all 14 `connectors/*/manifest/template.backendplugin.json` platforms are Linux/Windows amd64/arm64 only |
| Compile matrix for claimed platforms | `CGO_ENABLED=0` cross-build per connector × claimed GOOS/GOARCH |
| Unsupported pairs metadata | `.golip-crossplatform-matrix.json` (`golip.crossplatform.matrix/v1`, gitignored) |
| Make target | `make backend-plugin-cross-platform-qa` |
| Narrow CI matrix | `ubuntu-latest`, `macos-latest`, `windows-latest` in `backend-plugin-cross-platform.yml`; macOS validates fail-closed host profile rather than Darwin secure-IPC runtime |
| Windows-native evidence | Runnable on this Windows host via the Make target |
| Generated artifact ignore | `.gitignore` LF-clean: matrix JSON, package-check/staging, `.golip-plugins/` |

## Exact external action required

1. Push or open a PR that triggers `.github/workflows/backend-plugin-cross-platform.yml`.
2. Confirm **ubuntu-latest**, **macos-latest**, and **windows-latest** jobs all pass
   `make backend-plugin-cross-platform-qa` on the same SHA.
3. Only then mark task `9.4` complete, citing the Actions run URL / job logs.
4. Separately observe `acp-process-tree.yml` macOS and `codex-connector-race.yml`
   Ubuntu before closing 6.3 / 8.2.

## Explicit non-claims

- Configuring the workflow is **not** completion evidence.
- Cross-compiling Darwin binaries is **not** Darwin secure-IPC proof (Darwin remains fail-closed and unclaimed).
- Windows-only local green is **not** multi-OS CI evidence.
- Parent Phase 9 stays unchecked until 9.4 (and remaining Phase 9 tasks) have their required evidence.

## Human decision (2026-07-27)

Local macOS execution is skipped because no macOS host is available. This is an
approved local validation waiver only; implementation semantics are unchanged.
CI `macos-latest` (and ubuntu/windows) in
`.github/workflows/backend-plugin-cross-platform.yml` remains the source of
native multi-OS evidence after PR push. This blocker stays open; do not check
task `9.4` until those jobs are observed green for the reviewed SHA.
