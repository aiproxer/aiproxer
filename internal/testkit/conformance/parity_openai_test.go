//go:build integration

package conformance

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// OpenAI family parity anchors (see .kiro/specs/archive/llm-api-parity/design.md rows OAR-*, OAC-*).
func TestParity_OpenAI_canonicalAssistantMediaCollects(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventAssistantImageRef, AssistantRef: "https://cdn.example/a.png", AssistantMIME: "image/png"},
		{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
	})
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "ok" || len(col.AssistantMedia) != 1 || col.FinishReason != "stop" {
		t.Fatalf("collected: text=%q media=%v finish=%q", col.Text.String(), col.AssistantMedia, col.FinishReason)
	}
}

func TestParity_OpenAI_matrixIncludesBothFrontends(t *testing.T) {
	t.Parallel()
	var sawResponses, sawLegacy bool
	for _, id := range BundledFrontendIDs() {
		switch id {
		case "openai-responses":
			sawResponses = true
		case "openai-legacy":
			sawLegacy = true
		}
	}
	if !sawResponses || !sawLegacy {
		t.Fatalf("bundled frontends: responses=%v legacy=%v", sawResponses, sawLegacy)
	}
}
