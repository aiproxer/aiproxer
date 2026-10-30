package controlplane

import (
	"encoding/json"
	"fmt"
)

// EventDetail is the sum-type marker carried by [Event]. Exactly one non-nil
// detail value must match [Event.Category]. The Go API uses this interface
// field instead of parallel nullable detail pointers (v2-compatible wire JSON
// is preserved via custom marshaling on [Event]).
type EventDetail interface {
	eventCategory() Category
}

func (d *AuthDetail) eventCategory() Category                { return CategoryAuth }
func (d *SessionDetail) eventCategory() Category             { return CategorySession }
func (d *AttemptDetail) eventCategory() Category             { return CategoryAttempt }
func (d *UsageDetail) eventCategory() Category               { return CategoryUsage }
func (d *AccountingAuthorityDetail) eventCategory() Category { return CategoryAccountingAuthority }
func (d *PolicyDetail) eventCategory() Category              { return CategoryPolicy }
func (d *AuditDetail) eventCategory() Category               { return CategoryAudit }
func (d *LifecycleDetail) eventCategory() Category           { return CategoryLifecycle }

// Auth returns the auth detail when Detail carries one.
func (e Event) Auth() *AuthDetail {
	d, _ := e.Detail.(*AuthDetail)
	return d
}

// Session returns the session detail when Detail carries one.
func (e Event) Session() *SessionDetail {
	d, _ := e.Detail.(*SessionDetail)
	return d
}

// Attempt returns the attempt detail when Detail carries one.
func (e Event) Attempt() *AttemptDetail {
	d, _ := e.Detail.(*AttemptDetail)
	return d
}

// Usage returns the usage detail when Detail carries one.
func (e Event) Usage() *UsageDetail {
	d, _ := e.Detail.(*UsageDetail)
	return d
}

// AccountingAuthority returns the accounting-authority detail when Detail carries one.
func (e Event) AccountingAuthority() *AccountingAuthorityDetail {
	d, _ := e.Detail.(*AccountingAuthorityDetail)
	return d
}

// Policy returns the policy detail when Detail carries one.
func (e Event) Policy() *PolicyDetail {
	d, _ := e.Detail.(*PolicyDetail)
	return d
}

// Audit returns the audit detail when Detail carries one.
func (e Event) Audit() *AuditDetail {
	d, _ := e.Detail.(*AuditDetail)
	return d
}

// Lifecycle returns the lifecycle detail when Detail carries one.
func (e Event) Lifecycle() *LifecycleDetail {
	d, _ := e.Detail.(*LifecycleDetail)
	return d
}

type eventWire struct {
	ID             EventID         `json:"id"`
	SourceEventKey string          `json:"source_event_key,omitempty"`
	Category       Category        `json:"category"`
	OccurredAt     json.RawMessage `json:"occurred_at"`
	RecordedAt     json.RawMessage `json:"recorded_at"`
	Correlation    Correlation     `json:"correlation"`
	Scope          ScopeSnapshot   `json:"scope"`
	Source         SourceRef       `json:"source"`
	Visibility     Visibility      `json:"visibility"`
	EvidenceState  EvidenceState   `json:"evidence_state"`
	RedactionState RedactionState  `json:"redaction_state"`
	Summary        string          `json:"summary,omitempty"`

	Auth                *AuthDetail                `json:"auth,omitempty"`
	Session             *SessionDetail             `json:"session,omitempty"`
	Attempt             *AttemptDetail             `json:"attempt,omitempty"`
	Usage               *UsageDetail               `json:"usage,omitempty"`
	AccountingAuthority *AccountingAuthorityDetail `json:"accounting_authority,omitempty"`
	Policy              *PolicyDetail              `json:"policy,omitempty"`
	Audit               *AuditDetail               `json:"audit,omitempty"`
	Lifecycle           *LifecycleDetail           `json:"lifecycle,omitempty"`
}

// MarshalJSON preserves the historical wire shape with category-keyed detail blocks.
func (e Event) MarshalJSON() ([]byte, error) {
	wire := eventWire{
		ID:             e.ID,
		SourceEventKey: e.SourceEventKey,
		Category:       e.Category,
		Correlation:    e.Correlation,
		Scope:          e.Scope,
		Source:         e.Source,
		Visibility:     e.Visibility,
		EvidenceState:  e.EvidenceState,
		RedactionState: e.RedactionState,
		Summary:        e.Summary,
	}
	var err error
	if wire.OccurredAt, err = json.Marshal(e.OccurredAt); err != nil {
		return nil, err
	}
	if wire.RecordedAt, err = json.Marshal(e.RecordedAt); err != nil {
		return nil, err
	}
	switch d := e.Detail.(type) {
	case *AuthDetail:
		wire.Auth = d
	case *SessionDetail:
		wire.Session = d
	case *AttemptDetail:
		wire.Attempt = d
	case *UsageDetail:
		wire.Usage = d
	case *AccountingAuthorityDetail:
		wire.AccountingAuthority = d
	case *PolicyDetail:
		wire.Policy = d
	case *AuditDetail:
		wire.Audit = d
	case *LifecycleDetail:
		wire.Lifecycle = d
	case nil:
	default:
		return nil, fmt.Errorf("controlplane event: unsupported detail type %T", e.Detail)
	}
	return json.Marshal(wire)
}

// UnmarshalJSON decodes the historical wire shape into Detail by construction.
func (e *Event) UnmarshalJSON(data []byte) error {
	var wire eventWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if err := json.Unmarshal(wire.OccurredAt, &e.OccurredAt); err != nil {
		return err
	}
	if err := json.Unmarshal(wire.RecordedAt, &e.RecordedAt); err != nil {
		return err
	}
	e.ID = wire.ID
	e.SourceEventKey = wire.SourceEventKey
	e.Category = wire.Category
	e.Correlation = wire.Correlation
	e.Scope = wire.Scope
	e.Source = wire.Source
	e.Visibility = wire.Visibility
	e.EvidenceState = wire.EvidenceState
	e.RedactionState = wire.RedactionState
	e.Summary = wire.Summary

	set := 0
	var detail EventDetail
	if wire.Auth != nil {
		set++
		detail = wire.Auth
	}
	if wire.Session != nil {
		set++
		detail = wire.Session
	}
	if wire.Attempt != nil {
		set++
		detail = wire.Attempt
	}
	if wire.Usage != nil {
		set++
		detail = wire.Usage
	}
	if wire.AccountingAuthority != nil {
		set++
		detail = wire.AccountingAuthority
	}
	if wire.Policy != nil {
		set++
		detail = wire.Policy
	}
	if wire.Audit != nil {
		set++
		detail = wire.Audit
	}
	if wire.Lifecycle != nil {
		set++
		detail = wire.Lifecycle
	}
	if set > 1 {
		return fmt.Errorf("controlplane event: exactly one detail block is required, got %d", set)
	}
	e.Detail = detail
	return nil
}

func detailCategory(d EventDetail) Category {
	if d == nil {
		return ""
	}
	return d.eventCategory()
}
