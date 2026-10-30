package engine

import (
	"bytes"
	"cmp"
	"slices"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// MatcherOptions controls redaction behavior for a Matcher.
// Zero value masks the full match with '*'.
type MatcherOptions struct {
	PreserveKnownPrefixes bool
	MaskByte              byte // 0 => '*'
}

// Matcher performs exact, case-sensitive secret matching against an immutable catalog.
// Scan and Redact are safe for concurrent use.
type Matcher struct {
	entries []catalogEntry
	ac      *ahoCorasick
	opts    MatcherOptions
}

// Occurrence is feature-private positional metadata for one exact match.
// It contains only safe catalog attribution and the caller-provided span; it
// never exposes catalog values or a catalog accessor.
type Occurrence struct {
	Start          int
	End            int
	SecretRefName  string
	Aliases        []string
	SourceCategory sdk.SourceCategory
}

// NewMatcher returns an immutable matcher bound to cat with default options
// (full-length mask, no known-prefix preservation).
// A nil or empty catalog yields a matcher that never matches.
func NewMatcher(cat *Catalog) *Matcher {
	return NewMatcherWithOptions(cat, MatcherOptions{})
}

// NewMatcherWithOptions returns an immutable matcher bound to cat using opts.
func NewMatcherWithOptions(cat *Catalog, opts MatcherOptions) *Matcher {
	if opts.MaskByte == 0 {
		opts.MaskByte = '*'
	}
	if cat == nil || len(cat.entries) == 0 {
		return &Matcher{opts: opts}
	}
	entries := make([]catalogEntry, len(cat.entries))
	for i := range cat.entries {
		e := cat.entries[i]
		entries[i] = catalogEntry{
			value:             bytes.Clone(e.value),
			knownPublicPrefix: bytes.Clone(e.knownPublicPrefix),
			primaryName:       e.primaryName,
			aliases:           slices.Clone(e.aliases),
			sourceCategory:    e.sourceCategory,
		}
	}
	return &Matcher{
		entries: entries,
		ac:      buildAhoCorasick(entries),
		opts:    opts,
	}
}

// ScanBytes reports safe findings for exact catalog matches in input.
func (m *Matcher) ScanBytes(input []byte) []sdk.Finding {
	if m == nil || len(m.entries) == 0 || len(input) == 0 {
		return nil
	}
	counts := m.matchCounts(input)
	return m.findingsFromCounts(counts)
}

// ScanString reports safe findings for exact catalog matches in input.
func (m *Matcher) ScanString(input string) []sdk.Finding {
	if input == "" {
		return nil
	}
	return m.ScanBytes([]byte(input))
}

// ScanOccurrences reports exact match spans for the supplied request content.
// The method is deliberately concrete-engine-only; the public SDK Matcher
// contract remains safe-finding based and unchanged.
func (m *Matcher) ScanOccurrences(input []byte) []Occurrence {
	if m == nil || len(m.entries) == 0 || len(input) == 0 {
		return nil
	}
	hits := m.selectMatches(input)
	if len(hits) == 0 {
		return nil
	}
	out := make([]Occurrence, 0, len(hits))
	for _, hit := range hits {
		entry := m.entries[hit.entryIdx]
		out = append(out, Occurrence{
			Start:          hit.start,
			End:            hit.start + hit.length,
			SecretRefName:  entry.primaryName,
			Aliases:        slices.Clone(entry.aliases),
			SourceCategory: entry.sourceCategory,
		})
	}
	return out
}

// RedactBytes returns a copy of input with matched spans masked.
// With PreserveKnownPrefixes, a match that begins with the entry's KnownPublicPrefix
// keeps those prefix bytes and masks only the remainder.
func (m *Matcher) RedactBytes(input []byte) (redacted []byte, findings []sdk.Finding) {
	if len(input) == 0 {
		return nil, nil
	}
	if m == nil || len(m.entries) == 0 {
		return bytes.Clone(input), nil
	}
	hits := m.selectMatches(input)
	if len(hits) == 0 {
		return bytes.Clone(input), nil
	}
	out := bytes.Clone(input)
	counts := make(map[int]int, len(hits))
	mask := m.opts.MaskByte
	for _, hit := range hits {
		prefixLen := 0
		if m.opts.PreserveKnownPrefixes {
			p := m.entries[hit.entryIdx].knownPublicPrefix
			if len(p) > 0 && len(p) < hit.length && hasPrefixAt(input, hit.start, p) {
				prefixLen = len(p)
			}
		}
		for j := prefixLen; j < hit.length; j++ {
			out[hit.start+j] = mask
		}
		counts[hit.entryIdx]++
	}
	return out, m.findingsFromCounts(counts)
}

// RedactString returns a copy of input with matched spans masked.
func (m *Matcher) RedactString(input string) (redacted string, findings []sdk.Finding) {
	if input == "" {
		return "", nil
	}
	out, findings := m.RedactBytes([]byte(input))
	return string(out), findings
}

func (m *Matcher) matchCounts(input []byte) map[int]int {
	counts := make(map[int]int)
	for _, hit := range m.selectMatches(input) {
		counts[hit.entryIdx]++
	}
	return counts
}

type matchHit struct {
	start    int
	length   int
	entryIdx int
}

func (m *Matcher) selectMatches(input []byte) []matchHit {
	raw := m.ac.findAll(input)
	if len(raw) == 0 {
		return nil
	}
	slices.SortFunc(raw, func(a, b matchHit) int {
		if c := cmp.Compare(a.start, b.start); c != 0 {
			return c
		}
		if c := cmp.Compare(b.length, a.length); c != 0 {
			return c
		}
		return cmp.Compare(m.entries[a.entryIdx].primaryName, m.entries[b.entryIdx].primaryName)
	})
	out := make([]matchHit, 0, len(raw))
	pos := 0
	for _, hit := range raw {
		if hit.start < pos {
			continue
		}
		out = append(out, hit)
		pos = hit.start + hit.length
	}
	return out
}

func (m *Matcher) findingsFromCounts(counts map[int]int) []sdk.Finding {
	if len(counts) == 0 {
		return nil
	}
	idxs := make([]int, 0, len(counts))
	for i := range counts {
		idxs = append(idxs, i)
	}
	slices.SortFunc(idxs, func(a, b int) int {
		return cmp.Compare(m.entries[a].primaryName, m.entries[b].primaryName)
	})
	out := make([]sdk.Finding, 0, len(idxs))
	for _, i := range idxs {
		e := m.entries[i]
		out = append(out, sdk.Finding{
			SecretRefName:   e.primaryName,
			Aliases:         slices.Clone(e.aliases),
			SourceCategory:  e.sourceCategory,
			OccurrenceCount: counts[i],
			DetectorID:      sdk.DetectorIDExact,
		})
	}
	return out
}

func hasPrefixAt(input []byte, offset int, prefix []byte) bool {
	if offset < 0 || len(prefix) == 0 || offset+len(prefix) > len(input) {
		return false
	}
	for i := range prefix {
		if input[offset+i] != prefix[i] {
			return false
		}
	}
	return true
}
