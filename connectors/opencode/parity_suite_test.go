package opencode_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/opencode/internal/catalog"
	"github.com/aiproxer/aiproxer/connectors/opencode/internal/catalog/vendor"
	"github.com/aiproxer/aiproxer/connectors/opencode/internal/service"
	"github.com/aiproxer/aiproxer/connectors/opencode/internal/upstream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/conformance"
)

func TestDescribe_BothFactories(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]backendplugin.FactoryDescriptor{}
	for _, f := range d.Factories {
		kinds[f.Kind] = f
	}
	goF, ok := kinds[service.FactoryKindGo]
	if !ok || goF.ProcessSharing != backendplugin.ProcessSharingPerInstance || goF.CredentialMode != backendplugin.CredentialModeStatic {
		t.Fatalf("go=%+v", goF)
	}
	zenF, ok := kinds[service.FactoryKindZen]
	if !ok || zenF.ProcessSharing != backendplugin.ProcessSharingPerInstance {
		t.Fatalf("zen=%+v", zenF)
	}
}

func TestMediaCapabilitiesRequireCanonicalMediaMapping(t *testing.T) {
	t.Parallel()

	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, factory := range d.Factories {
		if factory.StaticCapabilities.Vision || factory.StaticCapabilities.Documents {
			t.Fatalf("factory %q advertises media without a canonical mapping: %+v", factory.Kind, factory.StaticCapabilities)
		}
	}

	srv, _ := opencodeModelServer(t, `{"data":[{"id":"emu-model"}]}`)
	//nolint:paralleltest // subtests share one httptest server whose handler mutates captured auth state
	for _, kind := range []string{service.FactoryKindGo, service.FactoryKindZen} {
		t.Run(kind, func(t *testing.T) {
			inst, err := service.New().Configure(context.Background(), mustCfg(t, kind,
				"base_url: "+srv.URL+"\n", "test-key"))
			if err != nil {
				t.Fatal(err)
			}
			profile, err := inst.Resolve(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if profile.Capabilities.Vision || profile.Capabilities.Documents {
				t.Fatalf("factory %q resolved media without a canonical mapping: %+v", kind, profile.Capabilities)
			}
			models, err := inst.ListModels(context.Background(), 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, model := range models.Models {
				if model.Capabilities.Vision || model.Capabilities.Documents {
					t.Fatalf("model %q advertises media without a canonical mapping: %+v", model.NativeModelID, model.Capabilities)
				}
			}
		})
	}
}

//nolint:paralleltest // t.Setenv modifies process-global environment
func TestConfigure_RequiresAPIKey(t *testing.T) {
	t.Setenv("OPENCODE_GO_API_KEY", "")
	t.Setenv("OPENCODE_ZEN_API_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	for _, kind := range []string{service.FactoryKindGo, service.FactoryKindZen} {
		t.Run(kind, func(t *testing.T) {
			_, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: kind, InstanceID: "i1", ConfigYAML: []byte("base_url: http://127.0.0.1:9\n"),
				Negotiation: backendplugin.Negotiation{Compatible: true},
			})
			if err == nil || !strings.Contains(err.Error(), "api_key") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestConfigure_DefaultBaseURLs(t *testing.T) {
	t.Parallel()
	goCfg, err := service.ParseConfigYAML(service.FactoryKindGo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if goCfg.BaseURL != service.DefaultGoURL {
		t.Fatalf("go base=%q", goCfg.BaseURL)
	}
	zenCfg, err := service.ParseConfigYAML(service.FactoryKindZen, nil)
	if err != nil {
		t.Fatal(err)
	}
	if zenCfg.BaseURL != service.DefaultZenURL {
		t.Fatalf("zen base=%q", zenCfg.BaseURL)
	}
}

func TestInventory_GoAndZenSeparate(t *testing.T) {
	t.Parallel()
	goSrv, goAuth := opencodeModelServer(t, `{"data":[{"id":"kimi-k2.7-code"}]}`)
	zenSrv, zenAuth := opencodeModelServer(t, `{"data":[{"id":"gpt-5.4"}]}`)
	goInst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindGo, "base_url: "+goSrv.URL+"\n", "go-key"))
	if err != nil {
		t.Fatal(err)
	}
	zenInst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindZen, "base_url: "+zenSrv.URL+"\n", "zen-key"))
	if err != nil {
		t.Fatal(err)
	}
	goResp, err := goInst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	zenResp, err := zenInst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if *goAuth != "Bearer go-key" {
		t.Fatalf("go auth=%q", *goAuth)
	}
	if *zenAuth != "Bearer zen-key" {
		t.Fatalf("zen auth=%q", *zenAuth)
	}
	if len(goResp.Models) != 1 || goResp.Models[0].NativeModelID != "kimi-k2.7-code" {
		t.Fatalf("go=%+v", goResp.Models)
	}
	if len(zenResp.Models) != 1 || zenResp.Models[0].NativeModelID != "gpt-5.4" {
		t.Fatalf("zen=%+v", zenResp.Models)
	}
	if goResp.InventorySource != service.FactoryKindGo || zenResp.InventorySource != service.FactoryKindZen {
		t.Fatalf("provenance go=%q zen=%q", goResp.InventorySource, zenResp.InventorySource)
	}
}

func TestParity_TwoGoInstancesNoGlobalLeak(t *testing.T) {
	t.Parallel()
	aSrv, aAuth := opencodeModelServer(t, `{"data":[{"id":"catalog-a"}]}`)
	bSrv, bAuth := opencodeModelServer(t, `{"data":[{"id":"catalog-b"}]}`)
	aInst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindGo,
		"base_url: "+aSrv.URL+"\n", "key-a"))
	if err != nil {
		t.Fatal(err)
	}
	bInst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindGo,
		"base_url: "+bSrv.URL+"\n", "key-b"))
	if err != nil {
		t.Fatal(err)
	}
	aResp, err := aInst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	bResp, err := bInst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if *aAuth != "Bearer key-a" || *bAuth != "Bearer key-b" {
		t.Fatalf("auth leak a=%q b=%q", *aAuth, *bAuth)
	}
	if aResp.Models[0].NativeModelID == bResp.Models[0].NativeModelID {
		t.Fatalf("catalog leak a=%+v b=%+v", aResp.Models, bResp.Models)
	}
}

func TestParity_GoRoutesOpenAIChat(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv, entries := flavorServerWithModels(t, &capture, catalog.BackendGo)
	router := upstream.NewRouter(catalog.BackendGo, srv.URL, "test-key", srv.Client())
	resolved, err := catalog.NewModelCatalog(catalog.BackendGo, entries, vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)).
		Resolve("moonshotai/kimi-k2.7-code")
	if err != nil {
		t.Fatal(err)
	}
	es, err := router.Open(context.Background(), nonStreamCall(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !hasText(drainEvents(t, es), "chat-ns-ok") {
		t.Fatal("expected chat response")
	}
	if !strings.HasSuffix(capture.Path, "/v1/chat/completions") {
		t.Fatalf("path=%q", capture.Path)
	}
	if capture.Authorization != "Bearer test-key" {
		t.Fatalf("auth=%q", capture.Authorization)
	}
	if !strings.HasPrefix(capture.OpenCodeSession, "aip-") {
		t.Fatalf("session=%q", capture.OpenCodeSession)
	}
}

func TestParity_GoRoutesAnthropicMessages(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv, entries := flavorServerWithModels(t, &capture, catalog.BackendGo)
	router := upstream.NewRouter(catalog.BackendGo, srv.URL, SyntheticAnthropicAPIKey, srv.Client())
	resolved, err := catalog.NewModelCatalog(catalog.BackendGo, entries, vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)).
		Resolve("minimax/minimax-m3")
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, mustOpen(t, router, resolved))
	if !strings.HasSuffix(capture.Path, "/v1/messages") {
		t.Fatalf("path=%q", capture.Path)
	}
	if capture.AnthropicAPIKey != SyntheticAnthropicAPIKey {
		t.Fatalf("x-api-key=%q", capture.AnthropicAPIKey)
	}
	if !strings.HasPrefix(capture.OpenCodeSession, "aip-") {
		t.Fatalf("session=%q", capture.OpenCodeSession)
	}
}

func TestParity_ZenRoutesResponses(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv, entries := flavorServerWithModels(t, &capture, catalog.BackendZen)
	router := upstream.NewRouter(catalog.BackendZen, srv.URL, "test-key", srv.Client())
	resolved, err := catalog.NewModelCatalog(catalog.BackendZen, entries, vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)).
		Resolve("openai/gpt-5.4")
	if err != nil {
		t.Fatal(err)
	}
	es, err := router.Open(context.Background(), nonStreamCall(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !hasText(drainEvents(t, es), "responses-ns-ok") {
		t.Fatal("expected responses output")
	}
	if !strings.HasSuffix(capture.Path, "/v1/responses") {
		t.Fatalf("path=%q", capture.Path)
	}
	if !strings.HasPrefix(capture.OpenCodeSession, "aip-") {
		t.Fatalf("session=%q", capture.OpenCodeSession)
	}
}

func TestParity_ZenRoutesGemini(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv, entries := flavorServerWithModels(t, &capture, catalog.BackendZen)
	router := upstream.NewRouter(catalog.BackendZen, srv.URL, "gemini-key", srv.Client())
	resolved, err := catalog.NewModelCatalog(catalog.BackendZen, entries, vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)).
		Resolve("google/gemini-3.1-pro")
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, mustOpen(t, router, resolved))
	if !strings.Contains(capture.Path, "generateContent") && !strings.Contains(capture.Path, "gemini") {
		t.Fatalf("path=%q", capture.Path)
	}
	if capture.GoogleAPIKey != "gemini-key" {
		t.Fatalf("google key=%q", capture.GoogleAPIKey)
	}
	if !strings.HasPrefix(capture.OpenCodeSession, "aip-") {
		t.Fatalf("session=%q", capture.OpenCodeSession)
	}
}

func TestParity_VendorFamiliesAccepted(t *testing.T) {
	t.Parallel()
	idx := vendor.NewSnapshotIndex(map[string]vendor.ModelFacts{
		"openai/gpt-5.4":            {Source: vendor.FactSourceCatalog},
		"anthropic/claude-sonnet-4": {Source: vendor.FactSourceCatalog},
		"google/gemini-3.1-pro":     {Source: vendor.FactSourceCatalog},
		"amazon/claude-sonnet-4":    {Source: vendor.FactSourceCatalog},
	})
	r := vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{Index: idx}, true)
	for _, id := range []string{
		"openai/gpt-5.4", "anthropic/claude-sonnet-4", "google/gemini-3.1-pro", "amazon/claude-sonnet-4",
	} {
		got := r.Resolve(id)
		if got.Kind == vendor.VendorResolveNoMatch || got.CanonicalID == "" {
			t.Fatalf("%s unresolved: %+v", id, got)
		}
	}
}

func TestParity_UnknownModelFails(t *testing.T) {
	t.Parallel()
	c := catalog.NewModelCatalog(catalog.BackendGo, goTestModels("http://unused"), vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true))
	_, err := c.Resolve("unknown/vendor-model")
	if err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("err=%v", err)
	}
}

func TestParity_ExecuteStreamingGo(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv := NewFlavorServer(t, &capture)
	inst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindGo,
		"base_url: "+srv.URL+"\nmodels:\n  - id: emu-model\n    endpoint: "+srv.URL+"/v1/chat/completions\n    ai_sdk_package: \"@ai-sdk/openai-compatible\"\n", "sk"))
	if err != nil {
		t.Fatal(err)
	}
	frames := mustExecute(t, inst, "opencode-go/emu-model", true)
	if !framesHaveText(frames, "chat-stream-ok") {
		t.Fatalf("frames=%v", frames)
	}
	if capture.Authorization != "Bearer sk" {
		t.Fatalf("auth=%q", capture.Authorization)
	}
	if capture.OpenCodeSession != "r1" {
		t.Fatalf("session=%q", capture.OpenCodeSession)
	}
}

func TestParity_ExecuteStreamingZen(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv := NewFlavorServer(t, &capture)
	inst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindZen,
		"base_url: "+srv.URL+"\nmodels:\n  - id: emu-model\n    endpoint: "+srv.URL+"/v1/responses\n    ai_sdk_package: \"@ai-sdk/openai\"\n", "sk"))
	if err != nil {
		t.Fatal(err)
	}
	frames := mustExecute(t, inst, "opencode-zen/emu-model", true)
	if !framesHaveText(frames, "responses-stream-ok") {
		t.Fatalf("frames=%v path=%q", frames, capture.Path)
	}
	if capture.OpenCodeSession != "r1" {
		t.Fatalf("session=%q", capture.OpenCodeSession)
	}
}

func TestParity_ConformanceGo(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv := NewFlavorServer(t, &capture)
	// SkipExecute: this fixture focuses on descriptor/inventory conformance;
	// dedicated TestParity_Execute* covers streaming on the text-only route.
	rep := conformance.RunWith(context.Background(), openaicompat.SecretBackedService{
		Service: service.New(),
		Secrets: backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("conformance-key")}},
	}, conformance.Options{
		FactoryKind: service.FactoryKindGo,
		ConfigYAML:  []byte("base_url: " + srv.URL + "\nmodels:\n  - id: emu-model\n    endpoint: " + srv.URL + "/v1/chat/completions\n"),
		SampleModel: "opencode-go/emu-model", DisableUsageRequirement: true, VisionInputOnly: true, SkipExecute: true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}

func TestParity_SessionHeaderForwarded(t *testing.T) {
	t.Parallel()

	t.Run("OpenAIChat", func(t *testing.T) {
		t.Parallel()
		var capture RequestCapture
		srv, entries := flavorServerWithModels(t, &capture, catalog.BackendGo)
		router := upstream.NewRouter(catalog.BackendGo, srv.URL, "test-key", srv.Client())
		resolved, err := catalog.NewModelCatalog(catalog.BackendGo, entries, vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)).
			Resolve("moonshotai/kimi-k2.7-code")
		if err != nil {
			t.Fatal(err)
		}
		call := nonStreamCall()
		call.Session.ClientSessionID = "custom-sess-chat"
		es, err := router.Open(context.Background(), call, resolved)
		if err != nil {
			t.Fatal(err)
		}
		drainEvents(t, es)
		if capture.OpenCodeSession != "custom-sess-chat" {
			t.Fatalf("expected custom-sess-chat, got %q", capture.OpenCodeSession)
		}
	})

	t.Run("OpenAIResponses", func(t *testing.T) {
		t.Parallel()
		var capture RequestCapture
		srv, entries := flavorServerWithModels(t, &capture, catalog.BackendZen)
		router := upstream.NewRouter(catalog.BackendZen, srv.URL, "test-key", srv.Client())
		resolved, err := catalog.NewModelCatalog(catalog.BackendZen, entries, vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)).
			Resolve("openai/gpt-5.4")
		if err != nil {
			t.Fatal(err)
		}
		call := nonStreamCall()
		call.Session.ClientSessionID = "custom-sess-resp"
		es, err := router.Open(context.Background(), call, resolved)
		if err != nil {
			t.Fatal(err)
		}
		drainEvents(t, es)
		if capture.OpenCodeSession != "custom-sess-resp" {
			t.Fatalf("expected custom-sess-resp, got %q", capture.OpenCodeSession)
		}
	})

	t.Run("AnthropicMessages", func(t *testing.T) {
		t.Parallel()
		var capture RequestCapture
		srv, entries := flavorServerWithModels(t, &capture, catalog.BackendGo)
		router := upstream.NewRouter(catalog.BackendGo, srv.URL, SyntheticAnthropicAPIKey, srv.Client())
		resolved, err := catalog.NewModelCatalog(catalog.BackendGo, entries, vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)).
			Resolve("minimax/minimax-m3")
		if err != nil {
			t.Fatal(err)
		}
		call := nonStreamCall()
		call.Session.ClientSessionID = "custom-sess-anthropic"
		es, err := router.Open(context.Background(), call, resolved)
		if err != nil {
			t.Fatal(err)
		}
		drainEvents(t, es)
		if capture.OpenCodeSession != "custom-sess-anthropic" {
			t.Fatalf("expected custom-sess-anthropic, got %q", capture.OpenCodeSession)
		}
	})

	t.Run("GoogleGemini", func(t *testing.T) {
		t.Parallel()
		var capture RequestCapture
		srv, entries := flavorServerWithModels(t, &capture, catalog.BackendZen)
		router := upstream.NewRouter(catalog.BackendZen, srv.URL, "gemini-key", srv.Client())
		resolved, err := catalog.NewModelCatalog(catalog.BackendZen, entries, vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)).
			Resolve("google/gemini-3.1-pro")
		if err != nil {
			t.Fatal(err)
		}
		call := nonStreamCall()
		call.Session.ClientSessionID = "custom-sess-gemini"
		es, err := router.Open(context.Background(), call, resolved)
		if err != nil {
			t.Fatal(err)
		}
		drainEvents(t, es)
		if capture.OpenCodeSession != "custom-sess-gemini" {
			t.Fatalf("expected custom-sess-gemini, got %q", capture.OpenCodeSession)
		}
	})
}

func TestParity_OmenAlphaStealthExecution(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv := NewFlavorServer(t, &capture)
	inst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindGo,
		"base_url: "+srv.URL+"\n", "sk-omen"))
	if err != nil {
		t.Fatal(err)
	}

	frames := mustExecute(t, inst, "opencode-go/omen-alpha", true)
	if !framesHaveText(frames, "chat-stream-ok") {
		t.Fatalf("frames=%v", frames)
	}
	if !strings.HasSuffix(capture.Path, "/v1/chat/completions") {
		t.Fatalf("expected chat completions endpoint, got %q", capture.Path)
	}
	if capture.Authorization != "Bearer sk-omen" {
		t.Fatalf("expected Bearer sk-omen, got %q", capture.Authorization)
	}
	if capture.OpenCodeSession == "" {
		t.Fatal("expected x-opencode-session header to be set")
	}
	var reqBody map[string]any
	if err := json.Unmarshal(capture.Body, &reqBody); err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}
	if reqBody["model"] != "omen-alpha" {
		t.Fatalf("expected wire model 'omen-alpha', got %v", reqBody["model"])
	}
	for _, forbidden := range []string{"reasoning", "reasoning_effort", "thinking"} {
		if _, exists := reqBody[forbidden]; exists {
			t.Fatalf("forbidden key %q found in request body", forbidden)
		}
	}
}

func TestParity_LegacyAliasKimiK27(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv := NewFlavorServer(t, &capture)
	inst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindGo,
		"base_url: "+srv.URL+"\nmodels:\n  - id: kimi-k2.7-code\n    endpoint: "+srv.URL+"/v1/chat/completions\n    ai_sdk_package: \"@ai-sdk/openai-compatible\"\n", "sk"))
	if err != nil {
		t.Fatal(err)
	}
	frames := mustExecute(t, inst, "opencode-go/kimi-k2.7", true)
	if !framesHaveText(frames, "chat-stream-ok") {
		t.Fatalf("frames=%v", frames)
	}
	var reqBody map[string]any
	if err := json.Unmarshal(capture.Body, &reqBody); err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}
	if reqBody["model"] != "kimi-k2.7-code" {
		t.Fatalf("expected wire model 'kimi-k2.7-code', got %v", reqBody["model"])
	}
}

func TestParity_ConformanceZen(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv := NewFlavorServer(t, &capture)
	rep := conformance.RunWith(context.Background(), openaicompat.SecretBackedService{
		Service: service.New(),
		Secrets: backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("conformance-key")}},
	}, conformance.Options{
		FactoryKind: service.FactoryKindZen,
		ConfigYAML:  []byte("base_url: " + srv.URL + "\nmodels:\n  - id: emu-model\n    endpoint: " + srv.URL + "/v1/responses\n"),
		SampleModel: "opencode-zen/emu-model", DisableUsageRequirement: true, VisionInputOnly: true, SkipExecute: true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}

func mustCfg(t *testing.T, kind, yaml string, secrets ...string) backendplugin.ConfigureRequest {
	t.Helper()
	values := make(map[string][]byte, len(secrets))
	for i, secret := range secrets {
		name := "api_key"
		if i > 0 {
			name = "api_key_" + strconv.Itoa(i+1)
		}
		values[name] = []byte(secret)
	}
	return backendplugin.ConfigureRequest{
		FactoryKind: kind, InstanceID: "i1", ConfigYAML: []byte(yaml),
		Secrets:       backendplugin.SecretBundle{Values: values},
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	}
}

func opencodeModelServer(t *testing.T, body string) (*httptest.Server, *string) {
	t.Helper()
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

func goTestModels(base string) []catalog.ModelEntry {
	return []catalog.ModelEntry{
		{RawID: "kimi-k2.7-code", Endpoint: base + "/v1/chat/completions", AISDKPackage: "@ai-sdk/openai-compatible"},
		{RawID: "minimax-m3", Endpoint: base + "/v1/messages", AISDKPackage: "@ai-sdk/anthropic"},
	}
}

func zenTestModels(base string) []catalog.ModelEntry {
	return []catalog.ModelEntry{
		{RawID: "gpt-5.4", Endpoint: base + "/v1/responses", AISDKPackage: "@ai-sdk/openai"},
		{RawID: "gemini-3.1-pro", Endpoint: base + "/v1beta/models/gemini-3.1-pro", AISDKPackage: "@ai-sdk/google"},
	}
}

func flavorServerWithModels(t *testing.T, capture *RequestCapture, kind catalog.BackendKind) (*httptest.Server, []catalog.ModelEntry) {
	t.Helper()
	if capture == nil {
		capture = &RequestCapture{}
	}
	srv := NewFlavorServer(t, capture)
	switch kind {
	case catalog.BackendZen:
		return srv, zenTestModels(srv.URL)
	default:
		return srv, goTestModels(srv.URL)
	}
}

func mustOpen(t *testing.T, router *upstream.Router, resolved catalog.ResolvedModel) aipapi.ManagedEventStream {
	t.Helper()
	es, err := router.Open(context.Background(), nonStreamCall(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func nonStreamCall() aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}},
		Invocation: aipapi.Invocation{
			DeliveryMode: aipapi.DeliveryModeNonStreaming, TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
}

func drainEvents(t *testing.T, es aipapi.ManagedEventStream) []aipapi.Event {
	t.Helper()
	defer func() { _ = es.Close() }()
	out := []aipapi.Event{}
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		out = append(out, ev)
	}
}

func hasText(events []aipapi.Event, want string) bool {
	for _, ev := range events {
		if ev.Kind == aipapi.EventTextDelta && strings.Contains(ev.Delta, want) {
			return true
		}
	}
	return false
}

type memExecuteStream struct {
	ctx    context.Context
	inbox  []backendplugin.ClientFrame
	outbox []backendplugin.ServerFrame
	ri     int
}

func (m *memExecuteStream) Context() context.Context { return m.ctx }
func (m *memExecuteStream) Recv() (backendplugin.ClientFrame, error) {
	if m.ri >= len(m.inbox) {
		return backendplugin.ClientFrame{}, io.EOF
	}
	f := m.inbox[m.ri]
	m.ri++
	return f, nil
}

func (m *memExecuteStream) Send(frame backendplugin.ServerFrame) error {
	m.outbox = append(m.outbox, frame)
	return nil
}

func mustExecute(t *testing.T, inst backendplugin.ConfiguredInstance, model string, streaming bool) []backendplugin.ServerFrame {
	t.Helper()
	text := "hi"
	inv := backendplugin.Invocation{
		RequestID: "r1", AttemptID: "a1", ALegID: "aleg", BLegID: "bleg",
		CanonicalModelID: model,
		Messages: []backendplugin.Message{{
			Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	call := aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
	if streaming {
		call.Invocation.DeliveryMode = aipapi.DeliveryModeStreaming
		call.Invocation.TransportMode = aipapi.TransportModeStreaming
	}
	if err := backendplugin.ApplyCallWireMetadata(&inv, call, nil); err != nil {
		t.Fatal(err)
	}
	ms := &memExecuteStream{
		ctx: context.Background(),
		inbox: []backendplugin.ClientFrame{{
			Kind: backendplugin.ClientFrameStart, InstanceID: "i1", Invocation: &inv,
		}},
	}
	if err := inst.Execute(ms); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return ms.outbox
}

func framesHaveText(frames []backendplugin.ServerFrame, want string) bool {
	for _, f := range frames {
		if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil &&
			f.Event.Kind == backendplugin.EventTextDelta && f.Event.Delta != nil &&
			strings.Contains(*f.Event.Delta, want) {
			return true
		}
	}
	return false
}
