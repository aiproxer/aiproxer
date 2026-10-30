package engine_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/internal/testkit"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func FuzzMatcher_ScanRedactRoundTrip(f *testing.F) {
	cat, err := engine.BuildCatalog([]engine.CatalogInput{
		{
			Name:           "OPENAI_API_KEY",
			Value:          testkit.SyntheticOpenAIAPIKey,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
		{
			Name:           "UNICODE_SECRET",
			Value:          testkit.SyntheticUnicodeSecret,
			SourceCategory: sdk.SourceCategoryOperatorEnv,
		},
	}, 8)
	if err != nil {
		f.Fatal(err)
	}
	m := engine.NewMatcher(cat)

	f.Add([]byte("plain"))
	f.Add([]byte(testkit.SyntheticOpenAIAPIKey))
	f.Add([]byte("x" + testkit.SyntheticUnicodeSecret + "y"))

	f.Fuzz(func(t *testing.T, input []byte) {
		findings := m.ScanBytes(input)
		assertFindingsNeverContainSecretValues(t, findings)
		out, findings2 := m.RedactBytes(input)
		assertFindingsNeverContainSecretValues(t, findings2)
		if len(out) != len(input) {
			t.Fatalf("redact length=%d want %d", len(out), len(input))
		}
		for _, secret := range []string{testkit.SyntheticOpenAIAPIKey, testkit.SyntheticUnicodeSecret} {
			if bytes.Contains(out, []byte(secret)) {
				t.Fatal("redacted output still contains a catalog secret")
			}
			if strings.Contains(string(out), secret) {
				t.Fatal("redacted string still contains a catalog secret")
			}
		}
	})
}
