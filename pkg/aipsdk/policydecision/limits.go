package policydecision

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Normalization bounds for evidence fields (design §Evidence Normalization
// Contract). Exported so operators, diagnostics, and tests can rely on the same
// bounds the normalizer applies.
const (
	MaxProviderIDBytes      = 128
	MaxIdentifierBytes      = 128
	MaxReasonCodeBytes      = 96
	MaxClientCategoryBytes  = 96
	MaxClientMessageBytes   = aipapi.MaxClientMessageBytes
	MaxAnnotationEntries    = 40
	MaxAnnotationKeyBytes   = 64
	MaxAnnotationValueBytes = 256
	truncatedAnnotationKey  = "truncated"
	truncatedAnnotationVal  = "true"
)

// unknownProviderID is the normalized provider ID when the source ID is empty after
// trimming (design §Evidence Normalization Contract).
const unknownProviderID = "unknown"

// unspecifiedReasonCode is the normalized reason code / client category when the
// source value is empty or not a safe token (design §Evidence Normalization
// Contract).
const unspecifiedReasonCode = "unspecified"

// NormalizeRecord returns a bounded, safe copy of record suitable for observer
// delivery and structured logging (requirements 7.3, 7.7). It clones maps and the
// embedded scope view, trims and bounds strings, normalizes reason codes and client
// categories to safe-token form, removes control characters from client messages,
// drops oversized or invalid annotation keys, truncates annotation values, and marks
// truncation with a bounded annotation. It does not validate legality; validation is
// performed by core before normalization is applied.
func NormalizeRecord(record Record) Record {
	out := record.Clone()

	out.Stage = trimSpaceBound(record.Stage, MaxIdentifierBytes)
	out.Provider.ID = trimSpaceBound(record.Provider.ID, MaxProviderIDBytes)
	out.Provider.Stage = trimSpaceBound(record.Provider.Stage, MaxIdentifierBytes)
	if out.Provider.ID == "" {
		out.Provider.ID = unknownProviderID
	}
	out.TraceID = trimSpaceBound(record.TraceID, MaxIdentifierBytes)
	out.ALegID = trimSpaceBound(record.ALegID, MaxIdentifierBytes)
	out.BLegID = trimSpaceBound(record.BLegID, MaxIdentifierBytes)
	out.ReasonCode = normalizeSafeToken(record.ReasonCode, MaxReasonCodeBytes)
	out.ClientCategory = normalizeSafeToken(record.ClientCategory, MaxClientCategoryBytes)
	out.ClientMessage = normalizeClientMessage(record.ClientMessage)
	out.Annotations = normalizeAnnotations(record.Annotations)

	return out
}

func trimSpaceBound(s string, maxLen int) string {
	t := strings.TrimSpace(s)
	return boundUTF8(t, maxLen)
}

func boundUTF8(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	cut := maxLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func normalizeSafeToken(s string, maxLen int) string {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" {
		return unspecifiedReasonCode
	}
	var b strings.Builder
	b.Grow(len(t))
	for _, r := range t {
		if isSafeTokenRune(r) {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return unspecifiedReasonCode
	}
	return boundUTF8(out, maxLen)
}

func isSafeTokenRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '_' || r == '-' || r == '.':
		return true
	}
	return false
}

// normalizeClientMessage delegates to the canonical aipapi.NormalizeClientMessage so the
// evidence path and the wire path apply identical bounds (design §Evidence Normalization
// Contract; requirements 5.4, 7.7).
func normalizeClientMessage(s string) string {
	return aipapi.NormalizeClientMessage(s)
}

func normalizeAnnotations(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	type entry struct{ key, val string }
	var entries []entry
	valueTruncated := false
	for k, v := range in {
		key := normalizeAnnotationKey(k)
		if key == "" {
			continue
		}
		val := stripControlChars(v)
		if len(val) > MaxAnnotationValueBytes {
			val = boundUTF8(val, MaxAnnotationValueBytes)
			valueTruncated = true
		}
		entries = append(entries, entry{key: key, val: val})
	}
	if len(entries) == 0 {
		return nil
	}
	// The truncation marker occupies one of the MaxAnnotationEntries slots; reserve
	// room for it whenever truncation will occur so the final map never exceeds the
	// bound (design §Evidence Normalization Contract).
	truncated := valueTruncated || len(entries) > MaxAnnotationEntries
	entryCap := MaxAnnotationEntries
	if truncated {
		entryCap = MaxAnnotationEntries - 1
	}
	out := make(map[string]string, min(len(entries), entryCap))
	for _, e := range entries {
		if len(out) >= entryCap {
			break
		}
		out[e.key] = e.val
	}
	if truncated {
		out[truncationMarkerKey(out)] = truncatedAnnotationVal
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func truncationMarkerKey(existing map[string]string) string {
	if _, ok := existing[truncatedAnnotationKey]; !ok {
		return truncatedAnnotationKey
	}
	for i := 1; i <= MaxAnnotationEntries; i++ {
		// ⚡ Bolt: replace fmt.Sprintf with direct string concatenation and strconv for performance
		key := truncatedAnnotationKey + "." + strconv.Itoa(i)
		if _, ok := existing[key]; !ok {
			return key
		}
	}
	// normalizeAnnotations reserves one slot for the marker, so a free candidate
	// above must exist for a bounded normalized map.
	return truncatedAnnotationKey
}

func normalizeAnnotationKey(k string) string {
	t := strings.TrimSpace(k)
	if t == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(t))
	for _, r := range t {
		if isAnnotationKeyRune(r) {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return ""
	}
	return boundUTF8(out, MaxAnnotationKeyBytes)
}

func isAnnotationKeyRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '_' || r == '.' || r == ':' || r == '-':
		return true
	}
	return false
}

func stripControlChars(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
