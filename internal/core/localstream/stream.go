package localstream

import (
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// NewTextStream returns a finite canonical EventStream with exactly one
// assistant text response. The sequence is response_started, message_started,
// text_delta, response_finished. It emits no usage, no B-leg identity, no
// background goroutine, and respects context cancellation via
// aipapi.FixedEventStream. The stream is valid for both streaming and
// non-streaming collection via aipapi.Collect.
func NewTextStream(text string) aipapi.EventStream {
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: text},
		{Kind: aipapi.EventResponseFinished},
	}
	return aipapi.NewFixedEventStream(events)
}

// Events returns the canonical event slice for a text response without
// allocating a stream. Useful for ValidateEventSequence testing.
func Events(text string) []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: text},
		{Kind: aipapi.EventResponseFinished},
	}
}

// CanonicalAssistantMessage returns the canonical assistant message whose
// semantic identity must equal the tagged reply identity. It is the same
// construction used by runtime local-turn reply tagging so that replay
// identity is stable across frontends.
func CanonicalAssistantMessage(text string) aipapi.Message {
	return aipapi.Message{
		Role:  aipapi.RoleAssistant,
		Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: text}},
	}
}

// CanonicalAssistantItem returns the item-authority equivalent of
// CanonicalAssistantMessage. It is used for materialized-history assertions
// where the continuation store uses Items.
func CanonicalAssistantItem(text string) aipapi.Item {
	return aipapi.Item{
		Kind:   aipapi.ItemKindMessage,
		Role:   aipapi.RoleAssistant,
		Status: aipapi.ItemStatusCompleted,
		Content: []aipapi.ContentPart{
			{Kind: aipapi.ContentPartText, Text: text},
		},
	}
}
