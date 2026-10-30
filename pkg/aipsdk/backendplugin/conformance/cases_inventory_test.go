package conformance

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type provenanceInstance struct {
	source  string
	fetched int64
}

func (p *provenanceInstance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{}, nil
}

func (p *provenanceInstance) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{
		Models: []backendplugin.ModelDescriptor{{
			CanonicalModelID: "fake/m",
			NativeModelID:    "m",
			FactoryKind:      "fake-prov",
		}},
		InventorySource: p.source,
		FetchedUnixMS:   p.fetched,
	}, nil
}

func (p *provenanceInstance) Execute(backendplugin.ExecuteStream) error { return nil }

func (p *provenanceInstance) Close(context.Context) error { return nil }

type provenanceService struct {
	dynamic bool
	inst    *provenanceInstance
}

func (s *provenanceService) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: backendplugin.ProtocolMajorV1,
		PluginID:      "test-prov",
		Version:       "0.1.0",
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:                     "fake-prov",
			CredentialMode:           backendplugin.CredentialModeNone,
			AccessScope:              backendplugin.AccessScopeLocalOnly,
			ProcessSharing:           backendplugin.ProcessSharingPerInstance,
			SupportsDynamicInventory: s.dynamic,
		}},
	}, nil
}

func (s *provenanceService) Configure(context.Context, backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	return s.inst, nil
}

func TestCaseInventoryProvenance(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		dynamic bool
		source  string
		fetched int64
		want    bool
	}{
		{name: "dynamic with provenance passes", dynamic: true, source: "fake", fetched: 1, want: true},
		{name: "dynamic without source fails", dynamic: true, source: "", fetched: 1, want: false},
		{name: "dynamic without timestamp fails", dynamic: true, source: "fake", fetched: 0, want: false},
		{name: "static skips without provenance", dynamic: false, source: "", fetched: 0, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := &provenanceService{
				dynamic: tc.dynamic,
				inst:    &provenanceInstance{source: tc.source, fetched: tc.fetched},
			}
			got := caseInventoryProvenance(context.Background(), svc, Options{})
			if got.Passed != tc.want {
				t.Fatalf("Passed = %v, want %v (detail: %s)", got.Passed, tc.want, got.Detail)
			}
		})
	}
}
