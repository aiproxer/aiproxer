package metering

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// ErrInvalidFact classifies public fact validation failures (D5/D14-safe messages).
var ErrInvalidFact = errors.New("metering: invalid fact")

// MoneyObservation is an optional monetary observation attached to a fact.
// It is intentionally independent of pkg/aipsdk/economics.Money so metering
// does not import economics (import DAG: authority → economics → metering).
type MoneyObservation struct {
	NanoUnits int64  `json:"nano_units"`
	Currency  string `json:"currency,omitempty"`
	Present   bool   `json:"present"`
	Source    Source `json:"source,omitempty"`
}

// Fact is one idempotent metering journal record (requirements 3.1–3.5, 13.2).
//
// Idempotency: the same FactID within a StreamID must be treated as the same
// fact. Replaying Append with SameFactReplay is a no-op at the store; a
// different Sequence, Kind, or double-count-sensitive payload for the same
// FactID is a contract violation for store implementations to reject.
type Fact struct {
	FactID   string `json:"fact_id"`
	StreamID string `json:"stream_id"`
	// Sequence is the producer-stable ordinal within StreamID. It is not part of
	// SourceEventKey; producers must keep it stable across retry/restart for the
	// same stream membership (design Deterministic Identity).
	Sequence        int64                    `json:"sequence"`
	IdentityVersion int                      `json:"identity_version,omitempty"`
	SourceRevision  int64                    `json:"source_revision,omitempty"`
	SourceEventKind string                   `json:"source_event_kind,omitempty"`
	SourceID        string                   `json:"source_id,omitempty"`
	Kind            FactKind                 `json:"kind"`
	Perspective     EconomicPerspective      `json:"perspective"`
	Boundary        Boundary                 `json:"boundary"`
	Lifecycle       LifecycleScope           `json:"lifecycle"`
	Correlation     Correlation              `json:"correlation"`
	Scope           scope.PrincipalScopeView `json:"scope"`
	FrontendID      string                   `json:"frontend_id,omitempty"`
	BackendID       string                   `json:"backend_id,omitempty"`
	Model           string                   `json:"model,omitempty"`
	AttemptOutcome  AttemptOutcome           `json:"attempt_outcome,omitempty"`
	Surfaced        SurfacedState            `json:"surfaced,omitempty"`
	Quantities      []Quantity               `json:"quantities,omitempty"`
	Money           *MoneyObservation        `json:"money,omitempty"`
	Source          Source                   `json:"source"`
	Authority       Authority                `json:"authority"`
	Presence        Presence                 `json:"presence"`
	Supersedes      []string                 `json:"supersedes,omitempty"`
	PolicyVersion   VersionRef               `json:"policy_version,omitzero"`
	RecordedAt      time.Time                `json:"recorded_at"`
}

// IdempotencyKey returns the stable journal key for this fact identity
// (requirement 3.1): StreamID + FactID. It intentionally excludes quantities,
// money, and other payload fields so replays with identical identity collide.
// Sequence is checked separately via SameFactIdentity for ordered stream membership;
// payload equality for idempotent replay is SameFactReplay.
func (f Fact) IdempotencyKey() string {
	return strings.TrimSpace(f.StreamID) + "\x00" + strings.TrimSpace(f.FactID)
}

// SourceEventKey returns the canonical SourceEventRef encoding (design
// Deterministic Identity / D6). Store uniqueness scopes this key by logical
// store_id. RecordedAt, Sequence, FactID (when SourceID is set), quantities,
// and money are excluded so retries stay stable.
func (f Fact) SourceEventKey() string {
	return f.SourceEventRef().CanonicalKey()
}

// SameFactIdentity reports whether a and b share FactID, StreamID, and Sequence
// (ordered stream membership). Matching identity alone is not sufficient for an
// idempotent Append replay; stores also require SameFactReplay content equality.
func SameFactIdentity(a, b Fact) bool {
	return strings.TrimSpace(a.FactID) == strings.TrimSpace(b.FactID) &&
		strings.TrimSpace(a.StreamID) == strings.TrimSpace(b.StreamID) &&
		a.Sequence == b.Sequence
}

// SameFactReplay reports whether a and b share stream membership
// (SameFactIdentity) and equal semantic payloads. Quantities and Supersedes
// compare as multisets (order-independent). RecordedAt is intentionally
// excluded because journal stores assign it when producers omit it; that
// store-assigned timestamp does not change the producer fact being replayed.
func SameFactReplay(a, b Fact) bool {
	if !SameFactIdentity(a, b) {
		return false
	}
	aRef, bRef := a.SourceEventRef(), b.SourceEventRef()
	if a.Kind != b.Kind ||
		aRef.EffectiveIdentityVersion() != bRef.EffectiveIdentityVersion() ||
		a.SourceRevision != b.SourceRevision ||
		aRef.EventKind != bRef.EventKind ||
		aRef.SourceID != bRef.SourceID ||
		a.Perspective != b.Perspective ||
		a.Boundary != b.Boundary ||
		a.Lifecycle != b.Lifecycle ||
		a.Correlation != b.Correlation ||
		a.FrontendID != b.FrontendID ||
		a.BackendID != b.BackendID ||
		a.Model != b.Model ||
		a.AttemptOutcome != b.AttemptOutcome ||
		a.Surfaced != b.Surfaced ||
		a.Source != b.Source ||
		a.Authority != b.Authority ||
		a.Presence != b.Presence ||
		a.PolicyVersion != b.PolicyVersion ||
		!scopeViewsEqual(a.Scope, b.Scope) {
		return false
	}
	if !quantitiesEqual(a.Quantities, b.Quantities) {
		return false
	}
	if !moneyEqual(a.Money, b.Money) {
		return false
	}
	if !stringSetEqual(a.Supersedes, b.Supersedes) {
		return false
	}
	return true
}

func scopeViewsEqual(a, b scope.PrincipalScopeView) bool {
	return a.SubjectKind == b.SubjectKind &&
		a.PrincipalID == b.PrincipalID &&
		a.DisplayName == b.DisplayName &&
		a.AuthMethod == b.AuthMethod &&
		a.CredentialID == b.CredentialID &&
		slices.Equal(a.Roles, b.Roles) &&
		maps.Equal(a.SafeClaims, b.SafeClaims) &&
		a.TenantID == b.TenantID &&
		a.OrganizationID == b.OrganizationID &&
		a.WorkspaceID == b.WorkspaceID &&
		a.ProjectID == b.ProjectID &&
		a.DepartmentID == b.DepartmentID &&
		a.CostCenterID == b.CostCenterID &&
		maps.Equal(a.PolicyLabels, b.PolicyLabels) &&
		a.Origin == b.Origin &&
		a.ParentTraceID == b.ParentTraceID
}

func quantitiesEqual(a, b []Quantity) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]Quantity(nil), a...)
	bc := append([]Quantity(nil), b...)
	slices.SortFunc(ac, quantityCmp)
	slices.SortFunc(bc, quantityCmp)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

func quantityCmp(a, b Quantity) int {
	if a.Component != b.Component {
		if a.Component < b.Component {
			return -1
		}
		return 1
	}
	if a.Unit != b.Unit {
		if a.Unit < b.Unit {
			return -1
		}
		return 1
	}
	if a.Value != b.Value {
		if a.Value < b.Value {
			return -1
		}
		return 1
	}
	if a.Present != b.Present {
		if !a.Present && b.Present {
			return -1
		}
		return 1
	}
	if a.Schema < b.Schema {
		return -1
	}
	if a.Schema > b.Schema {
		return 1
	}
	return 0
}

func moneyEqual(a, b *MoneyObservation) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.NanoUnits == b.NanoUnits &&
		a.Currency == b.Currency &&
		a.Present == b.Present &&
		a.Source == b.Source
}

func stringSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	slices.Sort(ac)
	slices.Sort(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

// Validate checks required identity and enum fields, quantity/money presence,
// and supersession rules for correction/replacement kinds (requirements 5.5–5.7,
// 6.1–6.4; design D2, D5, D6, D14). Failures wrap ErrInvalidFact.
func (f Fact) Validate() error {
	if strings.TrimSpace(f.FactID) == "" {
		return fmt.Errorf("%w: fact_id required", ErrInvalidFact)
	}
	if strings.TrimSpace(f.StreamID) == "" {
		return fmt.Errorf("%w: stream_id required", ErrInvalidFact)
	}
	if f.Sequence < 0 {
		return fmt.Errorf("%w: sequence must be non-negative", ErrInvalidFact)
	}
	if f.IdentityVersion < 0 {
		return fmt.Errorf("%w: identity_version must be non-negative", ErrInvalidFact)
	}
	if f.SourceRevision < 0 {
		return fmt.Errorf("%w: source_revision must be non-negative", ErrInvalidFact)
	}
	ref := f.SourceEventRef()
	if err := validateSourceEventField("lifecycle_id", ref.LifecycleID); err != nil {
		return err
	}
	if err := validateSourceEventField("source_event_kind", ref.EventKind); err != nil {
		return err
	}
	if err := validateSourceEventField("source_id", ref.SourceID); err != nil {
		return err
	}
	if err := f.Kind.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFact, err)
	}
	if err := f.Perspective.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFact, err)
	}
	if err := f.Boundary.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFact, err)
	}
	if err := f.Lifecycle.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFact, err)
	}
	if err := f.validatePerspectiveBoundaryLifecycle(); err != nil {
		return err
	}
	if err := f.Source.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFact, err)
	}
	if err := f.Authority.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFact, err)
	}
	if err := f.Presence.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFact, err)
	}
	if f.Surfaced != "" {
		if err := f.Surfaced.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidFact, err)
		}
	}
	if f.AttemptOutcome != "" {
		if err := f.AttemptOutcome.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidFact, err)
		}
	}
	allowNegative := f.Kind == FactKindCorrection
	for i, q := range f.Quantities {
		if err := q.Validate(); err != nil {
			return fmt.Errorf("%w: quantities[%d]: %v", ErrInvalidFact, i, err)
		}
		if !q.Present {
			if q.Value != 0 {
				return fmt.Errorf("%w: quantities[%d]: absent quantity must have zero value", ErrInvalidFact, i)
			}
			continue
		}
		if q.Value < 0 && !allowNegative {
			return fmt.Errorf("%w: quantities[%d]: negative value only allowed for corrections", ErrInvalidFact, i)
		}
	}
	if err := validateMoneyObservation(f.Money, allowNegative); err != nil {
		return err
	}
	if f.Kind.RequiresSupersedes() {
		if len(f.Supersedes) == 0 {
			return fmt.Errorf("%w: kind %q requires non-empty supersedes", ErrInvalidFact, f.Kind)
		}
		self := strings.TrimSpace(f.FactID)
		for i, id := range f.Supersedes {
			id = strings.TrimSpace(id)
			if id == "" {
				return fmt.Errorf("%w: supersedes[%d] empty", ErrInvalidFact, i)
			}
			if id == self {
				return fmt.Errorf("%w: supersedes must not include self", ErrInvalidFact)
			}
		}
	} else if len(f.Supersedes) > 0 {
		return fmt.Errorf("%w: kind %q must not set supersedes", ErrInvalidFact, f.Kind)
	}
	return nil
}

func validateMoneyObservation(m *MoneyObservation, allowNegative bool) error {
	if m == nil {
		return nil
	}
	if !m.Present {
		if m.NanoUnits != 0 {
			return fmt.Errorf("%w: absent money must have zero nano_units", ErrInvalidFact)
		}
		return nil
	}
	if strings.TrimSpace(m.Currency) == "" {
		return fmt.Errorf("%w: present money requires currency", ErrInvalidFact)
	}
	if m.NanoUnits < 0 && !allowNegative {
		return fmt.Errorf("%w: negative money only allowed for corrections", ErrInvalidFact)
	}
	if m.Source != "" {
		if err := m.Source.Validate(); err != nil {
			return fmt.Errorf("%w: money source: %v", ErrInvalidFact, err)
		}
	}
	return nil
}

func (f Fact) validatePerspectiveBoundaryLifecycle() error {
	switch f.Perspective {
	case PerspectiveCustomer:
		switch f.Boundary {
		case BoundaryFrontendIngress, BoundaryFrontendEgress:
		default:
			return fmt.Errorf("%w: customer perspective requires frontend boundary, got %q", ErrInvalidFact, f.Boundary)
		}
		if f.Lifecycle != LifecycleLogicalRequest {
			return fmt.Errorf("%w: customer perspective requires logical_request lifecycle, got %q", ErrInvalidFact, f.Lifecycle)
		}
	case PerspectiveOperator:
		switch f.Boundary {
		case BoundaryBackendIngress, BoundaryBackendEgress:
		default:
			return fmt.Errorf("%w: operator perspective requires backend boundary, got %q", ErrInvalidFact, f.Boundary)
		}
		switch f.Lifecycle {
		case LifecycleBackendAttempt, LifecycleAuxiliaryRequest:
		default:
			return fmt.Errorf("%w: operator perspective requires backend_attempt or auxiliary_request lifecycle, got %q", ErrInvalidFact, f.Lifecycle)
		}
	case PerspectiveNone:
		// Technical metrics are not bound to customer/operator boundary rules.
	}
	return nil
}
