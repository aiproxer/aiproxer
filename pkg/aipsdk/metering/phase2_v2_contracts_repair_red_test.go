package metering_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestPhase2RepairSafeEvidenceUsesEconomicLocationsAndValidLexemes(t *testing.T) {
	t.Parallel()

	accepted := []metering.SafeEvidenceField{
		{Path: "$.usage.audio_seconds", Lexeme: "12.5", Present: true, Acquisition: metering.AcquisitionProviderResponse},
		{Path: "$.provider_schema.custom_cost", Lexeme: "0.25", Present: true, Acquisition: metering.AcquisitionProviderResponse},
		{Name: "X-Usage-Output-Tokens", Value: "42", Present: true, Acquisition: metering.AcquisitionProviderHeader},
		{Name: "x-request-id", Value: "request-1", Present: true, Acquisition: metering.AcquisitionProviderHeader},
	}
	for _, field := range accepted {
		if err := field.Validate(); err != nil {
			t.Errorf("safe economic evidence rejected: %+v: %v", field, err)
		}
	}

	rejected := []metering.SafeEvidenceField{
		{Path: "$.provider.payload", Lexeme: "value", Present: true, Acquisition: metering.AcquisitionProviderResponse},
		{Path: "$.arbitrary.tokens", Lexeme: "42", Present: true, Acquisition: metering.AcquisitionProviderResponse},
		{Path: "$.provider_schema.payload_bytes", Lexeme: "42", Present: true, Acquisition: metering.AcquisitionProviderResponse},
		{Path: "$.prompt.text", Lexeme: "do not retain", Present: true, Acquisition: metering.AcquisitionProviderResponse},
		{Path: "$.tool_args.query", Lexeme: "secret query", Present: true, Acquisition: metering.AcquisitionProviderResponse},
		{Path: "$.ciphertext", Lexeme: "opaque", Present: true, Acquisition: metering.AcquisitionProviderResponse},
		{Path: "$.output.text", Lexeme: "model output", Present: true, Acquisition: metering.AcquisitionProviderResponse},
		{Name: "Authorization", Value: "Bearer secret", Present: true, Acquisition: metering.AcquisitionProviderHeader},
		{Name: "Cookie", Value: "session=secret", Present: true, Acquisition: metering.AcquisitionProviderHeader},
		{Name: "X-Api-Key", Value: "secret", Present: true, Acquisition: metering.AcquisitionProviderHeader},
		{Name: "x-provider-custom-field", Value: "value", Present: true, Acquisition: metering.AcquisitionProviderHeader},
		{Name: "X-Usage-Anything-Bytes", Value: "42", Present: true, Acquisition: metering.AcquisitionProviderHeader},
		{Name: "X-Usage-Output-Tokens-Anything", Value: "42", Present: true, Acquisition: metering.AcquisitionProviderHeader},
		{Name: "X-Usage-Output-Text", Value: "model output", Present: true, Acquisition: metering.AcquisitionProviderHeader},
		{Path: "$.usage.audio_seconds", Lexeme: string([]byte{0xff}), Present: true, Acquisition: metering.AcquisitionProviderResponse},
		{Path: "$.usage.audio_seconds", Lexeme: "value", Present: true, Acquisition: "provider_custom"},
	}
	for _, field := range rejected {
		if err := field.Validate(); err == nil {
			t.Errorf("unsafe or unbounded evidence accepted: %+v", field)
		}
	}
	malformedJSON := append([]byte(`{"path":"$.usage.audio_seconds","lexeme":"`), 0xff)
	malformedJSON = append(malformedJSON, []byte(`","present":true,"acquisition":"provider_response"}`)...)
	var malformed metering.SafeEvidenceField
	if err := json.Unmarshal(malformedJSON, &malformed); err == nil {
		t.Fatal("malformed UTF-8 evidence JSON must be rejected before replacement decoding")
	}
}

func TestPhase2RepairObservationRetainsExplicitAxesAndV1Scope(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0).UTC()
	wantScope := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectService,
		PrincipalID: scope.Known("principal-1"),
		TenantID:    scope.Known("tenant-1"),
		Roles:       []string{"operator"},
		SafeClaims:  map[string]string{"tier": "gold"},
		Origin:      scope.OriginClient,
	}
	fact := metering.Fact{
		FactID: "fact-scope", StreamID: "stream-1", Sequence: 3,
		Kind: metering.FactKindDelta, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Correlation: metering.Correlation{RequestID: "request-1", ALegID: "a-1", BLegID: "b-1", AttemptID: "attempt-1"},
		Scope:       wantScope,
		Source:      metering.SourceProviderReported, Authority: metering.AuthorityAuthoritative,
		Presence: metering.PresencePresent, RecordedAt: now,
		Quantities: []metering.Quantity{{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 12, Present: true}},
	}
	observation, err := metering.ObservationFromFact(fact)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Perspective != fact.Perspective || observation.Boundary != fact.Boundary || observation.Lifecycle != fact.Lifecycle {
		t.Fatalf("V1 axes were not retained: got perspective=%q boundary=%q lifecycle=%q", observation.Perspective, observation.Boundary, observation.Lifecycle)
	}
	if !reflect.DeepEqual(observation.Scope, fact.Scope) {
		t.Fatalf("V1 scope was not retained: got=%+v want=%+v", observation.Scope, fact.Scope)
	}

	projectedInput := validV2Observation(t)
	projectedInput.Origin = metering.OriginLocal
	projectedInput.Acquisition = metering.AcquisitionLocalEstimator
	projectedInput.Authority = metering.AuthorityEstimatedClaim
	projectedInput.Perspective = metering.PerspectiveCustomer
	projectedInput.Boundary = metering.BoundaryFrontendEgress
	projectedInput.Lifecycle = metering.LifecycleLogicalRequest
	projectedInput.Subject = metering.SubjectRef{Kind: metering.SubjectRequest, StoreID: "store-1", RequestID: "request-1"}
	projectedInput.Correlation = metering.CorrelationV2{StoreID: "store-1", RequestID: "request-1"}
	projectedInput.Scope = wantScope
	projectedInput.Measures = []metering.Measure{{
		Key:   metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID},
		Value: decimalPtr("12"), Quality: metering.QualityObserved,
	}}

	projected, err := metering.ProjectObservationToFact(projectedInput)
	if err != nil {
		t.Fatal(err)
	}
	if projected.Perspective != projectedInput.Perspective || projected.Boundary != projectedInput.Boundary || projected.Lifecycle != projectedInput.Lifecycle {
		t.Fatalf("V2 explicit axes were inferred/replaced: got perspective=%q boundary=%q lifecycle=%q", projected.Perspective, projected.Boundary, projected.Lifecycle)
	}
	if !reflect.DeepEqual(projected.Scope, projectedInput.Scope) {
		t.Fatalf("V2 scope was not retained: got=%+v want=%+v", projected.Scope, projectedInput.Scope)
	}
}

func TestPhase2RepairProviderInferenceRequiresBLegAndAttemptLineage(t *testing.T) {
	t.Parallel()

	requestScoped := validV2Observation(t)
	requestScoped.Subject = metering.SubjectRef{Kind: metering.SubjectRequest, StoreID: "store-1", RequestID: "request-1"}
	requestScoped.Correlation = metering.CorrelationV2{StoreID: "store-1", RequestID: "request-1"}
	if err := requestScoped.Validate(); err == nil {
		t.Fatal("provider inference usage must not use a request subject without a B-leg")
	}

	standaloneAttempt := validV2Observation(t)
	standaloneAttempt.Correlation.BLegID = ""
	standaloneAttempt.Correlation.AttemptSeq = 2
	if err := standaloneAttempt.Validate(); err == nil {
		t.Fatal("AttemptSeq must not be accepted as standalone ownership")
	}
	if err := (metering.CorrelationV2{StoreID: "store-1", AttemptSeq: 2}).Validate(); err == nil {
		t.Fatal("CorrelationV2 must reject standalone AttemptSeq")
	}

	withoutProviderChargeID := validV2Observation(t)
	withoutProviderChargeID.Correlation.ProviderChargeID = ""
	withoutProviderChargeID.Subject.ProviderChargeID = ""
	if err := withoutProviderChargeID.Validate(); err != nil {
		t.Fatalf("ProviderChargeID must remain optional for a B-leg: %v", err)
	}

	legacyFact := metering.Fact{
		FactID: "legacy-request", StreamID: "stream-1", Sequence: 2, Kind: metering.FactKindDelta,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Correlation: metering.Correlation{RequestID: "request-legacy"},
		Source:      metering.SourceProviderReported, Authority: metering.AuthorityAuthoritative,
		Presence:   metering.PresencePresent,
		Quantities: []metering.Quantity{{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 1, Present: true}},
	}
	legacyObservation, err := metering.ObservationFromFact(legacyFact)
	if err != nil {
		t.Fatalf("legacy provider request shape must remain readable: %v", err)
	}
	wire, err := json.Marshal(legacyObservation)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := metering.ReadLegacyV1Observation(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("persisted legacy provider request shape must remain validateable: %v", err)
	}
	spoofedLegacy := legacyObservation
	spoofedLegacy.Acquisition = metering.AcquisitionProviderResponse
	if err := spoofedLegacy.Validate(); err == nil {
		t.Fatal("provider response must not opt into the private legacy request exception")
	}

	for _, unsupported := range []struct {
		name      string
		source    metering.Source
		authority metering.Authority
	}{
		{name: "derived", source: metering.SourceDerived, authority: metering.AuthorityAuthoritative},
		{name: "configured", source: metering.SourceConfigured, authority: metering.AuthorityAuthoritative},
		{name: "delegated", source: metering.SourceObserved, authority: metering.AuthorityDelegated},
		{name: "advisory", source: metering.SourceObserved, authority: metering.AuthorityAdvisory},
	} {
		legacyFact.Source, legacyFact.Authority = unsupported.source, unsupported.authority
		if _, err := metering.ObservationFromFact(legacyFact); !errors.Is(err, metering.ErrUnrepresentableV1) {
			t.Errorf("%s V1 provenance must fail closed, err=%v", unsupported.name, err)
		}
	}
}

func TestPhase2RepairAccountWindowGaugeAndResourceSemantics(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0).UTC()

	accountWindow := validV2Observation(t)
	accountWindow.ID = "account-window"
	accountWindow.Subject = metering.SubjectRef{
		Kind: metering.SubjectAccountWindow, StoreID: "store-1", ProviderAccountKey: "provider-account",
		PoolID: "primary", WindowID: "minute", ResetAt: now.Add(time.Minute),
	}
	accountWindow.Correlation = metering.CorrelationV2{StoreID: "store-1", ProviderAccountKey: "provider-account"}
	accountWindow.Semantics = metering.SemanticsGauge
	accountWindow.Measures = []metering.Measure{{
		Key:   metering.ComponentKey{Direction: metering.DirectionNone, Component: "provider:account_utilization", Unit: metering.UnitPercent, SchemaID: "provider:account:v1"},
		Value: decimalPtr("12.5"), Quality: metering.QualityObserved,
	}}
	if err := accountWindow.Validate(); err != nil {
		t.Fatalf("account-window gauge rejected: %v", err)
	}

	nonGauge := accountWindow
	nonGauge.Semantics = metering.SemanticsDelta
	if err := nonGauge.Validate(); err == nil {
		t.Fatal("account-window utilization must remain a nonadditive gauge")
	}
	directional := accountWindow
	directional.Measures = []metering.Measure{{
		Key:   metering.ComponentKey{Direction: metering.DirectionOutput, Component: "provider:account_utilization", Unit: metering.UnitPercent, SchemaID: "provider:account:v1"},
		Value: decimalPtr("12.5"), Quality: metering.QualityObserved,
	}}
	if err := directional.Validate(); err == nil {
		t.Fatal("account-window utilization must remain nondirectional")
	}
	missingReset := accountWindow
	missingReset.Subject.ResetAt = time.Time{}
	if err := missingReset.Validate(); err == nil {
		t.Fatal("account-window observation must retain reset time")
	}

	resource := validV2Observation(t)
	resource.ID = "resource-interval"
	resource.Subject = metering.SubjectRef{
		Kind: metering.SubjectResource, StoreID: "store-1", ResourceID: "cache-1", PeriodID: "period-1",
		StartAt: now, EndAt: now.Add(time.Hour),
	}
	resource.Correlation = metering.CorrelationV2{StoreID: "store-1", ResourceID: "cache-1", PeriodID: "period-1"}
	resource.Semantics = metering.SemanticsDelta
	resource.Measures = []metering.Measure{{
		Key:   metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentStorage, Unit: metering.UnitByteSecond},
		Value: decimalPtr("1.5"), Quality: metering.QualityObserved,
	}}
	if err := resource.Validate(); err != nil {
		t.Fatalf("resource cost should remain representable as a non-gauge: %v", err)
	}
}

func TestPhase2RepairCompatibilityFailsClosedForNonRepresentableSemantics(t *testing.T) {
	t.Parallel()

	fact := metering.Fact{
		FactID: "fact-reservation", StreamID: "stream-1", Sequence: 1, Kind: metering.FactKindReservationEstimate,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Correlation: metering.Correlation{RequestID: "request-1", ALegID: "a-1", BLegID: "b-1", AttemptID: "attempt-1"},
		Source:      metering.SourceProviderReported, Authority: metering.AuthorityAuthoritative, Presence: metering.PresenceUnknown,
		Quantities: []metering.Quantity{{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 1, Present: true}},
	}
	if _, err := metering.ObservationFromFact(fact); !errors.Is(err, metering.ErrUnrepresentableV1) {
		t.Fatalf("V1 reservation estimate must fail closed instead of becoming delta: %v", err)
	}

	unavailable := fact
	unavailable.FactID = "fact-unavailable"
	unavailable.Kind = metering.FactKindUnavailable
	unavailable.Quantities = nil
	if err := unavailable.Validate(); err != nil {
		t.Fatal(err)
	}
	observation, err := metering.ObservationFromFact(unavailable)
	if err != nil {
		t.Fatalf("explicit V1 unavailable semantics must remain representable: %v", err)
	}
	if observation.Semantics != metering.SemanticsDelta || observation.Authority != metering.AuthorityUnavailableClaim {
		t.Fatalf("unavailable semantics were not explicit: %+v", observation)
	}
	if _, err := observation.CanonicalJSON(); err != nil {
		t.Fatalf("legacy unavailable observation must remain canonicalizable: %v", err)
	}
	projectedUnavailable, err := metering.ProjectObservationToFact(observation)
	if err != nil {
		t.Fatalf("unavailable V2 observation must retain its V1 kind: %v", err)
	}
	if projectedUnavailable.Kind != metering.FactKindUnavailable || projectedUnavailable.Authority != metering.AuthorityUnavailable {
		t.Fatalf("unavailable semantics changed during projection: %+v", projectedUnavailable)
	}
	contradictoryUnavailable := observation
	contradictoryUnavailable.Measures = []metering.Measure{{
		Key:   metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken},
		Value: decimalPtr("1"), Quality: metering.QualityObserved,
	}}
	if _, err := metering.ProjectObservationToFact(contradictoryUnavailable); !errors.Is(err, metering.ErrUnrepresentableV1) {
		t.Fatalf("unavailable authority with usable evidence must fail closed: %v", err)
	}

	gauge := validV2Observation(t)
	gauge.Semantics = metering.SemanticsGauge
	if _, err := metering.ProjectObservationToFact(gauge); !errors.Is(err, metering.ErrUnrepresentableV1) {
		t.Fatalf("V2 gauge must not project as V1 delta: %v", err)
	}

	statement := validV2Observation(t)
	statement.Origin = metering.OriginStatement
	statement.Acquisition = metering.AcquisitionStatementImporter
	statement.Authority = metering.AuthorityVerifiedStatement
	statement.Subject = metering.SubjectRef{Kind: metering.SubjectStatementLine, StoreID: "store-1", ProviderAccountKey: "provider-account", StatementID: "statement-1", StatementLineID: "line-1"}
	statement.Correlation = metering.CorrelationV2{StoreID: "store-1", ProviderAccountKey: "provider-account"}
	if _, err := metering.ProjectObservationToFact(statement); !errors.Is(err, metering.ErrUnrepresentableV1) {
		t.Fatalf("statement provenance must not project as ordinary observed V1 source: %v", err)
	}
}
