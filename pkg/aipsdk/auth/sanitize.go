package auth

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// DefaultChallengeSSOSummary is the safe client-visible message when no custom summary is set.
const DefaultChallengeSSOSummary = "Additional sign-in is required to use this key."

// PublicChallengeSummaryMaxRunes is the default and maximum safe length for
// [SanitizePublicChallengeSummary] when cap is non-positive. Keep call sites in sync
// (HTTP renderers, event dispatch, audit sinks).
const PublicChallengeSummaryMaxRunes = 256

// SanitizePublicChallengeSummary bounds and filters challenge copy for HTTP responses and logs.
// Callers should treat [Challenge.Summary] as non-secret by contract; this applies defense in depth
// when a remote decider or policy bug places credential-like text in the summary.
func SanitizePublicChallengeSummary(summary, fallback string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = PublicChallengeSummaryMaxRunes
	}
	s := strings.TrimSpace(summary)
	if s == "" {
		return fallback
	}
	s = replaceProblemRunes(s)
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if s == "" {
		return fallback
	}
	if containsCredentialLikePatterns(s) {
		return fallback
	}
	out := truncateUTF8Runes(s, maxRunes)
	if out == "" {
		return fallback
	}
	return out
}

func replaceProblemRunes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteByte(' ')
		case unicode.IsControl(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func containsCredentialLikePatterns(s string) bool {
	lower := strings.ToLower(s)
	patterns := []string{
		"bearer ",
		"access_token",
		"refresh_token",
		"id_token",
		"client_secret",
		"password=",
		"api_key=",
		"authorization:",
		"apikey:",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func truncateUTF8Runes(s string, maxLen int) string {
	if maxLen <= 0 {
		return s
	}
	const runeCap = 100_000 // avoid maxLen*4 overflow and pathological prealloc
	if maxLen > runeCap {
		maxLen = runeCap
	}
	n := utf8.RuneCountInString(s)
	if n <= maxLen {
		return s
	}
	var b strings.Builder
	b.Grow(maxLen*4 + utf8.UTFMax)
	i := 0
	for _, r := range s {
		if i >= maxLen-1 { // room for ellipsis
			break
		}
		b.WriteRune(r)
		i++
	}
	b.WriteString("…")
	return b.String()
}
