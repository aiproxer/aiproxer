package aipapi

import "strings"

// ToolCategory is a coarse, protocol-neutral category for coding-agent tool names.
// It is derived metadata for tool-policy/reactor consumers, never user or provider
// authority, and never an allow/deny decision by itself.
type ToolCategory string

const (
	ToolCategoryFileRead   ToolCategory = "file_read"
	ToolCategoryFileSearch ToolCategory = "file_search"
	ToolCategoryOSCommand  ToolCategory = "os_command"
	ToolCategoryFileEdit   ToolCategory = "file_edit"
	ToolCategoryFileRemove ToolCategory = "file_remove"
	ToolCategoryWebAccess  ToolCategory = "web_access"
	ToolCategoryUnknown    ToolCategory = "unknown"
)

// ClassifyToolName derives a coarse category and a conservative
// may-mutate-local-filesystem hint from a coding-agent tool name.
//
// The classifier trims surrounding whitespace, case-folds, and matches an exact
// static alias set. It does not inspect tool arguments, shell command text,
// schemas, descriptions, provider identity, or execution results, and it does not
// infer from arbitrary prefixes/suffixes/substrings. Empty or unrecognized names
// return (ToolCategoryUnknown, true) so an unfamiliar tool is never falsely
// asserted to be filesystem-safe.
//
// The returned bool means the named tool family has the capability to mutate the
// local filesystem; it is not evidence that a specific invocation did so.
func ClassifyToolName(name string) (ToolCategory, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	// Read-only filesystem families.
	case "read", "read_file", "notebook_read":
		return ToolCategoryFileRead, false
	case "grep", "find", "glob", "ls", "search_files", "list_files",
		"list_code_definition_names", "semantic_search", "codebase_search":
		return ToolCategoryFileSearch, false
	// Potentially mutating local execution/filesystem families.
	case "bash", "execute_command", "exec", "exec_command", "shell_command",
		"terminal", "process", "write_stdin", "background_process",
		"interactive_terminal", "powershell":
		return ToolCategoryOSCommand, true
	case "edit", "write", "replace_in_file", "write_to_file", "write_file",
		"patch", "apply_patch", "notebook_edit", "notebookedit":
		return ToolCategoryFileEdit, true
	case "delete_file", "remove_file", "delete_directory", "remove_directory":
		return ToolCategoryFileRemove, true
	// Read-oriented network lookup/fetch/extract.
	case "web.run", "web_search", "websearch", "web_fetch", "webfetch",
		"web_extract", "x_search":
		return ToolCategoryWebAccess, false
	// Interactive browser automation; a coarse name-only classifier cannot prove
	// an action cannot download/save or otherwise create local artifacts.
	case "browser", "browser_action", "browser_back", "browser_cdp",
		"browser_click", "browser_console", "browser_dialog",
		"browser_get_images", "browser_navigate", "browser_press",
		"browser_scroll", "browser_snapshot", "browser_type", "browser_vision":
		return ToolCategoryWebAccess, true
	default:
		return ToolCategoryUnknown, true
	}
}
