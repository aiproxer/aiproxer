package terminalwork

import (
	"errors"
	"strings"
	"time"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// ListQuery is a bounded operator filter for terminal-work rows (requirement 8.9, 12.6–12.7).
type ListQuery struct {
	WorkID        string
	SourceKey     SourceKey
	State         sdk.WorkState
	States        []sdk.WorkState
	ProviderID    string
	Kind          sdk.WorkKind
	RequestID     string
	AttemptID     string
	DueBefore     time.Time
	UpdatedAfter  time.Time
	UpdatedBefore time.Time
	Limit         int
	Cursor        string
}

// ListPage is one bounded page of work records.
type ListPage struct {
	Records []WorkRecord
	Cursor  string
}

// MaxQueryLimit is the hard upper bound for ListQuery.Limit.
const MaxQueryLimit = 500

var (
	ErrQueryTooBroad      = errors.New("terminalwork: query too broad")
	ErrQueryLimitExceeded = errors.New("terminalwork: query limit exceeded")
)

// ValidateListQuery rejects unsupported or too-broad operator filters.
func ValidateListQuery(q ListQuery) error {
	if !HasSelectiveBound(q) {
		return ErrQueryTooBroad
	}
	if q.Limit > MaxQueryLimit {
		return ErrQueryLimitExceeded
	}
	return nil
}

// HasSelectiveBound reports whether q includes at least one selective filter.
func HasSelectiveBound(q ListQuery) bool {
	if strings.TrimSpace(q.WorkID) != "" {
		return true
	}
	if err := q.SourceKey.Validate(); err == nil {
		return true
	}
	if strings.TrimSpace(q.RequestID) != "" ||
		strings.TrimSpace(q.AttemptID) != "" ||
		strings.TrimSpace(q.ProviderID) != "" {
		return true
	}
	if q.Kind != "" && q.Kind.IsKnown() {
		return true
	}
	if q.State != "" && q.State.IsKnown() {
		return true
	}
	if len(q.States) > 0 {
		return true
	}
	if !q.DueBefore.IsZero() {
		return true
	}
	if !q.UpdatedAfter.IsZero() || !q.UpdatedBefore.IsZero() {
		return true
	}
	return false
}
