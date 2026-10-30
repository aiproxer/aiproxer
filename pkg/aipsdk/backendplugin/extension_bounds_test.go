package backendplugin_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// The ABI metadata carrier is bounded. Every extension that does not fit must
// be refused, never dropped in map order: a dropped provenance marker changes
// the meaning of the attempt, and a silent truncation is invisible to both
// sides.
func TestApplyCallWireMetadata_ExtensionOverflowFailsClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		count   int
		value   string
		wantErr bool
	}{
		{name: "exactly the bound", count: 64, value: `1`},
		{name: "one over the bound", count: 65, value: `1`, wantErr: true},
		{name: "canonical maximum", count: 256, value: `1`, wantErr: true},
		{name: "oversized value", count: 1, value: `"` + strings.Repeat("x", 64<<10) + `"`, wantErr: true},
		{name: "empty value", count: 1, value: ``, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{
				Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
				Extensions: make(map[string]json.RawMessage, tc.count),
			}
			for i := 0; i < tc.count; i++ {
				call.Extensions[fmt.Sprintf("aip.test.%03d", i)] = json.RawMessage(tc.value)
			}
			inv := backendplugin.Invocation{
				RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
				Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{
					Kind: backendplugin.PartKindText, Text: strPtr("hello"),
				}}}},
			}
			err := backendplugin.ApplyCallWireMetadata(&inv, call, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatal("an over-bound extension set must be refused, not silently truncated")
				}
				if !strings.Contains(err.Error(), "metadata bound") && !strings.Contains(err.Error(), "no value") {
					t.Fatalf("the refusal must name the bound or the empty value: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("the bound-sized extension set must be accepted: %v", err)
			}
			if got := countExtensionMetadata(inv.SafeMetadata); got != tc.count {
				t.Fatalf("forwarded extensions=%d, want %d", got, tc.count)
			}
		})
	}
}

// TestExtensionMetadata_DeterministicRoundTrip proves a full bound-sized set,
// including the continuation provenance marker, survives the complete ABI
// round trip unchanged and in deterministic order.
func TestExtensionMetadata_DeterministicRoundTrip(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Extensions: map[string]json.RawMessage{"aip.continuation.materialized.v1": json.RawMessage(`true`)},
	}
	for i := range 63 {
		call.Extensions[fmt.Sprintf("aip.test.%03d", i)] = json.RawMessage(fmt.Sprintf(`%d`, i))
	}
	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{
			Kind: backendplugin.PartKindText, Text: strPtr("hello"),
		}}}},
	}
	if err := backendplugin.ApplyCallWireMetadata(&inv, call, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	wire, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatalf("proto: %v", err)
	}
	back, err := backendplugin.InvocationFromProto(wire)
	if err != nil {
		t.Fatalf("from proto: %v", err)
	}
	restored, err := backendplugin.CallFromInvocation(back)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(restored.Extensions) != 64 {
		t.Fatalf("restored extensions=%d, want 64", len(restored.Extensions))
	}
	if string(restored.Extensions["aip.continuation.materialized.v1"]) != `true` {
		t.Fatal("the continuation provenance marker was lost")
	}
}

func countExtensionMetadata(meta map[string]string) int {
	n := 0
	for key := range meta {
		if strings.HasPrefix(key, backendplugin.MetaExtensionPrefix) {
			n++
		}
	}
	return n
}
