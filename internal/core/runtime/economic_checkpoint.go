package runtime

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

const (
	// maxPreTerminalEconomicCheckpointPending bounds the attempt-owned queue.
	// Cumulative snapshots for one source coalesce into one entry, while delta
	// and replacement observations retain their distinct source revisions.
	maxPreTerminalEconomicCheckpointPending = 64
	// Deferred entries retain observations admitted while the bounded pending
	// queue is full. Keeping a second, equally bounded queue preserves retry
	// ownership without allowing a failed journal to grow receive-path state.
	maxPreTerminalEconomicCheckpointDeferred = maxPreTerminalEconomicCheckpointPending
	// Accepted terminal evidence is already bounded by the billing evidence
	// limit. Keep the durable revision fence within that same attempt bound.
	maxPreTerminalEconomicCheckpointDurableHeads = usageevidence.MaxCallLegEvidenceObservations
	preTerminalEconomicCheckpointBatch           = 8
	preTerminalEconomicCheckpointInterval        = 250 * time.Millisecond
	economicCheckpointFlushTimeout               = 2 * time.Second
)

var (
	errEconomicCheckpointPendingLimit        = errors.New("runtime: economic checkpoint pending limit reached")
	errEconomicCheckpointAtomicBatchRequired = errors.New("runtime: economic checkpoint sink requires atomic batch capability")
	// ErrEconomicCheckpointFlushTimeout classifies a terminal economic checkpoint
	// flush that exceeded economicCheckpointFlushTimeout. The terminal path
	// detaches from caller cancellation on purpose, so exceeding this budget is a
	// bounded, expected outcome under storage contention rather than a defect:
	// it proves the flush stayed inside its latency guarantee. Callers and tests
	// classify on this sentinel with errors.Is instead of matching the
	// underlying store error text, which varies by adapter.
	ErrEconomicCheckpointFlushTimeout = errors.New("runtime: economic checkpoint flush exceeded its bounded budget")
)

const (
	economicCheckpointCapacityRejectionLogMessage = "economic_checkpoint_capacity_rejected"
	economicCheckpointCapacityRejectionReason     = "checkpoint_capacity_exhausted"
)

// Durable-head metadata budgets. A cumulative source's durable head keeps one
// entry per still-effective component key and one per supersession ref so a
// reordered sparse revision is never mistaken for redundant. Those maps are
// otherwise unbounded per head: a provider reporting a novel component or ref
// key on each successful flush would grow them for the whole attempt lifetime.
// Each budget is checked before a field or ref is retained. Once a budget is
// exhausted, the field/ref is simply not retained and the head records that its
// coverage proof is truncated, so an uncovered key/ref stays conservatively
// inadmissible rather than being invented as covered.
//
// The byte budgets are conservative upper bounds on resident Go map storage, not
// exact payload sizes. A retained string-keyed entry costs more than its key
// bytes plus value: on a 64-bit runtime the map slot also holds a 16-byte string
// header, the Swiss-table control array adds at least one byte per slot, table
// growth leaves spare capacity, allocation size classes round both the key
// backing array and the table up, and every non-nil map additionally owns a
// table header plus a minimum one-group (eight-slot) allocation. A head also
// carries an outer-map entry keyed by its source identity. The per-entry and
// per-head reserves below charge the marginal entry cost and the fixed map cost,
// so the budgets dominate real resident metadata. The charges are sized from
// 64-bit worst-case layout, which also upper-bounds 32-bit Go, whose headers and
// pointers are smaller.
const (
	// A single V2 observation carries at most metering.MaxObservationMeasures
	// measures and metering.MaxObservationSupersedes supersession refs, so a
	// fresh head always retains one complete observation.
	maxEconomicCheckpointHeadFields        = metering.MaxObservationMeasures
	maxEconomicCheckpointHeadSupersedes    = metering.MaxObservationSupersedes
	maxEconomicCheckpointHeadMetadataBytes = metering.MaxSafeEvidenceBytes

	// Aggregate caps independent of the bounded durable-head count. Total
	// retained durable-head metadata is capped at a small multiple of a single
	// head's budget, so total metadata cannot grow with the number of tracked
	// sources even though the head map itself is bounded by the terminal
	// evidence cap.
	maxEconomicCheckpointAggregateHeadSources   = 16
	maxEconomicCheckpointTotalFields            = maxEconomicCheckpointAggregateHeadSources * metering.MaxObservationMeasures
	maxEconomicCheckpointTotalSupersedes        = maxEconomicCheckpointAggregateHeadSources * metering.MaxObservationSupersedes
	maxEconomicCheckpointTotalHeadMetadataBytes = maxEconomicCheckpointAggregateHeadSources * metering.MaxSafeEvidenceBytes
)

const (
	// checkpointDurableKeyBackingFactor multiplies a retained key's own bytes
	// before the fixed map-entry reserve is added. Go's allocator rounds a
	// string's backing array up to the next size class, so charging only
	// len(key)+reserve undercounts real resident storage: a 4097-byte canonical
	// key occupies a 4864-byte class (+18.75%), and a key just above a class
	// boundary can overshoot by up to ~1.5x. Doubling the key length strictly
	// dominates the backing array for every valid width (small widths, where the
	// ratio is larger, are covered by the reserve below), so the charged metadata
	// is a real upper bound on admitted backing.
	checkpointDurableKeyBackingFactor = 2

	// checkpointDurableMapEntryReserveBytes is the marginal charge, beyond the
	// key's doubled backing, for retaining one entry in a durable-head map on a
	// 64-bit Go runtime. It covers with margin: the 16-byte string header and the
	// value struct in the slot (the largest value here is checkpointDurableField,
	// 48 bytes on 64-bit), at least one Swiss control byte per slot, a 2x
	// capacity headroom so a grown table's live slot array stays within
	// 2*(slot+control) per entry, allocator size-class rounding of the table, and
	// the residual small-string rounding above the 2x factor. It is a strict
	// upper bound for every value type stored here, and a zero-length key can
	// never evade the byte budget because the reserve alone is charged.
	checkpointDurableMapEntryReserveBytes = 256

	// checkpointDurableHeadEntryReserveBytes is the fixed charge for one head in
	// the outer source-keyed durable-head map, independent of its entry count.
	// The head's source-identity key backing is charged separately at the same
	// doubled factor; this reserve covers the outer map's slot and
	// checkpointDurableHead value, the hmap headers and minimum one-group
	// (eight-slot plus control) allocations of the head's two inner maps, and
	// allocator rounding. It bounds those on 64-bit Go with margin.
	checkpointDurableHeadEntryReserveBytes = 2048
)

// checkpointDurableField records the effective reducer ordering of one component
// key already durable for a cumulative source. usableSequence/usableRevision
// track the newest durable reducible value so a reordered older revision is not
// mistaken for redundant; numericSequence/numericRevision separately track the
// newest durable value the reducer actually applies (which includes a
// QualityUnknown measure that still carries a value) so an unavailable
// diagnostic cannot stand in for that numeric contribution; hasUnusable retains
// an absent/unavailable/unknown diagnostic so a later usable value cannot
// silently erase payability.
type checkpointDurableField struct {
	usableSequence  uint64
	usableRevision  uint64
	hasUsable       bool
	numericSequence uint64
	numericRevision uint64
	hasNumeric      bool
	hasUnusable     bool
}

// checkpointDurableHead tracks the revision fence plus the per-component
// measure/supersession state already durable for one cumulative source.
// Coverage keeps the fence from discarding a late, reordered sparse revision
// whose still-effective field has not yet been persisted. Each key keeps its own
// effective ordering so a reordered older revision cannot be judged redundant
// merely because a *different* key advanced the global revision.
type checkpointDurableHead struct {
	revision   uint64
	fields     map[string]checkpointDurableField
	supersedes map[string]struct{}
	// headBytes is the fixed charged cost of the head's outer-map entry (its
	// source-identity key plus per-map base overhead); fieldBytes and
	// supersedeBytes are the charged marginal costs of the inner-map entries.
	// truncated records that a field or ref was refused by a budget, so coverage
	// for that key/ref is unprovable rather than covered.
	headBytes      int
	fieldBytes     int
	supersedeBytes int
	truncated      bool
}

// metadataBytes is the total charged metadata retained by one head.
func (h checkpointDurableHead) metadataBytes() int {
	return h.headBytes + h.fieldBytes + h.supersedeBytes
}

// checkpointDurableKeyEntryBytes is the overflow-safe charge for retaining one
// map entry keyed by a key of keyLen bytes: twice the key length (dominating the
// allocator's size-class-rounded backing array) plus the fixed map reserve. It
// saturates instead of wrapping, so a hostile or corrupted length can only make
// an entry more expensive, never evade the budget.
func checkpointDurableKeyEntryBytes(keyLen, reserve int) int {
	if keyLen <= 0 {
		return reserve
	}
	if keyLen > (math.MaxInt-reserve)/checkpointDurableKeyBackingFactor {
		return math.MaxInt
	}
	return checkpointDurableKeyBackingFactor*keyLen + reserve
}

// checkpointEntryExceedsBudget reports whether current+charge exceeds limit
// without overflowing. Callers keep current within limit, but charge is
// saturated for pathological key lengths, so the comparison must not assume a
// safe sum.
func checkpointEntryExceedsBudget(current, charge, limit int) bool {
	if current < 0 || charge < 0 {
		return true
	}
	if charge > limit {
		return true
	}
	return current > limit-charge
}

func checkpointDurableFieldEntryBytes(canonicalKey string) int {
	return checkpointDurableKeyEntryBytes(len(canonicalKey), checkpointDurableMapEntryReserveBytes)
}

func checkpointDurableSupersessionEntryBytes(refKey string) int {
	return checkpointDurableKeyEntryBytes(len(refKey), checkpointDurableMapEntryReserveBytes)
}

// checkpointDurableHeadEntryBytes charges the outer durable-head map entry: the
// source-identity key's doubled backing plus the fixed per-head map reserve.
func checkpointDurableHeadEntryBytes(sourceKey string) int {
	return checkpointDurableKeyEntryBytes(len(sourceKey), checkpointDurableHeadEntryReserveBytes)
}

// checkpointHeadLimits is the resolved retention budget for one head: the
// smaller of the per-head cap and what remains of the aggregate cap.
type checkpointHeadLimits struct {
	fields     int
	supersedes int
	bytes      int
}

// record merges one durably written cumulative observation into the head under
// the supplied budgets. The global revision advances monotonically; each field
// keeps the newest durable usable ordering, tracks the newest durable
// reducer-applied numeric ordering (including unknown-quality values), and
// grows its unusable diagnostic coverage. A field or supersession ref that
// cannot be retained within the budget is not added and marks the head's
// coverage proof truncated, so it is never mistaken for covered.
func (h checkpointDurableHead) record(observation metering.Observation, limits checkpointHeadLimits) checkpointDurableHead {
	if observation.Revision > h.revision {
		h.revision = observation.Revision
	}
	for _, measure := range observation.Measures {
		key, err := measure.Key.Normalize()
		if err != nil {
			continue
		}
		canonicalKey := key.CanonicalKey()
		field, exists := h.fields[canonicalKey]
		if !exists {
			entryBytes := checkpointDurableFieldEntryBytes(canonicalKey)
			if len(h.fields) >= limits.fields || checkpointEntryExceedsBudget(h.metadataBytes(), entryBytes, limits.bytes) {
				h.truncated = true
				continue
			}
			if h.fields == nil {
				h.fields = make(map[string]checkpointDurableField)
			}
			h.fieldBytes += entryBytes
		}
		if checkpointMeasureReducible(measure) {
			if !field.hasUsable || checkpointOrderAtLeastAsNew(observation.Sequence, observation.Revision, field.usableSequence, field.usableRevision) {
				field.hasUsable = true
				field.usableSequence = observation.Sequence
				field.usableRevision = observation.Revision
			}
		} else {
			field.hasUnusable = true
		}
		if checkpointMeasureValued(measure) {
			if !field.hasNumeric || checkpointOrderAtLeastAsNew(observation.Sequence, observation.Revision, field.numericSequence, field.numericRevision) {
				field.hasNumeric = true
				field.numericSequence = observation.Sequence
				field.numericRevision = observation.Revision
			}
		}
		h.fields[canonicalKey] = field
	}
	for _, ref := range observation.Supersedes {
		refKey := checkpointSupersessionRefKey(ref)
		if _, exists := h.supersedes[refKey]; exists {
			continue
		}
		entryBytes := checkpointDurableSupersessionEntryBytes(refKey)
		if len(h.supersedes) >= limits.supersedes || checkpointEntryExceedsBudget(h.metadataBytes(), entryBytes, limits.bytes) {
			h.truncated = true
			continue
		}
		if h.supersedes == nil {
			h.supersedes = make(map[string]struct{})
		}
		h.supersedes[refKey] = struct{}{}
		h.supersedeBytes += entryBytes
	}
	return h
}

// durableHeadCovers reports whether every still-effective field of observation
// is already durably represented by head. A usable field is covered only by a
// durable usable entry whose reducer ordering is at least as new, so a reordered
// older value remains admissible; an unusable field is covered only by a durable
// unusable entry for the same key, so a diagnostic is never hidden. A
// non-reducible measure that still carries a value (QualityUnknown with a value)
// additionally requires a durable numeric entry at least as new, because the
// reducer applies that value while an unavailable diagnostic does not represent
// it. Charge-bearing and unavailable-authority observations are never covered:
// cumulative charges accumulate one reduced entry per revision, and an
// unavailable claim contributes an explicit diagnostic that a later revision
// does not erase.
func durableHeadCovers(head checkpointDurableHead, observation metering.Observation) bool {
	if observation.Authority == metering.AuthorityUnavailableClaim || len(observation.Charges) != 0 {
		return false
	}
	for _, measure := range observation.Measures {
		key, err := measure.Key.Normalize()
		if err != nil {
			return false
		}
		field, ok := head.fields[key.CanonicalKey()]
		if !ok {
			return false
		}
		if checkpointMeasureReducible(measure) {
			if !field.hasUsable || !checkpointOrderAtLeastAsNew(field.usableSequence, field.usableRevision, observation.Sequence, observation.Revision) {
				return false
			}
		} else {
			if !field.hasUnusable {
				return false
			}
			if checkpointMeasureValued(measure) &&
				(!field.hasNumeric || !checkpointOrderAtLeastAsNew(field.numericSequence, field.numericRevision, observation.Sequence, observation.Revision)) {
				return false
			}
		}
	}
	for _, ref := range observation.Supersedes {
		if _, ok := head.supersedes[checkpointSupersessionRefKey(ref)]; !ok {
			return false
		}
	}
	return true
}

// cumulativeSnapshotCovers reports whether newer can represent every
// still-effective field of older for cumulative reduction, so dropping older
// cannot lose a value or a diagnostic. Present-field coverage is per component:
// explicit zero is a present, reducible value, while an older
// absent/unavailable/not-applicable field is never covered because it keeps the
// reduced snapshot incomplete even when newer carries the same key's value. An
// older revision carrying any charge or an unavailable authority claim is never
// covered because cumulative reduction retains those per observation.
func cumulativeSnapshotCovers(newer, older metering.Observation) bool {
	if newer.Semantics != metering.SemanticsCumulative || older.Semantics != metering.SemanticsCumulative {
		return false
	}
	if economicCheckpointPendingKey(newer) != economicCheckpointPendingKey(older) {
		return false
	}
	// Only a reducer-newer snapshot may coalesce an older one. Dropping
	// effective newer evidence because a reordered older revision arrived would
	// move the durable head backwards even when the field sets overlap.
	if !checkpointOrderAtLeastAsNew(newer.Sequence, newer.Revision, older.Sequence, older.Revision) {
		return false
	}
	if newer.Authority == metering.AuthorityUnavailableClaim || older.Authority == metering.AuthorityUnavailableClaim {
		return false
	}
	if len(older.Charges) != 0 {
		return false
	}
	if !checkpointSupersedesCover(newer.Supersedes, older.Supersedes) {
		return false
	}
	newerMeasures := make(map[string]bool, len(newer.Measures))
	for _, measure := range newer.Measures {
		key, err := measure.Key.Normalize()
		if err != nil {
			continue
		}
		newerMeasures[key.CanonicalKey()] = newerMeasures[key.CanonicalKey()] || checkpointMeasureReducible(measure)
	}
	for _, measure := range older.Measures {
		key, err := measure.Key.Normalize()
		if err != nil {
			return false
		}
		// An absent/unavailable/not-applicable older field keeps the reduced
		// snapshot incomplete even when a newer revision carries the same key's
		// usable value, so it must stay durable as an explicit diagnostic.
		if !checkpointMeasureReducible(measure) {
			return false
		}
		if reducible, present := newerMeasures[key.CanonicalKey()]; !present || !reducible {
			return false
		}
	}
	return true
}

func checkpointSupersedesCover(newer, older []metering.ObservationRef) bool {
	if len(older) == 0 {
		return true
	}
	known := make(map[string]struct{}, len(newer))
	for _, ref := range newer {
		known[checkpointSupersessionRefKey(ref)] = struct{}{}
	}
	for _, ref := range older {
		if _, ok := known[checkpointSupersessionRefKey(ref)]; !ok {
			return false
		}
	}
	return true
}

func checkpointSupersessionRefKey(ref metering.ObservationRef) string {
	return ref.StoreID + "\x00" + ref.ObservationID + "\x00" + strconv.FormatUint(ref.Revision, 10) + "\x00" + ref.PayloadHash
}

// checkpointMeasureReducible reports whether a measure is a usable present
// value for cumulative coalescing and durable-head coverage. It mirrors the
// reducer's usability rule: a QualityUnknown measure is incomplete even when it
// carries a value (see aggregate.measureIsIncomplete), so it must stay an
// unusable diagnostic rather than a usable field. Treating it as usable would
// let a later known value coalesce it away and silently drop the incomplete
// reduction that the original immutable observations establish.
func checkpointMeasureReducible(measure metering.Measure) bool {
	return measure.Value != nil && measure.Quality != metering.QualityUnknown &&
		measure.Quality != metering.QualityUnavailable && measure.Quality != metering.QualityNotApplicable
}

// checkpointMeasureValued reports whether the canonical reducer applies a
// numeric value for this measure. It mirrors aggregate.reductionState.apply
// exactly: a present value is reduced unless the quality is unavailable or
// not-applicable. A QualityUnknown measure with a non-nil value is therefore
// valued even though it is not reducible: the reducer applies the number and
// separately marks the snapshot incomplete. Durable-head coverage must track
// that numeric contribution independently of the incompleteness diagnostic.
func checkpointMeasureValued(measure metering.Measure) bool {
	return measure.Value != nil && measure.Quality != metering.QualityUnavailable &&
		measure.Quality != metering.QualityNotApplicable
}

// checkpointOrderAtLeastAsNew reports whether (sequence, revision) is reduced at
// or after (priorSequence, priorRevision) by the canonical reducer, which orders
// observations by declared stream sequence and then revision. Cumulative
// coalescing and durable-head coverage must respect that order so a reordered
// older revision can never replace effective newer evidence.
func checkpointOrderAtLeastAsNew(sequence, revision, priorSequence, priorRevision uint64) bool {
	if sequence != priorSequence {
		return sequence > priorSequence
	}
	return revision >= priorRevision
}

type economicCheckpointAdmission uint8

const (
	economicCheckpointRejected economicCheckpointAdmission = iota
	economicCheckpointRetained
	economicCheckpointIgnored
)

type economicCheckpointWrite struct {
	pendingKey  string
	hash        string
	deferred    bool
	observation metering.Observation
}

type economicCheckpointCapacityDiagnostic struct {
	semantics string
	revision  uint64
}

// queueEconomicCheckpoint admits an immutable observation to one of the
// attempt-owned bounded queues. Admission never waits for the journal: a full
// pending queue uses the bounded deferred queue, so receive callbacks retain
// ownership even when a recovery flush is unavailable.
func (a *attemptSession) queueEconomicCheckpoint(observation metering.Observation) economicCheckpointAdmission {
	if a == nil {
		return economicCheckpointRejected
	}
	if a.observationSink == nil {
		// No durable checkpoint consumer is configured; terminal evidence owns
		// the observation and no queue admission is required.
		return economicCheckpointIgnored
	}
	canonical, hash, err := canonicalEconomicCheckpoint(observation)
	if err != nil {
		a.noteEconomicCheckpointError(err)
		return economicCheckpointRejected
	}
	pendingKey := economicCheckpointPendingKey(canonical)

	a.checkpointMu.Lock()
	defer a.checkpointMu.Unlock()
	if canonical.Semantics == metering.SemanticsCumulative {
		if prior, ok := a.checkpointDurableHeads[pendingKey]; ok && canonical.Revision <= prior.revision && durableHeadCovers(prior, canonical) {
			// A durable cumulative head is authoritative for the fields it
			// already covers. A reordered older revision that still carries an
			// uncovered field must remain admissible rather than being lost.
			return economicCheckpointIgnored
		}
		// Coalesce only when the newer snapshot provably represents every
		// still-effective field of an older pending/deferred revision. A sparse
		// snapshot that omits a field must not erase the earlier revision.
		a.removeCoveredCumulativeCheckpointsLocked(pendingKey, canonical)
		// Coalescing deletes covered map entries but leaves their keys in the
		// append-only order slices. Rebuild both order indexes from the surviving
		// map entries even when no removal happened, so a stream of coalesced
		// cumulative revisions can never grow order bookkeeping past the bounded
		// maps regardless of whether a flush ever reaches a sink.
		a.removeStaleEconomicCheckpointsLocked()
	}
	if a.checkpointPending == nil {
		a.checkpointPending = make(map[string]metering.Observation)
	}
	storageKey := a.cumulativeCheckpointStorageKeyLocked(canonical, pendingKey)
	if prior, ok := a.checkpointPending[storageKey]; ok {
		if canonical.Revision < prior.Revision {
			// A reordered cumulative snapshot must not move the pending head
			// backwards. Delta observations use revision-qualified keys and
			// therefore do not enter this branch for normal delivery.
			return economicCheckpointIgnored
		}
		priorHash, priorErr := prior.ReplayFingerprint()
		if priorErr == nil && priorHash == hash {
			return economicCheckpointIgnored
		}
		a.checkpointPending[storageKey] = canonical
		return economicCheckpointRetained
	}
	if prior, ok := a.checkpointDeferred[storageKey]; ok {
		if canonical.Revision < prior.Revision {
			return economicCheckpointIgnored
		}
		priorHash, priorErr := prior.ReplayFingerprint()
		if priorErr == nil && priorHash == hash {
			return economicCheckpointIgnored
		}
		a.checkpointDeferred[storageKey] = canonical
		return economicCheckpointRetained
	}
	if len(a.checkpointPending) < maxPreTerminalEconomicCheckpointPending {
		a.checkpointPending[storageKey] = canonical
		a.checkpointOrder = append(a.checkpointOrder, storageKey)
		return economicCheckpointRetained
	}
	if len(a.checkpointDeferred) < maxPreTerminalEconomicCheckpointDeferred {
		if a.checkpointDeferred == nil {
			a.checkpointDeferred = make(map[string]metering.Observation)
		}
		a.checkpointDeferred[storageKey] = canonical
		a.checkpointDeferredOrder = append(a.checkpointDeferredOrder, storageKey)
		return economicCheckpointRetained
	}
	a.checkpointErr = errEconomicCheckpointPendingLimit
	if !a.checkpointCapacityDiagnosticPending && !a.checkpointCapacityDiagnosticEmitted {
		// Retain only bounded enum/numeric context for one coalesced diagnostic.
		// The source identity and observation payload never enter the log path.
		a.checkpointCapacityDiagnosticPending = true
		a.checkpointCapacityDiagnosticSemantics = canonical.Semantics
		a.checkpointCapacityDiagnosticRevision = canonical.Revision
	}
	return economicCheckpointRejected
}

// removeCoveredCumulativeCheckpointsLocked drops already-queued cumulative
// revisions that the incoming snapshot provably covers. Callers hold
// checkpointMu. An older revision that carries a field absent from the incoming
// snapshot is retained so a later sparse revision cannot erase it.
func (a *attemptSession) removeCoveredCumulativeCheckpointsLocked(base string, canonical metering.Observation) {
	a.removeCoveredCumulativeSetLocked(a.checkpointPending, base, canonical)
	a.removeCoveredCumulativeSetLocked(a.checkpointDeferred, base, canonical)
}

func (a *attemptSession) removeCoveredCumulativeSetLocked(set map[string]metering.Observation, base string, canonical metering.Observation) {
	for key, existing := range set {
		if economicCheckpointPendingKey(existing) != base {
			continue
		}
		if cumulativeSnapshotCovers(canonical, existing) {
			delete(set, key)
		}
	}
}

// cumulativeCheckpointStorageKeyLocked selects the queue key for a cumulative
// observation. It reuses the coalescing base key when free; when a
// non-subsumed sibling already occupies it, the revision-qualified identity is
// used so both sparse revisions remain durable. Callers hold checkpointMu.
func (a *attemptSession) cumulativeCheckpointStorageKeyLocked(canonical metering.Observation, base string) string {
	if canonical.Semantics != metering.SemanticsCumulative {
		return canonical.SourceEventIdentity()
	}
	if _, ok := a.checkpointPending[base]; ok {
		return canonical.SourceEventIdentity()
	}
	if _, ok := a.checkpointDeferred[base]; ok {
		return canonical.SourceEventIdentity()
	}
	return base
}

// takeEconomicCheckpointCapacityDiagnostic claims the one coalesced capacity
// rejection signal for this attempt. The caller emits it through the existing
// runtime diagnostic logger, outside checkpointMu, so receive-side admission
// never performs logging while holding checkpoint state.
func (a *attemptSession) takeEconomicCheckpointCapacityDiagnostic() (economicCheckpointCapacityDiagnostic, bool) {
	if a == nil {
		return economicCheckpointCapacityDiagnostic{}, false
	}
	a.checkpointMu.Lock()
	defer a.checkpointMu.Unlock()
	if !a.checkpointCapacityDiagnosticPending || a.checkpointCapacityDiagnosticEmitted {
		return economicCheckpointCapacityDiagnostic{}, false
	}
	a.checkpointCapacityDiagnosticPending = false
	a.checkpointCapacityDiagnosticEmitted = true
	return economicCheckpointCapacityDiagnostic{
		semantics: a.checkpointCapacityDiagnosticSemantics,
		revision:  a.checkpointCapacityDiagnosticRevision,
	}, true
}

func canonicalEconomicCheckpoint(observation metering.Observation) (metering.Observation, string, error) {
	canonical, err := observation.Canonical()
	if err != nil {
		return metering.Observation{}, "", fmt.Errorf("economic checkpoint observation: %w", err)
	}
	hash, err := canonical.ReplayFingerprint()
	if err != nil || strings.TrimSpace(hash) == "" {
		if err == nil {
			err = errors.New("empty replay fingerprint")
		}
		return metering.Observation{}, "", fmt.Errorf("economic checkpoint fingerprint: %w", err)
	}
	return canonical, hash, nil
}

// versionLocalBoundaryObservations turns the boundary accumulator's repeated
// revision-one snapshots into immutable cumulative revisions. Observation
// timestamps describe capture time and therefore do not, by themselves,
// advance a local measurement revision.
//
// drain is true only for the irreversible terminal drain. The receive-side
// checkpoint path can retry a rejected measurement from the still-live
// accumulator, so it neither records loss nor treats the rejection as final.
// On the terminal drain a rejected measurement has no later retry owner: the
// just-measured immutable snapshot is handed to the terminal record directly
// (never the stale prior head), and the truncated durable checkpoint is
// recorded as sticky capture loss that a later successful flush cannot clear.
func (a *attemptSession) versionLocalBoundaryObservations(observations []metering.Observation, drain bool) []metering.Observation {
	if a == nil || len(observations) == 0 {
		return nil
	}
	if a.observationSink == nil {
		return observations
	}
	// Boundary snapshots can be requested by receive and terminal paths. Keep
	// revision selection and queue admission in one attempt-owned sequence so a
	// failed admission cannot publish a local head that has no retry owner.
	a.checkpointLocalMu.Lock()
	defer a.checkpointLocalMu.Unlock()

	versioned := make([]metering.Observation, 0, len(observations))
	for _, observation := range observations {
		canonical, _, err := canonicalEconomicCheckpoint(observation)
		if err != nil {
			continue
		}
		pendingKey := economicCheckpointPendingKey(canonical)
		measurementHash, err := localBoundaryMeasurementFingerprint(canonical)
		if err != nil {
			continue
		}
		a.checkpointMu.Lock()
		prior, hasPrior := a.localCheckpointHeads[pendingKey]
		priorHash, hasPriorHash := a.localCheckpointHashes[pendingKey]
		a.checkpointMu.Unlock()
		if hasPriorHash && priorHash == measurementHash {
			if hasPrior {
				versioned = append(versioned, prior.Clone())
			}
			continue
		}
		if hasPrior && canonical.Revision <= prior.Revision {
			canonical.Revision = prior.Revision + 1
		}
		if a.queueEconomicCheckpoint(canonical) != economicCheckpointRetained {
			// The measurement has no durable checkpoint owner. On the terminal
			// drain that is an irreversible loss of the pre-terminal checkpoint
			// plane, so retain the sticky fail-closed disposition. The final
			// immutable snapshot itself is handed to the terminal record rather
			// than republishing a stale prior head as if it were final.
			if drain {
				a.recordEconomicCaptureLoss(evidenceCaptureLossCheckpointCapacity, canonical.SourceEventKey, economicEvidenceCaptureLossBytes(canonical))
			}
			versioned = append(versioned, canonical.Clone())
			continue
		}
		a.checkpointMu.Lock()
		if a.localCheckpointHeads == nil {
			a.localCheckpointHeads = make(map[string]metering.Observation)
		}
		if a.localCheckpointHashes == nil {
			a.localCheckpointHashes = make(map[string]string)
		}
		a.localCheckpointHeads[pendingKey] = canonical.Clone()
		a.localCheckpointHashes[pendingKey] = measurementHash
		a.checkpointMu.Unlock()
		versioned = append(versioned, canonical.Clone())
	}
	return versioned
}

func localBoundaryMeasurementFingerprint(observation metering.Observation) (string, error) {
	canonical, err := observation.Canonical()
	if err != nil {
		return "", err
	}
	canonical.Revision = 1
	canonical.ObservedAt = time.Unix(1, 0).UTC()
	canonical.ReceivedAt = canonical.ObservedAt
	return canonical.ReplayFingerprint()
}

func economicCheckpointPendingKey(observation metering.Observation) string {
	identity := observation.SourceEventIdentity()
	if observation.Semantics != metering.SemanticsCumulative {
		return identity
	}
	// IdentityKey places revision in the final component. Cumulative snapshots
	// may replace a pending revision from the same source. Replacement and
	// correction revisions retain their full identity because their supersession
	// graph must remain durable even when both revisions arrive in one flush.
	if idx := strings.LastIndexByte(identity, 0); idx >= 0 {
		identity = identity[:idx]
	}
	// Source identity alone omits the perspective, boundary, lifecycle, account
	// and charge-scope axes that the canonical reducer partitions on. Two valid
	// cumulative observations can therefore share a source identity while the
	// reducer keeps them in separate partitions; coalescing or suppressing one
	// would drop a whole reduced partition. Qualify the coalescing/durable-head
	// key with the reducer's own scope key so the checkpoint can never merge
	// across a reduction partition, without defining a third scope identity.
	return identity + "\x00" + aggregate.ScopeFor(observation).Key()
}

func (a *attemptSession) shouldFlushEconomicCheckpoints(now time.Time, force bool) bool {
	if a == nil || a.observationSink == nil {
		return false
	}
	a.checkpointMu.Lock()
	defer a.checkpointMu.Unlock()
	pendingCount := len(a.checkpointPending) + len(a.checkpointDeferred)
	if pendingCount == 0 {
		return false
	}
	if force || a.checkpointLastFlush.IsZero() || pendingCount >= preTerminalEconomicCheckpointBatch {
		return true
	}
	return !now.Before(a.checkpointLastFlush.Add(preTerminalEconomicCheckpointInterval))
}

func (a *attemptSession) economicCheckpointNow() time.Time {
	if a != nil && a.now != nil {
		return a.now().UTC()
	}
	return time.Now().UTC()
}

// flushEconomicCheckpoints appends immutable observations only. It never calls
// billing/authority code and can therefore be used around receive callbacks.
// A non-forced flush is cadence/size gated; terminal callers pass force=true.
// The sink must implement metering.AtomicObservationSink: retaining the queue
// after an error is safe only when the complete batch is atomic and idempotent.
func (a *attemptSession) flushEconomicCheckpoints(ctx context.Context, force bool) error {
	if a == nil || a.observationSink == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		a.noteEconomicCheckpointError(err)
		return err
	}
	// Snapshot and append are one serialized operation. Without this gate a
	// receive-side flush and terminal flush could both observe the same pending
	// head and append it twice before either removed it from the queue.
	a.checkpointFlushMu.Lock()
	defer a.checkpointFlushMu.Unlock()
	if err := ctx.Err(); err != nil {
		a.noteEconomicCheckpointError(err)
		return err
	}
	now := a.economicCheckpointNow()
	if !a.shouldFlushEconomicCheckpoints(now, force) {
		return nil
	}

	a.checkpointMu.Lock()
	writes := make([]economicCheckpointWrite, 0, len(a.checkpointPending)+len(a.checkpointDeferred))
	seenKeys := make(map[string]struct{}, len(a.checkpointPending)+len(a.checkpointDeferred))
	for _, pendingKey := range a.checkpointOrder {
		if _, duplicate := seenKeys[pendingKey]; duplicate {
			continue
		}
		observation, ok := a.checkpointPending[pendingKey]
		if !ok {
			continue
		}
		seenKeys[pendingKey] = struct{}{}
		canonical, hash, err := canonicalEconomicCheckpoint(observation)
		if err != nil {
			a.checkpointMu.Unlock()
			a.noteEconomicCheckpointError(err)
			return err
		}
		writes = append(writes, economicCheckpointWrite{pendingKey: pendingKey, hash: hash, observation: canonical})
	}
	for _, pendingKey := range a.checkpointDeferredOrder {
		if _, duplicate := seenKeys[pendingKey]; duplicate {
			continue
		}
		observation, ok := a.checkpointDeferred[pendingKey]
		if !ok {
			continue
		}
		seenKeys[pendingKey] = struct{}{}
		canonical, hash, err := canonicalEconomicCheckpoint(observation)
		if err != nil {
			a.checkpointMu.Unlock()
			a.noteEconomicCheckpointError(err)
			return err
		}
		writes = append(writes, economicCheckpointWrite{pendingKey: pendingKey, hash: hash, deferred: true, observation: canonical})
	}
	a.checkpointMu.Unlock()

	if len(writes) == 0 {
		a.checkpointMu.Lock()
		a.removeStaleEconomicCheckpointsLocked()
		a.checkpointLastFlush = now
		a.checkpointErr = nil
		a.checkpointMu.Unlock()
		return nil
	}

	observations := make([]metering.Observation, 0, len(writes))
	for _, write := range writes {
		observations = append(observations, write.observation)
	}
	batchSink, ok := a.observationSink.(metering.AtomicObservationSink)
	if !ok {
		err := errEconomicCheckpointAtomicBatchRequired
		a.noteEconomicCheckpointError(err)
		return err
	}
	if err := batchSink.AppendObservations(ctx, observations); err != nil {
		err = fmt.Errorf("runtime: economic checkpoint append batch: %w", err)
		a.noteEconomicCheckpointError(err)
		return err
	}

	a.checkpointMu.Lock()
	for _, write := range writes {
		current, ok := a.checkpointPending[write.pendingKey]
		if write.deferred {
			current, ok = a.checkpointDeferred[write.pendingKey]
		}
		if !ok {
			continue
		}
		currentHash, hashErr := current.ReplayFingerprint()
		if hashErr == nil && currentHash == write.hash {
			if write.deferred {
				delete(a.checkpointDeferred, write.pendingKey)
			} else {
				delete(a.checkpointPending, write.pendingKey)
			}
		}
	}
	a.recordDurableEconomicHeadsLocked(writes)
	a.removeStaleEconomicCheckpointsLocked()
	a.checkpointLastFlush = now
	a.checkpointErr = nil
	a.checkpointMu.Unlock()
	return nil
}

// removeStaleEconomicCheckpointsLocked rebuilds each order index from the map
// entries that still own a queue slot, preserving the relative arrival order of
// the survivors and dropping both stale keys and accidental duplicates. Because
// coalescing can delete a map entry whose key remains in the order slice, every
// index is bounded by its map, which is itself bounded by the configured
// capacity. The rebuild is idempotent and safe to run on the receive path.
func (a *attemptSession) removeStaleEconomicCheckpointsLocked() {
	if a == nil {
		return
	}
	a.checkpointOrder = compactEconomicCheckpointOrder(a.checkpointOrder, a.checkpointPending)
	a.checkpointDeferredOrder = compactEconomicCheckpointOrder(a.checkpointDeferredOrder, a.checkpointDeferred)
}

// compactEconomicCheckpointOrder keeps only keys still present in set, in their
// original relative order, with each key appearing at most once. It reuses the
// input backing array so steady-state coalescing does not allocate per revision
// beyond the small dedupe set bounded by the map size.
func compactEconomicCheckpointOrder(order []string, set map[string]metering.Observation) []string {
	if len(order) == 0 || len(set) == 0 {
		return nil
	}
	keep := order[:0]
	seen := make(map[string]struct{}, len(set))
	for _, key := range order {
		if _, ok := set[key]; !ok {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		keep = append(keep, key)
	}
	return keep
}

func (a *attemptSession) recordDurableEconomicHeadsLocked(writes []economicCheckpointWrite) {
	if a == nil || len(writes) == 0 {
		return
	}
	if a.checkpointDurableHeads == nil {
		a.checkpointDurableHeads = make(map[string]checkpointDurableHead)
	}
	// Recompute the bounded aggregate once per record pass and maintain it
	// locally, so total retained metadata cannot grow with the number of tracked
	// sources without an unbounded per-attempt counter.
	aggregate := a.durableHeadAggregateLocked()
	for _, write := range writes {
		if write.observation.Semantics != metering.SemanticsCumulative {
			continue
		}
		base := economicCheckpointPendingKey(write.observation)
		head, exists := a.checkpointDurableHeads[base]
		if !exists {
			if len(a.checkpointDurableHeads) >= maxPreTerminalEconomicCheckpointDurableHeads {
				// The terminal evidence bound is the lifetime cap for accepted
				// observations. Do not grow the revision fence beyond that cap.
				continue
			}
			headBytes := checkpointDurableHeadEntryBytes(base)
			if headBytes > maxEconomicCheckpointHeadMetadataBytes ||
				checkpointEntryExceedsBudget(aggregate.bytes, headBytes, maxEconomicCheckpointTotalHeadMetadataBytes) {
				// The head's own outer-map entry cannot fit the per-head or
				// aggregate metadata budget, so the head is not admitted at all.
				// The observation still reaches durable storage; only the
				// coverage cache declines to track it.
				continue
			}
			head = checkpointDurableHead{headBytes: headBytes}
			a.checkpointDurableHeads[base] = head
			aggregate.bytes += headBytes
		}
		beforeFields := len(head.fields)
		beforeSupersedes := len(head.supersedes)
		beforeBytes := head.metadataBytes()
		updated := head.record(write.observation, aggregate.headLimits(head))
		aggregate.fields += len(updated.fields) - beforeFields
		aggregate.supersedes += len(updated.supersedes) - beforeSupersedes
		aggregate.bytes += updated.metadataBytes() - beforeBytes
		a.checkpointDurableHeads[base] = updated
	}
}

// durableHeadAggregate is the running sum of durable-head metadata for one
// attempt. It is derived from the bounded head map at the start of a record
// pass and then updated locally.
type durableHeadAggregate struct {
	fields     int
	supersedes int
	bytes      int
}

func (a *attemptSession) durableHeadAggregateLocked() durableHeadAggregate {
	var aggregate durableHeadAggregate
	for _, head := range a.checkpointDurableHeads {
		aggregate.fields += len(head.fields)
		aggregate.supersedes += len(head.supersedes)
		aggregate.bytes += head.metadataBytes()
	}
	return aggregate
}

// headLimits resolves the per-head retention budget against the aggregate caps.
// The head itself is already counted in the aggregate, so its room is the
// head's current usage plus whatever remains of the aggregate budget.
func (aggregate durableHeadAggregate) headLimits(head checkpointDurableHead) checkpointHeadLimits {
	fieldRoom := max(maxEconomicCheckpointTotalFields-aggregate.fields, 0)
	supersedeRoom := max(maxEconomicCheckpointTotalSupersedes-aggregate.supersedes, 0)
	byteRoom := max(maxEconomicCheckpointTotalHeadMetadataBytes-aggregate.bytes, 0)
	return checkpointHeadLimits{
		fields:     min(maxEconomicCheckpointHeadFields, len(head.fields)+fieldRoom),
		supersedes: min(maxEconomicCheckpointHeadSupersedes, len(head.supersedes)+supersedeRoom),
		bytes:      min(maxEconomicCheckpointHeadMetadataBytes, head.metadataBytes()+byteRoom),
	}
}

func (a *attemptSession) noteEconomicCheckpointError(err error) {
	if a == nil || err == nil {
		return
	}
	a.checkpointMu.Lock()
	a.checkpointErr = err
	a.checkpointMu.Unlock()
}

func (a *attemptSession) flushEconomicCheckpointsAtTerminal(ctx context.Context) error {
	if a == nil || a.observationSink == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	budget := economicCheckpointFlushTimeout
	if a.terminalFlushBudget > 0 {
		budget = a.terminalFlushBudget
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()
	if err := a.flushEconomicCheckpoints(persistCtx, true); err != nil {
		if isEconomicCheckpointFlushTimeout(persistCtx, err) {
			return errors.Join(ErrEconomicCheckpointFlushTimeout, err)
		}
		return err
	}
	return nil
}

// isEconomicCheckpointFlushTimeout reports whether err was caused by the bounded
// terminal flush budget elapsing.
//
// It classifies on the flush's own context rather than the error chain: storage
// adapters wrap their retry-cancellation with %v, so the underlying
// context.DeadlineExceeded is deliberately NOT part of the chain, and generic
// core must not import a concrete store to match its sentinel. Because the
// terminal flush detaches from caller cancellation, ctx.Err() elapsing can only
// mean this flush's own budget expired.
func isEconomicCheckpointFlushTimeout(ctx context.Context, err error) bool {
	if err == nil || ctx == nil {
		return false
	}
	return ctx.Err() == context.DeadlineExceeded
}

// maxTerminalLocalEvidenceReserve bounds the terminal evidence envelope
// capacity reserved for the validated final local boundary origin. Local
// boundary capture emits a small fixed set of planes per attempt, so the
// reserve is deliberately small: provider and economic evidence keep priority
// for every unreserved slot, and the reserve only guarantees the final local
// origin is never silently displaced by an independently admitted
// provider/economic population.
const maxTerminalLocalEvidenceReserve = 8

// terminalEnvelopeLossIdentity is the bounded, runtime-owned identity carried
// by the reserved envelope-truncation disposition. It is a fixed runtime
// marker, never a source-controlled value, so the disposition cannot be forged
// from provider content and repeated truncations deduplicate.
const terminalEnvelopeLossIdentity = "terminal-evidence-envelope"

// terminalEvidenceEnvelopeLossConflict builds the trusted, reserved,
// fail-closed disposition emitted when the bounded terminal evidence envelope
// cannot include every validated origin. It reuses the existing capture-loss
// diagnostic shape so the terminal record stays unratable instead of
// presenting a truncated prefix as complete. It is bounded: one marker per
// record, carrying only a count and a closed cause.
func terminalEvidenceEnvelopeLossConflict(dropped int) []usageevidence.EvidenceConflict {
	if dropped <= 0 {
		return nil
	}
	return captureLossEvidenceConflicts(evidenceCaptureLoss{
		present:       true,
		count:         uint64(dropped),
		causes:        evidenceCaptureLossCauseSet(0).with(evidenceCaptureLossObservationCap),
		firstCause:    evidenceCaptureLossObservationCap,
		firstIdentity: terminalEnvelopeLossIdentity,
	})
}
