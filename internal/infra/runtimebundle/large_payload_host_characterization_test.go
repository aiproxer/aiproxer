package runtimebundle_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

type staticDispositionCheck interface {
	LargeBodyStaticDisposition(profileID string) (largebody.StaticWireDisposition, largebody.StaticWireReason)
}

func TestLargePayloadHost_StockHost_ExecutorRoutesCanonical(t *testing.T) {
	t.Parallel()

	basePath := runtimebundle.MaterializeExampleConfigForTest(
		t,
		filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
	)

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      basePath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	ex := hostActiveExecutor(t, host)
	if ex == nil {
		t.Fatal("expected non-nil executor from stock host")
	}

	// Characterization: The stock executor must satisfy LargeBodyExecutor (Phase 4 requirement).
	lbe, ok := largebody.AsLargeBodyExecutor(ex)
	if !ok || lbe == nil {
		t.Fatalf("stock executor must satisfy largebody.LargeBodyExecutor, got ok=%v", ok)
	}

	// Stock host has no wire backends / authority blockers -> static disposition is definitely canonical.
	if sdp, ok := any(ex).(staticDispositionCheck); ok {
		disp, reason := sdp.LargeBodyStaticDisposition("openai_chat_v1")
		if !disp.IsDefinitelyCanonical() {
			t.Fatalf("stock host without fast-path config must have definitely canonical disposition, got disp=%v reason=%v", disp, reason)
		}
	}

	// Proof evaluation on stock host must decline (zero spooling/allocation, canonical route).
	ctx := context.Background()
	proof := largebody.Proof{
		ProfileID: "openai_chat_v1",
		Operation: aipapi.OperationOpenAIChatCompletions,
		Delivery:  aipapi.DeliveryModeStreaming,
	}
	assessment, err := lbe.AssessLargeBody(ctx, proof)
	if err != nil {
		t.Fatalf("AssessLargeBody must return nil error (same-permit decline discipline), got: %v", err)
	}
	if assessment.Decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("stock host without eligible backend must decline assessment, got decision=%v", assessment.Decision)
	}

	// SpoolLedger and Diagnostics wiring verification (Task 19.1 unwired carry).
	if host.SpoolLedger() == nil {
		t.Fatal("expected non-nil SpoolLedger on host")
	}
	diag := host.LargePayloadDiagnostics()
	if diag == nil {
		t.Fatal("expected non-nil LargePayloadDiagnostics on host")
	}
}

// TestLargePayloadHost_StockHost_CandidatePrerequisites_TypeAssertionOnly characterizes that
// BuildHost's runtime.Executor satisfies largebody.LargeBodyExecutor for frontendpipe.CandidatePrerequisites.
//
// Phase 5 Census & Wiring Invariant: BuildHost links server.large_payload_fast_path into
// frontendpipe.LargePayloadConfig on all frontend Specs. In stock composition, standard census
// compilation (largebody.compilePortBlockers) occupies content authority ports lacking a non-Call
// wire alternative (such as RoutingRT.CapsResolver in build_executor.go). As a result, the production
// census correctly compiles HasStaticBlocker() == true, proving that stock hosts remain 100% canonical
// (zero spooling) even when fast-path is enabled. Candidate-path wire execution in an eligible census
// environment is characterized in TestLargePayload_EnabledConfig_PreCaptureGatesAndExecution.
func TestLargePayloadHost_StockHost_CandidatePrerequisites_TypeAssertionOnly(t *testing.T) {
	t.Parallel()

	basePath := runtimebundle.MaterializeExampleConfigForTest(
		t,
		filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
	)

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      basePath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	ex := hostActiveExecutor(t, host)

	// In Phase 4, CandidatePrerequisites evaluates AsLargeBodyExecutor.
	spec := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: ex,
		},
		Profile: stubFrontendProfile{id: "openai_chat_v1"},
	}
	prereqLbe, ok := frontendpipe.CandidatePrerequisites(spec)
	if !ok || prereqLbe == nil {
		t.Fatalf("CandidatePrerequisites must succeed with real runtime.Executor implementing LargeBodyExecutor, got ok=%v", ok)
	}
}

type stubFrontendProfile struct {
	id string
}

func (s stubFrontendProfile) ProfileID() string { return s.id }
func (s stubFrontendProfile) CompileProof(context.Context, frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	return frontendpipe.ProofOutput{}, nil
}

type charTestWireBackend struct {
	reqSupport    largebody.WireRequestSupport
	domainSupport largebody.WireDomainSupport
}

func (b charTestWireBackend) ResolveWireRequest(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
	return b.reqSupport
}

func (b charTestWireBackend) ResolveWireDomain(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
	return b.domainSupport
}

func newCharTestEligibleAssessor(t *testing.T, genID string) *coreruntime.ProductionLargeBodyAssessor {
	t.Helper()

	census := largebody.NewStandardDependencyCensus(genID)
	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    census.Planes,
		Hooks:                     census.Hooks,
		Ports:                     census.Ports,
		TwoPhaseExecutorAvailable: true,
	}, 4096)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}

	authGate := largebody.NewAuthorityAssessmentGate(summary, census, genID)

	wb := charTestWireBackend{
		reqSupport: largebody.WireRequestSupport{
			Compatible: true,
			Reason:     largebody.WireSupportReasonNone,
		},
		domainSupport: largebody.WireDomainSupport{
			Compatible:       true,
			AnyAcceptedModel: true,
			Reason:           largebody.WireSupportReasonNone,
		},
	}
	resolver := largebody.WireBackendMap{"test-backend": wb}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionInference, true
	})
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"test-backend",
		map[string]struct{}{"test-backend": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"test-backend",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		resolver,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(nil, validator, resolver)
	wireGate := largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, resolver)

	return coreruntime.NewProductionLargeBodyAssessor(
		genID,
		genID,
		authGate,
		wireGate,
		coreruntime.StandardLaneDomainPolicies(),
	)
}

func validCharTestProof(profileID string, op aipapi.Operation, delivery aipapi.DeliveryMode) largebody.Proof {
	return largebody.Proof{
		ProfileID:       profileID,
		Operation:       op,
		Delivery:        delivery,
		RouteSelector:   "gpt-4o",
		ClientModel:     "gpt-4o",
		MaxOutputTokens: 4096,
		Facts: largebody.ProtocolFacts{
			RequirementsID: "std-req-v1",
			ControlCount:   1,
		},
		Mode:      largebody.BodyModeIdentityJSON,
		Rewrite:   largebody.NewNoRewrite(),
		Identity:  largebody.NewIdentityDigest([32]byte{1, 2, 3}),
		Source:    largebody.NewSourceDigest([32]byte{4, 5, 6}),
		BodyBytes: 1024,
	}
}

// TestLargePayload_EnabledConfig_PreCaptureGatesAndExecution provides genuine
// enabled-config and eligible-generation candidate-path characterization.
//
// Note: BuildHost-level fast-path config linkage is established in Phase 5
// (see TestLargePayloadHost_EnabledConfig_LinkageToFrontendSpecs). This test
// characterizes candidate-path gate evaluation and wire execution on an assessor
// configured with an eligible dependency census.
func TestLargePayload_EnabledConfig_PreCaptureGatesAndExecution(t *testing.T) {
	t.Parallel()

	genID := "gen-eligible-cand"
	assessor := newCharTestEligibleAssessor(t, genID)

	ex := coreruntime.NewExecutor(coreruntime.ExecutorConfig{
		Core: coreruntime.CoreRuntime{
			LargeBodyAssessor:                  assessor,
			LargeBodyGenerationID:              genID,
			LargeBodyCandidateDomainGeneration: genID,
		},
	})

	spec := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: ex,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 1024,
			},
		},
		Profile: stubFrontendProfile{id: "openai_chat_v1"},
	}

	// 1. CandidatePrerequisites succeeds on real executor with LargeBodyExecutor capability.
	prereqLbe, ok := frontendpipe.CandidatePrerequisites(spec)
	if !ok || prereqLbe == nil {
		t.Fatalf("CandidatePrerequisites must succeed, got ok=%v lbe=%v", ok, prereqLbe)
	}

	// 2. EvaluatePreCaptureGates with uncompressed request >= threshold passes all gates to Eligible.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(bytes.Repeat([]byte("a"), 2048)))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = 2048

	gateRes := frontendpipe.EvaluatePreCaptureGates(spec, req)
	if !gateRes.Candidate {
		t.Fatalf("expected eligible candidate, got gate=%v reason=%v", gateRes.Gate, gateRes.Reason)
	}
	if gateRes.Gate != frontendpipe.PreCaptureGateNone {
		t.Fatalf("expected PreCaptureGateNone, got gate=%v", gateRes.Gate)
	}
	if gateRes.Reason != largebody.StaticWireReasonNone {
		t.Fatalf("expected StaticWireReasonNone, got reason=%v", gateRes.Reason)
	}

	// 3. Contrast: disabled config declines at Gate 1 (FeatureProfileExecutor).
	specDisabled := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: ex,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled: false,
			},
		},
		Profile: stubFrontendProfile{id: "openai_chat_v1"},
	}
	gateResDisabled := frontendpipe.EvaluatePreCaptureGates(specDisabled, req)
	if gateResDisabled.Candidate {
		t.Fatal("disabled config must not be eligible")
	}
	if gateResDisabled.Reason != largebody.StaticWireReasonFeatureDisabled {
		t.Fatalf("expected StaticWireReasonFeatureDisabled, got %v", gateResDisabled.Reason)
	}

	// 4. Contrast: below-threshold request declines at Gate 2 (KnownLengthThreshold).
	reqSmall := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte("small")))
	reqSmall.Header.Set("Content-Type", "application/json")
	reqSmall.ContentLength = 500
	gateResSmall := frontendpipe.EvaluatePreCaptureGates(spec, reqSmall)
	if gateResSmall.Candidate {
		t.Fatal("below-threshold request must not be eligible")
	}
	if gateResSmall.Reason != largebody.StaticWireReasonBelowThreshold {
		t.Fatalf("expected StaticWireReasonBelowThreshold, got %v", gateResSmall.Reason)
	}

	// 5. Contrast: gzip request declines at Gate 3 (GzipWave1).
	reqGzip := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte("gzip")))
	reqGzip.Header.Set("Content-Type", "application/json")
	reqGzip.Header.Set("Content-Encoding", "gzip")
	reqGzip.ContentLength = 2048
	gateResGzip := frontendpipe.EvaluatePreCaptureGates(spec, reqGzip)
	if gateResGzip.Candidate {
		t.Fatal("gzip request must not be eligible")
	}
	if gateResGzip.Reason != largebody.StaticWireReasonGzipCompressed {
		t.Fatalf("expected StaticWireReasonGzipCompressed, got %v", gateResGzip.Reason)
	}

	// 6. AssessLargeBody through real executor accepts valid streaming proof.
	proof := validCharTestProof("openai_chat_v1", aipapi.OperationOpenAIChatCompletions, aipapi.DeliveryModeStreaming)
	assessment, err := ex.AssessLargeBody(context.Background(), proof)
	if err != nil {
		t.Fatalf("AssessLargeBody: %v", err)
	}
	if assessment.Decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected Accept, got %v (reason: %v)", assessment.Decision, assessment.Reason)
	}
	if assessment.Stamp.IsZero() {
		t.Fatal("expected non-zero stamp on accepted assessment")
	}
	if assessment.Stamp.GenerationID() != genID {
		t.Fatalf("stamp generation ID mismatch: got %q want %q", assessment.Stamp.GenerationID(), genID)
	}

	// 7. Wire execution enforcement: declined assessment is rejected at the commit barrier.
	declined, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonRouteIncompatible)
	_, err = ex.ExecuteLargeBody(context.Background(), declined, nil)
	if err == nil {
		t.Fatal("ExecuteLargeBody must fail closed for non-accepted assessment")
	}
}

func TestLargePayloadHost_EnabledConfig_LinkageToFrontendSpecs(t *testing.T) {
	t.Parallel()

	basePath := runtimebundle.MaterializeExampleConfigForTest(
		t,
		filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
	)
	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	spoolDir := t.TempDir()
	// Inject large_payload_fast_path config and openresponses frontend
	customYAML := strings.Replace(
		string(raw),
		"server:\n  address: \"127.0.0.1:18080\"",
		fmt.Sprintf("server:\n  address: \"127.0.0.1:18080\"\n  large_payload_fast_path:\n    enabled: true\n    threshold_bytes: 4096\n    memory_spool_bytes: 32768\n    max_inflight_spool_bytes: 1048576\n    max_semantic_fact_bytes: 16384\n    spool_dir: %q", spoolDir),
		1,
	)
	customYAML = strings.Replace(
		customYAML,
		"    - id: gemini\n      enabled: true\n      config: {}",
		"    - id: gemini\n      enabled: true\n      config: {}\n    - id: openresponses\n      enabled: true\n      config: {}",
		1,
	)

	cfgPath := filepath.Join(t.TempDir(), "enabled-fast-path.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var capturedInput httpcontract.StandardHTTPInput
	interceptComposer := func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
		capturedInput = in
		return stdhttp.ComposeStandardHTTP(ctx, cfg, log, in)
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: interceptComposer,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	// 1. Verify that StandardHTTPInput received LargePayloadConfig with enabled settings and process bindings
	lpCfg := capturedInput.Frontends.LargePayload
	if !lpCfg.Enabled {
		t.Fatal("expected LargePayloadConfig.Enabled to be true")
	}
	if lpCfg.ThresholdBytes != 4096 {
		t.Fatalf("expected ThresholdBytes == 4096, got %d", lpCfg.ThresholdBytes)
	}
	if lpCfg.MemorySpoolBytes != 32768 {
		t.Fatalf("expected MemorySpoolBytes == 32768, got %d", lpCfg.MemorySpoolBytes)
	}
	if lpCfg.SpoolDir != spoolDir {
		t.Fatalf("expected SpoolDir == %q, got %q", spoolDir, lpCfg.SpoolDir)
	}
	if lpCfg.SpoolLedger == nil || lpCfg.SpoolLedger != host.SpoolLedger() {
		t.Fatalf("expected SpoolLedger to match host.SpoolLedger(), got %v vs %v", lpCfg.SpoolLedger, host.SpoolLedger())
	}
	if lpCfg.Diagnostics == nil || lpCfg.Diagnostics != host.LargePayloadDiagnostics() {
		t.Fatalf("expected Diagnostics to match host.LargePayloadDiagnostics(), got %v vs %v", lpCfg.Diagnostics, host.LargePayloadDiagnostics())
	}

	// 2. Mount bundled frontends to inspect handlers and specs
	testMux := http.NewServeMux()
	if err := stdhttp.MountBundledFrontends(stdhttp.MountBundledFrontendsInput{
		Mux:       testMux,
		Frontends: capturedInput.Frontends,
	}); err != nil {
		t.Fatalf("MountBundledFrontends: %v", err)
	}

	// OpenAI Responses handler
	hResp, _ := testMux.Handler(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	respHandler, ok := hResp.(*openairesponses.Handler)
	if !ok {
		t.Fatalf("expected *openairesponses.Handler at /v1/responses, got %T", hResp)
	}
	specResp := respHandler.Spec()
	if !specResp.LargePayload.Enabled {
		t.Error("expected openairesponses Spec.Config.LargePayload.Enabled == true")
	}
	if specResp.LargePayload.ThresholdBytes != 4096 {
		t.Errorf("expected openairesponses ThresholdBytes == 4096, got %d", specResp.LargePayload.ThresholdBytes)
	}
	if specResp.LargePayload.MemorySpoolBytes != 32768 {
		t.Errorf("expected openairesponses MemorySpoolBytes == 32768, got %d", specResp.LargePayload.MemorySpoolBytes)
	}
	if specResp.LargePayload.SpoolLedger != host.SpoolLedger() {
		t.Errorf("expected openairesponses SpoolLedger to match host.SpoolLedger()")
	}

	// OpenAI Legacy handler
	hLegacy, _ := testMux.Handler(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	legacyHandler, ok := hLegacy.(*openailegacy.Handler)
	if !ok {
		t.Fatalf("expected *openailegacy.Handler at /v1/chat/completions, got %T", hLegacy)
	}
	specLegacy := legacyHandler.Spec()
	if !specLegacy.LargePayload.Enabled {
		t.Error("expected openailegacy Spec.Config.LargePayload.Enabled == true")
	}
	if specLegacy.LargePayload.ThresholdBytes != 4096 {
		t.Errorf("expected openailegacy ThresholdBytes == 4096, got %d", specLegacy.LargePayload.ThresholdBytes)
	}

	// OpenResponses handler
	hOpenResp, _ := testMux.Handler(httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", nil))
	openRespHandler, ok := hOpenResp.(*openresponses.Handler)
	if !ok {
		t.Fatalf("expected *openresponses.Handler at /responses, got %T", hOpenResp)
	}
	specOpenResp := openRespHandler.Spec()
	if !specOpenResp.LargePayload.Enabled {
		t.Error("expected openresponses Spec.Config.LargePayload.Enabled == true")
	}
	if specOpenResp.LargePayload.ThresholdBytes != 4096 {
		t.Errorf("expected openresponses ThresholdBytes == 4096, got %d", specOpenResp.LargePayload.ThresholdBytes)
	}
}

func TestLargePayloadHost_DisabledConfig_LeavesFrontendSpecsDisabled(t *testing.T) {
	t.Parallel()

	basePath := runtimebundle.MaterializeExampleConfigForTest(
		t,
		filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
	)
	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	customYAML := strings.Replace(
		string(raw),
		"    - id: gemini\n      enabled: true\n      config: {}",
		"    - id: gemini\n      enabled: true\n      config: {}\n    - id: openresponses\n      enabled: true\n      config: {}",
		1,
	)
	cfgPath := filepath.Join(t.TempDir(), "disabled-fast-path.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var capturedInput httpcontract.StandardHTTPInput
	interceptComposer := func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
		capturedInput = in
		return stdhttp.ComposeStandardHTTP(ctx, cfg, log, in)
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: interceptComposer,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	// Captured LargePayload has Enabled == false
	if capturedInput.Frontends.LargePayload.Enabled {
		t.Fatal("expected LargePayload.Enabled to be false when config disabled")
	}

	testMux := http.NewServeMux()
	if err := stdhttp.MountBundledFrontends(stdhttp.MountBundledFrontendsInput{
		Mux:       testMux,
		Frontends: capturedInput.Frontends,
	}); err != nil {
		t.Fatalf("MountBundledFrontends: %v", err)
	}

	hResp, _ := testMux.Handler(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	if respHandler, ok := hResp.(*openairesponses.Handler); ok {
		if respHandler.Spec().LargePayload.Enabled {
			t.Error("expected openairesponses spec.Config.LargePayload.Enabled == false")
		}
	}
	hLegacy, _ := testMux.Handler(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if legacyHandler, ok := hLegacy.(*openailegacy.Handler); ok {
		if legacyHandler.Spec().LargePayload.Enabled {
			t.Error("expected openailegacy spec.Config.LargePayload.Enabled == false")
		}
	}
	hOpenResp, _ := testMux.Handler(httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", nil))
	if openRespHandler, ok := hOpenResp.(*openresponses.Handler); ok {
		if openRespHandler.Spec().LargePayload.Enabled {
			t.Error("expected openresponses spec.Config.LargePayload.Enabled == false")
		}
	}
}
