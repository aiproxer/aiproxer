# Implementation Plan: proxy-memory-residual-consolidation

> **Executor notes (read first):**
> - TDD: write the listed tests first and run them to see them fail (RED), then
>   implement (GREEN), then refactor. Keep RED evidence per
>   `docs/agent-handoffs.md`.
> - Never import `internal/core/...`, `internal/infra/...` or
>   `internal/standardplugins/...` from `internal/plugins/features/proxymemory`.
> - Never log or label metrics with project paths, principal IDs, A-leg IDs,
>   prompts or memory text.

- [x] 1. Store contract: obligation, order and settlement

- [x] 1.1 Extend the extraction schema and parity inventory
  - Add `consolidated_at_ms BIGINT NOT NULL DEFAULT 0` to
    `proxy_memory_extractions` in `migrations.go`; update the column inventory
    in `store_test.go` and the parity spec in `schema_spec_test.go`.
  - Reset `consolidated_at_ms = 0` in `extractionUpsertTx`'s conflict update.
  - RED first: the column inventory and parity spec fail on the new column.
  - _Requirements: 1.4, 1.7_
  - _Validation: `go test ./internal/infra/proxymemorystore/ -run 'TestSchema|TestStore' -count=1`_

- [x] 1.2 Return outstanding extractions and window names
  - Replace `ConsolidationInput`'s return with `ConsolidationBatch`;
    select outstanding rows oldest first, and the full in-window summary-name
    set.
  - RED first in `contract_test.go`: the limited read returns the older row, the
    wide read reports every window name, and a re-upserted A-leg is outstanding
    again.
  - _Requirements: 1.1, 1.2, 1.3, 2.1_
  - _Validation: `go test ./internal/infra/proxymemorystore/ -run 'TestDBParity_SQLite' -count=1`_

- [x] 1.3 Acknowledge carried revisions and settle atomically
  - `PublishBank` acknowledges `Publication.Acknowledged` revision-exactly, then
    advances `consolidated_seq` only when no in-window row remains outstanding,
    in the same transaction.
  - RED first in `contract_test.go`: a partial acknowledgment keeps
    `consolidated_seq`, a stale generation time acknowledges nothing, and a
    lease-lost publication acknowledges nothing.
  - _Requirements: 1.4, 1.5, 1.6, 1.8_
  - _Validation: `go test ./internal/infra/proxymemorystore/ -run 'TestDBParity_SQLite' -count=1`_

- [x] 2. Worker: window identity, prefix fit and acknowledgment

- [x] 2.1 Compare CHANGES against the window
  - Add `Phase2Input.WindowNames`; render CHANGES from it; retain previous names
    intersected with the window plus the carried names.
  - RED first in `prompts_phase2_test.go` and `worker_test.go`: a batch-omitted
    name is not reported as removed, and a name outside the window is removed.
  - _Requirements: 2.2, 2.3, 2.4_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ -run 'TestPhase2|TestWorker' -count=1`_

- [x] 2.2 Carry the scheduled order into the prompt fit
  - `fitPhase2Extractions` keeps the oldest prefix of the outstanding batch;
    update its comment and the budget tests.
  - RED first in `prompts_phase2_test.go`: an over-budget batch keeps the older
    extraction and leaves the newer one for the next attempt.
  - _Requirements: 1.3, 2.5_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ -run 'TestPhase2BuildRequestHonorsTokenBudget' -count=1`_

- [x] 2.3 Publish exactly what the prompt carried
  - Build `Acknowledged` and `WindowSinceMS` from the attempted input; refuse
    when outstanding content was left out by the prompt budget.
  - RED first in `worker_test.go`: the publication acknowledges only the carried
    revisions, and a budget-refused attempt publishes nothing.
  - _Requirements: 2.5, 2.6, 3.3_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ -count=1`_

- [x] 3. Regression on the real store and both engines

- [x] 3.1 Store regression on SQLite and PostgreSQL Direct
  - Add the two-attempt drain case, stale-ack case, lease-lost case and window
    identity case to `contract_test.go`, so both parity entry points run them.
  - _Requirements: 3.1, 3.2_
  - _Validation: `go test ./internal/infra/proxymemorystore/ -run 'TestDBParity_SQLite' -count=1`; `LIP_REQUIRE_POSTGRES=1 go test -tags=integration ./internal/infra/proxymemorystore/ -run 'TestDBParity_PostgresDirect' -count=1`_

- [x] 3.2 Worker drain regression against the real store
  - Extend `proxy_memory_phase2_progress_test.go`: cap one, two extractions, two
    ticks, first request carries the older extraction, bank settles only after
    the second.
  - _Requirements: 1.2, 3.3_
  - _Validation: `go test ./internal/standardplugins/featurehost/ -run 'TestProxyMemoryPhase2' -count=1`_

- [x] 4. Documentation and gates

- [x] 4.1 Sync the design text and run the delivery gates
  - Update `.kiro/specs/proxy-codex-memory/design.md` §12.5 only where it states
    the sampling behavior (shrink-only: replace, do not add).
  - `make dev-test-changed DEV_BASE=<base>`, `make dev-contract-check
    DEV_BASE=<base>`, `go run ./tools/kiro/kirocheck`.
  - _Requirements: 3.1, 3.2_
  - _Validation: the three commands above_

## Completion Status

- [x] Implementation merged to `main` by #1412 (merge `89c7b99441da7b37be666091113374dc33565ddc`), on the #1411 prerequisite (merge `c8364b4d81211cc82ba23a9db4b6b191687026af`).
- [x] Task 4.1 gates on head `ab733ae4`: `make dev-test-changed` and `make dev-contract-check` against `origin/main` (the only failure, `TestCertGateway_RealRuntime`, reproduces on `main`: host Node version drift), `go run ./tools/kiro/kirocheck`, SQLite parity, and PostgreSQL Direct parity on a fresh database (69/69).
- [x] Merged-main verification at `89c7b994`: `go build ./...` and `go test` for `internal/infra/proxymemorystore`, `internal/plugins/features/proxymemory`, `internal/standardplugins/featurehost`, `internal/core/billing`, `internal/core/runtime` and `cmd/lipstd`.

Deferred, not delivered here: the phase 2 token-budget refusal for a backlog that exceeds `phase2_input_tokens` (#1450), and freezing the store baseline schema before the first release (#1451).
