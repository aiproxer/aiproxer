package stdhttp

import (
	"net/http"
	"strings"
	"testing"

	coreconfig "github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestMountBundledFrontends_nilMux(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", nil)
	reg := pluginreg.NewRegistry()
	err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: nil,
		Frontends: HTTPFrontendInput{
			Executor:             ex,
			DefaultRouteSelector: "stub:x",
			Plugins:              []coreconfig.PluginConfig{{ID: "openai-responses", Enabled: true}},
			MaxRequestBodyBytes:  0,
			Registry:             reg,
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil mux") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMountBundledFrontends_nilExec(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	reg := pluginreg.NewRegistry()
	err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             nil,
			DefaultRouteSelector: "stub:x",
			Plugins:              []coreconfig.PluginConfig{{ID: "openai-responses", Enabled: true}},
			MaxRequestBodyBytes:  0,
			Registry:             reg,
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil exec") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMountBundledFrontends_nilRegistry(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", nil)
	err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             ex,
			DefaultRouteSelector: "stub:x",
			Plugins:              []coreconfig.PluginConfig{{ID: "openai-responses", Enabled: true}},
			MaxRequestBodyBytes:  0,
			Registry:             nil,
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil plugin registry") {
		t.Fatalf("unexpected error: %v", err)
	}
}
