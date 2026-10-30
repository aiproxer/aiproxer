package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// RED contract: every non-authoritative terminal candidate must be able to
// hand a provider Continue result to the same core transaction that publishes
// B2. These tests deliberately use the existing retry/attempt/steering owners;
// they do not add a second transaction fixture or owner.
func TestTerminalDecisionAllPathsContinueEOFPublishesDistinctB2(t *testing.T) {
	_, stream, b1, authority, provider, opens := newTask91TerminalPathHarness(t, 1)

	_, _ = testRecvEOF(context.Background(), stream)

	assertTask91PublishedB2(t, stream, b1, authority, provider, opens)
}

func TestTerminalDecisionAllPathsContinueSurfacedFailurePublishesDistinctB2(t *testing.T) {
	_, stream, b1, authority, provider, opens := newTask91TerminalPathHarness(t, 1)

	_, _ = testRecvError(context.Background(), stream, errors.New("transport failure"))

	assertTask91PublishedB2(t, stream, b1, authority, provider, opens)
}

func TestTerminalDecisionAllPathsContinuePartialFailurePublishesDistinctB2(t *testing.T) {
	_, stream, b1, authority, provider, opens := newTask91TerminalPathHarness(t, 1)
	stream.responsePipeline.bus = hooks.New(hooks.Config{
		ResponsePartHooks: []sdkhooks.ResponsePartHook{
			failingResponsePartHookStub{err: errors.New("response hook failure")},
		},
	})

	testStoreInner(stream, &task91SingleEventStream{event: aipapi.Event{
		Kind:  aipapi.EventTextDelta,
		Delta: "partial output",
	}})
	_, _ = stream.Recv(context.Background())

	assertTask91PublishedB2(t, stream, b1, authority, provider, opens)
}

func TestTerminalDecisionAllPathsPrePublicationFailureFinalizesOriginalB1(t *testing.T) {
	terminal, stream, b1, authority, provider, opens := newTask91TerminalPathHarness(t, 1)
	stream.recovery.opener = func(context.Context, replacementOpenRequest) (replacementOpenResult, error) {
		opens.Add(1)
		return replacementOpenResult{}, errors.New("B2 admission unavailable")
	}

	_, _ = testRecvEOF(context.Background(), stream)

	if current := stream.attempt.snapshot(); current != b1 {
		t.Fatalf("pre-publication failure replaced B1 with %p", current)
	}
	if stream.terminal.requestTerminal().Owner().State() == sdkterminal.StateOpen {
		t.Fatal("pre-publication failure left the A-side request open")
	}
	if authority.settleCalls.Load() != 0 {
		t.Fatalf("pre-publication failure settled B1 %d times, want zero", authority.settleCalls.Load())
	}
	if provider.calls.Load() == 0 || opens.Load() != 1 {
		t.Fatalf("pre-publication path calls=%d opens=%d, want provider evaluation and one attempted B2", provider.calls.Load(), opens.Load())
	}
	outcome, _ := terminal.requestTerminal().Owner().Outcome()
	if outcome.Command == sdkterminal.CommandCancel {
		t.Fatal("transport EOF was misclassified as cancellation")
	}
}

func TestTerminalDecisionAllPathsTimeoutAndCancellationBypassProvider(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*turnTerminal, requestTerminalFacts, *attemptSession, *responsePipeline)
	}{
		{name: "timeout", call: func(terminal *turnTerminal, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) {
			terminal.terminalizeTimeout(context.Background(), request, attempt, p)
		}},
		{name: "cancellation", call: func(terminal *turnTerminal, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) {
			terminal.terminalizeCancellation(context.Background(), request, attempt, p, "client canceled", false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal, stream, _, _, provider, _ := newTask91TerminalPathHarness(t, 1)
			tc.call(terminal, stream.facts.terminalFacts(), stream.attempt.require(), stream.responsePipeline)
			if provider.calls.Load() != 0 {
				t.Fatalf("authoritative %s invoked provider %d times", tc.name, provider.calls.Load())
			}
			if terminal.requestTerminal().Owner().State() == sdkterminal.StateOpen {
				t.Fatalf("authoritative %s left request terminal open", tc.name)
			}
		})
	}
}

func TestTerminalDecisionAllPathsGateReplacementIsAuthoritative(t *testing.T) {
	terminal, stream, _, authority, provider, _ := newTask91TerminalPathHarness(t, 1)

	err := terminal.terminalizeGateReplacement(context.Background(), stream.facts.terminalFacts(), stream.attempt.require(), stream.responsePipeline)
	if err == nil {
		t.Fatal("mandatory post-output recorder failure was converted to provider continuation")
	}
	if provider.calls.Load() != 0 {
		t.Fatalf("mandatory post-output recorder failure invoked provider %d times", provider.calls.Load())
	}
	if authority.settleCalls.Load() != 1 {
		t.Fatalf("gate replacement settled B1 %d times, want exactly once", authority.settleCalls.Load())
	}
}

func TestTerminalDecisionPlatformStopsAlwaysContinueProviderAtBoundedAttempt(t *testing.T) {
	provider := &task91TerminalDecisionProvider{continueThrough: -1}
	input := terminalDecisionInput(terminaldecision.CandidateCauseNormal, true)
	input.Policy.MaxContinuationAttempts = 2

	for attempt := range uint8(3) {
		input.Continuation.Attempt = attempt
		outcome := evaluateTerminalDecision(context.Background(), provider, input)
		if attempt < 2 && outcome.Decision.Kind != terminaldecision.DecisionContinue {
			t.Fatalf("attempt %d outcome = %#v, want Continue", attempt, outcome)
		}
		if attempt == 2 && (outcome.Decision.Kind != terminaldecision.DecisionAllowStop || outcome.Decision.Continue != nil) {
			t.Fatalf("attempt %d outcome = %#v, want bounded AllowStop", attempt, outcome)
		}
	}
	if got := provider.calls.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want two calls before platform budget stop", got)
	}
}

func newTask91TerminalPathHarness(t *testing.T, continueThrough int32) (*turnTerminal, *retryRecvStream, *attemptSession, *recordingAuthorityService, *task91TerminalDecisionProvider, *atomic.Int32) {
	t.Helper()
	terminal, stream, b1, authority, _ := newContinuationRedHarness(t, nil)
	provider := &task91TerminalDecisionProvider{continueThrough: continueThrough}
	terminal.terminalDecisionProvider = provider
	terminal.terminalDecisionProviderID = provider.ID()
	terminal.terminalDecisionProviderHasID = true
	terminal.continuationTransaction = func(ctx context.Context, intent terminaldecision.ContinuationIntent) (bool, error) {
		return runContinuationTransaction(ctx, terminal, stream, intent)
	}
	var opens atomic.Int32
	opener := stream.recovery.opener
	stream.recovery.opener = func(ctx context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		opens.Add(1)
		return opener(ctx, req)
	}
	return terminal, stream, b1, authority, provider, &opens
}

func assertTask91PublishedB2(t *testing.T, stream *retryRecvStream, b1 *attemptSession, authority *recordingAuthorityService, provider *task91TerminalDecisionProvider, opens *atomic.Int32) {
	t.Helper()
	current := stream.attempt.snapshot()
	if current == nil || current == b1 {
		t.Fatalf("current attempt = %p, want distinct published B2", current)
	}
	if opens.Load() != 1 {
		t.Fatalf("B2 opener calls = %d, want exactly one", opens.Load())
	}
	if provider.calls.Load() < 1 {
		t.Fatal("candidate did not invoke terminal decision provider")
	}
	if authority.settleCalls.Load() != 1 {
		t.Fatalf("B1 settlement calls = %d, want exactly one", authority.settleCalls.Load())
	}
}

type task91TerminalDecisionProvider struct {
	calls           atomic.Int32
	continueThrough int32
}

func (*task91TerminalDecisionProvider) ID() string { return "task91-red-provider" }

func (p *task91TerminalDecisionProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	call := p.calls.Add(1)
	if p.continueThrough < 0 || call <= p.continueThrough {
		return task91ContinueDecision(), nil
	}
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "bounded-stop"}, nil
}

func task91ContinueDecision() terminaldecision.Decision {
	return terminaldecision.Decision{
		Kind:       terminaldecision.DecisionContinue,
		ReasonCode: "continue",
		Continue: &terminaldecision.ContinuationIntent{
			TrajectoryRef: "trajectory-task91",
			Instruction:   "continue the already accepted work",
			Provenance:    "internal-control",
			ReasonCode:    "continue",
		},
	}
}

type task91SingleEventStream struct {
	event aipapi.Event
	done  bool
}

func (s *task91SingleEventStream) Recv(context.Context) (aipapi.Event, error) {
	if s.done {
		return aipapi.Event{}, errors.New("task91: stream exhausted")
	}
	s.done = true
	return s.event, nil
}

func (*task91SingleEventStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

func (*task91SingleEventStream) Close() error { return nil }
