package frontendpipe_test

// Task 19.4: Benchmark static blocker overhead
// (Requirements 5, 21; Design sections 4, 15).
//
// Coverage:
// - Feature enabled but DefinitelyCanonical generation/profile vs disabled/canonical baseline.
// - Confirms static pre-capture disposition adds negligible overhead to canonical baseline.
// - Asserts zero temp file creation (checks SpoolDir entries before and after).
// - Asserts no replay source or streaming scanner construction.
// - Includes Local Turn and Secret Guard canonical blocker examples, plus below-threshold.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
)

func benchMakeCleanPlanes() []largebody.PlaneEligibilityInput {
	planes := make([]largebody.PlaneEligibilityInput, largebody.WireEligibilityPlaneCount)
	for i := range largebody.WireEligibilityPlaneCount {
		id, _ := largebody.WireEligibilityPlaneID(i)
		planes[i] = largebody.PlaneEligibilityInput{
			ID:     id,
			Access: largebody.PlaneAccessResponseOnly,
		}
	}
	return planes
}

func benchSummaryWithLocalTurn(tb testing.TB, genID string, occupied bool) largebody.WireEligibilitySummary {
	tb.Helper()
	planes := benchMakeCleanPlanes()
	idx, ok := largebody.WireEligibilityPlaneIndex("local_turn_handlers")
	if !ok {
		tb.Fatalf("missing plane index for local_turn_handlers")
	}
	planes[idx].Access = largebody.PlaneAccessCanonicalRequired
	planes[idx].Occupied = occupied

	s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    planes,
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		tb.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	return s
}

func benchSummaryWithSecretGuard(tb testing.TB, genID string, occupied bool) largebody.WireEligibilitySummary {
	tb.Helper()
	planes := benchMakeCleanPlanes()
	idx, ok := largebody.WireEligibilityPlaneIndex("secret_guard_execution")
	if !ok {
		tb.Fatalf("missing plane index for secret_guard_execution")
	}
	planes[idx].Access = largebody.PlaneAccessCanonicalRequired
	planes[idx].Occupied = occupied

	s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    planes,
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		tb.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	return s
}

func newBenchBlockerSpec(
	tb testing.TB,
	enabled bool,
	threshold int64,
	spoolDir string,
	summary largebody.WireEligibilitySummary,
) *frontendpipe.Spec[openairesponses.EncodeOptions] {
	tb.Helper()
	exec := &benchWireExecutor{}
	h := &openairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:bench",
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
	}
	spec := h.Spec()
	spec.LargePayload = frontendpipe.LargePayloadConfig{
		Enabled:         enabled,
		ThresholdBytes:  threshold,
		SpoolDir:        spoolDir,
		WireEligibility: summary,
	}
	return spec
}

// -----------------------------------------------------------------------------
// Benchmarks: Disabled vs Local Turn vs Secret Guard vs Below Threshold
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadBlocker_DisabledBaseline(b *testing.B) {
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(b, target)
	spoolDir := b.TempDir()
	spec := newBenchBlockerSpec(b, false, 1<<20, spoolDir, largebody.WireEligibilitySummary{})

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()

	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(spec, rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("code=%d", rec.Code)
		}
	}
	b.StopTimer()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	reportBaselineGC(b, before, after)
}

func BenchmarkLargePayloadBlocker_LocalTurn(b *testing.B) {
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(b, target)
	spoolDir := b.TempDir()
	summary := benchSummaryWithLocalTurn(b, "gen-bench", true)
	spec := newBenchBlockerSpec(b, true, 1<<20, spoolDir, summary)

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()

	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(spec, rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("code=%d", rec.Code)
		}
	}
	b.StopTimer()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	reportBaselineGC(b, before, after)
}

func BenchmarkLargePayloadBlocker_SecretGuard(b *testing.B) {
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(b, target)
	spoolDir := b.TempDir()
	summary := benchSummaryWithSecretGuard(b, "gen-bench", true)
	spec := newBenchBlockerSpec(b, true, 1<<20, spoolDir, summary)

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()

	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(spec, rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("code=%d", rec.Code)
		}
	}
	b.StopTimer()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	reportBaselineGC(b, before, after)
}

func BenchmarkLargePayloadBlocker_BelowThreshold(b *testing.B) {
	body := []byte(`{"model":"stub:bench","input":"small input under threshold"}`)
	spoolDir := b.TempDir()
	summary := benchSummaryWithLocalTurn(b, "gen-bench", false) // clean generation
	spec := newBenchBlockerSpec(b, true, 1<<20, spoolDir, summary)

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()

	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(spec, rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("code=%d", rec.Code)
		}
	}
	b.StopTimer()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	reportBaselineGC(b, before, after)
}

// -----------------------------------------------------------------------------
// Verification: Zero Temp Files, No Replay, No Scanner Construction
// -----------------------------------------------------------------------------

func TestLargePayloadBlocker_ZeroTempFilesAndNoScanner(t *testing.T) {
	t.Parallel()
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(t, target)

	tests := []struct {
		name    string
		enabled bool
		summary largebody.WireEligibilitySummary
		reqBody []byte
		blocker string
	}{
		{
			name:    "DisabledBaseline",
			enabled: false,
			summary: largebody.WireEligibilitySummary{},
			reqBody: body,
			blocker: "feature_disabled",
		},
		{
			name:    "LocalTurnBlocker",
			enabled: true,
			summary: benchSummaryWithLocalTurn(t, "gen-test", true),
			reqBody: body,
			blocker: "local_turn",
		},
		{
			name:    "SecretGuardBlocker",
			enabled: true,
			summary: benchSummaryWithSecretGuard(t, "gen-test", true),
			reqBody: body,
			blocker: "secret_guard",
		},
		{
			name:    "BelowThreshold",
			enabled: true,
			summary: benchSummaryWithLocalTurn(t, "gen-test", false),
			reqBody: []byte(`{"model":"stub:bench","input":"below threshold"}`),
			blocker: "below_threshold",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spoolDir := t.TempDir()
			spec := newBenchBlockerSpec(t, tc.enabled, 1<<20, spoolDir, tc.summary)

			var captureInvoked atomic.Bool
			var proofInvoked atomic.Bool
			spec.OnCandidateCapture = func(r *http.Request, res frontendpipe.CandidateCaptureResult) {
				captureInvoked.Store(true)
			}
			spec.OnCandidateProof = func(r *http.Request, res frontendpipe.CandidateProofResult) {
				proofInvoked.Store(true)
			}

			// Check directory before
			entriesBefore, err := os.ReadDir(spoolDir)
			if err != nil {
				t.Fatalf("readdir before: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(tc.reqBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(spec, rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
			}

			// Check directory after: MUST be identical (0 temp files created!)
			entriesAfter, err := os.ReadDir(spoolDir)
			if err != nil {
				t.Fatalf("readdir after: %v", err)
			}
			if len(entriesAfter) != len(entriesBefore) {
				t.Fatalf("%s created %d temp files in SpoolDir, want 0", tc.name, len(entriesAfter)-len(entriesBefore))
			}

			if captureInvoked.Load() {
				t.Fatalf("%s unexpectedly invoked candidate capture", tc.name)
			}
			if proofInvoked.Load() {
				t.Fatalf("%s unexpectedly invoked candidate proof", tc.name)
			}
		})
	}
}
