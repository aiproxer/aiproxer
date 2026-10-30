package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// AttemptGuard is the optional generic pre-open invariant check.
//
// The core owns the chokepoint: the guard is consulted once, immediately
// before the provider open — after every mutating request transform, after
// the post-hook re-derive and its negotiated downgrade, after the
// route-parameter merge and after the projection adaptation — and before any
// provider I/O.
//
// A nil guard is inert: removing it restores today's behaviour exactly. A
// bound guard that returns an error refuses the whole operation: the attempt
// never reaches the provider, the refusal is explicit and counted, and the
// operation does not fall back to another candidate.
//
// The seam is generic: it names no provider, no feature and no policy domain.
// It is a pre-open admission refusal, not a terminal decision, so the
// terminal-decision plane is untouched.
type AttemptGuard interface {
	// CheckAttempt validates the attempt that is about to open. A non-nil
	// error refuses the attempt before any provider I/O.
	CheckAttempt(ctx context.Context, in AttemptGuardInput) error
}

// AttemptGuardRoleScope is the optional generic pre-open applicability fast
// path.
//
// A guard that implements it names the attempts it governs; a false result
// means no CheckAttempt and no pre-open fact gathering. Guards without this
// extension keep the existing unconditional check.
type AttemptGuardRoleScope interface {
	// ShouldCheckAttempt reports whether the guard governs an attempt with
	// the given auxiliary role ("", ordinary traffic included). It is a
	// pure selector: it must not perform I/O and must not parse the role
	// as an authority.
	ShouldCheckAttempt(auxiliaryRole string) bool
}

// AttemptGuardInput is the content-free content of one pre-open check.
//
// It carries the target, the frozen baseline and the current options. It
// never carries a prompt, a transcript, a memory or a canonical call.
type AttemptGuardInput struct {
	// CandidateKey is the runtime's opaque identity of the candidate the
	// attempt is about to open: the backend, the model and the route
	// parameters. A consumer compares it; it never parses it.
	CandidateKey string
	// Backend and Model are the candidate's logical primary, before the
	// backend-facing native projection.
	Backend string
	Model   string
	// BaselineRoute is the frozen dispatched route selector.
	BaselineRoute string
	// BaselineOptions are the frozen dispatched generation options.
	BaselineOptions aipapi.GenerationOptions
	// ExpectedOptions are the frozen baseline options merged with the actual
	// candidate's route parameters: the options the attempt must still carry
	// when it opens.
	ExpectedOptions aipapi.GenerationOptions
	// AttemptOptions are the options the attempt carries at the open point.
	AttemptOptions aipapi.GenerationOptions
	// AuxiliaryRole is the opaque label of the auxiliary request that
	// produced this attempt, or "" for ordinary traffic. It is a label, not
	// an authority: it says which request produced the attempt, never who
	// may make it.
	AuxiliaryRole string
	// Installation is the deployment fact about the (backend, model) pair,
	// computed from the request's frozen registry publication.
	Installation modelregistry.InstallationState
	// EffortSupport is the provider's own answer for the effort the attempt
	// carries on this candidate's model.
	EffortSupport execbackend.EffortSupportState
}

// AttemptDowngradeGuard is the optional extension of the pre-open guard seam.
//
// The runtime applies negotiated downgrades at two places: the pre-open
// candidate admission and the post-request transform. It consults this
// extension before either strip, so a bound guard can refuse a downgrade the
// operation must not serve instead of letting the runtime apply it and relying
// on the later pre-open check.
//
// The extension is generic: it names no feature and no provider. A guard that
// implements it decides which calls it governs; a guard that does not is
// untouched, and the existing pre-open check still applies.
type AttemptDowngradeGuard interface {
	// CheckNegotiatedDowngrade validates one negotiated downgrade before the
	// runtime applies it. A non-nil error refuses the whole operation.
	CheckNegotiatedDowngrade(ctx context.Context, in AttemptDowngradeInput) error
}

// AttemptDowngradeInput is the content-free content of one pre-apply
// downgrade check.
type AttemptDowngradeInput struct {
	// AuxiliaryRole is the opaque label of the auxiliary request, or "".
	AuxiliaryRole string
	// CandidateKey is the runtime's opaque identity of the candidate.
	CandidateKey string
	// Backend and Model are the candidate's logical primary.
	Backend string
	Model   string
	// Downgraded are the capabilities the negotiation classified as soft
	// downgrades, in the runtime's own order.
	Downgraded []aipapi.Capability
	// ReasoningEffort is the effort the attempt carries before the strip.
	ReasoningEffort string
	// ExpectedReasoningEffort is the effort the served call must carry: the
	// frozen baseline merged with the candidate's route parameters. A strip
	// of a different effort does not change what the call serves, so a guard
	// can leave it to the existing pre-open check.
	ExpectedReasoningEffort string
}

// errAttemptGuardDenied reports that a bound pre-open guard refused the
// attempt. It is an operation-level invariant failure: the whole operation
// fails explicitly instead of silently serving another candidate.
var errAttemptGuardDenied = errors.New("executor: the attempt guard refused the attempt")

// isAttemptGuardDenial reports whether err is the pre-open guard's
// operation-level refusal.
func isAttemptGuardDenial(err error) bool {
	return errors.Is(err, errAttemptGuardDenied)
}

// isGuardCancellation reports whether err is a cancellation of the guard's
// own context. A cancelled guard is a cancellation, not a policy refusal.
func isGuardCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// checkAttemptGuard consults the bound pre-open guard with the content-free
// facts of the attempt that is about to open.
//
// The expected options are computed from the frozen baseline and the actual
// candidate's route parameters with the same canonical merge the runtime
// applies to the attempt, so the comparison is exact per candidate and needs
// no second compilation: the guard never re-resolves capabilities and never
// adds a provider round trip.
//
// The consultation is contained: a guard that panics is an unknown policy
// result, and an unknown pre-open policy result refuses the attempt. The
// recovered panic is reported through the existing diagnostic seam, and
// neither its value nor its stack reaches the client, a log attribute or a
// metric.
func (e *Executor) checkAttemptGuard(
	ctx context.Context,
	tx *attemptTx,
	c routing.AttemptCandidate,
	openCall aipapi.Call,
	be execbackend.Backend,
) error {
	if e == nil || e.AttemptGuard == nil || tx == nil {
		return nil
	}
	if scoped, ok := e.AttemptGuard.(AttemptGuardRoleScope); ok {
		should, panicErr := safety.CallValue(
			safety.BoundaryBackend,
			"attempt_guard_role_scope",
			func() (bool, error) { return scoped.ShouldCheckAttempt(tx.reqFacts.auxiliaryRole), nil },
		)
		if panicErr != nil {
			return e.guardPanicDenial(ctx, tx, c, panicErr)
		}
		if !should {
			return nil
		}
	}
	baseline := tx.reqFacts.baseline
	expected, err := aipapi.MergeRouteQueryIntoGenerationOptions(baseline.Options, c.Primary.Params)
	if err != nil {
		return fmt.Errorf("%w: %w", errAttemptGuardDenied, err)
	}
	in := AttemptGuardInput{
		CandidateKey:    c.Key,
		Backend:         c.Primary.Backend,
		Model:           c.Primary.Model,
		BaselineRoute:   baseline.Route.Selector,
		BaselineOptions: baseline.Options,
		ExpectedOptions: expected,
		AttemptOptions:  openCall.Options,
		AuxiliaryRole:   tx.reqFacts.auxiliaryRole,
		Installation:    tx.reqFacts.boundRegistry.InstallationState(c.Primary.Backend, c.Primary.Model),
		EffortSupport:   execbackend.EffectiveEffortSupport(ctx, be, openCall, c, openCall.Options.ReasoningEffort),
	}
	guardErr, panicErr := safety.CallValue(
		safety.BoundaryBackend,
		"attempt_guard_check",
		func() (error, error) { return e.AttemptGuard.CheckAttempt(ctx, in), nil },
	)
	if panicErr != nil {
		return e.guardPanicDenial(ctx, tx, c, panicErr)
	}
	if guardErr != nil {
		if isGuardCancellation(guardErr) {
			// A cancelled guard is a cancellation, not a policy refusal.
			return guardErr
		}
		return fmt.Errorf("%w: %w", errAttemptGuardDenied, guardErr)
	}
	return nil
}

// checkAttemptDowngradeGuard consults the optional negotiated-downgrade
// extension before the runtime applies a negotiated strip.
//
// The consultation is feature-scoped by the guard itself: the runtime passes
// the content-free facts and lets the bound guard decide whether the call is
// one it governs. A nil guard, a guard that does not implement the extension,
// a lossless negotiation and an empty downgrade set are all inert, so
// ordinary traffic keeps the existing behaviour exactly.
//
// The expected effort is the frozen baseline merged with the candidate's route
// parameters — the same merge the runtime applies before the open — so a strip
// of an effort the served call would not carry is visible as such and can be
// left to the existing pre-open check.
//
// A refusal is operation-level, like the pre-open refusal: it is wrapped in
// the same denial sentinel, so the attempt is closed as never started and the
// operation does not fall back to another candidate.
func (e *Executor) checkAttemptDowngradeGuard(
	ctx context.Context,
	baseline aipapi.Call,
	role string,
	c routing.AttemptCandidate,
	effort string,
	res aipapi.NegotiationResult,
) error {
	if e == nil || e.AttemptGuard == nil {
		return nil
	}
	if res.Kind != aipapi.NegotiationDowngrade || len(res.Downgraded) == 0 {
		return nil
	}
	guard, ok := e.AttemptGuard.(AttemptDowngradeGuard)
	if !ok {
		return nil
	}
	expected, err := aipapi.MergeRouteQueryIntoGenerationOptions(baseline.Options, c.Primary.Params)
	if err != nil {
		return fmt.Errorf("%w: %w", errAttemptGuardDenied, err)
	}
	in := AttemptDowngradeInput{
		AuxiliaryRole:           role,
		CandidateKey:            c.Key,
		Backend:                 c.Primary.Backend,
		Model:                   c.Primary.Model,
		Downgraded:              slices.Clone(res.Downgraded),
		ReasoningEffort:         effort,
		ExpectedReasoningEffort: expected.ReasoningEffort,
	}
	guardErr, panicErr := safety.CallValue(
		safety.BoundaryBackend,
		"attempt_downgrade_guard_check",
		func() (error, error) { return guard.CheckNegotiatedDowngrade(ctx, in), nil },
	)
	if panicErr != nil {
		// A guard that panics is an unknown policy result, so the
		// operation fails closed instead of serving the strip.
		return fmt.Errorf("%w: %w", errAttemptGuardDenied, panicErr)
	}
	if guardErr == nil {
		return nil
	}
	if isGuardCancellation(guardErr) {
		return guardErr
	}
	return fmt.Errorf("%w: %w", errAttemptGuardDenied, guardErr)
}

// guardPanicDenial translates a panic raised inside the guard consultation
// into the operation-level guard refusal.
//
// The refusal is the same typed verdict a policy denial produces, so the
// attempt is refused instead of opened.
//
// The returned error carries only the bounded, content-free panic shape: the
// panic value and the stack stay in the server-side log.
func (e *Executor) guardPanicDenial(
	ctx context.Context,
	tx *attemptTx,
	c routing.AttemptCandidate,
	panicErr error,
) error {
	pe, ok := errors.AsType[*safety.PanicError](panicErr)
	if !ok {
		return fmt.Errorf("%w: %w", errAttemptGuardDenied, panicErr)
	}
	if e != nil && e.Log != nil {
		attrs := diag.IsolatedCrashAttrs(ctx, pe, diag.CrashAttrOpts{AttrOpts: diag.AttrOpts{CallID: tx.reqFacts.traceID}})
		attrs = diag.AppendIsolatedCrashStack(attrs, pe)
		e.Log.LogAttrs(ctx, slog.LevelError, "isolated_panic_attempt_guard", attrs...)
	}
	diag.LogDecision(ctx, e.Log, "attempt_guard_panic_denial", diag.AttrOpts{CallID: tx.reqFacts.traceID},
		slog.String("candidate_key", c.Key),
		slog.String("backend", c.Primary.Backend),
	)
	return fmt.Errorf("%w: %w", errAttemptGuardDenied, pe)
}
