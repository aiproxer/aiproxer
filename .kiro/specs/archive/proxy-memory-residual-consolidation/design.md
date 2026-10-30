# Design: proxy-memory-residual-consolidation

## 1. Problem

`ConsolidationInput` returns the newest `limit` extractions of the window
(`ORDER BY generated_at_ms DESC ... LIMIT ?`). `PublishBank` then writes
`consolidated_seq = b.DirtySeq`. With `MaxRawMemories` smaller than the new
backlog, the excluded extraction is stored and still eligible, but every later
attempt with the same cap selects the same newest rows again. The obligation is
silently dropped.

The phase 2 progress guard cannot see this loss: it only inspects the rows the
store returned.

## 2. Obligation model

One extraction row is one obligation until it is acknowledged.

- `proxy_memory_extractions.consolidated_at_ms BIGINT NOT NULL DEFAULT 0` marks
  the revision that a publication carried. `0` means outstanding.
- The primary key remains `a_leg_id`, so re-extraction of an A-leg replaces the
  row and resets `consolidated_at_ms = 0` (a new revision needs a new
  acknowledgment).
- `extraction_revision BIGINT NOT NULL DEFAULT 1` increments on every successful
  replacement. It distinguishes revisions even when generation timestamps match.
- The bank stays dirty while an in-window extraction is outstanding:
  `consolidated_seq` is advanced only when the acknowledgment of a publication
  leaves none.

`ConsolidatedNames` stays the published identity list used by the CHANGES
comparison. It is not the obligation record; the column above is.

The feature is unshipped, so the baseline `CREATE TABLE IF NOT EXISTS` in
`migrations.go` is edited in place. A database created by an earlier build of
this stack must be dropped; no backfill exists (same policy as #1334).

## 3. Store interface

```go
// ExtractionRef identifies one carried extraction revision.
type ExtractionRef struct {
    ALegID        string
    Revision      int64
    GeneratedAtMS int64
}

// ConsolidationBatch is one phase 2 read.
type ConsolidationBatch struct {
    Extractions []Extraction // outstanding, oldest first, at most limit
    WindowNames []string     // every in-window summary name
    Notes       []Note
}

type Publication struct {
    MemoryMD          string
    MemorySummary     string
    ConsolidatedNames []string
    ConsumedNoteIDs   []string
    Acknowledged      []ExtractionRef // revisions this publication carried
    WindowSinceMS     int64           // window the input was read with
    PublishedAtMS     int64
}

ConsolidationInput(ctx, b, sinceMS, limit) (ConsolidationBatch, error)
```

`WindowNames` is read from the same window as `Extractions`:

```sql
SELECT generated_at_ms, session_ref, rollout_slug
FROM proxy_memory_extractions
WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
  AND generated_at_ms >= ?
```

The Go side renders each name through `SummaryName`. The full name set is what
the CHANGES section and the retained-identity rule need; the selected batch is
what the prompt carries.

Outstanding selection:

```sql
SELECT ...
FROM proxy_memory_extractions
WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
  AND generated_at_ms >= ? AND consolidated_at_ms = 0
ORDER BY generated_at_ms ASC, a_leg_id ASC
LIMIT ?
```

Oldest first is the scheduling order. It drains a static backlog
deterministically and avoids the starvation that newest-first has under steady
arrival. The prompt fitter must therefore keep a prefix, not a suffix (see §5).

`limit < 0` remains "at most zero rows" so both engines agree.

## 4. Publication transaction

`PublishBank` keeps one transaction and one compare-and-set:

1. CAS the bank on `lease_owner` and `version`, writing the documents,
   `consolidated_names`, `published_at_ms`, leaving `consolidated_seq` unchanged
   and clearing the lease. A miss returns `ErrLeaseLost` and rolls back.
2. Acknowledge each carried revision, scoped to the owner:

   ```sql
   UPDATE proxy_memory_extractions SET consolidated_at_ms = ?
   WHERE a_leg_id = ? AND generated_at_ms = ? AND extraction_revision = ?
     AND tenant_id = ? AND principal_id = ? AND project_key = ?
   ```

   Zero rows affected is not an error: the revision was replaced or purged
   between the read and the publication, so it is simply not acknowledged.
   The replacement stays outstanding and keeps the bank dirty.
3. Count the remaining outstanding rows in the window. When the count is zero,
   advance the row this CAS just wrote:

   ```sql
   UPDATE proxy_memory_banks SET consolidated_seq = ?
   WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
     AND lease_owner = '' AND version = ?
   ```

   `?` is `b.DirtySeq` and the version is `b.Version + 1`. The predicate is the
   exact row the CAS produced, so the advance cannot touch another version.
4. Delete exactly the consumed note IDs. Preserve #1299's existing residual-note
   dirty-sequence bump in this same transaction; notes retain an independent obligation.

All steps commit or roll back together. A lease loss, an error, or a panic in
the transaction leaves both the bank and the acknowledgments untouched
(Requirement 1.8).

Rows that arrive after the claim carry a `dirty_seq` above `b.DirtySeq`, so they
keep the bank dirty regardless of the settle decision. `consolidated_seq` never
covers work the claim did not see.

In-window rows older than `max_unused_days` are never selected and never
acknowledged. They are outside the window by configuration; purging removes
them. Settlement therefore counts only rows at or after `WindowSinceMS`.

## 5. Worker flow

1. Read `ConsolidationBatch` with `sinceMS = now - MaxUnusedAge` and
   `limit = MaxRawMemories`.
2. Empty batch and no notes: preserve the existing documents when all consolidated
   identities remain live, and settle without inference. Empty windows clear
   documents. Actual removals from a nonempty window still require consolidation.
3. Build the prompt from `Extractions` (the outstanding batch) and
   `WindowNames` (identity context).
4. Progress guard:
   - a carried outstanding extraction counts as progress;
   - a consumed note counts as progress;
   - a previously consolidated name missing from `WindowNames` is a removal and
     counts as progress;
   - an outstanding extraction the prompt budget left out refuses the attempt
     with the existing extraction-budget error, so no inference is bought and
     nothing is acknowledged.
5. Publish with `Acknowledged` built from the extractions the prompt actually
   carried and `WindowSinceMS` equal to the read window.
6. Failure paths are unchanged: `FailBank` schedules the retry and keeps the
   obligation.

`Phase2Input` gains `WindowNames`. `phase2ChangesText` compares
`PreviousNames` against `WindowNames` for removals, while additions name only
carried documents, so an extraction outside the selected
batch is neither reported as removed nor dropped from `ConsolidatedNames`.
`retainedConsolidationNames` becomes previous names intersected with
`WindowNames` plus the carried names.

The prompt fitter (`fitPhase2Extractions`) keeps the **oldest prefix** of the
batch instead of the newest suffix. All input rows are outstanding, so the
oldest-prefix rule carries the scheduled order into the prompt and leaves the
newest for the next attempt. Size accounting is unchanged: each extraction
still contributes exactly its raw block and rollout block.

## 6. Regression

Store contract (SQLite and PostgreSQL Direct, same cases):

- two outstanding extractions, cap one: first read returns the older one;
  publishing it does not advance `consolidated_seq`; the second read returns the
  newer one; publishing it advances to `dirty_seq`; the bank is no longer
  claimable.
- acknowledgment with a stale generation time leaves the row outstanding.
- a lease-lost publication acknowledges nothing.
- `WindowNames` contains a previously acknowledged extraction that the batch
  does not carry.
- re-upsert of an A-leg resets its obligation.
- A stale reference cannot acknowledge a replacement with the same timestamp.

Worker against the real store (SQLite):

- cap one, two extractions: two ticks buy two inferences, the first request
  carries the older extraction, and the bank settles only after the second.

## 7. Non-goals

No new configuration key. No counters beyond the existing truncation event. No
change to capture, recall, retention or `Forget`.
