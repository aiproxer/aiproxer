# Current-State Review, Requirements Gap Analysis, and Design Validation: Cursor SDK Backend

Generated: 2026-07-17T17:27:04+02:00

## Status

- Repository: `matdev83/go-llm-interactive-proxy`
- Reviewed ref: `main` at `7fd53d275168170bb0a31cd42b1c73c5311c505a`
- Inspiration repository: `fitchmultz/pi-cursor-sdk`
- Supporting Go bridge reference: `remdev/cursor-go-sdk`
- Requirements source: `.kiro/specs/archive/cursor-sdk-backend/requirements.md`
- Design source: `.kiro/specs/archive/cursor-sdk-backend/design.md`
- Review mode: static source, steering, archived-spec, external repository, lifecycle, model-inventory, and composition-root review through the connected GitHub repositories.
- Scope: brownfield requirements analysis followed by design validation. This pull request changes Kiro specifications only.

## Executive Assessment

Go-LIP already has a materially stronger Cursor process integration than a simple CLI wrapper. `cursorcliacp` uses structured ACP JSON-RPC over stdio, pooled subprocesses keyed by workspace/model/client session, handshake and session creation, transcript divergence detection, cancellation, idle reaping, delayed stale termination, pipe cleanup, and PID-reuse hardening. Therefore an SDK backend is not justified merely as a way to "stop managing a CLI process."

The SDK path remains worthwhile because it replaces Cursor CLI and ACP-specific surface coupling with the official agent SDK contract, provides structured model discovery, opens future local/cloud agent capabilities, and gives the project direct access to agent/run lifecycle APIs. In Go, however, the official SDK still requires a Node boundary. The recommended delivery is a project-owned, versioned stdio bridge and a new opt-in `cursorsdk` backend beside `cursorcliacp`.

The brownfield review also found two repository seams that the initial feature framing did not cover:

1. a long-lived bridge needs composition-root shutdown ownership, while `execbackend.Backend` currently has no optional closer even though `runtimebundle.Built` already owns `Closers`; and
2. the new connector must publish the same canonical `cursor/...` models without colliding with the existing backend route-prefix ownership rules.

The final requirements and design remediate both points without changing public canonical contracts.

## Reviewed Assets

### Steering, Kiro rules, and workflow patterns

- `AGENTS.md`
- `.kiro/AGENTS.md`
- `.kiro/steering/{structure,tech,api-standards,routing-and-orchestration,testing}.md`
- `.kiro/rules/{ears-format,gap-analysis,design-principles,design-review}.md`
- `.kiro/templates/specs/{init.json,requirements.md,design.md,tasks.md}`
- `.kiro/specs/archive/base-api-connector-porting/`
- `.kiro/specs/archive/reasoning-output-preservation/`
- `EchoesVault/pages/plugin-system.md`

### Current Cursor and subprocess implementation

- `internal/plugins/backends/cursorcliacp/{connector,models,connector_test,models_test}.go`
- `internal/plugins/backends/acp/{subprocess_spec,subprocess_protocol,runtime_pool,runtime_pool_ensure}.go`
- ACP stream, transport, handshake, cancellation, history, and lifecycle tests
- `internal/standardplugins/backends_acp_cli.go`
- `internal/standardplugins/standard_table.go`
- `internal/archtest/backend_lifecycle_contract_test.go`

### Runtime, model inventory, and lifecycle contracts

- `internal/core/execbackend/backend.go`
- `pkg/lipapi/{capabilities,events,lifecycle}.go`
- `pkg/lipsdk/modelinventory/modelinventory.go`
- `internal/core/modelregistry/registry.go`
- `internal/infra/runtimebundle/{build_model,build_executor,built}.go`
- `internal/pluginreg/reg.go`
- `internal/standardplugins/keys.go`

### Inspiration and supporting external implementations

The inspiration repository currently pins `@cursor/sdk` exactly, requires a Node runtime, uses an explicit SDK API key rather than Cursor CLI/Desktop login state, and implements substantial lifecycle code around agent creation, reuse, busy state, invalidation, disposal, resumption, model discovery, event mapping, tool surfaces, and cross-platform smoke testing.

Important lessons carried into this design:

- SDK integration still needs explicit creating/ready/busy/disposed agent state.
- Pool identity must include cwd, model, API-key fingerprint, settings/safety, and tool surface.
- Agent and run finalization must execute on all error paths.
- Live, integration-shaped defects can escape large unit suites.
- SDK custom-tool cancellation is not sufficient to make it the default Go-LIP tool transport.
- SDK runtime errors can escape their originating turn, so isolation and bridge-process failure handling matter.

The supporting `cursor-go-sdk` repository demonstrates that a Go integration can use a Node adapter over the official SDK. It is treated as architecture evidence, not as a dependency decision.

## Existing Strengths to Preserve

1. **Canonical middle.** Backends already emit `lipapi.ManagedEventStream`; no frontend needs a Cursor-specific path.
2. **Core-owned orchestration.** Retry, failover, parallel races, output commitment, TTFT, and B-leg lineage already belong to the executor.
3. **Mature ACP process ownership.** The current connector provides a useful reliability baseline and fallback.
4. **Backend-qualified model registry.** The registry stores multiple rows for one canonical ID and preserves `BackendID` and `Kind`.
5. **Fail-soft inventory.** Per-backend discovery can fail without invalidating unrelated inventories.
6. **Explicit security profiles.** Standard backend registration declares credential mode and local-only scope.
7. **Runtime closer infrastructure.** `Built.Closers` exists even though backend-created process closers are not yet exposed.
8. **TDD and lifecycle gates.** Official backends require lifecycle contract tests, and goroutine-heavy packages already use race/leak evidence.

## Brownfield Requirements Gap Analysis

| ID | Severity | Finding | Gap tag | Required disposition |
| --- | --- | --- | --- | --- |
| **G-01** | P0 | The official Cursor agent SDK is not a Go dependency. | Missing | Add a project-owned Node bridge with a narrow, versioned adapter protocol. |
| **G-02** | P0 | A bridge remains a subprocess; SDK use does not remove process ownership. | Constraint | Require explicit launch, health, cancellation, restart, shutdown, and reaping behavior. |
| **G-03** | P0 | `execbackend.Backend` has no optional backend-resource closer. | Missing | Add one narrow internal lifecycle callback and register it in runtimebundle closers. |
| **G-04** | P1 | Model registry allows duplicate canonical IDs but forbids one route prefix across different backend kinds. | Partial | Publish `cursor/...` from both connectors while assigning `cursorsdk` its own backend prefix. |
| **G-05** | P0 | Current Cursor auth is CLI `cursor_login`; SDK auth is a separate API key and billing surface. | Missing | Add explicit static-key config and `CURSOR_API_KEY`; prohibit implicit CLI/Desktop auth reuse. |
| **G-06** | P1 | `UpstreamAPIKeys` has no Cursor SDK key field. | Missing | Extend composition-root key resolution and tests without logging secret material. |
| **G-07** | P0 | Current ACP model discovery parses human-readable `agent --list-models`. | Opportunity | Use structured SDK model discovery and existing fail-soft inventory contracts. |
| **G-08** | P0 | Existing ACP capabilities cannot be copied blindly to the SDK implementation. | Constraint | Advertise only bridge-proven model/capability mappings; omit unproven vision/tools/documents. |
| **G-09** | P0 | SDK host tools and configured MCP execute inside Cursor; canonical tool events mean client execution. | Semantic risk | Keep SDK tool activity internal and do not emit duplicate client tool calls. |
| **G-10** | P0 | Agent reuse can create a second hidden conversation history. | Missing | Make canonical transcript authoritative; fingerprint committed history and recreate on divergence. |
| **G-11** | P1 | SDK local persistence/resume has branch, compaction, and stale-state hazards. | Constraint | Defer `Agent.resume`; rebuild from canonical transcript after restart. |
| **G-12** | P0 | Shared bridge cancellation fallback can affect unrelated active agents. | Constraint | Use run cancel first, bounded process kill only as last resort, and surface collateral failures. |
| **G-13** | P0 | No bridge package, protocol schema, compatibility handshake, or release packaging exists. | Missing | Define a companion package, exact dependency pin, executable discovery, and no runtime installation. |
| **G-14** | P0 | SDK settings may load ambient rules/MCP/plugins and widen behavior unexpectedly. | Missing | Default settings sources to none; require explicit trusted opt-in and explicit safety policy. |
| **G-15** | P0 | SDK custom tools lack the cancellation/deadline behavior needed for a default Go-LIP tool bridge. | Constraint | Exclude custom tools and callback transport from the first delivery. |
| **G-16** | P1 | Cloud agents have different repo, tool, environment, and lifecycle semantics. | Constraint | Keep the first delivery local-only; require a later product/security specification for cloud. |
| **G-17** | P0 | Reliability superiority has not been measured and the SDK is a moving integration surface. | Unknown | Keep experimental status and define comparative, cross-platform, sustained replacement gates. |
| **G-18** | P1 | Default tests cannot rely on Node, npm, an account, or live network. | Constraint | Build fake bridge fixtures and mocked SDK tests; isolate live smoke behind explicit opt-in. |

## Requirement-to-Asset Map

| Requirement | Current support | Gap status | Notes |
| --- | --- | --- | --- |
| 1. Coexistence | Standard registry supports multiple backend kinds and instances. | Partial | New kind, docs, and explicit no-fallback behavior are needed. |
| 2. Bridge boundary | ACP has structured stdio transport patterns; no SDK bridge exists. | Missing | New adapter-private protocol and Node companion package are required. |
| 3. Auth/config | Static credential patterns and env resolution exist. | Partial | Cursor SDK key and bridge/safety config are absent. |
| 4. Inventory/caps | Model inventory and backend-qualified duplicate canonical rows exist. | Partial | Structured SDK provider, distinct backend prefix, and proven capabilities are needed. |
| 5. Session authority | ACP transcript history coordination provides a precedent. | Partial | SDK-specific agent pool and canonical history fingerprinting are absent. |
| 6. Streaming | Canonical events and managed streams exist. | Partial | SDK event mapping and internal-tool suppression are absent. |
| 7. Cancellation/recovery | Managed cancellation and core pre/post-output rules exist. | Partial | Run cancel, bridge escalation, and process-wide invalidation are absent. |
| 8. Bounds/shutdown | ACP pool patterns and `Built.Closers` exist. | Partial | Generic backend closer exposure and SDK pool limits are absent. |
| 9. Workspace/MCP/safety | Workspace and MCP config precedents exist in ACP. | Partial | SDK settings-source and sandbox policy need explicit definitions. |
| 10. Routing invariants | Fully implemented in core. | Constraint | New backend must integrate without local routing or hidden attempts. |
| 11. Diagnostics | Structured logging, inventory discovery, and security rules exist. | Partial | Bridge/SDK-specific low-cardinality status and redaction tests are needed. |
| 12. Evidence gates | Go quality, race, lifecycle, and live-test policies exist. | Partial | Node package, fake bridge, cross-platform smoke, and replacement comparison are needed. |

## Requirements Remediation

The initial requirements draft focused on a new SDK backend, structured inventory, API-key auth, and coexistence. The brownfield analysis required the following corrections before design:

- added an explicit project-owned bridge instead of implying an in-process Go SDK;
- stated that subprocess lifecycle remains a Go-LIP responsibility;
- added an optional generic backend closer and composition-root shutdown ownership;
- retained the `cursor/...` canonical namespace while assigning a distinct backend route prefix;
- separated SDK API-key auth from Cursor CLI/Desktop auth and billing assumptions;
- made capability claims evidence-based instead of copying ACP declarations;
- defined SDK host/MCP tools as agent-internal and excluded canonical client tools;
- made canonical transcript history authoritative and defined divergence invalidation;
- deferred cross-process `Agent.resume`, cloud agents, custom tools, and remote bridges;
- added fail-closed settings-source and sandbox requirements;
- defined provider-native cancellation followed by bounded bridge termination;
- prohibited runtime npm installation and required exact bridge/SDK version checks;
- added explicit process/agent limits, concurrency behavior, shutdown, race, and leak evidence;
- converted "SDK should be more reliable" into measurable replacement gates.

## Implementation Approach Options

### Option A: Replace `cursorcliacp` internals with the SDK bridge

**Description:** Keep the existing backend kind and configuration identity, but swap ACP for SDK execution.

**Advantages**

- One Cursor backend name.
- No duplicate model rows or user-facing choice after migration.
- Lower registration/config surface over the long term.

**Disadvantages**

- Breaks CLI-login compatibility and changes billing/auth posture.
- Removes a proven fallback before SDK reliability is demonstrated.
- Forces one configuration contract to represent incompatible transports.
- Makes rollback and comparative testing harder.

**Fit:** Rejected for the first delivery.

### Option B: Depend directly on the unofficial Go SDK and its Node bridge

**Description:** Use `remdev/cursor-go-sdk` as the Go API and companion bridge.

**Advantages**

- Existing Go-shaped API and bridge implementation.
- Faster proof of concept.
- Demonstrates local and cloud agent calls.

**Disadvantages**

- Adds an unofficial compatibility layer between Go-LIP and the official SDK.
- Gives Go-LIP less control over bridge schema, lifecycle, redaction, and release cadence.
- Still requires Node, process ownership, and bridge installation.
- Its public surface is broader than Go-LIP's first delivery needs.

**Fit:** Acceptable as reference or throwaway probe, not selected as the production boundary.

### Option C: Add `cursorsdk` with a project-owned minimal stdio bridge

**Description:** Create a separate backend package and a companion Node package pinned to an exact official SDK version. Use versioned NDJSON RPC over stdio. Keep ACP unchanged.

**Advantages**

- Preserves explicit rollback and A/B comparison.
- Keeps SDK and Node types at the adapter edge.
- Uses standard-library Go process and JSON primitives.
- Avoids a loopback listener and runtime npm installation.
- Lets the bridge contract remain limited to Go-LIP requirements.
- Fits external connector packaging (module + private companion) once revalidated against backend-connector-plugin-architecture.

**Disadvantages**

- Adds a companion package and cross-language release process.
- Requires new process-lifecycle and protocol tests.
- Duplicates some lifecycle concepts already present in ACP, although not its protocol.
- Users must choose and configure one of two Cursor connector kinds.

**Fit:** Selected.

## Recommended Design Direction

**Phase 8.3 supersession:** the 2026-07-17 draft that placed the adapter under `internal/plugins/backends/cursorsdk/` is **withdrawn**. Delivery is an external connector only.

1. Deliver `connectors/cursorsdk` as an independent Go module with `release.yaml` and closed `golip.backendplugin.manifest/v1` (factory kind `cursorsdk`); never `internal/plugins/backends/cursorsdk`.
2. Host consumes only public `pkg/lipsdk/backendplugin`, trusted-directory discovery, digest-bound exact-executable launch, approved secure local IPC, and lazy activation.
3. Package a project-owned Node companion under `connectors/cursorsdk/bridge-node/` (`private_companions`), pinned to an exact `@cursor/sdk` version; no root `package.json` / Node SDK dependency.
4. Declare closed-manifest `process_sharing: per_instance` with secret/concurrency/failure isolation justification; within one instance, at most one bridge process and a bounded agent pool.
5. Use versioned NDJSON RPC over adapter-private stdio with strict frame limits and stderr-only bridge diagnostics.
6. Keep Go ownership of canonical transcript fingerprinting, runtime keys, canonical event mapping, managed cancellation, and process-tree cleanup.
7. Keep Node ownership of official SDK objects, agent/run state, SDK event normalization, and SDK-specific error extraction.
8. Credential posture remains `static` + `local_only`; secrets via host Configure injection (`CURSOR_API_KEY` / config), never argv.
9. Inventory uses a distinct backend prefix with canonical `cursor/...` rows coexisting with `cursorcliacp`.
10. Default settings sources to none; require an explicit workspace and sandbox-required posture.
11. Keep `Agent.resume`, cloud, custom tools, client tool passthrough, automatic connector fallback, and post-content failover out of scope.
12. Require fake-bridge Go tests, mocked-SDK Node tests, opt-in live smoke, comparative dogfood evidence, and `make kiro-spec-check SPEC=cursor-sdk-backend`.

## Research Carried into Implementation

The design is sufficient for task generation, but implementation Task 1 intentionally revalidates these exact-version contracts before production code:

- official SDK model-list result and model parameter shape;
- local agent create/send/stream/cancel/dispose signatures;
- exact reasoning, usage, terminal, and error event semantics;
- SDK sandbox behavior on Linux, macOS, and Windows;
- whether API keys can be supplied per agent without process environment leakage;
- child-process behavior and whether bridge shutdown fully terminates SDK-controlled local processes;
- supported Node versions and platform optional-package installation;
- stable error classification fields that can be reduced to Go-LIP codes.

A failed probe narrows capability/config mapping; it does not authorize widening scope or leaking SDK types.

## Design Validation Stage

The first design revision was reviewed against the final requirements, current runtime assembly, model registry, canonical stream lifecycle, and Kiro design rules.

### Critical Issue 1: No runtime-owned bridge shutdown path

**Concern:** The initial design let the backend own a process manager but did not identify how the standard runtime would invoke it during shutdown or partial build failure.

**Impact:** A configured bridge could outlive Go-LIP, leak SDK child processes, or survive a model-registry startup failure.

**Correction:** The final design adds an optional internal `execbackend.Backend.Close` callback. `buildModelRuntime` registers backend closers immediately after backend construction and before later startup work. `Built.Closers` remains the composition-root owner.

**Traceability:** 2.7, 7.3, 8.5-8.9.

**Evidence:** `design.md` sections ÔÇťBackend Lifecycle Seam,ÔÇŁ ÔÇťBridge Shutdown,ÔÇŁ and ÔÇťFailure During Assembly.ÔÇŁ

### Critical Issue 2: Tool activity and capability semantics were ambiguous

**Concern:** The initial design described SDK native tool events without distinguishing internal Cursor execution from canonical client tool calls.

**Impact:** Mapping native tool activity to canonical tool events could cause a frontend client to execute a tool a second time, while claiming `CapabilityTools` would accept unsupported client tool definitions.

**Correction:** The final design treats SDK host/MCP activity as internal, never emits it as canonical client tool calls, and omits tools/parallel-tools capabilities. Vision/documents/structured-output capabilities are likewise opt-in only after exact-version proof.

**Traceability:** 4.6-4.8, 6.6-6.8, 9.2-9.3.

**Evidence:** `design.md` sections ÔÇťCapability Profile,ÔÇŁ ÔÇťSDK Event Mapping,ÔÇŁ and ÔÇťTool Surface Boundary.ÔÇŁ

### Critical Issue 3: Packaging, ambient settings, and trust posture were under-specified

**Concern:** The initial design assumed a Node bridge would be ÔÇťavailableÔÇŁ and allowed SDK defaults without defining installation, version compatibility, settings sources, or sandbox behavior.

**Impact:** Runtime npm installation would be non-reproducible and unsafe; ambient Cursor config could widen MCP/rule/plugin access; incompatible bridge/SDK versions could fail during requests.

**Correction:** The final design uses an explicitly installed project-owned companion executable, exact version handshake, no runtime installation, settings sources defaulting to none, explicit trusted opt-in, and sandbox-required default with affirmative local-only override.

**Traceability:** 2.2, 2.6, 3.5-3.8, 9.4-9.8, 11.6.

**Evidence:** `design.md` sections ÔÇťDistribution and Versioning,ÔÇŁ ÔÇťConfiguration Contract,ÔÇŁ and ÔÇťSecurity Considerations.ÔÇŁ

## Design Strengths

- The selected approach preserves the canonical middle and core-owned routing while isolating all SDK-specific behavior behind a narrow backend anti-corruption layer.
- Coexistence provides an empirical comparison and rollback path instead of converting a hypothesis about reliability into a destructive migration.

## Design Validation Checklist

- **Requirement coverage:** every acceptance criterion maps to at least one design component and task.
- **Dependency direction:** official SDK and bridge types stay under `connectors/cursorsdk` (never root / `internal/plugins/backends/cursorsdk`).
- **Canonical contracts:** no `pkg/lipapi` schema changes; host uses public `pkg/lipsdk/backendplugin` only.
- **Routing:** no hidden connector/model retries; core remains authoritative; no failover after first content.
- **Streaming:** deltas remain incremental and ordered.
- **Tools:** native agent tools are not replayed as client tool calls.
- **Capabilities:** unproven semantics are omitted.
- **History:** canonical transcript wins; divergence recreates the SDK agent.
- **Cancellation:** run cancel precedes bridge/process escalation and process-tree cleanup.
- **Shutdown:** plugin Close + host processhost reap own bridge termination and child reaping.
- **Process model:** closed-manifest `per_instance` with documented secret/concurrency/failure isolation.
- **Model inventory:** same `cursor/...` IDs coexist through backend-qualified rows and distinct backend prefixes.
- **Security:** static key, local-only registration, no ambient settings by default, no runtime installation, no secret-bearing argv/logs.
- **Testing:** deterministic defaults plus isolated live and cross-platform gates; `make kiro-spec-check SPEC=cursor-sdk-backend`.
- **Migration:** ACP remains supported as external connector; replacement requires separate evidence-based change.

## Final Validation Verdict

**PASS after corrections (2026-07-17).**

The final design is suitable for task generation. It preserves Go-LIP's canonical, routing, streaming, security, and lifecycle invariants; it accurately represents the SDK integration as a managed sidecar rather than an in-process replacement; and it leaves unsupported or insufficiently proven SDK surfaces outside the first delivery.

## Phase 8.3 Revalidation (2026-07-20T10:34:00+02:00)

Reviewed against active `.kiro/specs/archive/backend-connector-plugin-architecture/` after OpenCode/Codex externalization.

| Finding | Disposition |
| --- | --- |
| G-07 from backend-connector research: Cursor SDK would introduce Node under the root connector tree | **Remediated in this revalidation** ÔÇö Node/`@cursor/sdk` confined to `connectors/cursorsdk/bridge-node` private companion |
| Prior design owned `internal/plugins/backends/cursorsdk/` and root registration | **Superseded** ÔÇö external module + closed manifest discovery; root static registration forbidden |
| Prior design added `execbackend.Backend` closer in root | **Narrowed** ÔÇö plugin Close + host processhost lifecycle; no Cursor-specific root factory deps |
| Host trust/IPC | **Aligned** ÔÇö digest-bound exact executable + approved secure local IPC + lazy activation |
| Process sharing | **Declared `per_instance`** with secret/concurrency/failure isolation justification |
| Architecture gates | **Encoded** in Req 13 + `file-plan.md` + `make kiro-spec-check SPEC=cursor-sdk-backend` |

**Verdict:** Spec revalidation PASS for Task 8.3. Product implementation remains blocked until a later approved tasks wave; root-tree implementation remains forbidden.

## Merge note (2026-07-27T00:45:00+02:00)

origin/main landed an experimental in-tree adapter under `internal/plugins/backends/cursorsdk` with exact `@cursor/sdk` 1.0.23 Node bridge evidence, live/platform Makefile targets, and coexistence tests. The hybrid connector branch forbids root static registration of optional connectors.

**Merged Go reality (updated):** the in-tree adapter was externalized to `connectors/cursorsdk` (module + `bridge-node` companion). Root `ExperimentalCursorSDKRegistration` and `internal/plugins/backends/cursorsdk` are removed. Production optional delivery is manifest-discovered only; the kind stays outside `EssentialBackendBundle` / `InstallStandardBundleOn`. Human macOS local-skip decisions from platform evidence remain unchanged. Exact-version package evidence from main (Node `>=22.13`, Windows x64 binary package presence, no Windows arm64 package for 1.0.23) remains valid research input.
