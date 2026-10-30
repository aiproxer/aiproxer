# Implementation Plan

## Task List

Spec directory: `introduce-hexagonal-architecture`

**Implementation strategy:** harden the existing core-plus-adapters shape through compiler- and test-enforced boundaries, selective seam extraction, and explicit composition rules. Preserve current routing, streaming, continuity, and canonical translation behavior while tightening only the seams that still allow unwanted coupling.

**Implementation graph note:** establish only the minimum enforceable boundary rules first, then fix the one concrete composition leak around backend wiring, then preserve edge-only request context flow, and only then narrow extension or observer seams where a measured coupling problem still remains. Finish with composition-root tightening and non-regression coverage.

### Recommended first implementation slice

Deliver the smallest end-to-end slice that produces a real ownership improvement without broad refactor churn:

1. Task `1.1` - add or tighten architecture tests for core purity and the bounded composition exception.
2. Task `2.1` - keep `pkg/lipapi` and `pkg/lipsdk` as contract surfaces and document that no new orchestration policy moves there.
3. Tasks `3.1` and `3.2` - extract the backend seam out of the main executor package so `internal/pluginreg` and `internal/infra/runtimebundle` stop importing `internal/core/runtime` only to name backend construction types.
4. Task `3.3` plus a focused subset of `7.1` - prove executor behavior and standard wiring are unchanged.

Everything else in this spec remains intentionally deferred until a measured coupling problem justifies it.

---

- [x] 1. Establish enforceable boundary rules and migration classification
  - _Boundary:_ `internal/archtest`, spec-only migration classification artifacts, boundary guardrails
  - _Depends:_ existing package map and current composition exception inventory
- [x] 1.1 Add architecture tests for package-family dependency direction and the bounded composition exception
  - Encode allowed and forbidden dependencies for canonical contracts, the policy core, driving adapters, driven adapters, and composition helpers.
  - Fail broader composition-root imports into `internal/core/*` while permitting only the named backend-seam exception.
  - Keep the rules ownership-focused; do not add checks that force package renames, inbound interfaces, or generic `ports` buckets.
  - _Requirements: 4.2, 4.3, 4.5, 6.4, 7.5, 8.1, 8.4_

- [x] 1.2 Add a machine-checked migration classification baseline for aligned, extract, and exception seams
  - Capture the current seam statuses in the lightest executable form that prevents silent drift, such as focused architecture assertions or a small checked fixture.
  - Ensure the baseline distinguishes preserved seams from high-value extraction targets without forcing package relocation.
  - _Requirements: 6.1, 6.4, 6.6, 8.2, 8.5_

---

- [x] 2. Separate canonical contracts from policy-core ownership
  - _Boundary:_ `pkg/lipapi`, `pkg/lipsdk`, `internal/core/*`, driving-adapter call sites
  - _Depends:_ Task 1 guardrails so ownership rules are mechanically enforced
- [x] 2.1 Clarify and enforce the boundary between canonical public contracts and internal orchestration policy
  - Keep canonical request, event, capability, and error shapes in the public contract surface.
  - Keep routing, recovery, and extension-stage policy inside the internal core and block provider SDK, transport, and composition concerns from leaking into the public contract surface.
  - Preserve the ability for driving adapters to call concrete core services directly unless a real multi-consumer interface is needed.
  - _Requirements: 1.1, 1.3, 1.5, 3.5, 4.3, 8.1_

---

- [x] 3. Extract the executor-owned backend seam
  - _Boundary:_ executor-consumed outbound backend seam, `internal/pluginreg`, `internal/infra/runtimebundle`
  - _Depends:_ Tasks 1-2 to lock dependency direction and core-vs-contract ownership
- [x] 3.1 Introduce the executor-consumed backend port that opens canonical backend attempts
  - Define the seam where the executor requests backend work, including capability description and canonical stream-open semantics.
  - Prefer the smallest stable shape that improves ownership clarity; this may be a narrow interface or the existing struct-of-functions promoted out of the executor package.
  - Keep concrete backend construction helpers, provider SDK usage, and transport-specific concerns outside the seam.
  - _Requirements: 1.1, 1.2, 3.1, 3.2, 3.4, 3.7, 5.2, 5.4, 8.3, 8.7_

- [x] 3.2 Move runtime assembly and backend registration onto the new backend seam
  - Repoint composition helpers to the named backend seam instead of the broader executor package while preserving explicit wiring and official registration.
  - Keep the temporary composition exception narrowly scoped until the new seam fully replaces the old dependency edge.
  - Stop once `internal/pluginreg` and `internal/infra/runtimebundle` no longer need the broader executor package to name or construct backends; do not broaden the refactor into unrelated runtime cleanup.
  - _Requirements: 4.1, 4.2, 4.4, 6.5, 8.4_

- [x] 3.3 Add regression coverage proving backend seam extraction does not change executor behavior
  - Verify unchanged route planning, capability filtering, pre-output recovery, and no-retry-after-output semantics through the refactor.
  - Exercise adapter substitution through the seam with deterministic test doubles.
  - _Requirements: 1.4, 3.3, 5.2, 5.3, 5.4, 7.1, 7.2_

---

- [x] 4. Keep transport auth and protocol handling at the driving-adapter edge
  - _Boundary:_ `internal/stdhttp`, frontend adapters, `internal/core/execctx`, core entrypoints
  - _Depends:_ Task 2 boundary clarification so edge concerns do not leak inward
- [x] 4.1 (P) Normalize edge-authenticated request context into core-facing views only
  - Ensure only request identifiers, attempt lineage, principal summary, session/workspace views, route preferences, and correlation annotations cross inward.
  - Keep transport auth implementation details, raw HTTP request or response objects, and frontend payload types at the edge.
  - _Requirements: 2.1, 2.4, 3.1, 7.3, 7.4_

- [x] 4.2 Keep decode, encode, validation, and protocol-error mapping in driving adapters
  - Preserve translation of client input into canonical calls and canonical events back into protocol-legal responses.
  - Keep malformed-input handling and unsupported-semantics surfacing out of the application core.
  - _Requirements: 2.1, 2.2, 2.3, 2.5, 5.1_

- [x] 4.3 Keep inbound seams pragmatic at the transport-to-core boundary
  - Preserve direct use of concrete executor or use-case services from driving adapters where that is already a clean boundary.
  - Introduce inbound interfaces only if multiple real consumers or replaceability needs justify them.
  - _Requirements: 8.6_

---

- [x] 5. Narrow extension-runtime and observability seams
  - _Boundary:_ `internal/core/extensions`, observer seams, feature adapters, infra observers
  - _Depends:_ Task 1 guardrails and Task 2 ownership rules; may proceed in parallel with Task 4 once core-facing context is stable
- [x] 5.1 Replace the broad runtime snapshot dependency with service-specific extension contracts
  - Expose only the service families extension consumers actually need, such as auxiliary requests, state, workspace, traffic observation, and request transforms.
  - Keep cohesive grouped facades where splitting them further would add ceremony without reducing coupling.
  - Keep feature adapters from depending on undifferentiated runtime bundles or internal orchestration details.
  - Only split a consumer away from `RequestRuntimeSnapshot` when that consumer demonstrably depends on unrelated capabilities; if no such case exists yet, document the snapshot as a temporary grouped facade and defer further slicing.
  - _Requirements: 3.1, 3.6, 6.1, 6.5, 8.3, 8.4, 8.7_

- [x] 5.2 Define stable observer contracts for route and traffic outcomes
  - Emit only request IDs, lineage IDs, route summaries, and redacted annotations into observer seams.
  - Reuse the current observer seam if it already satisfies the contract; otherwise narrow it without dragging logging or transport concerns inward.
  - Keep transport payloads, provider payloads, and executor-private details out of observability consumers.
  - Stop at metadata-shape clarification if observer replacement is already possible; do not add a new observer layer just for naming consistency.
  - _Requirements: 3.1, 3.2, 5.5, 7.3, 7.5_

- [x] 5.3 Add contract tests for extension and observer seam consumption
  - Verify that feature and infra adapters consume only the allowed service and observer contracts and remain replaceable through the narrowed seams.
  - _Requirements: 3.4, 6.2, 7.1, 7.2, 7.5_

- [x] 5.4 Allow dedicated query-style seams where read paths benefit from them
  - For diagnostics, admin, or reporting-oriented reads, prefer query adapters and read DTOs when they are simpler than repository-shaped write seams.
  - Ensure those read paths remain outside core orchestration policy and do not leak transport or provider types inward.
  - _Requirements: 7.6_

---

- [x] 6. Align composition roots with the named seams
  - _Boundary:_ `cmd/lipstd`, `internal/pluginreg`, `internal/infra/runtimebundle`
  - _Depends:_ Tasks 3 and 5 so composition can target the clarified seams
- [x] 6.1 Update the standard runtime assembly to depend only on named core seams and approved support contracts
  - Preserve explicit construction of runtime services, stores, auth providers, and observers while removing broad imports of unrelated core policy packages.
  - Keep seam placement local to the owning core capability; do not introduce a generic repository-wide `ports` or `interfaces` package as part of the refactor.
  - Keep the standard distribution wiring static, testable, and free of container or reflection-driven discovery.
  - _Requirements: 4.1, 4.2, 4.4, 4.6, 6.2, 8.2, 8.7_

- [x] 6.2 Enforce the bounded composition exception and its retirement trigger
  - Keep the exception register executable by failing new dependency edges and limiting the current allowance to the named backend seam only.
  - Make the retirement path visible so future work can remove or formally ratify the exception without silent drift.
  - _Requirements: 4.5, 6.4, 6.6, 8.1, 8.4_

---

- [x] 7. Prove incremental migration without semantic regression
  - _Boundary:_ integration coverage across composition roots, frontends, executor path, and selected adapter seams
  - _Depends:_ Tasks 3-6 because verification should exercise the final mixed aligned/extracted/exception state
- [x] 7.1 Re-run focused integration coverage across composition, frontend execution, and backend selection after the seam changes
  - Verify canonical-in-the-middle flow, streaming-first execution, and explicit adapter registration still behave the same after the architecture hardening.
  - _Requirements: 1.6, 4.4, 5.1, 5.2, 5.3, 6.2, 6.3_

- [x] 7.2 Add non-regression coverage for coexistence of preserved seams, extracted seams, and documented exceptions
  - Exercise aligned, extracted, and exception paths together so the migration model remains incremental rather than rewrite-driven.
  - _Requirements: 6.1, 6.2, 6.4, 6.5, 6.6, 8.5_
