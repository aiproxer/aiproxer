package policydecision

import (
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// AllowedDecision describes one legal (stage, outcome, effect) combination that core
// may emit or accept for a stage (requirements 1.5, 3.6, 4.4, 6.6). The table is
// intentionally stricter than feature.StageMutationRole: the feature role says what
// the stage family may do, while this table says which shared policy-decision records
// core may emit or accept for that stage.
type AllowedDecision struct {
	Stage   string
	Outcome Outcome
	Effects []Effect
}

// legalDecisions is the canonical legality table. Order within a stage matches the
// design document. Effect slices are read-only after package init.
var legalDecisions = []AllowedDecision{
	{Stage: feature.StageIDTransportAuth, Outcome: OutcomeAllow, Effects: []Effect{EffectNone}},
	{Stage: feature.StageIDTransportAuth, Outcome: OutcomeDeny, Effects: []Effect{EffectNone}},
	{Stage: feature.StageIDTransportAuth, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDSessionOpen, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectAnnotate, EffectMutate}},
	{Stage: feature.StageIDSessionOpen, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDSecretGuard, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectAnnotate, EffectMutate}},
	{Stage: feature.StageIDSecretGuard, Outcome: OutcomeDeny, Effects: []Effect{EffectNone}},
	{Stage: feature.StageIDSecretGuard, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDSessionClassification, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectAnnotate}},
	{Stage: feature.StageIDSessionClassification, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDSubmit, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectAnnotate, EffectMutate}},
	{Stage: feature.StageIDSubmit, Outcome: OutcomeDeny, Effects: []Effect{EffectNone}},
	{Stage: feature.StageIDSubmit, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDToolCatalog, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectMutate}},
	{Stage: feature.StageIDToolCatalog, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDRequestWide, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectMutate}},
	{Stage: feature.StageIDRequestWide, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDPreRequest, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectAnnotate}},
	{Stage: feature.StageIDPreRequest, Outcome: OutcomeDeny, Effects: []Effect{EffectNone}},
	{Stage: feature.StageIDPreRequest, Outcome: OutcomeSkip, Effects: []Effect{EffectNone}},
	{Stage: feature.StageIDPreRequest, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDRouteHinting, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectAnnotate}},
	{Stage: feature.StageIDRouteHinting, Outcome: OutcomeSkip, Effects: []Effect{EffectNone}},
	{Stage: feature.StageIDRouteHinting, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDCandidateAttemptTransform, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectAnnotate, EffectMutate}},
	{Stage: feature.StageIDCandidateAttemptTransform, Outcome: OutcomeSkip, Effects: []Effect{EffectNone}},
	{Stage: feature.StageIDCandidateAttemptTransform, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDAttemptLifecycle, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectAnnotate}},
	{Stage: feature.StageIDAttemptLifecycle, Outcome: OutcomeSkip, Effects: []Effect{EffectNone}},
	{Stage: feature.StageIDAttemptLifecycle, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDStreamEventMutation, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectMutate}},
	{Stage: feature.StageIDStreamEventMutation, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDToolEventReaction, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectMutate, EffectReplace}},
	{Stage: feature.StageIDToolEventReaction, Outcome: OutcomeDeny, Effects: []Effect{EffectNone}},
	{Stage: feature.StageIDToolEventReaction, Outcome: OutcomeSkip, Effects: []Effect{EffectSwallow}},
	{Stage: feature.StageIDToolEventReaction, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDCompletionGating, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectReplace, EffectReplay}},
	{Stage: feature.StageIDCompletionGating, Outcome: OutcomeDeny, Effects: []Effect{EffectNone}},
	{Stage: feature.StageIDCompletionGating, Outcome: OutcomeSkip, Effects: []Effect{EffectNone}},
	{Stage: feature.StageIDCompletionGating, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDFinalStreamObservation, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectAnnotate}},
	{Stage: feature.StageIDFinalStreamObservation, Outcome: OutcomeSkip, Effects: []Effect{EffectNone}},
	{Stage: feature.StageIDFinalStreamObservation, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDTrafficObservation, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectAnnotate}},
	{Stage: feature.StageIDTrafficObservation, Outcome: OutcomeSkip, Effects: []Effect{EffectNone}},
	{Stage: feature.StageIDTrafficObservation, Outcome: OutcomeError, Effects: []Effect{EffectNone}},

	{Stage: feature.StageIDEgressEncoding, Outcome: OutcomeAllow, Effects: []Effect{EffectNone, EffectMutate}},
	{Stage: feature.StageIDEgressEncoding, Outcome: OutcomeError, Effects: []Effect{EffectNone}},
}

// AllowedDecisionsForStage returns the legal decision descriptors for stage in
// canonical order, or nil if the stage is unknown. The returned slice and its effect
// sub-slices are defensive copies; callers may mutate them without affecting the
// package table (requirements 1.5, 3.6).
func AllowedDecisionsForStage(stage string) []AllowedDecision {
	var out []AllowedDecision
	for _, a := range legalDecisions {
		if a.Stage != stage {
			continue
		}
		out = append(out, AllowedDecision{
			Stage:   a.Stage,
			Outcome: a.Outcome,
			Effects: slices.Clone(a.Effects),
		})
	}
	return out
}

// LegalDecisionStages returns the unique legal stage IDs that have at least one
// allowed decision, in canonical pipeline order. It is the set of stages for which
// policy decision records may be emitted or accepted.
func LegalDecisionStages() []string {
	seen := make(map[string]struct{}, len(legalDecisions))
	var out []string
	for _, id := range feature.LegalPipelineStageIDs() {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if hasAllowedDecision(id) {
			out = append(out, id)
		}
	}
	return out
}

func hasAllowedDecision(stage string) bool {
	for _, a := range legalDecisions {
		if a.Stage == stage {
			return true
		}
	}
	return false
}

// IsLegalPair reports whether (stage, outcome, effect) is one of the allowed
// combinations in the legality table. Unknown stages, OutcomeUnknown, unknown
// effects, or pairs not listed return false.
func IsLegalPair(stage string, outcome Outcome, effect Effect) bool {
	for _, a := range legalDecisions {
		if a.Stage != stage || a.Outcome != outcome {
			continue
		}
		if !effect.IsKnown() {
			return false
		}
		return slices.Contains(a.Effects, effect)
	}
	return false
}
