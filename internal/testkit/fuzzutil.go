package testkit

import "strings"

// CapBytes returns a subslice of at most maxLen bytes (or b unchanged if maxLen <= 0).
func CapBytes(b []byte, maxLen int) []byte {
	if maxLen <= 0 || len(b) <= maxLen {
		return b
	}
	return b[:maxLen]
}

// CapString returns s truncated to maxLen runes-worth is not what we want — byte cap for wire fuzzing.
func CapString(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// DefaultFuzzRouteSelector is used when a fuzz-packed selector decodes to empty.
const DefaultFuzzRouteSelector = "stub:model"

// SplitFuzzPackedRouteBody splits one fuzz blob into a route selector prefix and HTTP body.
// Layout: [1 byte: selLen][selector bytes][body bytes]. selLen is capped by maxSel and available bytes.
func SplitFuzzPackedRouteBody(b []byte, maxSel, maxBody int) (selector string, body []byte) {
	if len(b) == 0 {
		return DefaultFuzzRouteSelector, nil
	}
	selN := int(b[0])
	if maxSel > 0 && selN > maxSel {
		selN = maxSel
	}
	if len(b) <= 1 {
		return DefaultFuzzRouteSelector, CapBytes(nil, maxBody)
	}
	avail := len(b) - 1
	if selN > avail {
		selN = avail
	}
	selBytes := b[1 : 1+selN]
	body = CapBytes(b[1+selN:], maxBody)
	selector = string(selBytes)
	if strings.TrimSpace(selector) == "" {
		selector = DefaultFuzzRouteSelector
	}
	return selector, body
}
