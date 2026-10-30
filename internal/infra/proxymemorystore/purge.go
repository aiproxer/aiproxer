package proxymemorystore

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/uptrace/bun"
)

// retiredBindingPageSize is the maximum number of binding candidates one
// retired-A-leg sweep examines per invocation. One invocation costs at most
// one core liveness query.
const retiredBindingPageSize = 256

// The Forget statements of design §12.6 step 1: the fence takes the A-leg's
// recorded tenant and principal from its own rows, and ON CONFLICT DO NOTHING
// keeps an older fence untouched. The extraction ledger joins the union
// because Purge deletes extraction rows later, and the ledger is the durable
// identity of an A-leg that already contributed to the bank (Requirement 8.6).
// Every SELECT carries a WHERE clause, which also keeps SQLite's upsert
// parsing unambiguous.
const (
	forgetFenceBindingsSQL = `
		INSERT INTO proxy_memory_fence (a_leg_id, tenant_id, principal_id, forgotten_at_ms)
		SELECT a_leg_id, tenant_id, principal_id, ? FROM proxy_memory_session_bindings
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
		ON CONFLICT DO NOTHING`
	forgetFenceSessionsSQL = `
		INSERT INTO proxy_memory_fence (a_leg_id, tenant_id, principal_id, forgotten_at_ms)
		SELECT a_leg_id, tenant_id, principal_id, ? FROM proxy_memory_sessions
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
		ON CONFLICT DO NOTHING`
	forgetFenceExtractionsSQL = `
		INSERT INTO proxy_memory_fence (a_leg_id, tenant_id, principal_id, forgotten_at_ms)
		SELECT a_leg_id, tenant_id, principal_id, ? FROM proxy_memory_extractions
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
		ON CONFLICT DO NOTHING`
	forgetFenceLedgerSQL = `
		INSERT INTO proxy_memory_fence (a_leg_id, tenant_id, principal_id, forgotten_at_ms)
		SELECT a_leg_id, tenant_id, principal_id, ? FROM proxy_memory_extraction_ledger
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
		ON CONFLICT DO NOTHING`
)

// Forget erases one owner's memory and makes the erasure durable per design
// §12.6. One transaction advances the owner-scoped erasure epoch, inserts a
// fence row for every A-leg the owner still has in proxy_memory_sessions,
// proxy_memory_extractions, proxy_memory_extraction_ledger or
// proxy_memory_session_bindings, deletes the owner's sessions, extractions and
// ledger rows, scrubs the remembered content of the owner's bindings, takes
// the owner's bank lock — the same row AddNote locks, and still the first
// bank-table statement of the transaction — then deletes the owner's notes and
// published bank.
//
// The lock sits immediately before the notes and bank deletes because only
// those two must dominate a concurrent AddNote: a concurrent AddNote either
// commits before the lock and its note is erased with everything else, or it
// waits behind the lock and finds no bank afterwards (Requirements 8.3, 8.5).
// Taking the bank lock late is a lock-order choice, not a §12.6 semantics
// change: CompleteSession (§12.4) locks the session row first and writes the
// bank later, so a Forget that held the bank lock first and then waited on a
// session row would deadlock with it.
//
// A missing bank row is not an error: the owner's leftover sessions,
// extractions, ledger rows or notes are still cleaned up and counted. It
// reports ErrNotFound when nothing was erased (a repeated forget, or an owner
// with no memory left).
//
// The erasure epoch is advanced before anything is observed or deleted, so a
// first-turn decision queued before this call — whose A-leg never had a
// binding to fence — fails its epoch check (Requirement 8.6). The fence rows
// are what keep a vanished bank vanished: UpsertSession refuses a fenced
// A-leg, and CompleteSession/Purge delete a session that the §12.2 race may
// have recreated (Requirement 8.6).
func (s *Store) Forget(ctx context.Context, o proxymemory.Owner, nowMS int64) error {
	deleted := int64(0)
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		deleted = 0
		// Advance the durable fence before observing contributors or deleting
		// artifacts. A queued unbound first-turn job then fails its epoch check
		// even when no A-leg binding existed for Forget to discover.
		if err := bumpErasureEpochTx(ctx, tx, o); err != nil {
			return err
		}
		for _, fenceStatement := range []string{forgetFenceBindingsSQL, forgetFenceSessionsSQL, forgetFenceExtractionsSQL, forgetFenceLedgerSQL} {
			if _, err := tx.NewRaw(fenceStatement, nowMS, o.TenantID, o.PrincipalID, o.ProjectKey).Exec(ctx); err != nil {
				return opErr("fence forgotten A-legs", err)
			}
		}
		deleteOwnerRows := func(statement string) error {
			res, err := tx.NewRaw(statement, o.TenantID, o.PrincipalID, o.ProjectKey).Exec(ctx)
			if err != nil {
				return opErr("delete forgotten rows", err)
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return opErr("delete forgotten rows affected", err)
			}
			deleted += affected
			return nil
		}
		// Sessions, extractions and the participation ledger first: Forget
		// holds no bank lock here, so a CompleteSession or Purge that already
		// locked one of these rows can finish instead of waiting behind a bank
		// lock this transaction holds. The ledger delete follows the fencing
		// above, so deleting it cannot lose the fence it exists for.
		if err := deleteOwnerRows(`DELETE FROM proxy_memory_sessions WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`); err != nil {
			return err
		}
		if err := deleteOwnerRows(`DELETE FROM proxy_memory_extractions WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`); err != nil {
			return err
		}
		if err := deleteOwnerRows(`DELETE FROM proxy_memory_extraction_ledger WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`); err != nil {
			return err
		}
		// Failure evidence is owner-scoped diagnostic state: Forget erases it
		// in the same transaction that advanced the epoch, so a delayed
		// old-epoch insert is refused rather than resurrecting the fact.
		if err := deleteOwnerRows(`DELETE FROM proxy_memory_phase_failures WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`); err != nil {
			return err
		}
		// Erase the remembered content the binding still holds: the workspace
		// label and the frozen first-turn snapshot (its rendered injection,
		// the summary version and the epoch that authorized it). The row is
		// retained as content-free identity — owner columns and project key —
		// so the erased A-leg keeps its fence without keeping the owner's
		// memory text. The predicate selects only rows that still carry
		// content, so a repeated forget erases nothing. This runs before the
		// bank lock and keeps the session/extraction-before-bank order.
		scrub, err := tx.NewRaw(`UPDATE proxy_memory_session_bindings SET
				project_label = '', first_turn_outcome = '', first_turn_injection = '',
				first_turn_summary_version = 0, first_turn_epoch = 0
			WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
				AND (project_label <> '' OR first_turn_outcome <> '' OR first_turn_injection <> ''
					OR first_turn_summary_version <> 0 OR first_turn_epoch <> 0)`,
			o.TenantID, o.PrincipalID, o.ProjectKey).Exec(ctx)
		if err != nil {
			return opErr("scrub forgotten binding content", err)
		}
		scrubbed, err := scrub.RowsAffected()
		if err != nil {
			return opErr("scrub forgotten binding content rows affected", err)
		}
		deleted += scrubbed

		// The owner's bank row is the serialization anchor against AddNote:
		// the lock goes in here, immediately before the notes and bank deletes
		// it must dominate, and remains this transaction's first bank-table
		// statement.
		if _, err := s.lockOwnerBankTx(ctx, tx, o); err != nil {
			return err
		}
		if err := deleteOwnerRows(`DELETE FROM proxy_memory_notes WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`); err != nil {
			return err
		}
		if err := deleteOwnerRows(`DELETE FROM proxy_memory_banks WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if deleted == 0 {
		return proxymemory.ErrNotFound
	}
	return nil
}

// purgeOwnerKey names one owner with expired extraction rows.
type purgeOwnerKey struct {
	TenantID    string `bun:"tenant_id"`
	PrincipalID string `bun:"principal_id"`
	ProjectKey  string `bun:"project_key"`
}

// Purge deletes aged work per design §12.6 and returns the total number of
// deleted rows. Fence rows are never purged, and neither are the
// extraction-ledger rows: they carry no content, only the identity of an A-leg
// that already contributed to its bank, and that identity is what Forget must
// fence later (Requirement 8.6).
//
//  1. Abandoned sessions: older than sessionsBeforeMS and either unleased or
//     past their lease, so work under an active lease is left alone.
//  2. Expired extractions: up to limit distinct owners with rows generated
//     before extractionsBeforeMS; each owner's delete and its bank's
//     dirty_seq bump run in one transaction, so the next consolidation
//     republishes without the aged results instead of silently keeping them.
//     A non-positive limit skips this step (SQLite reads a negative LIMIT as
//     unbounded while PostgreSQL rejects it, so both engines stay on the "at
//     most limit owners" contract).
//  3. Sessions whose A-leg is fenced: this removes a session that the §12.2
//     race recreated after a Forget, which is what makes forget durable.
//  4. Retired bindings: at most limit binding rows whose authoritative A-leg
//     row no longer exists. Only binding metadata is removed — the learned
//     memory of a retired A-leg outlives its retirement.
//  5. Expired failure evidence: at most limit expired composite operation
//     keys, selected in stable order and deleted with the expiry predicate
//     repeated. A nonpositive limit deletes none.
//
// The abandoned-session step is bounded by its cutoff, not by limit: it
// removes every session row older than sessionsBeforeMS. The other steps are
// bounded by limit.
func (s *Store) Purge(ctx context.Context, nowMS, sessionsBeforeMS, extractionsBeforeMS int64, limit int) (int, error) {
	deleted := 0
	res, err := s.db.NewRaw(`
		DELETE FROM proxy_memory_sessions
		WHERE last_activity_ms < ? AND (lease_owner = '' OR lease_until_ms < ?)
	`, sessionsBeforeMS, nowMS).Exec(ctx)
	if err != nil {
		return 0, opErr("purge aged sessions", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, opErr("purge aged sessions rows affected", err)
	}
	deleted += int(affected)

	// Failure evidence is selected and deleted under the same per-pass budget
	// as the other bounded steps. The fixed expiry is only eligibility: it
	// never bounds the amount of work, so a nonpositive budget deletes nothing
	// and a positive budget removes at most that many stable operation keys.
	if limit > 0 {
		swept, err := s.purgeExpiredFailures(ctx, nowMS, limit)
		if err != nil {
			return 0, err
		}
		deleted += swept
	}

	if limit > 0 {
		var keys []purgeOwnerKey
		err := s.db.NewRaw(`
			SELECT DISTINCT tenant_id, principal_id, project_key FROM proxy_memory_extractions
			WHERE generated_at_ms < ?
			ORDER BY tenant_id, principal_id, project_key
			LIMIT ?
		`, extractionsBeforeMS, limit).Scan(ctx, &keys)
		if err != nil {
			return 0, opErr("select expired extraction owners", err)
		}
		for _, key := range keys {
			err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
				res, err := tx.NewRaw(`
					DELETE FROM proxy_memory_extractions
					WHERE tenant_id = ? AND principal_id = ? AND project_key = ? AND generated_at_ms < ?
				`, key.TenantID, key.PrincipalID, key.ProjectKey, extractionsBeforeMS).Exec(ctx)
				if err != nil {
					return opErr("purge expired extractions", err)
				}
				affected, err := res.RowsAffected()
				if err != nil {
					return opErr("purge expired extractions rows affected", err)
				}
				if _, err := tx.NewRaw(`
					UPDATE proxy_memory_banks SET dirty_seq = dirty_seq + 1
					WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
				`, key.TenantID, key.PrincipalID, key.ProjectKey).Exec(ctx); err != nil {
					return opErr("dirty purged bank", err)
				}
				deleted += int(affected)
				return nil
			})
			if err != nil {
				return 0, err
			}
		}
	}

	res, err = s.db.NewRaw(`
		DELETE FROM proxy_memory_sessions
		WHERE a_leg_id IN (SELECT a_leg_id FROM proxy_memory_fence)
	`).Exec(ctx)
	if err != nil {
		return 0, opErr("purge fenced sessions", err)
	}
	affected, err = res.RowsAffected()
	if err != nil {
		return 0, opErr("purge fenced sessions rows affected", err)
	}
	deleted += int(affected)

	if limit > 0 {
		swept, err := s.sweepRetiredBindings(ctx, limit)
		if err != nil {
			return 0, err
		}
		deleted += swept
	}
	return deleted, nil
}

// sweepRetiredBindings removes at most limit binding rows whose authoritative
// A-leg no longer exists in the core continuity database. Only binding
// metadata is removed — the learned memory of a retired A-leg outlives its
// retirement.
//
// The work of one invocation is bounded: the sweep examines at most
// min(limit, retiredBindingPageSize) candidates and asks the core database
// about them in at most one query, whatever the size of the binding table.
//
// The continuation cursor (the store's sweep state) names the last candidate
// the sweep resolved, so successive invocations walk the table instead of
// rescanning its first page: a retired binding behind an all-active prefix is
// reached eventually instead of being starved. At the end of the table the
// cursor wraps to its beginning, so a binding inserted before the cursor is
// considered again.
//
// The core A-leg liveness is read through the narrow core view, outside the
// memory database: the memory database is isolated from the core continuity
// database, so a memory statement must never join a core table. No memory
// transaction is open while the core liveness is read.
func (s *Store) sweepRetiredBindings(ctx context.Context, limit int) (int, error) {
	budget := min(limit, retiredBindingPageSize)
	if budget <= 0 {
		return 0, nil
	}
	// The continuation cursor is shared state of the store, so concurrent
	// sweeps are serialized here rather than in the database.
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()

	candidates, err := s.bindingCandidates(ctx, s.sweepAfter, budget)
	if err != nil {
		return 0, err
	}
	if len(candidates) == 0 {
		// The table ends here: the cursor wraps, so a binding inserted before
		// it is considered again.
		s.sweepAfter = ""
		return 0, nil
	}
	active, err := s.core.ActiveALegs(ctx, candidates)
	if err != nil {
		return 0, err
	}
	retired := make([]string, 0, len(candidates))
	for _, aLegID := range candidates {
		if !active[aLegID] {
			retired = append(retired, aLegID)
		}
	}
	deleted := 0
	if len(retired) > 0 {
		args := make([]any, 0, len(retired))
		for _, aLegID := range retired {
			args = append(args, aLegID)
		}
		res, err := s.db.NewRaw(
			`DELETE FROM proxy_memory_session_bindings WHERE a_leg_id IN (`+placeholders(len(retired))+`)`,
			args...).Exec(ctx)
		if err != nil {
			return 0, opErr("sweep retired A-leg bindings", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, opErr("sweep retired A-leg bindings rows affected", err)
		}
		deleted = int(affected)
	}
	// The cursor advances over the candidates this invocation resolved: the
	// active ones and the retired ones it deleted. The candidate budget never
	// exceeds the delete limit, so no retired candidate is left behind.
	s.sweepAfter = candidates[len(candidates)-1]
	return deleted, nil
}

// bindingCandidates reads one bounded page of binding A-leg IDs, ordered by
// A-leg ID, after the given cursor.
func (s *Store) bindingCandidates(ctx context.Context, after string, pageSize int) ([]string, error) {
	query := `SELECT a_leg_id FROM proxy_memory_session_bindings`
	var args []any
	if after != "" {
		query += ` WHERE a_leg_id > ?`
		args = append(args, after)
	}
	query += ` ORDER BY a_leg_id LIMIT ?`
	args = append(args, pageSize)
	var candidates []string
	if err := s.db.NewRaw(query, args...).Scan(ctx, &candidates); err != nil {
		return nil, opErr("select retired A-leg binding candidates", err)
	}
	return candidates, nil
}

// purgeFailureKey is the stable composite operation key selected for one
// bounded expiry sweep.
type purgeFailureKey struct {
	StoreID       string `bun:"store_id"`
	BillingCallID string `bun:"billing_call_id"`
	Phase         string `bun:"phase"`
}

// purgeExpiredFailures deletes at most limit expired failure facts. The
// candidate keys are selected in a stable composite-key order and each delete
// repeats the expiry predicate, so a concurrent replay/reinsert cannot be
// removed unless it is itself expired. limit must be positive; a nonpositive
// budget is the caller's "no work this pass" signal, not an unlimited sweep.
func (s *Store) purgeExpiredFailures(ctx context.Context, nowMS int64, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	var keys []purgeFailureKey
	err := s.db.NewRaw(`
		SELECT store_id, billing_call_id, phase FROM proxy_memory_phase_failures
		WHERE expires_at_ms <= ?
		ORDER BY store_id, billing_call_id, phase
		LIMIT ?
	`, nowMS, limit).Scan(ctx, &keys)
	if err != nil {
		return 0, opErr("select expired failure evidence", err)
	}
	deleted := 0
	for _, key := range keys {
		res, err := s.db.NewRaw(`
			DELETE FROM proxy_memory_phase_failures
			WHERE store_id = ? AND billing_call_id = ? AND phase = ? AND expires_at_ms <= ?
		`, key.StoreID, key.BillingCallID, key.Phase, nowMS).Exec(ctx)
		if err != nil {
			return 0, opErr("purge expired failure evidence", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, opErr("purge expired failure evidence rows affected", err)
		}
		deleted += int(affected)
	}
	return deleted, nil
}
