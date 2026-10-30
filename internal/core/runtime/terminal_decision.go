package runtime

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// terminalDecisionOutcome is the only result that crosses the runtime
// evaluator boundary. Retry/failover classification deliberately does not
// belong here; a later continuation transaction owns that decision.
type terminalDecisionOutcome struct {
	Decision        terminaldecision.Decision
	OutputCommitted bool
}

type terminalDecisionKey struct {
	cause               terminaldecision.CandidateCause
	reference           string
	outputCommitted     bool
	requestID           string
	traceID             string
	aLegID              string
	bLegID              string
	policyRevision      string
	trajectoryRef       string
	continuationAttempt uint8
}

type terminalDecisionFlight struct {
	key     terminalDecisionKey
	done    chan struct{}
	cancel  context.CancelFunc
	outcome terminalDecisionOutcome
}

// sharedTerminalDecision serializes one provisional candidate per turn. The
// latch is intentionally request-local and ephemeral: it is not a second
// terminal owner or a registry. Final outcomes remain observable to competing
// callers; Continue is discarded so a future continuation candidate can be
// evaluated afresh by Task 4.
func (t *turnTerminal) sharedTerminalDecision(ctx context.Context, provider terminaldecision.Provider, input terminaldecision.Input) terminalDecisionOutcome {
	if t == nil {
		return evaluateTerminalDecision(ctx, provider, input)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if input.Candidate.Cause.Authoritative() {
		// An authoritative close/cancel/refusal must not wait behind a provider
		// that ignores its context. Cancel the in-flight evaluation, then take
		// the typed pass-through directly.
		t.terminalDecisionMu.Lock()
		flight := t.terminalDecisionFlight
		cancel := func() {}
		if flight != nil && flight.cancel != nil {
			cancel = flight.cancel
		}
		t.terminalDecisionMu.Unlock()
		cancel()
		providerID, providerHasID := t.terminalDecisionProviderIdentity()
		return evaluateTerminalDecisionWithFrozenIdentity(ctx, provider, providerID, providerHasID, input, t.log)
	}
	key := terminalDecisionKey{
		cause:               input.Candidate.Cause,
		reference:           input.Candidate.Reference,
		outputCommitted:     input.Candidate.OutputCommitted,
		requestID:           input.Request.RequestID,
		traceID:             input.Request.TraceID,
		aLegID:              input.Request.ALegID,
		bLegID:              input.Request.BLegID,
		policyRevision:      input.Policy.Revision,
		trajectoryRef:       input.Continuation.TrajectoryRef,
		continuationAttempt: input.Continuation.Attempt,
	}
	for {
		t.terminalDecisionMu.Lock()
		flight := t.terminalDecisionFlight
		if flight != nil {
			if flight.key == key {
				done := flight.done
				t.terminalDecisionMu.Unlock()
				select {
				case <-done:
					return flight.outcome
				case <-ctxDone(ctx):
					return terminalDecisionOutcome{
						Decision:        allowStopDecision(decisionReasonForContext(ctx.Err())),
						OutputCommitted: input.Candidate.OutputCommitted,
					}
				}
			}
			done := flight.done
			t.terminalDecisionMu.Unlock()
			select {
			case <-done:
				t.terminalDecisionMu.Lock()
				final := t.terminalDecisionFlight == flight && flight.outcome.Decision.Kind != terminaldecision.DecisionContinue
				outcome := flight.outcome
				t.terminalDecisionMu.Unlock()
				if final {
					return outcome
				}
			case <-ctxDone(ctx):
				return terminalDecisionOutcome{
					Decision:        allowStopDecision(decisionReasonForContext(ctx.Err())),
					OutputCommitted: input.Candidate.OutputCommitted,
				}
			}
			continue
		}
		sharedCtx, cancel := context.WithCancel(ctx)
		flight = &terminalDecisionFlight{key: key, done: make(chan struct{}), cancel: cancel}
		t.terminalDecisionFlight = flight
		t.terminalDecisionMu.Unlock()

		providerID, providerHasID := t.terminalDecisionProviderIdentity()
		outcome := evaluateTerminalDecisionWithFrozenIdentity(sharedCtx, provider, providerID, providerHasID, input, t.log)
		cancel()
		t.terminalDecisionMu.Lock()
		flight.outcome = outcome
		close(flight.done)
		if outcome.Decision.Kind == terminaldecision.DecisionContinue {
			t.terminalDecisionFlight = nil
		}
		t.terminalDecisionMu.Unlock()
		return outcome
	}
}

func ctxDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}

const (
	defaultTerminalDecisionMaxContinuationAttempts uint8 = 3

	terminalDecisionReasonInvalidProvider = "invalid_provider"
	terminalDecisionReasonInvalidInput    = "invalid_input"
	terminalDecisionReasonProviderError   = "provider_error"
	terminalDecisionReasonProviderPanic   = "provider_panic"
	terminalDecisionReasonInvalidDecision = "invalid_decision"
	terminalDecisionReasonDeadline        = "deadline_exceeded"
	terminalDecisionReasonCanceled        = "context_canceled"
)

// evaluateTerminalDecision invokes one optional provider for one candidate.
// The nil-provider fast path intentionally does not inspect input: callers
// that have not yet projected a provider input must retain the old no-provider
// behavior exactly.
func evaluateTerminalDecision(ctx context.Context, provider terminaldecision.Provider, input terminaldecision.Input) terminalDecisionOutcome {
	return evaluateTerminalDecisionWithLogger(ctx, provider, input, nil)
}

func evaluateTerminalDecisionWithLogger(ctx context.Context, provider terminaldecision.Provider, input terminaldecision.Input, observationLogger *slog.Logger) terminalDecisionOutcome {
	var providerID string
	var hasProviderID bool
	if provider != nil {
		if id, err := terminaldecision.ProviderIdentity(provider); err == nil {
			providerID = id
			hasProviderID = true
		}
	}
	return evaluateTerminalDecisionWithFrozenIdentity(ctx, provider, providerID, hasProviderID, input, observationLogger)
}

func evaluateTerminalDecisionWithFrozenIdentity(
	ctx context.Context,
	provider terminaldecision.Provider,
	providerID string,
	hasProviderID bool,
	input terminaldecision.Input,
	observationLogger *slog.Logger,
) (out terminalDecisionOutcome) {
	out.OutputCommitted = input.Candidate.OutputCommitted
	out.Decision = allowStopDecision("no_provider")
	providerLabel := terminalDecisionObservationProviderInvalid
	if provider == nil {
		providerLabel = terminalDecisionObservationProviderNone
	} else if input.Candidate.Cause.Authoritative() {
		providerLabel = terminalDecisionObservationProviderBypass
	}
	defer func() {
		emitTerminalDecisionObservation(ctx, observationLogger, input, providerLabel, out)
	}()
	if provider == nil {
		return out
	}

	// Refusal, filtering, cancellation, and authority denial are authoritative
	// core outcomes. They cannot be converted into provider continuation.
	if input.Candidate.Cause.Authoritative() {
		out.Decision = allowStopDecision(string(input.Candidate.Cause))
		return out
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if !hasProviderID || providerID == "" {
		out.Decision = allowStopDecision(terminalDecisionReasonInvalidProvider)
		return out
	}
	providerLabel = providerID
	if err := input.Validate(); err != nil {
		out.Decision = allowStopDecision(terminalDecisionReasonInvalidInput)
		return out
	}
	maxAttempts := input.Policy.MaxContinuationAttempts
	if maxAttempts == 0 {
		maxAttempts = defaultTerminalDecisionMaxContinuationAttempts
	}
	if input.Continuation.Attempt >= maxAttempts {
		out.Decision = allowStopDecision("continuation_budget_exhausted")
		return out
	}
	if !input.Deadline.After(time.Now()) {
		out.Decision = allowStopDecision(terminalDecisionReasonDeadline)
		return out
	}

	callCtx, cancel := context.WithDeadline(ctx, input.Deadline)
	defer cancel()

	var (
		decision terminaldecision.Decision
		err      error
		panicked bool
	)
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		decision, err = provider.Decide(callCtx, input)
	}()

	if panicked {
		out.Decision = allowStopDecision(terminalDecisionReasonProviderPanic)
		return out
	}
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			out.Decision = allowStopDecision(terminalDecisionReasonDeadline)
		case errors.Is(err, context.Canceled):
			out.Decision = allowStopDecision(terminalDecisionReasonCanceled)
		default:
			out.Decision = allowStopDecision(terminalDecisionReasonProviderError)
		}
		return out
	}
	if err := callCtx.Err(); err != nil {
		out.Decision = allowStopDecision(decisionReasonForContext(err))
		return out
	}
	if err := decision.Validate(); err != nil {
		out.Decision = allowStopDecision(terminalDecisionReasonInvalidDecision)
		return out
	}

	// Detach the continuation pointer from provider-owned storage before it
	// crosses the core boundary. This is a value snapshot, not mutable policy.
	if decision.Continue != nil {
		intent := *decision.Continue
		decision.Continue = &intent
	}
	out.Decision = decision
	return out
}

func allowStopDecision(reason string) terminaldecision.Decision {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: reason}
}

func decisionReasonForContext(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return terminalDecisionReasonDeadline
	}
	return terminalDecisionReasonCanceled
}

const (
	terminalDecisionObservationMessage         = "terminal_decision_evaluation"
	terminalDecisionObservationProviderNone    = "none"
	terminalDecisionObservationProviderInvalid = "invalid"
	terminalDecisionObservationProviderBypass  = "authoritative_bypass"
	terminalDecisionObservationUnknown         = "unknown"
)

func emitTerminalDecisionObservation(ctx context.Context, logger *slog.Logger, input terminaldecision.Input, providerID string, outcome terminalDecisionOutcome) {
	if logger == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cause := terminalDecisionObservationUnknown
	if input.Candidate.Cause.IsKnown() {
		cause = string(input.Candidate.Cause)
	}
	kind := terminalDecisionObservationUnknown
	if outcome.Decision.Kind.IsKnown() {
		kind = string(outcome.Decision.Kind)
	}
	if providerID == "" {
		providerID = terminalDecisionObservationProviderInvalid
	}
	logger.LogAttrs(ctx, slog.LevelDebug, terminalDecisionObservationMessage,
		slog.String("candidate_cause", boundedTerminalDecisionString(cause, terminaldecision.MaxReasonCodeBytes)),
		slog.String("provider_id", boundedTerminalDecisionString(providerID, terminaldecision.MaxProviderIDBytes)),
		slog.String("decision_kind", boundedTerminalDecisionString(kind, terminaldecision.MaxReasonCodeBytes)),
		slog.String("reason_code", boundedTerminalDecisionString(outcome.Decision.ReasonCode, terminaldecision.MaxReasonCodeBytes)),
		slog.Bool("output_committed", outcome.OutputCommitted),
	)
}
