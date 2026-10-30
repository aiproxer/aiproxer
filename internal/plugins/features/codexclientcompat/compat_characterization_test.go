package codexclientcompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestApplyCompat_emptyInstructionsUsesLegacyCodexIdentity pins the prompt
// compensation that requirement 11.5 removes with the legacy connector. When a
// bridge is selected and the call carries no instructions, the feature seeds the
// bridge with the Codex CLI identity sentence rather than passing an empty
// instruction set through. This is legacy identity compensation, not a
// harness-neutral fallback.
func TestApplyCompat_emptyInstructionsUsesLegacyCodexIdentity(t *testing.T) {
	t.Parallel()

	const wantLegacyIdentity = "You are Codex, based on GPT-5. You are running as a coding agent in the Codex CLI on a user's computer."
	if codexDefaultInstruction != wantLegacyIdentity {
		t.Fatalf("codexDefaultInstruction = %q, want the literal legacy identity prompt", codexDefaultInstruction)
	}

	call := &aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("List the files in this repository.")},
		}},
		Tools: []aipapi.ToolDef{{Name: "bash"}},
		Extensions: map[string]json.RawMessage{
			extAgentKey: json.RawMessage(`"opencode/1.2.26"`),
		},
	}

	ApplyCompat(call)

	instructions := joinInstructionText(call.Instructions)
	if !strings.HasPrefix(instructions, wantLegacyIdentity) {
		t.Fatalf("instructions do not start with the legacy Codex identity: %q", instructions)
	}
	if !strings.Contains(instructions, openCodeBridgeMarker) {
		t.Fatalf("instructions do not carry the selected bridge: %q", instructions)
	}
	if len(call.Messages) == 0 || !strings.Contains(messageText(call.Messages[0]), openCodeBridgeMarker) {
		t.Fatalf("expected the bridge message first: %#v", call.Messages)
	}
}

// TestCollectAgentCandidates_readsCompatibilityHeadersExtension pins the
// "headers" extension input path. No in-tree frontend writes this extension;
// this test characterizes the feature's accepted input, not a production
// producer. The legacy Codex connector reads the same shape for continuation
// partitioning.
func TestCollectAgentCandidates_readsCompatibilityHeadersExtension(t *testing.T) {
	t.Parallel()

	for _, header := range []string{"user-agent", "User-Agent"} {
		t.Run(header, func(t *testing.T) {
			t.Parallel()

			headers, err := json.Marshal(map[string]string{header: "opencode/1.2.26"})
			if err != nil {
				t.Fatal(err)
			}
			in := detectCompatInput(&aipapi.Call{
				Extensions: map[string]json.RawMessage{extHeadersKey: headers},
			})
			if !openCodeAgentMatch(in) {
				t.Fatalf("expected OpenCode detection from the %q compatibility header", header)
			}
		})
	}
}

// TestApplyCompat_piDroidAndHermesBridgesRequireTools pins the no-tools guard:
// these three bridges are built only when the request exposes tools, while
// OpenCode intentionally keeps a no-tools textual-tool-call guard. Harness
// system-prompt filtering still runs first, and Hermes still receives its
// legacy tool_strict extension before the guard returns.
func TestApplyCompat_piDroidAndHermesBridgesRequireTools(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                   string
		agent                  string
		harnessPrompt          string
		marker                 string
		preservesHarnessPrompt bool
		wantToolStrict         bool
	}{
		{
			name:          "Pi",
			agent:         "@mariozechner/pi-coding-agent/0.55.3",
			harnessPrompt: "You are an expert coding assistant operating inside pi, a coding agent harness.\nAvailable tools:\n- bash: Execute bash commands\nGuidelines:\nbe concise",
			marker:        piBridgeMarker,
		},
		{
			name:          "Factory Droid",
			agent:         "factory-cli/1.2.3",
			harnessPrompt: "You are Factory Droid. Use Execute and TodoWrite for tasks.",
			marker:        droidBridgeMarker,
		},
		{
			name:                   "Hermes",
			agent:                  "hermes-agent/1.0",
			harnessPrompt:          hermesIdentitySentence,
			marker:                 hermesBridgeMarker,
			preservesHarnessPrompt: true,
			wantToolStrict:         true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			agent, err := json.Marshal(tc.agent)
			if err != nil {
				t.Fatal(err)
			}
			call := &aipapi.Call{
				Instructions: []aipapi.Message{{
					Role:  aipapi.RoleSystem,
					Parts: []aipapi.Part{aipapi.TextPart("Base instructions")},
				}},
				Messages: []aipapi.Message{
					{
						Role:  aipapi.RoleSystem,
						Parts: []aipapi.Part{aipapi.TextPart(tc.harnessPrompt)},
					},
					{
						Role:  aipapi.RoleUser,
						Parts: []aipapi.Part{aipapi.TextPart("hello")},
					},
				},
				Extensions: map[string]json.RawMessage{extAgentKey: agent},
			}

			ApplyCompat(call)

			if strings.Contains(joinInstructionText(call.Instructions), tc.marker) {
				t.Fatalf("no-tools request received the %s bridge in instructions", tc.name)
			}
			if strings.Contains(joinInstructionText(call.Messages), tc.marker) {
				t.Fatalf("no-tools request received the %s bridge message", tc.name)
			}
			if got := strings.Contains(joinInstructionText(call.Messages), tc.harnessPrompt); got != tc.preservesHarnessPrompt {
				t.Fatalf("harness prompt preserved = %t, want %t: %#v", got, tc.preservesHarnessPrompt, call.Messages)
			}
			if tc.wantToolStrict {
				raw, ok := call.Extensions[extCodexToolStrictKey]
				if !ok || string(raw) != "false" {
					t.Fatalf("%s tool_strict = %s, want false", tc.name, raw)
				}
			}
		})
	}
}
