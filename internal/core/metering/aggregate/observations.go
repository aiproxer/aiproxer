package aggregate

import (
	"container/heap"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/metering/replay"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

var (
	// ErrAmbiguousCumulative means two non-additive snapshots have the same
	// declared stream position and no revision ordering can select one.
	ErrAmbiguousCumulative = errors.New("metering/aggregate: ambiguous cumulative observation")
	// ErrIdentityConflict is the reducer-facing spelling of the shared replay
	// identity error. It remains one authority, owned by replay.
	ErrIdentityConflict = replay.ErrIdentityConflict
)

// Scope is the complete source/subject/charge scope of one reduced measure.
// A component key is not sufficient identity: local and provider evidence,
// distinct boundaries/perspectives, accounts, charge events, streams and
// subjects remain separate.
type Scope struct {
	StoreID     string
	TenantID    string
	AccountKey  string
	Origin      string
	Acquisition string
	Perspective metering.EconomicPerspective
	Boundary    metering.Boundary
	Lifecycle   metering.LifecycleScope
	Subject     metering.SubjectRef
	StreamID    string
	ChargeScope string
	// lineageKey is the normalized Subject/Correlation identity. Keep the
	// original Subject for audit/debug output, while using one identity for
	// reduction keys when the carriers split the same lineage fields.
	lineageKey string
}

type observationIdentity struct {
	store, id string
	revision  uint64
}

// Key returns a delimiter-safe deterministic scope key.
func (s Scope) Key() string {
	return lengthPrefixed(
		s.StoreID,
		s.TenantID,
		s.AccountKey,
		s.Origin,
		s.Acquisition,
		string(s.Perspective),
		string(s.Boundary),
		string(s.Lifecycle),
		s.SubjectIdentity(),
		s.StreamID,
		s.ChargeScope,
	)
}

func (s Scope) SubjectIdentity() string {
	if s.lineageKey != "" {
		return s.lineageKey
	}
	bytes, _ := json.Marshal(s.Subject)
	return string(bytes)
}

// ReducedMeasure is one exact component value after source-scoped reduction.
// Value is always present; absent/unknown input measures are represented by
// Snapshot.Complete and the retained canonical Observations instead.
type ReducedMeasure struct {
	Scope   Scope
	Key     metering.ComponentKey
	Value   metering.Decimal
	Quality string
	// Complete reports that this reduced component has an effective value.
	// Incomplete source revisions remain in SnapshotV2.Observations for audit,
	// but a superseded unavailable revision must not poison its replacement.
	Complete          bool
	LastSequence      uint64
	LastRevision      uint64
	LastObservationID string
}

// ReducedCharge retains each reported charge item without recomputing or
// combining monetary amounts. Coverage completeness is exposed separately.
type ReducedCharge struct {
	Scope         Scope
	ObservationID string
	Revision      uint64
	Charge        metering.ReportedCharge
	// Complete is field-local to this charge item. A correction can retain a
	// payable sibling charge while this item remains unresolved.
	Complete bool
	// unusable records that the incomplete field is rooted in a known
	// unavailable/missing baseline rather than a late reference. COGS may retain
	// the latter amount for partial reporting, but must exclude the former.
	unusable bool
}

// SnapshotV2 is the deterministic reduction result for canonical V2
// observations. It is source/subject/charge scoped and keeps canonical input
// observations for audit/replay rather than replacing them with a scalar map.
type SnapshotV2 struct {
	Measures          []ReducedMeasure
	Charges           []ReducedCharge
	Observations      []metering.Observation
	PendingCoverage   []metering.ChargeCoverageRef
	PendingSupersedes []metering.ObservationRef
	// UnusablePredecessors identifies resolved correction links whose
	// predecessor did not contain a usable baseline for the corrected field.
	// It is distinct from PendingSupersedes: the revision is known, but the
	// correction cannot be applied safely.
	UnusablePredecessors []metering.ObservationRef
	Unavailable          []string
	Complete             bool
	Payable              bool
	Replayed             int
	LastSequence         uint64
}

// ApplyObservations deduplicates exact source-event revisions, validates
// supersession/coverage graphs, then reduces each source-scoped stream in
// declared Sequence/Revision order. Graph validation deliberately occurs only
// after exact replay deduplication so a valid duplicate node is harmless.
func ApplyObservations(observations []metering.Observation) (SnapshotV2, error) {
	replayed, err := replay.Deduplicate(observations)
	if err != nil {
		return SnapshotV2{}, err
	}
	ordered := replay.Sort(replayed.Observations)
	if err := metering.ValidateSupersessionGraph(ordered); err != nil {
		return SnapshotV2{}, err
	}
	// Supersession is an effective-state operation for completeness as well
	// as reduction. Keep the source revisions in the audit envelope, while
	// ignoring an unavailable/unknown predecessor once a verified replacement
	// is present in this replay batch.
	known := make(map[observationIdentity]metering.Observation, len(ordered))
	for _, observation := range ordered {
		known[observationIdentity{store: observation.Subject.StoreID, id: observation.ID, revision: observation.Revision}] = observation
	}
	superseded := make(map[observationIdentity]struct{})
	for _, observation := range ordered {
		for _, ref := range observation.Supersedes {
			identity := observationIdentity{store: ref.StoreID, id: ref.ObservationID, revision: ref.Revision}
			if _, exists := known[identity]; exists {
				superseded[identity] = struct{}{}
			}
		}
	}
	// Historical superseded observations remain in the audit envelope, but their
	// coverage edges cannot affect effective graph validation. In particular, a
	// replacement is allowed to remove or change an inclusive edge without the
	// predecessor's stale relation suppressing the current child.
	effectiveObservations := effectiveCoverageObservations(ordered, superseded)
	if err := metering.ValidateCoverageGraph(effectiveObservations); err != nil {
		return SnapshotV2{}, err
	}
	if err := rejectAmbiguousSnapshots(effectiveObservations); err != nil {
		return SnapshotV2{}, err
	}

	snapshot := SnapshotV2{
		Observations: make([]metering.Observation, len(ordered)),
		Complete:     true,
		Payable:      true,
		Replayed:     replayed.Replayed,
	}
	charges := make(map[string]ReducedCharge)
	for i, observation := range ordered {
		snapshot.Observations[i] = observation.Clone()
		if observation.Sequence > snapshot.LastSequence {
			snapshot.LastSequence = observation.Sequence
		}
		if _, replaced := superseded[observationIdentity{store: observation.Subject.StoreID, id: observation.ID, revision: observation.Revision}]; replaced {
			continue
		}
		if observation.Authority == metering.AuthorityUnavailableClaim {
			snapshot.Unavailable = append(snapshot.Unavailable, observation.ID)
			snapshot.Complete = false
			snapshot.Payable = false
		}
		for _, measure := range observation.Measures {
			if measure.Value == nil || measure.Quality == metering.QualityUnknown || measure.Quality == metering.QualityUnavailable {
				snapshot.Complete = false
				snapshot.Payable = false
			}
		}
	}

	snapshot.PendingSupersedes = pendingSupersession(ordered, superseded)
	if len(snapshot.PendingSupersedes) != 0 {
		snapshot.Complete = false
		snapshot.Payable = false
	}
	var incompleteCorrectionFields map[observationIdentity]map[string]struct{}
	snapshot.UnusablePredecessors, incompleteCorrectionFields = unusableCorrectionPredecessors(ordered, known, superseded)
	if len(snapshot.UnusablePredecessors) != 0 {
		snapshot.Complete = false
		snapshot.Payable = false
	}
	// Correction diagnostics are attached to the correction node for the
	// historical replay view. A later delta is still additive, however, and a
	// partial replacement may supersede the correction without carrying the
	// affected field. Keep a second, chronology-aware taint view so either
	// successor remains incomplete until that exact field is replaced by
	// complete authoritative evidence.
	correctionTaintFields := allCorrectionTaintFields(ordered, known)
	// Effective reduction follows the explicit supersession graph: a
	// supersession predecessor is applied before its successor so an older
	// explicit revision delivered with a higher Sequence cannot overwrite newer
	// state, while a present-field replacement still retains the predecessor's
	// omitted fields. The immutable audit envelope is untouched.
	reductionOrder := supersessionOrder(ordered)
	propagatedFieldTaint := propagateFieldTaint(reductionOrder, correctionTaintFields)
	unusableCorrectionFields := knownUnusableCorrectionFields(ordered, known, correctionTaintFields)
	propagatedUnusableFields := propagateFieldTaint(reductionOrder, unusableCorrectionFields)
	states := make(map[string]*reductionState)
	for _, observation := range reductionOrder {
		scope := scopeFor(observation)
		key := scope.Key()
		state := states[key]
		if state == nil {
			state = &reductionState{scope: scope, values: make(map[string]ReducedMeasure)}
			states[key] = state
		}
		if err := state.apply(observation); err != nil {
			return SnapshotV2{}, err
		}
		for _, charge := range observation.Charges {
			applyCharge(charges, scope, observation, charge)
		}
	}
	// An unavailable source observation may be superseded while an omitted
	// charge item remains effective under partial replacement. Retain that
	// authority diagnostic with the effective charge item as well as in the
	// immutable observation envelope.
	effectiveUnavailable := make(map[string]struct{})
	for _, observation := range ordered {
		if observation.Authority != metering.AuthorityUnavailableClaim {
			continue
		}
		for _, charge := range observation.Charges {
			if _, ok := charges[effectiveChargeKey(scopeFor(observation), observation.ID, observation.Revision, charge.ChargeItemID)]; ok {
				effectiveUnavailable[observation.ID] = struct{}{}
				break
			}
		}
	}
	for observationID := range effectiveUnavailable {
		if !slices.Contains(snapshot.Unavailable, observationID) {
			snapshot.Unavailable = append(snapshot.Unavailable, observationID)
		}
	}
	// Coverage diagnostics follow the effective charge-item view. A partial
	// replacement can supersede an observation while retaining an omitted
	// charge item; that item's unresolved coverage must remain diagnostic. A
	// fully replaced charge item has no effective source and its historical
	// coverage remains audit-only.
	var pendingCoverageCharges map[string]struct{}
	snapshot.PendingCoverage, pendingCoverageCharges = pendingCoverage(ordered, superseded, charges)
	if len(snapshot.PendingCoverage) != 0 {
		snapshot.Complete = false
		snapshot.Payable = false
	}
	// A superseded observation can still carry an unresolved field that no
	// later present-field correction replaces. Keep the snapshot fail-closed
	// for that field-local gap; a healthy effective sibling does not make the
	// entire source complete. The billing projection separately retains the
	// missing field as an incomplete diagnostic line.
	effectiveMeasureKeys := make(map[string]struct{})
	for _, state := range states {
		for key := range state.values {
			effectiveMeasureKeys[state.scope.Key()+"\x00"+key] = struct{}{}
		}
	}
	for _, observation := range ordered {
		for _, measure := range observation.Measures {
			if measure.Value != nil && measure.Quality != metering.QualityUnknown && measure.Quality != metering.QualityUnavailable {
				continue
			}
			key, keyErr := measure.Key.Normalize()
			if keyErr != nil {
				continue
			}
			if _, present := effectiveMeasureKeys[scopeFor(observation).Key()+"\x00"+key.CanonicalKey()]; !present {
				snapshot.Complete = false
				snapshot.Payable = false
			}
		}
	}
	for key, charge := range charges {
		identity := observationIdentity{store: charge.Scope.Subject.StoreID, id: charge.ObservationID, revision: charge.Revision}
		if fields := incompleteCorrectionFields[identity]; fields != nil {
			if _, incomplete := fields[chargeFieldKey(charge.Charge.ChargeItemID)]; incomplete {
				charge.Complete = false
			}
		}
		if fields := propagatedFieldTaint[identity]; fields != nil {
			if _, incomplete := fields[chargeFieldKey(charge.Charge.ChargeItemID)]; incomplete {
				charge.Complete = false
			}
		}
		if fields := propagatedUnusableFields[identity]; fields != nil {
			if _, unusable := fields[chargeFieldKey(charge.Charge.ChargeItemID)]; unusable {
				charge.Complete = false
				charge.unusable = true
			}
		}
		if _, pending := pendingCoverageCharges[key]; pending {
			charge.Complete = false
		}
		if !charge.Complete {
			snapshot.Complete = false
			snapshot.Payable = false
		}
		charges[key] = charge
	}
	for _, charge := range charges {
		snapshot.Charges = append(snapshot.Charges, charge)
	}

	for _, state := range states {
		for _, measure := range state.values {
			identity := observationIdentity{store: measure.Scope.Subject.StoreID, id: measure.LastObservationID, revision: measure.LastRevision}
			if fields := incompleteCorrectionFields[identity]; fields != nil {
				if _, incomplete := fields[measureFieldKey(measure.Key)]; incomplete {
					measure.Complete = false
				}
			}
			if fields := propagatedFieldTaint[identity]; fields != nil {
				if _, incomplete := fields[measureFieldKey(measure.Key)]; incomplete {
					measure.Complete = false
				}
			}
			if !measure.Complete {
				snapshot.Complete = false
				snapshot.Payable = false
			}
			snapshot.Measures = append(snapshot.Measures, measure)
		}
	}
	sort.Slice(snapshot.Measures, func(i, j int) bool {
		if snapshot.Measures[i].Scope.Key() != snapshot.Measures[j].Scope.Key() {
			return snapshot.Measures[i].Scope.Key() < snapshot.Measures[j].Scope.Key()
		}
		return snapshot.Measures[i].Key.CanonicalKey() < snapshot.Measures[j].Key.CanonicalKey()
	})
	sort.Slice(snapshot.Charges, func(i, j int) bool {
		left := snapshot.Charges[i]
		right := snapshot.Charges[j]
		if left.Scope.Key() != right.Scope.Key() {
			return left.Scope.Key() < right.Scope.Key()
		}
		if left.ObservationID != right.ObservationID {
			return left.ObservationID < right.ObservationID
		}
		if left.Revision != right.Revision {
			return left.Revision < right.Revision
		}
		return left.Charge.ChargeItemID < right.Charge.ChargeItemID
	})
	slices.Sort(snapshot.Unavailable)
	return snapshot, nil
}

// supersessionOrder returns the deterministic order used for effective
// reduction. Every observation that a successor supersedes is emitted before
// that successor, so source revision order governs effective state for one
// source event even when a provider supplies a reversed or reused Sequence.
// Independent observations keep their declared Sequence order; ties fall back
// to the input position so the result is stable. The input is already
// cycle-checked by ValidateSupersessionGraph, so the traversal is a defensive
// fallback rather than a correctness gate.
func supersessionOrder(observations []metering.Observation) []metering.Observation {
	if len(observations) < 2 {
		return observations
	}
	index := make(map[observationIdentity]int, len(observations))
	hasSuccessor := false
	for i, observation := range observations {
		index[observationIdentity{store: observation.Subject.StoreID, id: observation.ID, revision: observation.Revision}] = i
	}
	successors := make([][]int, len(observations))
	indegree := make([]int, len(observations))
	for i, observation := range observations {
		for _, ref := range observation.Supersedes {
			predecessor, ok := index[observationIdentity{store: ref.StoreID, id: ref.ObservationID, revision: ref.Revision}]
			if !ok || predecessor == i {
				continue
			}
			successors[predecessor] = append(successors[predecessor], i)
			indegree[i]++
			hasSuccessor = true
		}
	}
	if !hasSuccessor {
		return observations
	}
	ready := &reductionCandidateHeap{observations: observations}
	for i := range observations {
		if indegree[i] == 0 {
			heap.Push(ready, i)
		}
	}
	out := make([]metering.Observation, 0, len(observations))
	emitted := make([]bool, len(observations))
	for ready.Len() > 0 {
		next, ok := heap.Pop(ready).(int)
		if !ok {
			panic("metering: reduction heap pop yielded a non-int candidate")
		}
		emitted[next] = true
		out = append(out, observations[next])
		for _, successor := range successors[next] {
			indegree[successor]--
			if indegree[successor] == 0 {
				heap.Push(ready, successor)
			}
		}
	}
	if len(out) != len(observations) {
		// Defensive: a caller that bypassed supersession validation could supply
		// a cycle. Emit the remaining observations in their original order so the
		// reducer stays total instead of looping.
		for i := range observations {
			if !emitted[i] {
				out = append(out, observations[i])
			}
		}
	}
	return out
}

// reductionCandidateHeap orders ready topological nodes by declared Sequence and
// then input position so independent observations keep Sequence order.
type reductionCandidateHeap struct {
	observations []metering.Observation
	items        []int
}

func (h reductionCandidateHeap) Len() int { return len(h.items) }
func (h reductionCandidateHeap) Less(i, j int) bool {
	left, right := h.items[i], h.items[j]
	if h.observations[left].Sequence != h.observations[right].Sequence {
		return h.observations[left].Sequence < h.observations[right].Sequence
	}
	return left < right
}
func (h reductionCandidateHeap) Swap(i, j int) { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *reductionCandidateHeap) Push(value any) {
	item, ok := value.(int)
	if !ok {
		panic("metering: reduction heap push requires an int candidate")
	}
	h.items = append(h.items, item)
}

func (h *reductionCandidateHeap) Pop() any {
	items := h.items
	last := len(items) - 1
	value := items[last]
	h.items = items[:last]
	return value
}

// effectiveCoverageObservations returns the non-superseded source revisions
// with coverage edges projected onto effective observations. The returned
// values are validation-only clones; SnapshotV2.Observations remains the
// immutable audit envelope, including historical edges.
func effectiveCoverageObservations(observations []metering.Observation, superseded map[observationIdentity]struct{}) []metering.Observation {
	out := make([]metering.Observation, 0, len(observations))
	for _, observation := range observations {
		identity := observationIdentity{store: observation.Subject.StoreID, id: observation.ID, revision: observation.Revision}
		if _, replaced := superseded[identity]; replaced {
			continue
		}
		clone := observation.Clone()
		for i := range clone.Charges {
			if len(clone.Charges[i].Covers) == 0 {
				continue
			}
			filtered := make([]metering.ChargeCoverageRef, 0, len(clone.Charges[i].Covers))
			for _, coverage := range clone.Charges[i].Covers {
				refIdentity := observationIdentity{
					store: coverage.Ref.StoreID, id: coverage.Ref.ObservationID, revision: coverage.Ref.Revision,
				}
				if _, replaced := superseded[refIdentity]; replaced {
					continue
				}
				filtered = append(filtered, coverage)
			}
			clone.Charges[i].Covers = filtered
		}
		out = append(out, clone)
	}
	return out
}

// EffectiveChargeObservations projects the reduced charge snapshot onto
// canonical observations. Only effective, field-complete charge items are
// returned, and every coverage edge is retained only when its target is also
// effective. Unknown or superseded targets therefore remain audit diagnostics
// rather than participating in effective coverage traversal.
func EffectiveChargeObservations(snapshot SnapshotV2) []metering.Observation {
	return effectiveChargeObservations(snapshot, false)
}

// EffectiveChargeGraphObservations projects the reduced charge snapshot onto
// canonical observations while preserving the unfiltered effective coverage
// graph: every non-superseded charge item is retained, including incomplete
// fields, and every coverage edge is retained verbatim, including edges to an
// unavailable target. It differs from EffectiveChargeObservations only in not
// dropping incomplete charges or dangling edges, so a caller that owns closed
// graph validation can reject or diagnose an invalid graph instead of silently
// losing the edge. Superseded revisions and charge items removed by a same-item
// replacement remain excluded because the reducer owns that reduction.
func EffectiveChargeGraphObservations(snapshot SnapshotV2) []metering.Observation {
	type observationKey struct {
		scope    string
		id       string
		revision uint64
	}
	if len(snapshot.Charges) == 0 {
		return nil
	}
	byObservation := make(map[observationKey][]metering.ReportedCharge)
	for _, reduced := range snapshot.Charges {
		key := observationKey{scope: reduced.Scope.Key(), id: reduced.ObservationID, revision: reduced.Revision}
		byObservation[key] = append(byObservation[key], reduced.Charge.Clone())
	}
	if len(byObservation) == 0 {
		return nil
	}
	out := make([]metering.Observation, 0, len(byObservation))
	for _, observation := range snapshot.Observations {
		key := observationKey{scope: scopeFor(observation).Key(), id: observation.ID, revision: observation.Revision}
		charges, ok := byObservation[key]
		if !ok {
			continue
		}
		clone := observation.Clone()
		clone.Charges = append([]metering.ReportedCharge(nil), charges...)
		out = append(out, clone)
	}
	return out
}

// EffectiveChargeObservationsForCOGS keeps an amount-bearing correction with
// only a late/unresolved predecessor reference available for COGS attribution.
// The caller still receives SnapshotV2.PendingSupersedes and must mark the
// result partial/non-payable. A correction whose known predecessor has an
// unusable baseline remains excluded, preserving the fail-closed charge taint.
func EffectiveChargeObservationsForCOGS(snapshot SnapshotV2) []metering.Observation {
	return effectiveChargeObservations(snapshot, true)
}

func effectiveChargeObservations(snapshot SnapshotV2, retainPending bool) []metering.Observation {
	type observationKey struct {
		scope    string
		id       string
		revision uint64
	}
	byObservation := make(map[observationKey][]metering.ReportedCharge)
	effectiveCharges := make(map[string]struct{})
	for _, reduced := range snapshot.Charges {
		if !reduced.Complete && (!retainPending || reduced.unusable) {
			continue
		}
		key := observationKey{scope: reduced.Scope.Key(), id: reduced.ObservationID, revision: reduced.Revision}
		byObservation[key] = append(byObservation[key], reduced.Charge.Clone())
		effectiveCharges[chargeIdentityKey(reduced.Scope.StoreID, reduced.ObservationID, reduced.Revision, reduced.Charge.ChargeItemID)] = struct{}{}
	}
	if len(byObservation) == 0 {
		return nil
	}
	out := make([]metering.Observation, 0, len(byObservation))
	for _, observation := range snapshot.Observations {
		key := observationKey{scope: scopeFor(observation).Key(), id: observation.ID, revision: observation.Revision}
		charges, ok := byObservation[key]
		if !ok {
			continue
		}
		clone := observation.Clone()
		clone.Charges = append([]metering.ReportedCharge(nil), charges...)
		for i := range clone.Charges {
			if len(clone.Charges[i].Covers) == 0 {
				continue
			}
			filtered := make([]metering.ChargeCoverageRef, 0, len(clone.Charges[i].Covers))
			for _, coverage := range clone.Charges[i].Covers {
				if _, ok := effectiveCharges[chargeIdentityKey(
					coverage.Ref.StoreID, coverage.Ref.ObservationID, coverage.Ref.Revision, coverage.Ref.ChargeItemID,
				)]; ok {
					filtered = append(filtered, coverage)
				}
			}
			clone.Charges[i].Covers = filtered
		}
		out = append(out, clone)
	}
	return out
}

func chargeIdentityKey(storeID, observationID string, revision uint64, chargeItemID string) string {
	return lengthPrefixed(storeID, observationID, strconv.FormatUint(revision, 10), chargeItemID)
}

// applyCharge keeps the effective reported-charge view revision aware while
// retaining every immutable source observation in SnapshotV2. A correction or
// replacement that supersedes a prior observation and carries the same charge
// item replaces that effective item; a differently named item remains an
// explicit adjustment/auxiliary charge. Ordinary distinct source events are
// never collapsed merely because their item IDs or amounts match.
func applyCharge(dst map[string]ReducedCharge, scope Scope, observation metering.Observation, charge metering.ReportedCharge) {
	if (observation.Semantics == metering.SemanticsCorrection || observation.Semantics == metering.SemanticsReplacement) && !isChargeAdjustment(observation, charge) {
		for key, prior := range dst {
			if chargeStreamKey(prior.Scope) != chargeStreamKey(scope) || prior.Charge.ChargeItemID != charge.ChargeItemID {
				continue
			}
			if supersedesObservation(observation.Supersedes, prior.ObservationID, prior.Revision, prior.Scope.Subject.StoreID, scope, prior.Scope) {
				delete(dst, key)
			}
		}
	}
	effectiveKey := effectiveChargeKey(scope, observation.ID, observation.Revision, charge.ChargeItemID)
	unavailable := observation.Authority == metering.AuthorityUnavailableClaim
	dst[effectiveKey] = ReducedCharge{
		Scope: scope, ObservationID: observation.ID, Revision: observation.Revision, Charge: charge.Clone(),
		Complete: !unavailable, unusable: unavailable,
	}
}

func effectiveChargeKey(scope Scope, observationID string, revision uint64, chargeItemID string) string {
	return lengthPrefixed(scope.Key(), observationID, strconv.FormatUint(revision, 10), chargeItemID)
}

// chargeStreamKey excludes the inferred charge-item axis. An observation
// containing one charge gets a charge-specific Scope, while a multi-charge
// observation does not; supersession must still replace the same charge item
// across those equivalent source streams.
func chargeStreamKey(scope Scope) string {
	scope.ChargeScope = ""
	return scope.Key()
}

func isChargeAdjustment(observation metering.Observation, charge metering.ReportedCharge) bool {
	if observation.Semantics != metering.SemanticsCorrection {
		return charge.Kind == metering.ChargeKindAdjustment || charge.Kind == metering.ChargeKindCredit
	}
	if charge.Kind == metering.ChargeKindAdjustment || charge.Kind == metering.ChargeKindCredit || observation.MappingRef == metering.LegacyV1MappingRef {
		return true
	}
	if charge.Amount == nil {
		return false
	}
	value, err := charge.Amount.Normalize()
	return err == nil && strings.HasPrefix(value.Coefficient, "-")
}

func supersedesObservation(refs []metering.ObservationRef, observationID string, revision uint64, storeID string, currentScope, priorScope Scope) bool {
	// Graph validation rejects every resolved edge whose complete source or
	// charge scope differs. Keep the reduction owner defensive as well: this
	// prevents a future caller from deleting a sibling partition if it bypasses
	// the batch validator.
	if chargeStreamKey(currentScope) != chargeStreamKey(priorScope) {
		return false
	}
	for _, ref := range refs {
		if ref.StoreID == storeID && ref.ObservationID == observationID && ref.Revision == revision {
			return true
		}
	}
	return false
}

// ApplyFacts is the explicit one-way V1 bridge. Historical facts are lifted
// through the SDK's trusted reader and then use this same V2 reduction owner;
// Fact identity and old hashes are never rewritten.
func ApplyFacts(facts []metering.Fact) (SnapshotV2, error) {
	observations := make([]metering.Observation, 0, len(facts))
	for i, fact := range facts {
		observation, err := metering.ObservationFromFact(fact)
		if err != nil {
			return SnapshotV2{}, fmt.Errorf("metering/aggregate: V1 fact[%d]: %w", i, err)
		}
		observations = append(observations, observation)
	}
	return ApplyObservations(observations)
}

// ValueFor returns the reduced value for the same source scope and component
// key as observation. An invalid or absent value returns an empty string.
func (s SnapshotV2) ValueFor(observation metering.Observation, key metering.ComponentKey) string {
	normalized, err := key.Normalize()
	if err != nil {
		return ""
	}
	scopeKey := scopeFor(observation).Key()
	for _, measure := range s.Measures {
		if measure.Scope.Key() == scopeKey && measure.Key.Equal(normalized) {
			return measure.Value.CanonicalString()
		}
	}
	return ""
}

// ValueForObservationID is a narrow diagnostic lookup for bridge/TCK tests.
// The observation ID is not itself a reduction key; all identity axes remain
// retained in the matching reduced measure.
func (s SnapshotV2) ValueForObservationID(observationID, component string) string {
	for _, measure := range s.Measures {
		if measure.LastObservationID == observationID && measure.Key.Component == component {
			return measure.Value.CanonicalString()
		}
	}
	return ""
}

type reductionState struct {
	scope  Scope
	values map[string]ReducedMeasure
}

func (s *reductionState) apply(observation metering.Observation) error {
	for _, measure := range observation.Measures {
		if measure.Value == nil || measure.Quality == metering.QualityUnavailable || measure.Quality == metering.QualityNotApplicable {
			continue
		}
		key, err := measure.Key.Normalize()
		if err != nil {
			return err
		}
		value, err := measure.Value.Normalize()
		if err != nil {
			return err
		}
		canonicalKey := key.CanonicalKey()
		prior, exists := s.values[canonicalKey]
		switch observation.Semantics {
		case metering.SemanticsDelta, metering.SemanticsCorrection:
			if exists {
				value, err = addDecimal(prior.Value, value)
				if err != nil {
					return err
				}
			}
		case metering.SemanticsCumulative, metering.SemanticsGauge, metering.SemanticsReplacement:
			// Replacement is intentionally present-field only. A missing measure
			// never reaches this loop and therefore cannot erase an unrelated key.
		default:
			return fmt.Errorf("metering/aggregate: unsupported observation semantics %q", observation.Semantics)
		}
		s.values[canonicalKey] = ReducedMeasure{
			Scope: s.scope, Key: key, Value: value, Quality: measure.Quality,
			Complete:     true,
			LastSequence: observation.Sequence, LastRevision: observation.Revision, LastObservationID: observation.ID,
		}
	}
	return nil
}

func scopeFor(observation metering.Observation) Scope {
	tenant := observation.Correlation.TenantID
	if tenant == "" {
		tenant = observation.Subject.TenantID
	}
	account := strings.TrimSpace(observation.Correlation.ProviderAccountKey)
	if account == "" {
		account = strings.TrimSpace(observation.Subject.ProviderAccountKey)
	}
	charge := strings.TrimSpace(observation.Correlation.ProviderChargeID)
	if charge == "" {
		charge = strings.TrimSpace(observation.Subject.ProviderChargeID)
	}
	if charge == "" && len(observation.Charges) == 1 {
		charge = strings.TrimSpace(observation.Charges[0].ChargeItemID)
	}
	return Scope{
		StoreID: observation.Correlation.StoreID, TenantID: tenant,
		AccountKey: account, Origin: observation.Origin, Acquisition: observation.Acquisition,
		Perspective: observation.Perspective, Boundary: observation.Boundary, Lifecycle: observation.Lifecycle,
		Subject: observation.Subject.Clone(), StreamID: observation.StreamID, ChargeScope: charge,
		lineageKey: observation.NormalizedLineageIdentity(),
	}
}

// ScopeFor exposes the canonical source scope used by ApplyObservations to
// consumers that project reduced measures at a later domain boundary. The
// reducer remains the sole owner of scope construction; callers must not
// rebuild source identity from component keys alone.
func ScopeFor(observation metering.Observation) Scope {
	return scopeFor(observation)
}

func rejectAmbiguousSnapshots(observations []metering.Observation) error {
	type position struct {
		scope    string
		sequence uint64
		revision uint64
	}
	positions := make(map[position][]metering.Observation)
	for _, observation := range observations {
		if observation.Semantics != metering.SemanticsCumulative && observation.Semantics != metering.SemanticsGauge {
			continue
		}
		key := position{scope: scopeFor(observation).Key(), sequence: observation.Sequence, revision: observation.Revision}
		positions[key] = append(positions[key], observation)
	}
	for _, samePosition := range positions {
		for i := range samePosition {
			for j := i + 1; j < len(samePosition); j++ {
				if overlappingMeasureKey(samePosition[i], samePosition[j]) {
					return fmt.Errorf("%w: sequence=%d revision=%d scope=%s", ErrAmbiguousCumulative,
						samePosition[i].Sequence, samePosition[i].Revision, scopeFor(samePosition[i]).Key())
				}
			}
		}
	}
	return nil
}

func overlappingMeasureKey(a, b metering.Observation) bool {
	keys := make(map[string]struct{}, len(a.Measures))
	for _, measure := range a.Measures {
		if measure.Value != nil {
			keys[measure.Key.CanonicalKey()] = struct{}{}
		}
	}
	for _, measure := range b.Measures {
		if measure.Value != nil {
			if _, ok := keys[measure.Key.CanonicalKey()]; ok {
				return true
			}
		}
	}
	return false
}

func pendingCoverage(observations []metering.Observation, superseded map[observationIdentity]struct{}, effectiveCharges map[string]ReducedCharge) ([]metering.ChargeCoverageRef, map[string]struct{}) {
	type node struct {
		store, observation string
		revision           uint64
		item               string
	}
	known := make(map[node]struct{})
	effectiveItems := make(map[node]struct{}, len(effectiveCharges))
	for _, charge := range effectiveCharges {
		effectiveItems[node{
			store: charge.Scope.Subject.StoreID, observation: charge.ObservationID,
			revision: charge.Revision, item: charge.Charge.ChargeItemID,
		}] = struct{}{}
	}
	for _, observation := range observations {
		for _, charge := range observation.Charges {
			known[node{observation.Subject.StoreID, observation.ID, observation.Revision, charge.ChargeItemID}] = struct{}{}
		}
	}
	seen := make(map[string]struct{})
	pendingCharges := make(map[string]struct{})
	var pending []metering.ChargeCoverageRef
	for _, observation := range observations {
		for _, charge := range observation.Charges {
			source := node{observation.Subject.StoreID, observation.ID, observation.Revision, charge.ChargeItemID}
			if _, ok := effectiveItems[source]; !ok {
				continue
			}
			for _, coverage := range charge.Covers {
				ref := coverage.Ref
				refIdentity := observationIdentity{store: ref.StoreID, id: ref.ObservationID, revision: ref.Revision}
				target := node{ref.StoreID, ref.ObservationID, ref.Revision, ref.ChargeItemID}
				if _, ok := known[target]; ok {
					// A link to a known charge never generates pending
					// coverage: an effective link remains resolved, while a
					// fully replaced item is audit-only.
					continue
				}
				if _, obsolete := superseded[refIdentity]; obsolete {
					continue
				}
				key := ref.StoreID + "\x00" + ref.ObservationID + "\x00" + strconv.FormatUint(ref.Revision, 10) + "\x00" + ref.ChargeItemID + "\x00" + string(coverage.Relation)
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				pending = append(pending, coverage)
				pendingCharges[effectiveChargeKey(scopeFor(observation), observation.ID, observation.Revision, charge.ChargeItemID)] = struct{}{}
			}
		}
	}
	sort.Slice(pending, func(i, j int) bool { return coverageKey(pending[i]) < coverageKey(pending[j]) })
	return pending, pendingCharges
}

func pendingSupersession(observations []metering.Observation, superseded map[observationIdentity]struct{}) []metering.ObservationRef {
	type node struct {
		store, observation string
		revision           uint64
	}
	known := make(map[node]struct{}, len(observations))
	for _, observation := range observations {
		known[node{observation.Subject.StoreID, observation.ID, observation.Revision}] = struct{}{}
	}
	seen := make(map[string]struct{})
	correctionDescendants := correctionDescendantSet(observations)
	var pending []metering.ObservationRef
	for _, observation := range observations {
		identity := observationIdentity{store: observation.Subject.StoreID, id: observation.ID, revision: observation.Revision}
		if observation.Semantics == metering.SemanticsCorrection {
			_, hasCorrectionDescendant := correctionDescendants[identity]
			if _, replaced := superseded[identity]; replaced && !hasCorrectionDescendant {
				// A complete replacement supersedes this correction. Its unresolved
				// ancestor is not an effective correction dependency.
				continue
			}
		} else if _, replaced := superseded[identity]; replaced {
			continue
		}
		for _, ref := range observation.Supersedes {
			refIdentity := observationIdentity{store: ref.StoreID, id: ref.ObservationID, revision: ref.Revision}
			if _, ok := known[node{ref.StoreID, ref.ObservationID, ref.Revision}]; ok {
				continue
			}
			if _, obsolete := superseded[refIdentity]; obsolete {
				continue
			}
			key := ref.StoreID + "\x00" + ref.ObservationID + "\x00" + strconv.FormatUint(ref.Revision, 10) + "\x00" + ref.PayloadHash
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			pending = append(pending, ref)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return supersessionKey(pending[i]) < supersessionKey(pending[j]) })
	return pending
}

// unusableCorrectionPredecessors separates field-local correction failures
// from genuinely pending supersessions. A correction is a delta/reconciliation
// operation, so its effective value cannot be invented for a field whose
// referenced baseline is unavailable, unknown, or missing that field. The
// returned map is keyed by correction identity and then by a measure/charge
// identity; healthy sibling fields in the same immutable observation remain
// usable. Replacement observations deliberately do not use this check: they
// carry a complete present-field projection by contract.
func unusableCorrectionPredecessors(observations []metering.Observation, known map[observationIdentity]metering.Observation, superseded map[observationIdentity]struct{}) ([]metering.ObservationRef, map[observationIdentity]map[string]struct{}) {
	seen := make(map[string]struct{})
	incomplete := make(map[observationIdentity]map[string]struct{})
	correctionDescendants := correctionDescendantSet(observations)
	var out []metering.ObservationRef
	// A correction may itself be the predecessor of a later correction. Walk
	// to a fixed point so an incomplete baseline remains incomplete
	// transitively, independent of delivery order within the replay batch.
	changed := true
	for changed {
		changed = false
		for _, correction := range observations {
			if correction.Semantics != metering.SemanticsCorrection {
				continue
			}
			identity := observationIdentity{store: correction.Subject.StoreID, id: correction.ID, revision: correction.Revision}
			_, hasCorrectionDescendant := correctionDescendants[identity]
			if _, replaced := superseded[identity]; replaced && !hasCorrectionDescendant {
				continue
			}
			fields := incomplete[identity]
			if fields == nil {
				fields = make(map[string]struct{})
				incomplete[identity] = fields
			}
			for _, measure := range correction.Measures {
				field := measureFieldKey(measure.Key)
				if _, alreadyIncomplete := fields[field]; alreadyIncomplete || !correctionFieldIncomplete(correction, field, known, incomplete) {
					continue
				}
				fields[field] = struct{}{}
				changed = true
			}
			for _, charge := range correction.Charges {
				if isChargeAdjustment(correction, charge) {
					continue
				}
				field := chargeFieldKey(charge.ChargeItemID)
				if _, alreadyIncomplete := fields[field]; alreadyIncomplete || !correctionFieldIncomplete(correction, field, known, incomplete) {
					continue
				}
				fields[field] = struct{}{}
				changed = true
			}
		}
	}
	for _, correction := range observations {
		if correction.Semantics != metering.SemanticsCorrection {
			continue
		}
		identity := observationIdentity{store: correction.Subject.StoreID, id: correction.ID, revision: correction.Revision}
		_, hasCorrectionDescendant := correctionDescendants[identity]
		if _, replaced := superseded[identity]; replaced && !hasCorrectionDescendant {
			continue
		}
		fields := incomplete[identity]
		if len(fields) == 0 {
			continue
		}
		// Keep every resolved edge as audit evidence for fields whose
		// correction cannot be charged. Unknown references remain owned by
		// PendingSupersedes.
		for _, ref := range correction.Supersedes {
			priorIdentity := observationIdentity{store: ref.StoreID, id: ref.ObservationID, revision: ref.Revision}
			if _, resolved := known[priorIdentity]; !resolved {
				continue
			}
			key := ref.StoreID + "\x00" + ref.ObservationID + "\x00" + strconv.FormatUint(ref.Revision, 10) + "\x00" + ref.PayloadHash
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, ref)
		}
	}
	sort.Slice(out, func(i, j int) bool { return supersessionKey(out[i]) < supersessionKey(out[j]) })
	return out, incomplete
}

// allCorrectionTaintFields computes correction-field diagnostics without
// applying effective supersession filtering. An unresolved correction that is
// later superseded by a partial replacement is still the last evidence for
// any field omitted by that replacement, so its taint must seed chronology
// propagation even though it is not an effective correction link itself.
func allCorrectionTaintFields(observations []metering.Observation, known map[observationIdentity]metering.Observation) map[observationIdentity]map[string]struct{} {
	incomplete := make(map[observationIdentity]map[string]struct{})
	changed := true
	for changed {
		changed = false
		for _, correction := range observations {
			if correction.Semantics != metering.SemanticsCorrection {
				continue
			}
			identity := observationIdentity{store: correction.Subject.StoreID, id: correction.ID, revision: correction.Revision}
			fields := incomplete[identity]
			if fields == nil {
				fields = make(map[string]struct{})
				incomplete[identity] = fields
			}
			for _, measure := range correction.Measures {
				field := measureFieldKey(measure.Key)
				if _, alreadyIncomplete := fields[field]; alreadyIncomplete || !correctionFieldIncomplete(correction, field, known, incomplete) {
					continue
				}
				fields[field] = struct{}{}
				changed = true
			}
			for _, charge := range correction.Charges {
				if isChargeAdjustment(correction, charge) {
					continue
				}
				field := chargeFieldKey(charge.ChargeItemID)
				if _, alreadyIncomplete := fields[field]; alreadyIncomplete || !correctionFieldIncomplete(correction, field, known, incomplete) {
					continue
				}
				fields[field] = struct{}{}
				changed = true
			}
		}
	}
	return incomplete
}

// knownUnusableCorrectionFields is the source-local subset of correction
// taint rooted in a known unusable predecessor. Unlike all correction taint,
// an unknown predecessor is merely pending and may be completed by a later
// replay batch, so it must not make an amount-bearing correction unrateable.
func knownUnusableCorrectionFields(observations []metering.Observation, known map[observationIdentity]metering.Observation, taint map[observationIdentity]map[string]struct{}) map[observationIdentity]map[string]struct{} {
	incomplete := make(map[observationIdentity]map[string]struct{})
	changed := true
	for changed {
		changed = false
		for _, correction := range observations {
			if correction.Semantics != metering.SemanticsCorrection {
				continue
			}
			identity := observationIdentity{store: correction.Subject.StoreID, id: correction.ID, revision: correction.Revision}
			fields := incomplete[identity]
			if fields == nil {
				fields = make(map[string]struct{})
				incomplete[identity] = fields
			}
			for _, measure := range correction.Measures {
				field := measureFieldKey(measure.Key)
				if _, exists := fields[field]; exists || !knownCorrectionFieldUnusable(correction, field, known, taint, incomplete) {
					continue
				}
				fields[field] = struct{}{}
				changed = true
			}
			for _, charge := range correction.Charges {
				if isChargeAdjustment(correction, charge) {
					continue
				}
				field := chargeFieldKey(charge.ChargeItemID)
				if _, exists := fields[field]; exists || !knownCorrectionFieldUnusable(correction, field, known, taint, incomplete) {
					continue
				}
				fields[field] = struct{}{}
				changed = true
			}
		}
	}
	return incomplete
}

func knownCorrectionFieldUnusable(correction metering.Observation, field string, known map[observationIdentity]metering.Observation, taint, incomplete map[observationIdentity]map[string]struct{}) bool {
	for _, ref := range correction.Supersedes {
		prior, exists := known[observationIdentity{store: ref.StoreID, id: ref.ObservationID, revision: ref.Revision}]
		if !exists || !observationCarriesField(prior, field) {
			continue
		}
		if fields := taint[observationIdentity{store: ref.StoreID, id: ref.ObservationID, revision: ref.Revision}]; fields != nil {
			if _, unusable := fields[field]; unusable {
				return true
			}
		}
		if fields := incomplete[observationIdentity{store: ref.StoreID, id: ref.ObservationID, revision: ref.Revision}]; fields != nil {
			if _, unusable := fields[field]; unusable {
				return true
			}
		}
		if strings.HasPrefix(field, "measure\x00") {
			if !priorHasUsableMeasureKey(prior, field[len("measure\x00"):]) {
				return true
			}
		}
		if strings.HasPrefix(field, "charge\x00") && !priorHasUsableCharge(prior, field[len("charge\x00"):]) {
			return true
		}
	}
	return false
}

// propagateFieldTaint carries unresolved fields through every chronological
// successor in the same source scope. Deltas and corrections cannot establish
// a missing baseline, while a complete cumulative, gauge, or replacement
// value can clear taint only for the field it carries. Omitted replacement
// fields leave their prior taint active, preserving independent sibling
// fields and charge items.
func propagateFieldTaint(observations []metering.Observation, correctionFields map[observationIdentity]map[string]struct{}) map[observationIdentity]map[string]struct{} {
	type taintKey struct {
		scope string
		field string
	}
	active := make(map[taintKey]struct{})
	byObservation := make(map[observationIdentity]map[string]struct{})
	for _, observation := range observations {
		identity := observationIdentity{store: observation.Subject.StoreID, id: observation.ID, revision: observation.Revision}
		measureScope := scopeFor(observation).Key()
		chargeScope := chargeStreamKey(scopeFor(observation))
		for field := range correctionFields[identity] {
			if strings.HasPrefix(field, "measure\x00") {
				active[taintKey{scope: measureScope, field: field}] = struct{}{}
			}
			if strings.HasPrefix(field, "charge\x00") {
				active[taintKey{scope: chargeScope, field: field}] = struct{}{}
			}
		}
		for _, measure := range observation.Measures {
			field := measureFieldKey(measure.Key)
			key := taintKey{scope: measureScope, field: field}
			_, tainted := active[key]
			if tainted {
				if taintClearedByMeasure(observation, measure) {
					delete(active, key)
					tainted = false
				} else {
					rememberTaintedField(byObservation, identity, field)
				}
			}
			if measureIsIncomplete(measure) && (tainted || taintCanStartAtObservation(observation)) {
				active[key] = struct{}{}
				rememberTaintedField(byObservation, identity, field)
			}
		}
		for _, charge := range observation.Charges {
			if isChargeAdjustment(observation, charge) {
				continue
			}
			field := chargeFieldKey(charge.ChargeItemID)
			key := taintKey{scope: chargeScope, field: field}
			_, tainted := active[key]
			if tainted {
				if taintClearedByCharge(observation, charge) {
					delete(active, key)
					tainted = false
				} else {
					rememberTaintedField(byObservation, identity, field)
				}
			}
			if charge.Amount == nil && (tainted || taintCanStartAtObservation(observation)) {
				active[key] = struct{}{}
				rememberTaintedField(byObservation, identity, field)
			}
		}
	}
	return byObservation
}

func rememberTaintedField(dst map[observationIdentity]map[string]struct{}, identity observationIdentity, field string) {
	fields := dst[identity]
	if fields == nil {
		fields = make(map[string]struct{})
		dst[identity] = fields
	}
	fields[field] = struct{}{}
}

func measureIsIncomplete(measure metering.Measure) bool {
	return measure.Value == nil || measure.Quality == metering.QualityUnknown || measure.Quality == metering.QualityUnavailable
}

func taintCanStartAtObservation(observation metering.Observation) bool {
	return observation.Semantics == metering.SemanticsCorrection || observation.Semantics == metering.SemanticsReplacement
}

func taintClearedByMeasure(observation metering.Observation, measure metering.Measure) bool {
	if observation.Semantics != metering.SemanticsCumulative && observation.Semantics != metering.SemanticsGauge && observation.Semantics != metering.SemanticsReplacement {
		return false
	}
	return priorHasUsableMeasure(observation, measure.Key)
}

func taintClearedByCharge(observation metering.Observation, charge metering.ReportedCharge) bool {
	if observation.Semantics != metering.SemanticsCumulative && observation.Semantics != metering.SemanticsReplacement {
		return false
	}
	return priorHasUsableCharge(observation, charge.ChargeItemID)
}

func correctionDescendantSet(observations []metering.Observation) map[observationIdentity]struct{} {
	set := make(map[observationIdentity]struct{})
	for _, observation := range observations {
		if observation.Semantics != metering.SemanticsCorrection {
			continue
		}
		for _, ref := range observation.Supersedes {
			set[observationIdentity{store: ref.StoreID, id: ref.ObservationID, revision: ref.Revision}] = struct{}{}
		}
	}
	return set
}

// correctionFieldIncomplete reports whether the correction's field lacks a
// complete deterministic baseline. Known parents that carry the field are all
// relevant: one unavailable/unknown/incomplete parent is enough to fail
// closed, even if another parent is usable. A known parent that does not carry
// the field is not relevant to that field, preserving independent sibling
// partitions. Unknown references are relevant when no known parent carries
// the field, because there is no evidence that can safely establish a
// baseline.
func correctionFieldIncomplete(correction metering.Observation, field string, known map[observationIdentity]metering.Observation, incomplete map[observationIdentity]map[string]struct{}) bool {
	hasRelevantParent := false
	hasUnknownParent := false
	hasUsableParent := false
	hasUnusableParent := false
	for _, ref := range correction.Supersedes {
		priorIdentity := observationIdentity{store: ref.StoreID, id: ref.ObservationID, revision: ref.Revision}
		prior, exists := known[priorIdentity]
		if !exists {
			hasUnknownParent = true
			continue
		}
		if !observationCarriesField(prior, field) {
			continue
		}
		hasRelevantParent = true
		if priorFields := incomplete[priorIdentity]; priorFields != nil {
			if _, priorIncomplete := priorFields[field]; priorIncomplete {
				hasUnusableParent = true
				continue
			}
		}
		if strings.HasPrefix(field, "measure\x00") {
			if priorHasUsableMeasureKey(prior, field[len("measure\x00"):]) {
				hasUsableParent = true
			} else {
				hasUnusableParent = true
			}
			continue
		}
		if strings.HasPrefix(field, "charge\x00") {
			if priorHasUsableCharge(prior, field[len("charge\x00"):]) {
				hasUsableParent = true
			} else {
				hasUnusableParent = true
			}
		}
	}
	if hasUnusableParent {
		return true
	}
	if !hasRelevantParent {
		return hasUnknownParent || len(correction.Supersedes) != 0
	}
	// A known usable parent is sufficient only when every relevant parent was
	// also usable. Unknown siblings are ignored here only when a known parent
	// establishes the field's source partition; otherwise they fail closed via
	// the no-relevant-parent branch above.
	return !hasUsableParent
}

func observationCarriesField(observation metering.Observation, field string) bool {
	if strings.HasPrefix(field, "measure\x00") {
		canonicalKey := field[len("measure\x00"):]
		for _, measure := range observation.Measures {
			key, err := measure.Key.Normalize()
			if err == nil && key.CanonicalKey() == canonicalKey {
				return true
			}
		}
		return false
	}
	if strings.HasPrefix(field, "charge\x00") {
		chargeItemID := field[len("charge\x00"):]
		for _, charge := range observation.Charges {
			if charge.ChargeItemID == chargeItemID {
				return true
			}
		}
	}
	return false
}

func measureFieldKey(key metering.ComponentKey) string {
	normalized, err := key.Normalize()
	if err != nil {
		return "measure\x00" + key.CanonicalKey()
	}
	return "measure\x00" + normalized.CanonicalKey()
}

func chargeFieldKey(chargeItemID string) string { return "charge\x00" + chargeItemID }

func priorHasUsableMeasureKey(prior metering.Observation, canonicalKey string) bool {
	for _, measure := range prior.Measures {
		key, err := measure.Key.Normalize()
		if err != nil || key.CanonicalKey() != canonicalKey {
			continue
		}
		return priorHasUsableMeasure(prior, key)
	}
	return false
}

func priorHasUsableMeasure(prior metering.Observation, key metering.ComponentKey) bool {
	if prior.Authority == metering.AuthorityUnavailableClaim {
		return false
	}
	normalized, err := key.Normalize()
	if err != nil {
		return false
	}
	for _, measure := range prior.Measures {
		if !measure.Key.Equal(normalized) || measure.Value == nil {
			continue
		}
		switch measure.Quality {
		case metering.QualityObserved, metering.QualityEstimated:
			return true
		}
	}
	return false
}

func priorHasUsableCharge(prior metering.Observation, chargeItemID string) bool {
	for _, charge := range prior.Charges {
		if charge.ChargeItemID == chargeItemID && charge.Amount != nil {
			return true
		}
	}
	return false
}

func coverageKey(ref metering.ChargeCoverageRef) string {
	return ref.Ref.StoreID + "\x00" + ref.Ref.ObservationID + "\x00" + strconv.FormatUint(ref.Ref.Revision, 10) + "\x00" + ref.Ref.ChargeItemID + "\x00" + string(ref.Relation)
}

func supersessionKey(ref metering.ObservationRef) string {
	return ref.StoreID + "\x00" + ref.ObservationID + "\x00" + strconv.FormatUint(ref.Revision, 10) + "\x00" + ref.PayloadHash
}

func addDecimal(a, b metering.Decimal) (metering.Decimal, error) {
	left, err := a.Normalize()
	if err != nil {
		return metering.Decimal{}, err
	}
	right, err := b.Normalize()
	if err != nil {
		return metering.Decimal{}, err
	}
	leftInt, ok := new(big.Int).SetString(left.Coefficient, 10)
	if !ok {
		return metering.Decimal{}, fmt.Errorf("%w: invalid left coefficient", ErrOverflow)
	}
	rightInt, ok := new(big.Int).SetString(right.Coefficient, 10)
	if !ok {
		return metering.Decimal{}, fmt.Errorf("%w: invalid right coefficient", ErrOverflow)
	}
	scale := max(right.Scale, left.Scale)
	if scale > left.Scale {
		leftInt.Mul(leftInt, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale-left.Scale)), nil))
	}
	if scale > right.Scale {
		rightInt.Mul(rightInt, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale-right.Scale)), nil))
	}
	leftInt.Add(leftInt, rightInt)
	result, err := (metering.Decimal{Coefficient: leftInt.String(), Scale: scale}).Normalize()
	if err != nil {
		return metering.Decimal{}, fmt.Errorf("%w: %w", ErrOverflow, err)
	}
	return result, nil
}

func lengthPrefixed(values ...string) string {
	var b strings.Builder
	for _, value := range values {
		b.WriteString(strconv.Itoa(len(value)))
		b.WriteByte(':')
		b.WriteString(value)
	}
	return b.String()
}
