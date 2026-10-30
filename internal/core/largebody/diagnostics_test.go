package largebody_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

func TestDiagnostics_SizeBucket(t *testing.T) {
	t.Parallel()

	cases := []struct {
		bytes int64
		want  string
	}{
		{0, "lt_32k"},
		{1024, "lt_32k"},
		{32*1024 - 1, "lt_32k"},
		{32 * 1024, "32k_256k"},
		{100 * 1024, "32k_256k"},
		{256*1024 - 1, "32k_256k"},
		{256 * 1024, "256k_1m"},
		{512 * 1024, "256k_1m"},
		{1024*1024 - 1, "256k_1m"},
		{1024 * 1024, "1m_5m"},
		{3 * 1024 * 1024, "1m_5m"},
		{5*1024*1024 - 1, "1m_5m"},
		{5 * 1024 * 1024, "5m_20m"},
		{15 * 1024 * 1024, "5m_20m"},
		{20*1024*1024 - 1, "5m_20m"},
		{20 * 1024 * 1024, "gte_20m"},
		{100 * 1024 * 1024, "gte_20m"},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d_bytes", tc.bytes), func(t *testing.T) {
			t.Parallel()
			got := largebody.SizeBucket(tc.bytes)
			if got != tc.want {
				t.Fatalf("SizeBucket(%d) = %q, want %q", tc.bytes, got, tc.want)
			}
		})
	}
}

func TestDiagnostics_StaticDeclineReasonResolution(t *testing.T) {
	t.Parallel()

	// 1. Gate reasons without summary dependency
	t.Run("feature_disabled", func(t *testing.T) {
		t.Parallel()
		got := largebody.ResolveStaticDeclineReason(largebody.WireEligibilitySummary{}, largebody.StaticWireReasonFeatureDisabled)
		if got != largebody.DeclineReasonFeatureDisabled {
			t.Fatalf("want %q, got %q", largebody.DeclineReasonFeatureDisabled, got)
		}
	})

	t.Run("below_threshold", func(t *testing.T) {
		t.Parallel()
		got := largebody.ResolveStaticDeclineReason(largebody.WireEligibilitySummary{}, largebody.StaticWireReasonBelowThreshold)
		if got != largebody.DeclineReasonBelowThreshold {
			t.Fatalf("want %q, got %q", largebody.DeclineReasonBelowThreshold, got)
		}
	})

	t.Run("gzip_compressed", func(t *testing.T) {
		t.Parallel()
		got := largebody.ResolveStaticDeclineReason(largebody.WireEligibilitySummary{}, largebody.StaticWireReasonGzipCompressed)
		if got != largebody.DeclineReasonGzipCompressed {
			t.Fatalf("want %q, got %q", largebody.DeclineReasonGzipCompressed, got)
		}
	})

	t.Run("legacy_resolver", func(t *testing.T) {
		t.Parallel()
		got := largebody.ResolveStaticDeclineReason(largebody.WireEligibilitySummary{}, largebody.StaticWireReasonLegacyResolverConfigured)
		if got != largebody.DeclineReasonFrontendRouteResolver {
			t.Fatalf("want %q, got %q", largebody.DeclineReasonFrontendRouteResolver, got)
		}
	})

	// 2. Summary plane blockers: local_turn, secret_guard, terminal_decision
	t.Run("local_turn", func(t *testing.T) {
		t.Parallel()
		summary := compileSummaryWithPlane(t, "local_turn_handlers")
		got := largebody.ResolveStaticDeclineReason(summary, largebody.StaticWireReasonStaticBlocker)
		if got != largebody.DeclineReasonLocalTurn {
			t.Fatalf("want %q, got %q", largebody.DeclineReasonLocalTurn, got)
		}
	})

	t.Run("secret_guard", func(t *testing.T) {
		t.Parallel()
		summary := compileSummaryWithPlane(t, "secret_guard_execution")
		got := largebody.ResolveStaticDeclineReason(summary, largebody.StaticWireReasonStaticBlocker)
		if got != largebody.DeclineReasonSecretGuard {
			t.Fatalf("want %q, got %q", largebody.DeclineReasonSecretGuard, got)
		}
	})

	t.Run("terminal_decision", func(t *testing.T) {
		t.Parallel()
		summary := compileSummaryWithPlane(t, "terminal_decision_provider")
		got := largebody.ResolveStaticDeclineReason(summary, largebody.StaticWireReasonStaticBlocker)
		if got != largebody.DeclineReasonTerminalDecision {
			t.Fatalf("want %q, got %q", largebody.DeclineReasonTerminalDecision, got)
		}
	})

	// 3. Summary port blockers: traffic, accounting/counting, custom_call_callback, backend_domain
	t.Run("traffic", func(t *testing.T) {
		t.Parallel()
		summary := compileSummaryWithPort(t, largebody.NarrowPortEligibilityInput{TrafficCapturing: true})
		got := largebody.ResolveStaticDeclineReason(summary, largebody.StaticWireReasonStaticBlocker)
		if got != largebody.DeclineReasonTraffic {
			t.Fatalf("want %q, got %q", largebody.DeclineReasonTraffic, got)
		}
	})

	t.Run("accounting_counting", func(t *testing.T) {
		t.Parallel()
		summary := compileSummaryWithPort(t, largebody.NarrowPortEligibilityInput{TokenCountingRequired: true})
		got := largebody.ResolveStaticDeclineReason(summary, largebody.StaticWireReasonStaticBlocker)
		if got != largebody.DeclineReasonAccountingCounting {
			t.Fatalf("want %q, got %q", largebody.DeclineReasonAccountingCounting, got)
		}
	})

	t.Run("custom_call_callback", func(t *testing.T) {
		t.Parallel()
		summary := compileSummaryWithPort(t, largebody.NarrowPortEligibilityInput{CustomCallCallbacksPresent: true})
		got := largebody.ResolveStaticDeclineReason(summary, largebody.StaticWireReasonStaticBlocker)
		if got != largebody.DeclineReasonCustomCallCallback {
			t.Fatalf("want %q, got %q", largebody.DeclineReasonCustomCallCallback, got)
		}
	})

	t.Run("backend_domain", func(t *testing.T) {
		t.Parallel()
		summary := compileSummaryWithPort(t, largebody.NarrowPortEligibilityInput{BackendsEmpty: true})
		got := largebody.ResolveStaticDeclineReason(summary, largebody.StaticWireReasonStaticBlocker)
		if got != largebody.DeclineReasonBackendDomain {
			t.Fatalf("want %q, got %q", largebody.DeclineReasonBackendDomain, got)
		}
	})
}

func TestDiagnostics_SpoolLedgerObserver(t *testing.T) {
	t.Parallel()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 * 1024,
		MaxInflightSpoolBytes: 10 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	obs := &testSpoolObserver{}
	ledger.SetObserver(obs)

	// Initial notification of 0 bytes
	if obs.get() != 0 {
		t.Fatalf("initial bytes = %d, want 0", obs.get())
	}

	// 1. Reserve 1000 bytes
	res, err := ledger.Reserve(1000)
	if err != nil {
		t.Fatalf("Reserve failed: %v", err)
	}
	if obs.get() != 1000 {
		t.Fatalf("after Reserve(1000), got %d want 1000", obs.get())
	}

	// 2. ReserveMore 500 bytes
	if err := res.ReserveMore(500); err != nil {
		t.Fatalf("ReserveMore failed: %v", err)
	}
	if obs.get() != 1500 {
		t.Fatalf("after ReserveMore(500), got %d want 1500", obs.get())
	}

	// 3. ShrinkTo 800 bytes
	if err := res.ShrinkTo(800); err != nil {
		t.Fatalf("ShrinkTo failed: %v", err)
	}
	if obs.get() != 800 {
		t.Fatalf("after ShrinkTo(800), got %d want 800", obs.get())
	}

	// 4. Release
	if freed := res.Release(); freed != 800 {
		t.Fatalf("Release returned %d, want 800", freed)
	}
	if obs.get() != 0 {
		t.Fatalf("after Release(), got %d want 0", obs.get())
	}
}

func TestDiagnostics_BoundedStaticLabelsOnly(t *testing.T) {
	t.Parallel()

	// Verify all decline reasons are bounded static labels
	reasons := []string{
		largebody.DeclineReasonLocalTurn,
		largebody.DeclineReasonSecretGuard,
		largebody.DeclineReasonTerminalDecision,
		largebody.DeclineReasonFrontendRouteResolver,
		largebody.DeclineReasonTraffic,
		largebody.DeclineReasonAccountingCounting,
		largebody.DeclineReasonCustomCallCallback,
		largebody.DeclineReasonBackendDomain,
		largebody.DeclineReasonFeatureDisabled,
		largebody.DeclineReasonBelowThreshold,
		largebody.DeclineReasonGzipCompressed,
		largebody.DeclineReasonStaticBlocker,
		largebody.DeclineReasonSpoolBudgetExhausted,
		largebody.DeclineReasonProofUncertain.String(),
		largebody.DeclineReasonAuthorityBlocker.String(),
		largebody.DeclineReasonLimitExceeded,
		largebody.DeclineReasonReadError,
	}

	for _, r := range reasons {
		if strings.ContainsAny(r, " \t\n\r/\\:;{}()[]") && r != "accounting/counting" {
			t.Fatalf("reason %q contains disallowed chars", r)
		}
		if len(r) > 40 {
			t.Fatalf("reason %q exceeds bounded length", r)
		}
	}

	stages := []largebody.PipelineStage{
		largebody.StageConsidered,
		largebody.StageStaticCanonical,
		largebody.StageCaptured,
		largebody.StageProfileProven,
		largebody.StageAssessmentEligible,
		largebody.StageWire,
		largebody.StageCanonical,
	}

	for _, s := range stages {
		if strings.ContainsAny(string(s), " \t\n\r/\\:;{}()[]") {
			t.Fatalf("stage %q contains disallowed chars", s)
		}
	}
}

func TestDiagnostics_SpoolLedgerObserver_ConcurrentOrdering(t *testing.T) {
	t.Parallel()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 * 1024,
		MaxInflightSpoolBytes: 100 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	obs := &testSpoolObserver{}
	ledger.SetObserver(obs)

	const numGoroutines = 40
	const iterations = 40
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := range numGoroutines {
		go func(id int) {
			defer wg.Done()
			for j := range iterations {
				bytes := int64((id + j + 1) * 1024)
				res, err := ledger.Reserve(bytes)
				if err != nil {
					continue
				}
				_ = res.ReserveMore(512)
				_ = res.ShrinkTo(bytes)
				res.Release()
			}
		}(i)
	}

	wg.Wait()

	if inflight := ledger.InflightBytes(); inflight != 0 {
		t.Fatalf("expected ledger inflight 0, got %d", inflight)
	}
	if got := obs.get(); got != 0 {
		t.Fatalf("expected observer bytes 0 matching ledger inflight, got %d", got)
	}
}

type testSpoolObserver struct {
	mu      sync.Mutex
	lastSeq uint64
	hasSeq  bool
	bytes   int64
}

func (o *testSpoolObserver) OnActiveSpoolBytes(seq uint64, b int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.hasSeq && seq < o.lastSeq {
		return
	}
	o.hasSeq = true
	o.lastSeq = seq
	o.bytes = b
}

func (o *testSpoolObserver) get() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.bytes
}

func compileSummaryWithPlane(t *testing.T, planeID string) largebody.WireEligibilitySummary {
	t.Helper()
	planes := make([]largebody.PlaneEligibilityInput, largebody.WireEligibilityPlaneCount)
	for i := range largebody.WireEligibilityPlaneCount {
		id, ok := largebody.WireEligibilityPlaneID(i)
		if !ok {
			t.Fatalf("unknown plane index %d", i)
		}
		acc := largebody.PlaneAccessMetadataOnly
		switch id {
		case "response_part_hooks", "completion_gates", "stream_observer_factories", "usage_observers":
			acc = largebody.PlaneAccessResponseOnly
		case planeID:
			acc = largebody.PlaneAccessCanonicalRequired
		}
		planes[i] = largebody.PlaneEligibilityInput{
			ID:       id,
			Access:   acc,
			Occupied: id == planeID,
		}
	}
	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID: "gen-test-1",
		Planes:       planes,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary failed: %v", err)
	}
	return summary
}

func compileSummaryWithPort(t *testing.T, ports largebody.NarrowPortEligibilityInput) largebody.WireEligibilitySummary {
	t.Helper()
	planes := make([]largebody.PlaneEligibilityInput, largebody.WireEligibilityPlaneCount)
	for i := range largebody.WireEligibilityPlaneCount {
		id, ok := largebody.WireEligibilityPlaneID(i)
		if !ok {
			t.Fatalf("unknown plane index %d", i)
		}
		acc := largebody.PlaneAccessMetadataOnly
		if id == "response_part_hooks" || id == "completion_gates" || id == "stream_observer_factories" || id == "usage_observers" {
			acc = largebody.PlaneAccessResponseOnly
		}
		planes[i] = largebody.PlaneEligibilityInput{
			ID:       id,
			Access:   acc,
			Occupied: false,
		}
	}
	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID: "gen-test-1",
		Planes:       planes,
		Ports:        ports,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary failed: %v", err)
	}
	return summary
}
