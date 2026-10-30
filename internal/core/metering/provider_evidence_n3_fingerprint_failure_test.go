package metering

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// N3: Add derives the replay fingerprint before any comparison. When the
// provider payload cannot be canonically encoded, draftFingerprint fails and
// silently returns "". A brand-new source then compares its empty hash against
// the absent lastFingerprint entry ("") and is misclassified as an exact replay,
// so it is dropped with no draft and no loss marker. Drain never reaches the
// invalid_draft branch, and an accepted healthy prefix reduces as COMPLETE.
//
// The canonical trigger is a nested Measure.Key with an invalid
// direction/component combination (ComponentInputToken + DirectionOutput),
// which ComponentKey.MarshalJSON rejects through its canonicalization. The
// encoding failure must be recorded as a bounded sanitized invalid_draft loss
// BEFORE any replay comparison, without mutating the accepted anchor.

// n3MalformedMeasure is a valid-looking quantity bound to an impossible
// direction/component pair. ComponentKey canonicalization rejects it.
func n3MalformedMeasure() sdkmetering.Measure {
	return sdkmetering.Measure{
		Key: sdkmetering.ComponentKey{
			Direction: sdkmetering.DirectionOutput,
			Component: sdkmetering.ComponentInputToken,
			Unit:      sdkmetering.UnitToken,
			SchemaID:  sdkmetering.DefaultInclusionSchemaID,
		},
		Value:   decimalPtr("1"),
		Quality: sdkmetering.QualityObserved,
	}
}

// n3MalformedDraft keeps a genuine provider charge and other valid fields but
// swaps in the incompressible measure key. It is far below the 64KB draft cap
// and supplies no ordering identity, so only the fingerprint encoding fails.
func n3MalformedDraft(key string) ProviderEvidenceDraft {
	draft := nativeMediaMoneyDraft(key, 2)
	draft.Measures = []sdkmetering.Measure{n3MalformedMeasure()}
	return draft
}

func n3AssertFixtureRejected(t *testing.T, draft ProviderEvidenceDraft) {
	t.Helper()
	if _, err := json.Marshal(draft.Measures[0].Key); err == nil {
		t.Fatalf("fixture is not rejected by canonical ComponentKey JSON encoding: %+v", draft.Measures[0].Key)
	}
}

func n3AssertNoMalformedMeasure(t *testing.T, observations []sdkmetering.Observation) {
	t.Helper()
	for _, observation := range observations {
		for _, measure := range observation.Measures {
			if measure.Key.Component == sdkmetering.ComponentInputToken && measure.Key.Direction == sdkmetering.DirectionOutput {
				t.Fatalf("malformed measure leaked into drained evidence: %+v", measure)
			}
		}
	}
}

// N3 counterexample: a healthy drained prefix followed by a NEW source whose
// fingerprint cannot be encoded must still surface one bounded invalid_draft
// marker, and the prefix plus marker must not reduce complete.
func TestN3FingerprintEncodingFailureRecordsInvalidDraftLoss(t *testing.T) {
	t.Parallel()
	const (
		prefixKey = "provider.n3.prefix"
		badKey    = "provider.n3.malformed"
	)
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())

	// Step 1: establish a healthy captured prefix and drain it.
	b.Add(nativeMediaMoneyDraft(prefixKey, 1))
	prefix := b.DrainEconomicObservations()
	if len(prefix) != 1 {
		t.Fatalf("healthy prefix drain=%d, want 1", len(prefix))
	}
	if _, ok := r5UnavailableMarker(prefix); ok {
		t.Fatalf("healthy prefix must not carry a loss marker: %+v", prefix)
	}

	// Step 2: a new source with an unencodable nested measure key.
	bad := n3MalformedDraft(badKey)
	n3AssertFixtureRejected(t, bad)
	b.Add(bad)

	// The bounded sanitized marker must be recorded before any replay
	// comparison; it carries the invalid_draft cause and partial coverage.
	b.mu.Lock()
	lossDrafts := append([]ProviderEvidenceDraft(nil), b.lossDrafts...)
	pending := b.pending
	b.mu.Unlock()
	if pending != 0 {
		t.Fatalf("fingerprint-failed draft was retained as pending: %d", pending)
	}
	if len(lossDrafts) != 1 {
		t.Fatalf("loss markers recorded=%d, want exactly one: %+v", len(lossDrafts), lossDrafts)
	}
	if lossDrafts[0].Coverage != "partial" || lossDrafts[0].CoverageReason != providerEvidenceLossInvalidDraft {
		t.Fatalf("loss marker coverage=%q reason=%q, want partial/%q",
			lossDrafts[0].Coverage, lossDrafts[0].CoverageReason, providerEvidenceLossInvalidDraft)
	}

	// Step 3: drain surfaces exactly the provider-neutral unavailable marker.
	drained := b.DrainEconomicObservations()
	if len(drained) != 1 {
		t.Fatalf("malformed new-source draft must surface exactly one loss marker, got %d: %+v", len(drained), drained)
	}
	marker, ok := r5UnavailableMarker(drained)
	if !ok {
		t.Fatalf("missing provider-neutral unavailable loss marker: %+v", drained)
	}
	if got := r5UnavailableCause(marker); got != providerEvidenceLossInvalidDraft {
		t.Fatalf("loss reason=%q, want %q", got, providerEvidenceLossInvalidDraft)
	}
	if marker.StreamID != providerEvidenceLossStreamID {
		t.Fatalf("loss marker stream=%q, want %q", marker.StreamID, providerEvidenceLossStreamID)
	}
	if _, leaked := f6ObservationByKey(drained, badKey); leaked {
		t.Fatalf("malformed draft leaked into accepted evidence: %+v", drained)
	}
	n3AssertNoMalformedMeasure(t, drained)

	// The reducer over the retained prefix plus the loss marker stays incomplete.
	combined := append(append([]sdkmetering.Observation(nil), prefix...), drained...)
	snapshot, err := aggregate.ApplyObservations(combined)
	if err != nil {
		t.Fatalf("apply prefix+marker: %v", err)
	}
	if snapshot.Complete {
		t.Fatal("prefix plus fingerprint-failure loss marker reduced complete")
	}
}

// A fingerprint failure on a PREVIOUSLY SEEN source must not mutate the accepted
// anchor, retain a pending baseline, or fabricate a supersession.
func TestN3FingerprintFailureLeavesAcceptedAnchorUnchanged(t *testing.T) {
	t.Parallel()
	const key = "provider.n3.anchor"
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	b.Add(nativeMediaMoneyDraft(key, 3))
	if got := b.DrainEconomicObservations(); len(got) != 1 {
		t.Fatalf("baseline drain=%d, want 1", len(got))
	}

	b.mu.Lock()
	fpBefore := b.lastFingerprint[key]
	revBefore := b.lastRevision[key]
	obsBefore := b.lastObservation[key].Fingerprint()
	anchorsBefore := len(b.lastObservation)
	b.mu.Unlock()

	bad := n3MalformedDraft(key)
	n3AssertFixtureRejected(t, bad)
	b.Add(bad)

	b.mu.Lock()
	fpAfter := b.lastFingerprint[key]
	revAfter := b.lastRevision[key]
	obsAfter := b.lastObservation[key].Fingerprint()
	anchorsAfter := len(b.lastObservation)
	pendingAfter := b.pending
	b.mu.Unlock()
	if fpAfter != fpBefore || revAfter != revBefore || obsAfter != obsBefore || anchorsAfter != anchorsBefore {
		t.Fatalf("fingerprint failure mutated the accepted anchor: fp %q->%q rev %d->%d anchors %d->%d",
			fpBefore, fpAfter, revBefore, revAfter, anchorsBefore, anchorsAfter)
	}
	if pendingAfter != 0 {
		t.Fatalf("fingerprint-failed same-key draft was retained as pending: %d", pendingAfter)
	}

	drained := b.DrainEconomicObservations()
	if len(drained) != 1 {
		t.Fatalf("same-key fingerprint failure drained=%d, want one marker: %+v", len(drained), drained)
	}
	marker, ok := r5UnavailableMarker(drained)
	if !ok || r5UnavailableCause(marker) != providerEvidenceLossInvalidDraft {
		t.Fatalf("same-key fingerprint failure must surface an invalid_draft marker: %+v", drained)
	}
	if len(marker.Supersedes) != 0 {
		t.Fatalf("invalid draft fabricated a supersession anchor: %+v", marker.Supersedes)
	}
	if _, leaked := f6ObservationByKey(drained, key); leaked {
		t.Fatalf("malformed same-key draft leaked as a superseding observation: %+v", drained)
	}
}

// Successful fingerprint semantics are preserved: an exact replay is a no-op
// with no marker, and an A -> B -> A correction remains admissible.
func TestN3FingerprintSuccessSemanticsPreserved(t *testing.T) {
	t.Parallel()
	const key = "provider.n3.replay"

	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	a := nativeMediaMoneyDraft(key, 1)
	b.Add(a)
	if got := b.DrainEconomicObservations(); len(got) != 1 {
		t.Fatalf("initial A drain=%d, want 1", len(got))
	}
	b.Add(a) // exact replay of the drained anchor
	replay := b.DrainEconomicObservations()
	if len(replay) != 0 {
		t.Fatalf("exact replay must emit nothing, got %+v", replay)
	}
	if _, ok := r5UnavailableMarker(replay); ok {
		t.Fatal("exact replay must not emit a loss marker")
	}

	// A -> B -> A: the final A is a legitimate correction, not a stale replay.
	b.Add(nativeMediaMoneyDraft(key, 2))
	if got := b.DrainEconomicObservations(); len(got) != 1 {
		t.Fatalf("B drain=%d, want 1", len(got))
	}
	b.Add(a)
	corrected := b.DrainEconomicObservations()
	if len(corrected) != 1 {
		t.Fatalf("A-after-B correction drain=%d, want 1: %+v", len(corrected), corrected)
	}
	if _, ok := r5UnavailableMarker(corrected); ok {
		t.Fatal("A -> B -> A correction must not emit a loss marker")
	}
}

// Repeated fingerprint failures across distinct sources collapse to one bounded
// invalid_draft marker per drain, mirroring the closed disposition set.
func TestN3RepeatedFingerprintFailuresAreBoundedToOneMarker(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	for i := range 5 {
		bad := n3MalformedDraft("provider.n3.repeat." + strconv.Itoa(i))
		n3AssertFixtureRejected(t, bad)
		b.Add(bad)
	}
	drained := b.DrainEconomicObservations()
	if len(drained) != 1 {
		t.Fatalf("repeated fingerprint failures drained=%d, want one bounded marker: %+v", len(drained), drained)
	}
	marker, ok := r5UnavailableMarker(drained)
	if !ok || r5UnavailableCause(marker) != providerEvidenceLossInvalidDraft {
		t.Fatalf("repeated fingerprint failures must surface one invalid_draft marker: %+v", drained)
	}
	n3AssertNoMalformedMeasure(t, drained)
}

// The reserved loss-marker capacity is independent of the ordinary pending cap:
// a saturated pending buffer must not hide the fingerprint-failure loss.
func TestN3FingerprintFailureSurvivesSaturatedPendingCapacity(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	for i := range maxProviderEvidenceDrafts {
		b.Add(nativeMediaMoneyDraft("provider.n3.full."+strconv.Itoa(i), 1))
	}
	b.mu.Lock()
	pending := b.pending
	b.mu.Unlock()
	if pending != maxProviderEvidenceDrafts {
		t.Fatalf("pending capacity=%d, want %d", pending, maxProviderEvidenceDrafts)
	}

	bad := n3MalformedDraft("provider.n3.full.malformed")
	n3AssertFixtureRejected(t, bad)
	b.Add(bad)

	drained := b.DrainEconomicObservations()
	if len(drained) != maxProviderEvidenceDrafts+1 {
		t.Fatalf("saturated drain=%d, want %d ordinary observations plus one loss marker", len(drained), maxProviderEvidenceDrafts+1)
	}
	marker, ok := r5UnavailableMarker(drained)
	if !ok || r5UnavailableCause(marker) != providerEvidenceLossInvalidDraft {
		t.Fatalf("saturated pending buffer hid the fingerprint-failure loss: %+v", marker)
	}
	if _, leaked := f6ObservationByKey(drained, "provider.n3.full.malformed"); leaked {
		t.Fatalf("malformed draft leaked under saturated capacity: %+v", drained)
	}
	n3AssertNoMalformedMeasure(t, drained)
}
