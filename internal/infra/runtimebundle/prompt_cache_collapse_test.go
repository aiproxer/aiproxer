package runtimebundle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

type cacheUsageProbe struct {
	before func()
	err    error
	calls  int
}

func (p *cacheUsageProbe) OnUsage(context.Context, usage.Event) error {
	p.calls++
	if p.before != nil {
		p.before()
	}
	return p.err
}

func TestPromptCacheCollapse_ProcessAndGeneration(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			cfg := &config.Config{Continuity: config.ContinuityConfig{InMemory: true}, Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}}}
			cfg.Observability.Metrics.Enabled = enabled
			cfg.Observability.Metrics.Path = "/metrics"
			cfg.Server.Address = "127.0.0.1:0"
			if err := config.Validate(cfg); err != nil {
				t.Fatal(err)
			}
			probe := &cacheUsageProbe{}
			var logs bytes.Buffer
			contributions := aipfeature.NewContributionSet()
			if err := aipfeature.Contribute(contributions, aipfeature.PlaneUsageObservers, "probe", []usage.Observer{probe}); err != nil {
				t.Fatal(err)
			}
			ps, err := NewProcessServices(t.Context(), ProcessServicesInput{Cfg: cfg, Log: slog.New(slog.NewJSONHandler(&logs, nil)), Opts: &BuildOptions{PluginRegistry: pluginreg.NewRegistry(), FeaturePlanes: contributions.Freeze()}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ps.Close() })
			count := func() float64 {
				if ps.Metrics == nil {
					return 0
				}
				families, err := ps.Metrics.Registry.Gather()
				if err != nil {
					t.Fatal(err)
				}
				var n float64
				for _, f := range families {
					if f.GetName() == "aip_prompt_cache_collapse_total" {
						for _, m := range f.GetMetric() {
							n += m.GetCounter().GetValue()
						}
					}
				}
				return n
			}
			first, err := compileCandidate(t.Context(), GenerationCompileInput{Process: ps})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = first.Close() })
			observer := first.security.runtimeSnapshot.UsageObserver()
			ev := usage.Event{SessionID: "session", BackendID: "backend", Model: "model", BLegID: "warm", AttemptSeq: 1, InputTokens: 8000, CacheReadTokens: 8000}
			if err := observer.OnUsage(t.Context(), ev); err != nil {
				t.Fatal(err)
			}
			for i := range 4 {
				ev.BLegID, ev.AttemptSeq, ev.CacheReadTokens = fmt.Sprint(i), i+2, 0
				if err := observer.OnUsage(t.Context(), ev); err != nil {
					t.Fatal(err)
				}
			}
			nextCfg := *cfg
			next, err := compileCandidate(t.Context(), GenerationCompileInput{Process: ps, Candidate: &nextCfg})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = next.Close() })
			observer = next.security.runtimeSnapshot.UsageObserver()
			probe.before = func() {
				if n := count(); n != 0 {
					t.Fatalf("detector ran before feature observer: %v", n)
				}
			}
			ev.BLegID, ev.AttemptSeq = "fifth", 6
			probe.err = errors.New("feature failure")
			if err := observer.OnUsage(t.Context(), ev); !errors.Is(err, probe.err) {
				t.Fatalf("chain error=%v", err)
			}
			if n := count(); n != 0 {
				t.Fatalf("error did not stop chain: %v", n)
			}
			probe.err = nil
			if err := observer.OnUsage(t.Context(), ev); err != nil {
				t.Fatal(err)
			}
			want := float64(0)
			if enabled {
				want = 1
			}
			if n := count(); n != want {
				t.Fatalf("reload trips=%v want %v", n, want)
			}
			if probe.calls != 7 {
				t.Fatalf("feature calls=%d want 7", probe.calls)
			}
			// The episode spans a reloaded candidate, so the surviving
			// detector attributes it to the config generation change while
			// still counting it once.
			if enabled && !strings.Contains(logs.String(), `"cause":"config-generation"`) {
				t.Fatalf("missing config-generation cause in %s", logs.String())
			}
			if !enabled && ps.Metrics != nil {
				t.Fatal("disabled metrics constructed a bundle")
			}
		})
	}
}

func TestPromptCacheCollapse_InvalidThresholdsFailStartup(t *testing.T) {
	cfg := &config.Config{Continuity: config.ContinuityConfig{InMemory: true}, Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}}}
	cfg.Observability.Metrics.Enabled = true
	cfg.Observability.Metrics.Path = "/metrics"
	cfg.Server.Address = "127.0.0.1:0"
	bad := 0
	cfg.Observability.Metrics.PromptCacheCollapse.MinPromptTokens = &bad
	contributions := aipfeature.NewContributionSet()
	ps, err := NewProcessServices(t.Context(), ProcessServicesInput{Cfg: cfg, Log: slog.New(slog.DiscardHandler), Opts: &BuildOptions{PluginRegistry: pluginreg.NewRegistry(), FeaturePlanes: contributions.Freeze()}})
	if err == nil {
		_ = ps.Close()
		t.Fatal("invalid collapse thresholds must fail process startup")
	}
}

func TestPromptCacheCollapse_CustomThresholdsReachDetector(t *testing.T) {
	minPrompt := 100
	cfg := &config.Config{Continuity: config.ContinuityConfig{InMemory: true}, Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}}}
	cfg.Observability.Metrics.Enabled = true
	cfg.Observability.Metrics.Path = "/metrics"
	cfg.Server.Address = "127.0.0.1:0"
	cfg.Observability.Metrics.PromptCacheCollapse.MinPromptTokens = &minPrompt
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	contributions := aipfeature.NewContributionSet()
	ps, err := NewProcessServices(t.Context(), ProcessServicesInput{Cfg: cfg, Log: slog.New(slog.DiscardHandler), Opts: &BuildOptions{PluginRegistry: pluginreg.NewRegistry(), FeaturePlanes: contributions.Freeze()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	if ps.Metrics == nil || ps.Metrics.PromptCacheCollapse == nil {
		t.Fatal("enabled metrics must construct the collapse detector")
	}
	// A 100-token prompt is below the historical 8000 floor: with the custom
	// minimum it must still drive one full episode.
	first, err := compileCandidate(t.Context(), GenerationCompileInput{Process: ps})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	observer := first.security.runtimeSnapshot.UsageObserver()
	ev := usage.Event{SessionID: "session", BackendID: "backend", Model: "model", BLegID: "warm", AttemptSeq: 1, InputTokens: 100, CacheReadTokens: 100}
	if err := observer.OnUsage(t.Context(), ev); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		ev.BLegID, ev.AttemptSeq, ev.CacheReadTokens = fmt.Sprint(i), i+2, 0
		if err := observer.OnUsage(t.Context(), ev); err != nil {
			t.Fatal(err)
		}
	}
	families, err := ps.Metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var n float64
	for _, f := range families {
		if f.GetName() == "aip_prompt_cache_collapse_total" {
			for _, m := range f.GetMetric() {
				n += m.GetCounter().GetValue()
			}
		}
	}
	if n != 1 {
		t.Fatalf("custom-threshold trips=%v want 1", n)
	}
}
