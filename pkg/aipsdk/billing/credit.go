package billing

import (
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

var (
	// ErrInvalidCreditScreen identifies a malformed credit-screen DTO.
	ErrInvalidCreditScreen = errors.New("billing: invalid credit screen")
	// ErrCreditResultMismatch identifies a structurally valid result that
	// answers a different store or account. It is never a decision for the
	// requested account, whatever its allow/deny value.
	ErrCreditResultMismatch = errors.New("billing: credit result does not match request")
)

// CreditDecision is the typed outcome of the cheap pre-route credit screen.
type CreditDecision string

const (
	// CreditAllow permits route planning to proceed.
	CreditAllow CreditDecision = "allow"
	// CreditDeny stops the request before provider execution.
	CreditDeny CreditDecision = "deny"
	// CreditDegraded permits execution with explicitly reduced guarantees.
	CreditDegraded CreditDecision = "degraded"
)

// IsKnown reports whether d is a documented credit decision.
func (d CreditDecision) IsKnown() bool {
	switch d {
	case CreditAllow, CreditDeny, CreditDegraded:
		return true
	default:
		return false
	}
}

// CreditScreenInput carries the trusted customer scope, the account reference,
// and the frozen policy version for one cheap pre-route credit check.
type CreditScreenInput struct {
	Scope     scope.PrincipalScopeView    `json:"scope"`
	StoreID   string                      `json:"store_id"`
	AccountID string                      `json:"account_id"`
	Policy    economics.PolicySnapshotRef `json:"policy"`
}

// Validate requires trusted scope, store/account identity, and policy version.
func (in CreditScreenInput) Validate() error {
	if !in.Scope.PrincipalID.IsKnown() {
		return fmt.Errorf("%w: trusted customer scope with known principal required", ErrInvalidCreditScreen)
	}
	if err := validateRef("credit store_id", in.StoreID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCreditScreen, err)
	}
	if err := validateRef("credit account_id", in.AccountID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCreditScreen, err)
	}
	if err := validateSnapshotRef("credit policy", in.Policy.ID, in.Policy.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCreditScreen, err)
	}
	if err := validateRef("credit policy_id", in.Policy.PolicyID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCreditScreen, err)
	}
	return nil
}

// Clone returns an independent copy of the input.
func (in CreditScreenInput) Clone() CreditScreenInput {
	out := in
	out.Scope = in.Scope.Clone()
	return out
}

// CreditScreenResult is the typed allow/deny/degraded outcome. Reason is a
// bounded human-readable label, never raw account state.
type CreditScreenResult struct {
	Decision  CreditDecision `json:"decision"`
	Reason    string         `json:"reason,omitempty"`
	StoreID   string         `json:"store_id"`
	AccountID string         `json:"account_id"`
}

// Validate requires a known decision and store/account identity.
func (r CreditScreenResult) Validate() error {
	if !r.Decision.IsKnown() {
		return fmt.Errorf("%w: unknown decision %q", ErrInvalidCreditScreen, r.Decision)
	}
	if err := validateRef("credit result store_id", r.StoreID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCreditScreen, err)
	}
	if err := validateRef("credit result account_id", r.AccountID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCreditScreen, err)
	}
	if r.Reason != "" {
		if err := validateRef("credit result reason", r.Reason, MaxReasonBytes); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidCreditScreen, err)
		}
	}
	return nil
}

// Clone returns an independent copy of the result.
func (r CreditScreenResult) Clone() CreditScreenResult { return r }

// MatchesInput reports whether the result answers exactly the submitted
// store and account. The decision value is irrelevant to identity.
func (r CreditScreenResult) MatchesInput(in CreditScreenInput) bool {
	return r.StoreID == in.StoreID && r.AccountID == in.AccountID
}
