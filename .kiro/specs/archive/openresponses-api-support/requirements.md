# Requirements Document

## Introduction

OpenResponses is a separately governed, dated interoperability specification derived from the OpenAI Responses API. The protocols share substantial vocabulary, but they are not interchangeable contracts. OpenResponses `2026-04-24` defines its own normative transport, item lifecycle, continuation, compaction, assistant phase, extension, response-resource, and compliance behavior.

Go-LIP already exposes OpenAI Chat Completions, OpenAI Responses, Anthropic Messages, and Gemini-compatible frontends and includes backend connectors for OpenAI Chat Completions, OpenAI Responses, Anthropic Messages, Gemini/Vertex, Amazon Bedrock, ACP, OpenRouter, and NVIDIA. This feature adds OpenResponses as a distinct protocol surface without changing the meaning, routes, or compatibility promises of those adapters. It provides:

- a client-facing OpenResponses frontend supporting HTTP JSON, HTTP SSE, standalone compaction, and persistent sequential WebSocket turns;
- a dependency-free generic backend mode for remote OpenResponses-capable providers and routers over HTTP JSON/SSE and compaction;
- an independent OpenResponses reference client emulator and an independent OpenResponses remote-backend emulator; and
- exhaustive, feature-aware cross-protocol validation by adding OpenResponses as both a frontend row and backend column in the repository's authoritative canonical FE×BE conformance matrix.

The implementation is pinned initially to the official `2026-04-24` snapshot and compliance suite. Portable semantics must be translated through protocol-neutral canonical contracts. Provider-bound or non-representable semantics must be capability-gated and rejected before upstream network work; they must never be silently dropped or accidentally downgraded.

## Boundary Context

- **In scope**: protocol identity/versioning; ordered item trajectory; HTTP JSON; SSE; complete response-resource synthesis; client-facing WebSocket; continuation state; compaction; generic remote backend mode; independent test-only client/backend emulators; full Cartesian FE×BE compatibility; explicit canonical projectors; provider-prefixed opaque extensions; configuration; diagnostics; limits; quality, coverage, conformance, race, fuzz, leak, and regression gates; adjacent architecture revalidation.
- **Out of scope**: changing the existing OpenAI Responses contract; pretending OpenResponses is an OpenAI alias; pairwise frontend-to-backend translator packages; background job retrieval APIs; arbitrary provider-specific header forwarding; OpenRouter-specific attribution/routing/billing in the generic mode; automatic upstream WebSocket pooling; runtime schema download; claiming unsupported features are portable.
- **Adjacent specifications requiring revalidation**: `backend-connector-plugin-architecture`, `generic-compatible-backend-modes`, `llm-api-parity`, canonical API steering, testing steering, routing/commitment steering, and any active backend plugin ABI work.

## Requirements

### Requirement 1: Distinct Protocol Identity and Version Pinning

**Objective:** As a maintainer, I want OpenResponses represented as an explicit dated protocol profile, so compatibility cannot drift with OpenAI's API or an unversioned external schema.

#### Acceptance Criteria

1. The system shall identify OpenResponses with a distinct frontend ID, operation identity, backend factory kind, diagnostics identity, and conformance profile rather than aliasing `openai.responses`.
2. The initial implementation shall pin behavior to the official OpenResponses `2026-04-24` normative specification, OpenAPI snapshot, examples, and compliance tests at a reviewed immutable upstream commit.
3. Runtime and CI shall not silently follow the mutable unversioned schema or website.
4. When configuration requests an unsupported profile, startup shall fail before serving and list supported profiles.
5. A later snapshot shall require an explicit profile addition, compatibility review, fixture update, and conformance run rather than mutating the existing profile.
6. Existing protocol frontends and backends shall remain behaviorally unchanged unless a separately reviewed compatibility fix is required.
7. Diagnostics shall expose the configured profile and whether an instance is client-facing, generic remote, provider-specific, or test-only.
8. Architecture tests shall fail if OpenResponses is registered as the existing OpenAI Responses operation or if either protocol exports the other's wire contract.

### Requirement 2: Client-Facing Route Ownership and HTTP Semantics

**Objective:** As an operator, I want an exact OpenResponses HTTP surface that safely coexists with existing frontends.

#### Acceptance Criteria

1. The frontend shall expose `POST <base_path>/responses`, `POST <base_path>/responses/compact`, and optionally `GET <base_path>/responses` for WebSocket upgrades.
2. The default base path shall be `/openresponses/v1`.
3. `/v1` may be configured only when no enabled frontend owns a resulting method/path pair.
4. Before serving, composition shall validate normalized method/path ownership and return a deterministic conflict naming both owners.
5. The system shall not select OpenAI Responses versus OpenResponses by sniffing body, headers, user agent, or response shape on one route.
6. Create and compact endpoints shall require JSON, enforce independent body/depth/item/tool/content limits, and return profile-shaped errors.
7. Create shall accept string input or an ordered item array and may omit `model` or `input` only when the remaining request and resolved continuation are semantically valid.
8. Non-streaming creation shall emit every field required by the pinned `ResponseResource`, including explicit null/default presence.
9. The OpenAI-specific cancel route shall not be exposed as part of OpenResponses.
10. Authentication, authoritative route/session selection, and request limits shall execute before continuation lookup or backend work.

### Requirement 3: Ordered Canonical Item Trajectory

**Objective:** As an adapter author, I want an ordered protocol-neutral item representation so OpenResponses semantics are not collapsed into a lossy message-only model.

#### Acceptance Criteria

1. The canonical API shall represent ordered message, item-reference, function-call, function-output, reasoning, compaction, and bounded-extension items.
2. Items shall preserve applicable identity, lifecycle status, role, assistant phase, call identity, ordering, content order, and opaque replay material without importing wire structs.
3. Canonical roles shall distinguish `system`, `developer`, `user`, and `assistant` when the source distinguishes them.
4. Assistant phase shall preserve `commentary` and `final_answer` on input and output.
5. Canonical content shall represent portable text, image, file, video, refusal, reasoning, summary, annotation, and assistant-reference forms.
6. Function output shall preserve both string output and ordered structured content-part arrays.
7. Reasoning shall preserve visible content, safe summaries, and bounded dialect-tagged opaque replay material without interpreting protected data.
8. Compaction items shall round-trip as ordered context and remain bound to compatible dialect/lineage when nonportable.
9. Legacy message-authority calls shall remain source-compatible, while validation prevents conflicting simultaneous item and message authorities.
10. Shared walkers, capability derivation, hooks, accounting, limits, redaction, and audit shall handle both call forms.
11. Projection between ordered items and legacy messages shall occur only through explicit deterministic tested projectors and shall reject non-representable semantics before execution.
12. Items and content shall have deterministic count, depth, reference, string, schema, and opaque-payload bounds.

### Requirement 4: Tools, Extensions, and Lossless Capability Negotiation

**Objective:** As a router user, I want portable tools and declared extensions preserved only on compatible candidates.

#### Acceptance Criteria

1. The portable profile shall support function tools, required `tool_choice` forms, parallel-tool policy, call items, and call-output items.
2. The implementation shall distinguish portable forms from provider-derived hosted tools/items present in the dated schema.
3. Standard pinned forms shall use typed project-owned representations rather than arbitrary maps.
4. A non-standard type may be carried opaquely only when its discriminator follows profile naming rules or is an explicitly recognized dated legacy type.
5. Opaque records shall preserve bounded JSON, discriminator, implementor, direction, and dialect while core inspects only validation/routing metadata.
6. Provider-bound tools, reasoning, compaction, items, or top-level extensions shall require an exact compatible backend declaration.
7. Candidate admission shall reject before upstream work when any semantic would be dropped, renamed, exposed to an unrelated implementor, or sent to an incompatible dialect.
8. Pre-output failover shall consider only candidates satisfying the complete requirements; no failover shall occur after visible output.
9. Generic backend configuration shall reject unknown proprietary top-level request fields by default.
10. Explicitly allowlisted bounded top-level extension keys shall remain candidate-bound, redacted, and shall not enable arbitrary header forwarding.
11. Provider-specific auth, attribution, routing, inventory, billing, or typed proprietary extensions shall remain provider-connector owned.
12. Unknown valid provider-prefixed output shall surface as bounded opaque events/items to an OpenResponses client rather than being discarded.

### Requirement 5: Streaming State Machine and Response Fidelity

**Objective:** As an OpenResponses client, I want schema-valid semantic events and deterministic terminal ordering.

#### Acceptance Criteria

1. `stream:true` shall return `text/event-stream` and each SSE `event` value shall equal the JSON body's `type`.
2. The final SSE record shall be literal `[DONE]` after exactly one terminal response event.
3. The encoder shall preserve or synthesize required response, item, content-part, text, refusal, reasoning, function-argument, and error lifecycle events.
4. Every output item shall have one added and one done lifecycle, with content-part lifecycle where applicable.
5. IDs, call IDs, indices, statuses, phases, and sequence numbers shall remain consistent across stream and final resource.
6. Writable streaming failures shall emit structured error and failed terminal semantics before `[DONE]`.
7. No event may follow terminal; duplicate done/terminal events are invalid.
8. Backpressure, cancellation, writer failure, panic recovery, and closure shall use bounded memory and exactly-once ownership.
9. Non-streaming collection and streaming encoding shall derive from the same canonical state machine.
10. Provider-native IDs shall remain evidence, not proxy continuation authority.

### Requirement 6: Proxy-Owned Response Continuation

**Objective:** As a client using `previous_response_id`, I want continuation without exposing backend state or weakening routing isolation.

#### Acceptance Criteria

1. Client response IDs shall be proxy-issued opaque IDs scoped to authoritative client/session identity.
2. Continuation shall materialize prior input, prior output, then new input in semantic order before candidate selection.
3. Records shall contain profile, canonical trajectory, model/route lineage, persistence mode, expiry, and replay requirements.
4. Portable history may reroute among candidates satisfying all requirements.
5. Provider-bound opaque/replay/compaction state shall pin compatible lineage or fail before upstream work.
6. The generic backend shall use materialized canonical history and shall not forward a client ID directly upstream.
7. Native remote IDs may be private optimizations only under matching lineage and a policy-valid canonical fallback.
8. Persisted state shall obey TTL, isolation, deletion, limits, and startup/shutdown ownership.
9. Missing, expired, unauthorized, evicted, or incompatible IDs shall return indistinguishable `previous_response_not_found` behavior.
10. Client metadata, response IDs, and model strings shall not override authoritative route/session identity.
11. Stored state shall follow reviewed secure-state policy and shall not enter ordinary logs/diagnostics.
12. Lookup/materialization shall be bounded against depth, cycles, amplification, and oversized reconstructed context.

### Requirement 7: Standalone Context Compaction

**Objective:** As an agent client, I want `/responses/compact` routed through normal backend selection.

#### Acceptance Criteria

1. Core shall expose a protocol-neutral context-compaction operation and capability.
2. Compact shall require a model and accept pinned compact fields and ordered input.
3. Only candidates declaring compatible compaction/item/replay capabilities shall be eligible.
4. The generic backend shall map compaction to remote `POST /responses/compact` without bypassing core.
5. The frontend shall return the complete pinned `response.compaction` resource.
6. Output shall contain a reusable ordered item window and required compaction item(s).
7. Later create shall start a new chain without the pre-compaction response ID.
8. Errors shall use normal pre-output classification/failover; returned results shall not be transparently replayed.
9. Absence of an eligible backend shall yield a profile-shaped capability error, never lossy local summarization.
10. Built-in and plugin backend contracts shall express and test the same compaction operation.

### Requirement 8: Client-Facing WebSocket Transport

**Objective:** As an agent client, I want persistent sequential OpenResponses turns with HTTP/SSE-equivalent semantics.

#### Acceptance Criteria

1. A valid authenticated `GET <base_path>/responses` upgrade shall use the approved WebSocket library and origin policy.
2. Each turn shall begin with `type: "response.create"`; HTTP-only fields shall be rejected.
3. At most one response shall be in flight per connection; accepted turns shall execute sequentially without multiplexing.
4. Output shall use the same event objects and lifecycle as SSE without SSE framing or `[DONE]`.
5. `store:false` continuation shall be connection-local and unavailable after reconnect.
6. Missing local state shall produce the required `previous_response_not_found` error envelope.
7. A classified 4xx/5xx-equivalent continuation failure shall evict the referenced local parent; disconnect/cancellation/unrelated transport failure shall not.
8. Maximum age shall be configurable but no greater than 60 minutes and shall emit `websocket_connection_limit_reached` when possible.
9. Message sizes, pending events, queue, idle time, ping/pong, and retained state shall be bounded.
10. Disconnect/cancellation shall propagate without post-output retry.
11. Client WebSocket support shall not require upstream WebSocket pooling.
12. Relaxed browser origin behavior shall require explicit development-only configuration.

### Requirement 9: Generic Remote OpenResponses Backend Mode

**Objective:** As an operator, I want standards-compliant remote providers without provider-specific code.

#### Acceptance Criteria

1. The standard distribution shall provide stable kind `custom-openresponses-compatible` or an equivalently reviewed name.
2. Every instance shall have independent ID, route prefix, endpoint, credentials, inventory, capability profile, limits, and provenance.
3. The backend shall support remote JSON create, SSE create, and compact using the pinned profile.
4. One validated endpoint descriptor shall join create, compact, and optional models paths while preserving intentional prefixes.
5. Authenticated and explicit no-auth endpoints shall be supported without empty auth headers.
6. Credentials shall use secret-aware shared infrastructure and need not be literal YAML values.
7. All claimed standard events shall be parsed; valid prefixed unknown output shall become bounded opaque canonical events.
8. Errors shall be classified into pre-output recoverability, auth/rate-limit state, terminal failure, and post-output committed failure.
9. Operation, transport, item, phase, replay, extension, continuation, and compaction capabilities shall be honest and model-aware where needed.
10. Unsupported semantics shall fail before the upstream request opens.
11. Upstream WebSocket pooling is not required initially and requires separate lifecycle/session-affinity review later.
12. OpenRouter-specific policy shall remain in its connector, which may reuse the shared wire codec.
13. The backend shall accept both canonical item-authority calls and legacy message-authority calls through explicit validated constructors/projectors.
14. Legacy-message projection shall preserve the portable ordered conversation, tools, content, and controls and shall reject conflicting or non-representable semantics before network work.

### Requirement 10: Configuration, Diagnostics, and Security

**Objective:** As an operator, I want strict configuration and bounded observability.

#### Acceptance Criteria

1. Frontend config shall validate profile, path, WebSocket policy, continuation persistence, TTL, and bounds.
2. Backend config shall validate prefix, endpoint, credential refs, inventory, capability/dialect declarations, and limits.
3. Unknown configuration fields shall fail with instance identity.
4. Base URLs shall be absolute HTTP(S), require a host, reject userinfo/fragments, and join paths deterministically.
5. Diagnostics shall expose sanitized profile, origin, route ownership, transports, continuation mode, capabilities, inventory, and conformance status.
6. Logs/metrics shall use bounded reason codes and cardinality-safe labels, not content, IDs, arbitrary types, or raw provider errors.
7. Request, response, event, message, item, content, schema, metadata, annotation, extension, and continuation sizes shall be independently bounded.
8. Authentication/authorization shall precede body work, WebSocket state, continuation lookup, or backend execution.
9. Client-controlled metadata/cache/safety/extensions shall follow explicit forwarding/redaction policy and never become routing authority.
10. Shutdown/reload shall close sessions, cancel attempts, handle state by policy, and release resources exactly once.
11. Reload shall reject changes that cannot be applied atomically without orphaning ownership.
12. Security review shall cover ID probing/fixation, extension smuggling, origin abuse, amplification, event injection, and sensitive replay data.

### Requirement 11: Licensed Implementation and Dependency Boundaries

**Objective:** As a maintainer, I want an auditable permissively licensed implementation.

#### Acceptance Criteria

1. Official schema/spec/examples/compliance fixtures may be used under verified Apache-2.0 terms with pinned provenance/notices.
2. Production shall not depend on source-unavailable or unlicensed packages.
3. Untagged runtime modules require a reviewed immutable-source, maintenance, license, security, API-fit, and conformance exception.
4. The OpenAI Go SDK shall remain confined to OpenAI-family backend packages.
5. Default OpenResponses wire code shall be project-owned, schema-aligned, and use standard HTTP/JSON/SSE plus the approved WebSocket dependency.
6. Generated/copied material shall be reproducible, minimal, reviewable, and provenance-tagged.
7. Provider SDK, wire, and third-party types shall not enter canonical/core/plugin ABI contracts.
8. A later SDK replacement must pass identical goldens, fuzz, conformance, cancellation, extension, and boundary tests.
9. Architecture tests shall prohibit core-to-adapter and generic-to-provider-specific dependencies.
10. Root builds shall work with `GOWORK=off`, no external connector modules, and no JavaScript runtime for normal build/unit tests.
11. Reference emulators shall remain test-only and shall not become production dependencies.

### Requirement 12: TDD, Conformance, Migration, and Delivery Gates

**Objective:** As a maintainer, I want executable brownfield migration and independently grounded compatibility evidence.

#### Acceptance Criteria

1. Interfaces, canonical contracts, projectors, capabilities, route ownership, emulator contracts, goldens, and failing conformance cases shall precede production implementation.
2. The suite shall include every pinned official HTTP, SSE, compaction, and WebSocket scenario.
3. Official cases shall be pinned immutably and shall not download mutable sources during tests.
4. Go-native goldens shall validate required fields, discriminators, sequence/lifecycle, errors, null presence, and compact resources.
5. Differential tests shall preserve existing protocol behavior and document every shared helper and divergence.
6. Fuzz tests shall cover production codecs/state machines and both independent emulators.
7. Race/leak tests shall cover cancellation, slow consumers, failed writes, WebSocket lifecycle, continuation, reload, shutdown, and backend termination.
8. Integration shall prove capability-safe pre-output failover, no retry after visible output, route collision rejection, isolation, and extension binding.
9. Adjacent plugin/generic compatibility contracts shall be revalidated and versioned fixtures updated in the same series.
10. Documentation shall distinguish OpenResponses, OpenAI Responses, and the unrelated `open-responses` project.
11. Completion requires the official suite against a full OpenResponses client → frontend → core → OpenResponses backend → independent reference backend deployment.
12. Release gates shall run focused/default/tagged tests, the complete emulator suites, all 45 FE×BE cells with feature-level positive/negative evidence, race/fuzz/leak checks, architecture tests, `go vet`, static analysis, coverage reports, and a clean `GOWORK=off` build.
13. Coverage percentage shall supplement—not replace—scenario and branch evidence; touched packages shall have no unexplained coverage regression.
14. New deterministic codec, state-machine, and emulator packages shall target at least 90% statement coverage unless a reviewed exception documents generated or unreachable branches.
15. No required matrix cell or feature row may remain `planned`, silently skipped, or lack linked automated evidence at completion.

### Requirement 13: Independent Protocol Emulators and Cross-API Compatibility Matrix

**Objective:** As a maintainer, I want independent black-box protocol evidence and exhaustive canonical translation coverage, so compatibility claims are not tautological or limited to one preferred path.

#### Acceptance Criteria

1. The repository shall add test-only `internal/refclient/openresponses` implementing black-box OpenResponses `2026-04-24` client behavior for JSON, SSE, compact, WebSocket, sequential turns, continuation, errors, tools, multimodal input, assistant phase, reasoning/item lifecycle, and required response presence.
2. The reference client shall not import production OpenResponses frontend/backend/profile codec packages or reuse their encoder/decoder; it may use immutable pinned fixtures and independently maintained schema metadata.
3. The repository shall add test-only `internal/refbackend/openresponses` implementing a spec-shaped remote inference endpoint with JSON, SSE, compact, direct WebSocket scenarios, request capture/assertions, scriptable portable and opaque items, tools, reasoning, phases, errors, malformed events, delays, backpressure, and cancellation.
4. The reference backend shall be independent of production backend/profile codec code; architecture tests shall prohibit production imports of either emulator and prohibit emulator imports that make assertions self-referential.
5. OpenResponses shall be added to both `BundledFrontendIDs()` and `BundledBackendIDs()` in `internal/testkit/conformance`, producing an authoritative 5 × 9 = 45-cell Cartesian matrix.
6. Matrix completeness tests shall fail when a frontend/backend is added without every resulting cell and feature row being classified.
7. Each cell shall classify at minimum: JSON text, streaming text, system/developer/instructions, multi-turn history, tools/results/tool choice, multimodal input, assistant multimodal output, usage/finish/error mapping, reasoning/replay, assistant phase, item references, continuation, compaction, extensions, cancellation/backpressure, and no-retry-after-visible-output.
8. Every feature outcome shall be one of `lossless`, `documented_deterministic_projection`, `rejected_before_network`, or `out_of_scope` only where no product surface exists.
9. Silent drop, accidental downgrade, unclassified skip, mock-only evidence, and pairwise translator packages are forbidden.
10. The OpenResponses frontend row shall explicitly exercise legacy OpenAI Chat Completions, OpenAI Responses, ACP, Anthropic Messages, Gemini/Vertex, Amazon Bedrock, OpenResponses-compatible, OpenRouter, and NVIDIA backends.
11. The ACP cell shall include a positive prompt-text subset and negative tool/multimodal/nonrepresentable-semantic cases proving zero upstream request.
12. The OpenResponses backend column shall explicitly exercise legacy OpenAI Chat Completions, OpenAI Responses, Anthropic Messages, Gemini/Vertex, and OpenResponses frontends/clients.
13. Existing backends shall consume item-authority OpenResponses calls only through explicit normalized projectors preserving the portable intersection and rejecting phase/replay/compaction/extensions or other unrepresentable semantics before network work.
14. The OpenResponses backend shall consume legacy message-authority calls only through an explicit message-to-ordered-item projector; conflicting authorities shall be invalid.
15. Every viable cell shall test non-streaming and streaming where supported, plus tools and multimodal subsets where representable.
16. Every unsupported feature shall have a negative test proving capability/projector rejection before the reference backend observes a request.
17. Evidence shall include direct `refclient/openresponses` ↔ `refbackend/openresponses` wire tests, refclient → frontend → canonical-stub tests, canonical → backend → refbackend tests, and full client → frontend → core → backend → remote-emulator tests.
18. The official compliance suite shall run on the full independent-emulator deployment path in addition to Go-native matrix tests.
19. Emulator and matrix suites shall use deterministic fixtures, named table cases, seeded randomized cases where useful, virtual clocks, bounded buffers, fuzzing, race/leak checks, malformed/adversarial streams, slow readers/writers, cancellation, failover, commitment, redaction, and reload/shutdown scenarios.
20. Implementation shall not be considered complete while any required cell/feature is planned, skipped without a normative out-of-scope reason, or lacks linked automated evidence.
