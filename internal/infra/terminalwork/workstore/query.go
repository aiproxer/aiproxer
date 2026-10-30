package workstore

import (
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Query/Page and bounds are owned by the core terminalwork package (hexagonal port).
type (
	Query = terminalwork.ListQuery
	Page  = terminalwork.ListPage
)

// Command types are owned by the core terminalwork package (hexagonal port).
type (
	ClaimDueCommand       = terminalwork.ClaimDueCommand
	RenewClaimCommand     = terminalwork.RenewClaimCommand
	CompleteCommand       = terminalwork.CompleteCommand
	ScheduleRetryCommand  = terminalwork.ScheduleRetryCommand
	QuarantineCommand     = terminalwork.QuarantineCommand
	PromotePendingCommand = terminalwork.PromotePendingCommand
)

func ValidateQuery(q Query) error {
	return terminalwork.ValidateListQuery(q)
}

func HasSelectiveBound(q Query) bool {
	return terminalwork.HasSelectiveBound(q)
}

func recordMatchesQuery(r terminalwork.WorkRecord, q Query) bool {
	if workID := strings.TrimSpace(q.WorkID); workID != "" && r.WorkID != workID {
		return false
	}
	if err := q.SourceKey.Validate(); err == nil && r.SourceKey != q.SourceKey {
		return false
	}
	if q.State != "" && r.State != q.State {
		return false
	}
	if len(q.States) > 0 && !slices.Contains(q.States, r.State) {
		return false
	}
	if provider := strings.TrimSpace(q.ProviderID); provider != "" && r.ProviderID != provider {
		return false
	}
	if q.Kind != "" && r.Kind != q.Kind {
		return false
	}
	if req := strings.TrimSpace(q.RequestID); req != "" && r.Lifecycle.RequestID != req {
		return false
	}
	if att := strings.TrimSpace(q.AttemptID); att != "" && r.Lifecycle.AttemptID != att {
		return false
	}
	if !q.DueBefore.IsZero() {
		switch r.State {
		case sdk.WorkStatePending:
		case sdk.WorkStateRetry:
			if r.NextRetryAt.After(q.DueBefore) {
				return false
			}
		case sdk.WorkStateClaimed:
			if r.Lease.HeldAt(q.DueBefore) {
				return false
			}
		default:
			return false
		}
	}
	if !q.UpdatedAfter.IsZero() && r.UpdatedAt.Before(q.UpdatedAfter) {
		return false
	}
	if !q.UpdatedBefore.IsZero() && r.UpdatedAt.After(q.UpdatedBefore) {
		return false
	}
	return true
}

func isDueForClaim(r terminalwork.WorkRecord, now time.Time) bool {
	switch r.State {
	case sdk.WorkStatePending:
		return true
	case sdk.WorkStateRetry:
		return !now.Before(r.NextRetryAt)
	case sdk.WorkStateClaimed:
		return !r.Lease.HeldAt(now)
	default:
		return false
	}
}
