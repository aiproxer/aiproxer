package aipapi_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDecisionCall_ExclusiveAuthority(t *testing.T) {
	t.Parallel()
	// Every mixed-authority case builds its own request. Sharing the parent's
	// request would let the invalid-payload check at the end of this test
	// satisfy the subtest assertion for the wrong reason.
	for name, mutate := range map[string]func(*aipapi.Call){
		"messages": func(c *aipapi.Call) {
			c.Messages = []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("text")}}}
		},
		"instructions": func(c *aipapi.Call) { c.Instructions = []aipapi.Message{{Role: aipapi.RoleSystem}} },
		"items":        func(c *aipapi.Call) { c.Items = []aipapi.Item{} },
		"continuation": func(c *aipapi.Call) { c.PreviousResponseID = "parent" },
		"tools":        func(c *aipapi.Call) { c.Tools = []aipapi.ToolDef{{Name: "tool"}} },
		"tool mode":    func(c *aipapi.Call) { c.ToolChoice.Mode = aipapi.ToolChoiceAuto },
		"tool name":    func(c *aipapi.Call) { c.ToolChoice.Name = "tool" },
		"tool subset":  func(c *aipapi.Call) { c.ToolChoice.AllowedTools = []string{"tool"} },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			req := mkDecisionRequest()
			c := aipapi.Call{Decision: &req}
			mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("mixed decision authority accepted")
			}
		})
	}
	req := mkDecisionRequest()
	call := aipapi.Call{Decision: &req}
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}
	call.Invocation.Operation = aipapi.OperationOpenAIChatCompletions
	if err := call.Validate(); err != nil {
		t.Fatalf("validation depends on nonserialized invocation: %v", err)
	}
	if !slices.Contains(aipapi.RequiredCapabilities(call), aipapi.CapabilityDecisions) {
		t.Fatal("decision capability missing")
	}
	req.Evidence = json.RawMessage(`{`)
	if err := call.Validate(); err == nil {
		t.Fatal("invalid decision payload accepted")
	}
}

func TestDecisionCall_CloneIsolatesAllPayloads(t *testing.T) {
	t.Parallel()
	req := mkDecisionRequest()
	req.Questions[0].FalseCriteria = json.RawMessage(`"false"`)
	req.Questions[1].Options[0].Description = json.RawMessage(`"option"`)
	before := mkDecisionRequest()
	before.Questions[0].FalseCriteria = json.RawMessage(`"false"`)
	before.Questions[1].Options[0].Description = json.RawMessage(`"option"`)
	clone := aipapi.CloneCall(aipapi.Call{Decision: &req})
	clone.Decision.Evidence[0] = ' '
	clone.Decision.Questions[0].ID = "changed"
	clone.Decision.Questions[0].Instructions[0] = ' '
	clone.Decision.Questions[0].TrueCriteria[0] = ' '
	clone.Decision.Questions[0].FalseCriteria[0] = ' '
	clone.Decision.Questions[1].Options[0].Name = "changed"
	clone.Decision.Questions[1].Options[0].Description[0] = ' '
	clone.Decision.Questions[2].Levels[0][0] = ' '
	if !reflect.DeepEqual(req, before) {
		t.Fatal("clone mutated original decision")
	}
}

func TestDecisionEvent_OrderingAndCommitment(t *testing.T) {
	t.Parallel()
	result := mkDecisionResult()
	ev := aipapi.Event{Kind: aipapi.EventDecisionResult, Decision: &result}
	valid := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, ev, {Kind: aipapi.EventUsageDelta}, {Kind: aipapi.EventResponseFinished}}
	if err := aipapi.ValidateEventSequence(valid); err != nil {
		t.Fatal(err)
	}
	if !aipapi.OutputCommitted(ev) || aipapi.OutputCommitted(valid[0]) {
		t.Fatal("result must commit output, lifecycle frame must not")
	}
	for name, events := range map[string][]aipapi.Event{
		"before started": {ev, {Kind: aipapi.EventResponseFinished}},
		"duplicate":      {valid[0], ev, ev, valid[3]},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := aipapi.ValidateEventSequence(events); err == nil {
				t.Fatal("invalid decision sequence accepted")
			}
		})
	}
	if err := aipapi.ValidateEventEnvelope(&ev); err != nil {
		t.Fatal(err)
	}
	ev.Decision = nil
	if err := aipapi.ValidateEventEnvelope(&ev); err == nil {
		t.Fatal("result event without payload accepted")
	}
	ev.Kind = aipapi.EventUsageDelta
	ev.Decision = &result
	if err := aipapi.ValidateEventEnvelope(&ev); err == nil {
		t.Fatal("decision payload on unrelated event accepted")
	}
}
