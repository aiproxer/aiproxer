package aipapi_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCloneCall_deepCopiesSlicesAndOptionPointers(t *testing.T) {
	t.Parallel()
	temp := 0.5
	parallel := true
	orig := aipapi.Call{
		Session: aipapi.SessionRef{Metadata: map[string]string{"tenant": "private"}},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools: []aipapi.ToolDef{{Name: "x", Parameters: []byte(`{}`)}},
		Options: aipapi.GenerationOptions{
			Temperature:       &temp,
			ParallelToolCalls: &parallel,
			ReasoningEffort:   "high",
			Verbosity:         aipapi.VerbosityHigh,
		},
	}
	cl := aipapi.CloneCall(orig)
	cl.Messages[0].Parts[0].Text = "mutated"
	*cl.Options.Temperature = 0.1
	*cl.Options.ParallelToolCalls = false
	cl.Tools[0].Name = "y"
	cl.Session.Metadata["tenant"] = "mutated"

	if orig.Messages[0].Parts[0].Text != "hi" {
		t.Fatalf("messages mutated")
	}
	if *orig.Options.Temperature != 0.5 {
		t.Fatalf("temperature pointer shared")
	}
	if !*orig.Options.ParallelToolCalls {
		t.Fatalf("parallel pointer shared")
	}
	if orig.Tools[0].Name != "x" {
		t.Fatalf("tools slice shared")
	}
	if orig.Session.Metadata["tenant"] != "private" {
		t.Fatalf("session metadata map shared")
	}
	if orig.Options.Verbosity != aipapi.VerbosityHigh {
		t.Fatalf("verbosity should be copied")
	}
}

func TestCloneCall_preservesNilReasoningOpaque(t *testing.T) {
	t.Parallel()
	orig := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleAssistant,
			Parts: []aipapi.Part{{
				Kind: aipapi.PartReasoning,
				Reasoning: &aipapi.ReasoningPart{
					Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
					Text:    "t",
					Opaque:  nil,
				},
			}},
		}},
	}
	cl := aipapi.CloneCall(orig)
	if cl.Messages[0].Parts[0].Reasoning == nil {
		t.Fatal("cloned Reasoning missing")
	}
	if cl.Messages[0].Parts[0].Reasoning.Opaque != nil {
		t.Fatalf("nil Opaque must stay nil, got %#v", cl.Messages[0].Parts[0].Reasoning.Opaque)
	}
}

func TestCloneCall_preservesEmptyNonNilReasoningOpaque(t *testing.T) {
	t.Parallel()
	empty := json.RawMessage{}
	orig := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleAssistant,
			Parts: []aipapi.Part{{
				Kind: aipapi.PartReasoning,
				Reasoning: &aipapi.ReasoningPart{
					Dialect:   aipapi.ReasoningDialectAnthropicRedactedThinkingV1,
					Signature: "sig",
					Opaque:    empty,
				},
			}},
		}},
	}
	cl := aipapi.CloneCall(orig)
	if cl.Messages[0].Parts[0].Reasoning == nil {
		t.Fatal("clone lost Reasoning")
	}
	if cl.Messages[0].Parts[0].Reasoning.Opaque == nil {
		t.Fatal("clone must preserve empty non-nil Opaque (not coerce to nil)")
	}
	if !reflect.DeepEqual(orig.Messages[0].Parts[0].Reasoning.Opaque, cl.Messages[0].Parts[0].Reasoning.Opaque) {
		t.Fatalf("empty Opaque DeepEqual failed: orig=%#v clone=%#v",
			orig.Messages[0].Parts[0].Reasoning.Opaque, cl.Messages[0].Parts[0].Reasoning.Opaque)
	}
	if !reflect.DeepEqual(orig.Messages[0].Parts[0].Reasoning, cl.Messages[0].Parts[0].Reasoning) {
		t.Fatal("cloned ReasoningPart must DeepEqual original when Opaque is empty non-nil")
	}
}

func TestCloneCall_deepCopiesAllowedToolsSubset(t *testing.T) {
	t.Parallel()
	orig := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools: []aipapi.ToolDef{{Name: "fn1"}, {Name: "fn2"}},
		ToolChoice: aipapi.ToolChoice{
			Mode:         aipapi.ToolChoiceAuto,
			AllowedTools: []string{"fn1", "fn2"},
		},
	}
	cl := aipapi.CloneCall(orig)
	cl.ToolChoice.AllowedTools[0] = "mutated"
	if orig.ToolChoice.AllowedTools[0] != "fn1" {
		t.Fatalf("allowed tools slice shared: %v", orig.ToolChoice.AllowedTools)
	}
	if len(orig.ToolChoice.AllowedTools) != 2 {
		t.Fatalf("subset length changed: %v", orig.ToolChoice.AllowedTools)
	}
}

// TestEventClone_ReleasesEmptySliceBackingArrays proves the withhold queue's
// deep copy cannot retain the producer's large backing arrays behind empty
// slices: every slice reachable from Event.Clone is independently owned. One
// recursive reflection walker covers every slice and pointer leaf so a new
// nested field fails here until Clone detaches it.
func TestEventClone_ReleasesEmptySliceBackingArrays(t *testing.T) {
	t.Parallel()
	checkEventClone(t, collectEventLeaves(t))
}
