package openairesponses

import (
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaiusage"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/openairesponsestream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3/responses"
)

// CompletionEvents converts a non-streaming Responses API payload into canonical events.
func CompletionEvents(resp responses.Response) ([]aipapi.Event, error) {
	pending := stream.NewPendingEventQueue(0)
	mapper := openairesponsestream.New(&pending)
	cur := responses.ResponseStreamEventUnion{Type: "response.completed", Response: resp}
	if err := handleCompletedUnion(mapper, cur); err != nil {
		return nil, fmt.Errorf("openai-responses: completion events: %w", err)
	}
	return stream.DrainPending(&pending), nil
}

func handleCompletedUnion(m *openairesponsestream.Mapper, cur responses.ResponseStreamEventUnion) error {
	resp := cur.Response
	if err := m.BeginCompleted(); err != nil {
		return err
	}
	if err := m.EmitCompletedOutputByIndex(resp); err != nil {
		return err
	}
	if resp.JSON.Usage.Valid() {
		ev := openaiusage.ResponsesUsageEvent(resp.Usage)
		openaiusage.AnnotateProviderContext(&ev, resp.ID, string(resp.ServiceTier))
		if err := m.PushUsage(&ev); err != nil {
			return err
		}
	}
	// The terminal honors the resource status exactly like the streaming
	// response.incomplete path: incomplete carries the authoritative vendor
	// reason and failed surfaces a terminal error. Completed and status-absent
	// payloads (which compatible providers still omit) stay clean, matching
	// the pre-existing mapping; anything else fails explicitly instead of
	// reporting clean.
	switch resp.Status {
	case responses.ResponseStatusIncomplete:
		return m.ResponseFinishedIncomplete(resp.IncompleteDetails.Reason)
	case responses.ResponseStatusFailed:
		return m.StreamError(string(resp.Error.Code), resp.Error.Message, "upstream response failed")
	case responses.ResponseStatusCompleted, "":
		return m.ResponseFinished()
	default:
		return fmt.Errorf("openai-responses: completion events: unexpected resource status %q", string(resp.Status))
	}
}
