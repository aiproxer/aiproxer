package openairesponsestream

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3/responses"
)

func BenchmarkMapper_ReasoningOutputItemDone(b *testing.B) {
	var item responses.ResponseOutputItemUnion
	if err := json.Unmarshal([]byte(`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"s"}],"status":"completed"}`), &item); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		m, _ := newTestMapper()
		if err := m.ReasoningOutputItemDone(0, item); err != nil {
			b.Fatal(err)
		}
	}
}
