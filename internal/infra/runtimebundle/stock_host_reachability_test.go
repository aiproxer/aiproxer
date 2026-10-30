package runtimebundle_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	ssessionapp "github.com/aiproxer/aiproxer/internal/core/securesession/app"
	securedomain "github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestStockHost_StockComposedCensus_Accept verifies that a stock-composed census
// (where conversation store, conversation reader, compaction detector, and session recorder
// are present from standard featurehost construction, with clean baseline / no mutating planes)
// is accepted by the authority gate rather than unconditionally declining.
func TestStockHost_StockComposedCensus_Accept(t *testing.T) {
	t.Parallel()

	genID := "gen-stock-test"
	census := largebody.NewStandardDependencyCensus(genID)

	// In stock composition, featurehost always instantiates conversation store and compaction detector.
	// Under PR-2 Item 3, when no mutating planes (local_turn_handlers, compaction_observers,
	// compaction_preservers, terminal_decision_provider, interleaved) are active, these stock
	// ports operate under a safe wire contract (identity no-op / idle request side).
	// We also register security.session_recorder (Item 5).
	census.AddPort("security.session_recorder", true)

	// Simulate stock-composed ports:
	// - ConversationViewReader: present but clean baseline (no local turn handlers)
	// - ConversationViewTagger: present but idle (no local turn handlers)
	// - SteeringWriterFactory: present but idle (no continuation / interleaved)
	// - CompactionDetector: present but request-idle (no compaction observers/preservers)
	census.Ports.BackendsEmpty = false
	census.Ports.ConversationViewReaderOccupied = false // after Item 3 refinement
	census.Ports.ConversationViewTaggerOccupied = false // after Item 3 refinement
	census.Ports.SteeringWriterFactoryOccupied = false  // after Item 3 refinement
	census.Ports.CompactionDetectorOccupied = false     // after Item 3 refinement

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
	decision, reason := authGate.Evaluate()
	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("stock-composed census with clean baseline must be accepted, got decision=%v reason=%v", decision, reason)
	}
}

// TestStockHost_BuildHost_WithFastPath_StockComposed_Reachability verifies that
// BuildHost with server.large_payload_fast_path enabled on a clean stock configuration
// compiles an authority gate that evaluates to Accept (wire reachable).
func TestStockHost_BuildHost_WithFastPath_StockComposed_Reachability(t *testing.T) {
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
	// Replace server configuration to enable large_payload_fast_path
	customYAML := strings.Replace(
		string(raw),
		"server:\n  address: \"127.0.0.1:18080\"",
		fmt.Sprintf("server:\n  address: \"127.0.0.1:18080\"\n  large_payload_fast_path:\n    enabled: true\n    threshold_bytes: 4096\n    memory_spool_bytes: 32768\n    max_inflight_spool_bytes: 1048576\n    max_semantic_fact_bytes: 16384\n    spool_dir: %q", spoolDir),
		1,
	)

	// Disable test-only hook noop features so the baseline has no hook blockers
	customYAML = strings.ReplaceAll(customYAML, "id: submit-noop\n      enabled: true", "id: submit-noop\n      enabled: false")
	customYAML = strings.ReplaceAll(customYAML, "id: parts-noop\n      enabled: true", "id: parts-noop\n      enabled: false")
	customYAML = strings.ReplaceAll(customYAML, "id: tool-reactor-noop\n      enabled: true", "id: tool-reactor-noop\n      enabled: false")
	customYAML = strings.ReplaceAll(customYAML, "id: tool-call-repair\n      enabled: true", "id: tool-call-repair\n      enabled: false")

	cfgPath := filepath.Join(t.TempDir(), "stock-wire-reachability.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	ex := hostActiveExecutor(t, host)
	pa, ok := ex.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	if !ok || pa == nil {
		t.Fatalf("expected *coreruntime.ProductionLargeBodyAssessor, got %T", ex.LargeBodyAssessor)
	}

	ports := pa.AuthorityGate.Census.Ports
	if !ports.ConversationViewReaderOccupied {
		t.Errorf("expected ConversationViewReaderOccupied to be true when reader is present")
	}
	if !ports.ConversationReaderFreshALegSupported {
		t.Errorf("expected ConversationReaderFreshALegSupported to be true for certified fresh A-leg reader")
	}
	if ports.ConversationViewTaggerOccupied {
		t.Errorf("ConversationViewTaggerOccupied must be false on clean stock host without local_turn_handlers")
	}
	if ports.SteeringWriterFactoryOccupied {
		t.Errorf("SteeringWriterFactoryOccupied must be false on clean stock host without interleaved/continuation")
	}
	if !ports.CompactionDetectorOccupied {
		t.Errorf("expected CompactionDetectorOccupied to be true when compaction detector is present")
	}
	if !ports.CompactionDetectorWireSupported {
		t.Errorf("expected CompactionDetectorWireSupported to be true for wire-capable detector")
	}
	if ports.ExposureAdmissionOccupied {
		t.Errorf("ExposureAdmissionOccupied must be false on stock host")
	}
	if ports.PreflightEnabled {
		t.Errorf("PreflightEnabled must be false on stock host")
	}
	if ports.StreamUsageOccupied {
		t.Errorf("StreamUsageOccupied must be false on stock host")
	}
	if ports.AdminCountServiceOccupied {
		t.Errorf("AdminCountServiceOccupied must be false on stock host")
	}
	if !ports.CapsResolverOccupied {
		t.Errorf("expected CapsResolverOccupied to remain true for stock capabilities map resolver")
	}
	if !ports.CapsResolverWireProofSubsumed {
		t.Errorf("expected CapsResolverWireProofSubsumed to be true when subsumed by BackendWireProofGate")
	}

	if pa.AuthorityGate.Summary.HasStaticBlocker() {
		t.Errorf("expected AuthorityGate.Summary.HasStaticBlocker() to be false on certified baseline, got true")
	}
	if pb := pa.AuthorityGate.Summary.PlaneBlockers(); pb != 0 {
		t.Errorf("expected 0 plane blockers, got %d", pb)
	}
	if hb := pa.AuthorityGate.Summary.HookBlockers(); hb != 0 {
		t.Errorf("expected 0 hook blockers, got %d", hb)
	}
	if portB := pa.AuthorityGate.Summary.PortBlockers(); portB != 0 {
		t.Errorf("expected 0 port blockers, got %d", portB)
	}

	decision, reason := pa.AuthorityGate.Evaluate()
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("AuthorityGate.Evaluate() on stock host with subsumed/certified baseline must accept, got decision=%v reason=%v", decision, reason)
	}
}

// TestStockHost_BuildHost_WithFastPath_RealE2EReachability verifies that
// a production BuildHost with server.large_payload_fast_path enabled, certified frontend
// profile (openai_chat_v1), and wire-capable backend routes streaming requests >= threshold
// through OpenWire (OpenWire == 1, canonical Open == 0) for a fresh session, and declines
// to canonical (canonical Open == 1, OpenWire == 0) for a resumed session.
func TestStockHost_BuildHost_WithFastPath_RealE2EReachability(t *testing.T) {
	t.Parallel()

	var (
		upstreamCalls      atomic.Int32
		openWireCalls      atomic.Int32
		openCanonicalCalls atomic.Int32
		lastUpstreamBody   atomic.Pointer[string]
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			s := string(b)
			lastUpstreamBody.Store(&s)
		}
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-e2e\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"reachability-wire-ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	customYAML := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"

model_aliases:
  - pattern: "^alias-chat$"
    replacement: "wire-backend:gpt-4o"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-legacy-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "stock-wire-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfgPathOff := filepath.Join(t.TempDir(), "stock-wire-e2e-off.yaml")
	if err := os.WriteFile(cfgPathOff, []byte(strings.Replace(customYAML, "enabled: true", "enabled: false", 1)), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	buildHostWithConfig := func(cpath string) *runtimebundle.Host {
		h, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
			ConfigPath:      cpath,
			Mandatory:       aipsdk.StandardDistributionRequirements(),
			LogWriter:       io.Discard,
			HandlerComposer: stdhttp.ComposeStandardHTTP,
			RegistrySetup: func(reg *pluginreg.Registry) error {
				return reg.WrapLifecycleBackend("custom-openai-legacy-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
					return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
						res, err := orig(instanceID, n, upstreamHTTP, deps)
						if err != nil {
							return pluginreg.BackendBuildResult{}, err
						}
						origOpenWire := res.Backend.OpenWire
						if origOpenWire != nil {
							res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
								openWireCalls.Add(1)
								return origOpenWire(ctx, req)
							}
						}
						origOpen := res.Backend.Open
						if origOpen != nil {
							res.Backend.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
								openCanonicalCalls.Add(1)
								return origOpen(ctx, call, cand)
							}
						}
						return res, nil
					}
				})
			},
		})
		if err != nil {
			t.Fatalf("BuildHost: %v", err)
		}
		hostServeCleanup(t, h)
		return h
	}

	host := buildHostWithConfig(cfgPath)
	hostOff := buildHostWithConfig(cfgPathOff)

	padding := strings.Repeat("x", 1500)
	bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hello %s"}]}`, padding)

	respIDRe := regexp.MustCompile(`"(id)":\s*"chatcmpl[-_][0-9a-zA-Z_-]+"`)
	tsRe := regexp.MustCompile(`"(created)":\s*[0-9]+`)
	normalizeBody := func(s string) string {
		s = respIDRe.ReplaceAllString(s, `"$1":"chatcmpl-NORMALIZED"`)
		s = tsRe.ReplaceAllString(s, `"$1":0`)
		return s
	}

	// Case 1: Fresh session streaming request -> wire accepted, OpenWire == 1, canonical Open == 0
	{
		wireBefore := openWireCalls.Load()
		canonBefore := openCanonicalCalls.Load()

		reqOn := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
		reqOn.Header.Set("Content-Type", "application/json")
		recOn := httptest.NewRecorder()

		host.HTTPHandler().ServeHTTP(recOn, reqOn)

		if recOn.Code != http.StatusOK {
			t.Fatalf("fresh wire request failed with HTTP %d: %s", recOn.Code, recOn.Body.String())
		}
		require.Contains(t, recOn.Body.String(), "reachability-wire-ok")
		require.Contains(t, recOn.Body.String(), "data: [DONE]")
		require.NotContains(t, recOn.Body.String(), `{"status":"ok"}`)
		require.Equal(t, "text/event-stream; charset=utf-8", recOn.Header().Get("Content-Type"))
		require.Equal(t, wireBefore+1, openWireCalls.Load(), "expected OpenWire == 1")
		require.Equal(t, canonBefore, openCanonicalCalls.Load(), "expected canonical Open == 0")

		reqOff := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
		reqOff.Header.Set("Content-Type", "application/json")
		recOff := httptest.NewRecorder()
		hostOff.HTTPHandler().ServeHTTP(recOff, reqOff)

		require.Equal(t, wireBefore+1, openWireCalls.Load(), "hostOff must not increment OpenWire")
		require.Equal(t, canonBefore+1, openCanonicalCalls.Load(), "hostOff must increment canonical Open")
		require.Contains(t, recOff.Body.String(), "reachability-wire-ok")
		require.Contains(t, recOff.Body.String(), "data: [DONE]")
		require.Equal(t, normalizeBody(recOff.Body.String()), normalizeBody(recOn.Body.String()), "plain response must match canonical oracle")
	}

	// Case 1b: Fresh session with options (temperature, top_p, max_tokens) -> wire accepted and matches canonical oracle
	{
		wireBefore := openWireCalls.Load()
		canonBefore := openCanonicalCalls.Load()

		bodyJSONOpts := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"temperature":0.2,"top_p":0.8,"max_tokens":256,"messages":[{"role":"user","content":"hello %s"}]}`, padding)
		reqOn := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSONOpts))
		reqOn.Header.Set("Content-Type", "application/json")
		recOn := httptest.NewRecorder()

		host.HTTPHandler().ServeHTTP(recOn, reqOn)

		if recOn.Code != http.StatusOK {
			t.Fatalf("options wire request failed with HTTP %d: %s", recOn.Code, recOn.Body.String())
		}
		require.Equal(t, wireBefore+1, openWireCalls.Load(), "options request must invoke wire fast-path")
		require.Equal(t, canonBefore, openCanonicalCalls.Load(), "options wire request must not invoke canonical Open")
		require.Contains(t, recOn.Body.String(), "reachability-wire-ok")
		require.Contains(t, recOn.Body.String(), "data: [DONE]")

		reqOff := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSONOpts))
		reqOff.Header.Set("Content-Type", "application/json")
		recOff := httptest.NewRecorder()
		hostOff.HTTPHandler().ServeHTTP(recOff, reqOff)

		require.Equal(t, wireBefore+1, openWireCalls.Load(), "hostOff must not increment OpenWire")
		require.Equal(t, canonBefore+1, openCanonicalCalls.Load(), "hostOff must increment canonical Open")
		require.Contains(t, recOff.Body.String(), "reachability-wire-ok")
		require.Contains(t, recOff.Body.String(), "data: [DONE]")
		require.Equal(t, normalizeBody(recOff.Body.String()), normalizeBody(recOn.Body.String()), "options response must match canonical oracle")
	}

	// Case 1c: Model rewrite / route alias (client selector != backend native)
	{
		wireBefore := openWireCalls.Load()
		canonBefore := openCanonicalCalls.Load()

		clientModel := "alias-chat"
		bodyJSONAlias := fmt.Sprintf(`{"model":"%s","stream":true,"messages":[{"role":"user","content":"hello %s"}]}`, clientModel, padding)
		reqOn := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSONAlias))
		reqOn.Header.Set("Content-Type", "application/json")
		recOn := httptest.NewRecorder()

		host.HTTPHandler().ServeHTTP(recOn, reqOn)

		if recOn.Code != http.StatusOK {
			t.Fatalf("alias wire request failed with HTTP %d: %s", recOn.Code, recOn.Body.String())
		}
		require.Equal(t, wireBefore+1, openWireCalls.Load(), "alias request must invoke wire fast-path")
		require.Equal(t, canonBefore, openCanonicalCalls.Load(), "alias wire request must not invoke canonical Open")
		require.NotNil(t, lastUpstreamBody.Load(), "upstream must receive request body")
		require.Contains(t, *lastUpstreamBody.Load(), `"model":"gpt-4o"`, "backend body model must be rewritten to candidate model")
		require.NotContains(t, *lastUpstreamBody.Load(), clientModel, "backend body must not contain original client alias")
		require.Contains(t, recOn.Body.String(), `"model":"alias-chat"`, "downstream response must echo exact client model")
		require.NotContains(t, recOn.Body.String(), `"model":"gpt-4o"`, "downstream response must not leak backend native model")
		require.Contains(t, recOn.Body.String(), "reachability-wire-ok")
		require.Contains(t, recOn.Body.String(), "data: [DONE]")

		reqOff := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSONAlias))
		reqOff.Header.Set("Content-Type", "application/json")
		recOff := httptest.NewRecorder()
		hostOff.HTTPHandler().ServeHTTP(recOff, reqOff)

		require.Equal(t, wireBefore+1, openWireCalls.Load(), "hostOff must not increment OpenWire")
		require.Equal(t, canonBefore+1, openCanonicalCalls.Load(), "hostOff must increment canonical Open")
		require.NotNil(t, lastUpstreamBody.Load(), "upstream must receive request body on hostOff")
		require.Contains(t, *lastUpstreamBody.Load(), `"model":"gpt-4o"`, "canonical backend body model must be rewritten to candidate model")
		require.NotContains(t, *lastUpstreamBody.Load(), clientModel, "canonical backend body must not contain original client alias")
		require.Contains(t, recOff.Body.String(), `"model":"alias-chat"`, "canonical downstream response must echo exact client model")
		require.NotContains(t, recOff.Body.String(), `"model":"gpt-4o"`, "canonical downstream response must not leak backend native model")
		require.Contains(t, recOff.Body.String(), "reachability-wire-ok")
		require.Contains(t, recOff.Body.String(), "data: [DONE]")
		require.Equal(t, normalizeBody(recOff.Body.String()), normalizeBody(recOn.Body.String()), "model alias response must match canonical oracle with exact client model")
	}

	// Case 2: Resumed session request -> pre-commit decline to canonical, OpenWire remains 1, canonical Open increments
	{
		// Seed a real session via a small below-threshold canonical request
		initReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"init"}]}`))
		initReq.Header.Set("Content-Type", "application/json")
		initRec := httptest.NewRecorder()
		host.HTTPHandler().ServeHTTP(initRec, initReq)
		if initRec.Code != http.StatusOK {
			t.Fatalf("session init failed with HTTP %d: %s", initRec.Code, initRec.Body.String())
		}
		resumeTok := initRec.Header().Get("X-AIP-Resume-Token")
		sessionID := initRec.Header().Get("X-AIP-Session-ID")
		if resumeTok == "" {
			t.Fatalf("expected non-empty X-AIP-Resume-Token from canonical init, got headers: %v", initRec.Header())
		}

		wireBefore := openWireCalls.Load()
		canonBefore := openCanonicalCalls.Load()

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-AIP-Resume-Token", resumeTok)
		if sessionID != "" {
			req.Header.Set("X-AIP-Session-ID", sessionID)
		}
		rec := httptest.NewRecorder()

		host.HTTPHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("resumed fallback request failed with HTTP %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "reachability-wire-ok") {
			t.Fatalf("expected streamed response containing reachability-wire-ok, got: %s", rec.Body.String())
		}
		if got := openWireCalls.Load(); got != wireBefore {
			t.Fatalf("expected OpenWire to remain %d, got %d", wireBefore, got)
		}
		if got := openCanonicalCalls.Load(); got != canonBefore+1 {
			t.Fatalf("expected canonical Open to increment by 1 (got %d, want %d)", got, canonBefore+1)
		}
	}
}

// TestStockHost_O1_ParallelToolCalls_DeclinesToCanonicalAndStrips verifies that o1 and o3 requests
// requesting parallel_tool_calls: true decline the wire fast-path (because wire cannot strip
// options from raw spooled bytes) and execute canonically, where ApplyNegotiatedDowngrades
// soft-strips parallel_tool_calls (call.Options.ParallelToolCalls == nil).
func TestStockHost_O1_ParallelToolCalls_DeclinesToCanonicalAndStrips(t *testing.T) {
	t.Parallel()

	var (
		openWireCalls         atomic.Int32
		openCanonicalCalls    atomic.Int32
		capturedCanonicalCall atomic.Pointer[aipapi.Call]
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"},{"id":"o1"},{"id":"o3-mini"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-o\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"o-ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	customYAML := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-backend:o1"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-legacy-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "stock-wire-o1.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-openai-legacy-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					res, err := orig(instanceID, n, upstreamHTTP, deps)
					if err != nil {
						return pluginreg.BackendBuildResult{}, err
					}
					origOpenWire := res.Backend.OpenWire
					if origOpenWire != nil {
						res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
							openWireCalls.Add(1)
							return origOpenWire(ctx, req)
						}
					}
					origOpen := res.Backend.Open
					if origOpen != nil {
						res.Backend.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							openCanonicalCalls.Add(1)
							callCopy := call
							capturedCanonicalCall.Store(&callCopy)
							return origOpen(ctx, call, cand)
						}
					}
					return res, nil
				}
			})
		},
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	padding := strings.Repeat("x", 1500)
	modelsToTest := []string{"o1", "o3-mini"}

	for _, modelID := range modelsToTest {
		wireBefore := openWireCalls.Load()
		canonBefore := openCanonicalCalls.Load()

		bodyJSON := fmt.Sprintf(`{"model":%q,"stream":true,"parallel_tool_calls":true,"messages":[{"role":"user","content":"hello %s"}]}`, modelID, padding)

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-AIP-Route", "wire-backend:"+modelID)
		rec := httptest.NewRecorder()

		host.HTTPHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s request failed with HTTP %d: %s", modelID, rec.Code, rec.Body.String())
		}
		require.Equal(t, wireBefore, openWireCalls.Load(), "wire fast-path must decline for %s with parallel_tool_calls", modelID)
		require.Equal(t, canonBefore+1, openCanonicalCalls.Load(), "canonical Open must be called for %s", modelID)

		captured := capturedCanonicalCall.Load()
		require.NotNil(t, captured, "canonical call must be captured for %s", modelID)
		require.Nil(t, captured.Options.ParallelToolCalls, "parallel_tool_calls must be stripped by ApplyNegotiatedDowngrades for %s", modelID)
	}
}

// TestStockHost_MissingToolsCap_DeclinesWireAndCanonicalHardRejects verifies that when a backend
// lacks CapabilityTools, a streaming request with tools declines the wire fast-path and canonical
// execution rejects with a capability hard error.
func TestStockHost_MissingToolsCap_DeclinesWireAndCanonicalHardRejects(t *testing.T) {
	t.Parallel()

	var (
		openWireCalls      atomic.Int32
		openCanonicalCalls atomic.Int32
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-tool\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	customYAML := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-legacy-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "stock-wire-notools.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-openai-legacy-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					res, err := orig(instanceID, n, upstreamHTTP, deps)
					if err != nil {
						return pluginreg.BackendBuildResult{}, err
					}
					// Narrow backend caps: supports streaming only, lacks tools
					streamingOnly := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
					res.Backend.Caps = streamingOnly
					res.Backend.ResolveCaps = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
						return streamingOnly
					}
					origOpenWire := res.Backend.OpenWire
					if origOpenWire != nil {
						res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
							openWireCalls.Add(1)
							return origOpenWire(ctx, req)
						}
					}
					origOpen := res.Backend.Open
					if origOpen != nil {
						res.Backend.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							openCanonicalCalls.Add(1)
							return origOpen(ctx, call, cand)
						}
					}
					return res, nil
				}
			})
		},
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	padding := strings.Repeat("x", 1500)
	bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"tools":[{"type":"function","function":{"name":"test_fn","description":"test","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"hello %s"}]}`, padding)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	host.HTTPHandler().ServeHTTP(rec, req)

	require.Equal(t, int32(0), openWireCalls.Load(), "wire fast-path must decline when backend lacks CapabilityTools")
	require.Equal(t, int32(0), openCanonicalCalls.Load(), "canonical Open must not proceed due to hard-cap reject")
	require.NotEqual(t, http.StatusOK, rec.Code, "request must fail with capability rejection")
}

// TestStockHost_DynamicUnprobedModel_NotInInitialInventory_DeclinesToCanonical verifies that
// a model not present in the initial inventory snapshot at host build time declines wire
// execution (due to WireSupportReasonModelUnsupported) and executes canonically, delivering
// the actual provider response body.
func TestStockHost_DynamicUnprobedModel_NotInInitialInventory_DeclinesToCanonical(t *testing.T) {
	t.Parallel()

	var (
		openWireCalls      atomic.Int32
		openCanonicalCalls atomic.Int32
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-dyn\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"reachability-dyn-ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	customYAML := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-legacy-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
        models:
          source: inline
          items:
            - canonical_id: gpt-4o
              native_id: gpt-4o
  features:
    - id: tool-call-repair
      enabled: false
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "stock-wire-dyn.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-openai-legacy-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					res, err := orig(instanceID, n, upstreamHTTP, deps)
					if err != nil {
						return pluginreg.BackendBuildResult{}, err
					}
					origOpenWire := res.Backend.OpenWire
					if origOpenWire != nil {
						res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
							openWireCalls.Add(1)
							return origOpenWire(ctx, req)
						}
					}
					origOpen := res.Backend.Open
					if origOpen != nil {
						res.Backend.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							openCanonicalCalls.Add(1)
							return origOpen(ctx, call, cand)
						}
					}
					res.Backend.ResolveWireDomain = func(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
						return largebody.WireDomainSupport{
							Compatible:       true,
							AnyAcceptedModel: true,
							Reason:           largebody.WireSupportReasonNone,
						}
					}
					return res, nil
				}
			})
		},
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	padding := strings.Repeat("x", 1500)

	// Step 1: Request with probed inventory model ("gpt-4o") -> accepted by wire fast-path
	{
		bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hello %s"}]}`, padding)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		host.HTTPHandler().ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, int32(1), openWireCalls.Load(), "initial inventory model must be accepted by wire")
		require.Equal(t, int32(0), openCanonicalCalls.Load(), "canonical Open must not be called")
	}

	// Step 2: Request with unprobed model NOT in initial inventory ("gpt-4o-new") -> declines wire to canonical
	{
		bodyJSON := fmt.Sprintf(`{"model":"gpt-4o-new","stream":true,"messages":[{"role":"user","content":"hello %s"}]}`, padding)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-AIP-Route", "wire-backend:gpt-4o-new")
		rec := httptest.NewRecorder()

		host.HTTPHandler().ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), "reachability-dyn-ok")
		require.Equal(t, int32(1), openWireCalls.Load(), "unprobed model must decline wire fast-path (openWireCalls stays 1)")
		require.Equal(t, int32(1), openCanonicalCalls.Load(), "unprobed model must route canonically (openCanonicalCalls becomes 1)")
	}
}

// TestStockHost_RuntimeCapabilityChanges_NoStaleBit_ToolsStreamingOnlyAndStockWire verifies:
//  1. gpt4ostockwire: standard gpt-4o streaming request reaches wire fast-path.
//  2. toolsstreamingonly: backend configured with streaming and tools capabilities only reaches wire fast-path for requests with tools.
//  3. Runtime capability change after host build (no stale bit): dynamic change in capability resolution
//     between host build and request dynamically declines or accepts without stale build-time bits.
func TestStockHost_RuntimeCapabilityChanges_NoStaleBit_ToolsStreamingOnlyAndStockWire(t *testing.T) {
	t.Parallel()

	var (
		openWireCalls      atomic.Int32
		openCanonicalCalls atomic.Int32
		currentCaps        atomic.Pointer[aipapi.BackendCaps]
	)

	// Initial runtime capabilities: streaming and tools only
	initialCaps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	currentCaps.Store(&initialCaps)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-dyn-cap\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"dyn-cap-ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	customYAML := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-legacy-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "stock-wire-runcaps.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-openai-legacy-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					res, err := orig(instanceID, n, upstreamHTTP, deps)
					if err != nil {
						return pluginreg.BackendBuildResult{}, err
					}
					// Dynamic caps resolver reflecting currentCaps
					res.Backend.Caps = initialCaps
					res.Backend.ResolveWireCaps = func(ctx context.Context, cand routing.AttemptCandidate) aipapi.BackendCaps {
						if p := currentCaps.Load(); p != nil {
							return *p
						}
						return aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
					}
					res.Backend.ResolveCaps = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
						if p := currentCaps.Load(); p != nil {
							return *p
						}
						return aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
					}
					origResolveWire := res.Backend.ResolveWireRequest
					res.Backend.ResolveWireRequest = func(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
						caps := *currentCaps.Load()
						if len(facts.RequiredCapabilities) > 0 {
							if res := aipapi.Negotiate(facts.RequiredCapabilities, caps); res.Kind != aipapi.NegotiationLossless {
								return largebody.WireRequestSupport{
									Compatible: false,
									Reason:     largebody.WireSupportReasonCapabilityUnsupported,
								}
							}
						}
						if origResolveWire != nil {
							return origResolveWire(ctx, facts, cand)
						}
						return largebody.WireRequestSupport{Compatible: true}
					}
					res.Backend.ResolveWireDomain = func(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
						caps := *currentCaps.Load()
						if len(facts.RequiredCapabilities) > 0 {
							if res := aipapi.Negotiate(facts.RequiredCapabilities, caps); res.Kind != aipapi.NegotiationLossless {
								return largebody.WireDomainSupport{
									Compatible:       false,
									AnyAcceptedModel: false,
									Reason:           largebody.WireSupportReasonCapabilityUnsupported,
								}
							}
						}
						return largebody.WireDomainSupport{
							Compatible:       true,
							AnyAcceptedModel: true,
							Reason:           largebody.WireSupportReasonNone,
						}
					}
					origOpenWire := res.Backend.OpenWire
					if origOpenWire != nil {
						res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
							openWireCalls.Add(1)
							return origOpenWire(ctx, req)
						}
					}
					origOpen := res.Backend.Open
					if origOpen != nil {
						res.Backend.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							openCanonicalCalls.Add(1)
							return origOpen(ctx, call, cand)
						}
					}
					return res, nil
				}
			})
		},
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	padding := strings.Repeat("x", 1500)

	// Step 1 (gpt4ostockwire): plain streaming request for gpt-4o reaches wire fast-path
	{
		bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hello %s"}]}`, padding)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		host.HTTPHandler().ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, int32(1), openWireCalls.Load(), "stock gpt-4o request must reach wire fast-path")
		require.Equal(t, int32(0), openCanonicalCalls.Load())
	}

	// Step 2 (toolsstreamingonly): backend has streaming and tools only; streaming request with tools reaches wire fast-path
	{
		bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"tools":[{"type":"function","function":{"name":"test_fn","description":"test","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"hello %s"}]}`, padding)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		host.HTTPHandler().ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, int32(2), openWireCalls.Load(), "request with tools and streaming must reach wire when backend has tools+streaming")
		require.Equal(t, int32(0), openCanonicalCalls.Load())
	}

	// Step 3 (runtime capability change after host build, no stale bit):
	// Backend dynamically drops CapabilityTools at runtime. The same request with tools must now decline wire to canonical.
	{
		streamingOnly := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
		currentCaps.Store(&streamingOnly)

		bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"tools":[{"type":"function","function":{"name":"test_fn","description":"test","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"hello %s"}]}`, padding)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		host.HTTPHandler().ServeHTTP(rec, req)

		require.Equal(t, int32(2), openWireCalls.Load(), "wire fast-path must decline after dynamic capability drop (no stale bit)")
		// Request proceeds to canonical path where capability check rejects
		require.NotEqual(t, http.StatusOK, rec.Code, "canonical path rejects missing CapabilityTools")
	}
}

// TestStockHost_Audit_PortsDeclineWhenActive verifies the audited ports
// (ExposureAdmission, Preflight, StreamUsage, AdminCountService, etc.)
// decline wire execution when they are actively occupied/configured.
func TestStockHost_Audit_PortsDeclineWhenActive(t *testing.T) {
	t.Parallel()

	genID := "gen-audit-test"

	cases := []struct {
		name     string
		mutate   func(*largebody.DependencyCensus)
		wantDecl largebody.AssessmentDecision
		wantReas largebody.DeclineReason
	}{
		{
			name: "ExposureAdmissionOccupied blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.ExposureAdmissionOccupied = true
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "PreflightEnabled without exact counter blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.PreflightEnabled = true
				c.Ports.PreflightHasExactCounter = false
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "StreamUsageOccupied blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.StreamUsageOccupied = true
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "AdminCountServiceOccupied blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.AdminCountServiceOccupied = true
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "CompactionObservers plane occupied blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				for i := range c.Planes {
					if c.Planes[i].ID == "compaction_observers" {
						c.Planes[i].Occupied = true
					}
				}
				c.Ports.CompactionDetectorOccupied = true
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "LocalTurnHandlers plane occupied blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				for i := range c.Planes {
					if c.Planes[i].ID == "local_turn_handlers" {
						c.Planes[i].Occupied = true
					}
				}
				c.Ports.ConversationViewTaggerOccupied = true
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "Arbitrary ConversationReader (not stock) blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.ConversationViewReaderOccupied = true
				c.Ports.ConversationReaderFreshALegSupported = false
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "Arbitrary CapsResolver (not stock) blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.CapsResolverOccupied = true
				c.Ports.CapsResolverWireProofSubsumed = false
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "CompactionPreservers plane occupied blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				idx, ok := largebody.WireEligibilityPlaneIndex("compaction_preservers")
				if ok {
					c.Planes[idx].Occupied = true
				}
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			census := largebody.NewStandardDependencyCensus(genID)
			tc.mutate(&census)

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
			decision, reason := authGate.Evaluate()
			if decision != tc.wantDecl {
				t.Fatalf("decision mismatch: got %v want %v", decision, tc.wantDecl)
			}
			if reason != tc.wantReas {
				t.Fatalf("reason mismatch: got %v want %v", reason, tc.wantReas)
			}
		})
	}
}

// TestStockHost_HistoricalFallback_PersistedNeverBackendAndSteering proves that on a
// resumed session with an actual persisted NeverBackend tag and steering overlay:
// 1. ProductionLargeBodyAssessor precommit evaluation declines wire fast path (!ProvesFreshALeg()).
// 2. OpenWire calls == 0 (wire is never invoked).
// 3. Request proceeds in canonical execution under held decode permit.
// 4. Canonical snapshotAndProject reads fresh snapshot from conversation store once.
// 5. Tagged message is removed from the projected call forwarded to backend Open.
// 6. Steering overlay is applied to the projected call forwarded to backend Open.
// 7. Canonical Open is called exactly once with the modified call.
// 8. Client receives 200 OK streamed response.
func TestStockHost_HistoricalFallback_PersistedNeverBackendAndSteering(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-hist\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hist-fallback-ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	customYAML := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-legacy-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "stock-hist-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var openWireCalls atomic.Int32
	var openCanonicalCalls atomic.Int32
	var capturedCall atomic.Pointer[aipapi.Call]

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-openai-legacy-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					res, err := orig(instanceID, n, upstreamHTTP, deps)
					if err != nil {
						return pluginreg.BackendBuildResult{}, err
					}
					origOpenWire := res.Backend.OpenWire
					if origOpenWire != nil {
						res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
							openWireCalls.Add(1)
							return origOpenWire(ctx, req)
						}
					}
					origOpen := res.Backend.Open
					if origOpen != nil {
						res.Backend.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							openCanonicalCalls.Add(1)
							clone := call
							capturedCall.Store(&clone)
							return origOpen(ctx, call, cand)
						}
					}
					return res, nil
				}
			})
		},
	})
	require.NoError(t, err)
	hostServeCleanup(t, host)

	// Obtain authoritative conversation store via active executor tagger port
	ex := hostActiveExecutor(t, host)
	convStore, ok := conversationview.AsStore(ex.ConversationViewTagger)
	require.True(t, ok, "expected ConversationViewTagger to provide conversationview.Store")
	require.NotNil(t, convStore)

	// Step 1: Initialize session via fresh large payload turn (> 1024 bytes threshold)
	// Because this is a fresh turn with clean baseline and certified stock origin, wire reachability succeeds.
	initPadding := strings.Repeat("x", 1500)
	initBody := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"init %s"}]}`, initPadding)
	initReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(initBody))
	initReq.Header.Set("Content-Type", "application/json")
	initRec := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(initRec, initReq)
	require.Equal(t, http.StatusOK, initRec.Code)
	require.Equal(t, int32(1), openWireCalls.Load(), "fresh turn must reach backend via wire streaming")
	require.Equal(t, int32(0), openCanonicalCalls.Load(), "canonical open must not be called for fresh wire stream")

	resumeTok := initRec.Header().Get("X-AIP-Resume-Token")
	sessionID := initRec.Header().Get("X-AIP-Session-ID")
	aLegID := initRec.Header().Get("X-AIP-A-Leg-ID")
	require.NotEmpty(t, resumeTok, "expected non-empty resume token")
	require.NotEmpty(t, aLegID, "expected non-empty a-leg id")

	// Step 2: Persist NeverBackend tag for a specific user message using authoritative aLegID
	msgToDrop := aipapi.Message{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart("never-backend-tagged-part")},
	}
	tagID, err := conversationview.MessageIdentityOf(msgToDrop)
	require.NoError(t, err)

	_, err = convStore.TagNeverBackend(context.Background(), aLegID, []conversationview.TagRequest{
		{Identity: tagID, Reason: "test-historical-tag"},
	})
	require.NoError(t, err)

	// Step 3: Persist Steering overlay
	steeringText := "historical-steering-overlay-content"
	_, err = convStore.PutSteering(context.Background(), aLegID, conversationview.PutSteeringRequest{
		OverlayID: "hist-steering-1",
		Message: conversationview.StoredMessageV1{
			Role: aipapi.RoleSystem,
			Text: steeringText,
		},
		Placement: conversationview.StoredPlacement{
			Kind: conversationview.PlacementStablePrefix,
		},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "test-historical-steering",
	})
	require.NoError(t, err)

	// Step 4: Send large payload request with resume token and session ID
	// Request contains both the tagged message to be dropped and a large user message (> 1024 bytes threshold)
	padding := strings.Repeat("z", 1500)
	bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"never-backend-tagged-part"},{"role":"user","content":"main-content %s"}]}`, padding)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AIP-Resume-Token", resumeTok)
	req.Header.Set("X-AIP-Session-ID", sessionID)
	rec := httptest.NewRecorder()

	wireBefore := openWireCalls.Load()
	canonBefore := openCanonicalCalls.Load()

	host.HTTPHandler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, aLegID, rec.Header().Get("X-Aip-A-Leg-ID"), "resumed turn must retain the same A-leg identity")
	// Wire calls must NOT increment (openWireCalls remains 1)
	require.Equal(t, wireBefore, openWireCalls.Load(), "openWireCalls must not increment for resumed historical request")
	// Canonical Open must increment by exactly 1 (0 -> 1)
	require.Equal(t, canonBefore+1, openCanonicalCalls.Load(), "canonical open must increment by 1")

	// Step 5: Verify captured canonical call to backend:
	// - Tagged message MUST be removed by canonical projection
	// - Steering overlay MUST be applied as leading system message
	captured := capturedCall.Load()
	require.NotNil(t, captured)

	for _, msg := range captured.Messages {
		for _, part := range msg.Parts {
			if strings.Contains(part.Text, "never-backend-tagged-part") {
				t.Fatalf("projected backend call still contains never-backend tagged turn: %q", part.Text)
			}
		}
	}

	hasSteering := false
	for _, msgList := range [][]aipapi.Message{captured.Instructions, captured.Messages} {
		for _, msg := range msgList {
			for _, part := range msg.Parts {
				if strings.Contains(part.Text, steeringText) {
					hasSteering = true
					break
				}
			}
		}
	}
	require.True(t, hasSteering, "projected backend call must contain applied steering overlay in instructions or messages")
}

type testProducerLTHandler struct {
	id          string
	ord         int
	matchTag    string
	replyText   string
	matchCalls  atomic.Int64
	handleCalls atomic.Int64
}

func (h *testProducerLTHandler) ID() string                        { return h.id }
func (h *testProducerLTHandler) Order() int                        { return h.ord }
func (h *testProducerLTHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (h *testProducerLTHandler) Match(_ context.Context, call aipapi.Call, meta localturn.Meta) (localturn.MatchResult, error) {
	h.matchCalls.Add(1)
	for i, m := range call.Messages {
		for _, p := range m.Parts {
			if strings.Contains(p.Text, h.matchTag) {
				return localturn.MatchResult{Claimed: true, Indexes: []int{i}, Reason: "test-producer-tag"}, nil
			}
		}
	}
	return localturn.MatchResult{Claimed: false}, nil
}

func (h *testProducerLTHandler) Handle(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
	h.handleCalls.Add(1)
	return localturn.Reply{Text: h.replyText}, nil
}

// TestStockHost_HistoricalFallback_ProducerTransition_ReloadAndResume proves that:
//  1. In Generation 1, an active feature producer (PlaneLocalTurnHandlers) drives local turn tagging.
//  2. A reload to Generation 2 disables the producer feature.
//  3. A resumed request (> 1024 bytes threshold) declines wire precommit under the SAME permit identity
//     (X-Aip-A-Leg-ID), executes canonically (Open1 / OpenWire0), and verifies the tagged message was removed
//     and steering overlay applied.
//  4. A subsequent fresh supported request on Generation 2 takes the wire fast-path (OpenWire1 / OpenCanonical0).
func TestStockHost_HistoricalFallback_ProducerTransition_ReloadAndResume(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-prod\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"upstream-ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	customYAML := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-legacy-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
    - id: test-local-turn-producer
      enabled: true
      config: {}
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "stock-producer-trans-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var openWireCalls atomic.Int32
	var openCanonicalCalls atomic.Int32
	var capturedCall atomic.Pointer[aipapi.Call]

	ltHandler := &testProducerLTHandler{
		id:        "producer-lt-1",
		ord:       10,
		matchTag:  "producer-tagged-part",
		replyText: "producer-turn-reply",
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			if err := reg.RegisterFeature("test-local-turn-producer", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
				return testkit.FeatureBundle(t, "test-local-turn-producer", func(cs *aipfeature.ContributionSet) error {
					return aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "test-local-turn-producer", []localturn.Handler{ltHandler})
				}, nil), nil
			}); err != nil {
				return err
			}

			return reg.WrapLifecycleBackend("custom-openai-legacy-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					res, err := orig(instanceID, n, upstreamHTTP, deps)
					if err != nil {
						return pluginreg.BackendBuildResult{}, err
					}
					origOpenWire := res.Backend.OpenWire
					if origOpenWire != nil {
						res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
							openWireCalls.Add(1)
							return origOpenWire(ctx, req)
						}
					}
					origOpen := res.Backend.Open
					if origOpen != nil {
						res.Backend.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							openCanonicalCalls.Add(1)
							clone := call
							capturedCall.Store(&clone)
							return origOpen(ctx, call, cand)
						}
					}
					return res, nil
				}
			})
		},
	})
	require.NoError(t, err)
	hostServeCleanup(t, host)

	// Verify Generation 1: PlaneLocalTurnHandlers and ConversationViewTagger are occupied in authority gate
	ex1 := hostActiveExecutor(t, host)
	pa1, ok := ex1.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	require.True(t, ok)
	require.True(t, pa1.AuthorityGate.Census.Ports.ConversationViewTaggerOccupied, "Generation 1 must have ConversationViewTaggerOccupied = true")
	var g1LocalTurnOccupied bool
	for _, p := range pa1.AuthorityGate.Census.Planes {
		if p.ID == aipfeature.PlaneLocalTurnHandlers.ID {
			g1LocalTurnOccupied = p.Occupied
			break
		}
	}
	require.True(t, g1LocalTurnOccupied, "Generation 1 must have PlaneLocalTurnHandlers occupied")

	// Step 1 (Generation 1): Send request claimed and tagged by local turn handler
	gen1Body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"producer-tagged-part"}]}`
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(gen1Body))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(rec1, req1)
	require.Equal(t, http.StatusOK, rec1.Code)
	require.Contains(t, rec1.Body.String(), "producer-turn-reply")
	require.Equal(t, int64(1), ltHandler.handleCalls.Load(), "producer local turn handler must be invoked")
	require.Equal(t, int32(0), openWireCalls.Load(), "local turn handler short-circuits wire")
	require.Equal(t, int32(0), openCanonicalCalls.Load(), "local turn handler short-circuits backend open")

	resumeTok := rec1.Header().Get("X-AIP-Resume-Token")
	sessionID := rec1.Header().Get("X-AIP-Session-ID")
	aLegID := rec1.Header().Get("X-Aip-A-Leg-ID")
	require.NotEmpty(t, resumeTok, "expected non-empty resume token")
	require.NotEmpty(t, sessionID, "expected non-empty session ID")
	require.NotEmpty(t, aLegID, "expected non-empty A-leg ID")

	// Also attach a steering overlay to this session to verify overlay preservation
	convStore, ok := conversationview.AsStore(ex1.ConversationViewTagger)
	require.True(t, ok)
	steeringText := "producer-transition-steering-overlay"
	_, err = convStore.PutSteering(context.Background(), aLegID, conversationview.PutSteeringRequest{
		OverlayID: "prod-steering-1",
		Message: conversationview.StoredMessageV1{
			Role: aipapi.RoleSystem,
			Text: steeringText,
		},
		Placement: conversationview.StoredPlacement{
			Kind: conversationview.PlacementStablePrefix,
		},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "test-producer-steering",
	})
	require.NoError(t, err)

	// Step 2: Reload to Generation 2 disabling the producer feature
	tmpPath := filepath.Join(t.TempDir(), "gen2.yaml")
	gen2YAML := strings.Replace(customYAML, "id: test-local-turn-producer\n      enabled: true", "id: test-local-turn-producer\n      enabled: false", 1)
	require.NoError(t, os.WriteFile(tmpPath, []byte(gen2YAML), 0o600))
	require.NoError(t, replaceTestFile(tmpPath, cfgPath))

	res := host.Reload(t.Context(), sdkreload.Trigger{
		Kind:       sdkreload.TriggerAPI,
		AcceptedAt: time.Now().UTC(),
		SafeActor:  "test-producer-transition",
	})
	require.Equal(t, sdkreload.ResultPublished, res.Category, "reload must publish Generation 2: %s", res.ReasonCategory)

	// Verify Generation 2: PlaneLocalTurnHandlers and ConversationViewTagger are now unoccupied
	ex2 := hostActiveExecutor(t, host)
	pa2, ok := ex2.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	require.True(t, ok)
	require.False(t, pa2.AuthorityGate.Census.Ports.ConversationViewTaggerOccupied, "Generation 2 must have ConversationViewTaggerOccupied = false")
	var g2LocalTurnOccupied bool
	for _, p := range pa2.AuthorityGate.Census.Planes {
		if p.ID == aipfeature.PlaneLocalTurnHandlers.ID {
			g2LocalTurnOccupied = p.Occupied
			break
		}
	}
	require.False(t, g2LocalTurnOccupied, "Generation 2 must have PlaneLocalTurnHandlers unoccupied")

	// Step 3 (Generation 2): Resumed request exceeding threshold (> 1024 bytes)
	// Must decline wire precommit under the SAME permit identity (X-Aip-A-Leg-ID),
	// canonical execution must open backend exactly once (Open1 / OpenWire0),
	// and the message tagged by the producer in Gen 1 must be removed and steering applied.
	padding := strings.Repeat("z", 1500)
	resumedBody := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"producer-tagged-part"},{"role":"user","content":"resumed-payload %s"}]}`, padding)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(resumedBody))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-AIP-Resume-Token", resumeTok)
	req2.Header.Set("X-AIP-Session-ID", sessionID)
	rec2 := httptest.NewRecorder()

	host.HTTPHandler().ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Equal(t, aLegID, rec2.Header().Get("X-Aip-A-Leg-ID"), "resumed turn must retain the same A-leg identity")
	require.Equal(t, int32(0), openWireCalls.Load(), "wire must not be called for resumed request (OpenWire0)")
	require.Equal(t, int32(1), openCanonicalCalls.Load(), "canonical open must be called exactly once (Open1)")

	captured := capturedCall.Load()
	require.NotNil(t, captured)
	for _, msg := range captured.Messages {
		for _, part := range msg.Parts {
			if strings.Contains(part.Text, "producer-tagged-part") {
				t.Fatalf("canonical call still contains producer-tagged turn: %q", part.Text)
			}
		}
	}
	hasSteering := false
	for _, msgList := range [][]aipapi.Message{captured.Instructions, captured.Messages} {
		for _, msg := range msgList {
			for _, part := range msg.Parts {
				if strings.Contains(part.Text, steeringText) {
					hasSteering = true
					break
				}
			}
		}
	}
	require.True(t, hasSteering, "canonical call must contain applied steering overlay")

	// Step 4 (Generation 2): Fresh supported request on the same host exceeds threshold (> 1024 bytes)
	// Because producer is now disabled and this turn proves a fresh A-leg, wire fast-path must succeed.
	freshBody := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"fresh-gen2-payload %s"}]}`, padding)
	req3 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(freshBody))
	req3.Header.Set("Content-Type", "application/json")
	rec3 := httptest.NewRecorder()

	host.HTTPHandler().ServeHTTP(rec3, req3)
	require.Equal(t, http.StatusOK, rec3.Code)
	require.Equal(t, int32(1), openWireCalls.Load(), "fresh request on generation 2 must reach wire streaming (OpenWire1)")
	require.Equal(t, int32(1), openCanonicalCalls.Load(), "canonical open must not increment for fresh wire request")
}

// TestStockHost_CustomOverriddenConversationReader_Decline verifies that if a non-stock
// ConversationReader is configured without stock origin certification, the authority gate
// evaluates ConversationReaderFreshALegSupported as false, triggering a static blocker
// that declines wire execution.
func TestStockHost_CustomOverriddenConversationReader_Decline(t *testing.T) {
	t.Parallel()

	genID := "gen-custom-reader-test"
	census := largebody.NewStandardDependencyCensus(genID)
	census.AddPort("security.session_recorder", true)
	census.Ports.BackendsEmpty = false
	census.Ports.ConversationViewReaderOccupied = true
	// Custom overridden reader does NOT certify stock origin
	census.Ports.ConversationReaderFreshALegSupported = false

	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    census.Planes,
		Hooks:                     census.Hooks,
		Ports:                     census.Ports,
		TwoPhaseExecutorAvailable: true,
	}, 4096)
	require.NoError(t, err)

	authGate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	decision, reason := authGate.Evaluate()
	require.Equal(t, largebody.AssessmentDecisionDecline, decision)
	require.Equal(t, largebody.DeclineReasonAuthorityBlocker, reason)
}

// TestStockHost_FastPath_ReloadContinuity verifies that when a stock host with fast-path enabled
// undergoes a configuration reload, stock origin certification is preserved across generations,
// authority gate continues to evaluate Accept on the new generation, and fresh requests reach wire streaming.
func TestStockHost_FastPath_ReloadContinuity(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-reload\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"reload-ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	customYAML := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-legacy-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "stock-reload-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var openWireCalls atomic.Int32
	var openCanonicalCalls atomic.Int32

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-openai-legacy-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					res, err := orig(instanceID, n, upstreamHTTP, deps)
					if err != nil {
						return pluginreg.BackendBuildResult{}, err
					}
					origOpenWire := res.Backend.OpenWire
					if origOpenWire != nil {
						res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
							openWireCalls.Add(1)
							return origOpenWire(ctx, req)
						}
					}
					origOpen := res.Backend.Open
					if origOpen != nil {
						res.Backend.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							openCanonicalCalls.Add(1)
							return origOpen(ctx, call, cand)
						}
					}
					return res, nil
				}
			})
		},
	})
	require.NoError(t, err)
	hostServeCleanup(t, host)

	// Pre-reload verification: executor has ConversationReaderFreshALegSupported == true
	ex1 := hostActiveExecutor(t, host)
	pa1, ok := ex1.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	require.True(t, ok)
	require.True(t, pa1.AuthorityGate.Census.Ports.ConversationReaderFreshALegSupported)

	// Send fresh large payload before reload
	padding := strings.Repeat("x", 1500)
	bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"turn-1 %s"}]}`, padding)
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(rec1, req1)
	require.Equal(t, http.StatusOK, rec1.Code)
	require.Equal(t, int32(1), openWireCalls.Load(), "pre-reload request must reach wire streaming")
	require.Equal(t, int32(0), openCanonicalCalls.Load())

	// Mutate config to trigger a genuine published reload via atomic replacement
	tmpPath := filepath.Join(t.TempDir(), "reloaded.yaml")
	reloadedYAML := strings.Replace(customYAML, "max_attempts: 3", "max_attempts: 4", 1)
	require.NoError(t, os.WriteFile(tmpPath, []byte(reloadedYAML), 0o600))
	require.NoError(t, replaceTestFile(tmpPath, cfgPath))

	// Trigger Host.Reload
	res := host.Reload(t.Context(), sdkreload.Trigger{
		Kind:       sdkreload.TriggerAPI,
		AcceptedAt: time.Now().UTC(),
		SafeActor:  "test-reload-continuity",
	})
	require.Equal(t, sdkreload.ResultPublished, res.Category, "reload must publish new generation: %s", res.ReasonCategory)

	// Post-reload verification: new generation executor still retains ConversationReaderFreshALegSupported
	ex2 := hostActiveExecutor(t, host)
	pa2, ok := ex2.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	require.True(t, ok)
	require.True(t, pa2.AuthorityGate.Census.Ports.ConversationReaderFreshALegSupported)

	// Send fresh large payload after reload on new active generation
	bodyJSON2 := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"turn-2 %s"}]}`, padding)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON2))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Equal(t, int32(2), openWireCalls.Load(), "post-reload request must also reach wire streaming")
	require.Equal(t, int32(0), openCanonicalCalls.Load())

	// Reject mixed candidates that would replace the process-owned spool limit.
	blocked := strings.NewReplacer("max_inflight_spool_bytes: 1048576", "max_inflight_spool_bytes: 524288", "max_attempts: 4", "max_attempts: 5").Replace(reloadedYAML)
	require.NoError(t, os.WriteFile(tmpPath, []byte(blocked), 0o600))
	require.NoError(t, replaceTestFile(tmpPath, cfgPath))
	rejected := host.Reload(t.Context(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	require.Equal(t, sdkreload.ResultRestartRequired, rejected.Category)
	require.Contains(t, rejected.RestartFields, "server.large_payload_fast_path.max_inflight_spool_bytes")
	require.Equal(t, res.ActiveGeneration, host.ActiveGenerationID(), "mixed candidate must not publish")
	require.Same(t, ex2, hostActiveExecutor(t, host), "last-good executor must remain active")
}

type forgedReaderForTest struct{}

func (f *forgedReaderForTest) Snapshot(ctx context.Context, aLegID string) (conversationprojection.Snapshot, error) {
	return conversationprojection.Snapshot{}, nil
}

// TestStockHost_ForgedConversationReaderStockOrigin_OverwrittenAndDeclined verifies that if a caller
// injects a custom ConversationReader while asserting ConversationReaderStockOrigin: true, the candidate
// composition boundary detects the discrepancy against standard features, overwrites ConversationReaderStockOrigin
// to false, and declines wire execution.
func TestStockHost_ForgedConversationReaderStockOrigin_OverwrittenAndDeclined(t *testing.T) {
	t.Parallel()

	forged := &forgedReaderForTest{}

	// Case 1: Nil sf - custom reader asserting stock origin is declined
	resolvedReader, stockOrigin := runtimebundle.ResolveCandidateConvReaderForTest(featurehost.CorePorts{
		ConversationViewReader:        forged,
		ConversationReaderStockOrigin: true,
	}, nil)
	require.Equal(t, forged, resolvedReader)
	require.False(t, stockOrigin, "forged reader with nil standard features must have stock origin cleared to false")

	// Case 2: Build a real host with standard features to get genuine stock reader
	basePath := runtimebundle.MaterializeExampleConfigForTest(
		t,
		filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
	)
	raw, err := os.ReadFile(basePath)
	require.NoError(t, err)

	spoolDir := t.TempDir()
	customYAML := strings.Replace(
		string(raw),
		"server:\n  address: \"127.0.0.1:18080\"",
		fmt.Sprintf("server:\n  address: \"127.0.0.1:18080\"\n  large_payload_fast_path:\n    enabled: true\n    threshold_bytes: 4096\n    memory_spool_bytes: 32768\n    max_inflight_spool_bytes: 1048576\n    max_semantic_fact_bytes: 16384\n    spool_dir: %q", spoolDir),
		1,
	)
	cfgPath := filepath.Join(t.TempDir(), "stock-forged-reader.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(customYAML), 0o600))

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	require.NoError(t, err)
	hostServeCleanup(t, host)

	ps := runtimebundle.HostProcess(host)
	require.NotNil(t, ps.StandardFeatures)

	stockReader := ps.StandardFeatures.ConversationReader()
	require.NotNil(t, stockReader)

	// Forged reader -> stock origin overwritten to false
	resForgedReader, resForgedOrigin := runtimebundle.ResolveCandidateConvReaderForTest(featurehost.CorePorts{
		ConversationViewReader:        forged,
		ConversationReaderStockOrigin: true,
	}, ps.StandardFeatures)
	require.Equal(t, forged, resForgedReader)
	require.False(t, resForgedOrigin, "forged reader must be declined even when ConversationReaderStockOrigin=true")

	// Genuine stock reader with StockOrigin=true -> accepted
	resStockReader, resStockOrigin := runtimebundle.ResolveCandidateConvReaderForTest(featurehost.CorePorts{
		ConversationViewReader:        stockReader,
		ConversationReaderStockOrigin: true,
	}, ps.StandardFeatures)
	require.Equal(t, stockReader, resStockReader)
	require.True(t, resStockOrigin, "genuine stock reader with ConversationReaderStockOrigin=true must be accepted")

	// Unspecified reader (nil) with StandardFeatures present -> defaults to genuine stock reader and true
	resNilReader, resNilOrigin := runtimebundle.ResolveCandidateConvReaderForTest(featurehost.CorePorts{
		ConversationViewReader:        nil,
		ConversationReaderStockOrigin: false,
	}, ps.StandardFeatures)
	require.Equal(t, stockReader, resNilReader)
	require.True(t, resNilOrigin, "nil reader must resolve to stock reader with stock origin true")

	// Verify that when stockOrigin is false, authority gate declines
	census := largebody.NewStandardDependencyCensus("gen-forged-test")
	census.AddPort("security.session_recorder", true)
	census.Ports.BackendsEmpty = false
	census.Ports.ConversationViewReaderOccupied = (resForgedReader != nil)
	census.Ports.ConversationReaderFreshALegSupported = resForgedOrigin
	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              "gen-forged-test",
		Planes:                    census.Planes,
		Hooks:                     census.Hooks,
		Ports:                     census.Ports,
		TwoPhaseExecutorAvailable: true,
	}, 4096)
	require.NoError(t, err)
	authGate := largebody.NewAuthorityAssessmentGate(summary, census, "gen-forged-test")
	decision, reason := authGate.Evaluate()
	require.Equal(t, largebody.AssessmentDecisionDecline, decision)
	require.Equal(t, largebody.DeclineReasonAuthorityBlocker, reason)
}

// TestStockHost_OpenResponses_WireReachability_AndDeclineParity verifies that with a normal
// BuildHost configured with the actual openresponses frontend and actual compatible responses backend,
// a fresh no-store streaming request above threshold executes over the wire fast-path (OpenWire=1, Open=0)
// delivering real downstream SSE without {"status":"ok"}, and paired advanced protocol shapes decline precommit
// to canonical execution with exact parity against a feature-off canonical oracle host.
func TestStockHost_OpenResponses_WireReachability_AndDeclineParity(t *testing.T) {
	t.Parallel()

	var (
		openWireCalls         atomic.Int32
		openCanonicalCalls    atomic.Int32
		capturedCanonicalCall atomic.Pointer[aipapi.Call]
		lastUpstreamBody      atomic.Pointer[string]
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			s := string(b)
			lastUpstreamBody.Store(&s)
		}
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"},{"id":"o1"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/responses") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_prod\",\"model\":\"gpt-4o\",\"status\":\"in_progress\"}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello-from-upstream\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_prod\",\"model\":\"gpt-4o\",\"status\":\"completed\"}}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	customYAML := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"

model_aliases:
  - pattern: "^alias-openresponses$"
    replacement: "wire-backend:gpt-4o"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openresponses
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-responses-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "stock-wire-openresponses.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfgPathOff := filepath.Join(t.TempDir(), "stock-wire-openresponses-off.yaml")
	if err := os.WriteFile(cfgPathOff, []byte(strings.Replace(customYAML, "enabled: true", "enabled: false", 1)), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	buildHostWithConfig := func(cpath string) *runtimebundle.Host {
		h, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
			ConfigPath:      cpath,
			Mandatory:       aipsdk.StandardDistributionRequirements(),
			LogWriter:       io.Discard,
			HandlerComposer: stdhttp.ComposeStandardHTTP,
			RegistrySetup: func(reg *pluginreg.Registry) error {
				return reg.WrapLifecycleBackend("custom-openai-responses-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
					return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
						res, err := orig(instanceID, n, upstreamHTTP, deps)
						if err != nil {
							return pluginreg.BackendBuildResult{}, err
						}
						origOpenWire := res.Backend.OpenWire
						if origOpenWire != nil {
							res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
								openWireCalls.Add(1)
								return origOpenWire(ctx, req)
							}
						}
						origOpen := res.Backend.Open
						if origOpen != nil {
							res.Backend.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
								openCanonicalCalls.Add(1)
								callCopy := call
								capturedCanonicalCall.Store(&callCopy)
								return origOpen(ctx, call, cand)
							}
						}
						return res, nil
					}
				})
			},
		})
		if err != nil {
			t.Fatalf("BuildHost: %v", err)
		}
		hostServeCleanup(t, h)
		return h
	}

	host := buildHostWithConfig(cfgPath)
	hostOff := buildHostWithConfig(cfgPathOff)

	padding := strings.Repeat("x", 1500)
	respIDRe := regexp.MustCompile(`"(id)":\s*"(resp_[0-9a-zA-Z_-]+)"`)
	tsRe := regexp.MustCompile(`"(created_at|completed_at)":\s*[0-9]+`)
	normalizeBody := func(s string) string {
		s = respIDRe.ReplaceAllString(s, `"$1":"resp_NORMALIZED"`)
		s = tsRe.ReplaceAllString(s, `"$1":0`)
		return s
	}

	// 1. Positive: Fresh no-store stream plain text above threshold -> OpenWire=1, Open=0 on host, OpenWire=0, Open=1 on hostOff
	{
		wireBefore := openWireCalls.Load()
		canonBefore := openCanonicalCalls.Load()

		bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"store":false,"input":"hello %s"}`, padding)
		reqOn := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(bodyJSON))
		reqOn.Header.Set("Content-Type", "application/json")
		reqOn.Header.Set("X-AIP-Route", "wire-backend:gpt-4o")
		recOn := httptest.NewRecorder()

		host.HTTPHandler().ServeHTTP(recOn, reqOn)

		if recOn.Code != http.StatusOK {
			t.Fatalf("request failed with HTTP %d: %s", recOn.Code, recOn.Body.String())
		}
		require.Equal(t, wireBefore+1, openWireCalls.Load(), "wire fast-path must be invoked for valid plain text openresponses create")
		require.Equal(t, canonBefore, openCanonicalCalls.Load(), "canonical Open must not be called")
		require.Nil(t, capturedCanonicalCall.Load(), "no canonical call materialization for wire fast-path")
		require.Contains(t, recOn.Body.String(), "response.created")
		require.Contains(t, recOn.Body.String(), "hello-from-upstream")
		require.Contains(t, recOn.Body.String(), "response.completed")
		require.Contains(t, recOn.Body.String(), "data: [DONE]")
		require.NotContains(t, recOn.Body.String(), `{"status":"ok"}`)
		require.Equal(t, "text/event-stream", recOn.Header().Get("Content-Type"))

		reqOff := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(bodyJSON))
		reqOff.Header.Set("Content-Type", "application/json")
		reqOff.Header.Set("X-AIP-Route", "wire-backend:gpt-4o")
		recOff := httptest.NewRecorder()
		hostOff.HTTPHandler().ServeHTTP(recOff, reqOff)

		require.Equal(t, wireBefore+1, openWireCalls.Load(), "hostOff must not call wire fast-path")
		require.Equal(t, canonBefore+1, openCanonicalCalls.Load(), "hostOff must call canonical Open")
		require.Contains(t, recOff.Body.String(), "response.completed")
		require.Contains(t, recOff.Body.String(), "data: [DONE]")
		require.Equal(t, normalizeBody(recOff.Body.String()), normalizeBody(recOn.Body.String()), "plain response must match canonical oracle")
	}

	// 1b. Positive with options: temperature, top_p, max_output_tokens -> must match canonical oracle
	{
		wireBefore := openWireCalls.Load()
		canonBefore := openCanonicalCalls.Load()

		bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"store":false,"temperature":0.2,"top_p":0.8,"max_output_tokens":256,"input":"hello %s"}`, padding)
		reqOn := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(bodyJSON))
		reqOn.Header.Set("Content-Type", "application/json")
		reqOn.Header.Set("X-AIP-Route", "wire-backend:gpt-4o")
		recOn := httptest.NewRecorder()
		host.HTTPHandler().ServeHTTP(recOn, reqOn)

		require.Equal(t, wireBefore+1, openWireCalls.Load(), "options-carrying request must be accepted by wire fast-path")
		require.Equal(t, canonBefore, openCanonicalCalls.Load(), "canonical Open must not be called")
		require.Contains(t, recOn.Body.String(), "response.completed")
		require.Contains(t, recOn.Body.String(), "data: [DONE]")

		reqOff := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(bodyJSON))
		reqOff.Header.Set("Content-Type", "application/json")
		reqOff.Header.Set("X-AIP-Route", "wire-backend:gpt-4o")
		recOff := httptest.NewRecorder()
		hostOff.HTTPHandler().ServeHTTP(recOff, reqOff)

		require.Equal(t, wireBefore+1, openWireCalls.Load(), "hostOff must not call wire fast-path")
		require.Equal(t, canonBefore+1, openCanonicalCalls.Load(), "hostOff must call canonical Open")
		require.Contains(t, recOff.Body.String(), "response.completed")
		require.Contains(t, recOff.Body.String(), "data: [DONE]")
		require.Equal(t, normalizeBody(recOff.Body.String()), normalizeBody(recOn.Body.String()), "options-carrying response must match canonical oracle")
	}

	// 1c. Positive with model alias / rewrite: client selector != backend native
	{
		wireBefore := openWireCalls.Load()
		canonBefore := openCanonicalCalls.Load()

		clientModel := "alias-openresponses"
		bodyJSONAlias := fmt.Sprintf(`{"model":"%s","stream":true,"store":false,"input":"hello %s"}`, clientModel, padding)
		reqOn := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(bodyJSONAlias))
		reqOn.Header.Set("Content-Type", "application/json")
		recOn := httptest.NewRecorder()
		host.HTTPHandler().ServeHTTP(recOn, reqOn)

		if recOn.Code != http.StatusOK {
			t.Fatalf("alias wire request failed with HTTP %d: %s", recOn.Code, recOn.Body.String())
		}
		require.Equal(t, wireBefore+1, openWireCalls.Load(), "alias request must invoke wire fast-path")
		require.Equal(t, canonBefore, openCanonicalCalls.Load(), "alias wire request must not invoke canonical Open")
		require.NotNil(t, lastUpstreamBody.Load(), "upstream must receive request body")
		require.Contains(t, *lastUpstreamBody.Load(), `"model":"gpt-4o"`, "backend body model must be rewritten to candidate model")
		require.NotContains(t, *lastUpstreamBody.Load(), clientModel, "backend body must not contain original client alias")
		require.Contains(t, recOn.Body.String(), `"model":"alias-openresponses"`, "downstream response must echo exact client model")
		require.NotContains(t, recOn.Body.String(), `"model":"gpt-4o"`, "downstream response must not leak backend native model")
		require.Contains(t, recOn.Body.String(), "response.created")
		require.Contains(t, recOn.Body.String(), "response.completed")
		require.Contains(t, recOn.Body.String(), "data: [DONE]")

		reqOff := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(bodyJSONAlias))
		reqOff.Header.Set("Content-Type", "application/json")
		recOff := httptest.NewRecorder()
		hostOff.HTTPHandler().ServeHTTP(recOff, reqOff)

		require.Equal(t, wireBefore+1, openWireCalls.Load(), "hostOff must not increment OpenWire")
		require.Equal(t, canonBefore+1, openCanonicalCalls.Load(), "hostOff must increment canonical Open")
		require.NotNil(t, lastUpstreamBody.Load(), "upstream must receive request body on hostOff")
		require.Contains(t, *lastUpstreamBody.Load(), `"model":"gpt-4o"`, "canonical backend body model must be rewritten to candidate model")
		require.NotContains(t, *lastUpstreamBody.Load(), clientModel, "canonical backend body must not contain original client alias")
		require.Contains(t, recOff.Body.String(), `"model":"alias-openresponses"`, "canonical downstream response must echo exact client model")
		require.NotContains(t, recOff.Body.String(), `"model":"gpt-4o"`, "canonical downstream response must not leak backend native model")
		require.Contains(t, recOff.Body.String(), "response.created")
		require.Contains(t, recOff.Body.String(), "response.completed")
		require.Contains(t, recOff.Body.String(), "data: [DONE]")
		require.Equal(t, normalizeBody(recOff.Body.String()), normalizeBody(recOn.Body.String()), "model alias response must match canonical oracle with exact client model")
	}

	testDeclineParity := func(name, bodyJSON string) {
		t.Helper()
		wireBefore := openWireCalls.Load()

		reqOn := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(bodyJSON))
		reqOn.Header.Set("Content-Type", "application/json")
		reqOn.Header.Set("X-AIP-Route", "wire-backend:gpt-4o")
		recOn := httptest.NewRecorder()
		host.HTTPHandler().ServeHTTP(recOn, reqOn)

		reqOff := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(bodyJSON))
		reqOff.Header.Set("Content-Type", "application/json")
		reqOff.Header.Set("X-AIP-Route", "wire-backend:gpt-4o")
		recOff := httptest.NewRecorder()
		hostOff.HTTPHandler().ServeHTTP(recOff, reqOff)

		require.Equal(t, wireBefore, openWireCalls.Load(), name+": must decline wire precommit")
		require.Equal(t, recOff.Code, recOn.Code, name+": status code must match canonical oracle")
		require.Equal(t, normalizeBody(recOff.Body.String()), normalizeBody(recOn.Body.String()), name+": body must match canonical oracle")
	}

	// 2. Negative: item_reference in input array -> declines precommit to canonical with oracle parity
	testDeclineParity("item_reference in input array", fmt.Sprintf(`{"model":"gpt-4o","stream":true,"store":false,"input":[{"type":"item_reference","id":"item_ref_1"}, {"type":"message","role":"user","content":"msg %s"}]}`, padding))

	// 3. Negative: compaction in input array -> declines precommit to canonical with oracle parity
	testDeclineParity("compaction in input array", fmt.Sprintf(`{"model":"gpt-4o","stream":true,"store":false,"input":[{"type":"compaction","id":"c_1","dialect":"test","encrypted_content":"enc_blob"}, {"type":"message","role":"user","content":"msg %s"}]}`, padding))

	// 4. Negative: assistant phase in message item -> declines precommit to canonical with oracle parity
	testDeclineParity("assistant phase in message item", fmt.Sprintf(`{"model":"gpt-4o","stream":true,"store":false,"input":[{"type":"message","role":"assistant","phase":"commentary","content":"thought %s"}]}`, padding))

	// 5. Negative: reasoning item -> declines precommit to canonical with oracle parity
	testDeclineParity("reasoning item", fmt.Sprintf(`{"model":"gpt-4o","stream":true,"store":false,"input":[{"type":"reasoning","content":"thought"}, {"type":"message","role":"user","content":"msg %s"}]}`, padding))

	// 6. Negative: content part annotations -> declines precommit to canonical with oracle parity
	testDeclineParity("content part annotations", fmt.Sprintf(`{"model":"gpt-4o","stream":true,"store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello %s","annotations":[{"type":"file_citation"}]}]}]}`, padding))

	// 7. Negative: content part assistant_ref -> declines precommit to canonical with oracle parity
	testDeclineParity("content part assistant_ref", fmt.Sprintf(`{"model":"gpt-4o","stream":true,"store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello %s","assistant_ref":"msg_123"}]}]}`, padding))
}

// TestStockHost_OpenAIResponses_WireReachability_AndDeclineParity verifies that with a normal
// BuildHost configured with the actual openai-responses frontend and actual compatible responses backend,
// a fresh streaming request above threshold executes over the wire fast-path (OpenWire=1, Open=0)
// delivering real downstream SSE without {"status":"ok"}, and a resumed session declines precommit
// to canonical execution with exact parity against a feature-off canonical oracle host.
func TestStockHost_OpenAIResponses_WireReachability_AndDeclineParity(t *testing.T) {
	t.Parallel()

	var (
		openWireCalls         atomic.Int32
		openCanonicalCalls    atomic.Int32
		capturedCanonicalCall atomic.Pointer[aipapi.Call]
		lastUpstreamBody      atomic.Pointer[string]
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			s := string(b)
			lastUpstreamBody.Store(&s)
		}
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/responses") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_prod\",\"model\":\"gpt-4o\",\"status\":\"in_progress\"}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello-openai-responses-upstream\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_prod\",\"model\":\"gpt-4o\",\"status\":\"completed\"}}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	customYAML := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"

model_aliases:
  - pattern: "^alias-responses$"
    replacement: "wire-backend:gpt-4o"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-responses-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "stock-wire-openairesponses.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfgPathOff := filepath.Join(t.TempDir(), "stock-wire-openairesponses-off.yaml")
	if err := os.WriteFile(cfgPathOff, []byte(strings.Replace(customYAML, "enabled: true", "enabled: false", 1)), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	buildHostWithConfig := func(cpath string) *runtimebundle.Host {
		h, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
			ConfigPath:      cpath,
			Mandatory:       aipsdk.StandardDistributionRequirements(),
			LogWriter:       io.Discard,
			HandlerComposer: stdhttp.ComposeStandardHTTP,
			RegistrySetup: func(reg *pluginreg.Registry) error {
				return reg.WrapLifecycleBackend("custom-openai-responses-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
					return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
						res, err := orig(instanceID, n, upstreamHTTP, deps)
						if err != nil {
							return pluginreg.BackendBuildResult{}, err
						}
						origOpenWire := res.Backend.OpenWire
						if origOpenWire != nil {
							res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
								openWireCalls.Add(1)
								return origOpenWire(ctx, req)
							}
						}
						origOpen := res.Backend.Open
						if origOpen != nil {
							res.Backend.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
								openCanonicalCalls.Add(1)
								callCopy := call
								capturedCanonicalCall.Store(&callCopy)
								return origOpen(ctx, call, cand)
							}
						}
						return res, nil
					}
				})
			},
		})
		if err != nil {
			t.Fatalf("BuildHost: %v", err)
		}
		hostServeCleanup(t, h)
		return h
	}

	host := buildHostWithConfig(cfgPath)
	hostOff := buildHostWithConfig(cfgPathOff)

	padding := strings.Repeat("x", 1500)
	bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"input":"hello %s"}`, padding)

	respIDRe := regexp.MustCompile(`"(id|response_id)":\s*"(resp_[0-9a-zA-Z_-]+)"`)
	msgIDRe := regexp.MustCompile(`"(item_id|id)":\s*"msg_[0-9a-zA-Z_-]+"`)
	tsRe := regexp.MustCompile(`"(created_at|completed_at|created)":\s*[0-9]+`)
	normalizeBody := func(s string) string {
		s = respIDRe.ReplaceAllString(s, `"$1":"resp_NORMALIZED"`)
		s = msgIDRe.ReplaceAllString(s, `"$1":"msg_NORMALIZED"`)
		s = tsRe.ReplaceAllString(s, `"$1":0`)
		return s
	}

	// 1. Positive: Fresh stream plain text above threshold -> OpenWire=1, Open=0
	{
		wireBefore := openWireCalls.Load()
		canonBefore := openCanonicalCalls.Load()

		reqOn := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(bodyJSON))
		reqOn.Header.Set("Content-Type", "application/json")
		reqOn.Header.Set("X-AIP-Route", "wire-backend:gpt-4o")
		recOn := httptest.NewRecorder()

		host.HTTPHandler().ServeHTTP(recOn, reqOn)

		if recOn.Code != http.StatusOK {
			t.Fatalf("request failed with HTTP %d: %s", recOn.Code, recOn.Body.String())
		}
		require.Equal(t, wireBefore+1, openWireCalls.Load(), "wire fast-path must be invoked for valid plain text openai-responses create")
		require.Equal(t, canonBefore, openCanonicalCalls.Load(), "canonical Open must not be called")
		require.Nil(t, capturedCanonicalCall.Load(), "no canonical call materialization for wire fast-path")
		require.Contains(t, recOn.Body.String(), "response.created")
		require.Contains(t, recOn.Body.String(), "hello-openai-responses-upstream")
		require.Contains(t, recOn.Body.String(), "response.completed")
		require.Contains(t, recOn.Body.String(), "data: [DONE]")
		require.NotContains(t, recOn.Body.String(), `{"status":"ok"}`)
		require.Equal(t, "text/event-stream; charset=utf-8", recOn.Header().Get("Content-Type"))

		reqOff := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(bodyJSON))
		reqOff.Header.Set("Content-Type", "application/json")
		reqOff.Header.Set("X-AIP-Route", "wire-backend:gpt-4o")
		recOff := httptest.NewRecorder()
		hostOff.HTTPHandler().ServeHTTP(recOff, reqOff)

		require.Equal(t, wireBefore+1, openWireCalls.Load(), "hostOff must not call wire fast-path")
		require.Equal(t, canonBefore+1, openCanonicalCalls.Load(), "hostOff must call canonical Open")
		require.Contains(t, recOff.Body.String(), "response.completed")
		require.Contains(t, recOff.Body.String(), "data: [DONE]")
		require.Equal(t, normalizeBody(recOff.Body.String()), normalizeBody(recOn.Body.String()), "plain response must match canonical oracle")
	}

	// 1b. Positive with options: temperature, top_p, max_output_tokens -> must match canonical oracle
	{
		wireBefore := openWireCalls.Load()
		canonBefore := openCanonicalCalls.Load()

		bodyJSONOpts := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"temperature":0.2,"top_p":0.8,"max_output_tokens":256,"input":"hello %s"}`, padding)
		reqOn := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(bodyJSONOpts))
		reqOn.Header.Set("Content-Type", "application/json")
		reqOn.Header.Set("X-AIP-Route", "wire-backend:gpt-4o")
		recOn := httptest.NewRecorder()
		host.HTTPHandler().ServeHTTP(recOn, reqOn)

		require.Equal(t, wireBefore+1, openWireCalls.Load(), "wire fast-path must be invoked for options-carrying request")
		require.Equal(t, canonBefore, openCanonicalCalls.Load(), "canonical Open must not be called")
		require.Contains(t, recOn.Body.String(), "response.completed")
		require.Contains(t, recOn.Body.String(), "data: [DONE]")

		reqOff := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(bodyJSONOpts))
		reqOff.Header.Set("Content-Type", "application/json")
		reqOff.Header.Set("X-AIP-Route", "wire-backend:gpt-4o")
		recOff := httptest.NewRecorder()
		hostOff.HTTPHandler().ServeHTTP(recOff, reqOff)

		require.Equal(t, wireBefore+1, openWireCalls.Load(), "hostOff must not call wire fast-path")
		require.Equal(t, canonBefore+1, openCanonicalCalls.Load(), "hostOff must call canonical Open")
		require.Contains(t, recOff.Body.String(), "response.completed")
		require.Contains(t, recOff.Body.String(), "data: [DONE]")
		require.Equal(t, normalizeBody(recOff.Body.String()), normalizeBody(recOn.Body.String()), "options response must match canonical oracle")
	}

	// 1c. Positive with model alias / rewrite: client selector != backend native
	{
		wireBefore := openWireCalls.Load()
		canonBefore := openCanonicalCalls.Load()

		clientModel := "alias-responses"
		bodyJSONAlias := fmt.Sprintf(`{"model":"%s","stream":true,"input":"hello %s"}`, clientModel, padding)
		reqOn := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(bodyJSONAlias))
		reqOn.Header.Set("Content-Type", "application/json")
		recOn := httptest.NewRecorder()
		host.HTTPHandler().ServeHTTP(recOn, reqOn)

		if recOn.Code != http.StatusOK {
			t.Fatalf("alias wire request failed with HTTP %d: %s", recOn.Code, recOn.Body.String())
		}
		require.Equal(t, wireBefore+1, openWireCalls.Load(), "alias request must invoke wire fast-path")
		require.Equal(t, canonBefore, openCanonicalCalls.Load(), "alias wire request must not invoke canonical Open")
		require.NotNil(t, lastUpstreamBody.Load(), "upstream must receive request body")
		require.Contains(t, *lastUpstreamBody.Load(), `"model":"gpt-4o"`, "backend body model must be rewritten to candidate model")
		require.NotContains(t, *lastUpstreamBody.Load(), clientModel, "backend body must not contain original client alias")
		require.Contains(t, recOn.Body.String(), `"model":"alias-responses"`, "downstream response must echo exact client model")
		require.NotContains(t, recOn.Body.String(), `"model":"gpt-4o"`, "downstream response must not leak backend native model")
		require.Contains(t, recOn.Body.String(), "response.created")
		require.Contains(t, recOn.Body.String(), "response.completed")
		require.Contains(t, recOn.Body.String(), "data: [DONE]")

		reqOff := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(bodyJSONAlias))
		reqOff.Header.Set("Content-Type", "application/json")
		recOff := httptest.NewRecorder()
		hostOff.HTTPHandler().ServeHTTP(recOff, reqOff)

		require.Equal(t, wireBefore+1, openWireCalls.Load(), "hostOff must not increment OpenWire")
		require.Equal(t, canonBefore+1, openCanonicalCalls.Load(), "hostOff must increment canonical Open")
		require.NotNil(t, lastUpstreamBody.Load(), "upstream must receive request body on hostOff")
		require.Contains(t, *lastUpstreamBody.Load(), `"model":"gpt-4o"`, "canonical backend body model must be rewritten to candidate model")
		require.NotContains(t, *lastUpstreamBody.Load(), clientModel, "canonical backend body must not contain original client alias")
		require.Contains(t, recOff.Body.String(), `"model":"alias-responses"`, "canonical downstream response must echo exact client model")
		require.NotContains(t, recOff.Body.String(), `"model":"gpt-4o"`, "canonical downstream response must not leak backend native model")
		require.Contains(t, recOff.Body.String(), "response.created")
		require.Contains(t, recOff.Body.String(), "response.completed")
		require.Contains(t, recOff.Body.String(), "data: [DONE]")
		require.Equal(t, normalizeBody(recOff.Body.String()), normalizeBody(recOn.Body.String()), "model alias response must match canonical oracle with exact client model")
	}

	// 2. Negative: Resumed session request -> declines precommit to canonical with oracle parity
	{
		wireBefore := openWireCalls.Load()

		reqOn := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(bodyJSON))
		reqOn.Header.Set("Content-Type", "application/json")
		reqOn.Header.Set("X-AIP-Route", "wire-backend:gpt-4o")
		reqOn.Header.Set("X-AIP-Resume-Token", "test-resume-token")
		recOn := httptest.NewRecorder()
		host.HTTPHandler().ServeHTTP(recOn, reqOn)

		reqOff := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(bodyJSON))
		reqOff.Header.Set("Content-Type", "application/json")
		reqOff.Header.Set("X-AIP-Route", "wire-backend:gpt-4o")
		reqOff.Header.Set("X-AIP-Resume-Token", "test-resume-token")
		recOff := httptest.NewRecorder()
		hostOff.HTTPHandler().ServeHTTP(recOff, reqOff)

		require.Equal(t, wireBefore, openWireCalls.Load(), "resumed session must decline wire precommit")
		require.Equal(t, recOff.Code, recOn.Code, "status code must match canonical oracle")
	}
}

// compileOpenResponsesProofOracle independently derives the certified proof for body
// through the REAL openresponses profile (no pipeline internals), mirroring production
// ProofInput construction in frontendpipe.replayCandidate (same route header, create path,
// body-model routing, semantic-fact budget). It is the independent oracle that assessed
// pipeline facts must equal.
func compileOpenResponsesProofOracle(t *testing.T, body string, hdr http.Header) frontendpipe.ProofOutput {
	t.Helper()
	src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		Memory: []byte(body),
		Size:   int64(len(body)),
	})
	require.NoError(t, err)
	defer func() { _ = src.Close() }()
	const factBudget = 16384
	scanCtx := largebody.WithSemanticFactBudget(context.Background(), factBudget)
	proofOut, err := openresponses.NewProfile().CompileProof(scanCtx, frontendpipe.ProofInput{
		Ctx:                scanCtx,
		Headers:            hdr,
		URLPath:            "/openresponses/v1/responses",
		RouteSelector:      "wire-backend:gpt-4o",
		RoutePrefixes:      routeselect.NewPrefixSet(nil),
		RouteFromBodyModel: true,
		Source:             src,
		BodyBytes:          int64(len(body)),
	})
	require.NoError(t, err, "oracle CompileProof must accept the fresh no-store fixture")
	require.NoError(t, proofOut.Validate(factBudget))
	return proofOut
}

// listStoredSessionIDs returns the set of session IDs in the ACTUAL process store
// (read-only; no executor mutation).
func listStoredSessionIDs(t *testing.T, store ssessionapp.Store) map[string]bool {
	t.Helper()
	require.NotNil(t, store)
	rows, err := store.Summary(context.Background(), securedomain.SummaryQuery{Limit: 100})
	require.NoError(t, err)
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[string(r.SessionID)] = true
	}
	return out
}

type storedClientInput struct {
	// One client.input transcript record from the ACTUAL process secure-session store.
	SessionID string
	TurnID    string
	TraceID   string
	Role      string
	Ordinal   int
	Parts     []string
}

// queryClientInputTranscript reads the ACTUAL process store (read-only via the existing
// runtimebundle.HostProcess testing accessor; no executor mutation) for client.input
// transcript records of one session.
func queryClientInputTranscript(t *testing.T, store ssessionapp.Store, sessionID string) []storedClientInput {
	t.Helper()
	require.NotEmpty(t, sessionID, "session ID must be present to query the store")
	require.NotNil(t, store, "process secure-session store must be present")
	items, err := store.Transcript(context.Background(), securedomain.SessionID(sessionID), securedomain.ReadOptions{Limit: 100})
	require.NoError(t, err)
	var out []storedClientInput
	for _, it := range items {
		if it.EventKind != "client.input" {
			continue
		}
		var payload struct {
			TraceID string   `json:"trace_id"`
			Role    string   `json:"role"`
			Ordinal int      `json:"ordinal"`
			Parts   []string `json:"parts"`
		}
		require.NoError(t, json.Unmarshal([]byte(it.PayloadRef), &payload), "stored client.input payload must decode")
		out = append(out, storedClientInput{
			SessionID: string(it.SessionID),
			TurnID:    string(it.TurnID),
			TraceID:   payload.TraceID,
			Role:      payload.Role,
			Ordinal:   payload.Ordinal,
			Parts:     payload.Parts,
		})
	}
	return out
}

func extractWireResponseIDs(t *testing.T, body string) []string {
	t.Helper()
	re := regexp.MustCompile(`"(id)":\s*"(resp_[0-9a-zA-Z_-]+|gen-[0-9a-zA-Z_-]+|call_[0-9a-zA-Z_-]+)"`)
	matches := re.FindAllStringSubmatch(body, -1)
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m[2])
	}
	return ids
}

// TestStockHost_WireProofHandoff_And_FreshRespIDs is the production-boundary proof for
// BLOCKER1 (assessed proof Session/Turn/identity handoff) and BLOCKER2 (frontend-owned
// fresh resp_* per response, distinct from deterministic internal RequestID).
// It runs a real BuildHost production lane above threshold with fresh sessions enabled
// and compares against a canonical-off oracle host.
func TestStockHost_WireProofHandoff_And_FreshRespIDs(t *testing.T) {
	t.Parallel()

	var (
		openWireCalls      atomic.Int32
		openCanonicalCalls atomic.Int32
		lastTrace          atomic.Pointer[string]
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/responses") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_prod\",\"model\":\"gpt-4o\",\"status\":\"in_progress\"}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello-from-upstream\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_prod\",\"model\":\"gpt-4o\",\"status\":\"completed\"}}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	customYAML := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openresponses
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-responses-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "stock-wire-proof-handoff.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-openai-responses-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					res, err := orig(instanceID, n, upstreamHTTP, deps)
					if err != nil {
						return pluginreg.BackendBuildResult{}, err
					}
					origOpenWire := res.Backend.OpenWire
					if origOpenWire != nil {
						res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
							openWireCalls.Add(1)
							tr := req.TraceID
							lastTrace.Store(&tr)
							return origOpenWire(ctx, req)
						}
					}
					origOpen := res.Backend.Open
					if origOpen != nil {
						res.Backend.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							openCanonicalCalls.Add(1)
							return origOpen(ctx, call, cand)
						}
					}
					return res, nil
				}
			})
		},
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	// Read-only handles: the actual process store via the existing HostProcess testing
	// accessor plus OpenWire observation need no executor mutation.
	store := runtimebundle.HostProcess(host).SecureSessions
	require.NotNil(t, store)

	padding := strings.Repeat("x", 1500)
	bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"store":false,"input":"hello %s"}`, padding)
	inputText := "hello " + padding

	wireHeaders := func() http.Header {
		h := http.Header{}
		h.Set("Content-Type", "application/json")
		h.Set("X-AIP-Route", "wire-backend:gpt-4o")
		return h
	}
	doWire := func(body string, hdr http.Header, ctx context.Context) (int, string, http.Header) {
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(body))
		req.Header = hdr.Clone()
		if ctx != nil {
			req = req.WithContext(ctx)
		}
		rec := httptest.NewRecorder()
		host.HTTPHandler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String(), rec.Header()
	}

	// Independent oracle for the fresh fixture through the REAL profile.
	oracle := compileOpenResponsesProofOracle(t, bodyJSON, wireHeaders())
	wantTrace := oracle.Proof().Identity.CallID("")
	require.True(t, strings.HasPrefix(wantTrace, "call_"), "oracle identity must be canonical call_*, got %q", wantTrace)
	wantTurn := oracle.Proof().Turn
	require.NoError(t, wantTurn.Validate(16384), "oracle turn must stay within the semantic-fact budget")
	require.True(t, oracle.Proof().Session.ProvesFreshALeg(), "fixture must assess as a fresh A-leg")
	require.Len(t, wantTurn.Items, 1, "fixture pins a single user text item")
	require.Equal(t, aipapi.RoleUser, wantTurn.Items[0].Role)
	require.Equal(t, int64(0), wantTurn.Items[0].Ordinal)
	require.Len(t, wantTurn.Items[0].Parts, 1)
	require.Equal(t, aipapi.ContentPartText, wantTurn.Items[0].Parts[0].Kind)
	require.Equal(t, int64(len(inputText)), wantTurn.Items[0].Parts[0].ContentBytes)
	require.Equal(t, int64(len(inputText)), wantTurn.TotalContentBytes)

	// A/B/C: fresh wire request executes via OpenWire with canonical identity and the
	// assessed turn recorded in the ACTUAL store.
	wireBefore := openWireCalls.Load()
	canonBefore := openCanonicalCalls.Load()
	code1, resp1, hdr1 := doWire(bodyJSON, wireHeaders(), nil)
	require.Equal(t, http.StatusOK, code1, "fresh wire request must return 200")
	require.Equal(t, wireBefore+1, openWireCalls.Load(), "fresh request must invoke OpenWire exactly once (single BeginTurn)")
	require.Equal(t, canonBefore, openCanonicalCalls.Load(), "fresh wire request must not invoke canonical Open")
	require.Contains(t, resp1, "response.created")
	require.Contains(t, resp1, "hello-from-upstream")
	require.Contains(t, resp1, "response.completed")
	require.NotContains(t, resp1, "gen-", "wire response must never contain gen- IDs")
	sess1 := hdr1.Get("X-AIP-Session-ID")
	require.NotEmpty(t, sess1, "real BeginTurn must issue session headers")

	trace1 := ""
	if v := lastTrace.Load(); v != nil {
		trace1 = *v
	}
	require.Equal(t, wantTrace, trace1, "OpenWire-observed TraceID must equal the independently derived canonical identity exactly")
	require.NotContains(t, trace1, "gen-", "wire identity must NEVER be generation-scoped")

	recs1 := queryClientInputTranscript(t, store, sess1)
	require.Len(t, recs1, len(wantTurn.Items), "actual store must hold EXACTLY the assessed turn lines (a missing handoff records nothing)")
	for i := range recs1 {
		require.Equal(t, sess1, recs1[i].SessionID, "stored SessionID must match the actual response header")
		require.NotEmpty(t, recs1[i].TurnID, "actual session turn ID must be recorded")
		require.Equal(t, trace1, recs1[i].TraceID, "stored recorder TraceID must equal OpenWire-observed canonical identity exactly")
		require.Equal(t, string(wantTurn.Items[i].Role), recs1[i].Role)
		require.Equal(t, int(wantTurn.Items[i].Ordinal), recs1[i].Ordinal)
		wantParts := make([]string, 0, len(wantTurn.Items[i].Parts))
		for _, p := range wantTurn.Items[i].Parts {
			wantParts = append(wantParts, string(p.Kind))
		}
		require.Equal(t, wantParts, recs1[i].Parts)
	}

	// D: two IDENTICAL requests on the same generation produce TWO distinct valid resp_* IDs,
	// each constant through created/deltas/completed; internal RequestID stays deterministic.
	code2, resp2, hdr2 := doWire(bodyJSON, wireHeaders(), nil)
	require.Equal(t, http.StatusOK, code2, "second identical request must return 200")
	require.Equal(t, wireBefore+2, openWireCalls.Load(), "second identical request must invoke OpenWire again")
	ids1 := extractWireResponseIDs(t, resp1)
	ids2 := extractWireResponseIDs(t, resp2)
	require.NotEmpty(t, ids1, "first response must contain response IDs")
	require.NotEmpty(t, ids2, "second response must contain response IDs")
	for _, id := range append(append([]string{}, ids1...), ids2...) {
		require.True(t, strings.HasPrefix(id, "resp_"), "every wire response ID must be valid resp_*, got %q", id)
		require.False(t, strings.HasPrefix(id, "gen-"), "gen-* is never a valid response ID, got %q", id)
	}
	for _, id := range ids1 {
		require.Equal(t, ids1[0], id, "response ID must stay constant through created/deltas/completed within one response")
	}
	for _, id := range ids2 {
		require.Equal(t, ids2[0], id, "response ID must stay constant through created/deltas/completed within one response")
	}
	require.NotEqual(t, ids1[0], ids2[0], "two identical requests must yield TWO distinct resp_* IDs")
	trace2 := ""
	if v := lastTrace.Load(); v != nil {
		trace2 = *v
	}
	require.Equal(t, trace1, trace2, "deterministic internal RequestID may repeat for identical payload (distinct from response ID)")
	require.NotEqual(t, trace1, ids1[0], "internal RequestID (call_*) must stay a distinct concept from frontend response ID (resp_*)")
	sess2 := hdr2.Get("X-AIP-Session-ID")
	require.NotEmpty(t, sess2)
	require.NotEqual(t, sess1, sess2, "fresh requests must mint distinct sessions")
	recs2 := queryClientInputTranscript(t, store, sess2)
	require.Len(t, recs2, len(wantTurn.Items))
	for i := range recs2 {
		require.Equal(t, trace2, recs2[i].TraceID)
		require.Equal(t, recs1[i].Role, recs2[i].Role, "repeat requests must record identical lines (no cross-request carrier mutation)")
		require.Equal(t, recs1[i].Ordinal, recs2[i].Ordinal)
		require.Equal(t, recs1[i].Parts, recs2[i].Parts)
	}

	// Stale-carrier precedence THROUGH the real HTTP pipeline (not helper-only): a poisoned
	// caller ctx carrying attacker Wire session/turn/identity plus a spoofed X-Trace-ID
	// header must not replace assessed proof. There is deliberately NO explicit request-ID
	// HTTP carrier in the canonical header set (pkg/aipsdk/httpheaders.go) and all three
	// profiles pass explicit "" (openresponses/profile.go, openairesponses/profile.go,
	// openailegacy/profile.go), so digest-derived call_* is the canonical contract; the
	// attacker-req carrier below exercises the explicit-carrier precedence path at the
	// pipeline boundary, where the stamp fallback can never mask it (fallback only applies
	// when ctx carries no identity at all).
	poisoned := largebody.WithWireSessionInput(context.Background(), largebody.SessionInput{
		AuthoritativeSessionID: "attacker-session",
		ClientSessionID:        "attacker-hint",
		ALegID:                 "attacker-aleg",
	})
	poisoned = largebody.WithWireClientTurnShape(poisoned, largebody.ClientTurnShape{})
	poisoned = largebody.WithWireIdentity(poisoned, "attacker-req", "attacker-trace")
	hdr3 := wireHeaders()
	hdr3.Set("X-Trace-ID", "attacker-trace")
	code3, resp3, hdr3out := doWire(bodyJSON, hdr3, poisoned)
	require.Equal(t, http.StatusOK, code3, "poisoned-ctx request must still return 200 via assessed proof")
	require.Equal(t, wireBefore+3, openWireCalls.Load(), "assessment must be unaffected by poisoned ctx carriers")
	require.Equal(t, canonBefore, openCanonicalCalls.Load(), "poisoned-ctx request must stay on the wire path")
	trace3 := ""
	if v := lastTrace.Load(); v != nil {
		trace3 = *v
	}
	require.Equal(t, wantTrace, trace3, "assessed identity must win over stale ctx carriers through the pipeline")
	require.NotContains(t, trace3, "attacker")
	require.NotContains(t, resp3, "attacker", "wire response must never leak stale ctx carriers")
	require.NotContains(t, resp3, "gen-")
	sess3 := hdr3out.Get("X-AIP-Session-ID")
	require.NotEmpty(t, sess3)
	require.NotContains(t, sess3, "attacker")
	require.NotEqual(t, "attacker-aleg", hdr3out.Get("X-AIP-A-Leg-Id"))
	recs3 := queryClientInputTranscript(t, store, sess3)
	require.Len(t, recs3, len(wantTurn.Items), "assessed turn must win over the poisoned empty turn shape")
	for i := range recs3 {
		require.Equal(t, wantTrace, recs3[i].TraceID)
		require.Equal(t, string(wantTurn.Items[i].Role), recs3[i].Role)
	}

	// Client-session-hint THROUGH the pipeline: X-AIP-Session-Hint (canonical header
	// pkg/aipsdk.HeaderSessionHint) is assessed canonical-only for openresponses, so the
	// request must decline wire precommit and execute canonically with its turn in the store.
	hdr4 := wireHeaders()
	hdr4.Set("X-AIP-Session-Hint", "client-hint-test")
	beforeSessions := listStoredSessionIDs(t, store)
	code4, _, _ := doWire(bodyJSON, hdr4, nil)
	require.Equal(t, http.StatusOK, code4, "hint request must return 200 via canonical")
	require.Equal(t, wireBefore+3, openWireCalls.Load(), "hint must decline wire precommit")
	require.Equal(t, canonBefore+1, openCanonicalCalls.Load(), "hint must execute canonically (single admission)")
	afterSessions := listStoredSessionIDs(t, store)
	var hintSessions []string
	for id := range afterSessions {
		if !beforeSessions[id] {
			hintSessions = append(hintSessions, id)
		}
	}
	require.Len(t, hintSessions, 1, "canonical decline path must begin exactly one real session in the actual store")
	recs4 := queryClientInputTranscript(t, store, hintSessions[0])
	require.NotEmpty(t, recs4, "canonical decline path must record the client turn in the actual store")
}
