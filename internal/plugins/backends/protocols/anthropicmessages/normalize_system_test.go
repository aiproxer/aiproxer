package anthropicmessages

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNormalizeRoles_SystemMidLiftedToSystem(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("stable instruction")}}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("U1")}},
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("mid-system")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("A1")}},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "alibaba-token-plan-intl", Model: "qwen3.8-max-preview"}}
	p, err := paramsForCall(&call, cand, true, nil)
	if err != nil {
		t.Fatalf("normalize true should not error for mid-system, got %v", err)
	}
	if len(p.System) != 2 {
		t.Fatalf("system blocks len %d, want 2 (instruction + lifted mid-system)", len(p.System))
	}
	joined := p.System[0].Text + " " + p.System[1].Text
	if !strings.Contains(joined, "stable instruction") || !strings.Contains(joined, "mid-system") {
		t.Fatalf("system should contain both stable and mid-system when NormalizeRoles=true: %q %q", p.System[0].Text, p.System[1].Text)
	}
	// With NormalizeRoles=true, plain assistant text is coerced to user and merged with preceding user,
	// so U1 and A1 become a single merged user message after lifting system.
	if len(p.Messages) != 1 {
		t.Fatalf("messages len %d, want 1 merged user (U1+A1) after lifting system with NormalizeRoles=true", len(p.Messages))
	}
	if len(p.Messages[0].Content) != 2 {
		t.Fatalf("merged message content blocks %d, want 2", len(p.Messages[0].Content))
	}
	// Ensure plain mode still rejects
	_, err = paramsForCall(&call, cand, false, nil)
	if err == nil {
		t.Fatal("normalize false should explicitly reject mid-system")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unsupported") {
		t.Fatalf("expected unsupported, got %v", err)
	}
	if strings.Contains(err.Error(), "mid-system") {
		t.Fatalf("error must not leak plaintext: %v", err)
	}
}
