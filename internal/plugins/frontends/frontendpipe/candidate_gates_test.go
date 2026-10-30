package frontendpipe_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Task 7.3: Apply cheap pre-capture gates in this order —
// 1. feature/profile/two-phase executor available;
// 2. parsed known identity/uncompressed request length below threshold => canonical;
// 3. gzip wave 1 => canonical;
// 4. frozen static disposition DefinitelyCanonical => canonical;
// 5. configured custom route hook (ResolvePreAdmissionRoute or ResolveBodyRoute) => canonical;
// only then allocate capture/scanner state.
// Do not trust compressed Content-Length as decoded length.
// Requirements 1, 2, 5, 11, 13, 21; design target-flow section.
//
// 7.2 reviewer obligation: production ServeHTTP proof that CandidatePrerequisites
// is not invoked after an outer rejection must land here.

type candidateGatesExec struct {
	mu            sync.Mutex
	executeCalls  int
	staticCalls   int
	staticDisp    largebody.StaticWireDisposition
	staticReason  largebody.StaticWireReason
	lastProfileID string
}

func (e *candidateGatesExec) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	e.mu.Lock()
	e.executeCalls++
	e.mu.Unlock()
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (e *candidateGatesExec) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

func (e *candidateGatesExec) WallClock() func() time.Time { return nil }

func (e *candidateGatesExec) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	return largebody.Assessment{}, nil
}

func (e *candidateGatesExec) ExecuteLargeBody(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
	return largebody.ExecutionResult{}, nil
}

func (e *candidateGatesExec) LargeBodyStaticDisposition(profileID string) (largebody.StaticWireDisposition, largebody.StaticWireReason) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.staticCalls++
	e.lastProfileID = profileID
	if e.staticDisp != largebody.StaticWireUnknown {
		return e.staticDisp, e.staticReason
	}
	return largebody.StaticWireNeedsRequestAssessment, largebody.StaticWireReasonNone
}

var (
	_ frontendpipe.StaticDispositionProvider = (*candidateGatesExec)(nil)
	_ largebody.LargeBodyExecutor            = (*candidateGatesExec)(nil)
)

type trackingCandidateProfile struct {
	profileID string
}

func (p *trackingCandidateProfile) ProfileID() string {
	if p.profileID != "" {
		return p.profileID
	}
	return "test_profile_v1"
}

func (p *trackingCandidateProfile) CompileProof(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	return frontendpipe.ProofOutput{}, nil
}

var _ frontendpipe.FrontendProfile = (*trackingCandidateProfile)(nil)

func newCandidateGatesSpec(
	exec *candidateGatesExec,
	prof frontendpipe.FrontendProfile,
	gateRecord *[]frontendpipe.PreCaptureResult,
) frontendpipe.Spec[struct{}] {
	return frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:       exec,
			FrontendID: "candidate_gates_test",
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 1 << 20, // 1 MiB
			},
		},
		Wire:    frontendpipe.OpenAIWire{},
		Profile: prof,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			if path == "/v1/create" {
				return frontendpipe.PathMatch{}, true
			}
			return frontendpipe.PathMatch{}, false
		},
		AltServe: func(_ context.Context, w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/v1/alt" {
				w.WriteHeader(http.StatusTeapot)
				return true
			}
			return false
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			return &frontendpipe.Decoded{
				Call: &aipapi.Call{
					ID: "call_candidate_test",
					Messages: []aipapi.Message{{
						Role:  aipapi.RoleUser,
						Parts: []aipapi.Part{aipapi.TextPart("hi")},
					}},
				},
			}, nil
		},
		BuildEncodeOpts: func(decoded *frontendpipe.Decoded) struct{} {
			return struct{}{}
		},
		WriteNonStream: func(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts struct{}) error {
			w.WriteHeader(http.StatusOK)
			return nil
		},
		OnPreCaptureGate: func(r *http.Request, res frontendpipe.PreCaptureResult) {
			if gateRecord != nil {
				*gateRecord = append(*gateRecord, res)
			}
		},
	}
}

func TestServeHTTP_CandidatePrerequisitesNotInvokedAfterOuterRejection(t *testing.T) {
	t.Parallel()

	// 7.2 reviewer obligation: production ServeHTTP proof that CandidatePrerequisites
	// is NOT invoked after an outer rejection must land here.
	t.Run("outer method rejection never reaches candidate gates", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{}
		prof := &trackingCandidateProfile{}
		var records []frontendpipe.PreCaptureResult
		spec := newCandidateGatesSpec(exec, prof, &records)

		req := httptest.NewRequest(http.MethodGet, "/v1/create", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405 Method Not Allowed", rec.Code)
		}
		if len(records) != 0 {
			t.Fatalf("candidate gates evaluated after method rejection: %v", records)
		}
		if exec.staticCalls != 0 {
			t.Fatalf("static disposition evaluated after method rejection: %d calls", exec.staticCalls)
		}
	})

	t.Run("outer altserve rejection never reaches candidate gates", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{}
		prof := &trackingCandidateProfile{}
		var records []frontendpipe.PreCaptureResult
		spec := newCandidateGatesSpec(exec, prof, &records)

		req := httptest.NewRequest(http.MethodPost, "/v1/alt", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusTeapot {
			t.Fatalf("status = %d, want 418 Teapot", rec.Code)
		}
		if len(records) != 0 {
			t.Fatalf("candidate gates evaluated after AltServe handled request: %v", records)
		}
		if exec.staticCalls != 0 {
			t.Fatalf("static disposition evaluated after AltServe: %d calls", exec.staticCalls)
		}
	})

	t.Run("outer matchpath rejection never reaches candidate gates", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{}
		prof := &trackingCandidateProfile{}
		var records []frontendpipe.PreCaptureResult
		spec := newCandidateGatesSpec(exec, prof, &records)

		req := httptest.NewRequest(http.MethodPost, "/v1/unknown", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 Not Found", rec.Code)
		}
		if len(records) != 0 {
			t.Fatalf("candidate gates evaluated after MatchPath 404: %v", records)
		}
		if exec.staticCalls != 0 {
			t.Fatalf("static disposition evaluated after MatchPath 404: %d calls", exec.staticCalls)
		}
	})

	t.Run("valid outer request reaches candidate gates", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{}
		prof := &trackingCandidateProfile{}
		var records []frontendpipe.PreCaptureResult
		spec := newCandidateGatesSpec(exec, prof, &records)

		// 2 MiB request body (above 1 MiB threshold)
		largeBody := `{"model":"test","prompt":"` + strings.Repeat("a", 2<<20) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(largeBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 OK", rec.Code)
		}
		if len(records) != 1 {
			t.Fatalf("candidate gates not evaluated once: %v", records)
		}
		if !records[0].Candidate {
			t.Fatalf("expected candidate accepted, got: %+v", records[0])
		}
	})
}

func TestCheapPreCaptureGates_EachGateDeclinesToCanonicalZeroSpool(t *testing.T) {
	t.Parallel()

	// Gate 1a: Feature disabled => canonical
	t.Run("gate 1 feature disabled declines to canonical", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{}
		prof := &trackingCandidateProfile{}
		var records []frontendpipe.PreCaptureResult
		spec := newCandidateGatesSpec(exec, prof, &records)
		spec.LargePayload.Enabled = false

		largeBody := `{"model":"test","prompt":"` + strings.Repeat("a", 2<<20) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(largeBody))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 OK", rec.Code)
		}
		if len(records) != 1 || records[0].Candidate {
			t.Fatalf("expected gate 1 decline, got: %v", records)
		}
		if records[0].Gate != frontendpipe.PreCaptureGateFeatureProfileExecutor {
			t.Fatalf("gate = %v, want PreCaptureGateFeatureProfileExecutor", records[0].Gate)
		}
		if records[0].Reason != largebody.StaticWireReasonFeatureDisabled {
			t.Fatalf("reason = %v, want StaticWireReasonFeatureDisabled", records[0].Reason)
		}
	})

	// Gate 1b: Nil profile => canonical
	t.Run("gate 1 nil profile declines to canonical", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{}
		var records []frontendpipe.PreCaptureResult
		spec := newCandidateGatesSpec(exec, nil, &records)

		largeBody := `{"model":"test","prompt":"` + strings.Repeat("a", 2<<20) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(largeBody))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 OK", rec.Code)
		}
		if len(records) != 1 || records[0].Candidate {
			t.Fatalf("expected gate 1 decline, got: %v", records)
		}
		if records[0].Gate != frontendpipe.PreCaptureGateFeatureProfileExecutor {
			t.Fatalf("gate = %v, want PreCaptureGateFeatureProfileExecutor", records[0].Gate)
		}
	})

	// Gate 1c: Missing LargeBodyExecutor capability => canonical
	t.Run("gate 1 missing two-phase executor declines to canonical", func(t *testing.T) {
		t.Parallel()
		prof := &trackingCandidateProfile{}
		var records []frontendpipe.PreCaptureResult
		spec := newCandidateGatesSpec(nil, prof, &records)
		spec.Exec = &stubExecutor{} // implements ExecutorView only, not LargeBodyExecutor

		largeBody := `{"model":"test","prompt":"` + strings.Repeat("a", 2<<20) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(largeBody))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 OK", rec.Code)
		}
		if len(records) != 1 || records[0].Candidate {
			t.Fatalf("expected gate 1 decline, got: %v", records)
		}
		if records[0].Gate != frontendpipe.PreCaptureGateFeatureProfileExecutor {
			t.Fatalf("gate = %v, want PreCaptureGateFeatureProfileExecutor", records[0].Gate)
		}
	})

	// Gate 2: Parsed known identity length below threshold => canonical
	t.Run("gate 2 known identity length below threshold declines to canonical", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{}
		prof := &trackingCandidateProfile{}
		var records []frontendpipe.PreCaptureResult
		spec := newCandidateGatesSpec(exec, prof, &records)

		// 500 bytes body < 1 MiB threshold
		smallBody := `{"model":"test","prompt":"` + strings.Repeat("a", 400) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(smallBody))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 OK", rec.Code)
		}
		if len(records) != 1 || records[0].Candidate {
			t.Fatalf("expected gate 2 decline, got: %v", records)
		}
		if records[0].Gate != frontendpipe.PreCaptureGateKnownLengthThreshold {
			t.Fatalf("gate = %v, want PreCaptureGateKnownLengthThreshold", records[0].Gate)
		}
		if records[0].Reason != largebody.StaticWireReasonBelowThreshold {
			t.Fatalf("reason = %v, want StaticWireReasonBelowThreshold", records[0].Reason)
		}
	})

	// Gate 3: Gzip wave 1 => canonical
	t.Run("gate 3 gzip wave 1 declines to canonical", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{}
		prof := &trackingCandidateProfile{}
		var records []frontendpipe.PreCaptureResult
		spec := newCandidateGatesSpec(exec, prof, &records)

		// Even with Content-Length >= 1 MiB, Content-Encoding: gzip must decline at Gate 3
		rawBody := `{"model":"test","prompt":"` + strings.Repeat("a", 2<<20) + `"}`
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		if _, err := gw.Write([]byte(rawBody)); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
		if err := gw.Close(); err != nil {
			t.Fatalf("gzip close: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/create", &buf)
		req.Header.Set("Content-Encoding", "gzip")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 OK", rec.Code)
		}

		if len(records) != 1 || records[0].Candidate {
			t.Fatalf("expected gate 3 decline, got: %v", records)
		}
		if records[0].Gate != frontendpipe.PreCaptureGateGzipWave1 {
			t.Fatalf("gate = %v, want PreCaptureGateGzipWave1", records[0].Gate)
		}
		if records[0].Reason != largebody.StaticWireReasonGzipCompressed {
			t.Fatalf("reason = %v, want StaticWireReasonGzipCompressed", records[0].Reason)
		}
	})

	// Gate 4: Frozen static disposition DefinitelyCanonical => canonical
	t.Run("gate 4 static disposition definitely canonical declines to canonical", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{
			staticDisp:   largebody.StaticWireDefinitelyCanonical,
			staticReason: largebody.StaticWireReasonStaticBlocker,
		}
		prof := &trackingCandidateProfile{}
		var records []frontendpipe.PreCaptureResult
		spec := newCandidateGatesSpec(exec, prof, &records)

		largeBody := `{"model":"test","prompt":"` + strings.Repeat("a", 2<<20) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(largeBody))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 OK", rec.Code)
		}
		if len(records) != 1 || records[0].Candidate {
			t.Fatalf("expected gate 4 decline, got: %v", records)
		}
		if records[0].Gate != frontendpipe.PreCaptureGateStaticDisposition {
			t.Fatalf("gate = %v, want PreCaptureGateStaticDisposition", records[0].Gate)
		}
		if records[0].Reason != largebody.StaticWireReasonStaticBlocker {
			t.Fatalf("reason = %v, want StaticWireReasonStaticBlocker", records[0].Reason)
		}
	})

	// Gate 5: a custom route hook forces canonical handling.
	t.Run("gate 5 custom body route hook declines to canonical", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{}
		prof := &trackingCandidateProfile{}
		var records []frontendpipe.PreCaptureResult
		spec := newCandidateGatesSpec(exec, prof, &records)
		spec.ResolveBodyRoute = func(_ []byte, _ frontendpipe.PathMatch) routing.ResolvedRoute {
			return routing.ExplicitRoute("resolved_route")
		}

		largeBody := `{"model":"test","prompt":"` + strings.Repeat("a", 2<<20) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(largeBody))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 OK", rec.Code)
		}
		if len(records) != 1 || records[0].Candidate {
			t.Fatalf("expected gate 5 decline, got: %v", records)
		}
		if records[0].Gate != frontendpipe.PreCaptureGateLegacyRouteResolver {
			t.Fatalf("gate = %v, want PreCaptureGateLegacyRouteResolver", records[0].Gate)
		}
		if records[0].Reason != largebody.StaticWireReasonLegacyResolverConfigured {
			t.Fatalf("reason = %v, want StaticWireReasonLegacyResolverConfigured", records[0].Reason)
		}
	})

	t.Run("gate 5 custom pre-admission route hook declines to canonical", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{}
		prof := &trackingCandidateProfile{}
		var records []frontendpipe.PreCaptureResult
		spec := newCandidateGatesSpec(exec, prof, &records)
		spec.ResolvePreAdmissionRoute = func(_ http.Header, _ frontendpipe.PathMatch) routing.ResolvedRoute {
			return routing.ExplicitRoute("resolved_route")
		}

		largeBody := `{"model":"test","prompt":"` + strings.Repeat("a", 2<<20) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(largeBody))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 OK", rec.Code)
		}
		if len(records) != 1 || records[0].Candidate {
			t.Fatalf("expected gate 5 decline, got: %v", records)
		}
		if records[0].Gate != frontendpipe.PreCaptureGateLegacyRouteResolver {
			t.Fatalf("gate = %v, want PreCaptureGateLegacyRouteResolver", records[0].Gate)
		}
		if records[0].Reason != largebody.StaticWireReasonLegacyResolverConfigured {
			t.Fatalf("reason = %v, want StaticWireReasonLegacyResolverConfigured", records[0].Reason)
		}
	})
}

func TestCheapPreCaptureGates_GateOrderEnforced(t *testing.T) {
	t.Parallel()

	// 1. Gate 1 vs Gate 2: Feature disabled + Content-Length below threshold => Gate 1 wins.
	t.Run("gate 1 precedes gate 2", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{}
		prof := &trackingCandidateProfile{}
		spec := newCandidateGatesSpec(exec, prof, nil)
		spec.LargePayload.Enabled = false

		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{"short":true}`))
		res := frontendpipe.EvaluatePreCaptureGates(&spec, req)

		if res.Gate != frontendpipe.PreCaptureGateFeatureProfileExecutor {
			t.Fatalf("gate = %v, want PreCaptureGateFeatureProfileExecutor", res.Gate)
		}
		if res.Reason != largebody.StaticWireReasonFeatureDisabled {
			t.Fatalf("reason = %v, want StaticWireReasonFeatureDisabled", res.Reason)
		}
	})

	// 2. Gate 2 does NOT trust compressed Content-Length:
	// If Content-Encoding is gzip and Content-Length < threshold, Gate 2 must NOT decline!
	// Instead, Gate 3 (gzip wave 1) must catch it and decline!
	t.Run("compressed Content-Length not trusted as decoded length: gate 3 catches gzip", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{}
		prof := &trackingCandidateProfile{}
		spec := newCandidateGatesSpec(exec, prof, nil)

		// 500 bytes body < 1 MiB threshold, but Content-Encoding: gzip
		compressedBody := bytes.Repeat([]byte{0x1f, 0x8b, 0x08}, 100)
		req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(compressedBody))
		req.Header.Set("Content-Encoding", "gzip")
		req.ContentLength = int64(len(compressedBody))

		res := frontendpipe.EvaluatePreCaptureGates(&spec, req)

		// Must NOT decline at Gate 2 (PreCaptureGateKnownLengthThreshold);
		// MUST decline at Gate 3 (PreCaptureGateGzipWave1) with StaticWireReasonGzipCompressed.
		if res.Gate != frontendpipe.PreCaptureGateGzipWave1 {
			t.Fatalf("gate = %v, want PreCaptureGateGzipWave1 (compressed Content-Length must not trigger gate 2)", res.Gate)
		}
		if res.Reason != largebody.StaticWireReasonGzipCompressed {
			t.Fatalf("reason = %v, want StaticWireReasonGzipCompressed", res.Reason)
		}
	})

	// 3. Gate 2 vs Gate 4: Uncompressed body below threshold + Static disposition blocked => Gate 2 wins.
	t.Run("gate 2 precedes gate 4", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{
			staticDisp:   largebody.StaticWireDefinitelyCanonical,
			staticReason: largebody.StaticWireReasonStaticBlocker,
		}
		prof := &trackingCandidateProfile{}
		spec := newCandidateGatesSpec(exec, prof, nil)

		// 200 bytes uncompressed body < 1 MiB threshold
		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{"short":true}`))
		res := frontendpipe.EvaluatePreCaptureGates(&spec, req)

		if res.Gate != frontendpipe.PreCaptureGateKnownLengthThreshold {
			t.Fatalf("gate = %v, want PreCaptureGateKnownLengthThreshold", res.Gate)
		}
		if res.Reason != largebody.StaticWireReasonBelowThreshold {
			t.Fatalf("reason = %v, want StaticWireReasonBelowThreshold", res.Reason)
		}
		// Prove Gate 4 was not even evaluated:
		if exec.staticCalls != 0 {
			t.Fatalf("static disposition evaluated after gate 2 declined: %d calls", exec.staticCalls)
		}
	})

	// 4. Gate 3 vs Gate 4: Gzip body (above threshold) + Static disposition blocked => Gate 3 wins.
	t.Run("gate 3 precedes gate 4", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{
			staticDisp:   largebody.StaticWireDefinitelyCanonical,
			staticReason: largebody.StaticWireReasonStaticBlocker,
		}
		prof := &trackingCandidateProfile{}
		spec := newCandidateGatesSpec(exec, prof, nil)

		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(strings.Repeat("x", 2<<20)))
		req.Header.Set("Content-Encoding", "gzip")
		res := frontendpipe.EvaluatePreCaptureGates(&spec, req)

		if res.Gate != frontendpipe.PreCaptureGateGzipWave1 {
			t.Fatalf("gate = %v, want PreCaptureGateGzipWave1", res.Gate)
		}
		if res.Reason != largebody.StaticWireReasonGzipCompressed {
			t.Fatalf("reason = %v, want StaticWireReasonGzipCompressed", res.Reason)
		}
		// Prove Gate 4 was not even evaluated:
		if exec.staticCalls != 0 {
			t.Fatalf("static disposition evaluated after gate 3 declined: %d calls", exec.staticCalls)
		}
	})

	// 5. Gate 4 vs Gate 5: Static disposition blocked + custom route hook present => Gate 4 wins.
	t.Run("gate 4 precedes gate 5", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{
			staticDisp:   largebody.StaticWireDefinitelyCanonical,
			staticReason: largebody.StaticWireReasonStaticBlocker,
		}
		prof := &trackingCandidateProfile{}
		spec := newCandidateGatesSpec(exec, prof, nil)
		spec.ResolveBodyRoute = func(_ []byte, _ frontendpipe.PathMatch) routing.ResolvedRoute {
			return routing.ExplicitRoute("custom")
		}

		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(strings.Repeat("x", 2<<20)))
		res := frontendpipe.EvaluatePreCaptureGates(&spec, req)

		if res.Gate != frontendpipe.PreCaptureGateStaticDisposition {
			t.Fatalf("gate = %v, want PreCaptureGateStaticDisposition", res.Gate)
		}
		if res.Reason != largebody.StaticWireReasonStaticBlocker {
			t.Fatalf("reason = %v, want StaticWireReasonStaticBlocker", res.Reason)
		}
	})

	// 6. Unknown/chunked length (Content-Length = -1) passes Gate 2:
	t.Run("chunked unknown length passes gate 2 and reaches subsequent gates", func(t *testing.T) {
		t.Parallel()
		exec := &candidateGatesExec{}
		prof := &trackingCandidateProfile{}
		spec := newCandidateGatesSpec(exec, prof, nil)

		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{"stream":true}`))
		req.ContentLength = -1 // chunked / unknown length

		res := frontendpipe.EvaluatePreCaptureGates(&spec, req)
		// Should pass all gates because ContentLength -1 is unknown (Requirement 2.3)
		if !res.Candidate {
			t.Fatalf("expected chunked body to pass pre-capture gates, got declined at %v: %v", res.Gate, res.Reason)
		}
		if res.Executor == nil {
			t.Fatal("expected non-nil LargeBodyExecutor on accepted candidate")
		}
	})
}
