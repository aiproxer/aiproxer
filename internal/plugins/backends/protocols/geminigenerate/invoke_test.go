package geminigenerate_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/geminigenerate"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestStreamParamsForCall_textOnly(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "t1",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "gemini", Model: "gemini-2.0-flash"},
	}
	sp, err := geminigenerate.StreamParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Model != "gemini-2.0-flash" {
		t.Fatalf("model: %s", sp.Model)
	}
	if len(sp.Contents) != 1 || sp.Contents[0].Parts[0].Text != "hello" {
		t.Fatalf("contents: %+v", sp.Contents)
	}
}

func TestStreamParamsForCall_validateRejectsInvalidCall(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{ID: "bad", Messages: nil}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	_, err := geminigenerate.StreamParamsForCall(&call, cand)
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestStreamParamsForCall_modelFromExtensions(t *testing.T) {
	t.Parallel()
	rawModel, _ := json.Marshal("gemini-2.0-flash")
	call := aipapi.Call{
		ID: "t2",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("x")},
		}},
		Extensions: map[string]json.RawMessage{"gemini.model": rawModel},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "gemini"}}
	sp, err := geminigenerate.StreamParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Model != "gemini-2.0-flash" {
		t.Fatalf("model: %s", sp.Model)
	}
}

func TestStreamParamsForCall_systemInstruction(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "t3",
		Instructions: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("Be brief.")},
		}},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	sp, err := geminigenerate.StreamParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Config.SystemInstruction == nil || sp.Config.SystemInstruction.Parts[0].Text != "Be brief." {
		t.Fatalf("system: %+v", sp.Config.SystemInstruction)
	}
}

func TestStreamParamsForCall_maxOutputTokensInt32Bound(t *testing.T) {
	t.Parallel()
	maxTok := math.MaxInt32
	call := aipapi.Call{
		ID: "mt",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Options: aipapi.GenerationOptions{MaxOutputTokens: &maxTok},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	sp, err := geminigenerate.StreamParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Config.MaxOutputTokens != math.MaxInt32 {
		t.Fatalf("MaxOutputTokens: %d", sp.Config.MaxOutputTokens)
	}
}

func TestStreamParamsForCall_toolsAndChoice(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "t4",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("x")},
		}},
		Tools: []aipapi.ToolDef{{
			Name:        "do_thing",
			Description: "d",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "do_thing"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	sp, err := geminigenerate.StreamParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(sp.Config)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "do_thing") {
		t.Fatalf("expected tool name in config: %s", s)
	}
	if sp.Config.ToolConfig == nil || sp.Config.ToolConfig.FunctionCallingConfig == nil {
		t.Fatal("missing toolConfig")
	}
	if len(sp.Config.ToolConfig.FunctionCallingConfig.AllowedFunctionNames) != 1 ||
		sp.Config.ToolConfig.FunctionCallingConfig.AllowedFunctionNames[0] != "do_thing" {
		t.Fatalf("allowed names: %+v", sp.Config.ToolConfig.FunctionCallingConfig.AllowedFunctionNames)
	}
}

// TestStreamParamsForCall_developerRoleCoercedToUserKeepingPosition pins the
// modeled downgrade for the canonical developer role on the Gemini wire, whose
// Content.role is a closed user|model enum with no developer value.
//
// aipapi.RoleDeveloper carries proxy-owned continuation steering text
// (internal/core/runtime/terminal_decision_continuation.go places it at
// AfterIngressTail). Because the enum has no room for it, the message is
// deliberately coerced to a user turn rather than rejected: the loss is that the
// provider reads proxy-owned instruction text as a client utterance, and the
// canonical role survives only in the proxy's own record, never on the wire.
//
// The downgrade is bounded on purpose. The steering message is never hoisted into
// systemInstruction: that is the top-level instruction slot, and hoisting a
// mid-conversation message there would destroy the trajectory ordering the
// generic runtime depends on. The message therefore keeps its ordered position
// in Contents, which this test pins together with the exact coerced wire role.
func TestStreamParamsForCall_developerRoleCoercedToUserKeepingPosition(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "dev-role",
		Instructions: []aipapi.Message{{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart("stable instruction")},
		}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("U1")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("A1")}},
			{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{aipapi.TextPart("STEER-NOW")}},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "gemini", Model: "gemini-2.0-flash"}}
	sp, err := geminigenerate.StreamParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("canonical developer role must be encodable on the Gemini wire, got error: %v", err)
	}

	// The coerced wire role, pinned exactly: no developer role reaches the wire.
	wantRoles := []string{"user", "model", "user"}
	if len(sp.Contents) != len(wantRoles) {
		t.Fatalf("wire contents len %d, want %d (steering must keep its own content)", len(sp.Contents), len(wantRoles))
	}
	for i, want := range wantRoles {
		if got := string(sp.Contents[i].Role); got != want {
			t.Fatalf("wire content %d role %q, want %q", i, got, want)
		}
	}

	// Position preservation: the steering text is still the last trajectory turn.
	last := sp.Contents[len(sp.Contents)-1]
	if len(last.Parts) != 1 || last.Parts[0].Text != "STEER-NOW" {
		t.Fatalf("steering text not preserved at its ordered position: %+v", last)
	}

	// Position preservation, negatively: steering text must not be hoisted into systemInstruction.
	if sp.Config.SystemInstruction == nil {
		t.Fatal("system instruction nil")
	}
	for i, part := range sp.Config.SystemInstruction.Parts {
		if strings.Contains(part.Text, "STEER-NOW") {
			t.Fatalf("systemInstruction part %d hoisted mid-conversation steering text: %q", i, part.Text)
		}
	}
	if len(sp.Config.SystemInstruction.Parts) != 1 || sp.Config.SystemInstruction.Parts[0].Text != "stable instruction" {
		t.Fatalf("systemInstruction must stay top-level instructions only, got %+v", sp.Config.SystemInstruction.Parts)
	}
}

// TestStreamParamsForCall_everyCanonicalRoleMapsToItsWireRole pins the complete
// canonical-role translation on this adapter's wire, one canonical message per
// role, because the role vocabulary is Gemini's provider contract rather than a
// canonical one. Every previously mapped role keeps its exact wire role, and the
// default arm keeps rejecting what it rejected.
func TestStreamParamsForCall_everyCanonicalRoleMapsToItsWireRole(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		role      aipapi.Role
		parts     []aipapi.Part
		wantRole  string
		wantError string
	}{
		{
			name:     "user",
			role:     aipapi.RoleUser,
			parts:    []aipapi.Part{aipapi.TextPart("U")},
			wantRole: "user",
		},
		{
			name:     "assistant",
			role:     aipapi.RoleAssistant,
			parts:    []aipapi.Part{aipapi.TextPart("A")},
			wantRole: "model",
		},
		{
			name:     "developer",
			role:     aipapi.RoleDeveloper,
			parts:    []aipapi.Part{aipapi.TextPart("D")},
			wantRole: "user",
		},
		{
			name: "tool",
			role: aipapi.RoleTool,
			parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: "call_role_map",
				ToolName:   "get_weather",
				Content:    json.RawMessage(`{"ok":true}`),
			}},
			wantRole: "user",
		},
		{
			// The default arm is unchanged: a mid-conversation system message
			// still fails explicitly instead of being lifted or coerced.
			name:      "system mid-conversation still rejected",
			role:      aipapi.RoleSystem,
			parts:     []aipapi.Part{aipapi.TextPart("S")},
			wantError: `gemini: unsupported message role "system"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{
				ID:       "role-map-" + tc.name,
				Messages: []aipapi.Message{{Role: tc.role, Parts: tc.parts}},
			}
			cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "gemini", Model: "gemini-2.0-flash"}}
			sp, err := geminigenerate.StreamParamsForCall(&call, cand)
			if tc.wantError != "" {
				if err == nil {
					t.Fatalf("canonical role %q must still fail, got contents %+v", tc.role, sp.Contents)
				}
				if !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("canonical role %q error %q, want it to contain %q", tc.role, err.Error(), tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("canonical role %q must be encodable, got error: %v", tc.role, err)
			}
			if len(sp.Contents) != 1 {
				t.Fatalf("canonical role %q produced %d wire contents, want 1", tc.role, len(sp.Contents))
			}
			if got := string(sp.Contents[0].Role); got != tc.wantRole {
				t.Fatalf("canonical role %q mapped to wire role %q, want %q", tc.role, got, tc.wantRole)
			}
		})
	}
}

func TestStreamParamsForCall_toolResultMessage(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "tool-res",
		Messages: []aipapi.Message{
			{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("call the tool")},
			},
			{
				Role: aipapi.RoleTool,
				Parts: []aipapi.Part{{
					Kind:       aipapi.PartToolResult,
					ToolCallID: "call_gem_1",
					ToolName:   "get_weather",
					Content:    json.RawMessage(`{"ok":true}`),
				}},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	sp, err := geminigenerate.StreamParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(sp.Contents)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "functionResponse") || !strings.Contains(s, "get_weather") || !strings.Contains(s, "call_gem_1") {
		t.Fatalf("expected functionResponse tool result mapping, got: %s", s)
	}
}
