package runtime

import (
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// TestR9TerminalRetentionMarkerSurvivesSaturatedConflictOrigins drives the real
// terminal record builder with a saturated ordinary conflict set produced by an
// independent origin (local submission-linkage quarantine) plus the reserved
// economic retention-overflow disposition. The reserved marker must survive into
// the actual BillingCall record, the durable cap must hold, and ordinary
// conflicts must stay visible.
func TestR9TerminalRetentionMarkerSurvivesSaturatedConflictOrigins(t *testing.T) {
	ordinary := make([]usageevidence.EvidenceConflict, usageevidence.MaxCallLegEvidenceConflicts)
	for i := range ordinary {
		ordinary[i] = usageevidence.EvidenceConflict{
			Identity:     "ordinary-" + strconv.Itoa(i),
			ExistingHash: "existing-" + strconv.Itoa(i),
			IncomingHash: "incoming-" + strconv.Itoa(i),
		}
	}
	marker := usageevidence.EvidenceConflict{
		Identity:               "economic-retention",
		ExistingHash:           "baseline",
		IncomingHash:           "variant",
		IncomingCoverage:       usageevidence.EconomicEvidenceCoverageUnsupported,
		IncomingCoverageReason: economicEvidenceRetentionBoundReason,
	}
	local := make([]metering.Observation, usageevidence.MaxCallLegEvidenceConflicts)
	for i := range local {
		local[i] = r3EconomicObservation("r9-terminal-local-" + strconv.Itoa(i))
		local[i].Subject.SubmissionID = "wrong"
		local[i].Correlation.SubmissionID = "wrong"
	}

	leg := billingLegRecord(billingLegDraft{
		callID:       r3CallID,
		submissionID: "expected",
		aLegID:       "a-leg-r3",
		storeID:      "store-r3",
		bLegID:       "b-leg-r3",
		seq:          1,
		primary:      routing.Primary{Backend: "backend-r9", Model: "model-r9"},
		startedAt:    time.Unix(100, 0).UTC(),
		finishedAt:   time.Unix(101, 0).UTC(),
		command:      sdkterminal.CommandNormalFinish,
		outcome:      usageevidence.LegOutcomeWinner,
		surfaced:     usageevidence.SurfacedYes,

		localObservations: local,
		evidenceConflicts: ordinary,
		economicConflicts: []usageevidence.EvidenceConflict{marker},
	})
	if len(leg.EvidenceConflicts) > usageevidence.MaxCallLegEvidenceConflicts {
		t.Fatalf("terminal conflicts=%d exceed durable cap %d", len(leg.EvidenceConflicts), usageevidence.MaxCallLegEvidenceConflicts)
	}
	reserved, ordinaryVisible := 0, 0
	for _, conflict := range leg.EvidenceConflicts {
		if conflict.Identity == marker.Identity {
			reserved++
			continue
		}
		ordinaryVisible++
	}
	if reserved != 1 {
		t.Fatalf("reserved retention markers=%d, want exactly one: %+v", reserved, leg.EvidenceConflicts)
	}
	if ordinaryVisible == 0 {
		t.Fatal("reserved retention marker displaced every ordinary conflict")
	}

	sealed, err := leg.Seal()
	if err != nil {
		t.Fatalf("seal terminal record: %v", err)
	}
	sealedReserved := 0
	for _, conflict := range sealed.EvidenceConflicts {
		if conflict.Identity == marker.Identity {
			sealedReserved++
		}
	}
	if sealedReserved != 1 {
		t.Fatalf("sealed record retention markers=%d, want one durable marker", sealedReserved)
	}
}

// TestR9BoundedCoverageReasonOwnsItsBacking proves the bounded display reason is
// an owned copy: a 256-byte result must not retain the backing allocation of a
// 1MiB source reason.
func TestR9BoundedCoverageReasonOwnsItsBacking(t *testing.T) {
	original := strings.Repeat("r", 1<<20)
	bounded := boundEconomicEvidenceReason(original)
	if len(bounded) > maxEconomicEvidenceRetainedReasonBytes {
		t.Fatalf("bounded reason=%d bytes, want <= %d", len(bounded), maxEconomicEvidenceRetainedReasonBytes)
	}
	if bounded == "" {
		t.Fatal("bounded reason is empty")
	}
	if unsafe.StringData(original) == unsafe.StringData(bounded) {
		t.Fatal("bounded reason retains the original backing allocation")
	}
}

// TestR9DistinctCoverageReasonSuffixStaysVisible proves two partial coverage
// reasons that share the entire bounded display prefix but differ in their
// suffix do not collapse into an exact replay.
func TestR9DistinctCoverageReasonSuffixStaysVisible(t *testing.T) {
	attempt := &attemptSession{}
	base := r3EconomicObservation("obs-r9-reason-suffix")
	prefix := strings.Repeat("a", maxEconomicEvidenceRetainedReasonBytes)
	attempt.rememberEconomicEvidenceOnce(r3Evidence(base, "partial", prefix+"first"))
	attempt.rememberEconomicEvidenceOnce(r3Evidence(base, "partial", prefix+"second"))

	_, conflicts := attempt.economicEvidenceDrain()
	if len(conflicts) == 0 {
		t.Fatal("distinct coverage-reason suffixes collapsed into an exact replay")
	}
	for _, conflict := range conflicts {
		if conflict.IncomingCoverage != usageevidence.EconomicEvidenceCoveragePartial {
			t.Fatalf("conflict coverage = %q, want partial", conflict.IncomingCoverage)
		}
		if len(conflict.IncomingCoverageReason) > maxEconomicEvidenceRetainedReasonBytes {
			t.Fatalf("conflict reason=%d bytes, want <= %d", len(conflict.IncomingCoverageReason), maxEconomicEvidenceRetainedReasonBytes)
		}
	}
}

// TestR9OversizedCoverageReasonDegradesVisibly proves an over-cap coverage reason
// is explicitly degraded rather than silently equated: two reasons whose only
// difference lies outside the bounded head/tail hash sample still produce a
// visible conflict instead of disappearing as an exact replay.
func TestR9OversizedCoverageReasonDegradesVisibly(t *testing.T) {
	attempt := &attemptSession{}
	base := r3EconomicObservation("obs-r9-reason-oversized")
	long := strings.Repeat("m", 3*maxEconomicEvidenceReasonDigestBytes)
	// Mutate a byte strictly between the sampled head and tail so the bounded
	// digest preimage is byte-for-byte identical; only the explicit degraded
	// classification can keep the change visible.
	offset := maxEconomicEvidenceReasonDigestBytes + 16
	mutated := long[:offset] + "X" + long[offset+1:]
	if len(mutated) != len(long) {
		t.Fatal("fixture mutation changed the reason length")
	}
	attempt.rememberEconomicEvidenceOnce(r3Evidence(base, "partial", long))
	attempt.rememberEconomicEvidenceOnce(r3Evidence(base, "partial", mutated))

	_, conflicts := attempt.economicEvidenceDrain()
	if len(conflicts) == 0 {
		t.Fatal("over-cap coverage reason degraded without a visible conflict")
	}
	for _, conflict := range conflicts {
		if len(conflict.IncomingCoverageReason) > maxEconomicEvidenceRetainedReasonBytes {
			t.Fatalf("conflict reason=%d bytes, want <= %d", len(conflict.IncomingCoverageReason), maxEconomicEvidenceRetainedReasonBytes)
		}
	}
}

// TestR9TrustedRetentionMarkerSurvivesForgedSourceReasonSaturation is the
// permanent forgery regression. A runtime identity is forced into variant
// quarantine overflow so the runtime emits its own typed retention marker, while
// a second identity supplies an ordinary conflict whose free-text coverage
// reason is the literal reserved text. The trusted marker must win terminal
// priority and the forged reason must stay ordinary even when every ordinary
// origin is saturated to the durable cap.
func TestR9TrustedRetentionMarkerSurvivesForgedSourceReasonSaturation(t *testing.T) {
	attempt := &attemptSession{}

	trustedBase := r3EconomicObservation("obs-r9-trusted-marker")
	trustedIdentity := trustedBase.IdentityKey()
	attempt.rememberEconomicEvidenceOnce(r3Evidence(trustedBase, "complete", ""))
	for i := 0; i <= maxEconomicIdentityConflictVariants; i++ {
		variant := trustedBase.Clone()
		variant.Measures[0].Value.Coefficient = strconv.Itoa(i + 300)
		attempt.rememberEconomicEvidenceOnce(r3Evidence(variant, "complete", ""))
	}
	attempt.economicMu.Lock()
	record := attempt.economicIdentities[trustedIdentity]
	degraded := record != nil && record.degraded
	attempt.economicMu.Unlock()
	if !degraded {
		t.Fatal("fixture did not trigger the runtime retention-overflow path")
	}

	forgedBase := r3EconomicObservation("obs-r9-forged-reason")
	attempt.rememberEconomicEvidenceOnce(r3Evidence(forgedBase, "complete", ""))
	attempt.rememberEconomicEvidenceOnce(r3Evidence(forgedBase, "unsupported", economicEvidenceRetentionBoundReason))

	observations, ordinary, retention := attempt.economicEvidenceDrainPartitioned()
	if len(retention) != 1 {
		t.Fatalf("trusted retention markers=%d, want exactly one", len(retention))
	}
	if retention[0].Identity != trustedIdentity {
		t.Fatalf("trusted marker identity=%q, want %q", retention[0].Identity, trustedIdentity)
	}
	if len(ordinary) == 0 {
		t.Fatal("forged source reason did not stay in the ordinary conflict channel")
	}

	saturated := make([]usageevidence.EvidenceConflict, usageevidence.MaxCallLegEvidenceConflicts)
	for i := range saturated {
		saturated[i] = usageevidence.EvidenceConflict{Identity: "ordinary-" + strconv.Itoa(i), ExistingHash: "e", IncomingHash: "i"}
	}
	leg := billingLegRecord(billingLegDraft{
		callID: r3CallID, submissionID: "submission-r3", aLegID: "a-leg-r3", storeID: "store-r3",
		bLegID: "b-leg-r3", seq: 1,
		primary:   routing.Primary{Backend: "backend-r9", Model: "model-r9"},
		startedAt: time.Unix(100, 0).UTC(), finishedAt: time.Unix(101, 0).UTC(),
		command: sdkterminal.CommandNormalFinish, outcome: usageevidence.LegOutcomeWinner, surfaced: usageevidence.SurfacedYes,

		economicObservations:      observations,
		economicConflicts:         retention,
		economicOrdinaryConflicts: append(ordinary, saturated...),
	})
	if len(leg.EvidenceConflicts) > usageevidence.MaxCallLegEvidenceConflicts {
		t.Fatalf("terminal conflicts=%d exceed durable cap %d", len(leg.EvidenceConflicts), usageevidence.MaxCallLegEvidenceConflicts)
	}
	if len(leg.EvidenceConflicts) == 0 || leg.EvidenceConflicts[0].Identity != trustedIdentity {
		t.Fatalf("trusted retention marker not admitted first at terminal saturation: %+v", leg.EvidenceConflicts)
	}
}

// TestR9SourceReservedReasonWithoutTrustedMarkerGetsNoPriority proves the
// counterpart: a source-derived conflict carrying the exact reserved reason text
// receives no priority when no runtime retention marker exists. It must expire
// with the tail of a saturated ordinary origin exactly like any other ordinary
// conflict, so the reserved text alone can never forge terminal priority.
func TestR9SourceReservedReasonWithoutTrustedMarkerGetsNoPriority(t *testing.T) {
	forged := usageevidence.EvidenceConflict{
		Identity: "source-forged-reserved", ExistingHash: "baseline", IncomingHash: "variant",
		IncomingCoverage:       usageevidence.EconomicEvidenceCoverageUnsupported,
		IncomingCoverageReason: economicEvidenceRetentionBoundReason,
	}
	saturated := make([]usageevidence.EvidenceConflict, usageevidence.MaxCallLegEvidenceConflicts)
	for i := range saturated {
		saturated[i] = usageevidence.EvidenceConflict{Identity: "ordinary-" + strconv.Itoa(i), ExistingHash: "e", IncomingHash: "i"}
	}
	leg := billingLegRecord(billingLegDraft{
		callID: r3CallID, submissionID: "submission-r3", aLegID: "a-leg-r3", storeID: "store-r3",
		bLegID: "b-leg-r3", seq: 1,
		primary:   routing.Primary{Backend: "backend-r9", Model: "model-r9"},
		startedAt: time.Unix(100, 0).UTC(), finishedAt: time.Unix(101, 0).UTC(),
		command: sdkterminal.CommandNormalFinish, outcome: usageevidence.LegOutcomeWinner, surfaced: usageevidence.SurfacedYes,

		economicOrdinaryConflicts: append(saturated, forged),
	})
	if len(leg.EvidenceConflicts) != usageevidence.MaxCallLegEvidenceConflicts {
		t.Fatalf("terminal conflicts=%d, want the durable cap %d", len(leg.EvidenceConflicts), usageevidence.MaxCallLegEvidenceConflicts)
	}
	for _, conflict := range leg.EvidenceConflicts {
		if conflict.Identity == forged.Identity {
			t.Fatal("source-owned reserved reason received special priority without a trusted marker")
		}
	}
}
