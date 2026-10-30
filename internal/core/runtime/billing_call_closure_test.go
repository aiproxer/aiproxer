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
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestCallClosureTimesUsesTerminalLegSpan(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	started, finished := callClosureTimes([]usageevidence.CallLegUsageRecord{
		{StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(150, 0).UTC()},
		{StartedAt: time.Unix(120, 0).UTC(), FinishedAt: time.Unix(180, 0).UTC()},
	}, now)
	if !started.Equal(time.Unix(100, 0).UTC()) || !finished.Equal(time.Unix(180, 0).UTC()) {
		t.Fatalf("closure span = %s..%s, want 100..180", started, finished)
	}
}

func TestAuthoritativeRuntimeWithoutTerminalSinkDoesNotHandoff(t *testing.T) {
	executor := &Executor{BillingRuntime: BillingRuntime{
		BillingIdentity: testBillingIdentity(),
	}}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:        "a-legacy",
			billingCallID: callID,
			baseline:      aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-legacy"}},
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-legacy", ALegID: "a-legacy", Seq: 1}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}}, authorityLifecycle{}),
	}
	stream = stampStreamIdentity(stream, executor)
	stream.terminal.recordBillingLegForAttempt(context.Background(), stream.facts.terminalFacts(), stream.attempt.snapshot(), stream.attempt.require().terminalEvidence(), sdkterminal.CommandNormalFinish, aipapi.Event{}, true, stream.facts.billingCallState)
	if err := stream.terminal.handoffBillingTurn(context.Background(), stream.facts.terminalFacts(), sdkterminal.CommandNormalFinish); err != nil {
		t.Fatalf("handoffBillingTurn: %v", err)
	}
	if executor.hasTerminalSink() || executor.hasTerminalCallSink() {
		t.Fatal("runtime without TerminalUsageSink must not report a terminal handoff")
	}
}

func TestTerminalUsageSinkFreezesAllocatedBLegsAtRequestTerminal(t *testing.T) {
	var got []usageevidence.CallUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		TerminalUsageSink: testTerminalSink{appendCall: func(_ context.Context, record usageevidence.CallUsageRecord) error {
			got = append(got, record)
			return nil
		}},
		BillingIdentity: testBillingIdentity(),
	}}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	stream := &retryRecvStream{
		recovery: &recoveryController{},
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:        "a-shared",
			billingCallID: callID,
			baseline:      aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-shared"}},
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-shared", Seq: 1}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}}, authorityLifecycle{}),
	}
	stream = stampStreamIdentity(stream, executor)
	state := stream.facts.billingCallState
	state.noteAllocatedBLeg("b-2", 2)
	state.noteAllocatedBLeg("b-1", 1)
	if err := stream.terminal.handoffBillingTurn(context.Background(), stream.facts.terminalFacts(), sdkterminal.CommandNormalFinish); err != nil {
		t.Fatalf("handoffBillingTurn: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("call-closure appends = %d, want 1", len(got))
	}
	if got[0].Key != callID.String() {
		t.Fatalf("call-closure key = %q, want BillingCallID", got[0].Key)
	}
	if strings.Contains(got[0].Key, "a-shared") || strings.Contains(got[0].Key, "sess-shared") || strings.Contains(got[0].Key, "acct") {
		t.Fatal("A-leg/session/account are correlation, not the durable key")
	}
	if got[0].ALegID != "a-shared" || got[0].SessionID != "sess-shared" {
		t.Fatalf("correlation = a=%q sess=%q", got[0].ALegID, got[0].SessionID)
	}
	if len(got[0].ExpectedBLegIDs) != 2 || got[0].ExpectedBLegIDs[0] != "b-1" || got[0].ExpectedBLegIDs[1] != "b-2" {
		t.Fatalf("frozen expected B-legs = %#v", got[0].ExpectedBLegIDs)
	}

	state.noteAllocatedBLeg("b-3", 3)
	if err := stream.terminal.handoffBillingTurn(context.Background(), stream.facts.terminalFacts(), sdkterminal.CommandNormalFinish); err != nil {
		t.Fatalf("handoffBillingTurn: %v", err)
	}
	frozen := state.freezeAllocatedBLegs()
	if len(frozen) != 2 || frozen[0] != "b-1" || frozen[1] != "b-2" {
		t.Fatalf("allocated set grew after terminal freeze: %#v", frozen)
	}
	if len(got) != 1 {
		t.Fatalf("identical call-closure replay must not append again: %d", len(got))
	}
	if len(got[0].ExpectedBLegIDs) != 2 {
		t.Fatalf("expected B-leg set grew after seal: %#v", got[0].ExpectedBLegIDs)
	}
}

func TestTerminalUsageSinkNilLeavesRuntimeWithoutFinancialHandoff(t *testing.T) {
	executor := &Executor{BillingRuntime: BillingRuntime{
		BillingIdentity: testBillingIdentity(),
	}}
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:   "a-1",
			baseline: aipapi.Call{},
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}}, authorityLifecycle{}),
	}
	stream = stampStreamIdentity(stream, executor)
	stream.terminal.recordBillingLegForAttempt(context.Background(), stream.facts.terminalFacts(), stream.attempt.snapshot(), stream.attempt.require().terminalEvidence(), sdkterminal.CommandNormalFinish, aipapi.Event{}, true, stream.facts.billingCallState)
	if err := stream.terminal.handoffBillingTurn(context.Background(), stream.facts.terminalFacts(), sdkterminal.CommandNormalFinish); err != nil {
		t.Fatalf("handoffBillingTurn: %v", err)
	}
}

func TestAuthoritativeBillingWithoutTerminalSinkFailsClosed(t *testing.T) {
	executor := &Executor{BillingRuntime: BillingRuntime{
		BillingIdentity: testBillingIdentity(),
	}}
	if executor.hasTerminalCallSink() || executor.hasTerminalSink() {
		t.Fatal("authoritative executor without TerminalUsageSink must not report a terminal sink")
	}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:        "a-1",
			billingCallID: callID,
			baseline:      aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-a"}},
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}}, authorityLifecycle{}),
	}
	stream = stampStreamIdentity(stream, executor)
	stream.terminal.recordBillingLegForAttempt(context.Background(), stream.facts.terminalFacts(), stream.attempt.snapshot(), stream.attempt.require().terminalEvidence(), sdkterminal.CommandNormalFinish, aipapi.Event{}, true, stream.facts.billingCallState)
	if err := stream.terminal.handoffBillingTurn(context.Background(), stream.facts.terminalFacts(), sdkterminal.CommandNormalFinish); err != nil {
		t.Fatalf("handoffBillingTurn: %v", err)
	}
}

func TestTerminalUsageSinkIsTheOnlyRuntimeTerminalBillingSink(t *testing.T) {
	var calls []usageevidence.CallUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		TerminalUsageSink: testTerminalSink{appendCall: func(_ context.Context, record usageevidence.CallUsageRecord) error {
			calls = append(calls, record)
			return nil
		}},
		BillingIdentity: testBillingIdentity(),
	}}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	stream := &retryRecvStream{
		recovery: &recoveryController{},
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:        "a-1",
			billingCallID: callID,
			baseline:      aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-1"}},
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-win", ALegID: "a-1", Seq: 1}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}}, authorityLifecycle{}),
	}
	stream = stampStreamIdentity(stream, executor)
	stream.terminal.recordBillingLegForAttempt(context.Background(), stream.facts.terminalFacts(), stream.attempt.snapshot(), stream.attempt.require().terminalEvidence(), sdkterminal.CommandNormalFinish, aipapi.Event{}, true, stream.facts.billingCallState)
	if err := stream.terminal.handoffBillingTurn(context.Background(), stream.facts.terminalFacts(), sdkterminal.CommandNormalFinish); err != nil {
		t.Fatalf("handoffBillingTurn: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("call-closure appends = %d, want 1", len(calls))
	}
	if len(calls[0].ExpectedBLegIDs) != 1 || calls[0].ExpectedBLegIDs[0] != "b-win" {
		t.Fatalf("expected B-legs from recorded allocation = %#v", calls[0].ExpectedBLegIDs)
	}
}

func TestTerminalUsageSinkSealsOnRequestOwnerPanicAndGateReplacementWithoutTUR(t *testing.T) {
	commands := []sdkterminal.Command{
		sdkterminal.CommandPanic,
		sdkterminal.CommandGateReplacement,
	}
	for _, command := range commands {
		t.Run(string(command), func(t *testing.T) {
			var got []usageevidence.CallUsageRecord
			executor := &Executor{BillingRuntime: BillingRuntime{
				TerminalUsageSink: testTerminalSink{appendCall: func(_ context.Context, record usageevidence.CallUsageRecord) error {
					got = append(got, record)
					return nil
				}},
				BillingIdentity: testBillingIdentity(),
			}}
			callID, err := usageevidence.NewBillingCallID()
			if err != nil {
				t.Fatal(err)
			}
			stream := &retryRecvStream{
				facts: testRecvTurnFacts(recvTurnFacts{
					aLegID:        "a-1",
					billingCallID: callID,
					baseline:      aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-1"}},
				}),
				attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}}, authorityLifecycle{}),
			}
			stream = stampStreamIdentity(stream, executor)
			result := testTerminalizeRequest(context.Background(), stream, command, nil)
			if result.Err != nil {
				t.Fatalf("request-owner %s: %v", command, result.Err)
			}
			if !result.Won {
				t.Fatalf("request-owner %s must win", command)
			}
			if len(got) != 1 {
				t.Fatalf("call-closure appends for %s = %d, want 1", command, len(got))
			}
			if got[0].Outcome != usageevidence.TurnOutcomeFailed {
				t.Fatalf("call-closure outcome for %s = %q, want failed", command, got[0].Outcome)
			}
			if len(got[0].ExpectedBLegIDs) != 1 || got[0].ExpectedBLegIDs[0] != "b-1" {
				t.Fatalf("expected B-legs for %s = %#v", command, got[0].ExpectedBLegIDs)
			}

			later := testTerminalizeRequest(context.Background(), stream, sdkterminal.CommandNormalFinish, nil)
			if later.Won {
				t.Fatalf("later finish must lose after %s already owns the request", command)
			}
			if len(got) != 1 {
				t.Fatalf("later finish after %s appended again: %d", command, len(got))
			}
		})
	}
}

func TestTerminalUsageSinkGateReplacementAppendFailureDoesNotRetryProvider(t *testing.T) {
	var opens atomic.Int32
	var got []usageevidence.CallUsageRecord
	executor := TestExecutor()
	executor.SecureSessionRecordingMandatory = true
	executor.BillingIdentity = testBillingIdentity()
	executor.TerminalUsageSink = testTerminalSink{appendCall: func(_ context.Context, record usageevidence.CallUsageRecord) error {
		got = append(got, record)
		return errors.New("durable unavailable")
	}}
	executor.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return nil, errors.New("must not open after committed gate-replacement")
			},
		},
	}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	stream := &retryRecvStream{
		recovery: &recoveryController{},
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:        "a-1",
			billingCallID: callID,
			baseline:      aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-1"}},
		}),
		attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1}, routing.AttemptCandidate{Key: "cand-1", Primary: routing.Primary{Backend: "backend", Model: "model"}}, authorityLifecycle{}),
		responsePipeline: &responsePipeline{recordingOutcome: responseRecordingMandatoryPostCommitFailure},
	}
	stream = stampStreamIdentity(stream, executor)
	stream.terminal.markCommitted(stream.attempt.snapshot())
	request := stream.facts.terminalFacts()
	if err := stream.terminal.terminalizeGateReplacement(context.Background(), request, stream.attempt.require(), stream.responsePipeline); err == nil {
		t.Fatal("gate replacement must return a post-output failure")
	}
	request.replacementBlocked = true
	_, err = stream.recovery.tryReplacementIteration(context.Background(), request, stream.attempt.require(), stream.terminal.committed())
	var uf *aipapi.UpstreamFailureError
	if !errors.As(err, &uf) || uf.Phase != aipapi.PhasePostOutput || uf.Recoverable {
		t.Fatalf("unexpected replacement error: %v", err)
	}
	if stream.terminal != nil && stream.terminal.requestTerminal() != nil && stream.terminal.requestTerminal().Owner().State().IsTerminal() {
		t.Fatal("committed gate-replacement must not take request ownership")
	}
	if len(got) != 1 {
		t.Fatalf("gate-replacement call-closure appends = %d, want 1", len(got))
	}
	if opens.Load() != 0 {
		t.Fatalf("call-closure append must not drive provider retry, opens=%d", opens.Load())
	}
}

func TestTerminalUsageSinkIncludesNeverOpenedNextBLegAtRequestTerminal(t *testing.T) {
	var got []usageevidence.CallUsageRecord
	var legs []usageevidence.CallLegUsageRecord
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		return billingport.CallExposure{AccountID: "acct", CallID: in.CallID, PricingRef: usageevidence.VersionRef{ID: "pricing:test", Version: "1"}, ChargePolicyRef: usageevidence.VersionRef{ID: "policy:test", Version: "1"}, Status: billingport.ExposureOpen}, nil
	})
	ex.TerminalUsageSink = testTerminalSink{
		appendCall: func(_ context.Context, record usageevidence.CallUsageRecord) error {
			got = append(got, record)
			return nil
		},
		appendLeg: func(_ context.Context, record usageevidence.CallLegUsageRecord) error {
			sealed, err := record.Seal()
			if err != nil {
				return err
			}
			legs = append(legs, sealed)
			return nil
		},
	}
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-nextbleg", ContinuityKey: "sess-nextbleg"},
		Route:   aipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	aLegID := strings.TrimSpace(call.Session.ALegID)
	if aLegID == "" {
		t.Fatal("expected A-leg after execute")
	}
	atts, err := st.LoadAttempts(context.Background(), aLegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("attempts = %d, want never-opened + winner", len(atts))
	}
	if atts[0].Outcome != aipapi.AttemptSwallowedFailure || atts[1].Outcome != aipapi.AttemptSuccess {
		t.Fatalf("attempt outcomes = %s, %s", atts[0].Outcome, atts[1].Outcome)
	}
	if len(got) != 1 {
		t.Fatalf("call-closure appends = %d, want 1", len(got))
	}
	seen := map[string]bool{}
	for _, id := range got[0].ExpectedBLegIDs {
		seen[id] = true
	}
	if len(got[0].ExpectedBLegIDs) != 2 || !seen[atts[0].BLegID] {
		t.Fatalf("never-opened NextBLeg id %q missing from frozen expected set %#v", atts[0].BLegID, got[0].ExpectedBLegIDs)
	}
	if !seen[atts[1].BLegID] {
		t.Fatalf("winner NextBLeg id %q missing from frozen expected set %#v", atts[1].BLegID, got[0].ExpectedBLegIDs)
	}
	if len(legs) != 2 {
		t.Fatalf("independent terminal leg rows = %d, want never-started + winner", len(legs))
	}
	if _, err := usageevidence.JoinCompleteCall(got[0], legs); err != nil {
		t.Fatalf("call closure with never-started leg must join complete: %v", err)
	}
}

func TestTerminalUsageSinkSwallowedAttemptDoesNotFreezeUntilRequestTerminal(t *testing.T) {
	var got []usageevidence.CallUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		TerminalUsageSink: testTerminalSink{appendCall: func(_ context.Context, record usageevidence.CallUsageRecord) error {
			got = append(got, record)
			return nil
		}},
		BillingIdentity: testBillingIdentity(),
	}}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:        "a-1",
			billingCallID: callID,
			baseline:      aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-1"}},
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-swallowed", ALegID: "a-1", Seq: 1}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-a", Model: "model-a"}}, authorityLifecycle{}),
	}
	stream = stampStreamIdentity(stream, executor)
	if result := testTerminalizeAttempt(context.Background(), stream, sdkterminal.CommandSwallowedAttempt, nil); result.Err != nil {
		t.Fatal(result.Err)
	}
	if len(got) != 0 {
		t.Fatalf("swallowed attempt-terminal must not append call-closure, got %d", len(got))
	}
	if stream.facts.billingCallState != nil && stream.facts.billingCallState.hasFrozen {
		t.Fatal("swallowed attempt-terminal must not freeze allocated B-legs")
	}

	testInstallSlot(&stream.attempt, newAttemptSession(attemptSessionInput{
		bleg: b2bua.BLegRecord{BLegID: "b-replacement", ALegID: "a-1", Seq: 2},
		cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-b", Model: "model-b"}},
	}))
	if result := testTerminalizeRequest(context.Background(), stream, sdkterminal.CommandNormalFinish, nil); result.Err != nil {
		t.Fatal(result.Err)
	}
	if len(got) != 1 {
		t.Fatalf("request-owner call-closure appends = %d, want 1", len(got))
	}
	if len(got[0].ExpectedBLegIDs) != 2 || got[0].ExpectedBLegIDs[0] != "b-replacement" || got[0].ExpectedBLegIDs[1] != "b-swallowed" {
		t.Fatalf("sealed expected B-legs = %#v, want swallowed + replacement", got[0].ExpectedBLegIDs)
	}
}

func TestInterleavedThinkerBillingCorrectness(t *testing.T) {
	t.Parallel()

	// 1. Verify isInterleavedThinker controls request terminal claim and call closure.
	var closures []usageevidence.CallUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		TerminalUsageSink: testTerminalSink{appendCall: func(_ context.Context, record usageevidence.CallUsageRecord) error {
			closures = append(closures, record)
			return nil
		}},
		BillingIdentity: testBillingIdentity(),
	}}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}

	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:        "a-shared",
			billingCallID: callID,
			baseline:      aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-shared"}},
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-thinker", ALegID: "a-shared", Seq: 1}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}}, authorityLifecycle{}),
	}
	stream = stampStreamIdentity(stream, executor)
	stream.terminal.setInterleavedThinker()

	// A normal finish of a thinker must only claim attempt terminal and NOT write call closure
	ev := aipapi.Event{Kind: aipapi.EventResponseFinished}
	_, _, err = stream.terminal.finalizeResponseFinishedAuthority(context.Background(), ev, stream.facts.terminalFacts(), stream.attempt.snapshot(), stream.responsePipeline)
	if err != nil {
		t.Fatalf("finalizeResponseFinishedAuthority: %v", err)
	}

	if len(closures) != 0 {
		t.Fatalf("thinker B-leg normal finish must not write call closure, got %+v", closures)
	}
	if stream.terminal.requestTerminal().Owner().State() != sdkterminal.StateOpen {
		t.Fatalf("thinker B-leg normal finish must keep request terminal Open, got %s", stream.terminal.requestTerminal().Owner().State())
	}
	if testAttemptSession(stream).terminal.Owner().State() != sdkterminal.StateReleased {
		t.Fatalf("thinker B-leg normal finish must release attempt terminal, got %s", testAttemptSession(stream).terminal.Owner().State())
	}

	// 2. Verify continuation stream carries billing identity/snapshot fields from thinker.
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:       true,
			Reserved:      true,
			ReservationID: "reservation-executor",
			PolicyRecord:  policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, thinker := setupInterleavedAuthorityContinuation(t, auth, "hidden")

	// Set billing identity fields on the thinker stream
	thinker = withTestRecvFacts(thinker, func(f recvTurnFacts) recvTurnFacts {
		f.billingCallID = callID
		f.billingAccountID = "acct-cont"
		f.billingCustomerPricing = usageevidence.VersionRef{ID: "pricing:cont", Version: "1"}
		f.billingChargePolicy = usageevidence.VersionRef{ID: "policy:cont", Version: "1"}
		f.billingIdentityStamped = true
		return f
	})
	thinker.responsePipeline = &responsePipeline{customer: newCustomerEvidenceAccumulator()}

	state := interleavedstate.State{}
	cont, err := ex.openInterleavedExecutorContinuation(context.Background(), thinker, state)
	if err != nil {
		t.Fatalf("openInterleavedExecutorContinuation: %v", err)
	}

	if cont.facts.billingCallID != thinker.facts.billingCallID {
		t.Errorf("continuation missing billingCallID")
	}
	if cont.facts.billingAccountID != thinker.facts.billingAccountID {
		t.Errorf("continuation missing billingAccountID")
	}
	if cont.facts.billingCustomerPricing != thinker.facts.billingCustomerPricing {
		t.Errorf("continuation missing billingCustomerPricing")
	}
	if cont.facts.billingChargePolicy != thinker.facts.billingChargePolicy {
		t.Errorf("continuation missing billingChargePolicy")
	}
	if cont.facts.billingIdentityStamped != thinker.facts.billingIdentityStamped {
		t.Errorf("continuation missing billingIdentityStamped")
	}
	if cont.responsePipeline == nil || cont.responsePipeline.customer == nil {
		t.Errorf("continuation missing customer accumulator")
	}
	if cont.terminal.isInterleavedThinker() {
		t.Errorf("continuation must not be marked isInterleavedThinker")
	}
}

func testInterleavedExecutor(t *testing.T, backends map[string]execbackend.Backend) (*Executor, *b2bua.MemoryStore) {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = backends
	ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{
		Enabled:               true,
		Instructions:          "Think step by step.",
		StreamToClient:        "hidden",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}, memoStore)
	RegisterTestMemoStore(ex, memoStore)
	return ex, st
}

func testThinkerMemoStream(memoBody string) aipapi.ManagedEventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: interleavedthinking.MemoOpenTag + memoBody + interleavedthinking.MemoCloseTag},
		{Kind: aipapi.EventResponseFinished},
	})
}

func testExecutorTextStream(text string) aipapi.ManagedEventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: text},
		{Kind: aipapi.EventResponseFinished},
	})
}

func resumeTestInterleavedCall(first, second *aipapi.Call) {
	second.Session = aipapi.SessionRef{
		AuthoritativeSessionID: first.Session.AuthoritativeSessionID,
		ALegID:                 first.Session.ALegID,
		ClientSessionID:        first.Session.ClientSessionID,
		ResumeToken:            first.Session.ResumeToken,
	}
}

func TestExecutor_InterleavedHandoffAborted_ClosureSealed(t *testing.T) {
	t.Parallel()

	var gotClosures []usageevidence.CallUsageRecord
	var gotLegs []usageevidence.CallLegUsageRecord
	var mu sync.Mutex

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
	})

	var execOpens atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	backends := map[string]execbackend.Backend{
		"thinker-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return testThinkerMemoStream("plan"), nil
			},
		},
		"exec-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(octx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if execOpens.Add(1) == 1 {
					return testExecutorTextStream("first exec"), nil
				}
				cancel()
				return testExecutorTextStream("handoff exec"), nil
			},
		},
	}

	ex, _ := testInterleavedExecutor(t, backends)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		return billingport.CallExposure{AccountID: "acct", CallID: in.CallID, PricingRef: usageevidence.VersionRef{ID: "pricing", Version: "1"}, ChargePolicyRef: usageevidence.VersionRef{ID: "policy", Version: "1"}, Status: billingport.ExposureOpen}, nil
	})
	ex.TerminalUsageSink = testTerminalSink{
		appendCall: func(_ context.Context, record usageevidence.CallUsageRecord) error {
			mu.Lock()
			gotClosures = append(gotClosures, record)
			mu.Unlock()
			return nil
		},
		appendLeg: func(_ context.Context, record usageevidence.CallLegUsageRecord) error {
			sealed, _ := record.Seal()
			mu.Lock()
			gotLegs = append(gotLegs, sealed)
			mu.Unlock()
			return nil
		},
	}

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := &aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-handoff-abort", ContinuityKey: "sess-handoff-abort"},
		Route:   aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	firstStream, err := ex.Execute(ctx, first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(ctx, firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := &aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-handoff-abort", ContinuityKey: "sess-handoff-abort"},
		Route:   aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	resumeTestInterleavedCall(first, second)
	stream, err := ex.Execute(ctx, second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	_, err = aipapi.Collect(ctx, stream)
	if err == nil {
		t.Fatal("expected handoff abortion error")
	}

	mu.Lock()
	closuresLen := len(gotClosures)
	legsLen := len(gotLegs)
	mu.Unlock()

	if closuresLen != 2 {
		t.Fatalf("expected exactly 2 call closures, got %d", closuresLen)
	}
	if legsLen != 3 { // first exec, thinker, continuation exec
		t.Fatalf("expected exactly 3 leg usage records, got %d", legsLen)
	}

	mu.Lock()
	var handoffLegFound bool
	for _, leg := range gotLegs {
		t.Logf("Found leg: BLegID=%q, BackendID=%q, Outcome=%v", leg.BLegID, leg.BackendID, leg.Outcome)
		if leg.BackendID == "exec-be" && leg.Outcome == usageevidence.LegOutcomeCanceled {
			handoffLegFound = true
		}
	}
	mu.Unlock()
	if !handoffLegFound {
		t.Fatal("continuation B-leg not found in leg usage records")
	}
}

func TestExecutor_InterleavedCancelDuringTransition_ClosureSealed(t *testing.T) {
	t.Parallel()

	var gotClosures []usageevidence.CallUsageRecord
	var mu sync.Mutex

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
	})

	var execOpens atomic.Int32
	blockChan := make(chan struct{})

	backends := map[string]execbackend.Backend{
		"thinker-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return testThinkerMemoStream("plan"), nil
			},
		},
		"exec-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(octx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if execOpens.Add(1) == 1 {
					return testExecutorTextStream("first exec"), nil
				}
				<-blockChan
				return testExecutorTextStream("exec"), nil
			},
		},
	}

	ex, _ := testInterleavedExecutor(t, backends)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		return billingport.CallExposure{AccountID: "acct", CallID: in.CallID, PricingRef: usageevidence.VersionRef{ID: "pricing", Version: "1"}, ChargePolicyRef: usageevidence.VersionRef{ID: "policy", Version: "1"}, Status: billingport.ExposureOpen}, nil
	})
	ex.TerminalUsageSink = testTerminalSink{appendCall: func(_ context.Context, record usageevidence.CallUsageRecord) error {
		mu.Lock()
		gotClosures = append(gotClosures, record)
		mu.Unlock()
		return nil
	}}

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := &aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-cancel-trans", ContinuityKey: "sess-cancel-trans"},
		Route:   aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := &aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-cancel-trans", ContinuityKey: "sess-cancel-trans"},
		Route:   aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	resumeTestInterleavedCall(first, second)
	secondStream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	errChan := make(chan error, 1)
	go func() {
		_, err := aipapi.Collect(context.Background(), secondStream)
		errChan <- err
	}()

	time.Sleep(100 * time.Millisecond)

	managed, ok := secondStream.(aipapi.ManagedEventStream)
	if !ok {
		t.Fatal("second stream does not support managed cancellation")
	}
	_ = managed.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelContextDone})

	close(blockChan)

	collectErr := <-errChan
	if collectErr == nil {
		t.Fatal("expected cancellation error during collect")
	}

	mu.Lock()
	closuresLen := len(gotClosures)
	mu.Unlock()

	if closuresLen != 2 {
		t.Fatalf("expected exactly 2 call closures, got %d", closuresLen)
	}
}

func TestExecutor_InterleavedCloseDuringTransition_ClosureSealed(t *testing.T) {
	t.Parallel()

	var gotClosures []usageevidence.CallUsageRecord
	var mu sync.Mutex

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
	})

	var execOpens atomic.Int32
	blockChan := make(chan struct{})

	backends := map[string]execbackend.Backend{
		"thinker-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return testThinkerMemoStream("plan"), nil
			},
		},
		"exec-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(octx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if execOpens.Add(1) == 1 {
					return testExecutorTextStream("first exec"), nil
				}
				<-blockChan
				return testExecutorTextStream("exec"), nil
			},
		},
	}

	ex, _ := testInterleavedExecutor(t, backends)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		return billingport.CallExposure{AccountID: "acct", CallID: in.CallID, PricingRef: usageevidence.VersionRef{ID: "pricing", Version: "1"}, ChargePolicyRef: usageevidence.VersionRef{ID: "policy", Version: "1"}, Status: billingport.ExposureOpen}, nil
	})
	ex.TerminalUsageSink = testTerminalSink{appendCall: func(_ context.Context, record usageevidence.CallUsageRecord) error {
		mu.Lock()
		gotClosures = append(gotClosures, record)
		mu.Unlock()
		return nil
	}}

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := &aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-close-trans", ContinuityKey: "sess-close-trans"},
		Route:   aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := &aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-close-trans", ContinuityKey: "sess-close-trans"},
		Route:   aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	resumeTestInterleavedCall(first, second)
	secondStream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	errChan := make(chan error, 1)
	go func() {
		_, err := aipapi.Collect(context.Background(), secondStream)
		errChan <- err
	}()

	time.Sleep(100 * time.Millisecond)

	_ = secondStream.Close()

	close(blockChan)

	collectErr := <-errChan
	if collectErr == nil {
		t.Fatal("expected close error during collect")
	}

	mu.Lock()
	closuresLen := len(gotClosures)
	mu.Unlock()

	if closuresLen != 2 {
		t.Fatalf("expected exactly 2 call closures, got %d", closuresLen)
	}
}

func TestExecutor_InterleavedOpenFailureDuringTransition_ClosureSealed(t *testing.T) {
	t.Parallel()

	var gotClosures []usageevidence.CallUsageRecord
	var mu sync.Mutex

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
	})

	var execOpens atomic.Int32

	backends := map[string]execbackend.Backend{
		"thinker-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return testThinkerMemoStream("plan"), nil
			},
		},
		"exec-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if execOpens.Add(1) == 1 {
					return testExecutorTextStream("exec-first"), nil
				}
				return nil, aipapi.RecoverablePreOutputError(errors.New("executor unavailable"))
			},
		},
	}

	ex, _ := testInterleavedExecutor(t, backends)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		return billingport.CallExposure{AccountID: "acct", CallID: in.CallID, PricingRef: usageevidence.VersionRef{ID: "pricing", Version: "1"}, ChargePolicyRef: usageevidence.VersionRef{ID: "policy", Version: "1"}, Status: billingport.ExposureOpen}, nil
	})
	ex.TerminalUsageSink = testTerminalSink{appendCall: func(_ context.Context, record usageevidence.CallUsageRecord) error {
		mu.Lock()
		gotClosures = append(gotClosures, record)
		mu.Unlock()
		return nil
	}}

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := &aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-open-fail", ContinuityKey: "sess-open-fail"},
		Route:   aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := &aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-open-fail", ContinuityKey: "sess-open-fail"},
		Route:   aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	resumeTestInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	_, err = aipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected continuation open failure error")
	}

	mu.Lock()
	closuresLen := len(gotClosures)
	mu.Unlock()

	if closuresLen != 2 {
		t.Fatalf("expected exactly 2 call closures, got %d", closuresLen)
	}
}

func TestExecutor_InterleavedThinkerError_ClosureSealed(t *testing.T) {
	t.Parallel()

	var gotClosures []usageevidence.CallUsageRecord
	var mu sync.Mutex

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
	})

	var thinkerOpens atomic.Int32
	var execOpens atomic.Int32

	backends := map[string]execbackend.Backend{
		"thinker-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				thinkerOpens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventError, ErrorCode: "thinker_failed", ErrorMessage: "backend thinker failed midstream"},
				}), nil
			},
		},
		"exec-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				val := execOpens.Add(1)
				if val == 1 {
					return testExecutorTextStream("exec"), nil
				}
				t.Error("exec-be should not be opened on thinker error in second turn")
				return nil, errors.New("exec-be should not be opened")
			},
		},
	}

	ex, _ := testInterleavedExecutor(t, backends)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		return billingport.CallExposure{AccountID: "acct", CallID: in.CallID, PricingRef: usageevidence.VersionRef{ID: "pricing", Version: "1"}, ChargePolicyRef: usageevidence.VersionRef{ID: "policy", Version: "1"}, Status: billingport.ExposureOpen}, nil
	})
	ex.TerminalUsageSink = testTerminalSink{appendCall: func(_ context.Context, record usageevidence.CallUsageRecord) error {
		mu.Lock()
		gotClosures = append(gotClosures, record)
		mu.Unlock()
		return nil
	}}

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := &aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-thinker-err", ContinuityKey: "sess-thinker-err"},
		Route:   aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := &aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-thinker-err", ContinuityKey: "sess-thinker-err"},
		Route:   aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	resumeTestInterleavedCall(first, second)
	secondStream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	_, err = aipapi.Collect(context.Background(), secondStream)
	if err == nil {
		t.Fatal("expected midstream thinker error during collect")
	}

	mu.Lock()
	closuresLen := len(gotClosures)
	mu.Unlock()

	if closuresLen != 2 {
		t.Fatalf("expected exactly 2 call closures, got %d", closuresLen)
	}

	if execOpens.Load() != 1 {
		t.Errorf("expected exec-be to open exactly once, got %d", execOpens.Load())
	}
}
