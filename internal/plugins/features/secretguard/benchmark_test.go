package secretguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

const (
	benchmarkOneKiB  = 1 << 10
	benchmarkTenKiB  = 10 << 10
	benchmarkHundred = 100 << 10
	benchmarkOneMiB  = 1 << 20
	benchmarkTwoMiB  = 2 << 20
)

type betterLeaksBenchmarkCase struct {
	name            string
	detector        string
	kind            FragmentKind
	payloadBytes    int
	hit             bool
	capBounded      bool
	call            aipapi.Call
	matcher         sdk.Matcher
	generation      *GenerationServices
	expectFindings  bool
	expectScanError bool
}

func BenchmarkBetterLeaksHotPath(b *testing.B) {
	for _, tc := range betterLeaksBenchmarkCases(b) {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(tc.payloadBytes))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := runBetterLeaksBenchmarkCase(tc); err != nil {
					if tc.expectScanError && errors.Is(err, errBetterLeaksFindingCap) {
						continue
					}
					b.Fatalf("benchmark case %s failed: %v", tc.name, err)
				}
			}
		})
	}
}

func BenchmarkBetterLeaksHotPathParallel(b *testing.B) {
	for _, tc := range betterLeaksBenchmarkCases(b) {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(tc.payloadBytes))
			b.SetParallelism(1)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if err := runBetterLeaksBenchmarkCase(tc); err != nil {
						if tc.expectScanError && errors.Is(err, errBetterLeaksFindingCap) {
							continue
						}
						b.Errorf("parallel benchmark case %s failed: %v", tc.name, err)
						return
					}
				}
			})
		})
	}
}

func BenchmarkBetterLeaksScannerBuild(b *testing.B) {
	policy := benchmarkBetterLeaksPolicy()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detector, err := newBetterLeaksScanner(policy)
		if err != nil || detector == nil {
			b.Fatal("scanner construction failed")
		}
		runtime.KeepAlive(detector)
	}
}

func BenchmarkBetterLeaksHotPathParallelSteady(b *testing.B) {
	for _, tc := range betterLeaksSteadyParallelCases(b) {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(tc.payloadBytes))
			b.SetParallelism(1)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if err := runBetterLeaksBenchmarkCase(tc); err != nil {
						b.Errorf("steady parallel benchmark case %s failed: %v", tc.name, err)
						return
					}
				}
			})
		})
	}
}

func BenchmarkBetterLeaksGoroutineRetainedCount(b *testing.B) {
	cases := betterLeaksBenchmarkCases(b)
	var tc betterLeaksBenchmarkCase
	for _, candidate := range cases {
		if candidate.detector == "betterleaks-only" && candidate.payloadBytes == benchmarkHundred && !candidate.hit && candidate.kind == FragmentText {
			tc = candidate
			break
		}
	}
	if tc.generation == nil {
		b.Fatal("missing BetterLeaks goroutine-bound case")
	}
	before := runtime.NumGoroutine()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := runBetterLeaksBenchmarkCase(tc); err != nil {
			b.Fatal("goroutine-bound benchmark case failed")
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(before), "goroutines-before")
	b.ReportMetric(float64(runtime.NumGoroutine()), "goroutines-after")
}

func BenchmarkBetterLeaksGoroutineConcurrentBound(b *testing.B) {
	cases := betterLeaksBenchmarkCases(b)
	var tc betterLeaksBenchmarkCase
	for _, candidate := range cases {
		if candidate.detector == "betterleaks-only" && candidate.payloadBytes == benchmarkHundred && !candidate.hit && candidate.kind == FragmentText {
			tc = candidate
			break
		}
	}
	if tc.generation == nil {
		b.Fatal("missing BetterLeaks goroutine-bound case")
	}
	before := runtime.NumGoroutine()
	var peak atomic.Int64
	peak.Store(int64(before))
	stop := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(100 * time.Microsecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				got := int64(runtime.NumGoroutine())
				for {
					old := peak.Load()
					if got <= old || peak.CompareAndSwap(old, got) {
						break
					}
				}
			case <-stop:
				return
			}
		}
	}()
	b.ReportAllocs()
	b.SetBytes(int64(tc.payloadBytes))
	b.SetParallelism(1)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := runBetterLeaksBenchmarkCase(tc); err != nil {
				b.Errorf("concurrent goroutine-bound benchmark case failed: %v", err)
				return
			}
		}
	})
	b.StopTimer()
	close(stop)
	<-monitorDone
	b.ReportMetric(float64(before), "goroutines-before")
	b.ReportMetric(float64(peak.Load()), "goroutines-peak-during")
}

// TestBetterLeaksBenchmarkCorpus is a canary for benchmark validity. It keeps
// positive cases from silently becoming no-op scans and keeps the generic-heavy
// case explicitly classified as a bounded finding-cap failure rather than as a
// throughput result.
func TestBetterLeaksBenchmarkCorpus(t *testing.T) {
	cases := betterLeaksBenchmarkCases(t)
	positive := 0
	capCases := 0
	for _, tc := range cases {
		err := runBetterLeaksBenchmarkCase(tc)
		if tc.expectScanError {
			capCases++
			if err == nil || !errors.Is(err, errBetterLeaksFindingCap) {
				t.Fatalf("benchmark corpus case %s did not exercise the expected finding-cap error: %v", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("benchmark corpus case %s failed: %v", tc.name, err)
		}
		if tc.hit {
			positive++
		}
	}
	if positive == 0 || capCases == 0 {
		t.Fatal("benchmark corpus lacks positive or cap-bounded coverage")
	}
}

func TestBetterLeaksBenchmarkNegativeControls(t *testing.T) {
	var positive betterLeaksBenchmarkCase
	var bounded betterLeaksBenchmarkCase
	var foundPositive, foundBounded bool
	for _, tc := range betterLeaksBenchmarkCases(t) {
		if tc.expectScanError && !foundBounded {
			bounded = tc
			foundBounded = true
		}
		if tc.detector == "betterleaks-only" && tc.hit && !tc.capBounded && !foundPositive {
			positive = tc
			foundPositive = true
		}
	}
	if !foundPositive || !foundBounded {
		t.Fatal("negative-control corpus cases are incomplete")
	}

	withoutDetection := positive
	withoutDetection.call = benchmarkCall(positive.kind, benchmarkPayload(positive.payloadBytes, positive.kind, false))
	if err := runBetterLeaksBenchmarkCase(withoutDetection); err == nil || !strings.Contains(err.Error(), "positive benchmark case produced no finding") {
		t.Fatalf("positive detection removal was not rejected by the benchmark harness: %v", err)
	}

	withoutCapStimulus := bounded
	withoutCapStimulus.call.Messages = append([]aipapi.Message(nil), bounded.call.Messages[:1]...)
	withoutCapStimulus.payloadBytes = len(bounded.call.Messages[0].Parts[0].Text)
	if err := runBetterLeaksBenchmarkCase(withoutCapStimulus); err == nil || errors.Is(err, errBetterLeaksFindingCap) || !strings.Contains(err.Error(), "did not return the finding-cap error") {
		t.Fatalf("cap stimulus removal was not rejected by the benchmark harness: %v", err)
	}
}

// TestBetterLeaksLatencyPercentiles is opt-in because wall-clock sampling is
// intentionally separate from Go's allocation benchmark loop. It prints only
// bounded case labels and durations, never request content or finding values.
func TestBetterLeaksLatencyPercentiles(t *testing.T) {
	if os.Getenv("SECRETGUARD_BENCH_PERCENTILES") != "1" {
		t.Skip("set SECRETGUARD_BENCH_PERCENTILES=1 to collect wall-latency percentiles")
	}
	samples := max(benchmarkEnvInt("SECRETGUARD_BENCH_SAMPLES", 100), 100)
	for _, tc := range betterLeaksPercentileCases(t) {
		for range 2 {
			if err := runBetterLeaksBenchmarkCase(tc); err != nil {
				t.Fatal("latency warmup failed")
			}
		}
		durations := make([]time.Duration, 0, samples)
		repetitions := benchmarkLatencyRepetitions(tc.payloadBytes)
		for range samples {
			started := time.Now()
			for range repetitions {
				if err := runBetterLeaksBenchmarkCase(tc); err != nil {
					t.Fatal("latency sample failed")
				}
			}
			durations = append(durations, time.Since(started)/time.Duration(repetitions))
		}
		slices.Sort(durations)
		t.Logf("percentile detector=%s kind=%s bytes=%d hit=%t samples=%d repetitions=%d p50=%s p95=%s p99=%s", tc.detector, benchmarkKindName(tc.kind), tc.payloadBytes, tc.hit, samples, repetitions, percentile(durations, 0.50), percentile(durations, 0.95), percentile(durations, 0.99))
	}
}

func betterLeaksBenchmarkCases(tb testing.TB) []betterLeaksBenchmarkCase {
	tb.Helper()
	policy := benchmarkBetterLeaksPolicy()
	generation, err := BuildGenerationServices(DetectorPolicy{BetterLeaks: policy}, engine.NewDisabledSource())
	if err != nil {
		tb.Fatal("build benchmark generation failed")
	}
	cat, err := engine.BuildCatalog([]engine.CatalogInput{{
		Name:           "GITHUB_TOKEN",
		Value:          testkit.SyntheticGitHubPAT,
		SourceCategory: sdk.SourceCategoryOperatorEnv,
	}}, 8)
	if err != nil {
		tb.Fatal("build benchmark exact catalog failed")
	}
	exact := engine.AsMatcher(engine.NewMatcher(cat))
	var cases []betterLeaksBenchmarkCase
	for _, size := range []int{benchmarkOneKiB, benchmarkTenKiB, benchmarkHundred, benchmarkOneMiB, benchmarkTwoMiB} {
		for _, kind := range []FragmentKind{FragmentText, FragmentJSON} {
			for _, hit := range []bool{false, true} {
				payload := benchmarkPayload(size, kind, hit)
				base := betterLeaksBenchmarkCase{
					kind:         kind,
					payloadBytes: len(payload),
					hit:          hit,
					call:         benchmarkCall(kind, payload),
				}
				cases = append(cases,
					base.with("exact-only", exact, nil),
					base.with("betterleaks-only", nil, generation),
					base.with("hybrid", exact, generation),
				)
			}
		}
	}
	capCall, capBytes := genericHeavyBenchmarkCall(benchmarkHundred)
	capBase := betterLeaksBenchmarkCase{
		kind:            FragmentText,
		payloadBytes:    capBytes,
		capBounded:      true,
		call:            capCall,
		expectScanError: true,
	}
	cases = append(cases,
		capBase.with("betterleaks-only-generic-heavy-cap-bounded", nil, generation),
		capBase.with("hybrid-generic-heavy-cap-bounded", exact, generation),
	)
	for _, fixture := range []struct {
		name    string
		payload string
	}{
		{name: "generic-hit", payload: `api_token = "` + testkit.SyntheticGenericDetectorAPIKey + `"`},
		{name: "aws-multipart-hit", payload: `aws_token = "` + testkit.SyntheticAWSAccessKeyID + `" aws_secret_access_key = "` + testkit.SyntheticAWSSecretAccessKey + `"`},
	} {
		payload := []byte(fixture.payload)
		base := betterLeaksBenchmarkCase{
			kind:         FragmentText,
			payloadBytes: len(payload),
			hit:          true,
			call:         benchmarkCall(FragmentText, payload),
		}
		cases = append(cases,
			base.with("betterleaks-only-"+fixture.name, nil, generation),
			base.with("hybrid-"+fixture.name, exact, generation),
		)
	}
	return cases
}

func betterLeaksSteadyParallelCases(tb testing.TB) []betterLeaksBenchmarkCase {
	tb.Helper()
	var steady []betterLeaksBenchmarkCase
	for _, tc := range betterLeaksBenchmarkCases(tb) {
		if tc.capBounded {
			continue
		}
		if tc.payloadBytes != benchmarkOneKiB && tc.payloadBytes != benchmarkHundred && tc.payloadBytes != benchmarkTwoMiB && !strings.Contains(tc.detector, "generic-hit") && !strings.Contains(tc.detector, "aws-multipart-hit") {
			continue
		}
		steady = append(steady, tc)
	}
	return steady
}

func betterLeaksPercentileCases(tb testing.TB) []betterLeaksBenchmarkCase {
	tb.Helper()
	var selected []betterLeaksBenchmarkCase
	for _, tc := range betterLeaksBenchmarkCases(tb) {
		if tc.capBounded || (tc.payloadBytes != benchmarkOneKiB && tc.payloadBytes != benchmarkHundred) {
			continue
		}
		selected = append(selected, tc)
	}
	return selected
}

func (tc betterLeaksBenchmarkCase) with(detector string, matcher sdk.Matcher, generation *GenerationServices) betterLeaksBenchmarkCase {
	tc.name = fmt.Sprintf("%s/%s/%s/%d/%s", detector, benchmarkKindName(tc.kind), benchmarkHitName(tc.hit), tc.payloadBytes, benchmarkCaseClass(tc.capBounded))
	tc.detector = detector
	tc.matcher = matcher
	tc.generation = generation
	tc.expectFindings = tc.hit
	return tc
}

func benchmarkBetterLeaksPolicy() BetterLeaksPolicy {
	workers := max(min(runtime.GOMAXPROCS(0), 4), 1)
	return BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
		Workers:           workers,
		MaxFindings:       DefaultBetterLeaksMaxFindings,
	}
}

func runBetterLeaksBenchmarkCase(tc betterLeaksBenchmarkCase) error {
	result, err := scanCall(context.Background(), &tc.call, tc.matcher, modeScan, tc.payloadBytes, tc.generation)
	if tc.expectScanError {
		if err == nil || !errors.Is(err, errBetterLeaksFindingCap) {
			return fmt.Errorf("bounded generic-heavy case did not return the finding-cap error: %w", err)
		}
		if len(result.Findings) != DefaultBetterLeaksMaxFindings {
			return fmt.Errorf("bounded generic-heavy case projected findings=%d, want %d", len(result.Findings), DefaultBetterLeaksMaxFindings)
		}
		if result.BytesScanned != tc.payloadBytes {
			return fmt.Errorf("bounded generic-heavy case admitted %d bytes, want %d", result.BytesScanned, tc.payloadBytes)
		}
		if result.ScanLimitHit {
			return fmt.Errorf("bounded generic-heavy case hit the byte scan limit")
		}
		return err
	}
	if err != nil {
		return err
	}
	if result.ScanLimitHit {
		return fmt.Errorf("throughput case hit the byte scan limit")
	}
	if result.BytesScanned != tc.payloadBytes {
		return fmt.Errorf("throughput case admitted %d bytes, want %d", result.BytesScanned, tc.payloadBytes)
	}
	if !tc.hit && len(result.Findings) != 0 {
		return fmt.Errorf("no-hit benchmark case produced %d findings", len(result.Findings))
	}
	if tc.expectFindings && len(result.Findings) == 0 {
		return fmt.Errorf("positive benchmark case produced no finding")
	}
	return nil
}

func benchmarkPayload(size int, kind FragmentKind, hit bool) []byte {
	if kind == FragmentJSON {
		if hit {
			prefix := []byte(`{"github_token":"`)
			suffix := []byte(`","blob":"`)
			end := []byte(`"}`)
			filler := benchmarkSizedBytes(size-len(prefix)-len(suffix)-len(end)-len(testkit.SyntheticGitHubPAT), nil, false)
			return append(append(append(append(prefix, testkit.SyntheticGitHubPAT...), suffix...), filler...), end...)
		}
		prefix := []byte(`{"blob":"`)
		suffix := []byte(`"}`)
		value := benchmarkSizedBytes(size-len(prefix)-len(suffix), nil, false)
		return append(append(prefix, value...), suffix...)
	}
	marker := []byte("GITHUB_TOKEN=" + testkit.SyntheticGitHubPAT + "\n")
	return benchmarkSizedBytes(size, marker, hit)
}

func benchmarkSizedBytes(size int, marker []byte, hit bool) []byte {
	if size < len(marker) {
		size = len(marker)
	}
	out := bytes.Repeat([]byte{'x'}, size)
	if hit && len(marker) > 0 {
		copy(out, marker)
	}
	return out
}

func genericHeavyBenchmarkCall(size int) (aipapi.Call, int) {
	const candidateCount = DefaultBetterLeaksMaxFindings + 1
	candidate := "api_token = \"" + testkit.SyntheticGenericDetectorAPIKey + "\" GITHUB_TOKEN=" + testkit.SyntheticGitHubPAT + "\n"
	messages := make([]aipapi.Message, candidateCount)
	bytesAdmitted := 0
	for i := range messages {
		text := candidate
		if i == 0 && len(text) < size {
			text += string(bytes.Repeat([]byte{'x'}, size-len(text)))
		}
		messages[i] = aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: text}}}
		bytesAdmitted += len(text)
	}
	return aipapi.Call{Messages: messages}, bytesAdmitted
}

func benchmarkCall(kind FragmentKind, payload []byte) aipapi.Call {
	part := aipapi.Part{Kind: aipapi.PartText, Text: string(payload)}
	if kind == FragmentJSON {
		part = aipapi.Part{Kind: aipapi.PartJSON, Content: json.RawMessage(bytes.Clone(payload))}
	}
	return aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{part}}}}
}

func benchmarkKindName(kind FragmentKind) string {
	if kind == FragmentJSON {
		return "json"
	}
	return "text"
}

func benchmarkHitName(hit bool) string {
	if hit {
		return "positive"
	}
	return "no-hit"
}

func benchmarkCaseClass(capBounded bool) string {
	if capBounded {
		return "bounded-failure"
	}
	return "throughput"
}

func benchmarkEnvInt(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func benchmarkLatencyRepetitions(payloadBytes int) int {
	switch {
	case payloadBytes <= benchmarkTenKiB:
		return 8
	case payloadBytes <= benchmarkHundred:
		return 2
	default:
		return 1
	}
}

func percentile(values []time.Duration, fraction float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := max(int(math.Ceil(float64(len(values))*fraction))-1, 0)
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}
