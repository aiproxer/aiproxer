package geminigenerate

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestMessageToContent_RejectsInlineSystemRole pins the adapter boundary for a
// system message carried inside canonical history. Gemini's Content.role is a
// closed user|model enum, and the ordinary route must reject before upstream
// I/O instead of lifting the text into systemInstruction, which would reorder
// the conversation.
func TestMessageToContent_RejectsInlineSystemRole(t *testing.T) {
	t.Parallel()

	got, err := messageToContent(aipapi.Message{
		Role:  aipapi.RoleSystem,
		Parts: []aipapi.Part{aipapi.TextPart("mid-system")},
	})
	if err == nil {
		t.Fatalf("messageToContent accepted an inline system message: %+v", got)
	}
	if want := `gemini: unsupported message role "system"`; err.Error() != want {
		t.Fatalf("error %q, want %q", err, want)
	}
	if strings.Contains(err.Error(), "mid-system") {
		t.Fatalf("error must not leak message text: %v", err)
	}
}
