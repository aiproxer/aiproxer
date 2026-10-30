# Session Classification

`session-classification` adds one narrow, provider-neutral fact to a logical
session: **has this session been positively identified as a coding-agent
session?** That single fact is derived metadata. It is deliberately conservative,
monotonic, and advisory, and it exists so coding-specific behavior can be gated
later without every consumer re-implementing client detection.

The feature is off unless an operator enables it. When it is disabled or absent,
the proxy performs no classification-specific work, makes no external call, and
exposes no positive classification.

## Scope: advisory metadata, not authorization

**Classification is not authorization.** A `coding_agent` result is derived
metadata, and nothing more. It must not be, and does not, do any of the
following on its own:

- authorize a tool or grant a permission;
- establish or imply identity, ownership, or tenancy;
- alter billing entitlement or accounting treatment;
- override routing authority, backend selection, or model selection.

It is equally not a substitute for authentication, authorization, workspace
ownership proof, secure-session resume proof, destructive-tool policy, or
billing identity. Any consumer that needs one of those must keep enforcing its
own check; a positive classification is not that check.

**Existing features do not become gated automatically.** When this infrastructure
first lands, the bundled coding-oriented features keep exactly the behavior they
had before it. Nothing in this change switches an existing feature onto
classification gating. A bundled feature becomes classification-aware only when
its own implementation explicitly opts in. Enabling `session-classification`
therefore changes what is *known*, not what any existing consumer *does*.

## Classification semantics

The V1 vocabulary has two members and no third:

| Kind | Meaning |
|---|---|
| `unknown` | The all-zero classification value `session.Classification{}`. It is the zero value of the kind, meaning absence of positive evidence. |
| `coding_agent` | Bounded positive evidence was accepted for this logical session. |

There is no `not_coding` state, and none is persisted.
Absence of evidence is never a negative classification: an ordinary chat turn with
no coding evidence is `unknown`, which means *unclassified*, not *known not to be
a coding agent*.

A positive snapshot is bounded typed state — kind, source, confidence band,
decisive evidence code, and revision:

- **Kind** — `coding_agent`.
- **Source** — one of `local_identity`, `local_tooling`, `remote_classifier`.
- **Confidence band** — `high`. It is the only positive band in V1.
- **Evidence** — one opaque bounded code from the closed vocabulary below.
  Codes are at most `MaxEvidenceCodeBytes` (64) bytes and use only ASCII letters,
  digits, dots, underscores, and hyphens.
- **Revision** — the monotonic snapshot revision the store assigned. The first
  accepted positive for an authoritative key normally moves revision 0 to
  revision 1 once.

Monotonicity is the load-bearing guarantee: once a session is `coding_agent`, it
is **never downgraded**. Later weak, absent, contradictory, excluded, or
remote-negative evidence leaves the accepted positive untouched, and a replayed
identical positive is idempotent rather than a second transition. The first
accepted positive wins — a later detector agreeing does not rewrite the recorded
source or evidence code.

Same-turn visibility: on the canonical lane the `session_classification`
extension stage runs after secret-guard processing and before frontend ingress,
request authority, submit, tool-catalog, request-shaping, pre-request, and
route-hint consumers, so a decisive first turn is visible to all of them on that
same turn. A validated result is projected onto the turn's session view as one
immutable classification.

## Authoritative session keying

Classification state is scoped to proxy-owned authority only. The key is
resolved in exactly this order:

1. If the session view's `AuthoritativeSessionID` is non-empty, the key is scope
   kind `secure_session` plus that identifier.
2. Otherwise, if the view has a proxy-owned `ALegID`, the key is scope kind
   `a_leg` plus that identifier.
3. Otherwise there is no authority: the turn stays `unknown`.

**ClientSessionHint is never a state key.** The client-controlled session hint is
not read by key resolution at all, so two clients that supply the same hint
without sharing proxy-owned session or A-leg authority keep entirely isolated
classification state. The scope kind is part of the key, so identical identifier
bytes issued by different authorities never share state.

Authority identifiers are treated as opaque: they are never normalized, trimmed,
or rewritten. A key is rejected (`ErrInvalidKey`) when it is empty, longer than
`MaxAuthorityIDBytes` (256) bytes, not valid UTF-8, blank after trimming, or
contains a control character. A rejected key is not a failure of the user
request; the turn simply stays `unknown`.

Because state follows the authoritative key, a routing, backend, model, retry,
race, failover, compaction, or provider-continuity change inside the same
logical session does not change the classification, and a new unrelated session
or A-leg starts `unknown` rather than inheriting another session's result.

## Local evidence rules

The local policy reads only bounded current-turn metadata: the canonical
operation, the already-accepted client `User-Agent`, the canonical tool-category
presence bitset, and the bounded workspace-marker window. It never reads a
transcript, a message, an instruction, tool definitions, tool arguments, a model
name, a backend name, or a route selector.

Tool categories come from the canonical tool-name taxonomy — the classifier
reuses the shared category classifier and never keeps the tool name itself, only
the presence bit.

### Rule A — high-confidence client identity

Promotes when the accepted client `User-Agent` matches a stable high-confidence
coding-harness identity rule from the shared identity catalog. Source is
`local_identity`. Only families with current stable identity evidence enter the
closed list; a generic or underlying-SDK `User-Agent` never matches. The
decisive evidence codes are:

| Family | Evidence code |
|---|---|
| Codex | `client_family.codex` |
| Roo | `client_family.roo` |
| OpenCode | `client_family.opencode` |
| Pi | `client_family.pi` |
| Droid | `client_family.droid` |
| Hermes | `client_family.hermes` |

Positive client identities are **not** operator-configurable. Extending positive
identity authority requires a reviewed code and catalog change.

### Rule B — distinctive coding-tool cluster

Promotes without any workspace marker when the turn's tool categories contain
all three of:

- at least one file-read or file-search category;
- at least one file-edit or file-remove category;
- an OS-command category.

Source is `local_tooling`; the decisive evidence code is
`tooling.distinct_coding_cluster`. This is stronger than technical prose because
it describes the client's executable capability surface.

### Rule C — corroborated tool plus project marker

Promotes when the tool categories contain read/search plus exactly one local
mutation category (file-edit, file-remove, or OS-command) **and** a recognized
project marker is present. Source is `local_tooling`; the decisive evidence code
is `tooling.project_marker_cluster`.

The marker window is bounded: at most `MaxWorkspaceMarkers` (32) markers are
inspected per evaluation, each at most `MaxWorkspaceMarkerBytes` (128) bytes, and
a marker must be a base name — it may not contain a path separator or a control
character. Recognized markers are matched case-insensitively and are the
basenames

`go.mod`, `go.work`, `cargo.toml`, `pyproject.toml`, `package.json`, `pom.xml`,
`build.gradle`, `build.gradle.kts`, `composer.json`, `gemfile`

plus a marker ending in `.sln`, `.slnx`, `.csproj`, `.fsproj`, `.vbproj`,
`.proj`, `.vcxproj`, `.xcodeproj`, or `.xcworkspace`. A bare VCS directory is not
decisive on its own.

### Explicit negatives

None of these promotes a session, alone or in any combination the rules above do
not already cover:

- one generic shell, browser, web, read, or search tool;
- a source filename or file extension;
- a Markdown code fence;
- a programming-language name;
- prose words such as code, bug, or repository;
- a model name, or a backend or route identity commonly used by coding agents;
- a generic, unknown, or underlying-SDK client `User-Agent`.

The classifier does not scan for these weak signals at all, so ordinary
technical chat stays `unknown` without any prompt-text inference.

### Exclusions

`config.heuristic.ignored_user_agent_prefixes` is a bounded, operator-supplied
list of literal prefixes. The list holds at most `MaxIgnoredUserAgentPrefixes`
(16) entries, each entry at most `MaxIgnoredUserAgentPrefixBytes` (128) bytes,
and entries are matched case-insensitively after lowercasing and trimming. There
is no regular-expression support. Duplicate entries are dropped at decode time.

Two properties matter operationally:

- The exclusion is checked before Rule A, and only around Rule A. It suppresses
  a prospective high-confidence client-identity match. It does **not** withdraw
  Rule B or Rule C: a turn whose client `User-Agent` matches an ignored prefix
  but which exposes a distinctive coding-tool cluster is still promoted.
  Exclusions scope identity evidence, not tooling evidence.
- An exclusion never revokes an already-established `coding_agent`. A turn that
  is already positive short-circuits before any exclusion is considered. And a
  high-confidence identity that the exclusion suppressed is never forwarded to an
  external classifier, so an exclusion also ends that turn's remote work in the
  remote modes.

## Configuration

The feature's semantic configuration lives entirely under its canonical feature
registration in `plugins.features`. There is no new generic core configuration
section, and the feature config introduces no second enablement flag — the outer
`enabled:` entry stays authoritative. An absent entry, or an entry with
`enabled: false`, leaves the feature absent: no classifier plane, no state, and
no schema or network dependency.

```yaml
plugins:
  features:
    - id: session-classification
      enabled: true
      config:
        mode: heuristic
```

### Keys

| Key | Type | Meaning |
|---|---|---|
| `config.mode` | string | `heuristic` (default), `jev`, or `hybrid`. An omitted or empty value is `heuristic`. |
| `config.heuristic.ignored_user_agent_prefixes` | sequence of strings | Bounded literal client-identity prefixes that suppress Rule A only. |
| `config.remote.provider` | string | Must be `jev`. |
| `config.remote.api_key_env` | string | Name of the environment variable holding the credential. A reference only. |
| `config.remote.timeout` | duration | Hard per-attempt timeout over the whole exchange. |
| `config.remote.max_attempts_per_session` | integer | Finite per-session remote attempt budget. |
| `config.remote.lease_ttl` | duration | Lifetime of the shared remote-decision lease. |
| `config.remote.retry_backoff` | duration | Delay between two attempts of one turn. |
| `config.remote.positive_threshold` | number | The only threshold input for a remote promotion. |

An unknown key, a duplicate key, or an extra field anywhere under `config`,
`config.heuristic`, or `config.remote` is rejected. Remote settings require
*every* remote field to be present explicitly; there are no measured product
defaults for them.

### Bounds

| Bound | Value |
|---|---|
| `MaxIgnoredUserAgentPrefixes` | 16 entries |
| `MaxIgnoredUserAgentPrefixBytes` | 128 bytes per entry |
| `MaxWorkspaceMarkers` | 32 markers per evaluation |
| `MaxWorkspaceMarkerBytes` | 128 bytes per marker |
| `MinRemoteTimeout` | 1ms |
| `MaxRemoteTimeout` | 30s |
| `MaxRemoteAttemptsPerSession` | 5 |
| `MaxRemoteLeaseTTL` | 2m |
| `MaxRemoteRetryBackoff` | 30s |
| `RemoteLeaseSafetyMargin` | 100ms |
| `MaxAuthorityIDBytes` | 256 bytes (authority identifier) |
| `MaxRemoteLeaseIDBytes` | 64 bytes (remote lease token) |
| `MaxEvidenceCodeBytes` | 64 bytes |

Consequences an operator should plan for:

- `config.remote.timeout` must be at least `MinRemoteTimeout` and at most
  `MaxRemoteTimeout`.
- `config.remote.max_attempts_per_session` must be between 1 and
  `MaxRemoteAttemptsPerSession`.
- `config.remote.lease_ttl` must be at most `MaxRemoteLeaseTTL` **and** strictly
  greater than `timeout + RemoteLeaseSafetyMargin`, so a lease cannot expire
  before its own hard timeout does.
- `config.remote.retry_backoff` must be between 0 and `MaxRemoteRetryBackoff`.
- `config.remote.positive_threshold` must be greater than zero and at most one.
  A threshold of zero or below, a threshold above one, or a non-number is refused.
- `config.remote.api_key_env` must name an environment variable — a
  bounded identifier. A credential value is never accepted in configuration.
- For `mode: jev` or `mode: hybrid`, the named environment variable must
  **currently resolve to a non-blank value in the process that compiles the
  generation**. An absent or blank reference rejects the candidate generation,
  the same as any other unservable setting.

An unservable configuration is rejected when the candidate generation is
compiled, so a bad mode, impossible threshold, non-positive timeout, invalid
attempt or lease bound, contradictory local/remote setting, or unbounded matcher
data fails **before** publication rather than serving a partially configured
mode.

Credential resolution is checked at publication and again per request. The value
is never stored in the adapter: only the variable's name is retained, and the
value is read afresh on each attempt. A variable that is unset or blanked *after*
publication therefore degrades to a bounded per-turn refusal with no request
made — it does not produce egress with an empty `Authorization` header. Restart
or reload the proxy after rotating the credential in the environment.

### Canonical examples

Two operator examples ship in `config/examples`. Both are parsed by the
configuration example suite and their feature config is decoded by the shipped
feature validation on every run, so an invalid key or an out-of-bounds value in a
published example fails the build:

- `config/examples/session-classification-heuristic.yaml` — deterministic local
  classification, default mode, no remote block.
- `config/examples/session-classification-remote-jev.yaml` — an explicit remote
  mode with every required remote field and an environment-variable *name* as the
  credential reference.

Neither example contains a credential. A credential is supplied only through the
referenced environment variable at call time.

## Remote (Jev) modes

Remote classification is a provider-neutral port. The feature policy depends only
on that port and its bounded input and decision, so the vendor HTTP client lives
entirely inside the adapter package that implements it. The remote decision is
**off unless explicitly configured**, and enabling it requires an explicit
provider and an explicit credential *reference* — the
environment-variable name — never an embedded credential.

In `heuristic` mode the classifier makes zero egress: no remote posture is
validated, no credential is referenced, and no HTTP client is constructed at all.

| Mode | What it constructs and calls |
|---|---|
| `heuristic` | Only deterministic local rules may promote. No remote posture is validated, no credential is referenced, no client is constructed, and the configuration loader refuses a `remote` block. **Zero egress.** |
| `jev` | Local rules still build the bounded evidence document, but only the configured remote decision may promote an unknown session. Every still-unknown turn is remote-eligible. |
| `hybrid` | Decisive local evidence promotes first. Only sessions still `unknown` after local evaluation claim a lease and make a remote call. |

`hybrid` is the useful choice when an operator wants local evidence to carry the
common case and remote evidence to resolve only the ambiguous remainder. `jev` is
the strict posture: nothing local ever promotes.

Only one remote call shape is made, and it is fully bounded:

- endpoint `https://api.typesafe.ai/v1/systemone`;
- model alias `jev-latest`, pinned by the adapter and not operator-configurable;
- `Content-Type: application/json` and `Authorization: Bearer` with the value
  resolved from the referenced environment variable at call time;
- one `noul` (yes/no) question with a fixed bounded instruction text that
  interpolates no turn content;
- the derived state document carries only closed-vocabulary members:
  `operation`, `client_family`, `has_ambiguous_client`, `tool_categories`,
  `workspace_class`, `local_evidence_code`;
- the request document is refused above 8 KiB and the response is read at most
  64 KiB;
- redirects are never followed, so the bearer header can never be forwarded to
  another origin.

Response mapping: the reported `noul` number becomes the coding probability. A
`noul` answer carries **no** confidence value, so the decision's confidence stays
zero, and promotion is decided by `positive_threshold` alone against the
probability. No confidence-style value is invented for a number the service did
not send. A valid result at or above `positive_threshold` may promote the session;
a below-threshold result leaves the session `unknown` and writes no durable
negative classification.

## Fail-open behavior

Classification never rejects a user request. The stage returns nothing, so an
absent plane is a no-op, and any evaluation, state, or remote failure can only
leave the session conservatively `unknown` while an already-positive
classification is preserved. The stage fails open in every one of those cases.

Every bounded remote failure degrades to `unknown` and the request proceeds:

| Failure | Outcome |
|---|---|
| Hard per-attempt timeout firing | `timeout` |
| `401` / `403` | credential failure, `skipped` |
| `422` | `skipped` |
| `429` | `rate_limited` |
| Any `5xx`, including `529` | `server_error` |
| A `3xx` redirect response | refused; the request is never re-sent |
| Malformed, missing documented member, wrong answer type, unparsable probability | `malformed` |
| Response above the read bound | `malformed` |
| Dial, TLS, or read failure | `network_error` |
| Caller cancellation | cancellation reported to the caller |
| A valid remote result below the configured threshold | `below_threshold`; the session stays `unknown` |

Only `200` is treated as success. Derived evidence outside the bounded contract
is refused before any egress happens, so an unrecognised fact is never sent.

Retry and concurrency bounds:

- Only a transient transport, rate-limit, server, or timeout failure is retried.
  A malformed or oversized result is a determined fact about the response, so
  repeating the identical request cannot repair it and does not spend budget.
- Retries happen only inside the finite per-session attempt budget
  (`max_attempts_per_session`, capped by `MaxRemoteAttemptsPerSession`).
- At most one active remote lease exists per authoritative session. A busy lease
  (`lease_busy`), a spent budget (`budget_exhausted`), or a completion backoff
  that has not yet elapsed ends this turn's remote work rather than spinning.
- If a process dies holding a lease, the lease expires on its own
  (`lease_ttl`), so a later eligible attempt can proceed within the finite
  attempt budget.
- A session that is already `coding_agent` makes no further remote call at all.

The one condition reported as an error rather than silently absorbed is a
durable-state failure — process-owned classification state could not be read or
written — together with a canceled caller context. Even then the conservative
`unknown` (or preserved positive) projection is returned with the error, and the
generic stage runner discards it without failing the request.

## Observability: bounded metrics and diagnostics

The feature exports six bounded metric families through the process-owned
collector that standard featurehost registers with the generic metrics registry.
Registration lifetime belongs to the metrics infrastructure; the collector belongs
to this feature because the feature owns the closed vocabulary.

Two properties keep the surface safe to scrape at any cardinality:

- only observed label combinations are exported, so a deployment that never
  classifies publishes no classification series at all;
- every observation is validated against the closed vocabularies before it is
  counted, and a rejected observation is dropped rather than exported. Rejected
  observations are counted in-process only, and the rejected value itself is never
  retained, so it cannot be published later.

| Metric | Type | Labels | Emitted by |
|---|---|---|---|
| `aip_session_classification_store_ready` | gauge | — no labels | `setStoreReady`: 1 while the process-owned classification store is initialized, 0 once it is released. |
| `aip_session_classification_evaluations_total` | counter | `mode`, `outcome` | `ObserveEvaluation`: once per bounded evaluation of a turn. |
| `aip_session_classification_transitions_total` | counter | `source`, `confidence`, `evidence` | `ObserveTransition`: once for the turn that established the first accepted positive. |
| `aip_session_classification_remote_total` | counter | `outcome` | `ObserveRemote`: once per remote attempt, including one skipped at the lease, beside its latency sample. |
| `aip_session_classification_remote_seconds` | histogram | `outcome` | `ObserveRemote`: the bounded latency of that same attempt, on fixed buckets from 5ms to 20.48s. |
| `aip_session_classification_store_total` | counter | `operation`, `outcome` | `ObserveStore`: once per durable classification-state operation. |

Every label value is a static identifier drawn from a closed enumeration. The
following are never a label:

- a session identifier such as `SessionID`, or an A-leg identifier such as
  `ALegID`;
- a raw `User-Agent`, whole or reduced;
- a filename or a path, absolute or relative;
- a prompt, a transcript excerpt, or any tool argument;
- an arbitrary client metadata value the classifier merely observed;
- an arbitrary vendor result string taken from a remote decision;
- a classification revision, which is unbounded and would therefore create
  unbounded cardinality.

The shipped labels honour that list: `mode`, `outcome`, `source`, `confidence`,
`evidence`, and `operation` are all members of closed vocabularies, and
`aip_session_classification_store_ready` carries none at all.

| Label | Metric | Closed values |
|---|---|---|
| `mode` | `aip_session_classification_evaluations_total` | `heuristic`, `hybrid`, `jev` |
| `outcome` | `aip_session_classification_evaluations_total` | `excluded`, `no_authority`, `preserved`, `promoted`, `remote_error`, `remote_skipped`, `remote_timeout`, `restored`, `state_unavailable`, `unknown` |
| `source` | `aip_session_classification_transitions_total` | `local_identity`, `local_tooling`, `remote_classifier` |
| `confidence` | `aip_session_classification_transitions_total` | `high` |
| `evidence` | `aip_session_classification_transitions_total` | `client_family.codex`, `client_family.droid`, `client_family.hermes`, `client_family.opencode`, `client_family.pi`, `client_family.roo`, `remote.above_threshold`, `tooling.distinct_coding_cluster`, `tooling.project_marker_cluster` |
| `outcome` | `aip_session_classification_remote_total` | `below_threshold`, `budget_exhausted`, `lease_busy`, `malformed`, `network_error`, `positive`, `rate_limited`, `server_error`, `skipped`, `timeout` |
| `outcome` | `aip_session_classification_remote_seconds` | `below_threshold`, `budget_exhausted`, `lease_busy`, `malformed`, `network_error`, `positive`, `rate_limited`, `server_error`, `skipped`, `timeout` |
| `operation` | `aip_session_classification_store_total` | `load`, `promote`, `remote_claim`, `remote_complete` |
| `outcome` | `aip_session_classification_store_total` | `applied`, `denied`, `error`, `hit`, `miss`, `unchanged` |

Two measured values are deliberately not labels, because each would make the series
count grow with traffic: a revision is unbounded, and one latency sample is
bounded by a ceiling rather than by a fixed set. An out-of-range value of either
kind is rejected or dropped, never published.

| Bound | Value | Effect |
|---|---|---|
| `MaxObservedRevision` | 4294967296 | the largest revision one observation may describe; a larger one is rejected instead of exported |
| `MaxRemoteObservationLatency` | 1m30s | the largest latency sample exported; an out-of-range sample is dropped, not truncated |

Reading the surface:

```promql
sum by (mode, outcome) (rate(aip_session_classification_evaluations_total[5m]))
```

A rising `outcome="unknown"` beside a flat `aip_session_classification_transitions_total`
means evidence is not arriving, not that sessions are being rejected: there is no
negative classification to observe. A rising `outcome="excluded"` means an operator
exclusion prefix is matching, which is a configuration fact rather than a client
fact. A `aip_session_classification_store_ready` that stays at 0 while traffic flows
means no enabled generation has initialized the shared store yet.

## Evidence-code semantics

The decisive evidence code is the one field of the positive snapshot that answers
*which rule fired?*. It names the rule and never the input that triggered it: a
code is bounded metadata, not content, and it is not authorization.

| Origin | Source | Codes |
|---|---|---|
| Client identity (Rule A) | `local_identity` | `client_family.codex`, `client_family.droid`, `client_family.hermes`, `client_family.opencode`, `client_family.pi`, `client_family.roo` |
| Distinctive tool cluster (Rule B) | `local_tooling` | `tooling.distinct_coding_cluster` |
| Corroborated tool plus project marker (Rule C) | `local_tooling` | `tooling.project_marker_cluster` |
| Configured remote decision | `remote_classifier` | `remote.above_threshold` |

What a code therefore tells an operator is which rule promoted the session and from
which source. Because `evidence` is also a transition label, the same code answers
which rules are actually driving promotions in a deployment, at a fixed number of
series however many sessions flow.

What a code never contains: the matched client identity, the workspace marker base
name, a tool name, a tool argument, the prompt, the transcript, or any part of a
remote response body. A code is one of a closed set of static identifiers, bounded
by the SDK contract:

| Bound | Value |
|---|---|
| `MaxEvidenceCodeBytes` | 64 bytes per code |
| Allowed characters | ASCII letters, digits, `.`, `_`, `-` |

A syntactically valid but unrecognised code is still refused as a label, so neither
a client-derived nor a vendor-derived string can become one. The same closed set is
what the persisted state holds; see
[Privacy and data minimization](#privacy-and-data-minimization).

## What must never be logged

The surface above is deliberately small, and widening it is how a deployment ends
up with thousands of series. The following shapes are wrong even when the value is
true and even when the caller "already knows" who the client is, so each is written
as a shape rather than as a real value.

| Never log | Instead log |
|---|---|
| `user_agent="<raw client User-Agent string>"` | the bounded evidence code, for example `evidence="client_family.codex"` |
| `path="<absolute workspace path>"` | nothing: a recognized project marker contributes only its presence |
| `filename="<source file name>"` | nothing: the classifier keeps the presence bit, never the name |
| `prompt="<first user message>"` | nothing: classification never reads prompt content |
| `a_leg="<proxy-owned A-leg identifier>"` | nothing: correlate through the existing tracing and exemplar mechanisms |
| `session_id="<proxy-owned session identifier>"` | nothing, for the same reason |
| an arbitrary vendor answer string | `outcome="below_threshold"`, or another closed remote outcome |
| a raw remote request or response body | the bounded `outcome` beside the measured latency |
| `revision="<monotonic revision>"` | nothing: a revision is unbounded, so it belongs in a bounded log field, never in a label |

Two habits keep this honest. First, publish the closed value rather than the input:
the question "why was this session classified?" is answered by
`aip_session_classification_transitions_total{source="local_tooling",evidence="tooling.distinct_coding_cluster"}`,
a fixed-size series whatever the traffic. Second, treat a request-scoped identifier
as a correlation handle owned by the tracing stack, not as classification
telemetry; a correlation that becomes a label stops being bounded.

## Durable and non-durable posture

One process-wide holder owns exactly one authoritative store plus one process
cache/coordinator, shared by every enabled generation. The posture depends on
whether standard featurehost has a shared durable database available for proxy
state:

**Non-durable (no shared database).** A bounded in-process store is used. It
provides the same process/A-leg isolation and the same monotonic semantics inside
one process, and it is bounded: a fixed maximum entry count with bounded idle
cleanup, where established positives and attempted unknowns are pinned rather
than evicted. This deployment makes **no restart-durability claim** for
classification — a process restart begins `unknown`.

**Durable (shared database available).** The feature owns a small, separate
logical table for classification state, ensured once, on either supported
dialect. A previously persisted positive is restored before a resumed turn
reaches classification consumers, subject to the configured state-retention
policy. The table is separate from secure-session policy metadata and
call-record rows and holds only bounded typed state; see
[Privacy and data minimization](#privacy-and-data-minimization).

The process cache retains positives only: a bounded capacity and a bounded idle
TTL, with capacity pressure evicting only local cache entries. Unknown results
are never cached, so a promotion made by another replica cannot be masked by a
stale negative. Concurrent first loads and promotions for the same key are
coalesced, unrelated keys do not wait on one long-held global lock, and no
per-session goroutine or background worker is ever created.

Constructing the holder performs no schema, network, or classification-state
work, and resolving state never initializes it. A deployment that never enables
the feature therefore acquires no classification schema or network dependency,
and a candidate that is rejected before publication leaves nothing behind.

## Reload behavior

A reload changes policy, never process state or durable rows.

- Each generation holds an immutable copy of its own policy — mode, exclusion
  list, and remote settings. Newly admitted turns use the new generation's
  classifier while in-flight turns keep the classifier policy of the generation
  they were admitted under.
- The authoritative store and process cache are shared across generations, so
  publishing a new generation never erases an already persisted positive for the
  same logical session.
- A candidate generation whose classification configuration is invalid is
  rejected before publication. The last-good published generation and all
  process-owned classification state remain unchanged.
- Disabling the feature by reload withdraws the classifier plane: newly admitted
  turns skip the stage. It does **not** delete positive durable rows merely
  because a generation stopped consuming them.
- Re-enabling the feature later for the same authoritative resumable session can
  project the previously persisted positive again, subject to the configured
  state-retention policy.

## Large-body (wire) compatibility

The classifier plane is declared `metadata_only` — the
`session_classifier` plane's request-access class carries bounded session,
workspace, and intent metadata only, never the canonical request body. That
matters for the large-payload fast path:

- The stock classifier's presence alone does **not** make an otherwise
  wire-eligible large request canonical-required, so operators do not lose the
  low-copy wire path by enabling this feature.
- A certified frontend proof compiles only the bounded evidence the classifier
  needs: the accepted client identity and a fixed-width tool-category presence
  summary, plus the operation. It carries no raw header bag, no tool-name list,
  no transcript, and no prompt text. Proof accounting charges the client-identity
  length plus the fixed category-bitset width, so the cost never scales with body
  size or tool count.
- After the one-way wire commit, the stage runs after secure-session, A-leg, and
  workspace binding and before the route and request consumers. It calls the same
  generic runner as the canonical lane, so monotonicity, prior-positive
  preservation, absent-plane no-op, and fail-open behave identically on both
  paths.
- On the wire lane the returned projection is deliberately discarded: every
  same-turn consumer that would read it is a canonical-required plane, and an
  occupied canonical-required plane already blocks the wire lane before it
  starts. The load-bearing effect is the monotonic session-state transition,
  which later turns observe.
- Insufficient or absent wire evidence simply leaves the session `unknown` or
  defers promotion. Classification never forces full request materialization, and
  on the committed wire lane a classification failure never requests a
  post-commit canonical fallback or starts a second ordinary execution.

## Privacy and data minimization

The feature is designed to reveal as little session content as possible, and it
never becomes a security authority.

**What crosses the remote port.** Only derived, bounded, content-free facts:
the protocol operation, the normalized client family or an ambiguity bit, the
canonical tool-category presence bitset, the bounded workspace summary
(`project_marker` or unmarked), and the decisive local evidence code when local
evaluation produced one. The input has no field for a raw `User-Agent`, a
workspace root, a prompt, a transcript, a response body, reasoning content, tool
names, tool arguments, a session or A-leg identifier, or a credential. Values
outside the closed vocabularies — an undefined category bit, an unrecognized
family, an unknown workspace class, or a remote evidence code presented as a
local one — are refused before egress.

**Credentials.** Configuration carries only the credential *reference*, the
name of an environment variable. The value is resolved from the process
environment at call time and is never stored, echoed, or returned; a missing or
blank value is a bounded credential failure that makes no request at all. No
`Authorization` header, provider credential, resume token, secret-guard finding
containing secret material, or raw HTTP header bag crosses the port — the port's
input type has no field that could carry one. The bearer credential is attached
only to the adapter's validated endpoint, and redirects are refused rather than
followed.

**Diagnostics.** Vendor error bodies are never read or echoed. A remote failure
reports a bounded failure kind and an adapter-written reason or a status code,
never vendor text, so nothing an endpoint puts in a response body can reach a log
or a client-visible error.

**Persisted state.** The classification store holds only bounded typed state:
classification kind, source, confidence, evidence code, revision, remote-attempt
state, lease and eligibility timestamps, and an update timestamp. It does not
persist raw `User-Agent` values, raw filesystem paths, prompt excerpts, tool
names or arguments, transcripts, or reversible content fingerprints.

**Untrusted client metadata.** A claimed agent identity from a client is evidence
subject to bounded matching rules, never identity authority. It is never elevated
into proxy identity, and an unrecognized identity is recorded as ambiguity rather
than as a fact.

**Evidence vocabulary.** The closed set of decisive evidence codes this feature
can produce is:

`client_family.codex`, `client_family.roo`, `client_family.opencode`,
`client_family.pi`, `client_family.droid`, `client_family.hermes`,
`tooling.distinct_coding_cluster`, `tooling.project_marker_cluster`,
`remote.above_threshold`

These are static bounded identifiers. A code that is not in this set is not a
classification fact an operator can rely on.

**Removing the feature.** If the feature is disabled or removed, the generic
proxy continues to route, stream, recover, account, and encode requests without
requiring any classification infrastructure. Nothing in the request path depends
on the feature existing.

## Consuming classification in a feature

The contract is one line: ask `IsCodingAgent()` of the `SessionView.Classification`
projected onto the turn. The generic stage runs before submit, tool-catalog,
request-shaping, pre-request, and route-hint consumers, so a decisive first turn is
visible on that same turn.

```go
// Correct: read the projected fact and nothing else.
func codingSpecificPath(view session.SessionView) string {
	if view.Classification.IsCodingAgent() {
		return "coding-agent-session"
	}
	return "generic-session"
}
```

A consumer of the projection must not:

- re-run the classifier, or construct one;
- parse the client `User-Agent`, match prefixes, or keep a second client catalog;
- rescan the transcript, the tool list, or any request content;
- reach into feature-private state such as the classifier, the shared process
  store, or the coordinator;
- read a vendor-specific result to decide what a coding session is.

```go
// Incorrect: a second client detector, coupled to request content and to one
// vendor's answer.
func codingSpecificPath(call *aipapi.Call, jevAnswer string) bool {
	if strings.HasPrefix(call.ClientUserAgent, clientfamily.CodexPrefix) {
		return true
	}
	if aipapi.ClassifyToolName(call.Tools[0].Name).IsFileEdit() {
		return true
	}
	return strings.EqualFold(jevAnswer, "yes")
}
```

The incorrect form is wrong in three separate ways. It re-derives identity from a
header the classifier already decided about, it makes a feature's behaviour depend
on request content the classifier deliberately never reads, and it treats one
vendor's answer as the definition of the session. It also drifts: a second detector
keeps its own rules, so it will disagree with the classification a consumer is
supposed to be reading.

Reading the fact is not the same as trusting it. Classification is advisory derived
metadata, and it is not authorization: a positive value authorizes no tool, grants
no permission, establishes no identity, and alters no billing entitlement. Keep
enforcing your own checks for each of those. Treat `unknown` as *not yet positively
classified*, never as *known not to be a coding agent*, and expect the value to
arrive on a later turn rather than on the first one — the first turn is only
decisive when the evidence is already there.

Enabling this feature switches nothing on: existing features
do not become gated automatically, and a bundled feature becomes
classification-aware only when its own implementation explicitly opts in. See
[Scope: advisory metadata, not authorization](#scope-advisory-metadata-not-authorization).

## Disabled posture

With the feature absent or `enabled: false`, no classifier plane is composed, so
nothing evaluates, nothing reaches the network, and nothing is stored. The
classification-specific series are absent, not zero:

| Family | While the feature is disabled |
|---|---|
| `aip_session_classification_evaluations_total` | absent — no series is exported |
| `aip_session_classification_transitions_total` | absent — no series is exported |
| `aip_session_classification_remote_total` | absent — no series is exported |
| `aip_session_classification_remote_seconds` | absent — no histogram series is exported |
| `aip_session_classification_store_total` | absent — no series is exported |
| `aip_session_classification_store_ready` | the one static series that may remain: 0 until the first enabled generation initializes the shared store |

"Absent" is literal. The collector exports only combinations it has actually
observed, so a disabled deployment presents no classification time series rather
than a wall of zeroes. The readiness gauge is the single exception the disabled
posture allows, because it is static feature state rather than a hot-path
observation.

Counters are cumulative, so disabling the feature stops them growing rather than
retroactively deleting series an earlier enabled generation already exported. That
is deliberate: a flat counter is easier to interpret than a disappeared one, and a
disappearing series would make a restart look like a change in client behaviour.

Removing the feature entirely is no different from disabling it. The generic proxy
will continue to route, stream, recover, account, and encode requests without
requiring any classification infrastructure, and a deployment that never enables
it acquires no classification schema and no network dependency.

## Future explanation surfaces

An operator-visible session/request explanation surface will consume this feature
as a read-only projection. That surface is a future consumer, tracked as issue
#456 and not a prerequisite for this feature: the classifier, its state, and this
metric surface ship independently of it, and nothing in this guide waits for it.

When issue #456 lands it reads `SessionView.Classification` — kind, source,
confidence band, evidence code, and revision — and never reaches into
feature-private classifier state, the shared store, or the coordinator. The same
projection is available without a request at all: the bounded transition
observation carries source, confidence band, evidence code, and revision, and
projects them as the same snapshot.

Because an explanation surface is a consumer, it inherits every constraint above.
It shows a classification rather than deciding on one, `unknown` renders as *not
yet classified*, a positive value is never presented as authorization, and the
evidence code is presented as the name of the rule that fired, never as the content
it matched.