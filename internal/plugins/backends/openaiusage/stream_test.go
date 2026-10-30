package openaiusage

import (
	"context"
	"testing"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	metering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestProviderEvidenceStreamBindsNonStreamingUsageToTrustedBLeg(t *testing.T) {
	t.Parallel()
	stream := NewProviderEvidenceStream([]aipapi.Event{{
		Kind: aipapi.EventUsageDelta, InputTokens: 2, OutputTokens: 3,
		UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
		RawUsageJSON:  `{"input_image_tokens":0,"output_audio_seconds":1.5}`,
		Accounting: aipapi.UsageAccountingMetadata{
			Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
			ProviderRequestID: "resp-1", DedupeKey: "openai.responses:resp-1",
		},
	}}, "openai.responses.v2")
	if source, ok := stream.(interface{ DrainEconomicObservations() []metering.Observation }); !ok {
		t.Fatal("non-streaming provider wrapper does not expose observation source")
	} else if got := source.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("unbound provider evidence escaped: %d", len(got))
	}
	binder, ok := stream.(coremetering.ProviderEvidenceBinder)
	if !ok {
		t.Fatal("non-streaming provider wrapper does not expose binder")
	}
	binder.BindEconomicEvidence(coremetering.ObservationIdentity{StoreID: "store", BillingCallID: "call", BLegID: "b-1"})
	source, ok := stream.(interface{ DrainEconomicObservations() []metering.Observation })
	if !ok {
		t.Fatal("non-streaming provider wrapper does not expose observation source")
	}
	observations := source.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("bound provider observations = %d, want 1", len(observations))
	}
	if observations[0].Subject.BLegID != "b-1" || observations[0].Subject.ProviderRequestID != "resp-1" {
		t.Fatalf("trusted/provider lineage was not separated: %+v", observations[0].Subject)
	}

	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatalf("event stream unexpectedly unavailable after evidence drain: %v", err)
	}
}

// TestProviderEventStreamDelegatesToEmbeddedBuffer is a termination regression:
// BindEconomicEvidence and DrainEconomicObservations must delegate to the
// embedded buffer, never recurse into themselves (a qualifying embedded
// selector must stay explicit where the outer method shadows it).
func TestProviderEventStreamDelegatesToEmbeddedBuffer(t *testing.T) {
	t.Parallel()
	stream := &providerEventStream{
		events:                 nil,
		ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer(),
	}
	stream.BindEconomicEvidence(coremetering.ObservationIdentity{StoreID: "store", BillingCallID: "call", BLegID: "b-1"})
	if got := stream.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("empty buffer drained %d observations, want 0", len(got))
	}
}
