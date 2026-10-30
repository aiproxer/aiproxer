# Phase 9 Task 9.5 blocker — multi-OS release-gate CI evidence

## Status

> [!warning] DEPRECATED
> Resolved on 2026-07-28. Ubuntu `Backend plugin release gates` run `30382809655`, three-OS cross-platform run `30382807401`, three-OS ACP process-tree run `30382805122`, and Ubuntu Codex race run `30382811954` all passed on SHA `e796998cc8f7095bce65e450211cb1aef3b76def`. Task `9.5` and parent Phase `9` are complete. The release workflow intentionally targets Ubuntu; cross-platform and ACP workflows provide the separate macOS/Windows evidence. The remainder of this file preserves the historical blocker contract.

The original blocker required the following workflows on the same reviewed SHA:

1. `.github/workflows/backend-plugin-release-gates.yml` — ubuntu/macOS/Windows
2. `.github/workflows/backend-plugin-cross-platform.yml` — task 9.4 matrix
3. Existing open blockers remain open unless their dedicated workflows are also
   observed green for that SHA:
   - Phase 6.3 macOS process-tree (`acp-process-tree.yml`)
   - Phase 8.2 Linux race (`codex-connector-race.yml` / race evidence)
   - Phase 9.3 Linux race/security + Darwin peer-cred (`phase9-task93-external-security-blocker.md`)

## Requirement (not weakened)

Task 9.5 requires structural connector/support module gates, root isolation,
hundred-manifest discovery, adversarial exact-executable/peer/digest suites,
mixed routing and stream/leak cleanup, security/fuzz/package/upgrade/rollback,
architecture independence, and a complete requirement traceability map
(1.1–12.11) to executable or honestly external/unsupported evidence.

## What is in place locally (this branch)

| Gate | Local observation |
|---|---|
| Structural module discovery | `tools/backendplugin/release_gates` + `discover_modules` (no name list) |
| Module matrix | `GOWORK=off` list/vet/tidy-diff/test/build + advertised-cap filters |
| Static slice in `make qa` | `backend-plugin-release-gates-static` (report + wiring tests only) |
| Full target | `make backend-plugin-release-gates` |
| Machine-readable report | `.golip-release-gates-report.json` (`golip.release.gates/v1`, gitignored; deterministic: no timestamps/`native_host`/abs-temp paths/durations/SHA in gate details) |
| Traceability 1.1–12.11 | embedded in report; external rows for 7.13 / 11.11 / 12.11 (and related) |
| Narrow CI matrix | ubuntu/macOS/Windows in `backend-plugin-release-gates.yml` |

## Exact external action required

1. Push or open a PR that triggers `.github/workflows/backend-plugin-release-gates.yml`.
2. Confirm **ubuntu-latest**, **macos-latest**, and **windows-latest** jobs all pass
   `make backend-plugin-release-gates` on the same SHA.
3. Separately observe 9.4 cross-platform, 9.3 security, 6.3 macOS, and 8.2 Linux-race
   evidence for that SHA before closing those blockers.
4. Only then mark task `9.5` and parent Phase `9` complete, citing Actions run URLs.

## Explicit non-claims

- Configuring the workflow is **not** completion evidence.
- Local Windows green is **not** multi-OS CI evidence.
- `make qa` integrating the **static** slice is **not** a substitute for full
  `make backend-plugin-release-gates` on all three OS runners.
- Parent Phase 9 stays unchecked while any Phase 9 task or listed external
  blocker lacks current-SHA evidence.

## Human decision (2026-07-27)

Local macOS execution is skipped because no macOS host is available. This is an
approved local validation waiver only; implementation semantics are unchanged.
CI `macos-latest` (and ubuntu/windows) in
`.github/workflows/backend-plugin-release-gates.yml` remains the source of
native multi-OS release-gate evidence after PR push. This blocker stays open;
do not check task `9.5` / Phase `9` until required workflows are observed green
for the reviewed SHA.
