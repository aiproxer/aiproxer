package stdhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	httpauth "github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
	"gopkg.in/yaml.v3"
)

func TestFrontendIsolation_CoexistenceAndIdentity(t *testing.T) {
	reg := isolationRegistry(t)
	var seen sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", &seen)
	mux := http.NewServeMux()
	plugins := []config.PluginConfig{{ID: "openai-responses", Enabled: true}, {ID: "openai-legacy", Enabled: true}, {ID: "anthropic", Enabled: true}, {ID: "gemini", Enabled: true}, {ID: "openresponses", Enabled: true}}
	if err := MountBundledFrontends(MountBundledFrontendsInput{Mux: mux, Frontends: HTTPFrontendInput{Executor: ex, DefaultRouteSelector: "stub:x", Plugins: plugins, Registry: reg}}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path, body, field string
		op                aipapi.Operation
	}{
		{"/v1/responses", `{"model":"stub:x","input":"ping","stream":false}`, "output", aipapi.OperationOpenAIResponses},
		{"/v1/chat/completions", `{"model":"stub:x","messages":[{"role":"user","content":"ping"}]}`, "choices", aipapi.OperationOpenAIChatCompletions},
		{"/v1/messages", `{"model":"stub:x","max_tokens":32,"messages":[{"role":"user","content":"ping"}]}`, "content", ""},
		{"/v1beta/models/stub:x:generateContent", `{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}`, "candidates", ""},
		{"/openresponses/v1/responses", `{"model":"stub:x","input":"ping","stream":false}`, "status", aipapi.OperationOpenResponsesCreate},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			r = r.WithContext(httpauth.WithPrincipal(r.Context(), execview.PrincipalView{ID: "isolation-test"}))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("User-Agent", "isolation-test")
			// The OpenResponses frontend defaults store:true, so a continuation scope
			// is required to reserve a proxy response id. The proxy-owned session id
			// header provides that scope (as the session/auth middleware does in the
			// standard composition).
			r.Header.Set("X-AIP-Session-Id", "sess-isolation")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var out map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if _, ok := out[tc.field]; !ok {
				t.Fatalf("missing %q in %s", tc.field, w.Body)
			}
			call := testkit.MustAIPCall(t, mustSeen(t, &seen))
			if tc.op != "" && call.Invocation.Operation != tc.op {
				t.Fatalf("operation=%q want %q", call.Invocation.Operation, tc.op)
			}
			if call.Invocation.ClientUserAgent != "isolation-test" {
				t.Fatalf("user-agent=%q", call.Invocation.ClientUserAgent)
			}
		})
	}
}

func TestFrontendIsolation_PathOwnsProtocolOverBodyHeadersAndUserAgent(t *testing.T) {
	reg := isolationRegistry(t)
	var seen sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", &seen)
	mux := http.NewServeMux()
	plugins := []config.PluginConfig{{ID: "openai-responses", Enabled: true}, {ID: "openresponses", Enabled: true}}
	if err := MountBundledFrontends(MountBundledFrontendsInput{Mux: mux, Frontends: HTTPFrontendInput{Executor: ex, DefaultRouteSelector: "stub:x", Plugins: plugins, Registry: reg}}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{"model":"stub:x","messages":[{"role":"user","content":"wrong"}]}`))
	r = r.WithContext(httpauth.WithPrincipal(r.Context(), execview.PrincipalView{ID: "isolation-test"}))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-AIP-Route", "switch")
	r.Header.Set("User-Agent", "switcher")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	if _, ok := seen.Load("last"); ok {
		t.Fatal("invalid OpenResponses body reached executor")
	}
}

func TestFrontendIsolation_GenericMountPreflightsConflictsWithoutProtocolBranching(t *testing.T) {
	reg := isolationRegistry(t)
	mux := http.NewServeMux()
	mux.Handle("POST /v1/responses", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	plugins := []config.PluginConfig{{ID: "openresponses", Enabled: true, Config: yamlNode(t, "base_path: /v1\n")}}
	err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "unused", nil),
			DefaultRouteSelector: "stub:x",
			Plugins:              plugins,
			Registry:             reg,
		},
	})
	if !strings.Contains(errString(err), "route") {
		t.Fatalf("expected generic route conflict, got %v", err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("pre-existing route was replaced after failed mount: %d", w.Code)
	}
}

// TestRouteClaims_ProductionCompositionRejectsCanonicalTakeover proves the
// generic claims seam wired into the production composition rejects an
// OpenResponses base_path=/v1 takeover of an existing /v1/responses owner
// BEFORE any handler is mounted, with an atomic (unchanged mux) failure and
// both-owner diagnostics reachable through the sentinel ErrRouteConflict.
func TestRouteClaims_ProductionCompositionRejectsCanonicalTakeover(t *testing.T) {
	reg := isolationRegistry(t)
	mux := http.NewServeMux()
	mux.Handle("POST /v1/responses", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	plugins := []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
		{ID: "openresponses", Enabled: true, Config: yamlNode(t, "base_path: /v1\n")},
	}
	err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "unused", nil),
			DefaultRouteSelector: "stub:x",
			Plugins:              plugins,
			Registry:             reg,
			FrontendRouteClaims:  standardplugins.StandardFrontendRouteClaims(),
		},
	})
	if err == nil {
		t.Fatal("expected canonical takeover to be rejected")
	}
	if !errors.Is(err, ErrRouteConflict) {
		t.Fatalf("want ErrRouteConflict, got %v", err)
	}
	var detail httpcontract.RouteConflictError
	if !errors.As(err, &detail) {
		t.Fatalf("want RouteConflictError (both owners), got %T: %v", err, err)
	}
	if detail.ExistingOwner != "openai-responses" || detail.NewOwner != "openresponses" {
		t.Fatalf("conflict must name both owners: existing=%q new=%q", detail.ExistingOwner, detail.NewOwner)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("canonical takeover failure was not atomic: pre-existing route replaced (%d)", w.Code)
	}
}

// TestRouteClaims_ProductionCompositionAdmitsDefaultPaths proves the generic
// claims seam admits the default non-colliding OpenResponses base path while
// the existing /v1 frontends remain mounted, so route-ownership validation
// does not reject a valid generation.
func TestRouteClaims_ProductionCompositionAdmitsDefaultPaths(t *testing.T) {
	reg := isolationRegistry(t)
	mux := http.NewServeMux()
	plugins := []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
		{ID: "openresponses", Enabled: true},
	}
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "unused", nil),
			DefaultRouteSelector: "stub:x",
			Plugins:              plugins,
			Registry:             reg,
			FrontendRouteClaims:  standardplugins.StandardFrontendRouteClaims(),
		},
	}); err != nil {
		t.Fatalf("default paths must be admitted: %v", err)
	}
	// The openai-responses route must remain mounted (a 404 would mean the
	// claims validation skipped its handler); a decode error for the empty body
	// is expected and proves the handler is reachable.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)))
	if w.Code == http.StatusNotFound {
		t.Fatal("openai-responses /v1/responses handler was not mounted after claims validation")
	}
}

func TestFrontendIsolation_DisabledOpenResponsesFrontendNotMounted(t *testing.T) {
	reg := isolationRegistry(t)
	mux := http.NewServeMux()
	plugins := []config.PluginConfig{{ID: "openresponses", Enabled: false, Config: yamlNode(t, "base_path: /openresponses/v1\n")}}
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "unused", nil),
			DefaultRouteSelector: "stub:x",
			Plugins:              plugins,
			Registry:             reg,
		},
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/openresponses/v1/responses",
		"/openresponses/v1/responses/compact",
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"stub:x","input":"ping"}`)))
		if w.Code == http.StatusOK {
			t.Fatalf("disabled OpenResponses frontend served %s: %d", path, w.Code)
		}
	}
}

func isolationRegistry(t *testing.T) *pluginreg.Registry {
	t.Helper()
	return testRegistryWithStdBundle(t)
}

func yamlNode(t *testing.T, s string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(s), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func mustSeen(t *testing.T, seen *sync.Map) any {
	t.Helper()
	v, ok := seen.Load("last")
	if !ok {
		t.Fatal("executor did not capture call")
	}
	return v
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
