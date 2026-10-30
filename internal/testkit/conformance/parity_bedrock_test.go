//go:build integration

package conformance

import (
	"context"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestParity_Bedrock_bundledBackend(t *testing.T) {
	t.Parallel()
	if !slices.Contains(BundledBackendIDs(), "bedrock") {
		t.Fatal("expected bedrock in BundledBackendIDs")
	}
}

func TestParity_Bedrock_canonicalAssistantMediaCollects(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventAssistantImageRef, AssistantRef: "https://cdn.example.com/x.png", AssistantMIME: "image/png"},
		{Kind: aipapi.EventResponseFinished},
	})
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.AssistantMedia) != 1 || col.AssistantMedia[0].ImageRef != "https://cdn.example.com/x.png" {
		t.Fatalf("collected: %#v", col.AssistantMedia)
	}
}
