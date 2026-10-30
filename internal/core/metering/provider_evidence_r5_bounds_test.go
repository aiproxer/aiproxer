package metering

import (
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// R5-A: pending occupancy is drained repeatedly, so lifetime retention must not
// permanently refuse future valid revisions. Native directional media and a
// genuine provider monetary charge must reach the final drain with a valid
// supersession chain, not merely generic token totals.
func TestProviderEvidenceBufferR5LifetimeRevisionsBeyondPendingCapacity(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())

	const revisions = 400
	emitted := make([]sdkmetering.Observation, 0, revisions)
	previousID := ""
	for revision := uint64(1); revision <= revisions; revision++ {
		b.Add(r5MediaMoneyDraft("provider.r5.lifetime", revision))
		drained := b.DrainEconomicObservations()
		if len(drained) != 1 {
			t.Fatalf("revision %d: drained observations=%d, want 1", revision, len(drained))
		}
		if revision > 1 {
			if len(drained[0].Supersedes) != 1 {
				t.Fatalf("revision %d: supersedes=%d, want 1", revision, len(drained[0].Supersedes))
			}
			ref := drained[0].Supersedes[0]
			if err := ref.Validate(); err != nil {
				t.Fatalf("revision %d: supersession ref invalid: %v", revision, err)
			}
			if ref.ObservationID != previousID {
				t.Fatalf("revision %d: supersedes %q, want previous %q", revision, ref.ObservationID, previousID)
			}
		}
		previousID = drained[0].ID
		emitted = append(emitted, drained[0])
	}
	final := emitted[len(emitted)-1]
	if final.Revision != revisions {
		t.Fatalf("final revision=%d, want %d", final.Revision, revisions)
	}
	if got, want := r5MeasureValue(final, imageKey()), canonicalCoefficient(strconv.FormatUint(revisions, 10)); got != want {
		t.Fatalf("final native media quantity=%q, want %q", got, want)
	}
	if len(final.Charges) != 1 || final.Charges[0].Amount == nil {
		t.Fatalf("final provider charge missing: %+v", final.Charges)
	}
	if got, want := final.Charges[0].Amount.CanonicalString(), canonicalCoefficient(strconv.FormatUint(revisions*100, 10)); got != want {
		t.Fatalf("final provider charge=%q, want %q", got, want)
	}
	snapshot, err := aggregate.ApplyObservations(emitted)
	if err != nil {
		t.Fatalf("apply lifetime evidence: %v", err)
	}
	if !snapshot.Complete {
		t.Fatal("complete provider revisions must not be marked incomplete")
	}
	if got, want := snapshot.ValueFor(final, imageKey()), canonicalCoefficient(strconv.FormatUint(revisions, 10)); got != want {
		t.Fatalf("effective media=%q, want %q", got, want)
	}
}

// R5-B: multiple sources, exact replays, and an A -> B -> A correction across
// drains must stay independently anchored.
func TestProviderEvidenceBufferR5MultiSourceReplaysCorrectionsAcrossDrains(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())

	values := []uint64{3, 4, 3}
	var firstA, finalA sdkmetering.Observation
	for round, value := range values {
		b.Add(r5MediaMoneyDraft("provider.r5.source.a", value))
		b.Add(r5MediaMoneyDraft("provider.r5.source.b", value*10))
		drained := b.DrainEconomicObservations()
		if len(drained) != 2 {
			t.Fatalf("round %d: drained=%d, want one per source", round, len(drained))
		}
		byKey := r5BySource(drained)
		finalA = byKey["provider.r5.source.a"]
		if round == 0 {
			firstA = finalA
		}
		if got, want := r5MeasureValue(finalA, imageKey()), canonicalCoefficient(strconv.FormatUint(value, 10)); got != want {
			t.Fatalf("round %d: source a media=%q, want %q", round, got, want)
		}
		// Exact replay through the cross-drain anchor is suppressed.
		b.Add(r5MediaMoneyDraft("provider.r5.source.a", value))
		b.Add(r5MediaMoneyDraft("provider.r5.source.b", value*10))
		if got := b.DrainEconomicObservations(); len(got) != 0 {
			t.Fatalf("round %d: exact replay drained=%d, want 0", round, len(got))
		}
	}
	if finalA.ID == firstA.ID || finalA.Revision <= firstA.Revision {
		t.Fatalf("A->B->A correction did not advance revision: first=%d final=%d", firstA.Revision, finalA.Revision)
	}
	if got, want := r5MeasureValue(finalA, imageKey()), canonicalCoefficient("3"); got != want {
		t.Fatalf("A->B->A final source a media=%q, want restored %q", got, want)
	}
}

// R5-C: pending occupancy exhaustion must surface an explicit bounded
// incomplete marker instead of a silently truncated clean prefix, and must not
// permanently refuse later valid evidence.
func TestProviderEvidenceBufferR5PendingCapacitySignalsIncomplete(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())

	for i := range maxProviderEvidenceDrafts + 44 {
		b.Add(r5MediaMoneyDraft("provider.r5.pending."+strconv.Itoa(i), uint64(i+1)))
	}
	observations := b.DrainEconomicObservations()
	marker, ok := r5UnavailableMarker(observations)
	if !ok {
		t.Fatalf("pending capacity exhaustion must emit an explicit incomplete marker, got %d observations", len(observations))
	}
	if marker.Subject.Kind != sdkmetering.SubjectBLeg {
		t.Fatalf("loss marker subject=%q, want B-leg", marker.Subject.Kind)
	}
	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		t.Fatalf("apply pending-cap evidence: %v", err)
	}
	if snapshot.Complete {
		t.Fatal("dropped evidence must surface as incomplete, not a clean prefix")
	}
	b.mu.Lock()
	pending := b.pending
	b.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending occupancy after drain=%d, want 0", pending)
	}
	// The lifetime sequence must not permanently refuse future valid evidence:
	// a changed payload for an already tracked source is accepted after drain.
	b.Add(r5MediaMoneyDraft("provider.r5.pending.0", 999))
	if got := b.DrainEconomicObservations(); len(got) != 1 {
		t.Fatalf("evidence after drain=%d, want 1 (lifetime retention must not saturate)", len(got))
	}
}

// R5-C: source-history (anchor) exhaustion must signal explicit incomplete and
// stay bounded.
func TestProviderEvidenceBufferR5SourceHistoryExhaustionSignalsIncomplete(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	var lossSeen bool
	for i := 0; i < maxProviderEvidenceAnchors+16 && !lossSeen; i++ {
		b.Add(r5MediaMoneyDraft("provider.r5.history."+strconv.Itoa(i), 1))
		for _, observation := range b.DrainEconomicObservations() {
			if observation.Authority == sdkmetering.AuthorityUnavailableClaim {
				lossSeen = true
			}
		}
	}
	if !lossSeen {
		t.Fatal("source-history exhaustion must emit an explicit incomplete marker")
	}
	b.mu.Lock()
	tracked, anchorBytes := len(b.lastObservation), b.anchorBytes
	b.mu.Unlock()
	if tracked > maxProviderEvidenceAnchors {
		t.Fatalf("tracked source anchors=%d exceed bound %d", tracked, maxProviderEvidenceAnchors)
	}
	if anchorBytes > maxProviderEvidenceAnchorBytes {
		t.Fatalf("anchor bytes=%d exceed bound %d", anchorBytes, maxProviderEvidenceAnchorBytes)
	}
}

// R5-C: retained-byte exhaustion must signal explicit incomplete and stay
// within the byte budget.
func TestProviderEvidenceBufferR5RetainedBytesExhaustionSignalsIncomplete(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	filler := strings.Repeat("a", maxProviderEvidenceDraftBytes/2)
	var lossSeen bool
	for i := 0; i < maxProviderEvidenceAnchors+16 && !lossSeen; i++ {
		draft := r5MediaMoneyDraft("provider.r5.bytes."+strconv.Itoa(i), 1)
		draft.CoverageReason = filler
		b.Add(draft)
		for _, observation := range b.DrainEconomicObservations() {
			if observation.Authority == sdkmetering.AuthorityUnavailableClaim {
				lossSeen = true
			}
		}
	}
	if !lossSeen {
		t.Fatal("retained-byte exhaustion must emit an explicit incomplete marker")
	}
	b.mu.Lock()
	anchorBytes := b.anchorBytes
	b.mu.Unlock()
	if anchorBytes > maxProviderEvidenceAnchorBytes {
		t.Fatalf("anchor bytes=%d exceed bound %d", anchorBytes, maxProviderEvidenceAnchorBytes)
	}
}

// R5-D: oversized input is rejected before any unbounded metadata is retained,
// and the rejection is visible as an explicit incomplete marker.
func TestProviderEvidenceBufferR5RejectsOversizedInput(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	draft := r5MediaMoneyDraft("provider.r5.oversized", 1)
	draft.CoverageReason = strings.Repeat("x", maxProviderEvidenceDraftBytes+1)
	b.Add(draft)
	b.mu.Lock()
	pending, buffered, anchors := b.pending, len(b.drafts), len(b.lastObservation)
	b.mu.Unlock()
	if pending != 0 || buffered != 0 || anchors != 0 {
		t.Fatalf("oversized input retained metadata: pending=%d drafts=%d anchors=%d", pending, buffered, anchors)
	}
	observations := b.DrainEconomicObservations()
	marker, ok := r5UnavailableMarker(observations)
	if !ok {
		t.Fatalf("oversized input must surface an explicit incomplete marker: %+v", observations)
	}
	if marker.Revision == 0 {
		t.Fatal("loss marker must retain a revision")
	}
}

// R5 anchor safety: an explicit source revision is rejected before it can touch
// the retained anchor state, even after the anchor history is exhausted. The
// rejection is visible, no tracked anchor is evicted or mutated, and the sealed
// head stays effective.
func TestProviderEvidenceBufferR5ExplicitRevisionRejectedAfterAnchorExhaustion(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	b.Add(nativeMediaMoneyDraft("provider.r5.stale-guard", 10))
	head := b.DrainEconomicObservations()
	if len(head) != 1 {
		t.Fatalf("initial implicit revision drained=%d, want 1", len(head))
	}
	for i := range maxProviderEvidenceAnchors + 8 {
		b.Add(nativeMediaMoneyDraft("provider.r5.fill."+strconv.Itoa(i), 1))
		_ = b.DrainEconomicObservations()
	}
	b.mu.Lock()
	anchors := len(b.lastObservation)
	b.mu.Unlock()

	b.Add(nativeExplicitMediaMoneyDraft("provider.r5.stale-guard", 5))
	stale := b.DrainEconomicObservations()
	marker, ok := r5UnavailableMarker(stale)
	if !ok || r5UnavailableCause(marker) != providerEvidenceLossUnsupportedOrderingIdentity {
		t.Fatalf("explicit revision must be visibly rejected after anchor exhaustion: %+v", stale)
	}
	b.mu.Lock()
	after := len(b.lastObservation)
	b.mu.Unlock()
	if after != anchors {
		t.Fatalf("rejected explicit revision mutated anchor state: %d -> %d", anchors, after)
	}
	snapshot, err := aggregate.ApplyObservations(head)
	if err != nil {
		t.Fatalf("apply sealed head: %v", err)
	}
	if got, want := snapshot.ValueFor(head[0], imageKey()), canonicalCoefficient("10"); got != want {
		t.Fatalf("sealed head changed: media=%q want %q", got, want)
	}
}

// R5 adversarial: every retained variable-size nested field must be priced
// before fingerprint/clone. A single oversized nested string in any nested
// enum-backed or identifier field must reject the whole draft, not merely the
// reviewer-listed top-level fields.
func TestProviderEvidenceBufferR5OversizedNestedFieldsRejected(t *testing.T) {
	t.Parallel()
	oversized := strings.Repeat("x", maxProviderEvidenceDraftBytes+1)
	cases := []struct {
		name   string
		mutate func(*ProviderEvidenceDraft)
	}{
		{"measure quality", func(d *ProviderEvidenceDraft) { d.Measures[0].Quality = oversized }},
		{"measure key direction", func(d *ProviderEvidenceDraft) {
			d.Measures[0].Key.Direction = sdkmetering.FlowDirection(oversized)
		}},
		{"charge component direction", func(d *ProviderEvidenceDraft) {
			d.Charges[0].Component = &sdkmetering.ComponentKey{Direction: sdkmetering.FlowDirection(oversized)}
		}},
		{"charge kind", func(d *ProviderEvidenceDraft) { d.Charges[0].Kind = sdkmetering.ChargeKind(oversized) }},
		{"charge payer kind", func(d *ProviderEvidenceDraft) {
			d.Charges[0].Payer = sdkmetering.PaymentParty{Kind: sdkmetering.PaymentPartyKind(oversized)}
		}},
		{"charge payer id", func(d *ProviderEvidenceDraft) {
			d.Charges[0].Payer = sdkmetering.PaymentParty{ID: oversized}
		}},
		{"evidence acquisition", func(d *ProviderEvidenceDraft) { d.Evidence[0].Acquisition = oversized }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := NewProviderEvidenceBuffer()
			b.BindEconomicEvidence(testObservationIdentity())
			draft := r5MediaMoneyDraft("provider.r5.oversized.nested", 1)
			draft.Evidence = []sdkmetering.SafeEvidenceField{{
				Path: "$.usage.input_tokens", Lexeme: "1", Present: true,
				Acquisition: sdkmetering.AcquisitionProviderResponse,
			}}
			tc.mutate(&draft)
			b.Add(draft)
			b.mu.Lock()
			pending, buffered, anchors := b.pending, len(b.drafts), len(b.lastObservation)
			b.mu.Unlock()
			if pending != 0 || buffered != 0 || anchors != 0 {
				t.Fatalf("oversized %s retained metadata: pending=%d drafts=%d anchors=%d", tc.name, pending, buffered, anchors)
			}
			marker, ok := r5UnavailableMarker(b.DrainEconomicObservations())
			if !ok {
				t.Fatalf("oversized %s must surface an explicit incomplete marker", tc.name)
			}
			if marker.Revision == 0 {
				t.Fatal("loss marker must retain a revision")
			}
		})
	}
}

// R5 adversarial: the reserved loss-marker capacity must cover every distinct
// loss disposition. Otherwise, when several dispositions are admitted before a
// drain, the last one silently disappears and the aggregate can look complete.
func TestProviderEvidenceBufferR5LossCapacityCoversAllCauses(t *testing.T) {
	t.Parallel()
	if maxProviderEvidenceLossMarkers < len(providerEvidenceLossCauses) {
		t.Fatalf("reserved loss capacity %d < %d distinct causes", maxProviderEvidenceLossMarkers, len(providerEvidenceLossCauses))
	}
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	b.mu.Lock()
	for _, cause := range providerEvidenceLossCauses {
		b.recordProviderEvidenceLossLocked(cause)
	}
	reserved := len(b.lossDrafts)
	b.mu.Unlock()
	if reserved != len(providerEvidenceLossCauses) {
		t.Fatalf("reserved loss markers=%d, want one per distinct cause %d", reserved, len(providerEvidenceLossCauses))
	}
	observations := b.DrainEconomicObservations()
	seen := make(map[string]sdkmetering.Observation, len(providerEvidenceLossCauses))
	for _, observation := range observations {
		if observation.Authority != sdkmetering.AuthorityUnavailableClaim {
			continue
		}
		if err := observation.Validate(); err != nil {
			t.Fatalf("loss marker invalid: %v", err)
		}
		if observation.Subject.Kind != sdkmetering.SubjectBLeg {
			t.Fatalf("loss marker subject=%q, want B-leg", observation.Subject.Kind)
		}
		if len(observation.Measures) != 1 {
			t.Fatalf("loss marker measures=%d, want exactly one disposition", len(observation.Measures))
		}
		cause := observation.Measures[0].Reason
		if cause == "" {
			t.Fatalf("loss marker must name its disposition: %+v", observation.Measures[0])
		}
		if _, dup := seen[cause]; dup {
			t.Fatalf("duplicate loss cause marker %q", cause)
		}
		seen[cause] = observation
	}
	for _, cause := range providerEvidenceLossCauses {
		if _, ok := seen[cause]; !ok {
			t.Fatalf("loss cause %q not visible after drain, got %v", cause, r5LossCauses(observations))
		}
	}
	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		t.Fatalf("apply loss evidence: %v", err)
	}
	if snapshot.Complete {
		t.Fatal("admitted loss dispositions must make the aggregate incomplete")
	}
}

func r5LossCauses(observations []sdkmetering.Observation) []string {
	var causes []string
	for _, observation := range observations {
		if len(observation.Measures) == 1 {
			causes = append(causes, observation.Measures[0].Reason)
		}
	}
	return causes
}

// r5MediaMoneyDraft carries one native directional media quantity and one
// genuine provider monetary charge, both derived from count so the final
// snapshot can be compared exactly.
func r5MediaMoneyDraft(key string, count uint64) ProviderEvidenceDraft {
	money := sdkmetering.Decimal{Coefficient: strconv.FormatUint(count*100, 10)}
	return ProviderEvidenceDraft{
		SourceEventKey: key, StreamID: "provider.v2",
		Measures: []sdkmetering.Measure{{
			Key: imageKey(), Value: &sdkmetering.Decimal{Coefficient: strconv.FormatUint(count, 10)},
			Quality: sdkmetering.QualityObserved,
		}},
		Charges: []sdkmetering.ReportedCharge{{
			ChargeItemID: "charge:" + key, Amount: &money, Currency: "USD",
			Kind: sdkmetering.ChargeKindAggregate,
		}},
	}
}

func r5MeasureValue(observation sdkmetering.Observation, key sdkmetering.ComponentKey) string {
	want := key.CanonicalKey()
	for _, measure := range observation.Measures {
		if measure.Key.CanonicalKey() == want && measure.Value != nil {
			return measure.Value.CanonicalString()
		}
	}
	return ""
}

func r5BySource(observations []sdkmetering.Observation) map[string]sdkmetering.Observation {
	out := make(map[string]sdkmetering.Observation, len(observations))
	for _, observation := range observations {
		out[observation.SourceEventKey] = observation
	}
	return out
}

func r5UnavailableMarker(observations []sdkmetering.Observation) (sdkmetering.Observation, bool) {
	for _, observation := range observations {
		if observation.Authority == sdkmetering.AuthorityUnavailableClaim {
			return observation, true
		}
	}
	return sdkmetering.Observation{}, false
}

// r5UnavailableCause returns the bounded loss disposition carried by an
// unavailable provider-neutral loss marker, or "" when none is present.
func r5UnavailableCause(observation sdkmetering.Observation) string {
	for _, measure := range observation.Measures {
		if measure.Quality == sdkmetering.QualityUnavailable && measure.Reason != "" {
			return measure.Reason
		}
	}
	return ""
}

// R5 adversarial: a collection of zero-length nested elements carries no
// content bytes, so without a cardinality charge it would slip under the
// per-draft byte cap and be fingerprinted/cloned. It must be rejected before
// any retention and surfaced as an explicit incomplete marker.
func TestProviderEvidenceBufferR5ZeroLengthElementsRespectByteCap(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	draft := r5MediaMoneyDraft("provider.r5.zero-length", 1)
	draft.Evidence = make([]sdkmetering.SafeEvidenceField, 100000)
	b.Add(draft)
	b.mu.Lock()
	pending, buffered, anchors := b.pending, len(b.drafts), len(b.lastObservation)
	b.mu.Unlock()
	if pending != 0 || buffered != 0 || anchors != 0 {
		t.Fatalf("zero-length collection retained metadata: pending=%d drafts=%d anchors=%d", pending, buffered, anchors)
	}
	marker, ok := r5UnavailableMarker(b.DrainEconomicObservations())
	if !ok {
		t.Fatal("zero-length collection must surface an explicit incomplete marker")
	}
	if marker.Revision == 0 {
		t.Fatal("loss marker must retain a revision")
	}
}

// R5 adversarial: growing an already-tracked source's anchor must be charged as
// a replacement (anchorBytes - oldSize + newSize) rather than as an increment,
// so many tracked sources cannot each grow past the total byte budget.
func TestProviderEvidenceBufferR5TrackedAnchorGrowthRespectsByteCap(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	for i := range 40 {
		b.Add(r5MediaMoneyDraft("provider.r5.grow."+strconv.Itoa(i), 1))
		if got := b.DrainEconomicObservations(); len(got) != 1 {
			t.Fatalf("seed %d observations=%d, want 1", i, len(got))
		}
	}
	filler := strings.Repeat("a", 32000)
	var sawLoss bool
	for i := range 40 {
		draft := r5MediaMoneyDraft("provider.r5.grow."+strconv.Itoa(i), 2)
		draft.CoverageReason = filler
		b.Add(draft)
		for _, observation := range b.DrainEconomicObservations() {
			if observation.Authority == sdkmetering.AuthorityUnavailableClaim {
				sawLoss = true
			}
		}
	}
	if !sawLoss {
		t.Fatal("anchor byte-cap refusal must surface an explicit incomplete marker")
	}
	b.mu.Lock()
	tracked, anchorBytes := len(b.lastObservation), b.anchorBytes
	b.mu.Unlock()
	if tracked > maxProviderEvidenceAnchors {
		t.Fatalf("tracked anchors=%d exceed bound %d", tracked, maxProviderEvidenceAnchors)
	}
	if anchorBytes > maxProviderEvidenceAnchorBytes {
		t.Fatalf("anchor bytes=%d exceed bound %d", anchorBytes, maxProviderEvidenceAnchorBytes)
	}
}

// R5 adversarial: when a tracked anchor growth is refused, the prior anchor,
// replay suppression and supersession relation must stay coherent, and the
// refusal must not partially mutate the retained state.
func TestProviderEvidenceBufferR5TrackedAnchorGrowthKeepsPriorAnchor(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	b.Add(r5MediaMoneyDraft("provider.r5.keep", 1))
	seeded := b.DrainEconomicObservations()
	if len(seeded) != 1 {
		t.Fatalf("seed observations=%d, want 1", len(seeded))
	}
	seedID := seeded[0].ID

	// Fill the anchor byte budget so the seeded source's large replacement must
	// be refused.
	filler := strings.Repeat("a", 60000)
	for i := range 17 {
		draft := r5MediaMoneyDraft("provider.r5.filler."+strconv.Itoa(i), 1)
		draft.CoverageReason = filler
		b.Add(draft)
		_ = b.DrainEconomicObservations()
	}
	grown := r5MediaMoneyDraft("provider.r5.keep", 2)
	grown.CoverageReason = filler
	b.Add(grown)
	var sawLoss bool
	for _, observation := range b.DrainEconomicObservations() {
		if observation.Authority == sdkmetering.AuthorityUnavailableClaim {
			sawLoss = true
		}
	}
	if !sawLoss {
		t.Fatal("refused replacement must surface an explicit incomplete marker")
	}
	b.mu.Lock()
	anchorBytes := b.anchorBytes
	b.mu.Unlock()
	if anchorBytes > maxProviderEvidenceAnchorBytes {
		t.Fatalf("anchor bytes=%d exceed bound %d", anchorBytes, maxProviderEvidenceAnchorBytes)
	}

	// The prior anchor is untouched: an exact replay of the seeded payload is
	// still suppressed, and a genuinely newer payload still supersedes it.
	b.Add(r5MediaMoneyDraft("provider.r5.keep", 1))
	if got := b.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("exact replay after refused replacement drained=%d, want 0", len(got))
	}
	b.Add(r5MediaMoneyDraft("provider.r5.keep", 3))
	superseding := b.DrainEconomicObservations()
	if len(superseding) != 1 {
		t.Fatalf("newer payload after refused replacement drained=%d, want 1", len(superseding))
	}
	if len(superseding[0].Supersedes) != 1 || superseding[0].Supersedes[0].ObservationID != seedID {
		t.Fatalf("supersession chain lost the prior anchor: %+v", superseding[0].Supersedes)
	}
}

// R5 adversarial: an explicit source revision is unsupported regardless of how
// many host revisions a source has already emitted. A changed payload at an
// explicitly supplied revision is rejected like every other explicit variant,
// never misread as a retained replay.
func TestProviderEvidenceBufferR5ExplicitRevisionAlwaysUnsupported(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	for revision := uint64(1); revision <= 8; revision++ {
		b.Add(nativeExplicitMediaMoneyDraft("provider.r5.explicit", revision))
		if got := b.DrainEconomicObservations(); len(got) != 1 {
			t.Fatalf("revision %d observations=%d, want one loss marker", revision, len(got))
		}
	}
	// A changed payload at a host revision that happens to match an earlier
	// explicit value is still rejected because the draft carries an explicit
	// revision.
	changed := r5MediaMoneyDraft("provider.r5.explicit", 999)
	changed.Revision = 1
	b.Add(changed)
	observations := b.DrainEconomicObservations()
	if marker, ok := r5UnavailableMarker(observations); !ok ||
		r5UnavailableCause(marker) != providerEvidenceLossUnsupportedOrderingIdentity {
		t.Fatalf("explicit revision must stay visibly unsupported: %+v", observations)
	}
}
