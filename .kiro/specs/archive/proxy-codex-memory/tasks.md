# Implementation Plan: proxy-codex-memory

> **Executor notes (read first):**
> - Owner-approved oversize spec: it is detailed enough for small-model execution. Every task names its files, the design section (§) that defines exact behaviour, the failing test to write first, and the command that proves it.
> - TDD: write the listed tests first and run them to see them fail (RED), then implement (GREEN), then refactor. Keep RED evidence per `docs/agent-handoffs.md`.
> - Never import `internal/core/...`, `internal/infra/...` or `internal/standardplugins/...` from `internal/plugins/features/proxymemory`. The archtest closure rule fails the build otherwise.
> - Never log or label metrics with project paths, principal IDs, A-leg IDs, prompts or memory text.
> - **Suggested PR slices**, each ≤ 100 `*.go` files and about 1,500 non-test lines:
>   - PR A = tasks 1–2 (foundation and persistence);
>   - PR B = tasks 3 and 4.1–4.2 (content, prompts, capture, worker);
>   - PR C = tasks 4.3–4.4 and 5.1–5.5 (service, HTTP, core change, composition, sentinel, docs).
>
>   A store without a consumer must be followed immediately by its consumer PR (`delivery.md`, substrate rule).

- [x] 1. Foundation: configuration, eligibility, project identity and shared contracts

- [x] 1.1 Feature configuration and principal eligibility
  - Create `internal/plugins/features/proxymemory/doc.go`, `config.go` and `owner.go` exactly as design §1 and §2 define them: the `ID` constant, the `Config` fields and defaults, strict `DecodeConfig` (snake_case keys, durations as strings, unknown keys and nested `enabled` rejected), `Validate`, `Principal`, `Owner`, the `Ineligible` constants, `LocalFallbackPrincipalID`, `CheckEligible` and `HasSession`.
  - RED first, in `config_test.go`, as a table test covering:
    - an empty node gives a validation error for the missing `principals` and routes;
    - all defaults are applied when only the required keys are set;
    - `["*", "alice"]` is rejected;
    - a blank route is rejected and the error names `extract_route` or `consolidate_route`;
    - `min_idle >= max_session_age` is rejected;
    - `summary_tokens > 4000` is rejected;
    - an unknown key and a nested `enabled` are rejected.
  - RED first, in `owner_test.go`, as a table over `scope.PrincipalScopeView`, one case per rule in §2 in order:
    - unknown principal;
    - internal origin;
    - the fallback ID `lip_local_unknown`;
    - local subject with and without `AllowLocalPrincipal`;
    - not allowlisted;
    - allowlisted;
    - wildcard;
    - tenant known versus unknown maps to `TenantID` `"x"` versus `""`;
    - `HasSession` is false for an empty view, true with only `AuthoritativeSessionID`, true with only `ClientSessionHint`, and false for whitespace-only values.
  - **Done when** both tables pass and `go vet` is clean for the package.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.8, 1.9, 3.7_
  - _Boundary: feature plugin (proxymemory)_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ -run 'TestConfig|TestCheckEligible' -count=1`_

- [x] 1.2 Project derivation, redaction and token truncation
  - Create `project.go` (design §3), `redact.go` (design §4) and `truncate.go` (design §6) in the feature package.
  - RED first, in `project_test.go`, as a table building `lipapi.Call` values:
    - a `workspace_path` extension wins over the instruction line;
    - extension priority order is `project_dir` > `workspace_path` > `cwd` > `project` (the same as `sessionwire.existingWorkspaceExtensionKeys`);
    - a non-string extension is skipped;
    - `Working directory: /a/b/` in `Instructions` gives label `/a/b`;
    - `Primary working directory: C:\\x\\y` in a system message gives `C:/x/y`;
    - a Codex user text part starting with `<environment_context>` and containing `<cwd>/r</cwd>` gives `/r`;
    - the same tag inside a longer user text that does **not** start with it is ignored;
    - Items-authority equivalents of the last two;
    - empty, over-length and control-character values fall through to the next source;
    - no source gives `ok=false`;
    - the key equals the hex SHA-256 of `"proxy-memory/v1\x00" + label`, with a fixed expected value asserted.
  - RED first, in `redact_test.go`: one case per Codex pattern (`sk-` key, `AKIA…`, `Bearer …`, `api_key = "…"`, `password: …`), the exact replacement strings, and one untouched string.
  - RED first, in `truncate_test.go`:
    - under the limit returns the same string;
    - over the limit keeps the head and tail with the marker containing the removed-token count;
    - multi-byte runes are never split (result is `utf8.ValidString`).
  - **Done when** all three test files pass. No function touches `os` or `filepath` (check with `grep -n '"os"\|path/filepath' project.go` returning nothing).
  - _Requirements: 2.1, 2.2, 2.3, 2.5, 3.3_
  - _Boundary: feature plugin (proxymemory)_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ -run 'TestDeriveProject|TestRedact|TestTruncate' -count=1`_

- [x] 1.3 Store contract types, summary names and metrics
  - Create `store.go` with every type, sentinel error and the `Store` interface exactly as in design §8, with doc comments stating the owner-predicate invariant. Also define `SummaryName(e Extraction) string` here, per its §7.3 definition. It is the single definition used by tasks 2.3, 3.3 and 4.3.
  - Create `metrics.go` per design §13: a `prometheus.CounterVec` `lip_proxy_memory_events_total{event}`, exported constants for every event value, a nil-safe `Inc`, and `NewPrometheusCollector()` (follow `internal/plugins/features/keepwarm` collector style).
  - RED first, in `metrics_test.go`:
    - `Inc` on a nil `*Metrics` does not panic;
    - after `Inc(EventCaptureRecorded)` the collector reports value 1 for that label (use `prometheus/testutil.ToFloat64`);
    - `SummaryName` of an extraction with `GeneratedAtMS` 0, `SessionRef` `session-0123456789ab` and slug `fix-tests` is `1970-01-01T00-00-00-012345-fix-tests`.
  - **Done when** the package compiles and the metrics test passes.
  - _Requirements: 1.5, 1.6_
  - _Boundary: feature plugin (proxymemory)_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ -run TestMetrics -count=1`_

- [x] 2. Persistence family `proxy-memory`

- [x] 2.1 Schema, capture upsert, fence check and published summary
  - Create `internal/infra/proxymemorystore/{doc.go,store.go,migrations.go,20261016000000_proxy_memory_baseline.go,sessions.go}`. The baseline file name must match `^\d{14}_.*\.go$` (archtest database-parity discovery):
    - `New(db *bun.DB) *Store` and `EnsureSchema(ctx, db)`, which uses `bunmigrate.Migrate` with table `bun_proxy_memory_migrations` and the DDL of design §12.1 for both dialects;
    - `UpsertSession` (§12.2, including live-row and durable extracted-source stale guards) and `PublishedSummary` (§12.6); ledger migration `20261018000000` creates the exact source-activity field from the start (unshipped feature, no legacy upgrade path);
    - stub every other `Store` method with `errors.New("not implemented")` so `var _ proxymemory.Store = (*Store)(nil)` compiles.
  - Create `contract_test.go` with `runContract(t *testing.T, open func(t *testing.T) *bun.DB)`, and `store_test.go` calling it with in-memory SQLite. Reuse the DSN helper used by `internal/infra/conversationview/bun_store_test.go`.
  - RED first, contract cases:
    - a first upsert stores revision 1;
    - a strictly newer upsert gives revision 2; older/equal captures cannot replace a live row or recreate an extracted row, including after purge and after a second extraction; a genuinely newer continuation recreates the row;
    - an upsert for the same A-leg with a **different principal** returns `UpsertOwnerMismatch` and leaves the original row unchanged;
    - a different project key gives `UpsertOwnerMismatch`;
    - an upsert for an A-leg present in `proxy_memory_fence` returns `UpsertForgotten` and inserts nothing;
    - an upsert for an A-leg with **no session row** but an existing `proxy_memory_extractions` row owned by another project or principal returns `UpsertOwnerMismatch` (seed the extraction row directly with SQL);
    - `PublishedSummary` returns `ErrNotFound` for a missing bank and for an empty summary, and returns the summary and version for a seeded bank;
    - **isolation**: a bank seeded for principal A with project P is `ErrNotFound` when queried by principal B with project P.
  - **Done when** the SQLite contract cases pass and `EnsureSchema` is idempotent (running it twice succeeds).
  - _Requirements: 1.5, 2.4, 8.6_
  - _Boundary: infrastructure (proxymemorystore)_
  - _Depends: 1.3_
  - _Validation: `go test ./internal/infra/proxymemorystore/ -count=1`_

- [x] 2.2 Leases, extraction completion and CAS publish
  - Implement in `sessions.go` and `banks.go`: `ClaimSessions`, `CompleteSession`, `FailSession` (§12.3, §12.4), and `ClaimBanks`, `ConsolidationInput`, `PublishBank`, `FailBank` (§12.3, §12.5).
  - Use `db.RunInTx`. On PostgreSQL, add ` FOR UPDATE` to the locking `SELECT` in `CompleteSession` (branch on `db.Dialect().Name() == dialect.PG`, precedent `internal/infra/conversationview/bun_bootstrap.go`).
  - RED first, contract cases:
    - **Idle gating**: a session outside the idle window is not claimed; lease acquisition returns the coherent transcript/revision/activity atomically. A capture committed just after acquisition cannot replace the returned idle snapshot and remains pending after that older extraction completes.
    - **At most one owner**: two claims with different `LeaseOwner` at the same `NowMS`. The first gets the row and the second gets none. After `NowMS` passes `lease_until_ms`, the second can claim it.
    - **Complete with the same revision**: the extraction is stored, the session row is deleted, the bank exists with `dirty_seq=1`.
    - **Complete after a newer upsert**: the extraction is stored, the session row remains with its lease cleared.
    - `CompleteSession` with the wrong lease owner returns `ErrLeaseLost`;
    - `CompleteSession` for an A-leg that has a fence row deletes the session row and returns `ErrLeaseLost`, with no extraction stored;
    - `CompleteSession` when an extraction row for the A-leg exists under a different owner returns `ErrLeaseLost` and changes nothing;
    - with a non-empty skip list, the claim query still compiles on both engines; with an empty skip list, no `NOT IN` clause is emitted.
    - `FailSession` clears the lease and sets `next_attempt_ms`; a claim before that time returns nothing. The settlement is revision-aware: a failure of a superseded revision releases the obsolete lease but keeps the newer revision's own retry gate, so the newer revision stays claimable on its own idle window.
    - **Bank claim**: only when `dirty_seq > consolidated_seq`. Publication deletes exactly consumed note IDs and keeps the bank dirty while residual notes remain; `max_note_bytes ≤ phase2_note_bytes` ensures notes-only passes can progress.
    - `PublishBank` with a stale `Version` or the wrong lease owner returns `ErrLeaseLost` and leaves the bank unchanged.
    - `ConsolidationInput` returns at most `limit` extractions within `sinceMS`, ascending by `generated_at_ms`, and only for that owner (seed a second principal to prove exclusion).
  - **Done when** all cases pass on SQLite.
  - _Requirements: 4.3, 4.4, 4.5, 4.6, 5.2, 5.3, 5.4, 5.5, 5.7_
  - _Boundary: infrastructure (proxymemorystore)_
  - _Depends: 2.1_
  - _Validation: `go test ./internal/infra/proxymemorystore/ -count=1`_

- [x] 2.3 Service queries, forget, purge, PostgreSQL parity and dbparity registration
  - Implement in `service_queries.go` and `purge.go`: `ListBanks`, `BankStatus`, `Documents` (using `proxymemory.SummaryName` from task 1.3), `AddNote`, `Forget` and `Purge` (§12.6, with the signature from §8).
  - Add `store_postgres_test.go` with `//go:build integration`, calling `runContract` with `testkit.SkipUnlessPostgres` (copy the setup from `internal/infra/conversationview/bun_store_postgres_test.go`).
  - Register the `proxy-memory` component in `internal/testkit/dbparity/catalog.go` per §12.7. Raise the pinned count from 9 to 10 and add `proxy-memory` to the expected ID lists in `internal/testkit/dbparity/catalog_test.go` and `inventory_test.go`. Update the "all 9" doc comment.
  - RED first, contract cases:
    - `ListBanks` for A returns only A's banks, with correct pending and extraction counts.
    - `Documents` returns `MEMORY.md`, the summary and the rollout summaries keyed `<SummaryName>.md`.
    - `AddNote` on a missing bank returns `ErrNotFound`. On an existing bank it stores the note and bumps `dirty_seq`.
    - `Forget` on A's bank deletes its sessions, extractions, notes and bank, writes fence rows for its A-legs, and leaves B's identical-path bank intact. A second `Forget` returns `ErrNotFound`.
    - **Forget during extraction**: claim a session, `Forget`, then `CompleteSession` returns `ErrLeaseLost`, and no extraction or bank row exists afterwards.
    - `Purge` deletes old sessions, deletes expired extractions while bumping their bank's `dirty_seq`, deletes session rows whose A-leg is fenced, and **never** deletes fence rows.
    - **Durable forget**: purge retains identity/timing ledger metadata; Forget fences contributors from that ledger and deletes it. After purge-then-Forget, the contributor remains `UpsertForgotten`. Prove fresh-schema parity and idempotence; source activity remains exact after repeated extraction and purge.
  - **Done when**:
    - the SQLite contract passes;
    - `go vet -tags integration ./internal/infra/proxymemorystore/` passes;
    - `go test ./internal/testkit/dbparity/... ./internal/archtest/ -run 'Parity|Catalog|Inventory' -count=1` passes with the new component;
    - if a PostgreSQL DSN is available, `LIP_TEST_POSTGRES_DSN=… go test -tags integration ./internal/infra/proxymemorystore/ -count=1` passes. Otherwise state in the handoff that CI provides the PostgreSQL evidence.
  - _Requirements: 1.5, 4.8, 5.2, 5.6, 7.5, 8.1, 8.2, 8.3, 8.5, 8.6_
  - _Boundary: infrastructure (proxymemorystore), test catalog (dbparity)_
  - _Depends: 2.2_
  - _Validation: `go test ./internal/infra/proxymemorystore/ ./internal/testkit/dbparity/... -count=1`_

- [x] 3. Content rendering and Codex prompts

- [x] 3.1 (P) Transcript renderer and assistant accumulator
  - Create `transcript.go` per design §5 (`Entry`, `RenderHistory`, `AssistantAccumulator`, `EncodeTranscript`).
  - RED first, in `transcript_test.go`:
    - **Messages authority**: system and developer messages and `Instructions` are absent; a user text becomes a `message` entry; an assistant text plus a `PartJSON` tool call becomes a `message` entry plus a `function_call` entry; a role-`tool` `PartToolResult` with JSON string content becomes `function_call_output` with the unquoted output; reasoning parts are absent; `image_ref` becomes `[image omitted]`.
    - **Items authority**: `tool_call` and `tool_result` items map correctly; reasoning, compaction and extension items are absent.
    - **Fragment filter**: a user part `# AGENTS.md instructions … </INSTRUCTIONS>` (mixed case) is dropped; `<skill>…</skill>` is dropped; a message left empty is dropped.
    - **Accumulator**: `text_delta` ×2, `tool_call_started`, `tool_call_args_delta` ×2 give one assistant message and one call with concatenated arguments.
    - **`EncodeTranscript`**: the output is a valid JSON array; secrets are redacted; NUL bytes are removed; with a small `maxBytes` the **oldest** entries are dropped first and `truncated == true`; a single oversized entry is truncated and the output is still valid JSON; output under the limit has `truncated == false`.
  - **Done when** the tests pass and `RenderHistory` never mutates its input (assert deep-equality of a clone before and after).
  - _Requirements: 3.1, 3.2, 3.3, 3.6_
  - _Boundary: feature plugin (proxymemory)_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ -run TestTranscript -count=1`_

- [x] 3.2 (P) Vendored Codex templates and phase 1 request and parser
  - Fetch the five upstream files listed in design §7.1 at commit `0df4aa67781ae45a3c97befa4db48b734afe46d9`, for example `curl -fsSL https://raw.githubusercontent.com/openai/codex/0df4aa67781ae45a3c97befa4db48b734afe46d9/<path>`. Save them byte-for-byte under `internal/plugins/features/proxymemory/templates/codex/`.
  - Write `templates/codex/NOTICE.md` (source, commit, Apache-2.0, "unmodified").
  - Create `prompts.go` with `//go:embed templates`, the template renderer (literal `{{ name }}` replacement), `SessionRef`, `BuildPhase1Request` and `ParsePhase1` (§7.2). Copy the request-building style from `internal/plugins/features/reasoningpreservation/compressor_request.go`.
  - RED first, in `prompts_phase1_test.go`:
    - the request has `Role` `proxy_memory_phase1`, `SessionModeDetached`, `DisablePlugins == []string{"proxy-memory", "proxy-memory.capture"}`, the route selector equal to the config, `ReasoningEffort` `low`, `ResponseMIMEType` `application/json`, and `ToolChoiceNone`;
    - the system text equals the embedded `stage_one_system.md`;
    - the user text contains the session ref, the project label and the transcript;
    - an over-limit transcript is truncated with the marker;
    - `call.Validate()` passes.
  - RED first, parser cases: plain JSON; fenced ```` ```json ````; null slug becomes `session`; a slug with spaces and capitals is normalized; an unknown field is rejected; a blank summary is rejected; secrets inside fields are redacted; NUL bytes are removed.
  - **Done when**:
    - the tests pass;
    - `sha256sum` of each vendored file matches the upstream raw file (record the hashes in NOTICE.md);
    - `SessionRef("x")` is `"session-"` plus 12 hex characters.
  - _Requirements: 4.2, 4.3, 4.7_
  - _Boundary: feature plugin (proxymemory)_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ -run TestPhase1 -count=1`_

- [x] 3.3 Phase 2 request and parser, summary names, and injection text
  - Add the following to `prompts.go`:
    - the `EXTENSIONS_FOLDER_STRUCTURE` and `EXTENSIONS_PRIMARY_INPUTS` constants copied from upstream `codex-rs/memories/write/src/lib.rs` (`mod prompt_blocks`), with an attribution comment;
    - `BuildPhase2Request`, `ParsePhase2` and `RenderInjection` (§7.3, §7.4).
  - Write the proxy templates `templates/proxy_phase2_header.md`, `templates/proxy_phase2_footer.md` and `templates/proxy_read_path.md` with the content rules in §7.3 and §7.4. Each file starts with a comment line saying it is derived from the Codex templates under Apache-2.0.
  - RED first, in `prompts_phase2_test.go`:
    - the system text contains, in order, the header, the rendered consolidation (no `{{ memory_root }}` left; `CHANGES` substituted) and the footer;
    - the user message has the six sections in order;
    - `CHANGES` lists added and removed names against `PreviousNames` and the new-note count;
    - `raw_memories.md` uses `## Thread \`session-…\`` headings in ascending order;
    - the route, `ReasoningEffort` `medium`, `Role` `proxy_memory_phase2` and `DisablePlugins` are set.
  - RED first, input budget cases (§7.3 "Input budget"):
    - notes over `Phase2NoteBytes` are not sent and not in `included.NoteIDs`;
    - with a small `Phase2InputTokens`, the oldest extractions are left out, the included ones are rendered ascending, `included.SummaryNames` lists only them, and `included.Truncated == true`;
    - no text is cut mid-extraction.
  - RED first, `ParsePhase2` cases: valid, fenced, unknown field, `memory_md` over 256 KiB, summary over 32 KiB, redaction, NUL removal.
  - RED first, `RenderInjection` cases: contains `scope: "<key>"`, the summary between the BEGINS and ENDS markers, the "tools are not available" fallback line, and no `<oai-mem-citation>` or `rollout_path` text; a long summary is truncated to `SummaryTokens`; the rendered size is under 64 KiB with a 4,000-token summary.
  - **Done when** all tests pass.
  - _Requirements: 5.1, 5.7, 5.9, 6.1, 7.1_
  - _Boundary: feature plugin (proxymemory)_
  - _Depends: 3.2_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ -run 'TestPhase2|TestRenderInjection' -count=1`_

- [x] 4. Feature runtime: capture, worker, service, HTTP

- [x] 4.1 Capture transform, observer, pending map and writer
  - Create `capture.go` per design §9: `Capture` with `Transform()` (ID `proxy-memory.capture`, order 1000, `FailOpen`), `ObserverFactory()`, a bounded pending map (cap 1,024, TTL 10 min), a queue (cap 256), and a single writer goroutine (`Start` and `Stop`) that encodes and upserts.
  - RED first, in `capture_test.go`, using a fake `Store` that records `UpsertSession` calls, a fake clock, and real `lipapi` types:
    - **Eligible request with a project**: transform, then `Open` with the same `TraceID` and `ALegID`, then `Observe` text deltas, then `Finish(OutcomeSuccessReleased)`. After `Stop`, exactly one upsert: correct owner, valid transcript JSON with the history then the assistant output, redacted.
    - An ineligible scope or no project: no pending entry and no upsert.
    - `Finish` with `OutcomeFailed`, `OutcomeCancelled` or `OutcomeClosed`: no upsert.
    - **Failover**: two `Open` calls for the same turn; the first finishes `OutcomeFailed`, the second `OutcomeSuccessReleased`. Exactly one upsert, containing only the second attempt's assistant output.
    - **Parallel**: two observers finish `OutcomeFailed` (loser) and `OutcomeSuccessReleased` (winner). One upsert.
    - A request without an established session (`HasSession` false) leaves no pending entry and counts `capture_no_session`.
    - **No panic**: a `Finish(OutcomeSuccessReleased)` after `Stop` returns nil.
    - **Queue full**: with the writer not started and 257 finishes, the 257th returns immediately, the `capture_dropped_queue` counter is 1, and one Warn log record is emitted.
    - **Truncation**: a transcript over `MaxTranscriptBytes` counts `capture_truncated` and logs one Warn record without content.
    - **Pending cap**: the 1,025th transform evicts the oldest and counts `capture_dropped_pending`.
    - **Fake store outcomes**: `UpsertOwnerMismatch` and `UpsertForgotten` increment their counters.
    - **No mutation**: the transform leaves the call deep-equal to a clone taken before.
    - **Leak-free**: `Stop` returns after the writer drains; use `go.uber.org/goleak` if the package already depends on it, otherwise assert the done channel is closed.
    - **Log hygiene**: capture all `slog` records with a test handler while a `capture_error` is forced (the fake store returns an error). No record attribute or message contains the project label, the principal ID, the A-leg ID or any transcript text.
  - **Done when** the tests pass, including with `-race` if it is available locally. Otherwise note that CI supplies the race evidence (`AGENTS.md`).
  - _Requirements: 1.9, 2.4, 2.6, 3.1, 3.4, 3.5, 3.6, 3.7, 8.6_
  - _Boundary: feature plugin (proxymemory)_
  - _Depends: 1.3, 3.1_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ -run TestCapture -count=1`_

- [x] 4.2 Background worker for phase 1, phase 2 and purge
  - Create `worker.go` per design §10 (`Worker`, `NewWorker`, `Start`, `Stop`, `Tick`, `QuiescePhase`, `SafeUnderCandidateOverlap`, the `ownerScope` helper, coalesce keys).
  - RED first, in `worker_test.go`, using a fake `Store` (an in-memory implementation of only the methods used) and a fake `auxiliary.BackgroundClient` that records the submit ctx scope and returns canned `lipapi.Collected` text:
    - **Phase 1 success**: `CompleteSession` receives the parsed and redacted extraction with `GeneratedAtMS = now`. The submit ctx has `scope.ScopeFromContext` with the owner principal and `Origin == OriginInternal`. The request route equals `ExtractRoute`.
    - **Invalid JSON**: `FailSession` is called with `now + RetryBackoff`, and `CompleteSession` is not called.
    - **Aux error or collected tool calls**: treated as a failure.
    - **Phase 2 with input**: `PublishBank` receives the parsed docs, `ConsolidatedNames` equal to the `SummaryName`s, and `ConsumedNoteIDs` equal to the IDs of the notes sent.
    - **Phase 2 with empty input**: `PublishBank` receives empty strings, no aux call is made, and `phase2_published_empty` is counted.
    - `ErrLeaseLost` from the store counts `phase*_lease_lost` and is not treated as a failure.
    - `Purge` is called once per tick with the §10 cutoffs.
    - A panic inside a fake store method is recovered and counted as `worker_panic`.
    - `Start` then `Stop` exits the loop (the done channel is closed).
    - **No tick at start**: right after `Start`, the fake store records zero calls. Advancing the fake ticker or clock by `PollInterval` triggers the first tick (inject a ticker factory for the test).
    - **One claim at a time**: with three idle sessions and `Phase1Batch` 2, `ClaimSessions` is called with `Limit: 1` and each session is processed before the next claim; exactly two are processed.
    - **Phase 2 notes**: only `included.NoteIDs` are passed as `ConsumedNoteIDs`; a truncated build counts `phase2_truncated`.
    - `QuiescePhase()` and `SafeUnderCandidateOverlap()` return true.
  - **Done when** the tests pass.
  - _Requirements: 4.1, 4.2, 4.5, 4.6, 4.7, 4.8, 5.1, 5.2, 5.4, 5.5, 5.6, 5.7, 5.8, 5.9_
  - _Boundary: feature plugin (proxymemory)_
  - _Depends: 3.3, 2.2_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ -run TestWorker -count=1`_

- [x] 4.3 (P) Memory service
  - Create `service.go` per design §11.1: `Caller`, `List`, `Status`, `Paths`, `Read`, `Search`, `AddNote`, `Forget`, and `SearchHit`.
  - RED first, in `service_test.go`, using a fake `Store`:
    - a malformed scope ID (not 64 lowercase hex characters) returns `ErrNotFound` without calling the store;
    - `Read` with an unknown path returns `ErrNotFound`;
    - `Read` with a line range returns numbered lines and truncates at `MaxReadBytes` with the marker;
    - `Search` is case-insensitive across documents in path order, stops at `MaxSearchResults`, cuts text to 400 bytes, and rejects an empty or over-256-byte query with `ErrInvalid`;
    - `AddNote` rejects empty and oversize notes, redacts secrets, and generates a 32-hex note ID;
    - `Forget` passes the caller owner and counts `control_forget`;
    - **Owner**: every store call receives `Owner{caller tenant, caller principal, scopeID}` (assert with the fake).
  - **Done when** the tests pass.
  - _Requirements: 7.2, 7.3, 7.4, 7.5, 8.1, 8.2, 8.3, 8.5_
  - _Boundary: feature plugin (proxymemory)_
  - _Depends: 1.3, 3.3_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ -run TestService -count=1`_

- [x] 4.4 REST and MCP HTTP handler
  - Create `internal/stdhttp/proxymemoryhttp/{handler.go,mcp.go}` per design §11.2 and §11.3. `NewHandler(nil)` returns nil.
  - RED first, in `handler_test.go` and `mcp_test.go`, using `httptest`, a real `Service` over a fake `Store`, and a request ctx built with `httpauth.WithScope`:
    - **Auth**: no scope gives 401; an ineligible principal gives 404 on every route.
    - `GET /v1/lip/memory` returns only the caller's banks.
    - `GET /v1/lip/memory/{scope}` returns 200 with `memory_summary`; another principal's scope returns 404 with a body identical to an unknown scope.
    - `DELETE` returns 204, then a repeated `DELETE` returns 404.
    - `PUT` returns 405.
    - **MCP `initialize`**: `protocolVersion` is negotiated (`2025-03-26` is echoed, an unknown version returns `2025-06-18`) and `capabilities.tools` is present.
    - **MCP `notifications/initialized`**: 202 with an empty body.
    - **MCP `tools/list`**: four tools, each with an input schema requiring `scope`.
    - **MCP `tools/call`**: each tool's happy path; a not-owned scope gives `isError: true` with text `not found`; an unknown tool gives JSON-RPC `-32602`; an unknown method gives `-32601`; a batch array gives `-32600`.
    - **MCP transport**: `GET` on `/mcp` returns 405; a body over 1 MiB is rejected.
  - **Done when** the tests pass.
  - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 8.1, 8.2, 8.3, 8.5_
  - _Boundary: standard HTTP (proxymemoryhttp)_
  - _Depends: 4.3_
  - _Validation: `go test ./internal/stdhttp/proxymemoryhttp/ -count=1`_

- [x] 5. Integration: core seam, composition, sentinel, docs

- [x] 5.1 Bootstrap signature change and probe-rollback pin
  - In `internal/core/runtime/conversation_bootstrap.go`, change `ConversationBootstrap` to take `accepted *lipapi.Call` as its third parameter and pass `&accepted` from `selectLocalAndBootstrap` (design §14.1).
  - Update the existing callers and tests to the new signature with no behaviour change:
    - `internal/standardplugins/featurehost/model_system_prompt.go` (the parameter is ignored);
    - `internal/core/runtime/conversation_bootstrap_test.go`;
    - `internal/core/runtime/core_ports_test.go`;
    - `internal/standardplugins/featurehost/model_system_prompt_test.go`;
    - `internal/infra/runtimebundle/model_system_prompt_reload_test.go`.
  - Add one assertion in `conversation_bootstrap_test.go`: the callback receives a non-nil `accepted` whose `Session.ALegID` equals the A-leg ID.
  - RED first, add to `internal/infra/conversationview/bootstrap_durable_test.go` (and its PostgreSQL counterpart if the file has one):
    - `decide` returns an error, so `BootstrapSteering` returns `ErrBootstrapDecision`;
    - afterwards there is no `a_leg_steering_bootstrap` row for `(aLeg, producer)` and no overlay;
    - a second call with a valid `matched` decision commits one overlay.
  - **Done when** the runtime, featurehost and conversationview tests, and the reload test, pass unchanged apart from the signature and the new assertions.
  - _Requirements: 6.1, 6.2, 6.5_
  - _Boundary: core runtime (signature only), conversation-view tests_
  - _Validation: `go test ./internal/core/runtime/ ./internal/infra/conversationview/ ./internal/standardplugins/featurehost/ ./internal/infra/runtimebundle/ -count=1`_

- [x] 5.2 Process wiring and feature registration
  - Implement design §14.2 and §14.5:
    - **Process**: in `internal/standardplugins/featurehost/process.go`, call `proxymemorystore.EnsureSchema` and `New` inside the existing `if bunDB != nil` block, after `conversationview.EnsureSchema`. Construct and register `proxymemory.NewPrometheusCollector()` next to the keepwarm collector.
    - **Fields**: `proxyMemoryStore` and `proxyMemoryMetrics` in `runtime.go`.
    - **Registration**: `{ID: proxymemory.ID, Factory: featureProxyMemory}` in `internal/standardplugins/standard_table.go`; `featureProxyMemory` (decode, validate, empty bundle) in `features_install.go`.
    - **Config**: the disabled example row from §1.3 in `config.yaml`.
  - RED first:
    - In `internal/standardplugins/featurehost/proxy_memory_test.go`:
      - a process built with a SQLite Bun DB has a non-nil `proxyMemoryStore`, and its tables exist (query `proxy_memory_banks`);
      - a process built without a DB has a nil store;
      - `proxymemory.LocalFallbackPrincipalID == auth.LocalUnknownOSPrincipalID`.
    - In the `internal/standardplugins` registration tests (precedent `registration_enabled_test.go`): a `proxy-memory` row with a missing route fails validation, and a valid row validates.
  - **Done when** the tests pass and loading the shipped `config.yaml` still succeeds (the existing config tests are green).
  - _Requirements: 1.1, 1.7, 1.8_
  - _Boundary: composition (featurehost process, standardplugins registration)_
  - _Depends: 2.3_
  - _Validation: `go test ./internal/standardplugins/... -run 'ProxyMemory|Registration|Config' -count=1`_

- [x] 5.3 (P) HTTP projection plumbing and mount
  - Implement design §14.4:
    - `HTTPOperationsInput.ProxyMemory http.Handler` in `internal/stdhttp/contract/http_input.go`;
    - `HostProjections.ProxyMemoryHTTP http.Handler` in `internal/standardplugins/featurehost/inputs.go`;
    - copy it in `internal/infra/runtimebundle/compile_generation.go`, next to `KeepwarmAdmin`;
    - `mountProxyMemory` in the new `internal/stdhttp/proxy_memory_mount.go`;
    - call it from `internal/stdhttp/handler.go` right after `installTerminalDecisionPolicy`.
  - RED first, in `internal/stdhttp/proxy_memory_mount_test.go` (precedent `terminal_decision_policy_mount_test.go`): with a non-nil handler, `GET /v1/lip/memory` and `GET /v1/lip/memory/x` reach it (a recording handler); with nil, `/v1/lip/memory` returns the mux 404.
  - **Done when** the stdhttp and runtimebundle tests pass and `go test ./internal/archtest/ -count=1` passes (contract cycle rules).
  - _Requirements: 7.1, 8.1_
  - _Boundary: composition (stdhttp contract, stdhttp mount, runtimebundle projection copy)_
  - _Depends: 4.4_
  - _Validation: `go test ./internal/stdhttp/ ./internal/infra/runtimebundle/ ./internal/archtest/ -count=1`_

- [x] 5.4 Generation composition and memory bootstrap producer
  - Implement design §14.3 in the new `internal/standardplugins/featurehost/proxy_memory.go`:
    - `compileProxyMemory`, including its prerequisite errors;
    - the request-transform recall plane (`proxyMemoryCapture`): one bounded admission read, one frozen first-turn decision applied to the request, and the decision queued for the background writer. The producer commits nothing in the request path; the durable first-turn binding is written by the capture writer, and the store refuses a capture whose binding is absent (`capture_unbound`);
    - `composeBootstraps`;
    - the plane contributions;
    - two lifecycles: `captureLifecycle`, and the `*Worker` with `QuiescePhase`;
    - the HTTP handler via `proxymemoryhttp.NewHandler(svc)`.

    Wire it in `generation.go`: compose with `modelSystemPrompt`, apply the planes after `bindSessionClassifier`, append the lifecycles, and set `HostProjections.ProxyMemoryHTTP`.
  - Add a package-local test seam: `var proxyMemoryWorkerHook func(*proxymemory.Worker)`, called right after the worker is built when non-nil. Tests use it to capture the worker and call `Tick` directly.
  - RED first, in `internal/standardplugins/featurehost/proxy_memory_test.go`, using a real `conversationview.BunStore` and a real `proxymemorystore.Store` on one in-memory SQLite DB (precedent `model_system_prompt_test.go`), and `GenerationInput{BackgroundClient: fake, NowFn: fake}`:
    - enabling without a Bun DB, or with `BackgroundClient == nil`, fails `CompileGeneration` with the prerequisite error;
    - **First turn with a published summary** (seeded through the store): exactly one overlay `proxy-memory.v1`, role system, stable prefix; its text contains the scope key and the summary; the counter is `inject_matched`;
    - **Second turn**: the decision is reused; a counting wrapper around the memory store records zero `PublishedSummary` calls;
    - **Ineligible principal, no session, or no project**: no bootstrap row for producer `proxy-memory`, and the matching counter is incremented;
    - **`PublishedSummary` error**: the turn proceeds, no row is recorded, and `inject_error` is counted;
    - **Composite**: with `model-system-prompt` also enabled, both overlays exist, the model-system-prompt overlay comes first, and a model-system-prompt error is still returned;
    - **Planes**: the generation's request-transform and stream-observer planes contain contributions from `proxy-memory`;
    - **Lifecycles**: two are appended, and one reports `QuiescePhase() == true`;
    - **HTTP projection**: `HostProjections.ProxyMemoryHTTP` is non-nil when enabled and nil when disabled;
    - **Log hygiene**: with a capturing `slog` handler during the error cases, no record contains the project label, the principal ID, the A-leg ID or the summary text.
  - **Done when**:
    - the featurehost tests pass;
    - `go test ./internal/archtest/ ./internal/qa/ -count=1` passes (import rules, change-size gate);
    - `go run ./tools/kiro/kirocheck` passes.
  - Corrective (independent review): the memory store owns an **isolated** database (`internal/infra/featuredb` + `internal/infra/db`), so an advisory memory transaction can never hold the mandatory core connection; the core A-leg facts are read through the narrow `proxymemorystore.CoreALegs` view outside every memory transaction. The process-owned decision cache and the published generation's retirement cleanup register under separate keys of the core's keyed retirement-observer seam, so neither registration displaces the other.
  - _Requirements: 1.6, 1.9, 2.4, 2.6, 6.1, 6.2, 6.3, 6.4, 6.5_
  - _Boundary: composition (featurehost generation)_
  - _Depends: 4.1, 4.2, 4.4, 5.1, 5.2, 5.3_
  - _Validation: `go test ./internal/standardplugins/featurehost/ ./internal/archtest/ ./internal/qa/ -count=1`_

- [x] 5.5 End-to-end isolation sentinel and documentation
  - Add `internal/standardplugins/featurehost/proxy_memory_sentinel_test.go`.
    - Build the generation through `CompileGeneration` with:
      - a real SQLite Bun DB for the continuity and conversation-view stores, and a separate real SQLite Bun DB for the memory store (the memory database is isolated from the continuity database);
      - `GenerationInput{BackgroundClient: fakeBG, NowFn: fakeClock}`, where `fakeBG` returns canned phase 1 JSON and then canned phase 2 JSON by inspecting `req.Role`;
      - `proxyMemoryWorkerHook` capturing the worker.
    - Drive turns through the generation's executor with a stub backend. Precedent: the composed fixtures in `sessionclassification_cross_harness_acceptance_test.go`.
    - Steps:
      1. Principal A sends a Codex-shaped turn, with a client session hint and a user text part starting `<environment_context><cwd>/w/app</cwd>…`, and the turn is released successfully. Stop the capture lifecycle to flush.
      2. Advance `fakeClock` by 7 h and call `worker.Tick(ctx)` twice: the first runs phase 1, the second runs phase 2.
      3. A new A-leg for principal A sends a Claude-Code-shaped turn (`Working directory: /w/app` in instructions, a different session hint). Assert the backend-bound request (recorded by the stub backend) contains the memory overlay with the canned summary.
      4. A new A-leg for principal B with the same path receives **no** overlay, and the projected HTTP handler serves `GET /v1/lip/memory` for B with an empty list.
      5. A's `DELETE /v1/lip/memory/{scope}` succeeds, and a further new A session receives no overlay.
  - Write `docs/proxy-memory.md` per design §16 and link it from `docs/conversation-view.md` next to the model-system-prompt reference.
  - Corrective (independent review): the guide states the **total** read budget (`recall_timeout`), the best-effort asynchronous decision persistence, the queue-overflow, shutdown and process-loss limits, the exact frozen replay only when the decision is available, and the fail-open omission (no current-bank reconstruction) when it is not. The counter table describes the applied, not the committed, decision, and lists the decision-unavailable, decision-drop and unbound-capture events.
  - **Done when**:
    - the sentinel passes;
    - the doc exists and contains the config example, MCP client setup for Codex, Claude Code and OpenCode, the forget limitation, the no-session requirement, and the plaintext-storage warning;
    - the focused verification commands from `docs/agent-workflow.md#verification` pass.
  - _Requirements: 1.5, 2.1, 2.2, 6.1, 8.3, 8.4_
  - _Boundary: tests (composed generation), docs_
  - _Depends: 5.4_
  - _Validation: `go test ./internal/standardplugins/featurehost/ -run TestProxyMemorySentinel -count=1`_

## Completion Status

- [x] All tasks checked. Delivered in #1233, #1273 and #1275 (task notes carry the per-task evidence).
- [x] Verified on main `5b5c873c` (2026-10-29): full root suite `go test -tags=precommit,integration ./...` and `scripts/check-all-modules.sh` pass.

## Implementation Notes

- Corrective round (independent judge run `20261019-233524`, verdict REJECTED): the remediation is implemented and re-verified locally, but the acceptance is **not** claimed here — an independent re-review is still required. Findings and remediation: (J1) the memory store shared the mandatory core connection, so a real memory transaction blocked session preparation; the memory store now owns an isolated database (separate SQLite file, dedicated PostgreSQL pool) and reads the core A-leg facts through the narrow `CoreALegs` view outside every memory transaction. (J2) a failed first-turn decision could leave a durable capture without its binding; the capture transaction now validates the durable binding and epoch and drops an unbound capture (`capture_unbound`), and the decision cache state is monotone, so a consumer failure is never overwritten by the producer. (J3) the published generation replaced the process-owned retirement callback, so the replay-cache eviction was dead; both cleanups now register under separate keys of the core's keyed retirement-observer seam. (J4) the operator guide claimed committed overlays and omitted the asynchronous decision-loss fallback; it now states the applied-not-committed semantics and the fail-open omission. (F4) the durable activity watermark is a separate ledger column, so a stale release or a regressed clock cannot make a session idle-eligible before its newest release, while the extracted source metadata stays exact.

- 1.1: review panel APPROVED (judge run `20261016-234608`, no Critical/Important findings). Optional hardening deferred: (F1) `configRoot` may reject odd-length hand-built mapping nodes; (F2) the fallback-literal oracle — task 5.2 already cross-checks `LocalFallbackPrincipalID == auth.LocalUnknownOSPrincipalID`, consider embedding the literal in an `owner_test.go` fallback case; (F3) spec wording: design file plan (~line 139) says `Eligibility`, §2 and tasks define `Ineligible` — correct when next touching the spec. `DecodeConfig` applies defaults then validates (sessionclassification precedent), so empty/null/partial subtrees fail at decode time (R1.8).
- 1.2: panel APPROVED (judge run `20261017-000317`; no blocking findings). Interpretations ratified by review: (a) extension-source candidates retry within the same source before falling through (`sessionwire.hasEffectiveWorkspaceHint` semantics, test-distinguished at `project_test.go:199-205`); (b) size/UTF-8/control rejection applies to the candidate as presented to `normalizeLabel` (extensions raw, instruction/env values pre-trimmed); (c) `removedTokens` = `ApproxTokens` of the elided middle; non-overlap needs `len >= 4*limit+1`. Deferred optional coverage (S1): invalid-UTF-8 fallback case and an exactly-4,096-byte acceptance case in `project_test.go`. Extension-key priority mirrors `sessionwire/workspace.go` as a local constant — a parity drift test is optional; no shared abstraction warranted (feature plugin cannot import frontends zone).
- 1.3: round 1 REJECTED (judge `20261017-002314`) on F1 — the `CompleteSession` contract comment mis-documented claimed-revision mismatch as `ErrLeaseLost`; design §12.4 step 5 treats a newer upsert as success (extraction stored, bank dirtied, retained newer session's lease cleared), with ErrLeaseLost only for lease-owner/fenced/foreign-owner failures. Remediated comment-only (round 2 judge `20261017-003445` APPROVED; delta proven by reconstruction). Ruling on the SummaryName exemplar: design §7.3 governs (`SessionRef[8:14]`, no fractional seconds) — the exemplar above was corrected to `-012345-`. Claim-record comments no longer overstate per-method recheck predicates (F3). Deferred optional: widen `store.go` ErrLeaseLost prose to mention the missing-session/owner mismatch case explicitly (§12.4 already governs). §8 types/§7.3 `SummaryName` in `store.go` are the single definitions for tasks 2.3/3.3/4.3.
- 2.1: round 1 REJECTED (judge `20261017-005822`) on J1 — the PostgreSQL contract reused fixture keys across subtests while the canonical `postgres-direct` lane runs on one shared dev DB, producing four SQLSTATE-23505 failures (would break the required `db-parity` CI job). Remediation (round 2 judge `20261017-012513` APPROVED): per-subtest-unique fixture keys (scope+runID) AND exact-key `t.Cleanup`; canonical PG lane green repeatedly incl. cleanup-disabled and census-wrapped runs; J2 comment now admits the §12.2 concurrent-Forget recreation race (enforced later by CompleteSession/Purge); J4 pins the recorded baseline migration name. Boundary ruling DELIVERED IN 2.1: dbparity catalog registration (9→10), stable wrappers and doc counts — forced by fail-closed archtest gates once the versioned migration file exists. Accounting: 2.2 owns ClaimSessions/CompleteSession/FailSession/ClaimBanks/ConsolidationInput/PublishBank/FailBank; 2.3 owns ListBanks/BankStatus/Documents/AddNote/Forget/Purge + expanded contracts on BOTH engines (keep exactly ONE `TestDBParity_PostgresDirect`; expand the existing integration entry). Deferred (optional): S1 exact-key cleanup of 42 pre-existing old-format fixture rows left by earlier runs in the shared dev DB (owner-authorized only; synthetic keys, not residue from this branch).
- 2.2: panel APPROVED round 1 (judge `20261017-020936`; no blocking findings; r1 reproduced RED via overlay, ran both PG lanes + content-digest census). Panel-verified semantics: claim APIs are GLOBAL worker operations (no owner filter; unique fixture keys prevent duplicate inserts, not cross-claiming) — shared-DB sequential reruns are the demonstrated lane; simultaneous suites can cross-claim bank fixtures (J1, test-only isolation: optional advisory-lock or per-suite schema). Extraction identity comes from the lease-verified claim, not `e.Owner`. Fence-path session delete must COMMIT before returning ErrLeaseLost. Deferred optional (judge J2-J4): contract cases for empty publication (docs `[]`, notes kept, bank stays dirty), negative-limit clamp, conflicting extraction-identity pin; one rollback-after-earlier-write regression and a dirty-after-claim assertion; update the `runContract` header comment to mention §12.3-§12.5. Six methods remain stubs for 2.3 (`Purge`, `ListBanks`, `BankStatus`, `Documents`, `AddNote`, `Forget`).
- 2.3: the hardest review cycle — three rounds (rounds 2 and 3 carried judge rejections with deterministic probes):
  (R1→R2, judge `20261017-024151`) J1 — concurrent AddNote/Forget on PG READ COMMITTED retained a note after successful Forget (R8.3/8.5). Fix: shared owner-triple bank-row lock (first bank-table statement in BOTH txs; PG `FOR UPDATE`, SQLite plain SELECT with engine-serialized writers documented) + AddNote's UPDATE RowsAffected check (zero → ErrNotFound).
  (R2→R3, judge `20261017-030722`) the lock position between fence INSERTs and deletes created two PG deadlocks (Forget↔CompleteSession ABBA; Forget↔Purge inversion). Fix: bank lock moved to AFTER sessions+extractions deletes, BEFORE notes/bank delete — still the tx's first bank-table statement; erasure ordering preserved (everything deleted after the lock wins over a concurrent AddNote).
  Both defect classes are pinned as deterministic PG-gated contract regressions: two interleaving cases (query-hook rendezvous, no sleeps) and two deadlock cases (bounded-context fail-fast, deadlock_detected=failure), each RED-reproducible via `-overlay` (retained-note case + 40P01).
  (R3 APPROVED, judge `20261017-040200` — reproduced both deadlocks pre-fix; 10-run interleaving probe on final source.)
  Store surface now fully implemented (15/15). Deferred optional FYIs: census residue +1 session/+1 bank predates round-3 (one synthetic pair, reviewer-era provenance not fully established — inert, owner-authorized cleanup); apply the same lock-order vigilance when 4.2's worker calls these methods concurrently; extended failure-atomicity cases from 2.2-J4 remain open.
- 3.1: FOUR review rounds (r1 ×3 APPROVED; r2 reviewer quota-crashed each time; judges REJECTED rounds 1-3, last judge run `20261017-141636` APPROVED). Established rulings: Content-authoritative-with-Text-fallback, fragment filter in BOTH authorities, `[video omitted]`, full-EventItem completion in place, Type/Role never truncated, nil-only marshal error, §5-verbatim pass + measured final encoded-byte fit (controller-recorded §5 clarification in code comments), late-started naming with canonical `lipapi.Collect` parity, padded JSON-string decoding, NUL-strip BEFORE Redact. THE CLASS INSIGHT (debug cycle after J4=break boundary, J5=truncate-then-reference, and a debugger-found reduce-path instance, all R3.3 vault violations): §4 regex matching is not compositional over edits — byte removal can CREATE boundaries, marker insertion can SUPPLY them, NUL removal re-splices halves. R3.3 is therefore enforced as: sanitize (NUL-strip → Redact) is the LAST byte-changing op before every returning marshal; `sanitizeEntries` runs as the first statement of every `fitEncodedTranscript` pass and the bound is measured POST-redact; marshal escaping is value-preserving (U+FFFD non-boundary) so it cannot newly-create matchability; Redact is idempotent on its own output (fuzz-verified). Pinned by the fixpoint battery (shapes × budgets, `Redact(field)==field` on every decoded field) plus per-instance regressions (truncate-boundary, reduce-path, canonical-Call Arguments geometry). Readers of later tasks (3.2/3.3: injection text; 4.x: capture) MUST keep Redact as the last byte-changing op on any stored string.
- 3.2: judges REJECTED round 1 (`20261017-145558`, J1 Critical) then APPROVED round 2 (`20261017-151137`); NOTE: both phase-reviewer agents quota-crashed (429) in this round, so the judge carried the review with a full independent pass (fresh upstream byte-comparison, hygiene scan, mutation tests). J1: slug normalization (lowercase + non-[a-z0-9]→'-') is LOSSY — it destroys secret shapes (assignment `=`, bearer space, AWS case), so "Redact last" alone misses them. GENERALIZED INVARIANT (companion to 3.1's class insight): a lossy transform ⇒ Redact BEFORE the transform ON TOP OF a final Redact pass (both passes needed; fixpoint-only assertion is insufficient — broken outputs are already fixpoints). `cleanRolloutSlug` = NUL-strip→Redact→normalize→final Redact. Judge-verified marker handling: `[REDACTED_SECRET]` normalizing to `redacted-secret` inside a slug is §7.2-sanctioned (the marker text stays intact in memory/summary fields). Mutation tests pin both passes. For 4.x: any new parser/transform on auxiliary-model output must apply this before+after pattern on every stored field. Uses the `password-redacted-secret` / `bearer-redacted-secret` / `redacted-secret` slug regressions.
- 3.3: judges REJECTED rounds 1 (`20261017-154318` J1/J2) and 2 (`20261017-160524` J3), APPROVED round 3 (`20261017-165009`; both phase-reviewer agents quota-crashed in every round — judge carried with full independent passes). Ratified rulings for 4.2: `Phase2Input` carries `ProjectLabel` (populate from `ClaimedBank.ProjectLabel`); CHANGES compares the WHOLE consolidation window vs `PreviousNames` (budget omission is NOT evidence deletion); notes = oldest-first prefix by bytes AND total token budget; extractions = newest-first whole-pair suffix rendered ascending; required-sections-floor over budget ⇒ explicit ERROR. NEW INSTANCES of the 3.1 R3.3 class (three total this task): injection `cleanInjectionSummary` and memory-cut `fitPhase2MemoryMD` BOTH now `cleanTranscriptString(TruncateTokens(cleanTranscriptString(X, limit)))` (redact before+after lossy cut; sanitized candidate measured); plus the escalated PRE-EXISTING 3.2 `BuildPhase1Request` `rollout_contents` cut (same wrap; implementer sweep: ALL 3 `TruncateTokens` sites in prompts.go now wrapped — transcript, memoryMD, summaryCutClean — verified fixpoint property: `Redact(rendered)==rendered`). Judge mutation-verified both passes load-bearing (pre-pass removal ⇒ fragment leak; post-pass removal ⇒ boundary leak). For 4.x: ANY new cut/transform applied to a stored string must reuse this exact wrap; final rendered user/system messages must satisfy `ApproxTokens ≤ config budget` with marker bytes counted, else the builder returns an error (`BuildPhase2Request` floor check style).
- 4.1: THREE review rounds (rounds 1-2 judges REJECTED, round 3 judge `20261017-181251` APPROVED; phase-reviewer agents quota-crashed each round — judge carried). Writer lifecycle precision now: the stop budget governs drain via an ABSOLUTE stop deadline (`ctx.Deadline()` captured by Stop, checked in the drain admission AND re-derived per write via `captureWriteDeadline` after encoding — `context.WithDeadline` carries it intrinsically through the encode; pre-store refusal when the write ctx is already dead; no store call, nothing counted). LIFECYCLE ctx (`run`/`drain` selects observe `<-ctx.Done()`, drain too) cancels an idle or mid-I/O writer promptly and closes `writerDone`; budgeted Stop drain unchanged; never-closed queue preserved; DeadlineExceeded compatibility preserved. Race-gone probes (judge's own byte-identical `extra_test.go`, -count=100 green: RunningStopDrainExpiry 43/100 → green; DrainThreeSecondClip 31/100 → green) + commit-side regressions `TestCaptureStopDrainAdmitsNoJobAfterExhaustedBudget` and `TestCaptureStartCancellationStopsWriter` (idle + mid-write writerDone) pin each mechanism, with the decomposed mutant matrix (`mutation-index-r3.md`, honest deviation noted as the judge's own pre-store gate made the single-mutant expectation non-discriminating). For 4.2: the worker's storage budget/loop uses different mechanics — do NOT assume capture's counters here; this element only cares that its lifecycle ctx terminates (`QuiescePhase() == true`, worker per §10).
- 4.2: TWO review rounds (round-1 judge `20261017-184451` REJECTED, round-2 judge `20261017-190629` APPROVED; both phase-reviewer agents quota-crashed in both rounds — judge carried). Ratified: failure COMPENSATION (non-ErrLeaseLost `CompleteSession`/`PublishBank` errors route through the transactional `failSession`/`failBank` schedulers → `next_attempt_ms`; exactly-once `phase*_failed` even when the scheduler itself errors — bounded log, no recursion, lease-expiry fallback; ErrLeaseLost never compensated); `phase2_truncated` counted+logged once immediately after a successful `BuildPhase2Request` (BEFORE SubmitCollect/Await/ParsePhase2 — visible on the failure path; single counting site, no double count); worker LIFECYCLE = one mutex-guarded `workerLifecycle` state (idle→running) where a later Stop observes the newly published cancel — Stop-before-Start exits promptly and concurrent Start/Stop cannot deadlock (judge verified 50 concurrent schedules under `testing/synctest`); failure-isolation pinned (per-session/per-bank scheduling isolation; truncated input never consumes excluded notes on any path). Tick order standing: one Purge per tick (cutoffs+limit 500) → sequential `ClaimSessions Limit:1` → sequential bank claims; no tick at start; Stop-during-Await → ctx.Err() then `aux.Forget`. Second allowlisted goroutine (worker tick loop, both scripts). For 4.3/5.x: the memory service composes worker+store+aux client; do not import worker internals — wire the interfaces as 5.4 prescribes.
- 4.3: TWO review rounds (round-1 judge `20261017-193118` REJECTED on J1 only, round-2 judge `20261017-195146` APPROVED; both phases quota-crashed, judge carried). J1: validated `MaxReadBytes` ∈ {1,14,15} → Read returned 16 bytes (newline+marker unconditional). POLICY: config minimum `minReadBytesLimit` = separator + marker constants (=16) DERIVED from cutReadOutput's same constants — placed split: `readTruncationSeparator`/floor in config.go, `readTruncatedLine` in service.go (judge-verified placement keeps the mutation overlays compiling AND failing on assertions, never on builds); `Config.Validate` rejects MaxReadBytes in 1..15 with the operator-facing floor message; `MaxReadBytes = 0` keeps its pre-task positive-integer semantics (no zero-unbounded invention); cutReadOutput belt (unvalidated misuse only) cuts within cap WITHOUT a marker below the floor. Cut-security standing (judge 14,400-case probes + mutation-proven both passes): `clean → rune-safe cut → clean` only-when-cut, marker expansion reserved, no re-redaction when no cut. Status = BankStatus then owner-scoped PublishedSummary; store ErrNotFound on the summary after bank found = EMPTY summary (R8.2/§12.6 composition, judge-ruled); other store errors propagate. Forget counts `control_forget` exactly-once only after success. Historical RED count FYI: 12 failing funcs (TestServiceCaller had no failure in the log). For 4.4: HTTP maps ErrInvalid→400-4xx, ErrNotFound→404 for both unknown scope and unknown path (identical body), and must NOT re-sanitize — service outputs are fixpoints already.
- 4.4: THREE review rounds (rounds 1-2 judges REJECTED, round-3 judge `20261017-210138` APPROVED; both phases quota-crashed every round, judge carried). MCP envelope authority: decode the POST body into `map[string]json.RawMessage` and validate ONLY exact-case members — `jsonrpc` literal `"2.0"`, nonblank exact-case `method`, optional exact-case `id` (JSON string|number, `json.Number` big-number fidelity on echo). encoding/json struct tags match case-insensitively (J1 round 1: unvalidated envelope dispatched memory writes; J2 round 2: `JSONRPC`/`Method`/`Id` aliases satisfied/overrode required members — `jsonrpc:"1.0"` + alias `JSONRPC:"2.0"` merged), so aliases are UNKNOWN EXTENSION MEMBERS supplying nothing. Validation order: envelope → method presence (-32600, not -32601) → notification branch (absent id → 202 empty for initialized; other methods silently ignored per §11.3) → dispatch (-32601/-32602/result). -32600 id policy: echo a LEGAL string/number request id; `null` for absent/null/object/array/bool ids. ALIAS-ID RULING (ratified round 3): alias-id envelopes (legal `Id:1` or illegal `Id:{}`) with otherwise-valid exact members are NOTIFICATION-SHAPED for every method — 202 empty, zero side effects, zero tool_call, never -32600. Invalid envelopes NEVER call the Service and never count `tool_call`. Structural FYIs for 5.x: `NewHandler(svc *proxymemory.Service, metrics ...*proxymemory.Metrics)` variadic (4.3's Service exposes no counter; **5.4 MUST pass `proxyMemoryMetrics` or the HTTP-owned counters stay nil**), and the internal/stdhttp shrinkage overlay measures 600/604 with **5.3's `proxy_memory_mount.go` required to join the marker set and be honestly remeasured (4 slack lines will NOT cover it — cap/floor honesty obligation stands)**.
- 5.1: ONE review round (judge `20261017-212842` APPROVED; phases quota-crashed, judge carried). Signature `ConversationBootstrap = func(ctx, aLegID, accepted *lipapi.Call, decide func() (InitialModelIntent, error)) error` with `&accepted` from `selectLocalAndBootstrap` (producers treat accepted as read-only; `modelSystemPrompt` ignores it). Probe-rollback property was PRE-EXISTING (blame `b3cd514d54`, `ErrBootstrap decision` inside RunInTx after failed decide) — pinned now: `probe_rollback` subtest asserts ErrBootstrapDecision + NO `a_leg_steering_bootstrap` marker row + NO overlay row/visible snapshot after a failed decide, and ONE overlay committed by a subsequent valid matched call, on BOTH engines (`LIP_REQUIRE_POSTGRES=1` shared runner, no skips). RED honesty: property pre-existing → mutation-driven purpose=red receipts (nil-accepted, swallow-decide, trace-after-fail mutants, external overlay only, no production code bent). For 5.4: `proxyMemoryBootstrap` receives the committed `&accepted` — do NOT re-derive the call; read §14.3's parameter flow.
- 5.2: ONE review round (judge `20261017-215801` APPROVED; phases quota-crashed, judge carried). Wiring standing: `proxymemorystore.EnsureSchema`→`New` AFTER `conversationview.EnsureSchema` inside the `if bunDB != nil` block; fields `proxyMemoryStore`/`proxyMemoryMetrics`; collector = `proxymemory.NewPrometheusCollector()` (`*Metrics`, implements prometheus.Collector) constructed ALWAYS and registered conditionally (§14.2 "usable with no registry" — 5.4 needs the value regardless; nil-registry safe; judge-verified startup-unwind: schema-init failure and collector-registration failure unwind owned resources with the borrowed DB intact). §14.5 factory = `DecodeConfig` + explicit `cfg.Validate()` + EMPTY `FeatureBundle{SchemaVersion: V1}`, no planes. §1.3's disabled row is LIVE with `enabled: false` in BOTH config files (disabled rows skip decode → zero schema change; distribution compile + check-config both green). Inventory test pinned with `"proxy-memory"` added. REGISTRATION: generation behavior is NOT an acceptance criterion here, judge ruled). For 5.4: use the `proxyMemoryStore`/`proxyMemoryMetrics` fields; pass `proxyMemoryMetrics` to `proxymemoryhttp.NewHandler` (4.4 FYI; without it the HTTP-owned counters stay nil).
- 5.3: ONE review round (judge `20261017-222231` APPROVED; phases quota-crashed, judge carried). Plumbing standing: `HTTPOperationsInput.ProxyMemory http.Handler` + `HostProjections.ProxyMemoryHTTP`; runtimebundle copy adjacent to `KeepwarmAdmin` (preserves handler identity AND nil — judge probe); `mountProxyMemory` in new `internal/stdhttp/proxy_memory_mount.go` called right after `installTerminalDecisionPolicy`; nil projection = NOT mounted (mux 404); handler construction deferred to 5.4. ARCHTEST OBLIGATION DISCHARGED (measured independently): overlay 226+374+14=**614, cap 639** (measured + usual 25 headroom — replaces 4.4's prediction 604, stale-579 comment corrected); convergence floor re-pinned **−791** (plumbing +5 non-overlay surface lines from the −796 base with 1-line headroom; documented secretguard precedent, 3 pins updated in shrinkage_test.go). Mount-contract inventory gate forced `mountProxyMemory` into `mount_contract_inventory_test.go` + the ARCHIVED `mount-dependency-inventory.md` (precedent `76015f28a`; the ACTIVE spec untouched). For 5.4: fill `HostProjections.ProxyMemoryHTTP` in generation composition; the mount contract test will fail if the projection is not copied.
- 5.4: TWO review rounds (round-1 judge `20261017-231008` REJECTED, round-2 judge `20261017-232952` APPROVED; phases quota-crashed, judge carried). Standing composition contract: probe order eligibility→session→project→BootstrapSteering(errProbe)→PublishedSummary→commit (probe error rolls back; ErrNotFound commits no-match; other summary errors = inject_error+store_unavailable, decision unconsumed); counters exact §13 nouns (matched/no_summary ONLY on fresh committed outcomes); overlay = RenderInjection VERBATIM (judge byte-equality probe; frozen after summary replacement; accepted read-only); compose = modelSystemPrompt FIRST with unchanged error propagation, memory guard errors counted+swallowed; observer via SafeObserver; planes contributed after bindSessionClassifier into frozen planes/bundle (order 1000/FailOpen from Capture); lifecycles capture-then-Worker appended last (capture no QuiescePhase → generation-Close stop; Worker QuiescePhase → retirement stop; no duplicate Start; borrowed store/DB never closed by adapters); HTTP `NewHandler(svc, r.proxyMemoryMetrics)` (nil when disabled); log hygiene at all sites. **CRITICAL GUARD (J1, round 1): the composition-store seam wraps concrete `*proxymemorystore.Store` into a non-nil interface — the durable-store prerequisite MUST check the PROCESS-OWNED concrete field: `r.proxyMemoryStore == nil ||` fires BEFORE the interface check; memory-only continuity (real b2bua.NewMemoryStore ContinuityStore, no Bun DB) is rejected at CompileGeneration with no bootstrap/planes/lifecycles/HTTP published.** Boundary: memory-continuity+BunDB is rejected earlier by the pre-existing bootstrapAuthority binding. SECOND TEST SEAM ratified: `proxyMemoryCompositionStore(r)` (production returns `r.proxyMemoryStore`) — needed for the mandated counting-wrapper case over 5.2's concrete field. RED honesty: stub-declared 14-failure RED (build-failure RED rejected by the validator; production restored byte-identically). FYI: the 5.2 SQLite helper is FILE-BACKED (temp file), not literally in-memory. For 5.5: the sentinel builds through the same CompileGeneration — reuse the established fixtures; seed summaries through the STORE (not direct SQL) and drive ticks via the worker hook.
- 5.5: TWO review rounds (round-1 judge `20261018-001157` REJECTED on J1 doc-accuracy, round-2 judge `20261018-002601` APPROVED; phases quota-crashed, judge carried). Sentinel standing: ComposeGeneration with real shared SQLite Bun DB (continuity+conversationview+memory stores), `{env:...}`-substituted configs; Codex→Claude cross-shape turns for the SAME owner land the SAME project/scope; isolation proved by OWNER SCOPING (B commits its own no-match decision), not the allowlist; one injected clock drives capture timestamps AND worker ticks (no sleeps); flush = the capture lifecycle's Stop joins the writer BEFORE the durable session read + ticks (ordering is load-bearing — caught a real ordering bug during build); tick cadence REALITY: §10's purge→extract→consolidate means a dirty bank is claimable IN THE SAME TICK — tick 1 does phase 1+2, tick 2 is a no-op; the task text's "first/second tick" wording was design-vs-text inconsistent, RULING: keep the actual two-tick drive with the doc comment stating the real cadence (judge-accepted as design-faithful). DOC (docs/proxy-memory.md): requires accurate LOCAL-AUTH prose — a no-API-key listener resolves the OS principal (username/UID → LIP_OS_PRINCIPAL_ID/USER/USERNAME hints → `lip_local_unknown` fallback ONLY when nothing resolves); local principals excluded by DEFAULT; eligibility ONLY with `allow_local_principal: true` AND allowlist match (`["*"]` matches); `lip_local_unknown` rejected unconditionally; separate-per-user-API-keys recommendation retained (round-1 J1 fixed after a false categorical-exclusion claim; judge verified via an external empty-credential probe). OpenCode MCP snippet NATIVE V2: `mcp.servers`, automatic/optional `disabled: true`, `oauth: false`, key via `{env:PROXY_MEMORY_API_KEY}` (NOT `${...}` — shipped binary's ConfigVariable.substitute expands only `{env:...}`; judge re-verified against docs + binary). All doc mandate pieces present (config example, three MCP client setups, forget limitation, no-session requirement, plaintext-storage warning). ALL 18 TASKS GREEN — proceed to /kiro-validate-impl as the GO/NO-GO gate.
