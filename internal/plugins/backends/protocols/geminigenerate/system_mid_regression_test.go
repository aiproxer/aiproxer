package geminigenerate_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/geminigenerate"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestSystemInMessages_ExplicitlyRejected(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("stable instruction")}}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("U1")}},
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("mid-system")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("A1")}},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "gemini", Model: "gemini-2.0-flash"}}
	_, err := geminigenerate.StreamParamsForCall(&call, cand)
	if err == nil {
		t.Fatal("expected error for mid-conversation system, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unsupported") {
		t.Fatalf("expected unsupported error, got %v", err)
	}
	if strings.Contains(err.Error(), "mid-system") {
		t.Fatalf("error must not leak plaintext: %v", err)
	}
}

func TestSystemInInstructions_StillMapsToSystemInstruction(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Instructions: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("stable instruction")}},
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("steering stable")}},
		},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("U1")}}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "gemini", Model: "gemini-2.0-flash"}}
	sp, err := geminigenerate.StreamParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sp.Config.SystemInstruction == nil {
		t.Fatal("system instruction nil")
	}
	txt := sp.Config.SystemInstruction.Parts[0].Text
	if !strings.Contains(txt, "stable instruction") || !strings.Contains(txt, "steering stable") {
		t.Fatalf("system instruction missing: %q", txt)
	}
	if len(sp.Contents) != 1 {
		t.Fatalf("contents len %d", len(sp.Contents))
	}
}
