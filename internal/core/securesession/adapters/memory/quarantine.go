package memory

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// Quarantine marks a session terminal and appends a secret_guard blocked audit row.
func (s *Store) Quarantine(ctx context.Context, in domain.QuarantineInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.byID[in.SessionID]
	if !ok {
		return domain.ErrSessionNotFound
	}
	plan, err := domain.PlanQuarantine(row.rec, in)
	if err != nil {
		return err
	}
	if !plan.Apply {
		return nil
	}
	row.rec.Status = plan.Status
	row.rec.ResumeEligible = plan.ResumeEligible
	row.rec.QuarantinedAt = plan.QuarantinedAt
	row.rec.QuarantineReasonCode = plan.ReasonCode
	row.rec.QuarantineEventID = plan.EventID
	row.audit = append(row.audit, domain.AuditItem{
		SessionID: in.SessionID,
		TurnID:    in.TurnID,
		Seq:       nextAuditSeqLocked(row),
		Action:    plan.AuditAction,
		Result:    plan.AuditResult,
		CreatedAt: plan.QuarantinedAt.UTC(),
	})
	return nil
}
