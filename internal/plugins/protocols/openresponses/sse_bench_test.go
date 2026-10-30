package openresponses

import (
	"encoding/json"
	"testing"
)

func BenchmarkFormatSSEEventDelta(b *testing.B) {
	evt := StreamEvent{
		Type:           "response.output_text.delta",
		SequenceNumber: 42,
		ItemID:         "msg_123",
		Delta:          "hello world, this is a streaming delta chunk",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := FormatSSEEvent(evt)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) == 0 {
			b.Fatal("empty frame")
		}
	}
}

func BenchmarkFormatSSEEventMultilineOpaque(b *testing.B) {
	evt := StreamEvent{
		Type:           "response.output_text.delta",
		SequenceNumber: 42,
		Opaque:         json.RawMessage("{\n\"x\": 1\r\n}"),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := FormatSSEEvent(evt)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) == 0 {
			b.Fatal("empty frame")
		}
	}
}
