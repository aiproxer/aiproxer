package runtimebundle_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

func TestCandidateCompile_ProviderProfile_PreservesCapabilitiesAndPrefix(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "groq-test-key")
	var upstreamRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"llama-3.3-70b"}]}`))
	}))
	t.Cleanup(srv.Close)

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("profile: groq\n"), &node); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{
					ID:      "groq-candidate",
					Kind:    standardplugins.ProviderProfileKind,
					Enabled: true,
					Config:  node,
				},
			},
		},
	}

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	// Candidate compilation owns provider-profile preparation (raw-only contract):
	// pass the raw provider-profile row; compileCandidate prepares it once.
	// Pre-preparing here would double-prepare and hit the forged-marker guard.
	_, cand := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Infra: runtimebundle.InfraOptions{
			HTTPClient: srv.Client(),
		},
	})

	prefixes := cand.RoutePrefixes()
	if !slices.Contains(prefixes, "groq") {
		t.Fatalf("expected candidate RoutePrefixes to contain 'groq', got %v", prefixes)
	}

	// Prove capability ceiling is enforced on the candidate assembly executor:
	// vision is disabled by the groq profile, so a vision call fails before any upstream I/O.
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "groq-candidate:llama-3.3-70b"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind:      aipapi.PartImageRef,
				ImageRef:  "https://example.com/test.png",
				ImageMIME: "image/png",
			}},
		}},
	}

	_, execErr := cand.Executor().Execute(context.Background(), call)
	if execErr == nil {
		t.Fatal("expected execution error when required capability (vision) is disabled by compiled profile")
	}

	if reqs := upstreamRequests.Load(); reqs != 0 {
		t.Fatalf("expected zero upstream requests on rejected capability, got %d", reqs)
	}
}
