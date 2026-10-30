package engine_test

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/internal/testkit"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func assertFindingsNeverContainSecretValues(t *testing.T, findings []sdk.Finding) {
	t.Helper()
	for _, secret := range testkit.AllSyntheticSecretGuardValues() {
		for _, f := range findings {
			if strings.Contains(f.SecretRefName, secret) ||
				strings.Contains(f.Location, secret) ||
				strings.Contains(string(f.SourceCategory), secret) {
				t.Fatal("finding metadata must not contain secret value material")
			}
			for _, a := range f.Aliases {
				if strings.Contains(a, secret) {
					t.Fatal("finding aliases must not contain secret value material")
				}
			}
			dump := fmt.Sprintf("%+v", f)
			if strings.Contains(dump, secret) {
				t.Fatal("finding dump must not contain secret value material")
			}
		}
	}
}

func mustCatalog(t *testing.T, in []engine.CatalogInput) *engine.Catalog {
	t.Helper()
	cat, err := engine.BuildCatalog(in, 8)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func TestMatcher_OverlapLongestWins(t *testing.T) {
	t.Parallel()
	cat := mustCatalog(t, []engine.CatalogInput{
		{
			Name:           "OVERLAP_LONGER",
			Value:          testkit.SyntheticOverlapLonger,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
		{
			Name:           "OVERLAP_SHORTER",
			Value:          testkit.SyntheticOverlapShorter,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
	})
	m := engine.NewMatcher(cat)
	input := "start " + testkit.SyntheticOverlapLonger + " end"
	findings := m.ScanString(input)
	assertFindingsNeverContainSecretValues(t, findings)
	if len(findings) != 1 {
		t.Fatalf("findings len=%d want 1 (longest wins)", len(findings))
	}
	if findings[0].SecretRefName != "OVERLAP_LONGER" {
		t.Fatalf("SecretRefName=%q want OVERLAP_LONGER", findings[0].SecretRefName)
	}
	if findings[0].OccurrenceCount != 1 {
		t.Fatalf("OccurrenceCount=%d want 1", findings[0].OccurrenceCount)
	}
	if findings[0].DetectorID != sdk.DetectorIDExact {
		t.Fatalf("DetectorID=%q want %q", findings[0].DetectorID, sdk.DetectorIDExact)
	}
	if findings[0].Location != "" {
		t.Fatalf("Location must be empty at matcher level, got %q", findings[0].Location)
	}
}

func TestMatcher_CaseSensitive_noMatchOnCaseFold(t *testing.T) {
	t.Parallel()
	const catalogValue = "sk-AbCdEfGh"
	cat := mustCatalog(t, []engine.CatalogInput{{
		Name:           "CASE_SENSITIVE_KEY",
		Value:          catalogValue,
		SourceCategory: sdk.SourceCategoryProxyEnv,
	}})
	m := engine.NewMatcher(cat)
	for _, input := range []string{"prefix sk-abcd efgh", "SK-ABCDEFGH", "sk-ABCDEFGH"} {
		findings := m.ScanString(input)
		if len(findings) != 0 {
			t.Fatalf("case-folded input %q must not match catalog value; findings=%v", input, findings)
		}
	}
	exact := m.ScanString("prefix " + catalogValue + " suffix")
	if len(exact) != 1 {
		t.Fatalf("exact case must match; got %d findings", len(exact))
	}
}

func TestMatcher_RepeatedOccurrences(t *testing.T) {
	t.Parallel()
	cat := mustCatalog(t, []engine.CatalogInput{
		{
			Name:           "OPENAI_API_KEY",
			Value:          testkit.SyntheticOpenAIAPIKey,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
	})
	m := engine.NewMatcher(cat)
	input := testkit.SyntheticOpenAIAPIKey + "|mid|" + testkit.SyntheticOpenAIAPIKey
	findings := m.ScanString(input)
	assertFindingsNeverContainSecretValues(t, findings)
	if len(findings) != 1 || findings[0].OccurrenceCount != 2 {
		t.Fatalf("want one finding with OccurrenceCount=2, got len=%d count=%d",
			len(findings), findingCount(findings))
	}
}

func TestMatcher_AdjacentSecrets(t *testing.T) {
	t.Parallel()
	cat := mustCatalog(t, []engine.CatalogInput{
		{
			Name:           "OPENAI_API_KEY",
			Value:          testkit.SyntheticOpenAIAPIKey,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
		{
			Name:           "OPENROUTER_API_KEY",
			Value:          testkit.SyntheticOpenRouterAPIKey,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
	})
	m := engine.NewMatcher(cat)
	input := testkit.SyntheticOpenAIAPIKey + testkit.SyntheticOpenRouterAPIKey
	findings := m.ScanString(input)
	assertFindingsNeverContainSecretValues(t, findings)
	if len(findings) != 2 {
		t.Fatalf("findings len=%d want 2", len(findings))
	}
	if findings[0].SecretRefName != "OPENAI_API_KEY" || findings[1].SecretRefName != "OPENROUTER_API_KEY" {
		t.Fatalf("stable ref order got %q,%q", findings[0].SecretRefName, findings[1].SecretRefName)
	}
	if findings[0].OccurrenceCount != 1 || findings[1].OccurrenceCount != 1 {
		t.Fatalf("occurrence counts: %d,%d", findings[0].OccurrenceCount, findings[1].OccurrenceCount)
	}
}

func TestMatcher_UnicodeLengthPreservation(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticUnicodeSecret
	cat := mustCatalog(t, []engine.CatalogInput{
		{
			Name:           "UNICODE_SECRET",
			Value:          secret,
			SourceCategory: sdk.SourceCategoryOperatorEnv,
		},
	})
	m := engine.NewMatcher(cat)
	input := "pre-" + secret + "-post"
	redacted, findings := m.RedactString(input)
	assertFindingsNeverContainSecretValues(t, findings)
	if len(findings) != 1 || findings[0].OccurrenceCount != 1 {
		t.Fatalf("findings len=%d count=%d", len(findings), findingCount(findings))
	}
	if len(redacted) != len(input) {
		t.Fatalf("redacted UTF-8 byte length=%d want %d", len(redacted), len(input))
	}
	if strings.Contains(redacted, secret) {
		t.Fatal("redacted string still contains secret")
	}
	wantMask := strings.Repeat("*", len(secret))
	if !strings.Contains(redacted, wantMask) {
		t.Fatal("redacted must preserve matched UTF-8 byte length with '*'")
	}

	inBytes := []byte(input)
	outBytes, findings := m.RedactBytes(inBytes)
	assertFindingsNeverContainSecretValues(t, findings)
	if len(outBytes) != len(inBytes) {
		t.Fatalf("RedactBytes length=%d want %d", len(outBytes), len(inBytes))
	}
	if bytes.Contains(outBytes, []byte(secret)) {
		t.Fatal("RedactBytes output still contains secret bytes")
	}
	if !bytes.Equal(inBytes, []byte(input)) {
		t.Fatal("RedactBytes must not mutate the input slice")
	}
}

func TestMatcher_ConcurrentScan(t *testing.T) {
	t.Parallel()
	cat := mustCatalog(t, []engine.CatalogInput{
		{
			Name:           "OPENAI_API_KEY",
			Value:          testkit.SyntheticOpenAIAPIKey,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
		{
			Name:           "OVERLAP_LONGER",
			Value:          testkit.SyntheticOverlapLonger,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
		{
			Name:           "OVERLAP_SHORTER",
			Value:          testkit.SyntheticOverlapShorter,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
	})
	m := engine.NewMatcher(cat)
	input := "a" + testkit.SyntheticOpenAIAPIKey + "b" + testkit.SyntheticOverlapLonger + "c"

	const n = 32
	var wg sync.WaitGroup
	errCh := make(chan string, n)
	for range n {
		wg.Go(func() {
			findings := m.ScanString(input)
			if len(findings) != 2 {
				errCh <- fmt.Sprintf("findings len=%d want 2", len(findings))
				return
			}
			for _, f := range findings {
				if f.OccurrenceCount != 1 {
					errCh <- fmt.Sprintf("OccurrenceCount=%d want 1", f.OccurrenceCount)
					return
				}
			}
			for _, secret := range testkit.AllSyntheticSecretGuardValues() {
				for _, f := range findings {
					if strings.Contains(f.SecretRefName, secret) {
						errCh <- "finding leaked secret material"
						return
					}
				}
			}
		})
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Fatal(msg)
	}
}

func TestMatcher_FindingsNeverContainSecretValues(t *testing.T) {
	t.Parallel()
	cat := mustCatalog(t, []engine.CatalogInput{
		{
			Name:           "OPENAI_API_KEY",
			Value:          testkit.SyntheticOpenAIAPIKey,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
	})
	m := engine.NewMatcher(cat)
	findings := m.ScanBytes([]byte("leak:" + testkit.SyntheticOpenAIAPIKey))
	assertFindingsNeverContainSecretValues(t, findings)
	_, findings = m.RedactBytes([]byte("leak:" + testkit.SyntheticOpenAIAPIKey))
	assertFindingsNeverContainSecretValues(t, findings)
}

func TestMatcher_ACLongestOverlapStillWins(t *testing.T) {
	t.Parallel()
	const shared = "secretguard-ac-prefix-shared"
	longer := shared + "-tail-aaaa"
	inputs := make([]engine.CatalogInput, 0, 12)
	inputs = append(
		inputs,
		engine.CatalogInput{
			Name:           "AC_LONGER",
			Value:          longer,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
		engine.CatalogInput{
			Name:           "AC_SHORTER",
			Value:          shared,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
	)
	for i := range 10 {
		inputs = append(inputs, engine.CatalogInput{
			Name:           fmt.Sprintf("AC_DISTRACTOR_%02d", i),
			Value:          fmt.Sprintf("secretguard-ac-distractor-%02d-xxxx", i),
			SourceCategory: sdk.SourceCategoryProxyEnv,
		})
	}
	cat := mustCatalog(t, inputs)
	m := engine.NewMatcher(cat)
	input := "head " + longer + " mid " + shared + " tail"
	findings := m.ScanString(input)
	assertFindingsNeverContainSecretValues(t, findings)
	if len(findings) != 2 {
		t.Fatalf("findings len=%d want 2", len(findings))
	}
	byName := map[string]int{}
	for _, f := range findings {
		byName[f.SecretRefName] = f.OccurrenceCount
	}
	if byName["AC_LONGER"] != 1 || byName["AC_SHORTER"] != 1 {
		t.Fatalf("counts=%v want AC_LONGER=1 AC_SHORTER=1", byName)
	}
	redacted, _ := m.RedactString(input)
	if strings.Contains(redacted, longer) || strings.Contains(redacted, shared) {
		t.Fatal("redacted output still contains overlap secrets")
	}
	if !strings.Contains(redacted, strings.Repeat("*", len(longer))) {
		t.Fatal("longest match must be fully masked")
	}
}

func TestMatcher_PreserveKnownPrefix(t *testing.T) {
	t.Parallel()
	const (
		value  = "sk-abcdefghijklmnop"
		prefix = "sk-"
	)
	cat := mustCatalog(t, []engine.CatalogInput{
		{
			Name:              "PREFIXED_KEY",
			Value:             value,
			KnownPublicPrefix: prefix,
			SourceCategory:    sdk.SourceCategoryProxyEnv,
		},
	})
	m := engine.NewMatcherWithOptions(cat, engine.MatcherOptions{
		PreserveKnownPrefixes: true,
	})
	input := "pre-" + value + "-post"
	redacted, findings := m.RedactString(input)
	assertFindingsNeverContainSecretValues(t, findings)
	if len(findings) != 1 || findings[0].OccurrenceCount != 1 {
		t.Fatalf("findings len=%d count=%d", len(findings), findingCount(findings))
	}
	if len(redacted) != len(input) {
		t.Fatalf("redacted length=%d want %d", len(redacted), len(input))
	}
	want := "pre-" + prefix + strings.Repeat("*", len(value)-len(prefix)) + "-post"
	if redacted != want {
		t.Fatalf("prefix-preserving redact mismatch: got len=%d want len=%d", len(redacted), len(want))
	}
	if strings.Contains(redacted, value) {
		t.Fatal("redacted still contains full secret")
	}
	if !strings.HasPrefix(redacted[len("pre-"):], prefix) {
		t.Fatal("redacted match span must keep known public prefix")
	}
}

func TestMatcher_PreserveKnownPrefixDisabledMasksAll(t *testing.T) {
	t.Parallel()
	const (
		value  = "sk-abcdefghijklmnop"
		prefix = "sk-"
	)
	cat := mustCatalog(t, []engine.CatalogInput{
		{
			Name:              "PREFIXED_KEY",
			Value:             value,
			KnownPublicPrefix: prefix,
			SourceCategory:    sdk.SourceCategoryProxyEnv,
		},
	})
	m := engine.NewMatcher(cat)
	input := "pre-" + value + "-post"
	redacted, findings := m.RedactString(input)
	assertFindingsNeverContainSecretValues(t, findings)
	if len(findings) != 1 {
		t.Fatalf("findings len=%d want 1", len(findings))
	}
	want := "pre-" + strings.Repeat("*", len(value)) + "-post"
	if redacted != want {
		t.Fatalf("full-mask redact mismatch: got len=%d want len=%d", len(redacted), len(want))
	}
	span := redacted[len("pre-") : len("pre-")+len(value)]
	if strings.HasPrefix(span, prefix) {
		t.Fatal("preserve disabled must mask the known public prefix too")
	}
}

func TestMatcher_PreserveKnownPrefixCustomMaskByte(t *testing.T) {
	t.Parallel()
	const (
		value  = "sk-abcdefghijklmnop"
		prefix = "sk-"
	)
	cat := mustCatalog(t, []engine.CatalogInput{
		{
			Name:              "PREFIXED_KEY",
			Value:             value,
			KnownPublicPrefix: prefix,
			SourceCategory:    sdk.SourceCategoryProxyEnv,
		},
	})
	m := engine.NewMatcherWithOptions(cat, engine.MatcherOptions{
		PreserveKnownPrefixes: true,
		MaskByte:              '#',
	})
	redacted, _ := m.RedactString(value)
	want := prefix + strings.Repeat("#", len(value)-len(prefix))
	if redacted != want {
		t.Fatalf("custom mask mismatch: got len=%d want len=%d", len(redacted), len(want))
	}
}

func TestMatcher_ConcurrentScanAndRedact(t *testing.T) {
	t.Parallel()
	const (
		value  = "sk-abcdefghijklmnop"
		prefix = "sk-"
	)
	cat := mustCatalog(t, []engine.CatalogInput{
		{
			Name:              "PREFIXED_KEY",
			Value:             value,
			KnownPublicPrefix: prefix,
			SourceCategory:    sdk.SourceCategoryProxyEnv,
		},
		{
			Name:           "OPENAI_API_KEY",
			Value:          testkit.SyntheticOpenAIAPIKey,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
	})
	m := engine.NewMatcherWithOptions(cat, engine.MatcherOptions{
		PreserveKnownPrefixes: true,
	})
	input := value + "|" + testkit.SyntheticOpenAIAPIKey

	const n = 32
	var wg sync.WaitGroup
	errCh := make(chan string, n*2)
	for range n {
		wg.Go(func() {
			findings := m.ScanString(input)
			if len(findings) != 2 {
				errCh <- fmt.Sprintf("scan findings len=%d want 2", len(findings))
			}
		})
		wg.Go(func() {
			redacted, findings := m.RedactString(input)
			if len(findings) != 2 {
				errCh <- fmt.Sprintf("redact findings len=%d want 2", len(findings))
				return
			}
			if strings.Contains(redacted, value) || strings.Contains(redacted, testkit.SyntheticOpenAIAPIKey) {
				errCh <- "redact leaked secret material"
				return
			}
			if !strings.HasPrefix(redacted, prefix) {
				errCh <- "concurrent redact lost known public prefix"
			}
		})
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Fatal(msg)
	}
}

func TestSDKAdapter_MatcherAndStaticResolver(t *testing.T) {
	t.Parallel()
	cat := mustCatalog(t, []engine.CatalogInput{
		{
			Name:           "OPENAI_API_KEY",
			Value:          testkit.SyntheticOpenAIAPIKey,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
	})
	m := engine.NewMatcher(cat)
	iface := engine.AsMatcher(m)
	findings, err := iface.ScanString(t.Context(), "x"+testkit.SyntheticOpenAIAPIKey+"y")
	if err != nil {
		t.Fatal(err)
	}
	assertFindingsNeverContainSecretValues(t, findings)
	if len(findings) != 1 {
		t.Fatalf("findings len=%d want 1", len(findings))
	}

	resolver := engine.NewStaticMatcherResolver(cat, engine.MatcherOptions{PreserveKnownPrefixes: true})
	got, err := resolver.Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	findings2, err := got.ScanString(t.Context(), testkit.SyntheticOpenAIAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingsNeverContainSecretValues(t, findings2)
	if len(findings2) != 1 || findings2[0].OccurrenceCount != 1 {
		t.Fatalf("resolver matcher findings unexpected: len=%d", len(findings2))
	}
}

func findingCount(findings []sdk.Finding) int {
	if len(findings) == 0 {
		return 0
	}
	return findings[0].OccurrenceCount
}
