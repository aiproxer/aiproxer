# Design Document

## Overview

This design introduces one bounded, provider-neutral session-classification substrate for coding-agent execution. It adds an exclusive metadata-only classifier plane, executes that plane after authoritative secure-session/A-leg binding and secret guard, projects the resulting immutable classification into SessionView on the same turn, and persists positive state under proxy-owned identity.

The standard implementation is an optional feature named session-classification. Its default mode is deterministic local heuristic classification. Optional Jev mode is an external decision adapter behind the same feature contract. No existing coding-oriented feature changes behavior merely because this substrate is present.

The design deliberately separates five concerns:

1. **Evidence extraction** — generic bounded request facts such as accepted client User-Agent and canonical tool categories.
2. **Policy** — feature-owned rules deciding whether evidence is sufficient for coding_agent.
3. **State authority** — feature-owned monotonic state keyed by secure SessionID or proxy A-leg.
4. **Projection** — one immutable SessionView classification for downstream same-turn consumers.
5. **Vendor adaptation** — optional TypeSafe/Jev HTTP details isolated behind a RemoteDecider port.

## Goals

- Provide one stable unknown to coding_agent session fact.
- Make decisive first-turn classification visible to later feature stages on that same turn.
- Keep coding-client-specific match rules out of generic core/runtime.
- Reuse existing ClientUserAgent and ClassifyToolName semantics.
- Preserve the large-payload wire fast path with bounded classification facts.
- Persist positive classification for durably resumable sessions without turning B2BUA or secure-session policy into a feature-state bag.
- Make optional remote classification fail-open and privacy-minimized.
- Keep the hot path O(1) for warm positive sessions and bounded for unknown sessions.
- Preserve current behavior of all existing feature consumers until they opt in separately.

## Non-Goals

- Programming-topic or code-snippet classification.
- A general session-label framework.
- A generic ML/classifier plugin marketplace.
- Request-purpose/helper-call classification.
- Automatic model/routing changes.
- Tool authorization or destructive-action policy.
- Full prompt/path/file-list retention.
- Replacing secure-session authority or B2BUA lineage.
- Making detached proxy-owned auxiliary calls independent coding sessions.
- Freezing a guessed TypeSafe early-access HTTP schema in public contracts.

## Architecture Analysis

### Current extension ordering

The secure canonical path on the design baseline resolves session/workspace state before ordinary request shaping:

~~~text
session open
workspace resolve
BeginTurn
Fetch A-leg
secret guard
frontend ingress checkpoint
request authority
submit request
CTP traffic
conversation projection
tool catalog
request wide shaping
pre request
route hint
candidate execution
~~~

session_open is too early and does not have authoritative SessionID/A-leg, WorkspaceView, ClientUserAgent, or tool evidence. request_wide_shaping is too late for same-turn tool-catalog gating.

The new stage therefore belongs after secret_guard and before frontend ingress/submit processing.

### Current ownership rules

- Optional product policy belongs in feature plugins.
- Generic runtime may execute provider-neutral SDK contracts but must not import concrete feature implementations.
- internal/standardplugins/featurehost is the standard distribution's concrete feature composition owner.
- Process-owned feature resources survive generation reload and are disposed by featurehost.
- Public SessionView is the established read-only session projection visible to downstream feature contracts.
- The extension plane catalog and large-body plane census are closed executable policies.

The design follows those boundaries rather than adding an Executor-specific session-classification field.

## Target Architecture

~~~mermaid
flowchart TD
    FE[Frontend decode or wire proof]
    EVID[Bounded classification evidence]
    SS[Secure session and A leg authority]
    SG[Secret guard]
    STAGE[Session classification stage]
    CLASS[Exclusive classifier plane]
    STATE[Feature state and coordinator]
    LOCAL[Local heuristic]
    REMOTE[Optional remote decider]
    VIEW[SessionView classification]
    NEXT[Submit tool catalog request and route stages]

    FE --> EVID
    FE --> SS
    SS --> SG
    SG --> STAGE
    EVID --> STAGE
    STAGE --> CLASS
    CLASS --> LOCAL
    CLASS --> STATE
    LOCAL --> STATE
    CLASS --> REMOTE
    REMOTE --> STATE
    STATE --> CLASS
    CLASS --> VIEW
    VIEW --> NEXT
~~~

The generic stage knows only the SDK Classifier interface. The concrete standard classifier, persistence, cache/coordinator, matcher catalog, and Jev adapter remain outside generic core.

## Public SDK Contracts

### Session classification projection

Add a bounded scalar classification value under pkg/lipsdk/session. The zero value represents unknown so existing SessionView literals remain source-compatible.

Conceptual contract:

~~~go
package session

type Kind string

const (
    KindUnknown     Kind = ""
    KindCodingAgent Kind = "coding_agent"
)

type ClassificationSource string

const (
    SourceLocalIdentity ClassificationSource = "local_identity"
    SourceLocalTooling  ClassificationSource = "local_tooling"
    SourceRemote        ClassificationSource = "remote_classifier"
)

type ConfidenceBand string

const (
    ConfidenceUnknown ConfidenceBand = ""
    ConfidenceHigh    ConfidenceBand = "high"
)

type EvidenceCode string

type Classification struct {
    Kind       Kind
    Source     ClassificationSource
    Confidence ConfidenceBand
    Evidence   EvidenceCode
    Revision   uint64
}

func (c Classification) IsCodingAgent() bool
func (c Classification) Validate() error
~~~

Rules:

- unknown is the all-zero value;
- coding_agent requires non-empty source, confidence, evidence and Revision greater than zero;
- no raw User-Agent, path, prompt excerpt, remote body, or arbitrary evidence slice is stored here;
- EvidenceCode is a bounded opaque typed code produced by the feature. It is suitable for diagnostics but is not authorization.

SessionView gains:

~~~go
Classification Classification
~~~

Because Classification contains scalars only, existing SessionView clone/copy helpers need no new deep-copy collection.

### Bounded classification evidence

Add pkg/lipsdk/sessionclassification for the generic plane contract.

Conceptual types:

~~~go
package sessionclassification

type ToolCategorySet uint16

type Evidence struct {
    Operation       lipapi.Operation
    ClientUserAgent string
    ToolCategories  ToolCategorySet
}

type Input struct {
    TraceID   string
    Session   session.SessionView
    Workspace workspace.WorkspaceView
    Evidence  Evidence
}

type Classifier interface {
    ID() string
    Classify(context.Context, Input) (session.Classification, error)
}
~~~

Evidence constraints:

- ClientUserAgent is already accepted/bounded by the canonical identity policy or the same frontend acceptance helper on the wire path;
- ToolCategories is a fixed bitset, never a ToolDef slice;
- WorkspaceView is read-only; the classifier inspects only bounded marker names/labels needed by policy and never persists ProjectRoot;
- no full Call is passed to Classifier;
- no classifier may mutate request content or route state.

### Tool evidence builder

The SDK package provides a small pure accumulator/helper that maps tool names through lipapi.ClassifyToolName and sets category bits. Canonical runtime and certified frontend proof compilers use the same helper.

The initial useful bits are:

- file_read;
- file_search;
- os_command;
- file_edit;
- file_remove;
- web_access;
- unknown_seen.

Counts are not required for V1 policy. Presence bits avoid unbounded counters and are sufficient for a distinctive cluster.

## New Extension Stage and Plane

### Stage

Add:

~~~text
StageIDSessionClassification = session_classification
~~~

Ordered after secret_guard and before submit_request.

Stage role is Observe from the canonical request's perspective: it derives session metadata but does not mutate the Call. It may update feature-owned state and returns a new SessionView classification projection.

### Plane

Add an exclusive plane:

~~~text
PlaneSessionClassifier
Type: sessionclassification.Classifier
Multiplicity: exclusive
Request access: MetadataOnly
Execution stage: session_classification
~~~

Why exclusive:

- two classifiers could race to promote with different source/evidence semantics;
- remote-attempt ownership would become ambiguous;
- consumer observability should describe one classification authority;
- combining local plus Jev is already handled inside one standard classifier through mode heuristic, jev, or hybrid.

The plane supports standard feature generation binding. Duplicate effective contributors fail generation compilation.

### Closed-plane updates

Implementation must update:

- plane manifest declaration;
- legal stage descriptor table;
- generated plane accessors/snapshot bindings;
- diagnostics/inventory stage ordering;
- generated-surface currency tests;
- large-body fixed plane order/count from 26 to 27;
- static disposition/request-access parity tests.

A future change that makes the classifier content-shaped must change the request-access classification and fail the existing wire-eligibility gates until a new certified wire contract exists.

## Generic Runtime Stage

Add a feature-neutral runner under internal/core/extensions plus narrow orchestration in runtime.

Conceptual behavior:

~~~go
func RunSessionClassificationStage(
    ctx context.Context,
    classifier sessionclassification.Classifier,
    in sessionclassification.Input,
) session.Classification
~~~

Runtime behavior:

1. start from the current SessionView classification;
2. if no classifier plane exists, return unchanged;
3. call Classifier.Classify with bounded evidence;
4. validate returned Classification;
5. on success, replace only SessionView.Classification;
6. on classifier error or invalid output:
   - record bounded failure;
   - preserve an already-positive current classification;
   - otherwise leave unknown;
   - never fail the user's inference request.

The runner does not inspect client-family names, Jev modes, state-store types, or feature configuration.

## Canonical Execution Integration

### Evidence construction

After secret guard and before frontend-ingress checkpoint:

1. use workingCall.Invocation.ClientUserAgent as the accepted UA;
2. summarize workingCall.Tools by name through the shared sessionclassification tool-evidence helper;
3. reuse workingCall.Invocation.Operation;
4. use ibt.preSession after BeginTurn/A-leg binding;
5. use ibt.workspace already resolved during secure-session preparation.

No message/instruction/item traversal is performed for classification.

### Projection ordering

The classification result updates ibt.preSession before the request enters later generic stages.

All later views built from ibt.preSession therefore observe the same immutable classification:

- submit hook evidence/views;
- tool-catalog CatalogMeta;
- request RequestMeta;
- pre-request Meta;
- route-hint Input;
- later attempt/policy/completion/session views where SessionView is propagated.

This avoids a second store read by each consumer.

### Detached auxiliary calls

prepareSubmitAndALegDetached does not run a new independent classification transaction. A detached child remains unknown unless a future explicit lineage projection requires inherited metadata. It never writes a new coding classification under its private child A-leg.

This prevents proxy-owned Jev/verifier/compression work from masquerading as coding-client traffic.

## Shared Client-Family Facts

### Package

Introduce a small pure root-module package such as internal/agentfacts.

It owns only stable normalization/matching facts, not session classification policy.

Conceptual contract:

~~~go
type Family string

type Match struct {
    Family     Family
    Confidence MatchConfidence
}

func MatchIdentity(candidate string) (Match, bool)
~~~

Rules:

- trim and case-fold;
- bounded exact/prefix/token matches only;
- no regex or fuzzy matching in the hot path;
- generic SDK identifiers such as OpenAI/JS or Anthropic/JS never map to a coding family;
- no prompt scanning in this package.

Initial matcher evidence is revalidated from existing codexclientcompat fixtures and current upstream sources.

### Brownfield reuse

codexclientcompat agent-string matching should delegate to agentfacts where semantics are identical. Feature-specific prompt signatures remain in codexclientcompat because session classification V1 intentionally does not inspect prompt text.

The separately versioned Codex connector may continue its local continuation-family normalization if importing root-internal agentfacts would create connector coupling. This spec prevents a new third root matcher; it does not require a harmful connector-module dependency.

## Standard Feature Implementation

### Feature identity

Standard feature ID:

~~~text
session-classification
~~~

Canonical configuration location:

~~~yaml
plugins:
  features:
    - id: session-classification
      enabled: true
      config:
        mode: heuristic
~~~

Outer Registration.Enabled remains authoritative. The feature config does not introduce a second enabled flag.

### Modes

~~~text
heuristic
jev
hybrid
~~~

Semantics:

- heuristic: local policy may promote; remote adapter is not constructed/called.
- jev: local logic constructs normalized evidence but only the remote decision may promote an unknown session.
- hybrid: local policy runs first; remote classification is eligible only if the session remains unknown.

Default when mode is omitted: heuristic.

### Local heuristic

The V1 local policy has a deliberately small positive surface.

#### Rule A: high-confidence client identity

A high-confidence agentfacts match from the accepted ClientUserAgent promotes to coding_agent.

Example decisive evidence codes are static bounded feature values such as:

~~~text
client_family.codex
client_family.roo
client_family.opencode
client_family.droid
client_family.hermes
client_family.pi
~~~

Only families with current stable identity evidence enter this list. Cline is not promoted from generic SDK UA.

#### Rule B: distinctive coding-tool cluster

A sufficiently distinctive structured tool catalog may promote without workspace markers when all of these are present:

- at least one file_read or file_search category;
- at least one file_edit or file_remove category;
- os_command.

This is stronger evidence than arbitrary technical prose because it describes the client agent's executable capability surface using the existing coding-tool taxonomy.

Decisive evidence code:

~~~text
tooling.distinct_coding_cluster
~~~

#### Rule C: corroborated tool plus project marker

A less distinctive local cluster may promote when both are true:

- read/search plus one local mutation category such as edit/remove/command;
- WorkspaceView.Markers contains a recognized project/build marker.

Built-in markers are bounded exact basenames/suffix classes such as go.mod, go.work, Cargo.toml, pyproject.toml, package.json, pom.xml, build.gradle, composer.json, Gemfile, and solution/project files. Generic .git alone is not decisive.

Decisive evidence code:

~~~text
tooling.project_marker_cluster
~~~

#### Explicit negatives

None of the following promotes alone:

- one filename or extension;
- one tool category;
- web/browser tools;
- model/backend name;
- code fence;
- technical words;
- generic SDK User-Agent.

V1 does not scan messages for those weak signals at all.

### Exclusions

Heuristic config may include a bounded list of ignored_user_agent_prefixes.

Constraints:

- normalized case-insensitive literal prefixes;
- fixed maximum entry count and bytes per entry;
- no regex;
- exclusion is checked before Rule A;
- exclusion never downgrades persisted coding_agent.

Additional positive client identities are not operator-configurable in V1; extending positive identity authority requires tested code/catalog updates.

## Feature State Model

### Authority key

Feature-private key:

~~~go
type ScopeKind string

const (
    ScopeSecureSession ScopeKind = "secure_session"
    ScopeALeg          ScopeKind = "a_leg"
)

type Key struct {
    Kind ScopeKind
    ID   string
}
~~~

Resolution:

1. if SessionView.AuthoritativeSessionID is non-empty, use secure_session plus that ID;
2. else require SessionView.ALegID and use a_leg plus that ID;
3. never use ClientSessionHint as the key.

### Record

Feature-private record:

~~~go
type Record struct {
    Key                  Key
    Classification       session.Classification

    RemoteAttempts       uint32
    RemoteLeaseID        string
    RemoteLeaseUntil     time.Time
    RemoteNextEligibleAt time.Time

    UpdatedAt            time.Time
}
~~~

No UA, prompt, path, tool arguments, transcript, or remote raw response is stored.

### State transitions

~~~mermaid
stateDiagram-v2
    [*] --> Unknown
    Unknown --> Unknown: weak or remote negative evidence
    Unknown --> Unknown: remote lease and attempt state
    Unknown --> CodingAgent: accepted positive proposal
    CodingAgent --> CodingAgent: every later turn
~~~

Classification revision advances only on a classification transition. In V1 an authoritative key therefore normally moves from revision 0 to revision 1 once.

The first accepted positive proposal wins. Later proposals do not rewrite source/evidence merely because another detector agrees.

## Store Contract

The feature implementation defines a narrow consumed interface. Concrete memory/Bun implementations are composed by standard featurehost.

Conceptual operations:

~~~go
type Store interface {
    Load(ctx context.Context, key Key) (Record, bool, error)

    Promote(
        ctx context.Context,
        key Key,
        proposal session.Classification,
        now time.Time,
    ) (record Record, promoted bool, err error)

    ClaimRemote(
        ctx context.Context,
        key Key,
        now time.Time,
        maxAttempts uint32,
        leaseTTL time.Duration,
        retryBackoff time.Duration,
    ) (claim RemoteClaim, record Record, ok bool, err error)

    CompleteRemote(
        ctx context.Context,
        claim RemoteClaim,
        result RemoteCompletion,
        now time.Time,
    ) (Record, error)
}
~~~

Required semantics:

- Promote is atomic compare-and-promote;
- a positive existing record wins over a new proposal;
- ClaimRemote returns false if coding_agent, active lease, attempt budget exhausted, or backoff not elapsed;
- claim increments/records the attempt before network I/O;
- lease ID proves ownership of CompleteRemote;
- expired lease can be reclaimed subject to attempt budget;
- positive remote completion can promote atomically;
- below-threshold/error completion clears/ages the lease but does not write a negative classification.

## Process Cache and Coordinator

Standard featurehost owns one process resource that wraps Store.

Responsibilities:

- positive classification cache;
- keyed coalescing of concurrent first loads/local promotion;
- memory-store implementation for non-durable deployments;
- remote-claim coordination;
- bounded cache capacity and idle eviction;
- metrics hooks.

Hot-path rules:

- warm coding_agent returns from cache without DB/network;
- unknown sessions may perform a bounded indexed durable Load so a promotion from another replica is not masked by a stale negative cache;
- concurrent same-key loads are coalesced;
- unrelated keys do not wait on one long-held global lock;
- network calls happen after store claim and outside locks/transactions;
- no per-session goroutine/timer.

A small negative cache may be used only if implementation proves it cannot hide an authoritative positive transition from a later turn under the required consistency contract. The baseline design does not require one.

## Durable Persistence

### Memory mode

When no shared Bun DB is available, featurehost uses a bounded in-memory store/coordinator. It provides process/A-leg semantics and makes no restart-durability claim.

### Bun mode

When the standard featurehost has the same shared Bun DB available for durable proxy state, it ensures a small feature-owned table and uses it for classification.

Logical schema:

~~~text
session_classification
  scope_kind
  scope_id
  kind
  source
  confidence
  evidence_code
  classification_revision
  remote_attempts
  remote_lease_id
  remote_lease_until
  remote_next_eligible_at
  updated_at

primary key scope_kind plus scope_id
~~~

Properties:

- dialect-neutral Bun implementation;
- SQLite/PostgreSQL logical parity;
- no credentials/content columns;
- indexed primary-key lookup only on request path;
- atomic promotion/claim transactions;
- schema/migration registration in the repository DB-parity catalog where applicable.

The feature table remains separate from secure-session PolicyMetadata and B2BUA rows.

## Optional Remote Classifier

### Provider-neutral port

The feature policy depends on:

~~~go
type RemoteDecider interface {
    Decide(ctx context.Context, in RemoteInput) (RemoteDecision, error)
}
~~~

RemoteInput is feature-private and derived after local normalization:

~~~go
type RemoteInput struct {
    Operation          lipapi.Operation
    ClientFamily       string
    HasAmbiguousClient bool
    ToolCategories     sessionclassification.ToolCategorySet
    WorkspaceClass     string
    LocalEvidenceCode  string
}
~~~

No raw UA is required by the remote contract.

RemoteDecision contains bounded numeric/enum facts sufficient for configured thresholding, for example coding probability/confidence. Exact TypeSafe response DTOs do not cross this interface.

### Jev adapter

A TypeSafe/Jev adapter is constructed only for jev/hybrid modes.

Implementation-time rule:

- consult current early-access TypeSafe API documentation;
- map current vendor request/response/auth to RemoteDecider;
- do not widen public SDK if vendor details change;
- use bounded body size and strict decode;
- reject redirects/origins inconsistent with the configured adapter posture;
- never log bearer credentials or raw vendor payloads.

Live TypeSafe access is not required for default tests.

### Remote configuration

Remote mode requires explicit fields rather than relying on unmeasured product defaults:

~~~yaml
plugins:
  features:
    - id: session-classification
      enabled: true
      config:
        mode: hybrid
        heuristic:
          ignored_user_agent_prefixes: []
        remote:
          provider: jev
          api_key_env: TYPESAFE_API_KEY
          timeout: 750ms
          max_attempts_per_session: 1
          lease_ttl: 2s
          retry_backoff: 0s
          positive_threshold: 0.90
~~~

Values above are examples, not frozen defaults. For remote-enabled mode, validation requires explicit operational values and enforces safe finite bounds. lease_ttl must exceed timeout with safety margin.

### Remote flow

~~~mermaid
sequenceDiagram
    participant R as Runtime stage
    participant C as Classifier
    participant S as State store
    participant J as Remote decider

    R->>C: classify bounded input
    C->>S: load authoritative key
    S-->>C: unknown
    C->>C: local evaluation
    C->>S: claim remote lease
    S-->>C: lease granted
    C->>J: derived evidence only
    J-->>C: typed decision
    C->>S: complete lease and maybe promote
    S-->>C: current record
    C-->>R: classification snapshot
~~~

Timeout/error follows the same sequence through CompleteRemote with no promotion.

## Large-Payload Wire Integration

### Proof evidence

Add the bounded sessionclassification.Evidence value to provider-neutral large-body proof/session facts.

Certified frontend proof compilers:

1. read only User-Agent from ProofInput.Headers through the same acceptance helper used by canonical frontend decoding;
2. observe tool names while validating/scanning the already-supported tool structures;
3. feed tool names into the shared fixed-bit evidence accumulator;
4. set Operation from existing proof facts;
5. store no raw header map or tool list in Proof.

Proof Validate and AggregateFactBytes account for ClientUserAgent length and fixed evidence size.

### Wire execution ordering

After accepted one-way wire commit, ExecuteLargeBody already performs secure-session/A-leg preparation from bounded facts. The classifier stage is inserted after authority/workspace binding and before route/request consumers in the wire path.

The wire path calls the same generic session-classification stage with:

- bound SessionView;
- resolved WorkspaceView;
- proof ClassificationEvidence.

If the plane is absent, this is a no-op.

If classifier returns unknown/error, wire execution continues. It never asks for canonical fallback post-commit.

### Plane eligibility

PlaneSessionClassifier is RequestBodyMetadataOnly. The large-body frozen plane census therefore records occupancy without setting a canonical-required blocker.

Architecture tests must prove:

- new plane count/order is current;
- its declared access remains MetadataOnly;
- its classifier Input cannot contain lipapi.Call, []ToolDef, message/item trees, raw headers, arbitrary maps, or request payload bytes;
- canonical and wire evidence/outcome are differential-equal.

## Configuration and Generation Lifecycle

### Decode ownership

internal/plugins/features/sessionclassification owns:

- YAML schema;
- defaults;
- bounds;
- mode validation;
- heuristic exclusion configuration;
- remote configuration validation;
- local policy.

Generic core config receives no new semantic section.

### Standard feature registration

internal/standardplugins registers session-classification like other feature factories.

The basic feature factory validates/decodes the opaque feature config. Standard featurehost re-decodes or consumes the registration during generation composition, creates the overlap-safe store-initialization lifecycle for enabled generations, builds the concrete classifier against the shared StateHolder, and contributes the classifier to PlaneSessionClassifier as a generation-bound value.

### Process ownership

featurehost process construction owns a lightweight classification StateHolder/coordinator shell and the feature metrics collector registration. Constructing the shell performs no feature schema migration, remote call, or classification-state mutation.

When an enabled generation is compiled, featurehost attaches an overlap-safe feature lifecycle to that generation. Lifecycle Start runs in the existing candidate resource-ledger prepare phase and:

- initializes the memory store or ensures/opens the Bun-backed feature store exactly once through the shared StateHolder;
- may fail the candidate before publication if required store setup is unavailable;
- does not create classification rows, call Jev, or alter an already-published classifier.

Lifecycle Stop releases only the generation's use/reference; it does not destroy shared process classification state while other generations may still use it. Final process Close disposes the holder/cache once. Borrowed DB/HTTP infrastructure is never double-closed.

Consequences:

- a deployment that never enables session-classification acquires no classification schema/network dependency;
- reload can enable the feature later without process restart;
- overlapping old/new enabled generations safely share the same monotonic store/cache;
- a rejected candidate cannot publish or mutate classification records, although an idempotent feature table created during successful prepare may remain as inert infrastructure.

### Reload

- new generation gets new immutable heuristic/remote policy;
- in-flight request keeps old classifier object;
- process state store/cache is shared across generations;
- disabling feature withdraws the plane but does not delete state;
- re-enabling can project existing durable positive classification.

Candidate generation failure does not mutate process state or replace the last-good generation.

## Observability

Feature-owned metrics use bounded labels only.

Suggested counters/histogram:

~~~text
session_classification_evaluations_total
  mode
  outcome

session_classification_transitions_total
  source
  confidence
  evidence

session_classification_remote_total
  outcome

session_classification_remote_seconds
  outcome

session_classification_store_total
  operation
  outcome
~~~

Allowed label values are closed enums/static evidence codes. Never labels:

- SessionID/A-leg;
- User-Agent;
- path/filename;
- prompt;
- client metadata values;
- remote response strings.

Structured debug/control observations may include the same bounded classification snapshot plus trace correlation through existing tracing/exemplar mechanisms, without copying user content.

## Error Handling

| Failure | Behavior |
| --- | --- |
| Invalid feature config | reject candidate generation |
| Duplicate classifier plane | reject generation composition |
| Invalid classifier output | bounded diagnostic, preserve prior positive else unknown |
| Memory/Bun Load failure | preserve prior cached positive else unknown; request continues |
| Promote conflict | reload winner and return current positive |
| Jev credential missing in remote mode | reject candidate generation |
| Jev timeout/429/5xx/network error | complete/expire attempt state, unknown, request continues |
| Jev malformed/oversized response | remote error, unknown, request continues |
| Remote lease already active | skip duplicate remote call, return current state |
| Remote lease abandoned | reclaim only after expiry and within attempt budget |
| Large-body evidence insufficient | unknown; do not force canonical solely for classification |
| Post-wire-commit classification error | unknown; continue one committed execution |
| Context cancellation | stop remote call promptly; ordinary request cancellation remains authoritative |

Classification is advisory, so runtime evaluation errors fail open to generic behavior. Configuration errors remain fail-before-serve.

## Security and Privacy

### Trust boundaries

- ClientUserAgent is untrusted evidence even after syntax validation.
- ClientSessionHint is never state authority.
- Workspace markers are evidence, not workspace ownership proof.
- Remote decision is evidence, not authorization.
- Positive coding_agent never grants tool permissions.

### Remote egress

Default Jev payload contains derived facts only. The adapter must not include:

- bearer/API credentials other than its own authorization header;
- upstream provider credentials;
- session resume tokens;
- raw client headers;
- prompt/transcript/reasoning;
- tool arguments;
- filesystem paths;
- secret-guard findings with raw secret data.

### Persistence

The classification table contains only bounded enums/codes/control timestamps/lease identifiers. It does not retain the evidence body that produced the decision.

## Performance and Concurrency

### Local path

Warm positive:

~~~text
process cache lookup
-> SessionView projection
~~~

Unknown:

~~~text
authoritative key
-> bounded state load or coalesced in-flight load
-> fixed UA matcher
-> fixed tool bit tests
-> bounded workspace marker scan
-> optional atomic promotion
~~~

No transcript/message scan.

### Remote path

Only unknown sessions in jev/hybrid mode can reach it.

- atomic claim before network;
- finite attempts;
- one active lease per authoritative key/store scope;
- context-bound timeout;
- no DB transaction held during HTTP;
- no unrelated-session global lock;
- no per-session goroutine.

### Memory bounds

Process coordinator has:

- configurable/internal maximum entry count;
- idle eviction;
- positive values re-loadable from durable store;
- no key from raw client hint.

The design intentionally accepts one indexed durable Load on an unknown turn in durable multi-instance mode rather than hide a positive promotion made by another replica behind a long-lived negative cache.

## Testing Strategy

### Unit tests

- Classification zero/positive validation and IsCodingAgent.
- Tool evidence bit accumulator.
- agentfacts high-confidence matches and ambiguous negatives.
- heuristic Rule A/B/C and exclusions.
- config mode/bounds validation.
- authority-key resolution.
- state Promote idempotency.
- remote lease claim/expiry/budget/complete.
- no persisted raw evidence.

### Brownfield fixture matrix

Reuse or adapt existing cross-agent fixtures from:

- tool-call-classification;
- compaction-event-detection;
- codexclientcompat;
- explicit-completion research.

Positive coverage is evidence-based rather than requiring every named harness to have a stable UA. Cline's generic SDK UA is an explicit negative unless its tool/workspace evidence becomes decisive.

### Canonical runtime tests

Pin exact order:

~~~text
BeginTurn
A-leg bind
secret guard
session classification
frontend ingress
submit
tool catalog
request shaping
pre request
route hint
~~~

Prove first-turn classification appears in all later SessionView-bearing metadata.

### Large-body differential tests

For OpenAI Responses, OpenAI Chat, and OpenResponses certified profiles where applicable:

- same UA/tool names -> same Evidence;
- same workspace/session facts -> same classification;
- absent/ambiguous evidence -> same unknown;
- occupied classifier plane does not create static canonical blocker;
- no shadow Call/raw headers/tool lists in wire DTOs;
- post-commit failure does not fall back.

### Persistence parity

Contract suite for memory, SQLite, PostgreSQL:

- Load missing;
- atomic first promotion;
- concurrent promotion;
- remote claim single winner;
- lease expiry;
- max attempts;
- positive beats later remote completion;
- reopen/restart restoration;
- same client hint/different authority isolation.

Register the durable component in dbparity when required by repository policy.

### Remote adapter tests

Hermetic httptest/fake adapter:

- success/threshold;
- timeout;
- 429;
- 5xx;
- redirect policy;
- malformed JSON;
- oversized body;
- missing fields;
- cancellation;
- credential redaction;
- derived-evidence-only request shape.

Optional live TypeSafe sentinel is explicit/tagged and never part of default unit tests.

### Architecture and QA

- plane generator/currency;
- plane census/request access;
- feature/core import guards;
- generic runtime has no concrete coding-client switch;
- no TypeSafe DTO outside adapter;
- large-body no-smuggling reflection tests;
- default test cost;
- race tests for coordinator/store;
- make parity-checks;
- make test-db-parity;
- make qa for final wide certification.

## File and Ownership Plan

### New public contracts

- pkg/lipsdk/session/classification.go
  - Kind, ClassificationSource, ConfidenceBand, EvidenceCode, Classification.
- pkg/lipsdk/sessionclassification/types.go
  - Evidence, ToolCategorySet, Input.
- pkg/lipsdk/sessionclassification/classifier.go
  - Classifier contract and pure evidence helpers.

### New shared internal facts

- internal/agentfacts/
  - pure bounded client-family identity matcher used by root-module consumers.

### New feature implementation

- internal/plugins/features/sessionclassification/
  - config.go / yaml.go;
  - heuristic.go;
  - classifier.go;
  - state contract/types;
  - remote decision contract;
  - feature constants/evidence codes;
  - tests.

### New standard featurehost child

- internal/standardplugins/featurehost/sessionclassification/
  - memory store;
  - Bun store/schema;
  - cache/coordinator;
  - Jev adapter/client;
  - composition helpers;
  - Prometheus collector;
  - store contract tests.

Exact file split may be adjusted to keep packages cohesive; ownership boundaries above are normative.

### Existing platform surfaces to modify during implementation

- pkg/lipsdk/session/view.go and clone/context tests.
- pkg/lipsdk/feature/stages.go.
- pkg/lipsdk/feature/plane_manifest.go plus generated plane outputs.
- internal/core/extensions snapshot/stage execution.
- internal/core/runtime secure canonical preparation.
- internal/core/runtime ExecuteLargeBody wire preparation.
- internal/core/execctx/session view propagation where required.
- internal/core/largebody proof/wire facts and fixed plane census.
- certified frontend profile proof compilers.
- internal/plugins/features/codexclientcompat agent-string matching.
- internal/standardplugins feature registration and featurehost process/generation composition.
- internal/testkit/dbparity catalog if the persistent component is required to register.
- docs/session-classification.md and operator/config examples.

## Requirement Traceability

| Requirement | Primary design components |
| --- | --- |
| 1 | Session Classification projection, Classifier plane, consumer projection |
| 2 | Authority Key, Store, cache/coordinator, durable state |
| 3 | agentfacts, ToolCategorySet, heuristic rules, exclusions |
| 4 | new legal stage, generic runner, canonical/wire ordering |
| 5 | bounded Evidence, Proof/WireSessionFacts, MetadataOnly plane, differential tests |
| 6 | RemoteDecider, lease state, Jev adapter |
| 7 | derived remote payload, bounded store schema, trust boundaries |
| 8 | feature-owned config, standard registration, featurehost generation/reload |
| 9 | feature metrics and bounded classification snapshot |
| 10 | cache/coordinator, state access policy, no per-session workers |
| 11 | shared matcher/tool taxonomy, featurehost ownership, no core client switches |
| 12 | fixture matrix, large-body differential, dbparity, architecture/QA gates |

## Brownfield Design Validation Result

The design review found and repaired five high-impact risks:

1. **Wrong stage placement** — repaired with a dedicated post-secret-guard/pre-submit stage.
2. **False MetadataOnly claim** — repaired by defining one bounded Evidence DTO used identically by canonical and wire paths.
3. **Multi-instance remote duplication** — repaired with atomic durable remote leases and finite attempts.
4. **SessionView evidence alias/copy growth** — repaired by storing one scalar decisive EvidenceCode instead of an arbitrary evidence slice.
5. **Disabled-feature startup coupling** — repaired with a lightweight process holder plus an enabled-generation overlap-safe initialization lifecycle, so a never-enabled classifier does not create a classification schema or network dependency.

With these repairs, the design is implementation-ready and preserves current architecture invariants.
