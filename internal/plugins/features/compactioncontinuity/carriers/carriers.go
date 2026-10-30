// Package carriers recognizes a small, versioned catalog of canonical
// structured plan shapes. Matching is based on explicit tool/item shape; no
// provider or agent brand is inferred from surrounding text.
package carriers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
)

const (
	CodexUpdatePlanV1   = "codex.update_plan.v1"
	OpenCodeTodoV1      = "opencode.todo.v1"
	ClineTaskProgressV1 = "cline.task_progress.v1"
)

var RuleIDs = [...]string{CodexUpdatePlanV1, OpenCodeTodoV1, ClineTaskProgressV1}

type Update struct {
	RuleID string
	Plan   capsule.Plan
}

// Extract recognizes a canonical tool-call/item JSON value. matched is false
// for ordinary or near-miss items; malformed input for a recognized tool name
// returns an error so callers can count and discard it deterministically.
func Extract(data []byte) (update Update, matched bool, err error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Update{}, false, fmt.Errorf("%w: empty JSON", ErrMalformedCarrier)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Update{}, false, fmt.Errorf("%w: invalid JSON: %v", ErrMalformedCarrier, err)
	}
	name, payload, explicit := toolPayload(raw)
	if !explicit {
		// OpenCode and Cline also expose their state as canonical session/tool
		// payloads without a wrapper name. The field itself is the pinned shape;
		// no agent identity is inferred from prose or model metadata.
		if value, ok := raw["todos"]; ok {
			name, payload, explicit = "todowrite", value, true
		}
		if value, ok := raw["task_progress"]; ok {
			name, payload, explicit = "task_progress", json.RawMessage(`{"task_progress":`+string(value)+`}`), true
		}
	}
	if !explicit {
		return Update{}, false, nil
	}
	switch name {
	case "update_plan":
		plan, err := codexPlan(payload)
		if err != nil {
			return Update{}, true, err
		}
		return Update{RuleID: CodexUpdatePlanV1, Plan: plan}, true, nil
	case "todowrite":
		plan, err := openCodePlan(payload)
		if err != nil {
			return Update{}, true, err
		}
		return Update{RuleID: OpenCodeTodoV1, Plan: plan}, true, nil
	case "task_progress":
		plan, err := clinePlan(payload)
		if err != nil {
			return Update{}, true, err
		}
		return Update{RuleID: ClineTaskProgressV1, Plan: plan}, true, nil
	default:
		return Update{}, false, nil
	}
}

// Apply merges a recognized deterministic plan into a parent capsule. The
// parent binding and compare-and-merge revision are supplied by the capsule,
// so a carrier cannot move state to a detached child branch.
func Apply(previous capsule.Envelope, update Update) (capsule.Envelope, error) {
	return capsule.Merge(previous, capsule.Delta{
		BaseRevision:  previous.Revision,
		BranchBinding: previous.BranchBinding,
		Plan:          &update.Plan,
	})
}

func toolPayload(raw map[string]json.RawMessage) (name string, payload json.RawMessage, explicit bool) {
	// Canonical function-call records may use name/tool/tool_name. We only
	// accept payloads when one of those fields explicitly identifies a catalog
	// rule; arbitrary objects containing "plan" are not inferred as carriers.
	for _, key := range []string{"name", "tool", "tool_name"} {
		var candidate string
		if value, ok := raw[key]; ok && json.Unmarshal(value, &candidate) == nil && strings.TrimSpace(candidate) != "" {
			if value, ok := raw["arguments"]; ok {
				return candidate, decodeArgumentString(value), true
			}
			if value, ok := raw["input"]; ok {
				return candidate, value, true
			}
			if value, ok := raw["parameters"]; ok {
				return candidate, value, true
			}
			return candidate, nil, true
		}
	}
	// Some canonical item envelopes put the function call under item.
	if item, ok := raw["item"]; ok {
		var nested map[string]json.RawMessage
		if json.Unmarshal(item, &nested) == nil {
			return toolPayload(nested)
		}
	}
	return "", nil, false
}

func decodeArgumentString(raw json.RawMessage) json.RawMessage {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return json.RawMessage(text)
	}
	return raw
}

type codexInput struct {
	Plan        []codexStep `json:"plan"`
	Explanation string      `json:"explanation"`
}
type codexStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

func codexPlan(payload []byte) (capsule.Plan, error) {
	if len(payload) == 0 {
		return capsule.Plan{}, fmt.Errorf("%w: update_plan has no arguments", ErrMalformedCarrier)
	}
	if err := requireArrayField(payload, "plan"); err != nil {
		return capsule.Plan{}, fmt.Errorf("%w: update_plan: %v", ErrMalformedCarrier, err)
	}
	var in codexInput
	if err := decodeStrict(payload, &in); err != nil {
		return capsule.Plan{}, fmt.Errorf("%w: update_plan: %v", ErrMalformedCarrier, err)
	}
	steps := make([]capsule.PlanStep, 0, len(in.Plan))
	inProgress := 0
	for i, item := range in.Plan {
		status, err := normalizeStatus(item.Status)
		if err != nil {
			return capsule.Plan{}, fmt.Errorf("%w: update_plan step %d: %v", ErrMalformedCarrier, i, err)
		}
		if status == capsule.StepInProgress {
			inProgress++
		}
		text := cleanText(item.Step)
		if text == "" {
			return capsule.Plan{}, fmt.Errorf("%w: update_plan step %d has empty step", ErrMalformedCarrier, i)
		}
		steps = append(steps, capsule.PlanStep{ID: capsule.StableStepID(text), Text: text, Status: status, SourceRef: fmt.Sprintf("%s:%d", CodexUpdatePlanV1, i)})
	}
	if inProgress > 1 {
		return capsule.Plan{}, fmt.Errorf("%w: update_plan has multiple in-progress steps", ErrMalformedCarrier)
	}
	return capsule.Plan{Status: capsule.PlanAccepted, Source: capsule.SourceStructured, Steps: steps}, nil
}

type todoInput struct {
	Todos []todoItem `json:"todos"`
}
type todoItem struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

func openCodePlan(payload []byte) (capsule.Plan, error) {
	if err := requireArrayField(payload, "todos"); err != nil {
		return capsule.Plan{}, fmt.Errorf("%w: todowrite: %v", ErrMalformedCarrier, err)
	}
	var in todoInput
	if err := decodeStrict(payload, &in); err != nil {
		return capsule.Plan{}, fmt.Errorf("%w: todowrite: %v", ErrMalformedCarrier, err)
	}
	steps := make([]capsule.PlanStep, 0, len(in.Todos))
	inProgress := 0
	for i, item := range in.Todos {
		text := cleanText(item.Content)
		if text == "" {
			return capsule.Plan{}, fmt.Errorf("%w: todowrite todo %d has empty content", ErrMalformedCarrier, i)
		}
		status, err := normalizeStatus(item.Status)
		if err != nil {
			return capsule.Plan{}, fmt.Errorf("%w: todowrite todo %d: %v", ErrMalformedCarrier, i, err)
		}
		if status == capsule.StepInProgress {
			inProgress++
		}
		steps = append(steps, capsule.PlanStep{ID: capsule.StableStepID(text), Text: text, Status: status, SourceRef: fmt.Sprintf("%s:%d", OpenCodeTodoV1, i)})
	}
	if inProgress > 1 {
		return capsule.Plan{}, fmt.Errorf("%w: todowrite has multiple in-progress todos", ErrMalformedCarrier)
	}
	return capsule.Plan{Status: capsule.PlanAccepted, Source: capsule.SourceStructured, Steps: steps}, nil
}

type clineInput struct {
	TaskProgress []clineItem `json:"task_progress"`
}
type clineItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm"`
}

func clinePlan(payload []byte) (capsule.Plan, error) {
	if err := requireArrayField(payload, "task_progress"); err != nil {
		return capsule.Plan{}, fmt.Errorf("%w: task_progress: %v", ErrMalformedCarrier, err)
	}
	var in clineInput
	if err := decodeStrict(payload, &in); err != nil {
		return capsule.Plan{}, fmt.Errorf("%w: task_progress: %v", ErrMalformedCarrier, err)
	}
	steps := make([]capsule.PlanStep, 0, len(in.TaskProgress))
	inProgress := 0
	for i, item := range in.TaskProgress {
		text := cleanText(item.Content)
		if text == "" {
			text = cleanText(item.ActiveForm)
		}
		if text == "" {
			return capsule.Plan{}, fmt.Errorf("%w: task_progress item %d has empty content", ErrMalformedCarrier, i)
		}
		status, err := normalizeStatus(item.Status)
		if err != nil {
			return capsule.Plan{}, fmt.Errorf("%w: task_progress item %d: %v", ErrMalformedCarrier, i, err)
		}
		if status == capsule.StepInProgress {
			inProgress++
		}
		steps = append(steps, capsule.PlanStep{ID: capsule.StableStepID(text), Text: text, Status: status, SourceRef: fmt.Sprintf("%s:%d", ClineTaskProgressV1, i)})
	}
	if inProgress > 1 {
		return capsule.Plan{}, fmt.Errorf("%w: task_progress has multiple in-progress items", ErrMalformedCarrier)
	}
	return capsule.Plan{Status: capsule.PlanAccepted, Source: capsule.SourceStructured, Steps: steps}, nil
}

func normalizeStatus(value string) (capsule.StepStatus, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "pending":
		return capsule.StepPending, nil
	case "in_progress", "in-progress":
		return capsule.StepInProgress, nil
	case "completed", "complete", "done":
		return capsule.StepCompleted, nil
	case "cancelled", "canceled":
		return capsule.StepCancelled, nil
	default:
		return "", fmt.Errorf("unknown status %q", value)
	}
}

func cleanText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func requireArrayField(data []byte, field string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	raw, ok := object[field]
	if !ok {
		return fmt.Errorf("missing %q", field)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return fmt.Errorf("%q must be an array", field)
	}
	return nil
}

func decodeStrict(data []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
