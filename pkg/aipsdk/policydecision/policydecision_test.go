package policydecision_test

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestOutcomeKnown(t *testing.T) {
	t.Parallel()
	if policydecision.OutcomeUnknown.IsKnown() {
		t.Fatalf("OutcomeUnknown must not be known")
	}
	for _, o := range []policydecision.Outcome{
		policydecision.OutcomeAllow,
		policydecision.OutcomeDeny,
		policydecision.OutcomeSkip,
		policydecision.OutcomeError,
	} {
		if !o.IsKnown() {
			t.Fatalf("outcome %q must be known", o)
		}
	}
}

func TestEffectKnown(t *testing.T) {
	t.Parallel()
	for _, e := range []policydecision.Effect{
		policydecision.EffectNone,
		policydecision.EffectAnnotate,
		policydecision.EffectMutate,
		policydecision.EffectReplace,
		policydecision.EffectReplay,
		policydecision.EffectSwallow,
	} {
		if !e.IsKnown() {
			t.Fatalf("effect %q must be known", e)
		}
	}
	var unknown policydecision.Effect = "bogus"
	if unknown.IsKnown() {
		t.Fatalf("unknown effect must not be known")
	}
}

func TestPolicyDecisionRecordCloneIsolatesState(t *testing.T) {
	t.Parallel()
	src := policydecision.Record{
		Stage:    feature.StageIDPreRequest,
		Outcome:  policydecision.OutcomeDeny,
		Effect:   policydecision.EffectNone,
		Provider: policydecision.ProviderRef{ID: "p1", Stage: feature.StageIDPreRequest},
		Annotations: map[string]string{
			"k": "v",
		},
		Scope: scope.PrincipalScopeView{
			PrincipalID:  scope.Known("principal-1"),
			SafeClaims:   map[string]string{"team": "platform"},
			PolicyLabels: map[string]string{"tier": "gold"},
			Roles:        []string{"admin"},
		},
	}
	clone := src.Clone()
	clone.Annotations["k"] = "mutated"
	clone.Scope.SafeClaims["team"] = "tampered"
	clone.Scope.PolicyLabels["tier"] = "bronze"
	clone.Scope.PrincipalID = scope.Known("attacker")

	if src.Annotations["k"] != "v" {
		t.Fatalf("source annotations mutated through clone: %q", src.Annotations["k"])
	}
	if src.Scope.SafeClaims["team"] != "platform" {
		t.Fatalf("source scope claims mutated through clone: %q", src.Scope.SafeClaims["team"])
	}
	if src.Scope.PolicyLabels["tier"] != "gold" {
		t.Fatalf("source scope labels mutated through clone: %q", src.Scope.PolicyLabels["tier"])
	}
	if src.Scope.PrincipalID.String() != "principal-1" {
		t.Fatalf("source scope principal mutated through clone: %q", src.Scope.PrincipalID.String())
	}
}

func TestPolicyDecisionRecordClonePreservesNilMaps(t *testing.T) {
	t.Parallel()
	r := policydecision.Record{Stage: feature.StageIDPreRequest, Outcome: policydecision.OutcomeAllow, Effect: policydecision.EffectNone}
	clone := r.Clone()
	if clone.Annotations != nil {
		t.Fatalf("nil annotations must be preserved, got %#v", clone.Annotations)
	}
}

func TestPolicyDecisionRecordZeroValuesInvalid(t *testing.T) {
	t.Parallel()
	r := policydecision.Record{}
	if r.Outcome.IsKnown() {
		t.Fatalf("zero outcome must be unknown")
	}
	if r.Stage != "" {
		t.Fatalf("zero stage must be empty")
	}
	if policydecision.IsLegalStageID(r.Stage) {
		t.Fatalf("empty stage must not be legal")
	}
}

func TestPolicyDecisionContextCloneIsolatesState(t *testing.T) {
	t.Parallel()
	src := policydecision.Context{
		Stage:      feature.StageIDPreRequest,
		ProviderID: "p1",
		Scope: scope.PrincipalScopeView{
			PrincipalID:  scope.Known("p"),
			SafeClaims:   map[string]string{"a": "b"},
			PolicyLabels: map[string]string{"x": "y"},
			Roles:        []string{"r"},
		},
		Principal: execview.PrincipalView{
			ID:          "principal-1",
			DisplayName: "Principal One",
			Roles:       []string{"admin"},
			Claims:      map[string]string{"team": "platform"},
		},
		Session: session.SessionView{
			AuthoritativeSessionID: "sess-1",
			Labels:                 map[string]string{"env": "prod"},
		},
		Workspace: workspace.WorkspaceView{
			ID:      "ws-1",
			Markers: []string{"pinned"},
			Labels:  map[string]string{"tier": "gold"},
		},
		Annotations: map[string]string{"k": "v"},
	}
	clone := src.Clone()
	clone.Annotations["k"] = "mutated"
	clone.Scope.SafeClaims["a"] = "tampered"
	clone.Scope.PolicyLabels["x"] = "tampered"
	clone.Scope.Roles[0] = "evil"
	clone.Principal.Roles[0] = "attacker"
	clone.Principal.Claims["team"] = "tampered"
	clone.Session.Labels["env"] = "tampered"
	clone.Workspace.Markers[0] = "tampered"
	clone.Workspace.Labels["tier"] = "tampered"

	if src.Annotations["k"] != "v" {
		t.Fatalf("source annotations mutated through context clone: %q", src.Annotations["k"])
	}
	if src.Scope.SafeClaims["a"] != "b" {
		t.Fatalf("source scope claims mutated through context clone: %q", src.Scope.SafeClaims["a"])
	}
	if src.Scope.PolicyLabels["x"] != "y" {
		t.Fatalf("source scope labels mutated through context clone: %q", src.Scope.PolicyLabels["x"])
	}
	if src.Scope.Roles[0] != "r" {
		t.Fatalf("source scope roles mutated through context clone: %q", src.Scope.Roles[0])
	}
	if src.Principal.ID != "principal-1" || src.Principal.DisplayName != "Principal One" {
		t.Fatalf("source principal scalar fields lost: %#v", src.Principal)
	}
	if src.Principal.Roles[0] != "admin" {
		t.Fatalf("source principal roles mutated through context clone: %q", src.Principal.Roles[0])
	}
	if src.Principal.Claims["team"] != "platform" {
		t.Fatalf("source principal claims mutated through context clone: %q", src.Principal.Claims["team"])
	}
	if src.Session.Labels["env"] != "prod" {
		t.Fatalf("source session labels mutated through context clone: %q", src.Session.Labels["env"])
	}
	if src.Workspace.Markers[0] != "pinned" {
		t.Fatalf("source workspace markers mutated through context clone: %q", src.Workspace.Markers[0])
	}
	if src.Workspace.Labels["tier"] != "gold" {
		t.Fatalf("source workspace labels mutated through context clone: %q", src.Workspace.Labels["tier"])
	}
}

func TestPolicyDecisionLegalStageIDsMatchPipeline(t *testing.T) {
	t.Parallel()
	got := policydecision.LegalStageIDs()
	want := feature.LegalPipelineStageIDs()
	if !slices.Equal(got, want) {
		t.Fatalf("LegalStageIDs mismatch\ngot  %#v\nwant %#v", got, want)
	}
}

func TestPolicyDecisionLegalAllowedDecisionsReadOnly(t *testing.T) {
	t.Parallel()
	allowed := policydecision.AllowedDecisionsForStage(feature.StageIDPreRequest)
	if len(allowed) == 0 {
		t.Fatalf("pre-request must have allowed decisions")
	}
	// Mutating the returned slice must not affect the package table.
	allowed[0].Outcome = policydecision.OutcomeError
	again := policydecision.AllowedDecisionsForStage(feature.StageIDPreRequest)
	if again[0].Outcome == policydecision.OutcomeError {
		t.Fatalf("mutating returned AllowedDecision slice affected the package table")
	}
}

func TestPolicyDecisionLegalAllowedForUnknownStageIsNil(t *testing.T) {
	t.Parallel()
	if got := policydecision.AllowedDecisionsForStage("no_such_stage"); got != nil {
		t.Fatalf("unknown stage must return nil, got %#v", got)
	}
}

func TestPolicyDecisionLegalPairs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		stage   string
		outcome policydecision.Outcome
		effect  policydecision.Effect
		legal   bool
	}{
		{feature.StageIDPreRequest, policydecision.OutcomeAllow, policydecision.EffectNone, true},
		{feature.StageIDPreRequest, policydecision.OutcomeAllow, policydecision.EffectAnnotate, true},
		{feature.StageIDPreRequest, policydecision.OutcomeDeny, policydecision.EffectNone, true},
		{feature.StageIDPreRequest, policydecision.OutcomeDeny, policydecision.EffectMutate, false},
		{feature.StageIDPreRequest, policydecision.OutcomeSkip, policydecision.EffectNone, true},
		{feature.StageIDPreRequest, policydecision.OutcomeSkip, policydecision.EffectSwallow, false},
		{feature.StageIDCompletionGating, policydecision.OutcomeAllow, policydecision.EffectReplay, true},
		{feature.StageIDPreRequest, policydecision.OutcomeAllow, policydecision.EffectReplay, false},
		{feature.StageIDToolEventReaction, policydecision.OutcomeSkip, policydecision.EffectSwallow, true},
		{feature.StageIDToolEventReaction, policydecision.OutcomeAllow, policydecision.EffectReplace, true},
		{feature.StageIDToolEventReaction, policydecision.OutcomeSkip, policydecision.EffectNone, false},
		{feature.StageIDCandidateAttemptTransform, policydecision.OutcomeAllow, policydecision.EffectMutate, true},
		{feature.StageIDCandidateAttemptTransform, policydecision.OutcomeSkip, policydecision.EffectNone, true},
		{feature.StageIDCandidateAttemptTransform, policydecision.OutcomeDeny, policydecision.EffectNone, false},
		{feature.StageIDCandidateAttemptTransform, policydecision.OutcomeAllow, policydecision.EffectReplace, false},
		{feature.StageIDCandidateAttemptTransform, policydecision.OutcomeSkip, policydecision.EffectSwallow, false},
		{feature.StageIDFinalStreamObservation, policydecision.OutcomeAllow, policydecision.EffectAnnotate, true},
		{feature.StageIDFinalStreamObservation, policydecision.OutcomeSkip, policydecision.EffectNone, true},
		{feature.StageIDFinalStreamObservation, policydecision.OutcomeAllow, policydecision.EffectMutate, false},
		{feature.StageIDFinalStreamObservation, policydecision.OutcomeAllow, policydecision.EffectReplace, false},
		{feature.StageIDFinalStreamObservation, policydecision.OutcomeAllow, policydecision.EffectReplay, false},
		{feature.StageIDFinalStreamObservation, policydecision.OutcomeDeny, policydecision.EffectNone, false},
		{"unknown_stage", policydecision.OutcomeAllow, policydecision.EffectNone, false},
	}
	for _, c := range cases {
		if got := policydecision.IsLegalPair(c.stage, c.outcome, c.effect); got != c.legal {
			t.Fatalf("IsLegalPair(%q,%q,%q) = %v, want %v", c.stage, c.outcome, c.effect, got, c.legal)
		}
	}
}

func TestPolicyDecisionLegalStagesCoverEveryFeatureStage(t *testing.T) {
	t.Parallel()
	stages := policydecision.LegalDecisionStages()
	want := feature.LegalPipelineStageIDs()
	if !slices.Equal(stages, want) {
		t.Fatalf("LegalDecisionStages must cover every legal stage\ngot  %#v\nwant %#v", stages, want)
	}
}

func TestPolicyDecisionRecordJSONRoundTrip(t *testing.T) {
	t.Parallel()
	r := policydecision.Record{
		TraceID:         "trace-1",
		ALegID:          "a-1",
		BLegID:          "b-1",
		AttemptSeq:      3,
		Stage:           feature.StageIDPreRequest,
		Provider:        policydecision.ProviderRef{ID: "p1", Stage: feature.StageIDPreRequest},
		Outcome:         policydecision.OutcomeDeny,
		Effect:          policydecision.EffectNone,
		ReasonCode:      "policy_denied",
		ClientCategory:  "policy_denied",
		ClientMessage:   "no",
		FailureBehavior: policydecision.FailureBehaviorFailClosed,
		Visibility:      policydecision.EvidenceDefault,
		Annotations:     map[string]string{"k": "v"},
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got policydecision.Record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.TraceID != r.TraceID || got.Outcome != r.Outcome || got.Effect != r.Effect ||
		got.Provider.ID != r.Provider.ID || got.AttemptSeq != r.AttemptSeq {
		t.Fatalf("json round trip lost fields: %#v", got)
	}
	if !maps.Equal(got.Annotations, r.Annotations) {
		t.Fatalf("json annotations mismatch: %#v", got.Annotations)
	}
}
