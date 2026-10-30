package auth

import (
	"bytes"
	"cmp"
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// exactCredentialMatcher holds presented request credential bytes privately and matches
// exact occurrences for secret-guard scanning/redaction.
type exactCredentialMatcher struct {
	secret  []byte
	refName string
}

func newExactCredentialMatcher(presented, keyID string) secretguard.Matcher {
	presented = strings.TrimSpace(presented)
	if presented == "" {
		return nil
	}
	return &exactCredentialMatcher{secret: bytes.Clone([]byte(presented)), refName: cmp.Or(strings.TrimSpace(keyID), "request_credential")}
}

func (m *exactCredentialMatcher) finding(n int) secretguard.Finding {
	return secretguard.Finding{
		SecretRefName:   m.refName,
		SourceCategory:  secretguard.SourceCategoryRequestCred,
		OccurrenceCount: n,
	}
}

func (m *exactCredentialMatcher) ScanBytes(_ context.Context, input []byte) ([]secretguard.Finding, error) {
	if m == nil || len(m.secret) == 0 {
		return nil, nil
	}
	n := bytes.Count(input, m.secret)
	if n == 0 {
		return nil, nil
	}
	return []secretguard.Finding{m.finding(n)}, nil
}

func (m *exactCredentialMatcher) ScanString(ctx context.Context, input string) ([]secretguard.Finding, error) {
	return m.ScanBytes(ctx, []byte(input))
}

// ScanOccurrences implements the optional neutral positional capability. It
// returns only offsets and safe accepted-credential attribution; the matcher
// retains the credential bytes privately.
func (m *exactCredentialMatcher) ScanOccurrences(input []byte) []secretguard.PositionalOccurrence {
	if m == nil || len(m.secret) == 0 || len(input) < len(m.secret) {
		return nil
	}
	var out []secretguard.PositionalOccurrence
	for from := 0; from <= len(input)-len(m.secret); {
		relative := bytes.Index(input[from:], m.secret)
		if relative < 0 {
			break
		}
		start := from + relative
		out = append(out, secretguard.PositionalOccurrence{
			Start:   start,
			End:     start + len(m.secret),
			Finding: m.finding(1),
		})
		from = start + len(m.secret)
	}
	return out
}

// RedactBytes is a fallback that masks the full credential with asterisks.
// Generation-owned SecretGuard redaction applies configured MaskByte and
// PreserveKnownPrefixes policy through the positional capability instead.
func (m *exactCredentialMatcher) RedactBytes(ctx context.Context, input []byte) ([]byte, []secretguard.Finding, error) {
	findings, err := m.ScanBytes(ctx, input)
	if err != nil || len(findings) == 0 {
		return bytes.Clone(input), findings, err
	}
	return bytes.ReplaceAll(input, m.secret, bytes.Repeat([]byte("*"), len(m.secret))), findings, nil
}

func (m *exactCredentialMatcher) RedactString(ctx context.Context, input string) (string, []secretguard.Finding, error) {
	out, findings, err := m.RedactBytes(ctx, []byte(input))
	return string(out), findings, err
}

var _ secretguard.PositionalMatcher = (*exactCredentialMatcher)(nil)
