# Implementation Plan

## Task Format Note
- `(P)` means parallel-safe with peers under the same parent once earlier groups are done.
- `_Boundary_` names the design component; `_Depends_` declares non-obvious cross-group dependencies; `_Validation_` names the focused proof command.
- Each task writes its failing tests first (TDD) and carries only the tests that prove it.

- [x] 1. Canonical decision contract
- [x] 1.1 Define typed decision requests and results with their invariants
  - Add the decision request, question, option, level, result and answer types, the decision reject error and its predicate, as specified in design "DecisionContract".
  - Request validation rejects unknown kinds, empty or duplicate question IDs, duplicate option names, choices outside 1–255 options, scores outside 2–10 levels, and invalid JSON values, reporting the offending field.
  - Result validation accepts only one answer per question in request order, finite values in range, distributions summing to 1 within tolerance, a maximal-probability selected option, a zero-based expected score within range, and confidence only when present and in [0, 1].
  - Observable completion: table tests for every rejection case in requirements 1.3 and 2.2 pass, and a valid three-type request with a matching result validates.
  - _Requirements: 1.2, 1.3, 2.1, 2.2, 2.3_
  - _Boundary: DecisionContract_
  - _Validation: go test ./pkg/lipapi/ -run 'Decision'_

- [x] 1.2 Carry decisions on canonical calls and streams
  - Add the decision field to calls with exclusive authority (no messages, instructions, items, previous response ID, tools or tool choice alongside it) and deep cloning; validation does not read invocation metadata.
  - Add the protocol-neutral `decision.evaluate` operation, the `decisions` capability derived whenever a call carries a decision, and the decision result event kind with its payload.
  - Sequence validation accepts the decision result after `response_started`, without `message_started`, at most once; the decision result commits output.
  - Observable completion: tests show a decision call validates, a decision mixed with messages fails, required capabilities include `decisions`, the four-event stream validates, and the result event commits output.
  - _Requirements: 2.1, 3.1, 3.5_
  - _Boundary: DecisionContract_
  - _Validation: go test ./pkg/lipapi/_

- [x] 2. Core touchpoints
- [x] 2.1 (P) Size billing quotes for decision calls
  - Extend the default request-size estimator to count decision evidence, instruction and criteria bytes for calls carrying a decision; leave chat estimates unchanged.
  - Observable completion: a decision call yields an available, non-zero estimate proportional to its payload, and existing estimator tests still pass.
  - _Requirements: 4.3, 4.4_
  - _Boundary: CoreTouchpoints_
  - _Validation: go test ./internal/core/modelcatalog/_

- [x] 2.2 (P) Fail closed for decisions while a secret guard is active
  - When the secret-guard plane has guards and the call carries a decision, return a policy-denied error before any guard runs or any upstream work starts; with no guards configured, decisions proceed.
  - Observable completion: executor tests show a policy-denied outcome with guards present and normal progress without guards.
  - _Requirements: 5.1_
  - _Boundary: CoreTouchpoints_
  - _Validation: go test ./internal/core/runtime/ -run 'SecretGuard'_

- [x] 2.3 (P) Refuse decision calls at the executable-connector bridge
  - The host-side connector invocation builder rejects a call carrying a decision with a capability reject instead of silently dropping it.
  - Observable completion: an adapter test shows a decision call is refused and a chat call converts unchanged.
  - _Requirements: 3.1, 3.5_
  - _Boundary: CoreTouchpoints_
  - _Validation: go test ./internal/infra/backendplugins/adapter/_

- [x] 3. System One-compatible backend family
- [x] 3.1 (P) Translate decisions to and from the System One upstream wire
  - Build the upstream body (candidate native model, original evidence bytes, questions in request order with original criteria) and send only authorization, content type and profile safe headers.
  - Parse bounded responses (1 MiB cap, no trailing data), drop fields outside the System One contract, align answers to request order, run result validation, and emit started, decision result, usage and finished events.
  - Map `usage.input_tokens`/`output_tokens` with presence and an upstream `usage.cost` to provider-reported cost evidence.
  - Observable completion: `httptest` cases show the exact request body and headers, an OpenRouter-style response with extra fields and cost producing the expected events, and an invalid answer rejected before any event.
  - _Requirements: 1.1, 2.2, 2.4, 4.1, 4.2, 5.3_
  - _Boundary: SystemOneBackend_
  - _Depends: 1.2_
  - _Validation: go test ./internal/plugins/backends/systemonecompat/ -run 'Wire'_

- [x] 3.2 Classify upstream failures and expose the backend
  - Classify upstream statuses: 401, 402, 403, 404, 429, 5xx, 524, 529, transport errors, timeouts and invalid answers are recoverable pre-output errors; 400, 413 and 422 become terminal decision rejects carrying only bounded field, message and type; cancellation is never retried.
  - Build the backend from compatible-mode config with only the `decisions` capability, non-streaming transport, static model inventory, and an open path that refuses other operations; log only backend, model, status and error category.
  - Observable completion: a status table test passes, a chat call to this backend fails negotiation, and logs captured in tests contain no request or answer content.
  - _Requirements: 3.2, 3.3, 3.5, 5.2_
  - _Boundary: SystemOneBackend_
  - _Validation: go test ./internal/plugins/backends/systemonecompat/_

- [x] 3.3 Register the family and its catalog profiles
  - Add the `systemone-compatible` family (capabilities `decisions`, static discovery required) and its factory kind binding, the `custom-systemone-compatible` backend contribution, and the profile family builder.
  - Add catalog profiles for TypeSafe, OpenRouter and Command Code System One endpoints with bearer environment credentials and static models, and update the catalog inventory expectations.
  - Observable completion: catalog compile and inventory tests list the three profiles under the new family, and a profile-referenced instance builds a decision-capable backend.
  - _Requirements: 3.4, 3.5_
  - _Boundary: SystemOneProfiles_
  - _Validation: go test ./internal/providerprofiles/ ./internal/standardplugins/_

- [x] 4. System One frontend
- [x] 4.1 (P) Decode System One requests without losing order or type
  - Apply bounded JSON preflight (depth, duplicate keys, trailing data), then decode `model`, `state` and ID-keyed `questions` preserving question, option and level order and the original evidence bytes.
  - Reject unknown fields, missing required fields and question-count overflow with a 422 detail naming the field; build a call carrying only the decision, the `decision.evaluate` operation, non-streaming delivery and route intent, with no client header copied.
  - Observable completion: golden fixtures for string, object and array state and for all three question types decode to the expected call, a swapped option order survives, and each rejection case maps to its field.
  - _Requirements: 1.2, 1.3, 1.4, 5.3_
  - _Boundary: SystemOneFrontend_
  - _Depends: 1.2_
  - _Validation: go test ./internal/plugins/frontends/systemone/ -run 'Decode'_

- [x] 4.2 (P) Encode answers and System One error envelopes
  - Collect the canonical stream to completion and write `{model, answers, usage}` with type-specific answer shapes, zero-based string keys, a legend from the request's level order, and confidence only when present.
  - Write 422 `detail[]` for decision rejects and `{"detail": message}` for other outcomes, reusing executor error classification for statuses.
  - Observable completion: encoding tests reproduce the documented TypeSafe response for all three types, omit confidence for noul, and emit the expected envelope per error class.
  - _Requirements: 1.1, 2.1, 2.3, 2.4, 3.1, 3.3_
  - _Boundary: SystemOneFrontend_
  - _Depends: 1.2_
  - _Validation: go test ./internal/plugins/frontends/systemone/ -run 'Encode|Error'_

- [x] 4.3 Mount the frontend on `POST /v1/systemone`
  - Add the route claim, plugin config (`max_questions`, default 256), mount and handler on the shared create pipeline with body-model route selection, and register the frontend contribution and route claims in the standard distribution.
  - Observable completion: a handler test shows an authenticated request reaches the executor and an unauthenticated one gets the standard authentication outcome; the standard contribution list includes `systemone` and the route claim does not collide with existing claims.
  - _Requirements: 1.1, 1.4, 1.5_
  - _Boundary: SystemOneFrontend_
  - _Validation: go test ./internal/plugins/frontends/systemone/ ./internal/standardplugins/_

- [x] 5. Integration
- [x] 5.1 Prove the end-to-end decision path and ship the config example
  - Add one runtime test through the standard host with two stub System One upstreams: the first answers 529, the second answers; assert one client response, two B-legs, upstream usage recorded, and one billing call when billing ports are injected.
  - In the same test file, assert a decision is denied while a secret guard is configured and a chat request routed to the decision backend is rejected before any upstream call.
  - Add `config/examples/custom-systemone-compatible.yaml` (frontend, TypeSafe profile, custom instance) and confirm it passes config checking.
  - Observable completion: the integration test passes and the example validates with `check-config`.
  - _Requirements: 1.1, 3.1, 3.2, 3.4, 3.5, 4.1, 4.3, 4.4, 5.1_
  - _Boundary: Integration_
  - _Depends: 2.1, 2.2, 2.3, 3.3, 4.3_
  - _Validation: go test ./internal/infra/runtimebundle/ -run 'Decision' && go run ./cmd/lipstd check-config --config config/examples/custom-systemone-compatible.yaml_

## Implementation Notes
- Feature validation remediation: existing scale fixtures include the fifth compatible family; System One has compile-time B-leg lifecycle assertions, fixed-route collision fixtures, and a typed positive/hard-negative standard backend contract instead of claiming chat scenarios.
- Task 5.1: stock request-size estimates require model_catalog.enabled; the integration fixture and example use an offline catalog. Failed upstream attempts retain validated usage/cost via the existing DrainUsageEvidence sideband, before any answer event. Quote sizing includes question/choice labels; validated operator profile headers are wired through the family builder.
- Task 4.3: shared frontendpipe uses an optional DecodeErrorWriter to retain field-specific rejection details without changing existing frontends. Nil decision carriers are omitted from canonical JSON to preserve ordinary chat/event serialization and wire-proof identities.
- Task 4.1: question IDs and option names are byte-bounded labels, not trimmed identifiers; exact client names survive decoding and canonical validation (requirement 1.2).
- Task 3.3: the maintainer explicitly approved the host-owned multi-user policy addition for operator-owned credentials. Unknown and personal-auth factories remain denied; the existing shared-deployment gate remains enabled.

## Completion Status
- [x] All 12 implementation tasks reviewed and verified.
- [x] Core #870 and endpoint #878 merged after CI repair #877; main baseline `953dcfc564311b3ea15faec7bea4384e68b9937c` verified.
- [x] Fresh merged-main decision integration, CLI build, example validation and spawned-distribution HTTP smoke passed.
- [x] Issue #804 is closed. Deferred scope remains unchanged; live vendor/SDK and remote race certification are not claimed.
