package anthropicmessages_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/anthropicmessages"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestParamsForCall_textOnly(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "t1",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
		Route: aipapi.RouteIntent{Selector: "anthropic:claude-3-5-haiku-20241022"},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "anthropic", Model: "claude-3-5-haiku-20241022"},
		Key:     "anthropic:claude-3-5-haiku-20241022",
	}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Model) != "claude-3-5-haiku-20241022" {
		t.Fatalf("model: %s", p.Model)
	}
	if p.MaxTokens != 4096 {
		t.Fatalf("default max_tokens: %d", p.MaxTokens)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"model":"claude-3-5-haiku-20241022"`) || !strings.Contains(s, `"max_tokens":4096`) {
		t.Fatalf("marshaled params: %s", s)
	}
}

func TestParamsForCall_modelFromExtensions(t *testing.T) {
	t.Parallel()
	rawModel, _ := json.Marshal("claude-3-5-haiku-20241022")
	call := aipapi.Call{
		ID: "t2",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("x")},
		}},
		Extensions: map[string]json.RawMessage{"anthropic.model": rawModel},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Model) != "claude-3-5-haiku-20241022" {
		t.Fatalf("model: %s", p.Model)
	}
}

func TestParamsForCall_multimodalParts(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "t3",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart("describe"),
				{Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64,AAA"},
				aipapi.FilePart("data:application/pdf;base64,QUFB", "application/pdf", "minimal.pdf"),
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"type":"image"`) || !strings.Contains(s, `"type":"document"`) {
		t.Fatalf("expected multimodal markers, got: %s", s)
	}
}

func TestParamsForCall_emptyTextPartSkipped(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "empty-text",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart(""),
				{Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64,AAA"},
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("empty text part should be skipped, not error: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, `"text":""`) {
		t.Fatalf("empty text part should not appear as empty text block: %s", s)
	}
	if !strings.Contains(s, `"type":"image"`) {
		t.Fatalf("image part should be present: %s", s)
	}
}

func TestParamsForCall_fileRefNonDataURL_rejected(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "bad-file-ref",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart("x"),
				aipapi.FilePart("https://example.com/file.pdf", "application/pdf", "file.pdf"),
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	_, err := anthropicmessages.ParamsForCall(&call, cand)
	if err == nil {
		t.Fatal("expected error for non-data URL file ref")
	}
	if !strings.Contains(err.Error(), "data URL") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParamsForCall_toolsAndParallel(t *testing.T) {
	t.Parallel()
	parallel := false
	call := aipapi.Call{
		ID: "tools",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("use a tool")},
		}},
		Tools: []aipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Weather lookup",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Options:    aipapi.GenerationOptions{ParallelToolCalls: &parallel},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"name":"get_weather"`) {
		t.Fatalf("expected tool name: %s", s)
	}
	if !strings.Contains(s, `"disable_parallel_tool_use":true`) {
		t.Fatalf("expected disable_parallel_tool_use when ParallelToolCalls is false: %s", s)
	}
	_ = p
}

func TestParamsForCall_systemInstructions(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "sys",
		Instructions: []aipapi.Message{{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart("You are concise.")},
		}},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "You are concise.") {
		t.Fatalf("system prompt missing: %s", raw)
	}
}

// TestParamsForCall_developerRoleCoercedToUserKeepingPosition pins the modeled
// downgrade for the canonical developer role on the Anthropic wire, whose
// Messages role vocabulary this adapter maps as user|assistant only.
//
// aipapi.RoleDeveloper carries proxy-owned continuation steering text
// (internal/core/runtime/terminal_decision_continuation.go places it at
// AfterIngressTail). Anthropic has no developer message role, so the message is
// deliberately coerced to a user turn rather than rejected: the loss is that the
// provider reads proxy-owned instruction text as a client utterance, and the
// canonical role survives only in the proxy's own record, never on the wire.
//
// The downgrade is bounded on purpose. The steering message is never hoisted into
// System: a mid-conversation system block is model-gated, placement-constrained,
// and would destroy the trajectory ordering the generic runtime depends on. The
// message therefore keeps its ordered position in Messages, which this test pins
// together with the exact coerced wire role.
func TestParamsForCall_developerRoleCoercedToUserKeepingPosition(t *testing.T) {
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
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("canonical developer role must be encodable on the Anthropic wire, got error: %v", err)
	}

	// The coerced wire role, pinned exactly: no developer role reaches the wire.
	wantRoles := []string{"user", "assistant", "user"}
	if len(p.Messages) != len(wantRoles) {
		t.Fatalf("wire messages len %d, want %d (steering must keep its own turn)", len(p.Messages), len(wantRoles))
	}
	for i, want := range wantRoles {
		if got := string(p.Messages[i].Role); got != want {
			t.Fatalf("wire message %d role %q, want %q", i, got, want)
		}
	}

	// Position preservation: the steering text is still the last trajectory turn.
	last := p.Messages[len(p.Messages)-1]
	if len(last.Content) != 1 || last.Content[0].OfText == nil || last.Content[0].OfText.Text != "STEER-NOW" {
		t.Fatalf("steering text not preserved at its ordered position: %+v", last)
	}

	// Position preservation, negatively: steering text must not be hoisted into System.
	for i, block := range p.System {
		if strings.Contains(block.Text, "STEER-NOW") {
			t.Fatalf("system block %d hoisted mid-conversation steering text: %q", i, block.Text)
		}
	}
	if len(p.System) != 1 || p.System[0].Text != "stable instruction" {
		t.Fatalf("system blocks must stay top-level instructions only, got %+v", p.System)
	}
}

// TestParamsForCall_everyCanonicalRoleMapsToItsWireRole pins the complete
// canonical-role translation on this adapter's wire, one canonical message per
// role, because the role vocabulary is Anthropic's provider contract rather than
// a canonical one. Every previously mapped role keeps its exact wire role, and
// the default arm keeps rejecting what it rejected.
func TestParamsForCall_everyCanonicalRoleMapsToItsWireRole(t *testing.T) {
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
			wantRole: "assistant",
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
				ToolName:   "bash",
				Text:       "ok",
			}},
			wantRole: "user",
		},
		{
			// The default arm is unchanged: a mid-conversation system message
			// still fails explicitly instead of being lifted or coerced.
			name:      "system mid-conversation still rejected",
			role:      aipapi.RoleSystem,
			parts:     []aipapi.Part{aipapi.TextPart("S")},
			wantError: `anthropic: unsupported message role "system"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{
				ID:       "role-map-" + tc.name,
				Messages: []aipapi.Message{{Role: tc.role, Parts: tc.parts}},
			}
			cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
			p, err := anthropicmessages.ParamsForCall(&call, cand)
			if tc.wantError != "" {
				if err == nil {
					t.Fatalf("canonical role %q must still fail, got params %+v", tc.role, p)
				}
				if !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("canonical role %q error %q, want it to contain %q", tc.role, err.Error(), tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("canonical role %q must be encodable, got error: %v", tc.role, err)
			}
			if len(p.Messages) != 1 {
				t.Fatalf("canonical role %q produced %d wire messages, want 1", tc.role, len(p.Messages))
			}
			if got := string(p.Messages[0].Role); got != tc.wantRole {
				t.Fatalf("canonical role %q mapped to wire role %q, want %q", tc.role, got, tc.wantRole)
			}
		})
	}
}

func TestUpstreamError_returnsAPIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`))
	}))
	t.Cleanup(srv.Close)

	cli := anthropic.NewClient(
		option.WithBaseURL(srv.URL),
		option.WithAPIKey(testkit.SyntheticAnthropicAPIKey),
	)
	_, err := cli.Messages.New(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 8,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("x")),
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *anthropic.Error, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", apiErr.StatusCode)
	}
}

func TestParamsForCall_mergesMultipleToolResults(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "multi-tool",
		Messages: []aipapi.Message{
			{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("run commands")},
			},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartJSON, ToolCallID: "call_1", ToolName: "bash", Content: json.RawMessage(`{"command":"git status"}`)},
					{Kind: aipapi.PartJSON, ToolCallID: "call_2", ToolName: "bash", Content: json.RawMessage(`{"command":"git diff"}`)},
				},
			},
			{
				Role:  aipapi.RoleTool,
				Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call_1", Text: "clean"}},
			},
			{
				Role:  aipapi.RoleTool,
				Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call_2", Text: "diff clean"}},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "qwen3.8-max"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Messages) != 3 {
		t.Fatalf("expected 3 messages after merging consecutive tool results, got %d", len(p.Messages))
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"tool_use_id":"call_1"`) || !strings.Contains(s, `"tool_use_id":"call_2"`) {
		t.Fatalf("marshaled params missing tool_use_ids: %s", s)
	}
}
