package codexclientcompat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestRequestPartHook_targetsConfiguredInstanceNameNotConnectorKind characterizes
// the gate's only input. Core sets sdk.PartMeta.BackendID from the selected
// route's configured backend instance id (c.Primary.Backend), never from the
// backend factory kind, and sdk.PartMeta carries no kind at all. This package
// therefore cannot tell the legacy openai-codex connector apart from any other
// connector configured under the same instance id.
//
// That is the migration hazard. Task 9.1 proves the new connector serves
// harness-shaped requests with this feature disabled; it does not prove a
// kind-level exemption. An operator who keeps the "openai-codex" instance id
// while swapping the factory kind to openai-chatgpt-plan is still targeted. The
// positive case below is the hook-boundary shape of that configuration, not an
// exemption claim.
func TestRequestPartHook_targetsConfiguredInstanceNameNotConnectorKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		backendID  string
		wantBridge bool
	}{
		{
			name:       "configured instance id openai-codex is targeted",
			backendID:  targetBackendID,
			wantBridge: true,
		},
		{
			name:       "a different configured instance id openai-chatgpt-plan is not targeted",
			backendID:  "openai-chatgpt-plan",
			wantBridge: false,
		},
		{
			name:       "task 9.1 baseline instance id is not targeted",
			backendID:  "plan-harness",
			wantBridge: false,
		},
		{
			name:       "renamed instance id codex-primary is not targeted",
			backendID:  "codex-primary",
			wantBridge: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			call := &aipapi.Call{
				Instructions: []aipapi.Message{{
					Role:  aipapi.RoleSystem,
					Parts: []aipapi.Part{aipapi.TextPart("Base instructions")},
				}},
				Messages: []aipapi.Message{{
					Role:  aipapi.RoleUser,
					Parts: []aipapi.Part{aipapi.TextPart("List the files in this repository.")},
				}},
				Tools: []aipapi.ToolDef{{Name: "bash"}},
				Extensions: map[string]json.RawMessage{
					extAgentKey: json.RawMessage(`"opencode/1.2.26"`),
				},
			}
			before, err := json.Marshal(call)
			if err != nil {
				t.Fatal(err)
			}

			runHook(t, call, tc.backendID)

			after, err := json.Marshal(call)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.wantBridge {
				if !bytes.Equal(before, after) {
					t.Fatalf("hook mutated the non-targeted instance %q: before=%s after=%s", tc.backendID, before, after)
				}
				return
			}
			instructions := joinInstructionText(call.Instructions)
			if !strings.Contains(instructions, openCodeBridgeMarker) {
				t.Fatalf("targeted instance %q did not receive the OpenCode bridge: %q", tc.backendID, instructions)
			}
			if len(call.Messages) == 0 || !strings.Contains(messageText(call.Messages[0]), openCodeBridgeMarker) {
				t.Fatalf("targeted instance %q did not receive the bridge message first: %#v", tc.backendID, call.Messages)
			}
			raw, ok := call.Extensions[CodexIgnoreUnsupportedGenParamsExt]
			if !ok || string(raw) != "true" {
				t.Fatalf("targeted instance %q did not receive %s=true: %s", tc.backendID, CodexIgnoreUnsupportedGenParamsExt, raw)
			}
		})
	}
}
