# Research and Brownfield Gap Analysis

## Summary

- **Feature**: coding-agent-session-classification
- **Source tracker**: GitHub issue #645, feat(session-classification): Detect and persist coding-agent session kind
- **Repository baseline**: main at c7fa416950ef342d417b34db52107cc2fff15396
- **Discovery type**: full brownfield analysis
- **Complexity**: L
- **Risk**: Medium-High
- **Selected direction**: add one feature-owned session-classification implementation behind a small provider-neutral SDK contract and one exclusive metadata-only extension plane. Execute it after authoritative session/A-leg binding and secret guard, project its result into SessionView before later request stages, preserve wire-path eligibility with bounded proof facts, and keep optional Jev integration behind a fail-open remote-decision adapter.

The issue is implementable without moving coding-client policy into the kernel. The main architectural work is not the heuristic itself; it is establishing a safe same-turn classification seam that works identically on canonical and large-body wire execution, persists monotonic state under proxy-owned authority, and remains cheap at high concurrency.

## Source Set

### Project sources

Primary repository sources reviewed:

- issue #645
- .kiro/steering/product.md
- .kiro/steering/tech.md
- .kiro/steering/structure.md
- .kiro/steering/testing.md
- .kiro/steering/api-standards.md
- .kiro/steering/routing-and-orchestration.md
- .kiro/rules/ears-format.md
- .kiro/rules/gap-analysis.md
- .kiro/rules/design-principles.md
- .kiro/rules/design-review.md
- pkg/lipapi/invocation.go
- pkg/lipapi/tool_classification.go
- pkg/lipsdk/session/view.go
- pkg/lipsdk/session/opener.go
- pkg/lipsdk/state/store.go
- pkg/lipsdk/workspace/view.go
- pkg/lipsdk/feature/stages.go
- pkg/lipsdk/feature/plane_manifest.go
- internal/core/state/partition.go
- internal/core/runtime/executor_prepare_secure.go
- internal/core/runtime/executor_execute_large_body.go
- internal/core/execctx/submit_views.go
- internal/core/b2bua/store.go
- internal/core/securesession/domain/types.go
- internal/core/largebody/largebody.go
- internal/core/largebody/wire_runtime_facts.go
- internal/core/largebody/eligibility.go
- internal/plugins/frontends/frontendpipe/profile.go
- internal/plugins/frontends/identitywire/identitywire.go
- internal/plugins/features/codexclientcompat/
- connectors/codex/internal/codex/continuation.go
- internal/standardplugins/featurehost/
- internal/plugins/features/README.md
- .kiro/specs/archive/tool-call-classification/
- .kiro/specs/archive/compaction-event-detection/
- .kiro/specs/archive/large-payload-streaming-fast-path/
- .kiro/specs/archive/core-feature-ownership-full-closure/
- .kiro/specs/agent-loop-explicit-completion-protocol/

### External/current sources

- TypeSafe AI, Introducing System One Models and Jev:
  https://typesafe.ai/blog/introducing-system-one-models-and-jev
- OpenAI Codex current repository, release workflow showing codex_cli_rs User-Agent:
  https://github.com/openai/codex/blob/main/.github/workflows/rust-release-prepare.yml
- Roo Code current OpenAI native provider showing roo-code User-Agent:
  https://github.com/RooCodeInc/Roo-Code/blob/main/src/api/providers/openai-native.ts
- Roo Code current Codex provider showing the same roo-code User-Agent:
  https://github.com/RooCodeInc/Roo-Code/blob/main/src/api/providers/openai-codex.ts
- Cline discussion documenting inconsistent/SDK-like User-Agent behavior:
  https://github.com/cline/cline/discussions/8377

External findings are treated as implementation evidence, not architectural authority. In particular, TypeSafe launch latency/pricing/intelligence claims are vendor claims and Cline User-Agent information comes from a project discussion rather than a frozen protocol contract.

## Brownfield Current-State Findings

### Canonical client User-Agent already exists

pkg/lipapi.Invocation already has ClientUserAgent. Frontends capture only the inbound User-Agent and validate it through the existing identity acceptance rules. This value is explicitly protocol-neutral canonical invocation metadata and is excluded from provider JSON.

**Implication:** session classification must consume this canonical value on the canonical path. Generic core must not reopen raw HTTP headers or introduce a second User-Agent validation policy.

### User-Agent quality differs materially by coding harness

Current upstream evidence is not uniform:

- Codex has a stable codex_cli_rs family User-Agent in its current release tooling.
- Roo Code currently emits roo-code/version style User-Agent values for relevant OpenAI/Codex traffic.
- Existing AIProxer codexclientcompat code already recognizes OpenCode, Pi, Factory Droid, and Hermes from bounded client/agent strings and sometimes from distinctive prompt signatures.
- Cline currently cannot be assumed to provide a stable Cline-specific User-Agent across provider paths; project discussion reports underlying SDK-like values such as Anthropic/JS and OpenAI/JS.

**Requirement repair:** the original concept of User-Agent equals coding session is too broad. A known high-confidence identity rule may be decisive, but generic/ambiguous SDK User-Agent values must remain unknown unless corroborated by other evidence.

### Existing client-family matching is already duplicated

internal/plugins/features/codexclientcompat reads candidate identity strings from canonical extensions such as agent, user_agent, openai_codex.agent and a compatibility headers extension. It then applies OpenCode/Pi/Droid/Hermes matching.

connectors/codex/internal/codex/continuation.go independently normalizes a smaller client-family set for continuation partitioning.

**Gap:** adding another standalone matcher table for #645 would grow the duplication.

**Design direction:** add one small pure root-module matcher/fact catalog for stable client-family string recognition and reuse it from the new classifier and codexclientcompat. Do not force the separately versioned connector module to import new root-internal implementation if that would worsen connector isolation; existing connector matching may remain independently adapter-owned.

### Coding-tool taxonomy is already canonical

pkg/lipapi.ClassifyToolName already maps stable cross-harness aliases to:

- file_read
- file_search
- os_command
- file_edit
- file_remove
- web_access
- unknown

The function intentionally uses exact, case-folded names and does not inspect arguments, schemas, descriptions, shell text, or provider identity.

**Implication:** #645 should derive a bounded category bitset/count summary from this existing classifier. It should not create a competing coding-tool registry.

### Workspace already exposes cheap project evidence

pkg/lipsdk/workspace.WorkspaceView exposes ID, ProjectRoot, DirtyTree, Markers, and Labels. The classifier needs only a bounded recognized-marker test; it does not need ProjectRoot text or a filesystem scan in the classification stage.

The current standard tree does not guarantee that every client/session has a production workspace resolver that emits language/build markers. Requiring a marker for every non-UA promotion would therefore make the local heuristic ineffective for some real coding harnesses.

**Requirement repair:** the original filename-extension idea is demoted from a suggested primary heuristic. V1 uses stronger cheap structured evidence: known client identity; a distinctive three-way coding tool cluster containing read/search + edit/remove + OS command; or a weaker read/search + mutation cluster corroborated by a project marker. Arbitrary transcript filename scanning is not required and would add false positives, payload-dependent work, and large-body complexity.

### Session opener is not the right execution seam

session.Opener runs during session_open before BeginTurn. Its OpenInput contains TraceID, Principal, and a pre-authority SessionView. It does not receive:

- authoritative SessionID/A-leg;
- resolved WorkspaceView;
- ClientUserAgent;
- tool catalog;
- canonical request metadata needed for the proposed heuristic.

It only returns session label upserts.

**Gap:** extending session_open to accept a full Call would violate its current metadata-only responsibility and would still occur before authoritative session binding.

**Design repair:** add a dedicated metadata-only classification stage after BeginTurn/A-leg resolution and secret guard, before submit_request.

### Same-turn propagation needs a stage before existing request consumers

Current secure execution order on main is materially:

~~~text
session open
workspace resolve
BeginTurn
Fetch A-leg
secret guard
frontend ingress checkpoint
request authority
submit hooks
CTP traffic
conversation projection
tool catalog
request transforms
pre-request
route hint
candidate execution
~~~

A classifier invoked as a normal request transform would be too late for tool-catalog consumers. Running it before BeginTurn would lack proxy-owned session authority.

**Selected placement:** after secret guard and before the frontend-ingress/submit sequence. The stage returns a derived SessionView update only and does not mutate the canonical Call.

This gives same-turn visibility to submit, tool catalog, request shaping, pre-request, route-hint, policy/evidence views, and future consumers.

### The feature-plane catalog is closed

pkg/lipsdk/feature/plane_manifest.go is the executable source of truth for standard extension planes. Adding a plane is a platform change, not an ad-hoc callback:

- declare the plane;
- add a legal stage;
- regenerate generated plane bindings;
- update diagnostics and architecture tables;
- update the large-body plane census.

Current large-body WireEligibilityPlaneCount is 26. A new production plane must extend the fixed census or the architecture checks fail closed.

**Implication:** the spec deliberately pays this platform cost because same-turn classification is a reusable cross-feature substrate. Hiding the behavior in Executor fields or a feature-specific core callback would violate the closed-plane architecture.

### Metadata-only planes are compatible with the wire lane

The large-body architecture distinguishes CanonicalRequired, MetadataOnly, ResponseOnly, and WireContract access. MetadataOnly exists specifically for bounded session/workspace/integer-style facts. Session openers are already MetadataOnly.

The new classifier can be MetadataOnly only if the contract is kept strict:

- accepted bounded User-Agent, not raw header map;
- compact tool-category summary, not full ToolDef list;
- operation/delivery scalars where required;
- SessionView authority;
- WorkspaceView marker summary or bounded marker reading;
- no prompt/messages/tool arguments.

**Design validation consequence:** any later attempt to add arbitrary transcript scanning to this plane must fail the request-access/large-body architecture guards instead of silently turning a wire-safe plane into a content dependency.

### Large-body proof currently lacks classification evidence

Certified frontend profiles already receive raw request headers locally during proof compilation and parse/validate tool structures without creating a shadow lipapi.Call. The provider-neutral proof/wire facts currently carry session, protocol, identity, routing, source and related bounded facts, but not client User-Agent or tool-category summary.

**Gap:** if the new plane were marked MetadataOnly without extending proof facts, canonical and wire execution could classify differently.

**Design repair:** add one bounded classification-evidence value to the large-body proof/session facts. Frontend profiles use the existing User-Agent acceptance helper and the same tool-category summarizer used by canonical execution. Differential tests pin equality.

### ScopeSession is unsafe as the only state key

pkg/lipsdk/state.ScopeSession partitions using SessionView.PartitionKey. That function prefers authoritative SessionID but falls back to ClientSessionHint.

ClientSessionHint is explicitly untrusted. Two callers can choose the same value.

**Requirement repair:** classification state is keyed by authoritative SessionID when available and otherwise proxy-owned A-leg. ClientSessionHint alone is never a state authority. Generic ScopeSession cannot be used blindly for anonymous/non-secure classification.

### B2BUA and secure-session records should not become a feature-state bag

ALegRecord is intentionally small. Secure-session PolicyMetadata is policy/recording metadata. Neither exposes an arbitrary classification map.

Steering requires optional UX/policy to remain feature-owned.

**Selected approach:** a feature-owned classification store keyed by typed proxy authority. It can use process memory and, when the standard host has Bun persistence, a small dedicated table. The public SessionView carries only the current immutable classification projection.

### Durable resume requires more than a SessionView label

A process-local label or ExtensionState value cannot by itself satisfy the word permanent for durably resumable sessions.

**Requirement repair:** if a secure session can survive process restart, positive classification must be durably restorable before a resumed turn reaches consumers. Non-durable deployments retain in-process/A-leg guarantees but do not claim restart durability.

### Standard featurehost is the correct concrete owner

internal/standardplugins/featurehost is the only standard-distribution layer allowed to know the concrete standard feature set. It already owns process resources such as:

- conversation-view state;
- compaction state/coordinator;
- keep-warm stores/registry;
- terminal policy state;
- feature-specific host bindings and generation composition.

ProcessInput already carries the borrowed Bun DB and generic extension state. Generation compilation can bind concrete standard-feature resources into ordinary typed planes without generic runtime importing feature code.

**Implication:** feature policy/config lives under internal/plugins/features/sessionclassification. A featurehost child can own the memory/Bun state adapter, cache/coordinator, and Jev HTTP adapter construction. Generic runtime sees only the SDK classifier interface.

### Jev is a good optional shape, not a safe architectural dependency

TypeSafe announced Jev on September 14, 2026 as early access. Public launch material describes:

- structured state/questions input;
- typed probabilistic decisions;
- probabilities/confidence;
- claimed 70 ms to 500 ms service response range;
- claimed $0.042 per million input tokens and no output-token charge.

The same post explicitly describes Jev as early-stage/early-access and the public blog is not a complete frozen API specification.

**Requirement repair:** no exact TypeSafe wire DTO or endpoint is frozen into AIProxer public contracts. The design freezes a provider-neutral RemoteDecider port and a bounded evidence schema. The concrete adapter must use current early-access API documentation at implementation time. Remote failures never fail the user request.

## Requirement-to-Asset Gap Map

| Requirement | Existing assets | Gap / constraint | Disposition |
| --- | --- | --- | --- |
| 1 Conservative contract | SessionView, typed SDK views | No classification value today | Add bounded typed classification projection |
| 2 Authority/lifetime | secure SessionID, A-leg, Bun access | ScopeSession can fall back to client hint; no feature store | Add typed authority key and feature-owned state |
| 3 Local evidence | ClientUserAgent, ClassifyToolName, WorkspaceView, codexclientcompat research | No unified classifier; UA quality varies | Pure evidence summary + conservative heuristic |
| 4 Same-turn ordering | immutable runtime snapshot and legal stages | session_open too early; transforms too late | New metadata-only stage after secret guard |
| 5 Large-body parity | proof compiler, WireEligibilitySummary, bounded wire facts | no classifier facts; plane census fixed at 26 | Add bounded proof fact and extend census |
| 6 Jev | auxiliary/network infrastructure patterns | no TypeSafe adapter; public API is early access | Provider-neutral port + optional adapter |
| 7 Privacy | secret guard, bounded diagnostics conventions | remote classifier creates new egress surface | Derived evidence only; no content/default secrets |
| 8 Config/reload | plugins.features, feature-owned YAML decode, generation publication | no feature config | New feature-owned config and generation binder |
| 9 Observability | Prometheus registration, inventory/evidence conventions | no classification metrics | Feature-owned bounded collector/observer |
| 10 Performance | high-concurrency steering, bounded process stores | naive DB/network/transcript scan would regress hot path | Cache positive state; bounded local evaluation; single-flight |
| 11 Brownfield reuse | featurehost, closed plane catalog, tool classifier | matcher duplication and temptation for core switches | shared pure matcher + feature-owned policy |
| 12 Certification | testkit, dbparity, plane generator, large-body differential tests | no cross-harness classifier matrix | Reuse existing fixtures and add negative matrix |

## Implementation Approach Options

### Option A: Extend session.Opener and SessionView.Labels

**Approach**

- detect at session_open;
- store coding=true as a label;
- let later features read the label.

**Advantages**

- few new public types;
- existing metadata-only plane;
- no new legal stage.

**Blocking problems**

- session_open runs before authoritative BeginTurn/A-leg binding;
- OpenInput lacks workspace, UA and tool evidence;
- labels are not a durable feature-state authority;
- widening Opener to full Call would distort a currently narrow stage;
- cannot safely satisfy restart, same-turn authority, or tool-cluster evidence.

**Verdict:** rejected.

### Option B: New exclusive metadata-only classifier plane with feature-owned state

**Approach**

- add a narrow sessionclassification SDK contract;
- add one legal classification stage/plane;
- run after secret guard/authority and before submit;
- derive bounded current-turn evidence;
- feature implementation evaluates local policy and state;
- featurehost owns process/durable resources and optional Jev adapter;
- project result into SessionView;
- add bounded wire evidence for large-body parity.

**Advantages**

- exact ownership boundary;
- same-turn consumers;
- no coding-client switch in core;
- large-body compatible;
- durable state remains feature-owned;
- future coding features share one fact.

**Costs**

- new platform plane/stage and generated-surface updates;
- one small feature table/store;
- dual canonical/wire integration needs differential tests.

**Verdict:** selected.

### Option C: Core-owned classification service

**Approach**

- add classifier/store directly to Executor/config/core session state.

**Advantages**

- easy runtime access;
- fewer SDK abstractions initially.

**Blocking problems**

- optional UX policy becomes kernel behavior;
- concrete coding-agent matchers would grow in core;
- conflicts with completed feature-ownership closure;
- risks generic service/state bags and core config growth.

**Verdict:** rejected.

## Brownfield Requirements Repairs

The full requirements pass changed the initial product brief in these material ways:

1. **No universal User-Agent assumption.** Stable known family identities can be decisive; ambiguous SDK User-Agent values cannot.
2. **No V1 negative state.** Unknown remains a real state so later evidence can promote without flapping.
3. **No raw client-hint persistence key.** Secure SessionID, otherwise proxy-owned A-leg.
4. **Permanent now has a precise durability contract.** Durable resumable sessions restore classification after restart; non-durable deployments do not claim restart persistence.
5. **Filename/content scanning is not a required V1 path.** A lone filename/code token is explicitly weak. Default classification uses bounded metadata and workspace/tool structure.
6. **Same-turn ordering is explicit.** Positive first-turn classification must be visible before downstream request feature stages.
7. **Large-body parity is a first-class requirement.** The new feature may not silently disable the wire lane.
8. **Jev is optional/fail-open.** No external classifier is a request correctness dependency, and the vendor wire schema is not a public AIProxer contract.
9. **Infrastructure landing is behavior-neutral.** Existing coding features do not become automatically gated merely because the classification substrate exists.

The repaired requirements passed the requirements gate: each criterion is observable, numeric, and testable; no requirement depends on a specific database schema, concrete HTTP endpoint, or guessed TypeSafe DTO.

## Design Discovery Decisions

### Decision: classification value is bounded scalar state

The SessionView classification projection should avoid variable content slices. A compact value can carry:

- Kind;
- Source;
- ConfidenceBand;
- one decisive EvidenceCode;
- Revision.

This is sufficient for consumers and observability while minimizing clone/copy changes throughout the SDK view graph. Detailed transient evidence used during evaluation remains feature-private and content-free.

### Decision: classification input is a bounded evidence summary

A provider-neutral evidence value should carry only what the classifier needs from the request:

- accepted ClientUserAgent;
- operation;
- a compact tool-category bitset/counters.

Workspace markers and session authority are already available as typed views and do not need to be copied into wire evidence.

No prompt text or tool arguments enter this contract.

### Decision: the extension plane is exclusive

There should be one effective session classifier per generation. Ordered multiple classifiers would make monotonic state, remote attempt ownership, confidence/evidence precedence, and observability ambiguous.

Multiple configured contributors therefore fail generation composition instead of running a chain.

### Decision: classifier failures are request fail-open

Classification is advisory. Runtime/store/remote failures produce bounded diagnostics and unknown unless a cached/persisted positive classification is already known. Invalid configuration still fails before generation publication.

### Decision: durable state uses feature-owned compare-and-promote semantics

The state machine is intentionally small:

~~~text
unknown
  |
  | accepted positive proposal
  v
coding_agent
~~~

Remote attempt/lease metadata is control state around unknown, not a third classification value.

A durable implementation must atomically:

- load current record;
- promote only if still unknown;
- preserve the first accepted positive classification;
- claim at most one active remote attempt lease per authoritative key;
- expire abandoned leases;
- consume a finite attempt budget.

### Decision: cache and durable store have separate jobs

The process cache/coordinator serves the hot path. Durable storage is used for:

- first cache fill on an authoritative session;
- promotion;
- remote claim/completion;
- restart restoration;
- bounded cleanup.

A warm coding_agent turn does not hit the database.

### Decision: no per-session background worker

Lease expiry is time-based state evaluated on claim/touch. Cache cleanup uses one bounded process owner or ordinary bounded eviction, not one goroutine/timer per session.

### Decision: V1 local positive rules stay intentionally small

The initial deterministic policy should support:

1. high-confidence known client identity;
2. a distinctive read/search + edit/remove + OS-command tool cluster without requiring a workspace marker;
3. a weaker read/search + mutation cluster only when corroborated by a recognized workspace marker.

Weak content-like evidence is intentionally excluded from V1 promotion. Additional evidence can be added later only if it preserves boundedness and large-body semantics.

### Decision: Jev modes have distinct authority

- heuristic: local positive rules may promote; no remote call.
- jev: local evidence is only an input summary; Jev decides positive promotion.
- hybrid: local positive rules run first; Jev is attempted only if still unknown.

All modes reuse the same monotonic state/store contract.

## Brownfield Design Validation

### Validation concern 1: same-turn stage versus existing closed pipeline

**Initial risk:** hide classification in a request transform or Executor callback.

**Repair:** define a legal StageIDSessionClassification after secret_guard and before submit_request plus one exclusive PlaneSessionClassifier. Update the manifest generator and large-body census. This makes ordering executable policy rather than a comment.

**Verdict:** resolved.

### Validation concern 2: MetadataOnly declaration could be false on the wire path

**Initial risk:** mark the plane MetadataOnly but feed it canonical Call/Tools, causing the wire lane either to skip semantics or silently fall back.

**Repair:** define a bounded classification evidence DTO in the SDK; canonical execution derives it from Invocation.ClientUserAgent plus ClassifyToolName over tool names; certified frontend proof compilers derive the same DTO without a shadow Call. Add it to bounded proof/session facts and require differential tests.

**Verdict:** resolved.

### Validation concern 3: remote single-flight only in process memory is insufficient for durable multi-instance sessions

**Initial risk:** two replicas could call Jev for the same session concurrently.

**Repair:** the feature state contract includes an atomic remote-attempt lease/claim with expiry and finite attempt count. Memory store implements process semantics; Bun store implements shared durable semantics. Network I/O occurs after the claim and outside database transactions.

**Verdict:** resolved.

### Validation concern 4: public classification details could create copy/alias churn

**Initial risk:** expose arbitrary []string evidence lists in SessionView and then repair clone paths across many SDK views.

**Repair:** persist/project one typed decisive EvidenceCode plus scalar Source/Confidence/Revision. Transient secondary evidence remains inside the classifier and bounded diagnostics.

**Verdict:** resolved.

### Validation concern 5: disabled optional feature must not become a startup/database dependency

**Initial risk:** eagerly constructing/ensuring the feature's Bun table during process featurehost startup would make a never-enabled optional classifier capable of failing startup and would perform feature-specific persistence work before any generation enabled it.

**Repair:** featurehost owns only a lightweight process StateHolder/coordinator shell initially. An enabled generation contributes an overlap-safe feature lifecycle whose prepare Start initializes/ensures the memory or Bun store once before publication. Disabled-only deployments never ensure the classification schema. Overlapping enabled generations share the process holder; final process close remains the single disposal owner.

**Verdict:** resolved.

### Design validation verdict

**GO.** After the repairs above, the design respects feature ownership, preserves same-turn ordering, has an exact large-body parity story, avoids untrusted session keys, keeps disabled deployments free of classification persistence/network dependencies, and gives Jev bounded fail-open semantics without freezing an unavailable vendor API contract.

## Implementation Risk and Effort

- **Effort: L (approximately 1-2 weeks for one experienced contributor)** — the heuristic is small, but the feature crosses SDK plane generation, runtime ordering, large-body proof parity, process/durable state, optional remote adapter, metrics, and DB parity.
- **Risk: Medium-High** — primary risks are semantic parity between canonical/wire execution, durable concurrency/lease behavior, and accidental core/feature ownership leakage. All three now have explicit contracts and test gates.

## Research Items Carried to Implementation

These do not block specification readiness but must be checked against current-main/current vendor documentation immediately before the corresponding implementation task:

1. Reconfirm the exact TypeSafe early-access API endpoint/auth/request/response contract before implementing the Jev HTTP adapter. The SDK port and privacy contract in design.md are authoritative if vendor details change.
2. Re-run the client-family matcher source survey against current upstream releases before freezing exact positive tokens. Do not convert generic SDK identifiers into positive client identities.
3. Revalidate feature-plane census and generated files against implementation-time main; current baseline is 26 planes and this design adds one.
4. Revalidate the exact canonical and ExecuteLargeBody ordering if #624/#637/#394 or later runtime work lands first; preserve the ordering invariants rather than stale line numbers.
