package metering

import (
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// F6: a draft can pass every Add-time check (non-zero-free ordering identity,
// valid source key, under the pending metadata byte cap) yet still violate the
// SDK observation contract at drain time. The canonical trigger is a
// provider-owned identifier one byte past its bound: ProviderRequestID of
// MaxSchemaIDBytes+1. The pre-fix drain silently dropped such a draft, so an
// accepted valid prefix reduced as COMPLETE and could be sealed and rated. The
// drain must instead keep the valid prefix, surface one bounded provider-neutral
// loss marker, persist no invalid identifier and no fabricated amount, and
// reduce incomplete.

// f6InvalidProviderRequestID returns a provider request identifier exactly one
// byte beyond the SDK identity bound.
func f6InvalidProviderRequestID() string {
	return strings.Repeat("r", sdkmetering.MaxSchemaIDBytes+1)
}

func f6InvalidMediaMoneyDraft(key string, count uint64) ProviderEvidenceDraft {
	draft := nativeMediaMoneyDraft(key, count)
	draft.ProviderRequestID = f6InvalidProviderRequestID()
	return draft
}

func f6ObservationByKey(observations []sdkmetering.Observation, key string) (sdkmetering.Observation, bool) {
	for _, observation := range observations {
		if observation.SourceEventKey == key {
			return observation, true
		}
	}
	return sdkmetering.Observation{}, false
}

func f6ChargePresent(observations []sdkmetering.Observation, itemID string) bool {
	for _, observation := range observations {
		for _, charge := range observation.Charges {
			if charge.ChargeItemID == itemID {
				return true
			}
		}
	}
	return false
}

// F6 terminal drain: a valid prefix followed by an invalid draft in one drain
// must retain the prefix, reject the invalid draft visibly, and stay incomplete.
func TestProviderEvidenceF6InvalidDraftMarkersIncomplete(t *testing.T) {
	t.Parallel()
	const (
		prefixKey  = "provider.f6.prefix"
		invalidKey = "provider.f6.invalid"
	)
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	b.Add(nativeMediaMoneyDraft(prefixKey, 5))
	b.Add(f6InvalidMediaMoneyDraft(invalidKey, 7))

	observations := b.DrainEconomicObservations()

	prefix, ok := f6ObservationByKey(observations, prefixKey)
	if !ok {
		t.Fatalf("valid prefix evidence was dropped: %+v", observations)
	}
	if got, want := r5MeasureValue(prefix, imageKey()), canonicalCoefficient("5"); got != want {
		t.Fatalf("retained prefix media=%q, want %q", got, want)
	}
	if _, leaked := f6ObservationByKey(observations, invalidKey); leaked {
		t.Fatalf("invalid provider identifier leaked into accepted evidence: %+v", observations)
	}
	if f6ChargePresent(observations, "charge:"+invalidKey) {
		t.Fatalf("invalid draft's charge became payable evidence: %+v", observations)
	}
	marker, ok := r5UnavailableMarker(observations)
	if !ok || r5UnavailableCause(marker) != providerEvidenceLossInvalidDraft {
		t.Fatalf("invalid draft must surface a %q loss marker: %+v", providerEvidenceLossInvalidDraft, observations)
	}
	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		t.Fatalf("apply mixed evidence: %v", err)
	}
	if snapshot.Complete {
		t.Fatal("accepted prefix plus rejected invalid draft reduced complete")
	}
}

// F6 live/cross-drain: the valid prefix is drained first (live sideband), then
// repeated invalid drafts arrive across later drains. Each drain must surface
// exactly one bounded loss marker, retain no invalid amount, and never grow the
// retained anchor state beyond the single valid source.
func TestProviderEvidenceF6InvalidDraftLossAcrossDrainsIsBounded(t *testing.T) {
	t.Parallel()
	const prefixKey = "provider.f6.live.prefix"
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())

	b.Add(nativeMediaMoneyDraft(prefixKey, 4))
	first := b.DrainEconomicObservations()
	if len(first) != 1 {
		t.Fatalf("live prefix drain=%+v, want the single valid prefix", first)
	}
	if _, ok := f6ObservationByKey(first, prefixKey); !ok {
		t.Fatalf("live prefix drain lost the valid prefix: %+v", first)
	}

	for round := range 5 {
		invalidKey := "provider.f6.live.invalid." + strconv.Itoa(round)
		b.Add(f6InvalidMediaMoneyDraft(invalidKey, uint64(round+1)))
		drained := b.DrainEconomicObservations()
		if _, leaked := f6ObservationByKey(drained, invalidKey); leaked {
			t.Fatalf("round %d: invalid draft leaked into evidence: %+v", round, drained)
		}
		if f6ChargePresent(drained, "charge:"+invalidKey) {
			t.Fatalf("round %d: invalid charge leaked: %+v", round, drained)
		}
		marker, ok := r5UnavailableMarker(drained)
		if !ok || r5UnavailableCause(marker) != providerEvidenceLossInvalidDraft {
			t.Fatalf("round %d: missing bounded loss marker: %+v", round, drained)
		}
		if len(drained) != 1 {
			t.Fatalf("round %d: drained=%d, want exactly one loss marker: %+v", round, len(drained), drained)
		}
	}

	b.mu.Lock()
	tracked, pending, anchorBytes := len(b.lastObservation), b.pending, b.anchorBytes
	b.mu.Unlock()
	if tracked != 1 || pending != 0 {
		t.Fatalf("invalid drafts grew retained state: anchors=%d pending=%d", tracked, pending)
	}
	if anchorBytes > maxProviderEvidenceAnchorBytes {
		t.Fatalf("anchor bytes=%d exceed bound %d", anchorBytes, maxProviderEvidenceAnchorBytes)
	}
}

// F6 batching: several invalid drafts admitted before one drain collapse to one
// bounded marker, and a valid identifier exactly at the SDK bound is not
// over-rejected.
func TestProviderEvidenceF6InvalidDraftOneMarkerPerDrain(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	for i := range 4 {
		b.Add(f6InvalidMediaMoneyDraft("provider.f6.batch."+strconv.Itoa(i), uint64(i+1)))
	}
	observations := b.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("batched invalid drafts drained=%d, want exactly one bounded marker: %+v", len(observations), observations)
	}
	marker, ok := r5UnavailableMarker(observations)
	if !ok || r5UnavailableCause(marker) != providerEvidenceLossInvalidDraft {
		t.Fatalf("batched invalid drafts must surface one %q marker: %+v", providerEvidenceLossInvalidDraft, observations)
	}

	boundary := NewProviderEvidenceBuffer()
	boundary.BindEconomicEvidence(testObservationIdentity())
	atBound := nativeMediaMoneyDraft("provider.f6.bound", 2)
	atBound.ProviderRequestID = strings.Repeat("r", sdkmetering.MaxSchemaIDBytes)
	boundary.Add(atBound)
	atBoundObservations := boundary.DrainEconomicObservations()
	if len(atBoundObservations) != 1 {
		t.Fatalf("identifier at the SDK bound must be accepted: %+v", atBoundObservations)
	}
	if _, ok := r5UnavailableMarker(atBoundObservations); ok {
		t.Fatal("identifier at the SDK bound must not be treated as loss")
	}
}
