package aipapi_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// harnessAlias is one surveyed coding-agent tool name and its expected
// classification. harness is provenance only: the classifier matches by name
// (trim + case-fold + exact alias), never by provider/harness identity.
type harnessAlias struct {
	harness   string
	toolName  string
	category  aipapi.ToolCategory
	mayMutate bool
}

// harnessAliasMatrix is the single data source for the cross-harness tool-name
// survey (`.kiro/specs/archive/tool-call-classification/research.md`): Codex, Pi, Cline,
// OpenCode, Hermes, OpenClaw, Kilo Code, and Claude Code. Case-folding covers
// casing dialects (Claude Code's PascalCase `Read`/`Bash` fold to the same exact
// aliases as snake_case `read`/`bash`), so no per-harness production branch exists.
//
// Multi-word camelCase names are intentionally NOT normalized to snake_case: the
// classifier is exact-name after case-folding, so `NotebookRead` folds to
// "notebookread" and falls back to unknown/true while `notebook_read` (the surveyed
// snake_case alias) classifies as file_read.
var harnessAliasMatrix = []harnessAlias{
	// Claude Code — PascalCase single-word dialects.
	{"claude", "Read", aipapi.ToolCategoryFileRead, false},
	{"claude", "Grep", aipapi.ToolCategoryFileSearch, false},
	{"claude", "Glob", aipapi.ToolCategoryFileSearch, false},
	{"claude", "LS", aipapi.ToolCategoryFileSearch, false},
	{"claude", "Bash", aipapi.ToolCategoryOSCommand, true},
	{"claude", "Edit", aipapi.ToolCategoryFileEdit, true},
	{"claude", "Write", aipapi.ToolCategoryFileEdit, true},
	{"claude", "WebFetch", aipapi.ToolCategoryWebAccess, false},
	{"claude", "WebSearch", aipapi.ToolCategoryWebAccess, false},
	// Concatenated PascalCase is not a case variant of notebook_read (no underscore).
	{"claude", "NotebookRead", aipapi.ToolCategoryUnknown, true},

	// OpenAI Codex.
	{"codex", "read_file", aipapi.ToolCategoryFileRead, false},
	{"codex", "write_file", aipapi.ToolCategoryFileEdit, true},
	{"codex", "edit", aipapi.ToolCategoryFileEdit, true},
	{"codex", "apply_patch", aipapi.ToolCategoryFileEdit, true},
	{"codex", "grep", aipapi.ToolCategoryFileSearch, false},
	{"codex", "glob", aipapi.ToolCategoryFileSearch, false},
	{"codex", "web_search", aipapi.ToolCategoryWebAccess, false},
	{"codex", "notebook_read", aipapi.ToolCategoryFileRead, false},
	{"codex", "notebook_edit", aipapi.ToolCategoryFileEdit, true},

	// Pi.
	{"pi", "read", aipapi.ToolCategoryFileRead, false},
	{"pi", "write", aipapi.ToolCategoryFileEdit, true},
	{"pi", "edit", aipapi.ToolCategoryFileEdit, true},
	{"pi", "bash", aipapi.ToolCategoryOSCommand, true},
	{"pi", "grep", aipapi.ToolCategoryFileSearch, false},
	{"pi", "glob", aipapi.ToolCategoryFileSearch, false},
	{"pi", "ls", aipapi.ToolCategoryFileSearch, false},
	{"pi", "find", aipapi.ToolCategoryFileSearch, false},
	{"pi", "list_files", aipapi.ToolCategoryFileSearch, false},
	{"pi", "semantic_search", aipapi.ToolCategoryFileSearch, false},
	{"pi", "codebase_search", aipapi.ToolCategoryFileSearch, false},
	{"pi", "web_search", aipapi.ToolCategoryWebAccess, false},
	{"pi", "web_fetch", aipapi.ToolCategoryWebAccess, false},
	{"pi", "notebook_read", aipapi.ToolCategoryFileRead, false},
	{"pi", "notebook_edit", aipapi.ToolCategoryFileEdit, true},
	{"pi", "notebookedit", aipapi.ToolCategoryFileEdit, true},
	{"pi", "execute_command", aipapi.ToolCategoryOSCommand, true},
	{"pi", "exec", aipapi.ToolCategoryOSCommand, true},
	{"pi", "exec_command", aipapi.ToolCategoryOSCommand, true},
	{"pi", "terminal", aipapi.ToolCategoryOSCommand, true},
	{"pi", "process", aipapi.ToolCategoryOSCommand, true},
	{"pi", "write_stdin", aipapi.ToolCategoryOSCommand, true},
	{"pi", "background_process", aipapi.ToolCategoryOSCommand, true},
	{"pi", "interactive_terminal", aipapi.ToolCategoryOSCommand, true},
	{"pi", "powershell", aipapi.ToolCategoryOSCommand, true},

	// Cline.
	{"cline", "execute_command", aipapi.ToolCategoryOSCommand, true},
	{"cline", "read_file", aipapi.ToolCategoryFileRead, false},
	{"cline", "write_to_file", aipapi.ToolCategoryFileEdit, true},
	{"cline", "replace_in_file", aipapi.ToolCategoryFileEdit, true},
	{"cline", "search_files", aipapi.ToolCategoryFileSearch, false},
	{"cline", "list_files", aipapi.ToolCategoryFileSearch, false},
	{"cline", "list_code_definition_names", aipapi.ToolCategoryFileSearch, false},
	{"cline", "web_fetch", aipapi.ToolCategoryWebAccess, false},
	{"cline", "browser_action", aipapi.ToolCategoryWebAccess, true},

	// Kilo Code (Cline fork — same tool surface).
	{"kilo", "execute_command", aipapi.ToolCategoryOSCommand, true},
	{"kilo", "read_file", aipapi.ToolCategoryFileRead, false},
	{"kilo", "write_to_file", aipapi.ToolCategoryFileEdit, true},
	{"kilo", "replace_in_file", aipapi.ToolCategoryFileEdit, true},
	{"kilo", "search_files", aipapi.ToolCategoryFileSearch, false},
	{"kilo", "list_files", aipapi.ToolCategoryFileSearch, false},
	{"kilo", "list_code_definition_names", aipapi.ToolCategoryFileSearch, false},
	{"kilo", "web_fetch", aipapi.ToolCategoryWebAccess, false},
	{"kilo", "browser_action", aipapi.ToolCategoryWebAccess, true},

	// OpenCode.
	{"opencode", "read", aipapi.ToolCategoryFileRead, false},
	{"opencode", "write", aipapi.ToolCategoryFileEdit, true},
	{"opencode", "edit", aipapi.ToolCategoryFileEdit, true},
	{"opencode", "patch", aipapi.ToolCategoryFileEdit, true},
	{"opencode", "bash", aipapi.ToolCategoryOSCommand, true},
	{"opencode", "grep", aipapi.ToolCategoryFileSearch, false},
	{"opencode", "glob", aipapi.ToolCategoryFileSearch, false},
	{"opencode", "ls", aipapi.ToolCategoryFileSearch, false},
	{"opencode", "search_files", aipapi.ToolCategoryFileSearch, false},
	{"opencode", "list_files", aipapi.ToolCategoryFileSearch, false},
	{"opencode", "semantic_search", aipapi.ToolCategoryFileSearch, false},
	{"opencode", "codebase_search", aipapi.ToolCategoryFileSearch, false},
	{"opencode", "web_search", aipapi.ToolCategoryWebAccess, false},
	{"opencode", "web_fetch", aipapi.ToolCategoryWebAccess, false},
	{"opencode", "web_extract", aipapi.ToolCategoryWebAccess, false},
	{"opencode", "x_search", aipapi.ToolCategoryWebAccess, false},
	{"opencode", "terminal", aipapi.ToolCategoryOSCommand, true},
	{"opencode", "process", aipapi.ToolCategoryOSCommand, true},

	// OpenClaw.
	{"openclaw", "read", aipapi.ToolCategoryFileRead, false},
	{"openclaw", "write", aipapi.ToolCategoryFileEdit, true},
	{"openclaw", "edit", aipapi.ToolCategoryFileEdit, true},
	{"openclaw", "apply_patch", aipapi.ToolCategoryFileEdit, true},
	{"openclaw", "bash", aipapi.ToolCategoryOSCommand, true},
	{"openclaw", "grep", aipapi.ToolCategoryFileSearch, false},
	{"openclaw", "glob", aipapi.ToolCategoryFileSearch, false},
	{"openclaw", "search_files", aipapi.ToolCategoryFileSearch, false},
	{"openclaw", "list_files", aipapi.ToolCategoryFileSearch, false},
	{"openclaw", "web_search", aipapi.ToolCategoryWebAccess, false},
	{"openclaw", "web_fetch", aipapi.ToolCategoryWebAccess, false},
	{"openclaw", "browser_action", aipapi.ToolCategoryWebAccess, true},
	{"openclaw", "execute_command", aipapi.ToolCategoryOSCommand, true},

	// Hermes Agent (file, shell, and browser toolset).
	{"hermes", "read_file", aipapi.ToolCategoryFileRead, false},
	{"hermes", "write_file", aipapi.ToolCategoryFileEdit, true},
	{"hermes", "edit", aipapi.ToolCategoryFileEdit, true},
	{"hermes", "grep", aipapi.ToolCategoryFileSearch, false},
	{"hermes", "shell_command", aipapi.ToolCategoryOSCommand, true},
	{"hermes", "web.run", aipapi.ToolCategoryWebAccess, false},
	{"hermes", "web_search", aipapi.ToolCategoryWebAccess, false},
	{"hermes", "websearch", aipapi.ToolCategoryWebAccess, false},
	{"hermes", "web_fetch", aipapi.ToolCategoryWebAccess, false},
	{"hermes", "webfetch", aipapi.ToolCategoryWebAccess, false},
	{"hermes", "web_extract", aipapi.ToolCategoryWebAccess, false},
	{"hermes", "browser", aipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_action", aipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_back", aipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_cdp", aipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_click", aipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_console", aipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_dialog", aipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_get_images", aipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_navigate", aipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_press", aipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_scroll", aipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_snapshot", aipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_type", aipapi.ToolCategoryWebAccess, true},
	{"hermes", "browser_vision", aipapi.ToolCategoryWebAccess, true},

	// Explicit removal-only names (generic/compatible, not a surveyed harness
	// primitive — gap-analysis G-12).
	{"generic", "delete_file", aipapi.ToolCategoryFileRemove, true},
	{"generic", "remove_file", aipapi.ToolCategoryFileRemove, true},
	{"generic", "delete_directory", aipapi.ToolCategoryFileRemove, true},
	{"generic", "remove_directory", aipapi.ToolCategoryFileRemove, true},
}

// TestHarnessToolAliasMatrix verifies every surveyed alias in the single
// cross-harness matrix classifies to its expected category and mutation posture.
func TestHarnessToolAliasMatrix(t *testing.T) {
	t.Parallel()

	if len(harnessAliasMatrix) == 0 {
		t.Fatal("harness alias matrix is empty")
	}
	seen := make(map[string]struct{}, len(harnessAliasMatrix))
	for _, a := range harnessAliasMatrix {
		if a.toolName == "" {
			t.Fatalf("matrix row has empty toolName: %#v", a)
		}
		// Rows must be unique per (harness, toolName): a shared alias like `read`
		// legitimately repeats across harnesses, but no single harness may list
		// the same name twice.
		key := a.harness + "\x00" + a.toolName
		if _, dup := seen[key]; dup {
			t.Fatalf("duplicate matrix row %q / %q", a.harness, a.toolName)
		}
		seen[key] = struct{}{}

		gotCat, gotMutate := aipapi.ClassifyToolName(a.toolName)
		if gotCat != a.category || gotMutate != a.mayMutate {
			t.Errorf("%s %q: ClassifyToolName = (%q, %v), want (%q, %v)",
				a.harness, a.toolName, gotCat, gotMutate, a.category, a.mayMutate)
		}
	}
}
