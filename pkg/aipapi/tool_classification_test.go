package aipapi_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestClassifyToolName_normalizationAndFallback covers normalization and the
// conservative fallback. The full cross-harness alias enumeration lives in the
// single harnessAliasMatrix data source (tool_classification_matrix_test.go).
func TestClassifyToolName_normalizationAndFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		wantCat    aipapi.ToolCategory
		wantMutate bool
	}{
		{"trims surrounding whitespace", "  Read  ", aipapi.ToolCategoryFileRead, false},
		{"trims tabs and newlines", "\tBash\n", aipapi.ToolCategoryOSCommand, true},
		{"empty name", "", aipapi.ToolCategoryUnknown, true},
		{"whitespace-only name", "   ", aipapi.ToolCategoryUnknown, true},
		{"unknown custom tool", "mcp_custom_tool", aipapi.ToolCategoryUnknown, true},
		{"read_ prefix not inferred", "read_something", aipapi.ToolCategoryUnknown, true},
		{"delete_ prefix not inferred", "delete_whatever", aipapi.ToolCategoryUnknown, true},
		{"browser_ prefix not inferred", "browser_whatever", aipapi.ToolCategoryUnknown, true},
		// Underscore-preserving case-fold of the surveyed notebook_read alias.
		{"Notebook_Read folds to notebook_read", "Notebook_Read", aipapi.ToolCategoryFileRead, false},
		// CamelCase without underscores is a different spelling, not a case variant.
		{"NotebookRead is not notebook_read", "NotebookRead", aipapi.ToolCategoryUnknown, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotCat, gotMutate := aipapi.ClassifyToolName(tc.input)
			if gotCat != tc.wantCat {
				t.Fatalf("ClassifyToolName(%q) category = %q, want %q", tc.input, gotCat, tc.wantCat)
			}
			if gotMutate != tc.wantMutate {
				t.Fatalf("ClassifyToolName(%q) mayMutate = %v, want %v", tc.input, gotMutate, tc.wantMutate)
			}
		})
	}
}

// TestToolEventFromEvent_osCommandIndependentOfArgumentPayload pins R2.6/R2.7/R6.6
// at the projection layer that actually carries command text. ClassifyToolName
// cannot see arguments; a regression that parsed Delta/ArgsDelta would still
// pass a name-only helper test.
func TestToolEventFromEvent_osCommandIndependentOfArgumentPayload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		toolName string
		delta    string
	}{
		{"exec_command cat", "exec_command", `{"command":"cat README.md"}`},
		{"bash ls", "bash", `{"command":"ls -la"}`},
		{"execute_command rg", "execute_command", `{"command":"rg TODO"}`},
		{"exec_command git status", "exec_command", `{"command":"git status"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := aipapi.Event{
				Kind:       aipapi.EventToolCallArgsDelta,
				ToolCallID: "c1",
				ToolName:   tc.toolName,
				Delta:      tc.delta,
			}
			te, ok := aipapi.ToolEventFromEvent(ev)
			if !ok {
				t.Fatal("expected ok")
			}
			if te.ArgsDelta != tc.delta {
				t.Fatalf("ArgsDelta=%q, want payload preserved", te.ArgsDelta)
			}
			if te.Category != aipapi.ToolCategoryOSCommand || !te.MayMutateLocalFS {
				t.Fatalf("name=%q delta=%q -> (%q,%v), want (os_command,true) without parsing arguments",
					tc.toolName, tc.delta, te.Category, te.MayMutateLocalFS)
			}
		})
	}
}

// TestToolEventFromEvent_applyPatchStaysFileEditWithDeleteHunk pins G-12 / R1.8:
// patch tools remain file_edit/true even when the payload grammar can delete files.
func TestToolEventFromEvent_applyPatchStaysFileEditWithDeleteHunk(t *testing.T) {
	t.Parallel()
	deleteHunk := "*** Begin Patch\n*** Delete File: secret.txt\n*** End Patch\n"
	ev := aipapi.Event{
		Kind:       aipapi.EventToolCallArgsDelta,
		ToolCallID: "c1",
		ToolName:   "apply_patch",
		Delta:      deleteHunk,
	}
	te, ok := aipapi.ToolEventFromEvent(ev)
	if !ok {
		t.Fatal("expected ok")
	}
	if te.ArgsDelta != deleteHunk {
		t.Fatalf("ArgsDelta not preserved")
	}
	if te.Category != aipapi.ToolCategoryFileEdit || !te.MayMutateLocalFS {
		t.Fatalf("apply_patch with delete hunk -> (%q,%v), want (file_edit,true)", te.Category, te.MayMutateLocalFS)
	}
}

func TestToolEventFromEvent_classifiesName(t *testing.T) {
	t.Parallel()
	ev := aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "Read"}
	te, ok := aipapi.ToolEventFromEvent(ev)
	if !ok {
		t.Fatal("expected ok")
	}
	if te.Category != aipapi.ToolCategoryFileRead || te.MayMutateLocalFS {
		t.Fatalf("category=%q mayMutate=%v, want file_read/false", te.Category, te.MayMutateLocalFS)
	}
}

func TestToolEventFromEvent_namelessIsConservativeUnknown(t *testing.T) {
	t.Parallel()
	ev := aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"x":1}`}
	te, ok := aipapi.ToolEventFromEvent(ev)
	if !ok {
		t.Fatal("expected ok")
	}
	if te.Category != aipapi.ToolCategoryUnknown || !te.MayMutateLocalFS {
		t.Fatalf("category=%q mayMutate=%v, want unknown/true", te.Category, te.MayMutateLocalFS)
	}
}
