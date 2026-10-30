package terminalwork

import (
	"time"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// ClaimDueCommand selects and claims a bounded batch of due work.
type ClaimDueCommand struct {
	OwnerID    string
	TTL        time.Duration
	Limit      int
	Now        time.Time
	ProviderID string
	Kind       sdk.WorkKind
}

// RenewClaimCommand extends a held claim lease for the same owner.
type RenewClaimCommand struct {
	WorkID  string
	OwnerID string
	TTL     time.Duration
	Now     time.Time
}

// CompleteCommand marks claimed work completed for the expected owner.
type CompleteCommand struct {
	WorkID          string
	ExpectedOwnerID string
	Now             time.Time
}

// ScheduleRetryCommand schedules a non-permanent retry for claimed work.
type ScheduleRetryCommand struct {
	WorkID          string
	ExpectedOwnerID string
	Schedule        RetrySchedule
	Err             BoundedError
	Now             time.Time
}

// QuarantineCommand permanently parks malformed or non-retryable work.
type QuarantineCommand struct {
	WorkID string
	Err    BoundedError
	Now    time.Time
}

// PromotePendingCommand moves intent rows to pending claim eligibility.
type PromotePendingCommand struct {
	WorkID string
	Now    time.Time
}
