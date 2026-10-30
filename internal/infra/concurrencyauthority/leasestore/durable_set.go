package leasestore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/uptrace/bun"
)

// AcquireSet atomically acquires or replays a complete multi-rule lease set.
func (s *DurableStore) AcquireSet(ctx context.Context, cmd app.AcquireSetCommand) (app.AcquireSetResult, error) {
	if s == nil || s.db == nil {
		return app.AcquireSetResult{}, fmt.Errorf("leasestore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return app.AcquireSetResult{}, err
	}
	if strings.TrimSpace(cmd.SetID) == "" || len(cmd.Members) == 0 {
		return app.AcquireSetResult{}, fmt.Errorf("leasestore: invalid acquire set command")
	}
	if err := domain.ValidateTiming(cmd.TTL, cmd.RenewBefore); err != nil {
		return app.AcquireSetResult{}, err
	}
	lockOrder := make([]string, 0, len(cmd.Members))
	byRule := map[string]app.AcquireSetMember{}
	for _, m := range cmd.Members {
		id := strings.TrimSpace(m.RuleID)
		lockOrder = append(lockOrder, id)
		byRule[id] = m
	}
	lockOrder = domain.SortedRuleIDs(lockOrder)
	nowUnix := cmd.Now.UnixNano()
	exp := cmd.Now.Add(cmd.TTL)

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return app.AcquireSetResult{}, fmt.Errorf("leasestore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if existing, ok, err := s.loadSetTx(ctx, tx, cmd.SetID); err != nil {
		return app.AcquireSetResult{}, err
	} else if ok && existing.OccupiesCapacity(cmd.Now) {
		if err := tx.Commit(); err != nil {
			return app.AcquireSetResult{}, err
		}
		return app.AcquireSetResult{Set: existing, Replayed: true, LockOrder: lockOrder}, nil
	}

	excludeIDs := make([]string, 0, len(cmd.Members))
	for _, m := range cmd.Members {
		if id := strings.TrimSpace(m.Lease.LeaseID); id != "" {
			excludeIDs = append(excludeIDs, id)
		}
	}
	for _, ruleID := range lockOrder {
		m := byRule[ruleID]
		dimKey := string(m.Dimensions.Key())
		if err := s.lockCapacity(ctx, tx, m.RuleID, dimKey); err != nil {
			return app.AcquireSetResult{}, err
		}
		if err := s.reclaimExpired(ctx, tx, m.RuleID, dimKey, nowUnix, cmd.Now); err != nil {
			return app.AcquireSetResult{}, err
		}
		live, err := s.countLiveTxExcluding(ctx, tx, m.RuleID, dimKey, nowUnix, excludeIDs)
		if err != nil {
			return app.AcquireSetResult{}, err
		}
		if live >= m.Limit && m.Mode != domain.RuleModeAdvisory {
			if err := tx.Commit(); err != nil {
				return app.AcquireSetResult{}, err
			}
			return app.AcquireSetResult{
				CapacityExceeded: true, RemainingSlots: 0, LockOrder: lockOrder, DenyingRuleID: ruleID,
			}, nil
		}
	}

	members := make([]domain.Lease, 0, len(lockOrder))
	for _, ruleID := range lockOrder {
		m := byRule[ruleID]
		lease := m.Lease
		lease.IdentityVersion = domain.IdentityVersionLeaseSet
		lease.SetID = cmd.SetID
		lease.SetGeneration = 1
		lease.SetState = domain.LeaseSetStateActive
		lease.State = domain.LeaseStateActive
		lease.LogicalID = cmd.RequestID
		lease.AcquiredAt = cmd.Now
		lease.RenewedAt = cmd.Now
		lease.ExpiresAt = exp
		lease.Generation = 1
		if lease.RuleID == "" {
			lease.RuleID = m.RuleID
		}
		dimKey := string(m.Dimensions.Key())
		row, err := leaseToRow(s.cfg.StoreID, lease, dimKey)
		if err != nil {
			return app.AcquireSetResult{}, err
		}
		if _, err := tx.NewRaw(
			`
INSERT INTO concurrency_leases(
	store_id, lease_id, rule_id, rule_version, namespace, dimension_key,
	logical_id, holder_id, acquired_at_unix, renewed_at_unix, expires_at_unix,
	released_at_unix, generation, state, dimensions_json,
	identity_version, set_id, set_generation, set_state
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(store_id, lease_id) DO UPDATE SET
	rule_id=excluded.rule_id, rule_version=excluded.rule_version, namespace=excluded.namespace,
	dimension_key=excluded.dimension_key, logical_id=excluded.logical_id, holder_id=excluded.holder_id,
	acquired_at_unix=excluded.acquired_at_unix, renewed_at_unix=excluded.renewed_at_unix,
	expires_at_unix=excluded.expires_at_unix, released_at_unix=excluded.released_at_unix,
	generation=excluded.generation, state=excluded.state, dimensions_json=excluded.dimensions_json,
	identity_version=excluded.identity_version, set_id=excluded.set_id,
	set_generation=excluded.set_generation, set_state=excluded.set_state
`,
			row.StoreID, row.LeaseID, row.RuleID, row.RuleVersion, row.Namespace, row.DimensionKey,
			row.LogicalID, row.HolderID, row.AcquiredAtUnix, row.RenewedAtUnix, row.ExpiresAtUnix,
			row.ReleasedAtUnix, row.Generation, row.State, row.DimensionsJSON,
			row.IdentityVersion, row.SetID, row.SetGeneration, row.SetState,
		).Exec(ctx); err != nil {
			return app.AcquireSetResult{}, fmt.Errorf("leasestore: upsert set member: %w", err)
		}
		members = append(members, lease)
	}
	if err := tx.Commit(); err != nil {
		return app.AcquireSetResult{}, fmt.Errorf("leasestore: commit: %w", err)
	}
	return app.AcquireSetResult{
		Set: domain.LeaseSet{
			SetID: cmd.SetID, RequestID: cmd.RequestID, Generation: 1,
			State: domain.LeaseSetStateActive, Members: members,
			AcquiredAt: cmd.Now, RenewedAt: cmd.Now, ExpiresAt: exp,
			TTL: cmd.TTL, RenewBefore: cmd.RenewBefore,
		},
		LockOrder: lockOrder,
	}, nil
}

// RenewSet renews every member under one set generation CAS.
func (s *DurableStore) RenewSet(ctx context.Context, cmd app.RenewSetCommand) (app.RenewSetResult, error) {
	if s == nil || s.db == nil {
		return app.RenewSetResult{}, fmt.Errorf("leasestore: nil store")
	}
	if err := domain.ValidateTiming(cmd.TTL, cmd.RenewBefore); err != nil {
		return app.RenewSetResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return app.RenewSetResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	set, ok, err := s.loadSetTx(ctx, tx, cmd.SetID)
	if err != nil {
		return app.RenewSetResult{}, err
	}
	if !ok {
		return app.RenewSetResult{}, app.ErrNotFound
	}
	if set.Generation != cmd.ExpectedGeneration {
		return app.RenewSetResult{}, domain.ErrGenerationMismatch
	}
	expUnix := cmd.Now.Add(cmd.TTL).UnixNano()
	next := set.Generation + 1
	res, err := tx.NewRaw(
		`
UPDATE concurrency_leases
SET renewed_at_unix=?, expires_at_unix=?, generation=generation+1,
	set_generation=?, set_state=?, state=?
WHERE store_id=? AND set_id=? AND set_generation=?
`,
		cmd.Now.UnixNano(), expUnix, next,
		string(domain.LeaseSetStateActive), string(domain.LeaseStateActive),
		s.cfg.StoreID, cmd.SetID, cmd.ExpectedGeneration,
	).Exec(ctx)
	if err != nil {
		return app.RenewSetResult{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return app.RenewSetResult{}, domain.ErrGenerationMismatch
	}
	set, _, err = s.loadSetTx(ctx, tx, cmd.SetID)
	if err != nil {
		return app.RenewSetResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return app.RenewSetResult{}, err
	}
	return app.RenewSetResult{Set: set}, nil
}

// ReleaseSet releases every member of a set idempotently.
func (s *DurableStore) ReleaseSet(ctx context.Context, cmd app.ReleaseSetCommand) (app.ReleaseSetResult, error) {
	if s == nil || s.db == nil {
		return app.ReleaseSetResult{}, fmt.Errorf("leasestore: nil store")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return app.ReleaseSetResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	set, ok, err := s.loadSetTx(ctx, tx, cmd.SetID)
	if err != nil {
		return app.ReleaseSetResult{}, err
	}
	if !ok {
		return app.ReleaseSetResult{Applied: false}, nil
	}
	if set.State == domain.LeaseSetStateReleased {
		return app.ReleaseSetResult{Applied: false, Set: set}, nil
	}
	if _, err := tx.NewRaw(
		`
UPDATE concurrency_leases
SET state=?, set_state=?, released_at_unix=?
WHERE store_id=? AND set_id=?
`,
		string(domain.LeaseStateReleased), string(domain.LeaseSetStateReleased), cmd.Now.UnixNano(),
		s.cfg.StoreID, cmd.SetID,
	).Exec(ctx); err != nil {
		return app.ReleaseSetResult{}, err
	}
	set.State = domain.LeaseSetStateReleased
	set.ReleasedAt = cmd.Now
	if err := tx.Commit(); err != nil {
		return app.ReleaseSetResult{}, err
	}
	return app.ReleaseSetResult{Applied: true, Set: set}, nil
}

// QuerySets returns a bounded page of lease sets.
func (s *DurableStore) QuerySets(ctx context.Context, q app.QuerySetsCommand) (app.QuerySetsResult, error) {
	if s == nil || s.db == nil {
		return app.QuerySetsResult{}, fmt.Errorf("leasestore: nil store")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = s.defaultPageSize
	}
	if limit > s.maxPageSize {
		limit = s.maxPageSize
	}
	var rows []leaseRow
	err := s.db.NewRaw(`
SELECT store_id, lease_id, rule_id, rule_version, namespace, dimension_key,
	logical_id, holder_id, acquired_at_unix, renewed_at_unix, expires_at_unix,
	released_at_unix, generation, state, dimensions_json,
	identity_version, set_id, set_generation, set_state
FROM concurrency_leases
WHERE store_id=? AND set_id != ''
ORDER BY set_id, rule_id
LIMIT ?
`, s.cfg.StoreID, limit*8).Scan(ctx, &rows)
	if err != nil {
		return app.QuerySetsResult{}, err
	}
	by := map[string]*domain.LeaseSet{}
	for _, row := range rows {
		lease, err := rowToLease(row)
		if err != nil {
			return app.QuerySetsResult{}, err
		}
		if q.SetID != "" && lease.SetID != q.SetID {
			continue
		}
		if q.RequestID != "" && lease.LogicalID != q.RequestID {
			continue
		}
		set := by[lease.SetID]
		if set == nil {
			set = &domain.LeaseSet{
				SetID: lease.SetID, RequestID: lease.LogicalID,
				Generation: lease.SetGeneration, State: lease.SetState,
				ExpiresAt: lease.ExpiresAt,
			}
			by[lease.SetID] = set
		}
		set.Members = append(set.Members, lease)
	}
	out := make([]domain.LeaseSet, 0, len(by))
	for _, set := range by {
		if q.State != "" && set.State != q.State {
			continue
		}
		out = append(out, *set)
		if len(out) >= limit {
			break
		}
	}
	return app.QuerySetsResult{Sets: out}, nil
}

func (s *DurableStore) loadSetTx(ctx context.Context, tx bun.Tx, setID string) (domain.LeaseSet, bool, error) {
	var rows []leaseRow
	err := tx.NewRaw(`
SELECT store_id, lease_id, rule_id, rule_version, namespace, dimension_key,
	logical_id, holder_id, acquired_at_unix, renewed_at_unix, expires_at_unix,
	released_at_unix, generation, state, dimensions_json,
	identity_version, set_id, set_generation, set_state
FROM concurrency_leases WHERE store_id=? AND set_id=?
ORDER BY rule_id
`, s.cfg.StoreID, setID).Scan(ctx, &rows)
	if err != nil {
		return domain.LeaseSet{}, false, err
	}
	if len(rows) == 0 {
		return domain.LeaseSet{}, false, nil
	}
	members := make([]domain.Lease, 0, len(rows))
	for _, row := range rows {
		lease, err := rowToLease(row)
		if err != nil {
			return domain.LeaseSet{}, false, err
		}
		members = append(members, lease)
	}
	first := members[0]
	return domain.LeaseSet{
		SetID: setID, RequestID: first.LogicalID, Generation: first.SetGeneration,
		State: first.SetState, Members: members,
		AcquiredAt: first.AcquiredAt, RenewedAt: first.RenewedAt, ExpiresAt: first.ExpiresAt,
	}, true, nil
}

var _ app.LeaseStore = (*DurableStore)(nil)

// MarkSetUncertain marks a durable set uncertain (task 6.3).
func (s *DurableStore) MarkSetUncertain(ctx context.Context, setID string, now time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("leasestore: nil store")
	}
	_, err := s.db.NewRaw(
		`
UPDATE concurrency_leases
SET set_state=?, state=?, renewed_at_unix=?
WHERE store_id=? AND set_id=?
`,
		string(domain.LeaseSetStateUncertain), string(domain.LeaseStateExpiring), now.UnixNano(),
		s.cfg.StoreID, setID,
	).Exec(ctx)
	return err
}
