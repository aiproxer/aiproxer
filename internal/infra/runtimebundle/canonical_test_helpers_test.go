package runtimebundle_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

// compileCandidateWithFeatures compiles a candidate through the production
// candidate compile (feature surface, feature host, assembly) with opts as the
// candidate overlay.
func compileCandidateWithFeatures(ctx context.Context, ps *runtimebundle.ProcessServices, _ *config.Config, opts *runtimebundle.BuildOptions) (*runtimebundle.CandidateHTTPCompile, error) {
	return runtimebundle.CompileCandidate(ctx, runtimebundle.GenerationCompileInput{
		Process:       ps,
		CandidateOpts: opts,
	})
}

// processAndCandidateErr is like mustProcessAndCandidate but returns compile/process
// errors for negative tests. Non-nil ProcessServices/CandidateRuntime are registered
// for cleanup (candidate before process). Unlike mustProcessAndCandidate, a nil
// PluginRegistry is preserved so negative registry tests can observe the error.
func processAndCandidateErr(t *testing.T, cfg *config.Config, opts *runtimebundle.BuildOptions) (*runtimebundle.ProcessServices, *runtimebundle.CandidateHTTPCompile, error) {
	t.Helper()
	if cfg == nil {
		return nil, nil, fmt.Errorf("nil config")
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		return nil, nil, err
	}
	cand, err := compileCandidateWithFeatures(context.Background(), ps, cfg, opts)
	if err != nil {
		// Close process immediately on compile failure so process-owned closers
		// are not left solely to deferred cleanup.
		_ = ps.Close()
		return nil, nil, err
	}
	// Single cleanup: candidate before process (canonical ownership order).
	t.Cleanup(func() {
		_ = cand.Close()
		_ = ps.Close()
	})
	return ps, cand, nil
}

// mustProcessAndCandidate constructs ProcessServices once and compiles one
// CandidateRuntime for behavior tests that previously called runtimebundle.Build.
// Cleanup closes the candidate first, then the process (canonical ownership order).
func mustProcessAndCandidate(t *testing.T, cfg *config.Config, opts *runtimebundle.BuildOptions) (*runtimebundle.ProcessServices, *runtimebundle.CandidateHTTPCompile) {
	t.Helper()
	if opts == nil {
		opts = &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()}
	}
	if opts.PluginRegistry == nil {
		opts.PluginRegistry = pluginreg.NewRegistry()
	}
	ps, cand, err := processAndCandidateErr(t, cfg, opts)
	if err != nil {
		t.Fatalf("process+candidate: %v", err)
	}
	if cand == nil {
		t.Fatal("expected candidate")
	}
	return ps, cand
}

// mustProcessAndCandidateLog is mustProcessAndCandidate with an explicit process logger
// (for secret-safe log capture tests).
func mustProcessAndCandidateLog(t *testing.T, cfg *config.Config, opts *runtimebundle.BuildOptions, log *slog.Logger) (*runtimebundle.ProcessServices, *runtimebundle.CandidateHTTPCompile) {
	t.Helper()
	if opts == nil {
		opts = &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()}
	}
	if opts.PluginRegistry == nil {
		opts.PluginRegistry = pluginreg.NewRegistry()
	}
	if log == nil {
		t.Fatal("nil logger")
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  log,
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	cand, err := compileCandidateWithFeatures(context.Background(), ps, cfg, opts)
	if err != nil {
		_ = ps.Close()
		t.Fatalf("CompileCandidate: %v", err)
	}
	t.Cleanup(func() {
		_ = cand.Close()
		_ = ps.Close()
	})
	return ps, cand
}

// mustProcessAndGeneration compiles a GenerationRuntime with ComposeStandardHTTP
// for tests that need a published-shape handler without a listener.
func mustProcessAndGeneration(t *testing.T, cfg *config.Config, opts *runtimebundle.BuildOptions) (*runtimebundle.ProcessServices, runtimebundle.GenerationRuntime) {
	t.Helper()
	if cfg == nil {
		t.Fatal("nil config")
	}
	if opts == nil {
		opts = &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()}
	}
	if opts.PluginRegistry == nil {
		reg := pluginreg.NewRegistry()
		if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
			t.Fatal(err)
		}
		opts.PluginRegistry = reg
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	gen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		_ = ps.Close()
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() {
		_ = gen.Close()
		_ = ps.Close()
	})
	return ps, gen
}

func TestMustProcessAndCandidate_CleanupClosesCandidateBeforeProcess(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
	}
	ps, cand := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if ps == nil || cand == nil {
		t.Fatal("expected process+candidate")
	}
	// Contract: candidate Close must not close process services; process Close is separate.
	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}
	if ps.Closed() {
		t.Fatal("process must still be open after candidate Close")
	}
	if err := ps.Close(); err != nil {
		t.Fatal(err)
	}
	if !ps.Closed() {
		t.Fatal("process must report Closed after Close")
	}
}
