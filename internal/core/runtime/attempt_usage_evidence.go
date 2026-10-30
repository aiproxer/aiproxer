package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

const (
	maxAttemptAccumulatedUsage    = 1024
	maxAttemptUsageDedupeKeyBytes = 4096
)

// evidenceCaptureLossCause is the finite, closed set of causes for an
// observation that a destructively drained source surrendered and admission
// then rejected. The zero value means no loss. No source-controlled value can
// select a cause.
type evidenceCaptureLossCause uint8

const (
	evidenceCaptureLossNone evidenceCaptureLossCause = iota
	evidenceCaptureLossObservationCap
	evidenceCaptureLossCheckpointCapacity
	evidenceCaptureLossInvalidEvidence
	// evidenceCaptureLossConnectorClaimRejected is the typed degraded
	// disposition for a connector claim the host could not admit. The claim was
	// never retained as a host observation, so the canonical host-evidence byte
	// total of such a loss is zero.
	evidenceCaptureLossConnectorClaimRejected
)

// reason returns a bounded printable diagnostic for one cause.
func (c evidenceCaptureLossCause) reason() string {
	switch c {
	case evidenceCaptureLossObservationCap:
		return "observation capacity"
	case evidenceCaptureLossCheckpointCapacity:
		return "checkpoint capacity"
	case evidenceCaptureLossInvalidEvidence:
		return "invalid evidence"
	case evidenceCaptureLossConnectorClaimRejected:
		return "connector evidence rejected"
	default:
		return "unknown"
	}
}

// evidenceCaptureLossCauseSet is a bounded bitmask over the closed cause set, so
// retained loss state never grows with the number of distinct causes observed.
type evidenceCaptureLossCauseSet uint8

func (s evidenceCaptureLossCauseSet) with(cause evidenceCaptureLossCause) evidenceCaptureLossCauseSet {
	if cause == evidenceCaptureLossNone {
		return s
	}
	return s | 1<<(cause-1)
}

func (s evidenceCaptureLossCauseSet) has(cause evidenceCaptureLossCause) bool {
	return cause != evidenceCaptureLossNone && s&(1<<(cause-1)) != 0
}

// maxEvidenceCaptureLossIdentityBytes bounds the retained first-loss identity so
// adversarial source keys cannot grow sticky loss state.
const maxEvidenceCaptureLossIdentityBytes = 256

const (
	evidenceCaptureLossConflictIdentityPrefix = "runtime:capture-loss:"
	evidenceCaptureLossConflictReasonPrefix   = "economic evidence capture loss: "
	evidenceCaptureLossExistingHash           = "capture-loss-baseline"
	evidenceCaptureLossIncomingHash           = "capture-loss-rejected"
)

// evidenceCaptureLoss is the sticky, attempt-owned record of observations that
// were destructively drained and then rejected by admission. It is bounded by a
// count, a byte total, and the closed cause set, and retains only the first
// loss's bounded identity and cause. It is deliberately never cleared by a
// later successful flush or by terminal accumulator resets, so a terminal owner
// can always see that the accepted prefix is known-truncated.
type evidenceCaptureLoss struct {
	present       bool
	count         uint64
	bytes         uint64
	causes        evidenceCaptureLossCauseSet
	firstIdentity string
	firstCause    evidenceCaptureLossCause
}

// economicEvidenceAdmissionDisposition is the explicit, closed classification of
// one native economic observation admission attempt.
type economicEvidenceAdmissionDisposition uint8

const (
	economicEvidenceAdmissionIgnored economicEvidenceAdmissionDisposition = iota
	economicEvidenceAdmissionRetained
	economicEvidenceAdmissionReplay
	economicEvidenceAdmissionConflict
	economicEvidenceAdmissionRejected
)

// economicEvidenceAdmission is the explicit outcome of
// rememberEconomicEvidenceOnce. cause is set only when the disposition is
// rejected and the observation became an irreversible capture loss.
type economicEvidenceAdmission struct {
	disposition economicEvidenceAdmissionDisposition
	cause       evidenceCaptureLossCause
}

func boundEvidenceCaptureLossIdentity(identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return ""
	}
	if len(identity) <= maxEvidenceCaptureLossIdentityBytes {
		return strings.Clone(identity)
	}
	limit := maxEvidenceCaptureLossIdentityBytes
	for limit > 0 && !utf8.ValidString(identity[:limit]) {
		limit--
	}
	return strings.Clone(identity[:limit])
}

func economicEvidenceCaptureLossBytes(canonical metering.Observation) int {
	payload, err := canonical.CanonicalJSON()
	if err != nil {
		return 0
	}
	return len(payload)
}

// recordEconomicCaptureLoss retains sticky bounded loss state for one
// destructively drained observation rejected by admission.
func (a *attemptSession) recordEconomicCaptureLoss(cause evidenceCaptureLossCause, identity string, size int) {
	if a == nil {
		return
	}
	a.economicMu.Lock()
	defer a.economicMu.Unlock()
	a.recordEconomicCaptureLossLocked(cause, identity, size)
}

// recordEconomicCaptureLossLocked retains sticky bounded loss state. Callers
// hold economicMu. Only the first loss's identity and cause are retained; later
// losses extend the bounded count/byte totals and the closed cause set.
func (a *attemptSession) recordEconomicCaptureLossLocked(cause evidenceCaptureLossCause, identity string, size int) {
	if a == nil || cause == evidenceCaptureLossNone {
		return
	}
	loss := &a.evidenceCaptureLoss
	loss.present = true
	loss.count++
	if size > 0 {
		loss.bytes += uint64(size)
	}
	loss.causes = loss.causes.with(cause)
	if loss.firstCause == evidenceCaptureLossNone {
		loss.firstCause = cause
		loss.firstIdentity = boundEvidenceCaptureLossIdentity(identity)
	}
}

// evidenceCaptureLossSnapshot returns a copy of the sticky capture-loss state
// without clearing it, so every terminal owner observes the same loss.
func (a *attemptSession) evidenceCaptureLossSnapshot() evidenceCaptureLoss {
	if a == nil {
		return evidenceCaptureLoss{}
	}
	a.economicMu.Lock()
	defer a.economicMu.Unlock()
	return a.evidenceCaptureLoss
}

// logConnectorEvidenceRejection emits the structured, bounded operator event
// for one rejected connector claim. It carries only the closed reason token,
// the rejection count, the canonical host-evidence byte total and the host
// lineage the runtime already holds. No connector text, source hash, payload
// or raw length is ever carried, and the byte total is never labelled as the
// connector claim's lost bytes.
func logConnectorEvidenceRejection(ctx context.Context, log *slog.Logger, attempt *attemptSession, reason string) {
	if log == nil || attempt == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return
	}
	loss := attempt.evidenceCaptureLossSnapshot()
	log.WarnContext(ctx, "connector economic evidence rejected",
		"reason", reason,
		"rejection_count", loss.count,
		"host_evidence_bytes", loss.bytes,
		"store_id", attempt.billingStoreID,
		"billing_call_id", string(attempt.billingCallID),
		"b_leg_id", attempt.bleg.BLegID,
		"attempt_seq", attempt.bleg.Seq,
	)
}

// captureLossEvidenceConflicts projects the sticky loss state into the
// runtime-owned reserved conflict channel. The marker can never be forged from
// source-controlled coverage or free-text fields because it is derived only
// from attempt-owned loss state.
func captureLossEvidenceConflicts(loss evidenceCaptureLoss) []usageevidence.EvidenceConflict {
	if !loss.present {
		return nil
	}
	return []usageevidence.EvidenceConflict{{
		Identity:               evidenceCaptureLossConflictIdentityPrefix + loss.firstIdentity,
		ExistingHash:           evidenceCaptureLossExistingHash,
		IncomingHash:           evidenceCaptureLossIncomingHash,
		IncomingCoverage:       usageevidence.EconomicEvidenceCoverageUnsupported,
		IncomingCoverageReason: evidenceCaptureLossConflictReasonPrefix + loss.firstCause.reason(),
	}}
}

func (a *attemptSession) rememberUsageEvidenceOnce(ev aipapi.Event) bool {
	return a.rememberUsageEvidenceOnceAs(ev, billingEvidenceRoleStream)
}

// rememberUsageEvidenceOnceAs owns the bounded per-attempt evidence set. The
// legacy internalUsageKeys map remains keyed by DedupeKey for compatibility;
// usageEvidence retains the event and acquisition role so terminal capture can
// preserve source separation. A changed payload under one source key is
// retained as a visible conflict, never merged into a later B-leg.
func (a *attemptSession) rememberUsageEvidenceOnceAs(ev aipapi.Event, role string) bool {
	if a == nil {
		return false
	}
	if len(ev.Accounting.DedupeKey) > maxAttemptUsageDedupeKeyBytes {
		return false
	}
	key := strings.TrimSpace(ev.Accounting.DedupeKey)
	if key == "" {
		return false
	}
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	if a.internalUsageKeys == nil {
		a.internalUsageKeys = make(map[string]struct{})
	}
	if a.usageEvidence == nil {
		a.usageEvidence = make(map[string]capturedBillingEvidence)
	}
	if prior, exists := a.usageEvidence[key]; exists {
		if eventEvidenceFingerprint(prior.event) != eventEvidenceFingerprint(ev) {
			a.usageConflicts = appendBoundedEvidenceConflict(a.usageConflicts, usageevidence.EvidenceConflict{
				Identity:     key + "\x00revision:1",
				ExistingHash: eventEvidenceFingerprint(prior.event),
				IncomingHash: eventEvidenceFingerprint(ev),
			})
		}
		return false
	}
	if len(a.internalUsageKeys) >= maxAttemptAccumulatedUsage {
		return false
	}
	owned := projectBillingEvidenceEvent(ev)
	a.internalUsageKeys[key] = struct{}{}
	a.accumulatedUsage = append(a.accumulatedUsage, owned)
	a.usageEvidence[key] = capturedBillingEvidence{event: owned, role: strings.TrimSpace(role)}
	a.usageEvidenceOrder = append(a.usageEvidenceOrder, key)
	return true
}

func (a *attemptSession) recordUsageEvidence(ev aipapi.Event) {
	if a == nil || ev.Kind == "" {
		return
	}
	if !a.rememberUsageEvidenceOnceAs(ev, billingEvidenceRoleSideband) {
		return
	}
	a.observeAccountingUsage(ev)
}

// billingEvidenceSnapshot returns caller-owned source-tagged evidence and
// drains the attempt accumulator. Draining is terminal-owner scoped: callers
// must invoke it only after all backend sideband drain passes have completed.
func (a *attemptSession) billingEvidenceSnapshot() []capturedBillingEvidence {
	out, _ := a.billingEvidenceDrain()
	return out
}

// billingEvidenceDrain returns source-tagged evidence and conflicts while
// clearing every attempt-owned accumulator. A second terminal callback sees
// no state from this B-leg and therefore cannot bind stale evidence to a
// replacement B-leg.
func (a *attemptSession) billingEvidenceDrain() ([]capturedBillingEvidence, []usageevidence.EvidenceConflict) {
	if a == nil {
		return nil, nil
	}
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	out := make([]capturedBillingEvidence, 0, len(a.usageEvidenceOrder))
	for _, key := range a.usageEvidenceOrder {
		if evidence, ok := a.usageEvidence[key]; ok {
			out = append(out, capturedBillingEvidence{event: projectBillingEvidenceEvent(evidence.event), role: evidence.role})
		}
	}
	conflicts := append([]usageevidence.EvidenceConflict(nil), a.usageConflicts...)
	a.internalUsageKeys = nil
	a.accumulatedUsage = nil
	a.usageEvidence = nil
	a.usageEvidenceOrder = nil
	a.usageConflicts = nil
	return out, conflicts
}

func (a *attemptSession) billingEvidenceConflictsSnapshot() []usageevidence.EvidenceConflict {
	if a == nil {
		return nil
	}
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	return append([]usageevidence.EvidenceConflict(nil), a.usageConflicts...)
}

// maxEconomicIdentityConflictVariants bounds how many distinct changed payloads
// one source identity quarantines as conflict refs. The first accepted payload
// is the deterministic canonical baseline observation; later distinct payloads
// retain only bounded hash/coverage metadata and never become terminal
// observations. Once the cap is reached the identity is marked degraded and
// every further variant keeps the same sticky conflict without extending
// retention or rescanning retained history.
const maxEconomicIdentityConflictVariants = 8

// economicEvidenceRetentionBoundReason labels the sticky degraded conflict
// emitted once one identity's variant quarantine bound is exceeded. The
// disposition is deliberately fail-closed: the retained baseline and the
// quarantined refs stay visible so retail rating refuses the whole B-leg.
const economicEvidenceRetentionBoundReason = "economic evidence variant retention bound exceeded"

// economicEvidenceHashBytes is the fixed hex-SHA-256 length of a retained
// observation evidence hash.
const economicEvidenceHashBytes = 64

// maxEconomicEvidenceRetainedReasonBytes bounds one retained coverage reason.
// Coverage classification is carried by EconomicEvidenceCoverage, so bounding
// the free-text reason cannot change complete/partial/unsupported
// classification and cannot empty a non-empty reason that an incomplete
// disposition requires. It keeps per-identity and aggregate diagnostic
// retention independent of the canonical observation's 64KiB envelope.
const maxEconomicEvidenceRetainedReasonBytes = 256

// economicEvidenceReasonTruncationMarker makes display truncation explicit in
// the durable record instead of presenting an over-long reason as a normal
// prefix. It is printable ASCII so it passes coverage-reason validation.
const economicEvidenceReasonTruncationMarker = "~[truncated]"

// maxEconomicEvidenceReasonDigestBytes bounds the raw coverage-reason bytes fed
// into the per-event reason digest, so an adversarial multi-megabyte reason
// cannot force unbounded hashing. A reason at or below the cap is hashed in
// full; a longer reason is explicitly degraded and sampled by head, exact
// length, and tail, so distinct reasons sharing a long common prefix do not
// silently collapse.
const maxEconomicEvidenceReasonDigestBytes = 4096

// economicEvidenceReasonDigestBytes is the fixed hex-SHA-256 length of one
// retained coverage-reason digest.
const economicEvidenceReasonDigestBytes = economicEvidenceHashBytes

// maxEconomicRetentionConflicts reserves bounded conflict capacity for the
// retention-overflow disposition, independent of the ordinary conflict cap.
// Evidence retention loss must stay visible even when ordinary conflicts are
// full. Capacity is derived from the closed retention-overflow disposition set
// (exactly one disposition), so a future disposition cannot silently overflow.
const maxEconomicRetentionConflicts = 1

// maxEconomicIdentityRetainedMetadataBytes bounds the coverage diagnostic
// metadata retained for one identity: one canonical baseline hash, its
// full-reason digest and bounded display reason, plus the bounded quarantine
// refs (each with its own hash, digest, coverage, and display reason). It is
// independent of the canonical observation's 64KiB envelope, so a baseline
// count of one plus at most maxEconomicIdentityConflictVariants refs is not by
// itself a total-byte proof.
const maxEconomicIdentityRetainedMetadataBytes = economicEvidenceHashBytes + economicEvidenceReasonDigestBytes + maxEconomicEvidenceRetainedReasonBytes +
	maxEconomicIdentityConflictVariants*(economicEvidenceHashBytes+economicEvidenceReasonDigestBytes+len(usageevidence.EconomicEvidenceCoverageUnsupported)+maxEconomicEvidenceRetainedReasonBytes)

// retentionEvidenceConflict is a runtime-owned reserved retention-overflow
// disposition. It is produced only by the internal variant-quarantine overflow
// path in rememberEconomicEvidenceOnce and carried through the attempt and the
// terminal handoff in its own typed channel. Keeping it separate from every
// provider Observation/EvidenceConflict makes the terminal reserved-priority
// decision a function of provenance rather than of forgeable coverage fields or
// free-text coverage reason: no source-controlled value can populate or
// reconstruct this channel.
type retentionEvidenceConflict struct {
	conflict usageevidence.EvidenceConflict
}

// economicVariantQuarantine is the bounded conflict metadata retained for one
// distinct changed payload under a source identity. The full observation is
// deliberately discarded: a changed payload is diagnostic evidence, never a
// second economic observation.
type economicVariantQuarantine struct {
	coverage       usageevidence.EconomicEvidenceCoverage
	coverageReason string
	reasonDigest   string
	reasonDegraded bool
}

// economicIdentityRecord is the bounded per-identity economic evidence state:
// exactly one canonical baseline observation plus a bounded quarantine of
// changed-payload refs. It replaces an unbounded hash -> full-evidence map so a
// single adversarial identity cannot grow memory or make conflict baseline
// selection quadratic in the number of previously rejected variants.
type economicIdentityRecord struct {
	baselineHash           string
	baselineCoverage       usageevidence.EconomicEvidenceCoverage
	baselineCoverageReason string
	baselineReasonDigest   string
	baselineReasonDegraded bool
	quarantine             map[string]economicVariantQuarantine
	degraded               bool
}

// rememberEconomicEvidenceOnce owns validated connector V2 observations until
// the B-leg terminal owner builds its durable record. Semantic source replays
// (same identity/revision and payload, ignoring receipt time and approved
// Subject/Correlation carrier placement) are ignored; a changed payload under
// the same identity remains a bounded visible conflict, and a coverage
// disposition change is reported separately without a second economic event.
// Coverage metadata remains attached until the terminal handoff.
//
// Retention is bounded per source identity. The first accepted payload becomes
// the deterministic canonical baseline; later distinct payloads are quarantined
// as hash/coverage refs up to maxEconomicIdentityConflictVariants. Once that cap
// is reached the identity is degraded and every further variant maps to one
// sticky conflict, so per-event work stays O(1) in the number of previously
// rejected variants instead of rescanning an unbounded hash map.
func (a *attemptSession) rememberEconomicEvidenceOnce(evidence execbackend.EconomicEvidence) economicEvidenceAdmission {
	if a == nil {
		return economicEvidenceAdmission{disposition: economicEvidenceAdmissionIgnored}
	}
	if reason := strings.TrimSpace(evidence.RejectionReason); reason != "" {
		// A rejected connector claim carries no observation and no
		// disposition: a disposition requires a retained observation. The
		// explicit capture loss is the only durable trace of the refusal.
		a.recordEconomicCaptureLoss(evidenceCaptureLossConnectorClaimRejected, "", 0)
		return economicEvidenceAdmission{
			disposition: economicEvidenceAdmissionRejected,
			cause:       evidenceCaptureLossConnectorClaimRejected,
		}
	}
	canonical, err := evidence.Observation.Canonical()
	if err != nil {
		a.recordEconomicCaptureLoss(evidenceCaptureLossInvalidEvidence, evidence.Observation.SourceEventKey, 0)
		return economicEvidenceAdmission{disposition: economicEvidenceAdmissionRejected, cause: evidenceCaptureLossInvalidEvidence}
	}
	if evidence.Coverage == "" {
		evidence.Coverage = "complete"
	}
	evidence.Observation = canonical
	identity := canonical.IdentityKey()
	// Replay identity is the shared semantic preimage: receipt arrival time and
	// approved Subject/Correlation carrier placement are normalized, so an
	// equivalent redelivery is the same effective economic event. Coverage is
	// compared separately because a genuine disposition change stays visible.
	hash := usageevidence.ObservationEvidenceHash(canonical)
	if identity == "" || hash == "" {
		a.recordEconomicCaptureLoss(evidenceCaptureLossInvalidEvidence, canonical.SourceEventKey, 0)
		return economicEvidenceAdmission{disposition: economicEvidenceAdmissionRejected, cause: evidenceCaptureLossInvalidEvidence}
	}
	coverage := usageevidence.EconomicEvidenceCoverage(evidence.Coverage)
	// Bound the free-text reason before it is cloned into the identity record,
	// the retained observation, or any conflict, so neither per-identity nor
	// aggregate diagnostic retention can grow past the byte bound. The coverage
	// classification (complete/partial/unsupported) is carried separately and
	// is never changed by this normalization. A full-reason digest is retained
	// alongside the bounded display text so two distinct reasons that share the
	// display prefix cannot collapse into an exact replay.
	rawReason := strings.TrimSpace(evidence.CoverageReason)
	reason := boundEconomicEvidenceReason(rawReason)
	reasonDigest, reasonDegraded := economicEvidenceReasonFingerprint(rawReason)
	evidence.CoverageReason = reason

	a.economicMu.Lock()
	defer a.economicMu.Unlock()
	// Keep the record allocation below the queue admission. A checkpoint
	// rejection must leave no dedupe marker behind, so a later replay can
	// retry after bounded capacity is released.
	record := a.economicIdentities[identity]
	if record == nil {
		if len(a.economicObservations) >= usageevidence.MaxCallLegEvidenceObservations {
			a.recordEconomicCaptureLossLocked(evidenceCaptureLossObservationCap, canonical.SourceEventKey, economicEvidenceCaptureLossBytes(canonical))
			return economicEvidenceAdmission{disposition: economicEvidenceAdmissionRejected, cause: evidenceCaptureLossObservationCap}
		}
		// Queue admission precedes the terminal evidence dedupe marker. If the
		// bounded checkpoint state is exhausted, this observation remains
		// retryable rather than being irreversibly accepted and suppressed on
		// replay. A nil sink reports Ignored, preserving the legacy terminal-only
		// path.
		if a.queueEconomicCheckpoint(canonical) == economicCheckpointRejected {
			a.recordEconomicCaptureLossLocked(evidenceCaptureLossCheckpointCapacity, canonical.SourceEventKey, economicEvidenceCaptureLossBytes(canonical))
			return economicEvidenceAdmission{disposition: economicEvidenceAdmissionRejected, cause: evidenceCaptureLossCheckpointCapacity}
		}
		if a.economicIdentities == nil {
			a.economicIdentities = make(map[string]*economicIdentityRecord)
		}
		a.economicIdentities[identity] = &economicIdentityRecord{
			baselineHash:           hash,
			baselineCoverage:       coverage,
			baselineCoverageReason: reason,
			baselineReasonDigest:   reasonDigest,
			baselineReasonDegraded: reasonDegraded,
		}
		a.economicObservations = append(a.economicObservations, evidence)
		// Keep the durable checkpoint queue separate from terminal billing
		// evidence. The queue only persists immutable V2 observations; it never
		// invokes valuation, authority, or money mutation paths.
		return economicEvidenceAdmission{disposition: economicEvidenceAdmissionRetained}
	}
	// An exact semantic replay is a no-op. A changed coverage disposition, a
	// changed full-reason digest, or an explicitly degraded (over-cap) reason
	// for the retained baseline stays a visible conflict, so two reasons we
	// could not fully compare are never silently equated.
	if hash == record.baselineHash {
		if record.baselineCoverage != coverage || record.baselineCoverageReason != reason ||
			record.baselineReasonDigest != reasonDigest || record.baselineReasonDegraded || reasonDegraded {
			a.economicConflicts = appendBoundedEvidenceConflict(a.economicConflicts, usageevidence.EvidenceConflict{
				Identity:               identity,
				ExistingHash:           hash,
				IncomingHash:           hash,
				ExistingCoverage:       record.baselineCoverage,
				ExistingCoverageReason: record.baselineCoverageReason,
				IncomingCoverage:       coverage,
				IncomingCoverageReason: reason,
			})
			return economicEvidenceAdmission{disposition: economicEvidenceAdmissionConflict}
		}
		return economicEvidenceAdmission{disposition: economicEvidenceAdmissionReplay}
	}
	if variant, exists := record.quarantine[hash]; exists {
		// A repeated quarantined variant is a no-op unless its coverage
		// disposition, full-reason digest, or degraded classification changed,
		// which stays a visible conflict.
		if variant.coverage != coverage || variant.coverageReason != reason ||
			variant.reasonDigest != reasonDigest || variant.reasonDegraded || reasonDegraded {
			a.economicConflicts = appendBoundedEvidenceConflict(a.economicConflicts, usageevidence.EvidenceConflict{
				Identity:               identity,
				ExistingHash:           hash,
				IncomingHash:           hash,
				ExistingCoverage:       variant.coverage,
				ExistingCoverageReason: variant.coverageReason,
				IncomingCoverage:       coverage,
				IncomingCoverageReason: reason,
			})
			return economicEvidenceAdmission{disposition: economicEvidenceAdmissionConflict}
		}
		return economicEvidenceAdmission{disposition: economicEvidenceAdmissionReplay}
	}
	// A genuinely distinct payload under the same identity is a visible
	// conflict. Retention stops at the cap; the identity then carries one
	// sticky degraded conflict so the disposition stays fail-closed without
	// unbounded work.
	if record.degraded || len(record.quarantine) >= maxEconomicIdentityConflictVariants {
		if !record.degraded {
			record.degraded = true
			a.economicRetentionConflicts = appendRetentionEvidenceConflict(a.economicRetentionConflicts, usageevidence.EvidenceConflict{
				Identity:               identity,
				ExistingHash:           record.baselineHash,
				IncomingHash:           hash,
				ExistingCoverage:       record.baselineCoverage,
				ExistingCoverageReason: record.baselineCoverageReason,
				IncomingCoverage:       usageevidence.EconomicEvidenceCoverageUnsupported,
				IncomingCoverageReason: economicEvidenceRetentionBoundReason,
			})
		}
		return economicEvidenceAdmission{disposition: economicEvidenceAdmissionConflict}
	}
	if record.quarantine == nil {
		record.quarantine = make(map[string]economicVariantQuarantine)
	}
	record.quarantine[hash] = economicVariantQuarantine{coverage: coverage, coverageReason: reason, reasonDigest: reasonDigest, reasonDegraded: reasonDegraded}
	a.economicConflicts = appendBoundedEvidenceConflict(a.economicConflicts, usageevidence.EvidenceConflict{
		Identity:               identity,
		ExistingHash:           record.baselineHash,
		IncomingHash:           hash,
		ExistingCoverage:       record.baselineCoverage,
		ExistingCoverageReason: record.baselineCoverageReason,
		IncomingCoverage:       coverage,
		IncomingCoverageReason: reason,
	})
	return economicEvidenceAdmission{disposition: economicEvidenceAdmissionConflict}
}

func (a *attemptSession) rememberEconomicObservationOnce(observation metering.Observation) {
	a.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{Observation: observation})
}

// economicEvidenceDrain returns attempt-owned economic observations plus the
// combined conflict view used by direct probes and the deferred terminal
// release. Runtime-owned retention markers are emitted ahead of ordinary
// conflicts; the terminal record builder uses economicEvidenceDrainPartitioned
// so reserved priority is granted from provenance, not from this merged view.
func (a *attemptSession) economicEvidenceDrain() ([]execbackend.EconomicEvidence, []usageevidence.EvidenceConflict) {
	observations, ordinary, retention := a.economicEvidenceDrainPartitioned()
	return observations, mergeEconomicEvidenceConflicts(retention, ordinary)
}

// economicEvidenceDrainPartitioned returns attempt-owned economic observations
// together with the two conflict origins kept strictly separate: the
// runtime-owned reserved retention markers and the provider/source-derived
// ordinary conflicts. Only the internal variant-quarantine overflow path can
// populate the reserved channel, so the terminal handoff can grant reserved
// priority from unforgeable provenance instead of from provider-controlled
// coverage or free-text reason. Draining clears every attempt-owned economic
// accumulator, so a later callback sees no stale state.
func (a *attemptSession) economicEvidenceDrainPartitioned() ([]execbackend.EconomicEvidence, []usageevidence.EvidenceConflict, []usageevidence.EvidenceConflict) {
	if a == nil {
		return nil, nil, nil
	}
	a.economicMu.Lock()
	defer a.economicMu.Unlock()
	observations := make([]execbackend.EconomicEvidence, len(a.economicObservations))
	for i, evidence := range a.economicObservations {
		observations[i] = evidence
		observations[i].Observation = evidence.Observation.Clone()
	}
	ordinary := append([]usageevidence.EvidenceConflict(nil), a.economicConflicts...)
	retention := make([]usageevidence.EvidenceConflict, 0, len(a.economicRetentionConflicts))
	for _, marker := range a.economicRetentionConflicts {
		retention = append(retention, marker.conflict)
	}
	// Sticky capture loss is projected from attempt-owned state on every drain
	// so no terminal accumulator reset can hide a destructively drained
	// rejection. The marker grants reserved priority by provenance, never from
	// source-controlled fields.
	retention = append(retention, captureLossEvidenceConflicts(a.evidenceCaptureLoss)...)
	a.economicObservations = nil
	a.economicIdentities = nil
	a.economicConflicts = nil
	a.economicRetentionConflicts = nil
	return observations, ordinary, retention
}

func (a *attemptSession) finalizeBillingResult(ctx context.Context, state *billingCallState, in execbackend.BillingFinalizationInput) (execbackend.BillingFinalizationResult, bool) {
	if a == nil || state == nil {
		return execbackend.BillingFinalizationResult{}, false
	}
	if a.finalizeBillingV2 != nil {
		return state.finalizeOnceWithEvidence(ctx, in, a.finalizeBillingV2)
	}
	if a.finalizeBilling == nil {
		return execbackend.BillingFinalizationResult{}, false
	}
	ev, ok := state.finalizeOnce(ctx, in, a.finalizeBilling)
	return execbackend.BillingFinalizationResult{Usage: ev}, ok
}

func (a *attemptSession) aggregatedUsageEvidence() aipapi.Event {
	if a == nil {
		return aipapi.Event{}
	}
	a.usageMu.Lock()
	if len(a.accumulatedUsage) == 0 {
		a.usageMu.Unlock()
		return aipapi.Event{}
	}
	events := append([]aipapi.Event(nil), a.accumulatedUsage...)
	a.usageMu.Unlock()
	return authorityUsageEvent(events)
}

func (a *attemptSession) drainStreamUsageEvidence(inner aipapi.ManagedEventStream) {
	if a == nil || inner == nil {
		return
	}
	source, ok := inner.(aipapi.UsageEvidenceSource)
	economicSource, economicOK := inner.(execbackend.EconomicEvidenceSource)
	if economicOK {
		// The drain owns no request context, so the log context is built the
		// way the runtime builds it elsewhere: from the attempt-owned trace id
		// and A-leg.
		logCtx := diag.EnsureCallDiag(context.Background(), strings.TrimSpace(a.traceID), strings.TrimSpace(a.bleg.ALegID))
		_ = safety.Call(safety.BoundaryBackend, "backend_stream_drain_economic", func() error {
			for _, evidence := range economicSource.DrainEconomicEvidenceRecords() {
				if reason := strings.TrimSpace(evidence.RejectionReason); reason != "" {
					// The terminal drain records the capture loss and emits the
					// same bounded structured event as the intermediate drain.
					// The event carries only the closed reason token, the
					// rejection count, the canonical host-evidence byte total
					// and the host lineage the runtime already holds.
					a.rememberEconomicEvidenceOnce(evidence)
					logConnectorEvidenceRejection(logCtx, a.authority.log, a, reason)
					continue
				}
				a.rememberEconomicEvidenceOnce(evidence)
			}
			return nil
		})
	}
	if !ok {
		if !economicOK {
			if observationSource, observationOK := inner.(metering.ObservationSource); observationOK {
				_ = safety.Call(safety.BoundaryBackend, "backend_stream_drain_economic", func() error {
					for _, observation := range observationSource.DrainEconomicObservations() {
						a.rememberEconomicObservationOnce(observation)
					}
					return nil
				})
			}
		}
		return
	}
	_ = safety.Call(safety.BoundaryBackend, "backend_stream_drain_usage", func() error {
		for _, ev := range source.DrainUsageEvidence() {
			if ev.Kind != aipapi.EventUsageDelta {
				continue
			}
			if a.rememberUsageEvidenceOnceAs(ev, billingEvidenceRoleSideband) {
				a.observeAccountingUsage(ev)
			}
		}
		return nil
	})
	if !economicOK {
		if observationSource, observationOK := inner.(metering.ObservationSource); observationOK {
			_ = safety.Call(safety.BoundaryBackend, "backend_stream_drain_economic", func() error {
				for _, observation := range observationSource.DrainEconomicObservations() {
					a.rememberEconomicObservationOnce(observation)
				}
				return nil
			})
		}
	}
}

// usageOrAccumulated returns primary when it carries token or cost presence,
// otherwise the best attempt-owned accumulated evidence, else an empty shell.
func (a *attemptSession) usageOrAccumulated(primary aipapi.Event) aipapi.Event {
	if primary.Kind != "" && (primary.UsagePresence.Any() || primary.CostPresent) {
		return primary
	}
	if acc := a.aggregatedUsageEvidence(); acc.Kind != "" {
		return acc
	}
	return emptyOperatorUsageShell()
}

// augmentBillingUsage merges attempt-owned accumulated evidence into a terminal
// billing stream event: full substitution when the stream event lacks presence,
// otherwise provider-cost backfill only. Accumulated host-only money fills the
// V1 compatibility projection carrier; source-separated V2 observations retain
// each source independently, so no source record is destroyed by the backfill.
// Task 18.2 (Migration Strategy step 8): this host-only V1 draining carrier is
// intentionally retained for in-flight V1 recovery; it is not a live V2 money
// path. Operator migration: V2 terminal observations own new economics.
func (a *attemptSession) augmentBillingUsage(streamEv, fallbackPrimary aipapi.Event) aipapi.Event {
	if streamEv.Kind == "" {
		streamEv = fallbackPrimary
	}
	if accumulatedEv := a.aggregatedUsageEvidence(); accumulatedEv.Kind != "" {
		if streamEv.Kind == "" || (!streamEv.UsagePresence.Any() && !streamEv.CostPresent) {
			streamEv = accumulatedEv
		} else if !streamEv.CostPresent && accumulatedEv.CostPresent {
			streamEv.CostPresent, streamEv.CostNanoUnits, streamEv.Currency = true, accumulatedEv.CostNanoUnits, accumulatedEv.Currency
		}
	}
	if streamEv.Kind == "" {
		return emptyOperatorUsageShell()
	}
	return streamEv
}

const (
	billingEvidenceRoleStream    = "stream"
	billingEvidenceRoleSideband  = "sideband"
	billingEvidenceRoleFinalizer = "finalizer"
	billingObservationMappingV1  = "aipapi.event.accounting.v1"
)

// capturedBillingEvidence keeps the acquisition path separate until terminal
// record construction. The legacy aipapi.Event remains an input boundary only;
// no provider wire value is parsed here.
type capturedBillingEvidence struct {
	event aipapi.Event
	role  string
}

// billingObservationInput is a terminal conversion input. Sequence is local
// ordering only and never replaces the concrete B-leg subject.
type billingObservationInput struct {
	event    aipapi.Event
	role     string
	sequence uint64
}

// projectBillingEvidenceEvent keeps only the canonical accounting carriers that
// the V1-to-V2 mapping is allowed to consume. In particular, raw provider
// bodies, reasoning/media payloads and other mutable Event interiors never
// enter the attempt-owned terminal accumulator.
func projectBillingEvidenceEvent(ev aipapi.Event) aipapi.Event {
	return aipapi.Event{
		Kind:             ev.Kind,
		InputTokens:      ev.InputTokens,
		OutputTokens:     ev.OutputTokens,
		CacheReadTokens:  ev.CacheReadTokens,
		CacheWriteTokens: ev.CacheWriteTokens,
		ReasoningTokens:  ev.ReasoningTokens,
		TotalTokens:      ev.TotalTokens,
		UsagePresence:    ev.UsagePresence,
		CostNanoUnits:    ev.CostNanoUnits,
		Currency:         ev.Currency,
		CostSource:       ev.CostSource,
		CostPresent:      ev.CostPresent,
		Accounting:       ev.Accounting,
		UsageScopes:      append([]aipapi.ScopedUsageDelta(nil), ev.UsageScopes...),
	}
}

func observationsFromBillingEvidence(draft billingLegDraft, captured []capturedBillingEvidence) ([]metering.Observation, []usageevidence.EvidenceConflict, int) {
	inputs := make([]billingObservationInput, 0, len(captured)+2)
	capturedKeys := make(map[string]struct{}, len(captured))
	for i, evidence := range captured {
		if directEvidenceCoveredByEconomicObservation(draft, evidence.event) {
			continue
		}
		inputs = append(inputs, billingObservationInput{event: evidence.event, role: evidence.role, sequence: uint64(i + 1)})
		if key := strings.TrimSpace(evidence.event.Accounting.DedupeKey); key != "" {
			capturedKeys[key] = struct{}{}
		}
	}
	// Keep direct stream/finalizer envelopes in the collection even when an
	// adapter did not expose an attempt accumulator. Once an accumulator or
	// negotiated V2 source exists, its source events are authoritative and the
	// stream field is only a derived V1 fallback; adding that aggregate would
	// manufacture a duplicate against the provider source. Exact source
	// identity replay is removed below; changed payloads are surfaced as
	// conflicts.
	appendDirectEvidence := func(event aipapi.Event, role string) {
		if event.Kind == "" || directEvidenceCoveredByEconomicObservation(draft, event) {
			return
		}
		key := strings.TrimSpace(event.Accounting.DedupeKey)
		if role == billingEvidenceRoleStream && key != "" && hasCapturedEvidenceKey(capturedKeys, key) {
			return
		}
		inputs = append(inputs, billingObservationInput{event: event, role: role, sequence: uint64(len(inputs) + 1)})
	}
	appendDirectEvidence(draft.stream, billingEvidenceRoleStream)
	appendDirectEvidence(draft.finalize, billingEvidenceRoleFinalizer)

	observations := make([]metering.Observation, 0, len(inputs))
	conflicts := make([]usageevidence.EvidenceConflict, 0)
	// aipapi's DedupeKey is the only source-event identity available at this
	// neutral boundary. Revision is fixed to one because Event has no revision
	// member; a later corrected source must arrive through the V2 observation
	// contract rather than being guessed here.
	seenSourceEvents := make(map[string]string, len(inputs))
	dropped := 0
	for _, input := range inputs {
		observation, ok := billingObservationFromEvent(draft, input)
		if !ok {
			continue
		}
		identity := strings.TrimSpace(input.event.Accounting.DedupeKey) + "\x00revision:1"
		hash := eventEvidenceFingerprint(input.event)
		if prior, exists := seenSourceEvents[identity]; exists {
			if prior != hash {
				conflicts = appendBoundedEvidenceConflict(conflicts, usageevidence.EvidenceConflict{
					Identity: identity, ExistingHash: prior, IncomingHash: hash,
				})
			}
			continue
		}
		seenSourceEvents[identity] = hash
		if len(observations) >= usageevidence.MaxCallLegEvidenceObservations {
			// The attempt accumulator is intentionally bounded. Preserve the
			// V1 scalar fallback for any dropped terminal source while keeping
			// the immutable V2 collection within the record contract. Report
			// the drop so the terminal builder can surface a trusted reserved
			// fail-closed disposition instead of silently truncating a
			// validated origin before it ever reaches the builder.
			dropped++
			continue
		}
		observations = append(observations, observation)
	}
	return observations, conflicts, dropped
}

// directEvidenceCoveredByEconomicObservation prevents a canonical provider
// usage fallback from becoming a second V2 observation when the same source
// event already arrived through the negotiated host-only V2 seam. Matching is
// deliberately limited to the same provider source key and trusted B-leg/store
// subject; unrelated local/finalizer evidence remains eligible for mapping.
func directEvidenceCoveredByEconomicObservation(draft billingLegDraft, event aipapi.Event) bool {
	if event.Kind != aipapi.EventUsageDelta || event.Accounting.Plane != aipapi.UsagePlaneProviderBillable {
		return false
	}
	key := strings.TrimSpace(event.Accounting.DedupeKey)
	if key == "" || len(draft.economicObservations) == 0 {
		return false
	}
	for _, evidence := range draft.economicObservations {
		observation, err := evidence.Observation.Canonical()
		if err != nil || strings.TrimSpace(observation.SourceEventKey) != key {
			continue
		}
		if observation.Subject.Kind != metering.SubjectBLeg {
			continue
		}
		if storeID := strings.TrimSpace(draft.storeID); storeID != "" && observation.Subject.StoreID != storeID {
			continue
		}
		if bLegID := strings.TrimSpace(draft.bLegID); bLegID != "" && observation.Subject.BLegID != bLegID {
			continue
		}
		return true
	}
	return false
}

func hasCapturedEvidenceKey(keys map[string]struct{}, key string) bool {
	_, ok := keys[strings.TrimSpace(key)]
	return ok
}

// mergeEconomicEvidenceConflicts emits the reserved retention-overflow
// dispositions first, then ordinary conflicts, so a saturated ordinary conflict
// set can never displace the explicit retention-overflow cause. The merged
// result never exceeds the durable conflict cap; when it would, ordinary
// conflicts are dropped from the tail deterministically after every reserved
// marker is retained.
func mergeEconomicEvidenceConflicts(retention, ordinary []usageevidence.EvidenceConflict) []usageevidence.EvidenceConflict {
	if len(retention) == 0 {
		return append([]usageevidence.EvidenceConflict(nil), ordinary...)
	}
	out := make([]usageevidence.EvidenceConflict, 0, usageevidence.MaxCallLegEvidenceConflicts)
	for _, conflict := range retention {
		out = appendBoundedEvidenceConflict(out, conflict)
		if len(out) >= usageevidence.MaxCallLegEvidenceConflicts {
			return out
		}
	}
	for _, conflict := range ordinary {
		out = appendBoundedEvidenceConflict(out, conflict)
		if len(out) >= usageevidence.MaxCallLegEvidenceConflicts {
			break
		}
	}
	return out
}

// mergeTerminalEvidenceConflicts merges the conflict origins of one terminal
// B-leg record. reserved carries the runtime-owned retention-overflow
// dispositions and is emitted first, so a saturated ordinary conflict set from
// any origin can never displace the explicit fail-closed cause. Every other
// origin is ordinary and fills the remaining capacity in origin order, dropped
// deterministically from the tail. Reserved priority is granted from the trusted
// reserved channel alone; it is never re-derived from coverage or free-text
// reason fields that a provider can control. The durable cap is never exceeded,
// no origin slice is mutated, and immutable evidence is only read.
func mergeTerminalEvidenceConflicts(reserved []usageevidence.EvidenceConflict, origins ...[]usageevidence.EvidenceConflict) []usageevidence.EvidenceConflict {
	if len(reserved) == 0 && len(origins) == 0 {
		return nil
	}
	out := make([]usageevidence.EvidenceConflict, 0, usageevidence.MaxCallLegEvidenceConflicts)
	for _, conflict := range reserved {
		out = appendBoundedEvidenceConflict(out, conflict)
		if len(out) >= usageevidence.MaxCallLegEvidenceConflicts {
			return out
		}
	}
	for _, origin := range origins {
		for _, conflict := range origin {
			out = appendBoundedEvidenceConflict(out, conflict)
			if len(out) >= usageevidence.MaxCallLegEvidenceConflicts {
				return out
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// appendRetentionEvidenceConflict retains one runtime-owned retention-overflow
// disposition in reserved capacity independent of the ordinary conflict cap.
// Repeated identical dispositions are deduplicated and the reserved set is
// bounded by the closed disposition set, so retention overflow stays visible
// without unbounded growth even when ordinary conflicts are full. The reserved
// slice is a distinct type so this is the only writer that can populate the
// trusted terminal channel.
func appendRetentionEvidenceConflict(retention []retentionEvidenceConflict, conflict usageevidence.EvidenceConflict) []retentionEvidenceConflict {
	for _, prior := range retention {
		if prior.conflict == conflict {
			return retention
		}
	}
	if len(retention) >= maxEconomicRetentionConflicts {
		return retention
	}
	return append(retention, retentionEvidenceConflict{conflict: conflict})
}

// boundEconomicEvidenceReason normalizes one coverage reason to an owned,
// bounded, UTF-8-valid diagnostic string. Truncation never changes the coverage
// classification and never empties a non-empty reason, so an incomplete
// disposition keeps the non-empty reason its validation requires. The returned
// string never aliases the caller's (possibly multi-megabyte) backing
// allocation, and an over-long input carries an explicit truncation marker so
// the loss is visible in the durable record.
func boundEconomicEvidenceReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	if len(reason) <= maxEconomicEvidenceRetainedReasonBytes {
		return strings.Clone(reason)
	}
	limit := maxEconomicEvidenceRetainedReasonBytes - len(economicEvidenceReasonTruncationMarker)
	bounded := reason[:limit]
	for len(bounded) > 0 && !utf8.ValidString(bounded) {
		bounded = bounded[:len(bounded)-1]
	}
	// Concatenation allocates a fresh buffer, so the result cannot alias the
	// original backing allocation.
	return bounded + economicEvidenceReasonTruncationMarker
}

// economicEvidenceReasonFingerprint returns a bounded digest of the full
// coverage reason and whether the raw reason exceeded the safe hashing cap. A
// reason at or below the cap is hashed in full. A longer reason is explicitly
// degraded and sampled by head, exact length, and tail: distinct reasons that
// share a long common prefix cannot silently collapse, while per-event hashing
// stays bounded at a few KiB regardless of adversarial input size. Callers must
// treat any degraded reason as a visible conflict rather than an exact replay.
func economicEvidenceReasonFingerprint(reason string) (digest string, degraded bool) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", false
	}
	preimage := reason
	if len(reason) > maxEconomicEvidenceReasonDigestBytes {
		degraded = true
		var b strings.Builder
		b.Grow(2*maxEconomicEvidenceReasonDigestBytes + 32)
		b.WriteString(reason[:maxEconomicEvidenceReasonDigestBytes])
		b.WriteByte(0)
		b.WriteString(strconv.Itoa(len(reason)))
		b.WriteByte(0)
		b.WriteString(reason[len(reason)-maxEconomicEvidenceReasonDigestBytes:])
		preimage = b.String()
	}
	sum := sha256.Sum256([]byte(preimage))
	return hex.EncodeToString(sum[:]), degraded
}

func appendBoundedEvidenceConflict(conflicts []usageevidence.EvidenceConflict, conflict usageevidence.EvidenceConflict) []usageevidence.EvidenceConflict {
	if slices.Contains(conflicts, conflict) {
		return conflicts
	}
	if len(conflicts) >= usageevidence.MaxCallLegEvidenceConflicts {
		return conflicts
	}
	return append(conflicts, conflict)
}

func billingObservationFromEvent(draft billingLegDraft, input billingObservationInput) (metering.Observation, bool) {
	event := input.event
	if event.Kind != aipapi.EventUsageDelta {
		return metering.Observation{}, false
	}
	key := strings.TrimSpace(event.Accounting.DedupeKey)
	// A source identity is required by the V2 replay contract. A proxy-owned
	// synthetic V1 key remains available for legacy closure/readers, but it is
	// not presented as provider source identity in V2.
	if key == "" {
		return metering.Observation{}, false
	}
	storeID := strings.TrimSpace(draft.storeID)
	if storeID == "" {
		// V1 compatibility evidence has no trusted store scope. Do not emit a
		// V2 observation with a synthetic, empty, or inferred store identity.
		return metering.Observation{}, false
	}
	origin, acquisition, ok := billingObservationProvenance(event.Accounting.Source, input.role)
	if !ok {
		return metering.Observation{}, false
	}
	authority := billingObservationAuthority(event.Accounting.Authority)
	perspective := billingObservationPerspective(event.Accounting.Plane)
	sequence := input.sequence
	if sequence == 0 {
		sequence = 1
	}
	observedAt := draft.finishedAt
	if observedAt.IsZero() {
		observedAt = draft.startedAt
	}
	if observedAt.IsZero() {
		observedAt = time.Unix(0, 0).UTC()
	}
	observedAt = observedAt.UTC()

	callID := ""
	if err := draft.callID.Validate(); err == nil {
		callID = draft.callID.String()
	}
	bLegID := strings.TrimSpace(draft.bLegID)
	if bLegID == "" {
		bLegID = billingSyntheticBLegID(draft.seq)
	}
	aLegID := strings.TrimSpace(draft.aLegID)
	streamID := "b-leg:" + bLegID + ":" + strings.TrimSpace(input.role)
	if strings.TrimSuffix(streamID, ":") == "b-leg:"+bLegID+":" {
		streamID = "b-leg:" + bLegID + ":stream"
	}
	observation := metering.Observation{
		Version:        metering.ObservationVersionV2,
		SourceEventKey: key,
		Revision:       1,
		StreamID:       streamID,
		Sequence:       sequence,
		Origin:         origin,
		Acquisition:    acquisition,
		Authority:      authority,
		Perspective:    perspective,
		Boundary:       metering.BoundaryBackendEgress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind:          metering.SubjectBLeg,
			StoreID:       storeID,
			SubmissionID:  strings.TrimSpace(draft.submissionID),
			ALegID:        aLegID,
			BillingCallID: callID,
			BLegID:        bLegID,
			AttemptSeq:    positiveAttemptSeq(draft.seq),
		},
		Correlation: metering.CorrelationV2{
			StoreID:       storeID,
			SubmissionID:  strings.TrimSpace(draft.submissionID),
			CallID:        callID,
			BillingCallID: callID,
			ALegID:        aLegID,
			BLegID:        bLegID,
			AttemptSeq:    positiveAttemptSeq(draft.seq),
		},
		Semantics:  billingObservationSemantics(input.role),
		ObservedAt: observedAt,
		ReceivedAt: observedAt,
		MappingRef: billingObservationMappingV1 + ":" + strings.TrimSpace(input.role),
	}

	observation.Measures = billingObservationMeasures(event, authority)
	// Cost is the one monetary field allowed through this neutral conversion,
	// and only a provider-reported source has the authority to claim it. Local
	// estimators remain V1 compatibility evidence; treating them as provider
	// charges here would turn an estimate into operator COGS.
	if event.CostPresent && event.Accounting.Source == aipapi.UsageSourceProviderReported && strings.TrimSpace(event.Currency) != "" && event.CostNanoUnits >= 0 {
		amount := metering.DecimalFromNanoUnits(event.CostNanoUnits)
		observation.Charges = []metering.ReportedCharge{{
			ChargeItemID: "cost:" + key,
			Amount:       &amount,
			Currency:     strings.TrimSpace(event.Currency),
			Kind:         metering.ChargeKindAggregate,
		}}
	}
	if len(observation.Measures) == 0 && len(observation.Charges) == 0 && authority != metering.AuthorityUnavailableClaim {
		// An empty authoritative event is not a useful V2 observation and would
		// fail the SDK envelope validator. Preserve it through the V1 projection.
		return metering.Observation{}, false
	}
	seed := strings.Join([]string{
		callID, aLegID, bLegID, strings.TrimSpace(input.role), origin, acquisition, key, "1",
	}, "\x00")
	sum := sha256.Sum256([]byte(seed))
	observation.ID = "aip-runtime-" + hex.EncodeToString(sum[:])
	if err := observation.Validate(); err != nil {
		return metering.Observation{}, false
	}
	return observation, true
}

func positiveAttemptSeq(seq int) uint64 {
	if seq <= 0 {
		return 0
	}
	return uint64(seq)
}

func billingObservationProvenance(source aipapi.UsageSource, role string) (origin, acquisition string, ok bool) {
	role = strings.TrimSpace(role)
	switch source {
	case aipapi.UsageSourceProviderReported:
		if role == billingEvidenceRoleFinalizer {
			return metering.OriginProvider, metering.AcquisitionProviderFinalizer, true
		}
		return metering.OriginProvider, metering.AcquisitionProviderResponse, true
	case aipapi.UsageSourceProviderCountAPI:
		return metering.OriginProvider, metering.AcquisitionProviderCountAPI, true
	case aipapi.UsageSourceLocalTokenizer:
		return metering.OriginLocal, metering.AcquisitionLocalTokenizer, true
	case aipapi.UsageSourceLocalEstimator:
		return metering.OriginLocal, metering.AcquisitionLocalEstimator, true
	case aipapi.UsageSourceUnavailable:
		return metering.OriginProvider, metering.AcquisitionProviderResponse, true
	default:
		// proxy_adjusted/policy_reserved have no approved V2 acquisition
		// mapping in the frozen SDK. Keep their V1 fields without inventing a
		// new provider-neutral source vocabulary.
		return "", "", false
	}
}

func billingObservationAuthority(authority aipapi.UsageAuthority) string {
	switch authority {
	case aipapi.UsageAuthorityAuthoritative, aipapi.UsageAuthorityDelegated:
		return metering.AuthorityObservedClaim
	case aipapi.UsageAuthorityEstimated, aipapi.UsageAuthorityAdvisory:
		return metering.AuthorityEstimatedClaim
	case aipapi.UsageAuthorityUnavailable:
		return metering.AuthorityUnavailableClaim
	default:
		return metering.AuthorityUnavailableClaim
	}
}

func billingObservationPerspective(plane aipapi.UsagePlane) metering.EconomicPerspective {
	switch plane {
	case aipapi.UsagePlaneClientVisible:
		return metering.PerspectiveCustomer
	case aipapi.UsagePlaneProviderBillable, aipapi.UsagePlaneProxyBillable:
		return metering.PerspectiveOperator
	default:
		return metering.PerspectiveNone
	}
}

func billingObservationSemantics(role string) string {
	if strings.TrimSpace(role) == billingEvidenceRoleFinalizer {
		return metering.SemanticsCumulative
	}
	return metering.SemanticsDelta
}

func billingObservationMeasures(event aipapi.Event, authority string) []metering.Measure {
	quality := metering.QualityObserved
	switch authority {
	case metering.AuthorityEstimatedClaim:
		quality = metering.QualityEstimated
	case metering.AuthorityUnavailableClaim:
		quality = metering.QualityUnknown
	}
	measures := make([]metering.Measure, 0, 6)
	appendMeasure := func(component string, direction metering.FlowDirection, value int, present bool) {
		if !present || value < 0 {
			return
		}
		decimal := metering.Decimal{Coefficient: strconv.Itoa(value)}
		measures = append(measures, metering.Measure{
			Key: metering.ComponentKey{
				Direction: direction, Component: component, Unit: metering.UnitToken,
				SchemaID: metering.DefaultInclusionSchemaID,
			},
			Value: &decimal, Quality: quality, MethodRef: billingObservationMappingV1,
		})
	}
	appendMeasure(metering.ComponentInputToken, metering.DirectionInput, event.InputTokens, event.UsagePresence.InputTokens)
	appendMeasure(metering.ComponentOutputToken, metering.DirectionOutput, event.OutputTokens, event.UsagePresence.OutputTokens)
	appendMeasure(metering.ComponentCacheReadInputToken, metering.DirectionInput, event.CacheReadTokens, event.UsagePresence.CacheReadTokens)
	appendMeasure(metering.ComponentCacheWriteInputToken, metering.DirectionInput, event.CacheWriteTokens, event.UsagePresence.CacheWriteTokens)
	appendMeasure(metering.ComponentReasoningOutputToken, metering.DirectionOutput, event.ReasoningTokens, event.UsagePresence.ReasoningTokens)
	appendMeasure(metering.ComponentTotalToken, metering.DirectionNone, event.TotalTokens, event.UsagePresence.TotalTokens)
	return measures
}

func eventEvidenceFingerprint(event aipapi.Event) string {
	payload, err := json.Marshal(projectBillingEvidenceEvent(event))
	if err != nil {
		return fmt.Sprintf("invalid:%v", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
