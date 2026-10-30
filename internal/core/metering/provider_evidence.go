package metering

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipsdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// ProviderEvidenceBinder is implemented by an in-process provider stream that
// captured provider-shaped usage before the runtime assigned the trusted
// request/B-leg identity. The binding is deliberately one-way: provider data
// cannot choose its store, call, or B-leg subject.
type ProviderEvidenceBinder interface {
	BindEconomicEvidence(ObservationIdentity)
}

// ProviderEvidenceDraft is the provider-neutral portion of one provider
// observation. Provider adapters own construction of measures, charges and
// safe evidence; this type only makes the runtime identity boundary explicit.
// A draft cannot be drained as V2 until a trusted B-leg binding is supplied.
type ProviderEvidenceDraft struct {
	SourceEventKey string
	// Revision is a host-assigned observation revision. A caller/provider
	// supplied source revision is NOT a supported capability: Add rejects any
	// non-zero Revision as an explicit unsupported-ordering-identity loss and
	// never drains the payload as provider evidence. This is an enforceable
	// boundary, not a TODO: the SDK observation cannot carry source-vs-host
	// revision provenance, so a late older explicit revision would be
	// observationally identical to a valid cumulative/delta update and could
	// duplicate a charge. The buffer assigns the revision instead.
	Revision uint64
	StreamID string
	// Sequence is a host-assigned lifetime receipt ordinal. A caller/provider
	// supplied non-zero Sequence is part of the same unsupported ordering
	// identity and is rejected at Add with the same loss disposition; the buffer
	// assigns the sequence.
	Sequence           uint64
	Origin             string
	Acquisition        string
	Authority          string
	Perspective        aipsdkmetering.EconomicPerspective
	Boundary           aipsdkmetering.Boundary
	Lifecycle          aipsdkmetering.LifecycleScope
	Semantics          string
	ProviderAccountKey string
	ProviderRequestID  string
	ProviderChargeID   string
	Measures           []aipsdkmetering.Measure
	Charges            []aipsdkmetering.ReportedCharge
	Supersedes         []aipsdkmetering.ObservationRef
	Evidence           []aipsdkmetering.SafeEvidenceField
	ObservedAt         time.Time
	ReceivedAt         time.Time
	Coverage           string
	CoverageReason     string
}

// ProviderEvidenceBuffer retains bounded, immutable provider drafts until the
// executor binds the concrete B-leg. It deduplicates exact replay of one
// source payload while preserving changed payloads as revisions.
type ProviderEvidenceBuffer struct {
	mu              sync.Mutex
	identity        ObservationIdentity
	bound           bool
	drafts          []ProviderEvidenceDraft
	lastFingerprint map[string]string
	lastRevision    map[string]uint64
	lastObservation map[string]aipsdkmetering.Observation
	anchorSizes     map[string]int
	anchorBytes     int
	// pending counts drafts currently buffered before the bind-time drain. It is
	// deliberately independent of the lifetime sequence/revision generation
	// counters so a long stream is never permanently refused after enough
	// drains; only the ordinary pending cap bounds it.
	pending int
	// sequence and nextRevision are lifetime monotonic host counters. They are
	// never reset by Drain, so host receipt ordering and host-assigned revision
	// identity are reused for neither a later add nor a later drain.
	sequence     uint64
	nextRevision uint64
	// lossDrafts reserves bounded capacity, independent of the ordinary pending
	// cap, for explicit incomplete markers. Evidence loss must remain visible
	// even when the ordinary pending buffer is full.
	lossDrafts []ProviderEvidenceDraft
	lossCauses map[string]struct{}
}

const (
	maxProviderEvidenceDrafts = 256
	// maxProviderEvidenceLossMarkers bounds the reserved loss-marker capacity.
	// It is derived from the closed loss-disposition set so a new disposition
	// can never silently overflow the bounded marker list.
	maxProviderEvidenceLossMarkers = len(providerEvidenceLossCauses)
	// maxProviderEvidenceAnchors bounds the number of sources whose immutable
	// replay/supersession anchor is retained. A tracked source's anchor is never
	// evicted, so a reordered payload can never be promoted as new after capacity
	// pressure; a brand-new source is instead refused with an explicit incomplete
	// marker.
	maxProviderEvidenceAnchors = 256
	// maxProviderEvidenceAnchorBytes bounds total retained anchor bytes across
	// all tracked sources.
	maxProviderEvidenceAnchorBytes = 1 << 20
	// maxProviderEvidenceDraftBytes bounds one retained draft's metadata so an
	// oversized provider payload is rejected before unbounded metadata grows.
	maxProviderEvidenceDraftBytes = 64 * 1024

	// providerEvidenceStringBytes charges one string header per retained field
	// so a cardinality of zero-length strings cannot evade the byte budgets.
	providerEvidenceStringBytes = 16
	// providerEvidenceElementBytes charges one fixed overhead per nested slice
	// element (measure, charge, evidence field, supersession ref, dimension or
	// charge coverage ref), independent of its strings, so arbitrarily many
	// empty elements cannot pass the byte checks.
	providerEvidenceElementBytes = 48

	providerEvidenceLossStreamID = "provider.evidence.loss.v1"
	providerEvidenceLossSchemaID = "aip.provider.evidence.v1"
)

// Loss dispositions carried by an explicit provider-neutral unavailable
// observation. They are bounded identifiers, never raw provider text.
const (
	providerEvidenceLossPendingCapacity = "pending_capacity"
	providerEvidenceLossSourceHistory   = "source_history_capacity"
	providerEvidenceLossRetainedBytes   = "retained_bytes_capacity"
	providerEvidenceLossOversizedInput  = "oversized_input"
	// providerEvidenceLossUnsupportedOrderingIdentity is the typed degraded
	// disposition for a draft that supplies its own source Revision or Sequence.
	// The capability is unsupported at the enforceable boundary because an
	// explicit source revision cannot be distinguished from a host revision
	// once it is an Observation, so a late older explicit revision could
	// duplicate a charge. Rejection is visible and sticky: it drains as an
	// unavailable provider observation that keeps the reduction incomplete.
	providerEvidenceLossUnsupportedOrderingIdentity = "unsupported_ordering_identity"
	// providerEvidenceLossInvalidDraft is the typed disposition for a draft that
	// passed every Add-time bound but violates the SDK observation contract when
	// it is projected at drain time (for example a provider identifier one byte
	// past MaxSchemaIDBytes). The invalid payload is never persisted as an
	// observation or anchor: only this bounded provider-neutral marker survives,
	// so an accepted prefix cannot look complete.
	providerEvidenceLossInvalidDraft = "invalid_draft"
)

// providerEvidenceLossCauses is the closed set of every distinct loss
// disposition the buffer can record. Reserved loss-marker capacity is derived
// from its length so no admitted disposition can silently disappear.
var providerEvidenceLossCauses = [...]string{
	providerEvidenceLossPendingCapacity,
	providerEvidenceLossSourceHistory,
	providerEvidenceLossRetainedBytes,
	providerEvidenceLossOversizedInput,
	providerEvidenceLossUnsupportedOrderingIdentity,
	providerEvidenceLossInvalidDraft,
}

// NewProviderEvidenceBuffer creates a stream-local provider evidence buffer.
func NewProviderEvidenceBuffer() *ProviderEvidenceBuffer {
	return &ProviderEvidenceBuffer{
		lastFingerprint: make(map[string]string),
		lastRevision:    make(map[string]uint64),
		lastObservation: make(map[string]aipsdkmetering.Observation),
		anchorSizes:     make(map[string]int),
		lossCauses:      make(map[string]struct{}),
	}
}

// Add appends one provider-owned draft. Exact replay under the same source key
// is ignored; changed content is retained and receives a buffer-assigned
// revision. A draft that supplies its own source Revision or Sequence is
// rejected as an unsupported ordering identity (see the field docs).
func (b *ProviderEvidenceBuffer) Add(draft ProviderEvidenceDraft) {
	if b == nil {
		// Nil buffer remains a genuine no-op: there is no loss ledger to record
		// the rejection in.
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if draft.Revision != 0 || draft.Sequence != 0 {
		// A caller/provider supplied ordering identity is an unsupported
		// capability. The SDK observation cannot carry source-vs-host revision
		// provenance, so accepting a non-zero Revision or Sequence would let a
		// late older explicit revision masquerade as a valid cumulative/delta
		// update and duplicate a charge. Reject the payload as an explicit,
		// sticky degraded disposition instead of silently accepting a prefix.
		//
		// This gate is deliberately ordered before the empty-source-key no-op so
		// an unsupported-ordering draft that also omits its source key cannot
		// vanish silently: otherwise a healthy admitted prefix followed by such
		// a draft would leave no marker and could look complete.
		b.recordProviderEvidenceLossLocked(providerEvidenceLossUnsupportedOrderingIdentity)
		return
	}
	if strings.TrimSpace(draft.SourceEventKey) == "" {
		// A draft with no source identity is not addressable provider evidence.
		// It is neither admitted nor a loss: there is nothing to key, replay or
		// supersede. (Drafts carrying an explicit ordering identity were already
		// rejected above and are never treated as this benign no-op.)
		return
	}
	if providerEvidenceDraftBytes(draft) > maxProviderEvidenceDraftBytes {
		// Reject oversized metadata before it can grow any retained structure.
		b.recordProviderEvidenceLossLocked(providerEvidenceLossOversizedInput)
		return
	}
	// Sequence is a lifetime host receipt ordinal. It must never restart at one
	// after a drain, or later evidence would not order after earlier evidence in
	// the shared reducer.
	b.sequence++
	draft.Sequence = b.sequence
	if draft.StreamID == "" {
		draft.StreamID = "provider:" + draft.SourceEventKey
	}
	if draft.Origin == "" {
		draft.Origin = aipsdkmetering.OriginProvider
	}
	if draft.Acquisition == "" {
		draft.Acquisition = aipsdkmetering.AcquisitionProviderResponse
	}
	if draft.Authority == "" {
		draft.Authority = aipsdkmetering.AuthorityObservedClaim
	}
	if draft.Perspective == "" {
		draft.Perspective = aipsdkmetering.PerspectiveOperator
	}
	if draft.Boundary == "" {
		draft.Boundary = aipsdkmetering.BoundaryBackendEgress
	}
	if draft.Lifecycle == "" {
		draft.Lifecycle = aipsdkmetering.LifecycleBackendAttempt
	}
	if draft.Semantics == "" {
		draft.Semantics = aipsdkmetering.SemanticsCumulative
	}
	if draft.Coverage == "" {
		draft.Coverage = "complete"
	}

	key := strings.TrimSpace(draft.SourceEventKey)
	draft.SourceEventKey = key
	hash, err := draftFingerprint(draft)
	if err != nil {
		// A payload whose canonical encoding fails cannot participate in replay
		// suppression: an empty hash would otherwise compare equal to an absent
		// baseline and be misread as an exact duplicate, silently dropping a
		// brand-new source so an admitted prefix could reduce as complete.
		// Record the bounded sanitized loss before any comparison and retain
		// neither the malformed payload nor any partial anchor state.
		b.recordProviderEvidenceLossLocked(providerEvidenceLossInvalidDraft)
		return
	}
	if b.lastFingerprint == nil {
		b.lastFingerprint = make(map[string]string)
	}
	if b.lastRevision == nil {
		b.lastRevision = make(map[string]uint64)
	}
	if b.lastObservation == nil {
		b.lastObservation = make(map[string]aipsdkmetering.Observation)
	}
	// Replay suppression is deliberately consecutive. A provider can emit a
	// legitimate correction A -> B -> A; retaining every hash ever observed
	// would incorrectly discard the final A. Pending drafts are checked first,
	// then the last accepted drained payload supplies the cross-drain anchor.
	latest := b.lastFingerprint[key]
	for _, draft := range slices.Backward(b.drafts) {
		if draft.SourceEventKey == key {
			pendingHash, pendingErr := draftFingerprint(draft)
			if pendingErr != nil {
				// An accepted pending draft must always fingerprint. If the
				// baseline can no longer be encoded, never let the failure
				// compare equal to an empty hash; surface the loss and refuse
				// the payload rather than risk suppressing a real revision
				// against a baseline that could not be verified.
				b.recordProviderEvidenceLossLocked(providerEvidenceLossInvalidDraft)
				return
			}
			latest = pendingHash
			break
		}
	}
	if latest == hash {
		return
	}
	// Host-assigned revision generation is a lifetime monotonic counter so an
	// evicted or drained anchor can never cause a revision to be reused with a
	// changed payload.
	b.nextRevision++
	revision := b.nextRevision
	if prior, exists := b.lastRevision[key]; exists && prior >= revision {
		revision = prior + 1
	}
	draft.Revision = revision
	b.retainAcceptedDraftLocked(draft, hash)
}

// retainAcceptedDraftLocked appends one accepted draft under the ordinary
// pending cap and the bounded source-history policy. Capacity pressure is never
// silent: it is recorded as an explicit loss disposition.
func (b *ProviderEvidenceBuffer) retainAcceptedDraftLocked(draft ProviderEvidenceDraft, fingerprint string) {
	if b.pending >= maxProviderEvidenceDrafts {
		b.recordProviderEvidenceLossLocked(providerEvidenceLossPendingCapacity)
		return
	}
	if cause := b.sourceCapacityLossLocked(draft.SourceEventKey, fingerprint, providerEvidenceDraftBytes(draft)); cause != "" {
		b.recordProviderEvidenceLossLocked(cause)
		return
	}
	b.drafts = append(b.drafts, cloneProviderEvidenceDraft(draft))
	b.pending++
}

// sourceCapacityLossLocked reports the loss disposition when a draft cannot be
// retained under the bounded anchor policy, and empty otherwise. A brand-new
// source is charged its full prospective anchor; a tracked source is charged
// the replacement delta so anchor growth cannot bypass the byte cap.
func (b *ProviderEvidenceBuffer) sourceCapacityLossLocked(key, fingerprint string, draftBytes int) string {
	newSize := providerEvidenceAnchorBytes(key, fingerprint, draftBytes)
	if oldSize, anchored := b.anchorSizes[key]; anchored {
		if b.anchorBytes-oldSize+newSize > maxProviderEvidenceAnchorBytes {
			return providerEvidenceLossRetainedBytes
		}
		return ""
	}
	if b.sourceTrackedLocked(key) {
		// Tracked only through a pending, not-yet-drained draft. Its anchor is
		// admitted at drain time against its then-current size.
		return ""
	}
	if len(b.lastObservation) >= maxProviderEvidenceAnchors {
		return providerEvidenceLossSourceHistory
	}
	if b.anchorBytes+newSize > maxProviderEvidenceAnchorBytes {
		return providerEvidenceLossRetainedBytes
	}
	return ""
}

func (b *ProviderEvidenceBuffer) sourceTrackedLocked(key string) bool {
	if _, exists := b.lastObservation[key]; exists {
		return true
	}
	for i := range b.drafts {
		if b.drafts[i].SourceEventKey == key {
			return true
		}
	}
	return false
}

// AddUsageEvent maps the six legacy token counters and a genuine provider
// monetary field into a V2 draft. Family adapters call this after parsing
// their own wire schema; no provider-specific interpretation occurs here.
func (b *ProviderEvidenceBuffer) AddUsageEvent(ev aipapi.Event, mapping string) {
	if b == nil || ev.Kind != aipapi.EventUsageDelta {
		return
	}
	key := strings.TrimSpace(ev.Accounting.DedupeKey)
	if key == "" {
		key = strings.TrimSpace(mapping) + ":stream"
	}
	draft := ProviderEvidenceDraft{
		SourceEventKey:     key,
		StreamID:           strings.TrimSpace(mapping),
		Origin:             aipsdkmetering.OriginProvider,
		Acquisition:        aipsdkmetering.AcquisitionProviderResponse,
		Authority:          aipsdkmetering.AuthorityObservedClaim,
		Perspective:        aipsdkmetering.PerspectiveOperator,
		Boundary:           aipsdkmetering.BoundaryBackendEgress,
		Lifecycle:          aipsdkmetering.LifecycleBackendAttempt,
		Semantics:          aipsdkmetering.SemanticsCumulative,
		ProviderAccountKey: ev.Accounting.ProviderAccountKey,
		ProviderRequestID:  ev.Accounting.ProviderRequestID,
		ProviderChargeID:   ev.Accounting.ProviderChargeID,
		Measures:           providerTokenMeasures(ev, mapping),
		Evidence:           providerTokenEvidence(ev),
	}
	if ev.CostPresent && ev.Accounting.Source == aipapi.UsageSourceProviderReported && ev.CostNanoUnits >= 0 && strings.TrimSpace(ev.Currency) != "" {
		amount := aipsdkmetering.DecimalFromNanoUnits(ev.CostNanoUnits)
		draft.Charges = []aipsdkmetering.ReportedCharge{{
			ChargeItemID: "cost:" + key,
			Amount:       &amount,
			Currency:     strings.TrimSpace(ev.Currency),
			Kind:         aipsdkmetering.ChargeKindAggregate,
		}}
		draft.Evidence = append(draft.Evidence,
			aipsdkmetering.SafeEvidenceField{Path: "$.cost.amount", Lexeme: strconv.FormatInt(ev.CostNanoUnits, 10), Present: true, Acquisition: aipsdkmetering.AcquisitionProviderResponse},
			aipsdkmetering.SafeEvidenceField{Path: "$.cost.currency", Lexeme: strings.TrimSpace(ev.Currency), Present: true, Acquisition: aipsdkmetering.AcquisitionProviderResponse},
		)
	}
	if ev.Accounting.Source != "" {
		draft.Acquisition = providerAcquisition(ev.Accounting.Source)
	}
	if ev.Accounting.Authority != "" {
		draft.Authority = providerAuthority(ev.Accounting.Authority)
	}
	if strings.TrimSpace(ev.Accounting.ServiceContext) != "" {
		// Service context has no pricing meaning. Keep it as a bounded,
		// provider-owned lexeme under the safe evidence contract.
		draft.Evidence = append(draft.Evidence, aipsdkmetering.SafeEvidenceField{
			Path: "$.provider_schema.service_context", Lexeme: strings.TrimSpace(ev.Accounting.ServiceContext),
			Present: true, Acquisition: draft.Acquisition,
		})
	}
	b.Add(draft)
}

// BindEconomicEvidence supplies only trusted runtime lineage. Binding may be
// called once after Open; subsequent calls replace no already-drained data.
func (b *ProviderEvidenceBuffer) BindEconomicEvidence(identity ObservationIdentity) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.bound {
		b.mu.Unlock()
		return
	}
	b.identity = identity
	b.identity.StoreID = strings.TrimSpace(identity.StoreID)
	b.identity.RequestID = strings.TrimSpace(identity.RequestID)
	b.identity.CallID = strings.TrimSpace(identity.CallID)
	b.identity.BillingCallID = strings.TrimSpace(identity.BillingCallID)
	b.identity.ALegID = strings.TrimSpace(identity.ALegID)
	b.identity.BLegID = strings.TrimSpace(identity.BLegID)
	b.identity.AttemptID = strings.TrimSpace(identity.AttemptID)
	b.bound = true
	b.mu.Unlock()
}

// DrainEconomicObservations returns only valid, strictly B-leg-owned V2
// observations. An unbound stream returns no evidence rather than inventing a
// store or attaching provider quantities to a logical request.
func (b *ProviderEvidenceBuffer) DrainEconomicObservations() []aipsdkmetering.Observation {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.bound || b.identity.StoreID == "" || b.identity.BLegID == "" {
		return nil
	}
	identity := b.identity
	drafts := make([]ProviderEvidenceDraft, len(b.drafts))
	for i, draft := range b.drafts {
		drafts[i] = cloneProviderEvidenceDraft(draft)
	}
	b.drafts = nil
	b.pending = 0

	observations := make([]aipsdkmetering.Observation, 0, len(drafts))
	if b.lastRevision == nil {
		b.lastRevision = make(map[string]uint64)
	}
	if b.lastObservation == nil {
		b.lastObservation = make(map[string]aipsdkmetering.Observation)
	}
	if b.anchorSizes == nil {
		b.anchorSizes = make(map[string]int)
	}
	priorBySource := make(map[string]aipsdkmetering.Observation, len(b.lastObservation))
	for key, observation := range b.lastObservation {
		priorBySource[key] = observation.Clone()
	}
	for _, draft := range drafts {
		// Fingerprint the provider payload before adding host-owned supersession
		// metadata. Runtime timestamps, revisions and source relations must not
		// turn an unchanged provider snapshot into a new replay.
		payloadFingerprint, fingerprintErr := draftFingerprint(draft)
		if fingerprintErr != nil {
			// The draft passed Add-time bounds but cannot be canonically
			// encoded, so it can never become an immutable observation or
			// anchor. Record the bounded provider-neutral loss before building
			// any supersession edge and retain no raw field content.
			b.recordProviderEvidenceLossLocked(providerEvidenceLossInvalidDraft)
			continue
		}
		// A changed payload is a new immutable observation, but it must retain
		// the source relation to the last accepted revision. Build the relation
		// only after the prior observation has passed validation so malformed
		// drafts cannot become supersession anchors.
		if len(draft.Supersedes) == 0 {
			if prior, exists := priorBySource[draft.SourceEventKey]; exists {
				if ref, err := prior.Ref(identity.StoreID); err == nil {
					draft.Supersedes = []aipsdkmetering.ObservationRef{ref}
					draft.Semantics = aipsdkmetering.SemanticsReplacement
				}
			}
		}
		if len(draft.Supersedes) != 0 && draft.Semantics != aipsdkmetering.SemanticsCorrection && draft.Semantics != aipsdkmetering.SemanticsReplacement {
			// A superseding provider snapshot replaces the prior source payload;
			// V2 intentionally does not permit a cumulative observation to carry
			// an immutable supersession edge.
			draft.Semantics = aipsdkmetering.SemanticsReplacement
		}
		observation, err := providerObservationValidated(identity, draft)
		if err != nil {
			// A draft admitted by Add can still violate the SDK observation
			// contract when it is projected at drain time (for example a
			// provider identifier one byte past its bound). Silently dropping it
			// would leave an accepted prefix indistinguishable from a complete
			// stream, so the runtime could seal and rate that prefix as
			// complete. Record the bounded, provider-neutral loss marker and
			// retain neither the invalid identifier nor any other raw field.
			b.recordProviderEvidenceLossLocked(providerEvidenceLossInvalidDraft)
			continue
		}
		if b.rememberAnchorLocked(draft.SourceEventKey, payloadFingerprint, draft, observation) {
			priorBySource[draft.SourceEventKey] = observation
			observations = append(observations, observation)
		}
	}
	// Loss markers use reserved capacity independent of the ordinary pending
	// cap, so a full buffer still makes dropped evidence visible as an explicit
	// provider-neutral unavailable observation.
	for _, draft := range b.lossDrafts {
		if observation, err := providerObservationValidated(identity, draft); err == nil {
			observations = append(observations, observation)
		}
	}
	b.lossDrafts = nil
	b.lossCauses = make(map[string]struct{})
	return observations
}

func providerObservation(identity ObservationIdentity, draft ProviderEvidenceDraft) (aipsdkmetering.Observation, bool) {
	observation, err := providerObservationValidated(identity, draft)
	return observation, err == nil
}

func providerObservationValidated(identity ObservationIdentity, draft ProviderEvidenceDraft) (aipsdkmetering.Observation, error) {
	if strings.TrimSpace(identity.StoreID) == "" || strings.TrimSpace(identity.BLegID) == "" || strings.TrimSpace(draft.SourceEventKey) == "" {
		return aipsdkmetering.Observation{}, aipsdkmetering.ErrInvalidObservation
	}
	callID := strings.TrimSpace(identity.CallID)
	if callID == "" {
		callID = strings.TrimSpace(identity.BillingCallID)
	}
	billingCallID := strings.TrimSpace(identity.BillingCallID)
	if billingCallID == "" {
		billingCallID = callID
	}
	attemptID := strings.TrimSpace(identity.AttemptID)
	if attemptID == "" {
		attemptID = identity.BLegID
	}
	observedAt := draft.ObservedAt
	if observedAt.IsZero() {
		observedAt = identity.ObservedAt
	}
	if observedAt.IsZero() {
		observedAt = time.Unix(0, 0).UTC()
	}
	receivedAt := draft.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = identity.ReceivedAt
	}
	if receivedAt.IsZero() {
		receivedAt = observedAt
	}
	subject := aipsdkmetering.SubjectRef{
		Kind: aipsdkmetering.SubjectBLeg, StoreID: identity.StoreID,
		RequestID: identity.RequestID, CallID: callID, BillingCallID: billingCallID,
		ALegID: identity.ALegID, BLegID: identity.BLegID, AttemptID: attemptID,
		AttemptSeq: identity.AttemptSeq, ProviderAccountKey: draft.ProviderAccountKey,
		ProviderRequestID: draft.ProviderRequestID, ProviderChargeID: draft.ProviderChargeID,
	}
	correlation := aipsdkmetering.CorrelationV2{
		StoreID: identity.StoreID, RequestID: identity.RequestID, CallID: callID,
		BillingCallID: billingCallID, ALegID: identity.ALegID, BLegID: identity.BLegID,
		AttemptID: attemptID, AttemptSeq: identity.AttemptSeq,
		ProviderAccountKey: draft.ProviderAccountKey, ProviderRequestID: draft.ProviderRequestID,
		ProviderChargeID: draft.ProviderChargeID,
	}
	seed := strings.Join([]string{identity.StoreID, billingCallID, identity.BLegID, draft.StreamID, draft.SourceEventKey, strconv.FormatUint(draft.Revision, 10)}, "\x00")
	hash := sha256.Sum256([]byte(seed))
	observation := aipsdkmetering.Observation{
		Version:        aipsdkmetering.ObservationVersionV2,
		ID:             "aip-provider-" + hex.EncodeToString(hash[:]),
		SourceEventKey: draft.SourceEventKey, Revision: draft.Revision,
		StreamID: draft.StreamID, Sequence: draft.Sequence,
		Origin: draft.Origin, Acquisition: draft.Acquisition, Authority: draft.Authority,
		Perspective: draft.Perspective, Boundary: draft.Boundary, Lifecycle: draft.Lifecycle,
		Subject: subject, Correlation: correlation, Scope: identity.Scope.Clone(),
		Semantics: draft.Semantics, ObservedAt: observedAt.UTC(), ReceivedAt: receivedAt.UTC(),
		MappingRef: draft.StreamID, Measures: cloneMeasures(draft.Measures),
		Charges: cloneCharges(draft.Charges), Supersedes: append([]aipsdkmetering.ObservationRef(nil), draft.Supersedes...),
		Evidence: append([]aipsdkmetering.SafeEvidenceField(nil), draft.Evidence...),
	}
	if err := observation.Validate(); err != nil {
		return aipsdkmetering.Observation{}, err
	}
	return observation, nil
}

func ProviderUsageEvent(ev aipapi.Event, mapping, sourceKey string) ProviderEvidenceDraft {
	if strings.TrimSpace(sourceKey) == "" {
		sourceKey = strings.TrimSpace(ev.Accounting.DedupeKey)
	}
	if strings.TrimSpace(sourceKey) == "" {
		sourceKey = strings.TrimSpace(mapping) + ":stream"
	}
	draft := ProviderEvidenceDraft{
		SourceEventKey: sourceKey, StreamID: mapping, Measures: providerTokenMeasures(ev, mapping),
		Evidence: providerTokenEvidence(ev), ProviderAccountKey: ev.Accounting.ProviderAccountKey,
		ProviderRequestID: ev.Accounting.ProviderRequestID, ProviderChargeID: ev.Accounting.ProviderChargeID,
	}
	if ev.Accounting.Source != "" {
		draft.Acquisition = providerAcquisition(ev.Accounting.Source)
	}
	if ev.Accounting.Authority != "" {
		draft.Authority = providerAuthority(ev.Accounting.Authority)
	}
	if strings.TrimSpace(ev.Accounting.ServiceContext) != "" {
		draft.Evidence = append(draft.Evidence, aipsdkmetering.SafeEvidenceField{
			Path: "$.provider_schema.service_context", Lexeme: strings.TrimSpace(ev.Accounting.ServiceContext),
			Present: true, Acquisition: draft.Acquisition,
		})
	}
	return draft
}

func providerTokenMeasures(ev aipapi.Event, mapping string) []aipsdkmetering.Measure {
	quality := aipsdkmetering.QualityObserved
	method := strings.TrimSpace(mapping)
	if method == "" {
		method = "provider.usage.v2"
	}
	measures := make([]aipsdkmetering.Measure, 0, 6)
	appendToken := func(component string, direction aipsdkmetering.FlowDirection, value int, present bool) {
		if !present || value < 0 {
			return
		}
		decimal := aipsdkmetering.Decimal{Coefficient: strconv.Itoa(value)}
		measures = append(measures, aipsdkmetering.Measure{Key: aipsdkmetering.ComponentKey{Direction: direction, Component: component, Unit: aipsdkmetering.UnitToken, SchemaID: aipsdkmetering.DefaultInclusionSchemaID}, Value: &decimal, Quality: quality, MethodRef: method})
	}
	appendToken(aipsdkmetering.ComponentInputToken, aipsdkmetering.DirectionInput, ev.InputTokens, ev.UsagePresence.InputTokens)
	appendToken(aipsdkmetering.ComponentOutputToken, aipsdkmetering.DirectionOutput, ev.OutputTokens, ev.UsagePresence.OutputTokens)
	appendToken(aipsdkmetering.ComponentCacheReadInputToken, aipsdkmetering.DirectionInput, ev.CacheReadTokens, ev.UsagePresence.CacheReadTokens)
	appendToken(aipsdkmetering.ComponentCacheWriteInputToken, aipsdkmetering.DirectionInput, ev.CacheWriteTokens, ev.UsagePresence.CacheWriteTokens)
	appendToken(aipsdkmetering.ComponentReasoningOutputToken, aipsdkmetering.DirectionOutput, ev.ReasoningTokens, ev.UsagePresence.ReasoningTokens)
	appendToken(aipsdkmetering.ComponentTotalToken, aipsdkmetering.DirectionNone, ev.TotalTokens, ev.UsagePresence.TotalTokens)
	return measures
}

func providerTokenEvidence(ev aipapi.Event) []aipsdkmetering.SafeEvidenceField {
	fields := make([]aipsdkmetering.SafeEvidenceField, 0, 6)
	appendField := func(path string, value int, present bool) {
		if !present || value < 0 {
			return
		}
		fields = append(fields, aipsdkmetering.SafeEvidenceField{Path: path, Lexeme: strconv.Itoa(value), Present: true, Acquisition: aipsdkmetering.AcquisitionProviderResponse})
	}
	appendField("$.usage.input_tokens", ev.InputTokens, ev.UsagePresence.InputTokens)
	appendField("$.usage.output_tokens", ev.OutputTokens, ev.UsagePresence.OutputTokens)
	appendField("$.usage.cache_read_input_tokens", ev.CacheReadTokens, ev.UsagePresence.CacheReadTokens)
	appendField("$.usage.cache_write_input_tokens", ev.CacheWriteTokens, ev.UsagePresence.CacheWriteTokens)
	appendField("$.usage.reasoning_output_tokens", ev.ReasoningTokens, ev.UsagePresence.ReasoningTokens)
	appendField("$.usage.total_tokens", ev.TotalTokens, ev.UsagePresence.TotalTokens)
	return fields
}

func providerAcquisition(source aipapi.UsageSource) string {
	if source == aipapi.UsageSourceProviderCountAPI {
		return aipsdkmetering.AcquisitionProviderCountAPI
	}
	return aipsdkmetering.AcquisitionProviderResponse
}

func providerAuthority(authority aipapi.UsageAuthority) string {
	switch authority {
	case aipapi.UsageAuthorityEstimated, aipapi.UsageAuthorityAdvisory:
		return aipsdkmetering.AuthorityEstimatedClaim
	case aipapi.UsageAuthorityUnavailable:
		return aipsdkmetering.AuthorityUnavailableClaim
	default:
		return aipsdkmetering.AuthorityObservedClaim
	}
}

func draftFingerprint(draft ProviderEvidenceDraft) (string, error) {
	data, err := json.Marshal(struct {
		Key                string
		Stream             string
		Origin             string
		Acquisition        string
		Authority          string
		Perspective        aipsdkmetering.EconomicPerspective
		Boundary           aipsdkmetering.Boundary
		Lifecycle          aipsdkmetering.LifecycleScope
		Semantics          string
		ProviderAccountKey string
		ProviderRequestID  string
		ProviderChargeID   string
		Measures           []aipsdkmetering.Measure
		Charges            []aipsdkmetering.ReportedCharge
		Supersedes         []aipsdkmetering.ObservationRef
		Evidence           []aipsdkmetering.SafeEvidenceField
		Coverage           string
		CoverageReason     string
	}{
		draft.SourceEventKey, draft.StreamID, draft.Origin, draft.Acquisition,
		draft.Authority, draft.Perspective, draft.Boundary, draft.Lifecycle,
		draft.Semantics, draft.ProviderAccountKey, draft.ProviderRequestID,
		draft.ProviderChargeID, draft.Measures, draft.Charges, draft.Supersedes,
		draft.Evidence, draft.Coverage, draft.CoverageReason,
	})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// rememberAnchorLocked retains the bounded per-source replay/supersession
// anchor. A tracked source's anchor is immutable for the stream lifetime: when
// capacity is exhausted a new source is refused and its loss is recorded, so an
// older explicit revision can never be promoted as new evidence. A tracked
// source is charged the replacement delta (newSize minus its retained size) and
// refused without partial mutation when that would exceed the byte cap.
func (b *ProviderEvidenceBuffer) rememberAnchorLocked(key, fingerprint string, draft ProviderEvidenceDraft, observation aipsdkmetering.Observation) bool {
	if b == nil || key == "" {
		return false
	}
	if b.lastFingerprint == nil {
		b.lastFingerprint = make(map[string]string)
	}
	if b.anchorSizes == nil {
		b.anchorSizes = make(map[string]int)
	}
	newSize := providerEvidenceAnchorBytes(key, fingerprint, providerEvidenceDraftBytes(draft))
	oldSize, anchored := b.anchorSizes[key]
	if !anchored {
		if len(b.lastObservation) >= maxProviderEvidenceAnchors {
			b.recordProviderEvidenceLossLocked(providerEvidenceLossSourceHistory)
			return false
		}
		if b.anchorBytes+newSize > maxProviderEvidenceAnchorBytes {
			b.recordProviderEvidenceLossLocked(providerEvidenceLossRetainedBytes)
			return false
		}
	} else if b.anchorBytes-oldSize+newSize > maxProviderEvidenceAnchorBytes {
		// Refuse the replacement without mutating the retained anchor or its
		// replay/supersession state.
		b.recordProviderEvidenceLossLocked(providerEvidenceLossRetainedBytes)
		return false
	}
	if anchored {
		b.anchorBytes -= oldSize
	}
	b.lastObservation[key] = observation.Clone()
	b.lastRevision[key] = observation.Revision
	b.lastFingerprint[key] = fingerprint
	b.anchorSizes[key] = newSize
	b.anchorBytes += newSize
	return true
}

// recordProviderEvidenceLossLocked appends one bounded marker per distinct loss
// disposition. Markers are independent of the ordinary pending cap, so evidence
// loss is always visible rather than silently truncated.
func (b *ProviderEvidenceBuffer) recordProviderEvidenceLossLocked(cause string) {
	if cause == "" {
		return
	}
	if b.lossCauses == nil {
		b.lossCauses = make(map[string]struct{})
	}
	if _, exists := b.lossCauses[cause]; exists {
		return
	}
	if len(b.lossDrafts) >= maxProviderEvidenceLossMarkers {
		return
	}
	b.lossCauses[cause] = struct{}{}
	b.lossDrafts = append(b.lossDrafts, b.providerEvidenceLossDraftLocked(cause))
}

// providerEvidenceLossDraftLocked builds a provider-neutral unavailable
// observation carrying one bounded loss disposition. It reuses the existing
// incomplete evidence contract (unavailable authority plus an unusable measure)
// instead of inventing provider/customer provenance.
func (b *ProviderEvidenceBuffer) providerEvidenceLossDraftLocked(cause string) ProviderEvidenceDraft {
	b.nextRevision++
	b.sequence++
	return ProviderEvidenceDraft{
		SourceEventKey: "provider.evidence.loss:" + cause,
		Revision:       b.nextRevision,
		StreamID:       providerEvidenceLossStreamID,
		Sequence:       b.sequence,
		Origin:         aipsdkmetering.OriginProvider,
		Acquisition:    aipsdkmetering.AcquisitionProviderResponse,
		Authority:      aipsdkmetering.AuthorityUnavailableClaim,
		Perspective:    aipsdkmetering.PerspectiveOperator,
		Boundary:       aipsdkmetering.BoundaryBackendEgress,
		Lifecycle:      aipsdkmetering.LifecycleBackendAttempt,
		Semantics:      aipsdkmetering.SemanticsCumulative,
		Coverage:       "partial",
		CoverageReason: cause,
		Measures: []aipsdkmetering.Measure{{
			Key: aipsdkmetering.ComponentKey{
				Direction: aipsdkmetering.DirectionNone,
				Component: "provider_evidence_loss",
				Unit:      aipsdkmetering.UnitCount,
				SchemaID:  providerEvidenceLossSchemaID,
			},
			Quality:   aipsdkmetering.QualityUnavailable,
			MethodRef: providerEvidenceLossStreamID,
			Reason:    cause,
		}},
	}
}

// providerEvidenceAnchorBytes prices one prospective per-source anchor: the
// source key, its rendered payload fingerprint and the retained draft metadata.
func providerEvidenceAnchorBytes(key, fingerprint string, draftBytes int) int {
	return len(key) + len(fingerprint) + draftBytes
}

// providerEvidenceDraftBytes estimates the metadata a draft retains. It charges
// a fixed per-string and per-element overhead in addition to content lengths so
// a collection of empty elements cannot slip under the byte budgets, and it
// includes the Origin/Acquisition/Authority envelope fields and every nested
// measure, charge, dimension, coverage and evidence field, including every
// enum-backed and identifier string. It never serializes.
func providerEvidenceDraftBytes(draft ProviderEvidenceDraft) int {
	addString := func(size int, value string) int {
		return size + len(value) + providerEvidenceStringBytes
	}
	addElements := func(size, elements int) int {
		return size + elements*providerEvidenceElementBytes
	}
	size := 0
	for _, value := range []string{
		draft.SourceEventKey, draft.StreamID, draft.Origin, draft.Acquisition,
		draft.Authority, string(draft.Perspective), string(draft.Boundary),
		string(draft.Lifecycle), draft.Semantics, draft.ProviderAccountKey,
		draft.ProviderRequestID, draft.ProviderChargeID, draft.Coverage,
		draft.CoverageReason,
	} {
		size = addString(size, value)
	}
	size = addElements(size, len(draft.Measures))
	for i := range draft.Measures {
		measure := draft.Measures[i]
		size = addString(size, string(measure.Key.Direction))
		size = addString(size, measure.Key.Component)
		size = addString(size, measure.Key.Unit)
		size = addString(size, measure.Key.SchemaID)
		size = addString(size, measure.Quality)
		size = addString(size, measure.MethodRef)
		size = addString(size, measure.Reason)
		size = addElements(size, len(measure.Key.Dimensions))
		for _, dimension := range measure.Key.Dimensions {
			size = addString(size, dimension.Name)
			size = addString(size, dimension.Value)
		}
		if measure.Value != nil {
			size = addString(size, measure.Value.Coefficient)
		}
	}
	size = addElements(size, len(draft.Charges))
	for i := range draft.Charges {
		charge := draft.Charges[i]
		size = addString(size, charge.ChargeItemID)
		size = addString(size, charge.Currency)
		size = addString(size, string(charge.Kind))
		size = addString(size, string(charge.Payer.Kind))
		size = addString(size, charge.Payer.ID)
		if charge.Amount != nil {
			size = addString(size, charge.Amount.Coefficient)
		}
		if charge.Component != nil {
			size = addString(size, string(charge.Component.Direction))
			size = addString(size, charge.Component.Component)
			size = addString(size, charge.Component.Unit)
			size = addString(size, charge.Component.SchemaID)
			size = addElements(size, len(charge.Component.Dimensions))
			for _, dimension := range charge.Component.Dimensions {
				size = addString(size, dimension.Name)
				size = addString(size, dimension.Value)
			}
		}
		size = addElements(size, len(charge.Covers))
		for _, cover := range charge.Covers {
			size = addString(size, cover.Ref.StoreID)
			size = addString(size, cover.Ref.ObservationID)
			size = addString(size, cover.Ref.ChargeItemID)
			size = addString(size, string(cover.Relation))
		}
	}
	size = addElements(size, len(draft.Evidence))
	for i := range draft.Evidence {
		field := draft.Evidence[i]
		size = addString(size, field.Path)
		size = addString(size, field.Name)
		size = addString(size, field.Lexeme)
		size = addString(size, field.Value)
		size = addString(size, field.Acquisition)
		size = addString(size, field.Sanitizer)
	}
	size = addElements(size, len(draft.Supersedes))
	for i := range draft.Supersedes {
		ref := draft.Supersedes[i]
		size = addString(size, ref.StoreID)
		size = addString(size, ref.ObservationID)
		size = addString(size, ref.PayloadHash)
	}
	return size
}

func cloneProviderEvidenceDraft(in ProviderEvidenceDraft) ProviderEvidenceDraft {
	out := in
	out.Measures = cloneMeasures(in.Measures)
	out.Charges = cloneCharges(in.Charges)
	out.Supersedes = append([]aipsdkmetering.ObservationRef(nil), in.Supersedes...)
	out.Evidence = append([]aipsdkmetering.SafeEvidenceField(nil), in.Evidence...)
	return out
}

func cloneMeasures(in []aipsdkmetering.Measure) []aipsdkmetering.Measure {
	if in == nil {
		return nil
	}
	out := make([]aipsdkmetering.Measure, len(in))
	for i := range in {
		out[i] = in[i].Clone()
	}
	return out
}

func cloneCharges(in []aipsdkmetering.ReportedCharge) []aipsdkmetering.ReportedCharge {
	if in == nil {
		return nil
	}
	out := make([]aipsdkmetering.ReportedCharge, len(in))
	for i := range in {
		out[i] = in[i].Clone()
	}
	return out
}
