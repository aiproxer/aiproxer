package checkpoint_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// largeHistoryCall builds a representative coding-agent history whose billable
// text is roughly one MiB. BillableFingerprint runs on the attempt admission
// path (clamp preview and backend open), so its temporary allocation cost is
// paid per attempt and must not scale with the prompt the user already holds.
func largeHistoryCall(textBytes int) aipapi.Call {
	const messages = 64
	body := strings.Repeat("x", textBytes)
	msgs := make([]aipapi.Message, 0, messages)
	for range messages {
		msgs = append(msgs, aipapi.Message{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(body)},
		})
	}
	return aipapi.Call{
		ID:       "bench-fingerprint",
		Messages: msgs,
		Tools: []aipapi.ToolDef{{
			Name:       "read_file",
			Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
	}
}

func BenchmarkBillableFingerprint_largeHistory(b *testing.B) {
	const textBytes = 16 << 10 // 64 messages * 16 KiB = 1 MiB of billable text
	call := largeHistoryCall(textBytes)
	b.SetBytes(int64(textBytes * 64))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := checkpoint.BillableFingerprint(call); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBillableWidened_largeHistory(b *testing.B) {
	call := largeHistoryCall(16 << 10)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := checkpoint.BillableWidened(call, call); err != nil {
			b.Fatal(err)
		}
	}
}
