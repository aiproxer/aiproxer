package economics

import (
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase9Repair5_ValuationContextHashIncludesTrustedSnapshotIdentity(t *testing.T) {
	t.Parallel()
	base := Valuation{
		Perspective: metering.PerspectiveCustomer,
		Basis:       BasisCustomerPolicy,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store", BLegID: "b"},
		Scope:       "call:phase9",
		Rater:       RatingSnapshotRef{VersionRef: VersionRef{ID: "rater", Version: "v1"}, RaterID: "reference"},
		RaterContent: &SnapshotContentRef{
			ContentRef: "catalog://rater/v1", ContentHash: "1111111111111111111111111111111111111111111111111111111111111111",
		},
		Tariff: RatingSnapshotRef{VersionRef: VersionRef{ID: "tariff", Version: "v1"}, RaterID: "reference"},
		TariffContent: &SnapshotContentRef{
			ContentRef: "catalog://tariff/v1", ContentHash: "2222222222222222222222222222222222222222222222222222222222222222",
		},
		Policy: PolicySnapshotRef{VersionRef: VersionRef{ID: "policy", Version: "v1"}, PolicyID: "customer"},
		PolicyContent: &SnapshotContentRef{
			ContentRef: "catalog://policy/v1", ContentHash: "3333333333333333333333333333333333333333333333333333333333333333",
		},
		QualifierSnapshot: "4444444444444444444444444444444444444444444444444444444444444444",
		QualifierSnapshotRef: &SnapshotContentRef{
			ContentRef: "catalog://qualifiers/v1", ContentHash: "4444444444444444444444444444444444444444444444444444444444444444",
		},
		Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"},
	}
	useSupportAdvisoryIdentityMoney(&base)
	if base.ContextHash() == "" {
		t.Fatal("base context hash is empty")
	}
	variants := []struct {
		name   string
		mutate func(*Valuation)
	}{
		{name: "rater identity", mutate: func(v *Valuation) { v.Rater.ID = "other-rater" }},
		{name: "rater content", mutate: func(v *Valuation) {
			v.RaterContent.ContentHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{name: "tariff identity", mutate: func(v *Valuation) { v.Tariff.ID = "other-tariff" }},
		{name: "tariff content", mutate: func(v *Valuation) {
			v.TariffContent.ContentHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}},
		{name: "policy identity", mutate: func(v *Valuation) { v.Policy.ID = "other-policy" }},
		{name: "policy content", mutate: func(v *Valuation) {
			v.PolicyContent.ContentHash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		}},
		{name: "qualifier content", mutate: func(v *Valuation) {
			v.QualifierSnapshotRef.ContentHash = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		}},
		{name: "payer", mutate: func(v *Valuation) { v.Payer.ID = "other-customer" }},
		{name: "support advisory enabled", mutate: func(v *Valuation) {
			v.SupportAdvisoryContexts = []SupportAdvisoryContext{supportAdvisoryIdentityContext()}
		}},
	}
	for _, tc := range variants {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			variant := base.Clone()
			tc.mutate(&variant)
			if got := variant.ContextHash(); got == "" || got == base.ContextHash() {
				t.Fatalf("context hash=%q, want variant distinct from %q", got, base.ContextHash())
			}
			if !reflect.DeepEqual(base.Lines, variant.Lines) || !reflect.DeepEqual(base.Totals, variant.Totals) || base.Completeness != variant.Completeness {
				t.Fatal("changing interpretation context changed valuation money")
			}
		})
	}
	if replay := base.Clone().ContextHash(); replay != base.ContextHash() {
		t.Fatalf("same-context replay hash changed: %q/%q", base.ContextHash(), replay)
	}
}
