package billingport

import (
	"context"
	"errors"

	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
)

type (
	Quantity      = usageevidence.Quantity
	MoneyEvidence = usageevidence.MoneyEvidence
)

var (
	ErrCreditScreenDenied = errors.New("billing: cheap credit screen denied")
	// ErrCreditScreenUnavailable classifies a degraded or failed cheap credit
	// screen: the decision could not be made, so no request may proceed as an
	// ordinary allow. The billing domain aliases this sentinel, so existing
	// error identity and classification stay valid after the carve.
	ErrCreditScreenUnavailable = errors.New("billing: cheap credit screen unavailable")

	// ErrAccountNotReady classifies an atomic admission against an account
	// whose state is not ready. The billing domain aliases this sentinel, so
	// existing error identity and wire classification stay valid after the
	// carve; the message text is byte-identical to the billing definition.
	ErrAccountNotReady = errors.New("billing: account is not ready")
	// ErrInsufficientSpendable classifies a posting/settlement that would push
	// the spendable balance below the account's credit floor. The billing
	// domain aliases this sentinel, so existing error identity and wire
	// classification stay valid after the carve; the message text is
	// byte-identical to the billing definition.
	ErrInsufficientSpendable = errors.New("billing: insufficient spendable balance")
	// ErrBillingStoreUnavailable classifies an exhausted store retry or a
	// missing maintenance store. The billing domain aliases this sentinel, so
	// existing error identity and wire classification stay valid after the
	// carve; the message text is byte-identical to the billing definition.
	ErrBillingStoreUnavailable = errors.New("billing: store unavailable")
)

// TerminalUsageSink is the single runtime terminal handoff. Implementations
// durably append immutable current call/leg records locally; they do not rate,
// authorize, or write financial journals.
//
// The context passed to AppendLeg/AppendCall carries the persistence deadline
// for the append and must not be derived from the caller's request context.
// Implementations must return success only after the record is durably
// committed (or, for replay-safe stores, provably already present with an
// identical fingerprint); a returned error means the record must be retried
// and never silently dropped. The same key may be appended more than once:
// replay must be idempotent and conflicting fingerprints must surface as a
// typed conflict rather than overwriting prior evidence.
type TerminalUsageSink interface {
	AppendLeg(context.Context, usageevidence.CallLegUsageRecord) error
	AppendCall(context.Context, usageevidence.CallUsageRecord) error
}

// DurableTerminalHandoff is implemented by a terminal usage sink that holds
// the obligation durably before it acknowledges the terminal append.
//
// A sink that does not implement it (a bare central store, a no-op default, a
// typed-nil port) cannot hold a completed obligation across a process
// restart, so a customer-funded operation must not run on such a host: the
// money would be paid to the provider and the customer charge would be lost.
//
// The readiness is a live fact, not a construction fact: a bounded durable
// holder can be full or closed, and it must then refuse new payable work
// instead of accepting an obligation it cannot hold.
type DurableTerminalHandoff interface {
	// TerminalHandoffReady reports whether the handoff can durably accept a
	// new obligation right now. The reason is a bounded, content-free label
	// for the refusal; it never names an account, a customer or a request.
	TerminalHandoffReady() (ready bool, reason string)
}
