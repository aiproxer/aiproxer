package standardplugins

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

type snapshotMapEnv map[string]string

func (m snapshotMapEnv) Lookup(name string) (string, bool) {
	v, ok := m[name]
	return v, ok
}

func TestUpstreamAPIKeysWithPolicy_sparseAcrossGaps(t *testing.T) {
	t.Parallel()
	env := snapshotMapEnv{
		"OPENAI_API_KEY":   "s1",
		"OPENAI_API_KEY_3": "s3",
	}
	keys, err := ResolveUpstreamAPIKeysWithPolicy(env, config.DefaultUpstreamMaxEnvIndex)
	if err != nil {
		t.Fatalf("ResolveUpstreamAPIKeysWithPolicy: %v", err)
	}
	if len(keys.OpenAI) != 2 || keys.OpenAI[0] != "s1" || keys.OpenAI[1] != "s3" {
		t.Fatalf("OpenAI = %#v, want sparse [s1 s3]", keys.OpenAI)
	}
}

func TestUpstreamAPIKeysWithPolicy_dedupAndRoot1(t *testing.T) {
	t.Parallel()
	env := snapshotMapEnv{
		"ANTHROPIC_API_KEY":   "dup",
		"ANTHROPIC_API_KEY_2": "dup",
		"ANTHROPIC_API_KEY_3": "next",
	}
	keys, err := ResolveUpstreamAPIKeysWithPolicy(env, config.DefaultUpstreamMaxEnvIndex)
	if err != nil {
		t.Fatalf("ResolveUpstreamAPIKeysWithPolicy: %v", err)
	}
	if len(keys.Anthropic) != 2 || keys.Anthropic[0] != "dup" || keys.Anthropic[1] != "next" {
		t.Fatalf("Anthropic = %#v, want deduped [dup next]", keys.Anthropic)
	}

	_, err = ResolveUpstreamAPIKeysWithPolicy(snapshotMapEnv{"GEMINI_API_KEY_1": "misconfigured"}, 20)
	if err == nil {
		t.Fatal("expected root_1 rejection")
	}
	if !strings.Contains(err.Error(), "GEMINI_API_KEY_1") {
		t.Fatalf("error %q must name the misconfigured variable", err.Error())
	}
}

func TestUpstreamAPIKeysWithPolicy_boundedScan(t *testing.T) {
	t.Parallel()
	env := snapshotMapEnv{"OPENAI_API_KEY_7": "late"}
	keys, err := ResolveUpstreamAPIKeysWithPolicy(env, 7)
	if err != nil {
		t.Fatalf("ResolveUpstreamAPIKeysWithPolicy: %v", err)
	}
	if len(keys.OpenAI) != 1 || keys.OpenAI[0] != "late" {
		t.Fatalf("OpenAI = %#v, want [late] at max_env_index 7", keys.OpenAI)
	}
	keys, err = ResolveUpstreamAPIKeysWithPolicy(env, 6)
	if err != nil {
		t.Fatalf("ResolveUpstreamAPIKeysWithPolicy: %v", err)
	}
	if len(keys.OpenAI) != 0 {
		t.Fatalf("OpenAI = %#v, want [] below bound", keys.OpenAI)
	}
	if _, err := ResolveUpstreamAPIKeysWithPolicy(env, 999); err == nil {
		t.Fatal("expected max_env_index bounds rejection")
	}
}

func compatibleRowConfig(t *testing.T, kind, id, root, base string) config.PluginConfig {
	t.Helper()
	raw := fmt.Sprintf("backend_prefix: %s\nbase_url: %s\napi_key_env_var_root: %s\n", id, base, root)
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return config.PluginConfig{Kind: kind, ID: id, Enabled: true, Config: n}
}

func TestSnapshotCandidateCredentials_includesCustomRoots(t *testing.T) {
	t.Parallel()
	env := snapshotMapEnv{"OPENAI_API_KEY": "built-in", "SNAP_CUSTOM_API_KEY": "custom-1", "SNAP_CUSTOM_API_KEY_3": "custom-3"}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			compatibleRowConfig(t, CustomOpenAILegacyCompatibleID, "inst-a", "SNAP_CUSTOM_API_KEY", "http://127.0.0.1:9/v1"),
		}},
	}
	snap, err := SnapshotCandidateCredentials(env, cfg)
	if err != nil {
		t.Fatalf("SnapshotCandidateCredentials: %v", err)
	}
	if !snap.Resolved() {
		t.Fatal("expected authoritative snapshot")
	}
	if got := snap.Keys("OPENAI_API_KEY"); len(got) != 1 || got[0] != "built-in" {
		t.Fatalf("OPENAI_API_KEY = %#v", got)
	}
	if got := snap.Keys("SNAP_CUSTOM_API_KEY"); len(got) != 2 || got[0] != "custom-1" || got[1] != "custom-3" {
		t.Fatalf("SNAP_CUSTOM_API_KEY = %#v, want sparse [custom-1 custom-3]", got)
	}
	if snap.MaxEnvIndex != config.DefaultUpstreamMaxEnvIndex {
		t.Fatalf("MaxEnvIndex = %d, want default %d", snap.MaxEnvIndex, config.DefaultUpstreamMaxEnvIndex)
	}
}

func TestSnapshotCandidateCredentials_rejectsCustomRootCollision(t *testing.T) {
	t.Parallel()
	env := snapshotMapEnv{"SNAP_DUP_API_KEY": "shared"}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			compatibleRowConfig(t, CustomOpenAILegacyCompatibleID, "inst-a", "SNAP_DUP_API_KEY", "http://127.0.0.1:9/v1"),
			compatibleRowConfig(t, CustomAnthropicCompatibleID, "inst-b", "SNAP_DUP_API_KEY", "http://127.0.0.1:9"),
		}},
	}
	_, err := SnapshotCandidateCredentials(env, cfg)
	if err == nil {
		t.Fatal("expected cross-family root collision rejection")
	}
	if !strings.Contains(err.Error(), "SNAP_DUP_API_KEY") {
		t.Fatalf("error %q must name the colliding root", err.Error())
	}
}

func TestSnapshotCandidateCredentials_rejectsBuiltinRootReuse(t *testing.T) {
	t.Parallel()
	env := snapshotMapEnv{"OPENAI_API_KEY": "built-in"}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			compatibleRowConfig(t, CustomAnthropicCompatibleID, "inst-b", "OPENAI_API_KEY", "http://127.0.0.1:9"),
		}},
	}
	_, err := SnapshotCandidateCredentials(env, cfg)
	if err == nil {
		t.Fatal("expected built-in root reuse rejection")
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("error %q must name the colliding root", err.Error())
	}
}

func TestSnapshotCandidateCredentials_rejectsInvalidPolicy(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{UpstreamCredentials: config.UpstreamCredentialsConfig{MaxEnvIndex: 999}}
	_, err := SnapshotCandidateCredentials(snapshotMapEnv{}, cfg)
	if err == nil {
		t.Fatal("expected invalid policy rejection")
	}
	if !strings.Contains(err.Error(), "max_env_index") {
		t.Fatalf("error %q must name max_env_index", err.Error())
	}
}

func TestSnapshotCandidateCredentials_bindsQuotaSwitchThreshold(t *testing.T) {
	t.Parallel()
	env := snapshotMapEnv{"OPENAI_API_KEY": "s1"}

	def, err := SnapshotCandidateCredentials(env, &config.Config{})
	if err != nil {
		t.Fatalf("SnapshotCandidateCredentials: %v", err)
	}
	if def.QuotaSwitchRetryAfter != time.Hour {
		t.Fatalf("default QuotaSwitchRetryAfter = %v, want 1h", def.QuotaSwitchRetryAfter)
	}

	custom := &config.Config{UpstreamCredentials: config.UpstreamCredentialsConfig{QuotaSwitchRetryAfter: "30m"}}
	snap, err := SnapshotCandidateCredentials(env, custom)
	if err != nil {
		t.Fatalf("SnapshotCandidateCredentials: %v", err)
	}
	if snap.QuotaSwitchRetryAfter != 30*time.Minute {
		t.Fatalf("QuotaSwitchRetryAfter = %v, want 30m", snap.QuotaSwitchRetryAfter)
	}
	keys := upstreamKeysFromSnapshot(snap)
	if keys.QuotaSwitchThreshold != 30*time.Minute {
		t.Fatalf("UpstreamAPIKeys.QuotaSwitchThreshold = %v, want 30m", keys.QuotaSwitchThreshold)
	}

	bad := &config.Config{UpstreamCredentials: config.UpstreamCredentialsConfig{QuotaSwitchRetryAfter: "bogus"}}
	if _, err := SnapshotCandidateCredentials(env, bad); err == nil {
		t.Fatal("expected invalid quota_switch_retry_after rejection")
	} else if !strings.Contains(err.Error(), "quota_switch_retry_after") {
		t.Fatalf("error %q must name quota_switch_retry_after", err.Error())
	}
}

func TestSnapshotCandidateCredentials_nilEnvFallsBack(t *testing.T) {
	t.Parallel()
	snap, err := SnapshotCandidateCredentials(nil, &config.Config{})
	if err != nil {
		t.Fatalf("nil env must fall back, got %v", err)
	}
	if snap.Resolved() {
		t.Fatal("nil env must not produce an authoritative snapshot")
	}
}

func TestBackendFactory_prefersCandidateSnapshot(t *testing.T) {
	t.Parallel()
	legacy := UpstreamAPIKeys{OpenAI: []string{"legacy-key"}}
	var got UpstreamAPIKeys
	inner := func(_ yaml.Node, _ *http.Client, keys UpstreamAPIKeys, _ identity.Config) (execbackend.Backend, error) {
		got = keys
		return execbackend.Backend{}, nil
	}
	factory := backendFactory(legacy, inner)
	if _, err := factory(yaml.Node{}, nil, pluginreg.BackendFactoryDeps{}); err != nil {
		t.Fatalf("factory: %v", err)
	}
	if len(got.OpenAI) != 1 || got.OpenAI[0] != "legacy-key" {
		t.Fatalf("empty deps must fall back to Install-time keys, got %#v", got.OpenAI)
	}
	snap := pluginreg.HostCredentialSnapshot{
		ByRoot:      map[string][]string{"OPENAI_API_KEY": {"candidate-key"}},
		MaxEnvIndex: config.DefaultUpstreamMaxEnvIndex,
	}
	if _, err := factory(yaml.Node{}, nil, pluginreg.BackendFactoryDeps{Credentials: snap}); err != nil {
		t.Fatalf("factory: %v", err)
	}
	if len(got.OpenAI) != 1 || got.OpenAI[0] != "candidate-key" {
		t.Fatalf("candidate snapshot must win over Install-time keys, got %#v", got.OpenAI)
	}
}

const snapshotServingResourceJSON = `{
  "id": "resp_snap_1",
  "object": "response",
  "created_at": 1719900000,
  "status": "completed",
  "model": "model-x",
  "output": [
    {"type": "message", "id": "msg_out_1", "status": "completed", "role": "assistant", "content": [{"type": "output_text", "text": "snapshot-ok"}]}
  ],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`

func snapshotServingCall() aipapi.Call {
	return aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenResponsesCreate,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
		Items: []aipapi.Item{{
			Kind:   aipapi.ItemKindMessage,
			ID:     "msg_1",
			Status: aipapi.ItemStatusCompleted,
			Role:   aipapi.RoleUser,
			Content: []aipapi.ContentPart{{
				Kind: aipapi.ContentPartText,
				Text: "ping",
			}},
		}},
	}
}

// TestCredentialSnapshotServingKeepsAdmittedKeys proves the serving path uses
// the candidate-build snapshot: rotating the process environment after build
// must not change the credential an admitted backend sends.
func TestCredentialSnapshotServingKeepsAdmittedKeys(t *testing.T) {
	root := "SNAP_SERVE_API_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "admitted-key")

	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, snapshotServingResourceJSON)
	}))
	t.Cleanup(srv.Close)

	reg := customCompatibleRegistry(t)
	raw := fmt.Sprintf(`backend_prefix: snap-serve
base_url: %s
api_key_env_var_root: %s
`, srv.URL, root)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be := buildCompatibleBackend(t, reg, CustomOpenResponsesCompatibleID, "snap-serve", node, srv.Client())

	t.Setenv(root, "rotated-key")
	openDrainCompatible(t, be, snapshotServingCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if auth != "Bearer admitted-key" {
		t.Fatalf("serving auth = %q, want admitted candidate-build key", auth)
	}
}
