package reasoninge2e

import (
	"fmt"
)

// ChatResponse is one proxy assistant observation recorded by the client emulator.
// Tool.Result is optional on the wire response; materialization uses the plan tool result.
type ChatResponse struct {
	VisibleText string
	Reasoning   []ReasoningBlock
	Tool        *ToolExchange
	Streaming   bool
}

// ClientEmulator (Conversation) owns an immutable Plan plus recorded actual proxy outputs.
// It keeps observed and submitted histories independently, returns defensive copies, and
// never calls testing.T or logs payloads.
type ClientEmulator struct {
	plan           Plan
	recorded       []AssistantTurn // actual observed visible/tool/reasoning
	submitted      []AssistantTurn // policy-materialized submitted turns
	userPrompts    []string        // prompts used for recorded turns (and open next)
	awaitingRecord bool
}

// NewClientEmulator returns a conversation emulator bound to plan.
func NewClientEmulator(plan Plan) *ClientEmulator {
	return &ClientEmulator{plan: plan}
}

// Record validates resp against the next Plan.Observed turn (structural, content-safe),
// then appends independent observed and submitted history entries.
// Visible/tool on submitted come from the actual recorded response; reasoning follows plan policy.
func (e *ClientEmulator) Record(resp ChatResponse) error {
	if e == nil {
		return fmt.Errorf("reasoninge2e client: nil emulator")
	}
	turns := e.plan.Turns()
	idx := len(e.recorded)
	if idx >= len(turns) {
		return fmt.Errorf("reasoninge2e client: seed=%d structural mismatch: record_past_plan turn_index=%d", e.plan.Seed, idx)
	}
	want := turns[idx]
	if err := matchObserved(e.plan.Seed, want, resp); err != nil {
		return err
	}
	observed := AssistantTurn{
		ID:          want.ID,
		VisibleText: resp.VisibleText,
		Reasoning:   cloneBlocks(resp.Reasoning),
		Tool:        cloneTool(resp.Tool),
		Streaming:   resp.Streaming,
	}
	// Preserve plan tool result for later materialization when wire response omits it.
	if observed.Tool != nil && observed.Tool.Result == "" && want.Observed.Tool != nil {
		observed.Tool.Result = want.Observed.Tool.Result
	}
	submitted := AssistantTurn{
		ID:          want.ID,
		VisibleText: observed.VisibleText,
		Reasoning:   cloneBlocks(want.Submitted.Reasoning),
		Tool:        cloneTool(observed.Tool),
		Streaming:   observed.Streaming,
	}
	e.recorded = append(e.recorded, observed)
	e.submitted = append(e.submitted, submitted)
	e.awaitingRecord = false
	return nil
}

// MaterializeChatRequest builds the next Chat Completions messages list.
// History assistant visible/tool structure comes from recorded actual responses;
// reasoning follows the plan retention policy (submitted). Tool results and prior
// user prompts are included. It is an error to materialize again while a prior
// materialize is still awaiting Record (unrecorded turn).
func (e *ClientEmulator) MaterializeChatRequest(nextUserPrompt string) ([]map[string]any, error) {
	if e == nil {
		return nil, fmt.Errorf("reasoninge2e client: nil emulator")
	}
	if e.awaitingRecord {
		return nil, fmt.Errorf("reasoninge2e client: seed=%d structural mismatch: unrecorded turn_index=%d", e.plan.Seed, len(e.recorded))
	}
	if len(e.recorded) > len(e.plan.Turns()) {
		return nil, fmt.Errorf("reasoninge2e client: seed=%d structural mismatch: recorded_past_plan", e.plan.Seed)
	}
	out := make([]map[string]any, 0, len(e.recorded)*3+1)
	for i := range e.recorded {
		prompt := ""
		if i < len(e.userPrompts) {
			prompt = e.userPrompts[i]
		}
		out = append(out, UserMessage(prompt))
		out = append(out, AssistantTurnToChatMessage(cloneTurn(e.submitted[i])))
		if e.submitted[i].Tool != nil {
			tool := *e.submitted[i].Tool
			out = append(out, ToolResultMessage(tool))
		}
	}
	out = append(out, UserMessage(nextUserPrompt))
	// Remember prompt for the turn about to be recorded.
	if len(e.userPrompts) == len(e.recorded) {
		e.userPrompts = append(e.userPrompts, nextUserPrompt)
	} else if len(e.userPrompts) == len(e.recorded)+1 {
		e.userPrompts[len(e.recorded)] = nextUserPrompt
	} else {
		// Keep prompts aligned with recorded count (+ at most one open prompt).
		e.userPrompts = append(append([]string(nil), e.userPrompts[:len(e.recorded)]...), nextUserPrompt)
	}
	e.awaitingRecord = true
	return cloneChatMessages(out), nil
}

// ObservedHistory returns defensive copies of recorded actual assistant observations.
func (e *ClientEmulator) ObservedHistory() []AssistantTurn {
	if e == nil || len(e.recorded) == 0 {
		return nil
	}
	out := make([]AssistantTurn, len(e.recorded))
	for i := range e.recorded {
		out[i] = cloneTurn(e.recorded[i])
	}
	return out
}

// SubmittedHistory returns defensive copies of policy-materialized submitted turns.
func (e *ClientEmulator) SubmittedHistory() []AssistantTurn {
	if e == nil || len(e.submitted) == 0 {
		return nil
	}
	out := make([]AssistantTurn, len(e.submitted))
	for i := range e.submitted {
		out[i] = cloneTurn(e.submitted[i])
	}
	return out
}

// Plan returns the immutable bound plan (defensive turn copies via Plan methods).
func (e *ClientEmulator) Plan() Plan {
	if e == nil {
		return Plan{}
	}
	return e.plan
}

func matchObserved(seed uint64, want PlannedTurn, got ChatResponse) error {
	prefix := fmt.Sprintf("reasoninge2e client: seed=%d turn=%s mode=%s", seed, want.ID, want.Mode)
	if got.VisibleText != want.Observed.VisibleText {
		return fmt.Errorf("%s structural mismatch: visible_text", prefix)
	}
	if err := checkTool(prefix, want.Observed.Tool, normalizeRecordTool(got.Tool, want.Observed.Tool)); err != nil {
		return err
	}
	if len(got.Reasoning) != len(want.Observed.Reasoning) {
		return fmt.Errorf("%s structural mismatch: reasoning_count got=%d want=%d", prefix, len(got.Reasoning), len(want.Observed.Reasoning))
	}
	for i := range want.Observed.Reasoning {
		if !blocksEqualStructural(want.Observed.Reasoning[i], got.Reasoning[i]) {
			return fmt.Errorf("%s structural mismatch: reasoning_block block=%d", prefix, i)
		}
	}
	return nil
}

func normalizeRecordTool(got, want *ToolExchange) *ToolExchange {
	if got == nil {
		return nil
	}
	cp := *got
	// Wire assistant messages omit tool result; compare using planned result when absent.
	if cp.Result == "" && want != nil {
		cp.Result = want.Result
	}
	return &cp
}

func cloneChatMessages(in []map[string]any) []map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make([]map[string]any, len(in))
	for i := range in {
		out[i] = cloneChatMessage(in[i])
	}
	return out
}

func cloneChatMessage(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch t := v.(type) {
		case []map[string]any:
			cp := make([]map[string]any, len(t))
			for i := range t {
				cp[i] = cloneChatMessage(t[i])
			}
			out[k] = cp
		case map[string]any:
			out[k] = cloneChatMessage(t)
		default:
			out[k] = v
		}
	}
	return out
}
