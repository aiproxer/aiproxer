package openairesponsesitem

import (
	"strings"
	"testing"
)

func FuzzCanonizeReasoningItemOpaque(f *testing.F) {
	f.Add(`{"id":"rs_1","summary":[]}`)
	f.Add(`{"id":"rs_1","summary":[],"encrypted_content":null,"status":"completed"}`)
	f.Add(`{"id":"rs_1","summary":[]}garbage`)
	f.Add(`{"id":"rs_1","summary":[],"id":"rs_2"}`)
	f.Add(`{"id":"rs_1","summary":null}`)
	f.Fuzz(func(t *testing.T, in string) {
		got, err := CanonizeReasoningItemOpaque([]byte(in))
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "\n") || len(msg) > 200 {
				t.Fatalf("error too large/contentful: %q", msg)
			}
			if got != nil {
				t.Fatalf("opaque must be nil on error")
			}
			return
		}
		if len(got) == 0 {
			t.Fatal("empty opaque")
		}
		again, err := CanonizeReasoningItemOpaque(got)
		if err != nil {
			t.Fatalf("canonical form must re-canonize: %v", err)
		}
		if string(again) != string(got) {
			t.Fatalf("not idempotent\n%s\n%s", got, again)
		}
	})
}
