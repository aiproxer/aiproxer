package economics

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// SupportAdvisoryVersionV1 identifies the first frozen support-advisory contract.
const SupportAdvisoryVersionV1 = "component-support-advisory-v1"

func validateSupportAdvisoryVersion(version string) error {
	if version == "" || version == SupportAdvisoryVersionV1 {
		return nil
	}
	return fmt.Errorf("%w: unsupported support advisory version %q", ErrInvalidTariffSnapshot, version)
}

// supportAdvisoryReasonsPerContext counts the distinct v1 incomplete reasons.
const supportAdvisoryReasonsPerContext = 4

// Public v1 bounds limit visibility without changing monetary rating.
const (
	MaxSupportAdvisoryPairs                 = 128
	MaxSupportAdvisoryContexts              = MaxValuationRefs + 3
	MaxSupportAdvisoryCandidateExaminations = 4096
	MaxSupportAdvisoryGraphVisits           = 65536
	MaxSupportAdvisoryIncompleteContexts    = MaxSupportAdvisoryContexts * supportAdvisoryReasonsPerContext
	// Scope keys embed subject JSON: nineteen bounded identity strings can
	// expand sixfold under JSON escaping, plus outer fields and framing.
	MaxSupportAdvisoryScopeKeyBytes = 64 * 1024
)

// SupportAdvisoryContext identifies the immutable source of an assessment.
type SupportAdvisoryContext struct {
	Version       string             `json:"version"`
	Tariff        RatingSnapshotRef  `json:"tariff"`
	TariffContent SnapshotContentRef `json:"tariff_content"`
}

// Key identifies reporting semantics independently of retrieval timestamps.
func (c SupportAdvisoryContext) Key() string {
	b, _ := json.Marshal([]string{c.Version, c.Tariff.ID, c.Tariff.Version, c.Tariff.RaterID, c.TariffContent.ContentRef, c.TariffContent.ContentHash})
	return string(b)
}

// SupportAdvisoryPair names unordered uncertain supports within one scope.
type SupportAdvisoryPair struct {
	ContextKey string                `json:"context_key"`
	ScopeKey   string                `json:"scope_key"`
	Left       metering.ComponentKey `json:"left"`
	Right      metering.ComponentKey `json:"right"`
}

// SupportAdvisoryReason explains why an assessment could not finish.
type SupportAdvisoryReason string

const (
	SupportAdvisoryCandidateBudget     SupportAdvisoryReason = "candidate_budget"
	SupportAdvisoryGraphBudget         SupportAdvisoryReason = "graph_budget"
	SupportAdvisoryPairLimit           SupportAdvisoryReason = "pair_limit"
	SupportAdvisoryEvidenceUnavailable SupportAdvisoryReason = "evidence_unavailable"
)

// SupportAdvisoryIncomplete records a context-wide reporting limitation.
type SupportAdvisoryIncomplete struct {
	ContextKey string                `json:"context_key"`
	Reason     SupportAdvisoryReason `json:"reason"`
}

// SupportAdvisoryReport carries visibility only, never amounts or quantities.
type SupportAdvisoryReport struct {
	Pairs              []SupportAdvisoryPair       `json:"pairs,omitempty"`
	IncompleteContexts []SupportAdvisoryIncomplete `json:"incomplete_contexts,omitempty"`
}

// Validate checks frozen advisory identity without resolving its content.
func (c SupportAdvisoryContext) Validate() error {
	if c.Version != SupportAdvisoryVersionV1 {
		return fmt.Errorf("%w: unsupported explicit support advisory version %q", ErrInvalidValuation, c.Version)
	}
	if err := validateRatingRef("support advisory tariff", c.Tariff); err != nil {
		return err
	}
	if err := c.TariffContent.Validate(); err != nil {
		return fmt.Errorf("%w: support advisory tariff content: %v", ErrInvalidValuation, err)
	}
	return nil
}

func (v Valuation) validateSupportAdvisory() error {
	// Bound raw slices before allocating indexes or accepting deduplication.
	if v.SupportAdvisoryContexts != nil && len(v.SupportAdvisoryContexts) == 0 {
		return fmt.Errorf("%w: explicit support advisory contexts cannot be empty", ErrInvalidValuation)
	}
	if len(v.SupportAdvisoryContexts) > MaxSupportAdvisoryContexts {
		return fmt.Errorf("%w: support advisory contexts exceed %d", ErrInvalidValuation, MaxSupportAdvisoryContexts)
	}
	if v.SupportAdvisory != nil {
		if len(v.SupportAdvisoryContexts) == 0 {
			return fmt.Errorf("%w: support advisory requires contexts", ErrInvalidValuation)
		}
		if len(v.SupportAdvisory.Pairs) > MaxSupportAdvisoryPairs {
			return fmt.Errorf("%w: support advisory pairs exceed %d", ErrInvalidValuation, MaxSupportAdvisoryPairs)
		}
		if len(v.SupportAdvisory.IncompleteContexts) > len(v.SupportAdvisoryContexts)*supportAdvisoryReasonsPerContext {
			return fmt.Errorf("%w: support advisory incomplete entries exceed context bound", ErrInvalidValuation)
		}
	}
	contexts := make(map[string]struct{}, len(v.SupportAdvisoryContexts))
	for _, c := range v.SupportAdvisoryContexts {
		if err := c.Validate(); err != nil {
			return err
		}
		contexts[c.Key()] = struct{}{}
	}
	if v.SupportAdvisory == nil {
		return nil
	}
	for _, p := range v.SupportAdvisory.Pairs {
		if _, ok := contexts[p.ContextKey]; !ok {
			return fmt.Errorf("%w: dangling support advisory pair context", ErrInvalidValuation)
		}
		if err := validateSupportScopeKey(p.ScopeKey); err != nil {
			return err
		}
		if err := p.Left.Validate(); err != nil {
			return fmt.Errorf("%w: support advisory left component: %v", ErrInvalidValuation, err)
		}
		if err := p.Right.Validate(); err != nil {
			return fmt.Errorf("%w: support advisory right component: %v", ErrInvalidValuation, err)
		}
		if string(p.Left.CanonicalBytes()) == string(p.Right.CanonicalBytes()) {
			return fmt.Errorf("%w: support advisory self-pair", ErrInvalidValuation)
		}
	}
	for _, entry := range v.SupportAdvisory.IncompleteContexts {
		if _, ok := contexts[entry.ContextKey]; !ok {
			return fmt.Errorf("%w: dangling support advisory incomplete context", ErrInvalidValuation)
		}
		switch entry.Reason {
		case SupportAdvisoryCandidateBudget, SupportAdvisoryGraphBudget, SupportAdvisoryPairLimit, SupportAdvisoryEvidenceUnavailable:
		default:
			return fmt.Errorf("%w: unsupported support advisory incomplete reason %q", ErrInvalidValuation, entry.Reason)
		}
	}
	return nil
}

func validateSupportScopeKey(key string) error {
	// Reduction scope identity is opaque here; its existing owner supplies
	// framing and semantics. Validate only the finite safe string envelope.
	if len(key) > MaxSupportAdvisoryScopeKeyBytes || !utf8.ValidString(key) {
		return fmt.Errorf("%w: invalid support advisory scope key envelope", ErrInvalidValuation)
	}
	if err := ValidateSafeRef("support advisory scope key", key); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValuation, err)
	}
	if strings.TrimSpace(key) != key {
		return fmt.Errorf("%w: support advisory scope key has surrounding whitespace", ErrInvalidValuation)
	}
	return nil
}

func (v *Valuation) canonicalizeSupportAdvisory() {
	// Timestamps are retrieval/publication metadata, not the frozen source
	// tuple. Removing them makes equivalent source contexts byte-identical.
	for i := range v.SupportAdvisoryContexts {
		v.SupportAdvisoryContexts[i].Tariff.EffectiveAt = time.Time{}
		v.SupportAdvisoryContexts[i].Tariff.FetchedAt = time.Time{}
	}
	slices.SortFunc(v.SupportAdvisoryContexts, func(a, b SupportAdvisoryContext) int { return strings.Compare(a.Key(), b.Key()) })
	v.SupportAdvisoryContexts = slices.CompactFunc(v.SupportAdvisoryContexts, func(a, b SupportAdvisoryContext) bool { return a.Key() == b.Key() })
	report := v.SupportAdvisory
	if report == nil {
		return
	}
	for i := range report.Pairs {
		pair := &report.Pairs[i]
		pair.Left, _ = pair.Left.Normalize()
		pair.Right, _ = pair.Right.Normalize()
		if strings.Compare(string(pair.Left.CanonicalBytes()), string(pair.Right.CanonicalBytes())) > 0 {
			pair.Left, pair.Right = pair.Right, pair.Left
		}
	}
	slices.SortFunc(report.Pairs, compareSupportAdvisoryPair)
	report.Pairs = slices.CompactFunc(report.Pairs, func(a, b SupportAdvisoryPair) bool { return compareSupportAdvisoryPair(a, b) == 0 })
	slices.SortFunc(report.IncompleteContexts, compareSupportAdvisoryIncomplete)
	report.IncompleteContexts = slices.Compact(report.IncompleteContexts)
	if len(report.Pairs) == 0 && len(report.IncompleteContexts) == 0 {
		v.SupportAdvisory = nil
	}
}

func compareSupportAdvisoryPair(a, b SupportAdvisoryPair) int {
	for _, pair := range [][2]string{{a.ContextKey, b.ContextKey}, {a.ScopeKey, b.ScopeKey}, {string(a.Left.CanonicalBytes()), string(b.Left.CanonicalBytes())}, {string(a.Right.CanonicalBytes()), string(b.Right.CanonicalBytes())}} {
		if cmp := strings.Compare(pair[0], pair[1]); cmp != 0 {
			return cmp
		}
	}
	return 0
}

func compareSupportAdvisoryIncomplete(a, b SupportAdvisoryIncomplete) int {
	if cmp := strings.Compare(a.ContextKey, b.ContextKey); cmp != 0 {
		return cmp
	}
	return strings.Compare(string(a.Reason), string(b.Reason))
}

// Clone copies all component dimensions and report slices independently.
func (r *SupportAdvisoryReport) Clone() *SupportAdvisoryReport {
	if r == nil {
		return nil
	}
	out := *r
	out.Pairs = slices.Clone(r.Pairs)
	for i := range out.Pairs {
		out.Pairs[i].Left = out.Pairs[i].Left.Clone()
		out.Pairs[i].Right = out.Pairs[i].Right.Clone()
	}
	out.IncompleteContexts = slices.Clone(r.IncompleteContexts)
	return &out
}
