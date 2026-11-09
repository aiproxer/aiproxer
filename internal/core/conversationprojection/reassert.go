package conversationprojection

import (
	"fmt"
	"sort"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Reassert restores the frozen conversation view after late attempt/candidate/interleaved transforms.
// It is pure and uses no store read. It removes any reintroduced never_backend messages and
// ensures each active overlay in snap is present exactly once at its frozen placement
// (rebuild via Project). It handles late calls that are already based on the projected baseline
// by stripping existing projection-owned copies (identified via placement-aware provenance) before
// re-projecting, to avoid duplicate injection. Identity collisions are handled by using
// exact injected trajectory positions (InjectedTrajectoryIndex / InjectedItemIndex / InjectedLegacy*)
// plus anchor/slot order, preserving legitimate same role/text messages. If ambiguity remains
// (multiple identical copies indistinguishable), it fails closed rather than silently deleting.
// FilteredBaseline is the frozen filtered call (without never_backend, without steering) carried
// from early projection; when provided it is used to distinguish legitimate user collisions from
// duplicate steering. If empty, it is derived on the fly.
//
// Transform-stable carry-forward. A backend-only content rewrite performed after early
// projection can change the identity the frozen after-message anchor names even though the
// complete-message trajectory is unchanged, because MessageIdentityOf hashes normalized
// semantic content. When that happens Reassert proves a one-to-one structural lineage from the
// supplied frozen FilteredBaseline to the cleaned call and re-injects the overlay at the same
// logical boundary. A stored occurrence may still resolve at a different duplicate
// after the rewrite; the proven boundary takes precedence only in that conflict or
// when exact resolution fails. Matching boundaries keep stored evidence.
// The proof never reads the conversation-view store, and identity equality is
// deliberately not part of it: a rewrite of the anchored payload always drifts that identity, so
// an identity-equality proof would refuse every legitimate rewrite, while identity is also weaker
// than the structural proof because the normalized atom drops several stable identities. Any
// insertion, deletion, reorder, role/kind change, stable-ID or reference change, or
// authority/partition change refuses the proof outright; in those cases the stored anchor still
// drives exact resolution and the configured anchor-missing policy remains authoritative.
// MessageIdentityOf, the stored MessageAnchor{Identity, Occurrence}, persistence, overlay
// lifecycle, and the anchor-missing/fallback policy are all unchanged by this path.
func Reassert(call aipapi.Call, snap Snapshot, provenance []OverlayProvenance, filteredBaseline aipapi.Call) (aipapi.Call, *ProjectionEvidence, error) {
	// Derive provenance if empty but snap has steering (fallback).
	if len(provenance) == 0 && len(snap.Steering) > 0 {
		// Try to derive from filteredBaseline if available, else from call filtered.
		var base aipapi.Call
		if filteredBaseline.Messages != nil || filteredBaseline.Instructions != nil || filteredBaseline.Items != nil {
			base = aipapi.CloneCall(filteredBaseline)
		} else {
			fb, err := FilterNeverBackend(call, snap)
			if err != nil {
				return aipapi.Call{}, nil, err
			}
			base = fb
		}
		_, ev2, err := Project(base, snap)
		if err != nil {
			return aipapi.Call{}, nil, err
		}
		provenance = ev2.Provenance
	}
	if len(snap.NeverBackend) == 0 && len(provenance) == 0 {
		if err := call.Validate(); err != nil {
			return aipapi.Call{}, nil, fmt.Errorf("%w: %v", ErrProjectionFailed, err)
		}
		return call, &ProjectionEvidence{}, nil
	}
	cleaned := aipapi.CloneCall(call)
	// If filteredBaseline not provided, derive it for collision disambiguation.
	var filtered aipapi.Call
	hasFiltered := false
	// hasFrozenBaseline records that the caller supplied the request-local frozen
	// filtered baseline it froze at early projection. Only that pre-rewrite evidence
	// can support transform-stable carry-forward; a baseline derived on the fly from
	// the already-rewritten call carries no lineage information at all.
	hasFrozenBaseline := len(filteredBaseline.Instructions) > 0 || len(filteredBaseline.Messages) > 0 || len(filteredBaseline.Items) > 0
	if hasFrozenBaseline {
		filtered = aipapi.CloneCall(filteredBaseline)
		hasFiltered = true
	} else {
		fb, err := FilterNeverBackend(call, snap)
		if err == nil {
			filtered = fb
			hasFiltered = true
		}
	}
	// Build maps for quick lookup.
	neverSet := make(map[MessageIdentity]struct{}, len(snap.NeverBackend))
	for _, t := range snap.NeverBackend {
		neverSet[t.Identity] = struct{}{}
	}
	// Fail-closed on same-slice, same-identity collision with extra duplicates
	// that would require ambiguous position heuristic. If a prov identity collides
	// with legitimate (filteredCount>0) and late contains more than the expected
	// total (filtered + prov), the position heuristic could select legitimate
	// instead of the owned copy. Fail closed rather than risk user data loss.
	if hasFiltered && len(provenance) > 0 {
		if cleaned.HasItemAuthority() {
			if err := failClosedOnAmbiguousSameSliceCollisionItems(cleaned.Items, provenance, filtered); err != nil {
				return aipapi.Call{}, nil, err
			}
		} else {
			if err := failClosedOnAmbiguousSameSliceCollisionLegacy(cleaned.Instructions, cleaned.Messages, provenance, filtered); err != nil {
				return aipapi.Call{}, nil, err
			}
		}
	}
	// Placement-aware removal of provenance-owned steering.
	if len(provenance) > 0 {
		if cleaned.HasItemAuthority() {
			cleaned.Items = reassertRemoveProvenanceItems(cleaned.Items, provenance, filtered, hasFiltered)
		} else {
			cleaned.Instructions, cleaned.Messages = reassertRemoveProvenanceLegacy(cleaned.Instructions, cleaned.Messages, provenance, filtered, hasFiltered)
		}
	}
	// Remove reintroduced never_backend (any remaining with that identity).
	if len(neverSet) > 0 {
		if cleaned.HasItemAuthority() {
			cleaned.Items = filterItemsByNeverSet(cleaned.Items, neverSet)
		} else {
			cleaned.Instructions = filterMessagesByNeverSet(cleaned.Instructions, neverSet)
			cleaned.Messages = filterMessagesByNeverSet(cleaned.Messages, neverSet)
		}
	}
	// After expected provenance removal, check for remaining duplicates vs legitimate user.
	// For each distinct prov identity, compare remaining count in cleaned vs expected user count in filtered.
	if hasFiltered && len(provenance) > 0 {
		if cleaned.HasItemAuthority() {
			if err := checkRemainingProvIdentitiesItems(cleaned.Items, provenance, filtered); err != nil {
				return aipapi.Call{}, nil, err
			}
			// Remove any remaining extra prov identities that are duplicates (beyond legitimate user count).
			cleaned.Items = removeExtraProvIdentitiesItems(cleaned.Items, provenance, filtered)
		} else {
			if err := checkRemainingProvIdentitiesLegacy(cleaned.Instructions, cleaned.Messages, provenance, filtered); err != nil {
				return aipapi.Call{}, nil, err
			}
			cleaned.Instructions, cleaned.Messages = removeExtraProvIdentitiesLegacy(cleaned.Instructions, cleaned.Messages, provenance, filtered)
		}
	}
	// Transform-stable carry-forward supplies a request-local anchor when the stored
	// anchor stopped resolving or resolves at a different duplicate after a rewrite.
	// Exact resolution remains authoritative without proof or on a matching boundary.
	// The carried anchor exists
	// only when the FROZEN filtered baseline and the cleaned trajectory prove a
	// one-to-one structural lineage, and it is never derived from a conversation-view
	// store read. never_backend filtering and projection-owned overlay removal above
	// are already applied and are not bypassed here.
	out, ev, err := projectWithLineage(cleaned, snap, carriedAnchorsForReassert(snap, provenance, filteredBaseline, hasFrozenBaseline, cleaned))
	if err != nil {
		return aipapi.Call{}, nil, err
	}
	if err := out.Validate(); err != nil {
		return aipapi.Call{}, nil, fmt.Errorf("%w: %v", ErrProjectionFailed, err)
	}
	return out, ev, nil
}

// carriedAnchorsForReassert derives the request-local carried anchors for one final
// reassertion, or nil when none may be carried.
//
// The frozen filtered baseline is the ONLY admissible basis. When the caller
// supplied none, no request-local pre-rewrite evidence exists, so nothing is carried
// and the existing exact-resolution / anchor-missing-policy behavior is unchanged.
func carriedAnchorsForReassert(snap Snapshot, provenance []OverlayProvenance, filteredBaseline aipapi.Call, hasFrozenBaseline bool, cleaned aipapi.Call) carriedAnchors {
	if !hasFrozenBaseline {
		return nil
	}
	return deriveCarriedAnchors(snap, provenance, filteredBaseline, cleaned)
}

func filterItemsByNeverSet(items []aipapi.Item, neverSet map[MessageIdentity]struct{}) []aipapi.Item {
	// First pass: collect concrete IDs of removed never_backend messages for
	// item_reference dependency cleanup (phase 2 of Project).
	removedIDs := make(map[string]struct{})
	for _, it := range items {
		if it.Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := ItemIdentityOf(it)
		if err != nil {
			continue
		}
		if _, ok := neverSet[id]; ok && it.ID != "" {
			removedIDs[it.ID] = struct{}{}
		}
	}
	out := make([]aipapi.Item, 0, len(items))
	for _, it := range items {
		if it.Kind == aipapi.ItemKindItemReference && it.Reference != nil {
			if _, removed := removedIDs[it.Reference.ID]; removed {
				continue
			}
			out = append(out, it)
			continue
		}
		if it.Kind != aipapi.ItemKindMessage {
			out = append(out, it)
			continue
		}
		id, err := ItemIdentityOf(it)
		if err != nil {
			out = append(out, it)
			continue
		}
		if _, ok := neverSet[id]; ok {
			continue
		}
		out = append(out, it)
	}
	return out
}

func filterMessagesByNeverSet(msgs []aipapi.Message, neverSet map[MessageIdentity]struct{}) []aipapi.Message {
	out := make([]aipapi.Message, 0, len(msgs))
	for _, m := range msgs {
		id, err := MessageIdentityOf(m)
		if err != nil {
			out = append(out, m)
			continue
		}
		if _, ok := neverSet[id]; ok {
			continue
		}
		out = append(out, m)
	}
	return out
}

func failClosedOnAmbiguousSameSliceCollisionItems(items []aipapi.Item, provenance []OverlayProvenance, filtered aipapi.Call) error {
	provSet := make(map[MessageIdentity]struct{})
	provCount := make(map[MessageIdentity]int)
	for _, p := range provenance {
		provSet[p.InjectedIdentity] = struct{}{}
		provCount[p.InjectedIdentity]++
	}
	filteredCount := make(map[MessageIdentity]int)
	for _, it := range filtered.Items {
		if it.Kind != aipapi.ItemKindMessage {
			continue
		}
		if id, err := ItemIdentityOf(it); err == nil {
			if _, ok := provSet[id]; ok {
				filteredCount[id]++
			}
		}
	}
	lateCount := make(map[MessageIdentity]int)
	for _, it := range items {
		if it.Kind != aipapi.ItemKindMessage {
			continue
		}
		if id, err := ItemIdentityOf(it); err == nil {
			if _, ok := provSet[id]; ok {
				lateCount[id]++
			}
		}
	}
	for id, fcnt := range filteredCount {
		if fcnt == 0 {
			continue
		}
		pc := provCount[id]
		if pc == 0 {
			continue
		}
		lcnt := lateCount[id]
		expTotal := fcnt + pc
		if lcnt > expTotal {
			return fmt.Errorf("%w: ambiguous same-slice collision for %s: filtered %d + prov %d = %d expected, got %d (extra duplicate with legitimate same-identity in same slice)", ErrProjectionFailed, id, fcnt, pc, expTotal, lcnt)
		}
	}
	return nil
}

func failClosedOnAmbiguousSameSliceCollisionLegacy(instr, msgs []aipapi.Message, provenance []OverlayProvenance, filtered aipapi.Call) error {
	provSet := make(map[MessageIdentity]struct{})
	provCount := make(map[MessageIdentity]int)
	for _, p := range provenance {
		provSet[p.InjectedIdentity] = struct{}{}
		provCount[p.InjectedIdentity]++
	}
	filteredCount := make(map[MessageIdentity]int)
	// Count without a transient combined slice: concatenating Instructions and
	// Messages would allocate O(len) messages on every reassertion.
	for _, m := range filtered.Instructions {
		if id, err := MessageIdentityOf(m); err == nil {
			if _, ok := provSet[id]; ok {
				filteredCount[id]++
			}
		}
	}
	for _, m := range filtered.Messages {
		if id, err := MessageIdentityOf(m); err == nil {
			if _, ok := provSet[id]; ok {
				filteredCount[id]++
			}
		}
	}
	lateCount := make(map[MessageIdentity]int)
	for _, m := range instr {
		if id, err := MessageIdentityOf(m); err == nil {
			if _, ok := provSet[id]; ok {
				lateCount[id]++
			}
		}
	}
	for _, m := range msgs {
		if id, err := MessageIdentityOf(m); err == nil {
			if _, ok := provSet[id]; ok {
				lateCount[id]++
			}
		}
	}
	for id, fcnt := range filteredCount {
		if fcnt == 0 {
			continue
		}
		pc := provCount[id]
		if pc == 0 {
			continue
		}
		lcnt := lateCount[id]
		expTotal := fcnt + pc
		if lcnt > expTotal {
			return fmt.Errorf("%w: ambiguous same-slice collision for %s: filtered %d + prov %d = %d expected, got %d", ErrProjectionFailed, id, fcnt, pc, expTotal, lcnt)
		}
	}
	return nil
}

// reassertRemoveProvenanceItems removes provenance-owned steering using placement-aware provenance
// and filtered baseline to preserve legitimate collisions. It only removes extra copies beyond
// legitimate user count.
func reassertRemoveProvenanceItems(items []aipapi.Item, provenance []OverlayProvenance, filtered aipapi.Call, hasFiltered bool) []aipapi.Item {
	if len(provenance) == 0 {
		return items
	}
	// Build per-identity filtered and late counts
	provSet := make(map[MessageIdentity]struct{})
	for _, p := range provenance {
		provSet[p.InjectedIdentity] = struct{}{}
	}
	filteredCount := make(map[MessageIdentity]int)
	if hasFiltered {
		for _, it := range filtered.Items {
			if it.Kind != aipapi.ItemKindMessage {
				continue
			}
			if id, err := ItemIdentityOf(it); err == nil {
				if _, ok := provSet[id]; ok {
					filteredCount[id]++
				}
			}
		}
	}
	// One hashing pass records both the late counts and the trajectory
	// positions per provenance identity, so the removal loop below resolves
	// occurrences by lookup instead of re-hashing every item once per
	// identity.
	lateCount := make(map[MessageIdentity]int)
	lateIndexes := make(map[MessageIdentity][]int)
	for i, it := range items {
		if it.Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := ItemIdentityOf(it)
		if err != nil {
			continue
		}
		if _, ok := provSet[id]; !ok {
			continue
		}
		lateCount[id]++
		lateIndexes[id] = append(lateIndexes[id], i)
	}
	// Determine per-identity extra to remove
	extraPerID := make(map[MessageIdentity]int)
	for id, cnt := range lateCount {
		exp := filteredCount[id]
		if cnt > exp {
			extraPerID[id] = cnt - exp
		}
	}
	if len(extraPerID) == 0 {
		return items
	}
	// For each identity with extra, remove `extra` many occurrences closest to expected provenance positions.
	toRemove := make(map[int]struct{})
	// Group provenance by identity
	provByID := make(map[MessageIdentity][]OverlayProvenance)
	for _, p := range provenance {
		provByID[p.InjectedIdentity] = append(provByID[p.InjectedIdentity], p)
	}
	for id, extra := range extraPerID {
		lateIndices := lateIndexes[id]
		// Collect expected indices for this identity from provenance
		var expectedIndices []int
		for _, p := range provByID[id] {
			expIdx := p.InjectedTrajectoryIndex
			if p.InjectedAuthority == "item" && p.InjectedItemIndex >= 0 {
				expIdx = p.InjectedItemIndex
			}
			if expIdx >= 0 {
				expectedIndices = append(expectedIndices, expIdx)
			}
		}
		// For each late index, compute minimal distance to any expected index
		type cand struct {
			idx  int
			dist int
		}
		var cands []cand
		for _, lIdx := range lateIndices {
			bestDist := 1 << 30
			for _, eIdx := range expectedIndices {
				dist := lIdx - eIdx
				if dist < 0 {
					dist = -dist
				}
				if dist < bestDist {
					bestDist = dist
				}
			}
			if len(expectedIndices) == 0 {
				bestDist = 0
			}
			cands = append(cands, cand{idx: lIdx, dist: bestDist})
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })
		for i := 0; i < extra && i < len(cands); i++ {
			toRemove[cands[i].idx] = struct{}{}
		}
	}
	if len(toRemove) == 0 {
		return items
	}
	out := make([]aipapi.Item, 0, len(items)-len(toRemove))
	for i, it := range items {
		if _, ok := toRemove[i]; ok {
			continue
		}
		out = append(out, it)
	}
	return out
}

func reassertRemoveProvenanceLegacy(instr, msgs []aipapi.Message, provenance []OverlayProvenance, filtered aipapi.Call, hasFiltered bool) ([]aipapi.Message, []aipapi.Message) {
	if len(provenance) == 0 {
		return instr, msgs
	}
	// Build per-identity filtered and late counts
	provSet := make(map[MessageIdentity]struct{})
	for _, p := range provenance {
		provSet[p.InjectedIdentity] = struct{}{}
	}
	filteredCount := make(map[MessageIdentity]int)
	if hasFiltered {
		// Count without a transient combined slice.
		for _, m := range filtered.Instructions {
			if id, err := MessageIdentityOf(m); err == nil {
				if _, ok := provSet[id]; ok {
					filteredCount[id]++
				}
			}
		}
		for _, m := range filtered.Messages {
			if id, err := MessageIdentityOf(m); err == nil {
				if _, ok := provSet[id]; ok {
					filteredCount[id]++
				}
			}
		}
	}
	// One hashing pass records both the late counts and the trajectory
	// positions per provenance identity, so the removal loop below resolves
	// occurrences by lookup instead of re-hashing the whole call once per
	// identity.
	lateCount := make(map[MessageIdentity]int)
	lateInstrIndexes := make(map[MessageIdentity][]int)
	lateMsgIndexes := make(map[MessageIdentity][]int)
	for i, m := range instr {
		id, err := MessageIdentityOf(m)
		if err != nil {
			continue
		}
		if _, ok := provSet[id]; !ok {
			continue
		}
		lateCount[id]++
		lateInstrIndexes[id] = append(lateInstrIndexes[id], i)
	}
	for i, m := range msgs {
		id, err := MessageIdentityOf(m)
		if err != nil {
			continue
		}
		if _, ok := provSet[id]; !ok {
			continue
		}
		lateCount[id]++
		lateMsgIndexes[id] = append(lateMsgIndexes[id], i)
	}
	extraPerID := make(map[MessageIdentity]int)
	for id, cnt := range lateCount {
		exp := filteredCount[id]
		if cnt > exp {
			extraPerID[id] = cnt - exp
		}
	}
	if len(extraPerID) == 0 {
		return instr, msgs
	}
	// For each identity with extra, remove `extra` many occurrences closest to expected provenance positions.
	toRemoveInstr := make(map[int]struct{})
	toRemoveMsgs := make(map[int]struct{})
	// Group provenance by identity
	provByID := make(map[MessageIdentity][]OverlayProvenance)
	for _, p := range provenance {
		provByID[p.InjectedIdentity] = append(provByID[p.InjectedIdentity], p)
	}
	for id, extra := range extraPerID {
		lateInstrIndices := lateInstrIndexes[id]
		lateMsgsIndices := lateMsgIndexes[id]
		// Collect expected indices for this identity from provenance
		var expectedInstrIndices []int
		var expectedMsgsIndices []int
		for _, p := range provByID[id] {
			if p.InjectedAuthority == "legacy" && p.InjectedLegacyIsInstruction != nil && p.InjectedLegacyIndex != nil {
				if *p.InjectedLegacyIsInstruction {
					expectedInstrIndices = append(expectedInstrIndices, *p.InjectedLegacyIndex)
				} else {
					expectedMsgsIndices = append(expectedMsgsIndices, *p.InjectedLegacyIndex)
				}
			} else {
				// Fallback to trajectory index
				if p.InjectedTrajectoryIndex < len(instr) {
					expectedInstrIndices = append(expectedInstrIndices, p.InjectedTrajectoryIndex)
				} else {
					expectedMsgsIndices = append(expectedMsgsIndices, p.InjectedTrajectoryIndex-len(instr))
				}
			}
		}
		// For this identity, we need to choose which late occurrences to remove.
		// Prefer those closest to expected positions.
		// Build candidate list for instr
		type cand struct {
			idx  int
			dist int
			kind string // "instr" or "msgs"
		}
		var cands []cand
		for _, lIdx := range lateInstrIndices {
			bestDist := 1 << 30
			for _, eIdx := range expectedInstrIndices {
				dist := lIdx - eIdx
				if dist < 0 {
					dist = -dist
				}
				if dist < bestDist {
					bestDist = dist
				}
			}
			if len(expectedInstrIndices) == 0 {
				// No expected in instr, this identity's expected is in msgs, so late instr occurrence is not expected
				bestDist = 1000
			}
			cands = append(cands, cand{idx: lIdx, dist: bestDist, kind: "instr"})
		}
		for _, lIdx := range lateMsgsIndices {
			bestDist := 1 << 30
			for _, eIdx := range expectedMsgsIndices {
				dist := lIdx - eIdx
				if dist < 0 {
					dist = -dist
				}
				if dist < bestDist {
					bestDist = dist
				}
			}
			if len(expectedMsgsIndices) == 0 {
				bestDist = 1000
			}
			cands = append(cands, cand{idx: lIdx, dist: bestDist, kind: "msgs"})
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })
		for i := 0; i < extra && i < len(cands); i++ {
			if cands[i].kind == "instr" {
				toRemoveInstr[cands[i].idx] = struct{}{}
			} else {
				toRemoveMsgs[cands[i].idx] = struct{}{}
			}
		}
	}
	if len(toRemoveInstr) == 0 && len(toRemoveMsgs) == 0 {
		return instr, msgs
	}
	newInstr := make([]aipapi.Message, 0, len(instr)-len(toRemoveInstr))
	for i, m := range instr {
		if _, ok := toRemoveInstr[i]; ok {
			continue
		}
		newInstr = append(newInstr, m)
	}
	newMsgs := make([]aipapi.Message, 0, len(msgs)-len(toRemoveMsgs))
	for j, m := range msgs {
		if _, ok := toRemoveMsgs[j]; ok {
			continue
		}
		newMsgs = append(newMsgs, m)
	}
	return newInstr, newMsgs
}

func checkRemainingProvIdentitiesItems(items []aipapi.Item, provenance []OverlayProvenance, filtered aipapi.Call) error {
	provCount := make(map[MessageIdentity]int)
	for _, p := range provenance {
		provCount[p.InjectedIdentity]++
	}
	filteredCount := make(map[MessageIdentity]int)
	hasFilteredItems := len(filtered.Items) > 0
	if hasFilteredItems {
		for _, it := range filtered.Items {
			if it.Kind != aipapi.ItemKindMessage {
				continue
			}
			id, err := ItemIdentityOf(it)
			if err != nil {
				continue
			}
			if _, ok := provCount[id]; ok {
				filteredCount[id]++
			}
		}
	}
	remainingCount := make(map[MessageIdentity]int)
	for _, it := range items {
		if it.Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := ItemIdentityOf(it)
		if err != nil {
			continue
		}
		if _, ok := provCount[id]; ok {
			remainingCount[id]++
		}
	}
	for id, cnt := range remainingCount {
		exp := filteredCount[id]
		if cnt > exp {
			// Extra prov identity remains beyond legitimate user count.
			// If filtered had 0 and we have 1 remaining, that's indistinguishable but should be 0.
			// Fail closed if ambiguity: multiple identical copies at same position?
			// For now, if extra >0 and filtered had 0, we will remove extra in next step, not fail.
			// Only fail if remaining < expected (we deleted legitimate).
			continue
		}
		if cnt < exp {
			return fmt.Errorf("%w: prov identity %s expected %d legitimate occurrences after cleanup, got %d (legitimate deleted)", ErrProjectionFailed, id, exp, cnt)
		}
	}
	return nil
}

func removeExtraProvIdentitiesItems(items []aipapi.Item, provenance []OverlayProvenance, filtered aipapi.Call) []aipapi.Item {
	provSet := make(map[MessageIdentity]struct{})
	for _, p := range provenance {
		provSet[p.InjectedIdentity] = struct{}{}
	}
	filteredCount := make(map[MessageIdentity]int)
	for _, it := range filtered.Items {
		if it.Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := ItemIdentityOf(it)
		if err != nil {
			continue
		}
		if _, ok := provSet[id]; ok {
			filteredCount[id]++
		}
	}
	remainingCount := make(map[MessageIdentity]int)
	for _, it := range items {
		if it.Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := ItemIdentityOf(it)
		if err != nil {
			continue
		}
		if _, ok := provSet[id]; ok {
			remainingCount[id]++
		}
	}
	// Determine how many extra per identity to remove
	extraToRemove := make(map[MessageIdentity]int)
	for id, cnt := range remainingCount {
		exp := filteredCount[id]
		if cnt > exp {
			extraToRemove[id] = cnt - exp
		}
	}
	if len(extraToRemove) == 0 {
		return items
	}
	// Remove last extra per identity (keep earliest legitimate, remove tail duplicates).
	keep := filteredCount
	out2 := make([]aipapi.Item, 0, len(items))
	seen2 := make(map[MessageIdentity]int)
	for _, it := range items {
		if it.Kind != aipapi.ItemKindMessage {
			out2 = append(out2, it)
			continue
		}
		id, err := ItemIdentityOf(it)
		if err != nil {
			out2 = append(out2, it)
			continue
		}
		if _, ok := provSet[id]; ok {
			if seen2[id] < keep[id] {
				out2 = append(out2, it)
			}
			seen2[id]++
		} else {
			out2 = append(out2, it)
		}
	}
	return out2
}

func checkRemainingProvIdentitiesLegacy(instr, msgs []aipapi.Message, provenance []OverlayProvenance, filtered aipapi.Call) error {
	provSet := make(map[MessageIdentity]struct{})
	for _, p := range provenance {
		provSet[p.InjectedIdentity] = struct{}{}
	}
	filteredCount := make(map[MessageIdentity]int)
	for _, m := range filtered.Instructions {
		if id, err := MessageIdentityOf(m); err == nil {
			if _, ok := provSet[id]; ok {
				filteredCount[id]++
			}
		}
	}
	for _, m := range filtered.Messages {
		if id, err := MessageIdentityOf(m); err == nil {
			if _, ok := provSet[id]; ok {
				filteredCount[id]++
			}
		}
	}
	remainingCount := make(map[MessageIdentity]int)
	for _, m := range instr {
		if id, err := MessageIdentityOf(m); err == nil {
			if _, ok := provSet[id]; ok {
				remainingCount[id]++
			}
		}
	}
	for _, m := range msgs {
		if id, err := MessageIdentityOf(m); err == nil {
			if _, ok := provSet[id]; ok {
				remainingCount[id]++
			}
		}
	}
	for id, cnt := range remainingCount {
		exp := filteredCount[id]
		if cnt < exp {
			return fmt.Errorf("%w: prov identity %s expected %d legitimate occurrences after cleanup, got %d", ErrProjectionFailed, id, exp, cnt)
		}
	}
	return nil
}

func removeExtraProvIdentitiesLegacy(instr, msgs []aipapi.Message, provenance []OverlayProvenance, filtered aipapi.Call) ([]aipapi.Message, []aipapi.Message) {
	provSet := make(map[MessageIdentity]struct{})
	for _, p := range provenance {
		provSet[p.InjectedIdentity] = struct{}{}
	}
	filteredCount := make(map[MessageIdentity]int)
	for _, m := range filtered.Instructions {
		if id, err := MessageIdentityOf(m); err == nil {
			if _, ok := provSet[id]; ok {
				filteredCount[id]++
			}
		}
	}
	for _, m := range filtered.Messages {
		if id, err := MessageIdentityOf(m); err == nil {
			if _, ok := provSet[id]; ok {
				filteredCount[id]++
			}
		}
	}
	keep := filteredCount
	keptInstr := make([]aipapi.Message, 0, len(instr))
	keptMsgs := make([]aipapi.Message, 0, len(msgs))
	seen2 := make(map[MessageIdentity]int)
	// Need to know total keep per identity
	for _, m := range instr {
		id, err := MessageIdentityOf(m)
		if err != nil {
			keptInstr = append(keptInstr, m)
			continue
		}
		if _, ok := provSet[id]; ok {
			if seen2[id] < keep[id] {
				keptInstr = append(keptInstr, m)
			}
			seen2[id]++
		} else {
			keptInstr = append(keptInstr, m)
		}
	}
	for _, m := range msgs {
		id, err := MessageIdentityOf(m)
		if err != nil {
			keptMsgs = append(keptMsgs, m)
			continue
		}
		if _, ok := provSet[id]; ok {
			if seen2[id] < keep[id] {
				keptMsgs = append(keptMsgs, m)
			}
			seen2[id]++
		} else {
			keptMsgs = append(keptMsgs, m)
		}
	}
	return keptInstr, keptMsgs
}

// VerifyAdaptationPreservesProjection checks that adaptedCall still satisfies full projection:
// - never_backend absent
// - steering exact count per identity, SlotOrdinal order, and same resolved placement (stable prefix / after anchor)
// - moved-tail/reordered steering rejects.
func VerifyAdaptationPreservesProjection(reasserted, adapted aipapi.Call, snap Snapshot, provenance []OverlayProvenance) error {
	_, err := VerifyAdaptationPreservesProjectionWithPositions(reasserted, adapted, snap, provenance)
	return err
}

// VerifyAdaptationPreservesProjectionWithPositions performs the same checks as
// VerifyAdaptationPreservesProjection and returns the adapted trajectory
// positions of the injected entries. Callers that need positional evidence use
// this single pass instead of verifying and then rescanning the call.
func VerifyAdaptationPreservesProjectionWithPositions(reasserted, adapted aipapi.Call, snap Snapshot, provenance []OverlayProvenance) ([]InjectedPosition, error) {
	if len(provenance) == 0 && len(snap.NeverBackend) == 0 && len(snap.Steering) == 0 {
		return nil, nil
	}
	// Check never_backend absent
	neverSet := make(map[MessageIdentity]struct{}, len(snap.NeverBackend))
	for _, t := range snap.NeverBackend {
		neverSet[t.Identity] = struct{}{}
	}
	if adapted.HasItemAuthority() {
		for _, it := range adapted.Items {
			if it.Kind != aipapi.ItemKindMessage {
				continue
			}
			id, err := ItemIdentityOf(it)
			if err != nil {
				continue
			}
			if _, ok := neverSet[id]; ok {
				return nil, fmt.Errorf("%w: adapted still contains never_backend %s", ErrProjectionFailed, id)
			}
		}
	} else {
		for _, m := range adapted.Instructions {
			if id, err := MessageIdentityOf(m); err == nil {
				if _, ok := neverSet[id]; ok {
					return nil, fmt.Errorf("%w: adapted instructions still contains never_backend %s", ErrProjectionFailed, id)
				}
			}
		}
		for _, m := range adapted.Messages {
			if id, err := MessageIdentityOf(m); err == nil {
				if _, ok := neverSet[id]; ok {
					return nil, fmt.Errorf("%w: adapted messages still contains never_backend %s", ErrProjectionFailed, id)
				}
			}
		}
	}
	return AdaptedInjectedPositions(reasserted, adapted, provenance)
}
