package diagnostics_test

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/diagnostics"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/discovery"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

func TestInspect_MinimalBuiltinNoLaunch(t *testing.T) {
	t.Parallel()
	var launches atomic.Int64
	rep, err := diagnostics.Inspect(diagnostics.InspectInput{
		DiscoveryEnabled: false,
		BuiltinKinds:     []string{"openai-responses", "anthropic"},
		Discover: func(discovery.Config) (discovery.Result, error) {
			launches.Add(1)
			t.Fatal("discover must not run when disabled")
			return discovery.Result{}, nil
		},
		Trust: func(string, sdkmanifest.Manifest, trust.VerifyOptions) trust.VerifyResult {
			launches.Add(1)
			t.Fatal("trust must not run when discovery disabled")
			return trust.VerifyResult{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if launches.Load() != 0 {
		t.Fatalf("launches=%d", launches.Load())
	}
	if len(rep.Entries) < 2 {
		t.Fatalf("%+v", rep.Entries)
	}
	for _, e := range rep.Entries {
		if e.Source == "builtin" && e.State != catalog.StateBuiltin {
			t.Fatalf("%+v", e)
		}
		if strings.Contains(string(e.Reason), `\`) || strings.Contains(string(e.Reason), `/home`) {
			t.Fatalf("path leaked in reason: %q", e.Reason)
		}
	}
}

func TestInspect_MissingPluginConfigured(t *testing.T) {
	t.Parallel()
	rep, err := diagnostics.Inspect(diagnostics.InspectInput{
		DiscoveryEnabled: true,
		Discovery:        discovery.Config{ExplicitPaths: []string{t.TempDir()}, Development: true},
		BuiltinKinds:     []string{"openai-responses"},
		Configured: []diagnostics.ConfiguredBackend{
			{InstanceID: "ext-1", Kind: "synthetic-external", Enabled: true},
		},
		Discover: func(discovery.Config) (discovery.Result, error) {
			return discovery.Result{}, nil
		},
	})
	if err == nil {
		t.Fatal("expected catalog unresolved error for missing configured kind")
	}
	for _, e := range rep.Entries {
		if e.InstanceID == "ext-1" && e.Reason == catalog.ReasonEnabledMissing {
			if strings.Contains(string(e.Reason), "secret") {
				t.Fatal("secret leak")
			}
			return
		}
	}
	t.Fatalf("missing configured instance not reported: %+v", rep.Entries)
}

func TestInspect_ConflictAndIncompatible(t *testing.T) {
	t.Parallel()
	d1 := discovery.Descriptor{
		SafeID: "r/a.json", Status: discovery.StatusDiscovered,
		Manifest: sdkmanifest.Manifest{
			PluginID: "io.dup", ProtocolMajor: 1, ProtocolMinMinor: 0, ProtocolMaxMinor: 1,
			Exports: []sdkmanifest.Export{{Kind: "shared-kind"}},
		},
	}
	d2 := discovery.Descriptor{
		SafeID: "r/b.json", Status: discovery.StatusDiscovered,
		Manifest: sdkmanifest.Manifest{
			PluginID: "io.dup", ProtocolMajor: 1, ProtocolMinMinor: 0, ProtocolMaxMinor: 1,
			Exports: []sdkmanifest.Export{{Kind: "other"}},
		},
	}
	d3 := discovery.Descriptor{
		SafeID: "r/p.json", Status: discovery.StatusDiscovered,
		Manifest: sdkmanifest.Manifest{
			PluginID: "io.p", ProtocolMajor: 1, ProtocolMinMinor: 9, ProtocolMaxMinor: 9,
			Exports: []sdkmanifest.Export{{Kind: "proto-x"}},
		},
	}
	rep, err := diagnostics.Inspect(diagnostics.InspectInput{
		DiscoveryEnabled: true,
		HostMajor:        1,
		HostMinor:        0,
		Discover: func(discovery.Config) (discovery.Result, error) {
			return discovery.Result{Descriptors: []discovery.Descriptor{d1, d2, d3}}, nil
		},
		Trust: func(_ string, m sdkmanifest.Manifest, _ trust.VerifyOptions) trust.VerifyResult {
			return trust.VerifyResult{Reason: trust.ReasonOK, Artifact: &trust.VerifiedArtifact{}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var conflict, incompatible bool
	for _, e := range rep.Entries {
		if e.Reason == catalog.ReasonDuplicatePluginID {
			conflict = true
			if len(e.Owners) < 2 {
				t.Fatalf("conflict owners: %+v", e)
			}
		}
		if e.State == catalog.StateIncompatible {
			incompatible = true
		}
	}
	if !conflict || !incompatible {
		t.Fatalf("conflict=%v incompatible=%v entries=%+v", conflict, incompatible, rep.Entries)
	}
}

func TestDiscoveryFromConfig_MapsFlags(t *testing.T) {
	t.Parallel()
	cfg := diagnostics.DiscoveryFromConfig(config.BackendDiscoveryConfig{
		Enabled: true, Paths: []string{"/a"}, DevelopmentMode: true,
	})
	if !cfg.Development || len(cfg.ExplicitPaths) != 1 || cfg.IncludeUpstreamDefaults {
		t.Fatalf("%+v", cfg)
	}
	prod := diagnostics.DiscoveryFromConfig(config.BackendDiscoveryConfig{Enabled: true, Paths: []string{"/a"}})
	if prod.Development || !prod.IncludeUpstreamDefaults {
		t.Fatalf("%+v", prod)
	}
	off := diagnostics.DiscoveryFromConfig(config.BackendDiscoveryConfig{})
	if len(off.ExplicitPaths) != 0 || off.IncludeUpstreamDefaults || off.Development {
		t.Fatalf("%+v", off)
	}
}
