package leasestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	dbinfra "github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	_ "modernc.org/sqlite" // register sqlite driver for durable lease stores
)

// DurableConfig configures a Bun-backed lease store (SQLite or PostgreSQL).
type DurableConfig struct {
	StoreID         string
	DefaultPageSize int
	MaxPageSize     int
}

// DurableStore persists leases via Bun. SQLite reports single-node limits;
// PostgreSQL is the distributed strict reference (requirement 16.7).
type DurableStore struct {
	cfg             DurableConfig
	db              *bun.DB
	dialect         dialect.Name
	defaultPageSize int
	maxPageSize     int
	nonOwning       bool
}

// Migrate applies the lease-store schema on an admin connection.
func Migrate(ctx context.Context, db *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("leasestore: nil context")
	}
	if db == nil {
		return fmt.Errorf("leasestore: nil bun db")
	}
	return runSchemaMigrate(ctx, db)
}

// VerifySchema checks required runtime relations without applying migrations.
func VerifySchema(ctx context.Context, db *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("leasestore: nil context")
	}
	if db == nil {
		return fmt.Errorf("leasestore: nil bun db")
	}
	if db.Dialect().Name() != dialect.PG {
		for _, probe := range []string{
			`SELECT 1 FROM concurrency_leases WHERE 1 = 0`,
			`SELECT 1 FROM concurrency_lease_capacity WHERE 1 = 0`,
		} {
			if _, err := db.ExecContext(ctx, probe); err != nil {
				return fmt.Errorf("leasestore: schema verification failed: %w", err)
			}
		}
		return nil
	}
	for _, probe := range []string{
		`SELECT * FROM concurrency_leases WHERE 1 = 0`,
		`SELECT * FROM concurrency_lease_capacity WHERE 1 = 0`,
	} {
		if _, err := db.ExecContext(ctx, probe); err != nil {
			return fmt.Errorf("leasestore: schema verification failed: %w", err)
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
			query:       `SELECT name FROM bun_concurrency_lease_migrations WHERE name = ? LIMIT 1`,
			args:        []any{BaselineMigrationName},
			fragments:   []string{BaselineMigrationName},
		},
		{
			description: "concurrency_leases primary key",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'concurrency_leases'
  AND c.contype = 'p'
LIMIT 1`,
			fragments: []string{"primary key (store_id, lease_id)"},
		},
		{
			description: "concurrency_lease_capacity primary key",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'concurrency_lease_capacity'
  AND c.contype = 'p'
LIMIT 1`,
			fragments: []string{"primary key (store_id, rule_id, dimension_key)"},
		},
		{
			description: "idx_concurrency_leases_capacity",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'concurrency_leases'
  AND indexname = 'idx_concurrency_leases_capacity'
LIMIT 1`,
			fragments: []string{"(store_id, rule_id, dimension_key, state, expires_at_unix)"},
		},
	}
	for _, check := range checks {
		if err := dbinfra.VerifyPostgresQueryRowContains(ctx, db, check.description, check.query, check.args, check.fragments...); err != nil {
			return fmt.Errorf("leasestore: schema verification failed: %w", err)
		}
	}
	return nil
}

// NewDurable migrates schema and returns a durable lease store. Caller owns
// closing via DurableStore.Close.
func NewDurable(ctx context.Context, db *bun.DB, cfg DurableConfig) (*DurableStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("leasestore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("leasestore: nil bun db")
	}
	if strings.TrimSpace(cfg.StoreID) == "" {
		return nil, fmt.Errorf("leasestore: durable store id is required")
	}
	if err := runSchemaMigrate(ctx, db); err != nil {
		return nil, fmt.Errorf("leasestore: migrate: %w", err)
	}
	return openStore(ctx, db, cfg, false)
}

// OpenStore opens a lease store without migrations and without taking
// ownership of db. The composition root owns the shared runtime pool.
func OpenStore(ctx context.Context, db *bun.DB, cfg DurableConfig) (*DurableStore, error) {
	return openStore(ctx, db, cfg, true)
}

func openStore(ctx context.Context, db *bun.DB, cfg DurableConfig, nonOwning bool) (*DurableStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("leasestore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("leasestore: nil bun db")
	}
	if strings.TrimSpace(cfg.StoreID) == "" {
		return nil, fmt.Errorf("leasestore: durable store id is required")
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
		return nil, fmt.Errorf("leasestore: max page size %d < default %d", maxPageSize, def)
	}
	return &DurableStore{
		cfg:             cfg,
		db:              db,
		dialect:         db.Dialect().Name(),
		defaultPageSize: def,
		maxPageSize:     maxPageSize,
		nonOwning:       nonOwning,
	}, nil
}

// Close closes the underlying bun DB handle.
func (s *DurableStore) Close() error {
	if s == nil || s.db == nil || s.nonOwning {
		return nil
	}
	return s.db.Close()
}

// CheckReadiness pings the database and reports dialect-specific posture.
func (s *DurableStore) CheckReadiness(ctx context.Context) (domain.Readiness, error) {
	if s == nil || s.db == nil {
		return domain.Readiness{
			State:  domain.ReadinessStateUnavailable,
			Reason: "durable lease store is nil",
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return domain.Readiness{}, err
	}
	if err := s.db.PingContext(ctx); err != nil {
		return domain.Readiness{
			State:  domain.ReadinessStateUnavailable,
			Reason: "durable lease store ping failed",
		}, nil
	}
	switch s.dialect {
	case dialect.SQLite:
		return domain.Readiness{
			State:  domain.ReadinessStateReady,
			Reason: "sqlite: single-node serialized writers; not multi-instance distributed strict",
		}, nil
	case dialect.PG:
		return domain.Readiness{
			State:  domain.ReadinessStateReady,
			Reason: "postgres: distributed strict reference backing",
		}, nil
	default:
		return domain.Readiness{
			State:  domain.ReadinessStateReady,
			Reason: "durable lease store ready",
		}, nil
	}
}

type leaseRow struct {
	StoreID         string `bun:"store_id"`
	LeaseID         string `bun:"lease_id"`
	RuleID          string `bun:"rule_id"`
	RuleVersion     string `bun:"rule_version"`
	Namespace       string `bun:"namespace"`
	DimensionKey    string `bun:"dimension_key"`
	LogicalID       string `bun:"logical_id"`
	HolderID        string `bun:"holder_id"`
	AcquiredAtUnix  int64  `bun:"acquired_at_unix"`
	RenewedAtUnix   int64  `bun:"renewed_at_unix"`
	ExpiresAtUnix   int64  `bun:"expires_at_unix"`
	ReleasedAtUnix  int64  `bun:"released_at_unix"`
	Generation      int64  `bun:"generation"`
	State           string `bun:"state"`
	DimensionsJSON  string `bun:"dimensions_json"`
	IdentityVersion int    `bun:"identity_version"`
	SetID           string `bun:"set_id"`
	SetGeneration   int64  `bun:"set_generation"`
	SetState        string `bun:"set_state"`
}

func leaseToRow(storeID string, lease domain.Lease, dimKey string) (leaseRow, error) {
	raw, err := json.Marshal(lease.Dimensions)
	if err != nil {
		return leaseRow{}, err
	}
	if dimKey == "" {
		dimKey = string(lease.Dimensions.Key())
	}
	identityVersion := lease.IdentityVersion
	if identityVersion == 0 {
		identityVersion = 1
	}
	return leaseRow{
		StoreID:         storeID,
		LeaseID:         lease.LeaseID,
		RuleID:          lease.RuleID,
		RuleVersion:     lease.RuleVersion,
		Namespace:       lease.Namespace,
		DimensionKey:    dimKey,
		LogicalID:       lease.LogicalID,
		HolderID:        lease.HolderID,
		AcquiredAtUnix:  lease.AcquiredAt.UnixNano(),
		RenewedAtUnix:   lease.RenewedAt.UnixNano(),
		ExpiresAtUnix:   lease.ExpiresAt.UnixNano(),
		ReleasedAtUnix:  lease.ReleasedAt.UnixNano(),
		Generation:      lease.Generation,
		State:           string(lease.State),
		DimensionsJSON:  string(raw),
		IdentityVersion: identityVersion,
		SetID:           lease.SetID,
		SetGeneration:   lease.SetGeneration,
		SetState:        string(lease.SetState),
	}, nil
}

func rowToLease(row leaseRow) (domain.Lease, error) {
	var dims domain.Dimensions
	if row.DimensionsJSON != "" {
		if err := json.Unmarshal([]byte(row.DimensionsJSON), &dims); err != nil {
			return domain.Lease{}, err
		}
	}
	lease := domain.Lease{
		LeaseID:         row.LeaseID,
		RuleID:          row.RuleID,
		RuleVersion:     row.RuleVersion,
		Namespace:       row.Namespace,
		Dimensions:      dims,
		LogicalID:       row.LogicalID,
		HolderID:        row.HolderID,
		AcquiredAt:      time.Unix(0, row.AcquiredAtUnix).UTC(),
		RenewedAt:       time.Unix(0, row.RenewedAtUnix).UTC(),
		ExpiresAt:       time.Unix(0, row.ExpiresAtUnix).UTC(),
		Generation:      row.Generation,
		State:           domain.LeaseState(row.State),
		IdentityVersion: row.IdentityVersion,
		SetID:           row.SetID,
		SetGeneration:   row.SetGeneration,
		SetState:        domain.LeaseSetState(row.SetState),
	}
	if row.ReleasedAtUnix != 0 {
		lease.ReleasedAt = time.Unix(0, row.ReleasedAtUnix).UTC()
	}
	if lease.IdentityVersion == 0 && lease.SetID == "" {
		// Legacy one-member set compatibility (requirement 11.4).
		lease.IdentityVersion = 1
		lease.SetID = lease.LeaseID
		lease.SetGeneration = lease.Generation
		if lease.State == domain.LeaseStateReleased {
			lease.SetState = domain.LeaseSetStateReleased
		} else {
			lease.SetState = domain.LeaseSetStateActive
		}
	}
	return lease, nil
}

// Acquire inserts or replays under capacity with transactional inline reclaim.
func (s *DurableStore) Acquire(ctx context.Context, cmd app.AcquireCommand) (app.AcquireResult, error) {
	if s == nil || s.db == nil {
		return app.AcquireResult{}, fmt.Errorf("leasestore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return app.AcquireResult{}, err
	}
	if strings.TrimSpace(cmd.Lease.LeaseID) == "" || cmd.Limit <= 0 {
		return app.AcquireResult{}, fmt.Errorf("leasestore: invalid acquire command")
	}
	dimKey := string(cmd.Dimensions.Key())
	if dimKey == "" {
		dimKey = string(cmd.Lease.Dimensions.Key())
	}
	nowUnix := cmd.Now.UnixNano()

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return app.AcquireResult{}, fmt.Errorf("leasestore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.lockCapacity(ctx, tx, cmd.RuleID, dimKey); err != nil {
		return app.AcquireResult{}, err
	}
	if err := s.reclaimExpired(ctx, tx, cmd.RuleID, dimKey, nowUnix, cmd.Now); err != nil {
		return app.AcquireResult{}, err
	}

	existing, found, err := s.loadLeaseTx(ctx, tx, cmd.Lease.LeaseID)
	if err != nil {
		return app.AcquireResult{}, err
	}
	if found && existing.IsLive(cmd.Now) {
		live, err := s.countLiveTx(ctx, tx, cmd.RuleID, dimKey, nowUnix)
		if err != nil {
			return app.AcquireResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return app.AcquireResult{}, fmt.Errorf("leasestore: commit: %w", err)
		}
		left := max(cmd.Limit-live, 0)
		return app.AcquireResult{Lease: existing, Replayed: true, RemainingSlots: left}, nil
	}

	live, err := s.countLiveTx(ctx, tx, cmd.RuleID, dimKey, nowUnix)
	if err != nil {
		return app.AcquireResult{}, err
	}
	if live >= cmd.Limit {
		if err := tx.Commit(); err != nil {
			return app.AcquireResult{}, fmt.Errorf("leasestore: commit: %w", err)
		}
		return app.AcquireResult{CapacityExceeded: true, RemainingSlots: 0}, nil
	}

	lease := cmd.Lease
	if lease.State == "" {
		lease.State = domain.LeaseStateActive
	}
	row, err := leaseToRow(s.cfg.StoreID, lease, dimKey)
	if err != nil {
		return app.AcquireResult{}, err
	}
	if found {
		if _, err := tx.NewRaw(
			`
UPDATE concurrency_leases SET
	rule_id=?, rule_version=?, namespace=?, dimension_key=?, logical_id=?, holder_id=?,
	acquired_at_unix=?, renewed_at_unix=?, expires_at_unix=?, released_at_unix=?,
	generation=?, state=?, dimensions_json=?
WHERE store_id=? AND lease_id=?
`,
			row.RuleID, row.RuleVersion, row.Namespace, row.DimensionKey, row.LogicalID, row.HolderID,
			row.AcquiredAtUnix, row.RenewedAtUnix, row.ExpiresAtUnix, row.ReleasedAtUnix,
			row.Generation, row.State, row.DimensionsJSON,
			s.cfg.StoreID, row.LeaseID,
		).Exec(ctx); err != nil {
			return app.AcquireResult{}, fmt.Errorf("leasestore: update lease: %w", err)
		}
	} else {
		if _, err := tx.NewRaw(
			`
INSERT INTO concurrency_leases(
	store_id, lease_id, rule_id, rule_version, namespace, dimension_key,
	logical_id, holder_id, acquired_at_unix, renewed_at_unix, expires_at_unix,
	released_at_unix, generation, state, dimensions_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`,
			row.StoreID, row.LeaseID, row.RuleID, row.RuleVersion, row.Namespace, row.DimensionKey,
			row.LogicalID, row.HolderID, row.AcquiredAtUnix, row.RenewedAtUnix, row.ExpiresAtUnix,
			row.ReleasedAtUnix, row.Generation, row.State, row.DimensionsJSON,
		).Exec(ctx); err != nil {
			return app.AcquireResult{}, fmt.Errorf("leasestore: insert lease: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return app.AcquireResult{}, fmt.Errorf("leasestore: commit: %w", err)
	}
	return app.AcquireResult{
		Lease:          lease,
		RemainingSlots: cmd.Limit - live - 1,
	}, nil
}

func (s *DurableStore) lockCapacity(ctx context.Context, tx bun.Tx, ruleID, dimKey string) error {
	if _, err := tx.NewRaw(`
INSERT INTO concurrency_lease_capacity(store_id, rule_id, dimension_key)
VALUES (?,?,?)
ON CONFLICT DO NOTHING
`, s.cfg.StoreID, ruleID, dimKey).Exec(ctx); err != nil {
		return fmt.Errorf("leasestore: ensure capacity row: %w", err)
	}
	if s.dialect == dialect.PG {
		var ignore string
		if err := tx.NewRaw(`
SELECT rule_id FROM concurrency_lease_capacity
WHERE store_id=? AND rule_id=? AND dimension_key=?
FOR UPDATE
`, s.cfg.StoreID, ruleID, dimKey).Scan(ctx, &ignore); err != nil {
			return fmt.Errorf("leasestore: lock capacity: %w", err)
		}
	}
	return nil
}

// reclaimExpired marks expired live leases for one capacity key. Bounded by
// (store_id, rule_id, dimension_key, state, expires_at) index predicates.
func (s *DurableStore) reclaimExpired(ctx context.Context, tx bun.Tx, ruleID, dimKey string, nowUnix int64, _ time.Time) error {
	_, err := tx.NewRaw(
		`
UPDATE concurrency_leases
SET state=?
WHERE store_id=? AND rule_id=? AND dimension_key=?
  AND state IN (?, ?)
  AND expires_at_unix <= ?
  AND COALESCE(set_state, '') != ?
`,
		string(domain.LeaseStateExpired),
		s.cfg.StoreID, ruleID, dimKey,
		string(domain.LeaseStateActive), string(domain.LeaseStateExpiring),
		nowUnix,
		string(domain.LeaseSetStateUncertain),
	).Exec(ctx)
	if err != nil {
		return fmt.Errorf("leasestore: reclaim: %w", err)
	}
	return nil
}

func (s *DurableStore) countLiveTx(ctx context.Context, tx bun.Tx, ruleID, dimKey string, nowUnix int64) (int, error) {
	return s.countLiveTxExcluding(ctx, tx, ruleID, dimKey, nowUnix, nil)
}

func (s *DurableStore) countLiveTxExcluding(ctx context.Context, tx bun.Tx, ruleID, dimKey string, nowUnix int64, exclude []string) (int, error) {
	var n int
	var q strings.Builder
	q.WriteString(`
SELECT COUNT(*) FROM concurrency_leases
WHERE store_id=? AND rule_id=? AND dimension_key=?
  AND state IN (?, ?)
  AND (expires_at_unix > ? OR COALESCE(set_state, '') = ?)
`)
	args := []any{
		s.cfg.StoreID, ruleID, dimKey,
		string(domain.LeaseStateActive), string(domain.LeaseStateExpiring),
		nowUnix,
		string(domain.LeaseSetStateUncertain),
	}
	if len(exclude) > 0 {
		q.WriteString(` AND lease_id NOT IN (`)
		for i, id := range exclude {
			if i > 0 {
				q.WriteString(`,`)
			}
			q.WriteString(`?`)
			args = append(args, id)
		}
		q.WriteString(`)`)
	}
	err := tx.NewRaw(q.String(), args...).Scan(ctx, &n)
	if err != nil {
		return 0, fmt.Errorf("leasestore: count live: %w", err)
	}
	return n, nil
}

func (s *DurableStore) loadLeaseTx(ctx context.Context, tx bun.Tx, leaseID string) (domain.Lease, bool, error) {
	var row leaseRow
	err := tx.NewRaw(`
SELECT store_id, lease_id, rule_id, rule_version, namespace, dimension_key,
	logical_id, holder_id, acquired_at_unix, renewed_at_unix, expires_at_unix,
	released_at_unix, generation, state, dimensions_json
FROM concurrency_leases WHERE store_id=? AND lease_id=?
`, s.cfg.StoreID, leaseID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Lease{}, false, nil
	}
	if err != nil {
		return domain.Lease{}, false, fmt.Errorf("leasestore: load lease: %w", err)
	}
	lease, err := rowToLease(row)
	if err != nil {
		return domain.Lease{}, false, err
	}
	return lease, true, nil
}

// Renew extends a lease with generation CAS.
func (s *DurableStore) Renew(ctx context.Context, cmd app.RenewCommand) (app.RenewResult, error) {
	if s == nil || s.db == nil {
		return app.RenewResult{}, fmt.Errorf("leasestore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return app.RenewResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return app.RenewResult{}, fmt.Errorf("leasestore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	lease, found, err := s.loadLeaseTx(ctx, tx, cmd.LeaseID)
	if err != nil {
		return app.RenewResult{}, err
	}
	if !found {
		return app.RenewResult{}, app.ErrNotFound
	}
	if err := lease.Renew(cmd.Now, cmd.ExpectedGeneration, cmd.TTL); err != nil {
		return app.RenewResult{}, err
	}
	row, err := leaseToRow(s.cfg.StoreID, lease, string(lease.Dimensions.Key()))
	if err != nil {
		return app.RenewResult{}, err
	}
	affected, err := s.renewCASUpdate(ctx, tx, cmd.LeaseID, cmd.ExpectedGeneration, row)
	if err != nil {
		return app.RenewResult{}, err
	}
	if affected == 0 {
		return app.RenewResult{}, s.renewCASMissError(ctx, tx, cmd)
	}
	if err := tx.Commit(); err != nil {
		return app.RenewResult{}, fmt.Errorf("leasestore: commit: %w", err)
	}
	return app.RenewResult{Lease: lease}, nil
}

// renewCASUpdate applies the Renew write under optimistic concurrency.
// The preimage requires an active/expiring row so Release (same generation)
// cannot be resurrected (requirements 10.7, 10.8, 16.2).
// Callers must pass the preimage generation (ExpectedGeneration), not row.Generation.
func (s *DurableStore) renewCASUpdate(ctx context.Context, tx bun.Tx, leaseID string, expectedGen int64, row leaseRow) (int64, error) {
	res, err := tx.NewRaw(
		`
UPDATE concurrency_leases SET
	renewed_at_unix=?, expires_at_unix=?, generation=?, state=?
WHERE store_id=? AND lease_id=? AND generation=? AND state IN (?, ?)
`,
		row.RenewedAtUnix, row.ExpiresAtUnix, row.Generation, row.State,
		s.cfg.StoreID, leaseID, expectedGen,
		string(domain.LeaseStateActive), string(domain.LeaseStateExpiring),
	).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("leasestore: renew: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("leasestore: renew rows affected: %w", err)
	}
	return affected, nil
}

// renewCASMissError reloads the lease after a lost Renew CAS and returns the
// stable domain sentinel (released/expired/generation mismatch).
func (s *DurableStore) renewCASMissError(ctx context.Context, tx bun.Tx, cmd app.RenewCommand) error {
	current, found, err := s.loadLeaseTx(ctx, tx, cmd.LeaseID)
	if err != nil {
		return err
	}
	if !found {
		return app.ErrNotFound
	}
	if err := current.Renew(cmd.Now, cmd.ExpectedGeneration, cmd.TTL); err != nil {
		return err
	}
	return domain.ErrGenerationMismatch
}

// Release marks a lease released idempotently.
func (s *DurableStore) Release(ctx context.Context, cmd app.ReleaseCommand) (app.ReleaseResult, error) {
	if s == nil || s.db == nil {
		return app.ReleaseResult{}, fmt.Errorf("leasestore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return app.ReleaseResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return app.ReleaseResult{}, fmt.Errorf("leasestore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	lease, found, err := s.loadLeaseTx(ctx, tx, cmd.LeaseID)
	if err != nil {
		return app.ReleaseResult{}, err
	}
	if !found {
		return app.ReleaseResult{Applied: false}, nil
	}
	if lease.State == domain.LeaseStateReleased {
		if err := tx.Commit(); err != nil {
			return app.ReleaseResult{}, fmt.Errorf("leasestore: commit: %w", err)
		}
		return app.ReleaseResult{Applied: true, Lease: lease}, nil
	}
	lease.Release(cmd.Now)
	affected, err := s.releaseCASUpdate(ctx, tx, cmd.LeaseID, lease)
	if err != nil {
		return app.ReleaseResult{}, err
	}
	if affected == 0 {
		return s.releaseCASMissResult(ctx, tx, cmd)
	}
	if err := tx.Commit(); err != nil {
		return app.ReleaseResult{}, fmt.Errorf("leasestore: commit: %w", err)
	}
	return app.ReleaseResult{Applied: true, Lease: lease}, nil
}

// releaseCASUpdate applies Release under a live-state preimage so concurrent
// writers cannot clobber released rows without a state check. Generation is
// intentionally not part of the predicate: Renew bumps generation, and cleanup
// Release must still win.
func (s *DurableStore) releaseCASUpdate(ctx context.Context, tx bun.Tx, leaseID string, lease domain.Lease) (int64, error) {
	res, err := tx.NewRaw(
		`
UPDATE concurrency_leases SET state=?, released_at_unix=?
WHERE store_id=? AND lease_id=? AND state IN (?, ?)
`,
		string(lease.State), lease.ReleasedAt.UnixNano(),
		s.cfg.StoreID, leaseID,
		string(domain.LeaseStateActive), string(domain.LeaseStateExpiring),
	).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("leasestore: release: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("leasestore: release rows affected: %w", err)
	}
	return affected, nil
}

// releaseCASMissResult reloads after a lost Release CAS. Concurrent Release is
// idempotent; expired rows get a dedicated state-predicate write.
func (s *DurableStore) releaseCASMissResult(ctx context.Context, tx bun.Tx, cmd app.ReleaseCommand) (app.ReleaseResult, error) {
	current, found, err := s.loadLeaseTx(ctx, tx, cmd.LeaseID)
	if err != nil {
		return app.ReleaseResult{}, err
	}
	if !found {
		return app.ReleaseResult{Applied: false}, nil
	}
	if current.State == domain.LeaseStateReleased {
		if err := tx.Commit(); err != nil {
			return app.ReleaseResult{}, fmt.Errorf("leasestore: commit: %w", err)
		}
		return app.ReleaseResult{Applied: true, Lease: current}, nil
	}
	if current.State == domain.LeaseStateActive || current.State == domain.LeaseStateExpiring {
		current.Release(cmd.Now)
		affected, err := s.releaseCASUpdate(ctx, tx, cmd.LeaseID, current)
		if err != nil {
			return app.ReleaseResult{}, err
		}
		if affected == 0 {
			current, found, err = s.loadLeaseTx(ctx, tx, cmd.LeaseID)
			if err != nil {
				return app.ReleaseResult{}, err
			}
			if !found {
				return app.ReleaseResult{Applied: false}, nil
			}
			if current.State != domain.LeaseStateReleased {
				return app.ReleaseResult{}, fmt.Errorf("leasestore: release cas lost")
			}
		}
		if err := tx.Commit(); err != nil {
			return app.ReleaseResult{}, fmt.Errorf("leasestore: commit: %w", err)
		}
		return app.ReleaseResult{Applied: true, Lease: current}, nil
	}
	// Expired (or other terminal non-released): apply Release with expired preimage.
	current.Release(cmd.Now)
	res, err := tx.NewRaw(`
UPDATE concurrency_leases SET state=?, released_at_unix=?
WHERE store_id=? AND lease_id=? AND state=?
`, string(current.State), current.ReleasedAt.UnixNano(), s.cfg.StoreID, cmd.LeaseID, string(domain.LeaseStateExpired)).Exec(ctx)
	if err != nil {
		return app.ReleaseResult{}, fmt.Errorf("leasestore: release expired: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return app.ReleaseResult{}, fmt.Errorf("leasestore: release expired rows affected: %w", err)
	}
	if affected == 0 {
		current, found, err = s.loadLeaseTx(ctx, tx, cmd.LeaseID)
		if err != nil {
			return app.ReleaseResult{}, err
		}
		if !found {
			return app.ReleaseResult{Applied: false}, nil
		}
		if current.State != domain.LeaseStateReleased {
			return app.ReleaseResult{}, fmt.Errorf("leasestore: release expired cas lost")
		}
	}
	if err := tx.Commit(); err != nil {
		return app.ReleaseResult{}, fmt.Errorf("leasestore: commit: %w", err)
	}
	return app.ReleaseResult{Applied: true, Lease: current}, nil
}

// Query returns a bounded page of leases.
func (s *DurableStore) Query(ctx context.Context, q app.QueryCommand) (app.QueryResult, error) {
	if s == nil || s.db == nil {
		return app.QueryResult{}, fmt.Errorf("leasestore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return app.QueryResult{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = s.defaultPageSize
	}
	if limit > s.maxPageSize {
		limit = s.maxPageSize
	}
	where := []string{"store_id = ?"}
	args := []any{s.cfg.StoreID}
	if q.LeaseID != "" {
		where = append(where, "lease_id = ?")
		args = append(args, q.LeaseID)
	}
	if q.RequestID != "" {
		where = append(where, "logical_id = ?")
		args = append(args, q.RequestID)
	}
	if q.RuleID != "" {
		where = append(where, "rule_id = ?")
		args = append(args, q.RuleID)
	}
	// Active/expiring are projected in Go (near-expiry active → expiring).
	if q.State != "" && q.State != domain.LeaseStateActive && q.State != domain.LeaseStateExpiring {
		where = append(where, "state = ?")
		args = append(args, string(q.State))
	} else if q.State == domain.LeaseStateActive || q.State == domain.LeaseStateExpiring {
		where = append(where, "state IN ('active', 'expiring')")
	}
	query := fmt.Sprintf(`
SELECT store_id, lease_id, rule_id, rule_version, namespace, dimension_key,
	logical_id, holder_id, acquired_at_unix, renewed_at_unix, expires_at_unix,
	released_at_unix, generation, state, dimensions_json
FROM concurrency_leases
WHERE %s
ORDER BY lease_id ASC
LIMIT ?
`, strings.Join(where, " AND "))
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return app.QueryResult{}, fmt.Errorf("leasestore: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]domain.Lease, 0, limit)
	for rows.Next() {
		var row leaseRow
		if err := rows.Scan(
			&row.StoreID, &row.LeaseID, &row.RuleID, &row.RuleVersion, &row.Namespace, &row.DimensionKey,
			&row.LogicalID, &row.HolderID, &row.AcquiredAtUnix, &row.RenewedAtUnix, &row.ExpiresAtUnix,
			&row.ReleasedAtUnix, &row.Generation, &row.State, &row.DimensionsJSON,
		); err != nil {
			return app.QueryResult{}, fmt.Errorf("leasestore: scan: %w", err)
		}
		lease, err := rowToLease(row)
		if err != nil {
			return app.QueryResult{}, err
		}
		state := lease.EffectiveState(q.Now)
		if state == domain.LeaseStateActive && leaseNearExpiry(lease, q.Now, 15*time.Second) {
			state = domain.LeaseStateExpiring
		}
		lease.State = state
		if q.State != "" && lease.State != q.State {
			continue
		}
		out = append(out, lease)
	}
	if err := rows.Err(); err != nil {
		return app.QueryResult{}, err
	}
	return app.QueryResult{Leases: out}, nil
}

// ExplainReclaimPlan returns the SQLite EXPLAIN QUERY PLAN for reclaim SQL.
// Used by tests to prove reclaim is bounded indexed work (requirement 10.9, 16.3).
func ExplainReclaimPlan(ctx context.Context, s *DurableStore) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("leasestore: nil store")
	}
	if s.dialect != dialect.SQLite {
		return "", fmt.Errorf("leasestore: explain reclaim is sqlite-only")
	}
	rows, err := s.db.QueryContext(ctx, `
EXPLAIN QUERY PLAN
UPDATE concurrency_leases
SET state='expired'
WHERE store_id=? AND rule_id=? AND dimension_key=?
  AND state IN ('active', 'expiring')
  AND expires_at_unix <= ?
`, s.cfg.StoreID, "max-active", "probe", time.Now().UnixNano())
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var b strings.Builder
	for rows.Next() {
		var selectid, order, from int
		var detail string
		if err := rows.Scan(&selectid, &order, &from, &detail); err != nil {
			return "", err
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(detail)
	}
	return b.String(), rows.Err()
}

var _ app.LeaseStore = (*DurableStore)(nil)
