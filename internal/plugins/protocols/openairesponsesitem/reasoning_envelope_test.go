package openairesponsesitem

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCanonizeReasoningItemOpaque_presenceVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "minimal",
			in:   `{"id":"rs_1","summary":[]}`,
			want: `{"id":"rs_1","type":"reasoning","summary":[]}`,
		},
		{
			name: "with_type_status",
			in:   `{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"s"}],"status":"completed"}`,
			want: `{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"s"}],"status":"completed"}`,
		},
		{
			name: "content_present",
			in:   `{"id":"rs_1","summary":[],"content":[{"type":"reasoning_text","text":"c"}]}`,
			want: `{"id":"rs_1","type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"c"}]}`,
		},
		{
			name: "encrypted_absent",
			in:   `{"id":"rs_1","summary":[]}`,
			want: `{"id":"rs_1","type":"reasoning","summary":[]}`,
		},
		{
			name: "encrypted_null",
			in:   `{"id":"rs_1","summary":[],"encrypted_content":null}`,
			want: `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":null}`,
		},
		{
			name: "encrypted_string",
			in:   `{"id":"rs_1","summary":[],"encrypted_content":"enc"}`,
			want: `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"enc"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := CanonizeReasoningItemOpaque([]byte(tc.in))
			if err != nil {
				t.Fatalf("CanonizeReasoningItemOpaque: %v", err)
			}
			assertJSONEqual(t, tc.want, string(got))
		})
	}
}

func TestCanonizeReasoningItemOpaque_rejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{name: "missing_id", in: `{"summary":[]}`},
		{name: "empty_id", in: `{"id":"","summary":[]}`},
		{name: "whitespace_id", in: `{"id":"  ","summary":[]}`},
		{name: "missing_summary", in: `{"id":"rs_1"}`},
		{name: "summary_null", in: `{"id":"rs_1","summary":null}`},
		{name: "summary_object", in: `{"id":"rs_1","summary":{}}`},
		{name: "summary_string", in: `{"id":"rs_1","summary":"x"}`},
		{name: "content_null", in: `{"id":"rs_1","summary":[],"content":null}`},
		{name: "content_string", in: `{"id":"rs_1","summary":[],"content":"x"}`},
		{name: "encrypted_number", in: `{"id":"rs_1","summary":[],"encrypted_content":1}`},
		{name: "bad_type", in: `{"id":"rs_1","type":"message","summary":[]}`},
		{name: "bad_status", in: `{"id":"rs_1","summary":[],"status":"failed"}`},
		{name: "unknown_field", in: `{"id":"rs_1","summary":[],"extra":1}`},
		{name: "malformed", in: `{`},
		{name: "trailing_garbage_syntax", in: `{"id":"rs_1","summary":[]}garbage`},
		{name: "trailing_second_value", in: `{"id":"rs_1","summary":[]}{"id":"rs_2"}`},
		{name: "duplicate_top_level_id", in: `{"id":"rs_1","summary":[],"id":"rs_2"}`},
		{name: "duplicate_nested_summary_key", in: `{"id":"rs_1","summary":[{"type":"summary_text","text":"a","text":"b"}]}`},
		{name: "summary_wrong_type", in: `{"id":"rs_1","summary":[{"type":"reasoning_text","text":"SECRET_SUM"}]}`},
		{name: "summary_missing_type", in: `{"id":"rs_1","summary":[{"text":"SECRET_SUM"}]}`},
		{name: "summary_missing_text", in: `{"id":"rs_1","summary":[{"type":"summary_text"}]}`},
		{name: "summary_unknown_field", in: `{"id":"rs_1","summary":[{"type":"summary_text","text":"SECRET_SUM","extra":1}]}`},
		{name: "summary_non_object", in: `{"id":"rs_1","summary":["SECRET_SUM"]}`},
		{name: "content_wrong_type", in: `{"id":"rs_1","summary":[],"content":[{"type":"summary_text","text":"SECRET_BODY"}]}`},
		{name: "content_missing_type", in: `{"id":"rs_1","summary":[],"content":[{"text":"SECRET_BODY"}]}`},
		{name: "content_unknown_field", in: `{"id":"rs_1","summary":[],"content":[{"type":"reasoning_text","text":"SECRET_BODY","extra":true}]}`},
		{name: "content_non_object", in: `{"id":"rs_1","summary":[],"content":[1]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := CanonizeReasoningItemOpaque([]byte(tc.in))
			if err == nil {
				t.Fatalf("expected error, got %s", got)
			}
			msg := err.Error()
			for _, leak := range []string{"rs_1", "rs_2", `"enc"`, `[{"`, "garbage", "SECRET_SUM", "SECRET_BODY"} {
				if strings.Contains(msg, leak) {
					t.Fatalf("error must be content-safe, got %v", err)
				}
			}
			if got != nil {
				t.Fatalf("opaque must be nil on error")
			}
		})
	}
}

func TestCanonizeReasoningItemOpaque_nestedElementsCanonical(t *testing.T) {
	t.Parallel()
	got, err := CanonizeReasoningItemOpaque([]byte(`{"id":"rs_1","summary":[{"text":"s","type":"summary_text"}],"content":[{"text":"c","type":"reasoning_text"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, `{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"s"}],"content":[{"type":"reasoning_text","text":"c"}]}`, string(got))
}

func TestCanonizeReasoningItemOpaque_typeAbsentCanonicalizesToReasoning(t *testing.T) {
	t.Parallel()
	got, err := CanonizeReasoningItemOpaque([]byte(`{"id":"rs_1","summary":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, `{"id":"rs_1","type":"reasoning","summary":[]}`, string(got))
}

func TestCanonizeReasoningItemOpaque_emptyArraysAccepted(t *testing.T) {
	t.Parallel()
	got, err := CanonizeReasoningItemOpaque([]byte(`{"id":"rs_1","summary":[],"content":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, `{"id":"rs_1","type":"reasoning","summary":[],"content":[]}`, string(got))
}

func TestCanonizeReasoningItemOpaque_whitespaceEquivalentDedupeForm(t *testing.T) {
	t.Parallel()
	a, err := CanonizeReasoningItemOpaque([]byte(`{"id":"rs_1","summary":[],"status":"completed"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonizeReasoningItemOpaque([]byte(`{ "status" : "completed" , "summary" : [] , "id" : "rs_1" }`))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("canonical forms differ\n%s\n%s", a, b)
	}
}

func TestCanonizeReasoningItemOpaque_oversize(t *testing.T) {
	t.Parallel()
	pad := strings.Repeat("x", aipapi.MaxReasoningOpaqueBytes)
	in := `{"id":"rs_1","summary":[],"encrypted_content":"` + pad + `"}`
	_, err := CanonizeReasoningItemOpaque([]byte(in))
	if err == nil {
		t.Fatal("expected oversize error")
	}
}

func assertJSONEqual(t *testing.T, want, got string) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want json: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got json: %v", err)
	}
	wb, _ := json.Marshal(w)
	gb, _ := json.Marshal(g)
	if string(wb) != string(gb) {
		t.Fatalf("json mismatch\nwant %s\ngot  %s", wb, gb)
	}
}
