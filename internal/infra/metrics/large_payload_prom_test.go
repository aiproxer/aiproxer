package metrics

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestLargePayloadProm_RegistrationAndMetrics(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	prom := RegisterLargePayloadProm(reg)
	if prom == nil {
		t.Fatal("RegisterLargePayloadProm returned nil")
	}

	sink := NewLargePayloadPromSink(prom)
	if sink == nil {
		t.Fatal("NewLargePayloadPromSink returned nil")
	}

	// 1. Observe pipeline stages
	sink.OnPipelineStage(largebody.StageConsidered)
	sink.OnPipelineStage(largebody.StageStaticCanonical)
	sink.OnPipelineStage(largebody.StageCaptured)
	sink.OnPipelineStage(largebody.StageProfileProven)
	sink.OnPipelineStage(largebody.StageAssessmentEligible)
	sink.OnPipelineStage(largebody.StageWire)
	sink.OnPipelineStage(largebody.StageCanonical)

	// 2. Observe declines
	sink.OnDecline(largebody.DeclineReasonLocalTurn)
	sink.OnDecline(largebody.DeclineReasonSecretGuard)
	sink.OnDecline(largebody.DeclineReasonTerminalDecision)
	sink.OnDecline(largebody.DeclineReasonFrontendRouteResolver)
	sink.OnDecline(largebody.DeclineReasonTraffic)
	sink.OnDecline(largebody.DeclineReasonAccountingCounting)
	sink.OnDecline(largebody.DeclineReasonCustomCallCallback)
	sink.OnDecline(largebody.DeclineReasonBackendDomain)
	sink.OnDecline(largebody.DeclineReasonFeatureDisabled)
	sink.OnDecline(largebody.DeclineReasonBelowThreshold)
	sink.OnDecline(largebody.DeclineReasonGzipCompressed)
	sink.OnDecline(largebody.DeclineReasonStaticBlocker)

	// 3. Observe capture size bucket and storage
	sink.OnCapture(largebody.SizeBucket(1024), largebody.StorageMemory)
	sink.OnCapture(largebody.SizeBucket(2*1024*1024), largebody.StorageFile)

	// 4. Observe replays and rewrites
	sink.OnReplay()
	sink.OnReplay()
	sink.OnRewrite()

	// 5. Observe stage duration
	sink.OnStageDuration("capture", 15*time.Millisecond)
	sink.OnStageDuration("proof", 5*time.Millisecond)
	sink.OnStageDuration("assessment", 2*time.Millisecond)
	sink.OnStageDuration("execution", 50*time.Millisecond)

	// 6. Observe active spool bytes
	sink.OnActiveSpoolBytes(1, 123456)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("reg.Gather failed: %v", err)
	}

	names := make(map[string]bool)
	for _, f := range families {
		names[f.GetName()] = true
	}

	expected := []string{
		"aip_large_payload_pipeline_stages_total",
		"aip_large_payload_declines_total",
		"aip_large_payload_captured_total",
		"aip_large_payload_replays_total",
		"aip_large_payload_rewrites_total",
		"aip_large_payload_stage_duration_seconds",
		"aip_large_payload_active_spool_bytes",
	}

	for _, exp := range expected {
		if !names[exp] {
			t.Errorf("missing metric family: %s", exp)
		}
	}
}

func TestLargePayloadProm_CardinalityAndRedaction(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	prom := RegisterLargePayloadProm(reg)
	sink := NewLargePayloadPromSink(prom)

	secretPrompt := "SUPER_SECRET_PROMPT_PAYLOAD"
	secretToken := "resume_token_secret_12345"
	userSessionID := "user_session_uuid_9999"

	for i := range 100 {
		hostileID := fmt.Sprintf("%s_%s_%d", secretToken, userSessionID, i)
		// Hostile decline reasons should be sanitized or mapped so arbitrary dynamic strings don't explode label cardinality
		sink.OnDecline(hostileID)
		sink.OnStageDuration(fmt.Sprintf("stage_%d_%s", i, secretPrompt), time.Duration(i)*time.Millisecond)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("reg.Gather failed: %v", err)
	}

	for _, f := range families {
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				v := lp.GetValue()
				if strings.Contains(v, secretPrompt) {
					t.Fatalf("secretPrompt leaked into label %s=%q", lp.GetName(), v)
				}
				if strings.Contains(v, secretToken) {
					t.Fatalf("secretToken leaked into label %s=%q", lp.GetName(), v)
				}
				if strings.Contains(v, userSessionID) {
					t.Fatalf("userSessionID leaked into label %s=%q", lp.GetName(), v)
				}
			}
		}
	}
}

func TestLargePayloadProm_BundleIntegration(t *testing.T) {
	t.Parallel()

	b := NewBundle(nil, nil, nil)
	if b == nil {
		t.Fatal("NewBundle returned nil")
	}

	diag := b.LargePayloadDiagnostics()
	if diag == nil {
		t.Fatal("b.LargePayloadDiagnostics() returned nil")
	}

	diag.OnPipelineStage(largebody.StageConsidered)
	diag.OnActiveSpoolBytes(1, 42)

	families, err := b.Registry.Gather()
	if err != nil {
		t.Fatalf("b.Registry.Gather failed: %v", err)
	}

	var foundStage, foundSpool bool
	for _, f := range families {
		if f.GetName() == "aip_large_payload_pipeline_stages_total" {
			foundStage = true
		}
		if f.GetName() == "aip_large_payload_active_spool_bytes" {
			foundSpool = true
		}
	}
	if !foundStage {
		t.Error("expected aip_large_payload_pipeline_stages_total in bundle registry")
	}
	if !foundSpool {
		t.Error("expected aip_large_payload_active_spool_bytes in bundle registry")
	}
}

func TestLargePayloadProm_ActiveSpoolSequenceOrdering(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	prom := RegisterLargePayloadProm(reg)
	sink := NewLargePayloadPromSink(prom)

	// Step 1: Initial notification seq=2, bytes=500
	sink.OnActiveSpoolBytes(2, 500)
	if got := testutil.ToFloat64(prom.activeSpool); got != 500 {
		t.Fatalf("expected 500, got %v", got)
	}

	// Step 2: Stale snapshot seq=1, bytes=1000 arrives late (stale snapshot); must be dropped
	sink.OnActiveSpoolBytes(1, 1000)
	if got := testutil.ToFloat64(prom.activeSpool); got != 500 {
		t.Fatalf("expected 500 after stale update, got %v", got)
	}

	// Step 3: Newer notification seq=3, bytes=0 arrives; must be applied
	sink.OnActiveSpoolBytes(3, 0)
	if got := testutil.ToFloat64(prom.activeSpool); got != 0 {
		t.Fatalf("expected 0, got %v", got)
	}
}

func TestLargePayloadProm_ConcurrentSpoolLedgerOrdering(t *testing.T) {
	t.Parallel()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 * 1024,
		MaxInflightSpoolBytes: 100 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	reg := prometheus.NewRegistry()
	prom := RegisterLargePayloadProm(reg)
	sink := NewLargePayloadPromSink(prom)
	ledger.SetObserver(sink)

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
	if gauge := testutil.ToFloat64(prom.activeSpool); gauge != 0 {
		t.Fatalf("expected gauge 0 matching ledger inflight, got %v", gauge)
	}
}
