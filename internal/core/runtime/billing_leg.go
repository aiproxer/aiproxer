package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type BillingLegObserver interface {
	ObserveBillingLeg(context.Context, usageevidence.CallLegUsageRecord)
}
type BillingLegObserverFunc func(context.Context, usageevidence.CallLegUsageRecord)

func (f BillingLegObserverFunc) ObserveBillingLeg(ctx context.Context, record usageevidence.CallLegUsageRecord) {
	if f != nil {
		f(ctx, record)
	}
}

type billingLegDraft struct {
	callID               usageevidence.BillingCallID
	submissionID         string
	aLegID               string
	storeID              string
	bLegID               string
	seq                  int
	primary              routing.Primary
	startedAt            time.Time
	finishedAt           time.Time
	command              sdkterminal.Command
	outcome              usageevidence.LegOutcome
	surfaced             usageevidence.SurfacedState
	finalize             aipapi.Event
	stream               aipapi.Event
	evidenceEvents       []capturedBillingEvidence
	evidenceConflicts    []usageevidence.EvidenceConflict
	economicObservations []execbackend.EconomicEvidence
	// economicConflicts is the trusted reserved channel: it carries only the
	// runtime-owned retention-overflow markers produced by the internal variant
	// quarantine. It is never populated from provider Observation/EvidenceConflict
	// content, so reserved terminal priority cannot be forged from coverage or
	// free-text reason fields.
	economicConflicts []usageevidence.EvidenceConflict
	// economicOrdinaryConflicts carries provider/source-derived economic conflicts
	// that must never receive reserved priority.
	economicOrdinaryConflicts []usageevidence.EvidenceConflict
	localObservations         []metering.Observation
	operatorRateRef           usageevidence.VersionRef
	workload                  usageevidence.WorkloadIdentity
}

func billingLegRecord(draft billingLegDraft) usageevidence.CallLegUsageRecord {
	backend := strings.TrimSpace(draft.primary.Backend)
	if backend == "" {
		backend = "unknown"
	}
	model := strings.TrimSpace(draft.primary.Model)
	if model == "" {
		model = "unknown"
	}
	bLegID := strings.TrimSpace(draft.bLegID)
	if bLegID == "" {
		bLegID = billingSyntheticBLegID(draft.seq)
	}
	// Evidence is captured source-by-source for V2. The scalar V1 value below
	// is retained only as a compatibility projection for old stores/readers;
	// it is deliberately not used as the terminal economic source of truth.
	evidence := projectV1BillingEvidence(finalBillingEvidenceFromEvent(draft.finalize), finalBillingEvidenceFromEvent(draft.stream))
	evidence = normalizeBillingEvidenceIdentity(evidence, draft.callID, bLegID)
	observations, conflicts, legacyDropped := observationsFromBillingEvidence(draft, draft.evidenceEvents)

	// The validated final local boundary origin is assembled last, after
	// provider and economic evidence, so it is the origin most exposed to the
	// bounded terminal evidence envelope. Reserve bounded capacity for it
	// before those origins can consume the whole envelope; provider/economic
	// evidence keeps priority for every unreserved slot. Any origin that still
	// cannot be included is surfaced as a trusted reserved fail-closed
	// disposition rather than silently truncated. legacyDropped carries the
	// V1 conversion cap that already fired inside the provider-origin builder.
	localSubmissionConflicts := submissionLinkageConflictsForObservations(draft.localObservations, draft.submissionID)
	localInput := retainSubmissionLinkedObservations(draft.localObservations, draft.submissionID)
	localReserve := min(len(localInput), maxTerminalLocalEvidenceReserve)
	nonLocalLimit := usageevidence.MaxCallLegEvidenceObservations - localReserve
	envelopeDropped := legacyDropped
	if len(observations) > nonLocalLimit {
		envelopeDropped += len(observations) - nonLocalLimit
		observations = observations[:nonLocalLimit]
	}

	// Economic observations may already be durable checkpoints. Their source
	// revisions and supersession references were fingerprinted before this
	// terminal handoff, so mutating Subject/Correlation here would invalidate
	// predecessor hashes. SubmissionID is therefore carried by the trusted
	// call-leg record; any adapter-carried value is accepted only when it agrees
	// with that record-level authority.
	economicSubmissionConflicts := submissionLinkageConflictsForEconomicEvidence(draft.economicObservations, draft.submissionID)
	economicInput := retainSubmissionLinkedEconomicEvidence(draft.economicObservations, draft.submissionID)
	var economicDispositions []usageevidence.EconomicEvidenceDisposition
	economicDropped := 0
	observations, economicDispositions, conflicts, economicDropped = appendCanonicalEconomicObservations(observations, conflicts, economicInput, nonLocalLimit)
	envelopeDropped += economicDropped

	localDropped := 0
	observations, conflicts, localDropped = appendCanonicalObservations(observations, conflicts, localInput, usageevidence.MaxCallLegEvidenceObservations)
	envelopeDropped += localDropped

	// Reserved envelope-truncation dispositions join the trusted reserved
	// channel so a saturated ordinary conflict set cannot displace them. The
	// caller's slice is copied first so this append never mutates a shared
	// backing array.
	reservedConflicts := draft.economicConflicts
	if envelopeDropped > 0 {
		reservedConflicts = append(append([]usageevidence.EvidenceConflict(nil), reservedConflicts...), terminalEvidenceEnvelopeLossConflict(envelopeDropped)...)
	}
	// Merge every conflict origin with reserved retention-overflow priority so a
	// saturated ordinary conflict set from any origin cannot displace the
	// explicit fail-closed retention disposition. Reserved priority comes only
	// from the trusted runtime-owned channel; every source-derived origin is
	// ordinary and cannot forge priority. The durable cap still holds, ordinary
	// conflicts stay visible, and no immutable evidence is mutated.
	conflicts = mergeTerminalEvidenceConflicts(
		reservedConflicts,
		conflicts,
		economicSubmissionConflicts,
		localSubmissionConflicts,
		draft.economicOrdinaryConflicts,
		draft.evidenceConflicts,
	)
	outcome := draft.outcome
	if outcome == "" {
		outcome = legOutcomeFromCommand(draft.command)
	}
	record := usageevidence.CallLegUsageRecord{
		CallID:          draft.callID,
		SubmissionID:    strings.TrimSpace(draft.submissionID),
		ALegID:          strings.TrimSpace(draft.aLegID),
		BLegID:          bLegID,
		AttemptSeq:      draft.seq,
		BackendID:       backend,
		ProviderID:      billingProviderID(draft.primary),
		ModelID:         model,
		OperatorRateRef: draft.operatorRateRef,
		StartedAt:       draft.startedAt,
		FinishedAt:      draft.finishedAt,
		Outcome:         outcome,
		Surfaced:        draft.surfaced,
		Evidence:        evidence,
		Workload:        draft.workload,
	}
	if len(observations) != 0 || len(conflicts) != 0 {
		record.EvidenceVersion = usageevidence.EvidenceFormatVersionV2
		record.EvidenceProjection = usageevidence.EvidenceProjectionV1
		record.Observations = observations
		record.EvidenceConflicts = conflicts
	}
	if len(economicDispositions) != 0 {
		record.EconomicEvidenceVersion = usageevidence.EconomicEvidenceDispositionVersionV1
		record.EconomicDispositions = economicDispositions
	}
	return record
}

// appendCanonicalObservations retains each source's provenance plane. It
// validates and deep-copies observations at the terminal handoff, and never
// converts or clones provider evidence into local evidence. Replay identity is
// the shared semantic preimage (receipt time and approved Subject/Correlation
// carrier placement normalized); a genuinely changed semantic payload remains
// a visible conflict. limit bounds the terminal envelope; the returned dropped
// count lets the caller surface a reserved fail-closed disposition instead of
// silently truncating a validated origin.
func appendCanonicalObservations(existing []metering.Observation, conflicts []usageevidence.EvidenceConflict, incoming []metering.Observation, limit int) ([]metering.Observation, []usageevidence.EvidenceConflict, int) {
	if limit <= 0 || limit > usageevidence.MaxCallLegEvidenceObservations {
		limit = usageevidence.MaxCallLegEvidenceObservations
	}
	if len(incoming) == 0 {
		return existing, conflicts, 0
	}
	seen := make(map[string]string, len(existing)+len(incoming))
	for _, observation := range existing {
		seen[observation.IdentityKey()] = usageevidence.ObservationEvidenceHash(observation)
	}
	dropped := 0
	for _, original := range incoming {
		observation, err := original.Canonical()
		if err != nil {
			continue
		}
		identity := observation.IdentityKey()
		hash := usageevidence.ObservationEvidenceHash(observation)
		if prior, ok := seen[identity]; ok {
			if prior != hash {
				conflicts = appendBoundedEvidenceConflicts(conflicts, []usageevidence.EvidenceConflict{{Identity: identity, ExistingHash: prior, IncomingHash: hash}})
			}
			continue
		}
		if len(existing) >= limit {
			dropped++
			continue
		}
		existing = append(existing, observation)
		seen[identity] = hash
	}
	return existing, conflicts, dropped
}

// retainSubmissionLinkedObservations keeps immutable observation envelopes
// intact at terminal handoff. Provider/local payload values cannot override the
// trusted submission authority. A source that carries a different submission
// claim is rejected rather than rewritten after its refs/fingerprint exist.
// The trusted submission remains explicit on CallLegUsageRecord.SubmissionID.
func retainSubmissionLinkedObservations(observations []metering.Observation, submissionID string) []metering.Observation {
	submissionID = strings.TrimSpace(submissionID)
	if len(observations) == 0 {
		return observations
	}
	out := make([]metering.Observation, 0, len(observations))
	for _, observation := range observations {
		if !observationSubmissionMatches(observation, submissionID) {
			continue
		}
		out = append(out, observation)
	}
	return out
}

func retainSubmissionLinkedEconomicEvidence(incoming []execbackend.EconomicEvidence, submissionID string) []execbackend.EconomicEvidence {
	submissionID = strings.TrimSpace(submissionID)
	if len(incoming) == 0 {
		return incoming
	}
	out := make([]execbackend.EconomicEvidence, 0, len(incoming))
	for _, evidence := range incoming {
		if !observationSubmissionMatches(evidence.Observation, submissionID) {
			continue
		}
		out = append(out, evidence)
	}
	return out
}

// submissionLinkageConflictsForObservations retains a bounded, immutable
// diagnostic for every adapter observation rejected because its carried
// submission claim disagrees with the trusted call-leg record. Dropping the
// observation alone would make an accepted earlier revision indistinguishable
// from a complete stream whose later revision was never delivered.
func submissionLinkageConflictsForObservations(observations []metering.Observation, submissionID string) []usageevidence.EvidenceConflict {
	if len(observations) == 0 {
		return nil
	}
	conflicts := make([]usageevidence.EvidenceConflict, 0, len(observations))
	for _, observation := range observations {
		if observationSubmissionMatches(observation, strings.TrimSpace(submissionID)) {
			continue
		}
		if conflict, ok := submissionLinkageConflict(observation); ok {
			conflicts = append(conflicts, conflict)
		}
	}
	return appendBoundedEvidenceConflicts(nil, conflicts)
}

func submissionLinkageConflictsForEconomicEvidence(incoming []execbackend.EconomicEvidence, submissionID string) []usageevidence.EvidenceConflict {
	if len(incoming) == 0 {
		return nil
	}
	conflicts := make([]usageevidence.EvidenceConflict, 0, len(incoming))
	for _, evidence := range incoming {
		observation := evidence.Observation
		if observationSubmissionMatches(observation, strings.TrimSpace(submissionID)) {
			continue
		}
		if conflict, ok := submissionLinkageConflict(observation); ok {
			conflict.IncomingCoverage = usageevidence.EconomicEvidenceCoverageUnsupported
			conflict.IncomingCoverageReason = "trusted_submission_linkage_mismatch"
			conflicts = append(conflicts, conflict)
		}
	}
	return appendBoundedEvidenceConflicts(nil, conflicts)
}

// submissionLinkageConflict computes expected and received hashes from
// detached copies only. It never rewrites a published observation or repairs a
// late reference; the conflict is a quarantine diagnostic for rating and
// reconciliation callers.
func submissionLinkageConflict(observation metering.Observation) (usageevidence.EvidenceConflict, bool) {
	incomingHash := usageevidence.ObservationEvidenceHash(observation)
	if incomingHash == "" {
		return usageevidence.EvidenceConflict{}, false
	}
	existingHash := ""
	for _, predecessor := range observation.Supersedes {
		if strings.TrimSpace(predecessor.PayloadHash) != "" {
			existingHash = predecessor.PayloadHash
			break
		}
	}
	if existingHash == "" {
		expected := observation.Clone()
		expected.Subject.SubmissionID = ""
		expected.Correlation.SubmissionID = ""
		existingHash = usageevidence.ObservationEvidenceHash(expected)
	}
	if existingHash == "" || existingHash == incomingHash {
		return usageevidence.EvidenceConflict{}, false
	}
	return usageevidence.EvidenceConflict{
		Identity:     observation.IdentityKey(),
		ExistingHash: existingHash,
		IncomingHash: incomingHash,
	}, true
}

func observationSubmissionMatches(observation metering.Observation, submissionID string) bool {
	for _, claimed := range []string{observation.Subject.SubmissionID, observation.Correlation.SubmissionID} {
		if strings.TrimSpace(claimed) == "" {
			continue
		}
		if claimed != submissionID {
			return false
		}
	}
	return true
}

func appendCanonicalEconomicObservations(existing []metering.Observation, conflicts []usageevidence.EvidenceConflict, incoming []execbackend.EconomicEvidence, limit int) ([]metering.Observation, []usageevidence.EconomicEvidenceDisposition, []usageevidence.EvidenceConflict, int) {
	if limit <= 0 || limit > usageevidence.MaxCallLegEvidenceObservations {
		limit = usageevidence.MaxCallLegEvidenceObservations
	}
	if len(incoming) == 0 {
		return existing, nil, conflicts, 0
	}
	dispositions := make([]usageevidence.EconomicEvidenceDisposition, 0, len(incoming))
	seen := make(map[string]string, len(existing)+len(incoming))
	for _, observation := range existing {
		canonical, err := observation.Canonical()
		if err != nil {
			continue
		}
		seen[canonical.IdentityKey()] = usageevidence.ObservationEvidenceHash(canonical)
	}
	seenDispositions := make(map[string]usageevidence.EconomicEvidenceDisposition, len(incoming))
	dropped := 0
	for _, evidence := range incoming {
		observation, err := evidence.Observation.Canonical()
		if err != nil {
			continue
		}
		disposition, err := usageevidence.NewEconomicEvidenceDisposition(observation, evidence.Coverage, evidence.CoverageReason)
		if err != nil {
			continue
		}
		identity := observation.IdentityKey()
		hash := usageevidence.ObservationEvidenceHash(observation)
		if prior, ok := seen[identity]; ok {
			if prior != hash {
				conflicts = appendBoundedEvidenceConflicts(conflicts, []usageevidence.EvidenceConflict{{
					Identity: identity, ExistingHash: prior, IncomingHash: hash,
					IncomingCoverage: disposition.Coverage, IncomingCoverageReason: disposition.CoverageReason,
				}})
				continue
			}
			if priorDisposition, hasDisposition := seenDispositions[identity]; hasDisposition {
				if priorDisposition.Coverage != disposition.Coverage || priorDisposition.CoverageReason != disposition.CoverageReason {
					conflicts = appendBoundedEvidenceConflicts(conflicts, []usageevidence.EvidenceConflict{{
						Identity: identity, ExistingHash: prior, IncomingHash: hash,
						ExistingCoverage: priorDisposition.Coverage, ExistingCoverageReason: priorDisposition.CoverageReason,
						IncomingCoverage: disposition.Coverage, IncomingCoverageReason: disposition.CoverageReason,
					}})
				}
				continue
			}
			dispositions = append(dispositions, disposition)
			seenDispositions[identity] = disposition
			continue
		}
		if len(existing) >= limit {
			dropped++
			continue
		}
		existing = append(existing, observation)
		seen[identity] = hash
		dispositions = append(dispositions, disposition)
		seenDispositions[identity] = disposition
	}
	return existing, dispositions, conflicts, dropped
}

func appendBoundedEvidenceConflicts(existing, incoming []usageevidence.EvidenceConflict) []usageevidence.EvidenceConflict {
	if len(existing) == 0 && len(incoming) == 0 {
		return nil
	}
	out := make([]usageevidence.EvidenceConflict, 0, min(usageevidence.MaxCallLegEvidenceConflicts, len(existing)+len(incoming)))
	for _, conflict := range existing {
		out = appendBoundedEvidenceConflict(out, conflict)
		if len(out) >= usageevidence.MaxCallLegEvidenceConflicts {
			return out
		}
	}
	for _, conflict := range incoming {
		out = appendBoundedEvidenceConflict(out, conflict)
		if len(out) >= usageevidence.MaxCallLegEvidenceConflicts {
			break
		}
	}
	return out
}

// normalizeBillingEvidenceIdentity keeps every independently accounted B-leg
// identifiable even when no provider usage event was observed (for example, a
// pre-output failover leg). Provider evidence remains authoritative when
// present; only missing identity/provenance fields receive explicit bounded
// fallback values.
func normalizeBillingEvidenceIdentity(evidence usageevidence.FinalBillingEvidence, callID usageevidence.BillingCallID, bLegID string) usageevidence.FinalBillingEvidence {
	if evidence.Source == usageevidence.EvidenceSourceUnknown {
		evidence.Source = usageevidence.EvidenceSourceUnavailable
	}
	if evidence.Authority == usageevidence.EvidenceAuthorityUnknown {
		evidence.Authority = usageevidence.EvidenceAuthorityUnavailable
	}
	if strings.TrimSpace(evidence.DedupeKey) == "" {
		if fallback, err := usageevidence.DedupeKeyForBLeg(callID, bLegID); err == nil {
			evidence.DedupeKey = fallback
		}
	}
	return evidence
}

func (e *Executor) operatorRateRef(ctx context.Context, primary routing.Primary) usageevidence.VersionRef {
	if e == nil || e.BillingIdentity.OperatorRateRef == nil {
		return usageevidence.VersionRef{}
	}
	backend := strings.TrimSpace(primary.Backend)
	model := strings.TrimSpace(primary.Model)
	return e.BillingIdentity.OperatorRateRef(ctx, backend, model)
}

// recordBillingLegForAttempt records exactly one leg for the explicitly
// snapshotted B-leg owner. Terminal callbacks may run after the attempt slot
// has been replaced; never re-read the current slot here.
func (t *turnTerminal) recordBillingLegForAttempt(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, evidence attemptTerminalEvidence, command sdkterminal.Command, streamEv aipapi.Event, committed bool, billingState *billingCallState) {
	if t == nil || t.billingEnabled == nil || !t.billingEnabled() || t.operatorRateRef == nil || t.billingWorkload == nil {
		return
	}
	if attempt == nil || !attempt.claimBillingLegRecord() {
		return
	}
	blegID := strings.TrimSpace(evidence.bleg.BLegID)
	if blegID == "" {
		blegID = billingSyntheticBLegID(evidence.bleg.Seq)
	}
	if evidence.bleg.Seq > 0 && billingState != nil {
		billingState.noteAllocatedBLeg(blegID, evidence.bleg.Seq)
	}
	now := t.nowTime()
	started := evidence.startedAt
	if started.IsZero() {
		started = now
	}
	if billingState != nil {
		billingState.noteLegTimes(started, now)
	}
	surfaced := usageevidence.SurfacedNo
	if command == sdkterminal.CommandNormalFinish || committed {
		surfaced = usageevidence.SurfacedYes
	}
	streamEv = attempt.augmentBillingUsage(streamEv, aipapi.Event{})
	evidenceEvents, evidenceConflicts := attempt.billingEvidenceDrain()
	localObservations := attempt.drainLocalBoundaryObservations(now)
	workloadCtx := request.toRecvTurnFacts(ctx).projectContext(ctx, nil)
	finalizeEv := t.finalizeBillingEvidence(ctx, request, evidence, billingState, "record_leg", streamEv, attempt)
	if err := attempt.flushEconomicCheckpointsAtTerminal(ctx); err != nil && t.logBillingAppendFailure != nil {
		t.logBillingAppendFailure(ctx, "economic_checkpoint_append_critical", "economic checkpoint append failed", err)
	}
	economicObservations, economicOrdinaryConflicts, economicRetentionConflicts := attempt.economicEvidenceDrainPartitioned()
	legRecord := billingLegRecord(billingLegDraft{
		callID:                    request.billingCallID,
		submissionID:              request.submissionID,
		aLegID:                    request.aLegID,
		storeID:                   request.storeID,
		bLegID:                    evidence.bleg.BLegID,
		seq:                       evidence.bleg.Seq,
		primary:                   evidence.candidate.Primary,
		startedAt:                 started,
		finishedAt:                now,
		command:                   command,
		surfaced:                  surfaced,
		finalize:                  finalizeEv,
		stream:                    streamEv,
		evidenceEvents:            evidenceEvents,
		evidenceConflicts:         evidenceConflicts,
		economicObservations:      economicObservations,
		economicConflicts:         economicRetentionConflicts,
		economicOrdinaryConflicts: economicOrdinaryConflicts,
		localObservations:         localObservations,
		operatorRateRef:           t.operatorRateRef(workloadCtx, evidence.candidate.Primary),
		workload:                  t.billingWorkload(workloadCtx, request.aLegID),
	})
	if t.observeBillingLeg != nil {
		t.observeBillingLeg(ctx, legRecord)
	}
	// The sink context carries the frozen request scope even when the
	// incoming terminal context does not, so the monetary handoff keeps
	// customer identity on every path.
	scopeCtx := withTerminalScope(ctx, request.recvViews.Scope)
	if t.appendBillingLegStrict != nil {
		if err := t.appendBillingLegStrict(scopeCtx, request.billingCallID, legRecord); err != nil {
			if t.logBillingAppendFailure != nil {
				t.logBillingAppendFailure(ctx, "billing_call_leg_append_critical", "billing call-leg append failed", err)
			}
		}
	} else if t.appendBillingLeg != nil {
		t.appendBillingLeg(scopeCtx, request.billingCallID, legRecord)
	}
}

func (t *turnTerminal) finalizeBillingEvidence(ctx context.Context, request requestTerminalFacts, evidence attemptTerminalEvidence, billingState *billingCallState, reason string, fallback aipapi.Event, attempt *attemptSession) aipapi.Event {
	if t == nil || billingState == nil || (t.finalizeBilling == nil && t.finalizeBillingV2 == nil) {
		return fallback
	}
	in := execbackend.BillingFinalizationInput{
		TraceID: strings.TrimSpace(request.traceID),
		ALegID:  strings.TrimSpace(request.aLegID),
		BLegID:  strings.TrimSpace(evidence.bleg.BLegID),
		Backend: strings.TrimSpace(evidence.candidate.Primary.Backend),
		Model:   strings.TrimSpace(evidence.candidate.Primary.Model),
		Reason:  strings.TrimSpace(reason),
	}
	result, ok := t.finalizeOnceWithEvidence(ctx, billingState, in)
	if !ok {
		return fallback
	}
	if attempt != nil {
		for _, economic := range result.EconomicEvidence {
			attempt.rememberEconomicEvidenceOnce(economic)
		}
	}
	return result.Usage
}

func (t *turnTerminal) finalizeOnceWithEvidence(ctx context.Context, state *billingCallState, in execbackend.BillingFinalizationInput) (execbackend.BillingFinalizationResult, bool) {
	if t == nil || state == nil || (t.finalizeBilling == nil && t.finalizeBillingV2 == nil) {
		return execbackend.BillingFinalizationResult{}, false
	}
	if t.finalizeBillingV2 != nil {
		return state.finalizeOnceWithEvidence(ctx, in, t.finalizeBillingV2)
	}
	ev, ok := state.finalizeOnce(ctx, in, t.finalizeBilling)
	return execbackend.BillingFinalizationResult{Usage: ev}, ok
}

func lastUsageDeltaOrShell(events []aipapi.Event) aipapi.Event {
	for _, event := range slices.Backward(events) {
		if event.Kind == aipapi.EventUsageDelta {
			return event
		}
	}
	return emptyOperatorUsageShell()
}

// projectV1BillingEvidence keeps the old scalar contract readable while
// refusing to combine values from incompatible source/authority planes. V2
// source observations are always retained independently by billingLegRecord.
// This explicit one-way compatibility projection is the only V1 cost
// selection; the legacy destructive merge helper was retired in Task 18.1
// (Migration Strategy step 8, Task 18.2). Operator migration: V2 observations
// are the financial truth; this V1 projection exists only for historical
// readers and must never be re-ingested as independent V2 evidence.
func projectV1BillingEvidence(finalize, stream usageevidence.FinalBillingEvidence) usageevidence.FinalBillingEvidence {
	if finalize.Cost.Present {
		return finalize
	}
	if !stream.Cost.Present {
		return finalize
	}
	// A provider-authoritative stream cost is a valid compatibility projection
	// when the finalizer only supplied non-economic provenance (the historical
	// FinalizeBilling ABI does this for token totals). This selects one V1
	// value; the V2 observations still retain the two source envelopes.
	if stream.Authority == usageevidence.EvidenceAuthorityAuthoritative {
		finalize.Cost = stream.Cost
		finalize.Authority = stream.Authority
		return finalize
	}
	if !compatibleBillingEvidenceProjection(finalize, stream) {
		return finalize
	}
	finalize.Cost = stream.Cost
	return finalize
}

func compatibleBillingEvidenceProjection(finalize, stream usageevidence.FinalBillingEvidence) bool {
	if finalize.Source == usageevidence.EvidenceSourceUnknown || finalize.Authority == usageevidence.EvidenceAuthorityUnknown {
		return false
	}
	if stream.Source == usageevidence.EvidenceSourceUnknown || stream.Authority == usageevidence.EvidenceAuthorityUnknown {
		return false
	}
	return finalize.Source == stream.Source && finalize.Authority == stream.Authority
}

func legOutcomeFromCommand(command sdkterminal.Command) usageevidence.LegOutcome {
	switch command {
	case sdkterminal.CommandNormalFinish:
		return usageevidence.LegOutcomeWinner
	case sdkterminal.CommandSwallowedAttempt:
		return usageevidence.LegOutcomeSwallowed
	case sdkterminal.CommandParallelLoser:
		return usageevidence.LegOutcomeLoser
	case sdkterminal.CommandCancel, sdkterminal.CommandClose, sdkterminal.CommandTimeout:
		return usageevidence.LegOutcomeCanceled
	default:
		return usageevidence.LegOutcomeFailed
	}
}

func turnOutcomeFromCommand(command sdkterminal.Command) usageevidence.TurnOutcome {
	switch command {
	case sdkterminal.CommandNormalFinish, sdkterminal.CommandEOF:
		return usageevidence.TurnOutcomeCompleted
	case sdkterminal.CommandCancel, sdkterminal.CommandClose, sdkterminal.CommandTimeout:
		return usageevidence.TurnOutcomeCanceled
	default:
		return usageevidence.TurnOutcomeFailed
	}
}

func billingSyntheticBLegID(seq int) string {
	return "seq_" + strconv.Itoa(seq)
}

func billingProviderID(primary routing.Primary) string {
	if provider := primary.TrimmedParam("provider"); provider != "" {
		return provider
	}
	backend := strings.TrimSpace(primary.Backend)
	if backend == "" {
		return "unknown"
	}
	return backend
}

func finalBillingEvidenceFromEvent(ev aipapi.Event) usageevidence.FinalBillingEvidence {
	return usageevidence.FinalBillingEvidence{
		InputTokens:      usageevidence.Quantity{Value: int64(ev.InputTokens), Present: ev.UsagePresence.InputTokens},
		OutputTokens:     usageevidence.Quantity{Value: int64(ev.OutputTokens), Present: ev.UsagePresence.OutputTokens},
		CacheReadTokens:  usageevidence.Quantity{Value: int64(ev.CacheReadTokens), Present: ev.UsagePresence.CacheReadTokens},
		CacheWriteTokens: usageevidence.Quantity{Value: int64(ev.CacheWriteTokens), Present: ev.UsagePresence.CacheWriteTokens},
		ReasoningTokens:  usageevidence.Quantity{Value: int64(ev.ReasoningTokens), Present: ev.UsagePresence.ReasoningTokens},
		TotalTokens:      usageevidence.Quantity{Value: int64(ev.TotalTokens), Present: ev.UsagePresence.TotalTokens},
		Cost:             usageevidence.MoneyEvidence{NanoUnits: ev.CostNanoUnits, Currency: strings.TrimSpace(ev.Currency), Present: ev.CostPresent},
		Source:           usageevidence.EvidenceSource(ev.Accounting.Source),
		Authority:        usageevidence.EvidenceAuthority(ev.Accounting.Authority),
		DedupeKey:        strings.TrimSpace(ev.Accounting.DedupeKey),
	}
}

func (e *Executor) appendIndependentCallLeg(ctx context.Context, callID usageevidence.BillingCallID, leg usageevidence.CallLegUsageRecord) {
	if err := e.appendIndependentCallLegStrict(ctx, callID, leg); err != nil {
		e.logBillingUsageAppendFailure(ctx, "billing_call_leg_append_critical", "billing call-leg append failed", err)
	}
}

// appendIndependentCallLegStrict is the terminal durability seam. It returns
// sink failures to the owning terminal effect while retaining the existing
// validation diagnostics and void observer wrapper for optional callers.
func (e *Executor) appendIndependentCallLegStrict(ctx context.Context, callID usageevidence.BillingCallID, leg usageevidence.CallLegUsageRecord) error {
	if !e.hasTerminalSink() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// AttemptSeq is the authoritative B2BUA financial fact; reject unknown
	// sequences rather than deriving order. Legacy NULL rows remain readable,
	// but order-dependent rating fails closed.
	if leg.AttemptSeq <= 0 {
		err := fmt.Errorf("%w: attempt sequence for B-leg %q", usageevidence.ErrInvalidRecord, leg.BLegID)
		if e.Log != nil {
			e.Log.ErrorContext(ctx, "billing call-leg append rejected: attempt sequence missing", "error", err, "b_leg_id", leg.BLegID)
		}
		return nil
	}
	leg.CallID = callID
	leg.Evidence = normalizeBillingEvidenceIdentity(leg.Evidence, callID, leg.BLegID)
	if err := usageevidence.ValidateIndependentLeg(leg); err != nil {
		if e.Log != nil {
			e.Log.ErrorContext(ctx, "billing call-leg append rejected: invalid independent leg", "error", err, "b_leg_id", leg.BLegID)
		}
		return nil
	}
	independent := leg.Clone()
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingHandoffTimeout)
	defer cancel()
	err := e.TerminalUsageSink.AppendLeg(persistCtx, independent)
	if err != nil {
		return fmt.Errorf("billing call-leg append: %w", err)
	}
	return nil
}

func (e *Executor) logBillingUsageAppendFailure(ctx context.Context, criticalMsg, warnMsg string, err error) {
	if e == nil || e.Log == nil || err == nil {
		return
	}
	if errors.Is(err, usageevidence.ErrReplayConflict) {
		e.Log.DebugContext(ctx, warnMsg, "error", err)
		return
	}
	e.Log.WarnContext(ctx, warnMsg, "error", err)
}

func (e *Executor) appendIndependentTerminalLeg(ctx context.Context, state *billingCallState, aLegID string, bleg b2bua.BLegRecord, primary routing.Primary, started, finished time.Time, outcome usageevidence.LegOutcome) {
	e.appendIndependentTerminalLegWithObservations(ctx, state, aLegID, bleg, primary, started, finished, outcome, nil)
}

func (e *Executor) appendIndependentTerminalLegWithObservations(ctx context.Context, state *billingCallState, aLegID string, bleg b2bua.BLegRecord, primary routing.Primary, started, finished time.Time, outcome usageevidence.LegOutcome, localObservations []metering.Observation) {
	if !e.hasTerminalSink() {
		return
	}
	// Frozen admission scope rides the handoff even on detached contexts.
	ctx = withTerminalScope(ctx, state.frozenScope())
	if started.IsZero() {
		started = finished
	}
	if finished.IsZero() {
		finished = started
	}
	backend := strings.TrimSpace(primary.Backend)
	if backend == "" {
		backend = "unknown"
	}
	model := strings.TrimSpace(primary.Model)
	if model == "" {
		model = "unknown"
	}
	leg := usageevidence.CallLegUsageRecord{
		SubmissionID: billingSubmissionID(state),
		ALegID:       strings.TrimSpace(aLegID), BLegID: strings.TrimSpace(bleg.BLegID), AttemptSeq: bleg.Seq,
		BackendID: backend, ProviderID: billingProviderID(primary), ModelID: model,
		StartedAt: started, FinishedAt: finished, Outcome: outcome, Surfaced: usageevidence.SurfacedNo,
		Evidence:        usageevidence.FinalBillingEvidence{Source: usageevidence.EvidenceSourceUnavailable, Authority: usageevidence.EvidenceAuthorityUnavailable},
		OperatorRateRef: e.operatorRateRef(ctx, primary),
		Workload:        e.billingWorkloadIdentityForALeg(ctx, aLegID),
	}
	if len(localObservations) != 0 {
		submissionID := billingSubmissionID(state)
		localConflicts := submissionLinkageConflictsForObservations(localObservations, submissionID)
		localInput := retainSubmissionLinkedObservations(localObservations, submissionID)
		leg.Observations, localConflicts, _ = appendCanonicalObservations(nil, localConflicts, localInput, usageevidence.MaxCallLegEvidenceObservations)
		if len(leg.Observations) != 0 || len(localConflicts) != 0 {
			leg.EvidenceVersion = usageevidence.EvidenceFormatVersionV2
			leg.EvidenceProjection = usageevidence.EvidenceProjectionV1
			leg.EvidenceConflicts = appendBoundedEvidenceConflicts(nil, localConflicts)
		}
	}
	var callID usageevidence.BillingCallID
	if state != nil {
		callID = state.callID
		leg.CallID = callID
		state.noteLegTimes(started, finished)
	}
	leg.Evidence = normalizeBillingEvidenceIdentity(leg.Evidence, leg.CallID, leg.BLegID)
	e.observeBillingLeg(ctx, leg)
	if callID != "" {
		e.appendIndependentCallLeg(ctx, callID, leg)
	}
}

func (e *Executor) appendPostOpenTerminalLeg(ctx context.Context, state *billingCallState, aLegID string, bleg b2bua.BLegRecord, primary routing.Primary, started, finished time.Time) {
	if e == nil || strings.TrimSpace(bleg.BLegID) == "" {
		return
	}
	// Frozen admission scope rides the handoff even on detached contexts.
	ctx = withTerminalScope(ctx, state.frozenScope())
	if started.IsZero() {
		started = e.now()
	}
	if finished.IsZero() {
		finished = e.now()
	}
	outcome := usageevidence.LegOutcomeFailed
	if ctx.Err() != nil {
		outcome = usageevidence.LegOutcomeCanceled
	}
	e.appendIndependentTerminalLeg(ctx, state, aLegID, bleg, primary, started, finished, outcome)
}

// appendPostOpenFallback appends the post-open terminal leg for an opened B-leg
// whose stream assembly failed before publication. It is a no-op unless this
// attempt still exclusively owns its unpublished session: once a lifecycle
// invalidation (cancel/close/dispose) has transferred terminal ownership, the
// ready capability has already detached the evidence-owning session, so
// BLeg/Candidate read empty here and no competing loss-less leg is appended.
// Execute and the wire/large-body path share this single decision.
func (e *Executor) appendPostOpenFallback(ctx context.Context, state *billingCallState, aLegID string, out openedAttempt) {
	if e == nil || out.ready == nil {
		return
	}
	bleg := out.ready.BLeg()
	if strings.TrimSpace(bleg.BLegID) == "" {
		return
	}
	e.appendPostOpenTerminalLeg(ctx, state, aLegID, bleg, out.ready.Candidate().Primary, time.Time{}, time.Time{})
}

func (e *Executor) recordParallelBillingLeg(ctx context.Context, leg *parallelLeg, usage aipapi.Event, command sdkterminal.Command, committed bool) {
	if e == nil || leg == nil || (e.BillingLegObserver == nil && !e.hasTerminalSink()) {
		return
	}
	if leg.startedAt.IsZero() {
		e.appendIndependentTerminalLeg(ctx, leg.billingCallState, leg.bleg.ALegID, leg.bleg, leg.cand.Primary, e.now(), e.now(), usageevidence.LegOutcomeNeverStarted)
		return
	}
	surfaced := usageevidence.SurfacedNo
	if committed {
		surfaced = usageevidence.SurfacedYes
	}
	fallback := lastUsageDeltaOrShell([]aipapi.Event{usage})
	finalizeEv, ok := leg.billingCallState.finalizeOnce(ctx, execbackend.BillingFinalizationInput{
		ALegID:  strings.TrimSpace(leg.bleg.ALegID),
		BLegID:  strings.TrimSpace(leg.bleg.BLegID),
		Backend: strings.TrimSpace(leg.cand.Primary.Backend),
		Model:   strings.TrimSpace(leg.cand.Primary.Model),
		Reason:  "parallel_loser",
	}, func(cctx context.Context, in execbackend.BillingFinalizationInput) (aipapi.Event, error) {
		return e.callFinalizeBilling(cctx, in)
	})
	if !ok {
		finalizeEv = fallback
	}
	if leg.billingCallState != nil {
		leg.billingCallState.noteLegTimes(leg.startedAt, e.now())
	}
	var callID usageevidence.BillingCallID
	if leg.billingCallState != nil {
		callID = leg.billingCallState.callID
	}
	legRecord := billingLegRecord(billingLegDraft{
		callID:          callID,
		submissionID:    billingSubmissionID(leg.billingCallState),
		aLegID:          leg.bleg.ALegID,
		storeID:         leg.storeID,
		bLegID:          leg.bleg.BLegID,
		seq:             leg.bleg.Seq,
		primary:         leg.cand.Primary,
		startedAt:       leg.startedAt,
		finishedAt:      e.now(),
		command:         command,
		surfaced:        surfaced,
		finalize:        finalizeEv,
		stream:          fallback,
		evidenceEvents:  []capturedBillingEvidence{{event: usage, role: billingEvidenceRoleStream}},
		operatorRateRef: e.operatorRateRef(ctx, leg.cand.Primary),
		workload:        e.billingWorkloadIdentityForALeg(ctx, leg.bleg.ALegID),
	})
	e.observeBillingLeg(ctx, legRecord)
	if leg.billingCallState != nil {
		e.appendIndependentCallLeg(ctx, leg.billingCallState.callID, legRecord)
	}
}
