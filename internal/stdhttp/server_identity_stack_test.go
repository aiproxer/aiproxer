package stdhttp

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

func assertServerHeader(t *testing.T, h http.Header, wantPresent bool, wantValue string) {
	t.Helper()
	_, present := h["Server"]
	if present != wantPresent {
		t.Fatalf("Server present=%v want %v (Get=%q)", present, wantPresent, h.Get("Server"))
	}
	if wantPresent && h.Get("Server") != wantValue {
		t.Fatalf("Server=%q want %q", h.Get("Server"), wantValue)
	}
}

func TestStackHTTPHandler_serverIdentity_modes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		server      identity.FieldPolicy
		wantPresent bool
		wantValue   string
	}{
		{
			name:        "ID-050_proxy_default_literal",
			server:      identity.FieldPolicy{},
			wantPresent: true,
			wantValue:   "aiproxer",
		},
		{
			name:        "custom",
			server:      identity.FieldPolicy{Mode: identity.ModeCustom, Value: "StackGW"},
			wantPresent: true,
			wantValue:   "StackGW",
		},
		{
			name:        "drop",
			server:      identity.FieldPolicy{Mode: identity.ModeDrop},
			wantPresent: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mux := http.NewServeMux()
			mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Server", "inner-conflict")
				w.WriteHeader(http.StatusNoContent)
			})
			cfg := &config.Config{
				Logging:       config.LoggingConfig{AccessLog: false},
				Observability: config.ObservabilityConfig{Tracing: config.TracingConfig{Enabled: false}},
				Identity: identity.Config{
					Downstream: identity.DownstreamPolicy{Server: tc.server},
					Upstream: identity.UpstreamPolicy{
						UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "UpstreamUA/9"},
					},
				},
			}
			h := stackHTTPHandler(stackHTTPInput{
				Cfg: cfg, Log: testkit.DiscardLogger(), Security: HTTPSecurityInput{},
				TraceGen: diag.NewTraceIDGenerator(), Inner: mux,
			})
			req := httptest.NewRequest(http.MethodGet, "/ok", nil)
			req.Header.Set("Server", "client-spoofed-server")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status=%d", rec.Code)
			}
			assertServerHeader(t, rec.Result().Header, tc.wantPresent, tc.wantValue)
		})
	}
}

func TestStackHTTPHandler_serverIdentity_errorPaths(t *testing.T) {
	t.Parallel()
	cfgBase := func() *config.Config {
		return &config.Config{
			Logging:       config.LoggingConfig{AccessLog: false},
			Observability: config.ObservabilityConfig{Tracing: config.TracingConfig{Enabled: false}},
			Identity: identity.Config{
				Downstream: identity.DownstreamPolicy{
					Server: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "ErrGW"},
				},
			},
		}
	}

	t.Run("not_found", func(t *testing.T) {
		t.Parallel()
		mux := http.NewServeMux()
		h := stackHTTPHandler(stackHTTPInput{
			Cfg: cfgBase(), Log: testkit.DiscardLogger(), Security: HTTPSecurityInput{},
			TraceGen: diag.NewTraceIDGenerator(), Inner: mux,
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d", rec.Code)
		}
		assertServerHeader(t, rec.Result().Header, true, "ErrGW")
	})

	t.Run("method_not_allowed_via_mux", func(t *testing.T) {
		t.Parallel()
		mux := http.NewServeMux()
		mux.HandleFunc("GET /only-get", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		h := stackHTTPHandler(stackHTTPInput{
			Cfg: cfgBase(), Log: testkit.DiscardLogger(), Security: HTTPSecurityInput{},
			TraceGen: diag.NewTraceIDGenerator(), Inner: mux,
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/only-get", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d want 405", rec.Code)
		}
		assertServerHeader(t, rec.Result().Header, true, "ErrGW")
	})

	t.Run("recovery_500", func(t *testing.T) {
		t.Parallel()
		mux := http.NewServeMux()
		mux.HandleFunc("/panic", func(http.ResponseWriter, *http.Request) { panic("boom") })
		h := stackHTTPHandler(stackHTTPInput{
			Cfg: cfgBase(), Log: testkit.DiscardLogger(), Security: HTTPSecurityInput{},
			TraceGen: diag.NewTraceIDGenerator(), Inner: mux,
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d", rec.Code)
		}
		assertServerHeader(t, rec.Result().Header, true, "ErrGW")
	})

	t.Run("outer_recovery_500_via_testOuterWrap", func(t *testing.T) {
		t.Parallel()
		mux := http.NewServeMux()
		mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		h := stackHTTPHandler(stackHTTPInput{
			Cfg: cfgBase(), Log: testkit.DiscardLogger(), Security: HTTPSecurityInput{},
			TraceGen: diag.NewTraceIDGenerator(), Inner: mux,
			testOuterWrap: func(http.Handler) http.Handler {
				return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					panic("outer-layer-boom")
				})
			},
		})
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		res, err := http.Get(srv.URL + "/ok")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status=%d", res.StatusCode)
		}
		assertServerHeader(t, res.Header, true, "ErrGW")
	})

	t.Run("auth_reject_403", func(t *testing.T) {
		t.Parallel()
		mux := http.NewServeMux()
		mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("inner must not run")
		})
		providers := []httpauth.Provider{
			rejectAuthProvider{status: http.StatusForbidden, body: []byte("denied")},
		}
		h := stackHTTPHandler(stackHTTPInput{
			Cfg: cfgBase(), Log: testkit.DiscardLogger(), Security: HTTPSecurityInput{HTTPAuthProviders: providers},
			TraceGen: diag.NewTraceIDGenerator(), Inner: mux,
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ok", nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		assertServerHeader(t, rec.Result().Header, true, "ErrGW")
	})

	t.Run("auth_reject_401", func(t *testing.T) {
		t.Parallel()
		mux := http.NewServeMux()
		providers := []httpauth.Provider{
			rejectAuthProvider{status: http.StatusUnauthorized, body: []byte("auth")},
		}
		h := stackHTTPHandler(stackHTTPInput{
			Cfg: cfgBase(), Log: testkit.DiscardLogger(), Security: HTTPSecurityInput{HTTPAuthProviders: providers},
			TraceGen: diag.NewTraceIDGenerator(), Inner: mux,
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", rec.Code)
		}
		assertServerHeader(t, rec.Result().Header, true, "ErrGW")
	})
}

type rejectAuthProvider struct {
	status int
	body   []byte
}

func (p rejectAuthProvider) Authenticate(_ context.Context, _ http.ResponseWriter, _ *http.Request) (httpauth.AuthenticationResult, error) {
	return httpauth.AuthenticationResult{
		Type:       httpauth.TypeReject,
		HTTPStatus: p.status,
		Body:       p.body,
	}, nil
}

func TestComposeStandardHTTP_serverIdentity_frontends(t *testing.T) {
	t.Parallel()
	cfg, reg := serverIdentityTestConfig(identity.Config{
		Downstream: identity.DownstreamPolicy{
			Server: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "FrontGW"},
		},
		Upstream: identity.UpstreamPolicy{
			UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "BLegUA/1"},
		},
	})
	app := mustRuntimeApp(t, cfg)
	startTestApp(context.Background(), t, app)
	h := composeServerIdentityHandler(t, cfg, nil, reg)

	cases := []struct {
		name            string
		method          string
		path            string
		body            string
		stream          bool
		wantContentType string
		wantBodySubstr  []string
	}{
		{
			name:           "openai_responses_nonstream",
			method:         http.MethodPost,
			path:           "/v1/responses",
			body:           `{"model":"model","stream":false,"input":[{"role":"user","content":"ping"}]}`,
			wantBodySubstr: []string{"server-id-ok"},
		},
		{
			name:            "openai_responses_stream",
			method:          http.MethodPost,
			path:            "/v1/responses",
			body:            `{"model":"model","stream":true,"input":[{"role":"user","content":"ping"}]}`,
			stream:          true,
			wantContentType: "text/event-stream",
			wantBodySubstr:  []string{"data:", "server-id-ok"},
		},
		{
			name:            "openai_legacy_stream",
			method:          http.MethodPost,
			path:            "/v1/chat/completions",
			body:            `{"model":"model","stream":true,"messages":[{"role":"user","content":"ping"}]}`,
			stream:          true,
			wantContentType: "text/event-stream",
			wantBodySubstr:  []string{"data:", "server-id-ok"},
		},
		{
			name:            "anthropic_stream",
			method:          http.MethodPost,
			path:            "/v1/messages",
			body:            `{"model":"model","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"ping"}]}`,
			stream:          true,
			wantContentType: "text/event-stream",
			wantBodySubstr:  []string{"data:", "server-id-ok"},
		},
		{
			name:            "gemini_stream",
			method:          http.MethodPost,
			path:            "/v1beta/models/model:streamGenerateContent",
			body:            `{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}`,
			stream:          true,
			wantContentType: "text/event-stream",
			wantBodySubstr:  []string{"data:", "server-id-ok"},
		},
		{
			name:   "openai_responses_decode_400",
			method: http.MethodPost,
			path:   "/v1/responses",
			body:   `{`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer sk-test")
			req.Header.Set("Server", "client-spoof")
			if strings.Contains(tc.path, "messages") {
				req.Header.Set("x-api-key", "sk-ant-test")
				req.Header.Set("anthropic-version", "2023-06-01")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assertServerHeader(t, rec.Result().Header, true, "FrontGW")
			if tc.name == "openai_responses_decode_400" {
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if tc.stream {
				ct := rec.Header().Get("Content-Type")
				if !strings.Contains(ct, tc.wantContentType) {
					t.Fatalf("Content-Type=%q want family %q body=%s", ct, tc.wantContentType, rec.Body.String())
				}
			}
			for _, sub := range tc.wantBodySubstr {
				if !bytes.Contains(rec.Body.Bytes(), []byte(sub)) {
					t.Fatalf("body missing %q; body=%s", sub, rec.Body.String())
				}
			}
		})
	}
}

func TestComposeStandardHTTP_serverIdentity_realHTTPServer_modes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		server      identity.FieldPolicy
		wantPresent bool
		wantValue   string
	}{
		{
			name:        "proxy",
			server:      identity.FieldPolicy{Mode: identity.ModeProxy},
			wantPresent: true,
			wantValue:   "aiproxer",
		},
		{
			name:        "custom",
			server:      identity.FieldPolicy{Mode: identity.ModeCustom, Value: "WireGW"},
			wantPresent: true,
			wantValue:   "WireGW",
		},
		{
			name:        "drop",
			server:      identity.FieldPolicy{Mode: identity.ModeDrop},
			wantPresent: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, reg := serverIdentityTestConfig(identity.Config{
				Downstream: identity.DownstreamPolicy{Server: tc.server},
				Upstream: identity.UpstreamPolicy{
					UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "ShouldNotAffect"},
				},
			})
			app := mustRuntimeApp(t, cfg)
			startTestApp(context.Background(), t, app)
			h := composeServerIdentityHandler(t, cfg, nil, reg)
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)

			req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(
				`{"model":"model","stream":false,"input":[{"role":"user","content":"ping"}]}`,
			))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer sk-test")
			req.Header.Set("Server", "client-spoof")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", res.StatusCode, body)
			}
			if !bytes.Contains(body, []byte("server-id-ok")) {
				t.Fatalf("body missing server-id-ok: %s", body)
			}
			assertServerHeader(t, res.Header, tc.wantPresent, tc.wantValue)
		})
	}
}

func TestComposeStandardHTTP_serverIdentity_independentOfUpstreamAndDrop(t *testing.T) {
	t.Parallel()
	cfg, reg := serverIdentityTestConfig(identity.Config{
		Downstream: identity.DownstreamPolicy{
			Server: identity.FieldPolicy{Mode: identity.ModeDrop},
		},
		Upstream: identity.UpstreamPolicy{
			UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "ShouldNotAffectALeg"},
		},
	})
	app := mustRuntimeApp(t, cfg)
	startTestApp(context.Background(), t, app)
	h := composeServerIdentityHandler(t, cfg, nil, reg)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"model","stream":false,"input":[{"role":"user","content":"ping"}]}`,
	))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Server", "client-spoof")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertServerHeader(t, rec.Result().Header, false, "")
}

func TestComposeStandardHTTP_serverIdentity_independentOfBackendWinner(t *testing.T) {
	t.Parallel()
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	open := func(text string) func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: text},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		}
	}
	ex := testkit.NewStubExecutor(t, caps, "unused", nil)
	ex.Backends = map[string]execbackend.Backend{
		"stub-a": {Caps: caps, Open: open("from-a")},
		"stub-b": {Caps: caps, Open: open("from-b")},
	}
	cfg, reg := serverIdentityTestConfig(identity.Config{
		Downstream: identity.DownstreamPolicy{
			Server: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "SameALeg"},
		},
	})
	app := mustRuntimeApp(t, cfg)
	startTestApp(context.Background(), t, app)
	h := composeServerIdentityHandler(t, cfg, ex, reg)

	for _, route := range []string{"stub-a:model", "stub-b:model"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
				`{"model":"model","stream":false,"input":[{"role":"user","content":"ping"}]}`,
			))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer sk-test")
			req.Header.Set("X-AIP-Route", route)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			assertServerHeader(t, rec.Result().Header, true, "SameALeg")
			wantText := "from-a"
			if strings.HasPrefix(route, "stub-b") {
				wantText = "from-b"
			}
			if !strings.Contains(rec.Body.String(), wantText) {
				t.Fatalf("body=%s want %q from selected backend", rec.Body.String(), wantText)
			}
		})
	}
}

func serverIdentityTestConfig(idCfg identity.Config) (*config.Config, *pluginreg.Registry) {
	reg := pluginreg.NewRegistry()
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{DefaultRoute: "stub:model", MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Identity:   idCfg,
		Logging:    config.LoggingConfig{AccessLog: false},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{
				{ID: "openai-responses", Enabled: true},
				{ID: "openai-legacy", Enabled: true},
				{ID: "anthropic", Enabled: true},
				{ID: "gemini", Enabled: true},
			},
		},
	}
	return cfg, reg
}

// composeServerIdentityHandler builds the handler only; callers start/stop App separately.
func composeServerIdentityHandler(t *testing.T, cfg *config.Config, ex *runtime.Executor, reg *pluginreg.Registry) http.Handler {
	t.Helper()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if ex == nil {
		ex = testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "server-id-ok", nil)
	}
	in := StandardHTTPInput{
		Core:      HTTPCoreInput{Executor: ex},
		Frontends: frontendInputForTest(cfg, ex, reg),
	}
	h, err := ComposeStandardHTTP(context.Background(), cfg, slog.Default(), in)
	if err != nil {
		t.Fatalf("ComposeStandardHTTP: %v", err)
	}
	return h
}
