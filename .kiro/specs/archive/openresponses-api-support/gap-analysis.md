# Brownfield Gap Analysis

## Scope and Method

This review compares OpenResponses `2026-04-24` with repository `main` at `eb843ba4f2d60a2b85c9be7e94f542311384b73b`. It covers the existing OpenAI Responses implementation, all bundled client-facing protocol adapters, all bundled backend connector families, canonical request/event contracts, route ownership, continuation/session infrastructure, generic compatible modes, the backend plugin ABI, independent reference clients/backends, and the authoritative FE×BE conformance framework.

Classifications:

- **Missing**: no current implementation or executable contract exists.
- **Partial**: reusable implementation exists but does not satisfy the required contract.
- **Constraint**: an architecture or compatibility commitment limits the solution.
- **Unknown**: implementation must validate a material external or brownfield assumption.

Effort:

- **S**: focused package/test change on an established seam.
- **M**: multi-package change with composed tests.
- **L**: broad adapter/canonical change with conformance impact.
- **XL**: architecture migration, stateful transport, or public plugin ABI impact.

## Current Assets to Reuse

### Existing protocol frontends

- `internal/plugins/frontends/openailegacy`
- `internal/plugins/frontends/openairesponses`
- `internal/plugins/frontends/anthropic`
- `internal/plugins/frontends/gemini`

Reusable strengths include authentication, route/session authority, body limits, canonical construction, streaming response plumbing, and composed frontend tests. Their wire contracts remain independently owned and must not be imported by OpenResponses adapters.

### Existing backend connectors

- `internal/plugins/backends/openailegacy`
- `internal/plugins/backends/openairesponses`
- `internal/plugins/backends/anthropic`
- `internal/plugins/backends/gemini`
- `internal/plugins/backends/bedrock`
- `internal/plugins/backends/acp`
- `internal/plugins/backends/openrouter`
- `internal/plugins/backends/nvidia`
- shared `openaicompat`, credentials, endpoint, inventory, admission, stream peeking, and error-classification infrastructure

The backend boundary already distinguishes canonical mapping from SDK/transport plumbing. Mapping behavior is primarily constrained by conformance, reference backends, and golden fixtures.

### Canonical and runtime seams

- `pkg/lipapi` call/message/content/tool/event/stream contracts
- `pkg/lipsdk` plugin-facing contracts
- capability derivation and candidate admission
- routing, failover, output commitment, accounting, audit, redaction, hooks, diagnostics, and secure state
- standard HTTP composition and route ownership work
- runtime reload/shutdown ownership
- approved `gorilla/websocket` dependency

### Existing independent reference clients

`internal/refclient/*` provides test-only black-box clients, normally wrapping official vendor SDKs or independently implementing documented wire behavior. Production/core packages must not depend on these packages. Existing families cover OpenAI Responses, OpenAI Chat Completions, Anthropic Messages, and Gemini.

This is the correct ownership location for an independent `internal/refclient/openresponses`. Because no suitable official Go SDK defines the standard, it must implement the pinned wire contract independently from production OpenResponses codecs while using immutable fixtures/schema metadata as evidence.

### Existing remote API emulators

`internal/refbackend/*` provides test-only spec-shaped HTTP emulators for remote inference APIs. Existing families include:

- OpenAI Responses
- OpenAI Chat Completions
- Anthropic Messages
- Gemini
- Amazon Bedrock
- ACP
- OpenRouter and NVIDIA support used by conformance helpers

Production packages must not import reference backends. This is the correct location for a scriptable `internal/refbackend/openresponses` remote-provider emulator.

### Existing FE×BE conformance framework

Relevant assets:

- `internal/testkit/conformance/matrix.go`
- `internal/testkit/conformance/frontend_server.go`
- `internal/testkit/conformance/harness.go`
- `internal/testkit/conformance/refparity.go`
- protocol-specific `parity_*.go` suites and golden fixtures

The authoritative frontend list currently contains four IDs:

1. `openai-responses`
2. `openai-legacy`
3. `anthropic`
4. `gemini`

The authoritative backend list currently contains eight IDs:

1. `openai-responses`
2. `openai-legacy`
3. `anthropic`
4. `gemini`
5. `bedrock`
6. `acp`
7. `openrouter`
8. `nvidia`

The current Cartesian product is 32 cells. Adding OpenResponses to both sides produces 5 × 9 = 45 cells. This established framework is preferable to hand-written pairwise test silos.

### Relevant architecture commitments

- canonical middle, not pairwise protocol translators;
- wire ownership in frontends/backends;
- no provider SDK or test-emulator types in core;
- no transparent retry after visible output;
- unsupported semantics fail before upstream work;
- deterministic `httptest`-based composed tests belong in normal CI where practical;
- tests, fixtures, stable contracts, and parity matrices form the recoverable specification bundle;
- scenario/branch evidence is more authoritative than raw coverage percentage.

## API and Evidence Comparison

| Area | Required OpenResponses behavior | Current repository state | Gap |
|---|---|---|---|
| Protocol identity | Separate dated profile | Existing OpenAI Responses operation | Distinct identity required |
| Route/transport | JSON, SSE, compact, sequential WS | Existing OAI route; no OR surface | Missing |
| Canonical model | Ordered item authority | Message-first with partial item semantics | Structural migration required |
| Roles/phase | Developer distinction and assistant phase | Partial/no phase | Missing |
| Response resource | Complete required presence | Sparse OAI resource | Non-conformant |
| Continuation | Proxy-owned persisted/local history | No OR continuation | Missing |
| Compaction | Core-routed operation | No canonical operation | Missing |
| Extensions | Typed portable plus bounded dialect-bound opaque | Unknown events may be ignored | Partial |
| Generic backend | Standards-only OR remote endpoint | OAI-compatible mode only | Separate mode required |
| Reference client | Independent OR client emulator | No `refclient/openresponses` | Missing |
| Reference backend | Scriptable OR provider emulator | No `refbackend/openresponses` | Missing |
| FE×BE matrix row | OR frontend against all bundled backends | OR absent from frontend list | Missing nine-cell row |
| FE×BE matrix column | Every frontend against OR backend | OR absent from backend list | Missing five-cell column |
| Feature classification | Per-cell lossless/projected/reject/out-of-scope evidence | Coarse viability metadata | Partial |
| Negative evidence | Zero upstream work for unsupported semantics | Not required for every cell | Missing |
| Emulator independence | Tests must not share production codec logic | No OR emulators/boundary tests | Missing; tautology risk |
| Coverage quality | Scenario/branch evidence plus no-regression and package targets | General steering only | Missing feature-specific gate |
| Official full-path evidence | Independent client through proxy into independent provider | Existing plan covers only frontend/generic endpoint | Partial |

## Mandatory Gap Register

| ID | Severity | Classification | Effort | Finding | Required disposition |
|---|---:|---|---:|---|---|
| G-01 | P0 | Missing | M | No distinct OpenResponses identity/profile. | Add separate operation, frontend, backend kind, diagnostics, and conformance identity. |
| G-02 | P0 | Constraint | M | `/v1/responses` ownership conflicts with existing OpenAI Responses. | Add configurable non-colliding default and deterministic route claims; never sniff. |
| G-03 | P0 | Missing | XL | Canonical call cannot preserve complete ordered item trajectory. | Add item authority, normalized walkers, and explicit projections. |
| G-04 | P0 | Partial | L | Developer role, phase, item status/identity, structured outputs, video, refusal, reasoning, compaction are incomplete. | Extend protocol-neutral canonical contracts. |
| G-05 | P0 | Partial | L | Existing frontend recognizes fields such as continuation without implementing semantics. | Add strict OR decoder and proxy-owned continuation. |
| G-06 | P0 | Missing | XL | No persisted or connection-local response-history contract. | Add bounded scoped continuation service/store. |
| G-07 | P0 | Missing | L | No context-compaction operation/capability. | Route a neutral operation through the normal executor. |
| G-08 | P0 | Missing | XL | No Responses WebSocket frontend. | Add authenticated sequential WS termination and lifecycle. |
| G-09 | P0 | Partial | L | Existing response resource is sparse. | Build profile-specific required-presence resource. |
| G-10 | P0 | Partial | L | Canonical stream is too coarse for all item/content lifecycle. | Add lifecycle metadata/events and deterministic normalizer. |
| G-11 | P0 | Partial | L | Unknown backend output can be silently ignored. | Add bounded opaque item/event carriage with exact capability binding. |
| G-12 | P0 | Constraint | M | Existing OAI-compatible backend uses OAI SDK/unions. | Add dependency-free `custom-openresponses-compatible`. |
| G-13 | P0 | Constraint | L | Plugin ABI may not express item dialects or compaction. | Revalidate/version ABI and conformance fixtures first. |
| G-14 | P0 | Missing | M | No pinned OR conformance profile. | Pin official sources/digests and mirror Go-native cases. |
| G-15 | P1 | Partial | M | SSE framing exists but full lifecycle/presence does not. | Separate low-level framing from profile state machine. |
| G-16 | P1 | Missing | M | No profile configuration/version rejection. | Add closed supported-profile catalog. |
| G-17 | P1 | Missing | M | Route ownership is not sufficient for colliding protocol aliases. | Add immutable method/path claims. |
| G-18 | P1 | Missing | L | Capabilities cannot express phase/item/replay/compaction/extension dialects. | Add hard semantic and exact dialect requirements. |
| G-19 | P1 | Partial | M | Raw extensions do not encode portability/lineage. | Add typed residual controls and bounded opaque records. |
| G-20 | P1 | Missing | M | Provider IDs could be confused with proxy IDs. | Issue proxy IDs; keep native IDs private evidence. |
| G-21 | P1 | Missing | M | No OR continuation TTL/depth/amplification/isolation policy. | Add bounded indistinguishable lookup behavior. |
| G-22 | P1 | Missing | M | WS origin/queue/age/backpressure policies absent. | Add strict bounded defaults and dev-only relaxation. |
| G-23 | P1 | Unknown | S | Normative extension prefix rules conflict with dated provider-derived schema types. | Pin precedence: portable typed, recognized legacy capability-gated, new unknown prefix-gated. |
| G-24 | P1 | Unknown | S | Third-party Go candidates lack stable auditable suitability. | Keep production codec project-owned unless dependency gate passes. |
| G-25 | P1 | Constraint | S | Official OpenAI SDK is protocol-specific and lacks required WS contract. | Confine it to OpenAI packages. |
| G-26 | P1 | Partial | M | Generic endpoint plumbing is reusable only through OAI flavor today. | Reuse infrastructure with separate OR codec/profile. |
| G-27 | P1 | Missing | M | OR HTTP/SSE/WS error mapping absent. | Add profile-specific bounded mappings. |
| G-28 | P1 | Missing | M | Shared-helper refactoring lacks complete regression proof. | Characterize all affected existing adapters first. |
| G-29 | P1 | Missing | M | Audit/redaction/counting may scan only legacy messages. | Add ordered walkers and adversarial tests. |
| G-30 | P1 | Missing | M | Reload/shutdown ownership for OR state/WS unspecified. | Define atomic generation and exactly-once cleanup. |
| G-31 | P2 | Constraint | S | Upstream WebSocket pooling adds session affinity without initial need. | Terminate client WS and use upstream HTTP/SSE initially. |
| G-32 | P2 | Partial | S | Background/conversation fields lack a complete proxy job surface. | Reject unsupported modes explicitly. |
| G-33 | P2 | Missing | S | Naming may be confused with unrelated `open-responses`. | Document distinction. |
| G-34 | P0 | Missing | M | No independent OpenResponses reference client exists. | Add `internal/refclient/openresponses` with independent wire implementation and black-box parser assertions. |
| G-35 | P0 | Missing | M | No independent OpenResponses remote backend emulator exists. | Add scriptable `internal/refbackend/openresponses` for JSON/SSE/compact/WS/errors/adversarial behavior. |
| G-36 | P0 | Missing | M | OpenResponses is absent from both authoritative conformance lists. | Add a nine-cell frontend row and five-cell backend column; assert exactly 45 cells. |
| G-37 | P0 | Partial | L | Existing backends are not proven to consume item-authority OR calls. | Implement/test explicit item-to-backend projectors and pre-network rejection. |
| G-38 | P0 | Partial | L | OR backend is not proven to consume legacy message-authority calls from existing frontends. | Implement/test explicit legacy-message-to-OR-item projection. |
| G-39 | P0 | Missing | M | User-requested forward/reverse paths are not individually scheduled. | Add named matrix tasks and linked evidence for every requested path. |
| G-40 | P1 | Partial | M | Matrix metadata is too coarse to prove tools, multimodal, reasoning, phase, continuation, compaction, and extensions. | Add feature-level status/evidence per cell. |
| G-41 | P0 | Missing | M | Unsupported semantics may be tested only as capability metadata, not zero-request behavior. | Add reference-backend request counters and negative pre-network assertions. |
| G-42 | P0 | Constraint | M | Reusing production OR codecs in emulators would make tests tautological. | Enforce emulator independence and immutable fixtures-only sharing with architecture tests. |
| G-43 | P1 | Missing | M | Official conformance is not required on the full independent client→proxy→independent provider path. | Add full-path deployment gate. |
| G-44 | P1 | Missing | M | No explicit coverage/no-regression target exists for new deterministic protocol packages. | Collect coverprofiles, prohibit unexplained regression, target ≥90% with reviewed exceptions. |
| G-45 | P1 | Missing | S | Matrix completion can currently tolerate planned or unlinked cells. | Make unresolved required cells/features a release blocker. |

## Required Cross-API Paths

### OpenResponses frontend row

The following must be exercised through the canonical executor:

- OpenResponses → OpenAI Chat Completions
- OpenResponses → OpenAI Responses
- OpenResponses → ACP (positive text subset; negative tools/multimodal/nonportable semantics)
- OpenResponses → Anthropic Messages
- OpenResponses → Gemini/Vertex
- OpenResponses → Amazon Bedrock
- OpenResponses → OpenResponses-compatible
- OpenResponses → OpenRouter
- OpenResponses → NVIDIA

### OpenResponses backend column

The following must be exercised through their real frontend wire handlers and canonical construction:

- OpenAI Chat Completions → OpenResponses-compatible
- OpenAI Responses → OpenResponses-compatible
- Anthropic Messages → OpenResponses-compatible
- Gemini/Vertex → OpenResponses-compatible
- OpenResponses → OpenResponses-compatible

Each path requires positive portable-subset evidence and negative pre-network rejection evidence for non-representable semantics.

## Current Behavior to Characterize Before Change

1. All existing frontend decode/encode routes, errors, streaming terminals, usage, tools, and multimodal behavior.
2. OpenAI reasoning exact replay and provider residual controls.
3. Every backend's request mapping, first-event peeking, credential rotation, cancellation, and terminal ownership.
4. Canonical capability derivation, downgrade policy, walkers, hooks, redaction, audit, and accounting.
5. Existing refclient/refbackend ownership and normative fixture boundaries.
6. Matrix list completeness, cell metadata, frontend mounting, backend construction, and parity helpers.
7. ACP prompt-turn subset and explicit exclusions.
8. Generic endpoint URL joining, no-auth, inventory, prefixes, reload, and diagnostics.

## Architecture Options

### Option A: Alias OpenResponses to OpenAI Responses

Rejected. It cannot support honest dual route contracts, dated presence, compaction, WebSocket continuation, phase, extension behavior, or exhaustive compatibility evidence.

### Option B: Raw reverse-proxy tunnel

Rejected. It bypasses canonical routing, hooks, accounting, redaction, failover, continuation, and the FE×BE compatibility framework.

### Option C: Pairwise translators for every requested path

Rejected. Ten named paths would quickly become divergent N×M translation code, duplicate semantics, and bypass canonical capability rules.

### Option D: Canonical ordered items plus independent emulators and Cartesian conformance

Selected.

- Add one protocol-neutral ordered authority and explicit projectors.
- Add separate OpenResponses frontend and backend adapters.
- Add independent black-box `refclient/openresponses` and scriptable `refbackend/openresponses` packages.
- Extend the existing authoritative lists to 5 frontends and 9 backends.
- Generate all 45 cells and feature-level outcomes from one registry.
- Require lossless mapping, documented deterministic projection, or pre-network rejection.
- Run official conformance on the full independent path.

## Effort and Risk

- **Overall effort:** XL.
- **Compatibility/test expansion:** L in addition to the core protocol work.
- **Risk:** Medium-High, reduced by TDD, independent emulators, matrix completeness checks, and fail-closed semantics.
- **Primary risk areas:** canonical authority migration, item lifecycle, continuation, WebSocket ownership, projectors into legacy APIs, plugin ABI, and false-positive interoperability from self-referential tests.

## Selected Disposition

Proceed only with Option D. The implementation series must treat independent emulators and the complete 45-cell matrix as first-class deliverables, not optional post-implementation tests. No compatibility claim is valid without linked wire, adapter, and full-path evidence.
