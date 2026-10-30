package modelcatalog_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
)

func testVendorIndex() *modelcatalog.SnapshotIndex {
	return modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"acme/widget-v4-7":     {Source: modelcatalog.FactSourceCatalog},
		"acme/widget-v3.6":     {Source: modelcatalog.FactSourceCatalog},
		"acme/widget-v3.7":     {Source: modelcatalog.FactSourceCatalog},
		"vendor-a/alpha-5.4":   {Source: modelcatalog.FactSourceCatalog},
		"vendor-b/beta-chat":   {Source: modelcatalog.FactSourceCatalog},
		"vendor-c/beta-chat":   {Source: modelcatalog.FactSourceCatalog},
		"vendor-b/gamma-chat":  {Source: modelcatalog.FactSourceCatalog},
		"vendor-d/model-trial": {Source: modelcatalog.FactSourceCatalog},
		"vendor-d/model":       {Source: modelcatalog.FactSourceCatalog},
		"other/model":          {Source: modelcatalog.FactSourceCatalog},
		"vendor-e/sonnet-4":    {Source: modelcatalog.FactSourceCatalog},
		"vendor-f/sonnet-4":    {Source: modelcatalog.FactSourceCatalog},
	})
}

func testVendorPolicy() modelcatalog.VendorPolicy {
	return modelcatalog.VendorPolicy{
		MapVendor: func(vendor string) string {
			if vendor == "alias-vendor" {
				return "acme"
			}
			return vendor
		},
		SuffixLookupVariants: func(suffix string) []string {
			suffix = strings.TrimSpace(suffix)
			if suffix == "" {
				return nil
			}
			seen := map[string]struct{}{suffix: {}}
			variants := []string{suffix}
			add := func(v string) {
				v = strings.TrimSpace(v)
				if v == "" {
					return
				}
				if _, ok := seen[v]; ok {
					return
				}
				seen[v] = struct{}{}
				variants = append(variants, v)
			}
			lower := strings.ToLower(suffix)
			for _, marker := range []string{":trial", "-trial", "-cloud"} {
				if strings.HasSuffix(lower, marker) {
					add(suffix[:len(suffix)-len(marker)])
				}
			}
			return variants
		},
		KeywordFallback: func(model string) (string, bool) {
			model = strings.TrimSpace(model)
			if model == "" {
				return "", false
			}
			suffix := modelcatalog.NormalizeStripOneProviderPrefix(model)
			if strings.HasPrefix(strings.ToLower(suffix), "alpha-") {
				return "vendor-a/" + suffix, true
			}
			if strings.HasPrefix(strings.ToLower(suffix), "beta-") {
				return "vendor-b/" + suffix, true
			}
			return "", false
		},
	}
}

func TestVendorResolver_exactCatalogIDWins(t *testing.T) {
	t.Parallel()
	idx := testVendorIndex()
	r := modelcatalog.NewVendorResolver(modelcatalog.StaticActiveSnapshotProvider{Index: idx}, true, testVendorPolicy())
	got := r.Resolve("acme/widget-v3.7")
	if got.Kind != modelcatalog.VendorResolveExact {
		t.Fatalf("Kind = %v want exact", got.Kind)
	}
	if got.CanonicalID != "acme/widget-v3.7" {
		t.Fatalf("CanonicalID = %q", got.CanonicalID)
	}
	if got.RouteModel != "acme/widget-v3.7" {
		t.Fatalf("RouteModel = %q", got.RouteModel)
	}
}

func TestVendorResolver_dashedCatalogMatchPreservesCallerSuffix(t *testing.T) {
	t.Parallel()
	idx := testVendorIndex()
	r := modelcatalog.NewVendorResolver(modelcatalog.StaticActiveSnapshotProvider{Index: idx}, true, testVendorPolicy())
	got := r.Resolve("widget-v4.7")
	if got.Kind != modelcatalog.VendorResolveCatalogSuffix {
		t.Fatalf("Kind = %v want catalog_suffix", got.Kind)
	}
	if got.CanonicalID != "acme/widget-v4.7" {
		t.Fatalf("CanonicalID = %q want acme/widget-v4.7", got.CanonicalID)
	}
	if got.MatchedCatalog != "acme/widget-v4-7" {
		t.Fatalf("MatchedCatalog = %q", got.MatchedCatalog)
	}
}

func TestVendorResolver_stripProviderPrefix(t *testing.T) {
	t.Parallel()
	idx := testVendorIndex()
	r := modelcatalog.NewVendorResolver(modelcatalog.StaticActiveSnapshotProvider{Index: idx}, true, testVendorPolicy())
	got := r.Resolve("gateway/widget-v4.7")
	if got.Kind != modelcatalog.VendorResolveCatalogSuffix {
		t.Fatalf("Kind = %v want catalog_suffix", got.Kind)
	}
	if got.CanonicalID != "acme/widget-v4.7" {
		t.Fatalf("CanonicalID = %q want acme/widget-v4.7", got.CanonicalID)
	}
	if got.RouteModel != "acme/widget-v4.7" {
		t.Fatalf("RouteModel = %q", got.RouteModel)
	}
}

func TestVendorResolver_vendorAliasPrefersRouteModel(t *testing.T) {
	t.Parallel()
	idx := testVendorIndex()
	r := modelcatalog.NewVendorResolver(modelcatalog.StaticActiveSnapshotProvider{Index: idx}, true, testVendorPolicy())
	got := r.Resolve("alias-vendor/widget-v3.7")
	if got.Kind != modelcatalog.VendorResolveVendorAlias {
		t.Fatalf("Kind = %v want vendor_alias", got.Kind)
	}
	if got.CanonicalID != "acme/widget-v3.7" {
		t.Fatalf("CanonicalID = %q want acme/widget-v3.7", got.CanonicalID)
	}
	if got.RouteModel != "alias-vendor/widget-v3.7" {
		t.Fatalf("RouteModel = %q want alias-vendor/widget-v3.7", got.RouteModel)
	}
	if got.CatalogVendor != "acme" {
		t.Fatalf("CatalogVendor = %q", got.CatalogVendor)
	}
}

func TestVendorResolver_ambiguousSuffix(t *testing.T) {
	t.Parallel()
	idx := testVendorIndex()
	r := modelcatalog.NewVendorResolver(modelcatalog.StaticActiveSnapshotProvider{Index: idx}, true, testVendorPolicy())
	got := r.Resolve("sonnet-4")
	if got.Kind != modelcatalog.VendorResolveAmbiguous {
		t.Fatalf("Kind = %v want ambiguous", got.Kind)
	}
	if got.CanonicalID != "" {
		t.Fatalf("CanonicalID = %q want empty", got.CanonicalID)
	}
	want := []string{"vendor-e/sonnet-4", "vendor-f/sonnet-4"}
	if len(got.Candidates) != len(want) {
		t.Fatalf("Candidates = %v", got.Candidates)
	}
	for i := range want {
		if got.Candidates[i] != want[i] {
			t.Fatalf("Candidates[%d] = %q want %q", i, got.Candidates[i], want[i])
		}
	}
}

func TestVendorResolver_catalogSuffixStripsProviderDecoratorsForLookupOnly(t *testing.T) {
	t.Parallel()
	idx := testVendorIndex()
	r := modelcatalog.NewVendorResolver(modelcatalog.StaticActiveSnapshotProvider{Index: idx}, true, testVendorPolicy())
	cases := []struct {
		name      string
		input     string
		canonical string
		matched   string
	}{
		{name: "dash trial", input: "widget-v3.6-trial", canonical: "acme/widget-v3.6-trial", matched: "acme/widget-v3.6"},
		{name: "colon trial", input: "gamma-chat:trial", canonical: "vendor-b/gamma-chat:trial", matched: "vendor-b/gamma-chat"},
		{name: "dash cloud", input: "widget-v4.7-cloud", canonical: "acme/widget-v4.7-cloud", matched: "acme/widget-v4-7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := r.Resolve(tc.input)
			if got.Kind != modelcatalog.VendorResolveCatalogSuffix {
				t.Fatalf("Kind = %v want catalog_suffix", got.Kind)
			}
			if got.CanonicalID != tc.canonical {
				t.Fatalf("CanonicalID = %q want %q", got.CanonicalID, tc.canonical)
			}
			if got.MatchedCatalog != tc.matched {
				t.Fatalf("MatchedCatalog = %q want %q", got.MatchedCatalog, tc.matched)
			}
			if got.RouteModel != tc.canonical {
				t.Fatalf("RouteModel = %q want %q", got.RouteModel, tc.canonical)
			}
		})
	}
}

func TestVendorResolver_catalogSuffixExactDecoratedModelWins(t *testing.T) {
	t.Parallel()
	idx := testVendorIndex()
	r := modelcatalog.NewVendorResolver(modelcatalog.StaticActiveSnapshotProvider{Index: idx}, true, testVendorPolicy())
	got := r.Resolve("model-trial")
	if got.Kind != modelcatalog.VendorResolveCatalogSuffix {
		t.Fatalf("Kind = %v want catalog_suffix", got.Kind)
	}
	if got.CanonicalID != "vendor-d/model-trial" {
		t.Fatalf("CanonicalID = %q want vendor-d/model-trial", got.CanonicalID)
	}
	if got.MatchedCatalog != "vendor-d/model-trial" {
		t.Fatalf("MatchedCatalog = %q want vendor-d/model-trial", got.MatchedCatalog)
	}
}

func TestVendorResolver_catalogSuffixStrippedAmbiguityDoesNotGuess(t *testing.T) {
	t.Parallel()
	idx := testVendorIndex()
	r := modelcatalog.NewVendorResolver(modelcatalog.StaticActiveSnapshotProvider{Index: idx}, true, testVendorPolicy())
	got := r.Resolve("model-cloud")
	if got.Kind != modelcatalog.VendorResolveAmbiguous {
		t.Fatalf("Kind = %v want ambiguous", got.Kind)
	}
	if got.CanonicalID != "" {
		t.Fatalf("CanonicalID = %q want empty", got.CanonicalID)
	}
}

func TestVendorResolver_noSnapshotKeywordFallback(t *testing.T) {
	t.Parallel()
	r := modelcatalog.NewVendorResolver(modelcatalog.StaticActiveSnapshotProvider{Index: nil}, true, testVendorPolicy())
	got := r.Resolve("alpha-5.4")
	if got.Kind != modelcatalog.VendorResolveKeywordFallback {
		t.Fatalf("Kind = %v want keyword_fallback", got.Kind)
	}
	if got.CanonicalID != "vendor-a/alpha-5.4" {
		t.Fatalf("CanonicalID = %q want vendor-a/alpha-5.4", got.CanonicalID)
	}
	if got.CatalogVendor != "vendor-a" {
		t.Fatalf("CatalogVendor = %q want vendor-a", got.CatalogVendor)
	}
}

func TestVendorResolver_keywordFallbackDoesNotMatchMidToken(t *testing.T) {
	t.Parallel()
	r := modelcatalog.NewVendorResolver(modelcatalog.StaticActiveSnapshotProvider{Index: nil}, true, testVendorPolicy())
	for _, input := range []string{
		"internal-beta-proxy",
		"legacyalpha-wrapper",
		"notalpha-model",
		"vendor/alphish-alias",
	} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			got := r.Resolve(input)
			if got.Kind != modelcatalog.VendorResolveNoMatch {
				t.Fatalf("Kind = %v want no_match for %q with canonical %q", got.Kind, input, got.CanonicalID)
			}
		})
	}
}

func TestVendorResolver_noSnapshotKeywordFallbackDisabled(t *testing.T) {
	t.Parallel()
	r := modelcatalog.NewVendorResolver(modelcatalog.StaticActiveSnapshotProvider{Index: nil}, false, testVendorPolicy())
	got := r.Resolve("alpha-5.4")
	if got.Kind != modelcatalog.VendorResolveNoMatch {
		t.Fatalf("Kind = %v want no_match", got.Kind)
	}
	if got.CanonicalID != "" {
		t.Fatalf("CanonicalID = %q want empty", got.CanonicalID)
	}
}

func TestVendorResolver_keywordFallbackAfterCatalogMiss(t *testing.T) {
	t.Parallel()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{})
	r := modelcatalog.NewVendorResolver(modelcatalog.StaticActiveSnapshotProvider{Index: idx}, true, testVendorPolicy())
	got := r.Resolve("alpha-5.4")
	if got.Kind != modelcatalog.VendorResolveKeywordFallback {
		t.Fatalf("Kind = %v want keyword_fallback", got.Kind)
	}
	if got.CanonicalID != "vendor-a/alpha-5.4" {
		t.Fatalf("CanonicalID = %q", got.CanonicalID)
	}
}

func TestVendorResolver_keywordFallbackDisabled(t *testing.T) {
	t.Parallel()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{})
	r := modelcatalog.NewVendorResolver(modelcatalog.StaticActiveSnapshotProvider{Index: idx}, false, testVendorPolicy())
	got := r.Resolve("alpha-5.4")
	if got.Kind != modelcatalog.VendorResolveNoMatch {
		t.Fatalf("Kind = %v want no_match", got.Kind)
	}
}

func TestVendorResolver_zeroPolicyUsesGenericBehavior(t *testing.T) {
	t.Parallel()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"acme/widget-v1": {Source: modelcatalog.FactSourceCatalog},
	})
	r := modelcatalog.NewVendorResolver(modelcatalog.StaticActiveSnapshotProvider{Index: idx}, true, modelcatalog.VendorPolicy{})
	got := r.Resolve("acme/widget-v1")
	if got.Kind != modelcatalog.VendorResolveExact {
		t.Fatalf("Kind = %v want exact", got.Kind)
	}
	got = r.Resolve("alpha-5.4")
	if got.Kind != modelcatalog.VendorResolveNoMatch {
		t.Fatalf("Kind = %v want no_match without policy hook", got.Kind)
	}
}

func TestSuffixLookupKeys_dotDashVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want []string
	}{
		{in: "widget-v4.7", want: []string{"widget-v4.7", "widget-v4-7"}},
		{in: "widget-v4-7", want: []string{"widget-v4-7", "widget-v4.7"}},
		{in: "item-v2.5", want: []string{"item-v2.5", "item-v2-5"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got := modelcatalog.SuffixLookupKeys(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("keys = %v want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("keys[%d] = %q want %q (full %v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

func TestSnapshotIndex_catalogIDsForSuffixLookup(t *testing.T) {
	t.Parallel()
	idx := testVendorIndex()
	ids := idx.CatalogIDsForSuffixLookup("widget-v4.7")
	if len(ids) != 1 || ids[0] != "acme/widget-v4-7" {
		t.Fatalf("ids = %v", ids)
	}
}
