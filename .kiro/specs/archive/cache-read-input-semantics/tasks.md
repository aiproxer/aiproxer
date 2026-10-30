# Implementation Plan

Base: independent correction slice from `origin/main`; PR #1079 remains observability-only. Requirements and design were explicitly approved by the controller, with implementation and PR submission authorized.

- [x] 1. Interpret cache-read exclusion without rewriting usage quantities
  - RED the inclusive, exclusive, mixed-entry and scoped-priority collector cases before implementation; cover pre-populated/copy behavior through the existing collector fixtures.
  - Add the neutral interpretation metadata and bounded private collector correction; preserve original quantities and presence.
  - Done: 300 exclusive input plus 600 cache reads produces 300 uncached; legacy inclusive 900/600 also produces 300, and ordinary counters remain unchanged.
  - Run focused API tests and consumer compilation/public compatibility gates; update only the existing ABI baseline if the gate requires it, without relaxing policy.
  - _Boundary: SDK/public contract and collector; existing ABI baseline only if required_
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 3.1, 3.3_

- [x] 2. Integrate essential Anthropic interpretation with existing frontend usage encoders
  - RED mapper/frontend regressions before annotating both usage constructors. Cover omitted input/cache-read quantities and retain existing absence/presence behavior.
  - Preserve raw/native provider quantities and run the existing V2 evidence fixtures; do not add scopes, emitted usage events, billing or core branches.
  - Done: stream and non-stream frontend fixtures expose corrected uncached extensions while native counters remain unchanged; unannotated events retain legacy behavior.
  - Run scoped mapper/frontend/API tests and lint, existing core normalization/provider-evidence tests, and `make dev-contract-check`; measure the slice against the design budget.
  - _Boundary: Explicit integration of essential backend mapper and existing frontend tests; no frontend production change_
  - _Depends: 1_
  - _Requirements: 2.1, 2.2, 2.3, 3.1, 3.2, 3.3, 3.4_

## Completion Status
- [x] Implementation tasks 1–2 complete with the evidence in validation.md.
- [x] PR #1087 merged as `4f0b608b7a97d08b599735bba7d8674a88fd960b`; PR #1079 merged as `85044572b43aa5261228c24c5019aa1c95c9c14a`.
- [x] Merged-main baseline `fa4bd2d5` (contains both merges) verified: scoped package tests, contract-check surface and feature-stub service smoke passed; evidence logs kept in session scratch outside the repository.
