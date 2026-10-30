# Brownfield Requirements Gap Analysis

## Scope and Method

This analysis compares the first requirements draft for `agent-runtime-routing-composition-safety` with repository `main` at `d0224a9de48c2e6e5a57b559f4f755ee4029c95d`.

The review covers:

- selector AST/parser/compiler/planner under `internal/core/routing`;
- normal request route-plan construction under `internal/core/runtime`;
- A-leg route-override preflight and persistence integration;
- backend factory registration under `internal/pluginreg` and `internal/standardplugins`;
- configured backend assembly under `internal/infra/runtimebundle`;
- executable connector manifest types/parsing/discovery;
- Codex dual-export packaging;
- Cursor SDK agent/tool/MCP semantics;
- model registry/native-model binding boundaries;
- steering rules for routing, plugins, streaming, testing, and architecture.

Classifications:

- **Missing** — required capability/contract does not exist.
- **Partial** — reusable machinery exists but does not satisfy the requirement.
- **Constraint** — current behavior/API constrains the solution.
- **Risk** — an initially plausible design would create a correctness or maintenance hazard.
- **Asset** — existing seam should be reused rather than replaced.
- **Deferred** — possible future refinement intentionally excluded.

Effort:

- **S** — focused type/validation/test change.
- **M** — multi-package contract/composition change.
- **L** — broad public/ABI/routing migration; should be avoided here.

## Current Assets Worth Preserving

### Pure selector compilation

`routing.CompileSelector` already owns alias resolution, parsing, model-only defaulting, and unresolved model-only rejection. It allocates no B-leg and opens no backend. This is the correct predecessor to execution-composition validation.

### Syntax-only parser and typed AST

The parser models primary/failover/weighted/parallel/thinker shapes without backend knowledge. This is a good boundary. The new feature can walk the AST without changing selector grammar.

### Generation/request sequencing

`runtime.buildRoutePlan` currently compiles the selector before native-model binding, affinity resolution, interleaved-state loading, billing authorization, and backend open. A new pure check can fail early without restructuring execution.

### Existing admin selector validator

The route-override generation validator already compiles selectors and rejects unknown backends before persistence. This gives the feature a second consumer for one shared semantic validation helper.

### Factory kind vs configured instance ID

`runtimebundle.buildBackends` already has both `FactoryID()` and `InstanceID()`, and executor backends are keyed by configured instance ID. This directly supports factory-declared metadata projected into generation-bound routing identities.

### Closed per-export connector manifest

Executable plugin manifests already enumerate individual exported factory kinds with credential/access/process metadata. The class can be additive per export; no connector runtime round trip is required.

## Gap Register

| ID | Severity | Class | Effort | Current finding | Required disposition |
|---|---:|---|---:|---|---|
| G-01 | P0 | Missing | M | There is no execution semantic distinct from canonical capabilities/security posture. | Add explicit `inference` / `agent_runtime` execution metadata plus effective compatibility `unknown`. |
| G-02 | P0 | Risk | S | `BackendSecurityProfile` is tempting to reuse but only describes credentials/access scope. | Keep execution metadata separate; test local-only inference as counterexample. |
| G-03 | P0 | Risk | S | Discovered/process-backed connector status could be mistaken for agent semantics. | Forbid inference from registration source/process sharing. |
| G-04 | P0 | Missing | M | Closed connector `Export` has no execution class. | Add bounded optional per-export manifest metadata and strict validation. |
| G-05 | P0 | Constraint | M | Manifest parsing rejects unknown fields. | Update manifest wire type/parser/validator atomically; old manifests omitting the field remain valid. |
| G-06 | P0 | Constraint | M | Codex connector artifact exports both `openai-codex` and `openai-codex-app-server`. | Classify per export/factory kind, never per plugin artifact. |
| G-07 | P0 | Asset | S | Plugin registry already owns factory metadata keyed by factory kind. | Add a focused execution profile/facet alongside, not inside, security profile. |
| G-08 | P0 | Asset | S | `buildBackends` already maps factory kind to configured instance ID. | Project execution class to immutable generation instance map at assembly. |
| G-09 | P0 | Missing | S/M | Executor has no generation-bound execution-class resolver/map. | Add a narrow core-facing immutable resolver/view in routing runtime. |
| G-10 | P0 | Asset | S | `CompileSelector` is side-effect-free and shared-worthy. | Validate the compiled AST after alias/defaulting, not raw strings. |
| G-11 | P0 | Constraint | S | Parser is syntax authority and should remain backend-agnostic. | Do not add ACP/Cursor/Codex checks or execution metadata to parser grammar. |
| G-12 | P0 | Missing | M | No semantic validator distinguishes direct routing from composition based on backend execution class. | Add pure recursive AST validation. |
| G-13 | P0 | Risk | M | Original issue framed the rule primarily as mixed agent-runtime + normal model. | Strengthen default rule: any composite selector requires every known backend to be explicit `inference`, including agent+agent. |
| G-14 | P0 | Risk | M | Existing B2BUA pre-output failover rule could be assumed safe for agent runtimes. | Include ordered failover in safe-mode denial. |
| G-15 | P0 | Constraint | S | Cursor SDK tools/MCP can execute inside the agent without canonical tool-call projection. | Treat “no downstream output” as insufficient side-effect evidence. |
| G-16 | P0 | Missing | S | No operator config selects execution-composition policy. | Add `safe` default and explicit `unrestricted`; reject unknown values. |
| G-17 | P0 | Risk | S | A client/per-selector escape hatch would nullify the default guard. | Make opt-out operator configuration only. |
| G-18 | P0 | Partial | M | Route-override admin validation compiles/rejects unknown backends but cannot check execution class. | Reuse the same generation-bound semantic validator before persistence. |
| G-19 | P0 | Partial | M | Normal request path compiles the selector but does not perform class validation. | Insert validation before native/dynamic planning state and billing/open. |
| G-20 | P1 | Partial | S/M | Configured/default route build has syntax/backend checks but no execution composition check. | Validate candidate generation defaults before publication/request execution. |
| G-21 | P0 | Constraint | S | Aliases are regexp rewrites and may expand arbitrary matching raw selector strings. | Validate after alias expansion at use time; do not attempt to enumerate all aliases statically. |
| G-22 | P0 | Constraint | S | Persisted route overrides store raw selector strings and survive generation changes. | Do not rewrite/clear them; later generation revalidates and may reject. |
| G-23 | P0 | Missing | S | Legacy third-party metadata has no class. | Direct known-unknown stays compatible; safe composition with unknown fails closed. |
| G-24 | P0 | Risk | M | Treating unknown class as inference would silently bypass safety for old third-party agent connectors. | Keep an explicit/effective unknown state with conservative composite policy. |
| G-25 | P1 | Constraint | S | Unknown backend identity and unknown execution class are different errors. | Preserve existing missing-backend semantics; resolver must distinguish configured-unknown-class from absent backend. |
| G-26 | P0 | Risk | L | Adding class to `pkg/lipapi` would make topology look like canonical model capability. | Do not change canonical call/event/capability contracts. |
| G-27 | P1 | Risk | L | Adding a runtime backend-plugin gRPC ABI field is unnecessary because host knows manifest before execution. | Keep execution class in install/registration metadata; no ABI feature/minor solely for this change. |
| G-28 | P1 | Partial | S/M | Standard contribution metadata has registration/security/contract facets but no execution facet. | Add the narrowest focused metadata surface needed; avoid a service-locator descriptor. |
| G-29 | P0 | Missing | S/M | No typed unsafe-composition error exists. | Add bounded typed routing error and standard invalid-request mapping. |
| G-30 | P1 | Risk | S | Raw selector in error/logging would leak high-cardinality/internal route detail. | Error carries bounded composition/backend/class/policy facts, not full selector. |
| G-31 | P0 | Missing | M | No test proves unsafe selection fails before backend/open and dynamic state mutation. | Add counters/barriers/fakes for zero dispatch and state invariants. |
| G-32 | P0 | Missing | S | No architecture guard prevents provider-name class switches in core. | Add import/content/registration-derived guard as appropriate. |
| G-33 | P1 | Deferred | L | Some agent runtimes might eventually prove pre-output idempotency/side-effect freedom. | Defer selective per-mode safety capabilities to a future evidence-backed spec. |
| G-34 | P1 | Constraint | S/M | Internal test helpers can construct `runtime.Executor` directly with fake `Backends` and no generation metadata. | Do not make nil metadata mean unrestricted/inference in production; update test builders to classify ordinary fake backends explicitly and allow focused tests to override class. |

## Requirements Review Round 1

### Assessment

**Decision: NO-GO**

The initial requirements captured explicit classification and rejection of mixed agent-runtime/inference routing, but they were not strong enough for the current execution model.

### Finding R1-A: “Mixed types only” leaves agent-runtime + agent-runtime unsafe

Two whole-agent runtimes can have different private sessions/workspaces/tool state. Parallel or weighted composition of two agent runtimes is not safer merely because their classes match.

**Remediation applied:**

- Requirement 2.4 defines one simple invariant: every composed selector must consist only of explicit `inference` backends.
- Requirements 2.5–2.8 explicitly cover weighted, parallel, thinker, and failover.
- Agent-runtime + agent-runtime compositions are tested under Requirement 10.2.

### Finding R1-B: Failover was under-specified

The first draft considered leaving ordered failover allowed because core only fails over before client-visible output.

Cursor SDK demonstrates that this is not a side-effect boundary: SDK-native tools/MCP can run before visible text.

**Remediation applied:**

- Added Requirement 3 dedicated to failover/side-effect semantics.
- Requirement 2.8 now rejects any failover chain reaching agent-runtime/unknown under `safe`.
- Future side-effect-free/idempotency relaxation is explicitly deferred.

### Finding R1-C: Per-plugin classification is wrong for Codex

The Codex connector exports both a direct inference backend and App Server in the same artifact.

**Remediation applied:**

- Requirement 1.4/1.6 require per-export granularity.
- Requirement 8.5 prohibits artifact-wide agent classification.
- Requirement 10.6 makes the dual-export case executable regression evidence.

### Finding R1-D: Missing legacy metadata could bypass the guard

Assuming absent metadata means inference would let an old third-party agent connector participate in composition.

**Remediation applied:**

- Added compatibility `unknown`.
- Direct unknown remains allowed (7.5).
- Composite unknown is denied in safe mode (2.4, 7.6).
- Unknown backend identity remains a separate existing condition (2.11, 4.5).

## Requirements Review Round 2

### Assessment

**Decision: NO-GO**

After correcting the semantic model, the requirements still allowed implementation drift across selector entry points and an unnecessary ABI change.

### Finding R2-A: Runtime-only validation would leave admin/default bypasses

An unsafe selector could enter through a persisted admin override or configured default and fail at a different stage than client input.

**Remediation applied:**

- Requirement 6 requires one semantic rule across runtime requests, generation defaults, aliases, and admin override writes.
- Persisted raw overrides are revalidated by each later generation rather than rewritten.

### Finding R2-B: Validation point was not early enough

A check after planning could consume weighted RNG/`[first]` state, consult mutable affinity/interleaved state, or authorize billing before rejecting.

**Remediation applied:**

- Requirement 5.4 fixes the ordering: compiled AST validation occurs before dynamic planner state and dispatch/billing effects.
- Requirement 10.8 requires zero-open and no-routing-state-mutation evidence.

### Finding R2-C: Runtime backend-plugin ABI change added cost without value

The host already knows factory/export metadata before activating a connector.

**Remediation applied:**

- Requirement 8.7 explicitly excludes a backend-plugin protocol change solely for this feature.
- Execution metadata remains installation/registration information.

### Finding R2-D: Public canonical leakage was not explicitly prohibited

A topology class could be mistaken for a canonical capability.

**Remediation applied:**

- Requirements 8.6 and Boundary Context explicitly keep it out of `pkg/lipapi`.
- Architecture tests in 10.10 protect provider/core isolation.

## Requirements Review Round 3

The remediated requirements were rechecked against current steering and code paths.

### Additional brownfield compatibility finding

Direct executor construction is used by `internal/testkit.NewTestExecutor`/`runtime.TestExecutor` in tests. A nil execution resolver cannot become a hidden production compatibility escape hatch. The final requirements therefore require configured backends without explicit metadata to remain conservative unknown, while test-only builders may opt their ordinary fake backends into inference explicitly. This contains test churn without weakening production policy.

### Quality checks

- **User intent:** direct agent runtimes remain routable; unsafe composition is default-denied; opt-out remains available.
- **Completeness:** weighted, parallel, thinker, nested thinker-parallel, failover, aliases, defaults, admin overrides, reload, unknown legacy metadata, errors, and tests are covered.
- **Brownfield fit:** reuses compiler, route-override preflight, plugin registration, manifest exports, factory/instance distinction, and immutable generations.
- **No scope inflation:** no new selector syntax, canonical semantics, agent protocol, workspace transaction layer, or provider exception table.
- **Safety:** pre-output is explicitly not treated as agent-side-effect-free.
- **Compatibility:** inference-only routing remains unchanged; direct old backends remain usable; unrestricted preserves legacy composition.
- **Architecture:** policy remains core-owned while concrete connectors only declare metadata.

## Requirements Quality Gate

**Decision: PASS**

The final `requirements.md` is testable, provider-neutral, consistent with Go-LIP ownership rules, and specific enough to drive brownfield design without prematurely prescribing an implementation that would violate existing seams.
