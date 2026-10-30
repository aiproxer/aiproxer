package metering

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
)

// R8 gate-order boundary. Add must reject an explicit Revision/Sequence before
// it treats an empty/whitespace SourceEventKey as a benign no-op. Otherwise a
// healthy admitted prefix followed by a malformed unsupported-ordering draft
// that also omitted its source key would silently disappear, leaving the
// accepted prefix able to look complete (and therefore payable). The rejection
// is only meaningful for a non-nil buffer; a nil buffer stays a genuine no-op.
func TestProviderEvidenceBufferR8OrderingGatePrecedesEmptySourceKey(t *testing.T) {
	t.Parallel()

	malformedShapes := []struct {
		name   string
		mutate func(*ProviderEvidenceDraft)
	}{
		{
			name: "empty_key_revision_only",
			mutate: func(d *ProviderEvidenceDraft) {
				d.SourceEventKey = ""
				d.Revision = 1
			},
		},
		{
			name: "whitespace_key_revision_only",
			mutate: func(d *ProviderEvidenceDraft) {
				d.SourceEventKey = " \t "
				d.Revision = 1
			},
		},
		{
			name: "empty_key_sequence_only",
			mutate: func(d *ProviderEvidenceDraft) {
				d.SourceEventKey = ""
				d.Sequence = 7
			},
		},
		{
			name: "whitespace_key_sequence_only",
			mutate: func(d *ProviderEvidenceDraft) {
				d.SourceEventKey = "\n"
				d.Sequence = 7
			},
		},
		{
			name: "empty_key_revision_and_sequence",
			mutate: func(d *ProviderEvidenceDraft) {
				d.SourceEventKey = ""
				d.Revision = 2
				d.Sequence = 9
			},
		},
		{
			name: "whitespace_key_revision_and_sequence",
			mutate: func(d *ProviderEvidenceDraft) {
				d.SourceEventKey = "   "
				d.Revision = 3
				d.Sequence = 11
			},
		},
	}

	for _, tc := range malformedShapes {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b := NewProviderEvidenceBuffer()
			b.BindEconomicEvidence(testObservationIdentity())

			// A healthy admitted prefix must not be able to look complete once a
			// subsequent unsupported-ordering draft arrives.
			b.Add(nativeMediaMoneyDraft("provider.r8.gate.prefix", 1))

			malformed := nativeMediaMoneyDraft("provider.r8.gate.malformed", 3)
			tc.mutate(&malformed)
			b.Add(malformed)

			// A later healthy update must retain the sticky marker rather than
			// clearing it or completing the prefix.
			b.Add(nativeMediaMoneyDraft("provider.r8.gate.update", 2))

			observations := b.DrainEconomicObservations()

			marker, ok := r5UnavailableMarker(observations)
			if !ok {
				t.Fatalf("malformed ordering draft left no unavailable loss marker: %+v", observations)
			}
			if cause := r5UnavailableCause(marker); cause != providerEvidenceLossUnsupportedOrderingIdentity {
				t.Fatalf("loss cause=%q, want %q", cause, providerEvidenceLossUnsupportedOrderingIdentity)
			}
			for _, observation := range observations {
				if strings.TrimSpace(observation.SourceEventKey) == "" {
					t.Fatalf("empty-key malformed draft leaked into provider evidence: %+v", observation)
				}
			}

			// prefix + healthy update + one loss marker; the rejected draft did
			// not silently replace any of them.
			if len(observations) != 3 {
				t.Fatalf("observations=%d, want prefix+update+loss marker", len(observations))
			}

			snapshot, err := aggregate.ApplyObservations(observations)
			if err != nil {
				t.Fatalf("apply malformed-ordering evidence: %v", err)
			}
			if snapshot.Complete {
				t.Fatal("unsupported ordering draft must keep the reduction incomplete")
			}
		})
	}
}

// The benign no-op is preserved for a draft that supplies neither a source key
// nor an ordering identity: there is nothing to key, replay or reject, so no
// loss marker may be fabricated.
func TestProviderEvidenceBufferR8EmptySourceKeyWithoutOrderingIsBenignNoop(t *testing.T) {
	t.Parallel()

	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	b.Add(nativeMediaMoneyDraft("provider.r8.benign", 1))

	noop := nativeMediaMoneyDraft("provider.r8.ignored", 1)
	noop.SourceEventKey = ""
	b.Add(noop)

	observations := b.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("observations=%d, want only the healthy draft", len(observations))
	}
	if _, ok := r5UnavailableMarker(observations); ok {
		t.Fatalf("benign empty-key draft must not fabricate a loss marker: %+v", observations)
	}
}

// A nil buffer has no loss ledger, so the ordering gate cannot record anything.
// Add must stay a genuine no-op and must never panic.
func TestProviderEvidenceBufferR8NilBufferOrderingGateIsNoop(t *testing.T) {
	t.Parallel()

	var b *ProviderEvidenceBuffer
	b.Add(ProviderEvidenceDraft{Revision: 1})
	b.Add(ProviderEvidenceDraft{Sequence: 1})
	b.Add(ProviderEvidenceDraft{SourceEventKey: "", Revision: 1, Sequence: 1})

	if got := b.DrainEconomicObservations(); got != nil {
		t.Fatalf("nil buffer drain=%+v, want nil", got)
	}
}
