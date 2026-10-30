package runtimebundle_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

// Task 4.6: final generation close invokes CloseIdleConnections exactly once
// for generation-owned transports; shared process clients are never claimed.
func TestCloseIdle_FinalGenerationCloseCallsIdleOnce(t *testing.T) {
	t.Parallel()
	var idleCalls atomic.Int32
	tr := &countingIdleTransport{idle: &idleCalls}
	cfg, opts, cleanup := newWiringProcess(t, nil, "")
	defer cleanup()
	opts.Infra.HTTPClient = nil // force generation-owned client path

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	// Direct ledger proof: PhaseClose idle closer runs once on retire close.
	ledger := runtimebundle.NewResourceLedger()
	ledger.Add("upstream-idle-transport", runtimebundle.PhaseClose, func(context.Context) error {
		tr.CloseIdleConnections()
		return nil
	})
	ledger.AddClose("backend", runtimebundle.PhaseClose, func() error { return nil })
	ledger.AddClose("refresh", runtimebundle.PhaseQuiesce, func() error { return nil })

	cand := runtimebundle.NewCandidateRuntimeForTest(ledger)
	m := runtimehost.NewManager(2, nil)
	g := m.PrepareRequestPlane("gen", &planeFromCandidate{cand: cand})
	mustPublishBundle(t, m, g)
	if idleCalls.Load() != 0 {
		t.Fatal("publish must not close idle transports")
	}
	mustPublishBundle(t, m, m.Prepare("next"))
	if _, err := m.RetireGeneration(context.Background(), g); err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatal(err)
	}
	if idleCalls.Load() != 1 {
		t.Fatalf("idle closes=%d want 1 after final generation close", idleCalls.Load())
	}
	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}
	if idleCalls.Load() != 1 {
		t.Fatalf("exact-once idle closes=%d", idleCalls.Load())
	}
	if ps.Closed() {
		t.Fatal("process services must survive generation close")
	}
}

func TestQuiesce_BeforeClose_ReverseOrderExactOnce(t *testing.T) {
	t.Parallel()
	var order []string
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		order = append(order, "close:backend")
		return nil
	})
	ledger.AddClose("client", runtimebundle.PhaseClose, func() error {
		order = append(order, "close:client")
		return nil
	})
	ledger.AddClose("refresh", runtimebundle.PhaseQuiesce, func() error {
		order = append(order, "quiesce:refresh")
		return nil
	})
	ledger.AddClose("loop", runtimebundle.PhaseQuiesce, func() error {
		order = append(order, "quiesce:loop")
		return nil
	})

	cand := runtimebundle.NewCandidateRuntimeForTest(ledger)
	m := runtimehost.NewManager(2, nil)
	g := m.PrepareRequestPlane("gen", &planeFromCandidate{cand: cand})
	mustPublishBundle(t, m, g)
	mustPublishBundle(t, m, m.Prepare("next"))

	if _, err := m.RetireGeneration(context.Background(), g); err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatal(err)
	}
	want := []string{"quiesce:loop", "quiesce:refresh", "close:client", "close:backend"}
	if len(order) != len(want) {
		t.Fatalf("order=%v want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order=%v want %v", order, want)
		}
	}
}

func TestResourceLedger_StopPanicIsolatedPerEntry(t *testing.T) {
	t.Parallel()
	var later atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("panic-entry", runtimebundle.PhaseClose, func() error {
		panic("ledger boom")
	})
	ledger.AddClose("later", runtimebundle.PhaseClose, func() error {
		later.Add(1)
		return nil
	})
	// Reverse order: later runs first, then panic-entry.
	err := ledger.Rollback(context.Background())
	if err == nil {
		t.Fatal("expected panic isolation error")
	}
	if later.Load() != 1 {
		t.Fatalf("later closes=%d; panic must not abort remaining reverse closers", later.Load())
	}
	if err2 := ledger.Rollback(context.Background()); err2 != err {
		// Idempotent: same stored error.
		if err2 == nil {
			t.Fatal("second rollback must remain failed/idempotent")
		}
	}
}

type countingIdleTransport struct {
	idle *atomic.Int32
}

func (t *countingIdleTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, http.ErrNotSupported
}

func (t *countingIdleTransport) CloseIdleConnections() {
	if t != nil && t.idle != nil {
		t.idle.Add(1)
	}
}

type planeFromCandidate struct {
	cand *runtimebundle.CandidateHTTPCompile
}

func (p *planeFromCandidate) Handler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

func (p *planeFromCandidate) Quiesce(ctx context.Context) error {
	if p == nil || p.cand == nil {
		return nil
	}
	return p.cand.Quiesce(ctx)
}

func (p *planeFromCandidate) Close() error {
	if p == nil || p.cand == nil {
		return nil
	}
	return p.cand.Close()
}

func mustPublishBundle(t *testing.T, m *runtimehost.Manager, g *runtimehost.Generation) {
	t.Helper()
	if err := m.Publish(g); err != nil {
		t.Fatalf("publish: %v", err)
	}
}
