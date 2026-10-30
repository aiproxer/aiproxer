package proxymemorystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/uptrace/bun"
)

// ClaimBanks claims up to q.Limit dirty banks under q.LeaseOwner per design
// §12.3: the same two-step as ClaimSessions, with the number of lost races of
// this call expressed as an OFFSET into the deterministic candidate order. A
// bank is claimable only while dirty_seq > consolidated_seq and its retry time
// has passed. One transaction locks the owner erasure epoch first, revalidates
// the complete candidate predicate and owner columns in a conditional UPDATE,
// then loads the claimed snapshot with the epoch and lease deadline. A bank that
// left the candidate window between the SELECT and the UPDATE is skipped
// exactly like a lost race instead of being leased with a stale candidacy.
func (s *Store) ClaimBanks(ctx context.Context, q proxymemory.ClaimQuery) ([]proxymemory.ClaimedBank, error) {
	if q.Limit <= 0 {
		return nil, nil
	}
	var claimed []proxymemory.ClaimedBank
	lost := 0
	for range q.Limit {
		key, found, err := s.bankCandidate(ctx, q, lost)
		if err != nil {
			return nil, err
		}
		if !found {
			break
		}
		var bank proxymemory.ClaimedBank
		err = s.claimBankTx(ctx, key, q, &bank)
		if errors.Is(err, errClaimCandidateLost) {
			// Another instance won the row between the SELECT and the UPDATE,
			// or the row left the candidate window in that gap.
			lost++
			continue
		}
		if errors.Is(err, sql.ErrNoRows) {
			// The bank was removed or published concurrently: nothing to claim.
			continue
		}
		if err != nil {
			return nil, opErr("claim bank lease", err)
		}
		claimed = append(claimed, bank)
	}
	return claimed, nil
}

// claimBankTx runs the same approved epoch-first transaction for a bank: the
// owner epoch lock is first, the complete candidate predicate and owner columns
// are revalidated in the conditional UPDATE, and the bank snapshot is loaded in
// the same transaction. The observed epoch and lease deadline belong to that
// transaction, not to a read after dispatch.
func (s *Store) claimBankTx(ctx context.Context, key bankKey, q proxymemory.ClaimQuery, bank *proxymemory.ClaimedBank) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		epoch, err := lockErasureEpochTx(ctx, tx, key.tenantID, key.principalID, key.projectKey)
		if err != nil {
			return err
		}
		res, err := tx.NewRaw(`
			UPDATE proxy_memory_banks
			SET lease_owner = ?, lease_until_ms = ?, attempts = attempts + 1
			WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
				AND dirty_seq > consolidated_seq
				AND next_attempt_ms <= ?
				AND (lease_owner = '' OR lease_until_ms < ?)
		`, q.LeaseOwner, q.LeaseUntilMS, key.tenantID, key.principalID, key.projectKey, q.NowMS, q.NowMS).Exec(ctx)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return errClaimCandidateLost
		}
		*bank, err = loadClaimedBankTx(ctx, tx, key, q.LeaseOwner)
		if err != nil {
			return err
		}
		bank.LeaseUntilMS = q.LeaseUntilMS
		bank.ErasureEpoch = epoch
		return nil
	})
}

// errClaimCandidateLost marks a conditional bank update that matched no row.
var errClaimCandidateLost = errors.New("proxymemorystore: bank claim candidate lost")

// bankKey identifies one bank row during the two-step claim.
type bankKey struct {
	tenantID    string
	principalID string
	projectKey  string
}

// bankCandidate runs design §12.3's bank candidate step: the dirtiest bank
// whose retry time has passed and whose lease is free or expired. offset skips
// the rows already lost to other instances in this call.
func (s *Store) bankCandidate(ctx context.Context, q proxymemory.ClaimQuery, offset int) (bankKey, bool, error) {
	var key bankKey
	err := s.db.NewRaw(`
		SELECT tenant_id, principal_id, project_key FROM proxy_memory_banks
		WHERE dirty_seq > consolidated_seq AND next_attempt_ms <= ?
			AND (lease_owner = '' OR lease_until_ms < ?)
		ORDER BY (dirty_seq - consolidated_seq) DESC, tenant_id, principal_id, project_key
		LIMIT 1 OFFSET ?
	`, q.NowMS, q.NowMS, offset).Scan(ctx, &key.tenantID, &key.principalID, &key.projectKey)
	if errors.Is(err, sql.ErrNoRows) {
		return bankKey{}, false, nil
	}
	if err != nil {
		return bankKey{}, false, opErr("select claimable bank", err)
	}
	return key, true, nil
}

// loadClaimedBankTx loads the bank row the conditionally updated lease owns,
// including the last published consolidation names, inside the claim
// transaction.
func loadClaimedBankTx(ctx context.Context, tx bun.Tx, key bankKey, leaseOwner string) (proxymemory.ClaimedBank, error) {
	var (
		b     proxymemory.ClaimedBank
		names string
	)
	err := tx.NewRaw(`
		SELECT tenant_id, principal_id, project_key, project_label, version, dirty_seq,
		       memory_md, memory_summary, consolidated_names
		FROM proxy_memory_banks
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ? AND lease_owner = ?
	`, key.tenantID, key.principalID, key.projectKey, leaseOwner).Scan(ctx,
		&b.Owner.TenantID, &b.Owner.PrincipalID, &b.Owner.ProjectKey, &b.ProjectLabel,
		&b.Version, &b.DirtySeq, &b.MemoryMD, &b.MemorySummary, &names)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return proxymemory.ClaimedBank{}, err
		}
		return proxymemory.ClaimedBank{}, opErr("load claimed bank", err)
	}
	b.LeaseOwner = leaseOwner
	if names != "" {
		if err := json.Unmarshal([]byte(names), &b.ConsolidatedNames); err != nil {
			return proxymemory.ClaimedBank{}, opErr("decode consolidated names", err)
		}
	}
	return b, nil
}

// ConsolidationInput returns one phase 2 read per design §12.5: the window's
// outstanding extractions (those not yet acknowledged by a publication), oldest
// first and at most limit, every in-window summary name in generation order,
// and all of the owner's notes ordered by creation time.
//
// Oldest first is the scheduling order: an extraction excluded by the limit
// stays outstanding and is selected by a later attempt instead of being lost
// behind a permanently newer sample.
func (s *Store) ConsolidationInput(ctx context.Context, b proxymemory.ClaimedBank, sinceMS int64, limit int) (proxymemory.ConsolidationBatch, error) {
	if limit < 0 {
		// SQLite reads a negative LIMIT as unbounded; keep both engines on the
		// "at most limit rows" contract.
		limit = 0
	}
	var rows []extractionRow
	err := s.db.NewRaw(`
		SELECT a_leg_id, session_ref, raw_memory, rollout_summary, rollout_slug, source_updated_ms, generated_at_ms, extraction_revision
		FROM proxy_memory_extractions
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ? AND generated_at_ms >= ?
			AND consolidated_at_ms = 0
		ORDER BY generated_at_ms ASC, a_leg_id ASC
		LIMIT ?
	`, b.Owner.TenantID, b.Owner.PrincipalID, b.Owner.ProjectKey, sinceMS, limit).Scan(ctx, &rows)
	if err != nil {
		return proxymemory.ConsolidationBatch{}, opErr("select consolidation extractions", err)
	}
	batch := proxymemory.ConsolidationBatch{
		Extractions: make([]proxymemory.Extraction, 0, len(rows)),
	}
	for _, row := range rows {
		batch.Extractions = append(batch.Extractions, proxymemory.Extraction{
			Revision:        row.Revision,
			Owner:           b.Owner,
			ALegID:          row.ALegID,
			SessionRef:      row.SessionRef,
			RawMemory:       row.RawMemory,
			RolloutSummary:  row.RolloutSummary,
			RolloutSlug:     row.RolloutSlug,
			SourceUpdatedMS: row.SourceUpdatedMS,
			GeneratedAtMS:   row.GeneratedAtMS,
		})
	}
	names, err := s.windowNames(ctx, b.Owner, sinceMS)
	if err != nil {
		return proxymemory.ConsolidationBatch{}, err
	}
	batch.WindowNames = names
	notes, err := s.ownerNotes(ctx, b.Owner)
	if err != nil {
		return proxymemory.ConsolidationBatch{}, err
	}
	batch.Notes = notes
	return batch, nil
}

// windowNames lists every in-window summary name of the owner, including
// already acknowledged extractions. It is the identity set the CHANGES section
// and the retained-consolidation rule compare against.
func (s *Store) windowNames(ctx context.Context, o proxymemory.Owner, sinceMS int64) ([]string, error) {
	var rows []extractionRow
	err := s.db.NewRaw(`
		SELECT session_ref, rollout_slug, generated_at_ms
		FROM proxy_memory_extractions
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ? AND generated_at_ms >= ?
		ORDER BY generated_at_ms ASC, a_leg_id ASC
	`, o.TenantID, o.PrincipalID, o.ProjectKey, sinceMS).Scan(ctx, &rows)
	if err != nil {
		return nil, opErr("select consolidation window", err)
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, proxymemory.SummaryName(proxymemory.Extraction{
			SessionRef:    row.SessionRef,
			RolloutSlug:   row.RolloutSlug,
			GeneratedAtMS: row.GeneratedAtMS,
		}))
	}
	return names, nil
}

// extractionRow maps one proxy_memory_extractions row for raw scans.
type extractionRow struct {
	Revision        int64  `bun:"extraction_revision"`
	ALegID          string `bun:"a_leg_id"`
	SessionRef      string `bun:"session_ref"`
	RawMemory       string `bun:"raw_memory"`
	RolloutSummary  string `bun:"rollout_summary"`
	RolloutSlug     string `bun:"rollout_slug"`
	SourceUpdatedMS int64  `bun:"source_updated_ms"`
	GeneratedAtMS   int64  `bun:"generated_at_ms"`
}

// noteRow maps one proxy_memory_notes row for raw scans.
type noteRow struct {
	NoteID      string `bun:"note_id"`
	Note        string `bun:"note"`
	CreatedAtMS int64  `bun:"created_at_ms"`
}

// ownerNotes selects all of the owner's notes in creation order.
func (s *Store) ownerNotes(ctx context.Context, o proxymemory.Owner) ([]proxymemory.Note, error) {
	var rows []noteRow
	err := s.db.NewRaw(`
		SELECT note_id, note, created_at_ms FROM proxy_memory_notes
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
		ORDER BY created_at_ms
	`, o.TenantID, o.PrincipalID, o.ProjectKey).Scan(ctx, &rows)
	if err != nil {
		return nil, opErr("select owner notes", err)
	}
	notes := make([]proxymemory.Note, 0, len(rows))
	for _, row := range rows {
		notes = append(notes, proxymemory.Note{NoteID: row.NoteID, Text: row.Note, CreatedAtMS: row.CreatedAtMS})
	}
	return notes, nil
}

// PublishBank stores p as the bank's next published version per design §12.5.
// One transaction runs the compare-and-set update — the claimed lease owner
// plus the claimed version must still match — acknowledges the extraction
// revisions the publication carried, advances consolidated_seq only when no
// in-window extraction remains outstanding, and deletes exactly the consumed
// note IDs, scoped to the owner. A CAS miss rolls back and returns
// ErrLeaseLost, so a version published by another instance stays in effect, no
// extraction is acknowledged and no note is deleted (Requirements 5.3, 5.7).
//
// The acknowledgment is revision-exact: an extraction replaced between the input
// read and this publication carries a different generation time, so it matches
// no row and stays outstanding. That is not an error; the replacement is the
// work the next attempt must consolidate.
func (s *Store) PublishBank(ctx context.Context, b proxymemory.ClaimedBank, p proxymemory.Publication) error {
	names := p.ConsolidatedNames
	if names == nil {
		names = []string{}
	}
	encodedNames, err := json.Marshal(names)
	if err != nil {
		return opErr("encode consolidated names", err)
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.NewRaw(`
			UPDATE proxy_memory_banks
			SET version = version + 1, memory_md = ?, memory_summary = ?, published_at_ms = ?,
				consolidated_names = ?,
				lease_owner = '', lease_until_ms = 0, attempts = 0, next_attempt_ms = 0
			WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
				AND lease_owner = ? AND version = ?
		`, p.MemoryMD, p.MemorySummary, p.PublishedAtMS, string(encodedNames),
			b.Owner.TenantID, b.Owner.PrincipalID, b.Owner.ProjectKey, b.LeaseOwner, b.Version).Exec(ctx)
		if err != nil {
			return opErr("publish bank", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return opErr("publish bank rows affected", err)
		}
		if affected == 0 {
			return proxymemory.ErrLeaseLost
		}
		if len(p.ConsumedNoteIDs) > 0 {
			args := make([]any, 0, len(p.ConsumedNoteIDs)+3)
			for _, noteID := range p.ConsumedNoteIDs {
				args = append(args, noteID)
			}
			args = append(args, b.Owner.TenantID, b.Owner.PrincipalID, b.Owner.ProjectKey)
			if _, err := tx.NewRaw(
				`DELETE FROM proxy_memory_notes WHERE note_id IN (`+placeholders(len(p.ConsumedNoteIDs))+`)
				 AND tenant_id = ? AND principal_id = ? AND project_key = ?`,
				args...,
			).Exec(ctx); err != nil {
				return opErr("delete consumed notes", err)
			}
		}
		// Zero means outstanding, so a publication without a timestamp still
		// has to produce an acknowledgment marker that is not zero.
		acknowledgedAtMS := p.PublishedAtMS
		if acknowledgedAtMS == 0 {
			acknowledgedAtMS = 1
		}
		for _, ref := range p.Acknowledged {
			if _, err := tx.NewRaw(`
				UPDATE proxy_memory_extractions SET consolidated_at_ms = ?
				WHERE a_leg_id = ? AND generated_at_ms = ? AND extraction_revision = ?
					AND tenant_id = ? AND principal_id = ? AND project_key = ?
			`, acknowledgedAtMS, ref.ALegID, ref.GeneratedAtMS, ref.Revision,
				b.Owner.TenantID, b.Owner.PrincipalID, b.Owner.ProjectKey).Exec(ctx); err != nil {
				return opErr("acknowledge extraction", err)
			}
		}
		var outstanding int
		if err := tx.NewRaw(`
			SELECT count(*) FROM proxy_memory_extractions
			WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
				AND generated_at_ms >= ? AND consolidated_at_ms = 0
		`, b.Owner.TenantID, b.Owner.PrincipalID, b.Owner.ProjectKey, p.WindowSinceMS).Scan(ctx, &outstanding); err != nil {
			return opErr("count outstanding extractions", err)
		}
		if outstanding == 0 {
			// The predicate is the exact row this transaction's CAS wrote, so the
			// advance can never touch another version.
			res, err = tx.NewRaw(`
				UPDATE proxy_memory_banks SET consolidated_seq = ?
				WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
					AND lease_owner = '' AND version = ?
			`, b.DirtySeq, b.Owner.TenantID, b.Owner.PrincipalID, b.Owner.ProjectKey, b.Version+1).Exec(ctx)
			if err != nil {
				return opErr("settle bank", err)
			}
			settled, err := res.RowsAffected()
			if err != nil {
				return opErr("settle bank rows affected", err)
			}
			if settled == 0 {
				return opErr("settle bank", errors.New("published bank row not found"))
			}
		}
		var residual int
		if err := tx.NewRaw(`
			SELECT count(*) FROM proxy_memory_notes
			WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
		`, b.Owner.TenantID, b.Owner.PrincipalID, b.Owner.ProjectKey).Scan(ctx, &residual); err != nil {
			return opErr("count residual notes", err)
		}
		if residual == 0 {
			return nil
		}
		res, err = tx.NewRaw(`
			UPDATE proxy_memory_banks
			SET dirty_seq = dirty_seq + 1, attempts = 0, next_attempt_ms = 0
			WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
				AND lease_owner = '' AND version = ?
		`, b.Owner.TenantID, b.Owner.PrincipalID, b.Owner.ProjectKey, b.Version+1).Exec(ctx)
		if err != nil {
			return opErr("bump residual note progress", err)
		}
		bumped, err := res.RowsAffected()
		if err != nil {
			return opErr("bump residual note progress rows affected", err)
		}
		if bumped == 0 {
			// The CAS above wrote version b.Version+1 and cleared the lease in
			// this same transaction, so the bump cannot miss; a miss would mean
			// the row vanished between two statements of one transaction.
			return opErr("bump residual note progress", errors.New("published bank row not found"))
		}
		return nil
	})
}

// FailBank clears the bank's lease and schedules its next attempt per design
// §12.5. The lease-owner predicate keeps a stale caller from clearing a fresh
// lease taken by another instance; a miss is not an error because there is no
// result to discard.
func (s *Store) FailBank(ctx context.Context, b proxymemory.ClaimedBank, retryAtMS int64) error {
	_, err := s.db.NewRaw(`
		UPDATE proxy_memory_banks
		SET lease_owner = '', lease_until_ms = 0, next_attempt_ms = ?
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ? AND lease_owner = ?
	`, retryAtMS, b.Owner.TenantID, b.Owner.PrincipalID, b.Owner.ProjectKey, b.LeaseOwner).Exec(ctx)
	if err != nil {
		return opErr("fail bank", err)
	}
	return nil
}
