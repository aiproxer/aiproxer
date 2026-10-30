package metering

import (
	"strconv"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestProviderEvidenceBufferRequiresTrustedBLegBinding(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.AddUsageEvent(providerUsageEvent(3, 2), "test.provider.v2")
	if got := b.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("unbound provider evidence must not become an observation: %d", len(got))
	}
	b.BindEconomicEvidence(testObservationIdentity())
	observations := b.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("bound provider evidence observations = %d, want 1", len(observations))
	}
	if got := observations[0].Subject.Kind; got != sdkmetering.SubjectBLeg {
		t.Fatalf("subject kind = %q, want B-leg", got)
	}
	if got := observations[0].Subject.BLegID; got != "b-leg-1" {
		t.Fatalf("B-leg = %q, want b-leg-1", got)
	}
}

func TestProviderEvidenceBufferDeduplicatesReplayAndRetainsRevision(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.AddUsageEvent(providerUsageEvent(3, 2), "test.provider.v2")
	b.AddUsageEvent(providerUsageEvent(3, 2), "test.provider.v2")
	b.BindEconomicEvidence(testObservationIdentity())
	observations := b.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("replay observations = %d, want 1", len(observations))
	}
	if observations[0].Revision != 1 {
		t.Fatalf("initial revision = %d, want 1", observations[0].Revision)
	}
	changed := providerUsageEvent(4, 2)
	b.AddUsageEvent(changed, "test.provider.v2")
	changedObservations := b.DrainEconomicObservations()
	if len(changedObservations) != 1 {
		t.Fatalf("late revision observations = %d, want 1", len(changedObservations))
	}
	if changedObservations[0].Revision != 2 {
		t.Fatalf("late revision = %d, want 2", changedObservations[0].Revision)
	}
	if observations[0].Fingerprint() == changedObservations[0].Fingerprint() {
		t.Fatal("changed provider payload must retain a distinct fingerprint")
	}
	if len(changedObservations[0].Supersedes) != 1 || changedObservations[0].Supersedes[0].ObservationID != observations[0].ID {
		t.Fatalf("revision supersession = %+v, want prior observation %q", changedObservations[0].Supersedes, observations[0].ID)
	}
	// Exact replay remains suppressed after the first drain as well.
	b.AddUsageEvent(changed, "test.provider.v2")
	if got := b.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("replayed late revision observations = %d, want 0", len(got))
	}
}

func TestProviderEvidenceBufferRetainsABACorrectionAcrossDrains(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())

	for _, wantInput := range []int{3, 4, 3} {
		b.AddUsageEvent(providerUsageEvent(wantInput, 2), "test.provider.v2")
		observations := b.DrainEconomicObservations()
		if len(observations) != 1 {
			t.Fatalf("input=%d observations=%d, want one correction", wantInput, len(observations))
		}
	}
	// Only an immediately consecutive replay is suppressed. The last A is
	// already the current source revision, so replaying it is a no-op.
	b.AddUsageEvent(providerUsageEvent(3, 2), "test.provider.v2")
	if got := b.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("consecutive A replay observations=%d, want 0", len(got))
	}
}

func TestProviderEvidenceBufferFingerprintRetainsSemanticChanges(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	base := ProviderEvidenceDraft{
		SourceEventKey: "provider.semantic",
		StreamID:       "provider.v2",
		Origin:         sdkmetering.OriginProvider,
		Acquisition:    sdkmetering.AcquisitionProviderResponse,
		Authority:      sdkmetering.AuthorityObservedClaim,
		Perspective:    sdkmetering.PerspectiveOperator,
		Boundary:       sdkmetering.BoundaryBackendEgress,
		Lifecycle:      sdkmetering.LifecycleBackendAttempt,
		Semantics:      sdkmetering.SemanticsCumulative,
		Coverage:       "partial",
		CoverageReason: "start snapshot",
	}
	changed := base
	changed.Origin = sdkmetering.OriginLocal
	changed.Acquisition = sdkmetering.AcquisitionLocalEstimator
	changed.Authority = sdkmetering.AuthorityEstimatedClaim
	changed.Perspective = sdkmetering.PerspectiveCustomer
	changed.Boundary = sdkmetering.BoundaryFrontendIngress
	changed.Lifecycle = sdkmetering.LifecycleLogicalRequest
	changed.Semantics = sdkmetering.SemanticsCorrection
	changed.Coverage = "complete"
	changed.CoverageReason = "corrected snapshot"
	b.Add(base)
	b.Add(changed)
	b.mu.Lock()
	got := len(b.drafts)
	b.mu.Unlock()
	if got != 2 {
		t.Fatalf("semantic revisions retained=%d, want 2", got)
	}
}

func TestProviderEvidenceBufferIgnoresTimestampOnlyReplay(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	usage := providerUsageEvent(3, 2)
	first := ProviderEvidenceDraft{
		SourceEventKey: "provider.timestamp", StreamID: "provider.v2",
		Measures: providerTokenMeasures(usage, "provider.v2"), Evidence: providerTokenEvidence(usage),
		ObservedAt: time.Unix(10, 0), ReceivedAt: time.Unix(11, 0),
	}
	second := first
	second.ObservedAt = time.Unix(20, 0)
	second.ReceivedAt = time.Unix(21, 0)
	b.Add(first)
	if got := b.DrainEconomicObservations(); len(got) != 1 {
		t.Fatalf("initial timestamp payload observations=%d, want 1", len(got))
	}
	b.Add(second)
	if got := b.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("timestamp-only replay observations=%d, want 0", len(got))
	}
}

// A provider-supplied explicit source revision is an UNSUPPORTED capability.
// The SDK observation cannot carry source-vs-host revision provenance, so a
// late older explicit revision is observationally identical to a valid
// cumulative/delta update and could duplicate a charge. Add rejects every
// explicit Revision/Sequence draft and surfaces one sticky unavailable loss
// marker; no provider revision evidence is accepted, so the reduction cannot be
// COMPLETE.
func TestProviderEvidenceBufferRejectsExplicitSourceRevision(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())

	// RED motivation: newer then older explicit revisions of one source, each
	// carrying its own provider charge.
	for _, revision := range []uint64{2, 1} {
		b.Add(nativeExplicitMediaMoneyDraft("provider.explicit-revision", revision))
	}

	observations := b.DrainEconomicObservations()
	marker, ok := r5UnavailableMarker(observations)
	if !ok {
		t.Fatalf("explicit source revision must surface one loss marker, got %d observations", len(observations))
	}
	if r5UnavailableCause(marker) != providerEvidenceLossUnsupportedOrderingIdentity {
		t.Fatalf("loss reason=%q, want %q", r5UnavailableCause(marker), providerEvidenceLossUnsupportedOrderingIdentity)
	}
	for _, observation := range observations {
		if observation.SourceEventKey == "provider.explicit-revision" {
			t.Fatalf("explicit source revision leaked into accepted provider evidence: %+v", observation)
		}
	}
	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		t.Fatalf("apply unsupported-revision evidence: %v", err)
	}
	if snapshot.Complete {
		t.Fatal("unsupported explicit revision must keep the reduction incomplete")
	}
}

// Both explicit-revision variants are unsupported: an exact duplicate and a
// changed payload at the same revision. Neither is misrated as a correction or
// conflict, and rejection stays visible on a later drain.
func TestProviderEvidenceBufferExplicitRevisionVariantsUnsupported(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	b.Add(nativeExplicitMediaMoneyDraft("provider.explicit-variants", 7))
	b.Add(nativeExplicitMediaMoneyDraft("provider.explicit-variants", 7)) // exact duplicate
	changed := nativeExplicitMediaMoneyDraft("provider.explicit-variants", 9)
	changed.Revision = 7 // changed payload under the same explicit revision
	b.Add(changed)

	observations := b.DrainEconomicObservations()
	marker, ok := r5UnavailableMarker(observations)
	if !ok || r5UnavailableCause(marker) != providerEvidenceLossUnsupportedOrderingIdentity {
		t.Fatalf("explicit revision variants must surface an unsupported-ordering loss marker: %+v", observations)
	}
	// A later explicit revision remains visibly unsupported rather than becoming
	// an apparently complete accepted prefix.
	b.Add(nativeExplicitMediaMoneyDraft("provider.explicit-variants", 1))
	again := b.DrainEconomicObservations()
	marker, ok = r5UnavailableMarker(again)
	if !ok || r5UnavailableCause(marker) != providerEvidenceLossUnsupportedOrderingIdentity {
		t.Fatalf("later explicit revision was not visibly rejected: %+v", again)
	}
}

// A provider-supplied Sequence is part of the same unsupported ordering
// identity and is rejected without accepting the payload.
func TestProviderEvidenceBufferRejectsProviderSequence(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	draft := nativeMediaMoneyDraft("provider.sequence", 0)
	draft.Sequence = 9
	b.Add(draft)
	observations := b.DrainEconomicObservations()
	marker, ok := r5UnavailableMarker(observations)
	if !ok || r5UnavailableCause(marker) != providerEvidenceLossUnsupportedOrderingIdentity {
		t.Fatalf("provider sequence must be visibly rejected: %+v", observations)
	}
}

// The healthy implicit path is unaffected: a draft that supplies no ordering
// identity is accepted, assigned a host revision/sequence, and reduced to a
// complete snapshot.
func TestProviderEvidenceBufferImplicitPathUnaffected(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	b.Add(nativeMediaMoneyDraft("provider.implicit", 0))
	b.Add(nativeMediaMoneyDraft("provider.implicit", 0)) // exact replay suppressed
	observations := b.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("implicit observations=%d, want 1", len(observations))
	}
	if observations[0].Revision == 0 || observations[0].Sequence == 0 {
		t.Fatalf("buffer must assign revision/sequence: %+v", observations[0])
	}
	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		t.Fatalf("apply implicit evidence: %v", err)
	}
	if !snapshot.Complete {
		t.Fatal("healthy implicit evidence must stay complete")
	}
}

func TestProviderEvidenceBufferPreservesMediaChargeAndProviderLineage(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	amount := sdkmetering.Decimal{Coefficient: "0"}
	b.Add(ProviderEvidenceDraft{
		SourceEventKey:     "provider.response:1",
		StreamID:           "test.provider.v2",
		ProviderAccountKey: "acct-1",
		ProviderRequestID:  "req-1",
		ProviderChargeID:   "charge-1",
		Measures: []sdkmetering.Measure{
			{Key: sdkmetering.ComponentKey{Direction: sdkmetering.DirectionInput, Component: sdkmetering.ComponentImage, Unit: sdkmetering.UnitImage, SchemaID: "provider.test.v1"}, Value: decimalPtr("2"), Quality: sdkmetering.QualityObserved},
			{Key: sdkmetering.ComponentKey{Direction: sdkmetering.DirectionOutput, Component: sdkmetering.ComponentAudio, Unit: sdkmetering.UnitSecond, SchemaID: "provider.test.v1"}, Value: &sdkmetering.Decimal{Coefficient: "15", Scale: 1}, Quality: sdkmetering.QualityObserved},
		},
		Charges:  []sdkmetering.ReportedCharge{{ChargeItemID: "charge-1", Amount: &amount, Currency: "USD", Kind: sdkmetering.ChargeKindAggregate}},
		Evidence: []sdkmetering.SafeEvidenceField{{Path: "$.cost.amount", Lexeme: "0", Present: true, Acquisition: sdkmetering.AcquisitionProviderResponse}},
	})
	b.mu.Lock()
	draft := cloneProviderEvidenceDraft(b.drafts[0])
	b.mu.Unlock()
	if _, err := providerObservationValidated(testObservationIdentity(), draft); err != nil {
		t.Fatalf("media draft should validate: %v", err)
	}
	b.BindEconomicEvidence(testObservationIdentity())
	observations := b.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("media provider observation count = %d, want 1", len(observations))
	}
	o := observations[0]
	if o.Subject.ProviderAccountKey != "acct-1" || o.Correlation.ProviderRequestID != "req-1" || o.Subject.ProviderChargeID != "charge-1" {
		t.Fatalf("provider lineage was not preserved: subject=%+v correlation=%+v", o.Subject, o.Correlation)
	}
	if len(o.Measures) != 2 || len(o.Charges) != 1 {
		t.Fatalf("media/charge counts = %d/%d, want 2/1", len(o.Measures), len(o.Charges))
	}
	if o.Charges[0].Amount == nil || o.Charges[0].Amount.Coefficient != "0" {
		t.Fatalf("present zero charge was not preserved: %+v", o.Charges[0])
	}
}

func TestProviderEvidenceBufferRejectsUnsafeEvidenceWithoutDroppingOtherDrafts(t *testing.T) {
	t.Parallel()
	b := NewProviderEvidenceBuffer()
	b.Add(ProviderEvidenceDraft{
		SourceEventKey: "unsafe",
		Evidence:       []sdkmetering.SafeEvidenceField{{Path: "$.secret", Lexeme: "should-not-retain", Present: true, Acquisition: sdkmetering.AcquisitionProviderResponse}},
	})
	b.AddUsageEvent(providerUsageEvent(1, 1), "test.provider.v2")
	b.BindEconomicEvidence(testObservationIdentity())
	observations := b.DrainEconomicObservations()
	// The unsafe field is never retained and the unrelated valid draft survives.
	// Its rejection is now visible as a bounded loss marker instead of a silent
	// drop, so an admitted prefix cannot reduce as complete (F6).
	valid, ok := f6ObservationByKey(observations, "test.provider.v2:stream")
	if !ok {
		t.Fatalf("valid draft was dropped alongside the unsafe draft: %+v", observations)
	}
	for _, observation := range observations {
		for _, field := range observation.Evidence {
			if field.Lexeme == "should-not-retain" {
				t.Fatalf("unsafe evidence leaked into an observation: %+v", observation)
			}
		}
	}
	marker, ok := r5UnavailableMarker(observations)
	if !ok || r5UnavailableCause(marker) != providerEvidenceLossInvalidDraft {
		t.Fatalf("unsafe draft must surface a %q loss marker: %+v", providerEvidenceLossInvalidDraft, observations)
	}
	snapshot, err := aggregate.ApplyObservations([]sdkmetering.Observation{valid, marker})
	if err != nil {
		t.Fatalf("apply retained evidence: %v", err)
	}
	if snapshot.Complete {
		t.Fatal("rejected unsafe evidence must keep the reduction incomplete")
	}
}

func providerUsageEvent(input, output int) aipapi.Event {
	return aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: input, OutputTokens: output,
		UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true},
		Accounting: aipapi.UsageAccountingMetadata{
			Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
			DedupeKey: "test.provider.v2:stream", ProviderAccountKey: "acct", ProviderRequestID: "req",
		},
	}
}

func testObservationIdentity() ObservationIdentity {
	now := time.Unix(1700000000, 0).UTC()
	return ObservationIdentity{
		StoreID: "store-1", RequestID: "request-1", CallID: "call-1", BillingCallID: "billing-1",
		ALegID: "a-leg-1", BLegID: "b-leg-1", AttemptID: "attempt-1", AttemptSeq: 1,
		ObservedAt: now, ReceivedAt: now,
	}
}

func decimalPtr(coefficient string) *sdkmetering.Decimal {
	value := sdkmetering.Decimal{Coefficient: coefficient}
	return &value
}

func canonicalCoefficient(coefficient string) string {
	return sdkmetering.Decimal{Coefficient: coefficient}.CanonicalString()
}

func imageKey() sdkmetering.ComponentKey {
	return sdkmetering.ComponentKey{
		Direction: sdkmetering.DirectionInput, Component: sdkmetering.ComponentImage,
		Unit: sdkmetering.UnitImage, SchemaID: "provider.test.v1",
	}
}

// nativeMediaMoneyDraft carries one native provider media field and one genuine
// monetary charge so a provider value cannot hide behind a generic token
// quantity. It supplies no ordering identity: the buffer assigns the revision
// and sequence.
func nativeMediaMoneyDraft(key string, count uint64) ProviderEvidenceDraft {
	quantity := strconv.FormatUint(count, 10)
	money := sdkmetering.Decimal{Coefficient: strconv.FormatUint(count*100, 10)}
	return ProviderEvidenceDraft{
		SourceEventKey: key, StreamID: "provider.v2",
		Measures: []sdkmetering.Measure{{
			Key: imageKey(), Value: &sdkmetering.Decimal{Coefficient: quantity},
			Quality: sdkmetering.QualityObserved,
		}},
		Charges: []sdkmetering.ReportedCharge{{
			ChargeItemID: "charge:" + key, Amount: &money, Currency: "USD", Kind: sdkmetering.ChargeKindAggregate,
		}},
	}
}

// nativeExplicitMediaMoneyDraft is nativeMediaMoneyDraft with an unsupported
// caller-supplied explicit source revision, used to prove the enforceable
// boundary rejects it.
func nativeExplicitMediaMoneyDraft(key string, revision uint64) ProviderEvidenceDraft {
	draft := nativeMediaMoneyDraft(key, revision)
	draft.Revision = revision
	return draft
}
