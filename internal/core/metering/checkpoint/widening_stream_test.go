package checkpoint_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestBillableFingerprint_StableEncoding pins the exact preimage byte stream.
// Streaming the fingerprint into SHA-256 changed how the bytes are buffered,
// never which bytes are hashed. These golden digests fail if a future edit
// alters field order, framing, or a length prefix.
func TestBillableFingerprint_StableEncoding(t *testing.T) {
	t.Parallel()
	temp := 0.7
	topP := 0.9
	call := aipapi.Call{
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("sys")}}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello"), {Kind: aipapi.PartJSON, Content: json.RawMessage(`{"a":1}`)}}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: "openai", Text: "think", Signature: "sig", Opaque: json.RawMessage(`{"o":true}`)}}}},
		},
		Tools:      []aipapi.ToolDef{{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired},
		Options:    aipapi.GenerationOptions{Temperature: &temp, TopP: &topP},
	}
	fp, err := checkpoint.BillableFingerprint(call)
	if err != nil {
		t.Fatalf("BillableFingerprint: %v", err)
	}
	const want = "1ec9b11babff75d235811dcc968334fe394323eabac19c2179d7530eb9cff01f"
	if string(fp) != want {
		t.Fatalf("fingerprint = %s, want %s", fp, want)
	}

	empty, err := checkpoint.BillableFingerprint(aipapi.Call{})
	if err != nil {
		t.Fatalf("BillableFingerprint(empty): %v", err)
	}
	const wantEmpty = "f0734446ed0dfee0a201568437bda856446d6c01076641c57c7bf57094f93fbe"
	if string(empty) != wantEmpty {
		t.Fatalf("empty fingerprint = %s, want %s", empty, wantEmpty)
	}
}

// TestBillableFingerprintLargeHistoryAllocationsStayBounded is an allocation
// ratchet for the attempt-admission path. The preimage is bounded only by
// request limits, so materializing it as a bias-free string made every attempt
// allocate several times the history size. Streaming through a fixed scratch
// buffer keeps the cost constant and independent of prompt size.
//
//nolint:paralleltest // AllocsPerRun/benchmark allocation counters are process-wide.
func TestBillableFingerprintLargeHistoryAllocationsStayBounded(t *testing.T) {
	const textBytes = 16 << 10 // 64 messages * 16 KiB = 1 MiB of billable text
	call := largeHistoryCall(textBytes)
	var sink []byte
	benchRes := testing.Benchmark(func(b *testing.B) {
		b.Helper()
		b.ReportAllocs()
		for b.Loop() {
			fp, err := checkpoint.BillableFingerprint(call)
			if err != nil {
				b.Fatal(err)
			}
			sink = fp
		}
	})
	if len(sink) == 0 {
		t.Fatal("fingerprint sink empty")
	}
	const maxAllowedBytesPerOp = 64 << 10
	if got := benchRes.AllocedBytesPerOp(); got > maxAllowedBytesPerOp {
		t.Fatalf("BillableFingerprint allocates %d B/op for a 1 MiB history, want <= %d B/op",
			got, maxAllowedBytesPerOp)
	}
}
