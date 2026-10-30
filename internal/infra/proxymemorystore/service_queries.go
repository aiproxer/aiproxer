package proxymemorystore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// bankStatusSelect builds one bank's read model with three owner-scoped
// COUNT(*) subqueries (design §12.6). The subqueries are correlated on the
// bank row, so they can only count rows of the same tenant, principal and
// project as the bank itself.
const bankStatusSelect = `
	SELECT b.project_key, b.project_label, b.version, b.published_at_ms,
		(SELECT count(*) FROM proxy_memory_sessions s
		  WHERE s.tenant_id = b.tenant_id AND s.principal_id = b.principal_id AND s.project_key = b.project_key) AS pending_sessions,
		(SELECT count(*) FROM proxy_memory_extractions e
		  WHERE e.tenant_id = b.tenant_id AND e.principal_id = b.principal_id AND e.project_key = b.project_key) AS extractions,
		(SELECT count(*) FROM proxy_memory_notes n
		  WHERE n.tenant_id = b.tenant_id AND n.principal_id = b.principal_id AND n.project_key = b.project_key) AS pending_notes
	FROM proxy_memory_banks b
`

// bankStatusRow maps one bank's read model and its pending-work counts.
type bankStatusRow struct {
	ProjectKey      string `bun:"project_key"`
	ProjectLabel    string `bun:"project_label"`
	Version         int64  `bun:"version"`
	PublishedAtMS   int64  `bun:"published_at_ms"`
	PendingSessions int    `bun:"pending_sessions"`
	Extractions     int    `bun:"extractions"`
	PendingNotes    int    `bun:"pending_notes"`
}

// status converts the scanned row into the feature read model; ScopeID is the
// bank's project key (design §8).
func (r bankStatusRow) status() proxymemory.BankStatus {
	return proxymemory.BankStatus{
		ScopeID:         r.ProjectKey,
		ProjectLabel:    r.ProjectLabel,
		Version:         r.Version,
		PublishedAtMS:   r.PublishedAtMS,
		PendingSessions: r.PendingSessions,
		Extractions:     r.Extractions,
		PendingNotes:    r.PendingNotes,
	}
}

// ListBanks returns the banks of one principal per design §12.6: the rows of
// (tenant_id, principal_id) with their owner-scoped pending-work counts,
// ordered by project key so repeated reads stay stable. Another tenant or
// principal is never selected (Requirement 1.5).
func (s *Store) ListBanks(ctx context.Context, p proxymemory.Principal) ([]proxymemory.BankStatus, error) {
	var rows []bankStatusRow
	err := s.db.NewRaw(bankStatusSelect+`
		WHERE b.tenant_id = ? AND b.principal_id = ?
		ORDER BY b.project_key ASC
	`, p.TenantID, p.PrincipalID).Scan(ctx, &rows)
	if err != nil {
		return nil, opErr("select principal banks", err)
	}
	banks := make([]proxymemory.BankStatus, 0, len(rows))
	for _, row := range rows {
		banks = append(banks, row.status())
	}
	return banks, nil
}

// BankStatus returns one bank's read model per design §12.6. A missing row,
// or a row of another owner, returns ErrNotFound.
func (s *Store) BankStatus(ctx context.Context, o proxymemory.Owner) (proxymemory.BankStatus, error) {
	var row bankStatusRow
	err := s.db.NewRaw(bankStatusSelect+`
		WHERE b.tenant_id = ? AND b.principal_id = ? AND b.project_key = ?
	`, o.TenantID, o.PrincipalID, o.ProjectKey).Scan(ctx,
		&row.ProjectKey, &row.ProjectLabel, &row.Version, &row.PublishedAtMS,
		&row.PendingSessions, &row.Extractions, &row.PendingNotes)
	if errors.Is(err, sql.ErrNoRows) {
		return proxymemory.BankStatus{}, proxymemory.ErrNotFound
	}
	if err != nil {
		return proxymemory.BankStatus{}, opErr("select bank status", err)
	}
	return row.status(), nil
}

// Documents returns the bank's current documents per design §12.6: the
// published handbook and summary, the project label, and one rollout summary
// per extraction of the owner, keyed SummaryName(e)+".md" through the feature
// package's single definition. A missing bank returns ErrNotFound. The
// extraction list is read in (generated_at_ms, a_leg_id) order, so two
// extractions that render the same document name resolve deterministically.
func (s *Store) Documents(ctx context.Context, o proxymemory.Owner) (proxymemory.Documents, error) {
	var docs proxymemory.Documents
	err := s.db.NewRaw(`
		SELECT project_label, memory_md, memory_summary FROM proxy_memory_banks
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
	`, o.TenantID, o.PrincipalID, o.ProjectKey).Scan(ctx,
		&docs.ProjectLabel, &docs.MemoryMD, &docs.MemorySummary)
	if errors.Is(err, sql.ErrNoRows) {
		return proxymemory.Documents{}, proxymemory.ErrNotFound
	}
	if err != nil {
		return proxymemory.Documents{}, opErr("select bank documents", err)
	}

	var rows []extractionRow
	err = s.db.NewRaw(`
		SELECT a_leg_id, session_ref, raw_memory, rollout_summary, rollout_slug, source_updated_ms, generated_at_ms
		FROM proxy_memory_extractions
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
		ORDER BY generated_at_ms ASC, a_leg_id ASC
	`, o.TenantID, o.PrincipalID, o.ProjectKey).Scan(ctx, &rows)
	if err != nil {
		return proxymemory.Documents{}, opErr("select bank extractions", err)
	}
	docs.RolloutSummaries = make(map[string]string, len(rows))
	for _, row := range rows {
		name := proxymemory.SummaryName(proxymemory.Extraction{
			SessionRef:    row.SessionRef,
			RolloutSlug:   row.RolloutSlug,
			GeneratedAtMS: row.GeneratedAtMS,
		})
		docs.RolloutSummaries[name+".md"] = rolloutSummaryDocument(docs.ProjectLabel, row)
	}
	return docs, nil
}

// rolloutSummaryDocument renders one rollout summary document per design
// §12.6: the session ref, the bank's project label, the extraction's
// source-update time as RFC 3339 (the Codex rollout updated_at, storage.rs),
// then the summary.
func rolloutSummaryDocument(label string, row extractionRow) string {
	return "session: " + row.SessionRef +
		"\ncwd: " + label +
		"\nupdated_at: " + time.UnixMilli(row.SourceUpdatedMS).UTC().Format(time.RFC3339) +
		"\n\n" + row.RolloutSummary
}

// AddNote appends n to the caller's bank per design §12.6. One transaction
// reads the bank row — through lockOwnerBankTx, so on PostgreSQL the read is
// the same row lock Forget takes — then inserts the note and bumps dirty_seq so
// the next consolidation picks the bank up (Requirement 7.5). A missing bank,
// or one of another owner, returns ErrNotFound and stores nothing. The final
// UPDATE re-checks the bank and its RowsAffected result is checked too: a
// zero-row match returns ErrNotFound, which rolls the note INSERT back, so a
// note can never outlive the erasure of its bank (Requirements 8.3, 8.5).
func (s *Store) AddNote(ctx context.Context, o proxymemory.Owner, n proxymemory.Note) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		exists, err := s.lockOwnerBankTx(ctx, tx, o)
		if err != nil {
			return err
		}
		if !exists {
			return proxymemory.ErrNotFound
		}
		if _, err := tx.NewRaw(`
			INSERT INTO proxy_memory_notes (note_id, tenant_id, principal_id, project_key, note, created_at_ms)
			VALUES (?, ?, ?, ?, ?, ?)
		`, n.NoteID, o.TenantID, o.PrincipalID, o.ProjectKey, n.Text, n.CreatedAtMS).Exec(ctx); err != nil {
			return opErr("insert note", err)
		}
		res, err := tx.NewRaw(`
			UPDATE proxy_memory_banks SET dirty_seq = dirty_seq + 1
			WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
		`, o.TenantID, o.PrincipalID, o.ProjectKey).Exec(ctx)
		if err != nil {
			return opErr("dirty note bank", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return opErr("dirty note bank rows affected", err)
		}
		if affected == 0 {
			// Belt and braces next to the bank lock: the bank row is gone, so
			// this note must not survive the erasure. The non-nil error rolls
			// the note INSERT back with the rest of the transaction.
			return proxymemory.ErrNotFound
		}
		return nil
	})
}

// lockOwnerBankTx reads the owner's bank row as the first bank-table statement
// of a transaction and, on PostgreSQL, locks that row for the rest of the
// transaction. AddNote calls it before everything else; Forget calls it after
// its session and extraction deletes, immediately before the notes and bank
// deletes that must dominate a concurrent AddNote (see purge.go Forget), so in
// both files no statement touches the bank table before this one. The two then
// serialize on the same row in one consistent order: whichever transaction
// locks first also finishes its notes/bank work first. A note therefore can
// neither be inserted into a bank a committed Forget has erased nor be missed
// by a Forget whose notes/bank deletes run before the note INSERT becomes
// visible: on PostgreSQL each statement of a READ COMMITTED transaction sees a
// fresh snapshot, and Forget's notes/bank deletes cannot pass this lock
// (Requirements 8.3, 8.5). Both directions take the same row, so the two
// cannot deadlock on it.
//
// SQLite keeps the nonlocking read: its engine serializes writers at the
// kernel level, so a second writer cannot commit between this read and the
// transaction's writes and the single-transaction shape is already safe there.
//
// The returned bool reports whether the row exists: AddNote maps false to
// ErrNotFound, while Forget proceeds without it so an owner whose bank is
// already gone still has leftover rows cleaned up.
func (s *Store) lockOwnerBankTx(ctx context.Context, tx bun.Tx, o proxymemory.Owner) (bool, error) {
	query := `
		SELECT 1 FROM proxy_memory_banks
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`
	if s.db.Dialect().Name() == dialect.PG {
		query += ` FOR UPDATE`
	}
	var one int
	err := tx.NewRaw(query, o.TenantID, o.PrincipalID, o.ProjectKey).Scan(ctx, &one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, opErr("lock owner bank", err)
	}
	return true, nil
}
