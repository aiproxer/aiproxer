# Process Substrate Decision (Task 1.1)

## Decision

**Selected substrate:** project-owned process host (`internal/infra/backendplugins/processhost`)

**Rejected for selection (default evidence):**
- Stock HashiCorp `go-plugin` v1.8.0 — mandatory dimensions fail or lack source-feasible proof
- Customized / hardened `go-plugin` v1.8.x — source-feasible only by replacing launch, auth/transport, bootstrap, lifecycle/process models, and limits, which exceeds retained substrate value

This is a **deterministic decision encoding**, not runtime implementation proof of launch, peer authentication, bootstrap, staging, or process cleanup.

## Why

1. **Stock `go-plugin` v1.8.0** fails required controls with anchored source references (pathname `SecureConfig.Check` then `cmd.Path` launch; Windows TCP loopback `serverListener`; AutoMTLS `PLUGIN_CLIENT_CERT` env bootstrap; `MaxCallRecvMsgSize(math.MaxInt32)`; first-class `ReattachConfig`).
2. **Customized `go-plugin`** can be marked source-feasible for those gaps only by replacing the same security/lifecycle subsystems. Residual reusable value is thin negotiation/streaming plumbing Go-LIP must own for its ABI anyway, while MPL-2.0 copyleft remains on modified files. Selection rejects on explicit `ReplacementCost.ExceedsRetainedValue`, not on fake runtime satisfaction.
3. **Project-owned host** is selected as the design substrate so one ownership surface covers launch binding, peer identity, bootstrap, supervision, bounds, and the public ABI adapter. Project-owned rows are `source_verified` design feasibility only.

Executable decision evidence: `go test ./internal/infra/backendplugins/processhost/...` selects `project_owned_host` from the default catalog and keeps the selection algorithm neutral (an all-`source_verified` stock catalog selects stock first).

## Official source / version / license evidence

| Field | Value |
|---|---|
| Evaluated module | `github.com/hashicorp/go-plugin` |
| Evaluated version | `v1.8.0` |
| License | MPL-2.0 |
| Release | https://github.com/hashicorp/go-plugin/releases/tag/v1.8.0 |
| License file | https://github.com/hashicorp/go-plugin/blob/v1.8.0/LICENSE |

Anchored failure/reference symbols used by the default catalog include `SecureConfig.Check` (`client.go`), `serverListener` (`server.go`), AutoMTLS env injection (`client.go`), `MaxCallRecvMsgSize` (`grpc_client.go`), and `ReattachConfig` (`client.go`).

## Approved platform profiles

| Platform | Verification status (this spike) | Launch binding | Local channel |
|---|---|---|---|
| Linux | `design_source_evidenced` | sealed/immutable descriptor (`execveat`/`AT_EMPTY_PATH` or equivalent) | private AF_UNIX + `SO_PEERCRED` bound to expected generation |
| macOS | `compile_unverified` | protected private digest staging + path launch | **fail-closed** (`unsupported_channel`) until approved peer-cred profile |
| Windows | `design_source_evidenced` | protected private digest staging + path launch | named pipe with DACL, token/PID, and job/expected-generation checks |

No platform is `runtime_verified` in Task 1.1: this spike runs no named-pipe, staging, peer, bootstrap, or launch probes.

## Unverified controls (deferred)

Runtime proof of these controls is **not** claimed here. Follow-on tasks own them:

| Control | Deferred to |
|---|---|
| Digest-bound exact-byte launch / protected staging / substitution resistance | Task 2.3 |
| Lazy supervised activation, expected-peer channel, protected bootstrap, process models | Task 3.1 |
| Process-tree cleanup, rollback, exactly-once reap | Task 3.2 |
| Bounded bidirectional streaming / disabled transport retries in live streams | Task 3.4 |

## Rejected options

| Option | Disposition |
|---|---|
| Stock `go-plugin` v1.8.0 | Not selectable on default evidence (failed/missing mandatory dimensions) |
| Customized `go-plugin` v1.8.x | Dimension-feasible via replacement, rejected by explicit replacement-cost ownership decision |
| Go native `plugin` | Already rejected by research/steering |
| Weaker IPC / pathname-only digest / env bootstrap | Prohibited; decision never authorizes weaker controls |

## Task 1.2 blockers

1. Define `api/backendplugin/v1` protobuf and `pkg/lipsdk/backendplugin` DTOs without importing internal `processhost` types.
2. Keep gRPC/protobuf out of `internal/core`; only the later host adapter may bridge public contracts to internal ports.
3. Encode fail-closed major-version rejection and disabled transport retries in public contract tests.
4. Do not implement process launch, peer authentication, or secure channel establishment in Task 1.2.

## Traceability

- Requirements: 2.4, 2.5, 4.3, 5.3, 5.4, 7.2, 7.3, 7.4, 7.6, 7.10, 11.10, 12.2
- Code: `internal/infra/backendplugins/processhost`
- Validation: `go test ./internal/infra/backendplugins/processhost/...`
