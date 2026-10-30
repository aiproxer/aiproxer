package resultmerge

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
)

func TestServiceEnforcesConfiguredCapsuleTokenBoundAfterMerge(t *testing.T) {
	t.Parallel()
	job, background, parent, decoder, base := validFixture(t)
	decoder.delta.Plan = &capsule.Plan{
		Status: capsule.PlanAccepted,
		Source: capsule.SourceSemantic,
		Steps: []capsule.PlanStep{{
			ID: "completed-large-step", Text: strings.Repeat("x", 2_000),
			Status: capsule.StepCompleted, SourceRef: "msg-1",
		}},
	}
	withoutCompletedStep, err := capsule.Merge(base, capsule.Delta{
		SchemaVersion: capsule.SchemaVersion,
		BaseRevision:  base.Revision,
		BranchBinding: base.BranchBinding,
		Decisions:     decoder.delta.Decisions,
	})
	if err != nil {
		t.Fatalf("build token-bound expectation: %v", err)
	}
	expectedBytes, err := capsule.Encode(withoutCompletedStep)
	if err != nil {
		t.Fatalf("encode token-bound expectation: %v", err)
	}

	service, err := New(background, parent, decoder, Config{
		MaxCapsuleBytes: 1 << 20, MaxCapsuleTokens: len(expectedBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Consume(context.Background(), job); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	merged, err := capsule.Decode(parent.committedBytes)
	if err != nil {
		t.Fatalf("decode committed capsule: %v", err)
	}
	if got := capsule.TokenEquivalent(parent.committedBytes); got > len(expectedBytes) {
		t.Fatalf("committed capsule token-equivalent size=%d, want <=%d", got, len(expectedBytes))
	}
	if len(merged.Plan.Steps) != 0 {
		t.Fatalf("completed step survived token pruning: %#v", merged.Plan.Steps)
	}
}
