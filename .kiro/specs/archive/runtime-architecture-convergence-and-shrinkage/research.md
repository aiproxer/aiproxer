# Current-State Review, Requirements Gap Analysis, Architecture Research, and Design Validation

Generated: 2026-07-23T14:29:26+02:00

## Status

- Repository: `matdev83/go-llm-interactive-proxy`
- Reviewed ref: `main` at `efe4624909cea318c7211d5cb3734059d3210802`
- Feature: `runtime-architecture-convergence-and-shrinkage`
- Source: thermonuclear whole-branch maintainability audit requested on 2026-07-23
- Workflow completed: initialization, requirements generation, mandatory brownfield gap analysis, requirements remediation, design generation, first design validation, design correction, final design validation, and task generation
- Change scope: Kiro specification artifacts only
- Review mode: static source, architecture, contract, prior-spec, test-topology, and release-evidence review through the connected GitHub repository
- Runtime verification: no implementation or test execution is claimed by this spec-only workflow
- Implementation readiness: final design validation is GO; human approvals remain false in `spec.json`

## Artifact Convention

The workflow follows the active five-artifact Kiro pattern used by recent repository specs:

1. `spec.json`
2. `requirements.md`
3. `research.md`
4. `design.md`
5. `tasks.md`

This `research.md` records both mandatory brownfield reviews:

- requirements gap review and remediation;
- design validation, correction, and final GO/NO-GO decision.

## Reviewed Steering, Rules, Templates, Specs, and Evidence

### Kiro process and steering

- `AGENTS.md`
- `.kiro/AGENTS.md`
- `.kiro/steering/{product,structure,tech,api-standards,routing-and-orchestration,testing}.md`
- `.kiro/rules/{ears-format,gap-analysis,design-principles,design-review}.md`
- `.kiro/settings/templates/specs/{init.json,requirements.md,design.md,tasks.md}`

### Prior architecture and active specifications

- `specs/go_llm_proxy_arch_review/{architecture-review,resolution-plan,findings-register,executive-summary}.md`
- `.kiro/specs/archive/versioned-runtime-reloadable-proxy-configuration/*`
- `.kiro/specs/archive/backend-connector-plugin-architecture/*`
- `.kiro/specs/archive/generic-compatible-backend-modes/*`
- `.kiro/specs/archive/cursor-sdk-backend/*`
- archived runtime hardening, extension platform, executable generation, and dual-plane specifications

### Runtime composition and host ownership

- `internal/infra/runtimebundle/{build,built,options,bootstrap_plan,bootstrap_host,reload_host}.go`
- `internal/infra/runtimebundle/{process_services,candidate_compile,compile_generation,generation_bundle,request_plane}.go`
- `internal/infra/runtimebundle/{resource_ledger,lifecycle_adapt,candidate_owned_closer}.go`
- `internal/infra/runtimehost/{generation,manager,lifecycle_worker,coordinator,shutdown,status}.go`
- `internal/stdhttp/{server,handler,request_plane,generation_host,mount,middleware}.go`
- `internal/stdhttp/admin/configreload/*`
- `cmd/lipstd/{command,serve_rollback,management_server,reload_signal_*}.go`
- `pkg/lipruntime/{build,options,normalize,reload,reload_map}.go`

### Guardrails and behavior evidence

- `internal/archtest/{guardrails,critical_files,reload_ownership_scan}.go`
- `docs/{architecture,architecture-guardrails,runtime-flow,runtime-config-reload,release-gates}.md`
- `.kiro/specs/archive/versioned-runtime-reloadable-proxy-configuration/release-evidence.md`
- focused runtimehost/runtimebundle/stdhttp/lipruntime characterization, race, soak, rollback, and compatibility tests

## Executive Assessment

The repository has strong architectural intent and unusually strong behavior evidence. The previous architecture remediation succeeded in several important areas:

- `runtime.Executor.Execute` is now a compact orchestration shell;
- `BuildOptions` is grouped by concern rather than flat;
- `pluginreg` is narrow and standard bundle ownership is separate;
- provider-family reuse is implemented at an appropriate abstraction level;
- architecture tests prevent many dependency-direction regressions;
- runtime reload has deterministic race, soak, rollback, no-drop, security, and lifecycle evidence.

The remaining problem is not lack of architecture. It is incomplete replacement.

The versioned-runtime work introduced process services, candidate runtimes, immutable generation bundles, a generation manager, a stable dispatcher, and a reload coordinator. It also retained the historical `Built`, `Build`, `RunWithRuntime`, App-based bootstrap, broad HTTP mount signatures, duplicate public/internal reload models, and public shutdown coordination. The new generation view is converted back into `Built` before mounting HTTP routes. The architecture therefore contains both the old and new runtime models.

The preferred strategy is an incremental contraction:

1. lock existing behavior and freeze new hotspots;
2. establish one canonical generation runtime and focused HTTP inputs;
3. migrate all production consumers and delete `Built`/old serve paths;
4. converge startup on one host and one config snapshot;
5. decompose reload coordination by state-machine ownership;
6. consolidate lifecycle and public contracts;
7. remove public legacy options at the compatible major boundary;
8. ratchet budgets below the reviewed baseline.

The target is not a renamed architecture. The target is fewer concepts and at least 800 fewer non-test production Go lines in the affected surfaces.

## Current-State Architecture

### Current runtime object graph

The broad runtime dependency set currently travels through several shapes:

```text
BuildBootstrap
  -> BootstrapApp
  -> ProcessServices
  -> CandidateRuntime
  -> RequestPlane
  -> requestPlaneAsBuilt
  -> Built accepting HTTP mounts
  -> GenerationBundle publication view
```

Not every arrow is a direct ownership transfer, but the same services are repeatedly projected or copied. A new runtime capability can require changes in process construction, candidate construction, `Built`, request-plane fields/getters, HTTP rehydration, and generation publication.

### Current startup paths

`BuildBootstrap` supports inspect and serve modes. Serve behavior then branches again:

- `HandlerComposer == nil`: legacy `Build`/`Built` path;
- `HandlerComposer != nil`: process services plus generation 1.

The generation-host path still constructs `BootstrapApp` and computes the feature surface before `CompileGeneration` computes generation composition again.

Production `cmd/lipstd` and `pkg/lipruntime` both perform:

```text
BuildBootstrap
-> local failure cleanup closure
-> AttachReloadHost
-> additional caller-owned shutdown sequencing
```

### Current validation path

`check-config` invokes serve-mode bootstrap with a handler composer, publishes generation 1 through a manager, then immediately shuts down the manager and process services. A separate dry-run helper exists, but the command does not use a true unpublished validation operation.

### Current reload state

`runtimehost.Coordinator` owns:

- busy and pending signal state;
- active attempt cancellation and completion;
- shutdown;
- source and effective config;
- read/load/classify/compile/publish workflow;
- rollback and panic isolation;
- last result/success/failure;
- model generation;
- history and status;
- observer integration.

The idle path handles a narrow window with a timed polling loop when busy is set before attempt completion state is available.

### Current lifecycle ownership

Generation lifecycle behavior is distributed:

- `Generation`: state/refcount, drain notification, payload, close state;
- `Manager`: publication, retention, shutdown;
- `LifecycleWorker`: quiesce/drain/close/retry policy and mutable last status;
- `CandidateRuntime`: resource ledger plus quiesce/close guards;
- `GenerationBundle`: another quiesce/close guard around the candidate owner;
- `Coordinator`: prepare/publish/discard;
- `ReloadHost` and public `Runtime`: process shutdown order.

### Current reload contracts

Equivalent trigger/result/status concepts exist in:

- `internal/core/configreload`;
- `pkg/lipruntime`;
- `internal/stdhttp/admin/configreload`.

`pkg/lipruntime/reload_map.go` maps between almost identical internal and public values.

### Current public compatibility load

`pkg/lipruntime.Options` supports canonical descriptor-bound registrations and deprecated parallel provider/rater fields. `normalize.go` contains substantial mutual exclusion, stage-family filtering, descriptor pairing, legacy ID, and conversion logic.

### Current guardrail mismatch

Critical-file budgets protect previous hotspots, but not the new ones. The reviewed files include approximately:

- `runtimehost/coordinator.go`: 799 lines;
- `runtimehost/generation.go`: 577 lines;
- `runtimebundle/candidate_compile.go`: 442 lines;
- `runtimebundle/process_services.go`: 366 lines;
- broad `pkg/lipruntime/build.go`.

The `runtimebundle` package budget rose from the pre-reload range near 6,500 lines to 9,950 lines. The historical comments are useful, but the budget currently records accepted growth rather than forcing old-path deletion.

## Strengths That Must Be Preserved

1. **Canonical streaming middle.** No frontend/backend pairwise translation or separate non-streaming engine.
2. **Executor decomposition.** The main executor delegates to named collaborators and should not be re-generalized.
3. **Explicit composition.** No global registry, hidden `init`, reflection, or DI container.
4. **Provider-family ownership.** Shared OpenAI-compatible mechanics are centralized without erasing provider-specific behavior.
5. **Strict config and source integrity.** Startup/reload normalization and safe error categories are strong.
6. **Stable-listener immutable publication.** Requests retain their generation and publication remains bounded.
7. **Resource ledger and reverse cleanup.** Candidate rollback has explicit ownership evidence.
8. **Behavior evidence.** Race, soak, fault, no-drop, lint, fuzz, architecture, and vulnerability gates are established.
9. **Public/private separation.** Public contracts intentionally avoid secrets and deep internal types.
10. **Architecture documentation.** Package maps and ownership rules are unusually explicit.

## Requirement-to-Asset Map

| Requirement area | Current assets | Gap classification | Design consequence |
|---|---|---|---|
| Behavior preservation | reload release evidence, conformance, race/soak tests | Strong asset | Reuse as deletion safety net |
| Runtime ownership | `ProcessServices`, resource ledger, generation bundle | Partial / duplicated | Converge on one process and one generation aggregate |
| Legacy path deletion | `Built`, `Build`, `RunWithRuntime`, compatibility tests | Obsolete production architecture | Migrate consumers and delete symbols |
| HTTP composition | `ComposeRequestPlane`, mount helpers | Partial / legacy bag dependency | Introduce focused immutable mount inputs |
| Startup host | `BuildBootstrap`, `publishInitialGeneration`, `AttachReloadHost` | Two-stage / dual path | Build one owned host from one snapshot |
| Validation | strict loader, `CompileGeneration`, `DryRunCompile` | Parity asset, wrong command flow | Add explicit unpublished validation entrypoint |
| Reload coordination | `Coordinator`, observer, manager | Functional but concentrated | Split gate, runner, state, thin coordinator |
| Reload contract | internal/public/HTTP models | Duplicated | Move safe vocabulary to one public contract |
| Lifecycle | generation, manager, worker, ledger | Functional but distributed | Assign one owner per layer and delete duplicate guards |
| Public facade | `pkg/lipruntime.Runtime` | Broad internal ownership | Wrap one host interface |
| Public options | registration-only (`RequestRegistrations` et al.) | Parallel API removed (alpha) | Alpha-stage removal 2026-07-25; see `docs/legacy-options-migration.md` |
| Guardrails | tree and critical-file budgets | Protect old hotspots | Add current hotspots and downward ratchet |
| Shrinkage proof | `arch-report` | Metrics asset | Add before/after deletion and LOC acceptance |

## Mandatory Brownfield Requirements Gap Analysis

| ID | Severity | Finding | Classification | Final requirement disposition |
|---|---:|---|---|---|
| G-01 | P0-M | Runtime dependencies are represented by `ProcessServices`, `CandidateRuntime`, `Built`, `RequestPlane`, and selected `GenerationBundle` fields. | Parallel architecture | 2.1-2.8, 3.1-3.10 |
| G-02 | P0-M | `requestPlaneAsBuilt` converts the new generation path back into the old runtime shape. | Compatibility loop | 3.1-3.7, 9.1-9.7 |
| G-03 | P0-M | `Built` has different lifecycle meaning depending on origin; the rehydrated form intentionally has no closers. | Ownership ambiguity | 2.8, 3.1, 3.8 |
| G-04 | P0-M | `runtimehost.Coordinator` contains several coupled concurrency and workflow state machines. | Gravity well | 6.1-6.11 |
| G-05 | P0 | Coordinator idle uses polling to bridge a busy/attempt registration window. | Concurrency design debt | 6.5-6.7 |
| G-06 | P0 | `BuildBootstrap` selects old/new runtime architecture through nullable `HandlerComposer`. | Incomplete migration | 4.1, 4.5-4.9 |
| G-07 | P0 | Serve-mode generation bootstrap constructs an App and merges features before generation compilation repeats composition. | Duplicate work/ownership | 4.6-4.7 |
| G-08 | P0 | `cmd/lipstd` and `pkg/lipruntime` both repeat bootstrap-attach-cleanup orchestration. | Duplicate composition | 4.1, 4.5, 4.8, 10.1-10.4 |
| G-09 | P0 | Serve validates multi-user CLI posture against a separately loaded effective config. | Startup TOCTOU | 4.2-4.4 |
| G-10 | P0 | `check-config` publishes generation 1 only to shut it down. | Artificial lifecycle | 5.1-5.6 |
| G-11 | P0 | Generation resource idempotency is guarded by ledger, candidate runtime, generation bundle, and generation state. | Duplicate lifecycle truth | 8.1-8.10 |
| G-12 | P0 | Shutdown ordering is reimplemented by internal host, public runtime, and command rollback helpers. | Distributed ownership | 8.6-8.7, 10.1-10.4 |
| G-13 | P1 | Reload vocabulary/status is declared in three layers. | Mirrored contract | 7.1-7.8 |
| G-14 | P1 | HTTP mount helpers depend on broad `Built`, causing transport changes to propagate through runtime aggregates. | Boundary leak | 9.1-9.7 |
| G-15 | P1 | Public Runtime stores concrete host internals and copied capability booleans. | Facade overreach | 10.1-10.4 |
| G-16 | P1 | Deprecated public option shapes create a second authority/rater configuration language. | Permanent compatibility branch | 10.5-10.10 |
| G-17 | P1 | Critical-file budgets omit current hotspots. | Guardrail gap | 11.1-11.4 |
| G-18 | P1 | Tree budgets repeatedly rise with narrow headroom but do not require deletion after migrations. | Governance gap | 11.5-11.9 |
| G-19 | P1 | Compatibility tests can keep obsolete production paths alive after all callers migrate. | Test-induced debt | 12.3-12.7 |
| G-20 | P1 | A file-only coordinator split could satisfy superficial size goals without reducing ownership complexity. | False remediation risk | 6.1-6.4, 11.6, 12.3 |
| G-21 | P1 | A broad replacement interface could reproduce the RequestPlane getter wall. | Abstraction risk | 3.5, 9.1-9.4 |
| G-22 | P1 | Big-bang deletion could compromise complex race/lifecycle behavior despite strong tests. | Delivery risk | 12.1-12.9, 13.1-13.10 |
| G-23 | P2 | ~~Public source compatibility may prevent immediate legacy option deletion.~~ **Resolved 2026-07-25:** alpha/no-users decision by maintainer matdev83 removed the legacy Options fields without a future major-version gate. | Resolved (alpha breaking change) | 10.6-10.10 |
| G-24 | P2 | Provider and executor abstractions are already appropriately factored. | Preserve / non-gap | 1.7-1.9 |

`P0-M` denotes a maintainability blocker for further growth in the affected architecture, not a claim of an active correctness outage.

## Implementation Approach Options

### Option A: Local file splits and helper extraction

**Approach**

- Split coordinator, generation, candidate compilation, and public build files.
- Keep current types and mappings.
- Add more helper functions and file budgets.

**Advantages**

- Low immediate risk.
- Small review diffs.
- Existing callers remain untouched.

**Disadvantages**

- Preserves the same runtime dependency copies.
- Preserves dual bootstrap and `Built` rehydration.
- Moves the coordinator state machine without changing ownership.
- Produces little or no net shrinkage.
- Fails the thermonuclear objective.

**Assessment:** not sufficient.

### Option B: Big-bang rewrite of runtime host and composition

**Approach**

- Replace process/generation composition, reload coordinator, lifecycle, HTTP mounts, public facade, and options in one branch.
- Delete compatibility code immediately.

**Advantages**

- Fastest conceptual convergence if correct.
- Maximum opportunity for one coherent design.

**Disadvantages**

- Unacceptably broad concurrency and ownership risk.
- Hard to review, bisect, and compare.
- Existing release evidence would be difficult to attribute.
- Public compatibility and implementation-major timing become entangled.

**Assessment:** rejected.

### Option C: Behavior-locked contraction with deletion gates

**Approach**

- Freeze current hotspots and characterize each deletion seam.
- Introduce the canonical generation runtime and focused HTTP inputs.
- Convert production consumers, then delete `Built` and old serving.
- Build one complete host from one snapshot.
- Decompose reload coordination by ownership.
- Consolidate lifecycle and reload contracts.
- Remove public legacy options under the alpha-stage compatibility decision (no future major-version wait).
- Lower budgets after every phase.

**Advantages**

- Deletes concepts rather than moving them.
- Keeps every intermediate revision buildable.
- Uses the strongest existing test assets.
- Makes shrinkage measurable.
- Applies an explicit alpha breaking change for public Options rather than indefinite quarantine.

**Disadvantages**

- Requires disciplined sequencing across several PRs.
- Temporary directional adapters may exist briefly.
- Architecture tests must be updated repeatedly.

**Assessment:** preferred and selected for design.

## Effort and Risk

- **Effort:** XL. The work crosses composition, HTTP mounting, reload concurrency, lifecycle, public facade, and compatibility surfaces.
- **Risk:** High initially, reducible to Medium through the phased TDD plan. The main risks are concurrency/lifecycle regressions and accidental public breakage; the existing test corpus substantially lowers behavioral uncertainty.
- **Expected delivery shape:** multiple small-to-medium implementation PRs. No phase contains more than five execution tasks.

## Requirements Gap Review and Remediation

The first requirements draft captured the ten audit findings but was not implementation-safe. The mandatory brownfield review identified the following requirement defects.

### RGR-01: Deletion outcomes were described but not enforced

**Initial gap:** requirements asked for cleaner ownership but did not explicitly require deletion of `Built`, `Build`, `RunWithRuntime`, `requestPlaneAsBuilt`, and old bootstrap products.

**Remediation:** added Requirement 3 with named deletion criteria and architecture reintroduction gates.

### RGR-02: Behavior preservation was implicit

**Initial gap:** the draft focused on internal structure and could permit accidental changes to routing, streaming, reload, auth, provider, or management behavior.

**Remediation:** added Requirement 1 and the full verification Requirement 13.

### RGR-03: Startup snapshot coherence was incomplete

**Initial gap:** the draft required one host but did not state that CLI access-mode validation, generation 1, process services, and reload active state must use the exact same effective snapshot.

**Remediation:** added 4.2-4.4 and a dedicated startup TOCTOU regression expectation.

### RGR-04: Coordinator decomposition could be satisfied by file movement

**Initial gap:** the draft asked for a smaller coordinator without naming authoritative owners.

**Remediation:** added 6.1-6.4, 6.11, 11.6, and 12.3. The gate, runner, and state owner must be independently testable.

### RGR-05: Idle polling and attempt registration race were not explicit

**Initial gap:** the current polling window could survive a generic coordinator refactor.

**Remediation:** added 6.5-6.7.

### RGR-06: Lifecycle idempotency remained ambiguous

**Initial gap:** the draft did not identify the resource ledger/generation runtime as the sole quiesce/close owner or forbid wrapper `sync.Once` layers.

**Remediation:** added 8.1-8.10.

### RGR-07: Public legacy API removal ignored compatibility timing

**Initial gap:** immediate removal might be source-breaking, while indefinite deprecation would not fully address the finding.

**Remediation:** added 10.5-10.10. Later amended (2026-07-25, maintainer matdev83): because the project is alpha with no users, removal proceeds without a future major-version gate; legacy fields and adapter are deleted in-tree.

### RGR-08: Shrinkage had no objective completion measure

**Initial gap:** “simpler” could be claimed after adding more abstractions.

**Remediation:** added 11.3-11.9, including file targets, deleted-symbol gates, and at least 800 net non-test production lines removed.

### RGR-09: `check-config` was not separately specified

**Initial gap:** host convergence alone could leave fake publication in validation.

**Remediation:** added Requirement 5.

### RGR-10: HTTP design could recreate a getter wall

**Initial gap:** replacing `RequestPlane` with a broad interface would satisfy type deletion but not reduce propagation.

**Remediation:** added 3.5 and Requirement 9.

## Final Requirements Review

The remediated requirements:

- use numeric IDs and EARS-compatible acceptance criteria;
- cover all audit findings F-01 through F-10;
- preserve behavior and safety as first-class requirements;
- define exact old-path deletion outcomes;
- separate alpha-stage Options removal from any future major-version myth (resolved 2026-07-25: remove now);
- include objective shrinkage and file-size completion criteria;
- require TDD sequencing and full release evidence.

**Requirements gap decision:** PASS. No unresolved P0/P1 requirement gap remains.

## Architecture Research and Key Decisions

### Decision 1: Canonical ownership aggregates, not a universal container

The target uses:

- one process runtime aggregate for process-owned services;
- one generation runtime aggregate for generation-owned services and explicitly allowed non-owning process references;
- request/async leases and pins for dynamic ownership.

The aggregates use cohesive substructures. They are not a generic service locator and are not exposed as a broad public interface.

### Decision 2: Generation runtime is the publication unit and lifecycle owner

The generation runtime directly implements the narrow capabilities runtimehost needs:

- HTTP handler;
- executor view;
- model-view binding;
- terminal-provider view;
- readiness;
- backend factory-kind count;
- quiesce/close ownership.

There is no candidate closer bag plus generation bundle wrapper plus request-plane getter object after migration.

### Decision 3: HTTP receives a transport projection, not an owner

The generation compiler constructs a grouped immutable standard-HTTP input and calls the composer. The input has no closers or lifecycle controls. Individual mount helpers receive smaller groups.

This is an intentional projection, not another runtime aggregate: it exists only during handler construction and is not stored as a second owner.

### Decision 4: Host construction is one transaction

`BuildHost` owns:

```text
one effective source snapshot
-> startup gate validation
-> process runtime
-> generation 1 compile
-> manager publication
-> reload state/coordinator
-> stable executor/dispatcher
-> complete host
```

Failure unwinds internally. `cmd/lipstd` and `pkg/lipruntime` do not attach a host afterward.

### Decision 5: Validation compiles but never publishes

`ValidateDistribution` uses the same process/generation compiler and handler composer, then rolls back the unpublished runtime. It does not construct a manager or generation ID.

### Decision 6: Reload coordinator owns orchestration, not all state

- `AttemptGate`: concurrency and shutdown admission.
- `AttemptRunner`: one reload transaction.
- `ReloadState`: active source/effective and safe status/history.
- `Coordinator`: thin composition.

No generic workflow engine is introduced.

### Decision 7: Reload contract moves to an independent public package

A dependency-neutral package under `pkg/lipsdk` owns the safe reload vocabulary. Internal runtimehost and HTTP consume it. `pkg/lipruntime` preserves existing names through aliases/delegation where appropriate.

### Decision 8: Manager owns retirement, generation runtime owns resources

A stateless or manager-owned retirement policy may remain. It does not own a second lifecycle truth. Quiesce/close idempotency lives in the resource owner/ledger once.

### Decision 9: Public Runtime wraps one host interface

The public facade delegates and does not retain manager/process/coordinator internals. Capability reporting is a derived host snapshot.

### Decision 10: Compatibility is directional and temporary

Consumers migrate toward the canonical model. Temporary adapters may translate old callers to new contracts only. No new code translates the canonical generation runtime back to `Built`.

## Initial Design Validation

### Design Review Summary

The first design was directionally correct and covered the audit findings, but it was not ready for implementation. Three critical boundary ambiguities could have recreated the same complexity under new names.

### Critical Issues

🔴 **Critical Issue 1: Canonical generation runtime was still exposed as a broad getter interface**
**Concern:** The first design let stdhttp and runtimehost consume one interface with most runtime dependencies.
**Impact:** This would reproduce the `RequestPlane` getter wall and retain synchronized propagation.
**Suggestion:** Let the concrete generation runtime implement only narrow runtimehost capabilities; pass a grouped value projection only to handler composition.
**Traceability:** 2.6-2.8, 3.5-3.7, 9.1-9.4
**Evidence:** Initial Components section, `GenerationView` contract.

🔴 **Critical Issue 2: Retirement and process shutdown ownership remained split**
**Concern:** Manager, retirement worker, Host, and public Runtime could each coordinate parts of shutdown.
**Impact:** Duplicate lifecycle truth and repeated idempotency would remain.
**Suggestion:** Make manager own retirement scheduling, generation runtime own resource phases, Host own process shutdown, and public Runtime delegate.
**Traceability:** 8.1-8.10, 10.1-10.4
**Evidence:** Initial Lifecycle and Public Facade sections.

🔴 **Critical Issue 3: Migration order could delete compatibility before focused HTTP consumers existed**
**Concern:** The first phase plan removed `Built` while mount helpers still accepted it.
**Impact:** Implementation would either create another temporary broad adapter or produce an unreviewable big-bang patch.
**Suggestion:** First introduce focused mount inputs and convert all consumers; then add symbol-forbidding gates and delete the old aggregate.
**Traceability:** 3.1-3.10, 9.1-9.7, 12.1-12.7
**Evidence:** Initial Migration Strategy.

### Design Strengths

- The design explicitly targeted concept deletion and measurable shrinkage.
- It preserved the current generation publication and no-drop behavior rather than replacing proven concurrency primitives without cause.

### Initial Assessment

**Decision:** NO-GO.

The ownership model and migration sequence required correction before task generation.

## Design Corrections

1. Replaced broad `GenerationView` with:
   - concrete internal `GenerationRuntime`;
   - narrow runtimehost capability interfaces already aligned with existing patterns;
   - one ephemeral grouped `StandardHTTPInput` value used only during handler composition.

2. Assigned definitive ownership:
   - generation runtime/resource ledger: rollback, quiesce, close;
   - generation: identity, refs, lifecycle, drained signal;
   - manager: publication, retention, retirement scheduling, all-generation shutdown;
   - host: reload stop, generation drain, process close, tracing close;
   - public Runtime: delegation only.

3. Reordered migration:
   - characterize and add budgets;
   - convert HTTP mount signatures;
   - create canonical generation runtime;
   - migrate production consumers;
   - delete `Built`/old path;
   - converge host/bootstrap;
   - decompose coordinator;
   - consolidate lifecycle/contracts/public facade.

4. Added explicit compile-time/AST gates for deleted symbols and exact one-snapshot startup.

5. Recorded the alpha-stage maintainer decision to remove legacy Options fields without waiting for a future major version (2026-07-25).

## Final Design Validation

### Design Review Summary

The corrected design aligns with repository boundaries, removes the specific parallel architectures identified by the audit, and preserves the proven runtime-reload behavior. It uses explicit Go construction, narrow capability interfaces, TDD sequencing, and objective shrinkage gates.

### Critical Issues

No unresolved critical issue remains.

### Design Strengths

1. **Deletion-oriented architecture.** Each phase names obsolete types, adapters, or orchestration paths that disappear.
2. **Concurrency ownership clarity.** Gate, runner, state, manager, resource owner, and host have non-overlapping state-machine responsibilities.

### Final Assessment

**Decision:** GO.

The design is ready for task generation, subject to normal human approval of the Kiro requirements and design.

## Requirement and Finding Coverage

| Audit finding | Requirements | Primary design owner |
|---|---|---|
| F-01 parallel runtime dependency shapes | 2, 3, 9 | ProcessRuntime, GenerationRuntime, StandardHTTPInput |
| F-02 coordinator gravity well | 6, 11, 12 | AttemptGate, AttemptRunner, ReloadState, Coordinator |
| F-03 dual bootstrap/two-stage host | 4, 5 | HostBuilder, ValidateDistribution |
| F-04 distributed lifecycle | 8 | Generation, Manager, GenerationRuntime, Host |
| F-05 mirrored reload contract | 7 | `pkg/lipsdk/configreload` contract |
| F-06 double startup config read | 4 | HostBuilder startup transaction |
| F-07 stale guardrails | 11, 12 | architecture ratchet suite |
| F-08 deprecated public options | 10, 12 | public facade and legacy adapter |
| F-09 check-config publication | 5 | ValidateDistribution |
| F-10 public Runtime overreach | 10 | thin Runtime facade and Host API |

## Research Conclusions Carried into Design

- Preserve generation manager linearizability and stable dispatcher behavior.
- Do not replace the resource ledger with ad hoc closer slices.
- Do not generalize provider adapters or re-expand Executor.
- Do not use a DI framework to hide duplicate runtime shapes.
- Treat focused HTTP construction inputs as projections, never owners.
- Use existing release evidence to permit deletion, not to justify permanent compatibility.
- Make architecture budgets a decreasing migration register.
- Require a major-boundary plan for source-breaking public cleanup.
