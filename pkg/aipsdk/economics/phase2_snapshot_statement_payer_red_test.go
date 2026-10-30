package economics_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase2Repair_DerivedValuationRequiresReplayableSnapshotMaterialAndInputIdentity(t *testing.T) {
	t.Parallel()
	historical, err := json.Marshal(economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "legacy", Version: "v1"}, RaterID: "reference"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(historical), "content") {
		t.Fatalf("historical snapshot reference wire shape changed: %s", historical)
	}

	derived := validValuation(economics.BasisProviderQuantityLocal)
	derived.InputSetHash = ""
	derived.RaterContent = nil
	derived.TariffContent = nil
	derived.PolicyContent = nil
	derived.QualifierSnapshot = ""
	derived.QualifierSnapshotRef = nil
	if err := derived.Validate(); err == nil {
		t.Fatal("derived valuation accepted without an immutable input-set identity")
	}

	derived.InputSetHash = phase2Hash('1')
	if err := derived.Validate(); err == nil {
		t.Fatal("derived valuation accepted snapshot IDs without immutable snapshot material")
	}

	derived.RaterContent = &economics.SnapshotContentRef{ContentRef: "store://snapshots/rater/v1", ContentHash: phase2Hash('2')}
	derived.TariffContent = &economics.SnapshotContentRef{ContentRef: "store://snapshots/tariff/v1", ContentHash: phase2Hash('3')}
	derived.PolicyContent = &economics.SnapshotContentRef{ContentRef: "store://snapshots/policy/v1", ContentHash: phase2Hash('4')}
	derived.QualifierSnapshotRef = &economics.SnapshotContentRef{ContentRef: "store://snapshots/qualifiers/v1", ContentHash: phase2Hash('5')}
	if err := derived.Validate(); err != nil {
		t.Fatalf("complete derived valuation rejected: %v", err)
	}
	missingQualifier := derived
	missingQualifier.QualifierSnapshotRef = nil
	if err := missingQualifier.Validate(); err == nil {
		t.Fatal("derived valuation accepted a qualifier hash without resolvable material")
	}

	badHash := derived
	badHash.RaterContent = &economics.SnapshotContentRef{ContentRef: "store://snapshots/rater/v1", ContentHash: "not-a-content-hash"}
	if err := badHash.Validate(); err == nil {
		t.Fatal("derived valuation accepted malformed snapshot content hash")
	}
	snapshotIDWithHash := derived
	snapshotIDWithHash.RaterContent = &economics.SnapshotContentRef{ContentRef: "rater", ContentHash: phase2Hash('2')}
	if err := snapshotIDWithHash.Validate(); err != nil {
		t.Fatalf("a snapshot ID paired with a valid immutable content hash must remain valid: %v", err)
	}
	badQualifier := derived
	badQualifier.QualifierSnapshot = "arbitrary-qualifier-text"
	if err := badQualifier.Validate(); err == nil {
		t.Fatal("derived valuation accepted arbitrary qualifier snapshot text")
	}
}

func TestPhase2Repair_DirectProviderAndStatementValuationsDoNotInventTariffMaterial(t *testing.T) {
	t.Parallel()

	for _, basis := range []economics.ValuationBasis{economics.BasisProviderReported, economics.BasisStatementReported} {
		t.Run(string(basis), func(t *testing.T) {
			t.Parallel()
			v := validValuation(basis)
			v.Rater = economics.RatingSnapshotRef{}
			v.RaterContent = nil
			v.Tariff = economics.RatingSnapshotRef{}
			v.TariffContent = nil
			v.Policy = economics.PolicySnapshotRef{}
			v.PolicyContent = nil
			v.InputSetHash = ""
			v.QualifierSnapshot = ""
			v.QualifierSnapshotRef = nil
			if err := v.Validate(); err != nil {
				t.Fatalf("direct %s valuation must remain representable without invented tariff/policy: %v", basis, err)
			}
		})
	}
}

func TestPhase2Repair_RatingAndQuoteRefsRequireV2MaterialForDerivedPlanes(t *testing.T) {
	t.Parallel()

	rating := validRatingInput()
	rating.Basis = economics.BasisProviderQuantityLocal
	rating.InputSetHash = phase2Hash('6')
	if err := rating.Validate(); err == nil {
		t.Fatal("derived rating input accepted version refs without immutable material")
	}
	rating.RaterContent = &economics.SnapshotContentRef{ContentRef: "store://snapshots/rater/v1", ContentHash: phase2Hash('7')}
	rating.TariffContent = &economics.SnapshotContentRef{ContentRef: "store://snapshots/tariff/v1", ContentHash: phase2Hash('8')}
	rating.PolicyContent = &economics.SnapshotContentRef{ContentRef: "store://snapshots/policy/v1", ContentHash: phase2Hash('9')}
	rating.QualifierSnapshotRef = &economics.SnapshotContentRef{ContentRef: "store://snapshots/qualifiers/v1", ContentHash: phase2Hash('a')}
	if err := rating.Validate(); err != nil {
		t.Fatalf("complete derived rating input rejected: %v", err)
	}

	quote := validQuoteInput()
	quote.Basis = economics.BasisCustomerPolicy
	quote.InputSetHash = phase2Hash('a')
	quote.PolicyContent = nil
	if err := quote.Validate(); err == nil {
		t.Fatal("customer-policy quote accepted a policy ID without immutable material")
	}
	quote.PolicyContent = &economics.SnapshotContentRef{ContentRef: "store://snapshots/policy/v1", ContentHash: phase2Hash('a')}
	if err := quote.Validate(); err != nil {
		t.Fatalf("customer-policy quote with immutable policy material rejected: %v", err)
	}

	exposure := validExposureQuote()
	exposure.Basis = economics.BasisCustomerPolicy
	exposure.InputSetHash = phase2Hash('b')
	exposure.Tariff = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff", Version: "v1"}}
	exposure.TariffContent = &economics.SnapshotContentRef{ContentRef: "store://snapshots/tariff/v1", ContentHash: phase2Hash('c')}
	exposure.PolicyContent = &economics.SnapshotContentRef{ContentRef: "store://snapshots/policy/v1", ContentHash: phase2Hash('d')}
	exposure.QualifierSnapshotRef = &economics.SnapshotContentRef{ContentRef: "store://snapshots/qualifiers/v1", ContentHash: phase2Hash('e')}
	if err := exposure.Validate(); err != nil {
		t.Fatalf("derived exposure quote with immutable refs rejected: %v", err)
	}
}

func TestPhase2Repair_StatementLinesResolveExactIncludedObservationChargeOrDeclareUnmatched(t *testing.T) {
	t.Parallel()

	observation := phase2StatementObservation(t)
	ref := metering.ObservationRef{
		StoreID: observation.Subject.StoreID, ObservationID: observation.ID, Revision: observation.Revision,
		PayloadHash: observation.Fingerprint(),
	}
	subject := observation.Subject
	base := economics.StatementBatch{
		Version: 1, ProviderAccountKey: "provider-account", StatementID: "statement-1", Revision: 1, PeriodID: "period-1",
		Subject: subject, Observations: []metering.Observation{observation},
		Lines: []economics.StatementLine{{
			ID: "line-1", Revision: 1, Subject: subject, Observation: ref, ChargeItemID: "charge-1", Outcome: economics.StatementLineMatched,
		}},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("exact statement line linkage rejected: %v", err)
	}

	for name, mutate := range map[string]func(*economics.StatementBatch){
		"missing observation": func(b *economics.StatementBatch) { b.Lines[0].Observation.ObservationID = "not-included" },
		"wrong revision":      func(b *economics.StatementBatch) { b.Lines[0].Observation.Revision = 2 },
		"wrong payload hash":  func(b *economics.StatementBatch) { b.Lines[0].Observation.PayloadHash = phase2Hash('b') },
		"missing charge":      func(b *economics.StatementBatch) { b.Lines[0].ChargeItemID = "charge-not-included" },
		"foreign account":     func(b *economics.StatementBatch) { b.Lines[0].Subject.ProviderAccountKey = "other-account" },
		"foreign statement":   func(b *economics.StatementBatch) { b.Lines[0].Subject.StatementID = "other-statement" },
		"foreign period":      func(b *economics.StatementBatch) { b.Lines[0].Subject.PeriodID = "other-period" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			invalid := base
			invalid.Lines = append([]economics.StatementLine(nil), base.Lines...)
			invalid.Lines[0].Subject = base.Lines[0].Subject
			mutate(&invalid)
			if err := invalid.Validate(); err == nil {
				t.Fatalf("statement batch accepted %s linkage", name)
			}
		})
	}
	for name, mutate := range map[string]func(*economics.StatementBatch){
		"foreign included observation account":   func(b *economics.StatementBatch) { b.Observations[0].Subject.ProviderAccountKey = "other-account" },
		"foreign included observation statement": func(b *economics.StatementBatch) { b.Observations[0].Subject.StatementID = "other-statement" },
		"missing included observation period":    func(b *economics.StatementBatch) { b.Observations[0].Subject.PeriodID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			invalid := base
			invalid.Observations = append([]metering.Observation(nil), base.Observations...)
			invalid.Lines = append([]economics.StatementLine(nil), base.Lines...)
			mutate(&invalid)
			invalid.Lines[0].Observation.PayloadHash = invalid.Observations[0].Fingerprint()
			if err := invalid.Validate(); err == nil {
				t.Fatalf("statement batch accepted %s linkage", name)
			}
		})
	}

	duplicate := base
	duplicate.Lines = append([]economics.StatementLine(nil), base.Lines...)
	duplicate.Lines = append(duplicate.Lines, economics.StatementLine{
		ID: "line-2", Revision: 1, Subject: subject, Observation: ref, ChargeItemID: "charge-1", Outcome: economics.StatementLineMatched,
	})
	if err := duplicate.Validate(); err == nil {
		t.Fatal("statement batch accepted duplicate observation/charge linkage")
	}

	unmatched := economics.StatementBatch{
		Version: 1, ProviderAccountKey: "provider-account", StatementID: "statement-1", Revision: 1, PeriodID: "period-1",
		Subject: subject,
		Lines: []economics.StatementLine{{
			ID: "line-unmatched", Revision: 1, Subject: func() metering.SubjectRef {
				out := subject
				out.StatementLineID = "line-unmatched"
				return out
			}(), Outcome: economics.StatementLineUnmatched,
			UnmatchedReason: "statement is account-period aggregate",
		}},
	}
	if err := unmatched.Validate(); err != nil {
		t.Fatalf("explicit unmatched statement line should not require invented B-leg attribution: %v", err)
	}
}

func TestPhase2Repair_PayerOwnershipIsTypedAndNeverDefaultsToOperator(t *testing.T) {
	t.Parallel()

	providerCharge := phase2ProviderChargeObservation(t)
	providerCharge.Charges[0].Payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-byok"}
	if err := providerCharge.Validate(); err != nil {
		t.Fatalf("customer-BYOK charge rejected: %v", err)
	}

	operator := providerCharge.Clone()
	operator.Charges[0].Payer = metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "operator"}
	if operator.Charges[0].Payer == providerCharge.Charges[0].Payer {
		t.Fatal("operator and customer payer classifications must remain distinct")
	}

	unknown := providerCharge.Clone()
	unknown.Charges[0].Payer = metering.PaymentParty{}
	if err := unknown.Validate(); err != nil {
		t.Fatalf("absent payer must remain a valid unknown state: %v", err)
	}
	if unknown.Charges[0].Payer.Kind == metering.PaymentPartyOperator {
		t.Fatal("absent payer must not default to operator")
	}
	for _, kind := range []metering.PaymentPartyKind{metering.PaymentPartyUnknown, metering.PaymentPartyUnallocated} {
		invalid := providerCharge.Clone()
		invalid.Charges[0].Payer = metering.PaymentParty{Kind: kind, ID: "claimed-owner"}
		if err := invalid.Validate(); err == nil {
			t.Fatalf("%s payer must not carry an attributable owner ID", kind)
		}
	}

	v := validValuation(economics.BasisProviderReported)
	v.Payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-byok"}
	if err := v.Validate(); err != nil {
		t.Fatalf("customer-owned valuation rejected: %v", err)
	}
	wire, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), "customer-byok") {
		t.Fatalf("payer ownership was lost from valuation wire: %s", wire)
	}
	var decoded economics.Valuation
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Payer.Kind != metering.PaymentPartyCustomer || decoded.Payer.ID != "customer-byok" {
		t.Fatalf("payer ownership changed across valuation round-trip: %+v", decoded.Payer)
	}
}

func phase2StatementObservation(t *testing.T) metering.Observation {
	t.Helper()
	o := phase2ProviderChargeObservation(t)
	o.ID = "statement-observation-1"
	o.SourceEventKey = "statement-event-1"
	o.Origin = metering.OriginStatement
	o.Acquisition = metering.AcquisitionStatementImporter
	o.Authority = metering.AuthorityVerifiedStatement
	o.Subject = metering.SubjectRef{
		Kind: metering.SubjectStatementLine, StoreID: "store-1", ProviderAccountKey: "provider-account",
		StatementID: "statement-1", StatementLineID: "line-1", PeriodID: "period-1",
	}
	o.Correlation = metering.CorrelationV2{StoreID: "store-1", ProviderAccountKey: "provider-account", PeriodID: "period-1"}
	return o
}

func phase2ProviderChargeObservation(t *testing.T) metering.Observation {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	o := metering.Observation{
		Version: 2, ID: "provider-observation-1", SourceEventKey: "provider-event-1", Revision: 1,
		StreamID: "stream-1", Sequence: 1, Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-1", ALegID: "a-1", BillingCallID: "call-1", BLegID: "b-1"},
		Correlation: metering.CorrelationV2{StoreID: "store-1", RequestID: "req-1", CallID: "call-1", BillingCallID: "call-1", ALegID: "a-1", BLegID: "b-1", AttemptID: "attempt-1"},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "provider:test:v1",
		Measures: []metering.Measure{{
			Key:   metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "provider:audio:v1"},
			Value: decimalPtr("1"), Quality: metering.QualityObserved,
		}},
	}
	o.ID = "provider-observation-1"
	o.SourceEventKey = "provider-event-1"
	o.Charges = []metering.ReportedCharge{{
		ChargeItemID: "charge-1", Amount: decimalPtr("1.25"), Currency: "USD", Kind: metering.ChargeKindComponent,
		Component: &metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "provider:audio:v1"},
	}}
	o.Measures = nil
	return o
}

func phase2Hash(ch byte) string {
	return strings.Repeat(string(ch), 64)
}
