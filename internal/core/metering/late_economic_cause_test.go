package metering

import (
	"context"
	"errors"
	"fmt"
	"testing"

	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// sentinelObservationResolver reports a typed resolution failure so a test can
// prove the resolver cause survives the lineage wrap.
type sentinelObservationResolver struct{ err error }

func (r sentinelObservationResolver) GetObservation(context.Context, string, uint64) (sdkmetering.Observation, error) {
	return sdkmetering.Observation{}, r.err
}

// TestLateEconomicSupersessionResolutionKeepsResolverCause proves a predecessor
// lookup failure is classified as a lineage mismatch while the resolver's own
// sentinel stays reachable: an operator needs the mismatch class and the
// storage/transport cause behind it, and the error text is not a classifier.
func TestLateEconomicSupersessionResolutionKeepsResolverCause(t *testing.T) {
	t.Parallel()

	prior := lateTestProviderObservation("cause-prior", 1)
	correction := lateTestProviderObservation("cause-correction", 2)
	correction.SourceEventKey = prior.SourceEventKey
	correction.Semantics = sdkmetering.SemanticsCorrection
	ref, err := prior.Ref("metering-1")
	if err != nil {
		t.Fatal(err)
	}
	correction.Supersedes = []sdkmetering.ObservationRef{ref}

	resolverCause := errors.New("predecessor store unavailable")
	reader := newLateTestLegReaderForObservation(t, correction, true)
	appender, err := NewLateEconomicAppender(LateEconomicAppenderConfig{
		StoreID: "metering-1", Legs: reader, Sink: &lateTestObservationSink{},
		Resolver: sentinelObservationResolver{err: fmt.Errorf("read predecessor: %w", resolverCause)},
	})
	if err != nil {
		t.Fatal(err)
	}

	appendErr := appender.AppendLateEconomicEvidence(context.Background(), lateEvidence(correction, LateEconomicCorrection))
	if !errors.Is(appendErr, ErrLateEconomicLineageMismatch) {
		t.Fatalf("append error = %v, want ErrLateEconomicLineageMismatch", appendErr)
	}
	if !errors.Is(appendErr, resolverCause) {
		t.Fatalf("append error = %v, want the resolver cause retained", appendErr)
	}
}
