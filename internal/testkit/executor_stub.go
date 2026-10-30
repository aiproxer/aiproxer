package testkit

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func stubToolPrefixEvents(call aipapi.Call) []aipapi.Event {
	if len(call.Tools) == 0 {
		return nil
	}
	name := strings.TrimSpace(call.Tools[0].Name)
	if name == "" {
		name = "stub_tool"
	}
	return []aipapi.Event{
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_stub1", ToolName: name},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_stub1", Delta: `{"q":"ok"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_stub1"},
	}
}

// WireConformanceExecutorSecureSession attaches an in-memory secure-session manager and
// aipapi denial mapping for conformance harness executors (and other tests that construct
// [*runtime.Executor] outside package runtime).
func WireConformanceExecutorSecureSession(tb testing.TB, ex *runtime.Executor) {
	tb.Helper()
	wireStubExecutorSecureSession(tb, ex)
}

func wireStubExecutorSecureSession(tb testing.TB, ex *runtime.Executor) {
	tb.Helper()
	if ex.SecureSession != nil {
		return
	}
	if ex.Store == nil {
		tb.Fatal("stub executor requires store")
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	fk := SecureSessionTestFingerprintKey()
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(fk), b2bualineage.New(ex.Store), app.ManagerConfig{
		FingerprintKey: fk,
		StoreDurable:   true,
	})
	if err != nil {
		tb.Fatal(err)
	}
	ex.SecureSession = mgr
	if ex.SessionDenialMapper == nil {
		ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	}
	ex.SyntheticLocalPrincipal = true
}

func NewStubExecutorWithDeltas(t *testing.T, caps aipapi.BackendCaps, deltas []string, capture *sync.Map) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(42)
	ex.Backends = map[string]execbackend.Backend{
		"stub": {
			Caps: caps,
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if capture != nil {
					capture.Store("last", call)
				}
				_ = ctx
				_ = cand
				prefix := stubToolPrefixEvents(call)
				evs := make([]aipapi.Event, 0, 2+len(prefix)+len(deltas)+1)
				evs = append(
					evs,
					aipapi.Event{Kind: aipapi.EventResponseStarted},
					aipapi.Event{Kind: aipapi.EventMessageStarted},
				)
				evs = append(evs, prefix...)
				for _, d := range deltas {
					evs = append(evs, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: d})
				}
				evs = append(evs, aipapi.Event{Kind: aipapi.EventResponseFinished})
				return aipapi.NewFixedEventStream(evs), nil
			},
		},
	}
	wireStubExecutorSecureSession(t, ex)
	return ex
}

func NewStubExecutor(t *testing.T, caps aipapi.BackendCaps, text string, capture *sync.Map) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(42)
	ex.Backends = map[string]execbackend.Backend{
		"stub": {
			Caps: caps,
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if capture != nil {
					capture.Store("last", call)
				}
				_ = ctx
				_ = cand
				prefix := stubToolPrefixEvents(call)
				evs := make([]aipapi.Event, 0, 2+len(prefix)+2)
				evs = append(
					evs,
					aipapi.Event{Kind: aipapi.EventResponseStarted},
					aipapi.Event{Kind: aipapi.EventMessageStarted},
				)
				evs = append(evs, prefix...)
				evs = append(
					evs,
					aipapi.Event{Kind: aipapi.EventTextDelta, Delta: text},
					aipapi.Event{Kind: aipapi.EventResponseFinished},
				)
				return aipapi.NewFixedEventStream(evs), nil
			},
		},
	}
	wireStubExecutorSecureSession(t, ex)
	return ex
}
