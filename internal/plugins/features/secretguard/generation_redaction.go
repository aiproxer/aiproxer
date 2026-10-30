package secretguard

import (
	"bytes"
	"context"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// generationRedactionMatcher keeps presentation policy separate from request
// credential attribution. The matcher supplies safe ranges; the generation
// decides how those ranges are masked.
type generationRedactionMatcher struct {
	sdk.Matcher
	positional sdk.PositionalMatcher
	options    engine.MatcherOptions
}

func (m generationRedactionMatcher) RedactionOptions() engine.MatcherOptions { return m.options }
func (m generationRedactionMatcher) ScanOccurrences(input []byte) []sdk.PositionalOccurrence {
	if m.positional == nil {
		return nil
	}
	return m.positional.ScanOccurrences(input)
}

func (m generationRedactionMatcher) RedactBytes(ctx context.Context, input []byte) ([]byte, []sdk.Finding, error) {
	if m.positional == nil {
		return m.Matcher.RedactBytes(ctx, input)
	}
	if configured, ok := m.Matcher.(interface{ RedactionOptions() engine.MatcherOptions }); ok && configured.RedactionOptions() == m.options {
		return m.Matcher.RedactBytes(ctx, input)
	}
	findings, err := m.ScanBytes(ctx, input)
	if err != nil {
		return nil, findings, err
	}
	out := bytes.Clone(input)
	for _, occurrence := range m.ScanOccurrences(input) {
		if occurrence.Start < 0 || occurrence.End <= occurrence.Start || occurrence.End > len(input) {
			continue
		}
		start := occurrence.Start
		if m.options.PreserveKnownPrefixes {
			prefix := engine.DetectKnownPublicPrefix(string(input[start:occurrence.End]))
			if len(prefix) < occurrence.End-start {
				start += len(prefix)
			}
		}
		for index := start; index < occurrence.End; index++ {
			out[index] = m.options.MaskByte
		}
	}
	return out, findings, nil
}

func (m generationRedactionMatcher) RedactString(ctx context.Context, input string) (string, []sdk.Finding, error) {
	out, findings, err := m.RedactBytes(ctx, []byte(input))
	return string(out), findings, err
}
