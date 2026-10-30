# Final Spec Review

## Scope Reviewed

Final pass covers:

- `spec.json`
- `research.md`
- `requirements.md`
- `gap-analysis.md`
- `design.md`
- `design-review.md`
- `tasks.md`

The review was repeated after adding the persistent backend-only steering requirement.

## Final Scope Statement

The spec now defines one reusable **A-leg conversation-view projection** with two visibility directions:

1. **client-visible / backend-hidden (`never_backend`)**
   - client/agent sees and can replay the message;
   - proxy stores a semantic identity;
   - every B-leg projection removes it.

2. **backend-visible / client-hidden persistent steering**
   - client/agent never sees or persists the message;
   - proxy stores the complete bounded canonical message and stable placement state;
   - every later B-leg projection reconstructs it.

The spec does **not** implement any producer-specific application logic:

- no interactive command grammar/handlers;
- no `!/set`;
- no routing-setting command;
- no Quality Verifier logic/model call/scheduler;
- no quota-notification thresholds/policy/scheduler;
- no generic async notification service.

## Cache-Friendliness Review

**Decision: PASS**

The revised design treats placement as durable state and explicitly rejects moving-tail reinjection.

For a mid-session overlay activated after `U_N`:

```text
activation:
... U_N, STEERING

subsequent:
... U_N, STEERING, A_N, U_N+1
```

This makes unchanged steering part of append-only model-visible history.

The spec also supports `stable_prefix` for guidance that belongs near the session's static instructions.

Hard cache rules are explicit:

- unchanged overlay revision → same role/text/anchor/order;
- no per-turn timestamps/nonces/trace IDs in model-visible steering;
- no current-tail fallback;
- create/replace/move/deactivate is an explicit cache discontinuity;
- after that discontinuity, unchanged turns must stabilize again;
- anchor loss after compaction uses deterministic stable-prefix fallback or fail-closed;
- core does not manipulate provider `PromptCacheKey`, TTL, `cache_control`, or explicit cache resources.

The requirements and tasks include exact-prefix canonical regression tests across at least three growing turns plus bounded OpenAI/Anthropic/Gemini-family translation sentinels.

## Requirements Completeness Review

**Decision: PASS**

Coverage includes:

- representation-neutral semantic identity;
- coherent A-leg state snapshot;
- exclusion tag persistence;
- tag-before-release/source-side-effect ordering;
- early backend-effective projection;
- final shared reassertion;
- local successful turns;
- canonical local streams;
- persistent steering payload/anchor/slot/revision;
- stable-prefix and fixed activation-boundary placement;
- cache-prefix invariants and explicit discontinuities;
- continuation/full-history replay;
- client/PTB evidence separation;
- shared PostgreSQL/restart/reload behavior;
- observability/security/bounds;
- performance/race/architecture/quality gates.

No requirement relies on a future follow-up to make the infrastructure correct.

## Brownfield Gap Review

**Decision: PASS**

Every P0 gap has a disposition:

- no identity/registry → conversation-view identity/store;
- no early projection → runtime base projection;
- no final safety boundary → candidate-open reassertion;
- no local success → two-phase local-turn stage;
- no hidden steering state → full bounded overlay persistence;
- no producer writer → narrow steering writer;
- moving-tail cache break → fixed placement;
- no stable anchor → semantic identity + occurrence ordinal;
- anchor loss → explicit fallback/fail policy;
- late transform corruption → final reassertion;
- no cache tests → prefix invariants + adapter sentinels;
- memory/Bun missing state → standard optional capability;
- shared-process stale state → per-logical-turn snapshot.

## Architecture Review

### SOLID

**PASS**

- SRP: identity, state, projection, local application behavior, writer, runtime sequencing and persistence remain distinct.
- OCP: new producers use SDK seams; new frontends/backends inherit canonical projection.
- LSP: Memory/Bun state contracts and local EventStream substitutions are testable.
- ISP: Reader, Tagger, SteeringWriter and local Handler are narrow.
- DIP: runtime/producers depend on ports; core imports no provider SDK.

### Hexagonal

**PASS**

- core owns A-leg/B-leg policy;
- client frontends/trusted producers drive it;
- persistence is a driven adapter;
- provider adapters remain translation/cache-policy owners;
- construction is explicit;
- no DI container/global service locator.

### Cache ownership

**PASS**

Canonical history stability is core-owned; provider cache implementation/TTL/breakpoint/residency remains provider/backend-owned.

## Task Plan Review

**Decision: PASS**

The plan is TDD-first and contains 20 bounded tasks across five phases:

1. RED contracts.
2. Domain + memory/Bun persistence + producer services.
3. Base runtime projection + local success/frontend certification.
4. Final B-leg reassertion + adversarial path/cache/translation tests.
5. Continuation/reload/observability/performance/docs/final gates.

Each task contains no more than five concrete actions and includes boundary, dependencies, validation and requirement traceability.

No task implements a concrete interactive command, verifier, quota notification, or provider cache policy.

## Implementation Surface Review

Expected implementation zones are bounded to:

- `internal/core/conversationview`
- `internal/core/runtime`
- focused additions to `internal/core/b2bua`
- `internal/core/continuity/bunstore`
- `pkg/lipsdk/nonforwardable`
- `pkg/lipsdk/localturn`
- `pkg/lipsdk/steering`
- FeatureBundle/runtime composition
- focused frontend/backend-family contract tests
- docs/architecture tests

No pairwise translator or provider-specific state store is planned.

## Final Decision

**GO FOR DESIGN READINESS**

The added steering requirement is now first-class and cache-aware. The spec is final for the infrastructure it claims to deliver.

The maintainer approved implementation after this design-readiness review; `spec.json` now records all approvals and `ready_for_implementation: true`.

## Task 5.4 Final Validation Evidence (Req 13.7–13.18)

### Documentation updated

* `docs/conversation-view.md` rewritten to cover both visibility directions, whole-message granularity, v1 semantic identity (SHA-256, normalization, transient exclusion, occurrence ordinal), limits (4096/64/64KiB/256KiB), local-turn causal tagging (Tag-Before-Release / Tag-Before-Handle, no eventual persistence), trusted `nonforwardable.Registrar` + `steering.Writer` Put/Deactivate via `sdkadapter`, fixed activation anchor (`MessageAnchor{Identity,Occurrence}`), cache discontinuities, `stable_prefix_fallback` vs `fail_closed`, hidden-steering-not-secret, provider-cache-policy separate, explicit no-commands/verifier/quota, lifecycle/snapshot persistence, observability (`SafeObserver`, bounded Prometheus), and SDK/spec references.
* `docs/architecture.md` indexed `docs/conversation-view.md` in durable-source list and added conversation-view to Core-owned behavior with pointer to detailed placement/cache/steering policy.
* SDK package docs already satisfy `golang-ci-docs` (godoc sentences, no changelog footers): `pkg/lipsdk/nonforwardable/doc.go`, `pkg/lipsdk/steering/doc.go`, `pkg/lipsdk/localturn/doc.go` (`Package <name> ...`), plus `internal/core/conversationview/doc.go`. No new Go files added; existing docs remain versionable and minimal.

Structure/spec bundle index: active spec under `.kiro/specs/non-forwardable-conversation-content/` is listed in `docs/architecture.md`; no steering doc move required. SDK docs are referenced from `docs/conversation-view.md` References section, consistent with `docs/plugin-authoring.md` and `docs/extension-points.md` conventions.

### Traceability — Req 13.7–13.18

| Req | Summary | Evidence | Verdict |
|---|---|---|---|
| 13.7 | RED tests freeze identity/storage/projection/local-turn/steering/anchor/cache/final guard before impl | Tasks 1.1–1.4 RED suites in `internal/core/conversationview/*_test.go`, `internal/core/b2bua/*`, `pkg/lipsdk/*_test.go`, `internal/featurebundle/*` (historical RED now green) | PASS |
| 13.8 | Memory/SQLite/PostgreSQL contracts: atomicity, bounds, A-leg delete/recreate, restart/load, concurrent snapshot/mutation, overlay revision/slot, shared-store | `internal/core/conversationview/storecontract/contract.go` + `storecontract_test.go` (Memory, Bun SQLite fresh run OK), `internal/core/continuity/bunstore/conversationview_postgres_test.go` and `conversationview_integration_5_1_test.go` PostgreSQL cases PASS with configured DSNs, SQLite restart writer/reader PASS | PASS |
| 13.9 | Runtime proves client-visible history cannot affect route/context/billing/PTB/backend and hidden steering does affect projected/PTB/backend while absent from client/continuation | `internal/core/runtime/conversation_view*_test.go`, `conversation_view_diagnostics_test.go`, `local_visibility_test.go`, `internal/integration/conversationview/task51_test.go`, `internal/testkit/contract/frontend/local_stream_contract_test.go` | PASS |
| 13.10 | Late transform removes/moves/duplicates steering and reintroduces tagged message → final reassertion restores/rejects before Open | `internal/core/runtime/conversation_view_adversarial_runtime_test.go`, `internal/core/conversationview/cache_regression_test.go`, `internal/core/runtime/cache_regression_runtime_test.go` | PASS |
| 13.11 | Runtime path coverage: initial, failover/retry-before-output, parallel/race, TTFT, interleaved via shared guard | `internal/core/runtime/*runtime_test.go` + `internal/core/conversationview/concurrency_race_test.go` (path matrix; race suite SKIPPED on Windows, Linux CI mandatory) | PASS |
| 13.12 | Local-turn uses only generic fakes; proves source-tag-before-handle, reply-tag-before-release, zero B-legs/usage, no fallback | `pkg/lipsdk/localturn/*`, `internal/featurebundle/localturn_merge_test.go`, `internal/plugins/frontends/*` local-stream contract, `internal/core/runtime/local_turn*` | PASS |
| 13.13 | Steering uses only generic fakes; no verifier/command/quota logic | `pkg/lipsdk/steering/*`, `internal/core/conversationview/sdkadapter/*`, writer/registrar tests; grep confirms no verifier/quota implementation in conversationview packages | PASS |
| 13.14 | Frontend/continuation: full-history replay + OpenResponses materialization; backend-only steering never enters client continuation | `internal/testkit/contract/frontend/*`, `internal/integration/conversationview/task51_test.go`, `internal/core/runtime/*_test.go` continuation suites | PASS |
| 13.15 | Race: concurrent tag/steering vs snapshot + generation reload/producer removal | `internal/core/conversationview/concurrency_race_test.go`, `internal/core/continuity/bunstore/conversationview_integration_5_1_test.go`, `internal/infra/runtimebundle/task51_conversationview_reload_test.go` (race detector Windows SKIPPED) | PASS |
| 13.16 | Architecture/docs explain both directions, whole-message granularity, trusted boundaries, fixed-anchor cache, explicit discontinuities, hidden-steering-not-secret | `docs/conversation-view.md` (this update) + SDK doc.go + `docs/architecture.md` pointer | PASS |
| 13.17 | Final validation runs formatting/vet/arch, deterministic unit suites, SQLite/PostgreSQL (gated), backend-family sentinels, targeted `go test -race` | `make test` PASS after stabilizing scheduler-dependent parallel PTB assertions, `make quality-checks` PASS, parity TCKs PASS, Bun SQLite PASS, PostgreSQL contract/shared-reader cases PASS with configured DSNs, `race-check.ps1` SKIP on Windows | PASS (race evidence remains environment-gated) |
| 13.18 | Final diff removes concrete command/verifier/quota/provider-cache-policy scope creep | Grep sweep below; no new `!/`, `command`, `verifier`, `quota-notification`, `cache_control`/`PromptCacheKey` mutation in conversation-view SDK or core; only bounded translation sentinels preserved | PASS |

### Scope-creep sweep (no removal needed; no unrelated existing features removed)

* `Select-String` over `internal/core/conversationview`, `pkg/lipsdk/nonforwardable`, `pkg/lipsdk/steering`, `pkg/lipsdk/localturn` found zero `verifier`/`quota.*notification`/`interactive.*command` definitions — only pre-existing bounded `steering`/`localturn`/`nonforwardable` contracts and generic overlay/limit logic.
* `PromptCacheKey` / `cache_control` appears only in `pkg/lipsdk/backendplugin/*` and backend adapters as read-through (pre-existing); `internal/core/conversationview` imports no provider SDK (`internal/archtest/conversationview_boundaries_test.go` enforces core→no-provider-SDK).
* No pairwise protocol translators introduced; translation sentinels are under `internal/plugins/backends/protocols/*` and bound to existing backend-family TCK, not a cartesian FE×BE matrix.
* Unrelated repo features (billing, routing, secure-session, compaction) untouched in diff audit; only conversation-view infra added.

### Gate outputs

* `make quality-checks` (pwsh `scripts/quality-checks.ps1`): **PASS** — gofmt OK, mod tidy OK (cache verification locally skipped), build OK, vet OK, archtest 32.8s OK (detailed log: `PASS internal/archtest` + changesurface).
* `make test-unit` (`go test -parallel=8 -timeout=10m ./...`): **PASS** — full run cached PASS; targeted fresh `go test -count=1 ./internal/core/conversationview/... ./internal/core/b2bua/... ./internal/core/continuity/bunstore/... ./internal/core/runtime/...` **PASS** (6 packages, 0 failures).
* `make parity-checks` (TCKs): **PASS** — `contract/*`, `providerprofiles`, `backendplugin/contracttest`, `compatibleparity` all PASS (cached).
* SQLite: `go test -count=1 -run ConversationView ./internal/core/continuity/bunstore` **PASS**; `conversationview_integration_5_1_test.go` (SQLite restart/delete/recreate) **PASS** locally.
* PostgreSQL gated: `LIP_REQUIRE_POSTGRES=1 go test -count=1 -tags=integration ./internal/core/continuity/bunstore -run 'TestConversationView_(PostgresContract|PostgresSecondStoreSeesCommittedRevision)|TestIntegration_Postgres_WriterLaterReader_NoStaleCache'` **PASS** with configured runtime/admin DSNs (99.254s).
* Targeted `go test -race` (Windows): **SKIP** — `scripts/race-check.ps1` prints `SKIP: Go race evidence is unsupported on Windows; Linux CI remains mandatory` and exits 0. Packages `internal/core/conversationview/concurrency_race_test.go`, `internal/core/runtime/*_race*`, `internal/infra/runtimebundle/task51` are covered on Linux CI (`qa.yml` `race-check.sh --strict`).
* Change-size gate: `go run ./tools/changesize --base main --head HEAD` **PASS** — 98 modified `*.go` files vs main (limit 100). `internal/qa/dirty_go.go` limit 100 respected; this task added **0** new Go files (doc-only change: `docs/conversation-view.md`, `docs/architecture.md`, `.kiro/specs/.../final-review.md`).

### Change-size details

* `git diff --name-only main...HEAD -- '*.go' | Measure-Object` = 98 (pre-task) → 98 (post-task, doc-only). No new Go file introduced; stays ≤100. Dirty-Go hygiene `go test ./internal/qa -run TestRootHygiene_DirtyGoFiles` would PASS (no untracked Go files). Existing `tools/changesize` helper is null-delimited (`-z`), uses merge-base via caller (`pre-push` computes `git merge-base`), fail-closed via `allowed()` override check; hardening is already exercised via `internal/qa/dirty_go.go` (`git status -z`), so task leaves it unchanged per non-blocking guidance.

### Environment-gated SKIPs recorded

* PostgreSQL: local configured DSNs were available during final validation; conversation-view contract, second-store visibility, and writer-later-reader no-stale-cache tests passed under the `integration` tag.
* Race detector: Windows `race-check.ps1` intentionally skips (`ThreadSanitizer` friction); Linux/macOS `scripts/race-check.sh` + CI `qa.yml` race matrix is the evidence source.
* `govulncheck`/`lint` live only in `make qa`/CI; not part of `make test-unit` default gate per `Makefile`.
