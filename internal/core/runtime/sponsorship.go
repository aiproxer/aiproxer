package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
)

var (
	// ErrSponsorshipUndeclared refuses background work whose provider call is
	// payable when the operator declared no funding decision. The proxy never
	// silently enables a free provider call: without an explicit operator
	// choice the work is refused before any provider open.
	ErrSponsorshipUndeclared = errors.New("executor: background funding mode is not declared")
	// ErrSponsorshipSponsorUndeclared refuses a background operation whose
	// selected sponsor key is not one the operator declared. A sponsor key
	// names a sponsor, never an account.
	ErrSponsorshipSponsorUndeclared = errors.New("executor: background sponsor key is not declared")
	// ErrSponsorshipMonetaryHostIncomplete refuses customer-funded background
	// work on a host that does not carry a complete monetary binding. Without
	// the monetary chokepoints there is no account to charge and no customer
	// settlement to produce, so the work must not reach a provider.
	ErrSponsorshipMonetaryHostIncomplete = errors.New("executor: customer-funded background work requires a complete monetary host binding")
)

// admitBackgroundSponsorship applies the operator-declared funding decision to
// one operation before any provider dispatch.
//
// The decision is refused, and counted, when the operator declared no funding
// mode, when the selected sponsor key is not one the operator declared, or
// when a customer-funded decision meets a host without a complete monetary
// binding. A refused operation never becomes a financial operation: the
// refusal happens before the billing call identity is minted, so no exposure
// is admitted and no customer record exists.
func (e *Executor) admitBackgroundSponsorship(ctx context.Context) error {
	decl, ok := execctx.SponsorshipFromContext(ctx)
	if !ok {
		return nil
	}
	if !decl.ModeDeclared() {
		// Without any monetary binding no money can move, so the work runs
		// usage-only exactly as it does on a build without billing. Where a
		// monetary binding is bound, an undeclared mode would let the
		// ordinary billing path charge a customer silently, so it is refused.
		if !e.monetaryBindingPresent() {
			return nil
		}
		return e.refuseBackgroundSponsorship(ctx, ReasonSponsorshipUndeclared, ErrSponsorshipUndeclared)
	}
	if !decl.SponsorKeyDeclared() {
		return e.refuseBackgroundSponsorship(ctx, ReasonSponsorshipSponsorUndeclared, ErrSponsorshipSponsorUndeclared)
	}
	switch decl.Mode {
	case execctx.SponsorshipModeCustomerFunded:
		ready, reason := e.monetaryHostReady()
		if !ready {
			return e.refuseBackgroundSponsorship(ctx, reason, ErrSponsorshipMonetaryHostIncomplete)
		}
		return nil
	case execctx.SponsorshipModeOperatorSponsored:
		return nil
	default:
		return e.refuseBackgroundSponsorship(ctx, ReasonSponsorshipUndeclared, ErrSponsorshipUndeclared)
	}
}

// operatorSponsoredWork reports whether the operator sponsors the operation
// carried by ctx.
//
// The trusted context is the carrier of the funding decision, so the decision
// is read from the same immutable value the gate validated: an
// operator-sponsored leg is metered, and no customer money moves.
func operatorSponsoredWork(ctx context.Context) bool {
	decl, ok := execctx.SponsorshipFromContext(ctx)
	return ok && decl.Mode == execctx.SponsorshipModeOperatorSponsored
}

// refuseBackgroundSponsorship records and logs one bounded refusal. The log
// record names the reason only: never a principal, an account, a sponsor or
// any request content.
func (e *Executor) refuseBackgroundSponsorship(ctx context.Context, reason string, err error) error {
	e.recordBackgroundSponsorshipRefused(reason)
	if e != nil && e.Log != nil {
		e.Log.WarnContext(ctx, "background funding refused", "event", "background_sponsorship_refused", "reason", reason)
	}
	return fmt.Errorf("%w: %w", err, ErrBillingAdmissionDenied)
}

// monetaryHostReady reports whether this executor is wired to a complete,
// validated monetary host whose terminal handoff can durably hold an
// obligation.
//
// The capability is the host composition's own evidence: it is published only
// where the host validated a complete monetary authority (the external
// binding adapter, or the internal reference money root), so a wired set of
// no-op or partial ports can never pass as a monetary host. The wired seams
// are checked as well, so a published capability without the ports still
// refuses.
//
// The terminal handoff is the durable half: the host must state that its
// terminal usage sink commits the obligation before it acknowledges the
// append, and the handoff must be ready right now. A bounded durable holder
// that is full or closed refuses new payable work instead of accepting an
// obligation it cannot hold.
func (e *Executor) monetaryHostReady() (bool, string) {
	if e == nil {
		return false, ReasonSponsorshipMonetaryHostIncomplete
	}
	if strings.TrimSpace(string(e.BillingIdentity.MonetaryHost.Authority)) == "" ||
		e.BillingIdentity.MonetaryHost.TerminalHandoff != TerminalHandoffDurable {
		return false, ReasonSponsorshipMonetaryHostIncomplete
	}
	if e.BillingCreditGate == nil || e.BillingExposureAdmission == nil ||
		e.BillingIdentity.AccountID == nil || e.TerminalUsageSink == nil {
		return false, ReasonSponsorshipMonetaryHostIncomplete
	}
	return e.terminalHandoffReady()
}

// terminalHandoffReady consults the terminal usage sink's live durable
// handoff. A sink that implements no durable handoff cannot hold a completed
// obligation, and a handoff that is not ready right now must not accept a
// new one.
func (e *Executor) terminalHandoffReady() (bool, string) {
	if e == nil || e.TerminalUsageSink == nil {
		return false, ReasonSponsorshipHandoffNotReady
	}
	handoff, ok := e.TerminalUsageSink.(billingport.DurableTerminalHandoff)
	if !ok {
		return false, ReasonSponsorshipHandoffNotReady
	}
	ready, _ := handoff.TerminalHandoffReady()
	if !ready {
		return false, ReasonSponsorshipHandoffNotReady
	}
	return true, ""
}

// monetaryBindingPresent reports whether any part of a monetary binding is
// bound to this executor: a declared monetary authority, a credit screen, an
// exposure admission or an account resolver. A build without billing has none
// of them, so no customer money can move there.
func (e *Executor) monetaryBindingPresent() bool {
	if e == nil {
		return false
	}
	return strings.TrimSpace(string(e.BillingIdentity.MonetaryHost.Authority)) != "" ||
		e.BillingCreditGate != nil ||
		e.BillingExposureAdmission != nil ||
		e.BillingIdentity.AccountID != nil
}
