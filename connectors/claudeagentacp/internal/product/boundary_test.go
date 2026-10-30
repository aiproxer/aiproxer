package product

import (
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/acp"
)

// Claude Code is the agent, not a model behind a client harness. The spec must
// not opt into delivering client instructions as its session system prompt.
func TestClaudeSpec_DoesNotAdoptClientSystemPrompt(t *testing.T) {
	t.Parallel()
	spec, err := NewClaudeSpec("/bin/true", "/ws", &AuthEnv{source: "api_key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := any(spec).(acp.SessionSystemPromptSpec); ok {
		t.Fatal("client instructions must not become the Claude Code session system prompt")
	}
}

// The agent executes its own tools. Canonical tool-call events mean "the client
// must execute this call" on every frontend (OpenAI tool_calls, Anthropic
// tool_use), so agent-internal activity must stay on the shared summary path.
func TestClaudeSpec_DoesNotEmitAgentToolsAsClientToolCalls(t *testing.T) {
	t.Parallel()
	spec, err := NewClaudeSpec("/bin/true", "/ws", &AuthEnv{source: "api_key"})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := any(spec).(acp.StructuredToolProvenanceSpec); ok && p.StructuredToolProvenance() {
		t.Fatal("agent-internal tool activity must not be emitted as canonical client tool calls")
	}
}
