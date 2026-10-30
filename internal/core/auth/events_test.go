package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

type fakeSink struct {
	authErr   error
	sessErr   error
	authCalls int
	sessCalls int
	lastAuth  sdkauth.AuthDecisionEvent
	lastSess  sdkauth.SessionStartEvent
}

func (f *fakeSink) OnAuthDecision(_ context.Context, ev sdkauth.AuthDecisionEvent) error {
	f.authCalls++
	f.lastAuth = ev
	return f.authErr
}

func (f *fakeSink) OnSessionStart(_ context.Context, ev sdkauth.SessionStartEvent) error {
	f.sessCalls++
	f.lastSess = ev
	return f.sessErr
}

func TestEventDispatcher_nilSink_noCalls(t *testing.T) {
	t.Parallel()
	d := NewEventDispatcher(nil, EventFailureFailClosed)
	ctx := context.Background()
	evA := sampleAuthEvent()
	evS := sampleSessionEvent()
	if err := d.DispatchAuthDecision(ctx, evA); err != nil {
		t.Fatalf("DispatchAuthDecision: %v", err)
	}
	if err := d.DispatchSessionStart(ctx, evS); err != nil {
		t.Fatalf("DispatchSessionStart: %v", err)
	}
}

func TestEventDispatcher_success_deliversEvents(t *testing.T) {
	t.Parallel()
	sink := &fakeSink{}
	d := NewEventDispatcher(sink, EventFailureBestEffort)
	ctx := context.Background()
	evA := sampleAuthEvent()
	evS := sampleSessionEvent()
	if err := d.DispatchAuthDecision(ctx, evA); err != nil {
		t.Fatalf("DispatchAuthDecision: %v", err)
	}
	if err := d.DispatchSessionStart(ctx, evS); err != nil {
		t.Fatalf("DispatchSessionStart: %v", err)
	}
	if sink.authCalls != 1 || sink.sessCalls != 1 {
		t.Fatalf("calls: auth=%d sess=%d", sink.authCalls, sink.sessCalls)
	}
	if sink.lastAuth.Outcome != evA.Outcome || sink.lastAuth.TraceID != evA.TraceID {
		t.Fatalf("auth event mismatch: %+v vs %+v", sink.lastAuth, evA)
	}
	if sink.lastSess.SessionID != evS.SessionID {
		t.Fatalf("session event mismatch")
	}
}

func TestEventDispatcher_bestEffort_swallowsSinkError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("sink down")
	sink := &fakeSink{authErr: wantErr, sessErr: wantErr}
	d := NewEventDispatcher(sink, EventFailureBestEffort)
	ctx := context.Background()
	if err := d.DispatchAuthDecision(ctx, sampleAuthEvent()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if err := d.DispatchSessionStart(ctx, sampleSessionEvent()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestEventDispatcher_failClosed_propagatesSinkError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("sink down")
	sink := &fakeSink{authErr: wantErr}
	d := NewEventDispatcher(sink, EventFailureFailClosed)
	ctx := context.Background()
	if err := d.DispatchAuthDecision(ctx, sampleAuthEvent()); !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
	sink2 := &fakeSink{sessErr: wantErr}
	d2 := NewEventDispatcher(sink2, EventFailureFailClosed)
	if err := d2.DispatchSessionStart(ctx, sampleSessionEvent()); !errors.Is(err, wantErr) {
		t.Fatalf("session: expected %v, got %v", wantErr, err)
	}
}

func TestEventDispatcher_serializesConcurrentSinkDelivery(t *testing.T) {
	t.Parallel()
	const calls = 64
	sink := &fakeSink{}
	d := NewEventDispatcher(sink, EventFailureBestEffort)
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(calls)
	for range calls {
		go func() {
			defer wg.Done()
			if err := d.DispatchAuthDecision(ctx, sampleAuthEvent()); err != nil {
				t.Errorf("DispatchAuthDecision: %v", err)
			}
		}()
	}
	wg.Wait()

	if sink.authCalls != calls {
		t.Fatalf("auth calls: got %d want %d", sink.authCalls, calls)
	}
}

func sampleAuthEvent() sdkauth.AuthDecisionEvent {
	return sdkauth.AuthDecisionEvent{
		Time:       time.Unix(1700000000, 0).UTC(),
		TraceID:    "trace-auth-1",
		AccessMode: sdkauth.AccessSingleUser,
		Outcome:    sdkauth.OutcomeAllow,
		Frontend:   "openai",
	}
}

func sampleSessionEvent() sdkauth.SessionStartEvent {
	return sdkauth.SessionStartEvent{
		Time:       time.Unix(1700000001, 0).UTC(),
		TraceID:    "trace-sess-1",
		AccessMode: sdkauth.AccessSingleUser,
		SessionID:  "sess-1",
		Frontend:   "openai",
	}
}
