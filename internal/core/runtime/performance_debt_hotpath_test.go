package runtime

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestPendingPriorAssistantText_Empty(t *testing.T) {
	t.Parallel()
	if pendingPriorAssistantText("", nil) {
		t.Fatal("empty released and empty base must be false")
	}
	if pendingPriorAssistantText("   \n\t ", []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "   "}}) {
		t.Fatal("whitespace-only must be false")
	}
}

func TestPendingPriorAssistantText_Released(t *testing.T) {
	t.Parallel()
	if !pendingPriorAssistantText("  hello ", nil) {
		t.Fatal("non-empty released must be true")
	}
}

func TestPendingPriorAssistantText_HeldBase(t *testing.T) {
	t.Parallel()
	base := []aipapi.Event{
		{Kind: aipapi.EventReasoningDelta, Delta: "thinking"},
		{Kind: aipapi.EventTextDelta, Delta: "   "},
		{Kind: aipapi.EventTextDelta, Delta: "hi"},
	}
	if !pendingPriorAssistantText("", base) {
		t.Fatal("held text delta must be true")
	}
	nonText := []aipapi.Event{
		{Kind: aipapi.EventReasoningDelta, Delta: "thinking"},
	}
	if pendingPriorAssistantText("", nonText) {
		t.Fatal("reasoning-only must be false")
	}
}

func TestPendingPriorAssistantText_SplitWhitespaceAcrossDeltas(t *testing.T) {
	t.Parallel()
	// Whitespace split across deltas is still whitespace-only.
	base := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "  \n"},
		{Kind: aipapi.EventTextDelta, Delta: "\t  "},
	}
	if pendingPriorAssistantText("", base) {
		t.Fatal("split whitespace must be false")
	}
	if got := strings.TrimSpace("  \n" + "\t  "); got != "" {
		t.Fatal("sanity: expected whitespace-only fixture")
	}
}
