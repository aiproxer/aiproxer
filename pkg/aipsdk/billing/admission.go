package billing

import (
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

const (
	// MaxExecutionLimits bounds finite candidate/work quantities per admission.
	MaxExecutionLimits = 128
	// MaxUnitReservations bounds non-monetary unit reservations per admission.
	MaxUnitReservations = 128
	// MaxAdmissionRoutes bounds the ordered planned-route facts carried by one
	// exposure admission.
	MaxAdmissionRoutes = 256
)

var (
	// ErrInvalidAdmission identifies a malformed exposure-admission DTO.
	ErrInvalidAdmission = errors.New("billing: invalid exposure admission")
	// ErrQuoteMismatch identifies a structurally valid quote that names a
	// different subject, store, or frozen snapshot than requested. It must
	// be rejected before atomic admission, never forwarded.
	ErrQuoteMismatch = errors.New("billing: quote does not match requested subject")
	// ErrAdmissionMismatch identifies a structurally valid handle that names
	// a different store, call, or quote than admitted. It cannot authorize
	// the requested call.
	ErrAdmissionMismatch = errors.New("billing: admission handle does not match request")
)

// AdmissionRouteFact is one planned route's backend identity: the configured
// backend instance the route would use and the factory kind that instance
// resolves to.
//
// The kind is empty when the host could not resolve a concrete kind for the
// instance. An empty kind is never an assumption: a consumer that needs the
// kind must fail closed for that route instead of guessing one. The fact
// carries metadata only; it is not a price, a rating, or a policy.
type AdmissionRouteFact struct {
	// BackendInstanceID is the configured backend instance the planned route
	// occurrence would use.
	BackendInstanceID string `json:"backend_instance_id"`
	// BackendKind is the factory kind that instance resolves to, or empty
	// when the host resolved no concrete kind for it.
	BackendKind string `json:"backend_kind,omitempty"`
}

// Validate requires a backend instance identity and a bounded optional kind.
func (f AdmissionRouteFact) Validate() error {
	if err := validateRef("admission route backend instance id", f.BackendInstanceID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAdmission, err)
	}
	if err := validateOptionalRef("admission route backend kind", f.BackendKind, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAdmission, err)
	}
	return nil
}

// Clone returns an independent copy of the fact.
func (f AdmissionRouteFact) Clone() AdmissionRouteFact { return f }

// cloneAdmissionRouteFacts returns an independent copy of the ordered facts.
func cloneAdmissionRouteFacts(in []AdmissionRouteFact) []AdmissionRouteFact {
	if in == nil {
		return nil
	}
	out := make([]AdmissionRouteFact, len(in))
	copy(out, in)
	return out
}

// ExposureAdmissionInput carries one frozen quote, the call identity, the
// trusted customer scope and credited account, finite execution limits, and
// unit reservations for atomic exposure admission.
type ExposureAdmissionInput struct {
	Subject          metering.SubjectRef      `json:"subject"`
	Scope            scope.PrincipalScopeView `json:"scope"`
	AccountID        string                   `json:"account_id"`
	Quote            economics.ExposureQuote  `json:"quote"`
	ExecutionLimits  []economics.Limit        `json:"execution_limits"`
	UnitReservations []economics.UnitBound    `json:"unit_reservations,omitempty"`
	// Routes carries the ordered planned-route facts of the route plan this
	// admission prices: one entry per planned route occurrence, in the shared
	// planned-leaf order, duplicates included. The list is optional: an
	// admission with no route facts stays valid, and a consumer that needs
	// them must not invent them.
	Routes []AdmissionRouteFact `json:"routes,omitempty"`
}

// Validate requires call identity, trusted customer scope and credited
// account for per-customer allowance binding, a frozen quote bound to
// exactly the requested subject, and at least one finite execution limit.
func (in ExposureAdmissionInput) Validate() error {
	if err := in.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: subject: %v", ErrInvalidAdmission, err)
	}
	if !in.Scope.PrincipalID.IsKnown() {
		return fmt.Errorf("%w: trusted customer scope with known principal required", ErrInvalidAdmission)
	}
	if err := validateRef("admission account_id", in.AccountID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAdmission, err)
	}
	if err := in.Quote.Validate(); err != nil {
		return fmt.Errorf("%w: quote: %v", ErrInvalidAdmission, err)
	}
	if !quoteSubjectMatches(in.Quote.Subject, in.Subject) {
		return fmt.Errorf("%w: quote subject %q/%q/%q differs from requested %q/%q/%q: %w",
			ErrInvalidAdmission,
			in.Quote.Subject.Kind, in.Quote.Subject.StoreID, in.Quote.Subject.BillingCallID,
			in.Subject.Kind, in.Subject.StoreID, in.Subject.BillingCallID,
			ErrQuoteMismatch)
	}
	if len(in.ExecutionLimits) == 0 {
		return fmt.Errorf("%w: at least one finite execution limit required", ErrInvalidAdmission)
	}
	if len(in.ExecutionLimits) > MaxExecutionLimits {
		return fmt.Errorf("%w: execution limit bound exceeded", ErrInvalidAdmission)
	}
	seen := make(map[string]struct{}, len(in.ExecutionLimits))
	for i, limit := range in.ExecutionLimits {
		if err := limit.Validate(); err != nil {
			return fmt.Errorf("%w: execution limit %d: %v", ErrInvalidAdmission, i, err)
		}
		key := limit.Name + "\x00" + limit.Unit
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: duplicate execution limit %q", ErrInvalidAdmission, limit.Name)
		}
		seen[key] = struct{}{}
	}
	if len(in.UnitReservations) > MaxUnitReservations {
		return fmt.Errorf("%w: unit reservation bound exceeded", ErrInvalidAdmission)
	}
	if len(in.Routes) > MaxAdmissionRoutes {
		return fmt.Errorf("%w: admission route bound exceeded", ErrInvalidAdmission)
	}
	for i, route := range in.Routes {
		if err := route.Validate(); err != nil {
			return fmt.Errorf("%w: route %d: %v", ErrInvalidAdmission, i, err)
		}
	}
	seenUnits := make(map[string]struct{}, len(in.UnitReservations))
	for i, bound := range in.UnitReservations {
		if err := bound.Validate(); err != nil {
			return fmt.Errorf("%w: unit reservation %d: %v", ErrInvalidAdmission, i, err)
		}
		if _, ok := seenUnits[bound.Unit]; ok {
			return fmt.Errorf("%w: duplicate unit reservation %q", ErrInvalidAdmission, bound.Unit)
		}
		seenUnits[bound.Unit] = struct{}{}
	}
	return nil
}

// Clone returns an independent copy of the input.
func (in ExposureAdmissionInput) Clone() ExposureAdmissionInput {
	out := in
	out.Subject = in.Subject.Clone()
	out.Scope = in.Scope.Clone()
	out.Quote = in.Quote.Clone()
	out.ExecutionLimits = append([]economics.Limit(nil), in.ExecutionLimits...)
	if in.UnitReservations != nil {
		out.UnitReservations = make([]economics.UnitBound, len(in.UnitReservations))
		for i, bound := range in.UnitReservations {
			out.UnitReservations[i] = bound.Clone()
		}
	}
	out.Routes = cloneAdmissionRouteFacts(in.Routes)
	return out
}

// ExposureHandle is the admitted-exposure identity returned by atomic
// admission. It carries store-scoped IDs only.
type ExposureHandle struct {
	StoreID       string `json:"store_id"`
	BillingCallID string `json:"billing_call_id"`
	ExposureID    string `json:"exposure_id"`
	QuoteID       string `json:"quote_id"`
}

// Validate requires complete store-scoped exposure identity.
func (h ExposureHandle) Validate() error {
	if err := validateRef("exposure store_id", h.StoreID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAdmission, err)
	}
	if err := validateRef("exposure billing_call_id", h.BillingCallID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAdmission, err)
	}
	if err := validateRef("exposure id", h.ExposureID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAdmission, err)
	}
	if err := validateRef("exposure quote_id", h.QuoteID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAdmission, err)
	}
	return nil
}

// Clone returns an independent copy of the handle.
func (h ExposureHandle) Clone() ExposureHandle { return h }

// MatchesAdmission reports whether the handle authorizes exactly the
// admitted request: store and call must equal the admission subject, and a
// carried quote ID must equal the admitted quote. The quote-ID check applies
// only when the quote itself carries an ID.
func (h ExposureHandle) MatchesAdmission(in ExposureAdmissionInput) bool {
	if h.StoreID != in.Subject.StoreID || h.BillingCallID != in.Subject.BillingCallID {
		return false
	}
	if in.Quote.ID != "" && h.QuoteID != in.Quote.ID {
		return false
	}
	return true
}

// quoteSubjectMatches reports whether the frozen quote names exactly the
// requested call subject: kind, store, and BillingCallID must all agree.
// Ancillary lineage may differ; identity may not.
func quoteSubjectMatches(quote, requested metering.SubjectRef) bool {
	return quote.Kind == requested.Kind &&
		quote.StoreID == requested.StoreID &&
		quote.BillingCallID == requested.BillingCallID
}
