package stdhttp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// startTestApp starts app and registers Shutdown cleanup. Lifecycle only —
// callers compose HTTP separately via ComposeStandardHTTP.
func startTestApp(ctx context.Context, t *testing.T, app *runtime.App) {
	t.Helper()
	if app == nil {
		t.Fatal("nil app")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start app: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		app.Shutdown(shutdownCtx)
	})
}

// frontendInputForTest builds a focused Frontends group from cfg + executor/registry.
func frontendInputForTest(cfg *config.Config, ex *runtime.Executor, reg *pluginreg.Registry) HTTPFrontendInput {
	route := ""
	var maxBody int64
	var preKA aipsdk.FrontendKeepaliveConfig
	var plugins []config.PluginConfig
	if cfg != nil {
		route = DefaultRouteSelector(cfg)
		maxBody = cfg.Server.EffectiveMaxRequestBodyBytes()
		ka := cfg.Server.EffectivePreRequestKeepalive()
		preKA = aipsdk.FrontendKeepaliveConfig{Enabled: ka.Enabled, Interval: ka.Interval}
		plugins = cfg.Plugins.Frontends
	}
	return HTTPFrontendInput{
		Executor:             ex,
		Registry:             reg,
		DefaultRouteSelector: route,
		Plugins:              httpcontract.ClonePluginConfigs(plugins),
		MaxRequestBodyBytes:  maxBody,
		PreRequestKeepalive:  preKA,
	}
}

// candidateHTTPInput projects a real CandidateRuntime into focused StandardHTTPInput
// groups without an intermediate Built-mirroring struct.
func candidateHTTPInput(cfg *config.Config, cand *runtimebundle.CandidateHTTPCompile, regs []aipsdk.Registration) StandardHTTPInput {
	if cand == nil {
		return StandardHTTPInput{}
	}
	route := strings.TrimSpace(cand.DefaultRouteSelector())
	if route == "" && cfg != nil {
		route = DefaultRouteSelector(cfg)
	}
	return cand.StandardHTTPInput(cfg, regs, route)
}

func compileTestCandidate(t *testing.T, cfg *config.Config, reg *pluginreg.Registry) (*runtimebundle.ProcessServices, *runtimebundle.CandidateHTTPCompile) {
	t.Helper()
	if reg == nil {
		reg = pluginreg.NewRegistry()
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
	})
	if err != nil {
		_ = ps.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cand.Close()
		_ = ps.Close()
	})
	return ps, cand
}
