# Implementation Plan

Implementation is TDD-first. This foundation stops at the provider-neutral residency/control contract and its essential/connector plumbing; it does not start the keep-warm scheduler or enable autonomous provider refresh.

## 1. Freeze the Residency Contract With RED Tests

- [x] 1.1 Define RED lifecycle/profile/validation tests
  - Pin supported lifecycle categories for sliding expiry, fixed expiry, minimum residency, best-effort and unknown lifetime.
  - Pin that minimum residency cannot be represented as deterministic expiration and unknown/best-effort profiles cannot silently gain a TTL.
  - Pin observation support and renewal support as independent capabilities, including observation-only and fully unsupported profiles.
  - Pin bounds, required-field validation, explicit optional-time presence, non-negative usage evidence, and deterministic normalization.
  - _Requirements: 2.1-2.8, 10.1_
  - _Boundary: prompt-cache residency SDK contract only_
  - _Depends: none_
  - _Validation: focused SDK contract tests_

- [x] 1.2 (P) Define RED observation and identity-isolation tests
  - Prove cache observations are host-only and never appear in canonical model events or client output.
  - Prove target identity and cache-content generation are distinct from A-leg, client/proxy session, prompt-cache-key hint, response continuation, and transport identifiers.
  - Prove zero, one, and multiple bounded maintenance targets can be reported by one B-leg without merging targets by model/session name.
  - Prove buffered renewable observations become visible only after successful terminal outcome; failed/cancelled attempts do not publish renewable targets.
  - Prove compaction/session rotation may preserve backend-declared cache identity while branches/subagents remain isolated unless the backend explicitly reports otherwise.
  - _Requirements: 1.2-1.4, 3.1-3.9, 4.1-4.7, 10.2_
  - _Boundary: foreground B-leg host-only observation sideband_
  - _Depends: none_
  - _Validation: canonical stream plus sideband characterization tests_

- [x] 1.3 (P) Define RED control-handle and affinity tests
  - Pin that a renewable observation requires a bounded non-empty handle while observation-only targets do not imply control support.
  - Prove handles are instance/generation scoped, stale after release/close, and release is idempotent local-forget rather than provider cache deletion.
  - Prove control cannot re-run selector parsing, failover/racing, model substitution, region/downstream changes, or account balancing.
  - Prove credential refresh may retain the same account binding, but loss of required affinity fails stale/unavailable rather than switching accounts.
  - Prove cancellation/deadline and provider/control failures remain isolated from completed foreground inference.
  - _Requirements: 5.1-5.8, 6.1-6.8, 7.1-7.6, 10.3_
  - _Boundary: backend-owned target/control lifetime_
  - _Depends: none_
  - _Validation: focused controller/target-store contract tests_

- [x] 1.4 (P) Define RED executable-connector compatibility tests
  - Pin additive feature/minor negotiation, old-peer behavior, no-feature downgrade, and configure-before-control requirements.
  - Pin dedicated prompt-cache observation frame exclusivity, DTO bounds, lineage attribution, and rejection when the feature was not negotiated.
  - Pin renew/release request/result round trips and stale/unsupported classifications.
  - Pin provider-billable accounting evidence stays a host-only sideband with operation-specific dedupe and explicit presence.
  - _Requirements: 8.1-8.5, 9.1-9.7, 10.5_
  - _Boundary: executable backend-plugin ABI only_
  - _Depends: none_
  - _Validation: backendplugin converter/protocol/contract tests_

## 2. Implement the Minimal Provider-Neutral SDK and Core Seam

- [x] 2.1 Implement the residency profile, observation, evidence, and control contracts
  - Add only protocol-neutral lifecycle/profile/observation/control data required by the design, including explicit timing/usage presence and fixed bounds.
  - Keep target/generation IDs opaque and equality-only; keep the maintenance handle bounded and uninterpreted outside the issuing backend.
  - Validate impossible timing, unknown enums, negative usage and renewable-without-handle states fail closed.
  - Expose no provider cache object schema, TTL table, provider SDK enum, request body, credential, or client-facing maintenance operation.
  - _Requirements: 1.1-1.4, 2.1-2.8, 3.3-3.7, 4.1-4.7, 5.1-5.4, 10.1_
  - _Boundary: stable plugin-facing residency contract_
  - _Depends: 1.1, 1.2, 1.3_
  - _Design: Prompt Cache Residency SDK; Domain Model_

- [x] 2.2 Add optional effective backend residency/control capability
  - Extend the existing executor-facing backend capability envelope additively so model/candidate-aware residency profiles and direct cache-control operations can be supplied without concrete provider imports.
  - Preserve nil/unsupported behavior for every existing backend and keep normal inference execution unchanged.
  - Ensure control accepts an issued handle rather than a newly routable selector/candidate and cannot call the normal route planner or inference-open path.
  - Keep generation retirement authoritative: the capability cannot pin an obsolete generation solely for cache maintenance.
  - _Requirements: 1.5-1.7, 2.1-2.8, 6.1-6.8, 7.1-7.5_
  - _Boundary: core outbound backend capability seam_
  - _Depends: 2.1_
  - _Design: Effective Backend Cache Capability_

- [x] 2.3 Add the one-shot host-only observation source contract
  - Allow supporting backend streams to expose bounded cache observations through a drainable sideband while continuing to yield only canonical events.
  - Make drain ownership one-shot and safe after successful terminal; discard buffered renewable observations on failed/cancelled terminal.
  - Preserve event ordering, streaming behavior, output commitment, and existing managed-stream close semantics.
  - Add no per-request goroutine or database/network lookup to observation collection.
  - _Requirements: 1.2-1.6, 3.1-3.9, 8.2-8.5, 10.2, 10.9_
  - _Boundary: selected B-leg stream sideband only_
  - _Depends: 1.2, 2.1_
  - _Design: Foreground Observation; In-Process Sideband Contract_

## 3. Evolve the Executable Backend ABI Additively

- [x] 3.1 Add negotiated residency profile and observation sideband support
  - Introduce the next additive protocol minor/feature and project the provider-neutral residency profile through model-aware resolve results.
  - Add a dedicated prompt-cache observation sideband frame that is mutually exclusive with canonical event, accounting, terminal, diagnostic and cancellation payloads.
  - Buffer connector observation frames at the host adapter and publish them through the same one-shot observation-source semantics only after successful terminal.
  - Enforce field/count/frame bounds and reject feature use from legacy or non-negotiated peers.
  - _Requirements: 2.1-2.8, 3.1-3.9, 9.1-9.7, 10.5_
  - _Boundary: backendplugin v1 additive ABI and host stream adapter_
  - _Depends: 1.4, 2.1, 2.3_
  - _Design: Executable Connector Bridge; Foreground observation sideband_

- [x] 3.2 Add optional instance-scoped renew/release control operations
  - Expose additive unary cache-control calls only after compatible feature negotiation/configuration.
  - Map them to an optional configured-instance controller so legacy connectors and connectors without safe renewal remain unsupported without affecting Execute.
  - Preserve caller context cancellation/deadlines and prohibit hidden retries or fallback to another instance/model/account.
  - Make local release idempotent and ensure connector/session close invalidates outstanding handles.
  - _Requirements: 5.1-5.8, 6.1-6.8, 7.1-7.6, 9.1-9.7_
  - _Boundary: executable connector cache-control plane_
  - _Depends: 3.1_
  - _Design: Prompt Cache Controller; Control RPCs_

- [x] 3.3 Reuse provider-billable accounting for control usage
  - Return provider-reported maintenance usage with explicit cache-read/cache-write/output/total presence when available.
  - Keep the existing provider-billable plane and source/authority semantics; use maintenance-operation correlation/dedupe instead of a foreground B-leg charge identity.
  - Keep maintenance accounting outside canonical usage events and do not create a new customer admission/journal path.
  - Prove missing accounting remains absent rather than becoming fabricated zero/estimated evidence inside the control contract.
  - _Requirements: 8.1-8.6, 9.5-9.7_
  - _Boundary: host-only provider accounting evidence for cache control_
  - _Depends: 3.2_
  - _Design: Accounting Projection_

## 4. Prove Both Backend Delivery Paths With Reusable Contracts

- [x] 4.1 Build an in-process reference residency/controller implementation
  - Exercise unsupported, observation-only, renewable known-expiry, multiple-target, stale/released, affinity-unavailable and cold-recreated outcomes without an external provider.
  - Retain any provider-ready fixture state only inside a bounded generation-local target store behind opaque handles.
  - Prove close invalidates handles, release does not delete simulated upstream cache state, and foreground stream output remains unchanged.
  - Return provider-billable evidence separately for renewable test scenarios.
  - _Requirements: 3.1-3.9, 5.1-5.8, 6.1-6.8, 7.1-7.5, 8.1-8.5, 10.4, 10.6_
  - _Boundary: in-process reference/test backend only_
  - _Depends: 2.2, 2.3_
  - _Validation: reusable residency TCK against in-process path_

- [x] 4.2 (P) Build an executable reference connector implementation
  - Exercise the same residency/control scenarios through negotiated gRPC DTO/frame/RPC conversion.
  - Prove connector process/session restart invalidates local handles and old peers remain ordinary inference-only peers.
  - Prove malformed/oversized sidebands fail the host-only cache path without creating canonical model events.
  - Prove cancellation and provider-billable accounting parity with the in-process reference path.
  - _Requirements: 5.5-5.8, 8.1-8.5, 9.1-9.7, 10.4-10.6_
  - _Boundary: executable connector reference/TCK path_
  - _Depends: 3.1, 3.2, 3.3_
  - _Validation: backendplugin contracttest plus residency TCK_

- [x] 4.3 Consolidate the residency contract TCK
  - Certify the same observable contract independently of frontend protocol or provider implementation.
  - Cover unsupported, observation-only, renewable, multi-target, stale, affinity failure, cold recreation, control failure, generation close, canonical-output isolation and maintenance accounting separation.
  - Ensure the test kit needs no real network, credentials, database, or provider SDK.
  - Avoid adding frontend-by-backend Cartesian completeness requirements.
  - _Requirements: 10.4-10.6, 10.8, 10.10_
  - _Boundary: backend-family and connector capability certification_
  - _Depends: 4.1, 4.2_
  - _Design: Residency Contract TCK_

## 5. Ratchet Architecture, Compatibility, and Safety

- [x] 5.1 Add architecture/security guards against abstraction leakage
  - Fail if prompt-cache provider SDKs/vendor enums or TTL tables enter core/canonical packages.
  - Fail if a client-facing cache-maintenance operation/event is introduced or cache control starts calling normal routing/failover/continuation execution.
  - Fail if opaque handles/provider-ready target state become persisted, logged, metric-labeled, or attached to canonical usage output.
  - Fail if cache optimization can retain an otherwise retired backend generation.
  - _Requirements: 1.1-1.7, 4.5-4.7, 5.2-5.5, 7.3-7.5, 8.5-8.6, 10.7, 10.10_
  - _Boundary: architecture and privacy guardrails_
  - _Depends: 2.1, 2.2, 3.1, 3.2_
  - _Validation: focused archtest/source-shape/privacy tests_

- [x] 5.2 Run compatibility, race/leak, and repository quality gates
  - Run the SDK/ABI/reference/TCK suites and older-peer negotiation regression tests.
  - Run targeted race/leak coverage around target release, connector close, stream-sideband drain, and concurrent generation retirement where applicable.
  - Run repository architecture/quality checks and default unit tests without provider credentials.
  - Confirm profile/observation collection adds no request-hot-path database lookup, per-request goroutine, or unbounded allocation.
  - _Requirements: 9.1-9.7, 10.1-10.9_
  - _Boundary: verification only_
  - _Depends: 4.3, 5.1_
  - _Validation: `make quality-checks`; `make test-unit`; focused race/goleak tests; backendplugin contract tests_

- [x] 5.3 Perform final foundation-scope review
  - Verify the final implementation contains no scheduler, OS-command trigger, keep-warm interval, session budget, or autonomous provider renewal policy.
  - Verify unknown/best-effort/minimum-residency providers remain accurately represented without invented expiry.
  - Remove duplicated cache DTOs/adapters that are not required by the stable SDK-to-ABI bridge.
  - Confirm the follow-on orchestration can consume the contract without changing provider or canonical boundaries.
  - _Requirements: 1.1-1.7, 2.4-2.8, 8.6, 10.10_
  - _Boundary: final spec-one implementation scope_
  - _Depends: 5.2_
  - _Validation: final diff/design traceability review_
