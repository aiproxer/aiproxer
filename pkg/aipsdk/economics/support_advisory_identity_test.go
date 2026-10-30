package economics

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

func supportAdvisoryIdentityContext() SupportAdvisoryContext {
	return SupportAdvisoryContext{
		Version: SupportAdvisoryVersionV1,
		Tariff: RatingSnapshotRef{
			VersionRef: VersionRef{ID: "advisory-tariff", Version: "tariff-v1"},
			RaterID:    "advisory-rater",
		},
		TariffContent: SnapshotContentRef{
			ContentRef:  "catalog://advisory/tariff-v1",
			ContentHash: strings.Repeat("a", 64),
		},
	}
}

func useSupportAdvisoryIdentityMoney(v *Valuation) {
	v.Completeness = CompletenessPartial
	v.Lines = []LineItem{{
		ID:                "identity-charge",
		RuleID:            "identity-rule",
		ItemID:            "identity-item",
		Unit:              "token",
		AmountNumerator:   "5",
		AmountDenominator: "2",
		RoundedAmount:     &Money{NanoUnits: 2_500_000_000, Currency: "USD", Present: true},
		Status:            RatingLineRated,
		ReportedAggregate: true,
	}}
	v.Totals = []CurrencyTotal{{
		Currency:          "USD",
		AmountNumerator:   "5",
		AmountDenominator: "2",
		RoundedAmount:     Money{NanoUnits: 2_500_000_000, Currency: "USD", Present: true},
	}}
}

func TestSupportAdvisoryContextSourceFieldsBindEnabledIdentity(t *testing.T) {
	base := preAdvisoryValuationFixture()
	useSupportAdvisoryIdentityMoney(&base)
	base.SupportAdvisoryContexts = []SupportAdvisoryContext{supportAdvisoryIdentityContext()}
	if err := base.Validate(); err != nil {
		t.Fatalf("enabled baseline valuation is invalid: %v", err)
	}
	baseHash := base.ContextHash()
	if baseHash == "" {
		t.Fatal("enabled baseline context hash is empty")
	}

	variants := []struct {
		name   string
		mutate func(*SupportAdvisoryContext)
	}{
		{name: "tariff id", mutate: func(c *SupportAdvisoryContext) { c.Tariff.ID = "other-advisory-tariff" }},
		{name: "tariff version", mutate: func(c *SupportAdvisoryContext) { c.Tariff.Version = "tariff-v2" }},
		{name: "rater id", mutate: func(c *SupportAdvisoryContext) { c.Tariff.RaterID = "other-advisory-rater" }},
		{name: "content reference", mutate: func(c *SupportAdvisoryContext) { c.TariffContent.ContentRef = "catalog://advisory/tariff-v2" }},
		{name: "content hash", mutate: func(c *SupportAdvisoryContext) { c.TariffContent.ContentHash = strings.Repeat("b", 64) }},
	}
	for _, tc := range variants {
		t.Run(tc.name, func(t *testing.T) {
			variant := base.Clone()
			tc.mutate(&variant.SupportAdvisoryContexts[0])
			if err := variant.Validate(); err != nil {
				t.Fatalf("mutated valuation is invalid: %v", err)
			}
			if got := variant.ContextHash(); got == "" || got == baseHash {
				t.Fatalf("context hash=%q, want nonempty hash distinct from enabled baseline %q", got, baseHash)
			}
			if !reflect.DeepEqual(base.Lines, variant.Lines) || !reflect.DeepEqual(base.Totals, variant.Totals) || base.Completeness != variant.Completeness {
				t.Fatal("source-context mutation changed valuation money")
			}
		})
	}
}

func TestSupportAdvisoryContextIdentityCanonicalization(t *testing.T) {
	base := preAdvisoryValuationFixture()
	useSupportAdvisoryIdentityMoney(&base)
	first := supportAdvisoryIdentityContext()
	second := first
	second.Tariff.ID = "second-tariff"
	second.Tariff.Version = "tariff-v2"
	second.TariffContent.ContentRef = "catalog://second/v2"
	second.TariffContent.ContentHash = strings.Repeat("b", 64)
	base.SupportAdvisoryContexts = []SupportAdvisoryContext{first, second}

	want, err := base.CanonicalContextJSON()
	if err != nil {
		t.Fatal(err)
	}
	wantHash := base.ContextHash()
	if wantHash == "" {
		t.Fatal("enabled support context produced an empty context hash")
	}

	firstWithTimestamps := first
	firstWithTimestamps.Tariff.EffectiveAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	firstWithTimestamps.Tariff.FetchedAt = firstWithTimestamps.Tariff.EffectiveAt.Add(time.Hour)
	secondWithTimestamps := second
	secondWithTimestamps.Tariff.EffectiveAt = firstWithTimestamps.Tariff.EffectiveAt.Add(2 * time.Hour)
	secondWithTimestamps.Tariff.FetchedAt = firstWithTimestamps.Tariff.FetchedAt.Add(2 * time.Hour)

	variants := []struct {
		name     string
		contexts []SupportAdvisoryContext
	}{
		{name: "reordered", contexts: []SupportAdvisoryContext{second, first}},
		{name: "exact duplicate", contexts: []SupportAdvisoryContext{first, second, first}},
		{name: "timestamps and timestamp duplicate", contexts: []SupportAdvisoryContext{secondWithTimestamps, firstWithTimestamps, first}},
	}
	for _, tc := range variants {
		t.Run(tc.name, func(t *testing.T) {
			variant := base.Clone()
			variant.SupportAdvisoryContexts = tc.contexts
			originalContexts := append([]SupportAdvisoryContext(nil), tc.contexts...)
			got, err := variant.CanonicalContextJSON()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("canonical context changed for equivalent source contexts:\n got %s\nwant %s", got, want)
			}
			if variant.ContextHash() != wantHash {
				t.Fatalf("context hash changed for equivalent source contexts: %q, want %q", variant.ContextHash(), wantHash)
			}
			if !reflect.DeepEqual(base.Lines, variant.Lines) || !reflect.DeepEqual(base.Totals, variant.Totals) || base.Completeness != variant.Completeness {
				t.Fatal("canonicalizing source contexts changed valuation money")
			}
			if !reflect.DeepEqual(variant.SupportAdvisoryContexts, originalContexts) {
				t.Fatal("canonical context serialization mutated caller-owned contexts")
			}
		})
	}
	if !reflect.DeepEqual(base.SupportAdvisoryContexts, []SupportAdvisoryContext{first, second}) {
		t.Fatal("canonical context serialization mutated source contexts")
	}
}

func TestSupportAdvisoryContextIdentityRejectsInvalidContexts(t *testing.T) {
	base := preAdvisoryValuationFixture()
	valid := SupportAdvisoryContext{
		Version:       SupportAdvisoryVersionV1,
		Tariff:        RatingSnapshotRef{VersionRef: VersionRef{ID: "tariff", Version: "v1"}, RaterID: "reference"},
		TariffContent: SnapshotContentRef{ContentRef: "catalog://tariff/v1", ContentHash: strings.Repeat("a", 64)},
	}
	unsupported := valid
	unsupported.Version = "component-support-advisory-v2"
	cases := []struct {
		name     string
		contexts []SupportAdvisoryContext
	}{
		{name: "unsupported version", contexts: []SupportAdvisoryContext{unsupported}},
		{name: "explicit empty", contexts: []SupportAdvisoryContext{}},
		{name: "raw bound", contexts: make([]SupportAdvisoryContext, MaxSupportAdvisoryContexts+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			variant := base.Clone()
			variant.SupportAdvisoryContexts = tc.contexts
			if _, err := variant.CanonicalContextJSON(); err == nil {
				t.Fatal("CanonicalContextJSON accepted invalid source contexts")
			}
			if got := variant.ContextHash(); got != "" {
				t.Fatalf("ContextHash = %q, want unavailable hash", got)
			}
		})
	}

	validVariant := base.Clone()
	validVariant.SupportAdvisoryContexts = []SupportAdvisoryContext{valid}
	if _, err := validVariant.CanonicalContextJSON(); err != nil {
		t.Fatalf("valid context rejected: %v", err)
	}
}

func TestSupportAdvisoryReportChangesResultFingerprintNotContextIdentity(t *testing.T) {
	base := supportAdvisoryFixture()
	useSupportAdvisoryIdentityMoney(&base)
	baseContext, err := base.CanonicalContextJSON()
	if err != nil {
		t.Fatal(err)
	}
	baseJSON, err := base.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	baseFingerprint := base.Fingerprint()
	baseHash := base.ContextHash()
	if baseFingerprint == "" || baseHash == "" {
		t.Fatal("base valuation has unavailable fingerprint or context hash")
	}

	variants := []struct {
		name   string
		mutate func(*Valuation)
	}{
		{
			name: "pair scope",
			mutate: func(v *Valuation) {
				v.SupportAdvisory.Pairs[0].ScopeKey = "other-scope"
			},
		},
		{
			name: "incomplete reason",
			mutate: func(v *Valuation) {
				v.SupportAdvisory.IncompleteContexts = append(v.SupportAdvisory.IncompleteContexts, SupportAdvisoryIncomplete{
					ContextKey: v.SupportAdvisoryContexts[0].Key(),
					Reason:     SupportAdvisoryGraphBudget,
				})
			},
		},
	}
	for _, tc := range variants {
		t.Run(tc.name, func(t *testing.T) {
			variant := base.Clone()
			tc.mutate(&variant)
			contextJSON, err := variant.CanonicalContextJSON()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(contextJSON, baseContext) || variant.ContextHash() != baseHash {
				t.Fatal("report variation changed canonical context identity")
			}
			variantJSON, err := variant.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(variantJSON, baseJSON) || variant.Fingerprint() == baseFingerprint {
				t.Fatal("report variation did not change canonical result JSON and fingerprint")
			}
			if !reflect.DeepEqual(base.Lines, variant.Lines) || !reflect.DeepEqual(base.Totals, variant.Totals) || base.Completeness != variant.Completeness {
				t.Fatal("report variation changed valuation money")
			}
		})
	}
}
