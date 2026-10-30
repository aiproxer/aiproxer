# Remediation plan: 19.3 proof-time transient + production assessor composition

Scope decisions (owner, 2026-09-11): all three lanes in one pass (no Lane-1 pilot);
full 20 MiB benchmark run is mandatory before any advertise decision; production
assessor lives in `internal/core/runtime` (`largebody` stays pure).

Status: PLAN ONLY. No code changed. Phases 0-7 not started.

## 1. Problem statement (evidence-locked)

### Issue A - 19.3 strict heap gate FAILED (proof-time transient)

`evidence/19.2-19.5-benchmarks.md` §5: post-commit retained heap **PASS**
(35,656 B/op, 12 allocs, 0.000% slope 1→5→20 MiB), but `CompileProof`
allocates **~6.5-7.9x body size transiently** (6.9 MB @1 MiB → 165 MB @20 MiB).
Root cause, identical in all three lane profiles
(`internal/plugins/frontends/openairesponses/profile.go`,
`internal/plugins/frontends/openailegacy/profile.go`,
`internal/plugins/frontends/openresponses/profile.go`):

1. `io.ReadAll(rc)` of the full replay,
2. full `json.Unmarshal` into wire DTO,
3. full `[]lipapi.Message` tree build,
4. per-message `json.Marshal` inside `CallIdentityWriter.AddMessage`
   (`internal/core/largebody/identity.go:470`).

Task 6.2 already built the streaming alternative (`BeginMessage` /
`BeginTextPart` / `StreamingEscapeWriter`, profile hash-writer contract) - the
lane profiles do not use it. Side effect: decode-permit hold stretches to
~24 ms @1 MiB / ~126 ms @5 MiB (proof under permit), i.e. head-of-line
blocking for other admissions.

### Issue B - no production assessor composition (feature dormant)

`runtime.Executor` implements `ExecuteLargeBody`, but zero `AssessLargeBody` /
`LargeBodyAssessor` references exist in `runtimebundle` / `stdhttp` /
`lipruntime` / `lipstd` (grep-verified in 19.6-19.7). `CandidatePrerequisites`
(`internal/plugins/frontends/frontendpipe/profile.go:268`) type-asserts
  `LargeBodyExecutor` (which embeds `LargeBodyAssessor`,
  `internal/core/largebody/capability.go:41-43`), so stock binaries fail the
assertion and route 100% of traffic canonical with zero spooling. All green
lane E2Es use test doubles at that seam. Related open carries: streaming-only
lane policy lives only in test `assessFunc`s (15.4 finding 4);
`Bundle.LargePayloadDiagnostics()` / `SpoolLedger.SetObserver` (19.1) have zero
production callers; metering-egress and cancel-determinism hardening are
test-side only.

## 2. Non-negotiable constraints

- TDD + RED evidence per behavioral task; `review` subagent per task; selective
  `git add`, parent-only `feat(...)` commits, never on `main` (dedicated
  `feat/` worktree; run `codegraph init` in any newly created worktree).
- `git diff --check`, `gofmt`, `go vet` clean; LF endings.
- No fake/shadow `lipapi.Call`; no second decode-admission decision; permit
  released on accept before commit; core never imports frontend/provider types;
  `pkg/lipsdk.ExecutorView` unchanged; public `pkg/lipruntime.Options` stays
  non-money (billing only via `runtimebundle.ComposeBilling`).
- No provider-name switch in generic core; no second plane registry;
  fail-closed on unknown authority; no expected canonical fallback after wire
  commit; no proof outside decode admission; no bypass of static
  `DefinitelyCanonical` or configured legacy resolver.
- Windows: `go test -count=1`, no `-race` (cgo limitation, repo policy).
- 100-changed-`*.go`-files gate applies (branch is at 221 vs merge-base, so
  plan for `allow-large-change` handling or split delivery via
  `lip-pr-delivery`).
- Do not weaken gates to pass them. If 19.3 still fails after rework, lanes
  stay canonical-only.

## 3. Phase plan

### Phase 0 - Freeze and harness (no production diff)

- Freeze current proof-time numbers as regression floor: re-run
  `BenchmarkLargePayloadHeap_ProofTimeTransient` + `RetainedPostProofGC`
  @1/5 MiB, and 20 MiB **mandatory full run** (no `-short` skip for the gate
  decision; keep `-short` gating for default `go test` runs).
- Add failing-first characterization tests asserting the target invariant:
  proof-time B/op bounded by `memory_spool_bytes + max_semantic_fact_bytes +
  fixed buffers` (RED against current `io.ReadAll`).
- Verify: new tests RED, existing suites green.

### Phase 1 - Streaming proof core in `internal/core/largebody`

- Extend the identity-writer streaming API with a message/part path usable by
  profiles (`CallIdentityWriter.BeginMessage` at `identity.go:479` and
  `MessageIdentityWriter.BeginTextPart` at `identity.go:787` exist from
  task 6.2 alongside `StreamingEscapeWriter` at `identity.go:46` - audit
  what `AddMessage`'s whole-message marshal at `identity.go:470` blocks and
  close the gap), plus a chunk-reader helper feeding replay bytes →
  `jsonshape.Scanner` (model span, duplicate/unknown detection) + SHA-256 +
  identity writer in **one pass, fixed buffers, no `ReadAll`**.
- Differential tests: streaming digest identical to `CanonicalCallIdentity`
  byte-for-byte across the 15.1/16.1 corpora + large-string + reasoning-item +
  escaped/late-model cases (closes 15.1/15.2 carry-overs).
- Purity: no I/O, stores, or session reads in new helpers; bounded fact
  budget enforced.
- Verify: `go test ./internal/core/largebody/ ./internal/core/jsonshape/`,
  new RED (whole-message-marshal mutation) then green.

### Phase 2 - Lane `CompileProof` rework, all three lanes

Same shape per lane (OpenAI Responses, OpenAI Chat, OpenResponses no-store);
one commit per lane:

- Replace `ReadAll` + full DTO unmarshal + message-tree build with the Phase 1
  streaming pass. Keep every conservative decline byte-identical (duplicates,
  unknown keys, `store`/continuation controls, body LIP metadata, malformed
  histories, session-hint decline, fact-budget overflow). Model span stays
  scanner-tracked; rewrite via existing `NewModelTokenRewrite`.
- Identity parity re-proven per lane (existing differential tests pass
  unchanged - they are the regression net).
- Verify per lane: lane profile suite + `frontendpipe` + `openaicompat`
  suites; proof-time bench @1 MiB must collapse from ~6.9 MB toward
  O(facts+buffers); RED = revert-to-`ReadAll` mutation fails the Phase 0
  bound tests.
- Risk: streaming extraction missing a normalization the full decode did.
  Mitigation: reuse canonical parsers where they do not materialize, decline
  anything ambiguous (fail-closed, Req 17.3 pattern already used).

### Phase 3 - Re-run the 19.3 gate

- Re-run full `BenchmarkLargePayloadHeap_*` @1/5/20 MiB (20 MiB mandatory)
  + `RetainedPostProofGC`; update `evidence/19.2-19.5-benchmarks.md` §5 with
  new tables.
- Gate rule: strict invariant flips to PASS only if proof-time B/op is
  ~flat across sizes. Any lane still sloping stays canonical-only; others may
  proceed independently (per-lane verdicts, Req 21.13 pattern).

### Phase 4 - Production `LargeBodyAssessor` in `internal/core/runtime`

- Implement a production assessor unifying `AuthorityAssessor` +
  `BackendWireProofAssessor` + `RouteOverrideAssessor` (task-11 pieces; verify
  current names/locations at impl time) with `runtime.Executor`'s
  `ExecuteLargeBody`, composed in `runtimebundle.BuildHost` (composition root:
  `internal/infra/runtimebundle/host_build.go:37/43`; production callers via
  `cmd/lipstd` and `pkg/lipruntime`).
- Encode in production (not test harnesses): streaming-only delivery gate
  (15.4 carry), universal-vs-finite domain policy per lane, same-permit
  decline discipline.
- Wire `LargePayloadConfig.Diagnostics` to `Bundle.LargePayloadDiagnostics()`
  and ledger `SetObserver` (closes 19.1 unwired carry).
- Characterization tests first: stock host without config still 100%
  canonical (zero spooling); with enabled test config + eligible generation,
  `CandidatePrerequisites` succeeds and candidate-path wire executes through
  the real executor.
- Verify: `runtimebundle` host-build suites, `runtime`, lane E2E subsets;
  pre-check archtest line-budget impact (`budgets.go` +25-headroom procedure
  if our lines push ceilings).

### Phase 5 - Enablement wiring and policy hardening

- Formally link `server.large_payload_fast_path` config to `runtimebundle`
  and frontend `Spec`s (today zero references there). Keep default
  `enabled: false`; invalid-reload last-good preserved (2.1 tests).
- Deterministic cancel-triggered cleanup proof (19.2-19.5 review F1: current
  cancel test cannot distinguish cancel-cleanup from normal cleanup -
  slow-reader + ctx-observation fix), metering-egress production-hook
  assertion, Phase-B evidence relabel.
- Verify: config tests, `WireSupportNotAdvertised` x3 still pass (nothing
  advertised yet), metrics tests, doc-convention QA.

### Phase 6 - Re-certification (mirrors 15.3/16.3/17.3)

- Re-run lane differentials + E2E suites unchanged against streaming proof +
  production assessor (any failure is a real regression: fix production, never
  the tests).
- Re-run archtest full (`budgets.go` bumps only per procedure with
  attribution), `internal/qa`, full `go test ./...` with baseline-vs-branch
  triage for the two known upstream doc-marker failures, fuzz smoke on
  touched scanner/identity paths.
- Refresh `evidence/19.6-19.7-eligibility-roi.md` per-lane verdicts: a lane
  flips to advertise-capable only with (a) 19.3 strict PASS, (b)
  production-assessor green, (c) streaming-only enforced in production.

### Phase 7 - Rollout (docs-only, mirrors 20.3-20.4)

- Update `docs/large-payload-fast-path.md` §4.3/§7 (new heap/latency numbers,
  per-lane advertisement status), new `evidence/20.x` closeout delta,
  `tasks.md` notes. No enablement until 19.7 per-lane gates pass. #532 stays
  open until then.

## 4. Verification matrix

| Phase | Must-run commands | Gate |
|---|---|---|
| 0 | new bound tests (RED), `frontendpipe` suite | RED captured |
| 1 | `largebody`, `jsonshape` suites + digest differentials | streaming identical to canonical digests |
| 2 (x3) | lane profile + `frontendpipe` + `openaicompat` suites; proof bench @1 MiB | B/op collapse; no decline-behavior change |
| 3 | full heap benches 1/5/20 MiB (20 MiB mandatory) | 19.3 strict PASS/FAIL per lane |
| 4 | `runtimebundle`, `runtime`, lane E2E subsets, archtest | stock-canonical default; eligible-config wires via real executor |
| 5 | config, metrics, qa doc-conventions, `WireSupportNotAdvertised` x3 | nothing advertised prematurely |
| 6 | differentials, E2Es, archtest, full `go test ./...`, fuzz smoke | green + triaged |
| 7 | `git diff --check`, qa docs | closeout artifacts |

## 5. Risks and mitigations

- Streaming extraction misses a normalization: fail-closed declines +
  differential corpora catch it; never weaken a decline to pass.
- Assessor composition changes production routing: default-off +
  characterization-first (stock host byte-identical), archtest budget
  procedure, full-suite triage.
- Permit-hold regression: 19.2 permit-hold test re-run is a hard gate in
  Phase 3.
- Scope creep into lane semantics: lane rework is mechanical (same declines,
  same digests); any semantic change is a separate spec task.

## 6. Handoff checklist for the implementing session

- Re-verify HEAD, branch, and `git status` clean in the `feat/` worktree
  before Phase 0.
- Re-run the two grep proofs (zero `AssessLargeBody` in
  `runtimebundle`/`stdhttp`; `CompileProof` `io.ReadAll` still present per
  lane) - they are cheap and the claims must be current.
- Kiro spec activity for this remediation is backend verification/rollout
  work, matching the existing steering note that specs stay opt-in with
  direct-code small fixes and narrow tests going straight to code.
