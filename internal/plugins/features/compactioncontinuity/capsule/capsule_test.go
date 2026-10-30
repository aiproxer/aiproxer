package capsule

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func testCapsule(t *testing.T) Envelope {
	t.Helper()
	branch, err := NewBranchBinding("session-parent", "a-parent", "principal")
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(branch)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestSealCanonicalDigestAndParentBinding(t *testing.T) {
	t.Parallel()
	e := testCapsule(t)
	if err := e.Verify(); err != nil {
		t.Fatalf("sealed capsule does not verify: %v", err)
	}
	if !strings.HasPrefix(e.ContentDigest, "sha256:") {
		t.Fatalf("digest = %q", e.ContentDigest)
	}
	clone := e.Clone()
	clone.Plan.Steps = append(clone.Plan.Steps, PlanStep{ID: StableStepID("keep branch"), Text: "keep branch", Status: StepPending, SourceRef: "test"})
	if err := clone.Verify(); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("mutated capsule error = %v, want digest mismatch", err)
	}
	other, err := NewBranchBinding("session-child", "a-child", "principal")
	if err != nil {
		t.Fatal(err)
	}
	if other == e.BranchBinding {
		t.Fatal("child branch unexpectedly shared parent binding")
	}
	if err := e.VerifyBranch(other); !errors.Is(err, ErrBranchMismatch) {
		t.Fatalf("wrong branch error = %v", err)
	}
}

func TestMergeExplicitCorrectionSupersedesSemanticAndDedupes(t *testing.T) {
	t.Parallel()
	base := testCapsule(t)
	semantic := Delta{BaseRevision: 1, BranchBinding: base.BranchBinding, Plan: &base.Plan, Decisions: []Decision{{
		ID: StableID("decision", "billing", "automatic"), ConflictKey: "architecture.billing.mode", Statement: "Use automatic billing", Status: DecisionActive, Authority: AuthoritySemantic, SourceRef: "semantic:1",
	}}}
	first, err := Merge(base, semantic)
	if err != nil {
		t.Fatal(err)
	}
	oldID := first.Decisions[0].ID
	correction := Delta{BaseRevision: first.Revision, BranchBinding: first.BranchBinding, Decisions: []Decision{{
		ID: StableID("decision", "billing", "manual"), ConflictKey: "architecture.billing.mode", Supersedes: []string{oldID}, Statement: "Use manual billing", Status: DecisionActive, Authority: AuthorityUserExplicit, SourceRef: "user:2",
	}}}
	second, err := Merge(first, correction)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 3 {
		t.Fatalf("revision = %d", second.Revision)
	}
	active := 0
	for _, d := range second.Decisions {
		if d.Status == DecisionActive {
			active++
			if d.Statement != "Use manual billing" {
				t.Fatalf("active statement = %q", d.Statement)
			}
		}
	}
	if active != 1 {
		t.Fatalf("active decisions = %d", active)
	}
	// A retry with the same ID/content is a no-op revision merge, not a second fact.
	var activeDecision Decision
	for _, d := range second.Decisions {
		if d.Status == DecisionActive {
			activeDecision = d
		}
	}
	retry, err := Merge(second, Delta{BaseRevision: second.Revision, BranchBinding: second.BranchBinding, Decisions: []Decision{{
		ID: activeDecision.ID, ConflictKey: activeDecision.ConflictKey, Supersedes: activeDecision.Supersedes, Statement: activeDecision.Statement, Status: activeDecision.Status, Authority: activeDecision.Authority, Rationale: activeDecision.Rationale, SourceRef: activeDecision.SourceRef,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.Decisions) != len(second.Decisions) {
		t.Fatalf("dedupe changed decision count %d -> %d", len(second.Decisions), len(retry.Decisions))
	}
}

func TestMergeRejectsStaleBranchAndUnknownSupersedes(t *testing.T) {
	t.Parallel()
	base := testCapsule(t)
	if _, err := Merge(base, Delta{BaseRevision: 0, BranchBinding: base.BranchBinding}); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale error = %v", err)
	}
	if _, err := Merge(base, Delta{BaseRevision: 1, BranchBinding: base.BranchBinding, Decisions: []Decision{{ID: "new", ConflictKey: "x", Supersedes: []string{"missing"}, Statement: "x", Status: DecisionActive, Authority: AuthorityUserExplicit}}}); !errors.Is(err, ErrUnknownSupersede) {
		t.Fatalf("unknown supersedes error = %v", err)
	}
}

func TestMergeLosingCandidateLeavesAllSupersededTargetsUnchanged(t *testing.T) {
	t.Parallel()
	base := testCapsule(t)
	semantic := Decision{
		ID: StableID("decision", "low-target"), ConflictKey: "slot.low", Statement: "semantic choice", Status: DecisionActive, Authority: AuthoritySemantic,
	}
	explicit := Decision{
		ID: StableID("decision", "high-target"), ConflictKey: "slot.high", Statement: "explicit choice", Status: DecisionActive, Authority: AuthorityUserExplicit,
	}
	withTargets, err := Merge(base, Delta{BaseRevision: base.Revision, BranchBinding: base.BranchBinding, Decisions: []Decision{semantic, explicit}})
	if err != nil {
		t.Fatal(err)
	}
	loser := Decision{
		ID: StableID("decision", "loser"), ConflictKey: "slot.new", Supersedes: []string{semantic.ID, explicit.ID}, Statement: "structured guess", Status: DecisionActive, Authority: AuthorityStructured,
	}
	got, err := Merge(withTargets, Delta{BaseRevision: withTargets.Revision, BranchBinding: withTargets.BranchBinding, Decisions: []Decision{loser}})
	if err != nil {
		t.Fatal(err)
	}
	for _, decision := range got.Decisions {
		switch decision.ID {
		case semantic.ID, explicit.ID:
			if decision.Status != DecisionActive {
				t.Fatalf("losing candidate changed prior decision %q to %q", decision.ID, decision.Status)
			}
		case loser.ID:
			if decision.Status != DecisionSuperseded {
				t.Fatalf("losing candidate status = %q", decision.Status)
			}
		}
	}
}

func TestMergeRejectsForwardSupersedesWithinOneDelta(t *testing.T) {
	t.Parallel()
	base := testCapsule(t)
	first := Decision{
		ID: StableID("decision", "same-delta-first"), ConflictKey: "slot.first", Statement: "first", Status: DecisionActive, Authority: AuthorityUserExplicit,
	}
	forward := Decision{
		ID: StableID("decision", "same-delta-forward"), ConflictKey: "slot.second", Supersedes: []string{first.ID}, Statement: "forward", Status: DecisionActive, Authority: AuthorityUserExplicit,
	}
	if _, err := Merge(base, Delta{BaseRevision: base.Revision, BranchBinding: base.BranchBinding, Decisions: []Decision{first, forward}}); !errors.Is(err, ErrUnknownSupersede) {
		t.Fatalf("same-delta supersedes error = %v, want %v", err, ErrUnknownSupersede)
	}
}

func TestMergeAppliesSemanticDecisionTransition(t *testing.T) {
	t.Parallel()
	base := testCapsule(t)
	semantic := Decision{ID: StableID("decision", "transition-target"), ConflictKey: "slot.transition", Statement: "semantic choice", Status: DecisionActive, Authority: AuthoritySemantic}
	withDecision, err := Merge(base, Delta{BaseRevision: base.Revision, BranchBinding: base.BranchBinding, Decisions: []Decision{semantic}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Merge(withDecision, Delta{BaseRevision: withDecision.Revision, BranchBinding: withDecision.BranchBinding, DecisionTransitions: []DecisionTransition{{ID: semantic.ID, Status: DecisionRejected, Authority: AuthoritySemantic, SourceRef: "extractor:item-2"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Decisions[0].Status != DecisionRejected || got.Decisions[0].SourceRef != semantic.SourceRef || got.Decisions[0].StatusSourceRef != "extractor:item-2" {
		t.Fatalf("transition result = %#v", got.Decisions[0])
	}
}

func TestMergeRejectsSupersedeAndTransitionForSameTarget(t *testing.T) {
	t.Parallel()
	base := testCapsule(t)
	target := Decision{ID: StableID("decision", "double-operation-target"), ConflictKey: "slot.double-operation", Statement: "semantic choice", Status: DecisionActive, Authority: AuthoritySemantic}
	withTarget, err := Merge(base, Delta{BaseRevision: base.Revision, BranchBinding: base.BranchBinding, Decisions: []Decision{target}})
	if err != nil {
		t.Fatal(err)
	}
	addition := Decision{ID: StableID("decision", "double-operation-addition"), ConflictKey: "slot.new", Supersedes: []string{target.ID}, Statement: "replacement", Status: DecisionActive, Authority: AuthoritySemantic}
	_, err = Merge(withTarget, Delta{BaseRevision: withTarget.Revision, BranchBinding: withTarget.BranchBinding, Decisions: []Decision{addition}, DecisionTransitions: []DecisionTransition{{ID: target.ID, Status: DecisionRejected, Authority: AuthoritySemantic, SourceRef: "extractor:item-double"}}})
	if !errors.Is(err, ErrInvalidDecisionTransition) {
		t.Fatalf("double-operation error = %v, want %v", err, ErrInvalidDecisionTransition)
	}
	if withTarget.Decisions[0].Status != DecisionActive || withTarget.Decisions[0].StatusSourceRef != "" {
		t.Fatalf("prior target changed after rejection: %#v", withTarget.Decisions[0])
	}
}

func TestMergeRejectsUnsafeDecisionTransitionsAtomically(t *testing.T) {
	t.Parallel()
	base := testCapsule(t)
	semantic := Decision{ID: StableID("decision", "safe"), ConflictKey: "slot.safe", Statement: "semantic choice", Status: DecisionActive, Authority: AuthoritySemantic}
	user := Decision{ID: StableID("decision", "protected"), ConflictKey: "slot.protected", Statement: "user choice", Status: DecisionActive, Authority: AuthorityUserExplicit}
	withDecisions, err := Merge(base, Delta{BaseRevision: base.Revision, BranchBinding: base.BranchBinding, Decisions: []Decision{semantic, user}})
	if err != nil {
		t.Fatal(err)
	}
	original := withDecisions.Clone()
	addition := Decision{ID: StableID("decision", "must-not-commit"), ConflictKey: "slot.new", Statement: "new semantic choice", Status: DecisionActive, Authority: AuthoritySemantic}
	_, err = Merge(withDecisions, Delta{
		BaseRevision: withDecisions.Revision, BranchBinding: withDecisions.BranchBinding, Decisions: []Decision{addition},
		DecisionTransitions: []DecisionTransition{{ID: user.ID, Status: DecisionSuperseded, Authority: AuthoritySemantic, SourceRef: "extractor:item-3"}},
	})
	if !errors.Is(err, ErrInvalidDecisionTransition) {
		t.Fatalf("protected transition error = %v, want %v", err, ErrInvalidDecisionTransition)
	}
	if verifyErr := withDecisions.Verify(); verifyErr != nil || len(withDecisions.Decisions) != len(original.Decisions) {
		t.Fatalf("prior capsule changed after rejection: verify=%v decisions=%d", verifyErr, len(withDecisions.Decisions))
	}
	for _, decision := range withDecisions.Decisions {
		if decision.Status != original.Decisions[indexOfDecision(original.Decisions, decision.ID)].Status {
			t.Fatalf("prior decision %q changed to %q", decision.ID, decision.Status)
		}
	}
}

func TestMergeRejectsUnknownNonActiveDuplicateAndNonSemanticTransitions(t *testing.T) {
	t.Parallel()
	base := testCapsule(t)
	semantic := Decision{ID: StableID("decision", "transition-errors"), ConflictKey: "slot.transition-errors", Statement: "semantic choice", Status: DecisionActive, Authority: AuthoritySemantic}
	withDecision, err := Merge(base, Delta{BaseRevision: base.Revision, BranchBinding: base.BranchBinding, Decisions: []Decision{semantic}})
	if err != nil {
		t.Fatal(err)
	}
	withDecision.Decisions[0].Status = DecisionSuperseded
	if err := withDecision.Seal(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		transitions []DecisionTransition
	}{
		{"unknown", []DecisionTransition{{ID: "missing", Status: DecisionRejected, Authority: AuthoritySemantic, SourceRef: "extractor:item"}}},
		{"non-active", []DecisionTransition{{ID: semantic.ID, Status: DecisionRejected, Authority: AuthoritySemantic, SourceRef: "extractor:item"}}},
		{"duplicate", []DecisionTransition{{ID: semantic.ID, Status: DecisionRejected, Authority: AuthoritySemantic, SourceRef: "extractor:item"}, {ID: semantic.ID, Status: DecisionSuperseded, Authority: AuthoritySemantic, SourceRef: "extractor:item-2"}}},
		{"non-semantic-authority", []DecisionTransition{{ID: semantic.ID, Status: DecisionRejected, Authority: AuthorityUserExplicit, SourceRef: "extractor:item"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			activeBase := testCapsule(t)
			activeBase, err := Merge(activeBase, Delta{BaseRevision: activeBase.Revision, BranchBinding: activeBase.BranchBinding, Decisions: []Decision{semantic}})
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "non-active" {
				activeBase.Decisions[0].Status = DecisionSuperseded
				if err := activeBase.Seal(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Merge(activeBase, Delta{BaseRevision: activeBase.Revision, BranchBinding: activeBase.BranchBinding, DecisionTransitions: test.transitions}); !errors.Is(err, ErrInvalidDecisionTransition) {
				t.Fatalf("transition error = %v, want %v", err, ErrInvalidDecisionTransition)
			}
		})
	}
}

func TestMergeSemanticTransitionProtectsAllNonSemanticAuthorities(t *testing.T) {
	t.Parallel()
	for _, authority := range []Authority{AuthorityUserExplicit, AuthorityUserAcceptance, AuthorityStructured} {
		t.Run(string(authority), func(t *testing.T) {
			t.Parallel()
			base := testCapsule(t)
			decision := Decision{ID: StableID("decision", string(authority)), ConflictKey: "slot.protected." + string(authority), Statement: "protected choice", Status: DecisionActive, Authority: authority}
			base, err := Merge(base, Delta{BaseRevision: base.Revision, BranchBinding: base.BranchBinding, Decisions: []Decision{decision}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Merge(base, Delta{BaseRevision: base.Revision, BranchBinding: base.BranchBinding, DecisionTransitions: []DecisionTransition{{ID: decision.ID, Status: DecisionRejected, Authority: AuthoritySemantic, SourceRef: "extractor:item"}}}); !errors.Is(err, ErrInvalidDecisionTransition) {
				t.Fatalf("protected authority transition error = %v, want %v", err, ErrInvalidDecisionTransition)
			}
		})
	}
}

func indexOfDecision(decisions []Decision, id string) int {
	for i, decision := range decisions {
		if decision.ID == id {
			return i
		}
	}
	return -1
}

func TestPlanProgressDoesNotRegressAndPruneRedigests(t *testing.T) {
	t.Parallel()
	base := testCapsule(t)
	step := PlanStep{ID: StableStepID("ship"), Text: "ship", Status: StepPending, SourceRef: "structured:1"}
	progress, err := Merge(base, Delta{BaseRevision: 1, BranchBinding: base.BranchBinding, Plan: &Plan{Status: PlanAccepted, Source: SourceStructured, Steps: []PlanStep{step}}})
	if err != nil {
		t.Fatal(err)
	}
	step.Status = StepCompleted
	done, err := Merge(progress, Delta{BaseRevision: progress.Revision, BranchBinding: progress.BranchBinding, Plan: &Plan{Status: PlanAccepted, Source: SourceStructured, Steps: []PlanStep{step}}})
	if err != nil {
		t.Fatal(err)
	}
	step.Status = StepPending
	regressed, err := Merge(done, Delta{BaseRevision: done.Revision, BranchBinding: done.BranchBinding, Plan: &Plan{Status: PlanAccepted, Source: SourceStructured, Steps: []PlanStep{step}}})
	if err != nil {
		t.Fatal(err)
	}
	if regressed.Plan.Steps[0].Status != StepCompleted {
		t.Fatalf("step regressed to %q", regressed.Plan.Steps[0].Status)
	}
	pruned, err := Prune(regressed, len(mustMarshal(regressed))-1)
	if err != nil {
		t.Fatal(err)
	}
	if err := pruned.Verify(); err != nil {
		t.Fatalf("pruned digest invalid: %v", err)
	}
	if len(pruned.Plan.Steps) != 0 {
		t.Fatalf("completed step was not pruned: %#v", pruned.Plan.Steps)
	}
}

func TestPruneWithLimitsBoundsTokenEquivalentWithoutTruncatingFacts(t *testing.T) {
	t.Parallel()
	e := testCapsule(t)
	completed := PlanStep{ID: StableStepID("completed"), Text: "completed", Status: StepCompleted, SourceRef: "structured:completed"}
	e.Plan.Steps = append(e.Plan.Steps, completed)
	large := Fact{
		ID:        StableFactID("rejected", "large historical fact", "user:large"),
		Statement: strings.Repeat("ż", 512),
		Status:    FactRejected,
		Authority: AuthorityUserAcceptance,
		SourceRef: "user:large",
	}
	e.RejectedAlternatives = append(e.RejectedAlternatives, large)
	if err := e.Seal(); err != nil {
		t.Fatal(err)
	}

	withoutLarge := e.Clone()
	withoutLarge.RejectedAlternatives = nil
	if err := withoutLarge.Seal(); err != nil {
		t.Fatal(err)
	}
	limit := len(mustMarshal(withoutLarge))
	pruned, err := PruneWithLimits(e, Limits{MaxBytes: 1 << 20, MaxTokens: limit})
	if err != nil {
		t.Fatalf("PruneWithLimits() error = %v", err)
	}
	if got := TokenEquivalent(mustMarshal(pruned)); got > limit {
		t.Fatalf("token-equivalent size = %d, max = %d", got, limit)
	}
	if len(pruned.RejectedAlternatives) != 0 {
		t.Fatalf("large fact was retained or partially truncated: %#v", pruned.RejectedAlternatives)
	}
	if len(pruned.Plan.Steps) != 0 {
		t.Fatalf("completed step was retained: %#v", pruned.Plan.Steps)
	}
	if err := pruned.VerifyBranch(e.BranchBinding); err != nil {
		t.Fatalf("pruned capsule does not verify: %v", err)
	}

	second, err := PruneWithLimits(e, Limits{MaxBytes: 1 << 20, MaxTokens: limit})
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, err := CanonicalBytes(pruned)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := CanonicalBytes(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) || pruned.ContentDigest != second.ContentDigest {
		t.Fatalf("pruning is not deterministic: first=%s second=%s", pruned.ContentDigest, second.ContentDigest)
	}
}

func TestPruneWithLimitsEnforcesByteLimitIndependently(t *testing.T) {
	t.Parallel()
	e := testCapsule(t)
	e.Plan.Steps = append(e.Plan.Steps, PlanStep{ID: StableStepID("completed-byte"), Text: strings.Repeat("byte", 32), Status: StepCompleted, SourceRef: "structured:byte"})
	if err := e.Seal(); err != nil {
		t.Fatal(err)
	}
	fullBytes := len(mustMarshal(e))
	pruned, err := PruneWithLimits(e, Limits{MaxBytes: fullBytes - 1, MaxTokens: 1 << 20})
	if err != nil {
		t.Fatalf("PruneWithLimits() error = %v", err)
	}
	if got := len(mustMarshal(pruned)); got > fullBytes-1 {
		t.Fatalf("serialized size = %d, max = %d", got, fullBytes-1)
	}
	if len(pruned.Plan.Steps) != 0 {
		t.Fatalf("completed step was retained under byte limit: %#v", pruned.Plan.Steps)
	}
}

func TestParseRejectsMalformedUnknownAndDigestMismatch(t *testing.T) {
	t.Parallel()
	e := testCapsule(t)
	b, err := marshalCanonical(e)
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(strings.Replace(string(b), `"schema_version":1`, `"schema_version":2`, 1))
	if _, err := Parse(b); !errors.Is(err, ErrInvalidCapsule) {
		t.Fatalf("unknown schema error = %v", err)
	}
	b, _ = marshalCanonical(e)
	b = []byte(strings.Replace(string(b), `"content_digest":"`+e.ContentDigest+`"`, `"content_digest":"sha256:`+strings.Repeat("0", 64)+`"`, 1))
	if _, err := Parse(b); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("digest mismatch error = %v", err)
	}
	if _, err := Parse([]byte(`{"schema_version":1}`)); !errors.Is(err, ErrInvalidCapsule) {
		t.Fatalf("malformed capsule error = %v", err)
	}
	b, _ = marshalCanonical(e)
	if _, err := Parse(append(b, []byte(` {}`)...)); !errors.Is(err, ErrInvalidCapsule) {
		t.Fatalf("trailing JSON error = %v", err)
	}
}
