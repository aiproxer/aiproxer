package snapshotgen_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/runtimegen"
)

func TestPhase55_MetadataPublishIsCompatibilityNotEnforcement(t *testing.T) {
	t.Parallel()
	pub := snapshotgen.NewPublisher()
	meta := pub.Publish(snapshotgen.RuntimeGeneration{
		State: economics.SnapshotReady,
		Usage: economics.Snapshot[economics.PolicyRulesView]{
			ID: "usage_authority", Version: "meta-label-v1", State: economics.SnapshotReady,
			Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
		},
	})
	if meta == nil || meta.Usage.Version != "meta-label-v1" {
		t.Fatalf("compatibility metadata view missing: %#v", meta)
	}
	if pub.CurrentExecutable() != nil {
		t.Fatal("metadata Publish must not install executable enforcement generation")
	}
	contrib := runtimegen.GenerationContribution{
		SourceID: "static-config",
		Version:  "exec-v1",
		State:    economics.SnapshotReady,
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: stubRequestProvider{id: "req-a"}.Describe(),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   stubRequestProvider{id: "req-a"},
		}},
	}
	exec, err := snapshotgen.CompileExecutable(contrib)
	if err != nil {
		t.Fatal(err)
	}
	published, err := pub.PublishExecutable(exec)
	if err != nil {
		t.Fatal(err)
	}
	if published.EvidenceObjectID() != "req-a" {
		t.Fatalf("evidence=%q want request authority object", published.EvidenceObjectID())
	}
	if published.EvidenceObjectID() == meta.Usage.Version {
		t.Fatal("evidence must not be metadata-only usage version label")
	}
	if pub.Current() == nil || pub.Current().Usage.Version != "meta-label-v1" {
		t.Fatal("additive metadata compatibility view must remain after executable publish")
	}
}
