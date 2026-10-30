# Implementation Validation

## Scope
The controller approved requirements, design, implementation and a separate correction PR. The implementation preserves native counters and fixes only the uncached-input extension for explicitly annotated usage.

## Evidence
- RED: before metadata, the focused collector test failed to compile on the missing flag; with the flag alone, exclusive/mixed/scoped-exclusive cases returned zero instead of 300/600/300.
- RED: existing Anthropic start/delta mapping fixtures returned uncached 8 instead of 11 before adapter annotation.
- RED: removing the correction from the helper failed OpenAI stream/non-stream and Anthropic extension fixtures; omitting the clone scalar failed collector clone results.
- PASS: focused tests for `pkg/lipapi`, essential Anthropic mapper, OpenAI legacy/Responses, Anthropic and Gemini frontends, and core metering normalization.
- PASS: scoped lint for changed API/mapper/frontend packages.
- PASS: `make dev-contract-check` selected structural ABI, architecture, family-composition and shared-boundary checks plus lint.
- PASS: `go test ./internal/archtest ./internal/core/auxreq ./pkg/lipsdk/backendplugin`; no public ABI baseline regeneration was required by these gates.
- Remote correction-PR CI (all five required checks) passed on the merged head before merge; merged-main scoped checks and service smoke were re-run on the merged baseline (see Completion Status in tasks.md).

## Coverage and Boundaries
- 1.1–1.4: collector inclusive/exclusive/mixed and scoped-priority table; shared collector clone retains correction state.
- 2.1–2.3: both essential mapper constructors, frontend stream/non-stream extension fixtures and retained output-only absence/presence behavior.
- 3.1–3.4: unchanged native counters in existing mapper/frontend assertions; existing provider V2 evidence and normalization fixtures pass. No core/runtime, rating, scopes, emitted event sequence, routing or retry code was changed.
- Directly constructed unannotated collectors still use inclusive semantics. Optional connector adoption (#1085), start-only usage visibility (#1084), native counter normalization and persisted backfill are not fixed here.

## Delivery State
Implementation tasks have local evidence and both delivery PRs are merged with all required checks green:
- PR #1087 (correction): head `3010674fdad262c65a066a05d51749271703a0a4`, merged `2026-10-14T21:14:04Z` as `4f0b608b7a97d08b599735bba7d8674a88fd960b`.
- PR #1079 (independent diagnostic): head `dea10d67a06aa5bef6f271245451f384584c4ed5`, merged `2026-10-14T18:15:44Z` as `85044572b43aa5261228c24c5019aa1c95c9c14a`.
- Merged-main baseline `fa4bd2d5` contains both merges; scoped tests, contract checks and service smoke were re-run there (see Completion Status in tasks.md).
- No whole-service FEATURE_GO claim is made. Deferred successor scope: #1084 (start-only usage visibility), #1085 (optional connector adoption).
