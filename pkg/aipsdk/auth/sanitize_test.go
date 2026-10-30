package auth

import (
	"strings"
	"testing"
)

func TestSanitizePublicChallengeSummary(t *testing.T) {
	t.Parallel()
	fb := "fallback-msg"
	cases := []struct {
		name     string
		in       string
		maxRunes int
		want     string
	}{
		{"empty uses fallback", "", 255, fb},
		{"whitespace uses fallback", "   \t  ", 255, fb},
		{"plain short unchanged", "Sign in to continue.", 255, "Sign in to continue."},
		{"collapses internal space", "a  \tb", 255, "a b"},
		{"strips controls", "line1\x00line2", 255, "line1 line2"},
		{"bearer triggers fallback", "Please use Bearer sk-abc", 255, fb},
		{"access_token triggers fallback", "Open https://x?access_token=secret", 255, fb},
		{"long truncates", strings.Repeat("x", 400), 255, strings.Repeat("x", 254) + "…"},
		{"zero maxRunes defaults to 256", strings.Repeat("x", 400), 0, strings.Repeat("x", 255) + "…"},
		{"negative maxRunes defaults to 256", strings.Repeat("x", 400), -1, strings.Repeat("x", 255) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := SanitizePublicChallengeSummary(tc.in, fb, tc.maxRunes)
			if got != tc.want {
				t.Fatalf("SanitizePublicChallengeSummary(%q, %d) = %q, want %q", tc.in, tc.maxRunes, got, tc.want)
			}
		})
	}
}
