package backendplugin_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"google.golang.org/protobuf/proto"
)

func invocationWithParts(parts ...backendplugin.Part) backendplugin.Invocation {
	return backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg",
		CanonicalModelID: "m",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: parts,
		}},
	}
}

// A plugin-side part kind with no canonical mapping must fail explicitly
// instead of being silently dropped from the canonical call.
func TestCallFromInvocation_UnsupportedPartKindFailsClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		kind backendplugin.PartKind
	}{
		{name: "tool_call has no canonical mapping", kind: backendplugin.PartKindToolCall},
		{name: "unknown kind", kind: backendplugin.PartKind("bogus")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := backendplugin.CallFromInvocation(invocationWithParts(backendplugin.Part{Kind: tc.kind}))
			if err == nil {
				t.Fatalf("kind %q: expected explicit error, got nil (part silently dropped)", tc.kind)
			}
			if !errors.Is(err, backendplugin.ErrUnsupportedPartKind) {
				t.Fatalf("kind %q: errors.Is(err, ErrUnsupportedPartKind)=false: %v", tc.kind, err)
			}
			if !strings.Contains(err.Error(), string(tc.kind)) {
				t.Fatalf("kind %q: error must name the offending kind, got %q", tc.kind, err.Error())
			}
		})
	}
}

// A plugin-side json part must map to a canonical json part with payload intact.
func TestCallFromInvocation_JSONPartMapped(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"key":"value"}`)
	inv := invocationWithParts(backendplugin.Part{
		Kind:         backendplugin.PartKindJSON,
		ToolArgsJSON: backendplugin.RawJSONFromBytes(payload),
	})
	call, err := backendplugin.CallFromInvocation(inv)
	if err != nil {
		t.Fatal(err)
	}
	parts := call.Messages[0].Parts
	if len(parts) != 1 || parts[0].Kind != aipapi.PartJSON {
		t.Fatalf("parts=%+v, want one json part", parts)
	}
	if string(parts[0].Content) != string(payload) {
		t.Fatalf("content=%q, want %q", parts[0].Content, payload)
	}
}

// A json part without payload must fail closed instead of producing an
// invalid canonical part.
func TestCallFromInvocation_JSONPartRequiresContent(t *testing.T) {
	t.Parallel()
	_, err := backendplugin.CallFromInvocation(invocationWithParts(backendplugin.Part{Kind: backendplugin.PartKindJSON}))
	if err == nil {
		t.Fatal("expected explicit error for json part without content, got nil")
	}
	if errors.Is(err, backendplugin.ErrUnsupportedPartKind) {
		t.Fatalf("json kind is supported; want invalid-invocation error, got %v", err)
	}
}

func TestCanonicalEventFromAipapi_UsageEvidenceExplicitZeroPresence(t *testing.T) {
	t.Parallel()
	ev := aipapi.Event{
		Kind: aipapi.EventUsageDelta,
		UsagePresence: aipapi.UsagePresence{
			InputTokens:     true,
			CacheReadTokens: true,
		},
		InputTokens:     0,
		CacheReadTokens: 50,
	}
	canonical := backendplugin.CanonicalEventFromAipapi(ev)
	if canonical == nil || canonical.Usage == nil {
		t.Fatal("expected non-nil UsageEvidence")
	}
	if !canonical.Usage.Presence.InputTokens || canonical.Usage.InputTokens == nil || *canonical.Usage.InputTokens != 0 {
		t.Fatalf("expected InputTokens 0 present, got %+v", canonical.Usage)
	}
	if !canonical.Usage.Presence.CacheReadTokens || canonical.Usage.CacheReadTokens == nil || *canonical.Usage.CacheReadTokens != 50 {
		t.Fatalf("expected CacheReadTokens 50 present, got %+v", canonical.Usage)
	}
}

// The bridge must not invent a provider total: an omitted total stays absent
// and an explicit zero stays a present zero, with the other counters intact.
func TestCanonicalEventFromAipapi_TotalPresenceSurvivesWireRoundTrip(t *testing.T) {
	t.Parallel()
	ptr := func(v int64) *int64 { return &v }
	cases := []struct {
		name         string
		total        int
		totalPresent bool
		wantTotal    *int64
	}{
		{name: "omitted total stays omitted"},
		{name: "explicit zero stays a present zero", totalPresent: true, wantTotal: ptr(0)},
		{name: "reported total is kept", total: 1020, totalPresent: true, wantTotal: ptr(1020)},
		{name: "legacy nonzero total is kept", total: 1020, wantTotal: ptr(1020)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := aipapi.Event{
				Kind: aipapi.EventUsageDelta, InputTokens: 300, OutputTokens: 20, CacheReadTokens: 600, CacheWriteTokens: 100, TotalTokens: tc.total,
				UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, CacheReadTokens: true, CacheWriteTokens: true, TotalTokens: tc.totalPresent},
			}
			wire, err := backendplugin.CanonicalEventToProto(backendplugin.CanonicalEventFromAipapi(ev))
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := proto.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			var decoded backendpluginv1.CanonicalEvent
			if err := proto.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			got, err := backendplugin.CanonicalEventFromProto(&decoded)
			if err != nil {
				t.Fatal(err)
			}
			want := backendplugin.UsageEvidence{
				InputTokens: ptr(300), OutputTokens: ptr(20), CacheReadTokens: ptr(600), CacheWriteTokens: ptr(100),
				TotalTokens:  tc.wantTotal,
				Presence:     backendplugin.UsagePresence{InputTokens: true, OutputTokens: true, CacheReadTokens: true, CacheWriteTokens: true, TotalTokens: tc.wantTotal != nil},
				RawUsageJSON: backendplugin.RawJSONAbsentValue(),
			}
			if !reflect.DeepEqual(got.Usage, &want) {
				t.Fatalf("usage evidence mismatch:\n got %+v\n want %+v", got.Usage, want)
			}
		})
	}
}
