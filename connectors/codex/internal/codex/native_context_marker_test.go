package codex

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/routingstub"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestConsumeNativeContinuityMarker_acceptsOnlyFixedPostureAndDeletesIt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  json.RawMessage
		want bool
	}{
		{name: "trusted", raw: json.RawMessage(nativeContinuityMarkerValue), want: true},
		{name: "spoofed", raw: json.RawMessage(`{"eligible":false,"dialect":"openai.responses.reasoning_item.v1"}`)},
		{name: "wrong dialect", raw: json.RawMessage(`{"eligible":true,"dialect":"openai.chat.reasoning_text.v1"}`)},
		{name: "malformed", raw: json.RawMessage(`{"eligible":true`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{Extensions: map[string]json.RawMessage{nativeContinuityMarkerKey: tc.raw}}
			if got := consumeNativeContinuityMarker(&call); got != tc.want {
				t.Fatalf("trusted=%v want %v", got, tc.want)
			}
			if _, ok := call.Extensions[nativeContinuityMarkerKey]; ok {
				t.Fatal("marker must be deleted before provider serialization")
			}
		})
	}
}

func TestPayloadForCall_doesNotSerializeNativeContinuityMarker(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Extensions: map[string]json.RawMessage{
			nativeContinuityMarkerKey: json.RawMessage(nativeContinuityMarkerValue),
		},
	}
	payload, err := PayloadForCall(call, routingstub.AttemptCandidate{Primary: routingstub.Primary{Model: "gpt-5.6-codex"}}, Config{})
	if err != nil {
		t.Fatalf("PayloadForCall: %v", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(call.Extensions[nativeContinuityMarkerKey]) != nativeContinuityMarkerValue {
		t.Fatal("PayloadForCall must not mutate the caller-owned call")
	}
	if bytes.Contains(body, []byte(nativeContinuityMarkerKey)) || bytes.Contains(body, []byte(nativeContinuityMarkerValue)) {
		t.Fatalf("provider body contains internal marker: %s", body)
	}
}
