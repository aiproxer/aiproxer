package testkit

import "testing"

func TestCapBytes(t *testing.T) {
	t.Parallel()
	b := []byte{1, 2, 3, 4}
	if got := string(CapBytes(b, 2)); got != "\x01\x02" {
		t.Fatalf("got %q", got)
	}
	if got := CapBytes(b, 0); len(got) != len(b) {
		t.Fatalf("max 0 means no cap: len=%d", len(got))
	}
}

func TestSplitFuzzPackedRouteBody(t *testing.T) {
	t.Parallel()
	// selLen=4, selector "abcd", body "EF"
	b := append([]byte{4}, []byte("abcdEF")...)
	sel, body := SplitFuzzPackedRouteBody(b, 64, 64)
	if sel != "abcd" {
		t.Fatalf("selector %q", sel)
	}
	if string(body) != "EF" {
		t.Fatalf("body %q", body)
	}
	sel2, _ := SplitFuzzPackedRouteBody([]byte{0}, 64, 64)
	if sel2 != DefaultFuzzRouteSelector {
		t.Fatalf("empty selector default: %q", sel2)
	}
}
