package source

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func digestItems(items []aipapi.Item) string {
	h := sha256.New()
	for _, item := range items {
		b, _ := json.Marshal(item)
		_, _ = h.Write(fmt.Appendf(nil, "%08x:", len(b)))
		_, _ = h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isZeroWatermark(w HighWatermark) bool { return w.ItemCount == 0 && w.Digest == "" }

func normalizeText(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

func truncateUTF8(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	b := []byte(s)
	if max <= 3 {
		cut := max
		for cut > 0 && !utf8.Valid(b[:cut]) {
			cut--
		}
		return string(b[:cut])
	}
	cut := max - 3
	for cut > 0 && !utf8.Valid(b[:cut]) {
		cut--
	}
	return string(b[:cut]) + "..."
}

func truncateEntryText(text string, untrusted bool, max int) string {
	if !untrusted {
		return truncateUTF8(text, max)
	}
	if !strings.HasPrefix(text, UntrustedOpen) || !strings.HasSuffix(text, UntrustedClose) {
		return truncateUTF8(text, max)
	}
	innerMax := max - len(UntrustedOpen) - len(UntrustedClose)
	if innerMax <= 0 {
		return ""
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(text, UntrustedOpen), UntrustedClose)
	return UntrustedOpen + truncateUTF8(inner, innerMax) + UntrustedClose
}

func boundUntrusted(text string, max int) string {
	innerMax := max - len(UntrustedOpen) - len(UntrustedClose)
	if innerMax < 1 {
		return ""
	}
	return truncateUTF8(text, innerMax)
}

func likelyDump(text string) bool {
	if strings.Contains(text, "```") || strings.Contains(text, "\x00") {
		return true
	}
	lines := strings.Split(text, "\n")
	if len(lines) >= 4 {
		logLines := 0
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "at ") || strings.HasPrefix(trimmed, "ERROR ") || strings.HasPrefix(trimmed, "INFO ") || strings.HasPrefix(trimmed, "$ ") || strings.HasPrefix(trimmed, "> ") {
				logLines++
			}
		}
		if logLines*2 >= len(lines) {
			return true
		}
	}
	return false
}

func toolOutputRelevant(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "todo") || strings.Contains(lower, "decision") || strings.Contains(lower, "constraint") || strings.Contains(lower, "plan status") || strings.Contains(lower, "task status")
}
