package reasoning

import (
	"strings"
	"testing"
)

func TestStripEmptyHTMLCommentMarkers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty comment marker", "<!-- -->", ""},
		{"inline empty comment", "start<!-- -->end", "start\nend"},
		{"trailing comment open", "**Plan**\n\n<!--", "**Plan**\n\n"},
		{"leading comment close", " -->text", "text"},
		{"split close only", " -->", ""},
		{"non-empty comment preserved", "<!-- note -->", "<!-- note -->"},
		{"text unchanged", "plain reasoning", "plain reasoning"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := StripEmptyHTMLCommentMarkers(tc.input)
			if got != tc.expected {
				t.Fatalf("StripEmptyHTMLCommentMarkers(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestSummarySanitizer_preservesThoughtSeparatorAcrossDeltas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		deltas []string
		want   string
	}{
		{
			name:   "marker supplies missing separator",
			deltas: []string{"thought", "<!--", " -->", "next"},
			want:   "thought\nnext",
		},
		{
			name:   "previous delta supplies newline",
			deltas: []string{"thought\n", "<!--", " -->", "next"},
			want:   "thought\nnext",
		},
		{
			name:   "next delta supplies newline",
			deltas: []string{"thought", "<!--", " -->", "\nnext"},
			want:   "thought\nnext",
		},
		{
			name:   "complete marker between thoughts",
			deltas: []string{"thought<!-- -->next"},
			want:   "thought\nnext",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sanitizer SummarySanitizer
			var got strings.Builder
			for _, delta := range tc.deltas {
				got.WriteString(sanitizer.SanitizeDelta(delta))
			}
			if got.String() != tc.want {
				t.Fatalf("sanitized stream = %q, want %q", got.String(), tc.want)
			}
		})
	}
}

func TestSummarySanitizer_resetClearsPendingSeparator(t *testing.T) {
	t.Parallel()

	var sanitizer SummarySanitizer
	if got := sanitizer.SanitizeDelta("old<!--"); got != "old" {
		t.Fatalf("first delta = %q, want %q", got, "old")
	}
	sanitizer.Reset()
	if got := sanitizer.SanitizeDelta("new"); got != "new" {
		t.Fatalf("delta after reset = %q, want %q", got, "new")
	}
}

func TestSummarySanitizer_preservesSummaryPartSeparators(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		first string
		next  string
		want  string
	}{
		{name: "boundary supplies missing separator", first: "first", next: "second", want: "first\nsecond"},
		{name: "previous part supplies newline", first: "first\n", next: "second", want: "first\nsecond"},
		{name: "next part supplies newline", first: "first", next: "\nsecond", want: "first\nsecond"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sanitizer SummarySanitizer
			sanitizer.StartSummaryPart()
			got := sanitizer.SanitizeDelta(tc.first)
			sanitizer.StartSummaryPart()
			got += sanitizer.SanitizeDelta(tc.next)
			if got != tc.want {
				t.Fatalf("sanitized parts = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSummarySanitizer_summaryPartBoundaryIsIdempotentWithMarker(t *testing.T) {
	t.Parallel()

	var sanitizer SummarySanitizer
	sanitizer.StartSummaryPart()
	got := sanitizer.SanitizeDelta("first")
	sanitizer.StartSummaryPart()
	sanitizer.StartSummaryPart()
	got += sanitizer.SanitizeDelta("<!--")
	got += sanitizer.SanitizeDelta(" -->")
	got += sanitizer.SanitizeDelta("second")
	if got != "first\nsecond" {
		t.Fatalf("sanitized parts = %q, want %q", got, "first\nsecond")
	}
}
