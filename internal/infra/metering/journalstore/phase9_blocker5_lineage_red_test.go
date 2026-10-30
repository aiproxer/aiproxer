package journalstore_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase9Blocker5_DurableReplayNormalizesSubjectCorrelationPlacement(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		first       blocker5DurableLineageCarrier
		second      blocker5DurableLineageCarrier
		firstLabel  string
		secondLabel string
	}{
		{name: "subject-to-correlation", first: blocker5DurableSubjectCarried, second: blocker5DurableCorrelationCarried, firstLabel: "subject", secondLabel: "correlation"},
		{name: "correlation-to-subject", first: blocker5DurableCorrelationCarried, second: blocker5DurableSubjectCarried, firstLabel: "correlation", secondLabel: "subject"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newSQLiteJournal(t)
			ctx := context.Background()
			if err := store.AppendObservation(ctx, blocker5DurableLineageObservation(test.first)); err != nil {
				t.Fatalf("append %s-carried observation: %v", test.firstLabel, err)
			}
			if err := store.AppendObservation(ctx, blocker5DurableLineageObservation(test.second)); err != nil {
				t.Fatalf("equivalent %s-carried replay was not idempotent: %v", test.secondLabel, err)
			}
		})
	}
}

type blocker5DurableLineageCarrier uint8

const (
	blocker5DurableSubjectCarried blocker5DurableLineageCarrier = iota
	blocker5DurableCorrelationCarried
)

func blocker5DurableLineageObservation(carrier blocker5DurableLineageCarrier) metering.Observation {
	observation := repair8DurableObservation("blocker5-durable-lineage", "blocker5-durable-lineage-stream", 1, metering.SemanticsDelta)
	observation.Subject.TenantID = "blocker5-durable-tenant"
	observation.Subject.ALegID = "blocker5-durable-a-leg"
	observation.Subject.BillingCallID = "blocker5-durable-billing-call"
	observation.Subject.CallID = "blocker5-durable-call"
	observation.Subject.ProviderAccountKey = "repair8-durable-account"
	observation.Subject.ProviderRequestID = "blocker5-durable-request"
	observation.Subject.ProviderChargeID = "repair8-durable-charge"
	observation.Correlation.TenantID = observation.Subject.TenantID
	observation.Correlation.ALegID = observation.Subject.ALegID
	observation.Correlation.BillingCallID = observation.Subject.BillingCallID
	observation.Correlation.CallID = observation.Subject.CallID
	observation.Correlation.ProviderRequestID = observation.Subject.ProviderRequestID
	if carrier == blocker5DurableSubjectCarried {
		observation.Correlation.TenantID = ""
		observation.Correlation.ALegID = ""
		observation.Correlation.BillingCallID = ""
		observation.Correlation.CallID = ""
		observation.Correlation.ProviderAccountKey = ""
		observation.Correlation.ProviderRequestID = ""
		observation.Correlation.ProviderChargeID = ""
	} else {
		observation.Subject.TenantID = ""
		observation.Subject.ALegID = ""
		observation.Subject.BillingCallID = ""
		observation.Subject.CallID = ""
		observation.Subject.ProviderAccountKey = ""
		observation.Subject.ProviderRequestID = ""
		observation.Subject.ProviderChargeID = ""
	}
	return observation
}
