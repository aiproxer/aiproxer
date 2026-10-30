package standardplugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/downstreamcacheaffinity"
	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestExpandProviderProfileRows_bindsCatalogDataWithoutChangingCustomRows(t *testing.T) {
	t.Parallel()
	var profileNode, customNode yaml.Node
	if err := yaml.Unmarshal([]byte("profile: groq\n"), &profileNode); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte("backend_prefix: private\nbase_url: https://private.example/v1\n"), &customNode); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
		{ID: "profile-instance", Kind: ProviderProfileKind, Enabled: true, Config: profileNode},
		{ID: "custom-instance", Kind: CustomOpenAIResponsesCompatibleID, Enabled: true, Config: customNode},
	}}}
	before := cfg.Plugins.Backends[1].Config.Value
	profileBefore := cfg.Plugins.Backends[0].Config.Value
	prepared, err := ExpandProviderProfileRows(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Plugins.Backends[0].Kind != ProviderProfileKind {
		t.Fatalf("source profile kind=%q", cfg.Plugins.Backends[0].Kind)
	}
	if cfg.Plugins.Backends[0].Config.Value != profileBefore {
		t.Fatal("source profile config changed")
	}
	if prepared.Plugins.Backends[0].Kind != CustomOpenAIResponsesCompatibleID {
		t.Fatalf("profile family kind=%q", prepared.Plugins.Backends[0].Kind)
	}
	if prepared.Plugins.Backends[1].Kind != CustomOpenAIResponsesCompatibleID {
		t.Fatal("custom row changed kind")
	}
	if !bytes.Equal([]byte(before), []byte(cfg.Plugins.Backends[1].Config.Value)) {
		t.Fatal("custom row config changed")
	}
}

func TestProviderProfile_ProductionRegistryPath_PromptCacheKeyQuirk(t *testing.T) {
	run := func(t *testing.T, family providerprofiles.Family, forward, custom bool, profile *providerprofiles.Profile) {
		t.Helper()
		for _, key := range []string{"profile-key", downstreamcacheaffinity.Derive("safe-header-instance", "session")} {
			t.Run(key, func(t *testing.T) {
				testProviderProfileSafeHeadersAndCacheKey(t, family, forward, key, profile, custom)
			})
		}
	}
	for _, family := range []providerprofiles.Family{providerprofiles.FamilyOpenAIChat, providerprofiles.FamilyOpenAIResponses} {
		for _, mode := range []struct {
			name            string
			forward, custom bool
		}{{"unquirked", false, false}, {"quirked", true, false}, {"custom", false, true}} {
			t.Run(string(family)+"/"+mode.name, func(t *testing.T) {
				run(t, family, mode.forward, mode.custom, nil)
			})
		}
	}
	for _, id := range []string{"groq", "deepseek-openai", "alibaba"} {
		t.Run(id, func(t *testing.T) {
			profile, err := providerprofiles.EmbeddedProfile(id)
			require.NoError(t, err)
			run(t, profile.Family, false, false, &profile)
		})
	}
}

func TestExpandProviderProfileRows_rejectsUnknownProfileBeforeActivation(t *testing.T) {
	t.Parallel()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile_id: does-not-exist\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{ID: "missing", Kind: ProviderProfileKind, Enabled: true, Config: node}}}}
	if _, err := ExpandProviderProfileRows(cfg); err == nil {
		t.Fatal("unknown profile accepted")
	}
}

func TestBuildProviderProfileBackend_certifiesExecutableMapping(t *testing.T) {
	t.Setenv("PROFILE_CERT_KEY", "profile-secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path=%q, want /v1/responses", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request JSON: %v; body=%s", err, body)
		}
		if request.Model != "native-model" {
			t.Errorf("request model mapping=%q, want native-model", request.Model)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer profile-secret" {
			t.Errorf("authorization=%q", got)
		}
		if got := r.Header.Get("X-Provider-Client"); got != "profile-cert" {
			t.Errorf("safe header=%q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"profile-response","object":"response","created_at":1,"status":"completed","model":"native-model","output":[{"type":"message","id":"message-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"profile-ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(server.Close)

	profile := providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "certified-profile",
		Family:     providerprofiles.FamilyOpenAIResponses,
		Endpoint:   providerprofiles.Endpoint{BaseURL: server.URL + "/v1", PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "PROFILE_CERT_KEY"},
		Models: providerprofiles.ModelDiscovery{
			Policy:    providerprofiles.DiscoveryStatic,
			Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
		},
		Tokenizer: providerprofiles.TokenizerAccounting{TokenizerID: "cl100k_base", Source: providerprofiles.AccountingLocalTokenizer},
	}
	profile.Headers = []providerprofiles.SafeHeader{{Name: "X-Provider-Client", Value: "profile-cert"}}
	profile.Models.Static = []providerprofiles.Model{{CanonicalID: "certified-profile/model", NativeID: "native-model", DisplayName: "Certified model"}}
	compiled, err := CompileProviderProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := BuildProviderProfileBackend(compiled, "profile-instance", server.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	models, err := backend.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models.Models) != 1 || models.Models[0].NativeID != "native-model" || models.Models[0].CanonicalID != "certified-profile/model" {
		t.Fatalf("model mapping=%+v", models.Models)
	}
	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenAIResponses,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "profile-instance", Model: "native-model"}})
	if err != nil {
		t.Fatal(err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Text.String() != "profile-ok" {
		t.Fatalf("mapped response text=%q", collected.Text.String())
	}
}

func TestBuildProviderProfileBackend_anthropicV1ModelsQuirkUsesProfilePath(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/provider/model-catalog":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-profile","display_name":"Profile Claude"}]}`)
		case "/v1/messages":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_start\ndata: "+
				`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-profile","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}`+
				"\n\n"+
				"event: content_block_start\ndata: "+
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+
				"\n\n"+
				"event: content_block_delta\ndata: "+
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`+
				"\n\n"+
				"event: content_block_stop\ndata: "+
				`{"type":"content_block_stop","index":0}`+
				"\n\n"+
				"event: message_delta\ndata: "+
				`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":1,"output_tokens":1}}`+
				"\n\n"+
				"event: message_stop\ndata: "+
				`{"type":"message_stop"}`+
				"\n\n")
		default:
			t.Errorf("unexpected path=%q", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	profile := providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "anthropic-profile",
		Family:     providerprofiles.FamilyAnthropic,
		Endpoint:   providerprofiles.Endpoint{BaseURL: server.URL, PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthNone},
		Models: providerprofiles.ModelDiscovery{
			Policy:    providerprofiles.DiscoveryFamilyDefault,
			Path:      "/provider/model-catalog",
			Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
		},
		Tokenizer: providerprofiles.TokenizerAccounting{Source: providerprofiles.AccountingLocalTokenizer},
		Quirks:    []providerprofiles.QuirkID{providerprofiles.QuirkAnthropicV1Models},
	}
	compiled, err := CompileProviderProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := BuildProviderProfileBackend(compiled, "anthropic-profile-instance", server.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	models, err := backend.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models.Models) != 1 || models.Models[0].NativeID != "claude-profile" || models.Models[0].CanonicalID != "anthropic-profile/claude-profile" {
		t.Fatalf("models=%+v", models.Models)
	}

	call := aipapi.Call{
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Invocation: aipapi.Invocation{DeliveryMode: aipapi.DeliveryModeNonStreaming, TransportMode: aipapi.TransportModeNonStreaming},
	}
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic-profile-instance", Model: "claude-profile"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
}

func TestBuildProviderProfileBackend_anthropicV1ModelsQuirkRejectsEncodedTraversal(t *testing.T) {
	t.Parallel()
	profile := providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "anthropic-profile",
		Family:     providerprofiles.FamilyAnthropic,
		Endpoint:   providerprofiles.Endpoint{BaseURL: "https://api.example.invalid", PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthNone},
		Models: providerprofiles.ModelDiscovery{
			Policy:    providerprofiles.DiscoveryFamilyDefault,
			Path:      "/%2e%2e/models",
			Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
		},
		Quirks: []providerprofiles.QuirkID{providerprofiles.QuirkAnthropicV1Models},
	}
	if err := providerprofiles.Validate(profile); err == nil {
		t.Fatal("encoded traversal accepted")
	}
}

func TestProjectProviderProfileDiagnostics_returnsCatalogRows(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
		{ID: "prof-1", Kind: "provider-profile", Enabled: true},
	}}}
	diags := ProjectProviderProfileDiagnostics(cfg)
	if len(diags) == 0 {
		t.Fatal("expected diagnostic rows for provider-profile backend")
	}
	if diags[0].Origin != "embedded_provider_profile_catalog" {
		t.Fatalf("origin=%q", diags[0].Origin)
	}
}

func TestProviderProfile_ProductionRegistryPath_PreservesCapabilityCeilingAndZeroUpstreamWork(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "groq-test-key")
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-1","output":[]}`)
	}))
	t.Cleanup(server.Close)

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile: groq\n"), &node); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{
					ID:      "groq-instance",
					Kind:    ProviderProfileKind,
					Enabled: true,
					Config:  node,
				},
			},
		},
	}

	sourceKindBefore := cfg.Plugins.Backends[0].Kind
	sourceConfigBefore := cfg.Plugins.Backends[0].Config.Value

	prepared, err := PrepareProviderProfiles(cfg)
	if err != nil {
		t.Fatalf("PrepareProviderProfiles: %v", err)
	}

	// 1. Assert source config immutability
	if cfg.Plugins.Backends[0].Kind != sourceKindBefore {
		t.Fatalf("source config kind mutated: got %q, want %q", cfg.Plugins.Backends[0].Kind, sourceKindBefore)
	}
	if cfg.Plugins.Backends[0].Config.Value != sourceConfigBefore {
		t.Fatal("source config value mutated")
	}

	// 2. Drive through same backend registry lifecycle used by candidate compilation
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatalf("InstallStandardBundleOn: %v", err)
	}

	backendRow := prepared.Plugins.Backends[0]
	res, err := prepared.BuildBackendWithLifecycle(reg,
		backendRow.FactoryID(),
		backendRow.InstanceID(),
		backendRow.Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle: %v", err)
	}
	backend := res.Backend

	// Assert backend prefix matches profile ID
	if !slices.Contains(backend.BackendPrefixes, "groq") {
		t.Fatalf("expected backend prefix 'groq', got %v", backend.BackendPrefixes)
	}

	// 3. Prove compiled disabled capabilities are preserved (vision, documents, reasoning, parallel_tool_calls disabled)
	if _, ok := backend.Caps[aipapi.CapabilityVision]; ok {
		t.Fatalf("expected CapabilityVision to be disabled in backend.Caps by compiled profile, but it was enabled")
	}
	if _, ok := backend.Caps[aipapi.CapabilityDocuments]; ok {
		t.Fatalf("expected CapabilityDocuments to be disabled in backend.Caps by compiled profile, but it was enabled")
	}
	if _, ok := backend.Caps[aipapi.CapabilityReasoning]; ok {
		t.Fatalf("expected CapabilityReasoning to be disabled in backend.Caps by compiled profile, but it was enabled")
	}

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind:      aipapi.PartImageRef,
				ImageRef:  "https://example.com/test.png",
				ImageMIME: "image/png",
			}},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "groq-instance", Model: "groq-model"},
	}
	resolved := backend.ResolveCaps(context.Background(), call, cand)
	if _, ok := resolved[aipapi.CapabilityVision]; ok {
		t.Fatalf("expected CapabilityVision to be disabled in resolved caps, but it was enabled")
	}

	// 4. Demonstrate zero upstream work when a disabled required capability is rejected
	ex := runtime.TestExecutor()
	ex.Backends = map[string]execbackend.Backend{"groq-instance": backend}
	callWithRoute := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "groq-instance:groq-model"},
		Messages: call.Messages,
	}
	_, execErr := ex.Execute(context.Background(), callWithRoute)
	if execErr == nil {
		t.Fatal("expected execution error when required capability (vision) is disabled")
	}
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("expected zero upstream calls on rejected capability, got %d", calls)
	}
}

func TestProviderProfile_ProductionRegistryPath_PreservesBoundedSafeHeaders(t *testing.T) {
	testProviderProfileSafeHeadersAndCacheKey(t, providerprofiles.FamilyOpenAIResponses, false, "profile-key", nil, false)
}

func testProviderProfileSafeHeadersAndCacheKey(t *testing.T, family providerprofiles.Family, forward bool, key string, embedded *providerprofiles.Profile, custom bool) {
	t.Helper()
	t.Setenv("SAFE_HEADER_KEY", "secret-key")
	var receivedClientHeader atomic.Pointer[string]
	bodies := make(chan map[string]json.RawMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies <- body
		h := r.Header.Get("X-Provider-Client")
		receivedClientHeader.Store(&h)
		w.Header().Set("Content-Type", "application/json")
		if family == providerprofiles.FamilyOpenAIChat {
			_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		} else {
			_, _ = io.WriteString(w, `{"id":"resp-header","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
		}
	}))
	t.Cleanup(server.Close)

	testProfile := providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "safe-header-profile",
		Family:     family,
		Endpoint:   providerprofiles.Endpoint{BaseURL: server.URL + "/v1", PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "SAFE_HEADER_KEY"},
		Headers:    []providerprofiles.SafeHeader{{Name: "X-Provider-Client", Value: "aip-safe-client"}},
		Models: providerprofiles.ModelDiscovery{
			Policy:    providerprofiles.DiscoveryStatic,
			Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
			Static:    []providerprofiles.Model{{CanonicalID: "safe-header-profile/m1", NativeID: "m1", DisplayName: "Model 1"}},
		},
	}
	if forward {
		testProfile.Quirks = []providerprofiles.QuirkID{providerprofiles.QuirkOpenAIPromptCacheKey}
	}
	if embedded != nil {
		// Replace external I/O only; retain embedded family, quirks and capability ceiling.
		endpoint, auth, models := testProfile.Endpoint.BaseURL, testProfile.Auth, testProfile.Models
		testProfile = *embedded
		testProfile.Endpoint.BaseURL, testProfile.Auth, testProfile.Models = endpoint, auth, models
	}
	catalog, err := providerprofiles.NewCatalog([]providerprofiles.Profile{testProfile})
	if err != nil {
		t.Fatal(err)
	}

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile: "+testProfile.ID+"\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "safe-header-instance", Kind: ProviderProfileKind, Enabled: true, Config: node},
			},
		},
	}

	prepared, err := PrepareProviderProfilesWithCatalog(cfg, catalog)
	if err != nil {
		t.Fatalf("PrepareProviderProfilesWithCatalog: %v", err)
	}

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	backendRow := prepared.Plugins.Backends[0]
	if custom {
		// Use the actual unmarked custom registry factory, not a profile constructor.
		backendRow.Kind = CustomOpenAIResponsesCompatibleID
		if family == providerprofiles.FamilyOpenAIChat {
			backendRow.Kind = CustomOpenAILegacyCompatibleID
		}
		require.NoError(t, yaml.Unmarshal([]byte("backend_prefix: custom\nbase_url: "+server.URL+"/v1\n"), &backendRow.Config))
	}
	res, err := prepared.BuildBackendWithLifecycle(reg,
		backendRow.FactoryID(),
		backendRow.InstanceID(),
		backendRow.Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle: %v", err)
	}
	backend := res.Backend

	op := aipapi.OperationOpenAIResponses
	if family == providerprofiles.FamilyOpenAIChat {
		op = aipapi.OperationOpenAIChatCompletions
	}
	call := aipapi.Call{
		PromptCacheKey: key,
		Messages:       []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
		Invocation: aipapi.Invocation{
			Operation:     op,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "safe-header-instance", Model: "m1"}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	value, present := (<-bodies)["prompt_cache_key"]
	if present != forward || (forward && string(value) != fmt.Sprintf("%q", key)) {
		t.Fatalf("prompt_cache_key=%s, present=%t; want forwarding=%t", value, present, forward)
	}
	hdr := receivedClientHeader.Load()
	if embedded != nil || custom {
		return
	}
	if hdr == nil || *hdr != "aip-safe-client" {
		got := "<nil>"
		if hdr != nil {
			got = *hdr
		}
		t.Fatalf("expected header X-Provider-Client: 'aip-safe-client', got %q", got)
	}
}

func TestProviderProfile_ProductionRegistryPath_AnthropicAlternateModelPathQuirk(t *testing.T) {
	t.Setenv("ANTHROPIC_TEST_KEY", "anthropic-key")
	var requestedPath atomic.Pointer[string]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		requestedPath.Store(&path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-alt-model","display_name":"Alt Claude"}]}`)
	}))
	t.Cleanup(server.Close)

	testProfile := providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "anthropic-alt-path-profile",
		Family:     providerprofiles.FamilyAnthropic,
		Endpoint:   providerprofiles.Endpoint{BaseURL: server.URL, PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthAPIKeyEnv, EnvVar: "ANTHROPIC_TEST_KEY"},
		Models: providerprofiles.ModelDiscovery{
			Policy:    providerprofiles.DiscoveryFamilyDefault,
			Path:      "/provider/model-catalog",
			Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
		},
		Quirks: []providerprofiles.QuirkID{providerprofiles.QuirkAnthropicV1Models},
	}
	catalog, err := providerprofiles.NewCatalog([]providerprofiles.Profile{testProfile})
	if err != nil {
		t.Fatal(err)
	}

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile: anthropic-alt-path-profile\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "anthropic-alt-instance", Kind: ProviderProfileKind, Enabled: true, Config: node},
			},
		},
	}

	prepared, err := PrepareProviderProfilesWithCatalog(cfg, catalog)
	if err != nil {
		t.Fatalf("PrepareProviderProfilesWithCatalog: %v", err)
	}

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	backendRow := prepared.Plugins.Backends[0]
	res, err := prepared.BuildBackendWithLifecycle(reg,
		backendRow.FactoryID(),
		backendRow.InstanceID(),
		backendRow.Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle: %v", err)
	}
	backend := res.Backend

	if _, err := backend.ModelInventory.LoadModels(context.Background()); err != nil {
		t.Fatalf("LoadModels: %v", err)
	}

	gotPath := requestedPath.Load()
	if gotPath == nil || *gotPath != "/provider/model-catalog" {
		got := "<nil>"
		if gotPath != nil {
			got = *gotPath
		}
		t.Fatalf("expected discovery path '/provider/model-catalog', got %q", got)
	}
}

func TestProviderProfile_ProductionRegistryPath_OpenResponsesCapabilitiesAndDialects(t *testing.T) {
	t.Setenv("OR_TEST_KEY", "or-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	testProfile := providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "openresponses-profile",
		Family:     providerprofiles.FamilyOpenResponses,
		Endpoint:   providerprofiles.Endpoint{BaseURL: server.URL, PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "OR_TEST_KEY"},
		Capabilities: providerprofiles.CapabilityOverrides{
			Disable: []aipapi.Capability{aipapi.CapabilityCompaction},
		},
		Dialects: providerprofiles.DialectOverrides{
			Item: []aipapi.DialectRequirement{{Dialect: "openresponses.2026-04-24", Implementor: "test-impl"}},
		},
		Models: providerprofiles.ModelDiscovery{
			Policy:    providerprofiles.DiscoveryStatic,
			Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
			Static:    []providerprofiles.Model{{CanonicalID: "openresponses-profile/m1", NativeID: "m1", DisplayName: "Model 1"}},
		},
	}
	catalog, err := providerprofiles.NewCatalog([]providerprofiles.Profile{testProfile})
	if err != nil {
		t.Fatal(err)
	}

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile: openresponses-profile\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "openresponses-instance", Kind: ProviderProfileKind, Enabled: true, Config: node},
			},
		},
	}

	prepared, err := PrepareProviderProfilesWithCatalog(cfg, catalog)
	if err != nil {
		t.Fatalf("PrepareProviderProfilesWithCatalog: %v", err)
	}

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	backendRow := prepared.Plugins.Backends[0]
	res, err := prepared.BuildBackendWithLifecycle(reg,
		backendRow.FactoryID(),
		backendRow.InstanceID(),
		backendRow.Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle: %v", err)
	}
	backend := res.Backend

	// Assert compiled disabled capability
	if _, ok := backend.Caps[aipapi.CapabilityCompaction]; ok {
		t.Fatalf("expected CapabilityCompaction to be disabled by compiled profile, but it was enabled in backend.Caps")
	}

	// Assert dialect requirement preserved
	if len(backend.DialectSupport.ItemDialects) == 0 || backend.DialectSupport.ItemDialects[0].Implementor != "test-impl" {
		t.Fatalf("expected dialect item implementor 'test-impl', got %+v", backend.DialectSupport.ItemDialects)
	}
}

func TestProviderProfile_CustomCompatibleSameInstanceID_RetainsGenericSemantics(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "groq-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-1","output":[]}`)
	}))
	t.Cleanup(server.Close)

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	// 1. Prepare and build a provider profile with instance ID "shared-instance-id"
	var profileNode yaml.Node
	if err := yaml.Unmarshal([]byte("profile: groq\n"), &profileNode); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "shared-instance-id", Kind: ProviderProfileKind, Enabled: true, Config: profileNode},
			},
		},
	}
	prepared, err := PrepareProviderProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	resProfile, err := prepared.BuildBackendWithLifecycle(reg,
		prepared.Plugins.Backends[0].FactoryID(),
		prepared.Plugins.Backends[0].InstanceID(),
		prepared.Plugins.Backends[0].Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Profile backend has Vision disabled
	if _, ok := resProfile.Backend.Caps[aipapi.CapabilityVision]; ok {
		t.Fatal("expected CapabilityVision to be disabled on groq profile backend")
	}

	// 2. Build an arbitrary custom-*-compatible row with the EXACT SAME instance ID "shared-instance-id"
	var customNode yaml.Node
	if err := yaml.Unmarshal([]byte("backend_prefix: custom_pfx\nbase_url: "+server.URL+"/v1\n"), &customNode); err != nil {
		t.Fatal(err)
	}
	resCustom, err := reg.BuildBackendWithLifecycle(
		CustomOpenAIResponsesCompatibleID,
		"shared-instance-id",
		customNode,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("build generic custom backend: %v", err)
	}

	// 3. Generic custom-compatible backend MUST retain generic compatible semantics:
	// - It must have backend_prefix "custom_pfx", NOT "groq"
	if len(resCustom.Backend.BackendPrefixes) != 1 || resCustom.Backend.BackendPrefixes[0] != "custom_pfx" {
		t.Fatalf("expected prefix ['custom_pfx'], got %v", resCustom.Backend.BackendPrefixes)
	}
	// - It must NOT be subject to the groq capability ceiling; CapabilityVision remains enabled
	if _, ok := resCustom.Backend.Caps[aipapi.CapabilityVision]; !ok {
		t.Fatal("expected CapabilityVision to be ENABLED on generic custom compatible backend, but it was disabled")
	}
}

func TestProviderProfile_SequentialReloads_DoNotLeakOrStale(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "groq-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-1","output":[]}`)
	}))
	t.Cleanup(server.Close)

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	// Cycle 1: Build generation with groq profile on instance "reload-id"
	var node1 yaml.Node
	if err := yaml.Unmarshal([]byte("profile: groq\n"), &node1); err != nil {
		t.Fatal(err)
	}
	cfg1 := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "reload-id", Kind: ProviderProfileKind, Enabled: true, Config: node1},
			},
		},
	}
	prep1, err := PrepareProviderProfiles(cfg1)
	if err != nil {
		t.Fatal(err)
	}
	res1, err := prep1.BuildBackendWithLifecycle(reg,
		prep1.Plugins.Backends[0].FactoryID(),
		prep1.Plugins.Backends[0].InstanceID(),
		prep1.Plugins.Backends[0].Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg1.Identity},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res1.Backend.Caps[aipapi.CapabilityVision]; ok {
		t.Fatal("cycle 1: expected vision disabled")
	}

	// Cycle 2 (reload): Replace instance "reload-id" with an arbitrary custom-compatible backend
	var customNode yaml.Node
	if err := yaml.Unmarshal([]byte("backend_prefix: custom_reload\nbase_url: "+server.URL+"/v1\n"), &customNode); err != nil {
		t.Fatal(err)
	}
	cfg2 := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "reload-id", Kind: CustomOpenAIResponsesCompatibleID, Enabled: true, Config: customNode},
			},
		},
	}
	prep2, err := PrepareProviderProfiles(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := prep2.BuildBackendWithLifecycle(reg,
		prep2.Plugins.Backends[0].FactoryID(),
		prep2.Plugins.Backends[0].InstanceID(),
		prep2.Plugins.Backends[0].Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg2.Identity},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that cycle 2 backend has generic semantics and did NOT leak/stale groq bindings
	if len(res2.Backend.BackendPrefixes) != 1 || res2.Backend.BackendPrefixes[0] != "custom_reload" {
		t.Fatalf("cycle 2: expected prefix ['custom_reload'], got %v", res2.Backend.BackendPrefixes)
	}
	if _, ok := res2.Backend.Caps[aipapi.CapabilityVision]; !ok {
		t.Fatal("cycle 2: expected vision enabled on generic custom compatible backend")
	}
}

func TestProviderProfile_YAMLMetadataStripping_PreservesCapabilityCeiling(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "groq-key")
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-1","output":[]}`)
	}))
	t.Cleanup(server.Close)

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile: groq\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "groq-strip-test", Kind: ProviderProfileKind, Enabled: true, Config: node},
			},
		},
	}
	prepared, err := PrepareProviderProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}

	backendRow := prepared.Plugins.Backends[0]
	// YAML presentation metadata must not carry executable profile policy.
	backendRow.Config.Anchor = ""
	backendRow.Config.HeadComment = ""
	backendRow.Config.LineComment = ""
	backendRow.Config.FootComment = ""
	for _, child := range backendRow.Config.Content {
		child.Anchor = ""
		child.HeadComment = ""
		child.LineComment = ""
		child.FootComment = ""
	}

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	res, err := prepared.BuildBackendWithLifecycle(reg,
		backendRow.FactoryID(),
		backendRow.InstanceID(),
		backendRow.Config,
		server.Client(),
		pluginreg.BackendFactoryDeps{Identity: cfg.Identity},
	)
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle after comment stripping: %v", err)
	}
	backend := res.Backend

	// Required capability rejection still happens before upstream work.
	if _, ok := backend.Caps[aipapi.CapabilityVision]; ok {
		t.Fatal("expected CapabilityVision to be disabled even after comments were stripped")
	}

	// Assert zero upstream work when required capability is rejected
	ex := runtime.TestExecutor()
	ex.Backends = map[string]execbackend.Backend{"groq-strip-test": backend}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "groq-strip-test:llama-3.3-70b"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind:      aipapi.PartImageRef,
				ImageRef:  "https://example.com/test.png",
				ImageMIME: "image/png",
			}},
		}},
	}
	_, execErr := ex.Execute(context.Background(), call)
	if execErr == nil {
		t.Fatal("expected capability mismatch error")
	}
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("expected zero upstream calls, got %d", calls)
	}
}

func TestBuildProviderProfileBackend_NilUpstream_FailsClosed(t *testing.T) {
	t.Parallel()
	profile, err := providerprofiles.EmbeddedProfile("groq")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := providerprofiles.CompileProfile(profile)
	if err != nil {
		t.Fatal(err)
	}

	_, err = BuildProviderProfileBackend(compiled, "groq-nil-upstream", nil, pluginreg.BackendFactoryDeps{})
	if err == nil {
		t.Fatal("expected error on nil upstream client, got nil")
	}
	wantSubstr := "nil upstream client"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("expected error containing %q, got %q", wantSubstr, err.Error())
	}
}

func forgedCustomNode(t *testing.T, anchor, headComment string) yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("backend_prefix: forged\nbase_url: https://example.invalid/v1\n"), &node); err != nil {
		t.Fatal(err)
	}
	if anchor != "" {
		node.Anchor = anchor
		if len(node.Content) > 0 {
			node.Content[0].Anchor = anchor
		}
	}
	if headComment != "" {
		node.HeadComment = headComment
		if len(node.Content) > 0 {
			node.Content[0].HeadComment = headComment
		}
	}
	return node
}

func TestExpandProviderProfileRows_rejectsForgedMarkerOnCustomRow(t *testing.T) {
	t.Parallel()
	kinds := []string{CustomOpenAIResponsesCompatibleID, CustomOpenAILegacyCompatibleID, CustomAnthropicCompatibleID, CustomOpenResponsesCompatibleID}
	for _, kind := range kinds {
		forgedAnchor := forgedCustomNode(t, "aip_profile_groq", "")
		cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{ID: "forged-anchor", Kind: kind, Enabled: true, Config: forgedAnchor},
		}}}
		if _, err := ExpandProviderProfileRows(cfg); err == nil {
			t.Fatalf("kind %q: forged anchor accepted", kind)
		} else if !strings.Contains(err.Error(), "remove the marker") {
			t.Fatalf("kind %q: anchor error missing operator guidance, got %q", kind, err.Error())
		}

		forgedComment := forgedCustomNode(t, "", "aip:provider-profile:groq")
		cfg = &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{ID: "forged-comment", Kind: kind, Enabled: true, Config: forgedComment},
		}}}
		if _, err := ExpandProviderProfileRows(cfg); err == nil {
			t.Fatalf("kind %q: forged comment accepted", kind)
		} else if !strings.Contains(err.Error(), "remove the marker") {
			t.Fatalf("kind %q: comment error missing operator guidance, got %q", kind, err.Error())
		}
	}
}

func TestPreparedProviderProfiles_rejectsCrossFamilyProfile(t *testing.T) {
	t.Parallel()
	var profileNode yaml.Node
	if err := yaml.Unmarshal([]byte("profile: groq\n"), &profileNode); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
		{ID: "x-fam", Kind: ProviderProfileKind, Enabled: true, Config: profileNode},
	}}}
	prepared, err := ExpandProviderProfileRows(cfg)
	if err != nil {
		t.Fatal(err)
	}
	expanded := prepared.Plugins.Backends[0].Config
	if _, err := prepared.BuildBackendWithLifecycle(pluginreg.NewRegistry(), CustomAnthropicCompatibleID, "x-fam", expanded, http.DefaultClient, pluginreg.BackendFactoryDeps{}); err == nil {
		t.Fatal("cross-family profile accepted")
	} else if !strings.Contains(err.Error(), "family") {
		t.Fatalf("cross-family error missing family detail, got %q", err.Error())
	}
}

func TestWrapCompatibleLifecycle_rejectsUnresolvableMarkerWithoutFallback(t *testing.T) {
	t.Parallel()
	forged := forgedCustomNode(t, "aip_profile_does-not-exist", "")
	baseCalled := false
	stubBase := func(string, yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
		baseCalled = true
		return pluginreg.BackendBuildResult{}, nil
	}
	wrapped := rejectProviderProfileMarkers(stubBase)
	if _, err := wrapped("forged", forged, http.DefaultClient, pluginreg.BackendFactoryDeps{}); err == nil {
		t.Fatal("unresolvable marker accepted")
	} else if !strings.Contains(err.Error(), "remove") {
		t.Fatalf("unresolvable-marker error missing operator guidance, got %q", err.Error())
	}
	if baseCalled {
		t.Fatal("unresolvable marker fell back to generic base")
	}
}

func TestWrapCompatibleLifecycle_rejectsEmptyProfileIDMarkerWithoutFallback(t *testing.T) {
	t.Parallel()
	cases := map[string]yaml.Node{
		"bare anchor":  forgedCustomNode(t, "aip_profile_", ""),
		"bare comment": forgedCustomNode(t, "", "aip:provider-profile:"),
	}
	for name, node := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			baseCalled := false
			stubBase := func(string, yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
				baseCalled = true
				return pluginreg.BackendBuildResult{}, nil
			}
			wrapped := rejectProviderProfileMarkers(stubBase)
			_, err := wrapped("empty-marker", node, http.DefaultClient, pluginreg.BackendFactoryDeps{})
			if err == nil {
				t.Fatal("empty profile-ID marker accepted")
			} else if !strings.Contains(err.Error(), "reserved provider-profile marker") {
				t.Fatalf("empty-marker error missing profile-ID detail, got %q", err.Error())
			}
			if baseCalled {
				t.Fatal("empty profile-ID marker fell back to generic base")
			}
		})
	}
}

func TestPreparedProviderProfiles_ConfigRoundtripKeepsProfileSemantics(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "groq-test-key")
	var profileNode yaml.Node
	if err := yaml.Unmarshal([]byte("profile: groq\n"), &profileNode); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
		{ID: "groq-roundtrip", Kind: ProviderProfileKind, Enabled: true, Config: profileNode},
	}}}
	prepared, err := PrepareProviderProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(prepared.Config)
	require.NoError(t, err)
	var roundtrip config.Config
	require.NoError(t, yaml.Unmarshal(raw, &roundtrip))
	row := roundtrip.Plugins.Backends[0]
	res, err := prepared.BuildBackendWithLifecycle(reg, row.FactoryID(), row.InstanceID(), row.Config, http.DefaultClient, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatalf("legitimate roundtrip failed: %v", err)
	}
	if !slices.Contains(res.Backend.BackendPrefixes, "groq") {
		t.Fatalf("expected prefix groq, got %v", res.Backend.BackendPrefixes)
	}
	if _, ok := res.Backend.Caps[aipapi.CapabilityVision]; ok {
		t.Fatal("expected vision disabled by groq profile ceiling")
	}
}

func TestWrapCompatibleLifecycle_cleanCustomRowStaysGeneric(t *testing.T) {
	t.Parallel()
	var customNode yaml.Node
	if err := yaml.Unmarshal([]byte("backend_prefix: custom_pfx\nbase_url: https://private.example/v1\n"), &customNode); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
		{ID: "clean-custom", Kind: CustomOpenAIResponsesCompatibleID, Enabled: true, Config: customNode},
	}}}
	prepared, err := ExpandProviderProfileRows(cfg)
	if err != nil {
		t.Fatalf("clean custom row rejected: %v", err)
	}
	if prepared.Plugins.Backends[0].Kind != CustomOpenAIResponsesCompatibleID {
		t.Fatalf("clean custom row kind changed to %q", prepared.Plugins.Backends[0].Kind)
	}
	baseCalled := false
	stubBase := func(string, yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
		baseCalled = true
		return pluginreg.BackendBuildResult{}, nil
	}
	wrapped := rejectProviderProfileMarkers(stubBase)
	if _, err := wrapped("clean-custom", customNode, http.DefaultClient, pluginreg.BackendFactoryDeps{}); err != nil {
		t.Fatalf("clean custom row failed: %v", err)
	}
	if !baseCalled {
		t.Fatal("clean custom row did not use generic base")
	}
}
