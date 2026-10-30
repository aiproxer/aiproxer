package app

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// Quarantine transitions a session to the terminal quarantined status via [Store.Quarantine].
// Phase 1 exposes the manager signature; store adapters may still return ErrQuarantineUnimplemented
// until Phase 5 implements durable/idempotent quarantine.
func (m *Manager) Quarantine(ctx context.Context, in domain.QuarantineInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	return m.store.Quarantine(ctx, in)
}

// AssertActive loads the session and returns ErrSessionQuarantined when status is quarantined.
// Phase 1 implements the status check when stores persist Status; unimplemented quarantine
// leaves sessions active so existing BeginTurn behavior is unchanged.
func (m *Manager) AssertActive(ctx context.Context, id domain.SessionID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rec, err := m.store.LoadByID(ctx, id)
	if err != nil {
		return err
	}
	if rec.Status.IsQuarantined() {
		return domain.ErrSessionQuarantined
	}
	return nil
}
