package frontendpipe_test

// Task 19.2: Benchmark all required sizes and stages for large-payload streaming fast path
// (Requirements 6, 21; Design section 15).
//
// Coverage:
// - Sizes: 32 KiB, 256 KiB, 1 MiB, 5 MiB, and test-only 20 MiB (gated by -short).
// - Stages: Capture (upload to spill buffer), Proof (CompileProof), Assessment
//   (AssessLargeBody), Provider-Open (loopback replay POST), Wire End-to-End.
// - Shapes: Giant string, Late model, Tools, Malformed JSON, Canonical fallback,
//   Replay/failover.
// - Invariant check: Decode permit is NEVER held during upload/spill I/O.
//
// Benchmarks run with -benchmem and record allocs/op, B/op, ns/op, GC cycles/pause,
// and heap metrics via reportBaselineGC.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// benchWireExecutor implements ExecutorView, LargeBodyExecutor, LargeBodyWireExecutor,
// and LargeBodyAssessor for benchmarking wire fast-path execution.
type benchWireExecutor struct {
	assessFunc        func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error)
	executeLargeFunc  func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error)
	canonicalExecFunc func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error)
	failoverAttempts  atomic.Int64
}

func (e *benchWireExecutor) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	if e.assessFunc != nil {
		return e.assessFunc(ctx, proof)
	}
	stamp, err := largebody.NewAssessmentStamp(
		"gen_bench",
		proof.ProfileID,
		proof.Source,
		proof.BodyBytes,
		proof.Mode,
		proof.Rewrite,
		proof.Identity,
	)
	if err != nil {
		return largebody.Assessment{}, err
	}
	wireReq := largebody.WireRequestFacts{
		ProfileID:       proof.ProfileID,
		Operation:       proof.Operation,
		Delivery:        proof.Delivery,
		BodyMode:        proof.Mode,
		Rewrite:         proof.Rewrite,
		ClientModel:     proof.ClientModel,
		CandidateModel:  proof.ClientModel,
		MaxOutputTokens: proof.MaxOutputTokens,
	}
	wireDomain := largebody.WireDomainFacts{
		ProfileID: proof.ProfileID,
		Operation: proof.Operation,
		Delivery:  proof.Delivery,
	}
	return largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
}

func (e *benchWireExecutor) ExecuteLargeBody(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
	if e.executeLargeFunc != nil {
		return e.executeLargeFunc(ctx, accepted, src)
	}
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
			{Kind: aipapi.EventTextDelta, Delta: "bench ok"},
			{Kind: aipapi.EventResponseFinished},
		}),
		Facts: largebody.ResponseFacts{
			RequestID:      accepted.Stamp.IdentityDigest().CallID("call_bench"),
			EffectiveModel: accepted.WireRequest.CandidateModel,
		},
	}, nil
}

func (e *benchWireExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	if e.canonicalExecFunc != nil {
		return e.canonicalExecFunc(ctx, call)
	}
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "canonical bench ok"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (e *benchWireExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

func (e *benchWireExecutor) WallClock() func() time.Time { return nil }

var (
	_ aipsdk.ExecutorView         = (*benchWireExecutor)(nil)
	_ largebody.LargeBodyExecutor = (*benchWireExecutor)(nil)
)

func newBenchWireSpec(
	tb testing.TB,
	exec *benchWireExecutor,
	threshold int64,
	spoolDir string,
	memorySpoolBytes int64,
) *frontendpipe.Spec[openairesponses.EncodeOptions] {
	tb.Helper()
	h := &openairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:bench",
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
		Profile:              openairesponses.NewProfile(),
	}
	spec := h.Spec()
	spec.LargePayload = frontendpipe.LargePayloadConfig{
		Enabled:          true,
		ThresholdBytes:   threshold,
		SpoolDir:         spoolDir,
		MemorySpoolBytes: memorySpoolBytes,
	}
	spec.WireWriteStream = func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		for {
			_, err := es.Recv(ctx)
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return err
			}
		}
		return nil
	}
	spec.WireWriteNonStream = func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"bench","object":"response","status":"completed"}`))
		return nil
	}
	return spec
}

// benchLateModelBody puts "model" at the very end of the JSON object.
func benchLateModelBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const prefix = `{"input":"`
	const suffix = `","model":"stub:bench"}`
	pad := target - len(prefix) - len(suffix)
	if pad < 0 {
		tb.Fatalf("target %d too small for envelope %d", target, len(prefix)+len(suffix))
	}
	var b strings.Builder
	b.Grow(target)
	b.WriteString(prefix)
	b.WriteString(strings.Repeat("a", pad))
	b.WriteString(suffix)
	out := []byte(b.String())
	if len(out) != target {
		tb.Fatalf("late model body len=%d want %d", len(out), target)
	}
	return out
}

// benchToolsBody includes a tools array plus input string.
func benchToolsBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const prefix = `{"model":"stub:bench","tools":[{"type":"function","function":{"name":"search","description":"search query","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}}],"input":"`
	const suffix = `"}`
	pad := target - len(prefix) - len(suffix)
	if pad < 0 {
		tb.Fatalf("target %d too small for envelope %d", target, len(prefix)+len(suffix))
	}
	var b strings.Builder
	b.Grow(target)
	b.WriteString(prefix)
	b.WriteString(strings.Repeat("a", pad))
	b.WriteString(suffix)
	out := []byte(b.String())
	if len(out) != target {
		tb.Fatalf("tools body len=%d want %d", len(out), target)
	}
	return out
}

// benchMalformedJSONBody produces invalid JSON of exact target length (unclosed quote).
func benchMalformedJSONBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const prefix = `{"model":"stub:bench","input":"`
	pad := target - len(prefix)
	if pad < 0 {
		tb.Fatalf("target %d too small for envelope %d", target, len(prefix))
	}
	var b strings.Builder
	b.Grow(target)
	b.WriteString(prefix)
	b.WriteString(strings.Repeat("a", pad))
	out := []byte(b.String())
	if len(out) != target {
		tb.Fatalf("malformed body len=%d want %d", len(out), target)
	}
	return out
}

// benchCanonicalFallbackBody includes an unsupported control key ("store": true).
func benchCanonicalFallbackBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const prefix = `{"model":"stub:bench","store":true,"input":"`
	const suffix = `"}`
	pad := target - len(prefix) - len(suffix)
	if pad < 0 {
		tb.Fatalf("target %d too small for envelope %d", target, len(prefix)+len(suffix))
	}
	var b strings.Builder
	b.Grow(target)
	b.WriteString(prefix)
	b.WriteString(strings.Repeat("a", pad))
	b.WriteString(suffix)
	out := []byte(b.String())
	if len(out) != target {
		tb.Fatalf("canonical fallback body len=%d want %d", len(out), target)
	}
	return out
}

// -----------------------------------------------------------------------------
// 19.2 Stage 1: Capture (upload to spill buffer, bounded memory then file)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadStages_Capture(b *testing.B) {
	for _, sz := range baselineSizes() {
		b.Run(sz.name, func(b *testing.B) {
			body := baselineResponsesBody(b, sz.target)
			spoolDir := b.TempDir()
			exec := &benchWireExecutor{}
			spec := newBenchWireSpec(b, exec, 16<<10, spoolDir, 64<<10)

			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()

			for b.Loop() {
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				res, _, err := frontendpipe.CaptureCandidateBody(b.Context(), spec, rec, req, int64(len(body)))
				if err != nil {
					b.Fatalf("capture: %v", err)
				}
				if res.Outcome != largebody.CaptureOutcomeCompleted || res.Completed == nil {
					b.Fatalf("capture outcome=%v want Completed", res.Outcome)
				}
				_ = res.Completed.Close()
			}
			b.StopTimer()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			reportBaselineGC(b, before, after)
		})
	}
}

// -----------------------------------------------------------------------------
// 19.2 Stage 2: Proof (CompileProof on captured source)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadStages_Proof(b *testing.B) {
	for _, sz := range baselineSizes() {
		b.Run(sz.name, func(b *testing.B) {
			body := baselineResponsesBody(b, sz.target)
			prof := openairesponses.NewProfile()
			spoolDir := b.TempDir()

			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()

			for b.Loop() {
				b.StopTimer()
				spill, err := largebody.NewSpillBuffer(largebody.SpillConfig{
					SpoolDir:         spoolDir,
					MemorySpoolBytes: 64 << 10,
					CopyBufferSize:   32 << 10,
				})
				if err != nil {
					b.Fatalf("new spill: %v", err)
				}
				if _, err := spill.Write(body); err != nil {
					b.Fatalf("spill write: %v", err)
				}
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
// 19.2 Stage 3: Assessment (AssessLargeBody side-effect-free decision)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadStages_Assessment(b *testing.B) {
	for _, sz := range baselineSizes() {
		b.Run(sz.name, func(b *testing.B) {
			body := baselineResponsesBody(b, sz.target)
			prof := openairesponses.NewProfile()
			spoolDir := b.TempDir()

			spill, _ := largebody.NewSpillBuffer(largebody.SpillConfig{
				SpoolDir:         spoolDir,
				MemorySpoolBytes: 64 << 10,
				CopyBufferSize:   32 << 10,
			})
			_, _ = spill.Write(body)
			src, _ := spill.Complete()
			defer func() { _ = src.Close() }()

			proofIn := frontendpipe.ProofInput{
				Ctx:                  b.Context(),
				URLPath:              "/v1/responses",
				RouteSelector:        "stub:bench",
				DefaultRouteSelector: "stub:bench",
				Source:               src,
				BodyBytes:            src.Size(),
			}
			proofOut, err := prof.CompileProof(b.Context(), proofIn)
			if err != nil {
				b.Fatalf("compile proof: %v", err)
			}
			proof := proofOut.Proof()

			exec := &benchWireExecutor{}

			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.ResetTimer()

			for b.Loop() {
				assessment, aerr := exec.AssessLargeBody(b.Context(), proof)
				if aerr != nil {
					b.Fatalf("assess: %v", aerr)
				}
				baselineSink = assessment
			}
			b.StopTimer()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			reportBaselineGC(b, before, after)
		})
	}
}

// -----------------------------------------------------------------------------
// 19.2 Stage 4: Provider-Open (loopback POST streaming replay source)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadStages_ProviderOpen(b *testing.B) {
	for _, sz := range baselineSizes() {
		b.Run(sz.name, func(b *testing.B) {
			body := baselineResponsesBody(b, sz.target)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				_ = r.Body.Close()
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			client := srv.Client()
			spoolDir := b.TempDir()

			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()

			for b.Loop() {
				b.StopTimer()
				spill, _ := largebody.NewSpillBuffer(largebody.SpillConfig{
					SpoolDir:         spoolDir,
					MemorySpoolBytes: 64 << 10,
					CopyBufferSize:   32 << 10,
				})
				_, _ = spill.Write(body)
				src, _ := spill.Complete()
				b.StartTimer()

				rc, err := src.Open()
				if err != nil {
					b.Fatalf("src open: %v", err)
				}
				req, err := http.NewRequestWithContext(b.Context(), http.MethodPost, srv.URL, rc)
				if err != nil {
					_ = rc.Close()
					b.Fatalf("new request: %v", err)
				}
				req.ContentLength = src.Size()
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					_ = rc.Close()
					b.Fatalf("client do: %v", err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				_ = rc.Close()

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
// 19.2 Stage 5: Wire End-to-End (frontendpipe.ServeHTTP with accepted wire mode)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadStages_WireEndToEnd(b *testing.B) {
	for _, sz := range baselineSizes() {
		b.Run(sz.name, func(b *testing.B) {
			body := baselineResponsesBody(b, sz.target)
			spoolDir := b.TempDir()
			exec := &benchWireExecutor{}
			spec := newBenchWireSpec(b, exec, 16<<10, spoolDir, 64<<10)

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
					b.Fatalf("wire end-to-end code=%d: %s", rec.Code, rec.Body.String())
				}
			}
			b.StopTimer()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			reportBaselineGC(b, before, after)
		})
	}
}

// -----------------------------------------------------------------------------
// 19.2 Required Body Shapes at 1 MiB
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadStages_Shapes(b *testing.B) {
	const target = 1 << 20 // 1 MiB
	spoolDir := b.TempDir()
	exec := &benchWireExecutor{}
	spec := newBenchWireSpec(b, exec, 16<<10, spoolDir, 64<<10)

	b.Run("GiantString", func(b *testing.B) {
		body := baselineResponsesBody(b, target)
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
	})

	b.Run("LateModel", func(b *testing.B) {
		body := benchLateModelBody(b, target)
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		b.ResetTimer()
		for b.Loop() {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(spec, rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("code=%d: %s", rec.Code, rec.Body.String())
			}
		}
	})

	b.Run("Tools", func(b *testing.B) {
		body := benchToolsBody(b, target)
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		b.ResetTimer()
		for b.Loop() {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(spec, rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("code=%d: %s", rec.Code, rec.Body.String())
			}
		}
	})

	b.Run("MalformedJSON", func(b *testing.B) {
		body := benchMalformedJSONBody(b, target)
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		b.ResetTimer()
		for b.Loop() {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(spec, rec, req)
			if rec.Code != http.StatusBadRequest {
				b.Fatalf("want 400 for malformed json, got %d", rec.Code)
			}
		}
	})

	b.Run("CanonicalFallback", func(b *testing.B) {
		body := benchCanonicalFallbackBody(b, target)
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		b.ResetTimer()
		for b.Loop() {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(spec, rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("code=%d: %s", rec.Code, rec.Body.String())
			}
		}
	})

	b.Run("ReplayFailover", func(b *testing.B) {
		body := baselineResponsesBody(b, target)
		failoverExec := &benchWireExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				// Attempt 1: open and simulate failure
				rc1, err := src.Open()
				if err != nil {
					return largebody.ExecutionResult{}, err
				}
				buf := make([]byte, 32*1024)
				_, _ = rc1.Read(buf)
				_ = rc1.Close()

				// Attempt 2: failover re-opens replay source and streams to completion
				rc2, err := src.Open()
				if err != nil {
					return largebody.ExecutionResult{}, err
				}
				defer func() { _ = rc2.Close() }()
				for {
					_, rerr := rc2.Read(buf)
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
						{Kind: aipapi.EventTextDelta, Delta: "failover ok"},
						{Kind: aipapi.EventResponseFinished},
					}),
					Facts: largebody.ResponseFacts{
						RequestID:      accepted.Stamp.IdentityDigest().CallID("call_failover"),
						EffectiveModel: accepted.WireRequest.CandidateModel,
					},
				}, nil
			},
		}
		failoverSpec := newBenchWireSpec(b, failoverExec, 16<<10, spoolDir, 64<<10)

		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		b.ResetTimer()
		for b.Loop() {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(failoverSpec, rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("code=%d", rec.Code)
			}
		}
	})
}

// bench20MiBChunkedBody splits content across 4 messages so each message string
// is ~5 MiB (< MaxPartTextBytes 8 MiB) while total payload is exactly target bytes.
func bench20MiBChunkedBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const numParts = 4
	const prefix = `{"model":"stub:bench","input":[`
	const msgPrefix = `{"role":"user","content":"`
	const msgSuffix = `"}`
	const suffix = `]}`
	fixedLen := len(prefix) + len(suffix) + numParts*(len(msgPrefix)+len(msgSuffix)) + (numParts - 1)
	totalPad := target - fixedLen
	if totalPad < 0 {
		tb.Fatalf("target %d too small", target)
	}
	padPerPart := totalPad / numParts
	remainder := totalPad % numParts

	var b strings.Builder
	b.Grow(target)
	b.WriteString(prefix)
	for i := range numParts {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(msgPrefix)
		p := padPerPart
		if i == 0 {
			p += remainder
		}
		b.WriteString(strings.Repeat("a", p))
		b.WriteString(msgSuffix)
	}
	b.WriteString(suffix)
	out := []byte(b.String())
	if len(out) != target {
		tb.Fatalf("chunked body len=%d want %d", len(out), target)
	}
	return out
}

// -----------------------------------------------------------------------------
// 19.2 Test-only 20 MiB Case (gated by testing.Short())
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadStages_20MiB_WireEndToEnd(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping 20 MiB benchmark in -short mode")
	}
	const target = 20 << 20 // 20 MiB
	body := bench20MiBChunkedBody(b, target)
	spoolDir := b.TempDir()
	exec := &benchWireExecutor{}
	spec := newBenchWireSpec(b, exec, 16<<10, spoolDir, 64<<10)
	spec.MaxRequestBodyBytes = 32 << 20

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
			b.Fatalf("wire 20MiB code=%d: %s", rec.Code, rec.Body.String())
		}
	}
	b.StopTimer()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	reportBaselineGC(b, before, after)
}

// -----------------------------------------------------------------------------
// 19.2 Verification: Decode Permit is Never Held During Upload/Spill I/O
// -----------------------------------------------------------------------------

func TestLargePayloadStages_DecodePermitNotHeldDuringUpload(t *testing.T) {
	t.Parallel()
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(t, target)
	spoolDir := t.TempDir()
	exec := &benchWireExecutor{}
	spec := newBenchWireSpec(t, exec, 16<<10, spoolDir, 64<<10)

	var uploadCompleted atomic.Bool
	var permitHeldDuringProof atomic.Bool
	var permitHeldDuringAssessment atomic.Bool
	var permitHeldDuringCommit atomic.Bool

	spec.OnCandidateCapture = func(r *http.Request, res frontendpipe.CandidateCaptureResult) {
		uploadCompleted.Store(true)
	}
	spec.OnCandidateProof = func(r *http.Request, res frontendpipe.CandidateProofResult) {
		if !uploadCompleted.Load() {
			t.Errorf("proof executed before upload completed!")
		}
		permitHeldDuringProof.Store(res.PermitHeld)
	}
	spec.OnCandidateAssessment = func(r *http.Request, res frontendpipe.CandidateAssessmentResult) {
		permitHeldDuringAssessment.Store(res.PermitHeld)
	}
	spec.OnWireCommit = func(r *http.Request, res frontendpipe.WireCommitResult) {
		// Permit must be RELEASED before wire commit
		permitHeldDuringCommit.Store(res.PermitHeld)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d: %s", rec.Code, rec.Body.String())
	}
	if !uploadCompleted.Load() {
		t.Fatal("upload was never recorded")
	}
	if !permitHeldDuringProof.Load() {
		t.Fatal("expected decode permit to be held during proof compilation")
	}
	if !permitHeldDuringAssessment.Load() {
		t.Fatal("expected decode permit to be held during assessment")
	}
	if permitHeldDuringCommit.Load() {
		t.Fatal("expected decode permit to be RELEASED before wire commit")
	}
}
