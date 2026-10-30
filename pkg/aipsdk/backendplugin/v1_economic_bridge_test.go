package backendplugin_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestAccountingEvidenceV2FromV1_IsExplicitlyPartialAndLosslessForTokens(t *testing.T) {
	t.Parallel()
	in, out, total := int64(2), int64(3), int64(5)
	got, err := backendplugin.LiftAccountingEvidenceV1(backendplugin.AccountingEvidence{
		InputTokens: &in, OutputTokens: &out, TotalTokens: &total,
		Presence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
		Source:   backendplugin.AccountingSourceProviderReported, Authority: backendplugin.AccountingAuthorityAuthoritative,
		Plane: backendplugin.AccountingPlaneProviderBillable, DedupeKey: "provider-charge-1",
	}, backendplugin.V1EvidenceIdentity{
		StoreID: "metering", ObservationID: "observation-1", SourceEventKey: "provider-charge-1", Revision: 1,
		StreamID: "stream-1", Subject: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "metering", BLegID: "b-1"},
		Correlation: metering.CorrelationV2{StoreID: "metering", BLegID: "b-1"}, ObservedAt: time.Unix(100, 0).UTC(), ReceivedAt: time.Unix(100, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("V1 lift: %v", err)
	}
	if got.Coverage != backendplugin.EvidenceCoveragePartial {
		t.Fatalf("coverage=%q want partial", got.Coverage)
	}
	if len(got.Observation.Charges) != 0 || len(got.Observation.Measures) != 3 {
		t.Fatalf("V1 bridge invented unsupported detail: %+v", got.Observation)
	}
	for _, measure := range got.Observation.Measures {
		if measure.Value == nil || measure.Value.Scale != 0 || measure.Quality != metering.QualityObserved {
			t.Fatalf("counter was not losslessly represented: %+v", measure)
		}
	}
}

func TestAccountingEvidenceV2FromV1_RequiresTrustedBLeg(t *testing.T) {
	t.Parallel()
	in := int64(1)
	_, err := backendplugin.LiftAccountingEvidenceV1(backendplugin.AccountingEvidence{
		InputTokens: &in, Presence: aipapi.UsagePresence{InputTokens: true},
		Source: backendplugin.AccountingSourceProviderReported, Authority: backendplugin.AccountingAuthorityAuthoritative,
		Plane: backendplugin.AccountingPlaneProviderBillable, DedupeKey: "charge-1",
	}, backendplugin.V1EvidenceIdentity{StoreID: "metering", ObservationID: "observation-1"})
	if !errors.Is(err, backendplugin.ErrAccountingEvidenceV2Unsupported) {
		t.Fatalf("error=%v want explicit unsupported B-leg bridge", err)
	}
}
