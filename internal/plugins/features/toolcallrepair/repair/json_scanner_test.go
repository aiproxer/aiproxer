package repair_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
)

func TestCompleteJSONSuffix_AppendOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{name: "already_valid", in: `{"a":1}`, want: `{"a":1}`, wantOK: true},
		{name: "truncated_object", in: `{"location":"NYC"`, want: `{"location":"NYC"}`, wantOK: true},
		{name: "truncated_nested", in: `{"query":"x","filters":{"tags":["a"`, want: `{"query":"x","filters":{"tags":["a"]}}`, wantOK: true},
		{name: "unterminated_string_only", in: `{"a":"hi`, want: `{"a":"hi"}`, wantOK: true},
		{name: "truncated_array", in: `[1,2`, want: `[1,2]`, wantOK: true},
		{name: "lone_object", in: `{`, want: `{}`, wantOK: true},
		{name: "lone_array", in: `[`, want: `[]`, wantOK: true},
		{name: "trailing_comma_refused", in: `{"a":1,`, wantOK: false},
		{name: "incomplete_value_refused", in: `{"a":`, wantOK: false},
		{name: "empty_refused", in: ``, wantOK: false},
		{name: "mismatched_close_refused", in: `{"a":1]`, wantOK: false},
		{name: "trailing_garbage_refused", in: `{"a":1}x`, wantOK: false},
		{name: "incomplete_backslash_escape", in: `{"a":"hi\`, wantOK: false},
		{name: "incomplete_unicode_escape", in: `{"a":"\u12`, wantOK: false},
		{name: "invalid_utf8_in_string", in: "{\"a\":\"\xff", wantOK: false},
		{name: "mismatched_open_close", in: `[}`, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := repair.CompleteJSONSuffix([]byte(tc.in))
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v got=%q", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			if string(got) != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			if !json.Valid(got) {
				t.Fatalf("result not json.Valid: %q", got)
			}
			if tc.in != "" && !json.Valid([]byte(tc.in)) {
				if len(got) < len(tc.in) || string(got[:len(tc.in)]) != tc.in {
					t.Fatal("repair must be append-only prefix-preserving")
				}
			}
		})
	}
}
