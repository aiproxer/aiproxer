package runtimebundle_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// TestDefaultRoute_ExecutorRunsTheAdvertisedRoute proves the route the
// operator read model advertises is the route the executor runs.
//
// The executor expands every selector it receives with the model aliases,
// including the default route the frontends fall back to. The default route
// must therefore reach the executor unexpanded: the executor applies the alias
// table to it once, exactly as it does to a client selector, and the route the
// operator is told about is that one. A second expansion would let another
// alias rule rewrite the route, sending the request to a backend the operator
// was never told about.
//
// A client model with no route prefix falls back to the default route, so the
// upstream that serves the request and the model it receives show the route
// the executor actually ran.
func TestDefaultRoute_ExecutorRunsTheAdvertisedRoute(t *testing.T) {
	t.Parallel()

	var (
		wireChatCalls atomic.Int32
		altChatCalls  atomic.Int32
		lastWireModel atomic.Pointer[string]
	)

	wireUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			lastWireModel.Store(&text)
		}
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"good-a"},{"id":"good-b"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			wireChatCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-route\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer wireUpstream.Close()

	altUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"model-x"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			altChatCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-alt\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer altUpstream.Close()

	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
routing:
  max_attempts: 1
  default_route: "wire-backend:good-a"
model_aliases:
  - pattern: "^wire-backend:good-a$"
    replacement: "wire-backend:good-b"
  - pattern: "^wire-backend:good-b$"
    replacement: "alt-backend:model-x"
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
    - kind: custom-openai-legacy-compatible
      id: alt-backend
      enabled: true
      config:
        backend_prefix: alt-backend
        base_url: %q
`, wireUpstream.URL+"/v1", altUpstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "default-route-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
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

	// The operator read model: the route the operator is told about.
	snap, err := runtimebundle.InspectRoutes(t.Context(), runtimebundle.InspectInput{
		ConfigPath: cfgPath,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatalf("InspectRoutes: %v", err)
	}
	const wantRoute = "wire-backend:good-b"
	if snap.EffectiveDefaultRoute != wantRoute {
		t.Fatalf("advertised default route = %q want %q", snap.EffectiveDefaultRoute, wantRoute)
	}

	// A client model with no route prefix falls back to the default route.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"plain-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if wireChatCalls.Load() == 0 {
		t.Fatalf("the advertised backend served no request (alt chat calls=%d)", altChatCalls.Load())
	}
	if altChatCalls.Load() != 0 {
		t.Fatalf("a backend the operator was not told about served %d requests", altChatCalls.Load())
	}
	body := lastWireModel.Load()
	if body == nil {
		t.Fatal("the advertised backend received no request body")
	}
	if !strings.Contains(*body, `"model":"good-b"`) {
		t.Fatalf("upstream body = %s want the advertised model", *body)
	}
	if strings.Contains(*body, "model-x") {
		t.Fatalf("upstream body = %s: the executor ran a route the operator was not told about", *body)
	}
}

// TestDefaultRoute_DiscoveredInventoryIsTheAdvertisedRoute proves a deployment
// whose backend discovers its models needs no model id in the configuration.
//
// The route the operator read model advertises is the default model the
// instance published in its runtime inventory, and the executor runs that
// route for a client that omits a route prefix. The model id stays the one the
// backend serves: the registry binds the canonical id to the native one.
func TestDefaultRoute_DiscoveredInventoryIsTheAdvertisedRoute(t *testing.T) {
	t.Parallel()

	var (
		chatCalls     atomic.Int32
		lastWireModel atomic.Pointer[string]
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			lastWireModel.Store(&text)
		}
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"good-a"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			chatCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-route\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
routing:
  max_attempts: 1
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
`, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "discovered-default-route-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
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

	// The operator read model: the route the operator is told about.
	snap, err := runtimebundle.InspectRoutes(t.Context(), runtimebundle.InspectInput{
		ConfigPath: cfgPath,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatalf("InspectRoutes: %v", err)
	}
	const wantRoute = "wire-backend:wire-backend/good-a"
	if snap.EffectiveDefaultRoute != wantRoute {
		t.Fatalf("advertised default route = %q want %q", snap.EffectiveDefaultRoute, wantRoute)
	}
	if snap.EffectiveDefaultRouteModel != "wire-backend/good-a" {
		t.Fatalf("advertised default route model = %q", snap.EffectiveDefaultRouteModel)
	}
	if snap.EffectiveDefaultRouteSource != "discovered_inventory" {
		t.Fatalf("advertised default route source = %q", snap.EffectiveDefaultRouteSource)
	}

	// A client model with no route prefix falls back to the default route.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"plain-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if chatCalls.Load() == 0 {
		t.Fatal("the discovered default route served no request")
	}
	body := lastWireModel.Load()
	if body == nil {
		t.Fatal("the backend received no request body")
	}
	if !strings.Contains(*body, `"model":"good-a"`) {
		t.Fatalf("upstream body = %s want the discovered model", *body)
	}
}

// TestDefaultRoute_InventoryRefreshKeepsTheDefaultModelBinding proves a
// generation keeps the model binding its default route needs when a later
// inventory refresh withdraws that model.
//
// The generation's default route is fixed at compile time, while the model
// registry keeps publishing refreshed inventories. Without the generation-owned
// binding, a refresh that withdrew the model would leave the route unbound and
// the proxy would send the canonical id as the wire model: an id the backend
// never published. The generation keeps the mapping it was compiled with, so
// the backend receives the model id it published and reports an unavailable
// model itself.
func TestDefaultRoute_InventoryRefreshKeepsTheDefaultModelBinding(t *testing.T) {
	t.Parallel()

	var (
		chatCalls     atomic.Int32
		lastWireModel atomic.Pointer[string]
		published     atomic.Pointer[string]
	)
	published.Store(new("good-a"))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			lastWireModel.Store(&text)
		}
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"object":"list","data":[{"id":%q}]}`, *published.Load())
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			chatCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-route\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
routing:
  max_attempts: 1
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
`, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "discovered-default-route-refresh-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
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

	// The upstream withdraws the model the generation's default route names.
	published.Store(new("good-b"))
	registry := runtimebundle.HostModelRegistryRuntime(host)
	if registry == nil {
		t.Fatal("expected the active generation to own a model registry runtime")
	}
	registry.RunRefresh(t.Context())

	// The generation is not rebuilt: the request still uses its default route.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"plain-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if chatCalls.Load() == 0 {
		t.Fatal("the discovered default route served no request")
	}
	body := lastWireModel.Load()
	if body == nil {
		t.Fatal("the backend received no request body")
	}
	if !strings.Contains(*body, `"model":"good-a"`) {
		t.Fatalf("upstream body = %s want the model id the backend published", *body)
	}
	if strings.Contains(*body, "wire-backend/good-a") {
		t.Fatalf("upstream body = %s: the canonical id must not be sent as the wire model", *body)
	}
}

// TestDefaultRoute_ExplicitRouteUsesTheCurrentRegistry proves an explicit client
// route is resolved against the current request-bound registry, even when it
// names the same backend and model as the generation's default route.
//
// The frozen binding belongs to the route the proxy chose. A client that names
// a route keeps the current registry's answer: a remapped canonical id
// resolves to its current native id, and a model the registry no longer knows
// is not silently rebound to a stale one.
func TestDefaultRoute_ExplicitRouteUsesTheCurrentRegistry(t *testing.T) {
	t.Parallel()

	var (
		chatCalls     atomic.Int32
		lastWireModel atomic.Pointer[string]
		published     atomic.Pointer[string]
	)
	published.Store(new("good-a"))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			lastWireModel.Store(&text)
		}
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"object":"list","data":[{"id":%q}]}`, *published.Load())
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			chatCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-route\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
routing:
  max_attempts: 1
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
`, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "explicit-route-refresh-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
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

	// The upstream withdraws the model the generation's default route names.
	published.Store(new("good-b"))
	registry := runtimebundle.HostModelRegistryRuntime(host)
	if registry == nil {
		t.Fatal("expected the active generation to own a model registry runtime")
	}
	registry.RunRefresh(t.Context())

	// An explicit client route names the same backend and model as the default
	// route, and must use the current registry.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"wire-backend:wire-backend/good-a","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if chatCalls.Load() == 0 {
		t.Fatal("the explicit route served no request")
	}
	body := lastWireModel.Load()
	if body == nil {
		t.Fatal("the backend received no request body")
	}
	if strings.Contains(*body, `"model":"good-a"`) {
		t.Fatalf("upstream body = %s: an explicit route must not use the frozen binding", *body)
	}
}

// TestDefaultRoute_CompositeAliasKeepsEveryLeafBinding proves a default route
// an operator expanded into a composite route keeps the bindings of every leaf.
//
// The expansion is operator-authored, so the proxy runs it: each leaf is a
// model the default route can address, and each keeps the mapping the
// generation was compiled with.
func TestDefaultRoute_CompositeAliasKeepsEveryLeafBinding(t *testing.T) {
	t.Parallel()

	var (
		chatCalls     atomic.Int32
		lastWireModel atomic.Pointer[string]
		published     atomic.Pointer[string]
	)
	published.Store(new("good-a"))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			lastWireModel.Store(&text)
		}
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"object":"list","data":[{"id":%q}]}`, *published.Load())
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			chatCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-route\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
routing:
  max_attempts: 1
model_aliases:
  - pattern: "^wire-backend:wire-backend/good-a$"
    replacement: "wire-backend:wire-backend/good-a|wire-backend:wire-backend/good-b"
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
`, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "composite-alias-refresh-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
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

	// The upstream withdraws the model the default route's first leaf names.
	published.Store(new("good-c"))
	registry := runtimebundle.HostModelRegistryRuntime(host)
	if registry == nil {
		t.Fatal("expected the active generation to own a model registry runtime")
	}
	registry.RunRefresh(t.Context())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"plain-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if chatCalls.Load() == 0 {
		t.Fatal("the derived default route served no request")
	}
	body := lastWireModel.Load()
	if body == nil {
		t.Fatal("the backend received no request body")
	}
	if !strings.Contains(*body, `"model":"good-a"`) {
		t.Fatalf("upstream body = %s want the model id the backend published", *body)
	}
}
