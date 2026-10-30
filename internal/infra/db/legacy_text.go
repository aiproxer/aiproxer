package db

import "strings"

// LegacyPersistedText returns the exact bytes Bun <= 1.2 stored for a string
// query argument: Bun's formatter silently dropped every NUL byte. Bun 1.3
// rejects NUL instead, so a key whose in-memory form is NUL-delimited must be
// converted explicitly to keep matching rows written by earlier releases.
func LegacyPersistedText(s string) string {
	return strings.ReplaceAll(s, "\x00", "")
}
