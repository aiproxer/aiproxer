# Implementation Plan

> Owner-approved for implementation. Task 1 (gate-off characterization) lands before any
> runtime change and stays green unmodified through every later task.

- [x] 1. Gate-off characterization test, before any runtime change
  - Reuse the existing scripted loop harness (`retryRecvStream`) to pin today's behavior for a clean empty finish, a contentful finish, and a reasoning-only finish: the client-visible event sequence, usage handling, and metric counts (including `lip_empty_visible_turn_total`). Once task 2 lands, the same scripts also run with the gate explicitly disabled. Every later task keeps this test byte-identical.
  - Observable: the characterization test passes on the unchanged tree and stays green unmodified through tasks 2-9; gate-off runs write no decision record and increment no new counter.
  - _Requirements: 1.1, 1.2_
  - _Boundary: core/runtime integration test_
  - _Depends: none_
  - _Validation: `make dev-test PKGS='./internal/core/runtime/...'` (focused `-run 'TestEmptyVisibleTurnRetryGateOff'`)_

- [x] 2. Opt-in gate: typed config section, validation, generation wiring, example (P)
  - `internal/core/config/empty_visible_turn_retry.go` (`EmptyVisibleTurnRetryConfig{Enabled bool}` plus effective resolution), field on `Config`, hook in `validate.go`; resolve it in `internal/infra/runtimebundle/build_executor.go` into the executor policy the receive stream captures; `config/examples/empty-visible-turn-retry.yaml` (disabled plus enabled snippet).
  - Observable: `go run ./cmd/lipstd check-config --config config/examples/empty-visible-turn-retry.yaml` succeeds; `TestConfigExamples_passInspectRoutes` loads the new example; an omitted or disabled section resolves to `false`, an enabled one to `true`, and a reload builds a new generation value while an in-flight turn keeps its own.
  - _Requirements: 1.1, 1.3_
  - _Boundary: config/wiring (composition root)_
  - _Depends: none_
  - _Validation: `make dev-test PKGS='./internal/core/config/... ./internal/infra/runtimebundle/...'`_

- [x] 3. Bounded prefix hold on the attempt (P)
  - `correctionHold` (armed/released/overflowed/discarded) with an ordered queue of prepared frames bounded by the existing retention constants (`maxWithheldEvents`, `maxWithheldBytes`) shared with the tool-name withhold queue and guarded by the same `withholdMu`; deep copy only after the bound check; ordered pop for release; discard wired into the existing `discardWithheld`/`discardSidebandState` lifecycle chokepoint.
  - Observable: table tests prove the event bound, the byte bound at the exact boundary, deep-copied retention, one pop per frame, and that cancellation, close, replacement, and disposal free the queue without releasing a frame; a nilled assembler does not affect the hold.
  - _Requirements: 3.1, 3.3, 3.5, 3.6_
  - _Boundary: core/runtime (attempt lifecycle)_
  - _Depends: 1_
  - _Validation: `make dev-test PKGS='./internal/core/runtime/...'` (focused `-run 'TestEmptyVisibleTurnRetryHold'`)_

- [x] 4. Terminal facts: released-frame latch, one-shot allowance, gate capture
  - `turnTerminal.releasedClientFrame` latched wherever a canonical frame is returned to the client (dispatch and drain release tails, keepalives excluded); `claimCorrectiveRetry()` one-shot compare-and-swap; capture the generation gate in `bindTurnTerminalRuntime` so a turn pins its value.
  - Observable: a released `response_started`, usage delta, or reasoning delta sets the latch; the latch survives attempt replacement; the allowance is claimed once across replacements; existing `empty_visible_turn` metric tests stay green and `lip_empty_visible_turn_total` still fires only at the accepted winner.
  - _Requirements: 1.3, 2.3, 4.1_
  - _Boundary: core/runtime (terminal)_
  - _Depends: 1, 2_
  - _Validation: `make dev-test PKGS='./internal/core/runtime/...'` (focused `-run 'TestEmptyVisibleTurnRetryTerminal'`)_

- [x] 5. Correction decision: rails in fixed order, decline records, counter seam
  - `maybeCorrectEmptyVisibleTurn` evaluates candidacy first, then the bounded rails in their declared order (`gate_disabled`, `budget_spent`, `released_frames`, `withholding`, `hidden_thinker`, `wire_path`, `completion_gates`, `parallel_race`, `terminal_decision_provider`, `replacement_blocked`, `attempt_budget`, `hold_overflow`, `hold_not_armed`) and records the first decline as one `skipped` decision record; a wired terminal-decision provider is declined without touching its evaluation path; `EmptyVisibleTurnRetryObserver` optional sink plus `Executor.recordEmptyVisibleTurnRetry`.
  - Observable: a table test per reason code; a wired provider leaves its evaluation path untouched; the record carries lineage, outcome, and reason only; no content, instruction, or model identifier appears.
  - _Requirements: 2.1, 2.2, 2.4, 4.7, 5.2, 5.4_
  - _Boundary: core/runtime (app orchestration plus driven observation port)_
  - _Depends: 3, 4_
  - _Validation: `make dev-test PKGS='./internal/core/runtime/...'` (focused `-run 'TestEmptyVisibleTurnRetryDecision'`)_

- [x] 6. Receive-loop wiring: arm, hold, release order, finish intercept
  - Arm the hold per attempt (gate on, allowance intact, latch clear); hold prepared frames instead of dispatching them; drain held frames through `dispatchClientFacingEvent` before reading the backend; intercept the prepared finish; release the held prefix before a finish that declines correction; release or discard on EOF, error, and close paths following the existing withhold rules.
  - Observable: scripted loop tests show the held `response_started` (and reasoning) released first and in order when text arrives, released unchanged on overflow, released before an uncorrected finish, and discarded on close or cancellation; each frame is transformed and recorded exactly once.
  - _Requirements: 2.3, 3.2, 3.4, 3.5, 3.6_
  - _Boundary: core/runtime (receive loop)_
  - _Depends: 5_
  - _Validation: `make dev-test PKGS='./internal/core/runtime/...'` (focused `-run 'TestEmptyVisibleTurnRetryLoop'`)_

- [x] 7. Corrective replacement trigger: bounded single open, retirement, published-or-failed record
  - On an accepted decision: claim the allowance before the fallible open, discard the hold, mark the correction pending, retire the empty attempt with the existing non-surfacing evidence, then run exactly one `recovery.tryReplacementIteration` through the corrective branch (no keepalive re-plan) with the existing prepare/register/swap sequence; record `corrected` once after publication, or `failed` once on the open, preparation, registration, or closed-window failure, taking `terminalizeReplacementFailure` with a typed `*lipapi.UpstreamFailureError{Phase: PhasePreOutput, Recoverable: true}` cause; a second clean empty finish records its own `skipped`/`budget_spent` and is delivered as the accepted outcome.
  - Observable: with a scripted empty B1 and contentful B2 the client receives only B2's frames, exactly one corrective open call happens, and B1 settles as non-surfacing with its usage evidence retained; a scripted empty B1 and empty B2 delivers one empty finish, increments `lip_empty_visible_turn_total` once, and writes two records (B1 `corrected`, B2 `skipped`/`budget_spent`); an uncorrectable open ends the turn through the failure terminal with that typed recoverable cause (asserted with `errors.As`), releases no held frame, and writes one `failed` record.
  - _Requirements: 4.2, 4.3, 4.4, 4.5, 4.6_
  - _Boundary: core/runtime (receive loop plus attempt lifecycle)_
  - _Depends: 6_
  - _Validation: `make dev-test PKGS='./internal/core/runtime/...'` (focused `-run 'TestEmptyVisibleTurnRetryReplacement'`)_

- [x] 8. Retry counter family `lip_empty_visible_turn_retry_total` (P)
  - `ExecutorProm` counter with the closed `{backend, outcome}` label set (`corrected|skipped|failed`), the sink method implementing the new observer, and the family ratchet extended; the existing `lip_empty_visible_turn_total` label set must not change.
  - Observable: the family appears with exactly the closed outcome set, an empty or blank backend uses the existing fallback label, and the ratchet test fails on any extra label.
  - _Requirements: 5.1, 5.3, 5.4_
  - _Boundary: config/wiring (metrics adapter)_
  - _Depends: 5_
  - _Validation: `make dev-test PKGS='./internal/infra/metrics/...'`_

- [x] 9. Provider-unchanged proof and operator documentation
  - With a wired terminal-decision provider, prove its evaluation and decision path is byte-identical with the gate on and off; document the gate in `config/config.yaml` next to `stream_recovery` with its disabled default, the conservative provider rule, and the explicit recoverable failure outcome.
  - Observable: a wired terminal-decision provider keeps its evaluation path byte-identical, the task 1 characterization test is still green unmodified, the default config still loads, and the documented snippet matches the example file.
  - _Requirements: 1.1, 1.2, 4.7_
  - _Boundary: core/runtime integration test plus docs_
  - _Depends: 7, 8_
  - _Validation: `make dev-test PKGS='./internal/core/runtime/... ./internal/infra/runtimebundle/...'`_

## Completion Status

- [x] All nine leaf tasks are checked; no unchecked, blocked, or deferred task remains in this plan.
- [x] The implementation is delivered by this same pull request: branch `feat/empty-visible-turn-retry`, squashed into `main` by that PR. The merged baseline is the PR's merge commit, confirmed on GitHub after the merge.
- [x] The required CI Go suite (three shards, 419 root packages under `-tags=precommit`, with `-tags=precommit,integration` for `internal/infra/runtimebundle`) and the tagged QA policy preflight were reproduced locally on the delivered head. The only local failure observed was the Kiro lifecycle gate on the active, fully checked spec (`active/empty-visible-turn-retry: all 9 tasks are checked; completed specs must be archived`), which this archival closes; the tagged QA suite, the three Go-suite shards, the owner-callback gate, the architecture guardrails, the change-surface ratchet, the CI lint scope, the database dialect parity, the operator example config check, and the affected feature evidence were re-run on this archived head and passed.
- [x] Hosted GitHub Actions was billing-blocked in this environment (every job on the PRs merged today failed with zero executed steps), so no remote check ran. Windows and macOS execution, the remote-only race detector, and CodeQL are not verifiable on this development host and are not claimed.
- [x] One baseline failure was observed on `origin/main` and was not owned by this slice: `TestLive_CommandCodeOpenAI` in `connectors/commandcode-openai` failed with a third-party `http 429 (RATE_LIMITED)` because the configured live account's weekly quota is exhausted. The probe was untagged, so the default module path ran a real provider call. It is now fixed upstream by #1305, which moved the commandcode live probes behind the `integration` build tag; this delivery is rebased on that commit and the default path no longer runs them.
- [x] The deferred scope remains as listed in `requirements.md` and is not implied by this archive: #1236 (open-first corrective publication preserving the original empty finish on open failure), #1237 (large-body/wire path parity), #1238 (hidden thinker legs), #1239 (truncated and refusal terminals), #1240 (operator-tunable hold bounds and per-backend budgets), #1241 (non-empty but unusable output), and #1242 (coexistence with a wired terminal-decision provider).
- [x] `spec.json` updated: `phase=completed`, `completed=true`, `ready_for_implementation=false`. The approved requirements, design, and task approvals are retained unchanged.
