package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// failAfterCommittedStream emits prefix events then returns fail on the next Recv.
// Used to prove OutputCommitted gating: a recoverable-shaped error after a content
// event must not open a secondary candidate.
type failAfterCommittedStream struct {
	events []aipapi.Event
	i      int
	fail   error
}

func (s *failAfterCommittedStream) Recv(context.Context) (aipapi.Event, error) {
	if s.i < len(s.events) {
		ev := s.events[s.i]
		s.i++
		return ev, nil
	}
	if s.fail != nil {
		return aipapi.Event{}, s.fail
	}
	return aipapi.Event{}, io.EOF
}
func (*failAfterCommittedStream) Close() error { return nil }
func (*failAfterCommittedStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func commitGateBaseCall(selector string) *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
}

func newCommitGateExecutor(t *testing.T, primary, secondary execbackend.Backend) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.MaxAttempts = 3
	ex.Rand = routing.NewSeededRng(11)
	ex.Backends = map[string]execbackend.Backend{
		"primary":   primary,
		"secondary": secondary,
	}
	return ex
}

func commitGateOpenCounter(opens *atomic.Int64, open func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error)) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return open(ctx, call, cand)
		},
	}
}

func secondarySuccessBackend(opens *atomic.Int64) execbackend.Backend {
	return commitGateOpenCounter(opens, func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: "secondary-ok"},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	})
}

func drainUntilError(t *testing.T, stream aipapi.EventStream) (committed []aipapi.Event, err error) {
	t.Helper()
	for {
		ev, rerr := stream.Recv(context.Background())
		if rerr != nil {
			return committed, rerr
		}
		if aipapi.OutputCommitted(ev) {
			committed = append(committed, ev)
		}
	}
}

func assertPostOutputNonRecoverable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected terminal error after committed output")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("post-commit error must not remain recoverable pre-output: %v", err)
	}
	var uf *aipapi.UpstreamFailureError
	if !errors.As(err, &uf) {
		t.Fatalf("want UpstreamFailureError classification, got %T %v", err, err)
	}
	if uf.Phase != aipapi.PhasePostOutput || uf.Recoverable {
		t.Fatalf("want PhasePostOutput non-recoverable, got phase=%q recoverable=%v", uf.Phase, uf.Recoverable)
	}
	msg := strings.ToLower(err.Error())
	for _, leak := range []string{"secret-opaque", "secret-text", `"summary"`, "rs_leak"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("error diagnostics must not echo reasoning/opaque body: %v", err)
		}
	}
}

// Positive control: RecoverablePreOutputError before any content-class event must
// open secondary. Proves the dual-candidate harness can failover when uncommitted.
// Drain manually (not Collect): primary may already have emitted response/message
// started before replacement, so a second response_started is expected on wire.
func TestOutputCommitFailoverGate_preOutputRecoverableReachesSecondary(t *testing.T) {
	t.Parallel()
	var primaryOpens, secondaryOpens atomic.Int64
	ex := newCommitGateExecutor(
		t,
		commitGateOpenCounter(&primaryOpens, func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &failAfterCommittedStream{
				events: []aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
				},
				fail: aipapi.RecoverablePreOutputError(errors.New("pre-output-temp")),
			}, nil
		}),
		secondarySuccessBackend(&secondaryOpens),
	)
	stream, err := ex.Execute(context.Background(), commitGateBaseCall("primary:m|secondary:m"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	defer func() { _ = stream.Close() }()
	var sawSecondaryText bool
	for {
		ev, rerr := stream.Recv(context.Background())
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			// Replacement may surface intermediate recoverable errors; keep draining.
			if aipapi.IsRecoverablePreOutput(rerr) {
				continue
			}
			t.Fatalf("unexpected recv error during pre-output failover: %v", rerr)
		}
		if ev.Kind == aipapi.EventTextDelta && ev.Delta == "secondary-ok" {
			sawSecondaryText = true
		}
	}
	if !sawSecondaryText {
		t.Fatal("positive control: secondary text must surface after recoverable pre-output primary failure")
	}
	if primaryOpens.Load() == 0 {
		t.Fatal("primary must open")
	}
	if secondaryOpens.Load() == 0 {
		t.Fatal("positive control: secondary must open after recoverable pre-output primary failure")
	}
}

func TestOutputCommitFailoverGate_textDeltaThenRecoverableBlocksSecondary(t *testing.T) {
	t.Parallel()
	var primaryOpens, secondaryOpens atomic.Int64
	ex := newCommitGateExecutor(
		t,
		commitGateOpenCounter(&primaryOpens, func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &failAfterCommittedStream{
				events: []aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "committed-visible"},
				},
				fail: aipapi.RecoverablePreOutputError(errors.New("would-retry-if-uncommitted")),
			}, nil
		}),
		secondarySuccessBackend(&secondaryOpens),
	)
	stream, err := ex.Execute(context.Background(), commitGateBaseCall("primary:m|secondary:m"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	defer func() { _ = stream.Close() }()
	committed, rerr := drainUntilError(t, stream)
	if len(committed) == 0 || committed[0].Kind != aipapi.EventTextDelta || committed[0].Delta != "committed-visible" {
		t.Fatalf("first committed event must be delivered TextDelta; got %#v", committed)
	}
	assertPostOutputNonRecoverable(t, rerr)
	if primaryOpens.Load() != 1 {
		t.Fatalf("primary opens=%d want 1", primaryOpens.Load())
	}
	if secondaryOpens.Load() != 0 {
		t.Fatalf("commit gate: secondary opens=%d want 0 (no failover after OutputCommitted TextDelta)", secondaryOpens.Load())
	}
}

func TestOutputCommitFailoverGate_reasoningPartThenRecoverableBlocksSecondary(t *testing.T) {
	t.Parallel()
	var primaryOpens, secondaryOpens atomic.Int64
	part := &aipapi.ReasoningPart{
		Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
		Opaque:  json.RawMessage(`{"id":"rs_gate","type":"reasoning","summary":[]}`),
	}
	ex := newCommitGateExecutor(
		t,
		commitGateOpenCounter(&primaryOpens, func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &failAfterCommittedStream{
				events: []aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventReasoningPart, Reasoning: part},
				},
				fail: aipapi.RecoverablePreOutputError(errors.New("would-retry-if-uncommitted")),
			}, nil
		}),
		secondarySuccessBackend(&secondaryOpens),
	)
	stream, err := ex.Execute(context.Background(), commitGateBaseCall("primary:m|secondary:m"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	defer func() { _ = stream.Close() }()
	committed, rerr := drainUntilError(t, stream)
	if len(committed) == 0 || committed[0].Kind != aipapi.EventReasoningPart || committed[0].Reasoning == nil {
		t.Fatalf("first committed event must be delivered EventReasoningPart; got %#v", committed)
	}
	assertPostOutputNonRecoverable(t, rerr)
	if primaryOpens.Load() != 1 {
		t.Fatalf("primary opens=%d want 1", primaryOpens.Load())
	}
	if secondaryOpens.Load() != 0 {
		t.Fatalf("commit gate: secondary opens=%d want 0 (no failover after OutputCommitted EventReasoningPart)", secondaryOpens.Load())
	}
}
