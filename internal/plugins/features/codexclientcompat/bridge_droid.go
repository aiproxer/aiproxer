package codexclientcompat

import (
	"slices"
	"sort"
	"strings"

	"github.com/aiproxer/aiproxer/internal/agentfacts"
)

var (
	droidNativeToolNames = map[string]struct{}{
		"Read": {}, "LS": {}, "Execute": {}, "Edit": {}, "Grep": {}, "Glob": {},
		"Create": {}, "TodoWrite": {}, "WebSearch": {}, "FetchUrl": {}, "ExitSpecMode": {},
	}
	droidSystemPromptKeywords = []string{
		"you are droid",
		"droid, an ai",
		"factory droid",
	}
	droidUserAgentTokens = []string{"factory-cli", "factory_cli", "factorydroid", "droid"}
)

func droidAgentMatch(in compatInput) bool {
	if matchesAgentFamily(in.agents, agentfacts.FamilyDroid) {
		return true
	}
	// Preserve legacy bridge substring behavior for existing compatibility
	// callers; session identity matching uses the strict shared token catalog.
	return slices.ContainsFunc(in.agents, droidUserAgentMatch)
}

func droidPromptMatch(in compatInput) bool {
	lower := strings.ToLower(in.prompt)
	for _, keyword := range droidSystemPromptKeywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func droidUserAgentMatch(userAgent string) bool {
	lower := strings.ToLower(userAgent)
	for _, pattern := range droidUserAgentTokens {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func isDroidHarnessText(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "factory droid") && strings.Contains(lower, "execute") && strings.Contains(lower, "todowrite")
}

func buildDroidBridge(availableTools []string) string {
	native := sortedNativeDroidTools()
	available := availableTools
	if len(available) == 0 {
		available = native
	}
	availableText := joinBacktickList(available)
	nativeText := joinBacktickList(native)
	return droidBridgeMarker + ":\n" +
		"- This session is using Factory Droid tools, not Codex-native tools.\n" +
		"- Use only tool names that are actually available in this session: " + availableText + ".\n" +
		"- Prefer the native Factory Droid tool family when available: " + nativeText + ".\n" +
		"- Use Droid argument shapes exactly for the native file/execute tools: `Read(file_path, offset?, limit?)`, `LS(directory_path?)`, `Execute(command, timeout?, cwd?)`, `Edit(file_path, old_str, new_str)`, `Grep(pattern, path?, file_pattern?, max_results?)`, `Glob(pattern, max_results?)`, `Create(file_path, content)`.\n" +
		"- Do not emit Codex-native tool names such as `read`, `read_file`, `bash`, `shell`, `apply_patch`, `grep_files`, or `list_dir`.\n" +
		"- Use `TodoWrite` instead of Codex task-planner tools, `WebSearch` for web search, and `FetchUrl` for direct URL fetches when those tools are available.\n" +
		"- Keep tool arguments as JSON objects; for `Execute`, the `command` value must be a single shell command string, not an array.\n" +
		"\n" +
		criticalInstruction("Droid")
}

func sortedNativeDroidTools() []string {
	out := make([]string, 0, len(droidNativeToolNames))
	for name := range droidNativeToolNames {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func joinBacktickList(names []string) string {
	if len(names) == 0 {
		return ""
	}
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = "`" + name + "`"
	}
	return strings.Join(parts, ", ")
}
