package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

func TestProjectTerminalDecisionEvidenceUsesCanonicalFacts(t *testing.T) {
	request := requestTerminalFacts{
		call: aipapi.Call{
			ID:                 "request-1",
			PreviousResponseID: "parent-1",
			Items: []aipapi.Item{
				{Kind: aipapi.ItemKindMessage, ID: "user-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "finish the migration"}}},
				{Kind: aipapi.ItemKindMessage, ID: "assistant-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "I inspected the repository"}}},
				{Kind: aipapi.ItemKindToolCall, ID: "tool-1", Status: aipapi.ItemStatusInProgress, ToolCall: &aipapi.ToolCallItem{CallID: "call-1", Name: "read_file"}},
				{Kind: aipapi.ItemKindToolCall, ID: "tool-2", Status: aipapi.ItemStatusCompleted, ToolCall: &aipapi.ToolCallItem{CallID: "call-2", Name: "attempt_completion"}},
				{Kind: aipapi.ItemKindToolResult, ID: "result-2", Status: aipapi.ItemStatusCompleted, ToolResult: &aipapi.ToolResultItem{CallID: "call-2", Name: "attempt_completion", Output: "migration complete"}},
			},
		},
		traceID: "trace-1",
		aLegID:  "a-leg-1",
	}
	attempt := &attemptSession{bleg: b2bua.BLegRecord{BLegID: "b-leg-2", ALegID: "a-leg-1", Seq: 2}}
	pipeline := newResponsePipeline()
	pipeline.rememberClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "candidate assistant output"})

	evidence := projectTerminalDecisionEvidence(request, attempt, pipeline)
	if evidence.Objective != "finish the migration" {
		t.Fatalf("objective = %q", evidence.Objective)
	}
	if !strings.Contains(evidence.RecentText, "I inspected the repository") {
		t.Fatalf("recent text = %q, missing canonical trajectory", evidence.RecentText)
	}
	if strings.Contains(evidence.RecentText, "migration complete") {
		t.Fatalf("recent text leaked raw tool result: %q", evidence.RecentText)
	}
	if evidence.CandidateText != "candidate assistant output" {
		t.Fatalf("candidate text = %q", evidence.CandidateText)
	}
	if !evidence.ExplicitCompletion {
		t.Fatal("explicit completion fact = false, want true")
	}
	if evidence.ActionCount != 5 {
		t.Fatalf("action count = %d, want 5", evidence.ActionCount)
	}
	if evidence.Actions[2].Status != aipapi.ItemStatusInProgress {
		t.Fatalf("partial tool status = %q, want in_progress", evidence.Actions[2].Status)
	}
	if evidence.Actions[3].Status != aipapi.ItemStatusCompleted || evidence.Actions[4].Status != aipapi.ItemStatusCompleted {
		t.Fatalf("completed tool statuses = %q, %q", evidence.Actions[3].Status, evidence.Actions[4].Status)
	}
	if evidence.Lineage.TrajectoryRef != "request-1" || evidence.Lineage.ParentRef != "parent-1" || evidence.Lineage.ProgressRef != "b-leg-2" || evidence.Lineage.Attempt != 2 {
		t.Fatalf("lineage = %+v", evidence.Lineage)
	}

	in := terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{Cause: terminaldecision.CandidateCauseNormal},
		Request:   terminaldecision.RequestIdentity{RequestID: request.call.ID},
		Policy:    terminaldecision.PolicySnapshot{Revision: "test-policy"},
		Evidence:  evidence,
		Deadline:  time.Now(),
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("projected evidence does not satisfy SDK contract: %v", err)
	}
}

func TestProjectTerminalDecisionEvidenceIsBoundedAndCopied(t *testing.T) {
	items := make([]aipapi.Item, 0, terminaldecision.MaxEvidenceActions+4)
	for i := range terminaldecision.MaxEvidenceActions + 4 {
		items = append(items, aipapi.Item{
			Kind:   aipapi.ItemKindToolCall,
			ID:     "item-" + string(rune('a'+i)),
			Status: aipapi.ItemStatusInProgress,
			ToolCall: &aipapi.ToolCallItem{
				CallID: "call-" + string(rune('a'+i)),
				Name:   "tool-name",
			},
		})
	}
	items[0] = aipapi.Item{Kind: aipapi.ItemKindMessage, ID: "objective", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: strings.Repeat("objective ", terminaldecision.MaxEvidenceTextBytes)}}}
	request := requestTerminalFacts{call: aipapi.Call{ID: "request-bound", Items: items}}
	pipeline := newResponsePipeline()
	pipeline.rememberClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: strings.Repeat("candidate ", terminaldecision.MaxEvidenceTextBytes)})

	evidence := projectTerminalDecisionEvidence(request, nil, pipeline)
	if len(evidence.Objective) > terminaldecision.MaxEvidenceTextBytes || len(evidence.RecentText) > terminaldecision.MaxEvidenceTextBytes || len(evidence.CandidateText) > terminaldecision.MaxEvidenceTextBytes {
		t.Fatalf("text bounds exceeded: objective=%d recent=%d candidate=%d", len(evidence.Objective), len(evidence.RecentText), len(evidence.CandidateText))
	}
	if evidence.ActionCount != terminaldecision.MaxEvidenceActions {
		t.Fatalf("action count = %d, want %d", evidence.ActionCount, terminaldecision.MaxEvidenceActions)
	}
	if err := (terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{Cause: terminaldecision.CandidateCauseNormal},
		Request:   terminaldecision.RequestIdentity{RequestID: request.call.ID},
		Policy:    terminaldecision.PolicySnapshot{Revision: "test-policy"},
		Evidence:  evidence,
		Deadline:  time.Now(),
	}).Validate(); err != nil {
		t.Fatalf("bounded projection rejected by SDK contract: %v", err)
	}

	items[1].ToolCall.Name = "mutated"
	if evidence.Actions[1].Name == "mutated" {
		t.Fatal("projected action aliases canonical item data")
	}
}
