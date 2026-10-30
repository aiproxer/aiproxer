package feature_test

import (
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

func TestLegalPipelineStageIDs_countAndOrder(t *testing.T) {
	t.Parallel()
	ids := feature.LegalPipelineStageIDs()
	if len(ids) != 17 {
		t.Fatalf("want 17 stages, got %d", len(ids))
	}
	seen := map[string]struct{}{}
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate stage id %q", id)
		}
		seen[id] = struct{}{}
		if !feature.ValidateStageID(id) {
			t.Fatalf("ValidateStageID(%q) false", id)
		}
	}
	desc := feature.LegalStageDescriptors()
	if len(desc) != 17 {
		t.Fatalf("descriptors: want 17 got %d", len(desc))
	}
	gotDescIDs := make([]string, len(desc))
	for i := range desc {
		gotDescIDs[i] = desc[i].ID
	}
	if !slices.Equal(ids, gotDescIDs) {
		t.Fatalf("descriptor order != pipeline ids\ngot  %#v\nwant %#v", gotDescIDs, ids)
	}
}

func TestLegalPipeline_sessionClassificationAfterSecretGuardBeforeSubmit(t *testing.T) {
	t.Parallel()

	guardIdx := feature.LegalStageDescriptorIndex(feature.StageIDSecretGuard)
	classificationIdx := feature.LegalStageDescriptorIndex(feature.StageIDSessionClassification)
	submitIdx := feature.LegalStageDescriptorIndex(feature.StageIDSubmit)
	if guardIdx < 0 || classificationIdx < 0 || submitIdx < 0 {
		t.Fatalf("missing stages guard=%d classification=%d submit=%d", guardIdx, classificationIdx, submitIdx)
	}
	if guardIdx >= classificationIdx || classificationIdx >= submitIdx {
		t.Fatalf("want secret_guard(%d) < session_classification(%d) < submit_request(%d)", guardIdx, classificationIdx, submitIdx)
	}
	desc, ok := feature.StageDescriptorByID(feature.StageIDSessionClassification)
	if !ok || desc.MutationRole != feature.StageRoleObserve {
		t.Fatalf("session_classification descriptor: ok=%v role=%v", ok, desc.MutationRole)
	}
}

func TestStageDescriptorByID_unknown(t *testing.T) {
	t.Parallel()
	_, ok := feature.StageDescriptorByID("not_a_real_stage")
	if ok {
		t.Fatal("expected false for unknown stage")
	}
}

func TestLegalPipelineStageIDs_secretGuardBetweenSessionOpenAndSubmit(t *testing.T) {
	t.Parallel()
	openIdx := feature.LegalStageDescriptorIndex(feature.StageIDSessionOpen)
	guardIdx := feature.LegalStageDescriptorIndex(feature.StageIDSecretGuard)
	submitIdx := feature.LegalStageDescriptorIndex(feature.StageIDSubmit)
	if openIdx >= guardIdx || guardIdx >= submitIdx {
		t.Fatalf("want session_open(%d) < secret_guard(%d) < submit_request(%d)", openIdx, guardIdx, submitIdx)
	}
	desc, ok := feature.StageDescriptorByID(feature.StageIDSecretGuard)
	if !ok || desc.MutationRole != feature.StageRoleMutateReject {
		t.Fatalf("secret_guard descriptor: ok=%v role=%v", ok, desc.MutationRole)
	}
}

func TestStageMutationRole_zeroIsUnknown(t *testing.T) {
	t.Parallel()
	var z feature.StageDescriptor
	if z.MutationRole != feature.StageRoleUnknown {
		t.Fatalf("zero descriptor mutation role: %v", z.MutationRole)
	}
}

func TestLegalPipeline_candidateAttemptTransformBetweenRouteHintAndAttemptLifecycle(t *testing.T) {
	t.Parallel()
	routeIdx := feature.LegalStageDescriptorIndex(feature.StageIDRouteHinting)
	xformIdx := feature.LegalStageDescriptorIndex(feature.StageIDCandidateAttemptTransform)
	lifeIdx := feature.LegalStageDescriptorIndex(feature.StageIDAttemptLifecycle)
	if routeIdx < 0 || xformIdx < 0 || lifeIdx < 0 {
		t.Fatalf("missing stages route=%d transform=%d lifecycle=%d", routeIdx, xformIdx, lifeIdx)
	}
	if routeIdx >= xformIdx || xformIdx >= lifeIdx {
		t.Fatalf("want route_hinting(%d) < candidate_attempt_transform(%d) < attempt_lifecycle(%d)", routeIdx, xformIdx, lifeIdx)
	}
	desc, ok := feature.StageDescriptorByID(feature.StageIDCandidateAttemptTransform)
	if !ok || desc.MutationRole != feature.StageRoleMutateReject {
		t.Fatalf("candidate_attempt_transform descriptor: ok=%v role=%v", ok, desc.MutationRole)
	}
	life, ok := feature.StageDescriptorByID(feature.StageIDAttemptLifecycle)
	if !ok || life.MutationRole != feature.StageRoleObserve {
		t.Fatalf("attempt_lifecycle must remain observe-only: ok=%v role=%v", ok, life.MutationRole)
	}
}

func TestLegalPipeline_finalStreamObservationBetweenCompletionGatingAndTraffic(t *testing.T) {
	t.Parallel()
	gateIdx := feature.LegalStageDescriptorIndex(feature.StageIDCompletionGating)
	obsIdx := feature.LegalStageDescriptorIndex(feature.StageIDFinalStreamObservation)
	trafficIdx := feature.LegalStageDescriptorIndex(feature.StageIDTrafficObservation)
	egressIdx := feature.LegalStageDescriptorIndex(feature.StageIDEgressEncoding)
	if gateIdx < 0 || obsIdx < 0 || trafficIdx < 0 || egressIdx < 0 {
		t.Fatalf("missing stages gate=%d obs=%d traffic=%d egress=%d", gateIdx, obsIdx, trafficIdx, egressIdx)
	}
	if gateIdx >= obsIdx || obsIdx >= trafficIdx || trafficIdx >= egressIdx {
		t.Fatalf("want completion_gating(%d) < final_stream_observation(%d) < traffic_observation(%d) < egress(%d)",
			gateIdx, obsIdx, trafficIdx, egressIdx)
	}
	desc, ok := feature.StageDescriptorByID(feature.StageIDFinalStreamObservation)
	if !ok || desc.MutationRole != feature.StageRoleObserve {
		t.Fatalf("final_stream_observation descriptor: ok=%v role=%v", ok, desc.MutationRole)
	}
	life, ok := feature.StageDescriptorByID(feature.StageIDAttemptLifecycle)
	if !ok || life.MutationRole != feature.StageRoleObserve {
		t.Fatalf("attempt_lifecycle must remain observe-only: ok=%v role=%v", ok, life.MutationRole)
	}
}
