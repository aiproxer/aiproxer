# ADR 0009: NUL-free persisted identity keys for Bun 1.3

## Status

Proposed (#853). Slice 1 (Bun 1.3 with byte-identical persisted keys) is
implemented. Slice 2 is implemented for observation identities (#933) and for
a second, claim-scoped persisted key (#990). Legacy-row backfill and the
`Fact.Validate` control-character policy remain deferred.

## Context

Several in-memory identity keys are NUL-delimited, most importantly
`metering.Observation.IdentityKey()` (`v{ver}\0{store}\0{tenant}\0{account}\0{origin}\0{acquisition}\0{lineage JSON}\0{stream}\0{source event key}\0{revision}`).
Bun's query builder inlines string arguments into the SQL text (`?` placeholders
are formatted client-side, including `bun.DB.QueryContext`/`ExecContext` and
`bun.Tx`). Bun's `BaseDialect.AppendString` changed:

| Bun | NUL in a string argument |
|---|---|
| ≤ 1.2.18 | silently dropped |
| 1.3.0 | formatter error; SQLite reports `unrecognized token: "!"` |

So every row written so far stores the NUL-stripped form, on both SQLite and
PostgreSQL. PostgreSQL never saw a NUL, because Bun stripped it before sending.
Lookups were stripped the same way, which is the only reason they matched.

### Inventory under Bun 1.3

The full suite was run with all four Bun modules at v1.3.0. Every
Bun-attributable failure traces to three production call sites:

| Site | Key | Role |
|---|---|---|
| `journalstore.AppendObservation` insert + lookup | `Observation.SourceEventIdentity()` → `metering_facts.source_event_key` | `UNIQUE(store_id, source_event_key)` |
| `journalstore.lookupDurableSourcePayload` + V1 fact insert | `Fact.SourceEventLookupKeys()`: phase-3.1 NUL key, V0/V1 NUL aliases, `IdempotencyKey` | ordered legacy lookup |
| durable store claim insert | `ScopedOperationKey(..., accountID+"\0"+submissionID)` → `claim_key` | primary key only; lookups use `(store_id, account_id, submission_id)` |

The other NUL-delimited keys in the tree are in-memory map keys, or values that
are JSON-encoded (`\u0000`) or hashed before they reach SQL.

### Latent defect in the stripped representation

After stripping, adjacent fields concatenate without a separator, so the stored
key is not injective. A probe on main with Bun 1.2.18 confirmed it: an
observation with source event key `evt-1` at revision 2, and a distinct one with
`evt-` at revision 12, both store `...evt-12`. The second is rejected with
`ErrIdentityCollision: stored observation identity drift`. Replay validation
compares observation ID, revision and fingerprint, so this fails closed and never
merges two observations. But it drops a legitimate metering observation. The same
ambiguity exists between tenant and provider-account key, and between the account
and submission parts of `claim_key`.

### Rollback constraint

Bun's migrator applies only the migrations it knows about and does not refuse a
database that has newer ones. The schema verifiers check that required objects
exist, not that newer ones are absent. A binary that is already released will
therefore open a database written by a newer release. If newer rows used a
different key encoding, a rolled-back binary would miss them on replay and insert
duplicate rows (double metering), and no unique constraint would stop it.

## Decision

### Slice 1: upgrade Bun without changing any stored byte (this change)

- Add `dbinfra.LegacyPersistedText(s)`: it drops NUL bytes and is documented as
  the exact representation Bun ≤ 1.2 persisted.
- Apply it at the SQL boundary of the three sites above, and only there.
  In-memory `IdentityKey()`, `SourceEventKey()` and `ScopedOperationKey()` are
  unchanged, because they also feed map identity, ordering and persisted
  fingerprints (for example revision replay payloads).
- Lookup aliases are de-duplicated after the conversion, because two NUL variants
  can map to the same stored key.
- Tests that seeded "legacy NUL" rows through Bun now seed through the same
  helper. This stores the same bytes they always stored; assertions are unchanged.
- A golden test pins the `source_event_key` bytes that Bun 1.2.18 stored for a
  fixed observation (captured from a real 1.2.18 run), so any later change to the
  encoding is caught.
- Bun's fail-closed formatter is kept. Any new NUL-bearing argument errors
  instead of being silently altered.

Old and new binaries read and write identical bytes, so no migration is needed
and rollback is safe in both directions. Replay, store-scoped identity and
SQLite/PostgreSQL parity are unchanged by construction.

### Slice 2: injective persisted keys (#933 observations, #990 claim key, implemented)

Fixing the latent collision changes stored identity:

1. `db.PersistedKeyV2` frames tagged key parts with their lengths.
   `PersistedIdentityKeyV2` applies it to the NUL-separated fields of the
   unstripped `IdentityKey()` (`obs:v2:`), and the claim-scoped key applies it
   to `(store, account, submission)` (`claim:v2:`, #990). Framing is injective,
   the prefixes are disjoint from every legacy key, and the encodings contain no
   NUL for Bun's SQL formatter. Claim reads use the immutable
   (store, account, submission) scope index, so legacy claim rows stay readable
   without a dual lookup.
2. The append writes the v2 key and resolves identity as follows. When the
   store may hold legacy keys (probed once per store instance through an
   indexed `source_event_key` range probe), the legacy stripped key is read
   first: a non-observation occupant stays a typed `ErrIdentityCollision`; a row
   whose recomputed unstripped identity is equal is the same observation and its
   replay or drift classification is returned without a write; a row that
   matches only after stripping is a false match, so the append continues and
   persists under the v2 key. After the insert, the v2 key resolves replays and
   identity drift, with the observation id/revision fence as the final fallback.
3. Rollback is a documented boundary, not a schema fence. Shipped binaries
   replaying a v2-keyed row still classify it through the encoding-independent
   `(store_id, stream_id, fact_id)` fence and observation id/revision columns,
   so rollback cannot duplicate rows; it only degrades to the pre-fix
   stripped-collision drops. `TestRollbackBoundaryLegacyReplayCannotDuplicateV2Row`
   pins that property. A mechanical fence (renaming a verifier-required schema
   object) would fire at deploy time, before the first v2 write, and was
   rejected because the duplicate hazard it guards against is not reachable for
   the shipped schema.
4. Deferred: a backfill of legacy observation rows to v2 keys, which retires
   the dual lookup.
5. Deferred: decide whether `Fact.Validate` should reject control characters in
   `StreamID`/`SourceID`/`SourceEventKind`. Today it checks length only, so a NUL
   there is silently stripped.

## Alternatives rejected

- **New encoding inside the Bun upgrade:** couples a dependency bump to an
  identity change and creates the duplicate-on-rollback hazard described above.
- **Quarantining the failing tests, or making the formatter tolerant again:**
  hides future NUL leaks. #853 explicitly forbids it.
- **Hex or base64 of the NUL-delimited key:** injective, but it is still a new
  encoding, with all of slice 2's rollout cost and none of its lookup design.
- **Removing NUL from `IdentityKey()` itself:** changes in-memory identity and
  persisted fingerprints far beyond SQL.

## Verification

- Full suite with Bun 1.3.0, Go 1.26.9: every Bun-attributable failure is fixed.
  The remaining local failures (`source-integrity-failed reason=read` in reload
  tests) reproduce identically on unmodified main and are environmental.
- Golden byte test for observations; existing phase-3.2 legacy-key replay tests
  cover V1 facts.
- Slice 2: stripped-collision, false-match, identity-drift, read-gating,
  rollback-boundary and golden tests in `journalstore`; the collision pair is
  also pinned on live PostgreSQL.
- Claim key: distinct stripped-collision scopes persist separately and legacy
  claims stay readable on SQLite and live PostgreSQL; the scope unique index
  remains the mixed-encoding duplicate guard.
- PostgreSQL parity: CI `Database parity` job.
