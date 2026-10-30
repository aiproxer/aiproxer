package codexclientcompat

import (
	"encoding/json"
	"strings"

	"github.com/aiproxer/aiproxer/internal/agentfacts"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// hermesIdentitySentence is the exact upstream Hermes Agent identity sentence.
const hermesIdentitySentence = "You are Hermes Agent, an intelligent AI assistant created by Nous Research."

var hermesUserAgentMarkers = []string{
	"hermes-agent",
	"nousresearch/hermes-agent",
	"hermes/",
}

func hermesAgentMatch(in compatInput) bool {
	if matchesAgentFamily(in.agents, agentfacts.FamilyHermes) {
		return true
	}
	// Keep the original compatibility substring rules without promoting those
	// looser matches into the shared high-confidence identity catalog.
	for _, candidate := range in.agents {
		lower := strings.ToLower(candidate)
		for _, marker := range hermesUserAgentMarkers {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return false
}

func hermesPromptMatch(in compatInput) bool {
	return strings.Contains(strings.ToLower(in.prompt), strings.ToLower(hermesIdentitySentence))
}

func isHermesBridgeText(text string) bool {
	return strings.Contains(text, hermesBridgeMarker)
}

func buildHermesBridge() string {
	return hermesBridgeMarker + ":\n" +
		"- Preserve the Hermes Agent identity and system prompt; do not replace or restate it as Codex.\n" +
		"- Use structured function/tool calls for every action; never inline textual " +
		"`to=functions.<name>` or Harmony-style tool calls in assistant content.\n" +
		"- Continue using the available tools until the task is complete and verified.\n" +
		"- Perform prerequisite lookup and discovery (files, symbols, context) with tools before acting.\n" +
		"- When retrievable context is missing, fetch it with available tools; do not guess or fabricate it.\n" +
		"\n" +
		"CRITICAL INSTRUCTION:\n" +
		"(a) Keep the Hermes identity/system prompt intact; append compatibility guidance, never overwrite it.\n" +
		"(b) Never emit textual tool-call syntax (`to=functions.<name>`, Harmony calls) in assistant content; use structured tool calls only."
}

func applyHermesToolStrict(call *aipapi.Call) {
	if call.Extensions == nil {
		call.Extensions = map[string]json.RawMessage{}
	}
	if _, ok := call.Extensions[extCodexToolStrictKey]; ok {
		return
	}
	call.Extensions[extCodexToolStrictKey] = json.RawMessage("false")
}
