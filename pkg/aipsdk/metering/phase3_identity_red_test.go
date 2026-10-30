package metering_test

import (
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Phase 3.1 RED public identity/correction seams (requirements 5.5–5.7, 6.1–6.4,
// 13.1; design Deterministic Identity / Corrections, D2, D6, D7, D17).
//
// Deferred production seams to tasks 3.2–3.4 (no fake compile sentinels):
// SourceEventRef encoding API, schema V2 columns, store-scoped source_event_key
// uniqueness, and supersession graph persistence.

func TestPhase3_Fact_IdentityVersionAndRevisionFields(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeFor[metering.Fact]()
	for _, name := range []string{"IdentityVersion", "SourceRevision", "SourceEventKind", "SourceID"} {
		if _, ok := rt.FieldByName(name); !ok {
			t.Fatalf("Fact.%s missing (design Deterministic Identity / V-06; task 3.2)", name)
		}
	}
}

func TestPhase3_Fact_SourceEventKeyIncludesIdentityVersionRevision(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeFor[metering.Fact]()
	_, hasVersion := rt.FieldByName("IdentityVersion")
	_, hasRevision := rt.FieldByName("SourceRevision")
	if !hasVersion || !hasRevision {
		t.Fatal("Fact lacks IdentityVersion/SourceRevision required for source_event_key (design Deterministic Identity; task 3.2)")
	}
	if _, ok := rt.MethodByName("SourceEventKey"); !ok {
		// IdempotencyKey today is StreamID+FactID only; V2 requires versioned source key.
		if _, ok := rt.MethodByName("CanonicalSourceEventKey"); !ok {
			t.Fatal("Fact.SourceEventKey/CanonicalSourceEventKey missing (design Deterministic Identity; task 3.2)")
		}
	}
}

func TestPhase3_OrdinaryFact_RejectsNegativeQuantity(t *testing.T) {
	t.Parallel()
	f := phase3CustomerIngressFact("req-neg", "fe-neg", 1)
	f.Kind = metering.FactKindDelta
	f.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     -1,
		Present:   true,
	}}
	if err := f.Validate(); err == nil {
		t.Fatal("ordinary non-correction fact must reject negative quantity (req 6.4; task 3.2)")
	}
}

func TestPhase3_OrdinaryFact_RejectsNegativeMoney(t *testing.T) {
	t.Parallel()
	f := phase3OperatorEgressFact("att-neg", "be-neg", 1)
	f.Kind = metering.FactKindCumulative
	f.Money = &metering.MoneyObservation{NanoUnits: -5, Currency: "USD", Present: true, Source: metering.SourceProviderReported}
	if err := f.Validate(); err == nil {
		t.Fatal("ordinary non-correction fact must reject negative money (req 6.4; task 3.2)")
	}
}

func TestPhase3_CorrectionFact_AllowsSignedNegativeDelta(t *testing.T) {
	t.Parallel()
	f := phase3OperatorEgressFact("att-corr", "be-corr", 2)
	f.Kind = metering.FactKindCorrection
	f.Supersedes = []string{"be-base"}
	f.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     -3,
		Present:   true,
	}}
	if err := f.Validate(); err != nil {
		t.Fatalf("signed correction delta must validate: %v (req 6.4; design Corrections)", err)
	}
}

func TestPhase3_PerspectiveBoundaryLifecycle_CustomerUsesFrontend(t *testing.T) {
	t.Parallel()
	bad := phase3CustomerIngressFact("req-d2", "fe-d2", 1)
	bad.Boundary = metering.BoundaryBackendEgress
	if err := bad.Validate(); err == nil {
		t.Fatal("customer perspective must reject backend boundary (req 5.5; design D2; task 3.2)")
	}
	badLife := phase3CustomerIngressFact("req-d2b", "fe-d2b", 1)
	badLife.Lifecycle = metering.LifecycleBackendAttempt
	if err := badLife.Validate(); err == nil {
		t.Fatal("customer frontend fact must reject backend-attempt lifecycle (req 5.5; task 3.2)")
	}
}

func TestPhase3_PerspectiveBoundaryLifecycle_OperatorUsesBackend(t *testing.T) {
	t.Parallel()
	bad := phase3OperatorIngressFact("att-d2", "be-d2", 1)
	bad.Boundary = metering.BoundaryFrontendIngress
	if err := bad.Validate(); err == nil {
		t.Fatal("operator perspective must reject frontend boundary (req 5.5; design D2; task 3.2)")
	}
}

func phase3CustomerIngressFact(requestID, factID string, seq int64) metering.Fact {
	return metering.Fact{
		FactID:      factID,
		StreamID:    "customer-request:" + requestID,
		Sequence:    seq,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveCustomer,
		Boundary:    metering.BoundaryFrontendIngress,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Correlation: metering.Correlation{RequestID: requestID, TraceID: "trace-" + requestID},
		FrontendID:  "openai-responses",
		Source:      metering.SourceObserved,
		Authority:   metering.AuthorityAuthoritative,
		Presence:    metering.PresencePresent,
		Quantities: []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     10,
			Present:   true,
		}},
	}
}

func phase3OperatorIngressFact(attemptID, factID string, seq int64) metering.Fact {
	return metering.Fact{
		FactID:      factID,
		StreamID:    "operator-attempt:" + attemptID,
		Sequence:    seq,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveOperator,
		Boundary:    metering.BoundaryBackendIngress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Correlation: metering.Correlation{
			RequestID: "req-" + attemptID,
			AttemptID: attemptID,
			BLegID:    "b-" + attemptID,
		},
		BackendID: "openai",
		Model:     "gpt-test",
		Source:    metering.SourceDerived,
		Authority: metering.AuthorityAuthoritative,
		Presence:  metering.PresencePresent,
		Quantities: []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     12,
			Present:   true,
		}},
	}
}

func phase3OperatorEgressFact(attemptID, factID string, seq int64) metering.Fact {
	f := phase3OperatorIngressFact(attemptID, factID, seq)
	f.Boundary = metering.BoundaryBackendEgress
	f.Source = metering.SourceProviderReported
	f.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     4,
		Present:   true,
	}}
	return f
}
