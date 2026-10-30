package runtime

import (
	"context"
	"errors"
	"strings"

	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func testRetirePriorAttempt(s *retryRecvStream) {
	if s == nil {
		return
	}
	attempt := s.attempt.require()
	if attempt == nil {
		return
	}
	evidence := attemptEvidence{
		Command:     sdkterminal.CommandSwallowedAttempt,
		ReleaseKind: usagelimit.ReleaseKindSwallowed,
		LegOutcome:  usageevidence.LegOutcomeSwallowed,
		Usage:       emptyOperatorUsageShell(),
	}
	attempt.TerminalizeAttempt(context.Background(), IntentSwallowedFailure, evidence)
}

// These fixture helpers keep legacy characterization tests focused on their
// assertions while constructing the same explicit owner calls production uses.
// They are test-only and intentionally capture the attempt before terminal
// effects begin.
func testTerminalOwners(s *retryRecvStream) (request, attempt *streamTerminal) {
	if s == nil || s.terminal == nil {
		return nil, nil
	}
	request = s.terminal.requestTerminal()
	if current := s.attempt.snapshot(); current != nil {
		attempt = current.terminal
	}
	return request, attempt
}

func testBuildEvidence(s *retryRecvStream, cmd sdkterminal.Command, snap *coreterm.AccumulatorSnapshot, effErr error) (attemptTerminalIntent, attemptEvidence) {
	ev := attemptEvidence{
		Command:   cmd,
		Snapshot:  snap,
		Committed: s.terminal.committed(),
	}
	if s.responsePipeline != nil {
		ev.Usage = s.responsePipeline.operatorUsageForFinalize()
		ev.StreamFallback = s.responsePipeline.billingEvidenceFallback()
	}
	ev.TraceID = s.facts.traceID
	ev.ALegID = s.facts.aLegID
	ev.BillingState = s.facts.billingCallState
	ev.BillingCallID = s.facts.billingCallID

	intent := IntentSurfacedFailure
	switch cmd {
	case sdkterminal.CommandNormalFinish:
		intent = IntentSuccess
		ev.LegOutcome = usageevidence.LegOutcomeWinner
		ev.ObsOutcome = response.OutcomeSuccessReleased
		ev.RecordOutcome = aipapi.AttemptSuccess
	case sdkterminal.CommandClose:
		intent = IntentCancellation
		ev.LegOutcome = usageevidence.LegOutcomeCanceled
		ev.ObsOutcome = response.OutcomeClosed
		ev.RecordOutcome = aipapi.AttemptCancelled
	case sdkterminal.CommandCancel:
		intent = IntentCancellation
		ev.LegOutcome = usageevidence.LegOutcomeCanceled
		ev.ObsOutcome = response.OutcomeCancelled
		ev.RecordOutcome = aipapi.AttemptCancelled
	}
	if effErr != nil {
		ev.Err = effErr
		ev.RecordReason = attemptReasonDetail(effErr)
		ev.RecordOutcome = aipapi.AttemptSurfacedFailure
		ev.LegOutcome = usageevidence.LegOutcomeFailed
		ev.ObsOutcome = response.OutcomeFailed
		intent = IntentSurfacedFailure
		ev.Command = sdkterminal.CommandPartialError
	}
	return intent, ev
}

func testTerminalizeRequest(ctx context.Context, s *retryRecvStream, cmd sdkterminal.Command, effects func(context.Context) error) coreterm.Result {
	if s == nil || s.terminal == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	snap := s.responsePipeline.accumulatorSnapshot()
	attempt := s.attempt.snapshot()
	reqEff := func(cctx context.Context, _ coreterm.Outcome) error {
		var effErr error
		if effects != nil {
			effErr = effects(cctx)
		}
		if attempt != nil && cmd.AllowsScope(sdkterminal.ScopeAttempt) {
			intent, ev := testBuildEvidence(s, cmd, &snap, effErr)
			res := attempt.TerminalizeAttempt(cctx, intent, ev)
			if res.Result.Err != nil {
				return res.Result.Err
			}
			if effErr != nil {
				return effErr
			}
		} else if effErr != nil {
			return effErr
		}
		if attempt != nil {
			s.terminal.recordBillingLegForAttempt(cctx, s.facts.terminalFacts(), attempt, attempt.terminalEvidence(), cmd, s.responsePipeline.billingEvidenceFallback(), s.terminal.committed(), s.facts.billingCallState)
		}
		return s.terminal.handoffBillingTurn(cctx, s.facts.terminalFacts(), cmd)
	}
	return s.terminal.terminalizeRequest(ctx, cmd, snap, reqEff)
}

func testTerminalizeRequestForAttempt(ctx context.Context, s *retryRecvStream, cmd sdkterminal.Command, attempt *attemptSession, effects func(context.Context) error) coreterm.Result {
	if s == nil || s.terminal == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	snap := s.responsePipeline.accumulatorSnapshot()
	if attempt != nil && cmd.AllowsScope(sdkterminal.ScopeAttempt) {
		intent, ev := testBuildEvidence(s, cmd, &snap, nil)
		attempt.TerminalizeAttempt(ctx, intent, ev)
	}
	reqEff := func(cctx context.Context, _ coreterm.Outcome) error {
		if effects != nil {
			if err := effects(cctx); err != nil {
				return err
			}
		}
		return s.terminal.handoffBillingTurn(cctx, s.facts.terminalFacts(), cmd)
	}
	return s.terminal.terminalizeRequest(ctx, cmd, snap, reqEff)
}

func testTerminalizeAttempt(ctx context.Context, s *retryRecvStream, cmd sdkterminal.Command, effects func(context.Context) error) coreterm.Result {
	if s == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	return testTerminalizeAttemptForAttempt(ctx, s, cmd, s.attempt.snapshot(), effects)
}

func testTerminalizeAttemptForAttempt(ctx context.Context, s *retryRecvStream, cmd sdkterminal.Command, attempt *attemptSession, effects func(context.Context) error) coreterm.Result {
	if s == nil || attempt == nil || !cmd.AllowsScope(sdkterminal.ScopeAttempt) {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	snap := s.responsePipeline.accumulatorSnapshot()
	intent, ev := testBuildEvidence(s, cmd, &snap, nil)
	if effects != nil {
		ev.ObsOutcome = response.OutcomeFailed
	}
	res := attempt.TerminalizeAttempt(ctx, intent, ev)
	if effects != nil {
		if err := effects(ctx); err != nil {
			res.Result.Err = err
		}
	}
	return res.Result
}

func testTurnTerminalize(ctx context.Context, turn *turnTerminal, cmd sdkterminal.Command, attempt *attemptSession, snapshot func() coreterm.AccumulatorSnapshot, effects func(context.Context, coreterm.Outcome) error) coreterm.Result {
	var evidence coreterm.AccumulatorSnapshot
	if snapshot != nil {
		evidence = snapshot()
	}
	if cmd == sdkterminal.CommandGateReplacement && turn != nil && turn.committed() {
		return turn.terminalizeRequest(ctx, cmd, evidence, nil)
	}
	if !cmd.AllowsScope(sdkterminal.ScopeRequest) {
		if attempt == nil {
			return coreterm.Result{Err: sdkterminal.ErrInvalid}
		}
		ev := attemptEvidence{Command: cmd, Snapshot: &evidence, Usage: aipapi.Event{}}
		intent := IntentSurfacedFailure
		if cmd == sdkterminal.CommandNormalFinish {
			intent = IntentSuccess
		}
		return attempt.TerminalizeAttempt(ctx, intent, ev).Result
	}
	return turn.terminalizeRequest(ctx, cmd, evidence, func(cctx context.Context, out coreterm.Outcome) error {
		var effErr error
		if effects != nil {
			effErr = effects(cctx, out)
		}
		if attempt != nil && cmd.AllowsScope(sdkterminal.ScopeAttempt) {
			ev := attemptEvidence{Command: cmd, Snapshot: &evidence, Usage: aipapi.Event{}}
			intent := IntentSurfacedFailure
			switch cmd {
			case sdkterminal.CommandNormalFinish:
				intent = IntentSuccess
				ev.LegOutcome = usageevidence.LegOutcomeWinner
				ev.ObsOutcome = response.OutcomeSuccessReleased
				ev.RecordOutcome = aipapi.AttemptSuccess
			case sdkterminal.CommandClose:
				intent = IntentCancellation
				ev.LegOutcome = usageevidence.LegOutcomeCanceled
				ev.ObsOutcome = response.OutcomeClosed
				ev.RecordOutcome = aipapi.AttemptCancelled
			case sdkterminal.CommandCancel:
				intent = IntentCancellation
				ev.LegOutcome = usageevidence.LegOutcomeCanceled
				ev.ObsOutcome = response.OutcomeCancelled
				ev.RecordOutcome = aipapi.AttemptCancelled
			}
			if effErr != nil {
				ev.Err = effErr
				ev.RecordReason = attemptReasonDetail(effErr)
				ev.RecordOutcome = aipapi.AttemptSurfacedFailure
				ev.LegOutcome = usageevidence.LegOutcomeFailed
				ev.ObsOutcome = response.OutcomeFailed
				intent = IntentSurfacedFailure
				ev.Command = sdkterminal.CommandPartialError
			}
			res := attempt.TerminalizeAttempt(cctx, intent, ev)
			if res.Result.Won {
				if res.Result.Err != nil {
					return res.Result.Err
				}
			} else if res.Result.Err != nil && !errors.Is(res.Result.Err, sdkterminal.ErrInvalid) && !strings.Contains(res.Result.Err.Error(), "conflict") {
				return res.Result.Err
			}
			if effErr != nil {
				return effErr
			}
		} else if effErr != nil {
			return effErr
		}
		return nil
	})
}
