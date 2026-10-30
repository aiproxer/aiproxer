package proxymemory

import (
	"strconv"
	"unicode/utf8"
)

// ApproxTokens is the Codex byte-per-token approximation (design §6):
// (len(s)+3)/4.
func ApproxTokens(s string) int {
	return (len(s) + 3) / 4
}

// TruncateTokens shortens s to approximately limit tokens (design §6). When s
// already fits, it is returned unchanged. Otherwise the result keeps the first
// and the last limit*2 bytes, each adjusted to a UTF-8 rune boundary, joined
// by a marker naming the approximate token count removed from the middle. The
// result of valid UTF-8 input is always valid UTF-8.
func TruncateTokens(s string, limit int) string {
	if ApproxTokens(s) <= limit {
		return s
	}
	headEnd := min(max(limit*2, 0), len(s))
	for headEnd > 0 && headEnd < len(s) && !utf8.RuneStart(s[headEnd]) {
		headEnd--
	}
	tailStart := min(max(len(s)-limit*2, 0), len(s))
	for tailStart < len(s) && !utf8.RuneStart(s[tailStart]) {
		tailStart++
	}
	removedTokens := ApproxTokens(s[headEnd:tailStart])
	return s[:headEnd] + "\n…[" + strconv.Itoa(removedTokens) + " tokens truncated]…\n" + s[tailStart:]
}
