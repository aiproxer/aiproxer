package responseitem

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCanonizeReasoningItemOpaque_PreservesEncryptedContentPresence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         string
		wantPresent bool
		wantRaw     string
	}{
		{name: "absent", raw: `{"id":"rs_1","summary":[]}`},
		{name: "null", raw: `{"id":"rs_1","summary":[],"encrypted_content":null}`, wantPresent: true, wantRaw: "null"},
		{name: "value", raw: `{"id":"rs_1","summary":[],"encrypted_content":"state"}`, wantPresent: true, wantRaw: `"state"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := CanonizeReasoningItemOpaque([]byte(tt.raw))
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]json.RawMessage
			if err := json.Unmarshal(got, &value); err != nil {
				t.Fatal(err)
			}
			raw, present := value["encrypted_content"]
			if present != tt.wantPresent {
				t.Fatalf("encrypted_content present = %v, want %v", present, tt.wantPresent)
			}
			if present && string(raw) != tt.wantRaw {
				t.Fatalf("encrypted_content = %s, want %s", raw, tt.wantRaw)
			}
		})
	}
}

func TestCanonizeCompactionSummaryItemOpaquePreservesAllowlistedEnvelope(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"status":"completed","type":"compaction_summary","id":null,"encrypted_content":"opaque","created_by":"codex"}`)
	got, err := CanonizeCompactionSummaryItemOpaque(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("summary envelope changed: got %s", got)
	}
}

func TestCanonizeCompactionSummaryItemOpaqueRejectsUnsafeShapes(t *testing.T) {
	t.Parallel()
	tooDeep := `{"type":"compaction_summary","encrypted_content":"x","nested":` + strings.Repeat("[", maxOpaqueJSONDepth+2) + `0` + strings.Repeat("]", maxOpaqueJSONDepth+2) + `}`
	for _, raw := range []string{
		`{"type":"compaction_summary","encrypted_content":"x","extra":1}`,
		`{"type":"compaction_summary","encrypted_content":"x","type":"compaction_summary"}`,
		`{"type":"compaction_summary","encrypted_content":"x","status":"in_progress"}`,
		tooDeep,
	} {
		if _, err := CanonizeCompactionSummaryItemOpaque([]byte(raw)); err == nil {
			t.Fatalf("accepted unsafe summary shape: %s", raw[:min(len(raw), 80)])
		}
	}
}

func TestCanonizeReasoningItemForInput_RemovesResponseStatus(t *testing.T) {
	t.Parallel()

	got, err := CanonizeReasoningItemForInput([]byte(`{"id":"rs_1","summary":[],"encrypted_content":"opaque","status":"completed"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), `"status"`) {
		t.Fatalf("input reasoning retained response status: %s", got)
	}
	want := `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"opaque"}`
	if string(got) != want {
		t.Fatalf("canonical JSON = %s, want %s", got, want)
	}
}

func TestCanonizeReasoningItemOpaque_ValidationAndOrdering(t *testing.T) {
	t.Parallel()

	valid := `{"status":"completed","encrypted_content":null,"summary":[],"id":"rs_1"}`
	got, err := CanonizeReasoningItemOpaque([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":null,"status":"completed"}`
	if string(got) != want {
		t.Fatalf("canonical JSON = %s, want %s", got, want)
	}

	oversize := `{"id":"rs_1","summary":[],"encrypted_content":"` + strings.Repeat("x", aipapi.MaxReasoningOpaqueBytes) + `"}`
	deep := `{"id":"rs_1","summary":[],"content":` + strings.Repeat("[", maxOpaqueJSONDepth+1) + `0` + strings.Repeat("]", maxOpaqueJSONDepth+1) + `}`
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "unknown field", raw: `{"id":"rs_1","summary":[],"extra":true}`},
		{name: "duplicate key", raw: `{"id":"rs_1","id":"rs_2","summary":[]}`},
		{name: "trailing JSON", raw: `{"id":"rs_1","summary":[]} true`},
		{name: "null content", raw: `{"id":"rs_1","summary":[],"content":null}`},
		{name: "invalid type", raw: `{"id":"rs_1","type":"message","summary":[]}`},
		{name: "invalid status", raw: `{"id":"rs_1","summary":[],"status":"done"}`},
		{name: "oversize", raw: oversize},
		{name: "excessive depth", raw: deep},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := CanonizeReasoningItemOpaque([]byte(tc.raw)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestCanonizeReasoningItemOpaque_NonEmptyElements(t *testing.T) {
	t.Parallel()

	got, err := CanonizeReasoningItemOpaque([]byte(`{"id":"rs_1","summary":[{"text":"sum","type":"summary_text"}],"content":[{"text":"body","type":"reasoning_text"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"rs_1","type":"reasoning","summary":[{"text":"sum","type":"summary_text"}],"content":[{"text":"body","type":"reasoning_text"}]}`
	if string(got) != want {
		t.Fatalf("canonical JSON = %s, want %s", got, want)
	}

	for _, raw := range []string{
		`{"id":"rs_1","summary":[{"type":"summary_text","text":"x","extra":true}]}`,
		`{"id":"rs_1","summary":[{"type":"reasoning_text","text":"x"}]}`,
		`{"id":"rs_1","summary":[{"type":"summary_text","text":1}]}`,
		`{"id":"rs_1","summary":[{"type":"summary_text","text":"x","text":"y"}]}`,
	} {
		if _, err := CanonizeReasoningItemOpaque([]byte(raw)); err == nil {
			t.Fatalf("expected element validation error for %s", raw)
		}
	}
}

func TestMarshalEnvelopeRejectsInvalidFields(t *testing.T) {
	t.Parallel()

	for _, fields := range []map[string]json.RawMessage{
		{"unknown": json.RawMessage(`true`)},
		{"id": json.RawMessage(`not-json`)},
	} {
		if _, err := MarshalEnvelope(fields); err == nil {
			t.Fatal("expected marshal error")
		}
	}
}

func TestCanonizeReasoningItemOpaque_ContentSafeErrors(t *testing.T) {
	t.Parallel()

	secretData := "SECRET_SENSITIVE_CIPHERTEXT_ABC123"
	invalidItemWithSecret := `{"id":"rs_1","summary":[],"extra":"` + secretData + `"}`
	_, err := CanonizeReasoningItemOpaque([]byte(invalidItemWithSecret))
	if err == nil {
		t.Fatal("expected error for invalid item with extra field")
	}

	if strings.Contains(err.Error(), secretData) {
		t.Fatalf("error message leaked secret data: %v", err)
	}
}
