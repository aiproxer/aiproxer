package compactioncontinuity

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/injection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

type matrixFact struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Statement string `json:"statement"`
	Status    string `json:"status"`
	Rationale string `json:"rationale"`
	SourceRef string `json:"source_ref"`
}

type matrixDecision struct {
	ID          string   `json:"id"`
	ConflictKey string   `json:"conflict_key"`
	Supersedes  []string `json:"supersedes"`
	Statement   string   `json:"statement"`
	Status      string   `json:"status"`
	Rationale   string   `json:"rationale"`
	SourceRef   string   `json:"source_ref"`
}

type matrixPlanUpdate struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Status    string `json:"status"`
	SourceRef string `json:"source_ref"`
}

func matrixResultWithPlan(base uint64, decision *matrixDecision, plans []matrixPlanUpdate, facts ...matrixFact) string {
	if facts == nil {
		facts = []matrixFact{}
	}
	if plans == nil {
		plans = []matrixPlanUpdate{}
	}
	updates := []matrixDecision{}
	if decision != nil {
		if decision.Supersedes == nil {
			decision.Supersedes = []string{}
		}
		updates = append(updates, *decision)
	}
	value := struct {
		SchemaVersion uint8              `json:"schema_version"`
		BaseRevision  uint64             `json:"base_revision"`
		Facts         []matrixFact       `json:"facts"`
		PlanUpdates   []matrixPlanUpdate `json:"plan_updates"`
		Decisions     []matrixDecision   `json:"decision_updates"`
		Removals      []any              `json:"remove_or_supersede"`
	}{1, base, facts, plans, updates, []any{}}
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func matrixPlanPayload(statuses ...string) string {
	names := []string{"inspect bounded state", "validate current decision", "release continuity"}
	plan := make([]map[string]string, 0, len(statuses))
	for i, status := range statuses {
		plan = append(plan, map[string]string{"step": names[i], "status": status})
	}
	raw, _ := json.Marshal(map[string]any{"plan": plan})
	return string(raw)
}

func matrixCall(previous aipapi.Call, carrierID string, statuses []string, userID, userText string) aipapi.Call {
	out := aipapi.CloneCall(previous)
	out.Items = append(
		out.Items,
		aipapi.Item{Kind: aipapi.ItemKindToolCall, ID: carrierID, ToolCall: &aipapi.ToolCallItem{
			Name: "update_plan", CallID: carrierID, Arguments: []byte(matrixPlanPayload(statuses...)),
		}},
		aipapi.Item{
			Kind: aipapi.ItemKindMessage, ID: userID, Role: aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: userText}},
		},
	)
	return out
}

func matrixOpen(t *testing.T, plugin *Plugin, parent *openParentFake, background *openBackgroundFake, call aipapi.Call, tx, result string) capsule.Envelope {
	t.Helper()
	before := len(background.submits)
	if err := plugin.RequestOpened(context.Background(), call, []compaction.Event{{Phase: compaction.PhaseCompleted, TransactionID: tx, BLegID: "primary-b"}}, openMeta(), compaction.Services{BackgroundAux: background}); err != nil {
		t.Fatalf("RequestOpened(%s): %v", tx, err)
	}
	if len(background.submits) != before+1 {
		t.Fatalf("%s submissions=%d want %d", tx, len(background.submits), before+1)
	}
	background.awaitResult.Text.Reset()
	background.awaitResult.Text.WriteString(result)
	if err := plugin.BeforeResponseRelease(context.Background(), nil, compaction.ResponsePreview{Kind: compaction.PreviewCompletionCandidate, TransactionID: tx}, openMeta(), compaction.Services{BackgroundAux: background}); err != nil {
		t.Fatalf("BeforeResponseRelease(%s): %v", tx, err)
	}
	if parent.state.PendingJobID != "" {
		t.Fatalf("%s left a pending job: %+v order=%v forget=%v", tx, parent.state, parent.order, background.forget)
	}
	e, err := capsule.Verify(parent.state.CapsuleJSON, parent.branch.Binding)
	if err != nil || e.Revision != parent.state.Revision {
		t.Fatalf("capsule revision=%d state revision=%d err=%v", e.Revision, parent.state.Revision, err)
	}
	return e
}

func TestRepeatedCompactionSemanticMatrix(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	var call aipapi.Call
	var revisions []capsule.Envelope
	call = aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, ID: "plan-1", Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Implementation plan: inspect bounded state before release."}}},
		{Kind: aipapi.ItemKindMessage, ID: "choice-1", Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "I choose the bounded adapter; constraint: no provider SDK. Rationale: safety. Reject the unbounded alternative. Open question: can reload preserve state?"}}},
	}}
	revisions = append(revisions, matrixOpen(t, plugin, parent, background, call, "compact-1", matrixResultWithPlan(
		1,
		&matrixDecision{ID: "decision-bounded", ConflictKey: "product.mode", Statement: "Use the bounded adapter.", Status: "active", Rationale: "Explicit user safety choice.", SourceRef: "choice-1"},
		[]matrixPlanUpdate{{ID: capsule.StableStepID("inspect bounded state"), Text: "inspect bounded state", Status: "pending", SourceRef: "plan-1"}},
		matrixFact{Kind: "constraint", ID: "constraint-sdk", Statement: "No provider SDK in core.", Status: "active", Rationale: "Keep the boundary portable.", SourceRef: "choice-1"},
		matrixFact{Kind: "rejected_alternative", ID: "reject-unbounded", Statement: "Do not use the unbounded path.", Status: "rejected", Rationale: "It risks uncontrolled context growth.", SourceRef: "choice-1"},
		matrixFact{Kind: "open_question", ID: "question-reload", Statement: "Can reload preserve state?", Status: "active", SourceRef: "choice-1"},
	)))
	call = matrixCall(call, "carrier-2", []string{"in_progress", "pending"}, "choice-2", "Correction: use the bounded adapter with strict validation; do not use the unbounded path.")
	revisions = append(revisions, matrixOpen(t, plugin, parent, background, call, "compact-2", matrixResultWithPlan(
		3,
		&matrixDecision{ID: "decision-bounded-v2", ConflictKey: "product.mode", Supersedes: []string{"decision-bounded"}, Statement: "Use the bounded adapter with strict validation.", Status: "active", Rationale: "Correction after review.", SourceRef: "choice-2"},
		nil,
	)))
	call = matrixCall(call, "carrier-3", []string{"completed", "in_progress", "pending"}, "choice-3", "Proceed with validation and keep the remaining release step open.")
	revisions = append(revisions, matrixOpen(t, plugin, parent, background, call, "compact-3", matrixResultWithPlan(
		5,
		nil,
		nil,
		matrixFact{Kind: "rejected_alternative", ID: "reject-legacy", Statement: "Reject the legacy rewrite path.", Status: "rejected", Rationale: "It loses authoritative state.", SourceRef: "choice-3"},
		matrixFact{Kind: "open_question", ID: "question-release", Statement: "When is release safe after reload?", Status: "active", SourceRef: "choice-3"},
	)))

	if len(revisions) != 3 || len(background.submits) != 3 {
		t.Fatalf("successive compactions=%d submissions=%d", len(revisions), len(background.submits))
	}
	e := revisions[2]
	finalEncoded, err := capsule.Encode(e)
	if err != nil {
		t.Fatalf("final capsule encoding for diagnostics: %v", err)
	}
	run := func(name string, ok bool) {
		t.Run(name, func(t *testing.T) {
			if !ok {
				t.Fatalf("matrix check failed: observed capsule revision=%d digest=%s envelope=%s parent_state=%+v", e.Revision, e.ContentDigest, finalEncoded, parent.state)
			}
		})
	}
	run("accepted plan", e.Plan.Status == capsule.PlanAccepted)
	run("decision retained", hasDecisionStatus(e, "decision-bounded", capsule.DecisionActive) || hasDecisionStatus(e, "decision-bounded", capsule.DecisionSuperseded))
	run("constraint retained", hasFactStatus(e.Constraints, "constraint-sdk", capsule.FactActive))
	run("rationale retained", slices.ContainsFunc(e.Decisions, func(d capsule.Decision) bool {
		return d.ID == "decision-bounded" && strings.Contains(d.Rationale, "safety")
	}))
	run("meaningful rejection retained", hasFactStatus(e.RejectedAlternatives, "reject-unbounded", capsule.FactRejected))
	run("open question retained", hasFactStatus(e.OpenQuestions, "question-reload", capsule.FactActive))
	run("correction is active", hasDecisionStatus(e, "decision-bounded-v2", capsule.DecisionActive))
	run("old decision superseded", hasDecisionStatus(e, "decision-bounded", capsule.DecisionSuperseded))
	run("first plan is pending", stepStatus(revisions[0], "inspect bounded state") == capsule.StepPending)
	run("plan advances in progress", stepStatus(revisions[1], "inspect bounded state") == capsule.StepInProgress)
	run("plan completes", stepStatus(e, "inspect bounded state") == capsule.StepCompleted)
	run("current step survives", stepStatus(e, "validate current decision") == capsule.StepInProgress)
	run("pending step survives", stepStatus(e, "release continuity") == capsule.StepPending)
	run("later rejection retained", hasFactStatus(e.RejectedAlternatives, "reject-legacy", capsule.FactRejected))
	run("later question retained", hasFactStatus(e.OpenQuestions, "question-release", capsule.FactActive))
	run("capsule remains parent-bound", e.BranchBinding == parent.branch.Binding)
	run("all facts are unique", uniqueFacts(e))
	run("prompts stay bounded", promptsBounded(background))
	for i, revision := range revisions {
		if revision.Revision == 0 || revision.ContentDigest == "" {
			t.Fatalf("revision %d lacks digest: %+v", i+1, revision)
		}
	}
	pruned := revisions[2].Clone()
	steps := pruned.Plan.Steps[:0]
	for _, step := range pruned.Plan.Steps {
		if step.Status != capsule.StepCompleted {
			steps = append(steps, step)
		}
	}
	pruned.Plan.Steps = steps
	pruned.ContentDigest = ""
	if err := pruned.Seal(); err != nil {
		t.Fatal(err)
	}
	encoded, err := capsule.Encode(pruned)
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := capsule.PruneWithLimits(revisions[2], capsule.Limits{MaxBytes: len(encoded)})
	if err != nil || stepStatus(bounded, "inspect bounded state") == capsule.StepCompleted || stepStatus(bounded, "release continuity") != capsule.StepPending {
		t.Fatalf("completed history was not pruned while current state survived: capsule=%+v err=%v", bounded, err)
	}
}

func TestRepeatedCompactionPaths(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		preserve PreserveConfig
		carrier  bool
		wantJobs int
	}{
		{"deterministic-only", PreserveConfig{Plan: true}, true, 0},
		{"semantic-only", PreserveConfig{UserDecisions: true}, false, 1},
		{"mixed", PreserveConfig{Plan: true, UserDecisions: true}, true, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parent := &openParentFake{branch: ParentBranch{Binding: "sha256:" + strings.Repeat("b", 64), TraceID: "trace", ALegID: "parent-a", BLegID: "parent-b"}}
			background := &openBackgroundFake{}
			cfg := openConfig(t)
			cfg.Preserve = tt.preserve
			plugin, err := New(cfg, parent)
			if err != nil {
				t.Fatal(err)
			}
			call := matrixCall(aipapi.Call{}, "carrier", []string{"pending"}, "choice", "I choose the bounded mode.")
			if !tt.carrier {
				call.Items = call.Items[1:]
			}
			_ = plugin.RequestOpened(context.Background(), call, openEvent(), openMeta(), compaction.Services{BackgroundAux: background})
			if got := len(background.submits); got != tt.wantJobs {
				t.Fatalf("jobs=%d want %d", got, tt.wantJobs)
			}
		})
	}
}

func TestRepeatedCompactionOpaqueBoundariesReinjectSameRevision(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	cfg := plugin.cfg
	cfg.Preserve = PreserveConfig{Plan: true}
	plugin, err := New(cfg, parent)
	if err != nil {
		t.Fatal(err)
	}
	call := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindToolCall, ID: "carrier", ToolCall: &aipapi.ToolCallItem{Name: "update_plan", CallID: "carrier", Arguments: []byte(matrixPlanPayload("pending"))}},
	}}
	seedBase, err := capsule.New(parent.branch.Binding)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := capsule.Encode(seedBase)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := digestArray(seedBase.ContentDigest)
	if err != nil {
		t.Fatal(err)
	}
	parent.state = ParentState{Revision: seedBase.Revision, CapsuleJSON: encoded, CapsuleDigest: digest}
	_ = plugin.RequestOpened(context.Background(), call, openEvent(), openMeta(), compaction.Services{BackgroundAux: background})
	e, err := capsule.Verify(parent.state.CapsuleJSON, parent.branch.Binding)
	if err != nil || e.Revision != parent.state.Revision {
		t.Fatalf("capsule verification: %v", err)
	}
	base := openCall()
	first := aipapi.CloneCall(base)
	if err := plugin.BeforeRequest(context.Background(), &first, compaction.RequestPreview{Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: "opaque-1"}, openMeta(), compaction.Services{}); err != nil {
		t.Fatal(err)
	}
	retry := aipapi.CloneCall(first)
	_ = plugin.BeforeRequest(context.Background(), &retry, compaction.RequestPreview{Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: "opaque-1"}, openMeta(), compaction.Services{})
	if len(retry.Instructions) != 1 || strings.Count(retry.Instructions[0].Parts[0].Text, injection.BlockStart) != 1 {
		t.Fatalf("same boundary duplicated projection: %d", len(retry.Instructions))
	}
	second := aipapi.CloneCall(base)
	_ = plugin.BeforeRequest(context.Background(), &second, compaction.RequestPreview{Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: "opaque-2"}, openMeta(), compaction.Services{})
	if len(second.Instructions) != 1 || strings.Count(second.Instructions[0].Parts[0].Text, injection.BlockStart) != 1 {
		t.Fatalf("distinct boundary did not receive one projection: %d", len(second.Instructions))
	}
	if !strings.Contains(first.Instructions[0].Parts[0].Text, fmt.Sprintf(`"revision":%d`, e.Revision)) || first.Instructions[0].Parts[0].Text != second.Instructions[0].Parts[0].Text {
		t.Fatal("same capsule revision was not reinjected identically across opaque boundaries")
	}
	if parent.state.Revision != e.Revision || len(background.submits) != 0 {
		t.Fatalf("opaque previews changed revision or submitted work: state=%+v submits=%d", parent.state, len(background.submits))
	}
}

func hasDecisionStatus(e capsule.Envelope, id string, status capsule.DecisionStatus) bool {
	return slices.ContainsFunc(e.Decisions, func(d capsule.Decision) bool { return d.ID == id && d.Status == status })
}

func hasFactStatus(items []capsule.Fact, id string, status capsule.FactStatus) bool {
	return slices.ContainsFunc(items, func(f capsule.Fact) bool { return f.ID == id && f.Status == status })
}

func stepStatus(e capsule.Envelope, text string) capsule.StepStatus {
	for _, s := range e.Plan.Steps {
		if s.Text == text {
			return s.Status
		}
	}
	return ""
}

func uniqueFacts(e capsule.Envelope) bool {
	seen := map[string]bool{}
	for _, group := range [][]capsule.Fact{e.Constraints, e.RejectedAlternatives, e.OpenQuestions} {
		for _, f := range group {
			if seen[f.ID] {
				return false
			}
			seen[f.ID] = true
		}
	}
	for _, d := range e.Decisions {
		if seen[d.ID] {
			return false
		}
		seen[d.ID] = true
	}
	return true
}

func promptsBounded(background *openBackgroundFake) bool {
	for _, request := range background.submits {
		if request.Call == nil || len(request.Call.Messages) < 2 || len(request.Call.Messages[1].Parts) == 0 || len(request.Call.Messages[1].Parts[0].Text) > 12_000 {
			return false
		}
	}
	return true
}
