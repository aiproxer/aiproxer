package stdhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"gopkg.in/yaml.v3"
)

// Claim (#1511): hybrid candidates cannot publish remotely, with non-local
// auth, destination-changing aliases/overrides, or bypassed canonical policies.
// Mutant: delete the corresponding admission condition.
func TestClaudeCodeHybrid_AdmissionBeforePublication(t *testing.T) {
	for _, kind := range []string{"safe", "write-default", "write-short", "write-boundary", "remote", "multi", "auth", "alias", "override", "policy", "unknown-backend", "durable", "accounting", "concurrency", "control-plane", "workspace"} {
		t.Run(kind, func(t *testing.T) {
			var n yaml.Node
			if err := yaml.Unmarshal([]byte("claude_code_hybrid: {enabled: true, external_models: [{model: aiproxer-reviewer, route: 'minimax:MiniMax-M2.7', require_subagent: true}]}"), &n); err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{Server: config.ServerConfig{Address: "127.0.0.1:0", WriteTimeout: "6m"}, Plugins: config.PluginsConfig{Frontends: []config.PluginConfig{{ID: "anthropic", Enabled: true, Config: n}}, Backends: []config.PluginConfig{{ID: "minimax", Enabled: true}}}}
			switch kind {
			case "write-default":
				cfg.Server.WriteTimeout = ""
			case "write-short":
				cfg.Server.WriteTimeout = "5m"
			case "write-boundary":
				cfg.Server.WriteTimeout = "5m59.999999999s"
			case "remote":
				cfg.Server.Address = "0.0.0.0:8080"
			case "multi":
				cfg.Access.Mode = "multi_user"
			case "auth":
				cfg.Auth.Handler = "local_api_key"
			case "alias":
				cfg.ModelAliases = []config.ModelAliasConfig{{Pattern: ".*", Replacement: "other:model"}}
			case "override":
				cfg.Routing.OverrideAdmin.Enabled = true
			case "policy":
				cfg.Plugins.Features = []config.PluginConfig{{ID: "secret-guard", Enabled: true}}
			case "unknown-backend":
				cfg.Plugins.Backends = nil
			case "durable":
				cfg.Continuity.Store = "sqlite"
			case "accounting":
				cfg.Accounting.Authority.Enabled = true
			case "concurrency":
				cfg.Accounting.Concurrency.Enabled = true
			case "control-plane":
				cfg.ControlPlane.Enabled = true
			case "workspace":
				cfg.SecureSession.RequireWorkspaceID = true
			}
			err := standardplugins.ValidateFrontendDeployment(cfg)
			if (err == nil) != (kind == "safe") {
				t.Fatalf("admission=%v", err)
			}
		})
	}
}

// Claim (#1511): a real standard auth/self-defense/frontend/executor/backend
// stack routes only to the approved leaf using its credential, never the
// original OAuth/header canaries. Mutant: pass the original request downstream.
func TestClaudeCodeHybrid_RealStackExternalToolRoundtrip(t *testing.T) {
	upstream := make(chan string, 4)
	inner := refbackend.NewHandler(refbackend.Config{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var wire struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &wire)
		if wire.Model != "gpt-4o-mini" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Error("approved backend model/credential not used")
		}
		if len(wire.Messages) < 2 || wire.Messages[0].Role != "user" || wire.Messages[1].Role != "system" || string(wire.Messages[1].Content) != `"worker instructions"` {
			t.Errorf("inline system role/order/content lost: %s", body)
		}
		upstream <- fmt.Sprint(r.Header) + string(body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()
	var back, front yaml.Node
	if err := yaml.Unmarshal(fmt.Appendf(nil, "base_url: %q\nmodels:\n  source: inline\n  items:\n    - canonical_id: openai/gpt-4o-mini\n      native_id: gpt-4o-mini\n", srv.URL+"/v1"), &back); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte("claude_code_hybrid: {enabled: true, external_models: [{model: aiproxer-reviewer, route: 'external:gpt-4o-mini', require_subagent: true}]}"), &front); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Server: config.ServerConfig{Address: "127.0.0.1:0", WriteTimeout: "6m"}, Continuity: config.ContinuityConfig{InMemory: true}, Plugins: config.PluginsConfig{Frontends: []config.PluginConfig{{ID: "anthropic", Enabled: true, Config: front}}, Backends: []config.PluginConfig{{ID: "external", Kind: "openai-legacy", Enabled: true, Config: back}}}}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	reg := testRegistryWithStdBundle(t)
	_, cand := compileTestCandidate(t, cfg, reg)
	logPath := t.TempDir() + "/hybrid.log"
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()
	log := slog.New(slog.NewJSONHandler(logFile, nil))
	in := candidateHTTPInput(cfg, cand, nil)
	in.Frontends.Log = log
	h, err := ComposeStandardHTTP(t.Context(), cfg, log, in)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	for turn, body := range []string{
		`{"model":"aiproxer-reviewer","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"weather?"},{"role":"system","content":"worker instructions"}],"tools":[{"name":"weather","description":"weather","input_schema":{"type":"object","properties":{}}}]}`,
		`{"model":"aiproxer-reviewer","max_tokens":64,"metadata":{"aip_session_id":"forged-old-session","aip_resume_token":"forged-old-proof"},"messages":[{"role":"user","content":"weather?"},{"role":"system","content":"worker instructions"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"weather","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"sunny"}]}]}`,
	} {
		r, err := http.NewRequestWithContext(context.Background(), "POST", server.URL+"/v1/messages?beta=true", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("User-Agent", "claude-cli/2.1.310")
		r.Header.Set("x-app", "cli")
		r.Header.Set("x-claude-code-agent-id", "worker")
		r.Header.Set("Authorization", "Bearer original-oauth-canary")
		r.Header.Set("Cookie", "original-cookie-canary")
		r.Header.Set("anthropic-beta", "original-beta-canary")
		r.Header.Set("Proxy-Authorization", "original-proxy-canary")
		resp, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			logs, _ := os.ReadFile(logPath)
			t.Fatalf("turn %d: %d %s logs=%s", turn, resp.StatusCode, raw, logs)
		}
		if turn == 0 && !strings.Contains(string(raw), `"type":"tool_use"`) {
			t.Fatalf("tool call missing: %s", raw)
		}
		if turn == 1 && !strings.Contains(string(raw), "ok") {
			t.Fatalf("tool continuation missing: %s", raw)
		}
		select {
		case capture := <-upstream:
			if strings.Contains(capture, "original-") {
				t.Fatal("original credential/header canary reached external backend")
			}
			if turn == 1 && !strings.Contains(capture, "sunny") {
				t.Fatalf("tool result lost: %s", capture)
			}
		default:
			t.Fatal("approved backend not called")
		}
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "original-") {
		t.Fatal("credential/header canary reached logs")
	}
	// Reusing the same real stack must deny a forged route before any I/O.
	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"aiproxer-reviewer"}`))
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("User-Agent", "claude-cli/2.1.310")
	r.Header.Set("x-app", "cli")
	r.Header.Set("X-AIP-Route", "other:model")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code < 400 {
		t.Fatal("forged route accepted")
	}
	select {
	case <-upstream:
		t.Fatal("denial attempted upstream")
	default:
	}
}
