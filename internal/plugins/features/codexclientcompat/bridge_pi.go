package codexclientcompat

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/agentfacts"
)

var (
	piPromptMarkers = []string{
		"operating inside pi",
		"coding agent harness",
		"available tools:",
		"in addition to the tools above",
		"guidelines:",
	}
	piUserAgentMarkers = []string{
		"@mariozechner/pi-coding-agent",
		" pi/",
		"pi-coding-agent",
	}
)

func piAgentMatch(in compatInput) bool {
	for _, candidate := range in.agents {
		if matchesSingleAgentFamily(candidate, agentfacts.FamilyPi) && hasPiUserAgentMarker(candidate) {
			return true
		}
		// Preserve the bridge's established substring markers independently of
		// the stricter shared identity rules used for classification.
		if hasPiUserAgentMarker(candidate) {
			return true
		}
	}
	return false
}

func hasPiUserAgentMarker(candidate string) bool {
	lower := strings.ToLower(candidate)
	for _, marker := range piUserAgentMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func piPromptMatch(in compatInput) bool {
	lower := strings.ToLower(in.prompt)
	hits := 0
	for _, marker := range piPromptMarkers {
		if strings.Contains(lower, marker) {
			hits++
		}
	}
	return hits >= 2
}

func isPiHarnessText(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range piPromptMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func buildPiBridge() string {
	return piBridgeMarker + ":\n" +
		"- Use only tools exposed by the pi client for this session.\n" +
		"- Use `bash` for terminal execution with a JSON object containing string `command` and optional numeric `timeout` in seconds; pi has no default timeout.\n" +
		"- Do not emit `shell`, `local_shell_call`, or array-valued shell commands; pi expects the `bash` tool name.\n" +
		"- Do not use `apply_patch`; use pi's `edit` tool for exact text replacement in a single file.\n" +
		"- For `edit`, pass `path` plus an `edits` array of replacements with `oldText` and `newText`, each matched against the original file.\n" +
		"- For file reads use `read` with `path` and optional `offset`/`limit`; for full rewrites use `write` with `path` and `content`.\n" +
		"- Keep responses concise and show file paths clearly.\n" +
		"\n" +
		criticalInstruction("pi")
}
