package runtimebundle_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguardhost"
	"gopkg.in/yaml.v3"
)

type leakTestEnv struct {
	vals map[string]string
}

func (m *leakTestEnv) Lookup(name string) (string, bool) {
	v, ok := m.vals[name]
	return v, ok
}

func (m *leakTestEnv) Snapshot() []string {
	out := make([]string, 0, len(m.vals))
	for k, v := range m.vals {
		out = append(out, k+"="+v)
	}
	return out
}

func assertNoSecretGuardFixtureLeak(tb testing.TB, haystacks ...string) {
	tb.Helper()
	for _, h := range haystacks {
		for _, s := range testkit.AllSyntheticSecretGuardValues() {
			if s != "" && strings.Contains(h, s) {
				tb.Fatalf("synthetic secret leaked into output (haystack len=%d)", len(h))
			}
		}
		for _, s := range testkit.AllSyntheticSecretGuardNeedles() {
			if s != "" && strings.Contains(h, s) {
				tb.Fatalf("synthetic secret substring leaked into output (haystack len=%d)", len(h))
			}
		}
	}
}

func TestBuild_secretGuardBlock_noSyntheticSecretLeakageInLogsOrErrors(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticOpenAIAPIKey
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeNoAuth},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "openai-only", Enabled: true, Config: empty,
			}},
			Features: []config.PluginConfig{{
				Kind: "secrets-guard", ID: "secrets-guard", Enabled: true,
				Config: mustSecretGuardYAML(t, "action: block\naudit_failure_policy: fail_closed\n"),
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	_, b := mustProcessAndCandidateLog(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Production: runtimebundle.ProductionOptions{
			FeatureHostRegistrations: []featurehost.Registration{
				(&secretguardhost.Binding{
					Environment: &leakTestEnv{vals: map[string]string{
						"OPENAI_API_KEY": secret,
					}},
					SingleUser: secretguardhost.SingleUserOptions{MinSecretBytes: 8},
				}).Registration(),
			},
		},
	}, log)
	if b.Executor() == nil || b.Executor().RuntimeSnapshot == nil {
		t.Fatal("expected executor snapshot")
	}
	plane := b.Executor().RuntimeSnapshot.SecretGuardPlane()
	if len(plane.Guards) == 0 {
		t.Fatal("enabled secrets-guard build must expose non-empty SecretGuards on RuntimeSnapshot")
	}
	if plane.MatcherResolver == nil {
		t.Fatal("enabled secrets-guard build must bind a non-nil SecretMatcherResolver")
	}
	if plane.DecisionObserver == nil {
		t.Fatal("expected secret decision observer to be wired")
	}

	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "user-sg-leak"})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("token=" + secret)},
		}},
	}
	stream, execErr := b.Executor().Execute(ctx, call)
	if stream != nil {
		_, _ = aipapi.Collect(t.Context(), stream)
	}
	if execErr == nil {
		t.Fatal("expected secret-guard block denial")
	}
	if !aipapi.IsPolicyDenied(execErr) {
		t.Fatalf("want policy denied, got %v", execErr)
	}
	assertNoSecretGuardFixtureLeak(t, execErr.Error(), logBuf.String())
	if !strings.Contains(logBuf.String(), "aip.secret_guard.decision") {
		t.Fatalf("expected secret-guard decision log (buf len=%d)", logBuf.Len())
	}
}

func mustSecretGuardYAML(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}
