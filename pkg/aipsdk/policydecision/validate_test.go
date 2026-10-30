package policydecision_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

func TestValidateRecordAcceptsLegal(t *testing.T) {
	t.Parallel()
	cases := []policydecision.Record{
		{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeAllow, Effect: policydecision.EffectNone},
		{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeDeny, Effect: policydecision.EffectNone},
		{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeSkip, Effect: policydecision.EffectNone},
		{Stage: feature.StageIDToolEventReaction, Outcome: policydecision.OutcomeSkip, Effect: policydecision.EffectSwallow},
		{Stage: feature.StageIDCompletionGating, Outcome: policydecision.OutcomeAllow, Effect: policydecision.EffectReplay},
	}
	for _, r := range cases {
		if err := policydecision.ValidateRecord(r); err != nil {
			t.Fatalf("legal record rejected: %#v -> %v", r, err)
		}
	}
}

func TestValidateRecordRejectsMalformed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		record policydecision.Record
		want   string
	}{
		{
			name:   "unknown_stage",
			record: policydecision.Record{Stage: "nope", Outcome: policydecision.OutcomeAllow, Effect: policydecision.EffectNone},
			want:   "unknown stage",
		},
		{
			name:   "outcome_unknown",
			record: policydecision.Record{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeUnknown, Effect: policydecision.EffectNone},
			want:   "unknown outcome",
		},
		{
			name:   "unknown_effect",
			record: policydecision.Record{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeAllow, Effect: policydecision.Effect("bogus")},
			want:   "unknown effect",
		},
		{
			name:   "deny_with_mutate",
			record: policydecision.Record{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeDeny, Effect: policydecision.EffectMutate},
			want:   "illegal pair",
		},
		{
			name:   "replay_at_pre_request",
			record: policydecision.Record{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeAllow, Effect: policydecision.EffectReplay},
			want:   "illegal pair",
		},
		{
			name:   "swallow_at_completion",
			record: policydecision.Record{Stage: feature.StageIDCompletionGating, Outcome: policydecision.OutcomeSkip, Effect: policydecision.EffectSwallow},
			want:   "illegal pair",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := policydecision.ValidateRecord(c.record)
			if err == nil {
				t.Fatalf("malformed record accepted: %#v", c.record)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q must contain %q", err.Error(), c.want)
			}
		})
	}
}

func TestValidateRecordEveryLegalStageHasAllowedPair(t *testing.T) {
	t.Parallel()
	for _, stage := range policydecision.LegalDecisionStages() {
		allowed := policydecision.AllowedDecisionsForStage(stage)
		if len(allowed) == 0 {
			t.Fatalf("stage %q has no allowed decisions", stage)
		}
		for _, a := range allowed {
			if a.Stage != stage {
				t.Fatalf("allowed decision returned for %q under %q", a.Stage, stage)
			}
			for _, effect := range a.Effects {
				if err := policydecision.ValidateRecord(policydecision.Record{
					Stage: a.Stage, Outcome: a.Outcome, Effect: effect,
				}); err != nil {
					t.Fatalf("stage %q allowed pair (%q,%q) rejected: %v", stage, a.Outcome, effect, err)
				}
			}
		}
	}
}

func TestClientCategoryConstantsAreStable(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		policydecision.CategoryAllowed:   "policy_allowed",
		policydecision.CategorySkipped:   "policy_skipped",
		policydecision.CategoryDenied:    "policy_denied",
		policydecision.CategoryFailure:   "policy_failure",
		policydecision.CategoryObserved:  "policy_observed",
		policydecision.CategoryMalformed: "policy_malformed",
	}
	for got, w := range want {
		if got != w {
			t.Fatalf("category constant drift: got %q want %q", got, w)
		}
	}
}
