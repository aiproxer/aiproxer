package runtimebundle_test

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/reasoninghost"
)

type testHostPolicy struct {
	action reasoninghost.EgressAction
}

func (p testHostPolicy) Decide(_ context.Context, _ reasoninghost.EgressInput) (reasoninghost.EgressDecision, error) {
	return reasoninghost.EgressDecision{
		Action:        p.action,
		PolicyVersion: "v1",
	}, nil
}

func TestBuildHost_WithFeatureHostRegistrations_ReasoningBinding(t *testing.T) {
	t.Parallel()

	cfgPath := runtimebundle.MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))

	binding := &reasoninghost.Binding{
		EgressPolicies: map[string]reasoninghost.EgressPolicy{
			"test-egress": testHostPolicy{action: reasoninghost.EgressAllow},
		},
	}

	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath: cfgPath,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
		Production: runtimebundle.ProductionOptions{
			FeatureHostRegistrations: []featurehost.Registration{
				binding.Registration(),
			},
		},
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost with FeatureHostRegistrations failed: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })
}
