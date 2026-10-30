package metering_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase9Blocker5_EquivalentSubjectCorrelationPlacementSharesIdentity(t *testing.T) {
	t.Parallel()

	subjectCarried := blocker5LineageObservation("blocker5-identity", 1, metering.SemanticsDelta, blocker5SubjectCarried)
	correlationCarried := blocker5LineageObservation("blocker5-identity", 1, metering.SemanticsDelta, blocker5CorrelationCarried)
	if err := subjectCarried.Validate(); err != nil {
		t.Fatalf("subject-carried observation: %v", err)
	}
	if err := correlationCarried.Validate(); err != nil {
		t.Fatalf("correlation-carried observation: %v", err)
	}

	if got, want := correlationCarried.SubjectIdentity(), subjectCarried.SubjectIdentity(); got != want {
		t.Fatalf("subject identity differs by carrier placement: got %q want %q", got, want)
	}
	if got, want := correlationCarried.NormalizedLineageIdentity(), subjectCarried.NormalizedLineageIdentity(); got != want {
		t.Fatalf("normalized lineage differs by carrier placement: got %q want %q", got, want)
	}
	if got, want := correlationCarried.IdentityKey(), subjectCarried.IdentityKey(); got != want {
		t.Fatalf("source identity differs by carrier placement: got %q want %q", got, want)
	}
	subjectHash, err := subjectCarried.ReplayFingerprint()
	if err != nil {
		t.Fatalf("subject-carried replay fingerprint: %v", err)
	}
	correlationHash, err := correlationCarried.ReplayFingerprint()
	if err != nil {
		t.Fatalf("correlation-carried replay fingerprint: %v", err)
	}
	if correlationHash != subjectHash {
		t.Fatalf("replay fingerprint differs by carrier placement: got %q want %q", correlationHash, subjectHash)
	}
}

func TestPhase9Blocker5_SupersessionAcceptsEitherLineageCarrier(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		parentCarrier blocker5LineageCarrier
		childCarrier  blocker5LineageCarrier
	}{
		{name: "subject-to-correlation", parentCarrier: blocker5SubjectCarried, childCarrier: blocker5CorrelationCarried},
		{name: "correlation-to-subject", parentCarrier: blocker5CorrelationCarried, childCarrier: blocker5SubjectCarried},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := blocker5LineageObservation("blocker5-parent-"+test.name, 1, metering.SemanticsCumulative, test.parentCarrier)
			parentRef, err := parent.Ref(parent.Subject.StoreID)
			if err != nil {
				t.Fatalf("parent ref: %v", err)
			}
			child := blocker5LineageObservation("blocker5-child-"+test.name, 2, metering.SemanticsCorrection, test.childCarrier)
			child.Supersedes = []metering.ObservationRef{parentRef}
			if err := metering.ValidateSupersessionGraph([]metering.Observation{child, parent}); err != nil {
				t.Fatalf("same effective lineage rejected in shuffled order: %v", err)
			}
		})
	}
}

func TestPhase9Blocker5_SupersessionRejectsTrueScopeMismatches(t *testing.T) {
	t.Parallel()

	parent := blocker5LineageObservation("blocker5-mismatch-parent", 1, metering.SemanticsCumulative, blocker5SubjectCarried)
	parentRef, err := parent.Ref(parent.Subject.StoreID)
	if err != nil {
		t.Fatalf("parent ref: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*metering.Observation)
	}{
		{name: "provider-account", mutate: func(observation *metering.Observation) {
			observation.Correlation.ProviderAccountKey = "blocker5-other-account"
		}},
		{name: "call", mutate: func(observation *metering.Observation) { observation.Correlation.CallID = "blocker5-other-call" }},
		{name: "b-leg", mutate: func(observation *metering.Observation) {
			observation.Subject.BLegID = "blocker5-other-b-leg"
			observation.Correlation.BLegID = "blocker5-other-b-leg"
		}},
		{name: "stream", mutate: func(observation *metering.Observation) { observation.StreamID = "blocker5-other-stream" }},
		{name: "acquisition", mutate: func(observation *metering.Observation) { observation.Acquisition = metering.AcquisitionProviderHeader }},
		{name: "tenant", mutate: func(observation *metering.Observation) { observation.Correlation.TenantID = "blocker5-other-tenant" }},
		{name: "store", mutate: func(observation *metering.Observation) {
			observation.Subject.StoreID = "blocker5-other-store"
			observation.Correlation.StoreID = "blocker5-other-store"
		}},
		{name: "provider-request", mutate: func(observation *metering.Observation) {
			observation.Correlation.ProviderRequestID = "blocker5-other-request"
		}},
		{name: "provider-charge", mutate: func(observation *metering.Observation) {
			observation.Correlation.ProviderChargeID = "blocker5-other-charge"
		}},
		{name: "parent-work", mutate: func(observation *metering.Observation) {
			observation.Correlation.ParentWorkID = "blocker5-other-parent"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			child := blocker5LineageObservation("blocker5-mismatch-"+test.name, 2, metering.SemanticsCorrection, blocker5CorrelationCarried)
			child.Supersedes = []metering.ObservationRef{parentRef}
			test.mutate(&child)
			if err := metering.ValidateSupersessionGraph([]metering.Observation{parent, child}); err == nil {
				t.Fatalf("scope mismatch was accepted")
			}
		})
	}

	// Keep the sentinel assertion explicit for the principal provider-account
	// mismatch used by callers to classify a rejected revision link.
	foreign := blocker5LineageObservation("blocker5-mismatch-sentinel", 2, metering.SemanticsCorrection, blocker5CorrelationCarried)
	foreign.Correlation.ProviderAccountKey = "blocker5-other-account"
	foreign.Supersedes = []metering.ObservationRef{parentRef}
	if err := metering.ValidateSupersessionGraph([]metering.Observation{parent, foreign}); !errors.Is(err, metering.ErrInvalidRevision) {
		t.Fatalf("provider-account mismatch error=%v, want ErrInvalidRevision", err)
	}
}

type blocker5LineageCarrier uint8

const (
	blocker5SubjectCarried blocker5LineageCarrier = iota
	blocker5CorrelationCarried
)

func blocker5LineageObservation(id string, sequence uint64, semantics string, carrier blocker5LineageCarrier) metering.Observation {
	value, err := metering.ParseDecimal("5")
	if err != nil {
		panic(err)
	}
	const storeID = "blocker5-store"
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID, TenantID: "blocker5-tenant", AccountID: "blocker5-account",
		ALegID: "blocker5-a-leg", RequestID: "blocker5-request", BillingCallID: "blocker5-billing-call", CallID: "blocker5-call",
		BLegID: "blocker5-b-leg", AttemptID: "blocker5-attempt", AttemptSeq: 7, SubmissionID: "blocker5-submission",
		ProviderAccountKey: "blocker5-provider-account", ProviderRequestID: "blocker5-provider-request", ProviderChargeID: "blocker5-provider-charge",
	}
	correlation := metering.CorrelationV2{
		StoreID: storeID, TenantID: "blocker5-tenant", RequestID: "blocker5-request", CallID: "blocker5-call", BillingCallID: "blocker5-billing-call",
		ALegID: "blocker5-a-leg", BLegID: subject.BLegID, AttemptID: subject.AttemptID, AttemptSeq: subject.AttemptSeq, SubmissionID: "blocker5-submission",
		ProviderAccountKey: "blocker5-provider-account", ProviderRequestID: "blocker5-provider-request", ProviderChargeID: "blocker5-provider-charge",
		ParentWorkID: "blocker5-parent-work",
	}
	if carrier == blocker5SubjectCarried {
		correlation.TenantID = ""
		correlation.RequestID = ""
		correlation.CallID = ""
		correlation.BillingCallID = ""
		correlation.ALegID = ""
		correlation.AttemptID = ""
		correlation.AttemptSeq = 0
		correlation.SubmissionID = ""
		correlation.ProviderAccountKey = ""
		correlation.ProviderRequestID = ""
		correlation.ProviderChargeID = ""
	} else {
		subject.TenantID = ""
		subject.RequestID = ""
		subject.CallID = ""
		subject.BillingCallID = ""
		subject.ALegID = ""
		subject.AttemptID = ""
		subject.AttemptSeq = 0
		subject.SubmissionID = ""
		subject.ProviderAccountKey = ""
		subject.ProviderRequestID = ""
		subject.ProviderChargeID = ""
	}
	now := time.Unix(20_000+int64(sequence), 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: "blocker5-source-event", Revision: 1,
		StreamID: "blocker5-stream", Sequence: sequence, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject, Correlation: correlation,
		Semantics: semantics, ObservedAt: now, ReceivedAt: now, MappingRef: "blocker5.lineage.v1",
		Measures: []metering.Measure{{Key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "blocker5.lineage.v1"}, Value: &value, Quality: metering.QualityObserved}},
	}
}
