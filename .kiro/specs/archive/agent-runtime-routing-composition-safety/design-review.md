# Design Validation Review

## Review Method

The design was validated as a brownfield routing/plugin-contract change against:

- root `AGENTS.md` and `.kiro/AGENTS.md`;
- routing, structure, and testing steering;
- repository `main` at `d0224a9de48c2e6e5a57b559f4f755ee4029c95d`;
- issue #323 and the architectural discussion recorded there;
- `internal/core/routing` AST/compiler/planner;
- `internal/core/runtime` route-plan ordering;
- `internal/infra/runtimebundle` factory/instance assembly;
- `internal/pluginreg` registration/security/lifecycle metadata;
- executable backend manifest v1 types and strict parser;
- Codex dual-export manifest and direct Codex backend documentation;
- Cursor SDK documented SDK-native tool/MCP semantics;
- route-override generation validation;
- every acceptance criterion in `requirements.md`;
- every gap in `gap-analysis.md`.

Any unresolved correctness, dependency-direction, compatibility, side-effect, or entry-point issue returned NO-GO and required remediation in the final requirements/design.

## Round 1: Semantic and Metadata Model

### Assessment

**Decision: NO-GO**

### Critical Issue 1: Plugin-artifact-level classification cannot represent Codex

**Concern:** The Codex connector artifact exports both `openai-codex` (direct inference) and `openai-codex-app-server` (agent runtime). One class per plugin would misclassify one export.

**Impact:** Direct Codex inference could be unnecessarily blocked from composition, or App Server could be incorrectly treated as inference.

**Resolution:** Execution class is per exported factory kind. Registration metadata is factory-scoped and runtimebundle projects it to configured instance IDs.

**Traceability:** Requirements 1.4, 1.6, 4; Design 4–7; D3.

### Critical Issue 2: “Reject only mixed classes” is not a sufficient invariant

**Concern:** `agentA ^ agentB` or `agentA ! agentB` still switches/races independent orchestration state even though both leaves have the same class.

**Impact:** Hidden workspace/session/tool side effects remain possible.

**Resolution:** Safe policy is structural: direct-any-class is allowed; every composed selector requires all reachable known classes to be explicit `inference`.

**Traceability:** Requirement 2; Design 8; D5.

### Critical Issue 3: Pre-output failover is not side-effect-free for agent runtimes

**Concern:** Existing B2BUA rules use downstream output commitment, but Cursor SDK can execute SDK-native tools/MCP before visible text.

**Impact:** Agent A can mutate external state, fail pre-output, and be transparently followed by B, duplicating/conflicting work.

**Resolution:** Include ordered failover in safe-mode composition denial. Keep existing output commitment unchanged for inference and as a separate invariant.

**Traceability:** Requirement 3; Design 13; D6.

### Critical Issue 4: Inferring class from connector/security/process metadata has false positives

**Concern:** Local/discovered process-backed inference runtimes are ordinary inference, while tool capability does not distinguish agent harnesses from model APIs.

**Impact:** Broad heuristics would break legitimate routing and create provider-specific technical debt.

**Resolution:** Explicit metadata only; separate execution profile from `BackendSecurityProfile`, process sharing, capability summaries, and registration source.

**Traceability:** Requirements 1.5, 8.1; Design 4, 21; D2.

## Round 2: Brownfield Integration and Compatibility

### Assessment

**Decision: NO-GO**

### Critical Issue 1: Legacy manifests have no execution class

**Concern:** Making missing metadata equivalent to inference would silently grant composition privileges to old third-party agent connectors. Making it equivalent to agent runtime would unnecessarily break all direct legacy usage.

**Resolution:** Introduce effective `unknown`: direct allowed, safe composition denied, explicit metadata or operator `unrestricted` required for composition.

**Traceability:** Requirements 1.2–1.3, 7.5–7.6; Design 14, 20; D4.

### Critical Issue 2: Runtime-only validation leaves selector-authority drift

**Concern:** Admin overrides and configured defaults already have their own compile/preflight paths. If only `buildRoutePlan` checks composition, invalid admin state can be persisted and startup/reload errors appear late.

**Resolution:** Reuse the generation-bound semantic validator across request execution, default route validation, and route-override write preflight. Aliases are validated after expansion at use time.

**Traceability:** Requirement 6; Design 9, 11, 12; D9.

### Critical Issue 3: Validation after dynamic planning is too late

**Concern:** Weighted RNG, `[first]`, affinity, or interleaved state could be touched before rejection.

**Impact:** A failed request could still mutate routing state or produce misleading diagnostics.

**Resolution:** Validate immediately after `CompileSelector` and before native/dynamic route-plan work. Tests require zero backend open/billing and unchanged dynamic routing state.

**Traceability:** Requirement 5; Design 10, 19.4; D8.

### Critical Issue 4: A backend-plugin runtime ABI field is unnecessary

**Concern:** Adding an execution-class field to gRPC negotiation would expand versioning/compatibility surface and require runtime communication even though installation metadata is already trusted and available before activation.

**Resolution:** Class lives in registration/manifest metadata. No execute/count/finalize protocol feature/minor is added solely for this feature.

**Traceability:** Requirements 8.2, 8.7; Design 5, 21; D10.

### Critical Issue 5: Direct test/alternate executor construction could become a fail-open hole

**Concern:** Internal test helpers can construct an executor with a backend map but no runtimebundle-generated execution view. Treating a nil view as unrestricted/inference would make standard generation safe while alternate construction silently bypasses the invariant; treating every test fake as unknown without helper support would cause noisy unrelated test churn.

**Resolution:** Missing metadata remains conservative unknown in the executor contract. Test-only builders explicitly classify ordinary fake backends as inference and focused tests can override them. There is no production nil-means-unrestricted fallback.

**Traceability:** Requirement 4.9, 10.14; Design 7.4; D14.

### Critical Issue 6: Canonical capability leakage would confuse topology with model semantics

**Concern:** Putting `agent_runtime` in `pkg/lipapi` capabilities would make frontends/capability negotiation participate in a backend topology decision.

**Resolution:** Keep canonical request/event/capability contracts unchanged. Public SDK growth is limited to backend registration metadata.

**Traceability:** Requirements 8.6, 8.8; Design 4, 21; D10.

## Round 3: Final Architecture Review

### Requirements Traceability

**Decision: PASS**

Every final requirement has a concrete design owner:

- classification -> SDK/manifest/registration;
- safe policy -> pure routing validator;
- failover side-effect rationale -> pre-dispatch legality;
- factory-to-instance mapping -> runtimebundle generation assembly;
- early validation -> request route-plan ordering;
- defaults/aliases/admin -> shared semantic preflight;
- legacy compatibility -> `unknown` + `unrestricted`;
- boundaries -> no canonical/ABI/provider-core leak;
- errors -> typed routing failure/frontends;
- proof -> TDD, zero-dispatch, archtests.

### SOLID Review

**Single Responsibility — PASS**

- security profile remains credential/trust metadata;
- execution profile describes execution topology only;
- manifest declares facts;
- routing validator owns selector legality;
- runtimebundle only maps factory metadata into configured generation identities;
- frontends only map errors.

**Open/Closed — PASS**

A future connector declares its execution class at its own export. Core policy does not change when new provider names appear.

**Liskov Substitution — PASS**

Only backends explicitly declaring ordinary inference are substitutable inside existing composition operators under safe mode. Unknown/agent runtimes are not falsely promised as inference substitutes.

**Interface Segregation — PASS**

The consumed resolver is one narrow lookup; no generic plugin metadata/service bag is passed into core.

**Dependency Inversion — PASS**

Core depends on SDK/core-facing metadata abstractions, never concrete connectors. Composition adapts plugin metadata to the core resolver.

### Hexagonal Review

**Decision: PASS**

- connector/provider facts stay at driven-adapter edges;
- policy stays at the application/core boundary;
- runtimebundle remains composition root;
- frontend/admin adapters do not own policy;
- no provider SDK leaks inward;
- no architecture-only package churn is required.

### Streaming and B2BUA Review

**Decision: PASS**

The design does not alter streaming, terminal ownership, or post-output commitment. It adds an earlier selector-legality gate. Inference failover remains governed by the existing pre-output rule; agent-runtime composition is rejected before that rule becomes relevant.

### Security Review

**Decision: PASS**

- client cannot bypass safe policy;
- missing metadata is conservative in composition;
- metadata comes from registration/trusted manifests;
- no raw selector/private agent state is required;
- no dynamic code or scripting added;
- no false promise that output commitment proves no side effect.

### Brownfield Compatibility Review

**Decision: PASS with intentional behavior change**

Preserved:

- direct existing backend selectors;
- all inference-only composition semantics;
- old manifests parsing without the field;
- route-override raw-selector persistence;
- selector syntax;
- backend-plugin runtime ABI;
- canonical `lipapi`.

Intentionally changed by default:

- composite routing containing agent-runtime or unclassified known backends now fails in `safe`.

Escape hatch:

- operator `unrestricted` restores legacy composition behavior.

### Manifest Compatibility Review

**Decision: PASS**

Current parsing is closed. The implementation must update `wireExport`, SDK manifest `Export`, validation, tests, and official manifest templates together. Omitted `execution_class` remains legal. No v2 is necessary because this is additive installation metadata and does not alter connector execution wire frames.

The release note must still call out that a newly generated manifest containing the field requires a host version whose strict manifest parser knows the field; this is normal for closed-manifest schema evolution and is preferable to a runtime ABI change.

### Error/Observability Review

**Decision: PASS**

The proposed typed routing error contains only bounded policy facts and does not require the raw selector. It maps as invalid routing rather than backend health/transport failure.

### Testability Review

**Decision: PASS**

The policy is pure and table-testable. Existing factory/instance separation enables deterministic composition tests. Runtime side-effect ordering can be proved with counters/fakes/barriers. Reload behavior can be tested without real network providers.

## Final Design Assessment

**Decision: GO FOR TASK GENERATION**

The final design is narrower and safer than the original issue wording while preserving the intended UX: direct agent backends work, ordinary inference composition works, accidental orchestration-layer composition is blocked by default, and advanced operators can explicitly retain legacy behavior.

It does not require a selector-language redesign, canonical-model change, backend-plugin runtime ABI change, provider-name matrix, or workspace transaction system.

## Final Spec Review and Polish

This section was executed after `tasks.md` generation.

### Cross-artifact checks

- `requirements.md` contains ten numbered requirement groups and all are represented in the design traceability table.
- Every task references requirements and design decisions.
- Tasks begin with RED contracts and defer production changes until their contracts exist.
- Factory-kind vs configured-instance mapping is represented in requirements, design, and tasks.
- Codex dual-export classification is a mandatory regression, not merely explanatory text.
- Failover is consistently denied for agent-runtime/unknown under safe mode across requirements, design, research, and tasks.
- `unknown` consistently means configured-but-unclassified, distinct from absent backend.
- Admin override persistence is never silently mutated on reload.
- `unrestricted` remains operator-only and retains existing output-commitment restrictions.
- No artifact requires changing selector grammar, `pkg/lipapi`, or backend-plugin runtime gRPC solely for this feature.
- No task introduces a core provider-name list.
- Direct/internal executor construction cannot silently interpret missing class metadata as inference/unrestricted; test helpers opt fake inference backends in explicitly.
- The spec directory is implementation-free and contains only SDD/research/review artifacts.

### Scope polish applied

The final pass removed or explicitly deferred:

- per-backend `allow_failover`/`allow_parallel` flags;
- a speculative “side-effect-free agent” capability;
- artifact-level connector classification;
- automatic override cleanup/migration;
- runtime self-classification by connectors;
- any client-controlled bypass.

### Approval state

The spec is generated but not maintainer-approved:

- requirements approved: `false`;
- design approved: `false`;
- tasks approved: `false`;
- ready for implementation: `false`.

## Final Spec Decision

**PASS — ready for maintainer review as a spec-only change.**

Implementation begins only after the project’s normal Kiro approval gate is explicitly advanced. The first implementation work must follow the RED-first tasks in `tasks.md`.
