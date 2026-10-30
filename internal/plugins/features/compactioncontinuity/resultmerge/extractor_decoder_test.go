package resultmerge

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
)

func TestExtractorDecoderStrictResultMergesThroughService(t *testing.T) {
	t.Parallel()
	job, background, parent, _, base := validFixture(t)
	background.result.Text.WriteString(`{"schema_version":1,"base_revision":1,"facts":[],"plan_updates":[],"decision_updates":[{"id":"decision-strict","conflict_key":"runtime.mode","supersedes":[],"statement":"Use the bounded mode.","status":"active","rationale":"","source_ref":"source-1"}],"remove_or_supersede":[]}`)
	decoder := NewExtractorDecoder(ExtractorDecoderConfig{AllowedSourceRefs: []string{"source-1"}})
	service, err := New(background, parent, decoder, Config{MaxCapsuleBytes: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := service.Consume(context.Background(), job)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if outcome.Status != StatusMerged || outcome.State.Revision != base.Revision+1 {
		t.Fatalf("outcome = %#v", outcome)
	}
	merged, err := capsule.Decode(parent.committedBytes)
	if err != nil {
		t.Fatalf("decode merged capsule: %v", err)
	}
	if len(merged.Decisions) != 1 || merged.Decisions[0].ID != "decision-strict" {
		t.Fatalf("merged decisions = %#v", merged.Decisions)
	}
}

func TestExtractorDecoderSemanticRemovalMergesThroughService(t *testing.T) {
	t.Parallel()
	job, background, parent, _, base := validFixture(t)
	base.Decisions = []capsule.Decision{{
		ID: "semantic-old", ConflictKey: "runtime.mode", Statement: "Use the old mode.",
		Status: capsule.DecisionActive, Authority: capsule.AuthoritySemantic, SourceRef: "source-1",
	}}
	if err := base.Seal(); err != nil {
		t.Fatal(err)
	}
	raw, err := capsule.Encode(base)
	if err != nil {
		t.Fatal(err)
	}
	parent.state.CapsuleJSON = raw
	parent.state.CapsuleDigest = mustDigestArray(t, base.ContentDigest)
	background.result.Text.WriteString(`{"schema_version":1,"base_revision":1,"facts":[],"plan_updates":[],"decision_updates":[],"remove_or_supersede":[{"id":"semantic-old","status":"rejected","source_ref":"source-1"}]}`)
	decoder := NewExtractorDecoder(ExtractorDecoderConfig{AllowedSourceRefs: []string{"source-1"}})
	service, err := New(background, parent, decoder, Config{MaxCapsuleBytes: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := service.Consume(context.Background(), job)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if outcome.Status != StatusMerged {
		t.Fatalf("outcome = %#v", outcome)
	}
	merged, err := capsule.Decode(parent.committedBytes)
	if err != nil {
		t.Fatalf("decode merged capsule: %v", err)
	}
	if got := merged.Decisions[0].Status; got != capsule.DecisionRejected {
		t.Fatalf("decision status = %q, want rejected", got)
	}
}

func TestExtractorDecoderRejectsToolOutputAndUnknownRemoval(t *testing.T) {
	t.Parallel()
	job, background, _, _, base := validFixture(t)
	decoder := NewExtractorDecoder(ExtractorDecoderConfig{AllowedSourceRefs: []string{"source-1"}})
	background.result.ToolNames = map[string]string{"tool-1": "shell"}
	background.result.ToolCallOrder = []string{"tool-1"}
	if _, err := decoder.Decode(background.result, DecodeInput{Previous: base, ExpectedBranch: job.ParentBranchBinding}); err == nil {
		t.Fatal("tool-bearing result accepted")
	}
	background.result.ToolNames = nil
	background.result.ToolCallOrder = nil
	background.result.Text.Reset()
	background.result.Text.WriteString(`{"schema_version":1,"base_revision":1,"facts":[],"plan_updates":[],"decision_updates":[],"remove_or_supersede":[{"id":"old","status":"superseded","source_ref":"source-1"}]}`)
	if _, err := decoder.Decode(background.result, DecodeInput{Previous: base, ExpectedBranch: job.ParentBranchBinding}); err == nil {
		t.Fatal("unknown removal result accepted")
	}
}
