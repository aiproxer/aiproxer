//go:build integration

package conformance

import (
	"context"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestParity_Gemini_bundledFrontend(t *testing.T) {
	t.Parallel()
	if !slices.Contains(BundledFrontendIDs(), "gemini") {
		t.Fatal("expected gemini in BundledFrontendIDs")
	}
}

func TestParity_Gemini_canonicalAssistantMediaCollects(t *testing.T) {
	t.Parallel()
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventAssistantImageRef, AssistantRef: "gs://bucket/x.png", AssistantMIME: "image/png"},
		{Kind: aipapi.EventResponseFinished},
	})
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.AssistantMedia) != 1 || col.AssistantMedia[0].ImageRef != "gs://bucket/x.png" {
		t.Fatalf("collected media: %#v", col.AssistantMedia)
	}
}
