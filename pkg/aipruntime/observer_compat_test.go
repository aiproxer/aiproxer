package aipruntime_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

type noopTraffic struct{}

func (noopTraffic) OnObservation(context.Context, traffic.Observation) error { return nil }

type noopUsage struct{}

func (noopUsage) OnUsage(context.Context, usage.Event) error { return nil }

func TestBuild_PreservesFeatureObservers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath:       repoConfigPath(t),
		TrafficObservers: []traffic.Observer{noopTraffic{}},
		UsageObservers:   []usage.Observer{noopUsage{}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if !rt.Ready() {
		t.Fatal("expected ready runtime")
	}
}

func TestBuild_AuthorityDescriptorAllowed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath: repoConfigPath(t),
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "enterprise-authority",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: allowRequestProvider{},
		}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if !rt.Ready() {
		t.Fatal("expected ready")
	}
}
