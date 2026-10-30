//go:build precommit

package runtime_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// v1_review_matrix_test encodes regression cases from .kiro/specs/archive/go-core-reimplementation-stage-two/v1_code_review.md
// (orchestration matrix: baseline vs attempts, hook metadata, max_attempts, B-leg switches).

func TestV1Matrix_submitHook_receivesTraceID(t *testing.T) {
	t.Parallel()
	var got *sdk.SubmitMeta
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sub := &submitMetaCapture{fn: func(_ context.Context, _ *aipapi.Call, meta *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
		cp := *meta
		got = &cp
		return sdk.SubmitDecision{}, nil
	}}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{SubmitHooks: []sdk.SubmitHook{sub}})
	ex.Rand = routing.NewSeededRng(4)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	call := &aipapi.Call{
		ID:    "trace-submit-1",
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	if _, err := ex.Execute(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.TraceID != "trace-submit-1" {
		t.Fatalf("SubmitMeta.TraceID: got %+v", got)
	}
}

type submitMetaCapture struct {
	fn func(context.Context, *aipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error)
}

func (s submitMetaCapture) ID() string                   { return "cap" }
func (s submitMetaCapture) Order() int                   { return 0 }
func (s submitMetaCapture) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (s submitMetaCapture) Handle(ctx context.Context, call *aipapi.Call, meta *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
	return s.fn(ctx, call, meta)
}

func TestV1Matrix_requestHook_metaChangesOnRecvReplacementBLeg(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var metas []sdk.PartMeta
	reqHook := &executorTestReqPart{
		id: "matrix-req", order: 0,
		fn: func(_ context.Context, _ *aipapi.Call, meta sdk.PartMeta) error {
			mu.Lock()
			metas = append(metas, meta)
			mu.Unlock()
			return nil
		},
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdk.RequestPartHook{reqHook}})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &flakyThenEOFStream{
					first: []aipapi.Event{{Kind: aipapi.EventResponseStarted}},
					then:  aipapi.RecoverablePreOutputError(errors.New("recv fail")),
				}, nil
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "matrix-bleg-switch"},
		Route:   aipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for {
		_, err := s.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
	}
	_ = s.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(metas) < 2 {
		t.Fatalf("expected at least two request-hook invocations (initial open + replacement), got %d", len(metas))
	}
	// First and second open should use different B-legs (IDs differ or seq increases).
	a, b := metas[0], metas[1]
	if a.BLegID == "" || b.BLegID == "" {
		t.Fatalf("missing BLegID: %+v %+v", a, b)
	}
	if a.BLegID == b.BLegID && a.AttemptSeq == b.AttemptSeq {
		t.Fatalf("expected distinct B-leg identity across replacement, got %+v and %+v", a, b)
	}
	if a.TraceID == "" || b.TraceID == "" || a.ALegID == "" || b.ALegID == "" {
		t.Fatalf("TraceID/ALegID must be populated: %+v %+v", a, b)
	}
}

// flakyThenEOFStream yields first batch then returns recoverable error on subsequent Recv.
type flakyThenEOFStream struct {
	i     int
	first []aipapi.Event
	then  error
}

func (f *flakyThenEOFStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	if f.i < len(f.first) {
		ev := f.first[f.i]
		f.i++
		return ev, nil
	}
	if f.then != nil {
		return aipapi.Event{}, f.then
	}
	return aipapi.Event{}, io.EOF
}

func (f *flakyThenEOFStream) Close() error { return nil }

func (f *flakyThenEOFStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func TestV1Matrix_requestHookMutationNotCompoundedAcrossRecvFailover(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reqHook := &executorTestReqPart{
		id: "append-once", order: 0,
		fn: func(_ context.Context, call *aipapi.Call, _ sdk.PartMeta) error {
			if len(call.Messages) == 0 || len(call.Messages[0].Parts) == 0 {
				return nil
			}
			call.Messages[0].Parts = append(call.Messages[0].Parts, aipapi.TextPart("[hook]"))
			return nil
		},
	}
	var partLens []int
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdk.RequestPartHook{reqHook}})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				partLens = append(partLens, len(call.Messages[0].Parts))
				return &flakyThenEOFStream{
					first: []aipapi.Event{{Kind: aipapi.EventResponseStarted}},
					then:  aipapi.RecoverablePreOutputError(errors.New("recv fail")),
				}, nil
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				partLens = append(partLens, len(call.Messages[0].Parts))
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "matrix-no-compound"},
		Route:   aipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, err := s.Recv(context.Background())
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
	}
	_ = s.Close()
	if len(partLens) < 2 {
		t.Fatalf("expected two backend opens, got %v", partLens)
	}
	// Each attempt is derived from baseline: user text + single hook suffix, not stacked markers.
	for i, n := range partLens {
		if n != 2 {
			t.Fatalf("open %d: want 2 parts (hi + one hook marker), got %d", i, n)
		}
	}
}
