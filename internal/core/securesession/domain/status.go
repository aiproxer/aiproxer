package domain

import "time"

// SessionStatus is the lifecycle status of a secure session.
type SessionStatus string

const (
	SessionStatusActive      SessionStatus = "active"
	SessionStatusQuarantined SessionStatus = "quarantined"
)

// QuarantineInput is the safe, idempotent transition request for Store.Quarantine.
// It must never carry secret values, prompt excerpts, or resume tokens.
type QuarantineInput struct {
	SessionID  SessionID
	TurnID     TurnID
	ReasonCode string
	EventID    string
	At         time.Time
}

// IsQuarantined reports whether s is the terminal quarantined status.
func (s SessionStatus) IsQuarantined() bool {
	return s == SessionStatusQuarantined
}

// IsActive reports whether s is active. Empty status is treated as active for
// pre-migration rows until Phase 5 writes an explicit active value.
func (s SessionStatus) IsActive() bool {
	return s == SessionStatusActive || s == ""
}
