package frontendpipe_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type recordingDiagnosticsObserver struct {
	mu        sync.Mutex
	stages    []largebody.PipelineStage
	declines  []string
	captures  []captureRecord
	replays   int
	rewrites  int
	durations map[string]time.Duration
	spool     []int64
}

type captureRecord struct {
	bucket  string
	storage largebody.StorageKind
}

func (r *recordingDiagnosticsObserver) OnPipelineStage(s largebody.PipelineStage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stages = append(r.stages, s)
}

func (r *recordingDiagnosticsObserver) OnDecline(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.declines = append(r.declines, reason)
}

func (r *recordingDiagnosticsObserver) OnCapture(sizeBucket string, storage largebody.StorageKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.captures = append(r.captures, captureRecord{bucket: sizeBucket, storage: storage})
}

func (r *recordingDiagnosticsObserver) OnReplay() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replays++
}

func (r *recordingDiagnosticsObserver) OnRewrite() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rewrites++
}

func (r *recordingDiagnosticsObserver) OnStageDuration(stage string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.durations == nil {
		r.durations = make(map[string]time.Duration)
	}
	r.durations[stage] = d
}

func (r *recordingDiagnosticsObserver) OnActiveSpoolBytes(seq uint64, b int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spool = append(r.spool, b)
}

func (r *recordingDiagnosticsObserver) hasStage(s largebody.PipelineStage) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(r.stages, s)
}

func (r *recordingDiagnosticsObserver) hasDecline(reason string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(r.declines, reason)
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatalf("gzip write failed: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close failed: %v", err)
	}
	return buf.Bytes()
}

func TestCandidateDiagnostics_StaticDeclineGates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		body        func(t *testing.T) []byte
		headers     map[string]string
		threshold   int64
		withProfile bool
		withExec    bool
		legacyRes   bool
		wantDecline string
	}{
		{
			name: "below_threshold",
			body: func(t *testing.T) []byte {
				t.Helper()
				return []byte(`{"prompt":"hi"}`)
			},
			threshold:   1024 * 1024,
			withProfile: true,
			withExec:    true,
			wantDecline: largebody.DeclineReasonBelowThreshold,
		},
		{
			name: "gzip_compressed",
			body: func(t *testing.T) []byte {
				t.Helper()
				return gzipBytes(t, []byte(`{"prompt":"hi"}`))
			},
			headers:     map[string]string{"Content-Encoding": "gzip"},
			threshold:   10,
			withProfile: true,
			withExec:    true,
			wantDecline: largebody.DeclineReasonGzipCompressed,
		},
		{
			name: "legacy_route_resolver",
			body: func(t *testing.T) []byte {
				t.Helper()
				return []byte(`{"prompt":"hi long payload"}`)
			},
			threshold:   5,
			withProfile: true,
			withExec:    true,
			legacyRes:   true,
			wantDecline: largebody.DeclineReasonFrontendRouteResolver,
		},
		{
			name: "missing_profile",
			body: func(t *testing.T) []byte {
				t.Helper()
				return []byte(`{"prompt":"hi"}`)
			},
			threshold:   10,
			withProfile: false,
			withExec:    true,
			wantDecline: largebody.DeclineReasonStaticBlocker,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			obs := &recordingDiagnosticsObserver{}
			exec := &testCandidateExec{}
			var prof frontendpipe.FrontendProfile
			if tc.withProfile {
				prof = testCandidateProfile{}
			}

			spec := &frontendpipe.Spec[struct{}]{
				Config: frontendpipe.Config{
					Exec: exec,
					LargePayload: frontendpipe.LargePayloadConfig{
						Enabled:        true,
						ThresholdBytes: tc.threshold,
						Diagnostics:    obs,
					},
				},
				Wire:    testWireErrors{},
				Profile: prof,
				MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
					return frontendpipe.PathMatch{}, true
				},
				Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
					return &frontendpipe.Decoded{
						Call: &aipapi.Call{ID: "call-1"},
					}, nil
				},
			}
			if tc.legacyRes {
				spec.ResolveBodyRoute = func(_ []byte, _ frontendpipe.PathMatch) routing.ResolvedRoute {
					return routing.ExplicitRoute("custom-route")
				}
			}

			reqData := tc.body(t)
			req := httptest.NewRequest(http.MethodPost, "/v1/test", bytes.NewReader(reqData))
			req.Header.Set("Content-Type", "application/json")
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()

			frontendpipe.ServeHTTP(spec, rec, req)

			if !obs.hasStage(largebody.StageConsidered) {
				t.Error("expected StageConsidered")
			}
			if !obs.hasStage(largebody.StageStaticCanonical) {
				t.Error("expected StageStaticCanonical")
			}
			if !obs.hasDecline(tc.wantDecline) {
				t.Errorf("expected decline %q, got declines: %v", tc.wantDecline, obs.declines)
			}
			if !obs.hasStage(largebody.StageCanonical) {
				t.Error("expected StageCanonical fallback")
			}
			if _, ok := obs.durations["pre_capture_gates"]; !ok {
				t.Error("expected pre_capture_gates duration measurement")
			}
		})
	}
}

func TestCandidateDiagnostics_CaptureStage(t *testing.T) {
	t.Parallel()

	obs := &recordingDiagnosticsObserver{}
	exec := &testCandidateExec{}
	prof := testCandidateProfile{
		compileFn: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
			return frontendpipe.ProofOutput{}, errors.New("proof decline")
		},
	}

	spec := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: exec,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:          true,
				ThresholdBytes:   50,
				MemorySpoolBytes: 1024 * 1024,
				Diagnostics:      obs,
			},
		},
		Wire:    testWireErrors{},
		Profile: prof,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, true
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			return &frontendpipe.Decoded{
				Call: &aipapi.Call{ID: "call-1"},
			}, nil
		},
	}

	body := []byte(`{"prompt":"hello world this is a large enough payload exceeding threshold"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec, req)

	if !obs.hasStage(largebody.StageConsidered) {
		t.Error("expected StageConsidered")
	}
	if !obs.hasStage(largebody.StageCaptured) {
		t.Error("expected StageCaptured")
	}
	if len(obs.captures) == 0 {
		t.Fatal("expected OnCapture emission")
	}
	if obs.captures[0].bucket != "lt_32k" {
		t.Errorf("expected bucket lt_32k, got %s", obs.captures[0].bucket)
	}
	if obs.captures[0].storage != largebody.StorageMemory {
		t.Errorf("expected storage memory, got %s", obs.captures[0].storage)
	}
	if _, ok := obs.durations["capture"]; !ok {
		t.Error("expected capture duration measurement")
	}
}

func TestCandidateDiagnostics_ProofDecline(t *testing.T) {
	t.Parallel()

	obs := &recordingDiagnosticsObserver{}
	exec := &testCandidateExec{}
	prof := testCandidateProfile{
		compileFn: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
			return frontendpipe.ProofOutput{}, errors.New("proof compilation failed")
		},
	}

	spec := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: exec,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 50,
				Diagnostics:    obs,
			},
		},
		Wire:    testWireErrors{},
		Profile: prof,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, true
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			return &frontendpipe.Decoded{
				Call: &aipapi.Call{ID: "call-1"},
			}, nil
		},
	}

	body := []byte(`{"prompt":"testing proof decline emission with sufficient length"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec, req)

	if !obs.hasDecline(largebody.DeclineReasonProofUncertain.String()) {
		t.Errorf("expected proof_uncertain decline, got: %v", obs.declines)
	}
	if !obs.hasStage(largebody.StageCanonical) {
		t.Error("expected StageCanonical fallback")
	}
	if _, ok := obs.durations["proof"]; !ok {
		t.Error("expected proof duration measurement")
	}
}

func TestCandidateDiagnostics_AssessmentDecline(t *testing.T) {
	t.Parallel()

	obs := &recordingDiagnosticsObserver{}
	exec := &testCandidateExec{
		assessFn: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
			return largebody.NewDeclinedAssessment(largebody.DeclineReasonRouteIncompatible)
		},
	}
	prof := minimalValidProofProfile()

	spec := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: exec,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 50,
				Diagnostics:    obs,
			},
		},
		Wire:    testWireErrors{},
		Profile: prof,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, true
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			return &frontendpipe.Decoded{
				Call: &aipapi.Call{ID: "call-1"},
			}, nil
		},
	}

	body := []byte(`{"prompt":"testing assessment decline with sufficient length for candidate"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec, req)

	if !obs.hasStage(largebody.StageProfileProven) {
		t.Error("expected StageProfileProven")
	}
	if !obs.hasDecline(largebody.DeclineReasonRouteIncompatible.String()) {
		t.Errorf("expected route_incompatible decline, got: %v", obs.declines)
	}
	if !obs.hasStage(largebody.StageCanonical) {
		t.Error("expected StageCanonical fallback")
	}
	if _, ok := obs.durations["assessment"]; !ok {
		t.Error("expected assessment duration measurement")
	}
}

func TestCandidateDiagnostics_AssessmentCanceledDecline(t *testing.T) {
	t.Parallel()

	obs := &recordingDiagnosticsObserver{}
	exec := &testCandidateExec{
		assessFn: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
			return largebody.Assessment{}, context.Canceled
		},
	}
	prof := minimalValidProofProfile()

	spec := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: exec,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 50,
				Diagnostics:    obs,
			},
		},
		Wire:    testWireErrors{},
		Profile: prof,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, true
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			return &frontendpipe.Decoded{
				Call: &aipapi.Call{ID: "call-1"},
			}, nil
		},
	}

	body := []byte(`{"prompt":"testing assessment cancellation decline with sufficient length"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec, req)

	if !obs.hasDecline(largebody.DeclineReasonCanceled.String()) {
		t.Errorf("expected canceled decline, got declines: %v", obs.declines)
	}
}

func TestCandidateDiagnostics_WireExecution(t *testing.T) {
	t.Parallel()

	obs := &recordingDiagnosticsObserver{}
	exec := &testCandidateExec{
		assessFn: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
			return makeAcceptedAssessment(proof)
		},
		executeFn: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			return largebody.ExecutionResult{}, nil
		},
	}
	prof := minimalValidProofProfile()

	spec := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: exec,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 50,
				Diagnostics:    obs,
			},
		},
		Wire:    testWireErrors{},
		Profile: prof,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, true
		},
	}

	body := []byte(`{"prompt":"wire execution test payload exceeding threshold bytes"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec, req)

	if !obs.hasStage(largebody.StageAssessmentEligible) {
		t.Error("expected StageAssessmentEligible")
	}
	if !obs.hasStage(largebody.StageWire) {
		t.Error("expected StageWire")
	}
	if obs.replays != 1 {
		t.Errorf("expected 1 replay, got %d", obs.replays)
	}
	if obs.rewrites != 1 {
		t.Errorf("expected 1 rewrite, got %d", obs.rewrites)
	}
	if _, ok := obs.durations["execution"]; !ok {
		t.Error("expected execution duration measurement")
	}
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

func TestCandidateDiagnostics_SummaryBlockers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		summary     func(t *testing.T) largebody.WireEligibilitySummary
		wantDecline string
	}{
		{
			name: "local_turn",
			summary: func(t *testing.T) largebody.WireEligibilitySummary {
				t.Helper()
				return compileSummaryWithPlane(t, "local_turn_handlers")
			},
			wantDecline: largebody.DeclineReasonLocalTurn,
		},
		{
			name: "secret_guard",
			summary: func(t *testing.T) largebody.WireEligibilitySummary {
				t.Helper()
				return compileSummaryWithPlane(t, "secret_guard_execution")
			},
			wantDecline: largebody.DeclineReasonSecretGuard,
		},
		{
			name: "terminal_decision",
			summary: func(t *testing.T) largebody.WireEligibilitySummary {
				t.Helper()
				return compileSummaryWithPlane(t, "terminal_decision_provider")
			},
			wantDecline: largebody.DeclineReasonTerminalDecision,
		},
		{
			name: "traffic",
			summary: func(t *testing.T) largebody.WireEligibilitySummary {
				t.Helper()
				return compileSummaryWithPort(t, largebody.NarrowPortEligibilityInput{TrafficCapturing: true})
			},
			wantDecline: largebody.DeclineReasonTraffic,
		},
		{
			name: "accounting_counting",
			summary: func(t *testing.T) largebody.WireEligibilitySummary {
				t.Helper()
				return compileSummaryWithPort(t, largebody.NarrowPortEligibilityInput{TokenCountingRequired: true})
			},
			wantDecline: largebody.DeclineReasonAccountingCounting,
		},
		{
			name: "custom_call_callback",
			summary: func(t *testing.T) largebody.WireEligibilitySummary {
				t.Helper()
				return compileSummaryWithPort(t, largebody.NarrowPortEligibilityInput{CustomCallCallbacksPresent: true})
			},
			wantDecline: largebody.DeclineReasonCustomCallCallback,
		},
		{
			name: "backend_domain",
			summary: func(t *testing.T) largebody.WireEligibilitySummary {
				t.Helper()
				return compileSummaryWithPort(t, largebody.NarrowPortEligibilityInput{BackendsEmpty: true})
			},
			wantDecline: largebody.DeclineReasonBackendDomain,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			obs := &recordingDiagnosticsObserver{}
			exec := &testCandidateExec{}
			prof := minimalValidProofProfile()

			spec := &frontendpipe.Spec[struct{}]{
				Config: frontendpipe.Config{
					Exec: exec,
					LargePayload: frontendpipe.LargePayloadConfig{
						Enabled:         true,
						ThresholdBytes:  10,
						WireEligibility: tc.summary(t),
						Diagnostics:     obs,
					},
				},
				Wire:    testWireErrors{},
				Profile: prof,
				MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
					return frontendpipe.PathMatch{}, true
				},
				Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
					return &frontendpipe.Decoded{
						Call: &aipapi.Call{ID: "call-1"},
					}, nil
				},
			}

			body := []byte(`{"prompt":"testing static blocker decline"}`)
			req := httptest.NewRequest(http.MethodPost, "/v1/test", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			frontendpipe.ServeHTTP(spec, rec, req)

			if !obs.hasStage(largebody.StageConsidered) {
				t.Error("expected StageConsidered")
			}
			if !obs.hasStage(largebody.StageStaticCanonical) {
				t.Error("expected StageStaticCanonical")
			}
			if !obs.hasDecline(tc.wantDecline) {
				t.Errorf("expected decline %q, got declines: %v", tc.wantDecline, obs.declines)
			}
			if !obs.hasStage(largebody.StageCanonical) {
				t.Error("expected StageCanonical fallback")
			}
		})
	}
}

type testWireErrors struct{}

func (testWireErrors) WriteBodyTooLarge(w http.ResponseWriter) error          { return nil }
func (testWireErrors) WriteReadBodyFailed(w http.ResponseWriter) error        { return nil }
func (testWireErrors) WriteExecutorNotConfigured(w http.ResponseWriter) error { return nil }
func (testWireErrors) WritePreflightCanceled(w http.ResponseWriter) error     { return nil }
func (testWireErrors) WriteInvalidJSON(w http.ResponseWriter) error           { return nil }
func (testWireErrors) WriteAdmissionReject(w http.ResponseWriter, d decodeqos.Decision) error {
	return nil
}
func (testWireErrors) WriteInvalidRequest(w http.ResponseWriter) error { return nil }
func (testWireErrors) WriteExecuteError(w http.ResponseWriter, out execerr.Outcome) error {
	return nil
}
func (testWireErrors) WriteEncodeFailed(w http.ResponseWriter) error { return nil }

type testExecView struct{}

func (testExecView) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	return nil, nil
}

func (testExecView) CancelALeg(ctx context.Context, req aipapi.ALegCancelRequest) error {
	return nil
}

func (testExecView) WallClock() func() time.Time {
	return nil
}

type testCandidateExec struct {
	testExecView
	assessFn  func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error)
	executeFn func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error)
}

func (e *testCandidateExec) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	if e.assessFn != nil {
		return e.assessFn(ctx, proof)
	}
	return largebody.Assessment{}, nil
}

func (e *testCandidateExec) ExecuteLargeBody(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
	if e.executeFn != nil {
		return e.executeFn(ctx, accepted, src)
	}
	return largebody.ExecutionResult{}, nil
}

var _ largebody.LargeBodyExecutor = (*testCandidateExec)(nil)

type testCandidateProfile struct {
	compileFn func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error)
}

func (testCandidateProfile) ProfileID() string { return "test_profile" }
func (p testCandidateProfile) CompileProof(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	if p.compileFn != nil {
		return p.compileFn(ctx, in)
	}
	return frontendpipe.ProofOutput{}, nil
}

var _ frontendpipe.FrontendProfile = (*testCandidateProfile)(nil)
