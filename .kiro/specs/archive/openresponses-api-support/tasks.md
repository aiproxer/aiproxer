# Implementation Plan

Implementation follows TDD throughout: red → green → refactor. Canonical contracts, projectors, profile fixtures, emulator contracts, matrix metadata, state-machine goldens, storage contracts, and failing conformance cases are committed before production behavior. A task is complete only when its focused validation passes and its observable completion condition is demonstrated.

No task may replace the selected architecture with an OpenAI alias, raw wire tunnel, direct frontend-to-backend call, pairwise translator package, raw upstream continuation ID, arbitrary pass-through, silent semantic drop, or emulator reuse of production codec logic.

Every phase contains no more than five implementation tasks. Phase 8 is the final compatibility and release phase.

## Phase 1: Lock Protocol, Canonical, Projector, and Architecture Contracts

- [x] 1. Establish immutable protocol and brownfield contracts

### 1.1 Pin official profile and dependency evidence

- [x] 1.1 Pin official profile and dependency evidence

- Write failing manifest tests for profile, immutable source commit, schema/compliance digests, Apache-2.0 attribution, and deviations.
- Commit reproducible official fixtures/examples without mutable network download.
- Add dependency policy gates rejecting source-unavailable, unlicensed, or unreviewed unstable runtime modules.
- Keep OpenAI SDK and provider SDKs confined to their existing adapter families.
- **Observable completion:** profile/source/license drift fails focused tests.
- _Requirements: 1.2–1.5, 11.1–11.8, 12.3–12.4_
- _Boundary: protocol source and dependency policy_
- _Depends: none_
- _Validation: `go test ./internal/plugins/protocols/openresponses/... ./internal/archtest/... -run 'Profile|Source|License|Dependency'`_

### 1.2 Characterize existing adapters and the current 32-cell matrix

- [x] 1.2 Characterize existing adapters and the current 32-cell matrix

- Write/lock request, response, streaming, tools, multimodal, reasoning, errors, cancellation, commitment, and route fixtures for all existing frontends/backends.
- Snapshot authoritative frontend/backend lists, current Cartesian cells, ACP exclusions, frontend mounting, backend construction, and reference parity behavior.
- Add differential helpers that compare only deliberately shared semantics.
- **Observable completion:** an existing adapter or matrix-cell behavior change fails before OpenResponses production code exists.
- _Requirements: 1.6, 3.9–3.11, 12.1, 12.5, 13.5–13.9_
- _Boundary: existing adapters and conformance framework_
- _Depends: 1.1_
- _Validation: `go test ./internal/plugins/frontends/... ./internal/plugins/backends/... ./internal/testkit/conformance/... ./internal/archtest/...`_

### 1.3 Define ordered canonical items and normalized walkers

- [x] 1.3 Define ordered canonical items and normalized walkers

- Write failing round-trip/validation tests for messages, references, calls/results, reasoning, compaction, phase, content, status/identity, and opaque extensions.
- Add one-authority tests for item versus legacy message calls.
- Add deterministic bounds and walkers used by capabilities, audit, hooks, redaction, accounting, and diagnostics.
- Keep all types protocol-neutral.
- **Observable completion:** portable trajectories round-trip; conflicting authority and skipped item-form inspection fail.
- _Requirements: 3.1–3.10, 4.3–4.5, 10.7, 11.7_
- _Boundary: `pkg/lipapi`, public SDK contracts, shared walkers_
- _Depends: 1.2_
- _Validation: `go test ./pkg/lipapi/... ./pkg/lipsdk/... ./internal/core/capabilities/... ./internal/core/tokenaccounting/...`_

### 1.4 Define both projector directions and capability/dialect contracts

- [x] 1.4 Define both projector directions and capability/dialect contracts

- Write failing item-authority→legacy-view and legacy-message-authority→ordered-item projector tests.
- Require all-or-nothing deterministic projection with stable rejection reasons.
- Define operation, transport, semantic capability, exact item/reasoning/compaction/extension dialect requirements.
- Prove incompatible candidates are rejected before upstream work and failover retains complete requirements.
- Revalidate/version external backend plugin DTOs and fake connector conformance.
- **Observable completion:** representative portable projections succeed and every unrepresentable semantic fails without invoking a backend.
- _Requirements: 3.11, 4.6–4.8, 7.1, 7.10, 9.9–9.14, 13.8–13.16_
- _Boundary: canonical projectors, candidate admission, plugin ABI_
- _Depends: 1.3_
- _Validation: `go test ./pkg/lipapi/... ./internal/core/capabilities/... ./internal/core/routing/... ./pkg/lipsdk/backendplugin/... -run 'Project|Authority|Dialect|Capability|NoNetwork'`_

### 1.5 Define route claims and continuation contracts

- [x] 1.5 Define route claims and continuation contracts

- Write failing tests for normalized method/path claims, duplicate owners, non-colliding defaults, canonical-path takeover, and diagnostics.
- Define protocol-neutral response ID, scope, continuation record/store, persistence, and recorder contracts.
- Test entropy, isolation, indistinguishable lookup, TTL, cycles, depth/materialization bounds, and idempotent deletion.
- **Observable completion:** in-memory contract implementations pass route and continuation security suites.
- _Requirements: 2.2–2.5, 6.1–6.12, 10.8, 10.12_
- _Boundary: HTTP composition and continuation ports_
- _Depends: 1.3_
- _Validation: `go test ./internal/stdhttp/contract/... ./internal/core/continuation/... ./pkg/lipsdk/...`_

## Phase 2: Build the Production Profile Codec, State Machine, and Emulator Contracts

- [x] 2. Implement production wire behavior and independent-test boundaries

### 2.1 Implement production request/item/content/tool/control codecs

- [x] 2.1 Implement production request/item/content/tool/control codecs

- Start with failing official-example and negative goldens.
- Implement typed portable unions, presence distinctions, tools/tool choice, phase, reasoning, structured outputs, residual controls, and extension precedence.
- Reject unknown unprefixed types and unsupported background/conversation modes.
- **Observable completion:** official fixtures decode/re-encode deterministically without semantic loss.
- _Requirements: 2.6–2.7, 3.2–3.8, 4.1–4.10, 10.7, 10.9, 11.5–11.6_
- _Boundary: production OpenResponses profile codec_
- _Depends: 1.1, 1.3, 1.4_
- _Validation: `go test ./internal/plugins/protocols/openresponses/... -run 'Request|Item|Content|Tool|Control|Presence'`_

### 2.2 Implement complete response and compaction builders

- [x] 2.2 Implement complete response and compaction builders

- Write exhaustive required-presence tests, including absent/null/default distinctions.
- Build resources from envelope metadata, canonical trajectories, usage, controls, errors, and timestamps.
- Preserve ordering, identity, status, phase, reasoning, compaction, annotations, and opaque output.
- **Observable completion:** official response/compact examples validate against the pinned schema.
- _Requirements: 2.8, 5.4–5.10, 7.2, 7.5–7.7, 12.4_
- _Boundary: production resource codec_
- _Depends: 1.3, 1.4_
- _Validation: `go test ./internal/plugins/protocols/openresponses/... -run 'ResponseResource|CompactResource|RequiredPresence'`_

### 2.3 Implement semantic event state machine and SSE framing

- [x] 2.3 Implement semantic event state machine and SSE framing

- Write failing lifecycle/sequence tests for response, items, content, text, refusal, reasoning, function arguments, errors, and terminal ownership.
- Use one state machine for final resource, SSE, and WebSocket event output.
- Add conservative legacy-stream normalization that never invents phase, replay, compaction, structured output, or extensions.
- **Observable completion:** scripted canonical streams produce equivalent JSON and valid SSE with exactly one terminal plus `[DONE]`.
- _Requirements: 4.12, 5.1–5.10, 12.4_
- _Boundary: production event normalizer/state machine/SSE primitives_
- _Depends: 2.1, 2.2_
- _Validation: `go test ./internal/plugins/protocols/openresponses/... -run 'StateMachine|SSE|Lifecycle|Sequence|Terminal|LegacyNormalize'`_

### 2.4 Implement errors, bounds, production fuzz targets, and coverage baseline

- [x] 2.4 Implement errors, bounds, production fuzz targets, and coverage baseline

- Add table tests for HTTP/SSE/WS error mapping and stable internal classification.
- Enforce independent request/resource/event/item/schema/opaque/continuation limits and sanitized messages.
- Add fuzz targets for unions, presence, discriminators, SSE, lifecycle, compact, and errors.
- Establish package coverprofiles and branch/scenario registry; document generated/unreachable branches.
- **Observable completion:** negative corpora are bounded, fuzzing finds no panic/excess allocation, and deterministic production packages have an approved baseline toward ≥90% coverage.
- _Requirements: 2.6, 5.6–5.8, 10.6–10.7, 10.9, 10.12, 12.6, 12.13–12.14_
- _Boundary: production validation, errors, fuzz, coverage_
- _Depends: 2.1–2.3_
- _Validation: `go test -coverprofile=/tmp/openresponses-codec.cover ./internal/plugins/protocols/openresponses/... && go test -fuzz=Fuzz -fuzztime=30s ./internal/plugins/protocols/openresponses/...`_

### 2.5 Define independent emulator contracts, immutable fixtures, and anti-tautology gates

- [x] 2.5 Define independent emulator contracts, immutable fixtures, and anti-tautology gates

- Write failing architecture tests requiring `internal/refclient/openresponses` and `internal/refbackend/openresponses` to remain test-only and independent from production OpenResponses adapters/codecs/state machines.
- Define declarative scenario IDs, immutable official fixture bytes/digests, neutral binary fixtures, request-observation and script contracts, virtual clock interfaces, bounded captures, and cleanup rules.
- Define direct wire, frontend-stub, backend-emulator, and full black-box harness interfaces without implementing production behavior in test packages.
- Add matrix evidence-schema tests for feature outcomes and linked scenario/test artifacts.
- **Observable completion:** importing production codec logic into either emulator, importing emulators from production, or adding unclassified matrix evidence fails architecture/contract tests.
- _Requirements: 11.11, 12.1, 13.1–13.9, 13.15–13.19_
- _Boundary: testkit contracts, immutable testdata, architecture gates_
- _Depends: 1.1, 1.2, 1.4_
- _Validation: `go test ./internal/archtest/... ./internal/testkit/conformance/... ./internal/testkit/openresponses/... -run 'EmulatorBoundary|Fixture|Evidence|MatrixContract|NoTautology'`_

## Phase 3: Implement Client-Facing HTTP and SSE

- [x] 3. Add the non-colliding OpenResponses HTTP/SSE service

### 3.1 Implement strict frontend configuration and route ownership

- [x] 3.1 Implement strict frontend configuration and route ownership

- Test profile/path/WS/continuation/origin/unknown fields and every route collision.
- Register immutable claims before handlers and expose sanitized diagnostics.
- **Observable completion:** default coexistence works and conflicting `/v1` fails before serving.
- _Requirements: 1.4, 1.7, 2.1–2.5, 10.1, 10.3, 10.5, 10.11_
- _Boundary: frontend config and HTTP composition_
- _Depends: 1.5, 2.1_
- _Validation: `go test ./internal/plugins/frontends/openresponses/... ./internal/stdhttp/... -run 'Config|Route|Collision|Coexist'`_

### 3.2 Implement authenticated create decode and canonical construction

- [x] 3.2 Implement authenticated create decode and canonical construction

- Test authentication/authority before body/state work.
- Decode string/items, tools, controls, metadata policy, extensions, conditional model/input, and limits.
- Construct one authoritative item trajectory and complete requirements.
- **Observable completion:** valid fixtures reach a canonical stub exactly; invalid/unauthorized requests cause zero executor/store work.
- _Requirements: 2.6–2.7, 2.10, 3.1–3.5, 4.1, 4.9–4.10, 10.7–10.9_
- _Boundary: frontend decode adapter_
- _Depends: 2.1, 3.1_
- _Validation: `go test ./internal/plugins/frontends/openresponses/... -run 'Decode|Auth|Authority|CreateRequest|NoWork'`_

### 3.3 Implement complete non-streaming response envelopes

- [x] 3.3 Implement complete non-streaming response envelopes

- Test proxy IDs, timestamps, model/parent linkage, required presence, output order, usage, errors, and incomplete state.
- Execute through standard core and build via the shared state machine.
- Keep native IDs private.
- **Observable completion:** all official non-streaming scenarios pass through a canonical stub.
- _Requirements: 2.8–2.9, 5.5, 5.9–5.10, 6.1, 10.6–10.7_
- _Boundary: frontend JSON adapter_
- _Depends: 2.2, 2.3, 3.2_
- _Validation: `go test ./internal/plugins/frontends/openresponses/... -run 'NonStreaming|ResponseResource|ProxyID|RequiredFields'`_

### 3.4 Implement incremental SSE and terminal ownership

- [x] 3.4 Implement incremental SSE and terminal ownership

- Test event order, lifecycle, errors, `[DONE]`, slow clients, writer failures, cancellation, and first-output commitment.
- Stream incrementally and close backend/recorder/HTTP resources exactly once.
- **Observable completion:** official streaming scenarios pass under bounded slow consumption with no retry or leaks.
- _Requirements: 5.1–5.10, 10.7, 10.10, 12.7–12.8_
- _Boundary: frontend SSE and terminal wrapper_
- _Depends: 2.3, 2.4, 3.2_
- _Validation: `go test -race ./internal/plugins/frontends/openresponses/... -run 'SSE|Backpressure|Cancel|WriteFailure|Commit|Terminal'`_

### 3.5 Prove frontend isolation and differential behavior

- [x] 3.5 Prove frontend isolation and differential behavior

- Mount all existing frontends plus OpenResponses and assert routes, resources, errors, identity, and unsupported features remain separate.
- Run existing adapter characterization unchanged.
- **Observable completion:** body/header/user-agent changes cannot switch protocol behavior and existing fixtures remain green.
- _Requirements: 1.1, 1.6, 1.8, 2.4–2.5, 2.9, 12.5_
- _Boundary: cross-frontend integration_
- _Depends: 3.3, 3.4_
- _Validation: `go test ./internal/stdhttp/... ./internal/plugins/frontends/... -run 'Coexist|Differential|Isolation|Regression'`_

## Phase 4: Implement Continuation and Compaction

- [x] 4. Add proxy-owned response state and compact routing

### 4.1 Implement bounded continuation storage and proxy IDs

- [x] 4.1 Implement bounded continuation storage and proxy IDs

- Test reserve/put/get/delete, scope, TTL, restart, cleanup, storage failure, entropy, limits, and incomplete eligibility.
- Store canonical trajectory, requirements, lineage, status, and protected native refs.
- **Observable completion:** memory/durable contract implementations pass isolation/restart/expiry/security tests.
- _Requirements: 6.1, 6.3, 6.8–6.12, 10.6–10.7, 10.12_
- _Boundary: continuation service and stores_
- _Depends: 1.5, 3.3, 3.4_
- _Validation: `go test -race ./internal/core/continuation/... ./internal/infra/continuation/... -run 'Store|Scope|TTL|Restart|Limit|ID'`_

### 4.2 Implement materialization, recording, and routing constraints

- [x] 4.2 Implement materialization, recording, and routing constraints

- Test previous-input→previous-output→new-input order, portable rerouting, provider-bound pinning, native optimization, and canonical fallback.
- Add incremental terminal recorder without changing commitment.
- **Observable completion:** portable history reroutes safely; nonportable history pins/rejects; client IDs never reach generic providers.
- _Requirements: 4.6–4.8, 6.2–6.7, 6.12, 12.8_
- _Boundary: materializer/recorder and candidate requirements_
- _Depends: 1.4, 4.1_
- _Validation: `go test ./internal/core/continuation/... ./internal/core/routing/... -run 'Materialize|Portable|Pinned|Native|Record|Failover'`_

### 4.3 Integrate HTTP continuation and storage policy

- [x] 4.3 Integrate HTTP continuation and storage policy

- Test `store:true`, defaults, success, missing/expired/unauthorized/incompatible IDs, parent echo, and unauthorized zero-work behavior.
- **Observable completion:** official multi-turn works with new input only and all non-resolvable parents fail identically.
- _Requirements: 2.7, 6.1–6.3, 6.8–6.10, 10.8–10.9_
- _Boundary: frontend-continuation integration_
- _Depends: 4.2_
- _Validation: `go test ./internal/plugins/frontends/openresponses/... ./internal/core/continuation/... -run 'PreviousResponse|StoreTrue|NotFound|Scope'`_

### 4.4 Implement compaction through normal execution

- [x] 4.4 Implement compaction through normal execution

- Test model-required validation, input, eligibility, failover, usage, required resource, later new chain, and unsupported capability.
- Route through the normal executor/backend stream port.
- **Observable completion:** official compact cases and two-candidate failover preserve exact output/usage without frontend shortcuts.
- _Requirements: 7.1–7.9, 12.2, 12.8_
- _Boundary: compact frontend, neutral operation, executor integration_
- _Depends: 1.4, 2.2, 4.2_
- _Validation: `go test ./internal/plugins/frontends/openresponses/... ./internal/core/runtime/... ./internal/core/routing/... -run 'Compact|MissingModel|Failover|NewChain'`_

### 4.5 Harden continuation/compaction security and lifecycle

- [x] 4.5 Harden continuation/compaction security and lifecycle

- Test probing, fixation, cycles, amplification, expired native refs, persistence/cancellation failure, redaction, reload, and shutdown.
- **Observable completion:** race/leak suites leave no record/stream/goroutine/lock/native-ref leak and errors remain bounded.
- _Requirements: 6.8–6.12, 7.8, 10.10–10.12, 12.6–12.8_
- _Boundary: continuation/compaction robustness_
- _Depends: 4.3, 4.4_
- _Validation: `go test -race ./internal/core/continuation/... ./internal/infra/continuation/... ./internal/plugins/frontends/openresponses/... -run 'Security|Race|Leak|Reload|Shutdown'`_

## Phase 5: Implement the Generic Remote OpenResponses Backend

- [x] 5. Add standards-only remote provider/router connectivity

### 5.1 Implement strict configuration and factory registration

- [x] 5.1 Implement strict configuration and factory registration

- Test stable kind, independent IDs/prefixes, profile, endpoint, env credentials, no-auth, inventory, capabilities/dialects, limits, unknown fields, and collisions.
- Reuse shared infrastructure without provider SDKs or external plugin requirements.
- **Observable completion:** multiple independent instances construct correctly and root builds without external modules.
- _Requirements: 9.1–9.6, 9.9, 10.2–10.5, 11.9–11.10_
- _Boundary: built-in-compatible backend composition_
- _Depends: 1.4, 2.1, 3.1_
- _Validation: `go test ./internal/standardplugins/... ./internal/plugins/backends/openresponsescompat/... -run 'Config|Factory|NoAuth|Instance|Prefix|Caps' && GOWORK=off go build ./cmd/lipstd`_

### 5.2 Implement item-authority request and JSON response mapping

- [x] 5.2 Implement item-authority request and JSON response mapping

- Test ordered portable items, controls, tools/results, phase, reasoning, extensions, endpoints, and unsupported semantics.
- Never forward proxy IDs or arbitrary fields.
- Parse complete resources to canonical lifecycle/usage/status/error/native evidence.
- **Observable completion:** an independent request observer receives exact schema-valid JSON and nonrepresentable calls cause zero requests.
- _Requirements: 4.9–4.11, 6.6–6.7, 9.3–9.10, 13.13, 13.16–13.17_
- _Boundary: backend item-authority JSON adapter_
- _Depends: 2.1, 2.2, 5.1_
- _Validation: `go test ./internal/plugins/backends/openresponsescompat/... -run 'ItemAuthority|Request|NonStreaming|Mapping|Unsupported|NoNetwork'`_

### 5.3 Implement legacy-message-authority projection into ordered requests

- [x] 5.3 Implement legacy-message-authority projection into ordered requests

- Start with failing calls produced by OpenAI Chat, OpenAI Responses, Anthropic, and Gemini frontend canonical fixtures.
- Use the explicit legacy→item projector; preserve order, instructions, tools/results, multimodal parts, controls, and exact replay requirements.
- Reject conflicts and incompatible source extensions/replay before network work.
- **Observable completion:** all four legacy frontend fixture families create correct ordered OR requests and all negative fixtures show zero upstream requests.
- _Requirements: 3.9–3.11, 9.13–9.14, 13.12, 13.14, 13.16–13.17_
- _Boundary: backend legacy-authority constructor/projector_
- _Depends: 1.4, 5.2_
- _Validation: `go test ./internal/plugins/backends/openresponsescompat/... ./internal/testkit/conformance/... -run 'LegacyAuthority|LegacyToItems|Conflict|ReplayDialect|NoNetwork'`_

### 5.4 Implement SSE mapping, errors, commitment, and remote compaction

- [x] 5.4 Implement SSE mapping, errors, commitment, and remote compaction

- Test every claimed event, unknown prefixed output, malformed/unprefixed data, event mismatch, duplicate terminal, slow consumers, cancellation, provider disconnect, compact endpoint, usage, and dialect output.
- Peek first canonical event before commitment and retain no-retry-after-output policy.
- **Observable completion:** incremental streams/compact preserve semantics under backpressure and classify pre/post-output failures correctly.
- _Requirements: 4.5–4.8, 5.5–5.8, 7.3–7.10, 9.7–9.11, 12.7–12.8_
- _Boundary: backend SSE/error/compact adapter_
- _Depends: 2.3, 2.4, 5.2_
- _Validation: `go test -race ./internal/plugins/backends/openresponsescompat/... ./internal/core/routing/... -run 'SSE|Extension|Backpressure|Cancel|Commit|Retry|Compact'`_

### 5.5 Enforce provider-specific boundaries and inventory provenance

- [x] 5.5 Enforce provider-specific boundaries and inventory provenance

- Define/test a narrow codec customization seam for provider connectors.
- Prove generic config rejects OpenRouter attribution/routing/billing/catalog/proprietary controls.
- Preserve instance/factory/profile/model/capability/inventory provenance.
- **Observable completion:** provider wrappers reuse explicit options while architecture tests prevent reverse imports/policy leakage.
- _Requirements: 1.7, 4.11, 9.2, 9.4, 9.12, 10.5, 11.9, 12.9_
- _Boundary: codec options, provider boundary, inventory_
- _Depends: 5.1–5.4_
- _Validation: `go test ./internal/plugins/backends/openresponsescompat/... ./internal/core/modelregistry/... ./internal/archtest/... -run 'ProviderBoundary|OpenRouter|Inventory|Provenance'`_

## Phase 6: Implement Client-Facing WebSocket

- [x] 6. Add persistent sequential OpenResponses sessions

### 6.1 Implement authenticated upgrade, origin policy, and limits

- [x] 6.1 Implement authenticated upgrade, origin policy, and limits

- Test non-upgrade GET, auth, origins, dev relaxation, routing, message limits, idle/ping/pong, and max age.
- Allocate local continuation only after authorization.
- **Observable completion:** invalid attempts allocate no session/state; valid clients establish bounded sessions.
- _Requirements: 8.1, 8.8–8.9, 8.12, 10.1, 10.7–10.8_
- _Boundary: frontend WS upgrade/transport_
- _Depends: 3.1, 4.1_
- _Validation: `go test ./internal/plugins/frontends/openresponses/... -run 'WebSocketUpgrade|Auth|Origin|Limit|Ping|Age'`_

### 6.2 Implement sequential turn execution and shared event output

- [x] 6.2 Implement sequential turn execution and shared event output

- Test valid/invalid create envelopes, forbidden fields, one active turn, bounded queue, sequential reuse, and event parity.
- Use the same executor and state machine as HTTP/SSE.
- **Observable completion:** basic/sequential scenarios pass and concurrent creates cannot create two active streams.
- _Requirements: 8.2–8.4, 8.9, 8.11, 12.2_
- _Boundary: WS turn runner_
- _Depends: 2.3, 3.2, 6.1_
- _Validation: `go test -race ./internal/plugins/frontends/openresponses/... -run 'WebSocketTurn|Sequential|NoMultiplex|EventParity'`_

### 6.3 Implement connection-local continuation and classified eviction

- [x] 6.3 Implement connection-local continuation and classified eviction

- Test recording, continuation with new input, reconnect loss, missing parent, 4xx/5xx eviction, transport/cancel retention, success retention, bounds, and close cleanup.
- Integrate compact output as a new base without old response ID.
- **Observable completion:** every pinned continuation/reconnect/eviction/compact-new-chain scenario passes.
- _Requirements: 6.9, 7.7, 8.5–8.9, 12.2_
- _Boundary: local continuation and WS turn integration_
- _Depends: 4.2, 4.4, 6.2_
- _Validation: `go test ./internal/plugins/frontends/openresponses/... -run 'WebSocketContinuation|Reconnect|Evict|Retain|CompactNewChain|StoreFalse'`_

### 6.4 Implement connection-age, disconnect, and terminal cleanup

- [x] 6.4 Implement connection-age, disconnect, and terminal cleanup

- Test age expiry/error, idle close, disconnect before/after output, writer failure, backend block, shutdown, and reload generation.
- Close all resources exactly once.
- **Observable completion:** closure paths terminate promptly with no duplicate terminal, cross-generation work, or leaks.
- _Requirements: 8.8–8.10, 10.10–10.11, 12.7–12.8_
- _Boundary: WS lifecycle and runtime generation_
- _Depends: 6.2, 6.3_
- _Validation: `go test -race ./internal/plugins/frontends/openresponses/... ./internal/infra/runtimebundle/... -run 'WebSocketClose|ConnectionLimit|Disconnect|Reload|Shutdown|Leak'`_

### 6.5 Fuzz and stress the WebSocket state machine

- [x] 6.5 Fuzz and stress the WebSocket state machine

- Fuzz malformed JSON/unions/fields/UTF-8/frames/repeated creates/errors/lifecycle.
- Stress slow IO, queue/local-state saturation, virtual one-hour age, and cancellation races.
- **Observable completion:** no panic/race/deadlock/unbounded queue/duplicate execution/retained state.
- _Requirements: 8.3, 8.8–8.10, 10.7, 10.10, 12.6–12.7_
- _Boundary: WS robustness_
- _Depends: 6.4_
- _Validation: `go test -race ./internal/plugins/frontends/openresponses/... -run 'WebSocketStress|Queue|Slow' && go test -fuzz=FuzzWebSocketDecodeTurn -fuzztime=5s ./internal/plugins/frontends/openresponses/...`_

## Phase 7: Integrate the Base Protocol and Prove Architecture Readiness

- [x] 7. Complete standard-runtime integration before exhaustive compatibility certification

### 7.1 Integrate registration, diagnostics, reload, and shutdown

- [x] 7.1 Integrate registration, diagnostics, reload, and shutdown

- Register frontend/backend with immutable route/prefix ownership.
- Add sanitized profile/origin/path/endpoint/transport/continuation/capability/inventory/conformance diagnostics.
- Integrate state and WS ownership into rollback, atomic reload, drain, and reverse shutdown.
- **Observable completion:** enable/disable/reload leaves no orphaned route/store/backend/socket/inventory state.
- _Requirements: 1.7, 2.3, 9.2, 10.1–10.6, 10.10–10.11_
- _Boundary: standard composition and lifecycle_
- _Depends: 3.5, 4.5, 5.5, 6.4_
- _Validation: `go test -race ./internal/standardplugins/... ./internal/stdhttp/... ./internal/infra/runtimebundle/... ./internal/core/diag/... -run 'OpenResponses|Reload|Rollback|Shutdown|Inspect'`_

### 7.2 Complete external backend plugin ABI integration

- [x] 7.2 Complete external backend plugin ABI integration

- Implement versioned DTO/host changes for ordered items, requirements, create, compaction, opaque output, and operation capabilities.
- Update fake external connectors and reject unsupported old versions before execution.
- **Observable completion:** built-in generic and external fake connector pass equivalent canonical create/compact contracts.
- _Requirements: 7.10, 9.1, 9.9, 11.7, 11.9–11.10, 12.9_
- _Boundary: public backend plugin ABI and host_
- _Depends: 1.4, 5.4_
- _Validation: `go test ./pkg/lipsdk/backendplugin/... ./internal/infra/backendplugins/... ./internal/archtest/... && GOWORK=off go build ./cmd/lipstd`_

### 7.3 Build the reusable full-path conformance deployment harness

- [x] 7.3 Build the reusable full-path conformance deployment harness

- Compose configurable client entrypoint, all real frontends, core, all real backends, and injectable reference-provider origins.
- Add hooks for the independent OpenResponses refclient/refbackend contracts from Task 2.5 and existing reference families.
- Support JSON, SSE, compact, WS client transport, request counters, multiple candidates, credential/failure injection, virtual clocks, and bounded artifacts.
- Run base smoke with contract fakes; final independent-emulator evidence is deferred to Phase 8.
- **Observable completion:** one deterministic harness can select any authoritative FE×BE cell without bespoke pairwise wiring.
- _Requirements: 12.1–12.4, 12.8, 13.5–13.9, 13.15–13.18_
- _Boundary: end-to-end test deployment/harness_
- _Depends: 7.1, 7.2, 2.5_
- _Validation: `go test ./internal/testkit/conformance/... ./internal/integration/openresponses/... -run 'Harness|CellSelect|JSON|SSE|Compact|WebSocket|FailureInjection'`_

### 7.4 Execute base security, failover, and existing-adapter regression proofs

- [x] 7.4 Execute base security, failover, and existing-adapter regression proofs

- Test extension smuggling, incompatible failover, ID probing/native leakage, route collision, origin abuse, amplification, event injection, and opaque replay.
- Prove pre-output classification and post-output commitment.
- Run all existing protocol differential/matrix fixtures unchanged.
- **Observable completion:** no sensitive/native leakage, illegal retry/failover, or existing-adapter regression.
- _Requirements: 4.6–4.8, 6.9, 10.6, 10.8–10.9, 10.12, 12.5, 12.7–12.8_
- _Boundary: base cross-cutting security/regression_
- _Depends: 7.3_
- _Validation: `go test -race ./internal/integration/openresponses/... ./internal/core/... ./internal/plugins/... -run 'Security|Failover|Commitment|Redaction|Regression'`_

### 7.5 Run base architecture and production-package quality gates

- [x] 7.5 Run base architecture and production-package quality gates

- Run focused/default/tagged tests, changed-package races, production fuzz smoke, architecture tests, vet, and required static analysis.
- Build with `GOWORK=off`, no external connector modules, no JavaScript, and OpenResponses enabled/disabled.
- Verify source notices, reproducibility, no mutable generation/provider SDK leakage, and production package coverage baseline/no unexplained regression.
- This is not the final compatibility release gate; Task 8.5 is final.
- **Observable completion:** base production implementation is architecture-green and ready for independent-emulator certification.
- _Requirements: 1.8, 11.1–11.10, 12.1, 12.3, 12.6–12.14_
- _Boundary: production architecture/quality gates_
- _Depends: 7.4_
- _Validation: `go test ./... && go test -race ./internal/plugins/frontends/openresponses/... ./internal/plugins/backends/openresponsescompat/... ./internal/core/continuation/... && go vet ./... && GOWORK=off go build ./cmd/lipstd`_

## Phase 8: Implement Independent Emulators and Prove Cross-API Compatibility

- [x] 8. Complete independent interoperability, all translation paths, and final release evidence

### 8.1 Build the independent OpenResponses reference client emulator

- [x] 8.1 Build the independent OpenResponses reference client emulator

- Begin with fixture/parser tests independent of production OpenResponses code.
- Implement HTTP JSON, SSE, compact, WebSocket sequential turns/continuation/errors, tools, multimodal, phase, reasoning/item lifecycle, extensions, required presence, cancellation, and slow-consumer behavior.
- Use immutable official fixtures/schema metadata, independent wire structs/parsers, bounded reads, deterministic clocks/IDs where applicable, and table-driven scenario IDs.
- Add fuzzing and architecture tests proving no production codec/adapter imports.
- **Observable completion:** the client independently creates/parses every pinned official scenario and rejects malformed responses without panic/leak or production-code dependency.
- _Requirements: 11.11, 12.2–12.4, 12.6–12.7, 12.13–12.14, 13.1–13.2, 13.4, 13.15, 13.17–13.19_
- _Boundary: `internal/refclient/openresponses` and immutable testdata_
- _Depends: 2.5, 7.3_
- _Validation: `go test -race -coverprofile=/tmp/refclient-openresponses.cover ./internal/refclient/openresponses/... && go test -fuzz=Fuzz -fuzztime=30s ./internal/refclient/openresponses/... && go test ./internal/archtest/... -run 'OpenResponsesRefClientBoundary'`_

### 8.2 Build the independent OpenResponses remote backend emulator

- [x] 8.2 Build the independent OpenResponses remote backend emulator

- Begin with independent request-validation and script-state tests.
- Implement JSON/SSE/compact/direct-WS modes, request capture/assertions, portable and opaque items, tools, reasoning, phases, extensions, required presence, auth/rate-limit/4xx/5xx/disconnect, malformed event/resource/content-type modes, virtual delays, slow writes, backpressure, and cancellation observation.
- Add atomic request counters/redacted bounded captures for zero-upstream tests.
- Add fuzz/race/leak tests and architecture gates prohibiting production codec imports.
- Run direct `refclient/openresponses` ↔ `refbackend/openresponses` official and adversarial wire scenarios.
- **Observable completion:** the emulator deterministically serves valid/invalid scripts, observes cancellation, remains bounded, and passes direct independent-client interoperability.
- _Requirements: 11.11, 12.2–12.4, 12.6–12.7, 12.13–12.14, 13.3–13.4, 13.15, 13.17–13.19_
- _Boundary: `internal/refbackend/openresponses` and direct wire suite_
- _Depends: 2.5, 8.1_
- _Validation: `go test -race -coverprofile=/tmp/refbackend-openresponses.cover ./internal/refbackend/openresponses/... ./internal/refclient/openresponses/... -run 'DirectWire|JSON|SSE|Compact|WebSocket|Malformed|Cancel|Leak' && go test -fuzz=Fuzz -fuzztime=30s ./internal/refbackend/openresponses/... && go test ./internal/archtest/... -run 'OpenResponsesRefBackendBoundary'`_

### 8.3 Implement and test the complete OpenResponses frontend compatibility row (complete)

- [x] 8.3 Implement and test the complete OpenResponses frontend compatibility row

- Add/finish explicit item-authority projectors and capability declarations for every existing backend.
- Run named positive and negative scenarios for:
  - OpenResponses → legacy OpenAI Chat Completions;
  - OpenResponses → OpenAI Responses;
  - OpenResponses → ACP positive text/resource subset and negative tools/multimodal/phase/replay/compaction/extensions;
  - OpenResponses → Anthropic Messages;
  - OpenResponses → Gemini/Vertex;
  - OpenResponses → Amazon Bedrock;
  - OpenResponses → OpenResponses-compatible;
  - OpenResponses → OpenRouter; and
  - OpenResponses → NVIDIA.
- For each cell test JSON text, streaming where supported, tools/multimodal where representable, usage/errors/commitment, and zero-request rejection for unsupported features.
- Link every feature outcome to scenario/test artifacts.
- **Observable completion:** all nine row cells are classified and green; no required feature is planned/silent/unlinked and every rejection shows zero remote requests.
- _Requirements: 3.11, 4.6–4.8, 12.8, 12.15, 13.5–13.11, 13.13, 13.15–13.17, 13.20_
- _Boundary: canonical item→target projectors, existing backend adapters, FE×BE row evidence_
- _Depends: 1.4, 7.3, 8.2_
- _Validation: `go test -tags=precommit,integration ./internal/testkit/conformance/... ./internal/integration/openresponses/... -run 'FrontendRow|OpenResponsesTo|ACPSubset|NoNetwork|Streaming|Tools|Multimodal|Commitment'`_

### 8.4 Implement and test the complete OpenResponses backend compatibility column

- [x] 8.4 Implement and test the complete OpenResponses backend compatibility column

- Add/finish the legacy-message-authority→ordered-item constructor/projector and exact replay/dialect checks.
- Run real frontend/client wire scenarios for:
  - legacy OpenAI Chat Completions → OpenResponses-compatible;
  - OpenAI Responses → OpenResponses-compatible;
  - Anthropic Messages → OpenResponses-compatible;
  - Gemini/Vertex → OpenResponses-compatible; and
  - OpenResponses → OpenResponses-compatible.
- Test JSON/SSE, tools/results, multimodal, system/instructions, usage/errors, cancellation/backpressure, and commitment where representable.
- Add negative conflicting-authority, provider-replay, extension, and unsupported-content cases with zero reference-backend requests.
- **Observable completion:** all five column cells are classified and green through independent refbackend capture, with exact ordered requests and no silent drop.
- _Requirements: 3.9–3.11, 9.13–9.14, 12.8, 12.15, 13.5–13.9, 13.12, 13.14–13.17, 13.20_
- _Boundary: legacy→item projector, OpenResponses backend, FE×BE column evidence_
- _Depends: 5.3, 7.3, 8.2_
- _Validation: `go test -tags=precommit,integration ./internal/testkit/conformance/... ./internal/integration/openresponses/... -run 'BackendColumn|ToOpenResponses|LegacyToItems|ReplayDialect|Conflict|NoNetwork|Streaming|Tools|Multimodal'`_

### 8.5 Extend and enforce the complete Cartesian matrix and final quality gates

- [x] 8.5 Extend and enforce the complete Cartesian matrix and final quality gates

- Add `openresponses` to both authoritative lists, mount/construct it in conformance helpers, and assert exactly 45 unique deterministic cells.
- Require feature-level evidence for JSON, streaming, roles/history, tools, multimodal, usage/errors, reasoning/replay, phase, item refs, continuation, compaction, extensions, cancellation/backpressure, failover, and commitment.
- Run all 45 baseline text cells, every supported streaming cell, positive tools/multimodal cases, all required negative pre-network cases, direct emulator wire suites, frontend-stub/backend-emulator tests, full black-box deployment, and the pinned official suite.
- Run fuzz/race/leak/security/regression, coverage/no-regression with ≥90% target for new deterministic codec/state-machine/emulator packages or reviewed exceptions, architecture tests, vet/static analysis, and clean `GOWORK=off` build.
- Fail release when any required cell/feature is planned, silently skipped, unclassified, or lacks linked evidence.
- **Observable completion:** all 45 cells and required feature rows are machine-green; official compliance passes on independent client→frontend→core→OpenResponses backend→independent provider; all final quality gates pass from a clean checkout.
- _Requirements: 1.8, 11.1–11.11, 12.1–12.15, 13.5–13.20_
- _Boundary: authoritative matrix, full black-box compliance, repository-wide final release gate_
- _Depends: 8.1–8.4_
- _Validation (Windows race adaptation): `go test ./... && make parity-checks && make test-precommit-extra && ./scripts/test-openresponses-compliance.ps1 && go vet ./... && GOWORK=off go build ./cmd/lipstd` (Linux/macOS additionally run `go test -race ./internal/refclient/openresponses/... ./internal/refbackend/openresponses/... ./internal/plugins/frontends/openresponses/... ./internal/plugins/backends/openresponsescompat/... ./internal/core/continuation/...` and `./scripts/test-openresponses-compliance.sh`; `make test-race` is skipped on Windows per repository policy)._
- **Matrix architecture note:** the authoritative 5×9 = 45 cells use `openresponses` in both `BundledFrontendIDs()` and `BundledBackendIDs()`. OpenRouter/NVIDIA are authoritative compatibility identities driven through the classified configured OpenAI-compatible provider-mode path (`DeployConfiguredProviderMode`); they remain optional connectors and are never promoted to essential backend kinds (drivers: `DriverBase` / `DriverConfiguredProviderMode`, evidence in `matrix_evidence_45.go`).
