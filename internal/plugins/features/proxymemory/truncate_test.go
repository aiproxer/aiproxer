package proxymemory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateApproxTokens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want int
	}{
		{name: "empty", in: "", want: 0},
		{name: "one byte", in: "a", want: 1},
		{name: "four bytes", in: "abcd", want: 1},
		{name: "five bytes", in: "abcde", want: 2},
		{name: "forty bytes", in: strings.Repeat("x", 40), want: 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ApproxTokens(tc.in); got != tc.want {
				t.Errorf("ApproxTokens(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestTruncateTokens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{
			name:  "under the limit is unchanged",
			in:    "hello world",
			limit: 10,
			want:  "hello world",
		},
		{
			name:  "exactly at the limit is unchanged",
			in:    strings.Repeat("a", 40),
			limit: 10,
			want:  strings.Repeat("a", 40),
		},
		{
			name:  "over the limit keeps the head and tail with the removed token count",
			in:    strings.Repeat("0123456789", 6),
			limit: 5,
			want:  "0123456789\n…[10 tokens truncated]…\n0123456789",
		},
		{
			name:  "a mid-rune head cut moves back to the rune boundary",
			in:    "a" + strings.Repeat("é", 10) + strings.Repeat("x", 40),
			limit: 4,
			want:  "aééé\n…[12 tokens truncated]…\nxxxxxxxx",
		},
		{
			name:  "a mid-rune tail cut moves forward to the rune boundary",
			in:    strings.Repeat("x", 30) + strings.Repeat("☃", 10),
			limit: 5,
			want:  "xxxxxxxxxx\n…[11 tokens truncated]…\n☃☃☃",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := TruncateTokens(tc.in, tc.limit)
			if got != tc.want {
				t.Errorf("TruncateTokens(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("TruncateTokens(%q, %d) = %q is not valid UTF-8", tc.in, tc.limit, got)
			}
		})
	}
}
