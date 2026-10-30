package workstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	dbinfra "github.com/aiproxer/aiproxer/internal/infra/db"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	_ "modernc.org/sqlite" // register sqlite driver for durable terminal-work stores
)

var (
	_ terminalworkapp.WorkStore     = (*DurableStore)(nil)
	_ terminalworkapp.RecoveryStore = (*DurableStore)(nil)
)

type DurableConfig struct {
	StoreID         string
	DefaultPageSize int
	MaxPageSize     int
	Now             func() time.Time
}

type DurableStore struct {
	cfg             DurableConfig
	db              *bun.DB
	dialect         dialect.Name
	defaultPageSize int
	maxPageSize     int
	now             func() time.Time
	nonOwning       bool
}

var RequiredMigrationNames = []string{BaselineMigrationName, RuntimeGenerationIDMigrationName, RuntimeInstanceIDMigrationName}

func Migrate(ctx context.Context, db *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("terminalwork/workstore: nil context")
	}
	if db == nil {
		return fmt.Errorf("terminalwork/workstore: nil bun db")
	}
	return runSchemaMigrate(ctx, db)
}

func VerifySchema(ctx context.Context, db *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("terminalwork/workstore: nil context")
	}
	if db == nil {
		return fmt.Errorf("terminalwork/workstore: nil bun db")
	}
	if db.Dialect().Name() != dialect.PG {
		for _, probe := range []string{
			`SELECT store_id, source_key, payload_version, claim_owner_id, runtime_generation_id, runtime_instance_id FROM economic_terminal_work WHERE 1 = 0`,
		} {
			if _, err := db.ExecContext(ctx, probe); err != nil {
				return fmt.Errorf("terminalwork/workstore: schema verification failed: %w", err)
			}
		}
		for _, name := range RequiredMigrationNames {
			var n int
			if err := db.NewRaw(
				`SELECT COUNT(1) FROM bun_terminal_work_migrations WHERE name = ?`, name,
			).Scan(ctx, &n); err != nil {
				return fmt.Errorf("terminalwork/workstore: schema verification failed: migration %s: %w", name, err)
			}
			if n < 1 {
				return fmt.Errorf("terminalwork/workstore: schema verification failed: missing migration %s", name)
			}
		}
		return nil
	}
	for _, probe := range []string{
		`SELECT * FROM economic_terminal_work WHERE 1 = 0`,
	} {
		if _, err := db.ExecContext(ctx, probe); err != nil {
			return fmt.Errorf("terminalwork/workstore: schema verification failed: %w", err)
		}
	}
	checks := []struct {
		description string
		query       string
		args        []any
		fragments   []string
	}{
		{
			description: "migration history",
			query:       `SELECT name FROM bun_terminal_work_migrations WHERE name = ? LIMIT 1`,
			args:        []any{BaselineMigrationName},
			fragments:   []string{BaselineMigrationName},
		},
		{
			description: "runtime_generation_id migration history",
			query:       `SELECT name FROM bun_terminal_work_migrations WHERE name = ? LIMIT 1`,
			args:        []any{RuntimeGenerationIDMigrationName},
			fragments:   []string{RuntimeGenerationIDMigrationName},
		},
		{
			description: "runtime_instance_id migration history",
			query:       `SELECT name FROM bun_terminal_work_migrations WHERE name = ? LIMIT 1`,
			args:        []any{RuntimeInstanceIDMigrationName},
			fragments:   []string{RuntimeInstanceIDMigrationName},
		},
		{
			description: "runtime_generation_id column",
			query: `SELECT lower(column_name) FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'economic_terminal_work'
  AND column_name = 'runtime_generation_id'
LIMIT 1`,
			fragments: []string{"runtime_generation_id"},
		},
		{
			description: "runtime_instance_id column",
			query: `SELECT lower(column_name) FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'economic_terminal_work'
  AND column_name = 'runtime_instance_id'
LIMIT 1`,
			fragments: []string{"runtime_instance_id"},
		},
		{
			description: "economic_terminal_work store-scoped source_key unique constraint",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'economic_terminal_work'
  AND c.contype = 'u'
  AND c.conname = 'economic_terminal_work_store_id_source_key_key'
LIMIT 1`,
			fragments: []string{"unique (store_id, source_key)"},
		},
		{
			description: "idx_terminal_work_due",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'economic_terminal_work'
  AND indexname = 'idx_terminal_work_due'
LIMIT 1`,
			fragments: []string{"(store_id, state, next_retry_at_unix, claim_expires_at_unix)"},
		},
		{
			description: "idx_terminal_work_provider",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'economic_terminal_work'
  AND indexname = 'idx_terminal_work_provider'
LIMIT 1`,
			fragments: []string{"(store_id, provider_id, state)"},
		},
		{
			description: "idx_terminal_work_request",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'economic_terminal_work'
  AND indexname = 'idx_terminal_work_request'
LIMIT 1`,
			fragments: []string{"(store_id, request_id)"},
		},
	}
	for _, check := range checks {
		if err := dbinfra.VerifyPostgresQueryRowContains(ctx, db, check.description, check.query, check.args, check.fragments...); err != nil {
			return fmt.Errorf("terminalwork/workstore: schema verification failed: %w", err)
		}
	}
	return nil
}

func NewDurableStore(ctx context.Context, db *bun.DB, cfg DurableConfig) (*DurableStore, error) {
	if err := Migrate(ctx, db); err != nil {
		return nil, fmt.Errorf("terminalwork/workstore: migrate: %w", err)
	}
	return openStore(ctx, db, cfg, false)
}

func OpenStore(ctx context.Context, db *bun.DB, cfg DurableConfig) (*DurableStore, error) {
	return openStore(ctx, db, cfg, true)
}

func openStore(ctx context.Context, db *bun.DB, cfg DurableConfig, nonOwning bool) (*DurableStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("terminalwork/workstore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("terminalwork/workstore: nil bun db")
	}
	if strings.TrimSpace(cfg.StoreID) == "" {
		return nil, fmt.Errorf("terminalwork/workstore: durable store id is required")
	}
	def := cfg.DefaultPageSize
	if def <= 0 {
		def = 100
	}
	maxPageSize := cfg.MaxPageSize
	if maxPageSize <= 0 {
		maxPageSize = 500
	}
	if maxPageSize < def {
		return nil, fmt.Errorf("terminalwork/workstore: max page size %d < default %d", maxPageSize, def)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &DurableStore{
		cfg:             cfg,
		db:              db,
		dialect:         db.Dialect().Name(),
		defaultPageSize: def,
		maxPageSize:     maxPageSize,
		now:             now,
		nonOwning:       nonOwning,
	}, nil
}

func (s *DurableStore) Close() error {
	if s == nil || s.db == nil || s.nonOwning {
		return nil
	}
	return s.db.Close()
}

func (s *DurableStore) CheckReadiness(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("terminalwork/workstore: nil store")
	}
	return s.db.PingContext(ctx)
}

func (s *DurableStore) AppendIntent(ctx context.Context, rec terminalwork.WorkRecord) error {
	_, err := s.AppendIntentOutcome(ctx, rec)
	return err
}

// AppendIntentOutcome implements the definitive insert-vs-replay seam.
func (s *DurableStore) AppendIntentOutcome(ctx context.Context, rec terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
	if err := ctx.Err(); err != nil {
		return terminalwork.AppendIntentOutcome{}, err
	}
	if err := rec.Validate(); err != nil {
		return terminalwork.AppendIntentOutcome{}, fmt.Errorf("terminalwork/workstore: %w", err)
	}
	if rec.State == "" {
		rec.State = sdk.WorkStateIntent
	}
	now := s.now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = now
	}
	cloned := cloneRecord(rec)
	row := recordToRow(s.cfg.StoreID, cloned)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return terminalwork.AppendIntentOutcome{}, fmt.Errorf("terminalwork/workstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, lerr := lookupBySourceKey(ctx, tx, s.cfg.StoreID, cloned.SourceKey.String())
	if lerr != nil {
		return terminalwork.AppendIntentOutcome{}, lerr
	}
	if found {
		return resolveExistingOutcome(existing, cloned)
	}
	existing, found, lerr = lookupByWorkID(ctx, tx, s.cfg.StoreID, cloned.WorkID)
	if lerr != nil {
		return terminalwork.AppendIntentOutcome{}, lerr
	}
	if found {
		return resolveExistingOutcome(existing, cloned)
	}

	_, err = tx.NewRaw(
		`
INSERT INTO economic_terminal_work(
  store_id, work_id, source_key, identity_version, payload_version, kind, state,
  provider_id, request_id, attempt_id, trace_id, generation_id, runtime_instance_id, runtime_generation_id, bound_provider_id,
  rating_id, fact_id, lease_set_id, payload_json, attempts, next_retry_at_unix,
  claim_owner_id, claim_expires_at_unix, error_code, error_permanent, error_message,
  created_at_unix, updated_at_unix
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		row.StoreID, row.WorkID, row.SourceKey, row.IdentityVersion, row.PayloadVersion,
		row.Kind, row.State, row.ProviderID, row.RequestID, row.AttemptID, row.TraceID,
		row.GenerationID, row.RuntimeInstanceID, row.RuntimeGenerationID, row.BoundProviderID, row.RatingID, row.FactID, row.LeaseSetID,
		row.PayloadJSON, row.Attempts, row.NextRetryAtUnix, row.ClaimOwnerID,
		row.ClaimExpiresAtUnix, row.ErrorCode, row.ErrorPermanent, row.ErrorMessage,
		row.CreatedAtUnix, row.UpdatedAtUnix,
	).Exec(ctx)
	if err != nil {
		if dbinfra.IsUniqueViolation(err) {
			_ = tx.Rollback()
			return s.resolveUniqueRaceOutcome(ctx, cloned)
		}
		return terminalwork.AppendIntentOutcome{}, fmt.Errorf("terminalwork/workstore: insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return terminalwork.AppendIntentOutcome{}, fmt.Errorf("terminalwork/workstore: commit: %w", err)
	}
	return terminalwork.AppendIntentOutcome{Inserted: true}, nil
}

func (s *DurableStore) resolveUniqueRace(ctx context.Context, cloned terminalwork.WorkRecord) error {
	_, err := s.resolveUniqueRaceOutcome(ctx, cloned)
	return err
}

func (s *DurableStore) resolveUniqueRaceOutcome(ctx context.Context, cloned terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
	existing, found, err := lookupBySourceKey(ctx, s.db, s.cfg.StoreID, cloned.SourceKey.String())
	if err != nil {
		return terminalwork.AppendIntentOutcome{}, err
	}
	if !found {
		existing, found, err = lookupByWorkID(ctx, s.db, s.cfg.StoreID, cloned.WorkID)
		if err != nil {
			return terminalwork.AppendIntentOutcome{}, err
		}
	}
	if !found {
		return terminalwork.AppendIntentOutcome{}, ErrUniqueRaceMissingRow
	}
	return resolveExistingOutcome(existing, cloned)
}

func (s *DurableStore) GetByWorkID(ctx context.Context, workID string) (terminalwork.WorkRecord, error) {
	rec, found, err := s.LookupIntent(ctx, workID)
	if err != nil {
		return terminalwork.WorkRecord{}, err
	}
	if !found {
		return terminalwork.WorkRecord{}, ErrNotFound
	}
	return rec, nil
}

// LookupIntent implements terminalworkapp.IntentLookup.
func (s *DurableStore) LookupIntent(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
	return lookupByWorkID(ctx, s.db, s.cfg.StoreID, workID)
}

func (s *DurableStore) GetBySourceKey(ctx context.Context, key terminalwork.SourceKey) (terminalwork.WorkRecord, error) {
	if err := key.Validate(); err != nil {
		return terminalwork.WorkRecord{}, fmt.Errorf("terminalwork/workstore: %w", err)
	}
	rec, found, err := lookupBySourceKey(ctx, s.db, s.cfg.StoreID, key.String())
	if err != nil {
		return terminalwork.WorkRecord{}, err
	}
	if !found {
		return terminalwork.WorkRecord{}, ErrNotFound
	}
	return rec, nil
}

func (s *DurableStore) PromotePending(ctx context.Context, cmd PromotePendingCommand) error {
	now := cmd.Now
	if now.IsZero() {
		now = s.now().UTC()
	}
	res, err := s.db.NewRaw(
		`
UPDATE economic_terminal_work
SET state = ?, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND state = ?`,
		string(sdk.WorkStatePending), timeUnixNano(now),
		s.cfg.StoreID, cmd.WorkID, string(sdk.WorkStateIntent),
	).Exec(ctx)
	if err != nil {
		return fmt.Errorf("terminalwork/workstore: promote pending: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		rec, err := s.GetByWorkID(ctx, cmd.WorkID)
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if rec.State == sdk.WorkStatePending {
			return nil
		}
		return ErrConflict
	}
	return nil
}

func (s *DurableStore) ClaimDue(ctx context.Context, cmd ClaimDueCommand) ([]terminalwork.WorkRecord, error) {
	if err := normalizeClaimDueCommand(&cmd, s.now); err != nil {
		return nil, err
	}
	now := cmd.Now
	limit := cmd.Limit
	expires := now.Add(cmd.TTL)

	if s.dialect == dialect.PG {
		return s.claimDuePostgres(ctx, cmd, now, expires, limit)
	}
	return s.claimDueSQLite(ctx, cmd, now, expires, limit)
}

func (s *DurableStore) claimDuePostgres(ctx context.Context, cmd ClaimDueCommand, now, expires time.Time, limit int) ([]terminalwork.WorkRecord, error) {
	providerFilter := ""
	args := []any{s.cfg.StoreID, timeUnixNano(now), timeUnixNano(now)}
	if provider := strings.TrimSpace(cmd.ProviderID); provider != "" {
		providerFilter = " AND provider_id = ?"
		args = append(args, provider)
	}
	kindFilter := ""
	if cmd.Kind != "" {
		kindFilter = " AND kind = ?"
		args = append(args, string(cmd.Kind))
	}
	args = append(args, limit, cmd.OwnerID, timeUnixNano(expires), timeUnixNano(now), s.cfg.StoreID)
	query := fmt.Sprintf(`
WITH picked AS (
  SELECT work_id FROM economic_terminal_work
  WHERE store_id = ?
    AND (
      state = 'pending'
      OR (state = 'retry' AND next_retry_at_unix <= ?)
      OR (state = 'claimed' AND claim_expires_at_unix <= ?)
    )%s%s
  ORDER BY created_at_unix ASC, work_id ASC
  LIMIT ?
  FOR UPDATE SKIP LOCKED
)
UPDATE economic_terminal_work AS w
SET state = 'claimed',
    claim_owner_id = ?,
    claim_expires_at_unix = ?,
    updated_at_unix = ?
FROM picked
WHERE w.store_id = ? AND w.work_id = picked.work_id
RETURNING w.store_id, w.work_id, w.source_key, w.identity_version, w.payload_version,
  w.kind, w.state, w.provider_id, w.request_id, w.attempt_id, w.trace_id,
  w.generation_id, w.runtime_instance_id, w.runtime_generation_id, w.bound_provider_id, w.rating_id, w.fact_id, w.lease_set_id,
  w.payload_json, w.attempts, w.next_retry_at_unix, w.claim_owner_id,
  w.claim_expires_at_unix, w.error_code, w.error_permanent, w.error_message,
  w.created_at_unix, w.updated_at_unix
`, providerFilter, kindFilter)

	var rows []workRow
	if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("terminalwork/workstore: claim due: %w", err)
	}
	out := make([]terminalwork.WorkRecord, 0, len(rows))
	for _, row := range rows {
		rec, err := rowToRecord(row)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

func (s *DurableStore) claimDueSQLite(ctx context.Context, cmd ClaimDueCommand, now, expires time.Time, limit int) ([]terminalwork.WorkRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	where := []string{
		"store_id = ?",
		`(state = 'pending' OR (state = 'retry' AND next_retry_at_unix <= ?) OR (state = 'claimed' AND claim_expires_at_unix <= ?))`,
	}
	args := []any{s.cfg.StoreID, timeUnixNano(now), timeUnixNano(now)}
	if provider := strings.TrimSpace(cmd.ProviderID); provider != "" {
		where = append(where, "provider_id = ?")
		args = append(args, provider)
	}
	if cmd.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, string(cmd.Kind))
	}
	query := "SELECT * FROM economic_terminal_work WHERE " + strings.Join(where, " AND ") + " ORDER BY created_at_unix ASC, work_id ASC"
	var candidates []workRow
	if err := tx.NewRaw(query, args...).Scan(ctx, &candidates); err != nil {
		return nil, err
	}

	out := make([]terminalwork.WorkRecord, 0, limit)
	for _, row := range candidates {
		if len(out) >= limit {
			break
		}
		rec, err := rowToRecord(row)
		if err != nil {
			return nil, err
		}
		if !isDueForClaim(rec, now) {
			continue
		}
		item := rec.ToWorkItem()
		if err := item.Claim(cmd.OwnerID, cmd.TTL, fixedClock{now}); err != nil {
			continue
		}
		rec.ApplyWorkItem(item, now)
		updated := recordToRow(s.cfg.StoreID, rec)
		res, err := tx.NewRaw(
			`
UPDATE economic_terminal_work
SET state = ?, claim_owner_id = ?, claim_expires_at_unix = ?, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND state = ? AND claim_owner_id = ? AND claim_expires_at_unix = ?`,
			updated.State, updated.ClaimOwnerID, updated.ClaimExpiresAtUnix, updated.UpdatedAtUnix,
			updated.StoreID, updated.WorkID, row.State, row.ClaimOwnerID, row.ClaimExpiresAtUnix,
		).Exec(ctx)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			continue
		}
		out = append(out, rec)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *DurableStore) RenewClaim(ctx context.Context, cmd RenewClaimCommand) error {
	if err := normalizeRenewClaimCommand(&cmd, s.now); err != nil {
		return err
	}
	now := cmd.Now
	expires := now.Add(cmd.TTL)
	res, err := s.db.NewRaw(
		`
UPDATE economic_terminal_work
SET claim_expires_at_unix = ?, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND state = ? AND claim_owner_id = ? AND claim_expires_at_unix > ?`,
		timeUnixNano(expires), timeUnixNano(now),
		s.cfg.StoreID, cmd.WorkID, string(sdk.WorkStateClaimed), cmd.OwnerID, timeUnixNano(now),
	).Exec(ctx)
	if err != nil {
		return fmt.Errorf("terminalwork/workstore: renew claim: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return s.classifyRenewClaimMiss(ctx, cmd, now)
	}
	return nil
}

func (s *DurableStore) classifyRenewClaimMiss(ctx context.Context, cmd RenewClaimCommand, now time.Time) error {
	rec, err := s.GetByWorkID(ctx, cmd.WorkID)
	if err != nil {
		return err
	}
	return rec.ToWorkItem().RenewClaim(cmd.OwnerID, cmd.TTL, fixedClock{now})
}

func (s *DurableStore) Complete(ctx context.Context, cmd CompleteCommand) error {
	now := cmd.Now
	if now.IsZero() {
		now = s.now().UTC()
	}
	res, err := s.db.NewRaw(
		`
UPDATE economic_terminal_work
SET state = ?, claim_owner_id = ?, claim_expires_at_unix = ?, error_code = ?, error_permanent = ?, error_message = ?, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND state = ? AND claim_owner_id = ?`,
		string(sdk.WorkStateCompleted), "", 0, "", false, "", timeUnixNano(now),
		s.cfg.StoreID, cmd.WorkID, string(sdk.WorkStateClaimed), cmd.ExpectedOwnerID,
	).Exec(ctx)
	if err != nil {
		return fmt.Errorf("terminalwork/workstore: complete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrConflict
	}
	return nil
}

func (s *DurableStore) ScheduleRetry(ctx context.Context, cmd ScheduleRetryCommand) error {
	if cmd.Err.Permanent {
		return sdk.ErrPermanent
	}
	now := cmd.Now
	if now.IsZero() {
		now = s.now().UTC()
	}
	rec, err := s.GetByWorkID(ctx, cmd.WorkID)
	if err != nil {
		return err
	}
	if rec.State != sdk.WorkStateClaimed || rec.Lease.OwnerID != cmd.ExpectedOwnerID {
		return ErrConflict
	}
	item := rec.ToWorkItem()
	if err := item.Retry(cmd.Schedule, fixedClock{now}, cmd.Err); err != nil {
		return err
	}
	rec.ApplyWorkItem(item, now)
	row := recordToRow(s.cfg.StoreID, rec)
	res, err := s.db.NewRaw(
		`
UPDATE economic_terminal_work
SET state = ?, attempts = ?, next_retry_at_unix = ?, claim_owner_id = ?, claim_expires_at_unix = ?,
  error_code = ?, error_permanent = ?, error_message = ?, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND state = ? AND claim_owner_id = ?`,
		row.State, row.Attempts, row.NextRetryAtUnix, row.ClaimOwnerID, row.ClaimExpiresAtUnix,
		row.ErrorCode, row.ErrorPermanent, row.ErrorMessage, row.UpdatedAtUnix,
		s.cfg.StoreID, cmd.WorkID, string(sdk.WorkStateClaimed), cmd.ExpectedOwnerID,
	).Exec(ctx)
	if err != nil {
		return fmt.Errorf("terminalwork/workstore: schedule retry: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrConflict
	}
	return nil
}

func (s *DurableStore) Quarantine(ctx context.Context, cmd QuarantineCommand) error {
	now := cmd.Now
	if now.IsZero() {
		now = s.now().UTC()
	}
	rec, err := s.GetByWorkID(ctx, cmd.WorkID)
	if err != nil {
		return err
	}
	item := rec.ToWorkItem()
	if err := item.Quarantine(cmd.Err); err != nil {
		return err
	}
	rec.ApplyWorkItem(item, now)
	row := recordToRow(s.cfg.StoreID, rec)
	res, err := s.db.NewRaw(
		`
UPDATE economic_terminal_work
SET state = ?, claim_owner_id = ?, claim_expires_at_unix = ?, error_code = ?, error_permanent = ?, error_message = ?, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND state IN (?, ?, ?)`,
		row.State, row.ClaimOwnerID, row.ClaimExpiresAtUnix, row.ErrorCode, row.ErrorPermanent, row.ErrorMessage, row.UpdatedAtUnix,
		s.cfg.StoreID, cmd.WorkID, string(sdk.WorkStatePending), string(sdk.WorkStateClaimed), string(sdk.WorkStateRetry),
	).Exec(ctx)
	if err != nil {
		return fmt.Errorf("terminalwork/workstore: quarantine: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if rec.State == sdk.WorkStateQuarantined {
			return nil
		}
		return ErrConflict
	}
	return nil
}

func (s *DurableStore) List(ctx context.Context, q Query) (Page, error) {
	if err := ValidateQuery(q); err != nil {
		return Page{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = s.defaultPageSize
	}
	if limit > s.maxPageSize {
		limit = s.maxPageSize
	}
	offset := 0
	if cur := strings.TrimSpace(q.Cursor); cur != "" {
		n, err := strconv.Atoi(cur)
		if err != nil || n < 0 {
			return Page{}, fmt.Errorf("terminalwork/workstore: invalid cursor")
		}
		offset = n
	}
	query, args := buildListQuery(s.cfg.StoreID, q, limit+1, offset)
	var rows []workRow
	if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil {
		return Page{}, fmt.Errorf("terminalwork/workstore: list: %w", err)
	}
	recs := make([]terminalwork.WorkRecord, 0, len(rows))
	for _, row := range rows {
		rec, err := rowToRecord(row)
		if err != nil {
			return Page{}, err
		}
		recs = append(recs, rec)
	}
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].CreatedAt.Equal(recs[j].CreatedAt) {
			return recs[i].WorkID < recs[j].WorkID
		}
		return recs[i].CreatedAt.Before(recs[j].CreatedAt)
	})
	page := Page{}
	if len(recs) > limit {
		page.Records = recs[:limit]
		page.Cursor = strconv.Itoa(offset + limit)
	} else {
		page.Records = recs
	}
	return page, nil
}

func lookupByWorkID(ctx context.Context, q bun.IDB, storeID, workID string) (terminalwork.WorkRecord, bool, error) {
	var row workRow
	err := q.NewRaw(
		`
SELECT store_id, work_id, source_key, identity_version, payload_version, kind, state,
  provider_id, request_id, attempt_id, trace_id, generation_id, runtime_instance_id, runtime_generation_id, bound_provider_id,
  rating_id, fact_id, lease_set_id, payload_json, attempts, next_retry_at_unix,
  claim_owner_id, claim_expires_at_unix, error_code, error_permanent, error_message,
  created_at_unix, updated_at_unix
FROM economic_terminal_work
WHERE store_id = ? AND work_id = ?`,
		storeID, workID,
	).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return terminalwork.WorkRecord{}, false, nil
	}
	if err != nil {
		return terminalwork.WorkRecord{}, false, fmt.Errorf("terminalwork/workstore: lookup work: %w", err)
	}
	rec, err := rowToRecord(row)
	if err != nil {
		return terminalwork.WorkRecord{}, false, err
	}
	return rec, true, nil
}

func lookupBySourceKey(ctx context.Context, q bun.IDB, storeID, sourceKey string) (terminalwork.WorkRecord, bool, error) {
	var row workRow
	err := q.NewRaw(
		`
SELECT store_id, work_id, source_key, identity_version, payload_version, kind, state,
  provider_id, request_id, attempt_id, trace_id, generation_id, runtime_instance_id, runtime_generation_id, bound_provider_id,
  rating_id, fact_id, lease_set_id, payload_json, attempts, next_retry_at_unix,
  claim_owner_id, claim_expires_at_unix, error_code, error_permanent, error_message,
  created_at_unix, updated_at_unix
FROM economic_terminal_work
WHERE store_id = ? AND source_key = ?`,
		storeID, sourceKey,
	).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return terminalwork.WorkRecord{}, false, nil
	}
	if err != nil {
		return terminalwork.WorkRecord{}, false, fmt.Errorf("terminalwork/workstore: lookup source: %w", err)
	}
	rec, err := rowToRecord(row)
	if err != nil {
		return terminalwork.WorkRecord{}, false, err
	}
	return rec, true, nil
}
