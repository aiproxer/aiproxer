package db

import "testing"

func TestLegacyPersistedTextDropsOnlyNUL(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"":                 "",
		"plain":            "plain",
		"a\x00b\x00\x00c":  "abc",
		"\x00":             "",
		"zażółć\x00'quote": "zażółć'quote",
	} {
		if got := LegacyPersistedText(in); got != want {
			t.Errorf("LegacyPersistedText(%q) = %q, want %q", in, got, want)
		}
	}
}
