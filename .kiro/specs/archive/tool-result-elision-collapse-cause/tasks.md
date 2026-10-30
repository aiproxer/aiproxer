# Implementation Plan

> Tasks 1 to 5 are implemented and verified on the delivery branch. Task 6
> stays open until the delivery is merged and the spec is archived: the
> follow-up issues for the deferred digest feed and the remaining causes are
> created with the archival, as the repository's spec flow requires.

- [x] 1. The canonical request carries a bounded structural cause
  - One proxy-owned, non-wire `StructuralCause` label on `lipapi.Call`, bounded by `MaxStructuralCauseBytes` and validated with the envelope fields; `CloneCall` copies it. No wire, no client input, no content.
  - Observable: the label survives cloning and per-attempt derivation, never appears in a serialized request, and an over-long label fails validation.
  - _Requirements: 1.1, 1.2_
  - _Boundary: canonical model (`pkg/lipapi`)_
  - _Depends: none_
  - _Validation: `make dev-test PKGS='./pkg/lipapi/...'`_
- [x] 2. The elision feature records the cause of an applied batch
  - The transform writes the `tool-result-elision` label onto the call it publishes, so the label reaches the live request exactly when the change does. A no-op pass, a fail-open pass and a panic-guarded pass record nothing; the first label on a request wins.
  - Observable: an applied batch records the label on the published call, a discarded pass records none, and the request's other fields are unchanged.
  - _Requirements: 1.3, 1.4, 1.5_
  - _Boundary: feature plugin (tool-result elision)_
  - _Depends: 1_
  - _Validation: `make dev-test PKGS='./internal/plugins/features/toolresultelision/...'`_
- [x] 3. Core carries the label onto the leg's own usage sample
  - The cause is captured at the final backend-open boundary, from the exact call the provider receives, after every request mutation, reassertion, clamp and adaptation. The attempt session carries it, and the usage emitter copies it into that leg's usage event. Core carries the label without interpreting it.
  - The request's recovery controller owns the publication claim set, and every attempt's request facts reference it, so a later leg of the same request, a parallel arm, or a replacement carries no cause for a key the request already published. The claim is taken only once the attempt has a stream and completed its open steps, so a nil-stream open publishes nothing. The request execution — not an A-leg, trace, billing or continuation identifier — is its identity.
  - Observable: a request that recorded a cause produces usage samples that carry it; a request that recorded none produces samples without it; a transformation that undoes the change clears the label, and neither the provider-bound request nor the usage sample carries it; a later leg of the same request carries no cause, while another request's own cause still applies.
  - _Requirements: 1.6, 2.1, 2.2, 3.4, 3.5, 4.1, 4.2_
  - _Boundary: core runtime_
  - _Depends: 1_
  - _Validation: `make dev-test PKGS='./internal/core/runtime/...'`_
- [x] 4. The diagnostic applies the cause of the sample it processes
  - The cause vocabulary gains `tool-result-elision` behind one shared whitelist; the usage path applies the sample's own cause to the sample's key before the healthy/cold evaluation, and only for an accepted sample. No pending store, no eviction bookkeeping, and no per-request state.
  - Observable: an episode after a carried cause reports it, a later healthy sample clears it, a repeated usage delta changes no state, every candidate key is attributed, two distinct requests with the same trace identifier each apply their own cause, and an unknown label changes nothing.
  - _Requirements: 3.1, 3.2, 3.3, 3.6, 3.7, 3.8, 3.9_
  - _Boundary: infrastructure adapter (collapse diagnostic)_
  - _Depends: 3_
  - _Validation: `make dev-test PKGS='./internal/infra/metrics/...'`_
- [x] 5. Adversarial acceptance tests
  - Resumed turns that share an A-leg, overlapping requests with out-of-order usage samples, two distinct requests with the same trace identifier, many candidate keys, a duplicate usage delta for a healthy elided leg, a later leg of the same request, interleaved requests, a late mutation that undoes the change, a timed-out transform whose worker keeps running after the runner skipped it, an end-to-end run of the real elision feature, a corrective replacement on the same key after a healthy publication, and a nil-stream open followed by a same-key retry.
  - Observable: no request inherits another request's cause; a duplicate delta changes no state; a late mutation leaves neither the provider-bound request nor the usage sample with a cause; a discarded worker leaves the live request without the change and without the cause; the real elision feature reaches the provider-bound request and the usage sample.
  - _Requirements: 1.3, 1.6, 2.3, 2.4, 3.1, 3.2, 3.3, 3.4, 3.5_
  - _Boundary: feature plugin + infrastructure adapter + core runtime_
  - _Depends: 1, 2, 3, 4_
  - _Validation: `make dev-test PKGS='./internal/infra/metrics/... ./internal/standardplugins/... ./internal/core/runtime/...'`_
- [x] 6. Delivery evidence
  - Run the local CI equivalents for the changed paths on the PR head, post the evidence with the tested head SHA, and record the deferred digest feed and remaining causes as follow-up issues.
  - Observable: the PR body lists each command with its result, and the follow-up issues exist for the digest feed and the remaining causes.
  - _Requirements: 1.6_
  - _Boundary: delivery_
  - _Depends: 5_
  - _Validation: `make dev-test-changed`_

## Completion Status

- [x] Tasks 1–5 implemented, tested and merged.
- [x] Delivery evidence recorded on PR #1366: head `8fe74a8b1`, merged to `main` as `a842f959f`.
- [x] Deferred successor work recorded as follow-up issues: the seam-digest feed (#1367) and the remaining structural causes (#1368).
