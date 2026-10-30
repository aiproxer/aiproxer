# Requirements: proxy-memory-residual-consolidation

## Introduction

Proxy Memory phase 2 reads consolidation input with a per-attempt cap
(`max_raw_memories`, default 256) and the store advances the bank's
`consolidated_seq` to the claimed `dirty_seq` on every publication. When more
extractions are outstanding than the cap, the excluded extractions stay stored
but a worker with the same cap never selects them again: the obligation is lost
silently. This slice makes the obligation durable.

## V1 Slice

A per-extraction acknowledgment recorded in the store, oldest-outstanding-first
selection that drains the backlog across attempts, and a publication that only
advances `consolidated_seq` once no in-window extraction remains
unacknowledged.

## Deferred

- Truncation counters and logs beyond the existing phase 2 truncation event.
- Residual-note progress (owned by #1299).
- Recall-path changes.
- A fairness policy against continuously arriving work beyond oldest-first
  ordering.
- Operator-visible backlog metrics.

## Boundary Context

- Feature plugin: `internal/plugins/features/proxymemory` (selection, prompt,
  progress and publication semantics).
- Infrastructure store: `internal/infra/proxymemorystore` (schema, queries,
  acknowledgment transaction).
- The store is consumed only through the `proxymemory.Store` interface.

## Requirements

### Requirement 1: Durable residual-extraction obligation

1. When a phase 2 attempt carries fewer outstanding extractions than the claim
   covers, its publication must not advance `consolidated_seq` to the claimed
   `dirty_seq`.
2. An extraction excluded by the per-attempt cap must remain outstanding and
   selectable by a later attempt using the same cap.
3. Outstanding extractions must be selected oldest first, with a deterministic
   tie-break on the A-leg identifier, so a static backlog is fully drained.
4. An extraction must be acknowledged only when the publication carried exactly
   that revision, identified by its A-leg and store-assigned revision (with
   generation time also checked). Equal generation times must not alias revisions.
5. Acknowledgment of a revision that no longer exists must acknowledge nothing
   and must not fail the publication.
6. When no in-window extraction remains unacknowledged after the
   acknowledgments of one publication, that same publication must advance
   `consolidated_seq` to the claimed `dirty_seq`.
7. Re-extracting an A-leg must reset that extraction's obligation.
8. A failed, rolled-back or lease-lost publication must acknowledge nothing and
   must not advance `consolidated_seq`.

### Requirement 2: Window identity and consolidation progress

1. The consolidation read must return the outstanding extractions separately
   from the full set of in-window summary names.
2. The phase 2 CHANGES section must compare the previous publication names with
   the full in-window name set, not with the selected batch.
3. A previously consolidated extraction that is not part of the selected batch
   must keep its consolidated identity and must not be reported as removed.
   Settling a drained batch must preserve documents for still-live identities.
4. A previously consolidated name that has left the window must be removed from
   the published identity list.
5. When the prompt budget excludes outstanding extraction content, the attempt
   must refuse without acknowledging the excluded extraction.
6. A publication must consume only the notes it carried.

### Requirement 3: Engine parity and observable degradation

1. SQLite and PostgreSQL Direct must produce identical behavior for
   acknowledgment, selection order and settlement.
2. The slice must not add a new operator configuration key: the existing
   `max_raw_memories`, `max_unused_days` and retry controls define the slice.
3. An unsettled bank must remain claimable without a new capture or note.

## Acceptance Criteria

1. With two outstanding extractions and a cap of one, two successive attempts
   consolidate both, oldest first.
2. Between the two attempts the bank is still claimable and `consolidated_seq`
   has not advanced.
3. An acknowledgment with a stale generation time leaves the extraction
   outstanding.
4. A lease-lost publication acknowledges nothing.
5. Batch omission alone never removes a consolidated name from the published
   identity list.
6. The CHANGES section reports no removal for an extraction that is merely
   outside the selected batch.
7. A prompt-budget refusal buys no inference and publishes nothing.
8. Both engines pass the same contract cases.
