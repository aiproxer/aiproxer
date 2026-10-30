package runtime

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// TestR9EconomicSameIdentityVariantsStayBounded feeds far more than the
// configured conflict bound of individually valid distinct payloads under one
// source identity through the real runtime admission seam. Retention must stay
// bounded to one canonical baseline plus a bounded quarantine of conflict refs,
// the degraded disposition must remain visible, and the conflicted prefix must
// stay unratable.
func TestR9EconomicSameIdentityVariantsStayBounded(t *testing.T) {
	attempt := &attemptSession{}
	pipeline := newResponsePipeline()

	base := r3EconomicObservation("obs-r9-baseline")
	identity := base.IdentityKey()
	if identity == "" {
		t.Fatal("fixture identity is empty")
	}
	observations := []metering.Observation{base}
	const variants = 12 * maxEconomicIdentityConflictVariants
	for i := 1; i <= variants; i++ {
		variant := base.Clone()
		variant.Measures[0].Value = &metering.Decimal{Coefficient: strconv.Itoa(i + 2)}
		if variant.IdentityKey() != identity {
			t.Fatalf("variant %d changed source identity", i)
		}
		if variant.Fingerprint() == base.Fingerprint() {
			t.Fatalf("variant %d is not a distinct payload", i)
		}
		observations = append(observations, variant)
	}

	stream := &phase7EconomicRuntimeStream{observations: observations}
	pipeline.consumeBackendUsageEvidenceForAttempt(context.Background(), recvTurnFacts{}, attempt, stream)

	attempt.economicMu.Lock()
	records := len(attempt.economicIdentities)
	fullEvidence := len(attempt.economicObservations)
	record := attempt.economicIdentities[identity]
	quarantine := 0
	degraded := false
	var retainedBytes int
	for _, evidence := range attempt.economicObservations {
		payload, err := evidence.Observation.CanonicalJSON()
		if err != nil {
			attempt.economicMu.Unlock()
			t.Fatalf("retained evidence canonicalization: %v", err)
		}
		retainedBytes += len(payload) + len(evidence.Coverage) + len(evidence.CoverageReason)
	}
	if record != nil {
		quarantine = len(record.quarantine)
		degraded = record.degraded
		for hash, variant := range record.quarantine {
			retainedBytes += len(hash) + len(variant.coverage) + len(variant.coverageReason)
		}
	}
	attempt.economicMu.Unlock()

	if records != 1 {
		t.Fatalf("economic identity records=%d, want one", records)
	}
	if fullEvidence != 1 {
		t.Fatalf("retained full observations=%d, want one canonical baseline", fullEvidence)
	}
	if quarantine > maxEconomicIdentityConflictVariants {
		t.Fatalf("same-identity quarantine refs=%d, want <= %d", quarantine, maxEconomicIdentityConflictVariants)
	}
	if !degraded {
		t.Fatalf("identity with %d variants was not marked degraded", variants)
	}
	if retainedBytes > metering.MaxSafeEvidenceBytes {
		t.Fatalf("same-identity retained bytes=%d, want <= one observation (%d)", retainedBytes, metering.MaxSafeEvidenceBytes)
	}

	economic, conflicts := attempt.economicEvidenceDrain()
	if len(economic) != 1 {
		t.Fatalf("drained economic observations=%d, want one baseline", len(economic))
	}
	if len(conflicts) == 0 || len(conflicts) > usageevidence.MaxCallLegEvidenceConflicts {
		t.Fatalf("drained conflicts=%d, want 1..%d", len(conflicts), usageevidence.MaxCallLegEvidenceConflicts)
	}
	degradedVisible := false
	for _, conflict := range conflicts {
		if conflict.IncomingCoverage == usageevidence.EconomicEvidenceCoverageUnsupported &&
			conflict.IncomingCoverageReason == economicEvidenceRetentionBoundReason {
			degradedVisible = true
		}
	}
	if !degradedVisible {
		t.Fatalf("sticky degraded disposition not visible in conflicts: %+v", conflicts)
	}
}

// TestR9EconomicSameIdentityOverflowWorkIsBounded proves that once the variant
// quarantine cap is reached, retention and conflict work stay constant: feeding
// many more distinct variants must not grow the quarantined ref set, the retained
// observation set, or the conflict set. This is a deterministic bounded-state
// probe, not a timing measurement.
func TestR9EconomicSameIdentityOverflowWorkIsBounded(t *testing.T) {
	attempt := &attemptSession{}
	base := r3EconomicObservation("obs-r9-work")
	identity := base.IdentityKey()
	remember := func(observation metering.Observation) {
		attempt.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{Observation: observation, Coverage: "complete"})
	}
	feed := func(from, to int) {
		for i := from; i < to; i++ {
			variant := base.Clone()
			variant.Measures[0].Value = &metering.Decimal{Coefficient: strconv.Itoa(i + 2)}
			remember(variant)
		}
	}

	remember(base)
	// One past the cap so the identity is guaranteed degraded.
	feed(1, maxEconomicIdentityConflictVariants+2)

	attempt.economicMu.Lock()
	snapshot := func() (int, int, int, bool) {
		record := attempt.economicIdentities[identity]
		return len(record.quarantine), len(attempt.economicObservations), len(attempt.economicConflicts), record.degraded
	}
	afterQuarantine, afterObservations, afterConflicts, afterDegraded := snapshot()
	attempt.economicMu.Unlock()
	if afterQuarantine != maxEconomicIdentityConflictVariants || !afterDegraded {
		t.Fatalf("post-cap quarantine=%d degraded=%v, want %d/true", afterQuarantine, afterDegraded, maxEconomicIdentityConflictVariants)
	}
	if afterObservations != 1 {
		t.Fatalf("post-cap observations=%d, want one baseline", afterObservations)
	}

	feed(maxEconomicIdentityConflictVariants+2, 40*maxEconomicIdentityConflictVariants)

	attempt.economicMu.Lock()
	finalQuarantine, finalObservations, finalConflicts, finalDegraded := snapshot()
	attempt.economicMu.Unlock()
	if finalQuarantine != afterQuarantine {
		t.Fatalf("quarantine grew after cap: %d -> %d", afterQuarantine, finalQuarantine)
	}
	if finalObservations != afterObservations {
		t.Fatalf("retained observations grew after cap: %d -> %d", afterObservations, finalObservations)
	}
	if finalConflicts != afterConflicts {
		t.Fatalf("conflict retention grew after cap: %d -> %d", afterConflicts, finalConflicts)
	}
	if !finalDegraded {
		t.Fatal("degraded disposition is not sticky")
	}
}

// TestR9EconomicExactReplayAndCoverageSemanticsPreserved proves R9 bounding does
// not regress the approved R3 replay semantics: exact semantic replays stay
// no-ops, a changed payload stays a visible conflict, and a changed coverage
// disposition stays visible.
func TestR9EconomicExactReplayAndCoverageSemanticsPreserved(t *testing.T) {
	attempt := &attemptSession{}
	base := r3EconomicObservation("obs-r9-replay")
	attempt.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{Observation: base, Coverage: "complete"})
	// Replay after a conflict: the exact baseline replay must not add conflicts.
	changed := base.Clone()
	changed.Measures[0].Value = &metering.Decimal{Coefficient: "9"}
	attempt.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{Observation: changed, Coverage: "complete"})
	attempt.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{Observation: base, Coverage: "complete"})
	attempt.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{Observation: changed, Coverage: "complete"})

	economic, conflicts := attempt.economicEvidenceDrain()
	if len(economic) != 1 || len(conflicts) != 1 {
		t.Fatalf("replay/conflict drain observations=%d conflicts=%d, want one and one", len(economic), len(conflicts))
	}
}

// TestR9EconomicStickyMarkerSurvivesFullConflictCapacity is the permanent
// RED/GREEN terminal regression: once the ordinary conflict set is saturated by
// distinct coverage-disposition conflicts, the reserved retention-overflow
// disposition must still reach the terminal drain, and it must displace at most
// the tail of the ordinary conflicts, never all of them. (The downstream retail
// refusal over the saturated prefix is Enterprise; see the file header.)
func TestR9EconomicStickyMarkerSurvivesFullConflictCapacity(t *testing.T) {
	attempt := &attemptSession{}
	base := r3EconomicObservation("obs-r9-saturated-cap")
	identity := base.IdentityKey()
	attempt.rememberEconomicEvidenceOnce(r3Evidence(base, "complete", ""))
	for i := range usageevidence.MaxCallLegEvidenceConflicts {
		attempt.rememberEconomicEvidenceOnce(r3Evidence(base, "unsupported", "reason-"+strconv.Itoa(i)))
	}
	for i := 0; i <= maxEconomicIdentityConflictVariants; i++ {
		variant := base.Clone()
		variant.Measures[0].Value.Coefficient = strconv.Itoa(i + 100)
		attempt.rememberEconomicEvidenceOnce(r3Evidence(variant, "complete", ""))
	}

	attempt.economicMu.Lock()
	degraded := attempt.economicIdentities[identity].degraded
	attempt.economicMu.Unlock()
	if !degraded {
		t.Fatal("identity with overflowed variant quarantine was not marked degraded")
	}

	economic, conflicts := attempt.economicEvidenceDrain()
	if len(conflicts) == 0 || len(conflicts) > usageevidence.MaxCallLegEvidenceConflicts {
		t.Fatalf("drained conflicts=%d, want 1..%d", len(conflicts), usageevidence.MaxCallLegEvidenceConflicts)
	}
	retentionMarker := false
	ordinaryRetained := false
	for _, conflict := range conflicts {
		if conflict.IncomingCoverage == usageevidence.EconomicEvidenceCoverageUnsupported &&
			conflict.IncomingCoverageReason == economicEvidenceRetentionBoundReason {
			retentionMarker = true
			continue
		}
		ordinaryRetained = true
	}
	if !retentionMarker {
		t.Fatalf("sticky retention-bound marker displaced at saturated conflict capacity: %+v", conflicts)
	}
	if !ordinaryRetained {
		t.Fatal("reserved retention marker displaced all ordinary conflicts")
	}
	if len(economic) != 1 {
		t.Fatalf("drained economic observations=%d, want one canonical baseline", len(economic))
	}
}

// TestR9EconomicRetainedCoverageMetadataIsByteBounded proves that coverage
// diagnostic metadata retained for one identity is bounded independently of the
// 64KiB canonical observation envelope. Adversarial maximal-length coverage
// reasons must not grow retained per-identity metadata past the exact bound,
// while the coverage classification and its required diagnostic reason stay
// visible.
func TestR9EconomicRetainedCoverageMetadataIsByteBounded(t *testing.T) {
	attempt := &attemptSession{}
	base := r3EconomicObservation("obs-r9-metadata-bound")
	identity := base.IdentityKey()
	oversized := strings.Repeat("r", metering.MaxSafeEvidenceFieldBytes)
	if len(oversized) <= maxEconomicEvidenceRetainedReasonBytes {
		t.Fatalf("fixture reason=%d bytes must exceed the bound %d", len(oversized), maxEconomicEvidenceRetainedReasonBytes)
	}

	attempt.rememberEconomicEvidenceOnce(r3Evidence(base, "unsupported", oversized))
	for i := 0; i <= maxEconomicIdentityConflictVariants; i++ {
		variant := base.Clone()
		variant.Measures[0].Value.Coefficient = strconv.Itoa(i + 100)
		attempt.rememberEconomicEvidenceOnce(r3Evidence(variant, "unsupported", oversized+strconv.Itoa(i)))
	}

	attempt.economicMu.Lock()
	record := attempt.economicIdentities[identity]
	if record == nil {
		attempt.economicMu.Unlock()
		t.Fatal("identity record missing")
	}
	metadataBytes := len(record.baselineHash) + len(record.baselineCoverageReason) + len(record.baselineReasonDigest)
	for hash, variant := range record.quarantine {
		metadataBytes += len(hash) + len(variant.coverage) + len(variant.coverageReason) + len(variant.reasonDigest)
		if variant.coverageReason == "" {
			attempt.economicMu.Unlock()
			t.Fatalf("quarantined coverage ref lost its required diagnostic reason: %+v", variant)
		}
	}
	baselineReason := record.baselineCoverageReason
	quarantine := len(record.quarantine)
	degraded := record.degraded
	attempt.economicMu.Unlock()

	if metadataBytes > maxEconomicIdentityRetainedMetadataBytes {
		t.Fatalf("per-identity retained coverage metadata=%d bytes, want <= %d", metadataBytes, maxEconomicIdentityRetainedMetadataBytes)
	}
	if len(baselineReason) > maxEconomicEvidenceRetainedReasonBytes {
		t.Fatalf("retained baseline coverage reason=%d bytes, want <= %d", len(baselineReason), maxEconomicEvidenceRetainedReasonBytes)
	}
	if !degraded || quarantine != maxEconomicIdentityConflictVariants {
		t.Fatalf("oversized reason variants must stay quarantined and degraded: degraded=%v quarantine=%d", degraded, quarantine)
	}

	economic, conflicts := attempt.economicEvidenceDrain()
	if len(economic) != 1 {
		t.Fatalf("drained economic observations=%d, want one canonical baseline", len(economic))
	}
	for _, evidence := range economic {
		if len(evidence.CoverageReason) > maxEconomicEvidenceRetainedReasonBytes {
			t.Fatalf("retained evidence coverage reason=%d bytes, want <= %d", len(evidence.CoverageReason), maxEconomicEvidenceRetainedReasonBytes)
		}
	}
	if len(conflicts) > usageevidence.MaxCallLegEvidenceConflicts {
		t.Fatalf("drained conflicts=%d, want <= %d", len(conflicts), usageevidence.MaxCallLegEvidenceConflicts)
	}
	classificationVisible := false
	for _, conflict := range conflicts {
		if conflict.IncomingCoverage != usageevidence.EconomicEvidenceCoverageUnsupported {
			continue
		}
		classificationVisible = true
		if conflict.IncomingCoverageReason == "" {
			t.Fatalf("coverage conflict lost its required diagnostic reason: %+v", conflict)
		}
		if len(conflict.IncomingCoverageReason) > maxEconomicEvidenceRetainedReasonBytes {
			t.Fatalf("conflict coverage reason=%d bytes, want <= %d", len(conflict.IncomingCoverageReason), maxEconomicEvidenceRetainedReasonBytes)
		}
	}
	if !classificationVisible {
		t.Fatalf("oversized coverage evidence lost its conflict classification: %+v", conflicts)
	}
}
