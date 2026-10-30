package metering

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipsdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// TestPhase6BoundaryEveryLocalMeasureCarriesTheLocalMappingIdentity proves the
// neutral carriage of the local measurement plane: every measure the local
// boundary capture emits carries the local boundary mapping identity, so the
// local channel can never be read as a canonical billable claim.
//
// The proof covers the valued local facts and the honest unavailable markers
// alike, and it pins the evidence each measure carries: a present value stays
// present with its value, and an unobservable component stays unavailable with
// its method and reason. Nothing is stripped, fabricated, or zeroed.
func TestPhase6BoundaryEveryLocalMeasureCarriesTheLocalMappingIdentity(t *testing.T) {
	t.Parallel()
	acc := NewBoundaryAccumulator(BoundaryConfig{MaxTextBytes: 64, MaxMediaEntries: 8})
	acc.PrepareCall(aipapi.Call{Messages: []aipapi.Message{{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello"}},
	}}})
	acc.ObservePreparedInput(PreparedInputSummary{
		TextBytes:        6,
		TextBytesPresent: true,
		Media:            []MediaSummary{{Kind: MediaImage, Count: 2, Bytes: 400, BytesPresent: true}},
		MethodRef:        "adapter:test-transcode.v1",
	})
	acc.MarkAttempted()
	acc.MarkAccepted(true)
	acc.ObserveProviderEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "world"})
	acc.ObserveCustomerEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "world"})

	observations := acc.Snapshot().Observations(testBoundaryIdentity())
	if len(observations) != 3 {
		t.Fatalf("observation count = %d, want three local planes", len(observations))
	}
	var measures int
	var unavailable int
	for _, observation := range observations {
		if observation.Origin != aipsdkmetering.OriginLocal || observation.Acquisition != aipsdkmetering.AcquisitionLocalMeasurement {
			t.Fatalf("local observation provenance = %s/%s", observation.Origin, observation.Acquisition)
		}
		if observation.MappingRef != BoundaryMappingRef {
			t.Fatalf("observation mapping ref = %q, want %q", observation.MappingRef, BoundaryMappingRef)
		}
		for _, measure := range observation.Measures {
			measures++
			if measure.Key.SchemaID != BoundaryMappingRef {
				t.Fatalf("local measure %q carries schema %q, want the local mapping identity %q",
					measure.Key.CanonicalKey(), measure.Key.SchemaID, BoundaryMappingRef)
			}
			if measure.Quality == aipsdkmetering.QualityUnavailable {
				unavailable++
				if measure.Value != nil {
					t.Fatalf("unobservable measure %q carries a value", measure.Key.CanonicalKey())
				}
				if measure.MethodRef == "" || measure.Reason == "" {
					t.Fatalf("unobservable measure %q lost its method or reason: %#v", measure.Key.CanonicalKey(), measure)
				}
				continue
			}
			if measure.Value == nil {
				t.Fatalf("valued local measure %q lost its value", measure.Key.CanonicalKey())
			}
			if measure.MethodRef == "" {
				t.Fatalf("valued local measure %q lost its method", measure.Key.CanonicalKey())
			}
		}
	}
	if measures == 0 || unavailable == 0 {
		t.Fatalf("the local capture carried %d measures with %d unavailable markers", measures, unavailable)
	}
	// The valued local facts are still present: the neutral carriage did not
	// strip the payload, the text or the request lifecycle measures.
	var sawTextBytes, sawRequest bool
	for _, observation := range observations {
		for _, measure := range observation.Measures {
			switch measure.Key.Component {
			case "text_bytes":
				if measure.Value != nil {
					sawTextBytes = true
				}
			case aipsdkmetering.ComponentRequest:
				sawRequest = true
			}
		}
	}
	if !sawTextBytes {
		t.Fatal("the local capture lost its valued text-bytes measure")
	}
	if !sawRequest {
		t.Fatal("the local capture lost its request lifecycle measure")
	}
}
