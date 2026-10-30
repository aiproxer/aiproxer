package runtimebundle_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	stdhttpauth "github.com/aiproxer/aiproxer/internal/stdhttp/auth"
	"github.com/aiproxer/aiproxer/internal/testkit"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
	"gopkg.in/yaml.v3"
)

type fixedOSIdentity struct {
	snap coreauth.OSIdentitySnapshot
	err  error
}

func (o fixedOSIdentity) Current(ctx context.Context) (coreauth.OSIdentitySnapshot, error) {
	_ = ctx
	return o.snap, o.err
}

func TestBuild_defaultComposedLocalNoop_principalNotEmpty(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "openai-only", Enabled: true, Config: empty,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	var pid string
	stdhttpauth.Middleware(nil, runtimebundle.CandidateHTTPAuthProviders(b), http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, ok := httpauth.PrincipalFromContext(r.Context())
		if !ok {
			t.Error("missing principal")
			return
		}
		pid = p.ID
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if strings.TrimSpace(pid) == "" {
		t.Fatal("expected non-empty principal (no anonymous pass-through)")
	}
}

func TestBuild_minimalSingleUser_HTTPAuthProviders_nonEmpty(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "openai-only", Enabled: true, Config: empty,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if len(runtimebundle.CandidateHTTPAuthProviders(b)) == 0 || runtimebundle.CandidateHTTPAuthProviders(b)[0] == nil {
		t.Fatalf("HTTPAuthProviders: want at least one non-nil provider, got %#v", runtimebundle.CandidateHTTPAuthProviders(b))
	}
}

type wireRendererA struct{}

func (wireRendererA) RenderAuthError(ctx context.Context, in httpauth.AuthErrorRenderInput) httpauth.AuthErrorRenderResult {
	_ = ctx
	_ = in
	return httpauth.AuthErrorRenderResult{Status: http.StatusTeapot, Body: []byte("A")}
}

type wireRendererB struct{}

func (wireRendererB) RenderAuthError(ctx context.Context, in httpauth.AuthErrorRenderInput) httpauth.AuthErrorRenderResult {
	_ = ctx
	_ = in
	return httpauth.AuthErrorRenderResult{Status: http.StatusTeapot, Body: []byte("B")}
}

func TestBuild_registryAuthErrorRendererByFrontend_wiresPolicyProvider(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterAuthErrorRenderer("openai_compatible", wireRendererA{}); err != nil {
		t.Fatal(err)
	}
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "openai-only", Enabled: true, Config: empty,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	pp, ok := runtimebundle.CandidateHTTPAuthProviders(b)[0].(*stdhttpauth.PolicyProvider)
	if !ok {
		t.Fatalf("want *stdhttpauth.PolicyProvider, got %T", runtimebundle.CandidateHTTPAuthProviders(b)[0])
	}
	if pp.RendererByFrontend == nil || pp.RendererByFrontend["openai_compatible"] == nil {
		t.Fatalf("RendererByFrontend: %#v", pp.RendererByFrontend)
	}
	out := pp.RendererByFrontend["openai_compatible"].RenderAuthError(context.Background(), httpauth.AuthErrorRenderInput{})
	if string(out.Body) != "A" {
		t.Fatalf("renderer body: %q", out.Body)
	}
}

func TestBuild_authErrorRenderers_registryIdCaseFoldsToLower(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	// Intentional mixed case; merge must resolve the same key as "openai_compatible" from requests.
	if err := reg.RegisterAuthErrorRenderer("OpenAI_Compatible", wireRendererA{}); err != nil {
		t.Fatal(err)
	}
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "openai-only", Enabled: true, Config: empty,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	pp, ok := runtimebundle.CandidateHTTPAuthProviders(b)[0].(*stdhttpauth.PolicyProvider)
	if !ok {
		t.Fatalf("want *stdhttpauth.PolicyProvider, got %T", runtimebundle.CandidateHTTPAuthProviders(b)[0])
	}
	rend, ok := pp.RendererByFrontend["openai_compatible"]
	if !ok || rend == nil {
		t.Fatalf("lowercase key must work after register with mixed case: %#v", pp.RendererByFrontend)
	}
	out := rend.RenderAuthError(context.Background(), httpauth.AuthErrorRenderInput{})
	if string(out.Body) != "A" {
		t.Fatalf("renderer: %q", out.Body)
	}
}

func TestBuild_optsAuthErrorRenderersByFrontend_overridesRegistry(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterAuthErrorRenderer("openai_compatible", wireRendererA{}); err != nil {
		t.Fatal(err)
	}
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "openai-only", Enabled: true, Config: empty,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Auth: runtimebundle.AuthOptions{
			AuthErrorRenderersByFrontend: map[string]httpauth.AuthErrorRenderer{
				"openai_compatible": wireRendererB{},
			},
		},
	})
	pp, ok := runtimebundle.CandidateHTTPAuthProviders(b)[0].(*stdhttpauth.PolicyProvider)
	if !ok {
		t.Fatalf("want *stdhttpauth.PolicyProvider, got %T", runtimebundle.CandidateHTTPAuthProviders(b)[0])
	}
	out := pp.RendererByFrontend["openai_compatible"].RenderAuthError(context.Background(), httpauth.AuthErrorRenderInput{})
	if string(out.Body) != "B" {
		t.Fatalf("want opts renderer B, got %q", out.Body)
	}
}

func TestBuild_remoteAuthPolicyRequiresRemoteDecider(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "multi_user"},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "be", Enabled: true, Config: empty,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil || !errors.Is(err, runtimebundle.ErrRemoteDeciderRequired) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrRemoteDeciderRequired, err)
	}
}

func TestBuild_composedLocalNoop_setsPrincipalOnRequest(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "openai-only", Enabled: true, Config: empty,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Auth: runtimebundle.AuthOptions{
			OSIdentity: fixedOSIdentity{snap: coreauth.OSIdentitySnapshot{
				PrincipalID: "compose-test-user",
			}},
		},
	})
	if len(runtimebundle.CandidateHTTPAuthProviders(b)) != 1 {
		t.Fatalf("HTTPAuthProviders: want 1, got %d", len(runtimebundle.CandidateHTTPAuthProviders(b)))
	}
	var innerPID string
	h := stdhttpauth.Middleware(nil, runtimebundle.CandidateHTTPAuthProviders(b), http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, ok := httpauth.PrincipalFromContext(r.Context())
		if !ok {
			t.Error("expected principal in context")
			return
		}
		innerPID = p.ID
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if innerPID != "compose-test-user" {
		t.Fatalf("principal id %q", innerPID)
	}
}

func TestBuild_multiUserLocalAPIKey_middlewareAllowsValidBearer(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "multi_user"},
		Auth: config.AuthConfig{
			Handler:       "local_api_key",
			RequiredLevel: "api_key",
			LocalAPIKeys: []config.AuthLocalAPIKeyRecord{
				{KeyID: "k1", PrincipalID: "api-user-1", Key: "test-local-api-key-16"},
			},
		},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "be", Enabled: true, Config: empty,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	var inner int
	h := stdhttpauth.Middleware(nil, runtimebundle.CandidateHTTPAuthProviders(b), http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		inner++
		p, _ := httpauth.PrincipalFromContext(r.Context())
		if p.ID != "api-user-1" {
			t.Errorf("principal %q", p.ID)
		}
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-local-api-key-16")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if inner != 1 {
		t.Fatalf("inner calls %d", inner)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestBuild_multiUserLocalAPIKey_middlewareDeniesWithoutBearer(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "multi_user"},
		Auth: config.AuthConfig{
			Handler:       "local_api_key",
			RequiredLevel: "api_key",
			LocalAPIKeys: []config.AuthLocalAPIKeyRecord{
				{KeyID: "k1", PrincipalID: "api-user-1", Key: "test-local-api-key-16"},
			},
		},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "be", Enabled: true, Config: empty,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	var inner int
	h := stdhttpauth.Middleware(nil, runtimebundle.CandidateHTTPAuthProviders(b), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { inner++ }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if inner != 0 {
		t.Fatalf("inner should not run, got %d", inner)
	}
}

func TestBuild_HTTPAuthProvidersOnlyNil_fallsBackToComposedAuth(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "multi_user"},
		Auth: config.AuthConfig{
			Handler:       "local_api_key",
			RequiredLevel: "api_key",
			LocalAPIKeys: []config.AuthLocalAPIKeyRecord{
				{KeyID: "k1", PrincipalID: "api-user-1", Key: "test-local-api-key-16"},
			},
		},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "be", Enabled: true, Config: empty,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Auth: runtimebundle.AuthOptions{
			HTTPAuthProviders: []httpauth.Provider{nil},
		},
	})
	if len(runtimebundle.CandidateHTTPAuthProviders(b)) != 1 {
		t.Fatalf("composed providers: want 1, got %d", len(runtimebundle.CandidateHTTPAuthProviders(b)))
	}
	var inner int
	rec := httptest.NewRecorder()
	stdhttpauth.Middleware(nil, runtimebundle.CandidateHTTPAuthProviders(b), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { inner++ })).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if inner != 0 {
		t.Fatalf("inner should not run without credentials, got %d", inner)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("without bearer: want status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestBuild_remoteStub_allowReachesInner(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "be", Enabled: true, Config: empty,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Auth: runtimebundle.AuthOptions{
			RemoteDecider: &testkit.StubRemoteDecider{
				Decision: sdkauth.Decision{
					Outcome:   sdkauth.OutcomeAllow,
					Principal: execview.PrincipalView{ID: "remote-allow-p"},
				},
			},
		},
	})
	var innerPID string
	stdhttpauth.Middleware(nil, runtimebundle.CandidateHTTPAuthProviders(b), http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := httpauth.PrincipalFromContext(r.Context())
		innerPID = p.ID
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/", nil))
	if innerPID != "remote-allow-p" {
		t.Fatalf("principal %q", innerPID)
	}
}

func TestBuild_remoteStub_denySkipsInner(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "be", Enabled: true, Config: empty,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Auth: runtimebundle.AuthOptions{
			RemoteDecider: &testkit.StubRemoteDecider{
				Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "remote_denied"},
			},
		},
	})
	var inner int
	stdhttpauth.Middleware(nil, runtimebundle.CandidateHTTPAuthProviders(b), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { inner++ })).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/", nil))
	if inner != 0 {
		t.Fatal("inner ran")
	}
}

func TestBuild_remoteStub_challengeTerminates(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "be", Enabled: true, Config: empty,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Auth: runtimebundle.AuthOptions{
			RemoteDecider: &testkit.StubRemoteDecider{
				Decision: sdkauth.Decision{
					Outcome:   sdkauth.OutcomeChallenge,
					Challenge: sdkauth.Challenge{Kind: sdkauth.ChallengeSSORequired, Summary: "sso"},
				},
			},
		},
	})
	var inner int
	rec := httptest.NewRecorder()
	stdhttpauth.Middleware(nil, runtimebundle.CandidateHTTPAuthProviders(b), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { inner++ })).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/", nil))
	if inner != 0 {
		t.Fatal("inner ran")
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200, got %d", rec.Code)
	}
}
