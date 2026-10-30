package openairesponses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/openairesponsestream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3/responses"
)

func TestEmitOutputMediaFromResponse_inputImageAndFile(t *testing.T) {
	t.Parallel()
	const raw = `{
  "id": "resp_mm",
  "object": "response",
  "status": "completed",
  "output": [
    {
      "type": "message",
      "id": "msg_mm",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "input_image", "image_url": "https://cdn.example.com/out.png"},
        {"type": "input_file", "file_id": "file_doc_1"}
      ]
    }
  ]
}`
	var resp responses.Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	pending := stream.NewPendingEventQueue(0)
	mapper := openairesponsestream.New(&pending)
	if err := openairesponsestream.EmitOutputMediaFromResponse(mapper, resp); err != nil {
		t.Fatal(err)
	}
	evs := stream.DrainPending(&pending)
	var media []aipapi.Event
	for _, ev := range evs {
		if ev.Kind == aipapi.EventAssistantImageRef || ev.Kind == aipapi.EventAssistantFileRef {
			media = append(media, ev)
		}
	}
	if len(media) != 2 {
		t.Fatalf("assistant media events: %+v (all %d)", media, len(evs))
	}
	if media[0].Kind != aipapi.EventAssistantImageRef || media[0].AssistantRef != "https://cdn.example.com/out.png" {
		t.Fatalf("image event: %+v", media[0])
	}
	if media[1].Kind != aipapi.EventAssistantFileRef || media[1].AssistantRef != "file_doc_1" {
		t.Fatalf("file event: %+v", media[1])
	}
}

func TestHandleUnion_completed_emitsAssistantMediaAfterText(t *testing.T) {
	t.Parallel()
	raw := `{
  "type": "response.completed",
  "sequence_number": 1,
  "response": {
    "id": "resp_x",
    "object": "response",
    "status": "completed",
    "output": [
      {
        "type": "message",
        "id": "m1",
        "status": "completed",
        "role": "assistant",
        "content": [
          {"type": "output_text", "text": "see"},
          {"type": "input_image", "image_url": {"url": "https://img.example/x.png"}}
        ]
      }
    ]
  }
}`
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &sdkStream{}
	if err := s.handleUnion(u); err != nil {
		t.Fatal(err)
	}
	evs := stream.DrainPending(&s.pending)
	var gotText strings.Builder
	var img string
	for _, ev := range evs {
		switch ev.Kind {
		case aipapi.EventTextDelta:
			gotText.WriteString(ev.Delta)
		case aipapi.EventAssistantImageRef:
			img = ev.AssistantRef
		}
	}
	if got := gotText.String(); got != "see" {
		t.Fatalf("text %q", got)
	}
	if img != "https://img.example/x.png" {
		t.Fatalf("image ref %q", img)
	}
}
