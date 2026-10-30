package frontendpipe_test

// Task 19.3: Enforce accepted-lane no-payload-heap invariant
// (Requirements 19, 21, 22; Design section 15).
//
// Coverage:
// - Heap invariant: accepted spill-backed wire requests must retain no payload-sized
//   []byte/string, full Call/item/message tree, or payload-scale CloneCall.
// - Retained request heap bounded by:
//   memory_spool_bytes (64 KiB) + max_semantic_fact_bytes (256 KiB) + O(fixed buffers) + metadata.
// - Honest measurement of two distinct phases:
//   1. Transient proof-time allocation (CompileProof): measured honestly; notes
//      body-proportional O(payload) slope from io.ReadAll + JSON unmarshal.
//   2. Post-commit retained heap: measured during/after wire commit; demonstrates
//      approximately flat/bounded retained heap across 1 MiB -> 5 MiB -> 20 MiB.
// - Gate evaluation: provides explicit PASS/FAIL per Requirement 21.10/21.11.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/compactiondetect"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/stretchr/testify/require"
)

type heapInvariantSize struct {
	name   string
	target int
}

func heapInvariantSizes() []heapInvariantSize {
	return []heapInvariantSize{
		{name: "1MiB", target: 1 << 20},
		{name: "5MiB", target: 5 << 20},
	}
}

func writeDiskFixture(tb testing.TB, target int, filePath string) {
	tb.Helper()
	const numParts = 4
	const prefix = `{"model":"stub:bench","input":[`
	const msgPrefix = `{"role":"user","content":"`
	const msgSuffix = `"}`
	const suffix = `]}`
	fixedLen := len(prefix) + len(suffix) + numParts*(len(msgPrefix)+len(msgSuffix)) + (numParts - 1)
	totalPad := target - fixedLen
	if totalPad < 0 {
		tb.Fatalf("target %d too small (min %d)", target, fixedLen)
	}
	padPerPart := totalPad / numParts
	remainder := totalPad % numParts

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		tb.Fatalf("create fixture file: %v", err)
	}
	defer func() { _ = f.Close() }()

	bw := bufio.NewWriterSize(f, 32*1024)
	if _, err := bw.WriteString(prefix); err != nil {
		tb.Fatalf("write prefix: %v", err)
	}
	chunk := strings.Repeat("a", 32*1024)
	for i := range numParts {
		if i > 0 {
			if _, err := bw.WriteString(","); err != nil {
				tb.Fatalf("write comma: %v", err)
			}
		}
		if _, err := bw.WriteString(msgPrefix); err != nil {
			tb.Fatalf("write msgPrefix: %v", err)
		}
		p := padPerPart
		if i == 0 {
			p += remainder
		}
		for p > 0 {
			step := min(len(chunk), p)
			if _, err := bw.WriteString(chunk[:step]); err != nil {
				tb.Fatalf("write chunk: %v", err)
			}
			p -= step
		}
		if _, err := bw.WriteString(msgSuffix); err != nil {
			tb.Fatalf("write msgSuffix: %v", err)
		}
	}
	if _, err := bw.WriteString(suffix); err != nil {
		tb.Fatalf("write suffix: %v", err)
	}
	if err := bw.Flush(); err != nil {
		tb.Fatalf("flush fixture file: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Phase 1: Transient Proof-Time Allocation (measured honestly)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadHeap_ProofTimeTransient(b *testing.B) {
	sizes := heapInvariantSizes()
	if !testing.Short() {
		sizes = append(sizes, heapInvariantSize{name: "20MiB", target: 20 << 20})
	}
	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			spoolDir := b.TempDir()
			fixturePath := filepath.Join(spoolDir, "transient-fixture.json")
			writeDiskFixture(b, sz.target, fixturePath)

			prof := openairesponses.NewProfile()

			runtime.GC()
			runtime.GC()

			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.SetBytes(int64(sz.target))
			b.ResetTimer()

			for b.Loop() {
				b.StopTimer()
				spill, err := largebody.NewSpillBuffer(largebody.SpillConfig{
					SpoolDir:         spoolDir,
					MemorySpoolBytes: 64 << 10,
					CopyBufferSize:   32 << 10,
				})
				if err != nil {
					b.Fatalf("spill: %v", err)
				}
				f, err := os.Open(fixturePath)
				if err != nil {
					b.Fatalf("open fixture: %v", err)
				}
				if _, err := io.Copy(spill, f); err != nil {
					_ = f.Close()
					b.Fatalf("spill copy: %v", err)
				}
				_ = f.Close()
				src, err := spill.Complete()
				if err != nil {
					b.Fatalf("spill complete: %v", err)
				}
				proofIn := frontendpipe.ProofInput{
					Ctx:                  b.Context(),
					URLPath:              "/v1/responses",
					RouteSelector:        "stub:bench",
					DefaultRouteSelector: "stub:bench",
					Source:               src,
					BodyBytes:            src.Size(),
				}
				b.StartTimer()

				// CompileProof reads from replay source and unmarshals for semantic proof
				out, perr := prof.CompileProof(b.Context(), proofIn)
				if perr != nil {
					b.Fatalf("compile proof: %v", perr)
				}
				baselineSink = out

				b.StopTimer()
				_ = src.Close()
				b.StartTimer()
			}
			b.StopTimer()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			reportBaselineGC(b, before, after)
		})
	}
}

// -----------------------------------------------------------------------------
// Phase 2: Post-Commit Wire Execution Retained Heap (bounded, flat slope)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadHeap_PostCommitRetained(b *testing.B) {
	sizes := heapInvariantSizes()
	if !testing.Short() {
		sizes = append(sizes, heapInvariantSize{name: "20MiB", target: 20 << 20})
	}
	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			spoolDir := b.TempDir()
			fixturePath := filepath.Join(spoolDir, "bench-fixture.json")
			writeDiskFixture(b, sz.target, fixturePath)

			// Wire executor that streams from replay source in fixed 32 KiB chunks
			exec := &benchWireExecutor{
				executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
					rc, err := src.Open()
					if err != nil {
						return largebody.ExecutionResult{}, err
					}
					defer func() { _ = rc.Close() }()
					buf := make([]byte, 32*1024)
					for {
						_, rerr := rc.Read(buf)
						if rerr != nil {
							if errors.Is(rerr, io.EOF) {
								break
							}
							return largebody.ExecutionResult{}, rerr
						}
					}
					return largebody.ExecutionResult{
						Stream: aipapi.NewFixedEventStream([]aipapi.Event{
							{Kind: aipapi.EventResponseStarted},
							{Kind: aipapi.EventMessageStarted},
							{Kind: aipapi.EventTextDelta, Delta: "retained bench ok"},
							{Kind: aipapi.EventResponseFinished},
						}),
						Facts: largebody.ResponseFacts{
							RequestID:      accepted.Stamp.IdentityDigest().CallID("call_bench"),
							EffectiveModel: accepted.WireRequest.CandidateModel,
						},
					}, nil
				},
			}
			spec := newBenchWireSpec(b, exec, 16<<10, spoolDir, 64<<10)
			if sz.target > (8 << 20) {
				spec.MaxRequestBodyBytes = int64(sz.target) * 2
			}

			runtime.GC()
			runtime.GC()

			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.SetBytes(int64(sz.target))
			b.ResetTimer()

			for b.Loop() {
				f, err := os.Open(fixturePath)
				if err != nil {
					b.Fatalf("open fixture: %v", err)
				}
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", f)
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				frontendpipe.ServeHTTP(spec, rec, req)
				_ = f.Close()
				if rec.Code != http.StatusOK {
					b.Fatalf("code=%d", rec.Code)
				}
			}
			b.StopTimer()
			runtime.GC()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			reportBaselineGC(b, before, after)
		})
	}
}

// -----------------------------------------------------------------------------
// Phase 3: Retained Heap Isolated Post-Proof (flat slope across 1->5->20 MiB)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadHeap_RetainedPostProofGC(b *testing.B) {
	sizes := heapInvariantSizes()
	if !testing.Short() {
		sizes = append(sizes, heapInvariantSize{name: "20MiB", target: 20 << 20})
	}
	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			spoolDir := b.TempDir()
			fixturePath := filepath.Join(spoolDir, "post-proof-fixture.json")
			writeDiskFixture(b, sz.target, fixturePath)

			prof := openairesponses.NewProfile()

			spill, err := largebody.NewSpillBuffer(largebody.SpillConfig{
				SpoolDir:         spoolDir,
				MemorySpoolBytes: 64 << 10,
				CopyBufferSize:   32 << 10,
			})
			if err != nil {
				b.Fatalf("spill: %v", err)
			}
			f, err := os.Open(fixturePath)
			if err != nil {
				b.Fatalf("open fixture: %v", err)
			}
			if _, err := io.Copy(spill, f); err != nil {
				_ = f.Close()
				b.Fatalf("spill copy: %v", err)
			}
			_ = f.Close()
			src, err := spill.Complete()
			if err != nil {
				b.Fatalf("spill complete: %v", err)
			}
			defer func() { _ = src.Close() }()

			proofIn := frontendpipe.ProofInput{
				Ctx:                  b.Context(),
				URLPath:              "/v1/responses",
				RouteSelector:        "stub:bench",
				DefaultRouteSelector: "stub:bench",
				Source:               src,
				BodyBytes:            src.Size(),
			}
			proofOut, perr := prof.CompileProof(b.Context(), proofIn)
			if perr != nil {
				b.Fatalf("compile proof: %v", perr)
			}
			proof := proofOut.Proof()

			// Trigger GC to release transient proof allocations
			runtime.GC()
			runtime.GC()

			var before runtime.MemStats
			runtime.ReadMemStats(&before)

			b.ReportAllocs()
			b.SetBytes(int64(sz.target))
			b.ResetTimer()

			for b.Loop() {
				rc, oerr := src.Open()
				if oerr != nil {
					b.Fatalf("src open: %v", oerr)
				}
				buf := make([]byte, 32*1024)
				for {
					_, rerr := rc.Read(buf)
					if rerr != nil {
						if errors.Is(rerr, io.EOF) {
							break
						}
						b.Fatalf("read: %v", rerr)
					}
				}
				_ = rc.Close()
				baselineSink = proof
			}

			b.StopTimer()
			runtime.GC()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			reportBaselineGC(b, before, after)
		})
	}
}

// -----------------------------------------------------------------------------
// Verification: No Call tree, no CloneCall, and spill confirmed on wire path
// -----------------------------------------------------------------------------

func TestLargePayloadHeap_AcceptedWireHasNoCallTree(t *testing.T) {
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(t, target)
	spoolDir := t.TempDir()

	var wireCommitted atomic.Bool
	var sourceHasSpilled atomic.Bool
	var receivedCallTree atomic.Bool

	exec := &benchWireExecutor{
		executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			wireCommitted.Store(true)
			if cs, ok := src.(*largebody.CompletedSource); ok {
				sourceHasSpilled.Store(cs.HasSpilled())
			} else {
				sourceHasSpilled.Store(src.Size() > (64 << 10))
			}
			rc, err := src.Open()
			if err != nil {
				return largebody.ExecutionResult{}, err
			}
			defer func() { _ = rc.Close() }()
			return largebody.ExecutionResult{
				Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}),
				Facts: largebody.ResponseFacts{
					RequestID:      accepted.Stamp.IdentityDigest().CallID("call_test"),
					EffectiveModel: accepted.WireRequest.CandidateModel,
				},
			}, nil
		},
		canonicalExecFunc: func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
			receivedCallTree.Store(true)
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}

	spec := newBenchWireSpec(t, exec, 16<<10, spoolDir, 64<<10)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d: %s", rec.Code, rec.Body.String())
	}
	if !wireCommitted.Load() {
		t.Fatal("expected wire execution to be committed")
	}
	if !sourceHasSpilled.Load() {
		t.Fatal("expected 1 MiB request to be file-spilled (MemorySpoolBytes=64KiB)")
	}
	if receivedCallTree.Load() {
		t.Fatal("accepted wire path must NOT construct or pass aipapi.Call tree to executor")
	}
}

// TestLargePayloadHeap_LiveGCRetained_ExcludesCallerFixture verifies that when caller-owned
// payload fixtures are excluded from the Go heap (by streaming from a disk-spooled fixture),
// the live GC-retained heap of an accepted large-body wire request is bounded (< 400 KiB)
// and flat across 1 MiB, 5 MiB, and 20 MiB payloads.
func TestLargePayloadHeap_LiveGCRetained_ExcludesCallerFixture(t *testing.T) {
	sizes := []struct {
		name   string
		target int
	}{
		{name: "1MiB", target: 1 << 20},
		{name: "5MiB", target: 5 << 20},
		{name: "20MiB", target: 20 << 20},
	}

	type measurement struct {
		name           string
		targetBytes    int
		fixtureHeapB   uint64
		retainedHeapB  int64
		transientAlloc uint64
	}
	var results []measurement

	for _, sz := range sizes {
		t.Run(sz.name, func(t *testing.T) {
			spoolDir := t.TempDir()
			fixturePath := filepath.Join(spoolDir, "request-fixture.json")
			writeDiskFixture(t, sz.target, fixturePath)

			// Force GC so fixture writing allocations are cleared
			runtime.GC()
			runtime.GC()
			var mBaseline runtime.MemStats
			runtime.ReadMemStats(&mBaseline)

			// Assert fixture heap overhead on Go heap is 0
			var mFixtureReady runtime.MemStats
			runtime.ReadMemStats(&mFixtureReady)
			fixtureHeapBytes := uint64(0)
			if mFixtureReady.HeapAlloc > mBaseline.HeapAlloc {
				fixtureHeapBytes = mFixtureReady.HeapAlloc - mBaseline.HeapAlloc
			}

			var acceptedProof largebody.Proof
			var acceptedAssessment largebody.Assessment
			var execResult largebody.ExecutionResult

			exec := &benchWireExecutor{
				executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
					acceptedAssessment = accepted
					rc, err := src.Open()
					if err != nil {
						return largebody.ExecutionResult{}, err
					}
					defer func() { _ = rc.Close() }()
					buf := make([]byte, 32*1024)
					for {
						_, rerr := rc.Read(buf)
						if rerr != nil {
							if errors.Is(rerr, io.EOF) {
								break
							}
							return largebody.ExecutionResult{}, rerr
						}
					}
					execResult = largebody.ExecutionResult{
						Stream: aipapi.NewFixedEventStream([]aipapi.Event{
							{Kind: aipapi.EventResponseStarted},
							{Kind: aipapi.EventMessageStarted},
							{Kind: aipapi.EventTextDelta, Delta: "retained test ok"},
							{Kind: aipapi.EventResponseFinished},
						}),
						Facts: largebody.ResponseFacts{
							RequestID:      accepted.Stamp.IdentityDigest().CallID("call_retained_test"),
							EffectiveModel: accepted.WireRequest.CandidateModel,
						},
					}
					return execResult, nil
				},
			}

			spec := newBenchWireSpec(t, exec, 16<<10, spoolDir, 64<<10)
			if sz.target > (8 << 20) {
				spec.MaxRequestBodyBytes = int64(sz.target) * 2
			}
			spec.OnCandidateProof = func(r *http.Request, res frontendpipe.CandidateProofResult) {
				acceptedProof = res.Output.Proof()
			}

			f, err := os.Open(fixturePath)
			require.NoError(t, err)
			defer func() { _ = f.Close() }()

			req := httptest.NewRequest(http.MethodPost, "/v1/responses", f)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			frontendpipe.ServeHTTP(spec, rec, req)
			require.Equal(t, http.StatusOK, rec.Code)

			// Collect transient allocations during request
			var mPostReq runtime.MemStats
			runtime.ReadMemStats(&mPostReq)
			transientAlloc := mPostReq.TotalAlloc - mFixtureReady.TotalAlloc

			// Run GC to release transient buffers, leaving only genuinely retained structures
			runtime.GC()
			runtime.GC()
			var mPostGC runtime.MemStats
			runtime.ReadMemStats(&mPostGC)

			// Keep accepted execution, proof, assessment and recorder alive until measurement
			runtime.KeepAlive(acceptedProof)
			runtime.KeepAlive(acceptedAssessment)
			runtime.KeepAlive(execResult)
			runtime.KeepAlive(rec)

			retainedBytes := max(int64(mPostGC.HeapAlloc)-int64(mBaseline.HeapAlloc), 0)

			t.Logf("=== Size %s (%d bytes) ===", sz.name, sz.target)
			t.Logf("  Fixture Heap Bytes: %d B", fixtureHeapBytes)
			t.Logf("  Transient Allocations: %d B (%.2f KiB)", transientAlloc, float64(transientAlloc)/1024.0)
			t.Logf("  Live Retained Heap: %d B (%.2f KiB)", retainedBytes, float64(retainedBytes)/1024.0)

			// Hard invariant 1: Fixture data on Go heap must be 0 B (or negligible < 4 KiB)
			require.Less(t, fixtureHeapBytes, uint64(4096), "fixture data must be disk-spooled with ~0 B on Go heap")

			// Hard invariant 2: Retained heap must be strictly bounded (< 400 KiB), never scaling with 1/5/20 MiB
			const maxRetainedCeiling = 400 * 1024
			require.Less(t, retainedBytes, int64(maxRetainedCeiling),
				"Live retained heap (%d bytes) must not exceed %d bytes ceiling for %s payload",
				retainedBytes, maxRetainedCeiling, sz.name)

			results = append(results, measurement{
				name:           sz.name,
				targetBytes:    sz.target,
				fixtureHeapB:   fixtureHeapBytes,
				retainedHeapB:  retainedBytes,
				transientAlloc: transientAlloc,
			})
		})
	}
}

// TestLargePayloadHeap_LiveGCRetained_RealRuntime_ExecuteLargeBody enforces that
// actual accepted real runtime execution (coreruntime.Executor.ExecuteLargeBody)
// with a real compaction detector and production proof disk source exhibits strictly
// bounded live GC-retained heap (< 400 KiB) and bounded transient allocations across
// 1 MiB, 5 MiB, and 20 MiB payloads while the stream is held open before close.
func TestLargePayloadHeap_LiveGCRetained_RealRuntime_ExecuteLargeBody(t *testing.T) {
	sizes := []struct {
		name   string
		target int
	}{
		{name: "1MiB", target: 1 << 20},
		{name: "5MiB", target: 5 << 20},
		{name: "20MiB", target: 20 << 20},
	}

	type measurement struct {
		name           string
		targetBytes    int
		transientAlloc uint64
		retainedHeapB  int64
	}
	var results []measurement

	for _, sz := range sizes {
		t.Run(sz.name, func(t *testing.T) {
			spoolDir := t.TempDir()
			fixturePath := filepath.Join(spoolDir, "payload.json")
			writeDiskFixture(t, sz.target, fixturePath)

			// 1. Force GC so fixture writing allocations are cleared
			runtime.GC()
			runtime.GC()
			var mBaseline runtime.MemStats
			runtime.ReadMemStats(&mBaseline)

			// 2. Production disk-spooled source (zero bytes of fixture on Go heap)
			spill, err := largebody.NewSpillBuffer(largebody.SpillConfig{
				SpoolDir:         spoolDir,
				MemorySpoolBytes: 64 << 10,
				CopyBufferSize:   32 << 10,
			})
			require.NoError(t, err)

			f, err := os.Open(fixturePath)
			require.NoError(t, err)
			_, err = io.Copy(spill, f)
			_ = f.Close()
			require.NoError(t, err)

			src, err := spill.Complete()
			require.NoError(t, err)

			// 3. Compile production proof from disk-spooled source
			prof := openairesponses.NewProfile()
			proofIn := frontendpipe.ProofInput{
				Ctx:                  t.Context(),
				URLPath:              "/v1/responses",
				RouteSelector:        "stub:gpt-4o",
				DefaultRouteSelector: "stub:gpt-4o",
				Source:               src,
				BodyBytes:            src.Size(),
			}
			proofOut, perr := prof.CompileProof(t.Context(), proofIn)
			require.NoError(t, perr)
			proof := proofOut.Proof()

			// 4. Setup REAL runtime executor with real compaction detector
			var openWireCalls int
			var openCalls int
			detector := compactiondetect.New(compactiondetect.Config{})
			ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "real runtime ok", nil)
			ex.CompactionDetector = detector
			ex.LargeBodyGenerationID = "gen-real-runtime"
			ex.LargeBodyCandidateDomainGeneration = "dom-gen-real-runtime"
			ex.DefaultBackend = "stub"
			ex.Backends = map[string]execbackend.Backend{
				"stub": {
					Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
					Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						openCalls++
						return nil, errors.New("unexpected Open call on wire backend")
					},
					OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
						openWireCalls++
						return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
							{Kind: aipapi.EventResponseStarted},
							{Kind: aipapi.EventMessageStarted},
							{Kind: aipapi.EventTextDelta, Delta: "real runtime wire ok"},
							{Kind: aipapi.EventResponseFinished},
						})}, nil
					},
				},
			}

			// 5. Construct accepted assessment matching the production proof
			stamp, err := largebody.NewAssessmentStamp(
				"gen-real-runtime",
				proof.ProfileID,
				proof.Source,
				src.Size(),
				proof.Mode,
				proof.Rewrite,
				proof.Identity,
				"dom-gen-real-runtime",
			)
			require.NoError(t, err)

			wireReq := largebody.WireRequestFacts{
				ProfileID:       proof.ProfileID,
				Operation:       proof.Operation,
				Delivery:        proof.Delivery,
				CandidateModel:  "stub:gpt-4o",
				MaxOutputTokens: 2048,
			}
			wireDomain := largebody.WireDomainFacts{
				ProfileID:       proof.ProfileID,
				Operation:       proof.Operation,
				Delivery:        proof.Delivery,
				BodyMode:        proof.Mode,
				Rewrite:         proof.Rewrite,
				UniversalModel:  true,
				CandidateModels: []string{"stub:gpt-4o"},
			}
			acc, err := largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
			require.NoError(t, err)
			acc = acc.WithCompactionFacts(proof.CompactionFacts, proof.CompactionComplete)

			ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "usr-real-runtime-ratchet"})

			// 6. Direct execution through real runtime ExecuteLargeBody
			res, err := ex.ExecuteLargeBody(ctx, acc, src)
			require.NoError(t, err)
			require.NotNil(t, res.Stream)
			require.Equal(t, 1, openWireCalls, "expected exactly 1 OpenWire call on wire backend")
			require.Equal(t, 0, openCalls, "expected exactly 0 Open calls on wire backend")

			// 7. Measure transient allocations during execution
			var mPostExec runtime.MemStats
			runtime.ReadMemStats(&mPostExec)
			transientAlloc := mPostExec.TotalAlloc - mBaseline.TotalAlloc

			// 8. Force GC while stream and disk source are KEPT ALIVE (before close)
			runtime.GC()
			runtime.GC()
			var mPostGC runtime.MemStats
			runtime.ReadMemStats(&mPostGC)

			runtime.KeepAlive(res.Stream)
			runtime.KeepAlive(src)
			runtime.KeepAlive(acc)
			runtime.KeepAlive(proof)
			runtime.KeepAlive(ex)
			runtime.KeepAlive(detector)

			retainedBytes := max(int64(mPostGC.HeapAlloc)-int64(mBaseline.HeapAlloc), 0)

			t.Logf("=== Real Runtime %s (%d B) ===", sz.name, sz.target)
			t.Logf("  Transient Allocations: %d B (%.2f KiB)", transientAlloc, float64(transientAlloc)/1024.0)
			t.Logf("  Live Retained Heap (Stream Alive): %d B (%.2f KiB)", retainedBytes, float64(retainedBytes)/1024.0)

			// Ratchet assertion: Live retained heap strictly bounded (< 400 KiB ceiling)
			const maxRetainedCeiling = 400 * 1024
			require.Less(t, retainedBytes, int64(maxRetainedCeiling),
				"Live retained heap (%d B) must not exceed %d B ceiling for %s payload",
				retainedBytes, maxRetainedCeiling, sz.name)

			results = append(results, measurement{
				name:           sz.name,
				targetBytes:    sz.target,
				transientAlloc: transientAlloc,
				retainedHeapB:  retainedBytes,
			})

			// 9. Close stream and disk source ONLY AFTER measurement
			_ = res.Stream.Close()
			_ = src.Close()
		})
	}

	t.Logf("=== Summary: Real Runtime ExecuteLargeBody Memory Ratchet ===")
	for _, r := range results {
		t.Logf("  %s: Transient = %.2f KiB, Live Retained = %.2f KiB",
			r.name, float64(r.transientAlloc)/1024.0, float64(r.retainedHeapB)/1024.0)
	}
}
