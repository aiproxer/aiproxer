package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

const (
	// BindingVersionV1 is the first version of the external billing binding.
	BindingVersionV1 uint32 = 1
)

var (
	// ErrInvalidBinding identifies a malformed binding envelope.
	ErrInvalidBinding = errors.New("billing: invalid binding")
	// ErrInvalidBindingID identifies a missing or malformed binding ID.
	ErrInvalidBindingID = errors.New("billing: invalid binding ID")
	// ErrUnsupportedBindingVersion identifies an unknown binding version.
	ErrUnsupportedBindingVersion = errors.New("billing: unsupported binding version")
	// ErrMissingCreditScreen identifies an absent credit-screen port.
	ErrMissingCreditScreen = errors.New("billing: credit screen port is required")
	// ErrMissingQuoter identifies an absent quote port.
	ErrMissingQuoter = errors.New("billing: quoter port is required")
	// ErrMissingAdmission identifies an absent exposure-admission port.
	ErrMissingAdmission = errors.New("billing: exposure admission port is required")
	// ErrMissingTerminal identifies an absent terminal handoff port.
	ErrMissingTerminal = errors.New("billing: terminal port is required")
	// ErrDuplicateMonetaryBinding rejects a second monetary binding. Ordinary
	// non-money authority registrations may coexist for distinct quotas, but
	// they must never create a second monetary authority.
	ErrDuplicateMonetaryBinding = errors.New("billing: duplicate monetary binding")
	// ErrMissingAccountResolver identifies a supplied identity resolver that
	// carries no account callback. Such a resolver is unusable: the host
	// never substitutes a principal-derived account for it.
	ErrMissingAccountResolver = errors.New("billing: identity resolver requires an account callback")
)

// IdentityResolver carries the optional request-identity resolvers a binding
// owner supplies. The resolvers are the owner's authority: the host invokes
// them at the request boundary and carries their returned values unchanged.
//
// A nil resolver means no identity was supplied and the host keeps its stock
// trusted-principal account mapping and fixed binding store identity. A
// supplied resolver without an account callback is unusable and is refused
// before publication.
//
// The account resolver receives the actual request, so one principal may be
// credited to different accounts depending on the request content and
// metadata. The host never reorders, reinterprets or substitutes the resolved
// value.
type IdentityResolver struct {
	// AccountID resolves the credited billing account for one request. An
	// empty result stays missing; the host never replaces it with a
	// principal-derived account.
	AccountID func(context.Context, aipapi.Call) string
	// StoreID resolves the trusted durable store identity for one request. It
	// stays separate from the binding's fixed store identity: an explicitly
	// supplied result is preserved unchanged, and only an absent resolver is
	// filled from the authoritative store.
	StoreID func(context.Context) string
}

// Binding is the minimal typed external monetary host binding. It carries a
// stable identity and version, complete cheap-screen/quote-admit/terminal
// ports, the optional provider-billable maintenance attachment, and explicit
// owned-resource lifecycle registration. Public rater and
// statement/reconciliation ports stay separate in pkg/aipsdk/economics;
// workers consume them after terminal persistence, not through this binding.
type Binding struct {
	ID           string
	Version      uint32
	Scope        BindingScope
	CreditScreen CreditScreener
	Quoter       economics.Quoter
	Admission    ExposureAdmitter
	Terminal     TerminalSink
	// Maintenance is the optional provider-billable maintenance accounting
	// attachment. Nil keeps legacy version-1 behavior (no accounting) and is
	// never an error; one observer owns delivery of provider maintenance
	// usage. The field is additive: a version-1 binding without Maintenance
	// remains valid.
	Maintenance MaintenanceObserver
	Lifecycle   Lifecycle
	// IdentityResolver optionally supplies the owner's request-identity
	// resolvers. Nil keeps the stock trusted-principal account mapping and the
	// fixed binding store identity. A supplied resolver is preserved
	// unchanged by the host; the field is additive, so a version-1 binding
	// without it remains valid.
	IdentityResolver *IdentityResolver
}

// IsMonetary reports that the binding owns the monetary authority. It lets a
// future host composition reject a second monetary binding while ordinary
// non-money authority registrations coexist for distinct quotas.
func (b Binding) IsMonetary() bool { return true }

// Validate rejects incomplete bindings before publication: invalid ID or
// version, nil or typed-nil monetary ports, and invalid lifecycle
// declarations.
func (b Binding) Validate() error {
	if err := validateRef("binding id", b.ID, MaxBindingIDLength); err != nil {
		return fmt.Errorf("%w: %v: %w", ErrInvalidBinding, err, ErrInvalidBindingID)
	}
	if b.Version != BindingVersionV1 {
		return fmt.Errorf("%w: version %d: %w", ErrInvalidBinding, b.Version, ErrUnsupportedBindingVersion)
	}
	if err := b.Scope.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidBinding, err)
	}
	if isNilValue(b.CreditScreen) {
		return fmt.Errorf("%w: %w", ErrInvalidBinding, ErrMissingCreditScreen)
	}
	if isNilValue(b.Quoter) {
		return fmt.Errorf("%w: %w", ErrInvalidBinding, ErrMissingQuoter)
	}
	if isNilValue(b.Admission) {
		return fmt.Errorf("%w: %w", ErrInvalidBinding, ErrMissingAdmission)
	}
	if isNilValue(b.Terminal) {
		return fmt.Errorf("%w: %w", ErrInvalidBinding, ErrMissingTerminal)
	}
	// Maintenance is optional; a non-nil interface holding a typed-nil
	// observer is rejected so the host never installs a silently-dead seam.
	if b.Maintenance != nil && isNilValue(b.Maintenance) {
		return fmt.Errorf("%w: maintenance observer is typed-nil", ErrInvalidBinding)
	}
	// A supplied identity resolver is the owner's authority. A resolver
	// without an account callback is unusable and is refused here, before
	// the binding is published.
	if b.IdentityResolver != nil && b.IdentityResolver.AccountID == nil {
		return fmt.Errorf("%w: %w", ErrInvalidBinding, ErrMissingAccountResolver)
	}
	if err := b.Lifecycle.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidBinding, err)
	}
	return nil
}

// ValidateBindings validates each candidate binding and enforces the
// single-monetary-authority rule: at most one monetary binding may be
// published. An empty set is valid and keeps the host non-money.
func ValidateBindings(bindings []Binding) error {
	monetary := 0
	for i, binding := range bindings {
		if err := binding.Validate(); err != nil {
			return fmt.Errorf("billing: binding %d: %w", i, err)
		}
		if binding.IsMonetary() {
			monetary++
		}
	}
	if monetary > 1 {
		return fmt.Errorf("%w: %d monetary bindings", ErrDuplicateMonetaryBinding, monetary)
	}
	return nil
}
