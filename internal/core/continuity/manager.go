package continuity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Session represents a resolved or created A-leg continuity session.
type Session struct {
	ALegID        string
	ContinuityKey string
	CreatedAt     time.Time
	IsNew         bool
}

// Manager provides the core-owned B2BUA and lineage boundary.
// It wraps b2bua.Store with higher-level session resolution semantics.
type Manager struct {
	store b2bua.Store
}

// NewManager creates a Manager backed by store.
func NewManager(store b2bua.Store) (*Manager, error) {
	if store == nil {
		return nil, errors.New("continuity: nil store")
	}
	return &Manager{store: store}, nil
}

// ResolveSession returns the existing session for ref, or creates a new one.
// It follows the same resolution order as the executor: ALegID > ContinuityKey > new.
func (m *Manager) ResolveSession(ctx context.Context, ref aipapi.SessionRef) (Session, error) {
	if ref.ALegID != "" {
		rec, err := m.store.FetchALeg(ctx, ref.ALegID)
		if err == nil {
			return Session{
				ALegID:        rec.ALegID,
				ContinuityKey: rec.ContinuityKey,
				CreatedAt:     rec.CreatedAt,
				IsNew:         false,
			}, nil
		}
		if !errors.Is(err, b2bua.ErrALegNotFound) {
			return Session{}, fmt.Errorf("continuity: get a-leg: %w", err)
		}
	}
	if ref.ContinuityKey != "" {
		rec, err := m.store.ResolveALeg(ctx, ref.ContinuityKey)
		if err == nil {
			return Session{
				ALegID:        rec.ALegID,
				ContinuityKey: rec.ContinuityKey,
				CreatedAt:     rec.CreatedAt,
				IsNew:         false,
			}, nil
		}
		if !errors.Is(err, b2bua.ErrALegNotFound) {
			return Session{}, fmt.Errorf("continuity: resolve a-leg by continuity key: %w", err)
		}
		created, err := m.store.CreateALeg(ctx, ref.ContinuityKey)
		if err != nil {
			return Session{}, fmt.Errorf("continuity: create a-leg for continuity key: %w", err)
		}
		return Session{
			ALegID:        created.ALegID,
			ContinuityKey: created.ContinuityKey,
			CreatedAt:     created.CreatedAt,
			IsNew:         true,
		}, nil
	}
	created, err := m.store.CreateALeg(ctx, "")
	if err != nil {
		return Session{}, fmt.Errorf("continuity: create new a-leg: %w", err)
	}
	return Session{
		ALegID:        created.ALegID,
		ContinuityKey: created.ContinuityKey,
		CreatedAt:     created.CreatedAt,
		IsNew:         true,
	}, nil
}

// Store returns the underlying b2bua.Store for direct access by the executor.
func (m *Manager) Store() b2bua.Store {
	return m.store
}

// ResolveALegRecord resolves ref to a stored A-leg row (ALegID, WeightedFirstConsumed, etc.).
// Session resolution order matches Manager.ResolveSession: ALegID, then ContinuityKey, then new A-leg.
func ResolveALegRecord(ctx context.Context, store b2bua.Store, ref aipapi.SessionRef) (b2bua.ALegRecord, error) {
	m, err := NewManager(store)
	if err != nil {
		return b2bua.ALegRecord{}, fmt.Errorf("continuity: resolve a-leg record: %w", err)
	}
	sess, err := m.ResolveSession(ctx, ref)
	if err != nil {
		return b2bua.ALegRecord{}, fmt.Errorf("continuity: resolve a-leg record: %w", err)
	}
	rec, err := store.FetchALeg(ctx, sess.ALegID)
	if err != nil {
		return b2bua.ALegRecord{}, fmt.Errorf("continuity: resolve a-leg record: %w", err)
	}
	return rec, nil
}
