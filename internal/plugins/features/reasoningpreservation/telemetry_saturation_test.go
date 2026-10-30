package reasoningpreservation

import (
	"math"
	"testing"
	"time"
)

func TestTelemetry_RawBytes_Saturation_WrapRegression(t *testing.T) {
	t.Parallel()

	tel := NewTelemetry()
	// Prime rawBytes near MaxInt64 via internal bucket registry.
	b := tel.getBucket(OutcomeRestored)
	b.rawBytes.Store(math.MaxInt64 - 5)
	// Public Record path with positive bytes that should saturate, not wrap.
	tel.Record(OutcomeRestored, map[string]int{"bytes": 100})
	snap := tel.RawBytesSnapshot()
	got, ok := snap[OutcomeRestored]
	if !ok {
		raw := b.rawBytes.Load()
		t.Fatalf("wrap bug: snapshot missing for %s (raw atomic=%d) want positive saturated value near MaxInt64", OutcomeRestored, raw)
	}
	if got < 0 {
		t.Fatalf("wrap bug: RawBytesSnapshot[%s]=%d negative after near-max accumulation; want saturated MaxInt64", OutcomeRestored, got)
	}
	if got != math.MaxInt64 {
		t.Fatalf("want saturated MaxInt64, got %d", got)
	}
	if c := tel.Snapshot()[OutcomeRestored]; c <= 0 {
		t.Fatalf("count snapshot must stay positive, got %d", c)
	}
}

func TestTelemetry_Saturation_CountAndBytes(t *testing.T) {
	t.Parallel()

	t.Run("count saturation", func(t *testing.T) {
		t.Parallel()

		tel := NewTelemetry()
		b := tel.getBucket(OutcomePreserved)
		b.count.Store(math.MaxInt64 - 1)
		tel.Record(OutcomePreserved, nil)
		tel.Record(OutcomePreserved, nil)
		if got := tel.Snapshot()[OutcomePreserved]; got != math.MaxInt64 {
			t.Fatalf("count saturate: got %d want %d", got, int64(math.MaxInt64))
		}
	})
	t.Run("rawBytes saturation via RecordCompression", func(t *testing.T) {
		t.Parallel()

		tel := NewTelemetry()
		b := tel.getBucket(OutcomeRestored)
		b.rawBytes.Store(math.MaxInt64 - 10)
		tel.RecordCompression(OutcomeRestored, 100)
		if got := tel.RawBytesSnapshot()[OutcomeRestored]; got != math.MaxInt64 {
			t.Fatalf("raw saturate: got %d", got)
		}
	})
	t.Run("decoded saturation", func(t *testing.T) {
		t.Parallel()

		tel := NewTelemetry()
		b := tel.getBucket(OutcomeRestored)
		b.decodedBytes.Store(math.MaxInt64 - 5)
		tel.RecordCompressionMeasurement(OutcomeRestored, 0, 100, 0)
		if got := tel.DecodedBytesSnapshot()[OutcomeRestored]; got != math.MaxInt64 {
			t.Fatalf("decoded saturate: got %d", got)
		}
	})
	t.Run("saved saturation", func(t *testing.T) {
		t.Parallel()

		tel := NewTelemetry()
		b := tel.getBucket(OutcomeRestored)
		b.savedBytes.Store(math.MaxInt64 - 5)
		tel.RecordCompressionMeasurement(OutcomeRestored, 0, 0, 100)
		if got := tel.SavedBytesSnapshot()[OutcomeRestored]; got != math.MaxInt64 {
			t.Fatalf("saved saturate: got %d", got)
		}
	})
	t.Run("source saturation via shadow", func(t *testing.T) {
		t.Parallel()

		tel := NewTelemetry()
		b := tel.getBucket(OutcomeRestored)
		b.sourceBytes.Store(math.MaxInt64 - 5)
		tel.RecordShadowMeasurement(OutcomeRestored, 100, 0, 0, 0, 0)
		if got := tel.SourceBytesSnapshot()[OutcomeRestored]; got != math.MaxInt64 {
			t.Fatalf("source saturate: got %d", got)
		}
	})
	t.Run("latency saturation", func(t *testing.T) {
		t.Parallel()

		tel := NewTelemetry()
		b := tel.getBucket(OutcomeRestored)
		b.latencyNanos.Store(math.MaxInt64 - int64(time.Second))
		tel.RecordShadowMeasurement(OutcomeRestored, 0, 0, 0, 0, 2*time.Second)
		if got := tel.LatencySnapshot()[OutcomeRestored]; got != time.Duration(math.MaxInt64) {
			t.Fatalf("latency saturate: got %d", got)
		}
	})
}
