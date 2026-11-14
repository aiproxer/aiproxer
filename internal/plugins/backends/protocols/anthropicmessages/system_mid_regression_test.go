package anthropicmessages_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/anthropicmessages"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Claim (#793): a default (non-bare) Claude Code turn keeps its mid-history
// environment message as a system-role message in its original position, so the
// turn reaches the provider instead of being refused at backend open.
// Mutant: revert the RoleSystem arm of messageToParam to the default refusal.
func TestSystemInMessages_KeepsRoleAndPosition(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("stable instruction")}}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("U1")}},
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("mid-system")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("A1")}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("U2")}},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic", Model: "claude-sonnet-5-5"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("mid-conversation system must be encodable on the Anthropic wire, got: %v", err)
	}
	wantRoles := []string{"user", "system", "assistant", "user"}
	if len(p.Messages) != len(wantRoles) {
		t.Fatalf("wire messages len %d, want %d (history must keep every turn)", len(p.Messages), len(wantRoles))
	}
	for i, want := range wantRoles {
		if got := string(p.Messages[i].Role); got != want {
			t.Fatalf("wire message %d role %q, want %q", i, got, want)
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"role":"system"`) || !strings.Contains(s, `"text":"mid-system"`) {
		t.Fatalf("system block missing from in-place wire position: %s", s)
	}
	// The system text must not be lifted into top-level System: that would
	// change its authority and scope from a point in history to the whole turn.
	if len(p.System) != 1 || p.System[0].Text != "stable instruction" {
		t.Fatalf("system blocks must stay top-level instructions only, got %+v", p.System)
	}
}

// Claim (#793): a mid-history system message that violates the vendor placement
// rules is refused before upstream I/O instead of emitting a body the provider
// answers with 400.
// Mutant: drop validateSystemMessagePlacement from the message builder.
func TestSystemInMessages_PlacementViolationsRefused(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		messages []aipapi.Message
		wantErr  string
	}{
		{
			name: "system as the only turn",
			messages: []aipapi.Message{
				{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("S")}},
			},
			wantErr: "cannot be the first message",
		},
		{
			name: "system right after an assistant turn",
			messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("U1")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("A1")}},
				{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("S")}},
			},
			wantErr: "must follow a user message",
		},
		{
			name: "system followed by a user turn",
			messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("U1")}},
				{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("S")}},
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("U2")}},
			},
			wantErr: "must be followed by an assistant turn or end the request",
		},
		{
			name: "system carrying a non-text part",
			messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("U1")}},
				{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("S"), {Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64,AAA"}}},
			},
			wantErr: "system message supports text parts only",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{Messages: tc.messages}
			cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic", Model: "claude-sonnet-5-5"}}
			_, err := anthropicmessages.ParamsForCall(&call, cand)
			if err == nil {
				t.Fatal("expected refusal")
			}
			if !errors.Is(err, aipapi.ErrCapabilityReject) {
				t.Fatalf("error %v must classify as a capability reject", err)
			}
			var rej *aipapi.RejectError
			if !errors.As(err, &rej) || strings.TrimSpace(rej.Reason) == "" {
				t.Fatalf("error %v must carry a nonempty RejectError reason", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q, want it to contain %q", err.Error(), tc.wantErr)
			}
			if strings.Contains(err.Error(), `"S"`) {
				t.Fatalf("error must not leak plaintext: %v", err)
			}
		})
	}
}

func TestSystemInInstructions_StillMapsToSystemBlocks(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Instructions: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("stable instruction")}},
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("steering stable")}},
		},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("U1")}}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic", Model: "claude-3-5-sonnet"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.System) != 1 {
		t.Fatalf("system blocks len %d", len(p.System))
	}
	if !strings.Contains(p.System[0].Text, "stable instruction") || !strings.Contains(p.System[0].Text, "steering stable") {
		t.Fatalf("system missing expected texts: %q", p.System[0].Text)
	}
	if len(p.Messages) != 1 {
		t.Fatalf("messages len %d", len(p.Messages))
	}
}
