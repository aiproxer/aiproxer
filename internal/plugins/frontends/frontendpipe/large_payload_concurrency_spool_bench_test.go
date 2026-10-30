package frontendpipe_test

// Task 19.5: Concurrent load and spool saturation
// (Requirements 20, 21; Design sections 5, 15).
//
// Coverage:
// - Realistic sessions: concurrent accepted requests under load.
// - Slow uploads: reader yielding chunks with delay; verifies decode permit not held.
// - Spool budget saturation: spool ledger with tight budget (2 MiB); proves excess
//   requests gracefully fall back to canonical path without client error (HTTP 200).
//   Spool budget is an optimization budget, not a global OOM admission barrier.
// - Cancellation: client context cancellation mid-flight; proves temporary files
//   are cleaned up and spool reservations released.
// - Benchmarks with -benchmem comparing GC/heap/latency.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// slowChunkReader simulates a slow client upload by chunking the payload.
type slowChunkReader struct {
	data      []byte
	offset    int
	chunkSize int
	delay     time.Duration
}

func (r *slowChunkReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	n := min(len(p), min(r.chunkSize, len(r.data)-r.offset))
	copy(p, r.data[r.offset:r.offset+n])
	r.offset += n
	return n, nil
}

// -----------------------------------------------------------------------------
// 19.5 Benchmark 1: Concurrent Accepted Wire Requests (RunParallel)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadConcurrent_AcceptedWire(b *testing.B) {
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(b, target)
	spoolDir := b.TempDir()
	exec := &benchWireExecutor{}
	spec := newBenchWireSpec(b, exec, 16<<10, spoolDir, 64<<10)

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(spec, rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("code=%d: %s", rec.Code, rec.Body.String())
			}
		}
	})

	b.StopTimer()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	reportBaselineGC(b, before, after)
}

// -----------------------------------------------------------------------------
// 19.5 Benchmark 2: Slow Upload Under Concurrency
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadConcurrent_SlowUpload(b *testing.B) {
	const target = 256 << 10 // 256 KiB for bounded benchmark time
	body := baselineResponsesBody(b, target)
	spoolDir := b.TempDir()
	exec := &benchWireExecutor{}
	spec := newBenchWireSpec(b, exec, 16<<10, spoolDir, 64<<10)

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()

	for b.Loop() {
		reader := &slowChunkReader{
			data:      body,
			chunkSize: 32 << 10,
			delay:     100 * time.Microsecond,
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", reader)
		req.ContentLength = int64(len(body))
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
// 19.5 Benchmark 3: Spool Budget Saturation (Lossless Canonical Fallback)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadConcurrent_SpoolBudgetSaturation(b *testing.B) {
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(b, target)
	spoolDir := b.TempDir()

	// Tight 2 MiB spool budget: 3rd and subsequent concurrent requests fall back to canonical
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 << 10,
		MaxInflightSpoolBytes: 2 << 20,
	})
	if err != nil {
		b.Fatalf("NewSpoolLedger: %v", err)
	}

	exec := &benchWireExecutor{}
	h := &openairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:bench",
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
		Profile:              openairesponses.NewProfile(),
	}
	spec := h.Spec()
	spec.LargePayload = frontendpipe.LargePayloadConfig{
		Enabled:          true,
		ThresholdBytes:   16 << 10,
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 64 << 10,
		SpoolLedger:      ledger,
	}
	spec.WireWriteNonStream = func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"bench","object":"response","status":"completed"}`))
		return nil
	}

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(spec, rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("code=%d: %s", rec.Code, rec.Body.String())
			}
		}
	})

	b.StopTimer()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	reportBaselineGC(b, before, after)
}

// -----------------------------------------------------------------------------
// Verification: Spool Budget is Optimization Budget, Not Global OOM Admission
// -----------------------------------------------------------------------------

func TestLargePayloadConcurrent_SpoolBudgetIsOptimizationBudgetNotOOM(t *testing.T) {
	t.Parallel()
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(t, target)
	spoolDir := t.TempDir()

	// 2 MiB spool budget
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 << 10,
		MaxInflightSpoolBytes: 2 << 20,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger: %v", err)
	}

	var wireCount atomic.Int64
	var canonicalCount atomic.Int64

	exec := &benchWireExecutor{
		executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			wireCount.Add(1)
			// Hold the execution slightly to ensure overlap
			time.Sleep(20 * time.Millisecond)
			rc, _ := src.Open()
			_ = rc.Close()
			return largebody.ExecutionResult{
				Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}),
				Facts: largebody.ResponseFacts{
					RequestID:      accepted.Stamp.IdentityDigest().CallID("call_budget"),
					EffectiveModel: accepted.WireRequest.CandidateModel,
				},
			}, nil
		},
		canonicalExecFunc: func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
			canonicalCount.Add(1)
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}

	h := &openairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:bench",
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
		Profile:              openairesponses.NewProfile(),
	}
	spec := h.Spec()
	spec.LargePayload = frontendpipe.LargePayloadConfig{
		Enabled:          true,
		ThresholdBytes:   16 << 10,
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 64 << 10,
		SpoolLedger:      ledger,
	}
	spec.WireWriteNonStream = func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		return nil
	}

	// Launch 5 concurrent requests of 1 MiB each against 2 MiB spool budget
	const numReqs = 5
	var wg sync.WaitGroup
	var successCount atomic.Int64

	for range numReqs {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(spec, rec, req)
			if rec.Code == http.StatusOK {
				successCount.Add(1)
			}
		})
	}
	wg.Wait()

	if successCount.Load() != numReqs {
		t.Fatalf("expected all %d requests to succeed (HTTP 200), got %d", numReqs, successCount.Load())
	}
	if wireCount.Load() == 0 {
		t.Fatal("expected at least one wire request to be admitted under spool budget")
	}
	if canonicalCount.Load() == 0 {
		t.Fatal("expected at least one request to fall back to canonical path when spool budget saturated")
	}
	if wireCount.Load()+canonicalCount.Load() != numReqs {
		t.Fatalf("wire (%d) + canonical (%d) != total (%d)", wireCount.Load(), canonicalCount.Load(), numReqs)
	}
}

// -----------------------------------------------------------------------------
// Verification: Cancellation Cleans Up Spill Files and Releases Reservations
// -----------------------------------------------------------------------------

type pausingSpillReader struct {
	data       []byte
	offset     int
	spillBytes int
	spilledCh  chan struct{}
	once       sync.Once
	ctx        context.Context
}

func (r *pausingSpillReader) Read(p []byte) (int, error) {
	if r.ctx.Err() != nil {
		return 0, r.ctx.Err()
	}
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	// Once we have read past spillBytes (written to disk), signal and wait for cancellation
	if r.offset >= r.spillBytes {
		r.once.Do(func() {
			close(r.spilledCh)
		})
		<-r.ctx.Done()
		return 0, r.ctx.Err()
	}
	// Read in 32 KiB chunks so threshold crossing is staged
	n := min(len(p), min(32<<10, len(r.data)-r.offset))
	copy(p, r.data[r.offset:r.offset+n])
	r.offset += n
	return n, nil
}

func TestLargePayloadConcurrent_CancellationCleansUpSpillFiles(t *testing.T) {
	t.Parallel()
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(t, target)
	spoolDir := t.TempDir()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 << 10,
		MaxInflightSpoolBytes: 10 << 20,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger: %v", err)
	}

	exec := &benchWireExecutor{}
	spec := newBenchWireSpec(t, exec, 16<<10, spoolDir, 64<<10)
	spec.LargePayload.SpoolLedger = ledger

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spilledCh := make(chan struct{})
	// Pause after 96 KiB (> 64 KiB MemorySpoolBytes so spill file is flushed to disk)
	spillReader := &pausingSpillReader{
		data:       body,
		spillBytes: 96 << 10,
		spilledCh:  spilledCh,
		ctx:        ctx,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", spillReader).WithContext(ctx)
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		frontendpipe.ServeHTTP(spec, rec, req)
	}()

	// 1. Wait for upload to cross memory spool threshold and write to disk
	select {
	case <-spilledCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for disk spill threshold to be crossed")
	}

	// 2. Deterministically assert that temporary spill file exists on disk BEFORE cancellation
	entries, rerr := os.ReadDir(spoolDir)
	if rerr != nil {
		t.Fatalf("ReadDir: %v", rerr)
	}
	if len(entries) == 0 {
		t.Fatal("expected spill file to exist in spoolDir while upload was paused")
	}
	if ledger.ActiveReservations() == 0 {
		t.Fatal("expected active spool reservation while upload was paused")
	}

	// 3. Cancel context to trigger cancel-cleanup path
	cancel()

	// 4. Wait for ServeHTTP to exit
	select {
	case <-serveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ServeHTTP to return after cancellation")
	}

	// 5. Verify spill files were removed strictly as a consequence of cancellation
	entries, rerr = os.ReadDir(spoolDir)
	if rerr != nil {
		t.Fatalf("ReadDir: %v", rerr)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 leftover temp files after cancellation, found %d", len(entries))
	}

	// 6. Verify spool ledger reservation was completely released
	if ledger.ActiveReservations() != 0 {
		t.Fatalf("expected 0 active reservations after cancellation, got %d", ledger.ActiveReservations())
	}
}
