package stdhttp

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	feopenairesponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	refresponses "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// ID-147-FE: OpenAI Responses decode → runtime → hosted-compatible upstream User-Agent
// passthrough, with A-leg Server identity independent of the client UA.
func TestComposeStandardHTTP_ID147_openaiResponsesUserAgentPassthroughToUpstream(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		clientUA    string
		wantPresent bool
		wantUA      string
	}{
		{
			name:        "valid_passthrough",
			clientUA:    "FrontendClient/9.9",
			wantPresent: true,
			wantUA:      "FrontendClient/9.9",
		},
		{
			name:        "missing_omits",
			clientUA:    "",
			wantPresent: false,
		},
		{
			name:        "invalid_omits",
			clientUA:    "bad\r\nagent",
			wantPresent: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var sawPresent bool
			var sawUA string
			inner := refresponses.NewHandler(refresponses.Config{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				_, sawPresent = r.Header["User-Agent"]
				sawUA = r.Header.Get("User-Agent")
				mu.Unlock()
				inner.ServeHTTP(w, r)
			}))
			t.Cleanup(srv.Close)

			g := identity.Config{
				Upstream: identity.UpstreamPolicy{
					UserAgent: identity.FieldPolicy{Mode: identity.ModePassthrough},
				},
				Downstream: identity.DownstreamPolicy{
					Server: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "ALegPassthroughGW"},
				},
			}
			if err := identity.Validate(&g); err != nil {
				t.Fatal(err)
			}
			reg := pluginreg.NewRegistry()
			if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{OpenAI: []string{"sk-test"}}); err != nil {
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

			ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "unused", nil)
			ex.Backends = map[string]execbackend.Backend{
				"oa": be,
			}

			cfg := &config.Config{
				Server:     config.ServerConfig{Address: "127.0.0.1:0"},
				Routing:    config.RoutingConfig{DefaultRoute: "oa:gpt-4o-mini", MaxAttempts: 3},
				Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
				Identity:   g,
				Logging:    config.LoggingConfig{AccessLog: false},
				Plugins: config.PluginsConfig{
					Frontends: []config.PluginConfig{
						{ID: "openai-responses", Enabled: true},
					},
				},
			}
			app := mustRuntimeApp(t, cfg)
			ctx := context.Background()
			startTestApp(ctx, t, app)
			in := StandardHTTPInput{
				Core:      HTTPCoreInput{Executor: ex},
				Frontends: frontendInputForTest(cfg, ex, reg),
			}
			h, err := ComposeStandardHTTP(ctx, cfg, slog.Default(), in)
			if err != nil {
				t.Fatalf("ComposeStandardHTTP: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
				`{"model":"gpt-4o-mini","stream":false,"input":[{"role":"user","content":"ping"}]}`,
			))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer sk-test")
			if tc.clientUA != "" {
				req.Header.Set("User-Agent", tc.clientUA)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			assertServerHeader(t, rec.Result().Header, true, "ALegPassthroughGW")

			mu.Lock()
			gotPresent, gotUA := sawPresent, sawUA
			mu.Unlock()
			if gotPresent != tc.wantPresent {
				t.Fatalf("upstream User-Agent present=%v want %v value=%q", gotPresent, tc.wantPresent, gotUA)
			}
			if tc.wantPresent && gotUA != tc.wantUA {
				t.Fatalf("upstream User-Agent=%q want %q", gotUA, tc.wantUA)
			}
		})
	}
}

// ID-147-FE-DECODE: evidence that decode captures UA into Call before executor (compose with wire path above).
func TestDecodeOpenAIResponses_ID147_clientUserAgentCaptureMatchesPassthroughContract(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","input":"hi"}`)
	h := http.Header{}
	h.Set("User-Agent", "ComposeClient/1")
	d, err := feopenairesponses.DecodeCreateRequest(body, feopenairesponses.DecodeOptions{
		RouteSelector: "oa:gpt-4o-mini",
		Headers:       h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.ClientUserAgent != "ComposeClient/1" {
		t.Fatalf("ClientUserAgent=%q", d.Call.Invocation.ClientUserAgent)
	}
}
