package runtime_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

type testReplaceGate struct{}

func (testReplaceGate) ID() string                        { return "replace-all" }
func (testReplaceGate) Order() int                        { return 0 }
func (testReplaceGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testReplaceGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.ReplaceOutcome([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "replaced"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func TestExecute_completionGateReplacesStream(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompletionGates: []completion.Gate{testReplaceGate{}},
		}),
	})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "orig"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stream.Close(); err != nil {
			t.Errorf("stream close: %v", err)
		}
	})
	col, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if got := col.Text.String(); got != "replaced" {
		t.Fatalf("aggregated text: %q", got)
	}
	if !col.FinishReceived {
		t.Fatal("expected finish")
	}
}

type partialThenEOFStream struct {
	evs []aipapi.Event
	i   int
}

func (s *partialThenEOFStream) Recv(context.Context) (aipapi.Event, error) {
	if s.i < len(s.evs) {
		ev := s.evs[s.i]
		s.i++
		return ev, nil
	}
	return aipapi.Event{}, io.EOF
}

func (*partialThenEOFStream) Close() error { return nil }

func (*partialThenEOFStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func TestExecute_completionGate_truncatedUpstreamNoSyntheticSuccess(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompletionGates: []completion.Gate{testReplaceGate{}},
		}),
	})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &partialThenEOFStream{evs: []aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "partial"},
				}}, nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "trunc-gate"},
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stream.Close(); err != nil {
			t.Errorf("stream close: %v", err)
		}
	})
	if _, err := aipapi.Collect(context.Background(), stream); err == nil {
		t.Fatal("expected truncated stream collect error")
	}
	alegID := strings.TrimSpace(call.Session.ALegID)
	if alegID == "" {
		t.Fatal("expected aleg id on call after execute")
	}
	leg, err := st.FetchALeg(context.Background(), alegID)
	if err != nil {
		t.Fatal(err)
	}
	atts, err := st.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) == 0 {
		t.Fatal("expected attempt records")
	}
	last := atts[len(atts)-1]
	if last.Outcome != aipapi.AttemptSurfacedFailure {
		t.Fatalf("want AttemptSurfacedFailure got %s (%s)", last.Outcome, last.Reason)
	}
	if !strings.Contains(last.Reason, "response_finished") {
		t.Fatalf("reason: %q", last.Reason)
	}
}

type testPassGate struct{}

func (testPassGate) ID() string                        { return "pass" }
func (testPassGate) Order() int                        { return 0 }
func (testPassGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testPassGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

func TestExecute_completionGateOverflowLivePassthrough(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompletionGates: []completion.Gate{testPassGate{}},
		}),
	})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.CompletionBufferLimits = completion.BufferLimits{
		MaxEvents: 2,
	}
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "a"},
					{Kind: aipapi.EventTextDelta, Delta: "b"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stream.Close(); err != nil {
			t.Errorf("stream close: %v", err)
		}
	})
	col, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if got := col.Text.String(); got != "ab" {
		t.Fatalf("aggregated text: %q", got)
	}
	if !col.FinishReceived {
		t.Fatal("expected finish")
	}
}

type panicCompletionGate struct{}

func (panicCompletionGate) ID() string                        { return "panic-g" }
func (panicCompletionGate) Order() int                        { return 0 }
func (panicCompletionGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (panicCompletionGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	panic("completion gate boom")
}

// preOutputPanicOnlyGate always panics in Handle. Used with a no-delta stream so
// [aipapi.OutputCommitted] is false when ResponseFinished is processed (pre-output
// path for the completion-gate buffer).
type preOutputPanicOnlyGate struct{}

func (preOutputPanicOnlyGate) ID() string                        { return "pre-panic-only" }
func (preOutputPanicOnlyGate) Order() int                        { return 0 }
func (preOutputPanicOnlyGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (preOutputPanicOnlyGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	panic("pre-output completion gate")
}

func TestExecute_completionGatePanic_preOutput_recoverableWithoutCommittedOutput(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompletionGates: []completion.Gate{preOutputPanicOnlyGate{}},
		}),
	})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"minstream": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				// No text/tool deltas: pre-output; completion gate panics on ResponseFinished.
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "gate-panic-pre-recv"},
		Route:   aipapi.RouteIntent{Selector: "minstream:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stream.Close(); err != nil {
			t.Errorf("stream close: %v", err)
		}
	})
	_, err = aipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected collect error after pre-output completion-gate panic")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("pre-output completion-gate panic should map to recoverable pre-output, got %v", err)
	}
	// A second run with a fresh executor does not re-use a poisoned buffer (gateBuf cleared on panic).
	ex2 := runtime.TestExecutor()
	ex2.Store = st
	ex2.Bus = hooks.New(hooks.Config{})
	ex2.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(
		hooks.New(hooks.Config{}), extensions.SnapshotOptions{
			FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
				CompletionGates: []completion.Gate{testPassGate{}},
			}),
		},
	)
	ex2.Backends = map[string]execbackend.Backend{
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "x"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex2.Rand = routing.NewSeededRng(1)
	call2 := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "gate-ok-pre-panic-sanity"},
		Route:   aipapi.RouteIntent{Selector: "ok:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s2, err := ex2.Execute(context.Background(), call2)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	t.Cleanup(func() {
		if err := s2.Close(); err != nil {
			t.Errorf("s2 close: %v", err)
		}
	})
	if col, err := aipapi.Collect(context.Background(), s2); err != nil {
		t.Fatalf("second collect: %v", err)
	} else if col.Text.String() != "x" {
		t.Fatalf("second run text: %q", col.Text.String())
	}
}

func TestExecute_completionGatePanic_postCommittedNotRecoverable(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompletionGates: []completion.Gate{panicCompletionGate{}},
		}),
	})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "x"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "gate-panic-post"},
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stream.Close(); err != nil {
			t.Errorf("stream close: %v", err)
		}
	})
	_, err = aipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected collect error after completion gate panic")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatal("post-commit completion gate panic must not be recoverable pre-output")
	}
	var uf *aipapi.UpstreamFailureError
	if !errors.As(err, &uf) || uf.Phase != aipapi.PhasePostOutput {
		t.Fatalf("want post-output upstream failure, got %v", err)
	}
}
