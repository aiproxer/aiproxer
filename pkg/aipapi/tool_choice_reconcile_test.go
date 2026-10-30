package aipapi_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func testCallWithTools(tools []aipapi.ToolDef, tc aipapi.ToolChoice) *aipapi.Call {
	return &aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind: aipapi.PartText,
				Text: "hi",
			}},
		}},
		Tools:      tools,
		ToolChoice: tc,
	}
}

func TestReconcileToolChoiceAfterToolListChange_noneWithToolsBecomesAuto(t *testing.T) {
	t.Parallel()
	c := testCallWithTools([]aipapi.ToolDef{{Name: "x"}}, aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone})
	aipapi.ReconcileToolChoiceAfterToolListChange(c)
	if c.ToolChoice.Mode != aipapi.ToolChoiceAuto {
		t.Fatalf("mode %q", c.ToolChoice.Mode)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileToolChoiceAfterToolListChange_requiredMissingNameDowngrades(t *testing.T) {
	t.Parallel()
	c := testCallWithTools([]aipapi.ToolDef{{Name: "a"}}, aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "b"})
	aipapi.ReconcileToolChoiceAfterToolListChange(c)
	if c.ToolChoice.Mode != aipapi.ToolChoiceAuto || c.ToolChoice.Name != "" {
		t.Fatalf("got %+v", c.ToolChoice)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileToolChoiceAfterToolListChange_requiredNoToolsDowngrades(t *testing.T) {
	t.Parallel()
	c := testCallWithTools(nil, aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "x"})
	aipapi.ReconcileToolChoiceAfterToolListChange(c)
	if c.ToolChoice.Mode != aipapi.ToolChoiceAuto {
		t.Fatalf("mode %q", c.ToolChoice.Mode)
	}
}

func TestReconcileToolChoiceAfterToolListChange_allowedSubsetDropsRemovedTools(t *testing.T) {
	t.Parallel()
	tools := []aipapi.ToolDef{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	c := testCallWithTools(tools, aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny, AllowedTools: []string{"a", "b", "c"}})
	// A catalog filter removed "a" and "b"; the subset must follow the survivors.
	c.Tools = []aipapi.ToolDef{{Name: "c"}}
	aipapi.ReconcileToolChoiceAfterToolListChange(c)
	if c.ToolChoice.Mode != aipapi.ToolChoiceAny {
		t.Fatalf("mode %q", c.ToolChoice.Mode)
	}
	if len(c.ToolChoice.AllowedTools) != 1 || c.ToolChoice.AllowedTools[0] != "c" {
		t.Fatalf("subset %v", c.ToolChoice.AllowedTools)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileToolChoiceAfterToolListChange_allowedSubsetNoSurvivorsKeepsMode(t *testing.T) {
	t.Parallel()
	c := testCallWithTools([]aipapi.ToolDef{{Name: "a"}}, aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{"a"}})
	c.Tools = nil
	aipapi.ReconcileToolChoiceAfterToolListChange(c)
	if c.ToolChoice.Mode != aipapi.ToolChoiceAuto {
		t.Fatalf("mode %q", c.ToolChoice.Mode)
	}
	if len(c.ToolChoice.AllowedTools) != 0 {
		t.Fatalf("subset should be cleared, got %v", c.ToolChoice.AllowedTools)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
