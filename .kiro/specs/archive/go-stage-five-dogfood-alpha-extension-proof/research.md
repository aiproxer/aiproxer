# Implementation Gap Analysis: Go Stage Five Dogfood Alpha Extension Proof

Generated: 2026-04-29T18:07:05Z

## Executive Summary

The current repository already has the core runtime, standard bundle composition, HTTP server wiring, extension platform, diagnostics inventory, reference clients/backends, and several reference feature plugins needed for Stage 5. The main gap is not core architecture; it is packaging those capabilities into dogfoodable operator workflows with no-key local configs, default-suite smoke coverage, complete diagnostic visibility, and proof-plugin evidence.

The highest implementation risks are: accidentally reusing test-only reference backends on production paths, leaving diagnostics open on non-local binds without a secret, relying on `integration`-tagged conformance tests for dogfood confidence, and letting proof plugins imply core-owned advanced feature ports.

Requirements are not yet approved in `spec.json`; this analysis is safe to use for design planning and requirement refinement.

## Current State Investigation

### Existing Assets

- `cmd/lipstd/main.go` loads config, initializes tracing/logging, installs the standard bundle, validates mandatory bundled factories, merges feature surfaces, builds runtime, and serves via `stdhttp.RunWithRuntime`.
- `internal/pluginreg/standardbundle/install.go`, `internal/pluginreg/standard_table.go`, and `pkg/lipsdk/standard_bundle.go` provide explicit standard bundle composition and mandatory plugin IDs.
- `internal/infra/runtimebundle` composes executor, stores, backend plugins, extension snapshots, traffic/usage/capture seams, secure sessions, shared HTTP clients, and route defaults.
- `internal/stdhttp` mounts bundled frontends, diagnostics, security guard, auth middleware, metrics, pprof, and model catalog diagnostics.
- `config/config.yaml` is a rich reference config; `config/config.multi-instance.example.yaml` covers multi-instance routing. No dedicated `config/examples/local-stub.yaml` or protocol-specific no-key stub examples were found.
- `internal/core/diag/inventory.go` and `internal/core/diag/inventory_extensions.go` expose plugin inventory, legal extension pipeline, per-feature stage occupancy, bundle errors, and privileged capabilities.
- `internal/refbackend` and `internal/refclient` provide test-only emulator servers and official-SDK-shaped clients for conformance; docs and architecture tests warn they must not appear on production paths.
- `internal/plugins/features` includes no-op features plus reference plugins such as `refautoappend`, `reftoolpolicy`, `reftraffictranscript`, `refverifier`, and `refworkspaceguard`.
- `internal/archtest` already includes import and boundary guardrails for extension platform and hexagonal constraints.

### Existing Patterns and Conventions

- Runtime serving currently has one default CLI path: `lipstd --config <path>` serves immediately. There are no discovered `check-config`, `inventory`, `routes`, or explicit `serve` subcommands.
- Tests commonly assemble runtime pieces directly with `runtimebundle.Build`, `MountBundledFrontends`, and `httptest`; `internal/stdhttp/standard_wiring_roundtrip_test.go` covers one OpenAI Responses standard-wiring path.
- Broad cross-frontend conformance exists in `internal/testkit/conformance`, but some key tests are behind `//go:build integration`, so they do not protect the default quick test path.
- Config validation already covers many diagnostics path and secret-shape rules, but `diagnostics.shared_secret` is only length-validated when set. Secure-session diagnostics require it when summaries are exposed, but generic diagnostics/metrics/pprof on non-local binds need additional validation analysis.
- The extension platform uses typed `FeatureBundle` contributions and immutable runtime snapshots. Recent work added tool-call policies and usage observers through SDK, runtime, pluginreg, inventory, and tests.

### Key Constraints from Steering

- The product must preserve a small policy-owning core, canonical request/event contracts, streaming-first execution, and explicit capability failures.
- Routing, failover, B2BUA continuity, and no-retry-after-first-output are core-owned behavior.
- Feature behavior belongs behind extension seams; feature plugins must not import core internals or provider SDKs.
- Official provider SDKs belong only at adapter/refclient boundaries, never in `pkg/lipapi`, `pkg/lipsdk`, or `internal/core`.
- Diagnostics must preserve trust boundaries and must not expose secrets, resume tokens, or raw unredacted capture data.

## Requirement-to-Asset Map

| Requirement | Existing Assets | Gap Tag | Gap Summary |
| --- | --- | --- | --- |
| R1 Alpha server operator workflow | `cmd/lipstd/main.go`, `runtimebundle`, `stdhttp`, `pluginreg` | Missing | Serve path exists, but validation/routes/inventory subcommands are not present as operator workflows. |
| R2 No-key local dogfood configs | `config/config.yaml`, `config/config.multi-instance.example.yaml`, refbackend test emulators | Missing / Constraint | Reference config exists, but no dedicated no-key local stub config examples; test-only refbackend must not become production wiring accidentally. |
| R3 Stub-backed reference-client smoke paths | `internal/refclient`, `internal/refbackend`, `internal/testkit/conformance`, `standard_wiring_roundtrip_test.go` | Missing / Constraint | Broad conformance exists but is partly integration-tagged and often bypasses full `lipstd` middleware path; only one standard-wiring roundtrip was found. |
| R4 Extension proof plugins | `refautoappend`, `reftoolpolicy`, `reftraffictranscript`, `refverifier`, `refworkspaceguard`, `FeatureBundle` | Partial | Reference plugins exist and are registered, but design should define which are proof scope and ensure they demonstrate the newest seams without becoming production feature ports. |
| R5 Inventory and diagnostics visibility | `InventoryHandler`, `InventoryExtensions`, stage occupancy tests, privilege flags | Partial | Core inventory exists and now includes additional seams, but Stage 5 must verify command/server exposure, redaction, disabled/error states, and doc consistency. |
| R6 Startup and diagnostic safety | `stdhttp/security_guard.go`, `config.Validate`, diagnostics path/secret validation, access/auth model | Partial / Unknown | Local no-auth posture is enforced; generic non-local diagnostics/metrics/pprof secret posture may require additional validation. Research needed in design. |
| R7 Regression and boundary guardrails | `internal/archtest`, hook validation, runtime attempt-stream tests | Partial | Boundary tests exist; dogfood-specific no-retry-after-output and standard-server smoke invariants need explicit coverage. |
| R8 Operator docs and migration clarity | `README.md`, `docs/architecture.md`, `docs/runtime-flow.md`, `docs/extension-points.md`, `docs/plugin-authoring.md`, `docs/feature-migration-map.md` | Partial | Docs exist, some may be untracked or recently changed; Stage 5 needs consistency checks and concise dogfood workflow docs. |

## Missing Capabilities and Integration Challenges

### 1. CLI Operator Workflows

Current `cmd/lipstd` serves after config load. Requirements ask for observable validation, route, inventory, and serve workflows. The implementation gap is a command interface that reuses the existing bootstrap path without duplicating runtime assembly or weakening startup validation.

Risks:
- Duplicating bootstrap logic between commands and serve path.
- Initializing tracing/logging or opening backend resources during read-only commands unnecessarily.
- Breaking existing `lipstd --config` serve behavior.

### 2. No-Key Local Stub Operation

The repository has test-only reference backends and reference clients, but not obvious operator-facing stub backend config examples. Requirements need no-key local dogfood, while structure steering says `internal/refbackend` is test-only.

Risks:
- Importing `internal/refbackend` into production standard distribution would violate support-surface boundaries.
- Using hosted backend configs with empty keys may fail startup or confuse operators.
- Example configs may accidentally expose diagnostics or imply provider parity.

Research Needed:
- Whether an existing production-safe local stub backend plugin exists outside `internal/refbackend`; none was found in this pass.
- Whether Stage 5 should introduce a bundled dev-only local-stub backend plugin or keep no-key stub workflows test-only plus documented commands.

### 3. Full-Stack Smoke Coverage

Existing conformance tests use refclients/refbackends and frontend mounts, but they often mount frontends directly or require `integration` tags. The Stage 5 requirement asks for stub-backed smoke paths that prove dogfood alpha behavior.

Risks:
- Tests that bypass `stdhttp.RunWithRuntime` or auth/security middleware can miss command/server drift.
- Full conformance may be too broad for quick developer workflows.
- Live-provider tests must remain optional and environment-gated.

### 4. Proof Plugin Scope

Reference plugins already exist, but proof-plugin requirements need a curated proof set with operator-visible behavior. The risk is turning reference plugins into broad Python feature ports rather than narrow seam demonstrations.

Risks:
- `refverifier` may depend on auxiliary behavior that is still disabled by default in some paths.
- Capture/transcript behavior must redact before persistence or exposure.
- Tool policy should use both catalog filtering and emitted tool-call policy seams where relevant.

### 5. Diagnostics and Secrets

Inventory is powerful and must remain safe. `diag.WrapDiagnosticsProtect` allows pass-through when the shared secret is empty; this is safe only when startup/config posture ensures exposure is local or deliberately protected.

Risks:
- Non-local diagnostics, metrics, pprof, or session summaries could be exposed without a secret.
- Inventory could reveal plugin config values, route metadata, or capture details beyond safe operator metadata.

Research Needed:
- Exact relationship between server bind, access mode, diagnostics enabled, metrics path, pprof path, and `diagnostics.shared_secret` in current config validation.

## Implementation Approach Options

### Option A: Extend Existing Components

Extend `cmd/lipstd`, `internal/stdhttp`, `internal/core/diag`, existing config examples, existing reference plugins, and existing conformance tests.

Likely modules:
- `cmd/lipstd/*` for subcommands and command output.
- `internal/infra/runtimebundle` for reusable build/inspection helpers if needed.
- `internal/core/diag` for inventory completeness.
- `config/examples/*` for no-key/local/live examples.
- `internal/testkit/conformance` or `internal/stdhttp` for smoke tests.
- Existing `internal/plugins/features/ref*` packages for proof coverage.

Trade-offs:
- Pros: fastest path, reuses established wiring and tests, minimal new abstractions.
- Cons: risk of bloating `cmd/lipstd/main.go`; local stub backend may not fit existing production plugin set; CLI command responsibilities may become tangled with serve startup.

Effort: M (3-7 days). Risk: Medium due to diagnostics safety and full-stack smoke breadth.

### Option B: Create New Dogfood Support Components

Create dedicated packages for command handling, example validation, smoke harnesses, and possibly a production-safe local stub backend plugin.

Candidate modules:
- `cmd/lipstd` command package or helper files for subcommands.
- `internal/plugins/backends/localstub` or similar if design chooses a standard no-key backend plugin.
- `internal/testkit/dogfood` for standard-server smoke harnesses.
- Dedicated docs under `docs/dogfood-alpha.md` or updates to existing runtime docs.

Trade-offs:
- Pros: clearer separation, avoids overloading conformance or main, makes no-key stub semantics explicit.
- Cons: more files and a new backend-like capability require careful boundary decisions; local stub plugin could be mistaken for production provider support.

Effort: L (1-2 weeks). Risk: Medium/High if a new backend plugin is introduced without strict scope and naming.

### Option C: Hybrid Approach

Extend existing composition and diagnostics while adding only narrow new support where current assets cannot satisfy requirements.

Suggested shape:
- Add small command handling around existing bootstrap paths.
- Reuse existing reference clients/backends for tests only.
- Add a clearly named local-stub backend plugin only if required for operator no-key serving.
- Harden existing proof plugins rather than creating new feature families.
- Add focused dogfood smoke tests separate from broad conformance.

Trade-offs:
- Pros: balances speed and clarity, respects current boundaries, reduces core churn.
- Cons: requires design discipline to prevent dogfood support from splitting into too many near-duplicate harnesses.

Effort: M/L (about 1 week depending on local-stub plugin decision). Risk: Medium.

## Recommendations for Design Phase

### Preferred Direction to Evaluate

Use the hybrid approach. Most Stage 5 needs can extend existing components, but no-key operator-facing stub behavior likely requires either a deliberately scoped local-stub backend plugin or an explicit decision to keep stub operation test-only. This is the primary design decision.

### Design Decisions to Make

1. CLI compatibility: whether `lipstd --config` continues to serve by default, and how subcommands parse flags.
2. Local stub strategy: production-safe dev plugin vs test-only harness plus docs.
3. Smoke placement: default test suite vs `integration` tag vs separate make target.
4. Inventory output format: stable JSON for commands/docs vs current HTTP diagnostics JSON only.
5. Diagnostic posture: exact rule for non-local diagnostics/metrics/pprof/shared-secret combinations.
6. Proof plugin set: which reference plugins graduate as Stage 5 proof plugins and which remain examples only.
7. Live-provider smoke: optional scripts/tests and environment variable gates.

### Research Needed

- Confirm whether any non-test local/stub backend plugin already exists or is planned.
- Audit config validation for diagnostics, metrics, pprof, and secure-session summary exposure on non-local binds.
- Check current `docs/extension-points.md`, `docs/plugin-authoring.md`, and `docs/feature-migration-map.md` status because they may be untracked or recently generated.
- Determine whether `internal/testkit/conformance` integration-tagged tests should be mirrored by a smaller default smoke suite.

## Complexity and Risk

- Overall effort: M/L. Most pieces exist, but making them operator-facing and safe requires cross-package work.
- Overall risk: Medium. Architecture is mature, but diagnostics safety and test/production boundary separation are easy to get wrong.
- Highest-risk requirement: R2 if no-key local serving needs a new backend plugin, and R6 if diagnostics exposure validation is incomplete.
- Lowest-risk requirement: R8 documentation updates, provided source-of-truth docs are already aligned.

---

# Design Discovery Update: Stage Five Dogfood Alpha Extension Proof

Generated: 2026-04-29T18:31:22Z

## Summary

- **Feature**: `go-stage-five-dogfood-alpha-extension-proof`
- **Discovery Scope**: Extension, using light discovery plus existing gap analysis.
- **Key Findings**:
  - Existing runtime and extension architecture are sufficient; the design should add operator packaging and evidence rather than core orchestration changes.
  - A production-safe local stub backend is the cleanest way to satisfy no-key standard-server dogfood without importing test-only reference backends into production paths.
  - CLI inventory and HTTP inventory should share the same `diag.InventorySnapshot` builder to avoid drift and secret leaks.

## Research Log

### CLI and Standard Runtime Entry
- **Context**: Requirements 1.1-1.5 need validation, route, inventory, and serve workflows.
- **Sources Consulted**: `cmd/lipstd/main.go`, `cmd/lipstd/wiring.go`, `internal/infra/runtimebundle`, `internal/stdhttp/server.go`.
- **Findings**:
  - `cmd/lipstd` currently serves immediately after `--config` parsing.
  - Bootstrap logic combines config load, registry install, feature merge, app build, runtime build, and serving in one function.
  - The serve path is mature and should remain the default compatibility behavior.
- **Implications**:
  - Design uses a small command dispatcher and shared bootstrap helper instead of duplicating command-specific startup logic.

### Local Dogfood Backend Strategy
- **Context**: Requirements 2.1-2.6 and 3.1-3.6 require no-key dogfood and stub-backed smoke paths.
- **Sources Consulted**: `internal/refbackend`, `internal/refclient`, `internal/pluginreg/standard_table.go`, `internal/stdhttp/standard_wiring_roundtrip_test.go`.
- **Findings**:
  - Reference backends and clients are test-only support surfaces.
  - No existing production-safe local stub backend was found outside test support.
- **Implications**:
  - Design introduces a deliberately scoped optional local-stub backend plugin. It is not mandatory and is documented as deterministic dogfood support, not provider parity.

### Inventory and Diagnostics Safety
- **Context**: Requirements 5.1-5.6 and 6.1-6.6 require active extension visibility and safe diagnostics exposure.
- **Sources Consulted**: `internal/core/diag/inventory.go`, `internal/core/diag/inventory_extensions.go`, `internal/core/diag/auth.go`, `internal/stdhttp/server.go`, `internal/core/config/validate.go`.
- **Findings**:
  - HTTP inventory already serializes plugin rows and extension occupancy without config payloads.
  - `WrapDiagnosticsProtect` deliberately passes through when shared secret is empty.
  - Config validation only enforces secret length when a secret is present, with stricter checks for secure-session summary exposure.
- **Implications**:
  - Design adds explicit diagnostics exposure validation for non-local trust boundaries and a shared inventory snapshot builder for CLI and HTTP.

### Proof Plugins and Extension Seams
- **Context**: Requirements 4.1-4.5 need representative proof plugins.
- **Sources Consulted**: `internal/plugins/features/refautoappend`, `reftoolpolicy`, `reftraffictranscript`, `refverifier`, `pkg/lipsdk/feature`, `pkg/lipsdk/toolpolicy`, `pkg/lipsdk/usage`.
- **Findings**:
  - Existing reference plugins align with the desired proof set.
  - Recent tool policy and usage observer seams close the biggest missing proof gaps.
- **Implications**:
  - Design hardens and documents existing proof plugins rather than adding broad new Python-era features.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|--------|-------------|-----------|---------------------|-------|
| Extend existing components | Add commands, configs, tests, and docs around existing runtime | Fast, low churn | May bloat `main.go`; no production stub answer | Good for CLI and docs |
| New dogfood support package | Add dedicated command/test/stub components | Clear boundaries | More files and boundary decisions | Useful for local-stub and smoke harness |
| Hybrid | Extend composition/read models and add only local-stub plus focused smoke support | Balances clarity and reuse | Requires careful task boundaries | Selected for design |

## Design Decisions

### Decision: Preserve default serve compatibility
- **Context**: Existing users run `lipstd --config ./config/config.yaml`.
- **Alternatives Considered**:
  1. Make `serve` mandatory.
  2. Keep default serve and add explicit subcommands.
- **Selected Approach**: Keep default serve compatibility; add `serve`, `check-config`, `routes`, and `inventory` as explicit command names.
- **Rationale**: Adds operator workflows without breaking current quickstart.
- **Trade-offs**: Command parsing must distinguish legacy flags from subcommands.
- **Follow-up**: Add tests for legacy invocation and explicit commands.

### Decision: Add optional local-stub backend plugin
- **Context**: No-key standard-server dogfood cannot use `internal/refbackend` in production wiring.
- **Alternatives Considered**:
  1. Use test-only refbackend only.
  2. Add optional deterministic local-stub backend.
  3. Require live provider keys for dogfood.
- **Selected Approach**: Add optional local-stub backend plugin, available in standard registry but not mandatory.
- **Rationale**: Satisfies operator dogfood while preserving test/production boundaries.
- **Trade-offs**: Adds one backend plugin with narrow scope and documentation burden.
- **Follow-up**: Architecture tests must keep refbackend/refclient out of production paths.

### Decision: Share inventory snapshot builder
- **Context**: CLI and HTTP inventory must remain consistent and secret-safe.
- **Alternatives Considered**:
  1. CLI calls HTTP handler internally.
  2. CLI reimplements inventory formatting.
  3. Extract shared snapshot construction.
- **Selected Approach**: Extract or expose a shared `InventorySnapshotForConfig`-style function.
- **Rationale**: Avoids duplication and drift while preserving existing JSON contract.
- **Trade-offs**: Small API adjustment inside `internal/core/diag`.
- **Follow-up**: Add CLI/HTTP parity and no-secret tests.

## Risks & Mitigations

- Test-only refbackend leakage — mitigate with architecture tests and local-stub production plugin.
- Diagnostics exposure without secret — mitigate with config validation for non-local diagnostics/metrics/pprof/session surfaces.
- Smoke tests too slow or broad — mitigate with a small default-suite smoke set and leave full conformance separate.
- Proof plugin scope creep — mitigate by documenting proof-only semantics and preserving disabled-by-default behavior.
