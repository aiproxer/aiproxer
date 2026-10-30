package execbackend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// ConnectorEconomicEvidencePolicy selects the host's reaction to connector
// economic evidence it cannot admit. The default preserves the client session
// with an explicit, durable degradation; the strict option refuses where a
// refusal is still safe.
type ConnectorEconomicEvidencePolicy string

const (
	// ConnectorEconomicEvidenceDegrade continues the request and records the
	// explicit capture loss. It is the default.
	ConnectorEconomicEvidenceDegrade ConnectorEconomicEvidencePolicy = "degrade"
	// ConnectorEconomicEvidenceStrict refuses the attempt with a typed error.
	ConnectorEconomicEvidenceStrict ConnectorEconomicEvidencePolicy = "strict"
)

// ParseConnectorEconomicEvidencePolicy validates one configured policy value.
// An empty value selects the default; every other value must be exact.
func ParseConnectorEconomicEvidencePolicy(value string) (ConnectorEconomicEvidencePolicy, error) {
	switch ConnectorEconomicEvidencePolicy(strings.TrimSpace(value)) {
	case "":
		return ConnectorEconomicEvidenceDegrade, nil
	case ConnectorEconomicEvidenceDegrade:
		return ConnectorEconomicEvidenceDegrade, nil
	case ConnectorEconomicEvidenceStrict:
		return ConnectorEconomicEvidenceStrict, nil
	default:
		return "", &InvalidConnectorEconomicEvidencePolicyError{Value: value}
	}
}

// InvalidConnectorEconomicEvidencePolicyError reports an unusable configured
// policy value. The value is echoed because it is operator configuration and
// the error surfaces as a startup failure.
type InvalidConnectorEconomicEvidencePolicyError struct {
	Value string
}

func (e *InvalidConnectorEconomicEvidencePolicyError) Error() string {
	return "execbackend: invalid connector economic evidence policy " + strconv.Quote(e.Value)
}

// ConnectorEvidenceRejection is the closed set of reasons a connector claim
// was rejected. Every token maps to exactly one rejection category and every
// category is reachable, so a rejection can never be reported without a
// category and no token can be orphaned.
type ConnectorEvidenceRejection string

const (
	// ConnectorEvidenceRejectedPeerNotHostBound is the P1 reason for a claim
	// from a peer that did not negotiate the host-bound evidence feature.
	ConnectorEvidenceRejectedPeerNotHostBound ConnectorEvidenceRejection = "peer_not_host_bound"
	// ConnectorEvidenceRejectedTrustAttemptIdentityConflict is the P2 trust
	// reason for a claim that contradicts the trusted attempt identity.
	ConnectorEvidenceRejectedTrustAttemptIdentityConflict ConnectorEvidenceRejection = "trust_attempt_identity_conflict"
	// ConnectorEvidenceRejectedSourceFingerprintMismatch is the P2 reference
	// reason for a connector reference whose payload hash does not match the
	// predecessor's source fingerprint.
	ConnectorEvidenceRejectedSourceFingerprintMismatch ConnectorEvidenceRejection = "source_fingerprint_mismatch"
	// ConnectorEvidenceRejectedScopeConflict is the P2 reference reason for a
	// connector reference whose predecessor pair fails the SDK scope rule.
	ConnectorEvidenceRejectedScopeConflict ConnectorEvidenceRejection = "scope_conflict"
	// ConnectorEvidenceRejectedPredecessorAbsent is the P2 reference reason for
	// a claim whose predecessor was still unresolved at the terminal drain.
	ConnectorEvidenceRejectedPredecessorAbsent ConnectorEvidenceRejection = "predecessor_absent"
	// ConnectorEvidenceRejectedLocalIDConflict is the P2 replay/identity reason
	// for a connector-local key reused with a different source event.
	ConnectorEvidenceRejectedLocalIDConflict ConnectorEvidenceRejection = "local_id_conflict"
	// ConnectorEvidenceRejectedPayloadConflict is the P2 replay/identity reason
	// for a changed payload under one bound identity.
	ConnectorEvidenceRejectedPayloadConflict ConnectorEvidenceRejection = "payload_conflict"
	// ConnectorEvidenceRejectedCapacity is the P3 reason for a genuinely new
	// source identity at the attempt-lifetime logical-item cap.
	ConnectorEvidenceRejectedCapacity ConnectorEvidenceRejection = "capacity"
)

// Connector evidence rejection categories. The categories are the closed
// policy surface: P1 is an unsupported peer, P2 covers every claim rejection
// and P3 is the capacity rejection.
const (
	// ConnectorEvidenceCategoryP1 is the unsupported-peer category.
	ConnectorEvidenceCategoryP1 = "p1"
	// ConnectorEvidenceCategoryP2 is the rejected-claim category.
	ConnectorEvidenceCategoryP2 = "p2"
	// ConnectorEvidenceCategoryP3 is the capacity category.
	ConnectorEvidenceCategoryP3 = "p3"
)

// ConnectorEvidenceRejectionCategoryOf maps one closed reason token onto its
// category. The mapping is total over the closed set.
func ConnectorEvidenceRejectionCategoryOf(reason ConnectorEvidenceRejection) (string, bool) {
	switch reason {
	case ConnectorEvidenceRejectedPeerNotHostBound:
		return ConnectorEvidenceCategoryP1, true
	case ConnectorEvidenceRejectedTrustAttemptIdentityConflict,
		ConnectorEvidenceRejectedSourceFingerprintMismatch,
		ConnectorEvidenceRejectedScopeConflict,
		ConnectorEvidenceRejectedPredecessorAbsent,
		ConnectorEvidenceRejectedLocalIDConflict,
		ConnectorEvidenceRejectedPayloadConflict:
		return ConnectorEvidenceCategoryP2, true
	case ConnectorEvidenceRejectedCapacity:
		return ConnectorEvidenceCategoryP3, true
	default:
		return "", false
	}
}

const (
	// HostBoundObservationIDPrefix is the reserved host namespace for
	// host-bound connector observations. It is distinct from the in-process
	// provider and local-boundary namespaces.
	HostBoundObservationIDPrefix = "aip-v2-"
	// hostBoundObservationIDTag is the fixed version tag of the durable
	// identity preimage.
	hostBoundObservationIDTag = "aip-v2-source-id:v1"
	// DefaultMaxHostBoundLogicalItems bounds the attempt-lifetime host-binding
	// map. It matches the per-attempt economic evidence cap.
	DefaultMaxHostBoundLogicalItems = 1024
)

// ProviderLocalKey identifies one connector-local observation revision. The
// connector's own label is never a durable identity; it is only the local
// uniqueness key and the reference lookup key.
type ProviderLocalKey struct {
	// StreamID is the connector's stream identifier.
	StreamID string
	// ObservationID is the connector's own local observation identifier.
	ObservationID string
	// Revision is the connector's own local revision.
	Revision uint64
}

// String renders the local key for map indexing. It is not a durable identity.
func (k ProviderLocalKey) String() string {
	return strings.Join([]string{k.StreamID, k.ObservationID, strconv.FormatUint(k.Revision, 10)}, "\x00")
}

// referenceLookupKey is the key used to find a bound predecessor from a
// connector reference. A connector reference names only the local observation
// id and the local revision, so the stream dimension cannot be part of the
// lookup; an ambiguous lookup is treated as "not found" by the caller.
func (k ProviderLocalKey) referenceLookupKey() string {
	return k.ObservationID + "\x00" + strconv.FormatUint(k.Revision, 10)
}

// ProviderBindingIdentity carries the trusted host facts required to convert
// an opt-in connector-supplied provider claim into a host-bound observation.
type ProviderBindingIdentity struct {
	// StoreID is the trusted store of the attempt.
	StoreID string
	// TenantID is the trusted tenant of the attempt.
	TenantID string
	// RequestID is the trusted request identity.
	RequestID string
	// CallID is the trusted call identity.
	CallID string
	// BillingCallID is the trusted billing call identity.
	BillingCallID string
	// ALegID is the trusted A-leg identity.
	ALegID string
	// BLegID is the trusted B-leg identity.
	BLegID string
	// AttemptID is the trusted attempt identity.
	AttemptID string
	// AttemptSeq is the trusted attempt sequence.
	AttemptSeq uint64
	// ConnectorLegPseudonym is the transport B-leg pseudonym the host supplied
	// to the connector. A connector claim naming it is accepted and replaced
	// by the sealed B-leg.
	ConnectorLegPseudonym string
	// AccountID is the credited billing account frozen for this call. It is
	// empty when no account was admitted.
	AccountID string
	// Scope is the customer scope frozen at exposure admission.
	Scope scope.PrincipalScopeView
	// ScopePresent reports whether a trusted scope was actually frozen for
	// this call. It is read from the carrier, never inferred from the view.
	ScopePresent bool
	// ReceivedAt is the host's first receipt time for the claim.
	ReceivedAt time.Time
}

// BoundProviderClaim is one connector claim bound to trusted host facts.
type BoundProviderClaim struct {
	// Observation is the host-bound observation.
	Observation metering.Observation
	// ClaimKey is the host identity key. It is the replay, conflict and
	// capacity key; the connector's own label is never a key.
	ClaimKey string
	// HostObservationID is the durable host-namespaced identity.
	HostObservationID string
	// LocalKey is the connector-local key of the claim.
	LocalKey ProviderLocalKey
	// SourceFingerprint is the connector's own full fingerprint, computed
	// before the host stamp. It is used only to verify a connector reference.
	SourceFingerprint string
	// SourceReplayFingerprint is the connector's own replay-stable
	// fingerprint, computed before the host stamp.
	SourceReplayFingerprint string
	// ScopeDigest is the canonical scope projection digest.
	ScopeDigest string
	// Overrides lists the bounded host-owned dimensions the connector claimed
	// and the host overwrote. The tokens are closed and carry no value.
	Overrides []string
}

// ProviderBindingRejection is one closed rejection of a connector claim that
// was decided while the claim was being released. It carries the closed reason
// token and the host identity of the rejected claim.
//
// The record is internal to the host. It never carries a raw reference, a
// payload, a revision, a connector text or a hash of a target: the source
// fingerprint is the connector's own pre-stamp fingerprint of the rejected
// claim and never reaches the runtime, a log or a client.
type ProviderBindingRejection struct {
	// Reason is the closed rejection token of the rejected claim.
	Reason ConnectorEvidenceRejection
	// ClaimKey is the host identity key of the rejected claim.
	ClaimKey string
	// HostObservationID is the durable host-namespaced identity of the
	// rejected claim.
	HostObservationID string
	// ReceivedAt is the claim's exact first host receipt.
	ReceivedAt time.Time
	// SourceFingerprint is the connector's own pre-stamp fingerprint of the
	// rejected claim.
	SourceFingerprint string
}

// ProviderBindingOutcome is the result of one bind attempt.
type ProviderBindingOutcome struct {
	// Bound is the bound claim. It is populated for an admitted claim, an
	// exact replay and a held claim.
	Bound BoundProviderClaim
	// Reason is the closed rejection token. It is empty when the claim was
	// admitted, replayed or held.
	Reason ConnectorEvidenceRejection
	// Replay reports an exact replay. The first bound observation and its
	// first receipt are reused and no new entry was created.
	Replay bool
	// Held reports that the claim is bound but awaits its predecessor.
	Held bool
	// Released lists claims that were held and became resolvable because this
	// claim bound their predecessor.
	Released []BoundProviderClaim
	// ReleasedRejections lists the claims that were held and rejected while
	// this claim released them. Every rejected claim is reported exactly
	// once: a rejection decided inside the release path is never dropped.
	ReleasedRejections []ProviderBindingRejection
}

// Admitted reports whether the outcome carries a usable bound claim.
func (o ProviderBindingOutcome) Admitted() bool {
	return o.Reason == ""
}

// ScopeDigest returns the canonical digest of the frozen scope projection. The
// projection carries the carrier's own presence fact, so an absent scope and a
// present, all-unknown scope produce different digests.
func ScopeDigest(view scope.PrincipalScopeView, present bool) string {
	projection := struct {
		Present bool                     `json:"present"`
		View    scope.PrincipalScopeView `json:"view"`
	}{Present: present, View: view.Clone()}
	encoded, err := json.Marshal(projection)
	if err != nil {
		// The scope view is a closed, always-encodable value. A failure here
		// would mean the projection is unencodable, in which case no distinct
		// identity can be produced and the empty digest is the only safe value.
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// HostBoundObservationID returns the durable host identity for one bound
// observation. The preimage joins a fixed version tag, the SDK's canonical
// identity and the fixed-length scope digest, so distinct trusted states
// produce distinct preimages. The rendered id is collision-resistant, not
// mathematically injective.
func HostBoundObservationID(hostIdentity, scopeDigest string) string {
	sum := sha256.Sum256([]byte(hostBoundObservationIDTag + "\x00" + hostIdentity + "\x00" + scopeDigest))
	return HostBoundObservationIDPrefix + hex.EncodeToString(sum[:])
}

// providerBindingEntry is one attempt-lifetime map entry. A map entry and the
// observation it holds are one logical item; a held claim is a subset of the
// entries and is never counted twice.
type providerBindingEntry struct {
	claimKey          string
	localKey          ProviderLocalKey
	hostObservationID string
	hostRevision      uint64
	// hostReplayFingerprint is the replay-stable hash of the host-bound
	// content without the host-derived references. It decides replay equality
	// and conflict detection.
	hostReplayFingerprint string
	// hostReferenceFingerprint is the replay-stable hash of the bound
	// observation including its verified host references. It is the payload
	// hash the host publishes for a reference to this claim.
	hostReferenceFingerprint string
	sourceFingerprint        string
	// sourceReplayFingerprint is the connector's own replay-stable hash,
	// computed before the host stamp. It is used only to verify a connector
	// reference.
	sourceReplayFingerprint string
	ordinal                 uint64
	bound                   metering.Observation
	// pendingRefs keeps the connector's own references until they can be
	// verified and substituted.
	pendingRefs []metering.ObservationRef
	// held marks a claim that is bound but awaits its predecessor.
	held bool
	// waitingOn is the reference lookup key of the unresolved predecessor.
	waitingOn string
}

// SourceReferenceMap is the bounded, attempt-lifetime store of host-bound
// connector claims. It survives every runtime drain, is never persisted and
// never evicts: capacity pressure rejects a genuinely new claim instead.
type SourceReferenceMap struct {
	mu       sync.Mutex
	maxItems int
	trusted  ProviderBindingIdentity

	byClaim map[string]*providerBindingEntry
	byLocal map[string]string
	byRef   map[string][]string
	// waitingOn maps a reference lookup key onto the held claims that wait
	// for it.
	waitingOn map[string][]string

	// nextOrdinal is the lifetime monotonic host receipt ordinal counter. It
	// is never reset by a drain.
	nextOrdinal uint64
}

// NewSourceReferenceMap creates an attempt-lifetime host-binding map. A
// non-positive limit selects the default logical-item cap.
func NewSourceReferenceMap(maxItems int) *SourceReferenceMap {
	if maxItems <= 0 {
		maxItems = DefaultMaxHostBoundLogicalItems
	}
	return &SourceReferenceMap{
		maxItems:  maxItems,
		byClaim:   make(map[string]*providerBindingEntry),
		byLocal:   make(map[string]string),
		byRef:     make(map[string][]string),
		waitingOn: make(map[string][]string),
	}
}

// Len reports the number of logical items currently held.
func (m *SourceReferenceMap) Len() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byClaim)
}

// Bind binds one connector claim into the host namespace.
//
// The whole decision is taken under one lock and has two phases. The
// validation phase decides the trust, the replay, the conflict, the capacity
// and the reference questions and mutates nothing. The commit phase applies
// every mutation at once: the host receipt ordinal is allocated here and
// only here, the identity is assigned and the entry is inserted. A rejected
// claim therefore consumes no id, no ordinal and no reference slot.
func (m *SourceReferenceMap) Bind(claim metering.Observation, trusted ProviderBindingIdentity) ProviderBindingOutcome {
	if m == nil {
		return ProviderBindingOutcome{Reason: ConnectorEvidenceRejectedCapacity}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// ---- validation phase: no map mutation and no counter mutation ----
	// The attempt-identity trust rule decides the reason of a conflicting
	// claim, so it runs before the mechanical encoding check: the SDK's own
	// cross-carrier rule rejects a claim whose correlation carrier
	// contradicts its subject carrier, and that rejection would otherwise
	// mask the trust reason with the replay/identity token.
	if reason, ok := connectorClaimTrustConflict(claim, trusted); !ok {
		return ProviderBindingOutcome{Reason: reason}
	}
	sourceFingerprint := claim.Fingerprint()
	sourceReplayFingerprint, replayErr := claim.ReplayFingerprint()
	if sourceFingerprint == "" || replayErr != nil || sourceReplayFingerprint == "" {
		// A claim that cannot be canonically encoded cannot participate in
		// replay suppression or reference verification.
		return ProviderBindingOutcome{Reason: ConnectorEvidenceRejectedPayloadConflict}
	}

	bound, claimKey, hostID, scopeDigest, overrides := stampBoundObservation(claim, trusted)
	bound.ID = hostID
	localKey := ProviderLocalKey{
		StreamID:      claim.StreamID,
		ObservationID: claim.ID,
		Revision:      claim.Revision,
	}

	if existing, ok := m.byClaim[claimKey]; ok {
		// The replay decision is taken on the host-bound content: the host
		// lineage, the host-assigned ordinal and the connector's own payload.
		// Two deliveries of one source event that differ only in the
		// connector's own lineage are therefore the same claim.
		bound.Sequence = existing.ordinal
		bound.ID = hostID
		fingerprint := hostReplayFingerprint(bound)
		if existing.hostReplayFingerprint == fingerprint {
			return ProviderBindingOutcome{
				Bound: BoundProviderClaim{
					Observation:             existing.bound.Clone(),
					ClaimKey:                existing.claimKey,
					HostObservationID:       existing.hostObservationID,
					LocalKey:                existing.localKey,
					SourceFingerprint:       existing.sourceFingerprint,
					SourceReplayFingerprint: existing.sourceReplayFingerprint,
					ScopeDigest:             scopeDigest,
				},
				Replay: true,
			}
		}
		// A changed payload under one bound identity preserves the first
		// binding and rejects the later claim.
		return ProviderBindingOutcome{Reason: ConnectorEvidenceRejectedPayloadConflict}
	}

	localKeyIndex := localKey.String()
	if owner, ok := m.byLocal[localKeyIndex]; ok && owner != claimKey {
		// The connector reused a local key for a different source event. The
		// first binding is preserved.
		return ProviderBindingOutcome{Reason: ConnectorEvidenceRejectedLocalIDConflict}
	}

	if len(m.byClaim) >= m.maxItems {
		// Only a genuinely new source identity reaches the overflow path. An
		// exact replay and a conflicting reuse are decided above.
		return ProviderBindingOutcome{Reason: ConnectorEvidenceRejectedCapacity}
	}

	// The reference pre-validation runs against the same stamped observation
	// the commit produces. The observation's validity, the supersession graph
	// and the supersession scope never read the sequence, so the decision is
	// the one the commit would take.
	storeID := m.trusted.StoreID
	if storeID == "" {
		storeID = trusted.StoreID
	}
	provisional := &providerBindingEntry{
		claimKey:                claimKey,
		localKey:                localKey,
		hostObservationID:       hostID,
		hostRevision:            bound.Revision,
		sourceFingerprint:       sourceFingerprint,
		sourceReplayFingerprint: sourceReplayFingerprint,
		bound:                   bound.Clone(),
		pendingRefs:             append([]metering.ObservationRef(nil), claim.Supersedes...),
	}
	refs, reason, unresolved := m.resolveReferencesLocked(provisional, provisional, storeID)
	if reason != "" {
		// The whole claim is rejected before anything was mutated: it
		// consumes no id, no ordinal and no reference slot.
		return ProviderBindingOutcome{Reason: reason}
	}

	// ---- commit phase: every mutation happens here, under the same lock ----
	sequence := claim.Sequence
	if sequence == 0 {
		m.nextOrdinal++
		sequence = m.nextOrdinal
	}
	bound.Sequence = sequence
	bound.ID = hostID

	hostReplay := hostReplayFingerprint(bound)
	entry := &providerBindingEntry{
		claimKey:                claimKey,
		localKey:                localKey,
		hostObservationID:       hostID,
		hostRevision:            bound.Revision,
		hostReplayFingerprint:   hostReplay,
		sourceFingerprint:       sourceFingerprint,
		sourceReplayFingerprint: sourceReplayFingerprint,
		ordinal:                 sequence,
		bound:                   bound.Clone(),
		pendingRefs:             append([]metering.ObservationRef(nil), claim.Supersedes...),
	}
	m.byClaim[claimKey] = entry
	m.byLocal[localKeyIndex] = claimKey
	refKey := localKey.referenceLookupKey()
	m.byRef[refKey] = append(m.byRef[refKey], claimKey)
	if m.trusted.StoreID == "" {
		m.trusted = trusted
	}

	outcome := ProviderBindingOutcome{
		Bound: BoundProviderClaim{
			Observation:             bound.Clone(),
			ClaimKey:                claimKey,
			HostObservationID:       hostID,
			LocalKey:                localKey,
			SourceFingerprint:       sourceFingerprint,
			SourceReplayFingerprint: sourceReplayFingerprint,
			ScopeDigest:             scopeDigest,
			Overrides:               overrides,
		},
	}

	if unresolved != "" {
		// The predecessor has not arrived yet. The claim is held: it is bound
		// and counted once, and it is released as soon as the predecessor
		// arrives.
		entry.held = true
		entry.waitingOn = unresolved
		m.waitingOn[unresolved] = append(m.waitingOn[unresolved], claimKey)
		outcome.Held = true
		return outcome
	}

	entry.bound.Supersedes = refs
	entry.hostReferenceFingerprint = recomputeReplayFingerprint(entry.bound)
	outcome.Bound.Observation = entry.bound.Clone()

	// A newly bound predecessor releases every held claim that was waiting
	// for it, immediately and within the same attempt. A held claim that
	// cannot be verified is rejected, and the rejection is reported: it is
	// never dropped.
	released, rejections := m.releaseWaitersLocked(refKey, storeID)
	outcome.Released = released
	outcome.ReleasedRejections = rejections
	return outcome
}

// removeEntryLocked removes one entry and every index reference to it. It is
// used when a claim is rejected after its provisional insertion, so a rejected
// claim leaves no partial id, ordinal, reference or map mutation.
func (m *SourceReferenceMap) removeEntryLocked(entry *providerBindingEntry) {
	if entry == nil {
		return
	}
	delete(m.byClaim, entry.claimKey)
	if owner, ok := m.byLocal[entry.localKey.String()]; ok && owner == entry.claimKey {
		delete(m.byLocal, entry.localKey.String())
	}
	refKey := entry.localKey.referenceLookupKey()
	keys := m.byRef[refKey]
	for i, key := range keys {
		if key == entry.claimKey {
			keys = append(keys[:i], keys[i+1:]...)
			break
		}
	}
	if len(keys) == 0 {
		delete(m.byRef, refKey)
	} else {
		m.byRef[refKey] = keys
	}
	if entry.waitingOn != "" {
		waiting := m.waitingOn[entry.waitingOn]
		for i, key := range waiting {
			if key == entry.claimKey {
				waiting = append(waiting[:i], waiting[i+1:]...)
				break
			}
		}
		if len(waiting) == 0 {
			delete(m.waitingOn, entry.waitingOn)
		} else {
			m.waitingOn[entry.waitingOn] = waiting
		}
	}
}

// releaseWaitersLocked releases the held claims that were waiting for the
// just-bound reference key. A released claim that cannot be verified is
// rejected with its closed reason token, and the rejection is reported: it is
// never dropped.
func (m *SourceReferenceMap) releaseWaitersLocked(refKey string, storeID string) ([]BoundProviderClaim, []ProviderBindingRejection) {
	pending := m.waitingOn[refKey]
	if len(pending) == 0 {
		return nil, nil
	}
	var released []BoundProviderClaim
	var rejections []ProviderBindingRejection
	queue := append([]string(nil), pending...)
	delete(m.waitingOn, refKey)
	for len(queue) > 0 {
		claimKey := queue[0]
		queue = queue[1:]
		entry, ok := m.byClaim[claimKey]
		if !ok || !entry.held {
			continue
		}
		refs, reason, unresolved := m.resolveReferencesLocked(entry, nil, storeID)
		if unresolved != "" {
			// The predecessor is still held. The claim keeps waiting on it.
			entry.waitingOn = unresolved
			m.waitingOn[unresolved] = append(m.waitingOn[unresolved], claimKey)
			continue
		}
		if reason != "" {
			// The whole claim is rejected: no raw reference, no synthetic
			// reference and no invalid correction is ever serialized. The
			// closed reason, the host identity, the first host receipt and the
			// connector's own source fingerprint are reported to the caller.
			// No raw reference, payload or connector text is carried.
			rejections = append(rejections, ProviderBindingRejection{
				Reason:            reason,
				ClaimKey:          entry.claimKey,
				HostObservationID: entry.hostObservationID,
				ReceivedAt:        entry.bound.ReceivedAt,
				SourceFingerprint: entry.sourceFingerprint,
			})
			m.removeEntryLocked(entry)
			continue
		}
		entry.held = false
		entry.waitingOn = ""
		entry.bound.Supersedes = refs
		entry.hostReferenceFingerprint = recomputeReplayFingerprint(entry.bound)
		released = append(released, BoundProviderClaim{
			Observation:             entry.bound.Clone(),
			ClaimKey:                entry.claimKey,
			HostObservationID:       entry.hostObservationID,
			LocalKey:                entry.localKey,
			SourceFingerprint:       entry.sourceFingerprint,
			SourceReplayFingerprint: entry.sourceReplayFingerprint,
		})
		// A released claim can unblock its own successors immediately.
		queue = append(queue, m.waitingOn[entry.localKey.referenceLookupKey()]...)
		delete(m.waitingOn, entry.localKey.referenceLookupKey())
	}
	return released, rejections
}

// resolveReferencesLocked verifies the connector's references against the bound
// claims. It returns the substituted host references, a closed rejection
// reason, or the lookup key of an unresolved predecessor.
//
// The pendingInsert argument is the entry that is not in the map yet: when it
// is set, the reference lookup counts it as inserted, so the lookup sees the
// same key set it would see after the insertion and an ambiguous reference
// stays ambiguous.
func (m *SourceReferenceMap) resolveReferencesLocked(entry *providerBindingEntry, pendingInsert *providerBindingEntry, storeID string) ([]metering.ObservationRef, ConnectorEvidenceRejection, string) {
	if len(entry.pendingRefs) == 0 {
		return nil, "", ""
	}
	out := make([]metering.ObservationRef, 0, len(entry.pendingRefs))
	for _, ref := range entry.pendingRefs {
		prior, ok := m.lookupReferenceLocked(ref, pendingInsert)
		if !ok {
			// The predecessor is unknown, cross-attempt or not yet arrived.
			return nil, "", ProviderLocalKey{ObservationID: ref.ObservationID, Revision: ref.Revision}.referenceLookupKey()
		}
		if prior.held {
			// The predecessor is bound but not yet released, so it is not a
			// canonical observation yet. The claim keeps waiting for it.
			return nil, "", prior.localKey.referenceLookupKey()
		}
		if !referenceHashMatches(ref, prior) {
			return nil, ConnectorEvidenceRejectedSourceFingerprintMismatch, ""
		}
		hostRef := metering.ObservationRef{
			StoreID:       storeID,
			ObservationID: prior.hostObservationID,
			Revision:      prior.hostRevision,
			PayloadHash:   prior.hostReferenceFingerprint,
		}
		candidate := entry.bound.Clone()
		candidate.Supersedes = append(append([]metering.ObservationRef(nil), out...), hostRef)
		if err := metering.ValidateSupersessionGraph([]metering.Observation{prior.bound, candidate}); err != nil {
			return nil, ConnectorEvidenceRejectedScopeConflict, ""
		}
		out = append(out, hostRef)
	}
	return out, "", ""
}

// lookupReferenceLocked finds the bound predecessor a connector reference
// names. A reference carries only the connector's local observation id and
// revision, so an ambiguous lookup is treated as not found. The pendingInsert
// entry is counted as inserted: the lookup sees the key set it would see after
// the insertion.
func (m *SourceReferenceMap) lookupReferenceLocked(ref metering.ObservationRef, pendingInsert *providerBindingEntry) (*providerBindingEntry, bool) {
	key := ProviderLocalKey{ObservationID: ref.ObservationID, Revision: ref.Revision}.referenceLookupKey()
	keys := m.byRef[key]
	pendingKey := ""
	if pendingInsert != nil {
		pendingKey = pendingInsert.localKey.referenceLookupKey()
	}
	count := len(keys)
	if pendingKey == key {
		count++
	}
	if count != 1 {
		return nil, false
	}
	if pendingKey == key {
		return pendingInsert, true
	}
	entry, ok := m.byClaim[keys[0]]
	if !ok {
		return nil, false
	}
	return entry, true
}

// referenceHashMatches reports whether the connector's supplied payload hash
// matches the predecessor's source fingerprints. Both forms the SDK's graph
// validator accepts are allowed.
func referenceHashMatches(ref metering.ObservationRef, prior *providerBindingEntry) bool {
	if ref.PayloadHash == "" {
		return false
	}
	return ref.PayloadHash == prior.sourceFingerprint || ref.PayloadHash == prior.sourceReplayFingerprint
}

// recomputeReplayFingerprint returns the replay-stable hash of a bound
// observation, or the empty string when it cannot be encoded.
func recomputeReplayFingerprint(observation metering.Observation) string {
	hash, err := observation.ReplayFingerprint()
	if err != nil {
		return ""
	}
	return hash
}

// hostReplayFingerprint returns the replay-stable hash of the host-bound
// content of one claim, excluding the host-derived references. The references
// name the host's own predecessor, so they are not part of the claim's replay
// identity; everything the connector owns and every host-owned fact is.
//
// A claim that carries references uses correction or replacement semantics,
// which the SDK only accepts together with a reference. The probe therefore
// drops the references and normalizes that semantics member, so the probe
// stays encodable while the claim's own content is preserved.
func hostReplayFingerprint(observation metering.Observation) string {
	probe := observation.Clone()
	probe.Supersedes = nil
	if probe.Semantics == metering.SemanticsCorrection || probe.Semantics == metering.SemanticsReplacement {
		probe.Semantics = metering.SemanticsCumulative
	}
	return recomputeReplayFingerprint(probe)
}

// ReleaseUnresolvedHeld rejects every claim that is still held at the
// terminal drain. The whole unresolved claim is rejected: no raw reference, no
// synthetic reference and no invalid correction is ever serialized.
func (m *SourceReferenceMap) ReleaseUnresolvedHeld() []ProviderBindingOutcome {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	held := make([]*providerBindingEntry, 0, len(m.byClaim))
	for _, entry := range m.byClaim {
		if entry.held {
			held = append(held, entry)
		}
	}
	out := make([]ProviderBindingOutcome, 0, len(held))
	for _, entry := range held {
		m.removeEntryLocked(entry)
		out = append(out, ProviderBindingOutcome{Reason: ConnectorEvidenceRejectedPredecessorAbsent})
	}
	return out
}

// connectorClaimTrustConflict validates the connector's claimed attempt
// identity against the trusted host facts. An empty claim is filled by the
// host, a claim equal to the trusted value is accepted, a claim naming the
// transport B-leg pseudonym is accepted and replaced, and anything else is a
// conflicting claim.
//
// The attempt identity is checked in every carrier it is carried in. The host
// stamp replaces the correlation carrier wholesale, so a conflict that is
// only visible in the correlation carrier would otherwise be erased before
// it could be observed.
func connectorClaimTrustConflict(claim metering.Observation, trusted ProviderBindingIdentity) (ConnectorEvidenceRejection, bool) {
	if trusted.StoreID == "" || trusted.BLegID == "" {
		// A bind without a trusted store or B-leg cannot produce a bound
		// observation; the claim is not admitted.
		return ConnectorEvidenceRejectedTrustAttemptIdentityConflict, false
	}
	pairs := []struct {
		claimed   string
		trusted   string
		pseudonym string
	}{
		{claim.Subject.ALegID, trusted.ALegID, ""},
		{claim.Subject.RequestID, trusted.RequestID, ""},
		{claim.Subject.BillingCallID, trusted.BillingCallID, ""},
		{claim.Subject.CallID, trusted.CallID, ""},
		{claim.Subject.AttemptID, trusted.AttemptID, ""},
		{claim.Subject.BLegID, trusted.BLegID, trusted.ConnectorLegPseudonym},
		{claim.Correlation.ALegID, trusted.ALegID, ""},
		{claim.Correlation.RequestID, trusted.RequestID, ""},
		{claim.Correlation.BillingCallID, trusted.BillingCallID, ""},
		{claim.Correlation.CallID, trusted.CallID, ""},
		{claim.Correlation.AttemptID, trusted.AttemptID, ""},
		{claim.Correlation.BLegID, trusted.BLegID, trusted.ConnectorLegPseudonym},
	}
	for _, pair := range pairs {
		claimed := strings.TrimSpace(pair.claimed)
		if claimed == "" {
			continue
		}
		if claimed == pair.trusted {
			continue
		}
		if pair.pseudonym != "" && claimed == pair.pseudonym {
			continue
		}
		return ConnectorEvidenceRejectedTrustAttemptIdentityConflict, false
	}
	if claim.Subject.AttemptSeq != 0 && claim.Subject.AttemptSeq != trusted.AttemptSeq {
		return ConnectorEvidenceRejectedTrustAttemptIdentityConflict, false
	}
	if claim.Correlation.AttemptSeq != 0 && claim.Correlation.AttemptSeq != trusted.AttemptSeq {
		return ConnectorEvidenceRejectedTrustAttemptIdentityConflict, false
	}
	return "", true
}

// stampBoundObservation applies the host stamp to one connector claim. Every
// host-owned dimension is replaced with the trusted value; every preserved
// provider-owned dimension survives byte-for-byte.
func stampBoundObservation(claim metering.Observation, trusted ProviderBindingIdentity) (metering.Observation, string, string, string, []string) {
	providerAccountKey := firstNonEmptyClaim(claim.Subject.ProviderAccountKey, claim.Correlation.ProviderAccountKey)
	providerRequestID := firstNonEmptyClaim(claim.Subject.ProviderRequestID, claim.Correlation.ProviderRequestID)
	providerChargeID := firstNonEmptyClaim(claim.Subject.ProviderChargeID, claim.Correlation.ProviderChargeID)

	bound := claim.Clone()
	bound.Subject = metering.SubjectRef{
		Kind:               metering.SubjectBLeg,
		StoreID:            trusted.StoreID,
		TenantID:           trusted.TenantID,
		AccountID:          trusted.AccountID,
		ALegID:             trusted.ALegID,
		RequestID:          trusted.RequestID,
		BillingCallID:      trusted.BillingCallID,
		CallID:             trusted.CallID,
		BLegID:             trusted.BLegID,
		AttemptID:          trusted.AttemptID,
		AttemptSeq:         trusted.AttemptSeq,
		ProviderAccountKey: providerAccountKey,
		ProviderRequestID:  providerRequestID,
		ProviderChargeID:   providerChargeID,
	}
	bound.Correlation = metering.CorrelationV2{
		StoreID:            trusted.StoreID,
		TenantID:           trusted.TenantID,
		RequestID:          trusted.RequestID,
		CallID:             trusted.CallID,
		BillingCallID:      trusted.BillingCallID,
		ALegID:             trusted.ALegID,
		BLegID:             trusted.BLegID,
		AttemptID:          trusted.AttemptID,
		AttemptSeq:         trusted.AttemptSeq,
		ProviderAccountKey: providerAccountKey,
		ProviderRequestID:  providerRequestID,
		ProviderChargeID:   providerChargeID,
	}
	bound.Scope = trusted.Scope.Clone()
	bound.ReceivedAt = trusted.ReceivedAt.UTC()
	if bound.ObservedAt.IsZero() {
		bound.ObservedAt = bound.ReceivedAt
	}
	bound.ID = ""
	bound.Sequence = 0
	bound.Supersedes = nil

	var overrides []string
	if strings.TrimSpace(claim.Subject.StoreID) != "" && claim.Subject.StoreID != trusted.StoreID {
		overrides = append(overrides, "store")
	}
	if strings.TrimSpace(claim.Subject.TenantID) != "" && claim.Subject.TenantID != trusted.TenantID {
		overrides = append(overrides, "tenant")
	}
	if strings.TrimSpace(claim.Subject.AccountID) != "" && claim.Subject.AccountID != trusted.AccountID {
		overrides = append(overrides, "account")
	}
	if claim.Subject.Kind != metering.SubjectBLeg {
		overrides = append(overrides, "subject_kind")
	}

	hostIdentity := bound.IdentityKey()
	scopeDigest := ScopeDigest(trusted.Scope, trusted.ScopePresent)
	hostID := HostBoundObservationID(hostIdentity, scopeDigest)
	return bound, hostIdentity, hostID, scopeDigest, overrides
}

func firstNonEmptyClaim(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
