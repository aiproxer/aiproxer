//go:build precommit

package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestExecutor_weightedFirstBranch_persistsConsumed(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(99)
	ex.Backends = map[string]execbackend.Backend{
		"cheap": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
		"expensive": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				t.Fatal("expensive backend must not open when [first] cheap arm succeeds")
				return nil, errors.New("unexpected")
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "wf-first"},
		Route:   aipapi.RouteIntent{Selector: "[first]cheap:m^[weight=100]expensive:m"},
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
	alegID := strings.TrimSpace(call.Session.ALegID)
	if alegID == "" {
		t.Fatal("expected aleg id on call after execute")
	}
	got, err := st.FetchALeg(context.Background(), alegID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.WeightedFirstConsumed {
		t.Fatal("expected WeightedFirstConsumed persisted on A-leg after [first] branch open")
	}
}

type storeAssertRecordWithoutCancel struct {
	*b2bua.MemoryStore
	t *testing.T
}

func (s *storeAssertRecordWithoutCancel) RecordAttempt(ctx context.Context, rec aipapi.AttemptRecord) error {
	if ctx.Err() != nil {
		s.t.Fatalf("RecordAttempt must receive non-canceled context (WithoutCancel); got %v", ctx.Err())
	}
	return s.MemoryStore.RecordAttempt(ctx, rec)
}

func TestExecutor_recordAttempt_usesWithoutCancelOnCanceledRequestCtx(t *testing.T) {
	t.Parallel()
	base, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st := &storeAssertRecordWithoutCancel{MemoryStore: base, t: t}
	ctx, cancel := context.WithCancel(context.Background())
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"slow": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				_ = call
				_ = cand
				return &cancelWaitStream{ctx: ctx}, nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "wc-sess"},
		Route:   aipapi.RouteIntent{Selector: "slow:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = stream.Recv(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v", err)
	}
	_ = stream.Close()
}

type captureRouteObserver struct {
	mu  sync.Mutex
	got []routeObs
}

type routeObs struct {
	decision string
	detail   string
}

func (o *captureRouteObserver) ObserveRouteDecision(_ context.Context, _ string, decision, detail string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.got = append(o.got, routeObs{decision: decision, detail: detail})
}

func TestExecutor_plan_candidate_observedAndTraced(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obs := &captureRouteObserver{}
	rt := diag.NewRouteTraceBuffer(8)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.RouteObserver = obs
	ex.RouteTrace = rt
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
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
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	obs.mu.Lock()
	var found bool
	for _, e := range obs.got {
		if e.decision == "plan_candidate" && e.detail == "openai:gpt-4" {
			found = true
			break
		}
	}
	obs.mu.Unlock()
	if !found {
		t.Fatalf("expected plan_candidate observation, got %#v", obs.got)
	}
	snap := rt.Snapshot()
	var traced bool
	for _, e := range snap {
		if e.Decision == "plan_candidate" && e.Detail == "openai:gpt-4" {
			traced = true
			break
		}
	}
	if !traced {
		t.Fatalf("expected route trace plan_candidate, got %#v", snap)
	}
}

var _ aipsdk.RouteObserver = (*captureRouteObserver)(nil)
