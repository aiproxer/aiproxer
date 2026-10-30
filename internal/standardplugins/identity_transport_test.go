package standardplugins_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/bedrock"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	refanthropic "github.com/aiproxer/aiproxer/internal/refbackend/anthropicmessages"
	refbedrock "github.com/aiproxer/aiproxer/internal/refbackend/bedrock"
	refgemini "github.com/aiproxer/aiproxer/internal/refbackend/gemini"
	refchat "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	refresponses "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

func TestIdentityTransport_approvedFactoriesWireUserAgent(t *testing.T) {
	t.Parallel()

	type modeCase struct {
		name       string
		global     identity.Config
		yamlExtra  string
		callUA     *string
		wantHeader bool
		wantUA     string
	}

	modes := []modeCase{
		{
			name:       "default_proxy_literal",
			global:     identity.Config{},
			wantHeader: true,
			wantUA:     "aiproxer",
		},
		{
			name: "custom",
			global: identity.Config{
				Upstream: identity.UpstreamPolicy{
					UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "FactoryCustom/1"},
				},
			},
			wantHeader: true,
			wantUA:     "FactoryCustom/1",
		},
		{
			name: "passthrough_call",
			global: identity.Config{
				Upstream: identity.UpstreamPolicy{
					UserAgent: identity.FieldPolicy{Mode: identity.ModePassthrough},
				},
			},
			callUA:     new("ClientAgent/2"),
			wantHeader: true,
			wantUA:     "ClientAgent/2",
		},
		{
			name: "drop",
			global: identity.Config{
				Upstream: identity.UpstreamPolicy{
					UserAgent: identity.FieldPolicy{Mode: identity.ModeDrop},
				},
			},
			wantHeader: false,
		},
		{
			name: "backend_override_wins",
			global: identity.Config{
				Upstream: identity.UpstreamPolicy{
					UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "Global/1"},
				},
			},
			yamlExtra: `
identity:
  user_agent:
    mode: custom
    value: BackendWin/1
`,
			wantHeader: true,
			wantUA:     "BackendWin/1",
		},
		{
			name: "backend_explicit_drop_over_global_custom",
			global: identity.Config{
				Upstream: identity.UpstreamPolicy{
					UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "Global/1"},
				},
			},
			yamlExtra: `
identity:
  user_agent:
    mode: drop
`,
			wantHeader: false,
		},
	}

	connectors := []struct {
		id       string
		yamlBase func(srvURL string) string
		open     func(t *testing.T, beOpen func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error), ctx context.Context)
		handler  func() http.Handler
	}{
		{
			id: openairesponses.ID,
			yamlBase: func(u string) string {
				return "base_url: " + u + "/v1\n"
			},
			handler: func() http.Handler { return refresponses.NewHandler(refresponses.Config{}) },
			open: func(t *testing.T, openFn func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error), ctx context.Context) {
				t.Helper()
				call := identityTransportCall(aipapi.OperationOpenAIResponses)
				es, err := openFn(ctx, call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
				if err != nil {
					t.Fatal(err)
				}
				_, err = aipapi.Collect(context.Background(), es)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			id: openailegacy.ID,
			yamlBase: func(u string) string {
				return "base_url: " + u + "/v1\n"
			},
			handler: func() http.Handler { return refchat.NewHandler(refchat.Config{}) },
			open: func(t *testing.T, openFn func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error), ctx context.Context) {
				t.Helper()
				call := identityTransportCall(aipapi.OperationOpenAIChatCompletions)
				es, err := openFn(ctx, call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
				if err != nil {
					t.Fatal(err)
				}
				_, err = aipapi.Collect(context.Background(), es)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			id: anthropic.ID,
			yamlBase: func(u string) string {
				return "base_url: " + u + "\n"
			},
			handler: func() http.Handler { return refanthropic.NewHandler(refanthropic.Config{}) },
			open: func(t *testing.T, openFn func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error), ctx context.Context) {
				t.Helper()
				call := identityTransportCall(aipapi.OperationOpenAIChatCompletions)
				es, err := openFn(ctx, call, routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}})
				if err != nil {
					t.Fatal(err)
				}
				_, err = aipapi.Collect(context.Background(), es)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			id: gemini.ID,
			yamlBase: func(u string) string {
				return "base_url: " + u + "\n"
			},
			handler: func() http.Handler { return refgemini.NewHandler(refgemini.Config{}) },
			open: func(t *testing.T, openFn func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error), ctx context.Context) {
				t.Helper()
				call := identityTransportCall(aipapi.OperationOpenAIChatCompletions)
				es, err := openFn(ctx, call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}})
				if err != nil {
					t.Fatal(err)
				}
				_, err = aipapi.Collect(context.Background(), es)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			id: bedrock.ID,
			yamlBase: func(u string) string {
				return "region: us-east-1\naccess_key_id: AKID\nsecret_access_key: SECRET\nbase_endpoint: " + u + "\ndisable_https: true\n"
			},
			handler: func() http.Handler { return refbedrock.NewHandler(refbedrock.Config{}) },
			open: func(t *testing.T, openFn func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error), ctx context.Context) {
				t.Helper()
				call := identityTransportCall(aipapi.OperationOpenAIChatCompletions)
				es, err := openFn(ctx, call, routing.AttemptCandidate{Primary: routing.Primary{Model: "anthropic.claude-3-haiku-20240307-v1:0"}})
				if err != nil {
					t.Fatal(err)
				}
				_, err = aipapi.Collect(context.Background(), es)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, conn := range connectors {
		t.Run(conn.id, func(t *testing.T) {
			t.Parallel()
			for _, mc := range modes {
				t.Run(mc.name, func(t *testing.T) {
					t.Parallel()
					var sawUA string
					var sawPresent bool
					inner := conn.handler()
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						_, sawPresent = r.Header["User-Agent"]
						sawUA = r.Header.Get("User-Agent")
						inner.ServeHTTP(w, r)
					}))
					t.Cleanup(srv.Close)

					g := mc.global
					if err := identity.Validate(&g); err != nil {
						t.Fatal(err)
					}
					reg := pluginreg.NewRegistry()
					if err := standardplugins.InstallStandardBackendsOn(reg, identityTransportTestKeys()); err != nil {
						t.Fatal(err)
					}
					raw := conn.yamlBase(srv.URL) + mc.yamlExtra
					var node yaml.Node
					if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
						t.Fatal(err)
					}
					be, err := reg.BuildBackend(conn.id, node, srv.Client(), pluginreg.BackendFactoryDeps{Identity: g})
					if err != nil {
						t.Fatal(err)
					}
					ctx := context.Background()
					if mc.callUA != nil {
						ctx = identity.WithClientUserAgent(ctx, *mc.callUA)
					}
					conn.open(t, be.Open, ctx)
					if sawPresent != mc.wantHeader {
						t.Fatalf("User-Agent present=%v want %v value=%q", sawPresent, mc.wantHeader, sawUA)
					}
					if mc.wantHeader && sawUA != mc.wantUA {
						t.Fatalf("User-Agent=%q want %q", sawUA, mc.wantUA)
					}
				})
			}
		})
	}
}

func TestIdentityTransport_inventoryUsesProxyNotClientPassthrough(t *testing.T) {
	t.Parallel()

	type invCase struct {
		id       string
		yamlBase func(srvURL string) string
		handler  http.HandlerFunc
	}
	cases := []invCase{
		{
			id: openairesponses.ID,
			yamlBase: func(u string) string {
				return "base_url: " + u + "/v1\n"
			},
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o-mini"}]}`))
			},
		},
		{
			id: bedrock.ID,
			yamlBase: func(u string) string {
				return "region: us-east-1\naccess_key_id: AKID\nsecret_access_key: SECRET\nbase_endpoint: " + u + "\ndisable_https: true\n"
			},
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-amz-json-1.1")
				_, _ = w.Write([]byte(`{"modelSummaries":[{"modelId":"anthropic.claude-3-haiku-20240307-v1:0","modelName":"Claude 3 Haiku","providerName":"Anthropic"}]}`))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			var sawUA string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sawUA = r.Header.Get("User-Agent")
				tc.handler(w, r)
			}))
			t.Cleanup(srv.Close)

			g := identity.Config{
				Upstream: identity.UpstreamPolicy{
					UserAgent: identity.FieldPolicy{Mode: identity.ModePassthrough},
				},
			}
			if err := identity.Validate(&g); err != nil {
				t.Fatal(err)
			}
			reg := pluginreg.NewRegistry()
			if err := standardplugins.InstallStandardBackendsOn(reg, identityTransportTestKeys()); err != nil {
				t.Fatal(err)
			}
			raw := tc.yamlBase(srv.URL)
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
				t.Fatal(err)
			}
			be, err := reg.BuildBackend(tc.id, node, srv.Client(), pluginreg.BackendFactoryDeps{Identity: g})
			if err != nil {
				t.Fatal(err)
			}
			if be.ModelInventory == nil {
				t.Fatal("nil ModelInventory")
			}
			if _, err := be.ModelInventory.LoadModels(context.Background()); err != nil {
				t.Fatal(err)
			}
			if sawUA != "aiproxer" {
				t.Fatalf("inventory User-Agent=%q want aiproxer (never client passthrough)", sawUA)
			}
		})
	}
}

func TestIdentityTransport_invalidCallUAOmitsOnWire(t *testing.T) {
	t.Parallel()
	var sawPresent bool
	var sawUA string
	inner := refresponses.NewHandler(refresponses.Config{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawPresent = r.Header["User-Agent"]
		sawUA = r.Header.Get("User-Agent")
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	g := identity.Config{
		Upstream: identity.UpstreamPolicy{
			UserAgent: identity.FieldPolicy{Mode: identity.ModePassthrough},
		},
	}
	if err := identity.Validate(&g); err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, identityTransportTestKeys()); err != nil {
		t.Fatal(err)
	}
	raw := "base_url: " + srv.URL + "/v1\n"
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend(openairesponses.ID, node, srv.Client(), pluginreg.BackendFactoryDeps{Identity: g})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate executor attaching a programmatically constructed invalid UA.
	ctx := identity.WithClientUserAgent(context.Background(), "bad\r\nagent")
	call := identityTransportCall(aipapi.OperationOpenAIResponses)
	es, err := be.Open(ctx, call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if sawPresent {
		t.Fatalf("invalid call UA must omit User-Agent, got %q", sawUA)
	}
}

func TestIdentityTransport_resolveIdentityHTTPValidatesGlobalCopy(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, identityTransportTestKeys()); err != nil {
		t.Fatal(err)
	}

	t.Run("mixed_case_custom_normalized_without_mutating", func(t *testing.T) {
		t.Parallel()
		var sawUA string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sawUA = r.Header.Get("User-Agent")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o-mini"}]}`))
		}))
		t.Cleanup(srv.Close)

		g := identity.Config{
			Upstream: identity.UpstreamPolicy{
				UserAgent: identity.FieldPolicy{Mode: " CuStOm ", Value: "  ProgAgent/1  "},
			},
		}
		beforeMode := g.Upstream.UserAgent.Mode
		beforeValue := g.Upstream.UserAgent.Value
		raw := "base_url: " + srv.URL + "/v1\n"
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
			t.Fatal(err)
		}
		be, err := reg.BuildBackend(openairesponses.ID, node, srv.Client(), pluginreg.BackendFactoryDeps{Identity: g})
		if err != nil {
			t.Fatal(err)
		}
		if g.Upstream.UserAgent.Mode != beforeMode || g.Upstream.UserAgent.Value != beforeValue {
			t.Fatalf("global identity mutated: %+v", g.Upstream.UserAgent)
		}
		if _, err := be.ModelInventory.LoadModels(context.Background()); err != nil {
			t.Fatal(err)
		}
		if sawUA != "ProgAgent/1" {
			t.Fatalf("User-Agent=%q want ProgAgent/1", sawUA)
		}
	})

	t.Run("invalid_global_rejected_field_qualified", func(t *testing.T) {
		t.Parallel()
		g := identity.Config{
			Upstream: identity.UpstreamPolicy{
				UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom},
			},
		}
		raw := "base_url: https://api.openai.com/v1\n"
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
			t.Fatal(err)
		}
		_, err := reg.BuildBackend(openairesponses.ID, node, nil, pluginreg.BackendFactoryDeps{Identity: g})
		if err == nil {
			t.Fatal("expected validation error")
		}
		msg := err.Error()
		if !strings.Contains(msg, "identity.upstream.user_agent") {
			t.Fatalf("want field-qualified identity.upstream.user_agent error, got %q", msg)
		}
	})
}

func TestIdentityTransport_invalidBackendOverrideRejected(t *testing.T) {
	t.Parallel()
	g := identity.Config{}
	_ = identity.Validate(&g)
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, identityTransportTestKeys()); err != nil {
		t.Fatal(err)
	}
	raw := `
base_url: https://api.openai.com/v1
identity:
  user_agent:
    mode: custom
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	_, err := reg.BuildBackend(openairesponses.ID, node, nil, pluginreg.BackendFactoryDeps{Identity: g})
	if err == nil {
		t.Fatal("expected validation error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "identity") || !strings.Contains(msg, "user_agent") {
		t.Fatalf("want field-qualified identity error, got %q", msg)
	}
}

func TestIdentityTransport_excludedHTTPConnectorsIgnoreGlobalCustomUA(t *testing.T) {
	t.Setenv("EXCL_LEGACY_API_KEY", "sk-test")

	cases := []struct {
		name  string
		id    string
		yaml  func(u string) string
		model string
		op    aipapi.Operation
	}{
		{
			name: "custom-openai-legacy-compatible",
			id:   standardplugins.CustomOpenAILegacyCompatibleID,
			yaml: func(u string) string {
				return "backend_prefix: excl-legacy\nbase_url: " + u + "/v1\napi_key_env_var_root: EXCL_LEGACY_API_KEY\n"
			},
			model: "excl-legacy/gpt-4o-mini",
			op:    aipapi.OperationOpenAIChatCompletions,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sawUA string
			var sawPresent bool
			inner := refchat.NewHandler(refchat.Config{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/models") || r.URL.Path == "/v1/models" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"data":[{"id":"remote-only-model"},{"id":"gpt-4o-mini"},{"id":"llama3:latest"}]}`)
					return
				}
				sawPresent = true
				sawUA = r.Header.Get("User-Agent")
				inner.ServeHTTP(w, r)
			}))
			t.Cleanup(srv.Close)

			g := identity.Config{
				Upstream: identity.UpstreamPolicy{
					UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "MustNotAppear/9"},
				},
			}
			if err := identity.Validate(&g); err != nil {
				t.Fatal(err)
			}
			reg := pluginreg.NewRegistry()
			if err := standardplugins.InstallStandardBackendsOn(reg, identityTransportTestKeys()); err != nil {
				t.Fatal(err)
			}
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(tc.yaml(srv.URL)), &node); err != nil {
				t.Fatal(err)
			}
			res, err := reg.BuildBackendWithLifecycle(tc.id, "excl-legacy", node, srv.Client(), pluginreg.BackendFactoryDeps{Identity: g})
			if err != nil {
				t.Fatal(err)
			}
			be := res.Backend
			es, err := be.Open(identity.WithClientUserAgent(context.Background(), "ClientMustNotAppear/1"), identityTransportCall(tc.op), routing.AttemptCandidate{Primary: routing.Primary{Model: tc.model}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = aipapi.Collect(context.Background(), es)
			if err != nil {
				t.Fatal(err)
			}
			if !sawPresent {
				t.Fatal("expected chat completion request")
			}
			if sawUA == "MustNotAppear/9" || strings.Contains(sawUA, "MustNotAppear") {
				t.Fatalf("global identity User-Agent leaked into excluded %s: %q", tc.id, sawUA)
			}
			if sawUA == "aiproxer" {
				t.Fatalf("AIProxer product User-Agent applied to excluded %s: %q", tc.id, sawUA)
			}
		})
	}
}

func TestIdentityTransport_acpFamilyAbsentFromStaticRegistry(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, identityTransportTestKeys()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"acp", "cursorcliacp", "geminicliacp", "agycliacp"} {
		if reg.HasBackend(id) {
			t.Fatalf("%s must not remain a static backend factory after Phase 6 cutover", id)
		}
	}
}

func identityTransportCall(op aipapi.Operation) aipapi.Call {
	return aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation:     op,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
}

// identityTransportTestKeys is the host snapshot bound at registration for
// identity transport tests: literal-free YAML plus env-only keys (requirement
// 2.1). Bedrock keeps its explicit workload YAML credentials.
func identityTransportTestKeys() standardplugins.UpstreamAPIKeys {
	return standardplugins.UpstreamAPIKeys{
		OpenAI:    []string{"sk-test"},
		Anthropic: []string{testkit.SyntheticAnthropicAPIKey},
		Gemini:    []string{"gemini-test"},
	}
}
