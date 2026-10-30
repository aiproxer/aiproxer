package openairesponsesitem

import "testing"

func BenchmarkCanonizeReasoningItemOpaque(b *testing.B) {
	in := []byte(`{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"plan"}],"content":[{"type":"reasoning_text","text":"body"}],"encrypted_content":"enc","status":"completed"}`)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := CanonizeReasoningItemOpaque(in); err != nil {
			b.Fatal(err)
		}
	}
}
