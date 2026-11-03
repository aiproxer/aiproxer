package openailegacy

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestUserOrSystemChatMessage_InlineSystemKeepsRoleAndText pins the mapping for
// a system message carried inside canonical history: Chat Completions has a
// native system role, so the text reaches the wire as a system message rather
// than being coerced to user or hoisted into the leading instructions turn.
func TestUserOrSystemChatMessage_InlineSystemKeepsRoleAndText(t *testing.T) {
	t.Parallel()

	u, err := userOrSystemChatMessage(aipapi.Message{
		Role:  aipapi.RoleSystem,
		Parts: []aipapi.Part{aipapi.TextPart("mid-system")},
	})
	if err != nil {
		t.Fatalf("inline system message must be encodable: %v", err)
	}
	if u.OfSystem == nil {
		t.Fatalf("inline system message mapped to %+v, want an OpenAI system message", u)
	}
	if got := u.OfSystem.Content.OfString.Value; got != "mid-system" {
		t.Fatalf("system text %q, want %q", got, "mid-system")
	}
	if u.OfUser != nil || u.OfAssistant != nil || u.OfDeveloper != nil {
		t.Fatalf("inline system message also populated a non-system variant: %+v", u)
	}
}

// TestBuildChatMessages_InlineSystemKeepsHistoryPosition pins that the mapped
// system message keeps its ordered position among the trajectory turns; only
// top-level canonical instructions form the leading system turn.
func TestBuildChatMessages_InlineSystemKeepsHistoryPosition(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("top-level")}}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("U1")}},
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("mid-system")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("A1")}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("U2")}},
		},
	}

	msgs, err := buildChatMessages(&call)
	if err != nil {
		t.Fatalf("buildChatMessages: %v", err)
	}
	want := []struct{ role, text string }{
		{"system", "top-level"},
		{"user", "U1"},
		{"system", "mid-system"},
		{"assistant", "A1"},
		{"user", "U2"},
	}
	if len(msgs) != len(want) {
		t.Fatalf("wire messages len %d, want %d", len(msgs), len(want))
	}
	for i, exp := range want {
		var gotRole, gotText string
		switch {
		case msgs[i].OfSystem != nil:
			gotRole, gotText = "system", msgs[i].OfSystem.Content.OfString.Value
		case msgs[i].OfUser != nil:
			gotRole, gotText = "user", msgs[i].OfUser.Content.OfString.Value
		case msgs[i].OfAssistant != nil:
			gotRole, gotText = "assistant", msgs[i].OfAssistant.Content.OfString.Value
		default:
			t.Fatalf("message %d has no supported variant: %+v", i, msgs[i])
		}
		if gotRole != exp.role || gotText != exp.text {
			t.Fatalf("message %d is %s %q, want %s %q", i, gotRole, gotText, exp.role, exp.text)
		}
	}
}
