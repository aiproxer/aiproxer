package controlplane

import (
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// Category classifies one control-plane evidence record into a lifecycle domain.
// Exactly one Category is carried per Event, and it must match the single typed
// detail block (requirement 1.7, 1.8).
type Category string

const (
	CategoryAuth                Category = "auth"
	CategorySession             Category = "session"
	CategoryAttempt             Category = "attempt"
	CategoryUsage               Category = "usage"
	CategoryAccountingAuthority Category = "accounting_authority"
	CategoryPolicy              Category = "policy"
	CategoryAudit               Category = "audit"
	CategoryLifecycle           Category = "lifecycle"
)

// IsKnown reports whether c is one of the documented category values.
func (c Category) IsKnown() bool {
	switch c {
	case CategoryAuth, CategorySession, CategoryAttempt, CategoryUsage, CategoryAccountingAuthority, CategoryPolicy, CategoryAudit, CategoryLifecycle:
		return true
	}
	return false
}

// EvidenceState reports the availability of an evidence record or field to a
// query consumer (requirement 1.8, 3.5, 3.6). Distinct from an empty string:
// unavailable, expired, redacted, and unsupported must be explicit rather than
// ambiguous empty values.
type EvidenceState string

const (
	EvidenceRecorded    EvidenceState = "recorded"
	EvidencePartial     EvidenceState = "partial"
	EvidenceUnavailable EvidenceState = "unavailable"
	EvidenceRedacted    EvidenceState = "redacted"
	EvidenceExpired     EvidenceState = "expired"
	EvidenceUnsupported EvidenceState = "unsupported"
)

// IsKnown reports whether s is one of the documented evidence states.
func (s EvidenceState) IsKnown() bool {
	switch s {
	case EvidenceRecorded, EvidencePartial, EvidenceUnavailable, EvidenceRedacted, EvidenceExpired, EvidenceUnsupported:
		return true
	}
	return false
}

// RedactionState reports how a record or field has been redacted for query
// visibility (requirement 4.6, 4.7, 6.2).
type RedactionState string

const (
	RedactionNone       RedactionState = "none"
	RedactionSummarized RedactionState = "summarized"
	RedactionRedacted   RedactionState = "redacted"
	RedactionHashed     RedactionState = "hashed"
	RedactionPrivileged RedactionState = "privileged"
)

// IsKnown reports whether r is one of the documented redaction states.
func (r RedactionState) IsKnown() bool {
	switch r {
	case RedactionNone, RedactionSummarized, RedactionRedacted, RedactionHashed, RedactionPrivileged:
		return true
	}
	return false
}

// Visibility controls whether privileged detail may leave the control-plane
// boundary for a record or query result (requirement 4.6, 6.5).
type Visibility string

const (
	VisibilityDefault    Visibility = "default"
	VisibilityPrivileged Visibility = "privileged"
)

// IsKnown reports whether v is one of the documented visibility values.
func (v Visibility) IsKnown() bool {
	switch v {
	case VisibilityDefault, VisibilityPrivileged:
		return true
	}
	return false
}

// EventID is the stable identity assigned by a backing store. It is opaque to
// consumers and stable within one configured backing store (requirement 1.7,
// 2.7). The zero EventID is unassigned.
type EventID struct {
	StoreID  string `json:"store_id"`
	Sequence int64  `json:"sequence"`
}

// IsZero reports whether the identity is unassigned.
func (id EventID) IsZero() bool { return id.StoreID == "" && id.Sequence == 0 }

// SourceRef identifies the originating diagnostic, observer, ledger, or store
// that supplied a record (requirement 3.5, 8.5). Consumers do not need to
// understand it to use the safe query contract (requirement 9.5).
type SourceRef struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Correlation carries the shared identifiers that let query consumers
// deduplicate and correlate evidence from auth, session, attempt, usage,
// policy, and audit paths (requirement 3.1, 3.7, 9.1). All fields are
// optional; unknown identifiers must be omitted rather than invented
// (requirement 3.6).
type Correlation struct {
	TraceID       string `json:"trace_id,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	ALegID        string `json:"a_leg_id,omitempty"`
	BLegID        string `json:"b_leg_id,omitempty"`
	AttemptSeq    int    `json:"attempt_seq,omitempty"`
	FrontendID    string `json:"frontend_id,omitempty"`
	BackendID     string `json:"backend_id,omitempty"`
	Model         string `json:"model,omitempty"`
	ParentTraceID string `json:"parent_trace_id,omitempty"`
}

// ScopeSnapshot preserves safe principal/scope attribution with flattened,
// presence-aware filter dimensions (requirement 4.1, 4.2, 4.3, 9.1). The
// embedded Principal view is the authoritative safe snapshot; the flattened
// Value fields are the queryable dimensions and preserve unknown vs
// known-empty state.
type ScopeSnapshot struct {
	Principal      scope.PrincipalScopeView `json:"principal"`
	PrincipalID    scope.Value              `json:"principal_id"`
	CredentialID   scope.Value              `json:"credential_id"`
	TenantID       scope.Value              `json:"tenant_id"`
	OrganizationID scope.Value              `json:"organization_id"`
	WorkspaceID    scope.Value              `json:"workspace_id"`
	ProjectID      scope.Value              `json:"project_id"`
	DepartmentID   scope.Value              `json:"department_id"`
	CostCenterID   scope.Value              `json:"cost_center_id"`
}

// Event is one normalized lifecycle evidence record with stable identity,
// category, correlation, safe scope, availability state, redaction state, and
// exactly one typed detail block (requirement 1.7, 1.8, 3.5, 4.1).
type Event struct {
	ID             EventID        `json:"id"`
	SourceEventKey string         `json:"source_event_key,omitempty"`
	Category       Category       `json:"category"`
	OccurredAt     time.Time      `json:"occurred_at"`
	RecordedAt     time.Time      `json:"recorded_at"`
	Correlation    Correlation    `json:"correlation"`
	Scope          ScopeSnapshot  `json:"scope"`
	Source         SourceRef      `json:"source"`
	Visibility     Visibility     `json:"visibility"`
	EvidenceState  EvidenceState  `json:"evidence_state"`
	RedactionState RedactionState `json:"redaction_state"`
	Summary        string         `json:"summary,omitempty"`

	// Detail carries exactly one typed detail block matching Category. Use the
	// typed accessors (Auth, Session, …) or assign a concrete *XDetail pointer.
	// JSON wire shape remains the historical category-keyed object via custom
	// marshaling (Go API v2-compatible; see package doc).
	Detail EventDetail `json:"-"`
}

// Validate performs structural invariant checks for an Event. It is the
// authoritative SDK-level guard used by core normalization and store adapters
// before persistence or query output (requirement 1.7, 3.5, 4.4, 4.5).
// It does not check source-specific detail semantics; those belong to the
// core normalizer.
//
// Event-timestamp contract (see validateTimestamps): OccurredAt and
// RecordedAt are mandatory lifecycle anchors and zero values are rejected.
//
// WindowStart/End/ResetAt on AccountingAuthorityDetail (and on the
// AccountingLimitStatusRow / AccountingDecisionRow query types) are
// intentionally NOT validated. A projector that emits an
// AccountingAuthorityDetail may not have access to the rule's window
// metadata (the rule snapshot is the authoritative source for window
// bounds). The zero time.Time value is the documented "no window" signal —
// see AccountingAuthorityDetail's godoc for the SDK consumer contract.
// Callers that need the actual window MUST query AccountingLimitStatus
// instead.
func (e Event) Validate() error {
	if !e.Category.IsKnown() {
		return errf("controlplane event: unknown category %q", e.Category)
	}
	if err := validateTimestamps(e); err != nil {
		return err
	}
	if !e.Visibility.IsKnown() {
		return errf("controlplane event: unknown visibility %q", e.Visibility)
	}
	if !e.EvidenceState.IsKnown() {
		return errf("controlplane event: unknown evidence state %q", e.EvidenceState)
	}
	if !e.RedactionState.IsKnown() {
		return errf("controlplane event: unknown redaction state %q", e.RedactionState)
	}
	if err := ensureSingleDetail(e); err != nil {
		return err
	}
	if err := ensureDetailMatchesCategory(e); err != nil {
		return err
	}
	if e.Visibility == VisibilityPrivileged && e.RedactionState != RedactionPrivileged {
		return errf("controlplane event: privileged visibility requires privileged redaction state")
	}
	return nil
}

// validateTimestamps enforces the event-timestamp contract and makes the
// asymmetry between required event timestamps and optional rule-window
// metadata explicit at the contract boundary. OccurredAt and RecordedAt are
// mandatory lifecycle anchors; both must be non-zero, and RecordedAt must
// not precede OccurredAt. Each is checked independently so the failure
// message identifies which timestamp is wrong.
//
// WindowStart/End/ResetAt on AccountingAuthorityDetail (and on the
// AccountingLimitStatusRow / AccountingDecisionRow query types) are
// intentionally NOT checked here. The zero time.Time is the documented
// "no window" signal — see the AccountingAuthorityDetail godoc for the
// SDK consumer contract. Projectors that don't have access to the rule's
// window metadata MUST be able to emit a valid event with those fields
// left as the zero value.
func validateTimestamps(e Event) error {
	if e.OccurredAt.IsZero() {
		return errf("controlplane event: occurred_at is required")
	}
	if e.RecordedAt.IsZero() {
		return errf("controlplane event: recorded_at is required")
	}
	if e.RecordedAt.Before(e.OccurredAt) {
		return errf("controlplane event: recorded_at precedes occurred_at")
	}
	return nil
}

// ensureSingleDetail verifies exactly one typed detail block is set. Detail
// fields are pointers; a typed nil pointer inside an `any` is not equal to
// untyped nil, so each field is checked directly rather than via a slice.
func ensureSingleDetail(e Event) error {
	if e.Detail == nil {
		return errf("controlplane event: exactly one detail block is required, got none")
	}
	return nil
}

// ensureDetailMatchesCategory verifies the set detail block matches the category.
func ensureDetailMatchesCategory(e Event) error {
	want := string(e.Category)
	switch e.Category {
	case CategoryAuth:
		want = "auth"
	case CategorySession:
		want = "session"
	case CategoryAttempt:
		want = "attempt"
	case CategoryUsage:
		want = "usage"
	case CategoryAccountingAuthority:
		want = "accounting_authority"
	case CategoryPolicy:
		want = "policy"
	case CategoryAudit:
		want = "audit"
	case CategoryLifecycle:
		want = "lifecycle"
	}
	if detailCategory(e.Detail) != e.Category {
		return errf("controlplane event: category %q requires %s detail", e.Category, want)
	}
	return nil
}

// RecordResult is the stable outcome of appending an Event to a store
// (requirement 1.7). Dedupe reports whether the source event key was a
// duplicate of an already-recorded fact.
type RecordResult struct {
	ID         EventID       `json:"id"`
	Dedupe     DedupeOutcome `json:"dedupe"`
	RecordedAt time.Time     `json:"recorded_at"`
}

// DedupeOutcome reports the idempotent projection outcome for a source event
// key (requirement 1.7, design "Idempotency").
type DedupeOutcome string

const (
	DedupeInserted  DedupeOutcome = "inserted"
	DedupeDuplicate DedupeOutcome = "duplicate"
)

// IsKnown reports whether d is one of the documented dedupe outcomes.
func (d DedupeOutcome) IsKnown() bool {
	switch d {
	case DedupeInserted, DedupeDuplicate:
		return true
	}
	return false
}

// errf wraps fmt.Errorf so invariant helpers share one formatting path.
func errf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
