package db

import (
	"strconv"
	"strings"
)

// PersistedKeyV2 returns the injective, NUL-free persisted form of a keyed
// identity: a version tag followed by each part length-framed.
//
// Framing every part with its own length makes the mapping injective, so
// distinct part tuples can never share a key, and the "<tag>:v2:" prefix is
// disjoint from the legacy persisted forms, so the encodings can never be
// confused. The result contains no NUL, which Bun's SQL formatter requires.
func PersistedKeyV2(tag string, parts ...string) string {
	var b strings.Builder
	b.Grow(len(tag) + 16)
	b.WriteString(tag)
	b.WriteString(":v2:")
	for _, part := range parts {
		b.WriteString(strconv.Itoa(len(part)))
		b.WriteByte(':')
		b.WriteString(part)
	}
	return b.String()
}

// PersistedIdentityKeyV2 returns the injective persisted form of a
// NUL-delimited source identity preimage.
//
// LegacyPersistedText drops the NUL delimiters, so adjacent identity fields
// concatenate and two distinct observations can persist one key (ADR 0009).
// Splitting the preimage on NUL is lossless and framing every field with its
// length is injective over field sequences, so the "obs:v2:" encoding can
// never collide with another observation or with a legacy persisted key.
func PersistedIdentityKeyV2(preimage string) string {
	return PersistedKeyV2("obs", strings.Split(preimage, "\x00")...)
}
