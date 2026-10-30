package sessionclassification

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// This file owns the integration between one generation's local decision, the
// shared durable lease state, and the remote decider. It retains nothing between
// turns: every remote fact it needs comes from the immutable policy, the
// process-owned store, or the frozen port (requirements 10.3, 10.5, 10.7).
//
// The sequence is fixed, and it is the structural guarantee requirement 10.4
// asks for: claim, release every store lock, network, complete. No store
// transaction, database connection, or coordinator mutex is held while a decider
// runs, so a slow or hung remote service cannot serialize unrelated sessions.

// remoteResult is the bounded projection of one leased remote attempt. It holds
// only the classification snapshot the store accepted, the attempt number the
// store assigned, and the record the store returned, so a caller cannot observe
// the decider, the lease, or any request content through it.
type remoteResult struct {
	// classification is the projected positive, or the zero value when the
	// session stays unknown.
	classification session.Classification
	// attempts is the store-assigned attempt number of the attempt.
	attempts uint32
	// record is the store's current record after the attempt.
	record Record
	// accepted reports whether this turn's completion was accepted rather than
	// stale, which is what distinguishes an established positive from one another
	// turn established (requirement 9.1).
	accepted bool
	// promoted reports that this turn's own accepted completion established the
	// positive. It is the first-positive-ownership rule for the remote path.
	promoted bool
	// latency is the measured duration of the decider call itself. It carries no
	// request content and is validated against the bounded observation
	// vocabulary before export (requirements 9.3, 9.4).
	latency time.Duration
	// budgetExhausted reports that the store has spent this session's finite
	// attempt budget (requirement 6.7).
	budgetExhausted bool
}

// RemoteFailure is the bounded, provider-neutral failure contract a decider may
// implement on its error type. It exists so requirement 9.3's bounded remote
// outcome and the retry decision can be made from the closed vocabulary instead
// of parsing error text or naming a vendor's failure kinds (requirements 6.9,
// 7.5, 9.4).
//
// An error that does not implement it is still handled: it is reported as
// RemoteNetworkError and remains bounded by the finite attempt budget.
type RemoteFailure interface {
	error
	// Outcome returns the bounded failure member of the closed RemoteOutcome
	// vocabulary. It must not carry request content, a vendor response, or a
	// credential.
	Outcome() RemoteOutcome
}

// remoteFailureOutcome maps a decider error onto the closed bounded failure
// vocabulary. An error that carries no bounded classification, or one whose
// classification is not a member of the closed vocabulary, is reported as a
// network failure, because that is the only claim a caller may make about an
// error it cannot classify (requirements 6.9, 9.4).
func remoteFailureOutcome(err error) RemoteOutcome {
	if failure, ok := errors.AsType[RemoteFailure](err); ok {
		if outcome := failure.Outcome(); RemoteOutcomeAllowed(outcome) {
			return outcome
		}
	}
	return RemoteNetworkError
}

// remoteRetryable reports whether a bounded failure may be retried inside the
// finite per-session attempt budget.
//
// Only a transient transport, rate-limit, server, or timeout failure is
// retryable. A malformed or oversized result is not: another identical attempt
// cannot make untrusted output parseable, and repeating it would spend the
// session's finite budget on a failure that is already determined
// (requirements 6.7, 12.8).
func remoteRetryable(outcome RemoteOutcome) bool {
	switch outcome {
	case RemoteNetworkError, RemoteRateLimited, RemoteServerError, RemoteTimeout:
		return true
	default:
		return false
	}
}

// remoteProposal converts one validated remote decision into the bounded
// promotion proposal a completion may carry.
//
// Only a remote-sourced positive at or above the configured threshold is
// proposed. A below-threshold result, an invalid result, and any failure produce
// the zero proposal, which a store records as no promotion at all: absence of a
// positive is never a negative classification (requirements 6.8, 6.9, 1.5).
func remoteProposal(decision RemoteDecision, threshold float64) session.Classification {
	if ValidateRemoteDecision(decision) != nil || !decision.Positive(threshold) {
		return session.Classification{}
	}
	return session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceRemote,
		Confidence: session.ConfidenceHigh,
		Evidence:   EvidenceCodeRemoteAboveThreshold,
		Revision:   1,
	}
}

// runRemote executes the bounded remote-decision phase for one still-unknown
// turn and returns the classification the store accepted.
//
// Every path preserves the user request. A refused input, a busy or exhausted
// claim, a bounded remote failure, and a below-threshold result all degrade to
// unknown; only a durable-state failure or a canceled caller returns an error
// (requirements 6.3, 6.4, 6.6, 6.7, 6.9).
func (c *Classifier) runRemote(ctx context.Context, key Key, store Store, in sdkclassification.Input) (remoteResult, RemoteOutcome, error) {
	remote := BuildRemoteInput(in, EvaluateLocal(c.cfg, in))
	if err := ValidateRemoteInput(remote); err != nil {
		// Derived evidence outside the bounded contract is refused before any
		// lease is consumed and before any egress (requirements 6.7, 7.2).
		return remoteResult{}, RemoteSkipped, nil
	}
	settings := c.remote
	budget := settings.MaxAttemptsPerSession

	result, outcome := remoteResult{}, RemoteSkipped
	for range budget {
		attempted, attempt, err := c.leaseRemoteAttempt(ctx, key, store, remote, settings)
		c.observeRemote(attempt, attempted.latency)
		result, outcome = attempted, attempt
		if err != nil {
			return remoteResult{}, attempt, err
		}
		if attempt == RemotePositive {
			// Only an accepted completion promotes. A refused claim or a stale
			// completion that observed another turn's positive projects that
			// immutable value without claiming the transition, and without making
			// any remote call of its own (requirements 6.5, 6.8, 9.1).
			break
		}
		// A busy lease, a spent budget, or a store refusal ends this turn's remote
		// work. Claiming again past a refusal would be the per-turn retry storm
		// requirement 6.6 forbids, and the shared durable record owns the
		// cross-turn decision through its finite attempt counter.
		if !remoteRetryable(attempt) || attempted.budgetExhausted {
			break
		}
		// The backoff wait is the single point at which a canceled caller is
		// released between two attempts. It observes the context itself, so an
		// abandoned request never sleeps out the delay and never spends another
		// attempt (requirements 6.7, 6.9).
		if err := c.waitRemoteBackoff(ctx, settings.RetryBackoff); err != nil {
			return remoteResult{}, attempt, err
		}
	}
	// The caller's context ending during the phase is reported as the
	// cancellation rather than as a finished remote result. Reporting a bounded
	// outcome as if the turn had completed normally would hide an abandoned
	// request from the caller that owns it (requirements 6.9, 4.5).
	if err := ctx.Err(); err != nil {
		return remoteResult{}, outcome, err
	}
	return result, outcome, nil
}

// leaseRemoteAttempt performs one complete claim-network-complete cycle.
//
// The claim is published and released before the decider runs, and the completion
// is made only after it returns, so the durable lease - not a process lock - is
// what serializes concurrent turns for one session (requirements 6.6, 10.4).
func (c *Classifier) leaseRemoteAttempt(ctx context.Context, key Key, store Store, remote RemoteInput, settings RemoteConfig) (remoteResult, RemoteOutcome, error) {
	now := c.now()
	claim, record, granted, err := store.ClaimRemote(ctx, key, now, uint32(settings.MaxAttemptsPerSession), settings.LeaseTTL, settings.RetryBackoff)
	if err != nil {
		return remoteResult{record: record}, RemoteSkipped, fmt.Errorf("%w: %w", ErrStateUnavailable, err)
	}
	if !granted {
		return refusedClaim(record, uint32(settings.MaxAttemptsPerSession), now)
	}

	// Requirement 10.4: the decider runs with no store transaction, database
	// connection, or coordinator mutex held.
	//
	// The measured duration deliberately comes from the process clock rather than
	// the generation clock: a generation may inject a fixed logical clock for
	// lease arithmetic, and a latency sample read from it would always be zero.
	// The sample is a bounded integer duration, so it can carry neither request
	// content nor a session identifier (requirements 9.3, 9.4).
	started := time.Now()
	decision, decideErr := c.remoteDecider.Decide(ctx, remote)
	latency := time.Since(started)

	if decideErr != nil && ctx.Err() != nil {
		// Caller cancellation stays authoritative: the turn reports the
		// cancellation rather than a decision. The lease is deliberately left to
		// expire instead of being completed with a canceled context, because
		// requirement 6.11 makes expiry the safe path for an abandoned lease and
		// requirement 6.7 bounds how many times a session can reclaim it.
		return remoteResult{record: record, attempts: claim.Attempt, latency: latency},
			remoteFailureOutcome(decideErr), nil
	}

	completedAt := c.now()
	completed, completeErr := store.CompleteRemote(ctx, claim, RemoteCompletion{
		Proposal: remoteProposal(decision, settings.PositiveThreshold),
	}, completedAt)
	if completeErr != nil && !errors.Is(completeErr, ErrStaleRemoteClaim) {
		return remoteResult{record: record, attempts: claim.Attempt, latency: latency}, RemoteSkipped,
			fmt.Errorf("%w: %w", ErrStateUnavailable, completeErr)
	}
	accepted := completeErr == nil

	result := remoteResult{
		attempts:        claim.Attempt,
		record:          completed,
		accepted:        accepted,
		latency:         latency,
		budgetExhausted: claim.Attempt >= uint32(settings.MaxAttemptsPerSession),
	}
	if completed.Classification.IsCodingAgent() {
		// A positive is projected whether this turn's completion was accepted or
		// stale: an established classification is immutable for the whole logical
		// session, so a turn that merely observed it still returns it
		// (requirements 1.4, 6.5).
		result.classification = completed.Classification
		result.promoted = accepted
		return result, RemotePositive, nil
	}
	if decideErr != nil {
		return result, remoteFailureOutcome(decideErr), nil
	}
	return result, RemoteBelowThreshold, nil
}

// refusedClaim projects a claim the store refused. A refused claim means no
// egress may happen this turn, so it reports the bounded reason and leaves the
// session unknown unless another turn already promoted it (requirements 6.6,
// 6.7).
func refusedClaim(record Record, budget uint32, now time.Time) (remoteResult, RemoteOutcome, error) {
	if record.Classification.IsCodingAgent() {
		// The promotion committed between this turn's authoritative read and its
		// claim. The winner owns the transition observation, so this turn reports
		// the value and nothing more (requirement 9.1).
		return remoteResult{classification: record.Classification, record: record, accepted: true}, RemotePositive, nil
	}
	switch {
	case record.RemoteLeaseID != "" && now.Before(record.RemoteLeaseUntil):
		return remoteResult{record: record}, RemoteLeaseBusy, nil
	case record.RemoteAttempts >= budget:
		return remoteResult{record: record, budgetExhausted: true}, RemoteBudgetExhausted, nil
	default:
		// The remaining refusal is a completion backoff the store has not
		// released yet. The turn ends rather than spinning on it: the backoff is
		// the store's cross-turn decision, and a later turn observes it
		// immediately (requirements 6.6, 6.7).
		return remoteResult{record: record}, RemoteSkipped, nil
	}
}

// waitRemoteBackoff pauses between two attempts of one turn for the configured
// retry_backoff.
//
// The wait is bounded by the validated backoff, it observes the caller's context
// so an abandoned request returns promptly, and it creates no goroutine, timer,
// or background worker that outlives the call (requirements 6.7, 10.7).
func (c *Classifier) waitRemoteBackoff(ctx context.Context, backoff time.Duration) error {
	if backoff <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
