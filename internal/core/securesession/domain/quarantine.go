package domain

import (
	"fmt"
	"strings"
	"time"
)

const (
	QuarantineAuditActionSecretGuard = "secret_guard"
	QuarantineAuditResultBlocked     = "blocked"
)

// QuarantinePlan captures the pure transition outcome for Store.Quarantine.
type QuarantinePlan struct {
	Apply          bool
	Status         SessionStatus
	ResumeEligible bool
	QuarantinedAt  time.Time
	ReasonCode     string
	EventID        string
	AuditAction    string
	AuditResult    string
}

// Validate reports whether in contains the minimum safe fields for quarantine.
func (in QuarantineInput) Validate() error {
	if strings.TrimSpace(string(in.SessionID)) == "" {
		return fmt.Errorf("%w: session_id", ErrInvalidQuarantineInput)
	}
	if strings.TrimSpace(string(in.TurnID)) == "" {
		return fmt.Errorf("%w: turn_id", ErrInvalidQuarantineInput)
	}
	if strings.TrimSpace(in.ReasonCode) == "" {
		return fmt.Errorf("%w: reason_code", ErrInvalidQuarantineInput)
	}
	if strings.TrimSpace(in.EventID) == "" {
		return fmt.Errorf("%w: event_id", ErrInvalidQuarantineInput)
	}
	if in.At.IsZero() {
		return fmt.Errorf("%w: at", ErrInvalidQuarantineInput)
	}
	return nil
}

// PlanQuarantine derives the terminal quarantine transition from the current record and input.
// It never mutates the record; adapters own locking, persistence, and audit writes.
func PlanQuarantine(current Record, in QuarantineInput) (QuarantinePlan, error) {
	if err := in.Validate(); err != nil {
		return QuarantinePlan{}, err
	}
	plan := QuarantinePlan{
		AuditAction: QuarantineAuditActionSecretGuard,
		AuditResult: QuarantineAuditResultBlocked,
	}
	if current.Status.IsQuarantined() {
		plan.ReasonCode = current.QuarantineReasonCode
		plan.EventID = current.QuarantineEventID
		plan.QuarantinedAt = current.QuarantinedAt
		return plan, nil
	}
	plan.Apply = true
	plan.Status = SessionStatusQuarantined
	plan.ResumeEligible = false
	plan.QuarantinedAt = in.At
	plan.ReasonCode = in.ReasonCode
	plan.EventID = in.EventID
	return plan, nil
}
