package aipapi

import "strings"

// JoinInstructionText concatenates non-empty text parts from a slice of system-style
// instruction messages, separated by a blank line, and trims outer whitespace.
// Non-text parts and empty/whitespace-only text parts are skipped.
func JoinInstructionText(insts []Message) string {
	var b strings.Builder
	for _, m := range insts {
		for _, p := range m.Parts {
			if p.Kind != PartText {
				continue
			}
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(b.String())
}
