package runtimebundle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/prometheus/client_golang/prometheus"
)

type captureMetricsRegistry struct {
	collector *keepwarm.PrometheusCollector
}

func (c *captureMetricsRegistry) Register(col prometheus.Collector) error {
	if kw, ok := col.(*keepwarm.PrometheusCollector); ok {
		c.collector = kw
	}
	return nil
}

func newKeepwarmTimingProcess(t *testing.T) (*runtimebundle.ProcessServices, *captureMetricsRegistry) {
	t.Helper()
	cfg := keepwarmLedgerTestConfig()
	ps := mustKeepwarmLedgerProcess(t, cfg)
	capture := &captureMetricsRegistry{}
	fh, err := featurehost.NewProcess(context.Background(), featurehost.ProcessInput{
		Logger:          testkit.DiscardLogger(),
		MetricsRegistry: capture,
	})
	if err != nil {
		t.Fatalf("featurehost.NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = fh.Close() })
	if capture.collector == nil {
		t.Fatal("featurehost.NewProcess did not register a keepwarm.PrometheusCollector")
	}
	ps.StandardFeatures = fh
	return ps, capture
}

// TestCompileGeneration_MetricsSwapNarrowLifecycle verifies that MetricsSwap is
// never called during compilation, is executed exactly once upon StartPublished,
// and is idempotent on repeated StartPublished calls.
func TestCompileGeneration_MetricsSwapNarrowLifecycle(t *testing.T) {
	t.Parallel()
	ps, capture := newKeepwarmTimingProcess(t)
	cfg := keepwarmLedgerTestConfig()

	// 1. CompileCandidate must never invoke MetricsSwap.
	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}
	defer func() { _ = cand.Close() }()
	if got := capture.collector.SwapCount(); got != 0 {
		t.Fatalf("CompileCandidate invoked MetricsSwap: count=%d want 0", got)
	}

	// 2. CompileGeneration must never invoke MetricsSwap during compilation.
	gen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	defer func() { _ = gen.Close() }()

	// After compile / before StartPublished: count == 0.
	if got := capture.collector.SwapCount(); got != 0 {
		t.Fatalf("after CompileGeneration / before StartPublished: swap count=%d want 0", got)
	}
	if got := capture.collector.Manager(); got != nil {
		t.Fatalf("after CompileGeneration / before StartPublished: manager=%v want nil", got)
	}

	// StartPublished(ctx): count == 1.
	starter, ok := gen.(interface{ StartPublished(context.Context) error })
	if !ok {
		t.Fatal("generation does not expose StartPublished")
	}
	if err := starter.StartPublished(context.Background()); err != nil {
		t.Fatalf("StartPublished: %v", err)
	}
	if got := capture.collector.SwapCount(); got != 1 {
		t.Fatalf("after StartPublished: swap count=%d want 1", got)
	}
	if got := capture.collector.Manager(); got == nil {
		t.Fatal("after StartPublished: manager is nil")
	}

	// Second StartPublished(ctx): count still 1 (publishDone idempotence).
	if err := starter.StartPublished(context.Background()); err != nil {
		t.Fatalf("second StartPublished: %v", err)
	}
	if got := capture.collector.SwapCount(); got != 1 {
		t.Fatalf("after second StartPublished: swap count=%d want 1", got)
	}
}

// TestCompileGeneration_EndToEndPublication_RejectedCandidateDoesNotSwap verifies
// that a candidate whose publication is rejected with ErrRetentionBlocked never
// retargets process metrics, leaving the active generation's keep-warm manager in place.
func TestCompileGeneration_EndToEndPublication_RejectedCandidateDoesNotSwap(t *testing.T) {
	t.Parallel()
	ps, capture := newKeepwarmTimingProcess(t)
	cfg := keepwarmLedgerTestConfig()

	m := runtimehost.NewManager(1, nil)

	// Seed an initial generation g0, and pin it so it stays retained when gA publishes.
	g0 := m.Prepare("seed")
	if err := m.Publish(g0); err != nil {
		t.Fatalf("publish g0: %v", err)
	}
	lease0, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire g0")
	}
	pin0, ok := lease0.TransferPin(runtimehost.PinAsync)
	if !ok {
		t.Fatal("pin g0")
	}
	lease0.Release()

	// 1. Publish generation A; assert collector points to A's keep-warm manager.
	genA, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile genA: %v", err)
	}
	defer func() { _ = genA.Close() }()

	candA := m.PrepareRequestPlane("gen-a", genA)
	if err := m.Publish(candA); err != nil {
		t.Fatalf("publish genA: %v", err)
	}

	mgrA := capture.collector.Manager()
	if mgrA == nil {
		t.Fatal("expected non-nil manager after publishing genA")
	}
	if got := capture.collector.SwapCount(); got != 1 {
		t.Fatalf("after genA publish: swap count=%d want 1", got)
	}

	// 2. Compile B; force Manager.Publish(B) to fail with ErrRetentionBlocked.
	genB, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile genB: %v", err)
	}
	defer func() { _ = genB.Close() }()

	candB := m.PrepareRequestPlane("gen-b", genB)
	pubErr := m.Publish(candB)
	if !errors.Is(pubErr, runtimehost.ErrRetentionBlocked) {
		t.Fatalf("expected ErrRetentionBlocked, got %v", pubErr)
	}

	// 3. Assert A remains the active request plane and collector STILL points to A.
	leaseA, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire active generation")
	}
	if leaseA.RequestPlane() != genA {
		t.Fatalf("active request plane=%v want genA (%v)", leaseA.RequestPlane(), genA)
	}
	leaseA.Release()

	if got := capture.collector.Manager(); got != mgrA {
		t.Fatalf("collector manager shifted after rejected publish: got %p want mgrA %p", got, mgrA)
	}
	if got := capture.collector.SwapCount(); got != 1 {
		t.Fatalf("collector swap count changed after rejected publish: got %d want 1", got)
	}

	// 4. Release pin on g0, retire, and sweep closed so retention budget opens.
	pin0.Release()
	if _, err := m.RetireGeneration(context.Background(), g0); err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("retire g0: %v", err)
	}
	m.SweepClosed()

	// 5. Successfully publish C; collector points to C.
	genC, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile genC: %v", err)
	}
	defer func() { _ = genC.Close() }()

	candC := m.PrepareRequestPlane("gen-c", genC)
	if err := m.Publish(candC); err != nil {
		t.Fatalf("publish genC: %v", err)
	}

	mgrC := capture.collector.Manager()
	if mgrC == nil {
		t.Fatal("expected non-nil manager after publishing genC")
	}
	if mgrC == mgrA {
		t.Fatalf("expected collector to point to genC manager, still points to genA manager %p", mgrA)
	}
	if got := capture.collector.SwapCount(); got != 2 {
		t.Fatalf("after genC publish: swap count=%d want 2", got)
	}
}
