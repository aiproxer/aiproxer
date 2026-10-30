package runtimebundle_test

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

func newSeamKeyProcess(t *testing.T, seamEnabled bool) (*runtimebundle.ProcessServices, func()) {
	t.Helper()
	return newDigestKeyProcess(t, true, seamEnabled)
}

func newDigestKeyProcess(t *testing.T, metricsEnabled, seamEnabled bool) (*runtimebundle.ProcessServices, func()) {
	t.Helper()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Observability: config.ObservabilityConfig{
			Metrics: config.MetricsConfig{Enabled: metricsEnabled},
		},
		Server: config.ServerConfig{
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 1024,
		},
	}
	cfg.Observability.Metrics.PromptCacheCollapse.SeamDigestsEnabled = seamEnabled
	opts := &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  slog.Default(),
		Opts: opts,
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	return ps, func() { _ = ps.Close() }
}

func stubCompose(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
	return http.NotFoundHandler(), nil
}

func processSeamKey(t *testing.T, ps *runtimebundle.ProcessServices) [32]byte {
	t.Helper()
	if ps.Metrics == nil {
		t.Fatal("process metrics bundle is nil with metrics enabled")
	}
	return ps.Metrics.SeamDigestKey()
}

// TestProcessServices_SeamDigestKeyUniqueNonZero proves the process owns a
// secret key: every process generates a non-zero unique key in its metrics
// bundle, and the accessor returns a copy so callers cannot mutate it.
func TestProcessServices_SeamDigestKeyUniqueNonZero(t *testing.T) {
	t.Parallel()
	ps1, cleanup1 := newSeamKeyProcess(t, true)
	defer cleanup1()
	ps2, cleanup2 := newSeamKeyProcess(t, true)
	defer cleanup2()
	k1, k2 := processSeamKey(t, ps1), processSeamKey(t, ps2)
	if k1 == ([32]byte{}) || k2 == ([32]byte{}) {
		t.Fatal("process seam keys must never be zero")
	}
	if k1 == k2 {
		t.Fatal("process seam keys must be unique per process")
	}
	mutated := k1
	mutated[0] ^= 0xFF
	if processSeamKey(t, ps1) != k1 {
		t.Fatal("SeamDigestKey must return a copy: mutating the result changed process state")
	}
}

// TestGeneration_SeamDigestKeyStableAcrossRebuilds proves the
// process→generation boundary when the feed is active: two generations
// compiled from the same process borrow the same bundle key, so the same
// prompt hashes to the same digests after a reload. A per-generation key
// would fail this test.
func TestGeneration_SeamDigestKeyStableAcrossRebuilds(t *testing.T) {
	t.Parallel()
	ps, cleanup := newSeamKeyProcess(t, true)
	defer cleanup()
	want := processSeamKey(t, ps)
	if want == ([32]byte{}) {
		t.Fatal("process seam key must never be zero")
	}
	for i := range 2 {
		bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
			Process: ps,
			Compose: stubCompose,
		})
		if err != nil {
			t.Fatalf("CompileGeneration %d: %v", i, err)
		}
		t.Cleanup(func() { _ = bundle.Close() })
		exec := runtimebundle.GenerationExecutorOf(bundle)
		if exec == nil {
			t.Fatalf("generation %d executor is nil", i)
		}
		if exec.SeamDigestKey != want {
			t.Fatalf("generation %d borrowed key != process bundle key", i)
		}
	}
}

// TestGeneration_SeamDigestKeyNotBorrowedWhenDisabled proves the secret is
// not propagated when the feed is off: with the gate disabled the generation
// executor keeps the zero key, so an inactive diagnostic widens no exposure.
// Combined with the fail-closed request gate, a disabled feed can never
// publish digests.
func TestGeneration_SeamDigestKeyNotBorrowedWhenDisabled(t *testing.T) {
	t.Parallel()
	ps, cleanup := newSeamKeyProcess(t, false)
	defer cleanup()
	if processSeamKey(t, ps) == ([32]byte{}) {
		t.Fatal("process bundle key must exist even when the feed is disabled")
	}
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Compose: stubCompose,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	exec := runtimebundle.GenerationExecutorOf(bundle)
	if exec == nil {
		t.Fatal("generation executor is nil")
	}
	if !exec.SeamDigestKey.IsZero() {
		t.Fatal("disabled feed must not borrow the process key into the executor")
	}
	if exec.SeamDigestsEnabled {
		t.Fatal("disabled feed must leave the executor gate off")
	}
}

// TestGeneration_SystemPromptDigestKeyBorrowedWithMetrics proves the
// instruction-identity feed's key lifetime: with the collapse diagnostic
// enabled (metrics on) the executor borrows the process-stable key and arms
// the feed even when the per-item seam feed is off, which is the default
// configuration.
func TestGeneration_SystemPromptDigestKeyBorrowedWithMetrics(t *testing.T) {
	t.Parallel()
	ps, cleanup := newSeamKeyProcess(t, false)
	defer cleanup()
	want := processSeamKey(t, ps)
	if want == ([32]byte{}) {
		t.Fatal("process seam key must never be zero")
	}
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Compose: stubCompose,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	exec := runtimebundle.GenerationExecutorOf(bundle)
	if exec == nil {
		t.Fatal("generation executor is nil")
	}
	if !exec.SystemPromptDigestEnabled {
		t.Fatal("metrics-enabled generation must arm the instruction-identity feed")
	}
	if exec.SystemPromptDigestKey != want {
		t.Fatal("generation must borrow the process bundle key for the instruction-identity feed")
	}
}

// TestGeneration_SystemPromptDigestDisabledWithoutMetrics proves the cost gate
// at composition: with no metrics consumer the feed is off and no key is
// borrowed, so a deployment without the collapse diagnostic pays no hashing and
// widens no exposure.
func TestGeneration_SystemPromptDigestDisabledWithoutMetrics(t *testing.T) {
	t.Parallel()
	ps, cleanup := newDigestKeyProcess(t, false, false)
	defer cleanup()
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Compose: stubCompose,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	exec := runtimebundle.GenerationExecutorOf(bundle)
	if exec == nil {
		t.Fatal("generation executor is nil")
	}
	if exec.SystemPromptDigestEnabled {
		t.Fatal("metrics-disabled generation must leave the instruction-identity feed off")
	}
	if !exec.SystemPromptDigestKey.IsZero() {
		t.Fatal("metrics-disabled generation must not borrow a digest key")
	}
}
