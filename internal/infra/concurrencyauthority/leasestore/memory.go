package leasestore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
)

// MemoryConfig configures an in-memory lease store.
type MemoryConfig struct {
	StoreID         string
	State           *MemoryState // optional shared backend for multi-handle tests
	DefaultPageSize int
	MaxPageSize     int
}

// MemoryState is the shared mutable backend for one or more MemoryStore handles.
type MemoryState struct {
	mu     sync.Mutex
	leases map[string]domain.Lease // key: storeID + "\x00" + leaseID
}

// NewMemoryState returns an empty shared memory backend.
func NewMemoryState() *MemoryState {
	return &MemoryState{leases: make(map[string]domain.Lease)}
}

// MemoryStore is a single-process LeaseStore. It is never reported as distributed
// strict enforcement (requirements 15.8, 16.7).
type MemoryStore struct {
	cfg             MemoryConfig
	state           *MemoryState
	defaultPageSize int
	maxPageSize     int
}

// NewMemory returns a ready in-memory lease store.
func NewMemory(cfg MemoryConfig) *MemoryStore {
	state := cfg.State
	if state == nil {
		state = NewMemoryState()
	}
	def := cfg.DefaultPageSize
	if def <= 0 {
		def = 100
	}
	maxPageSize := cfg.MaxPageSize
	if maxPageSize <= 0 {
		maxPageSize = 500
	}
	return &MemoryStore{
		cfg:             cfg,
		state:           state,
		defaultPageSize: def,
		maxPageSize:     maxPageSize,
	}
}

func (s *MemoryStore) key(leaseID string) string {
	return s.cfg.StoreID + "\x00" + leaseID
}

// CheckReadiness reports single-process degraded posture.
func (s *MemoryStore) CheckReadiness(ctx context.Context) (domain.Readiness, error) {
	if err := ctx.Err(); err != nil {
		return domain.Readiness{}, err
	}
	return domain.Readiness{
		State:  domain.ReadinessStateDegraded,
		Reason: "memory: single-process only; not distributed strict enforcement",
	}, nil
}

// Acquire inserts or replays a lease under the capacity limit with inline reclaim.
func (s *MemoryStore) Acquire(ctx context.Context, cmd app.AcquireCommand) (app.AcquireResult, error) {
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

	s.state.mu.Lock()
	defer s.state.mu.Unlock()

	s.reclaimLocked(cmd.RuleID, dimKey, cmd.Now)

	if existing, ok := s.state.leases[s.key(cmd.Lease.LeaseID)]; ok {
		if existing.IsLive(cmd.Now) {
			return app.AcquireResult{
				Lease:          existing,
				Replayed:       true,
				RemainingSlots: remainingLocked(s, cmd.RuleID, dimKey, cmd.Limit, cmd.Now),
			}, nil
		}
	}

	live := countLiveLocked(s, cmd.RuleID, dimKey, cmd.Now)
	if live >= cmd.Limit {
		return app.AcquireResult{CapacityExceeded: true, RemainingSlots: 0}, nil
	}

	lease := cmd.Lease
	if lease.State == "" {
		lease.State = domain.LeaseStateActive
	}
	s.state.leases[s.key(lease.LeaseID)] = lease
	return app.AcquireResult{
		Lease:          lease,
		RemainingSlots: cmd.Limit - live - 1,
	}, nil
}

func (s *MemoryStore) reclaimLocked(ruleID, dimKey string, now time.Time) {
	prefix := s.cfg.StoreID + "\x00"
	for k, lease := range s.state.leases {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if lease.RuleID != ruleID {
			continue
		}
		if string(lease.Dimensions.Key()) != dimKey {
			continue
		}
		if lease.State == domain.LeaseStateReleased || lease.State == domain.LeaseStateExpired {
			continue
		}
		if lease.SetState == domain.LeaseSetStateUncertain {
			continue
		}
		if !lease.ExpiresAt.IsZero() && !now.Before(lease.ExpiresAt) {
			lease.Expire(now)
			s.state.leases[k] = lease
		}
	}
}

func countLiveLocked(s *MemoryStore, ruleID, dimKey string, now time.Time) int {
	return countLiveExcludingLocked(s, ruleID, dimKey, now, nil)
}

func countLiveExcludingLocked(s *MemoryStore, ruleID, dimKey string, now time.Time, exclude map[string]struct{}) int {
	prefix := s.cfg.StoreID + "\x00"
	n := 0
	for k, lease := range s.state.leases {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if lease.RuleID != ruleID {
			continue
		}
		if string(lease.Dimensions.Key()) != dimKey {
			continue
		}
		if exclude != nil {
			if _, skip := exclude[strings.TrimSpace(lease.LeaseID)]; skip {
				continue
			}
		}
		if lease.IsLive(now) {
			n++
		}
	}
	return n
}

func remainingLocked(s *MemoryStore, ruleID, dimKey string, limit int, now time.Time) int {
	left := limit - countLiveLocked(s, ruleID, dimKey, now)
	if left < 0 {
		return 0
	}
	return left
}

// Renew extends a lease with generation CAS.
func (s *MemoryStore) Renew(ctx context.Context, cmd app.RenewCommand) (app.RenewResult, error) {
	if err := ctx.Err(); err != nil {
		return app.RenewResult{}, err
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	lease, ok := s.state.leases[s.key(cmd.LeaseID)]
	if !ok {
		return app.RenewResult{}, app.ErrNotFound
	}
	if err := lease.Renew(cmd.Now, cmd.ExpectedGeneration, cmd.TTL); err != nil {
		return app.RenewResult{}, err
	}
	s.state.leases[s.key(cmd.LeaseID)] = lease
	return app.RenewResult{Lease: lease}, nil
}

// Release marks a lease released idempotently.
func (s *MemoryStore) Release(ctx context.Context, cmd app.ReleaseCommand) (app.ReleaseResult, error) {
	if err := ctx.Err(); err != nil {
		return app.ReleaseResult{}, err
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	lease, ok := s.state.leases[s.key(cmd.LeaseID)]
	if !ok {
		return app.ReleaseResult{Applied: false}, nil
	}
	lease.Release(cmd.Now)
	s.state.leases[s.key(cmd.LeaseID)] = lease
	return app.ReleaseResult{Applied: true, Lease: lease}, nil
}

// Query returns a bounded page of leases.
func (s *MemoryStore) Query(ctx context.Context, q app.QueryCommand) (app.QueryResult, error) {
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
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	prefix := s.cfg.StoreID + "\x00"
	out := make([]domain.Lease, 0)
	for k, lease := range s.state.leases {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if q.LeaseID != "" && lease.LeaseID != q.LeaseID {
			continue
		}
		if q.RequestID != "" && lease.LogicalID != q.RequestID {
			continue
		}
		if q.RuleID != "" && lease.RuleID != q.RuleID {
			continue
		}
		state := lease.EffectiveState(q.Now)
		if state == domain.LeaseStateActive && leaseNearExpiry(lease, q.Now, 15*time.Second) {
			state = domain.LeaseStateExpiring
		}
		if q.State != "" && state != q.State {
			continue
		}
		cp := lease
		cp.State = state
		out = append(out, cp)
		if len(out) >= limit {
			break
		}
	}
	return app.QueryResult{Leases: out}, nil
}

func leaseNearExpiry(lease domain.Lease, now time.Time, window time.Duration) bool {
	if lease.ExpiresAt.IsZero() || window <= 0 {
		return false
	}
	return !now.Before(lease.ExpiresAt.Add(-window))
}

var _ app.LeaseStore = (*MemoryStore)(nil)
