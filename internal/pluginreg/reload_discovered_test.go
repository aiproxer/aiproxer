package pluginreg_test

import (
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func TestReloadDiscovered_ActivationAfterFreeze(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	empty := yaml.Node{Kind: yaml.MappingNode}
	if err := reg.RegisterDiscoveredBackend("discovered-stub", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			BackendPrefixes: []string{"discovered-stub"},
			ModelInventory: modelinventory.StaticProvider{
				Source: modelinventory.SourceStaticBuiltin,
				Models: []modelinventory.Model{{CanonicalID: "discovered-stub/m", NativeID: "m", DisplayName: "m"}},
			},
		}, nil
	}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone}, pluginreg.BackendReloadPolicy{
		AllowsCandidateOverlap: true,
	}); err != nil {
		t.Fatal(err)
	}
	reg.FreezeDiscovery()
	if !reg.DiscoveryFrozen() {
		t.Fatal("discovery catalog must be frozen")
	}
	if !reg.HasBackend("discovered-stub") {
		t.Fatal("discovered kind must remain registered")
	}
	if _, err := reg.BuildBackend("discovered-stub", empty, nil, pluginreg.BackendFactoryDeps{}); err != nil {
		t.Fatalf("activate discovered: %v", err)
	}
}

func TestReloadDiscovered_OverlapPolicyDefaultsAllow(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("plain", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	pol, ok := reg.BackendReloadPolicy("plain")
	if !ok {
		t.Fatal("expected default reload policy")
	}
	if !pol.AllowsCandidateOverlap {
		t.Fatal("default must allow candidate overlap")
	}
}
