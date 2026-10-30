# Requirements Document

## Introduction

AIProxer increasingly provides agentic and coding-oriented capabilities that are valuable for terminal and IDE coding harnesses but inappropriate to apply indiscriminately to ordinary chat or API traffic. This specification adds one provider-neutral session classification fact that answers a deliberately narrow question: has this logical session been positively identified as a coding-agent session?

The V1 classification is monotonic and conservative. A session begins as unknown and may be promoted to coding_agent when bounded positive evidence is sufficient. Absence of evidence is not a negative classification, and V1 does not persist a not_coding state. Classification is advisory feature metadata only: it does not grant authorization, entitlement, billing treatment, or permission.

The feature is brownfield. It must reuse existing canonical client User-Agent capture, coding-tool classification, workspace facts, secure-session and A-leg authority, immutable runtime generations, feature-host ownership, and the large-payload wire path. The infrastructure landing is behavior-neutral for existing coding-oriented features; consumers opt in separately.

## Boundary Context

- **In scope**: the unknown to coding_agent classification contract; bounded local heuristics; authoritative session/A-leg keying; monotonic state and restart-resume restoration; same-turn projection to downstream feature consumers; a metadata-only extension stage and plane; bounded canonical and large-body wire evidence; optional Jev remote classification; configuration, observability, performance, persistence, and certification.
- **Out of scope**: general programming-topic classification; semantic completion verification; request-purpose/helper-call classification from #458; automatic route/model selection; authorization/RBAC; billing entitlement; changing #637/#467/#468/#469/#475 behavior in this change; full transcript scanning; retaining codebase/file inventories; mandatory external classifier use.
- **Adjacent expectations**: #645 is the product tracker. Existing secure-session authority, B2BUA lineage, tool classification, large-payload fast path, feature-host composition, generation reload, and database parity remain authoritative. Future coding-specific consumers decide independently whether positive coding_agent gating is semantically appropriate.
- **Boundary ownership**: session-classification policy and state are optional feature concerns; public SDK contracts expose only bounded provider-neutral classification/evidence; generic core executes a narrow feature-neutral stage and projects the result; standard featurehost owns concrete standard-feature process/generation resources; TypeSafe/Jev remains an adapter behind the feature boundary.
- **Revalidation triggers**: SessionView shape or cloning rules, secure-session/A-leg authority, extension-stage ordering, closed feature-plane catalog/code generation, canonical tool classification, workspace evidence, large-body proof/wire facts, featurehost process ownership, persistence topology, or config generation semantics change.

## Requirements

### Requirement 1: Conservative Session Classification Contract

**Objective:** As a feature consumer, I want one stable coding-agent classification fact so that coding-specific behavior can be gated without duplicating client heuristics.

#### Acceptance Criteria

1.1. **Where** session classification is absent or disabled, AIProxer shall preserve existing request behavior, shall perform no classification-specific external call, and shall expose no positive coding-agent classification to newly admitted turns.

1.2. **When** a logical session has not accumulated sufficient positive evidence, the session classification shall remain unknown.

1.3. **When** sufficient positive evidence is accepted, AIProxer shall promote the logical session to coding_agent and shall expose a bounded classification snapshot containing kind, source, confidence band, decisive evidence code, and revision.

1.4. **While** a logical session is classified as coding_agent, later weak, absent, contradictory, excluded, or remote-negative evidence shall not downgrade the session to unknown or another negative state.

1.5. **The** V1 classification vocabulary shall contain unknown and coding_agent semantics and shall not persist a not_coding state.

1.6. **When** a downstream feature consumes classification, the feature shall be able to decide from the immutable session classification snapshot without parsing User-Agent, rescanning prompt content, re-running the classifier, or consulting a vendor-specific result.

1.7. **The** classification shall be advisory derived metadata and shall not by itself authorize a tool, grant permissions, establish identity, alter billing entitlement, or override routing authority.

1.8. **When** this infrastructure first lands, existing coding-oriented features shall retain their existing behavior until their own implementation explicitly opts into classification gating.

### Requirement 2: Authoritative Scope, Monotonic State, and Resume Semantics

**Objective:** As an operator, I want classification to follow proxy-owned session authority so that state cannot leak between clients or disappear across valid continuation.

#### Acceptance Criteria

2.1. **When** an authoritative secure SessionID is available, classification state shall be scoped to that proxy-owned SessionID.

2.2. **When** no authoritative secure SessionID is available for an ordinary client turn, classification state shall be scoped to the proxy-owned A-leg rather than to an arbitrary client session hint.

2.3. **If** two clients supply the same client-controlled session hint without sharing proxy-owned session/A-leg authority, AIProxer shall keep their classification state isolated.

2.4. **When** routing, backend/model selection, retry, race, failover, compaction, or provider continuity changes inside the same authoritative logical session, the positive classification shall remain unchanged.

2.5. **When** concurrent turns race to classify the same authoritative session, AIProxer shall converge on one monotonic positive classification transition and shall not publish contradictory revisions.

2.6. **When** identical positive evidence is replayed after classification, the classification transition shall be idempotent and shall not advance the classification revision merely because the evidence was repeated.

2.7. **When** a secure session is durably resumable across process restart, a previously persisted coding_agent classification shall be restored before a resumed turn reaches coding-classification consumers.

2.8. **Where** the deployment does not provide durable session/feature persistence, AIProxer shall not claim restart durability for classification, while preserving the same in-process/A-leg isolation semantics.

2.9. **When** a new unrelated authoritative session or A-leg is created, it shall begin unknown and shall not inherit another session's classification.

2.10. **When** configuration reload publishes a new runtime generation, the reload shall not erase an already persisted positive classification for the same logical session.

### Requirement 3: Explainable Local Evidence and False-Positive Control

**Objective:** As a user of ordinary technical chat as well as coding agents, I want positive classification to require coding-agent evidence rather than merely a programming topic.

#### Acceptance Criteria

3.1. **When** a bounded client identity value matches a versioned high-confidence coding-harness identity rule, the local heuristic may classify the session as coding_agent without requiring prompt-text inference.

3.2. **If** a User-Agent is generic, shared with an underlying SDK, unknown, or known to be ambiguous for a coding harness, that User-Agent alone shall not classify the session as coding_agent.

3.3. **When** the current turn exposes a distinctive coding tool cluster containing at least one file read/search category, at least one file edit/remove category, and an OS-command category, the local heuristic shall be able to classify the session as coding_agent without a workspace marker; **when** the tool cluster contains read/search plus only one local mutation category, a recognized project marker shall be required as corroboration before promotion.

3.4. **If** only one generic shell, browser, web, read, or search tool is present without other decisive evidence, the local heuristic shall remain unknown.

3.5. **If** evidence consists only of a source filename or extension, a Markdown code fence, programming-language names, words such as code/bug/repository, or other technical prose, the local heuristic shall remain unknown.

3.6. **If** evidence consists only of a model name or backend/route identity commonly used by coding agents, the local heuristic shall remain unknown.

3.7. **Where** bounded exclusion rules are configured, an exclusion may prevent a prospective local match but shall not revoke an already established coding_agent classification.

3.8. **The** stock local heuristic shall not require rescanning the complete accumulated transcript or retaining raw prompt history to make a decision.

3.9. **When** tool evidence is derived, AIProxer shall reuse the canonical tool-category semantics rather than maintain a second incompatible coding-tool taxonomy.

3.10. **When** known client-family identity rules are shared by more than one root-module feature, their stable bounded matching vocabulary shall have one reusable source of truth rather than independent copies.

### Requirement 4: Same-Turn Ordering and Safe Degradation

**Objective:** As a coding-specific feature, I want decisive first-turn classification before my stage runs so that behavior does not depend on waiting for a second user turn.

#### Acceptance Criteria

4.1. **When** classification runs for an ordinary client turn, authoritative secure-session/A-leg identity and the resolved workspace shall already be available to the classifier.

4.2. **When** the classifier positively classifies the first eligible turn, the resulting classification shall be visible on that same turn to later submit, tool-catalog, request-shaping, pre-request, and route-hint consumers.

4.3. **When** any classification mode would send or inspect content beyond bounded metadata, that work shall occur only after applicable secret-guard processing; the V1 default path shall not require raw content.

4.4. **If** classification evaluation, state loading, persistence, or an optional remote decision fails at request time, AIProxer shall preserve the user request and conservatively expose unknown unless a prior positive classification is already available.

4.5. **When** a proxy-owned detached auxiliary call is created, AIProxer shall not treat its private child A-leg as evidence of a new independent coding-agent client session.

4.6. **The** classification stage shall not itself mutate user messages, instructions, tools, route selectors, model options, or backend choice.

4.7. **When** classification is disabled in a newly published generation, newly admitted turns shall skip classification-stage work even if previously persisted state remains available for a later re-enabled generation.

### Requirement 5: Large-Payload Wire-Path Compatibility

**Objective:** As an operator serving large coding contexts, I want classification without forfeiting the low-copy wire fast path.

#### Acceptance Criteria

5.1. **Where** the stock classifier is enabled, its presence alone shall not make an otherwise wire-eligible large request canonical-required.

5.2. **When** a certified frontend compiles a large-body proof, the proof shall be able to carry only bounded classification evidence needed by the stock classifier, including accepted client identity and a bounded tool-category summary, without carrying a raw header bag or prompt text.

5.3. **When** canonical and wire paths observe the same accepted User-Agent and tool catalog, they shall derive equivalent bounded classification evidence.

5.4. **When** wire-path evidence is insufficient to classify, AIProxer shall keep the session unknown or defer promotion rather than force full request materialization solely for classification.

5.5. **The** wire representation used for classification shall contain no transcript, message tree, tool-definition list, arbitrary options map, raw local path, or shadow canonical Call.

5.6. **When** the new extension plane is declared, it shall have an explicit request-access class consistent with its bounded metadata contract; unclassified or accidentally content-shaped changes shall fail the existing plane/large-body architecture gates.

5.7. **When** the large-body path commits to wire execution, classification failure shall not request a post-commit canonical fallback or create a second ordinary execution.

### Requirement 6: Optional Jev Remote Classification

**Objective:** As an operator, I want an optional low-latency structured classifier for ambiguous sessions without making an external service a correctness dependency.

#### Acceptance Criteria

6.1. **Where** remote/Jev classification is not explicitly configured, AIProxer shall make no remote classifier request.

6.2. **When** mode heuristic is selected, only deterministic local rules shall be allowed to promote an unknown session.

6.3. **When** mode jev is selected, local logic may construct bounded evidence but shall require the configured remote decision to promote an otherwise unknown session.

6.4. **When** mode hybrid is selected, decisive local evidence may promote first; only sessions still unknown after local evaluation shall be eligible for a remote decision.

6.5. **While** a session is already coding_agent, AIProxer shall make no further remote classification call for that logical session.

6.6. **When** concurrent ambiguous turns target the same logical session, AIProxer shall permit at most one active remote classification lease for that session within the configured persistence/coordinator scope and shall prevent a per-turn retry storm.

6.7. **When** a remote attempt is permitted, AIProxer shall enforce a hard timeout, a finite configured attempt budget, and bounded retry/lease-expiry behavior.

6.8. **When** a valid remote result meets the configured positive threshold, AIProxer may promote the session to coding_agent; a below-threshold result shall leave the session unknown and shall not create a durable negative classification.

6.9. **If** the remote service times out, returns an error, is rate limited, is unavailable, or produces malformed/unmappable output, AIProxer shall preserve the user request and leave an unknown session unknown.

6.10. **If** remote mode is configured but required credentials or mandatory configuration are invalid or absent, candidate generation shall fail before publication rather than serving a partially configured remote mode.

6.11. **When** a process dies while holding a shared remote-classification lease, the lease shall expire safely so a later eligible attempt can proceed within the finite attempt budget.

### Requirement 7: Privacy, Security, and Data Minimization

**Objective:** As a user and operator, I want classification to reveal as little session content as possible and never become a security authority.

#### Acceptance Criteria

7.1. **When** AIProxer calls a remote classifier, it shall not send Authorization headers, provider credentials, resume tokens, secret-guard findings containing secret material, or raw HTTP header bags.

7.2. **The** default V1 remote payload shall consist of bounded derived evidence and shall not contain the complete prompt, transcript, reasoning content, tool arguments, or response body.

7.3. **The** classification store shall not persist raw User-Agent values, raw filesystem paths, prompt excerpts, tool arguments, or reversible content fingerprints solely for classification.

7.4. **When** classification evidence is persisted, it shall use bounded typed state such as classification kind/source/confidence/evidence code, revision, remote-attempt state, and timestamps rather than retained content.

7.5. **When** errors or diagnostics are emitted, raw remote response bodies and credential-bearing request details shall not cross the client boundary or enter ordinary logs.

7.6. **The** classification result shall not substitute for authentication, authorization, workspace ownership proof, secure-session resume proof, destructive-tool policy, or billing identity.

7.7. **When** untrusted client metadata supplies a claimed agent identity, the classifier shall treat it as evidence subject to bounded matching rules and shall not elevate it into proxy identity authority.

### Requirement 8: Feature-Owned Configuration and Generation Semantics

**Objective:** As an operator, I want classification policy to be explicit, reload-safe, and validated before it affects serving traffic.

#### Acceptance Criteria

8.1. **Where** the standard session-classification feature is configured, its semantic configuration shall live under the canonical feature registration/config payload rather than a new generic core configuration section.

8.2. **When** the feature is enabled without an explicit mode, the documented V1 default shall be deterministic heuristic mode.

8.3. **Where** Jev is configured, network use shall remain opt-in and credentials shall be referenced through the supported secret/environment mechanism rather than embedded into diagnostics or generated examples.

8.4. **If** configuration specifies an unknown mode, impossible threshold, non-positive timeout, invalid attempt/lease bounds, contradictory local/remote settings, or unbounded matcher data, candidate generation shall fail before publication.

8.5. **When** configuration reload changes heuristic/exclusion rules, remote mode, thresholds, or timeouts, newly admitted turns shall use the new immutable generation while in-flight turns retain their admitted generation's classifier policy.

8.6. **When** a candidate generation containing invalid classification configuration fails, the last-good published generation and process-owned classification state shall remain unchanged.

8.7. **When** the feature is disabled by reload, positive durable classification rows shall not be deleted merely because a generation stopped consuming them.

8.8. **When** the feature is later re-enabled for the same authoritative resumable session, AIProxer shall be able to recover the previously persisted positive classification subject to the configured state-retention policy.

### Requirement 9: Bounded Explainability and Observability

**Objective:** As an operator, I want to understand classification transitions without logging user content or creating high-cardinality metrics.

#### Acceptance Criteria

9.1. **When** a session transitions from unknown to coding_agent, AIProxer shall emit one bounded classification observation describing source, confidence band, decisive evidence code, and revision.

9.2. **When** no transition occurs, diagnostics may expose bounded evaluation outcomes such as unknown, excluded, remote_skipped, remote_timeout, or remote_error without storing prompt excerpts.

9.3. **When** remote classification is attempted, AIProxer shall expose bounded outcome and latency observations without recording the request/response body as a metric label.

9.4. **The** metrics surface shall use only bounded labels and shall not use session IDs, A-leg IDs, raw User-Agent, filenames, paths, prompts, arbitrary client metadata, or arbitrary vendor result strings as labels.

9.5. **When** an operator-visible session/request explanation surface later consumes classification evidence, it shall be able to project the bounded classification snapshot without reaching into feature-private classifier state.

9.6. **Where** classification is disabled, classification-specific hot-path observations shall remain absent except static feature/inventory state.

### Requirement 10: Hot-Path Performance, Concurrency, and Resource Bounds

**Objective:** As an operator targeting high concurrency, I want session classification to remain a bounded metadata operation rather than a new request-path bottleneck.

#### Acceptance Criteria

10.1. **While** a session has a warm positive classification cache entry, serving a new turn shall require no remote classifier call, no transcript scan, and no classification-specific database write.

10.2. **While** a session is unknown, local evaluation shall inspect only bounded current-turn metadata, tool-name categories, workspace markers, and cached/persisted classification control state.

10.3. **The** implementation shall not serialize unrelated sessions behind one long-held process-wide classification mutex.

10.4. **When** a remote call is made, network I/O shall occur outside locks/transactions that block unrelated sessions.

10.5. **The** process cache/coordinator shall have explicit capacity and idle/expiry cleanup semantics and shall not grow without bound with unique session hints.

10.6. **The** durable store shall not be read or written on every classified turn merely to rediscover a stable positive value; durable I/O shall be limited to cache misses, promotion, remote-claim/completion, or lifecycle/cleanup work.

10.7. **The** implementation shall not create one permanent goroutine, timer, or background worker per session or per request.

10.8. **When** classification is disabled, benchmark/architecture evidence shall show no classifier evaluation and no remote/store hot-path work attributable to the feature.

### Requirement 11: Brownfield Reuse and Architecture Boundaries

**Objective:** As a maintainer, I want one classification substrate that fits existing AIProxer boundaries instead of creating another parallel client-detection subsystem.

#### Acceptance Criteria

11.1. **When** canonical request execution supplies client User-Agent evidence, the classifier shall consume the already validated canonical client User-Agent rather than reparsing HTTP headers in generic core.

11.2. **When** tool evidence is summarized, the implementation shall reuse the existing canonical tool-name classifier and its category semantics.

11.3. **When** the session classifier and an existing root-module compatibility feature need the same stable client-family token matching, the implementation shall reuse one bounded pure matcher catalog where doing so does not violate connector/module isolation.

11.4. **The** generic core/runtime shall not contain a switch over Codex, Cline, Roo, OpenCode, Droid, Hermes, Pi, or other concrete coding-client identities.

11.5. **The** public/canonical contracts shall not contain TypeSafe/Jev HTTP DTOs, provider SDK types, database models, or raw frontend header maps.

11.6. **The** implementation shall not turn B2BUA records, secure-session PolicyMetadata, or SessionView.Labels into an arbitrary generic feature-state database.

11.7. **When** the feature is removed or disabled, the generic proxy shall continue to route, stream, recover, account, and encode requests without requiring classification infrastructure.

11.8. **The** classification implementation shall preserve streaming-first response behavior and the existing prohibition on transparent retry/failover after first client-visible output.

### Requirement 12: Certification Matrix and Regression Gates

**Objective:** As a maintainer, I want executable evidence that classification is useful for real coding harnesses without regressing ordinary traffic or architecture guarantees.

#### Acceptance Criteria

12.1. **When** the local classifier is tested against the repository's existing coding-agent research fixtures, each harness with stable supported positive evidence shall have at least one positive fixture and documented evidence source.

12.2. **When** ordinary technical-chat fixtures contain code fences, programming-language names, repository discussion, or isolated filenames, they shall remain unknown unless an independent decisive rule is also satisfied.

12.3. **When** a Cline-like request exposes only an ambiguous underlying SDK User-Agent, it shall remain unknown until other supported positive evidence is present.

12.4. **When** two sessions reuse the same client-controlled session hint, tests shall prove no classification state crosses authoritative session/A-leg boundaries.

12.5. **When** concurrent turns classify the same session, tests shall prove monotonic promotion, one coherent revision, and bounded remote single-flight/lease behavior.

12.6. **When** reload, process restart with durable state, route change, retry/failover, and compaction scenarios are exercised, tests shall prove positive classification continuity without rewriting historical request state.

12.7. **When** the canonical and large-body wire paths receive equivalent bounded evidence, differential tests shall prove equivalent classification input and classification outcome.

12.8. **When** Jev/remote timeout, malformed output, rate limit, server error, stale lease, and below-threshold outcomes are simulated, tests shall prove fail-open-to-unknown behavior and finite attempts.

12.9. **The** default unit and composed test suites shall remain hermetic and shall not require TypeSafe/Jev credentials or external network access; any live remote sentinel shall be explicit and opt-in.

12.10. **Where** classification persistence supports both SQLite and PostgreSQL through the standard Bun topology, logical schema and behavioral parity shall pass the repository database-parity gate.

12.11. **When** the new stage/plane is added, the closed-plane generator, plane census, request-access classification, architecture guards, diagnostics inventory, and generated-output currency checks shall pass.

12.12. **Before** implementation is considered complete, focused feature/runtime/large-body tests plus the applicable quality, parity, database, race/concurrency, and wide QA gates shall pass or any environment-gated omissions shall be documented truthfully.
