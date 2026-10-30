package openairesponses

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/protocols/openairesponsesitem"
)

func TestExactReasoningAddedShell_usesMarshalEnvelopeOrder(t *testing.T) {
	t.Parallel()
	canon := json.RawMessage(`{"status":"completed","id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"s"}],"content":[{"type":"reasoning_text","text":"c"}],"encrypted_content":"enc"}`)
	got, err := exactReasoningAddedShell(canon)
	if err != nil {
		t.Fatal(err)
	}
	wantFields := map[string]json.RawMessage{
		"id":      json.RawMessage(`"rs_1"`),
		"type":    json.RawMessage(`"reasoning"`),
		"summary": json.RawMessage(`[]`),
		"status":  json.RawMessage(`"completed"`),
	}
	want, err := openairesponsesitem.MarshalEnvelope(wantFields)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("added shell must use MarshalEnvelope order:\n got %s\nwant %s", got, want)
	}
	if string(got) != `{"id":"rs_1","type":"reasoning","summary":[],"status":"completed"}` {
		t.Fatalf("unexpected shell bytes: %s", got)
	}
}
