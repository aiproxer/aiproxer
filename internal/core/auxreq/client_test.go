package auxreq_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	corestate "github.com/aiproxer/aiproxer/internal/core/state"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

type countingSubmit struct {
	id   string
	n    atomic.Int32
	fail sdk.FailureMode
}

func (c *countingSubmit) ID() string                   { return c.id }
func (c *countingSubmit) FailureMode() sdk.FailureMode { return c.fail }
func (c *countingSubmit) Order() int                   { return 0 }
func (c *countingSubmit) Handle(context.Context, *aipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
	c.n.Add(1)
	return sdk.SubmitDecision{}, nil
}

func TestClient_suppressedSubmitSkipped(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	counter := &countingSubmit{id: "watch-me", fail: sdk.FailOpen}
	bus := hooks.New(hooks.Config{
		SubmitHooks: []sdk.SubmitHook{counter},
	})
	var ex *runtime.Executor
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		State: corestate.NewMem(nil),
		Aux: auxreq.NewClient(func() auxreq.ExecutorRunner {
			return ex
		}),
	})
	mgr := mustSecureManager(t, st)
	ex = runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = true
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)

	ctx := context.Background()
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")},
		}},
	}

	primary := aipapi.CloneCall(*call)
	_, err = ex.Execute(ctx, &primary)
	if err != nil {
		t.Fatal(err)
	}
	if counter.n.Load() != 1 {
		t.Fatalf("primary submit hooks want 1 got %d", counter.n.Load())
	}

	counter.n.Store(0)
	aux := snap.Aux()
	ac := aipapi.CloneCall(*call)
	_, err = aux.Stream(ctx, auxiliary.Request{
		Call:           &ac,
		DisablePlugins: []string{"watch-me"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if counter.n.Load() != 0 {
		t.Fatalf("suppressed auxiliary path should skip submit hook, got %d", counter.n.Load())
	}
}

func TestClient_Stream_auxiliaryDepthIncremented(t *testing.T) {
	t.Parallel()
	var depthSeen atomic.Int32
	var ex *runtime.Executor
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		State: corestate.NewMem(nil),
		Aux: auxreq.NewClient(func() auxreq.ExecutorRunner {
			return ex
		}),
	})
	lineageStore := mustMemStore(t)
	mgr := mustSecureManager(t, lineageStore)
	ex = runtime.TestExecutor()
	ex.Store = lineageStore
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = true
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				depthSeen.Store(int32(execctx.AuxiliaryDepth(ctx)))
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(2)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")},
		}},
	}
	_, err := snap.Aux().Stream(context.Background(), auxiliary.Request{Call: call})
	if err != nil {
		t.Fatal(err)
	}
	if depthSeen.Load() != 1 {
		t.Fatalf("auxiliary depth want 1 got %d", depthSeen.Load())
	}
}

func TestClient_lineageExtension(t *testing.T) {
	t.Parallel()
	var ex *runtime.Executor
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		State: corestate.NewMem(nil),
		Aux: auxreq.NewClient(func() auxreq.ExecutorRunner {
			return ex
		}),
	})
	var captured aipapi.Call
	lineageStore := mustMemStore(t)
	mgr := mustSecureManager(t, lineageStore)
	ex = runtime.TestExecutor()
	ex.Store = lineageStore
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = true
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				captured = call
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(3)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")},
		}},
	}
	_, err := snap.Aux().Stream(context.Background(), auxiliary.Request{
		Call:          call,
		Role:          "verifier",
		ParentTraceID: "p-trace",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := captured.Extensions["aip.aux.lineage.v1"]
	if !ok || len(raw) == 0 {
		t.Fatalf("missing lineage extension: %#v", captured.Extensions)
	}
	s := string(raw)
	if !strings.Contains(s, "verifier") || !strings.Contains(s, "p-trace") {
		t.Fatalf("lineage payload unexpected: %s", s)
	}
}

func mustMemStore(t *testing.T) b2bua.Store {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func mustSecureManager(t *testing.T, lineageStore b2bua.Store) *app.Manager {
	t.Helper()
	fk := testkit.SecureSessionTestFingerprintKey()
	st := memory.New(memory.Options{SimulateDurable: true})
	mgr, err := app.NewManager(
		st,
		app.NewRandGenerator(fk),
		b2bualineage.New(lineageStore),
		app.ManagerConfig{
			FingerprintKey: fk,
			StoreDurable:   true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}
