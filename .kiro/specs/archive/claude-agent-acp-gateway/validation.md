# Completion validation: claude-agent-acp-gateway

## Delivered baseline

- Feature implementation: [PR #1244](https://github.com/matdev83/go-llm-interactive-proxy/pull/1244), merged as `3a2a1c47b0f7c68f9f533c8d09079fd10f43b60a`.
- Approved safety/certification remediation: [PR #1290](https://github.com/matdev83/go-llm-interactive-proxy/pull/1290), merged as `143f29bf9575198ac147ade4c3727c2190924f75`.
- Task-owned architecture regression repair: [PR #1292](https://github.com/matdev83/go-llm-interactive-proxy/pull/1292), merged as `46b6be78ea70f38ef15fdc7757414518aa7bae88` (the closeout implementation baseline).
- Issue [#1195](https://github.com/matdev83/go-llm-interactive-proxy/issues/1195) is closed. Original requirements, design and task approvals are retained.

## Task reconciliation

| Task | Implemented behavior and owning evidence |
| --- | --- |
| 1.1 | Gateway URL/header validation, stock-host secret-reference grammar, missing-ref and literal-secret refusal: `gateway_test.go`, `internal/service/configure_test.go`, `authenv_test.go`. |
| 1.2 | Source isolation, gate composition, argv/environment/diagnostic redaction, safe refusal of independent joined probe failures: `authenv_test.go`, `gates_test.go`, `identity_probe_linux_test.go`. |
| 2.1 | Gateway-only capability and exact authentication payload, no first-party combination: `TestAuthEnv_GatewaySourceIsolationAndRedaction`, `TestService_AllSourcesOrderedTwoTurnsAndCleanup`, and the real-runtime certification handshake. |
| 2.2 | Fresh one-use gateway identity, exact endpoint/header binding, stale/first-party/wrong-host refusal and byte admission: `effectiveidentity_test.go`, `extension_test.go`, `adapter-extension/identity.test.mjs`, and the pinned-runtime harness. |
| 3.1 | Ordered canonical deltas, commitment-aware safe errors, generic gateway quota taxonomy and owned cleanup: service engine/lifecycle suites, error tables and real stub-gateway streaming. Provider classification and generic routing are tested separately in their owning modules; connector tests import no host internals. |
| 4.1 | Eight certification rows, exact headers on model POSTs, uncredentialed preflight, raw-output/artifact leakage scans, startup/profile disclosure, documented pins and the disabled `config/examples/claude-agent-acp.yaml` gateway example. |
| 4.2 | Full Claude, shared ACP, Cursor CLI/SDK, AGY and Gemini default suites; architecture and QA checks; delivery report. The archive audit exposed the misplaced host-routing test; #1292 repairs its ownership without changing production behavior or weakening the rule. |

## Verification

Local evidence was collected with remote CI explicitly excluded as an acceptance dependency by the owner. No local race bypass was used.

| Check | Outcome |
| --- | --- |
| `LIP_GATEWAY_CERT_RUNTIME=<certified offline install> go -C connectors/claudeagentacp test -count=1 ./...` | PASS, including real bundled-CLI startup admission and sidecar/adapter/SDK streaming to a project-owned loopback gateway. |
| `node --test connectors/claudeagentacp/adapter-extension/identity.test.mjs connectors/claudeagentacp/certgateway/ndjson.test.mjs` | 39 tests passed. |
| `go -C connector-support/acp test ./...` | PASS. |
| `go -C connectors/<module> test ./...` for `cursorcliacp`, `cursorsdk`, `agycliacp`, `geminicliacp` | All PASS on the merged implementation baseline; these modules are unchanged by #1292. |
| `go test ./internal/core/runtime ./internal/archtest ./internal/qa` | PASS after the ownership repair. Before #1292 the connector-import architecture rule correctly failed; this is not reported as a green predecessor baseline. |
| Mandatory changed-package module lint and pre-commit gates | PASS. |
| `go test ./tools/kiro/speccheck` and `make docs-check` | PASS for the archive/status update. The docs gate includes `TestExampleConfig_RepoOperatorYAMLsParse`. |

Review remediation also has observed negative evidence: exit 1 joined with cleanup/wait failures refuses; wrong model-request headers, credential-bearing preflights, unknown auxiliary endpoints and missing streamed content fail certification. Execution still succeeds after the original sidecar path is made unavailable because the driver uses the verified staged copy; restoring the old launch path makes that probe fail.

Detailed command receipts and negative-probe logs are retained in the session handoff evidence outside Git. This document records the reproducible commands and exact merged baselines rather than treating external scratch paths as shipped prerequisites.

## Certification boundary and deferred work

The admitted profile remains Linux/amd64 glibc: adapter 0.89.1, SDK 0.3.293, bundled CLI 2.1.293 and Node 22.23.3. Dependency-tree digest is `2c70d058987bf4de9043c05685b6f311b8cdce214735a0e43eeec66f5e81232f`; sidecar closure is `45a7abfd3c48095a7c82d5ea4b6dc9f96b8322fe147249dae80526f923ff2093`; combined profile is `993f346e7b4aa9a1901d5bffce360f29277e9460a898dc40c4924159ee272371`.

The real-runtime harness certifies Go startup gates and real sidecar/adapter/SDK streaming, not a complete real-provider Go service deployment. Service canonical streaming/lifecycle is covered separately with the in-process fixture. No live-provider, Windows/macOS/WSL or remote race certification is claimed.

No-fallback remains instance-local: a route with an explicit alternate may fail over before output. Operators requiring gateway-only traffic must keep sequential and parallel alternatives gateway-only.

The requirements' Deferred list is unchanged: `gateway-bedrock`, other protocols, dynamic provider switching, interactive gateway authentication, additional usage-accounting integration and Windows/macOS gateway certification remain outside this completed slice. No successor dependency record requires updating, and no deferred item is marked implemented by this archive.
