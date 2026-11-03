package anthropic

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Claim (#822): stock worker instructions remain in their original history
// position, without requiring the unrelated hybrid routing mode.
func TestDecodeMessage_InlineSystemPreservesHistory(t *testing.T) {
	d, err := DecodeMessageRequest([]byte(`{"model":"external:model","max_tokens":64,"system":"top-level instructions","messages":[{"role":"user","content":"question"},{"role":"assistant","content":"earlier answer"},{"role":"system","content":"worker instructions"},{"role":"user","content":"next question"}]}`), DecodeOptions{RouteSelector: "external:model"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Instructions) != 1 || d.Call.Instructions[0].Parts[0].Text != "top-level instructions" {
		t.Fatalf("instructions changed: %+v", d.Call.Instructions)
	}
	wantRoles := []aipapi.Role{aipapi.RoleUser, aipapi.RoleAssistant, aipapi.RoleSystem, aipapi.RoleUser}
	wantText := []string{"question", "earlier answer", "worker instructions", "next question"}
	if len(d.Call.Messages) != len(wantRoles) {
		t.Fatalf("history length: %d", len(d.Call.Messages))
	}
	for i, m := range d.Call.Messages {
		if m.Role != wantRoles[i] || len(m.Parts) != 1 || m.Parts[0].Text != wantText[i] {
			t.Fatalf("message %d changed: %+v", i, m)
		}
	}
}
