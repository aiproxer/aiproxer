package adapter_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

type phase7EconomicFinalizerSession struct {
	minimalSession
}

func (s *phase7EconomicFinalizerSession) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{Capabilities: backendplugin.CapabilitySummary{Streaming: true}, SupportsFinalizeBilling: true, SupportsAccountingEvidenceV2: true}, nil
}

func (*phase7EconomicFinalizerSession) FinalizeBilling(context.Context, backendplugin.FinalizeBillingRequest) (backendplugin.FinalizeBillingResponse, error) {
	value := metering.Decimal{Coefficient: "4", Scale: 0}
	now := time.Unix(1700000000, 0).UTC()
	return backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{Presence: backendplugin.UsagePresence{InputTokens: true}, InputTokens: new(int64(1))},
		AccountingV2: []backendplugin.AccountingEvidenceV2{{
			Coverage: backendplugin.EvidenceCoverageComplete,
			Observation: metering.Observation{
				Version: metering.ObservationVersionV2, ID: "obs-finalizer", SourceEventKey: "source-finalizer", Revision: 1,
				StreamID: "stream-finalizer", Sequence: 1, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderFinalizer,
				Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
				Lifecycle:   metering.LifecycleBackendAttempt,
				Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-finalizer", BLegID: "b-finalizer"},
				Correlation: metering.CorrelationV2{StoreID: "store-finalizer", BLegID: "b-finalizer", ProviderRequestID: "provider-finalizer"},
				Semantics:   metering.SemanticsCumulative, ObservedAt: now, ReceivedAt: now, MappingRef: "provider.finalizer",
				Measures: []metering.Measure{{Key: metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "provider.media.v2"}, Value: &value, Quality: metering.QualityObserved}},
			},
		}},
	}, nil
}

func TestFinalizeBillingV2PreservesHostOnlyEvidence(t *testing.T) {
	t.Parallel()
	session := &phase7EconomicFinalizerSession{}
	profile, err := session.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	build := adapter.Build(session, profile, adapter.Options{
		InstanceID:  "phase7-finalizer",
		Negotiation: backendplugin.Negotiation{Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorAccountingEvidenceV2, EnabledFeatures: []string{backendplugin.FeatureAccountingEvidenceV2}},
	})
	if build.Backend.FinalizeBillingV2 == nil {
		t.Fatal("V2 finalizer bridge is not installed")
	}
	result, err := build.Backend.FinalizeBillingV2(context.Background(), execbackend.BillingFinalizationInput{TraceID: "trace", ALegID: "a", BLegID: "b", Backend: "backend", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.Kind != aipapi.EventUsageDelta || len(result.EconomicEvidence) != 1 {
		t.Fatalf("result=%+v, want usage event and one V2 evidence", result)
	}
	got := result.EconomicEvidence[0]
	if got.Coverage != string(backendplugin.EvidenceCoverageComplete) || got.Observation.Correlation.ProviderRequestID != "provider-finalizer" {
		t.Fatalf("finalizer evidence=%+v, lost coverage/provider identity", got)
	}
}
