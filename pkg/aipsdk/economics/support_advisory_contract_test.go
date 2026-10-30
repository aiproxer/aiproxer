package economics

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func supportAdvisoryFixture() Valuation {
	v := preAdvisoryValuationFixture()
	c := SupportAdvisoryContext{Version: SupportAdvisoryVersionV1, Tariff: RatingSnapshotRef{VersionRef: VersionRef{ID: "tariff", Version: "v1"}, RaterID: "rater"}, TariffContent: SnapshotContentRef{ContentRef: "tariff://v1", ContentHash: strings.Repeat("a", 64)}}
	v.SupportAdvisoryContexts = []SupportAdvisoryContext{c}
	v.SupportAdvisory = &SupportAdvisoryReport{Pairs: []SupportAdvisoryPair{{ContextKey: c.Key(), ScopeKey: "scope", Left: metering.ComponentKey{Direction: metering.DirectionInput, Component: "b", Unit: metering.UnitToken, SchemaID: "schema", Dimensions: []metering.Dimension{{Name: "z", Value: "z"}, {Name: "a", Value: "a"}}}, Right: metering.ComponentKey{Direction: metering.DirectionInput, Component: "c", Unit: metering.UnitToken, SchemaID: "schema"}}}}
	return v
}

func TestSupportAdvisoryCanonicalAndClone(t *testing.T) {
	v := supportAdvisoryFixture()
	v.SupportAdvisory.IncompleteContexts = []SupportAdvisoryIncomplete{{v.SupportAdvisoryContexts[0].Key(), SupportAdvisoryGraphBudget}, {v.SupportAdvisoryContexts[0].Key(), SupportAdvisoryCandidateBudget}}
	want, err := v.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	reversed := v.Clone()
	reversed.SupportAdvisory.Pairs[0].Left, reversed.SupportAdvisory.Pairs[0].Right = reversed.SupportAdvisory.Pairs[0].Right, reversed.SupportAdvisory.Pairs[0].Left
	reversed.SupportAdvisory.Pairs = append(reversed.SupportAdvisory.Pairs, reversed.SupportAdvisory.Pairs[0])
	reversed.SupportAdvisoryContexts = append(reversed.SupportAdvisoryContexts, reversed.SupportAdvisoryContexts[0])
	reversed.SupportAdvisory.IncompleteContexts[0], reversed.SupportAdvisory.IncompleteContexts[1] = reversed.SupportAdvisory.IncompleteContexts[1], reversed.SupportAdvisory.IncompleteContexts[0]
	reversed.SupportAdvisory.IncompleteContexts = append(reversed.SupportAdvisory.IncompleteContexts, reversed.SupportAdvisory.IncompleteContexts[0])
	got, err := reversed.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("reordering and exact duplicates changed canonical report")
	}
	clone := v.Clone()
	clone.SupportAdvisoryContexts[0].Version = "changed"
	clone.SupportAdvisory.Pairs[0].Left.Dimensions[0].Value = "changed"
	clone.SupportAdvisory.IncompleteContexts[0].Reason = "changed"
	if v.SupportAdvisoryContexts[0].Version != SupportAdvisoryVersionV1 || v.SupportAdvisory.Pairs[0].Left.Dimensions[0].Value != "z" || v.SupportAdvisory.IncompleteContexts[0].Reason != SupportAdvisoryGraphBudget {
		t.Fatal("clone aliases source")
	}
	var decoded Valuation
	if err := json.Unmarshal(want, &decoded); err != nil {
		t.Fatal(err)
	}
	roundtrip, err := decoded.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(roundtrip) != string(want) {
		t.Fatal("advisory lost on JSON roundtrip")
	}
	clean := v.Clone()
	clean.SupportAdvisory = &SupportAdvisoryReport{}
	canonical, err := clean.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if canonical.SupportAdvisory != nil || len(canonical.SupportAdvisoryContexts) != 1 {
		t.Fatal("clean enabled result must retain context and omit empty report")
	}
	clean.SupportAdvisory = nil
	if err := clean.Validate(); err != nil {
		t.Fatal(err)
	}
	c := v.SupportAdvisoryContexts[0]
	c.Tariff.FetchedAt = time.Now()
	c.Tariff.EffectiveAt = time.Now()
	if c.Key() != v.SupportAdvisoryContexts[0].Key() {
		t.Fatal("timestamps changed context identity")
	}
	for _, mutate := range []func(*SupportAdvisoryContext){func(c *SupportAdvisoryContext) { c.Version = "v2" }, func(c *SupportAdvisoryContext) { c.Tariff.ID = "other" }, func(c *SupportAdvisoryContext) { c.Tariff.Version = "other" }, func(c *SupportAdvisoryContext) { c.Tariff.RaterID = "other" }, func(c *SupportAdvisoryContext) { c.TariffContent.ContentRef = "other" }, func(c *SupportAdvisoryContext) { c.TariffContent.ContentHash = strings.Repeat("b", 64) }} {
		c := v.SupportAdvisoryContexts[0]
		mutate(&c)
		if c.Key() == v.SupportAdvisoryContexts[0].Key() {
			t.Fatal("context tuple field missing from key")
		}
	}
}

func TestSupportAdvisoryInvalidDTO(t *testing.T) {
	cases := map[string]func(*Valuation){
		"version":                 func(v *Valuation) { v.SupportAdvisoryContexts[0].Version = "future" },
		"empty version":           func(v *Valuation) { v.SupportAdvisoryContexts[0].Version = "" },
		"tariff":                  func(v *Valuation) { v.SupportAdvisoryContexts[0].Tariff.ID = "" },
		"rater":                   func(v *Valuation) { v.SupportAdvisoryContexts[0].Tariff.RaterID = "\x00" },
		"content":                 func(v *Valuation) { v.SupportAdvisoryContexts[0].TariffContent.ContentHash = "bad" },
		"content ref":             func(v *Valuation) { v.SupportAdvisoryContexts[0].TariffContent.ContentRef = "" },
		"dangling":                func(v *Valuation) { v.SupportAdvisory.Pairs[0].ContextKey = "missing" },
		"no contexts":             func(v *Valuation) { v.SupportAdvisoryContexts = nil },
		"explicit empty contexts": func(v *Valuation) { v.SupportAdvisory = nil; v.SupportAdvisoryContexts = []SupportAdvisoryContext{} },
		"empty scope":             func(v *Valuation) { v.SupportAdvisory.Pairs[0].ScopeKey = "" },
		"unsafe scope":            func(v *Valuation) { v.SupportAdvisory.Pairs[0].ScopeKey = "bad\x00scope" },
		"invalid UTF8":            func(v *Valuation) { v.SupportAdvisory.Pairs[0].ScopeKey = string([]byte{255}) },
		"scope bound": func(v *Valuation) {
			v.SupportAdvisory.Pairs[0].ScopeKey = strings.Repeat("x", MaxSupportAdvisoryScopeKeyBytes+1)
		},
		"component": func(v *Valuation) { v.SupportAdvisory.Pairs[0].Left.Unit = "" },
		"self":      func(v *Valuation) { v.SupportAdvisory.Pairs[0].Right = v.SupportAdvisory.Pairs[0].Left.Clone() },
		"canonical self": func(v *Valuation) {
			p := &v.SupportAdvisory.Pairs[0]
			p.Right = p.Left.Clone()
			p.Right.Dimensions[0], p.Right.Dimensions[1] = p.Right.Dimensions[1], p.Right.Dimensions[0]
		},
		"reason": func(v *Valuation) {
			v.SupportAdvisory.IncompleteContexts = []SupportAdvisoryIncomplete{{v.SupportAdvisoryContexts[0].Key(), "future"}}
		},
		"incomplete dangling": func(v *Valuation) {
			v.SupportAdvisory.IncompleteContexts = []SupportAdvisoryIncomplete{{"missing", SupportAdvisoryGraphBudget}}
		},
		"raw pairs": func(v *Valuation) {
			p := v.SupportAdvisory.Pairs[0]
			v.SupportAdvisory.Pairs = make([]SupportAdvisoryPair, MaxSupportAdvisoryPairs+1)
			for i := range v.SupportAdvisory.Pairs {
				v.SupportAdvisory.Pairs[i] = p
			}
		},
		"raw contexts": func(v *Valuation) {
			c := v.SupportAdvisoryContexts[0]
			v.SupportAdvisoryContexts = make([]SupportAdvisoryContext, MaxSupportAdvisoryContexts+1)
			for i := range v.SupportAdvisoryContexts {
				v.SupportAdvisoryContexts[i] = c
			}
		},
		"raw incomplete": func(v *Valuation) {
			v.SupportAdvisory.IncompleteContexts = make([]SupportAdvisoryIncomplete, 5)
			for i := range v.SupportAdvisory.IncompleteContexts {
				v.SupportAdvisory.IncompleteContexts[i] = SupportAdvisoryIncomplete{v.SupportAdvisoryContexts[0].Key(), SupportAdvisoryGraphBudget}
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v := supportAdvisoryFixture()
			mutate(&v)
			if err := v.Validate(); !errors.Is(err, ErrInvalidValuation) {
				t.Errorf("Validate = %v, want invalid valuation", err)
			}
			if _, err := v.Canonical(); !errors.Is(err, ErrInvalidValuation) {
				t.Errorf("Canonical = %v, want invalid valuation", err)
			}
		})
	}
}

func TestSupportAdvisoryLimitsAndLongScope(t *testing.T) {
	if MaxSupportAdvisoryPairs != 128 || MaxSupportAdvisoryContexts != 1027 || MaxSupportAdvisoryCandidateExaminations != 4096 || MaxSupportAdvisoryGraphVisits != 65536 {
		t.Fatal("v1 limits changed")
	}
	v := supportAdvisoryFixture()
	escaped := strings.Repeat("<", 512)
	subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: escaped, TenantID: escaped, AccountID: escaped, ALegID: escaped, RequestID: escaped, BillingCallID: escaped, CallID: escaped, BLegID: escaped, AttemptID: escaped, SubmissionID: escaped, ProviderAccountKey: escaped, ProviderRequestID: escaped, ProviderChargeID: escaped, PeriodID: escaped}
	if err := subject.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(subject)
	if err != nil {
		t.Fatal(err)
	}
	fields := []string{escaped, escaped, escaped, escaped, escaped, "operator", "provider", "request", string(b), escaped, escaped}
	var frame strings.Builder
	for _, field := range fields {
		fmt.Fprintf(&frame, "%d:%s", len(field), field)
	}
	if frame.Len() <= 32*1024 {
		t.Fatalf("fixture too small: %d", frame.Len())
	}
	v.SupportAdvisory.Pairs[0].ScopeKey = frame.String()
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	v.SupportAdvisory.Pairs = make([]SupportAdvisoryPair, MaxSupportAdvisoryPairs)
	for i := range v.SupportAdvisory.Pairs {
		v.SupportAdvisory.Pairs[i] = supportAdvisoryFixture().SupportAdvisory.Pairs[0]
		v.SupportAdvisory.Pairs[i].ScopeKey = fmt.Sprintf("scope-%d", i)
	}
	v.SupportAdvisoryContexts = make([]SupportAdvisoryContext, MaxSupportAdvisoryContexts)
	for i := range v.SupportAdvisoryContexts {
		v.SupportAdvisoryContexts[i] = supportAdvisoryFixture().SupportAdvisoryContexts[0]
		v.SupportAdvisoryContexts[i].Tariff.ID = fmt.Sprintf("tariff-%d", i)
	}
	for i := range v.SupportAdvisory.Pairs {
		v.SupportAdvisory.Pairs[i].ContextKey = v.SupportAdvisoryContexts[0].Key()
	}
	for _, c := range v.SupportAdvisoryContexts {
		for _, reason := range []SupportAdvisoryReason{SupportAdvisoryCandidateBudget, SupportAdvisoryGraphBudget, SupportAdvisoryPairLimit, SupportAdvisoryEvidenceUnavailable} {
			v.SupportAdvisory.IncompleteContexts = append(v.SupportAdvisory.IncompleteContexts, SupportAdvisoryIncomplete{c.Key(), reason})
		}
	}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSupportAdvisoryContextTimestampDuplicates(t *testing.T) {
	v := supportAdvisoryFixture()
	want, err := v.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	early := v.SupportAdvisoryContexts[0]
	early.Tariff.EffectiveAt = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	early.Tariff.FetchedAt = early.Tariff.EffectiveAt.Add(time.Hour)
	late := early
	late.Tariff.EffectiveAt = early.Tariff.EffectiveAt.Add(time.Hour)
	late.Tariff.FetchedAt = early.Tariff.FetchedAt.Add(time.Hour)
	for _, contexts := range [][]SupportAdvisoryContext{{early, late}, {late, early}} {
		v.SupportAdvisoryContexts = contexts
		clone := v.Clone()
		if clone.SupportAdvisoryContexts[0].Tariff != contexts[0].Tariff {
			t.Fatal("clone must retain timestamp metadata")
		}
		got, err := v.CanonicalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatal("timestamp metadata or duplicate order changed canonical bytes")
		}
		if v.SupportAdvisoryContexts[0].Tariff.EffectiveAt.IsZero() {
			t.Fatal("canonicalization mutated source timestamp")
		}
	}
}

func TestSupportAdvisoryCanonicalOrderAcrossContextsAndScopes(t *testing.T) {
	v := supportAdvisoryFixture()
	first := v.SupportAdvisoryContexts[0]
	second := first
	second.Tariff.ID = "z-tariff"
	p := v.SupportAdvisory.Pairs[0]
	q := p
	q.ScopeKey = "z-scope"
	r := p
	r.ContextKey = second.Key()
	v.SupportAdvisoryContexts = []SupportAdvisoryContext{second, first}
	v.SupportAdvisory.Pairs = []SupportAdvisoryPair{r, q, p}
	canonical, err := v.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if canonical.SupportAdvisoryContexts[0].Key() != first.Key() || canonical.SupportAdvisory.Pairs[0].ScopeKey != "scope" || canonical.SupportAdvisory.Pairs[1].ScopeKey != "z-scope" || canonical.SupportAdvisory.Pairs[2].ContextKey != second.Key() {
		t.Fatal("context/scope/pair order is not canonical")
	}
	if canonical.SupportAdvisory.Pairs[0].Left.Dimensions[0].Name != "a" {
		t.Fatal("dimensions not normalized")
	}
}
