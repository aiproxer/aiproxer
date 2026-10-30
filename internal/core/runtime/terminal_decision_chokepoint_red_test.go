package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// RED contract: evaluateTerminalDecision is the one bounded runtime evaluator.
// Its production return type is a small normalized outcome retaining
// OutputCommitted and Decision, with no retry or failover classification.
type terminalDecisionProviderFunc struct {
	id string
	fn func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error)
}

func (p terminalDecisionProviderFunc) ID() string { return p.id }

func (p terminalDecisionProviderFunc) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	return p.fn(ctx, in)
}

func TestTerminalDecisionChokepointNilProviderPassesThroughWithoutCall(t *testing.T) {
	t.Parallel()
	got := evaluateTerminalDecision(context.Background(), nil, terminalDecisionInput(terminaldecision.CandidateCauseNormal, false))
	if got.Decision.Kind != terminaldecision.DecisionAllowStop || got.Decision.Continue != nil {
		t.Fatalf("nil provider outcome = %#v, want allow-stop without continuation", got)
	}
	if got.OutputCommitted {
		t.Fatal("nil provider changed output commitment")
	}
}

func TestTerminalDecisionChokepointCandidateCausesUseOneSeam(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		cause    terminaldecision.CandidateCause
		wantKind terminaldecision.DecisionKind
	}{
		{name: "normal", cause: terminaldecision.CandidateCauseNormal, wantKind: terminaldecision.DecisionContinue},
		{name: "transport", cause: terminaldecision.CandidateCauseTransport, wantKind: terminaldecision.DecisionContinue},
		{name: "limit", cause: terminaldecision.CandidateCauseLimit, wantKind: terminaldecision.DecisionContinue},
		{name: "provider error seam", cause: terminaldecision.CandidateCauseProviderError, wantKind: terminaldecision.DecisionContinue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := terminalDecisionProviderFunc{id: "candidate-provider", fn: func(_ context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
				if in.Candidate.Cause != tc.cause {
					t.Fatalf("provider saw cause %v, want %v", in.Candidate.Cause, tc.cause)
				}
				return continueDecision(), nil
			}}
			got := evaluateTerminalDecision(context.Background(), provider, terminalDecisionInput(tc.cause, false))
			if got.Decision.Kind != tc.wantKind {
				t.Fatalf("cause %v outcome = %#v, want kind %q", tc.cause, got, tc.wantKind)
			}
			if tc.wantKind == terminaldecision.DecisionContinue && got.Decision.Continue == nil {
				t.Fatalf("cause %v lost continuation intent", tc.cause)
			}
			if tc.wantKind != terminaldecision.DecisionContinue && got.Decision.Continue != nil {
				t.Fatalf("cause %v continued despite authoritative stop", tc.cause)
			}
		})
	}
}

func TestTerminalDecisionChokepointCommandCandidateCauseMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		command sdkterminal.Command
		want    terminaldecision.CandidateCause
	}{
		{command: sdkterminal.CommandNormalFinish, want: terminaldecision.CandidateCauseNormal},
		{command: sdkterminal.CommandEOF, want: terminaldecision.CandidateCauseTransport},
		{command: sdkterminal.CommandCancel, want: terminaldecision.CandidateCauseCancellation},
		{command: sdkterminal.CommandClose, want: terminaldecision.CandidateCauseCancellation},
		{command: sdkterminal.CommandTimeout, want: terminaldecision.CandidateCauseCancellation},
		{command: sdkterminal.CommandPartialError, want: terminaldecision.CandidateCauseProviderError},
	}
	for _, tc := range cases {
		t.Run(string(tc.command), func(t *testing.T) {
			t.Parallel()
			if got := decisionCandidateCause(tc.command); got != tc.want {
				t.Fatalf("command %q candidate cause = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

func TestTerminalDecisionChokepointProviderErrorPanicMalformedUnknownNormalizeBoundedAllowStop(t *testing.T) {
	t.Parallel()
	const attackerText = "attacker-controlled-provider-detail-should-not-escape"
	cases := []struct {
		name string
		fn   func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error)
	}{
		{name: "provider error", fn: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
			return terminaldecision.Decision{}, errors.New(attackerText)
		}},
		{name: "provider panic", fn: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
			panic(attackerText)
		}},
		{name: "malformed", fn: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
			return terminaldecision.Decision{Kind: terminaldecision.DecisionContinue, ReasonCode: "malformed"}, nil
		}},
		{name: "unknown", fn: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
			return terminaldecision.Decision{Kind: terminaldecision.DecisionKind(strings.Repeat("unknown-", 1024)), ReasonCode: "unknown"}, nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			provider := terminalDecisionProviderFunc{id: "failure-provider", fn: func(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
				calls.Add(1)
				return tc.fn(ctx, in)
			}}
			got := evaluateTerminalDecision(context.Background(), provider, terminalDecisionInput(terminaldecision.CandidateCauseNormal, true))
			if calls.Load() != 1 {
				t.Fatalf("%s provider calls = %d, want exactly one", tc.name, calls.Load())
			}
			if got.Decision.Kind != terminaldecision.DecisionAllowStop || got.Decision.Continue != nil {
				t.Fatalf("%s outcome = %#v, want bounded allow-stop", tc.name, got)
			}
			if !got.OutputCommitted {
				t.Fatal("failure normalization dropped committed-output evidence")
			}
			if len(got.Decision.ReasonCode) > terminaldecision.MaxReasonCodeBytes {
				t.Fatalf("%s reason code exceeds bound", tc.name)
			}
			if strings.Contains(got.Decision.ReasonCode, attackerText) || strings.Contains(got.Decision.ReasonCode, "unknown-") {
				t.Fatalf("%s leaked attacker text in reason code %q", tc.name, got.Decision.ReasonCode)
			}
		})
	}
}

func TestTerminalDecisionChokepointCancellationIsAuthoritativeBeforeProvider(t *testing.T) {
	t.Parallel()
	for _, cause := range []terminaldecision.CandidateCause{
		terminaldecision.CandidateCauseRefusal,
		terminaldecision.CandidateCauseContentFilter,
		terminaldecision.CandidateCauseCancellation,
		terminaldecision.CandidateCauseAuthorityDenied,
	} {
		t.Run(string(cause), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			turn := newTurnTerminal()
			attempt, _ := newAuthorityTerminalDecisionAttempt(t)
			provider := terminalDecisionProviderFunc{id: "cancellation-provider", fn: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
				calls.Add(1)
				return continueDecision(), nil
			}}
			result := turn.terminalizeTurnWithDecision(context.Background(), provider, terminalDecisionInput(cause, false), sdkterminal.CommandCancel, attempt)
			if !result.Won || result.Outcome.Command != sdkterminal.CommandCancel || result.Outcome.Code == "" || calls.Load() != 0 {
				t.Fatalf("cause %v result = %#v, provider calls = %d, want cancel winner with no provider call", cause, result, calls.Load())
			}
			if result.Outcome.Snapshot.OutputCommitted() && result.Outcome.Command == sdkterminal.CommandCancel {
				t.Fatal("cancellation unexpectedly published continuation evidence")
			}
		})
	}
}

func TestTerminalDecisionChokepointBlockedProviderPreventsTerminalClaim(t *testing.T) {
	turn := newTurnTerminal()
	attempt, auth := newAuthorityTerminalDecisionAttempt(t)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan coreterm.Result, 1)
	ctx, cancel := context.WithCancel(context.Background())
	var releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseProvider)
	var wg sync.WaitGroup
	wg.Add(1)
	t.Cleanup(func() { cancel(); releaseProvider(); waitTerminalGoroutines(&wg) })
	provider := terminalDecisionProviderFunc{id: "blocking-provider", fn: func(ctx context.Context, _ terminaldecision.Input) (terminaldecision.Decision, error) {
		close(started)
		select {
		case <-release:
			return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
		case <-ctx.Done():
			return terminaldecision.Decision{}, ctx.Err()
		}
	}}
	go func() {
		defer wg.Done()
		done <- turn.terminalizeTurnWithDecision(ctx, provider, terminalDecisionInput(terminaldecision.CandidateCauseNormal, false), sdkterminal.CommandEOF, attempt)
	}()
	awaitSignal(t, started)
	if turn.requestTerminal().Owner().State() != sdkterminal.StateOpen || attempt.terminal.Owner().State() != sdkterminal.StateOpen {
		t.Fatal("terminal owner claimed while provider decision was blocked")
	}
	select {
	case <-done:
		t.Fatal("terminal integration returned while provider decision was blocked")
	default:
	}
	releaseProvider()
	result := awaitTerminalResult(t, done)
	if !result.Won || turn.requestTerminal().Owner().State() == sdkterminal.StateOpen || attempt.terminal.Owner().State() == sdkterminal.StateOpen {
		t.Fatalf("terminal integration result = %#v, want one settled A/B terminal", result)
	}
	requestOutcome, requestClaimed := turn.requestTerminal().Owner().Outcome()
	attemptOutcome, attemptClaimed := attempt.terminal.Owner().Outcome()
	if !requestClaimed || requestOutcome.Scope != sdkterminal.ScopeRequest || !attemptClaimed || attemptOutcome.Scope != sdkterminal.ScopeAttempt {
		t.Fatalf("terminal outcomes = request(%#v,%t) attempt(%#v,%t), want exactly one A and one B settlement", requestOutcome, requestClaimed, attemptOutcome, attemptClaimed)
	}
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("attempt authority settlements = %d, want exactly one", auth.settleCalls.Load())
	}
}

func TestTerminalDecisionChokepointCommittedOutputPreservesContinueWithoutRetryOrFailover(t *testing.T) {
	t.Parallel()
	input := terminalDecisionInput(terminaldecision.CandidateCauseTransport, true)
	got := evaluateTerminalDecision(context.Background(), terminalDecisionProviderFunc{id: "post-output-provider", fn: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
		return continueDecision(), nil
	}}, input)
	if got.Decision.Kind != terminaldecision.DecisionContinue || got.Decision.Continue == nil {
		t.Fatalf("committed-output outcome = %#v, want valid continue", got)
	}
	if !got.OutputCommitted {
		t.Fatal("committed-output evidence was not retained")
	}
	// The normalized outcome intentionally has no retry/failover classification.
}

func TestTerminalDecisionChokepointFinalDecisionIsSharedAcrossCandidateKeys(t *testing.T) {
	var calls atomic.Int32
	provider := terminalDecisionProviderFunc{id: "shared-final-provider", fn: func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
		calls.Add(1)
		return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
	}}
	turn := newTurnTerminal()
	attempt, _ := newAuthorityTerminalDecisionAttempt(t)
	firstInput := terminalDecisionInput(terminaldecision.CandidateCauseNormal, false)
	firstInput.Candidate.Reference = "candidate-1"
	secondInput := firstInput
	secondInput.Candidate.Reference = "candidate-2"
	first := turn.terminalizeTurnWithDecision(context.Background(), provider, firstInput, sdkterminal.CommandEOF, attempt)
	second := turn.terminalizeTurnWithDecision(context.Background(), provider, secondInput, sdkterminal.CommandEOF, attempt)
	if !first.Won || second.Won {
		t.Fatalf("terminal results = %#v, %#v, want one winner", first, second)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want one shared final evaluation", calls.Load())
	}
}

func TestTerminalDecisionChokepointCancellationBarrierHasOneTerminalWinner(t *testing.T) {
	turn := newTurnTerminal()
	attempt, auth := newAuthorityTerminalDecisionAttempt(t)
	started := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseProvider)
	var startedOnce sync.Once
	var providerCalls atomic.Int32
	provider := terminalDecisionProviderFunc{id: "barrier-provider", fn: func(ctx context.Context, _ terminaldecision.Input) (terminaldecision.Decision, error) {
		providerCalls.Add(1)
		startedOnce.Do(func() { close(started) })
		select {
		case <-release:
			return continueDecision(), nil
		case <-ctx.Done():
			return continueDecision(), nil
		}
	}}
	results := make(chan coreterm.Result, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	t.Cleanup(func() { cancel(); releaseProvider(); waitTerminalGoroutines(&wg) })
	for range 2 {
		go func() {
			defer wg.Done()
			results <- turn.terminalizeTurnWithDecision(ctx, provider, terminalDecisionInput(terminaldecision.CandidateCauseNormal, false), sdkterminal.CommandCancel, attempt)
		}()
	}
	awaitSignal(t, started)
	cancel()
	releaseProvider()
	first := awaitTerminalResult(t, results)
	second := awaitTerminalResult(t, results)
	if first.Won == second.Won {
		t.Fatalf("terminal claim results = %#v, %#v, want one winner and one loser", first, second)
	}
	if turn.requestTerminal().Owner().State() == sdkterminal.StateOpen || attempt.terminal.Owner().State() == sdkterminal.StateOpen {
		t.Fatal("cancellation barrier did not settle both terminal owners")
	}
	requestOutcome, requestClaimed := turn.requestTerminal().Owner().Outcome()
	if !requestClaimed || requestOutcome.Command != sdkterminal.CommandCancel {
		t.Fatalf("request outcome = %#v (claimed=%t), want cancellation winner", requestOutcome, requestClaimed)
	}
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("attempt authority settlements = %d, want exactly one", auth.settleCalls.Load())
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("provider calls = %d, want one shared evaluation", providerCalls.Load())
	}
}

func TestTerminalDecisionChokepointTimeoutNormalizesWithoutAttackerText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	started := make(chan struct{})
	var calls atomic.Int32
	resultCh := make(chan coreterm.Result, 1)
	turn := newTurnTerminal()
	attempt, _ := newAuthorityTerminalDecisionAttempt(t)
	var wg sync.WaitGroup
	wg.Add(1)
	t.Cleanup(func() { waitTerminalGoroutines(&wg) })
	go func() {
		defer wg.Done()
		resultCh <- turn.terminalizeTurnWithDecision(ctx, terminalDecisionProviderFunc{id: "timeout-provider", fn: func(_ context.Context, _ terminaldecision.Input) (terminaldecision.Decision, error) {
			calls.Add(1)
			close(started)
			return terminaldecision.Decision{}, context.DeadlineExceeded
		}}, terminalDecisionInput(terminaldecision.CandidateCauseNormal, false), sdkterminal.CommandCancel, attempt)
	}()
	awaitSignal(t, started)
	got := awaitTerminalResult(t, resultCh)
	if calls.Load() != 1 {
		t.Fatalf("timeout provider calls = %d, want exactly one", calls.Load())
	}
	if !got.Won || got.Outcome.Command != sdkterminal.CommandCancel {
		t.Fatalf("timeout outcome = %#v, want bounded cancellation", got)
	}
	if got.Err != nil && strings.Contains(got.Err.Error(), "context deadline exceeded") {
		t.Fatalf("timeout leaked raw provider error: %v", got.Err)
	}
}

func TestTerminalDecisionChokepointExpiredInputDeadlineCannotBlockProvider(t *testing.T) {
	const attackerText = "raw-deadline-provider-detail"
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseProvider)
	var wg sync.WaitGroup
	wg.Add(1)
	t.Cleanup(func() { releaseProvider(); waitTerminalGoroutines(&wg) })
	var calls atomic.Int32
	resultCh := make(chan terminaldecision.Decision, 1)
	input := terminalDecisionInput(terminaldecision.CandidateCauseNormal, false)
	input.Deadline = time.Now().Add(-time.Minute)
	go func() {
		defer wg.Done()
		outcome := evaluateTerminalDecision(context.Background(), terminalDecisionProviderFunc{id: "deadline-provider", fn: func(ctx context.Context, _ terminaldecision.Input) (terminaldecision.Decision, error) {
			calls.Add(1)
			select {
			case <-release:
				return terminaldecision.Decision{}, errors.New(attackerText)
			case <-ctx.Done():
				return terminaldecision.Decision{}, ctx.Err()
			}
		}}, input)
		resultCh <- outcome.Decision
	}()
	var got terminaldecision.Decision
	select {
	case got = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expired input deadline allowed evaluator/provider to block")
	}
	if calls.Load() > 1 {
		t.Fatalf("deadline provider calls = %d, want zero or one", calls.Load())
	}
	if got.Kind != terminaldecision.DecisionAllowStop || got.Continue != nil {
		t.Fatalf("expired deadline outcome = %#v, want allow-stop without continuation", got)
	}
	if len(got.ReasonCode) > terminaldecision.MaxReasonCodeBytes || strings.Contains(got.ReasonCode, attackerText) || strings.Contains(got.ReasonCode, "context deadline") {
		t.Fatalf("expired deadline leaked unbounded/raw reason: %q", got.ReasonCode)
	}
}

func terminalDecisionInput(cause terminaldecision.CandidateCause, outputCommitted bool) terminaldecision.Input {
	return terminaldecision.Input{
		Candidate:    terminaldecision.CanonicalTerminalCandidate{Cause: cause, Reference: "candidate-1", OutputCommitted: outputCommitted},
		Request:      terminaldecision.RequestIdentity{RequestID: "request-1", TraceID: "trace-1", ALegID: "a-leg-1", BLegID: "b-leg-1"},
		Policy:       terminaldecision.PolicySnapshot{Revision: "policy-1", MaxContinuationAttempts: 2},
		Continuation: terminaldecision.ContinuationEvidence{TrajectoryRef: "trajectory-1", Attempt: 1},
		Deadline:     time.Now().Add(time.Minute),
	}
}

func continueDecision() terminaldecision.Decision {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionContinue, ReasonCode: "continue", Continue: &terminaldecision.ContinuationIntent{TrajectoryRef: "trajectory-1", ControlRef: "control-1", Provenance: "internal-control", ReasonCode: "continue"}}
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	t.Cleanup(func() { timer.Stop() })
	select {
	case <-signal:
	case <-timer.C:
		t.Fatal("timed out waiting for provider barrier")
	}
}

func awaitTerminalResult(t *testing.T, results <-chan coreterm.Result) coreterm.Result {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	t.Cleanup(func() { timer.Stop() })
	select {
	case result := <-results:
		return result
	case <-timer.C:
		t.Fatal("timed out waiting for terminal decision result")
		return coreterm.Result{}
	}
}

func waitTerminalGoroutines(wg *sync.WaitGroup) {
	joined := make(chan struct{})
	go func() {
		wg.Wait()
		close(joined)
	}()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-joined:
	case <-timer.C:
	}
}

func newAuthorityTerminalDecisionAttempt(t *testing.T) (*attemptSession, *recordingAuthorityService) {
	t.Helper()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed: true, Reserved: true, ReservationID: "terminal-decision-reservation",
			ReservedAmount: authorityInputAmount(7),
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	lifecycle := testAuthorityLifecycle(ex, attemptAuthorityState{
		admissionInput: testAuthorityAdmissionInput(7), admissionResult: auth.admitResult,
	}, authorityCandidate())
	return newAttemptSession(attemptSessionInput{
		bleg: b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, cand: authorityCandidate(), authority: lifecycle,
	}), auth
}
