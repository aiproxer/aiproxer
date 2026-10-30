package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/runtimegen"
)

type genBindConc struct{ id string }

func (s genBindConc) Describe() authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID: s.id, Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage: authority.StageLeaseAdmit, Strength: authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func (s genBindConc) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "L1"}, nil
}

func (s genBindConc) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "L1"}, nil
}

func (s genBindConc) ReleaseLease(context.Context, authority.LeaseRelease) error { return nil }

func (s genBindConc) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func TestApplyGenerationBoundVersion_PrefersPublishedGeneration(t *testing.T) {
	t.Parallel()
	pub := snapshotgen.NewPublisher()
	gen, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID: "static", Version: "gen-v9", State: economics.SnapshotReady,
		ConcurrencyRegistration: &authority.ConcurrencyRegistration{
			Descriptor: genBindConc{id: "conc"}.Describe(),
			Provider:   genBindConc{id: "conc"},
		},
		MaxActiveRequests: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pub.PublishExecutable(gen); err != nil {
		t.Fatal(err)
	}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{SnapshotGeneration: pub},
	}
	res := usagelimit.AdmissionResult{
		BoundVersion: economics.PolicySnapshotRef{
			VersionRef: economics.VersionRef{ID: "usage_authority", Version: "rules-old"},
			PolicyID:   "usage_authority",
		},
	}
	ex.applyGenerationBoundVersionFrom(nil, &res)
	if res.BoundVersion.Version != "gen-v9" {
		t.Fatalf("bound=%+v, want gen-v9 from SnapshotGeneration", res.BoundVersion)
	}
}

func TestApplyGenerationBoundVersion_DeprecatedPublishCompatibility(t *testing.T) {
	t.Parallel()
	pub := snapshotgen.NewPublisher()
	//nolint:staticcheck // SA1019: metadata Publish remains the compatibility path under test
	pub.Publish(snapshotgen.RuntimeGeneration{
		State: economics.SnapshotReady,
		Usage: economics.Snapshot[economics.PolicyRulesView]{
			ID: "usage_authority", Version: "gen-v9", State: economics.SnapshotReady,
			EffectiveAt: time.Unix(1, 0).UTC(),
			Value:       economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
		},
	})
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{SnapshotGeneration: pub},
	}
	res := usagelimit.AdmissionResult{
		BoundVersion: economics.PolicySnapshotRef{
			VersionRef: economics.VersionRef{ID: "usage_authority", Version: "rules-old"},
			PolicyID:   "usage_authority",
		},
	}
	ex.applyGenerationBoundVersionFrom(nil, &res)
	if res.BoundVersion.Version != "gen-v9" {
		t.Fatalf("bound=%+v, want gen-v9 from SnapshotGeneration", res.BoundVersion)
	}
}

func TestApplyGenerationBoundVersion_MetadataOnlyPublishMetadataFallback(t *testing.T) {
	t.Parallel()
	pub := snapshotgen.NewPublisher()
	pub.PublishMetadata(snapshotgen.RuntimeGeneration{
		State: economics.SnapshotReady,
		Usage: economics.Snapshot[economics.PolicyRulesView]{
			ID: "usage_authority", Version: "meta-v3", State: economics.SnapshotReady,
			Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
		},
		Rating: economics.Snapshot[economics.RatingCatalogView]{
			ID: "rating", Version: "rate-v3", State: economics.SnapshotReady,
		},
	})
	if pub.CurrentExecutable() != nil {
		t.Fatal("metadata publish must not create an executable generation")
	}
	ex := &Executor{AccountingRuntime: AccountingRuntime{SnapshotGeneration: pub}}
	res := usagelimit.AdmissionResult{
		BoundVersion: economics.PolicySnapshotRef{
			VersionRef: economics.VersionRef{ID: "usage_authority", Version: "rules-old"},
			PolicyID:   "usage_authority",
		},
	}
	ex.applyGenerationBoundVersionFrom(nil, &res)
	if res.BoundVersion.Version != "meta-v3" {
		t.Fatalf("bound=%+v, want meta-v3 from PublishMetadata Current()", res.BoundVersion)
	}
	if res.BoundRatingVersion.Version != "" {
		t.Fatalf("bound rating=%+v, want empty after monetary rater retirement", res.BoundRatingVersion)
	}
}
