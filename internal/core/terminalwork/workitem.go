package terminalwork

import (
	"fmt"
	"strings"
	"time"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// WorkItem is one independently idempotent terminal action.
type WorkItem struct {
	SourceKey   SourceKey
	WorkID      string
	Kind        sdk.WorkKind
	State       sdk.WorkState
	ProviderID  string
	Lifecycle   LifecycleCorrelation
	Versions    BoundVersions
	Attempts    int
	NextRetryAt time.Time
	Lease       ClaimLease
	Error       BoundedError
}

// NewIntent records durable intent first (design D9).
func NewIntent(source SourceKey, workID string, kind sdk.WorkKind, providerID string, life LifecycleCorrelation, ver BoundVersions) (*WorkItem, error) {
	if err := source.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(workID) == "" {
		return nil, fmt.Errorf("%w: empty work id", sdk.ErrInvalid)
	}
	if err := kind.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", sdk.ErrInvalid, err)
	}
	if kind.RequiresProvider() && strings.TrimSpace(providerID) == "" {
		return nil, fmt.Errorf("%w: provider required for %s", sdk.ErrInvalid, kind)
	}
	return &WorkItem{
		SourceKey:  source,
		WorkID:     workID,
		Kind:       kind,
		State:      sdk.WorkStateIntent,
		ProviderID: providerID,
		Lifecycle:  life,
		Versions:   ver,
	}, nil
}

// MarkPending promotes intent to pending.
func (w *WorkItem) MarkPending() error {
	if w.State != sdk.WorkStateIntent {
		return fmt.Errorf("%w: %s -> pending", sdk.ErrInvalidTransition, w.State)
	}
	w.State = sdk.WorkStatePending
	return nil
}

// Claim attempts to claim due work with a lease.
func (w *WorkItem) Claim(ownerID string, ttl time.Duration, clock Clock) error {
	if clock == nil {
		return fmt.Errorf("%w: nil clock", sdk.ErrInvalid)
	}
	if strings.TrimSpace(ownerID) == "" {
		return fmt.Errorf("%w: empty claim owner", sdk.ErrInvalid)
	}
	if ttl <= 0 {
		return fmt.Errorf("%w: non-positive claim ttl", sdk.ErrInvalid)
	}
	now := clock.Now()

	switch w.State {
	case sdk.WorkStatePending:
		// first claim
	case sdk.WorkStateRetry:
		if now.Before(w.NextRetryAt) {
			return sdk.ErrNotDue
		}
	case sdk.WorkStateClaimed:
		if w.Lease.HeldAt(now) {
			return sdk.ErrClaimLeaseHeld
		}
		// expired lease: re-claim in place
	default:
		return fmt.Errorf("%w: claim from %s", sdk.ErrInvalidTransition, w.State)
	}

	w.State = sdk.WorkStateClaimed
	w.Lease = ClaimLease{OwnerID: ownerID, ExpiresAt: now.Add(ttl)}
	return nil
}

// Complete marks the work completed.
func (w *WorkItem) Complete() error {
	if w.State != sdk.WorkStateClaimed {
		return fmt.Errorf("%w: complete from %s", sdk.ErrInvalidTransition, w.State)
	}
	w.State = sdk.WorkStateCompleted
	w.Lease = ClaimLease{}
	w.Error = BoundedError{}
	return nil
}

// Retry schedules a retry using the clock and schedule.
func (w *WorkItem) Retry(schedule RetrySchedule, clock Clock, err BoundedError) error {
	if clock == nil {
		return fmt.Errorf("%w: nil clock", sdk.ErrInvalid)
	}
	if w.State != sdk.WorkStateClaimed {
		return fmt.Errorf("%w: retry from %s", sdk.ErrInvalidTransition, w.State)
	}
	if err.Permanent {
		return sdk.ErrPermanent
	}
	w.Attempts++
	delay := schedule.Delay(w.Attempts)
	w.NextRetryAt = clock.Now().Add(delay)
	w.State = sdk.WorkStateRetry
	w.Lease = ClaimLease{}
	w.Error = err
	return nil
}

// RenewClaim extends a held claim lease for the same owner.
func (w *WorkItem) RenewClaim(ownerID string, ttl time.Duration, clock Clock) error {
	if clock == nil {
		return fmt.Errorf("%w: nil clock", sdk.ErrInvalid)
	}
	if strings.TrimSpace(ownerID) == "" {
		return fmt.Errorf("%w: empty claim owner", sdk.ErrInvalid)
	}
	if ttl <= 0 {
		return fmt.Errorf("%w: non-positive claim ttl", sdk.ErrInvalid)
	}
	if w.State != sdk.WorkStateClaimed {
		return fmt.Errorf("%w: renew from %s", sdk.ErrInvalidTransition, w.State)
	}
	if w.Lease.OwnerID != ownerID {
		return sdk.ErrConflict
	}
	now := clock.Now()
	if !w.Lease.HeldAt(now) {
		return sdk.ErrClaimLeaseHeld
	}
	w.Lease.ExpiresAt = now.Add(ttl)
	return nil
}

// Quarantine permanently parks malformed or non-retryable work.
func (w *WorkItem) Quarantine(err BoundedError) error {
	switch w.State {
	case sdk.WorkStatePending, sdk.WorkStateClaimed, sdk.WorkStateRetry:
		// ok
	default:
		return fmt.Errorf("%w: quarantine from %s", sdk.ErrInvalidTransition, w.State)
	}
	w.State = sdk.WorkStateQuarantined
	w.Lease = ClaimLease{}
	w.Error = err
	return nil
}
