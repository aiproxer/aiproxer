package metering

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func phase11ProviderDebit(t *testing.T, id, value string) ProviderDebit {
	t.Helper()
	quantity, err := ParseDecimal(value)
	if err != nil {
		t.Fatalf("parse debit quantity: %v", err)
	}
	return ProviderDebit{
		Version:            ProviderDebitVersionV1,
		ID:                 id,
		SourceEventKey:     id + "-source",
		Revision:           1,
		StreamID:           "provider-debit-stream",
		Sequence:           1,
		StoreID:            "store-11",
		TenantID:           "tenant-11",
		ProviderAccountKey: "provider-account-11",
		PoolID:             "pool-11",
		WindowID:           "window-11",
		ResetAt:            time.Unix(1_000, 0).UTC(),
		RequestID:          "request-11",
		BillingCallID:      "billing-call-11",
		BLegID:             "b-leg-11",
		ALegID:             "a-leg-11",
		AttemptID:          "attempt-11",
		ProviderRequestID:  "provider-request-11",
		Component: ComponentKey{
			Direction: DirectionNone,
			Component: ComponentCredit,
			Unit:      UnitCredit,
		},
		Quantity:    &quantity,
		Quality:     QualityObserved,
		MethodRef:   "provider-api-v1",
		Acquisition: AcquisitionProviderResponse,
		Authority:   AuthorityObservedClaim,
		Semantics:   SemanticsDelta,
		ObservedAt:  time.Unix(2_000, 0).UTC(),
		ReceivedAt:  time.Unix(2_001, 0).UTC(),
	}
}

func TestProviderDebit_RoundTripsAsRequestScopedObservation(t *testing.T) {
	debit := phase11ProviderDebit(t, "debit-1", "0.125")

	if err := debit.Validate(); err != nil {
		t.Fatalf("valid provider debit rejected: %v", err)
	}
	payload, err := debit.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical provider debit: %v", err)
	}
	var decoded ProviderDebit
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode provider debit: %v", err)
	}
	if got, want := decoded.ReplayFingerprint(), debit.ReplayFingerprint(); got != want {
		t.Fatalf("replay fingerprint after JSON round-trip = %q, want %q", got, want)
	}

	observation, err := debit.ToObservation()
	if err != nil {
		t.Fatalf("provider debit observation: %v", err)
	}
	if err := observation.Validate(); err != nil {
		t.Fatalf("provider debit observation invalid: %v", err)
	}
	if observation.Subject.Kind != SubjectProviderDebit {
		t.Fatalf("subject kind = %q, want provider_debit", observation.Subject.Kind)
	}
	if observation.Subject.RequestID != debit.RequestID || observation.Subject.BillingCallID != debit.BillingCallID || observation.Subject.BLegID != debit.BLegID {
		t.Fatalf("request lineage was not retained: %+v", observation.Subject)
	}
	if observation.Subject.ProviderAccountKey != debit.ProviderAccountKey || observation.Subject.PoolID != debit.PoolID || observation.Subject.WindowID != debit.WindowID || !observation.Subject.ResetAt.Equal(debit.ResetAt) {
		t.Fatalf("provider account-window binding was not retained: %+v", observation.Subject)
	}
	if len(observation.Measures) != 1 || observation.Measures[0].Value == nil || !observation.Measures[0].Value.Equal(*debit.Quantity) {
		t.Fatalf("debit measure = %+v, want %s", observation.Measures, debit.Quantity.CanonicalString())
	}
	if len(observation.Charges) != 0 {
		t.Fatalf("provider unit debit unexpectedly carried money charges: %+v", observation.Charges)
	}

	restored, err := ProviderDebitFromObservation(observation)
	if err != nil {
		t.Fatalf("provider debit from observation: %v", err)
	}
	if got, want := restored.ReplayFingerprint(), debit.ReplayFingerprint(); got != want {
		t.Fatalf("replay fingerprint after observation round-trip = %q, want %q", got, want)
	}
}

func TestProviderDebit_RejectsGaugeAndUntrustedEvidence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ProviderDebit)
		want   string
	}{
		{name: "gauge semantics", mutate: func(d *ProviderDebit) { d.Semantics = SemanticsGauge }, want: "gauge"},
		{name: "estimated quality", mutate: func(d *ProviderDebit) { d.Quality = QualityEstimated }, want: "quality"},
		{name: "estimated authority", mutate: func(d *ProviderDebit) { d.Authority = AuthorityEstimatedClaim }, want: "authority"},
		{name: "percent component", mutate: func(d *ProviderDebit) {
			d.Component = ComponentKey{Direction: DirectionNone, Component: "window_utilization", Unit: UnitPercent, SchemaID: "provider-window-v1"}
		}, want: "percent"},
		{name: "remaining credits", mutate: func(d *ProviderDebit) {
			d.Component = ComponentKey{Direction: DirectionNone, Component: "remaining_credits", Unit: UnitCredit, SchemaID: "provider-window-v1"}
		}, want: "gauge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			debit := phase11ProviderDebit(t, "debit-invalid-"+strings.ReplaceAll(tc.name, " ", "-"), "1")
			tc.mutate(&debit)
			if err := debit.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestProviderDebit_AccountWindowObservationCannotBecomeDebit(t *testing.T) {
	reset := time.Unix(3_000, 0).UTC()
	percent := Decimal{Coefficient: "125", Scale: 1}
	window := Observation{
		Version:        ObservationVersionV2,
		ID:             "window-observation",
		SourceEventKey: "window-source",
		Revision:       1,
		StreamID:       "window-stream",
		Sequence:       1,
		Origin:         OriginProvider,
		Acquisition:    AcquisitionProviderResponse,
		Authority:      AuthorityObservedClaim,
		Perspective:    PerspectiveOperator,
		Boundary:       BoundaryBackendEgress,
		Lifecycle:      LifecycleBackendAttempt,
		Subject: SubjectRef{
			Kind:               SubjectAccountWindow,
			StoreID:            "store-11",
			ProviderAccountKey: "provider-account-11",
			PoolID:             "pool-11",
			WindowID:           "window-11",
			ResetAt:            reset,
		},
		Correlation: CorrelationV2{StoreID: "store-11"},
		Semantics:   SemanticsGauge,
		MappingRef:  "account_window_v1",
		ObservedAt:  time.Unix(3_001, 0).UTC(),
		ReceivedAt:  time.Unix(3_002, 0).UTC(),
		Measures: []Measure{{
			Key:     ComponentKey{Direction: DirectionNone, Component: "window_utilization", Unit: UnitPercent, SchemaID: "provider-window-v1"},
			Value:   &percent,
			Quality: QualityObserved,
		}},
	}
	if err := window.Validate(); err != nil {
		t.Fatalf("valid account-window observation rejected: %v", err)
	}
	if _, err := ProviderDebitFromObservation(window); err == nil {
		t.Fatal("account-window gauge was accepted as request debit")
	}
}
