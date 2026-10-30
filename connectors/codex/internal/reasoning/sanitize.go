package reasoning

import (
	"regexp"
	"strings"
)

var (
	leadingHTMLCommentClose = regexp.MustCompile(`^\s*-->\s*`)
	htmlCommentBoundary     = regexp.MustCompile(`[ \t]*<!--\s*-->[ \t]*|[ \t]*<!--\s*$`)
)

// SummarySanitizer removes hidden Codex reasoning-summary boundaries while
// preserving one separator between the visible thoughts on either side. Its
// zero value is ready for use and must be scoped to one upstream stream.
type SummarySanitizer struct {
	hasVisibleText           bool
	previousEndedWithNewline bool
	separatorPending         bool
}

// StartSummaryPart records a native Codex reasoning-summary part boundary.
// Repeated boundary signals remain idempotent until visible text arrives.
func (s *SummarySanitizer) StartSummaryPart() {
	s.markBoundary()
}

// SanitizeDelta sanitizes one streaming reasoning-summary delta. A hidden
// boundary is held pending until the next visible delta so newlines supplied
// by either neighboring delta are respected regardless of chunk boundaries.
func (s *SummarySanitizer) SanitizeDelta(text string) string {
	text = leadingHTMLCommentClose.ReplaceAllString(text, "")
	parts := htmlCommentBoundary.Split(text, -1)

	var output strings.Builder
	for i, part := range parts {
		if i > 0 {
			s.markBoundary()
		}
		if part == "" {
			continue
		}
		if s.separatorPending {
			if s.hasVisibleText && !s.previousEndedWithNewline && !startsWithNewline(part) {
				output.WriteByte('\n')
			}
			s.separatorPending = false
		}
		output.WriteString(part)
		s.hasVisibleText = true
		s.previousEndedWithNewline = endsWithNewline(part)
	}
	return output.String()
}

func (s *SummarySanitizer) markBoundary() {
	s.separatorPending = s.separatorPending || s.hasVisibleText
}

// Reset clears all cross-delta state for a new response or turn.
func (s *SummarySanitizer) Reset() {
	*s = SummarySanitizer{}
}

// StripEmptyHTMLCommentMarkers removes private empty HTML comment markers from
// Codex reasoning summaries before they become client-visible reasoning.
func StripEmptyHTMLCommentMarkers(text string) string {
	var sanitizer SummarySanitizer
	return sanitizer.SanitizeDelta(text)
}

func startsWithNewline(text string) bool {
	return strings.HasPrefix(text, "\r") || strings.HasPrefix(text, "\n")
}

func endsWithNewline(text string) bool {
	return strings.HasSuffix(text, "\r") || strings.HasSuffix(text, "\n")
}
