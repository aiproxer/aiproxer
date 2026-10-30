package metering

import (
	"fmt"
	"strconv"
	"strings"
)

// IdentityVersionV1 is the current canonical source-event identity version.
// Historical producers that omit IdentityVersion (JSON zero) are treated as V1.
const IdentityVersionV1 = 1

// MaxSourceEventFieldLen bounds lifecycle ID, source ID, and event-kind strings
// in SourceEventRef encodings (design Deterministic Identity: bounded key).
const MaxSourceEventFieldLen = 512

// SourceEventRef is the deterministic identity of one economic source event
// (design Deterministic Identity / D6): identity version, lifecycle ID, boundary,
// event kind, source ID, and revision. Sequence and FactID are stream membership
// fields, not part of this ref.
type SourceEventRef struct {
	IdentityVersion int
	LifecycleID     string
	Boundary        Boundary
	EventKind       string
	SourceID        string
	SourceRevision  int64
}

// EffectiveIdentityVersion returns IdentityVersionV1 when IdentityVersion is 0
// (historical producers) and otherwise the declared version.
func (r SourceEventRef) EffectiveIdentityVersion() int {
	if r.IdentityVersion == 0 {
		return IdentityVersionV1
	}
	return r.IdentityVersion
}

// CanonicalKey returns a length-prefixed, delimiter-safe encoding of the ref.
// Field values may contain NUL or ':' without shifting later fields.
func (r SourceEventRef) CanonicalKey() string {
	var b strings.Builder
	b.Grow(64)
	appendLenPrefixed(&b, strconv.Itoa(r.EffectiveIdentityVersion()))
	appendLenPrefixed(&b, r.LifecycleID)
	appendLenPrefixed(&b, string(r.Boundary))
	appendLenPrefixed(&b, r.EventKind)
	appendLenPrefixed(&b, r.SourceID)
	appendLenPrefixed(&b, strconv.FormatInt(r.SourceRevision, 10))
	return b.String()
}

func appendLenPrefixed(b *strings.Builder, s string) {
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
}

// SourceEventRef builds the deterministic identity ref for this fact.
// Lifecycle ID is the durable stream identifier (StreamID). Empty SourceEventKind
// defaults to Kind; empty SourceID defaults to FactID (V1 producer compatibility).
func (f Fact) SourceEventRef() SourceEventRef {
	kind := strings.TrimSpace(f.SourceEventKind)
	if kind == "" {
		kind = string(f.Kind)
	}
	sourceID := strings.TrimSpace(f.SourceID)
	if sourceID == "" {
		sourceID = strings.TrimSpace(f.FactID)
	}
	return SourceEventRef{
		IdentityVersion: f.IdentityVersion,
		LifecycleID:     strings.TrimSpace(f.StreamID),
		Boundary:        f.Boundary,
		EventKind:       kind,
		SourceID:        sourceID,
		SourceRevision:  f.SourceRevision,
	}
}

// EffectiveIdentityVersion returns the V1-compatible identity version for f.
func (f Fact) EffectiveIdentityVersion() int {
	return f.SourceEventRef().EffectiveIdentityVersion()
}

// LegacySourceEventKeyPhase31 returns the exact NUL-delimited SourceEventKey
// encoding written by task 3.1 before length-prefixed CanonicalKey. It uses the
// literal IdentityVersion field (including 0), not EffectiveIdentityVersion.
func (f Fact) LegacySourceEventKeyPhase31() string {
	kind := strings.TrimSpace(f.SourceEventKind)
	if kind == "" {
		kind = string(f.Kind)
	}
	sourceID := strings.TrimSpace(f.SourceID)
	if sourceID == "" {
		sourceID = strings.TrimSpace(f.FactID)
	}
	lifecycleID := strings.TrimSpace(f.StreamID)
	// ⚡ Bolt: replace fmt.Sprintf with direct string concatenation and strconv for performance
	return strconv.Itoa(f.IdentityVersion) + "\x00" +
		lifecycleID + "\x00" +
		string(f.Boundary) + "\x00" +
		kind + "\x00" +
		sourceID + "\x00" +
		strconv.FormatInt(f.SourceRevision, 10)
}

// SourceEventLookupKeys returns durable lookup candidates in order: current
// canonical SourceEventKey, phase-3.1 NUL legacy key (literal IdentityVersion),
// bidirectional V0/V1 NUL aliases when EffectiveIdentityVersion is V1, then
// IdempotencyKey. IdentityVersion >= 2 adds no V0/V1 aliases. Duplicates are
// omitted while preserving order.
func (f Fact) SourceEventLookupKeys() []string {
	candidates := []string{
		f.SourceEventKey(),
		f.LegacySourceEventKeyPhase31(),
	}
	if f.EffectiveIdentityVersion() == IdentityVersionV1 {
		v0 := f
		v0.IdentityVersion = 0
		v1 := f
		v1.IdentityVersion = IdentityVersionV1
		candidates = append(
			candidates,
			v0.LegacySourceEventKeyPhase31(),
			v1.LegacySourceEventKeyPhase31(),
		)
	}
	candidates = append(candidates, f.IdempotencyKey())
	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, k := range candidates {
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

func validateSourceEventField(name, value string) error {
	if len(value) > MaxSourceEventFieldLen {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidFact, name, MaxSourceEventFieldLen)
	}
	return nil
}
