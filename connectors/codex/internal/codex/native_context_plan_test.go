package codex

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
)

func TestResolveCompactionModelProfile_TriggerPrecedenceAndBounds(t *testing.T) {
	t.Parallel()
	cat, err := catalog.Parse([]byte(`{"models":[{"slug":"m","default_reasoning_level":"high","supported_reasoning_levels":[{"effort":"high"}],"context_window":10000,"max_context_window":12000,"auto_compact_token_limit":700,"comp_hash":"hash"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	base, err := ResolveCompactionModelProfile(cat, "m", NativeCompactionConfig{RetainedMessageTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if base.TriggerTokens != 700 || base.HardLimit != 12000 || base.DefaultReasoning != "high" || base.CompHash != "hash" {
		t.Fatalf("profile = %+v", base)
	}
	override, err := ResolveCompactionModelProfile(cat, "m", NativeCompactionConfig{TriggerTokens: 900, RetainedMessageTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if override.TriggerTokens != 900 {
		t.Fatalf("explicit trigger = %d, want 900", override.TriggerTokens)
	}
	if _, err := ResolveCompactionModelProfile(cat, "m", NativeCompactionConfig{TriggerTokens: 10000, RetainedMessageTokens: 100}); err == nil {
		t.Fatal("impossible explicit trigger was silently changed")
	}
	unknown, err := ResolveCompactionModelProfile(nil, "unknown", NativeCompactionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if unknown.TriggerTokens != defaultHardLimit/3 || unknown.TriggerTokens <= 0 || unknown.TriggerTokens >= unknown.HardLimit {
		t.Fatalf("conservative fallback profile = %+v", unknown)
	}
}

func TestResolveCompactionModelProfile_CodexHarnessHeadroomV1Fallbacks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, model, policy                            string
		wantHard, wantUsable, wantTrigger, wantReserve int64
	}{
		{"spark", "gpt-5.3-codex-spark", "CodexHarnessHeadroomV1", 128000, 96000, 80000, 32000},
		{"gpt5", "gpt-5.4-codex", "CodexHarnessHeadroomV1", 250000, 250000, 220000, 30000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveCompactionModelProfile(nil, tt.model, NativeCompactionConfig{RetainedMessageTokens: 1})
			if err != nil {
				t.Fatal(err)
			}
			if got.HardLimit != tt.wantHard || got.UsableContextCeiling != tt.wantUsable || got.TriggerTokens != tt.wantTrigger || got.SafetyHeadroom != tt.wantReserve || got.BudgetPolicyName != tt.policy {
				t.Fatalf("profile = %+v", got)
			}
		})
	}
}

func TestResolveCompactionModelProfile_CatalogHardLimitClampsFallback(t *testing.T) {
	t.Parallel()
	cat, err := catalog.Parse([]byte(`{"models":[{"slug":"gpt-5.4-codex","supported_reasoning_levels":[{"effort":"high"}],"max_context_window":200000,"auto_compact_token_limit":180000}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolveCompactionModelProfile(cat, "gpt-5.4-codex", NativeCompactionConfig{RetainedMessageTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.HardLimit != 200000 || got.UsableContextCeiling != 170000 || got.TriggerTokens != 169999 {
		t.Fatalf("catalog-clamped profile = %+v", got)
	}
	if _, err := ResolveCompactionModelProfile(cat, "gpt-5.4-codex", NativeCompactionConfig{TriggerTokens: 170000, RetainedMessageTokens: 1}); err == nil {
		t.Fatal("trigger at usable ceiling was accepted")
	}
}

func TestPlanCompaction_DefaultRetentionFitsApprovedBudgets(t *testing.T) {
	t.Parallel()
	config := NativeContextConfig{
		Enabled:             true,
		ReasoningContinuity: ContinuityBestEffort,
		Compaction:          NativeCompactionConfig{Enabled: true, RetainedMessageTokens: DefaultRetainedMessageTokens},
	}
	for _, model := range []string{"gpt-5.3-codex-spark", "gpt-5.4-codex"} {
		t.Run(model, func(t *testing.T) {
			t.Parallel()
			profile, err := ResolveCompactionModelProfile(nil, model, config.Compaction)
			if err != nil {
				t.Fatal(err)
			}
			history := NativeHistory{
				Items: []inputItem{
					textMessageItem{Type: "message", Role: "user", Content: "small first request"},
				},
				Fingerprints: []string{"first"},
				Boundaries:   []TrajectoryBoundary{{ItemIndex: 0, UserTurnStart: true, PairSafe: true}, {ItemIndex: 1, PairSafe: true}},
			}
			plan := PlanCompaction(CompactionPlanInput{
				Context: context.Background(), History: history, Profile: profile, Config: config, MarkerEligible: true,
			})
			if plan.Kind != DecisionBypass || plan.Reason != "below_trigger" {
				t.Fatalf("default-budget first request plan = %+v, want below_trigger bypass", plan)
			}
		})
	}
}

func TestPlanCompaction_UsesUsableCeilingForHardFailure(t *testing.T) {
	t.Parallel()
	profile := CompactionModelProfile{
		ModelSlug: "gpt-5.3-codex-spark", HardLimit: 128000, UsableContextCeiling: 96000,
		TriggerTokens: 80000, SafetyHeadroom: 32000,
	}
	config := NativeContextConfig{
		Enabled: true, ReasoningContinuity: ContinuityBestEffort,
		Compaction: NativeCompactionConfig{Enabled: true, RetainedMessageTokens: 64000, MinSavingsTokens: 1},
	}
	history := NativeHistory{
		Items: []inputItem{
			textMessageItem{Type: "message", Role: "user", Content: "old"},
			textMessageItem{Type: "message", Role: "user", Content: "live"},
		},
		Fingerprints: []string{"old", "live"},
		Boundaries: []TrajectoryBoundary{
			{ItemIndex: 0, PairSafe: true}, {ItemIndex: 1, UserTurnStart: true, PairSafe: true}, {ItemIndex: 2, PairSafe: true},
		},
	}
	plan := PlanCompaction(CompactionPlanInput{
		Context: context.Background(), History: history, Profile: profile, Config: config, MarkerEligible: true,
		Estimator: fixedHistoryEstimator{tokens: 97000},
	})
	if plan.Kind != DecisionHardFailure {
		t.Fatalf("history above usable ceiling plan = %+v, want hard failure", plan)
	}
}

type fixedHistoryEstimator struct{ tokens int64 }

func (e fixedHistoryEstimator) Estimate(context.Context, NativeHistory) (CompactionEstimate, error) {
	return CompactionEstimate{Tokens: e.tokens}, nil
}

func TestCompactionProfileReplayRequiresExactModelAndHashIsOnlyInvalidation(t *testing.T) {
	t.Parallel()
	want := CompactionModelProfile{ModelSlug: "m", CompHash: "new"}
	if !profilesCompatibleForReplay(want, CompactionModelProfile{ModelSlug: "m", CompHash: "new"}) {
		t.Fatal("model equality should use exact slug")
	}
	if profilesCompatibleForReplay(want, CompactionModelProfile{ModelSlug: "m", CompHash: "old"}) {
		t.Fatal("non-empty comp hash mismatch was accepted")
	}
	if profilesCompatibleForReplay(want, CompactionModelProfile{ModelSlug: "other", CompHash: "new"}) {
		t.Fatal("different model was accepted")
	}
	if !profilesCompatibleForReplay(CompactionModelProfile{ModelSlug: "m"}, CompactionModelProfile{ModelSlug: "m", CompHash: "old"}) {
		t.Fatal("missing comp hash should remain compatible")
	}
	if compHashCompatible(want, CompactionModelProfile{ModelSlug: "m", CompHash: "old"}) {
		t.Fatal("comp hash mismatch should be observable")
	}
}

func TestPlanCompaction_DecisionTables(t *testing.T) {
	t.Parallel()
	history := NativeHistory{
		Items: []inputItem{
			textMessageItem{Type: "message", Role: "user", Content: "old"},
			textMessageItem{Type: "message", Role: "assistant", Content: "answer"},
			textMessageItem{Type: "message", Role: "user", Content: "live"},
			textMessageItem{Type: "message", Role: "assistant", Content: "tail"},
		},
		Fingerprints: []string{"a", "b", "c", "d"},
		Boundaries: []TrajectoryBoundary{
			{ItemIndex: 0, PairSafe: true},
			{ItemIndex: 1, PairSafe: true},
			{ItemIndex: 2, UserTurnStart: true, PairSafe: true},
			{ItemIndex: 3, PairSafe: true},
			{ItemIndex: 4, PairSafe: true},
		},
	}
	profile := CompactionModelProfile{ModelSlug: "m", HardLimit: 50, TriggerTokens: 4}
	base := CompactionPlanInput{Context: context.Background(), History: history, Profile: profile, Config: NativeContextConfig{Enabled: true, ReasoningContinuity: ContinuityBestEffort, Compaction: NativeCompactionConfig{Enabled: true, MinSavingsTokens: 1}}}
	for _, test := range []struct {
		name   string
		input  CompactionPlanInput
		kind   CompactionDecisionKind
		reason string
	}{
		{"disabled", CompactionPlanInput{Config: NativeContextConfig{}}, DecisionBypass, "disabled"},
		{"marker", func() CompactionPlanInput { v := base; v.Config.ReasoningContinuity = ContinuityRequired; return v }(), DecisionBypass, "continuity_not_eligible"},
		{"create", base, DecisionCreate, "threshold_crossed"},
		{"in flight", func() CompactionPlanInput { v := base; v.InFlight = true; return v }(), DecisionBypass, "compaction_in_flight"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := PlanCompaction(test.input)
			if got.Kind != test.kind || got.Reason != test.reason {
				t.Fatalf("plan = %+v, want %s/%s", got, test.kind, test.reason)
			}
		})
	}
}

func TestPlanCompaction_LiveFixtureCompactsOnceThenReusesBelowTrigger(t *testing.T) {
	t.Parallel()
	const trigger int64 = 256
	history := NativeHistory{Items: []inputItem{
		textMessageItem{Type: "message", Role: "user", Content: strings.Repeat("older context ", 72)},
		textMessageItem{Type: "message", Role: "assistant", Content: "retained answer"},
		textMessageItem{Type: "message", Role: "user", Content: "live tail"},
	}}
	history.Fingerprints = make([]string, 0, len(history.Items))
	for _, item := range history.Items {
		fp, err := fingerprintNativeItem(item)
		if err != nil {
			t.Fatal(err)
		}
		history.Fingerprints = append(history.Fingerprints, fp)
	}
	history.Boundaries = nativeTrajectoryBoundaries(history.Items)
	config := NativeContextConfig{Enabled: true, ReasoningContinuity: ContinuityBestEffort, Compaction: NativeCompactionConfig{
		Enabled: true, TriggerTokens: trigger, RetainedMessageTokens: 1, MinSavingsTokens: 1,
	}}
	profile := CompactionModelProfile{ModelSlug: "gpt-5.3-codex-spark", HardLimit: 10000, TriggerTokens: trigger}
	first := PlanCompaction(CompactionPlanInput{Context: context.Background(), History: history, Profile: profile, MarkerEligible: true, Config: config})
	if first.Kind != DecisionCreate {
		t.Fatalf("live fixture first plan = %+v, want create", first)
	}
	checkpoint := &CheckpointView{
		Model: profile.ModelSlug, SourcePrefixFP: append([]string(nil), history.Fingerprints[:first.SourcePrefixEnd]...),
		Replacement: []inputItem{
			textMessageItem{Type: "message", Role: "user", Content: "retained"},
			textMessageItem{Type: "message", Role: "assistant", Content: "compact summary"},
		},
	}
	followUp := NativeHistory{Items: append(append([]inputItem(nil), history.Items...), textMessageItem{Type: "message", Role: "user", Content: "tiny follow-up"})}
	followUp.Fingerprints = make([]string, 0, len(followUp.Items))
	for _, item := range followUp.Items {
		fp, err := fingerprintNativeItem(item)
		if err != nil {
			t.Fatal(err)
		}
		followUp.Fingerprints = append(followUp.Fingerprints, fp)
	}
	followUp.Boundaries = nativeTrajectoryBoundaries(followUp.Items)
	second := PlanCompaction(CompactionPlanInput{Context: context.Background(), History: followUp, Profile: profile, MarkerEligible: true, Checkpoint: checkpoint, Config: config})
	if second.Kind != DecisionReuse || second.Reason != "checkpoint_reuse" {
		t.Fatalf("live fixture follow-up plan = %+v, want checkpoint reuse below trigger", second)
	}
	if second.EffectiveTokens >= trigger {
		t.Fatalf("live fixture effective checkpoint history estimate = %d, want < %d", second.EffectiveTokens, trigger)
	}
}

func TestPlanCompaction_UsesEffectiveCheckpointHistoryAndRejectsMismatch(t *testing.T) {
	t.Parallel()
	history := NativeHistory{Items: []inputItem{textMessageItem{Type: "message", Role: "user", Content: "old"}, textMessageItem{Type: "message", Role: "user", Content: "live"}}, Fingerprints: []string{"old", "live"}, Boundaries: []TrajectoryBoundary{{ItemIndex: 0, PairSafe: true}, {ItemIndex: 1, UserTurnStart: true, PairSafe: true}, {ItemIndex: 2, PairSafe: true}}}
	profile := CompactionModelProfile{ModelSlug: "m", CompHash: "h", HardLimit: 100, TriggerTokens: 50}
	checkpoint := &CheckpointView{Model: "m", CompHash: "h", SourcePrefixFP: []string{"old"}, Replacement: []inputItem{textMessageItem{Type: "message", Role: "assistant", Content: "compact"}}}
	in := CompactionPlanInput{Context: context.Background(), History: history, Profile: profile, MarkerEligible: true, Checkpoint: checkpoint, Config: NativeContextConfig{Enabled: true, ReasoningContinuity: ContinuityBestEffort, Compaction: NativeCompactionConfig{Enabled: true}}}
	got := PlanCompaction(in)
	if got.Kind != DecisionReuse || got.Reason != "checkpoint_reuse" {
		t.Fatalf("reuse plan = %+v", got)
	}
	checkpoint.SourcePrefixFP[0] = "edited"
	got = PlanCompaction(in)
	if got.Kind == DecisionReuse {
		t.Fatal("mismatched checkpoint was reused")
	}
}

type countingHistoryEstimator struct{}

func (countingHistoryEstimator) Estimate(_ context.Context, history NativeHistory) (CompactionEstimate, error) {
	return CompactionEstimate{Tokens: int64(len(history.Items) * 10)}, nil
}

type failingHistoryEstimator struct{}

func (failingHistoryEstimator) Estimate(context.Context, NativeHistory) (CompactionEstimate, error) {
	return CompactionEstimate{}, context.Canceled
}

type recordingHistoryEstimator struct {
	seenOpaque bool
}

func (e *recordingHistoryEstimator) Estimate(_ context.Context, history NativeHistory) (CompactionEstimate, error) {
	for _, item := range history.Items {
		if _, ok := item.(opaqueResponseItem); ok {
			e.seenOpaque = true
		}
	}
	return CompactionEstimate{Tokens: int64(len(history.Items))}, nil
}

func TestPlanCompaction_CheckpointOverCheckpointUsesEffectiveViewAndSourceMapping(t *testing.T) {
	t.Parallel()
	history := NativeHistory{
		Items: []inputItem{
			textMessageItem{Type: "message", Role: "user", Content: "old"},
			textMessageItem{Type: "message", Role: "user", Content: "latest"},
			textMessageItem{Type: "message", Role: "assistant", Content: "tail"},
		},
		Fingerprints: []string{"source-old", "live", "tail"},
		Boundaries: []TrajectoryBoundary{
			{ItemIndex: 0, PairSafe: true},
			{ItemIndex: 1, UserTurnStart: true, PairSafe: true},
			{ItemIndex: 2, PairSafe: true},
			{ItemIndex: 3, PairSafe: true},
		},
	}
	checkpoint := &CheckpointView{
		Model: "m", SourcePrefixFP: []string{"source-old"},
		Replacement: []inputItem{
			textMessageItem{Type: "message", Role: "developer", Content: "retained"},
			textMessageItem{Type: "message", Role: "assistant", Content: "summary"},
			textMessageItem{Type: "message", Role: "assistant", Content: "context"},
		},
	}
	in := CompactionPlanInput{
		Context: context.Background(), History: history, Profile: CompactionModelProfile{ModelSlug: "m", HardLimit: 100, TriggerTokens: 30},
		MarkerEligible: true, Checkpoint: checkpoint, Estimator: countingHistoryEstimator{},
		Config: NativeContextConfig{Enabled: true, ReasoningContinuity: ContinuityBestEffort, Compaction: NativeCompactionConfig{Enabled: true, MinSavingsTokens: 1}},
	}
	got := PlanCompaction(in)
	if got.Kind != DecisionCreate || got.PrefixEnd != 3 || got.SourcePrefixEnd != 1 {
		t.Fatalf("effective plan = %+v, want create at effective 3/source 1", got)
	}
	if len(got.EffectiveHistory.Items) != 5 {
		t.Fatalf("effective history length = %d, want 5", len(got.EffectiveHistory.Items))
	}
}

func TestPlanCompaction_LatestUserTailIncludesEveryLaterItem(t *testing.T) {
	t.Parallel()
	history := NativeHistory{
		Items: []inputItem{
			textMessageItem{Type: "message", Role: "user", Content: "old"},
			textMessageItem{Type: "message", Role: "assistant", Content: "answer"},
			textMessageItem{Type: "message", Role: "user", Content: "latest"},
			functionCallItem{Type: "function_call", CallID: "call-1", Name: "lookup", Arguments: "{}"},
			functionCallOutputItem{Type: "function_call_output", CallID: "call-1", Output: "result"},
			textMessageItem{Type: "message", Role: "developer", Content: "later instruction"},
		},
		Fingerprints: []string{"a", "b", "c", "d", "e", "f"},
		Boundaries: []TrajectoryBoundary{
			{ItemIndex: 0, PairSafe: true},
			{ItemIndex: 1, PairSafe: true},
			{ItemIndex: 2, UserTurnStart: true, PairSafe: true},
			{ItemIndex: 3, PairSafe: false},
			{ItemIndex: 4, PairSafe: false},
			{ItemIndex: 5, PairSafe: true},
			{ItemIndex: 6, PairSafe: true},
		},
	}
	in := CompactionPlanInput{Context: context.Background(), History: history, Profile: CompactionModelProfile{ModelSlug: "m", HardLimit: 100, TriggerTokens: 1}, MarkerEligible: true, Estimator: countingHistoryEstimator{}, Config: NativeContextConfig{Enabled: true, ReasoningContinuity: ContinuityBestEffort, Compaction: NativeCompactionConfig{Enabled: true, MinSavingsTokens: 1}}}
	got := PlanCompaction(in)
	if got.Kind != DecisionCreate || got.PrefixEnd != 2 || got.LiveTailStart != 2 {
		t.Fatalf("latest user split = %+v, want split before latest user", got)
	}
}

func TestPlanCompaction_InFlightPrecedesCheckpointReuse(t *testing.T) {
	t.Parallel()
	in := CompactionPlanInput{
		Context: context.Background(), History: NativeHistory{Items: []inputItem{textMessageItem{Type: "message", Role: "user", Content: "live"}}, Fingerprints: []string{"live"}},
		Profile: CompactionModelProfile{ModelSlug: "m", HardLimit: 100, TriggerTokens: 1}, MarkerEligible: true, InFlight: true,
		Checkpoint: &CheckpointView{Model: "m", SourcePrefixFP: []string{"live"}, Replacement: []inputItem{textMessageItem{Type: "message", Role: "assistant", Content: "replacement"}}},
		Config:     NativeContextConfig{Enabled: true, ReasoningContinuity: ContinuityBestEffort, Compaction: NativeCompactionConfig{Enabled: true}},
	}
	got := PlanCompaction(in)
	if got.Kind != DecisionBypass || got.Reason != "compaction_in_flight" {
		t.Fatalf("in-flight plan = %+v", got)
	}
}

func TestEstimateHistory_SeparatesOpaqueStateAndUsesMetadata(t *testing.T) {
	t.Parallel()
	history := NativeHistory{
		Items:                []inputItem{textMessageItem{Type: "message", Role: "user", Content: "ordinary"}, opaqueResponseItem{raw: []byte(`{"type":"reasoning","encrypted_content":"secret"}`)}},
		OpaqueMetadataTokens: []int64{0, 77},
	}
	spy := &recordingHistoryEstimator{}
	got, err := estimateHistory(context.Background(), spy, history)
	if err != nil {
		t.Fatal(err)
	}
	if spy.seenOpaque {
		t.Fatal("normal estimator received opaque item")
	}
	if got.MetadataTokens != 77 || got.OpaqueTokens != 77 || got.Tokens <= 77 {
		t.Fatalf("opaque estimate = %+v", got)
	}
}

func TestPlanCompaction_EstimatorFailureIsHardFailure(t *testing.T) {
	t.Parallel()
	in := CompactionPlanInput{
		Context: context.Background(), History: NativeHistory{Items: []inputItem{textMessageItem{Type: "message", Role: "user", Content: "history"}}},
		Profile: CompactionModelProfile{ModelSlug: "m", HardLimit: 100, TriggerTokens: 1}, MarkerEligible: true, Estimator: failingHistoryEstimator{},
		Config: NativeContextConfig{Enabled: true, ReasoningContinuity: ContinuityBestEffort, Compaction: NativeCompactionConfig{Enabled: true}},
	}
	got := PlanCompaction(in)
	if got.Kind != DecisionHardFailure || got.Reason != "estimate_cancelled" {
		t.Fatalf("estimator failure plan = %+v", got)
	}
}

func TestPlanCompaction_HardFailureAndOpaqueEstimatorBoundary(t *testing.T) {
	t.Parallel()
	history := NativeHistory{Items: []inputItem{textMessageItem{Type: "message", Role: "user", Content: "old"}, textMessageItem{Type: "message", Role: "user", Content: "live"}}, Fingerprints: []string{"a", "b"}, Boundaries: []TrajectoryBoundary{{ItemIndex: 0, PairSafe: true}, {ItemIndex: 1, UserTurnStart: true, PairSafe: true}, {ItemIndex: 2, PairSafe: true}}}
	profile := CompactionModelProfile{ModelSlug: "m", HardLimit: 4098, TriggerTokens: 1, SafetyHeadroom: 4096}
	in := CompactionPlanInput{Context: context.Background(), History: history, Profile: profile, MarkerEligible: true, Config: NativeContextConfig{Enabled: true, ReasoningContinuity: ContinuityBestEffort, Compaction: NativeCompactionConfig{Enabled: true}}}
	got := PlanCompaction(in)
	if got.Kind != DecisionHardFailure {
		t.Fatalf("hard plan = %+v", got)
	}
	if got.Reason != "live_tail_too_large" && got.Reason != "no_safe_split" {
		t.Fatalf("hard reason = %q", got.Reason)
	}
}
