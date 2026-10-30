package runtimebundle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

func TestDecision_StandardHostFailoverUsageAndBilling(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name          string
		invalidAnswer bool
	}{{"overload", false}, {"invalid-answer", true}} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			var firstCalls, secondCalls atomic.Int32
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				firstCalls.Add(1)
				if scenario.invalidAnswer {
					_, _ = io.WriteString(w, `{"model":"jev","answers":{},"usage":{"input_tokens":3,"output_tokens":0,"cost":0.0000005}}`)
					return
				}
				w.WriteHeader(529)
				_, _ = io.WriteString(w, `{"detail":"overloaded"}`)
			}))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secondCalls.Add(1)
				if r.Header.Get("X-Upstream-Session") != "" {
					t.Error("client header reached decision upstream")
				}
				_, _ = io.WriteString(w, `{"model":"jev-resolved","answers":{"q":{"type":"noul","noul":0.8}},"usage":{"input_tokens":10,"output_tokens":2,"cost":0.000001}}`)
			}))
			defer second.Close()
			host, aLeg, logs := buildDecisionHost(t, first.URL, second.URL, false)
			req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{"model":"first:jev|second:jev","state":{"evidence":"private-systemone-evidence-canary"},"questions":{"q":{"type":"noul"}}}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Upstream-Session", "must-not-forward")
			w := httptest.NewRecorder()
			host.HTTPHandler().ServeHTTP(w, req)
			var response struct {
				Model   string `json:"model"`
				Answers map[string]struct {
					Noul float64 `json:"noul"`
				} `json:"answers"`
				Usage map[string]int `json:"usage"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Model != "jev-resolved" || response.Answers["q"].Noul != 0.8 || response.Usage["input_tokens"] != 10 || response.Usage["output_tokens"] != 2 || firstCalls.Load() != 1 || secondCalls.Load() != 1 {
				t.Fatalf("decision failover = %d %s, calls=%d/%d logs=%s", w.Code, w.Body.String(), firstCalls.Load(), secondCalls.Load(), logs.String())
			}
			if strings.Contains(logs.String(), "private-systemone-evidence-canary") {
				t.Fatal("decision evidence reached default logs")
			}
			ex := hostActiveExecutor(t, host)
			attempts, err := ex.Store.LoadAttempts(t.Context(), *aLeg.Load())
			if err != nil || len(attempts) != 2 || attempts[0].BLegID == attempts[1].BLegID {
				t.Fatalf("B-leg lineage = %+v, err=%v headers=%v", attempts, err, w.Header())
			}
		})
	}
}

func TestDecision_StandardHostGuardAndChatRefuseBeforeUpstream(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	host, _, _ := buildDecisionHost(t, upstream.URL, upstream.URL, true)
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/v1/systemone", `{"model":"first:jev","state":"x","questions":{"q":{"type":"noul"}}}`, 403},
		{"/v1/chat/completions", `{"model":"first:jev","messages":[{"role":"user","content":"hi"}]}`, 400},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		host.HTTPHandler().ServeHTTP(w, r)
		if w.Code != tc.status || calls.Load() != 0 {
			t.Fatalf("refusal = %d %s, calls=%d", w.Code, w.Body.String(), calls.Load())
		}
	}
}

func buildDecisionHost(t *testing.T, first, second string, guarded bool) (*runtimebundle.Host, *atomic.Pointer[string], *decisionTestLog) {
	t.Helper()
	return buildDecisionHostWithRouteTrace(t, first, second, guarded, "")
}

func buildDecisionHostWithRouteTrace(t *testing.T, first, second string, guarded bool, routeTracePath string) (*runtimebundle.Host, *atomic.Pointer[string], *decisionTestLog) {
	t.Helper()
	features := "  features: []\n"
	if guarded {
		features = "  features:\n    - id: secrets-guard\n      enabled: true\n      config:\n        action: block\n"
	}
	var text strings.Builder
	text.WriteString(`server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 2
  default_route: "first:jev|second:jev"
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
plugins:
  frontends:
    - id: systemone
      enabled: true
    - id: openai-legacy
      enabled: true
  backends:
`)
	for i, endpoint := range []string{first, second} {
		id := []string{"first", "second"}[i]
		fmt.Fprintf(&text, "    - id: %s\n      kind: custom-systemone-compatible\n      enabled: true\n      config:\n        backend_prefix: %s\n        base_url: %s\n        models:\n          source: inline\n          items:\n            - canonical_id: jev\n              native_id: jev\n", id, id, endpoint)
	}
	text.WriteString(features)
	fmt.Fprintf(&text, "model_catalog:\n  enabled: true\n  external_updates_enabled: false\n  cache_path: %q\n", filepath.Join(t.TempDir(), "catalog.json"))
	if routeTracePath != "" {
		fmt.Fprintf(&text, "diagnostics:\n  enabled: true\n  route_trace_path: %q\n", routeTracePath)
	}
	path := filepath.Join(t.TempDir(), "decision.yaml")
	if err := os.WriteFile(path, []byte(text.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := &decisionTestLog{}
	aLeg := &atomic.Pointer[string]{}
	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath: path, Mandatory: aipsdk.StandardDistributionRequirements(), LogWriter: logs,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-systemone-compatible", func(original pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(id string, node yaml.Node, client *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					result, err := original(id, node, client, deps)
					if err != nil {
						return result, err
					}
					open := result.Backend.Open
					result.Backend.Open = func(ctx context.Context, call aipapi.Call, candidate routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						id := call.Session.ALegID
						aLeg.Store(&id)
						return open(ctx, call, candidate)
					}
					return result, nil
				}
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hostServeCleanup(t, host)
	return host, aLeg, logs
}

type decisionTestLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *decisionTestLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(data)
}

func (l *decisionTestLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// TestDecision_RouteTraceRecordsRequestThroughBuiltHost proves the composition
// root owns the route-trace buffer: a request through the built host records an
// entry in the same ring the diagnostics endpoint serves, with no HTTP-side
// post-construction mutation of the executor.
func TestDecision_RouteTraceRecordsRequestThroughBuiltHost(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"model":"jev-resolved","answers":{"q":{"type":"noul","noul":0.8}},"usage":{"input_tokens":10,"output_tokens":2,"cost":0.000001}}`)
	}))
	defer upstream.Close()

	const routeTracePath = "/diag/route-trace"
	host, _, _ := buildDecisionHostWithRouteTrace(t, upstream.URL, upstream.URL, false, routeTracePath)

	req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{"model":"first:jev|second:jev","state":{"evidence":"route-trace-canary"},"questions":{"q":{"type":"noul"}}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("request status %d body=%s", w.Code, w.Body.String())
	}

	ex := hostActiveExecutor(t, host)
	if ex.RouteTrace == nil {
		t.Fatal("the composition root must install the route-trace buffer on the executor")
	}
	if len(ex.RouteTrace.Snapshot()) == 0 {
		t.Fatal("the request must record at least one route-trace entry")
	}

	tr := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(tr, httptest.NewRequest(http.MethodGet, routeTracePath, nil))
	if tr.Code != http.StatusOK {
		t.Fatalf("route trace endpoint status %d body=%s", tr.Code, tr.Body.String())
	}
	var entries []diag.RouteTraceEntry
	if err := json.Unmarshal(tr.Body.Bytes(), &entries); err != nil {
		t.Fatalf("route trace JSON: %v body=%s", err, tr.Body.String())
	}
	if len(entries) == 0 {
		t.Fatal("route trace endpoint must serve the recorded entries")
	}
}
