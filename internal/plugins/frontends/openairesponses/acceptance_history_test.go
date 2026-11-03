package openairesponses

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// #822: Responses clients replay the assistant output_text returned by this
// frontend. Rejecting that emitted wire shape breaks the next tool-loop turn.
func TestDecodeCreate_AssistantOutputTextReplay(t *testing.T) {
	d, err := DecodeCreateRequest([]byte(`{"model":"test:model","input":[{"role":"user","content":"question"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"role":"user","content":"next"}]}`), DecodeOptions{RouteSelector: "test:model"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 3 || d.Call.Messages[1].Role != aipapi.RoleAssistant || len(d.Call.Messages[1].Parts) != 1 || d.Call.Messages[1].Parts[0].Text != "answer" {
		t.Fatalf("history changed: %+v", d.Call.Messages)
	}
	if _, err := DecodeCreateRequest([]byte(`{"model":"test:model","input":[{"role":"user","content":[{"type":" output_text ","text":"not assistant output"}]}]}`), DecodeOptions{RouteSelector: "test:model"}); err == nil {
		t.Fatal("output_text accepted under a non-assistant role")
	}
}
