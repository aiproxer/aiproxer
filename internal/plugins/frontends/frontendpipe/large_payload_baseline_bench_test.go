package frontendpipe_test

// Task 1.10 current-main performance baseline (Requirements: 21; Design: 15).
//
// Test-only benchmark harness for the canonical request path at large-payload
// sizes. No production changes; no fast path exists yet.
//
// Coverage: shared JSON preflight, OpenAI Responses decode, OpenAI Chat decode,
// Call clone amplification, canonical re-serialization (provider-encode proxy),
// provider-open fixture latency (httptest loopback POST), and production-like
// frontend composition (OpenAI Responses Handler with mock executor,
// traffic-disabled nil observer path including current #602 no-marshal
// optimization). Current main also includes #592 workstore query savings (not
// on this path, noted for provenance; do not use stale #531-era numbers).
//
// Sizes: 32 KiB, 256 KiB, 1 MiB, 5 MiB. The test-only 20 MiB raised-limit case
// lives in explicitly named BenchmarkLargePayloadBaseline_20MiB_* benchmarks
// with raised MaxRequestBodyBytes/jsonguard limits and is skipped under
// -short, so default unit tests never allocate 20 MiB. Benchmarks never run
// under plain `go test`; run explicitly, e.g.:
//
//	go test -run=^$ -bench=LargePayloadBaseline -benchmem -benchtime=100x ./internal/plugins/frontends/frontendpipe/
//	go test -run=^$ -bench='LargePayloadBaseline_(Preflight|DecodeResponses|DecodeChat|CloneCall|EncodeJSON)' -benchmem -benchtime=10x ./internal/plugins/frontends/frontendpipe/
//	go test -run=^$ -bench='LargePayloadBaseline_20MiB' -benchmem -benchtime=10x ./internal/plugins/frontends/frontendpipe/
//
// Machine/mode caveats are recorded in
// .kiro/specs/archive/large-payload-streaming-fast-path/evidence/1.10-perf-baseline.md
// (Windows runner, no -race).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/jsonguard"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// baselineSink retains the last benchmark result so the compiler cannot
// eliminate the measured work.
var baselineSink any

type baselineSize struct {
	name   string
	target int
}

func baselineSizes() []baselineSize {
	return []baselineSize{
		{name: "32KiB", target: 32 << 10},
		{name: "256KiB", target: 256 << 10},
		{name: "1MiB", target: 1 << 20},
		{name: "5MiB", target: 5 << 20},
	}
}

// baselineResponsesBody builds a valid OpenAI Responses create body whose total
// length is exactly target bytes: {"model":"stub:bench","input":"<pad>"}.
// Single-string input keeps every size valid under the default 8 MiB envelope
// (5 MiB string < MaxPartTextBytes); the 20 MiB case uses raised limits.
func baselineResponsesBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const prefix = `{"model":"stub:bench","input":"`
	const suffix = `"}`
	pad := target - len(prefix) - len(suffix)
	if pad < 0 {
		tb.Fatalf("target %d smaller than envelope %d", target, len(prefix)+len(suffix))
	}
	var b strings.Builder
	b.Grow(target)
	b.WriteString(prefix)
	b.WriteString(strings.Repeat("a", pad))
	b.WriteString(suffix)
	out := []byte(b.String())
	if len(out) != target {
		tb.Fatalf("responses body len=%d want %d", len(out), target)
	}
	return out
}

// baselineChatBody builds a valid OpenAI Chat create body with total length
// exactly target bytes:
// {"model":"stub:bench","messages":[{"role":"user","content":"<pad>"}]}.
func baselineChatBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const prefix = `{"model":"stub:bench","messages":[{"role":"user","content":"`
	const suffix = `"}]}`
	pad := target - len(prefix) - len(suffix)
	if pad < 0 {
		tb.Fatalf("target %d smaller than envelope %d", target, len(prefix)+len(suffix))
	}
	var b strings.Builder
	b.Grow(target)
	b.WriteString(prefix)
	b.WriteString(strings.Repeat("a", pad))
	b.WriteString(suffix)
	out := []byte(b.String())
	if len(out) != target {
		tb.Fatalf("chat body len=%d want %d", len(out), target)
	}
	return out
}

func reportBaselineGC(b *testing.B, before, after runtime.MemStats) {
	b.Helper()
	if b.N <= 0 {
		return
	}
	n := float64(b.N)
	b.ReportMetric(float64(after.NumGC-before.NumGC)/n, "gc-cycles/op")
	var pause uint64
	if after.PauseTotalNs >= before.PauseTotalNs {
		pause = after.PauseTotalNs - before.PauseTotalNs
	}
	b.ReportMetric(float64(pause)/n, "gc-pause-ns/op")
	b.ReportMetric(float64(after.HeapAlloc), "live-heap-B")
	b.ReportMetric(float64(after.HeapInuse), "peak-heap-B")
}

type baselineExec struct{}

func (baselineExec) Execute(_ context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (baselineExec) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }
func (baselineExec) WallClock() func() time.Time                                { return nil }

func BenchmarkLargePayloadBaseline_Preflight(b *testing.B) {
	for _, sz := range baselineSizes() {
		b.Run(sz.name, func(b *testing.B) {
			body := baselineResponsesBody(b, sz.target)
			limits := jsonguard.DefaultLimits()
			if _, err := jsonguard.Preflight(body, limits); err != nil {
				b.Fatalf("preflight sanity: %v", err)
			}
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for b.Loop() {
				if _, err := jsonguard.PreflightWithContext(b.Context(), body, limits); err != nil {
					b.Fatalf("preflight: %v", err)
				}
			}
			b.StopTimer()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			reportBaselineGC(b, before, after)
		})
	}
}

func BenchmarkLargePayloadBaseline_DecodeResponses(b *testing.B) {
	for _, sz := range baselineSizes() {
		b.Run(sz.name, func(b *testing.B) {
			body := baselineResponsesBody(b, sz.target)
			if _, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:bench"}); err != nil {
				b.Fatalf("decode sanity: %v", err)
			}
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for b.Loop() {
				decoded, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:bench"})
				if err != nil {
					b.Fatalf("decode: %v", err)
				}
				baselineSink = decoded
			}
			b.StopTimer()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			reportBaselineGC(b, before, after)
		})
	}
}

func BenchmarkLargePayloadBaseline_DecodeChat(b *testing.B) {
	for _, sz := range baselineSizes() {
		b.Run(sz.name, func(b *testing.B) {
			body := baselineChatBody(b, sz.target)
			if _, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:bench"}); err != nil {
				b.Fatalf("decode sanity: %v", err)
			}
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for b.Loop() {
				decoded, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:bench"})
				if err != nil {
					b.Fatalf("decode: %v", err)
				}
				baselineSink = decoded
			}
			b.StopTimer()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			reportBaselineGC(b, before, after)
		})
	}
}

func BenchmarkLargePayloadBaseline_CloneCall(b *testing.B) {
	for _, sz := range baselineSizes() {
		b.Run(sz.name, func(b *testing.B) {
			body := baselineResponsesBody(b, sz.target)
			decoded, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:bench"})
			if err != nil {
				b.Fatalf("decode sanity: %v", err)
			}
			if decoded.Call == nil {
				b.Fatal("decode returned nil call")
			}
			call := *decoded.Call
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for b.Loop() {
				cl := aipapi.CloneCall(call)
				if len(cl.Messages) == 0 {
					b.Fatal("clone lost messages")
				}
				baselineSink = cl
			}
			b.StopTimer()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			reportBaselineGC(b, before, after)
		})
	}
}

func BenchmarkLargePayloadBaseline_EncodeJSON(b *testing.B) {
	for _, sz := range baselineSizes() {
		b.Run(sz.name, func(b *testing.B) {
			body := baselineResponsesBody(b, sz.target)
			decoded, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:bench"})
			if err != nil {
				b.Fatalf("decode sanity: %v", err)
			}
			if decoded.Call == nil {
				b.Fatal("decode returned nil call")
			}
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for b.Loop() {
				out, err := json.Marshal(decoded.Call)
				if err != nil {
					b.Fatalf("marshal: %v", err)
				}
				if len(out) == 0 {
					b.Fatal("empty marshal")
				}
				baselineSink = out
			}
			b.StopTimer()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			reportBaselineGC(b, before, after)
		})
	}
}

func BenchmarkLargePayloadBaseline_ProviderOpen(b *testing.B) {
	for _, sz := range baselineSizes() {
		b.Run(sz.name, func(b *testing.B) {
			body := baselineResponsesBody(b, sz.target)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				_ = r.Body.Close()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()
			client := server.Client()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for b.Loop() {
				resp, err := client.Post(server.URL, "application/json", bytes.NewReader(body))
				if err != nil {
					b.Fatalf("post: %v", err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					b.Fatalf("status=%d want 200", resp.StatusCode)
				}
			}
			b.StopTimer()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			reportBaselineGC(b, before, after)
		})
	}
}

func BenchmarkLargePayloadBaseline_PipeEndToEnd(b *testing.B) {
	for _, sz := range baselineSizes() {
		b.Run(sz.name, func(b *testing.B) {
			body := baselineResponsesBody(b, sz.target)
			h := &openairesponses.Handler{
				Exec:                 baselineExec{},
				DefaultRouteSelector: "stub:bench",
				RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
				MaxRequestBodyBytes:  8 << 20,
			}
			sanityReq := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			sanityRR := httptest.NewRecorder()
			h.ServeHTTP(sanityRR, sanityReq)
			if sanityRR.Code != http.StatusOK {
				b.Fatalf("pipe sanity: status=%d body=%.200s", sanityRR.Code, sanityRR.Body.String())
			}
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for b.Loop() {
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, req)
				if rr.Code != http.StatusOK {
					b.Fatalf("pipe status=%d", rr.Code)
				}
				baselineSink = rr.Body.Len()
			}
			b.StopTimer()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			reportBaselineGC(b, before, after)
		})
	}
}

// BenchmarkLargePayloadBaseline_20MiB_PreflightDecodeCloneEncode covers the
// test-only 20 MiB raised-limit case for CPU stages. It is skipped under
// -short so default unit tests never allocate 20 MiB; run explicitly with
// -bench='LargePayloadBaseline_20MiB' -benchtime=10x. Raised limits apply to
// the body ceiling and jsonguard envelope only; canonical single-part Validate
// still caps one text part at 8 MiB, so this measures ingress/decode/clone/
// encode cost, not a full 200 end-to-end execution.
func BenchmarkLargePayloadBaseline_20MiB_PreflightDecodeCloneEncode(b *testing.B) {
	if testing.Short() {
		b.Skip("20MiB test-only baseline requires non-short mode")
	}
	const target = 20 << 20
	body := baselineResponsesBody(b, target)
	limits := jsonguard.DefaultLimits()
	limits.MaxBytes = 32 << 20
	limits.MaxStringBytes = 32 << 20
	if _, err := jsonguard.Preflight(body, limits); err != nil {
		b.Fatalf("preflight sanity: %v", err)
	}
	decoded, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:bench"})
	if err != nil {
		b.Fatalf("decode sanity: %v", err)
	}
	if decoded.Call == nil {
		b.Fatal("decode returned nil call")
	}
	call := *decoded.Call

	b.Run("Preflight", func(b *testing.B) {
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		b.ResetTimer()
		for b.Loop() {
			if _, err := jsonguard.PreflightWithContext(b.Context(), body, limits); err != nil {
				b.Fatalf("preflight: %v", err)
			}
		}
		b.StopTimer()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		reportBaselineGC(b, before, after)
	})

	b.Run("DecodeResponses", func(b *testing.B) {
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		b.ResetTimer()
		for b.Loop() {
			d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:bench"})
			if err != nil {
				b.Fatalf("decode: %v", err)
			}
			baselineSink = d
		}
		b.StopTimer()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		reportBaselineGC(b, before, after)
	})

	b.Run("CloneCall", func(b *testing.B) {
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		b.ResetTimer()
		for b.Loop() {
			cl := aipapi.CloneCall(call)
			if len(cl.Messages) == 0 {
				b.Fatal("clone lost messages")
			}
			baselineSink = cl
		}
		b.StopTimer()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		reportBaselineGC(b, before, after)
	})

	b.Run("EncodeJSON", func(b *testing.B) {
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		b.ResetTimer()
		for b.Loop() {
			out, err := json.Marshal(decoded.Call)
			if err != nil {
				b.Fatalf("marshal: %v", err)
			}
			baselineSink = out
		}
		b.StopTimer()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		reportBaselineGC(b, before, after)
	})
}

// BenchmarkLargePayloadBaseline_20MiB_ProviderOpen measures loopback
// provider-open latency for a 20 MiB replay-sized POST. Same -short gating and
// explicit-name discipline as the CPU stages above.
func BenchmarkLargePayloadBaseline_20MiB_ProviderOpen(b *testing.B) {
	if testing.Short() {
		b.Skip("20MiB test-only baseline requires non-short mode")
	}
	const target = 20 << 20
	body := baselineResponsesBody(b, target)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client := server.Client()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		resp, err := client.Post(server.URL, "application/json", bytes.NewReader(body))
		if err != nil {
			b.Fatalf("post: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("status=%d want 200", resp.StatusCode)
		}
	}
	b.StopTimer()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	reportBaselineGC(b, before, after)
}
