package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

// Claims (#1511): only recognized CLI traffic enters hybrid dispatch; external
// dispatch contains no original credentials/control headers; denials do no I/O.
// Mutants: remove client gate, copy inbound headers, or fall back on unknown model.
func TestHybrid_DispatchAndCredentialIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, model, agent, class, ua, app, route string
		status                                    int
		external, normal                          bool
	}{
		{"external", "aiproxer-reviewer", "worker", "", "claude-cli/2.1.310", "cli", "", 204, true, false},
		{"missing-agent", "aiproxer-reviewer", "", "", "claude-cli/2.1.310", "cli", "", 400, false, false},
		{"main", "aiproxer-reviewer", "worker", "main", "claude-cli/2.1.310", "cli", "", 400, false, false},
		{"unknown", "other", "worker", "", "claude-cli/2.1.310", "cli", "", 400, false, false},
		{"forged-route", "aiproxer-reviewer", "worker", "", "claude-cli/2.1.310", "cli", "evil:model", 400, false, false},
		{"non-cli", "aiproxer-reviewer", "worker", "", "curl/8", "cli", "", 202, false, true},
		{"missing-app", "aiproxer-reviewer", "worker", "", "claude-cli/2.1.310", "", "", 202, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			external, normal := false, false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !isClaudeCodeCLI(r.Header) {
					if r.Header.Get("Authorization") != "Bearer oauth-canary" || r.Header.Get("Cookie") != "cookie-canary" {
						t.Error("unrecognized client entered hybrid sanitization")
					}
					normal = true
					w.WriteHeader(202)
					return
				}
				external = true
				if r.Header.Get(aipsdk.HeaderRoute) != "minimax:MiniMax-M2.7" {
					t.Error("approved route not injected")
				}
				for _, name := range []string{"Authorization", "x-api-key", "Cookie", "Proxy-Authorization", "anthropic-beta", "X-Route-Alias"} {
					if r.Header.Get(name) != "" {
						t.Errorf("leaked header %s", name)
					}
				}
				if r.URL.RawQuery != "" {
					t.Error("query reached canonical handler")
				}
				w.WriteHeader(204)
			})
			h := newHybridHandler(next, hybridConfig{Enabled: true, ExternalModels: []hybridModel{{Model: "aiproxer-reviewer", Route: "minimax:MiniMax-M2.7", RequireSubagent: true}}}, aipsdk.FrontendMountOptions{HTTPHeaders: aipsdk.HTTPHeaders{Route: []string{aipsdk.HeaderRoute, "X-Route-Alias"}}})
			r := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(`{"model":"`+tc.model+`","messages":[]}`))
			r.RemoteAddr = "127.0.0.1:1234"
			r.Host = "127.0.0.1:8080"
			r.Header.Set("User-Agent", tc.ua)
			r.Header.Set("x-app", tc.app)
			r.Header.Set("x-claude-code-agent-id", tc.agent)
			r.Header.Set("request-class", tc.class)
			r.Header.Set("Authorization", "Bearer oauth-canary")
			r.Header.Set("Cookie", "cookie-canary")
			r.Header.Set("Proxy-Authorization", "proxy-canary")
			r.Header.Set("anthropic-beta", "oauth-canary")
			if tc.route != "" {
				r.Header.Set("X-Route-Alias", tc.route)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || external != tc.external || normal != tc.normal {
				t.Fatalf("status=%d external=%v normal=%v body=%s", w.Code, external, normal, w.Body.String())
			}
		})
	}
}

// Claim (#1511): fixed native destination, byte fidelity and safe streaming
// headers, with no cookies/proxy credentials/control headers in either direction.
func TestHybrid_NativeOpaqueStream(t *testing.T) {
	const body = ` {"model":"claude-sonnet-4-6", "messages":[],"stream":true} `
	h := newHybridHandler(nil, hybridConfig{Enabled: true}, aipsdk.FrontendMountOptions{})
	h.native.Transport = hybridRoundTrip(func(r *http.Request) (*http.Response, error) {
		got, _ := io.ReadAll(r.Body)
		if string(got) != body || r.URL.String() != "https://api.anthropic.com/v1/messages?beta=true" || r.Host != "api.anthropic.com" {
			t.Errorf("native request changed: %s %s %s", got, r.URL, r.Host)
		}
		if r.Header.Get("Authorization") != "Bearer synthetic" || r.Header.Get("anthropic-beta") != "oauth-test" {
			t.Error("first-party headers lost")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-AIP-Route") != "" {
			t.Error("unsafe native header")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}, "Set-Cookie": []string{"secret"}}, Body: io.NopCloser(strings.NewReader("event: content_block_delta\ndata: {\"signature\":\"opaque\"}\n\n"))}, nil
	})
	r := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Host = "127.0.0.1:8080"
	r.Header.Set("User-Agent", "claude-cli/2.1.310")
	r.Header.Set("x-app", "cli")
	r.Header.Set("Authorization", "Bearer synthetic")
	r.Header.Set("anthropic-beta", "oauth-test")
	r.Header.Set("Cookie", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !w.Flushed || !strings.Contains(w.Body.String(), `"signature":"opaque"`) || w.Header().Get("Set-Cookie") != "" || w.Header().Get("X-AIP-Canonical-Processing") != "false" {
		t.Fatalf("unsafe or changed native response: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}

type hybridRoundTrip func(*http.Request) (*http.Response, error)

func (f hybridRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHybrid_RefusalsAndRetirement(t *testing.T) {
	for _, kind := range []string{"remote", "host", "query", "absolute", "credentials", "duplicate-auth", "blank-bearer", "connection-nomination", "oversize", "duplicate-model", "model-case", "model-case-first", "escaped-model-case", "retired"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h := newHybridHandler(nil, hybridConfig{Enabled: true, ExternalModels: []hybridModel{{Model: "aiproxer-reviewer", Route: "external:model"}}}, aipsdk.FrontendMountOptions{GenerationContext: ctx, MaxRequestBodyBytes: 128})
			calls := 0
			h.external = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(204) })
			h.native.Transport = hybridRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, context.Canceled })
			r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-test"}`))
			r.RemoteAddr = "127.0.0.1:1"
			r.Host = "127.0.0.1:8080"
			r.Header.Set("User-Agent", "claude-cli/2.1.310")
			r.Header.Set("x-app", "cli")
			r.Header.Set("Authorization", "Bearer synthetic")
			switch kind {
			case "remote":
				r.RemoteAddr = "192.0.2.1:1"
			case "host":
				r.Host = "evil.example"
			case "query":
				r.URL.RawQuery = "target=evil"
			case "absolute":
				r.URL.Scheme = "https"
				r.URL.Host = "evil.example"
			case "credentials":
				r.Header.Set("x-api-key", "other")
			case "duplicate-auth":
				r.Header.Add("Authorization", "Bearer another")
			case "blank-bearer":
				r.Header.Set("Authorization", "Bearer ")
			case "connection-nomination":
				r.Header.Set("Connection", "Authorization")
			case "oversize":
				r.Body = io.NopCloser(strings.NewReader(strings.Repeat(" ", 129)))
			case "duplicate-model":
				r.Body = io.NopCloser(strings.NewReader(`{"model":"claude-test","model":"other"}`))
			case "model-case":
				r.Body = io.NopCloser(strings.NewReader(`{"model":"aiproxer-reviewer","MODEL":"claude-test"}`))
			case "model-case-first":
				r.Body = io.NopCloser(strings.NewReader(`{"Model":"aiproxer-reviewer","model":"claude-test"}`))
			case "escaped-model-case":
				r.Body = io.NopCloser(strings.NewReader(`{"model":"aiproxer-reviewer","\u004dODEL":"claude-test"}`))
			case "retired":
				cancel()
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if strings.Contains(kind, "model-case") && w.Code != http.StatusBadRequest {
				t.Fatalf("ambiguous model must return HTTP 400: %d", w.Code)
			}
			if w.Code < 400 || calls != 0 {
				t.Fatalf("status=%d upstream=%d", w.Code, calls)
			}
		})
	}
}

func TestHybrid_ConfigStrictAndDisabled(t *testing.T) {
	for _, raw := range []string{
		"claude_code_hybrid: {enabled: true, typo: true}",
		"claude_code_hybrid: {enabled: true, external_models: [{model: claude-x, route: 'minimax:x'}]}",
		"claude_code_hybrid: {enabled: true, external_models: [{model: x, route: 'minimax:x|other:y'}]}",
		"claude_code_hybrid: {enabled: true, external_models: [{model: x, route: 'minimax:x'}, {model: x, route: 'minimax:y'}]}",
		"unknown: true",
	} {
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeConfig(n); err == nil {
			t.Errorf("accepted unsafe config %s", raw)
		}
	}
	var n yaml.Node
	_ = yaml.Unmarshal([]byte("expose_aip_usage_extensions: true"), &n)
	c, err := DecodeConfig(n)
	if err != nil || !c.ExposeAipUsageExtensions || c.ClaudeCodeHybrid.Enabled {
		t.Fatalf("disabled regression: %+v %v", c, err)
	}
}
