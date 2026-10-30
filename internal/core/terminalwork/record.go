package terminalwork

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// WorkRecord is the persisted terminal-work row (requirements 8.1–8.2, design D14).
type WorkRecord struct {
	WorkID         string
	SourceKey      SourceKey
	PayloadVersion int
	Kind           sdk.WorkKind
	State          sdk.WorkState
	ProviderID     string
	Lifecycle      LifecycleCorrelation
	Versions       BoundVersions
	Payload        []byte
	FactID         string
	LeaseSetID     string
	Attempts       int
	NextRetryAt    time.Time
	Lease          ClaimLease
	Error          BoundedError
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Validate rejects malformed persisted records before store I/O.
func (r WorkRecord) Validate() error {
	if err := r.SourceKey.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.WorkID) == "" {
		return fmt.Errorf("%w: empty work id", sdk.ErrInvalid)
	}
	if r.PayloadVersion < 1 {
		return fmt.Errorf("%w: payload version", sdk.ErrInvalid)
	}
	if err := r.Kind.Validate(); err != nil {
		return fmt.Errorf("%w: %v", sdk.ErrInvalid, err)
	}
	if err := r.State.Validate(); err != nil {
		return fmt.Errorf("%w: %v", sdk.ErrInvalid, err)
	}
	if r.Kind.RequiresProvider() && strings.TrimSpace(r.ProviderID) == "" {
		return fmt.Errorf("%w: provider required for %s", sdk.ErrInvalid, r.Kind)
	}
	return nil
}

// SameIntentReplay reports whether a and b describe the same durable intent
// payload for idempotent AppendIntent replay (requirements 8.5, design D6).
func SameIntentReplay(a, b WorkRecord) bool {
	if a.SourceKey != b.SourceKey || a.WorkID != b.WorkID {
		return false
	}
	if a.PayloadVersion != b.PayloadVersion || a.Kind != b.Kind || a.ProviderID != b.ProviderID {
		return false
	}
	if a.Lifecycle != b.Lifecycle || a.Versions != b.Versions {
		return false
	}
	if a.FactID != b.FactID || a.LeaseSetID != b.LeaseSetID {
		return false
	}
	return bytes.Equal(a.Payload, b.Payload)
}

// ToWorkItem projects persisted state into the domain item for transitions.
func (r WorkRecord) ToWorkItem() *WorkItem {
	return &WorkItem{
		SourceKey:   r.SourceKey,
		WorkID:      r.WorkID,
		Kind:        r.Kind,
		State:       r.State,
		ProviderID:  r.ProviderID,
		Lifecycle:   r.Lifecycle,
		Versions:    r.Versions,
		Attempts:    r.Attempts,
		NextRetryAt: r.NextRetryAt,
		Lease:       r.Lease,
		Error:       r.Error,
	}
}

// ApplyWorkItem copies domain transition results back into the record.
func (r *WorkRecord) ApplyWorkItem(w *WorkItem, now time.Time) {
	r.State = w.State
	r.Attempts = w.Attempts
	r.NextRetryAt = w.NextRetryAt
	r.Lease = w.Lease
	r.Error = w.Error
	r.UpdatedAt = now.UTC()
}
