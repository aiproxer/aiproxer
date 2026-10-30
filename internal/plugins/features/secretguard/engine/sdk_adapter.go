package engine

import (
	"context"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

type matcherAdapter struct {
	m *Matcher
}

// AsMatcher adapts *Matcher to sdk.Matcher.
func AsMatcher(m *Matcher) sdk.Matcher {
	if m == nil {
		m = &Matcher{}
	}
	return matcherAdapter{m: m}
}

func (a matcherAdapter) ScanBytes(ctx context.Context, input []byte) ([]sdk.Finding, error) {
	_ = ctx
	return a.m.ScanBytes(input), nil
}

func (a matcherAdapter) ScanString(ctx context.Context, input string) ([]sdk.Finding, error) {
	_ = ctx
	return a.m.ScanString(input), nil
}

// ScanOccurrences is the neutral positional extension. It accepts only actual
// request content and converts the engine's safe attribution to the SDK shape;
// no catalog value crosses this adapter.
func (a matcherAdapter) ScanOccurrences(input []byte) []sdk.PositionalOccurrence {
	occurrences := a.m.ScanOccurrences(input)
	if len(occurrences) == 0 {
		return nil
	}
	out := make([]sdk.PositionalOccurrence, 0, len(occurrences))
	for _, occurrence := range occurrences {
		out = append(out, sdk.PositionalOccurrence{
			Start: occurrence.Start,
			End:   occurrence.End,
			Finding: sdk.Finding{
				SecretRefName:   occurrence.SecretRefName,
				Aliases:         append([]string(nil), occurrence.Aliases...),
				SourceCategory:  occurrence.SourceCategory,
				OccurrenceCount: 1,
			},
		})
	}
	return out
}

func (a matcherAdapter) RedactBytes(ctx context.Context, input []byte) ([]byte, []sdk.Finding, error) {
	_ = ctx
	out, findings := a.m.RedactBytes(input)
	return out, findings, nil
}

func (a matcherAdapter) RedactString(ctx context.Context, input string) (string, []sdk.Finding, error) {
	_ = ctx
	out, findings := a.m.RedactString(input)
	return out, findings, nil
}

// RedactionOptions is consumed by feature-private transient matchers so their
// rewrites use the same configured mask byte as the exact matcher.
func (a matcherAdapter) RedactionOptions() MatcherOptions {
	if a.m == nil {
		return MatcherOptions{}
	}
	return a.m.opts
}

type staticMatcherResolver struct {
	m sdk.Matcher
}

// NewStaticMatcherResolver returns a MatcherResolver that always resolves to a matcher for cat.
// Callers pass MatcherOptions; composition typically enables PreserveKnownPrefixes.
func NewStaticMatcherResolver(cat *Catalog, opts MatcherOptions) sdk.MatcherResolver {
	return staticMatcherResolver{m: AsMatcher(NewMatcherWithOptions(cat, opts))}
}

func (r staticMatcherResolver) Resolve(ctx context.Context) (sdk.Matcher, error) {
	_ = ctx
	return r.m, nil
}

var (
	_ sdk.Matcher           = matcherAdapter{}
	_ sdk.PositionalMatcher = matcherAdapter{}
	_ sdk.MatcherResolver   = staticMatcherResolver{}
)
