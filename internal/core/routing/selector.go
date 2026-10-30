package routing

import (
	"net/url"
	"strings"
	"time"
)

// Selector is the root AST: an ordered failover chain (left-to-right).
type Selector struct {
	Alternatives      []FailoverAlt
	GlobalTTFTTimeout *time.Duration
	Affinity          AffinityMode
}

// AffinityMode controls route-wide backend stickiness. It is intentionally global
// selector metadata because affinity constrains the whole planning process.
type AffinityMode string

const (
	AffinityNone    AffinityMode = ""
	AffinitySession AffinityMode = "session"
	AffinityClient  AffinityMode = "client"
)

// FailoverAlt is one arm of a failover selector (before the next '|').
// Exactly one of Primary, Weighted, or Parallel is non-nil.
type FailoverAlt struct {
	Primary  *Primary
	Weighted *Weighted
	Parallel *Parallel
}

// Parallel is a set of branches executed concurrently; the first to produce
// a non-whitespace content delta wins. Branches are separated by '!' in the selector string.
type Parallel struct {
	Branches []ParallelBranch
}

// ParallelBranch is one leg of a parallel group.
type ParallelBranch struct {
	Target   Primary
	Handicap time.Duration
}

// Primary is a concrete backend:model (or model-only) with optional query parameters.
type Primary struct {
	// Backend is empty for model-only selectors. It may contain dots (e.g. openai.azure).
	Backend string
	// Model is the requested/logical identity (canonical or raw pass-through).
	// Route keys, affinity, catalog matching, traces, and diagnostics use Model.
	Model string
	// NativeModel is the request-bound backend wire id when the registry mapped
	// this leaf. Empty means unresolved pass-through (use Model at seams).
	// NativeModel must not appear in String()/exclusion keys.
	NativeModel string
	Params      url.Values
	Size        RequestSizeConstraint
	TTFTTimeout *time.Duration
}

// TrimmedParam returns the first query parameter value with surrounding whitespace removed.
func (p Primary) TrimmedParam(key string) string {
	if p.Params == nil {
		return ""
	}
	return strings.TrimSpace(p.Params.Get(key))
}

// RequestSizeConstraint carries per-leaf request token eligibility bounds.
type RequestSizeConstraint struct {
	MinContextTokens *int64
	MaxContextTokens *int64
}

// Weighted is a set of branches; exactly one is chosen per planning step using weights.
type Weighted struct {
	Branches []WeightedBranch
}

// WeightedBranch is one weighted arm after splitting on '^'.
type WeightedBranch struct {
	Weight int
	// IsFirst marks a [first] annotation (only meaningful when not on retry path and session allows).
	IsFirst bool
	// IsThinker marks a [thinker] annotation; the branch produces a planning memo
	// in interleaved-thinking cycles. At most one thinker branch is allowed per
	// weighted group, and it cannot combine with [first] on the same branch.
	IsThinker bool
	// Target is the resolved primary for this branch. Zero when Parallel is set.
	Target Primary
	// Parallel, when non-nil, marks this branch as targeting an embedded parallel
	// executor group. Allowed only on the non-thinker branch of a thinker hybrid
	// weighted selector; the parser enforces that narrow shape. Target is zero in
	// that case. General weighted/parallel mixing remains rejected.
	Parallel *Parallel
}

// String returns a stable diagnostic key for a primary (exclusion / health maps).
func (p Primary) String() string {
	var b strings.Builder
	if p.Backend != "" {
		b.WriteString(p.Backend)
		b.WriteByte(':')
	}
	b.WriteString(p.Model)
	if len(p.Params) > 0 {
		b.WriteByte('?')
		b.WriteString(p.Params.Encode())
	}
	return b.String()
}
