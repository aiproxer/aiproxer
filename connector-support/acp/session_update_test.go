package acp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestJSONRPCIDEqual_jsonNumberBeyondFloat53(t *testing.T) {
	t.Parallel()
	// 2^53+1: not every integer is representable in float64; json.Number preserves it.
	const id = int64(1<<53 + 1)
	n := json.Number("9007199254740993")
	if !jsonRPCIDEqual(n, id) {
		t.Fatalf("json.Number should match int64 id")
	}
}

func TestJSONRPCIDEqual_stringID(t *testing.T) {
	t.Parallel()
	if !jsonRPCIDEqual("42", 42) {
		t.Fatalf("string id should match")
	}
	if jsonRPCIDEqual("41", 42) {
		t.Fatalf("mismatch should not match")
	}
}

func TestParseNDJSONLine_resultMatchesLargeID(t *testing.T) {
	t.Parallel()
	// Same as TestJSONRPCIDEqual — terminal result must associate with this request id.
	const want = int64(1<<53 + 1)
	line := `{"jsonrpc":"2.0","id":9007199254740993,"result":{"stopReason":"end_turn"}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, want)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != aipapi.EventResponseFinished {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_resultLargeIDMismatch(t *testing.T) {
	t.Parallel()
	const want = int64(1<<53 + 2)
	line := `{"jsonrpc":"2.0","id":9007199254740993,"result":{"stopReason":"end_turn"}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, want)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Fatalf("expected no events for id mismatch, got %#v", evs)
	}
}

func TestParseNDJSONLine_errorIncludesStructuredDetail(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","id":99,"method":"session/prompt","error":{"code":-32603,"message":"Internal error","data":{"error":"Gemini quota exhausted for this account"}}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Kind != aipapi.EventError || evs[1].Kind != aipapi.EventResponseFinished {
		t.Fatalf("got %#v", evs)
	}
	if got, want := evs[0].ErrorMessage, "Internal error: Gemini quota exhausted for this account"; got != want {
		t.Fatalf("error message = %q, want %q", got, want)
	}
}

func TestParseNDJSONLine_errorDoesNotDuplicateDetail(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","id":99,"method":"session/prompt","error":{"code":-32003,"message":"Quota exceeded: no tokens left","data":{"error":"no tokens left"}}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Kind != aipapi.EventError || evs[1].Kind != aipapi.EventResponseFinished {
		t.Fatalf("got %#v", evs)
	}
	if got, want := evs[0].ErrorMessage, "Quota exceeded: no tokens left"; got != want {
		t.Fatalf("error message = %q, want %q", got, want)
	}
}

func TestParseNDJSONLine_stringIDResult(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","id":"99","result":{}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != aipapi.EventResponseFinished {
		t.Fatalf("got %#v", evs)
	}
}
