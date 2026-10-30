package compactioncontinuity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/carriers"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/injection"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/resultmerge"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/source"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

func extractCarrierUpdate(carrier source.StructuredCarrier) (carriers.Update, bool, error) {
	name := ""
	switch carrier.Type {
	case carriers.CodexUpdatePlanV1:
		name = "update_plan"
	case carriers.OpenCodeTodoV1:
		name = "todowrite"
	case carriers.ClineTaskProgressV1:
		name = "task_progress"
	}
	if name == "" {
		return carriers.Update{}, false, nil
	}
	record, err := json.Marshal(struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{Name: name, Arguments: json.RawMessage(carrier.Payload)})
	if err != nil {
		return carriers.Update{}, false, err
	}
	return carriers.Extract(record)
}

func committedEvent(events []compaction.Event) (compaction.Event, bool) {
	for _, event := range events {
		if strings.TrimSpace(event.TransactionID) == "" {
			continue
		}
		if event.Phase != compaction.PhaseStarted && event.Phase != compaction.PhaseCompleted {
			continue
		}
		return event, true
	}
	return compaction.Event{}, false
}

func (p *Plugin) previousState(parent ParentBranch, state ParentState) (capsule.Envelope, source.Envelope, error) {
	var previous capsule.Envelope
	if len(state.CapsuleJSON) != 0 {
		decoded, err := capsule.Verify(state.CapsuleJSON, parent.Binding)
		if err != nil || decoded.Revision != state.Revision {
			return capsule.Envelope{}, source.Envelope{}, fmt.Errorf("invalid parent capsule")
		}
		storedDigest, digestErr := digestArray(decoded.ContentDigest)
		if digestErr != nil || (state.CapsuleDigest != ([32]byte{}) && storedDigest != state.CapsuleDigest) {
			return capsule.Envelope{}, source.Envelope{}, fmt.Errorf("invalid parent capsule digest")
		}
		previous = decoded
	}
	window := source.Envelope{Version: source.EnvelopeVersion}
	if len(state.SourceJSON) != 0 {
		dec := json.NewDecoder(strings.NewReader(string(state.SourceJSON)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&window); err != nil || window.Version != source.EnvelopeVersion {
			return capsule.Envelope{}, source.Envelope{}, fmt.Errorf("invalid parent source")
		}
		window.Bytes = len(state.SourceJSON)
	}
	return previous, window, nil
}

type carrierRecognizer struct{}

func (carrierRecognizer) Recognize(item aipapi.Item) (source.StructuredCarrier, bool) {
	if item.ToolCall == nil || len(item.ToolCall.Arguments) == 0 {
		return source.StructuredCarrier{}, false
	}
	// carriers.Extract recognizes the complete canonical call shape. Item
	// authority stores the name beside the raw arguments, so wrap those fields
	// for recognition while retaining only the bounded arguments as source.
	record, err := json.Marshal(struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{Name: item.ToolCall.Name, Arguments: item.ToolCall.Arguments})
	if err != nil {
		return source.StructuredCarrier{}, false
	}
	update, matched, err := carriers.Extract(record)
	if err != nil || !matched {
		return source.StructuredCarrier{}, false
	}
	return source.StructuredCarrier{Type: update.RuleID, Version: 1, Payload: string(item.ToolCall.Arguments)}, true
}

func encodeWatermark(value source.HighWatermark) string {
	b, _ := json.Marshal(value)
	return string(b)
}

func stateBound(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func coalesceKey(binding, transaction, watermark string) string {
	h := sha256.New()
	_, _ = h.Write([]byte("aip.compaction-continuity.extract.v1\x00"))
	for _, value := range []string{strings.TrimSpace(binding), strings.TrimSpace(transaction), strings.TrimSpace(watermark)} {
		_, _ = fmt.Fprintf(h, "%d:", len(value))
		_, _ = h.Write([]byte(value))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func digestArray(value string) ([32]byte, error) {
	var out [32]byte
	encoded := strings.TrimPrefix(strings.TrimSpace(value), "sha256:")
	if len(encoded) != 64 {
		return out, errors.New("invalid capsule digest")
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil || len(raw) != len(out) {
		return out, errors.New("invalid capsule digest")
	}
	copy(out[:], raw)
	return out, nil
}

func validParentBranch(parent ParentBranch) bool {
	for _, value := range []string{parent.Binding, parent.TraceID, parent.ALegID, parent.BLegID} {
		if len([]byte(strings.TrimSpace(value))) > 512 {
			return false
		}
	}
	return strings.TrimSpace(parent.Binding) != ""
}

// semanticExtractionEligible keeps deterministic plan updates as a complete
// answer only for an explicitly plan-only policy. A carrier never satisfies
// requested user-decision, constraint, rationale, or rejected-alternative
// categories, so those categories retain semantic eligibility.
func semanticExtractionEligible(cfg Config, candidate, deterministicPlan bool) bool {
	if !cfg.Extractor.Enabled || !candidate {
		return false
	}
	requestedSemantic := cfg.Preserve.UserDecisions || cfg.Preserve.Constraints || cfg.Preserve.Rationale || cfg.Preserve.RejectedAlternatives
	if deterministicPlan && !requestedSemantic {
		return false
	}
	return cfg.Preserve.Plan || requestedSemantic
}

type parentCoordinator struct {
	port   ParentPort
	parent ParentBranch
}

func (p parentCoordinator) ValidatePendingJob(ctx context.Context, binding string, jobID auxiliary.JobID) (resultmerge.ParentState, error) {
	if binding != p.parent.Binding {
		return resultmerge.ParentState{}, fmt.Errorf("resultmerge: parent branch mismatch")
	}
	state, err := p.port.ValidatePendingJob(ctx, p.parent, jobID)
	if err != nil {
		return resultmerge.ParentState{}, err
	}
	return resultParentState(state, p.parent), nil
}

func (p parentCoordinator) CommitCapsuleForJob(ctx context.Context, binding string, jobID auxiliary.JobID, resultBinding string, expectedRevision uint64, capsuleJSON []byte, digest [32]byte, watermark string) (resultmerge.ParentState, error) {
	if binding != p.parent.Binding || resultBinding != p.parent.Binding {
		return resultmerge.ParentState{}, fmt.Errorf("resultmerge: parent branch mismatch")
	}
	state, err := p.port.CommitCapsuleForJob(ctx, p.parent, jobID, resultBinding, expectedRevision, capsuleJSON, digest, watermark)
	if err != nil {
		return resultmerge.ParentState{}, err
	}
	return resultParentState(state, p.parent), nil
}

func resultParentState(state ParentState, parent ParentBranch) resultmerge.ParentState {
	return resultmerge.ParentState{
		BranchBinding:            parent.Binding,
		Revision:                 state.Revision,
		CapsuleJSON:              append([]byte(nil), state.CapsuleJSON...),
		CapsuleDigest:            state.CapsuleDigest,
		SourceHighWatermark:      state.SourceHighWatermark,
		PendingJobID:             state.PendingJobID,
		PendingJobTargetRevision: state.PendingJobTargetRevision,
		PendingJobBranchBinding:  state.PendingJobBranchBinding,
	}
}

func sourceRefs(previous capsule.Envelope, window source.Envelope) []string {
	seen := make(map[string]struct{})
	for _, entry := range window.Entries {
		if value := strings.TrimSpace(entry.ItemID); value != "" {
			seen[value] = struct{}{}
		}
	}
	for _, step := range previous.Plan.Steps {
		if value := strings.TrimSpace(step.SourceRef); value != "" {
			seen[value] = struct{}{}
		}
	}
	for _, decision := range previous.Decisions {
		if value := strings.TrimSpace(decision.SourceRef); value != "" {
			seen[value] = struct{}{}
		}
	}
	for _, facts := range [][]capsule.Fact{previous.Constraints, previous.RejectedAlternatives, previous.OpenQuestions} {
		for _, fact := range facts {
			if value := strings.TrimSpace(fact.SourceRef); value != "" {
				seen[value] = struct{}{}
			}
		}
	}
	refs := make([]string, 0, len(seen))
	for value := range seen {
		refs = append(refs, value)
	}
	slices.Sort(refs)
	return refs
}

func injectionContainsProjection(call aipapi.Call, previous capsule.Envelope, binding string, cfg Config) bool {
	return injection.HasProjection(call, previous, binding, injection.ProjectionLimits{MaxBytes: cfg.Capsule.MaxBytes, MaxTokens: cfg.Capsule.MaxTokens})
}
