package conversationprojection

import (
	"fmt"
	"sort"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ---------------------------------------------------------------------------
// Reassertion phases over derived identity tables.
//
// Reassertion compares the same two trajectories in five phases: the ambiguity
// collision check, provenance removal, never_backend filtering, the
// remaining-identity check, and extra-identity removal. Each phase used to
// re-derive every identity it looked at, so one Reassert over a 100-message
// history with 256 never_backend tags and two overlays derived identities
// 1,206 times. Reassert now derives each element's identity once and hands the
// table to the phases below, which decide exactly what the identity-scanning
// versions decided while hashing each element once.
//
// Every phase keeps its per-authority behaviour: item authority looks at message
// items only and skips non-message items, legacy authority looks at instructions
// and then messages, and an element whose identity cannot be derived is skipped
// by the counting phases exactly as before. Candidate ranking keeps the original
// distance comparator, so the same occurrences are selected for removal.
// ---------------------------------------------------------------------------

// failClosedOnAmbiguousSameSliceCollision rejects a trajectory in which a
// provenance-owned identity collides with legitimate same-identity content and the
// late trajectory holds more copies than the expected filtered-plus-provenance
// total, because the position heuristic could then delete legitimate user content.
func failClosedOnAmbiguousSameSliceCollision(cleanedIn, frozenIn projectionInput, provenance []OverlayProvenance) error {
	provCount := provenanceCounts(provenance)
	filteredCount := countFilteredIdentities(cleanedIn, frozenIn, provCount)
	lateCount := countCleanedIdentities(cleanedIn, provCount)
	for id, fcnt := range filteredCount {
		pc := provCount[id]
		if fcnt == 0 || pc == 0 {
			continue
		}
		if lcnt := lateCount[id]; lcnt > fcnt+pc {
			return ambiguousCollisionError(cleanedIn, id, fcnt, pc, lcnt)
		}
	}
	return nil
}

// ambiguousCollisionError preserves the denial text of the authority the phase
// inspected, so existing operator-facing diagnostics stay byte-identical.
func ambiguousCollisionError(in projectionInput, id MessageIdentity, filtered, prov, late int) error {
	if in.itemAuthority {
		return fmt.Errorf("%w: ambiguous same-slice collision for %s: filtered %d + prov %d = %d expected, got %d (extra duplicate with legitimate same-identity in same slice)", ErrProjectionFailed, id, filtered, prov, filtered+prov, late)
	}
	return fmt.Errorf("%w: ambiguous same-slice collision for %s: filtered %d + prov %d = %d expected, got %d", ErrProjectionFailed, id, filtered, prov, filtered+prov, late)
}

// reassertRemoveProvenance removes provenance-owned steering using the
// placement-aware evidence, keeping legitimate same-identity content: only the
// copies beyond the legitimate filtered count are removed, choosing the
// occurrences closest to the recorded injected positions.
func reassertRemoveProvenance(cleanedIn, frozenIn projectionInput, provenance []OverlayProvenance, hasFiltered bool) projectionInput {
	if len(provenance) == 0 {
		return cleanedIn
	}
	provCount := provenanceCounts(provenance)
	filteredCount := map[MessageIdentity]int{}
	if hasFiltered {
		filteredCount = countFilteredIdentities(cleanedIn, frozenIn, provCount)
	}
	if cleanedIn.itemAuthority {
		cleanedIn.items = removeProvenanceItems(cleanedIn.items, provenance, provCount, filteredCount)
		return cleanedIn
	}
	cleanedIn.legacy.instr, cleanedIn.legacy.msgs = removeProvenanceLegacy(cleanedIn.legacy, provenance, provCount, filteredCount)
	return cleanedIn
}

// removalCandidate ranks one late occurrence against the expected positions of
// its identity.
type removalCandidate struct {
	region int
	idx    int
	dist   int
}

const (
	regionInstructions = iota
	regionMessages
)

// unmatchedRegionDistance orders an occurrence that has no expected position in
// its own region behind every occurrence that has one, exactly as the scanning
// legacy phase did with its 1000 sentinel.
const unmatchedRegionDistance = 1000

// rankRemovalCandidates orders candidates by distance with the same comparator and
// the same sort the previous phase used, so the same occurrences are selected even
// when several copies sit at equal distance from an expected position.
func rankRemovalCandidates(cands []removalCandidate) {
	sort.Slice(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })
}

// nearestDistance returns the distance from idx to the closest expected position,
// or unmatched when the region holds none for this identity.
func nearestDistance(idx int, expected []int, unmatched int) int {
	if len(expected) == 0 {
		return unmatched
	}
	best := maxInt
	for _, e := range expected {
		dist := idx - e
		if dist < 0 {
			dist = -dist
		}
		if dist < best {
			best = dist
		}
	}
	return best
}

// maxInt is the distance sentinel used before any expected position was seen.
const maxInt = 1 << 30

// removeProvenanceItems removes the extra provenance-owned item messages, keeping
// the occurrences closest to the recorded injected positions.
func removeProvenanceItems(items itemTrajectory, provenance []OverlayProvenance, provCount, filteredCount map[MessageIdentity]int) itemTrajectory {
	lateCount := map[MessageIdentity]int{}
	lateIndexes := map[MessageIdentity][]int{}
	for i := range items.elems {
		if items.elems[i].Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := items.identity(i)
		if err != nil {
			continue
		}
		if _, ok := provCount[id]; !ok {
			continue
		}
		lateCount[id]++
		lateIndexes[id] = append(lateIndexes[id], i)
	}
	extraPerID := extraOccurrences(lateCount, filteredCount)
	if len(extraPerID) == 0 {
		return items
	}
	provByID := provenanceByIdentity(provenance)
	toRemove := map[int]struct{}{}
	for id, extra := range extraPerID {
		expected := itemExpectedIndices(provByID[id])
		cands := make([]removalCandidate, 0, len(lateIndexes[id]))
		for _, idx := range lateIndexes[id] {
			cands = append(cands, removalCandidate{region: regionMessages, idx: idx, dist: nearestDistance(idx, expected, 0)})
		}
		rankRemovalCandidates(cands)
		for i := 0; i < extra && i < len(cands); i++ {
			toRemove[cands[i].idx] = struct{}{}
		}
	}
	if len(toRemove) == 0 {
		return items
	}
	kept := make([]bool, items.len())
	for i := range items.elems {
		if _, ok := toRemove[i]; !ok {
			kept[i] = true
		}
	}
	return items.retained(kept)
}

// removeProvenanceLegacy is removeProvenanceItems over the two legacy regions,
// whose expected positions are recorded per region.
func removeProvenanceLegacy(cleaned legacyTrajectory, provenance []OverlayProvenance, provCount, filteredCount map[MessageIdentity]int) (identityTrajectory[aipapi.Message], identityTrajectory[aipapi.Message]) {
	instr, msgs := cleaned.instr, cleaned.msgs
	lateCount := map[MessageIdentity]int{}
	lateInstrIndexes := map[MessageIdentity][]int{}
	lateMsgIndexes := map[MessageIdentity][]int{}
	for i := range instr.elems {
		id, err := instr.identity(i)
		if err != nil {
			continue
		}
		if _, ok := provCount[id]; !ok {
			continue
		}
		lateCount[id]++
		lateInstrIndexes[id] = append(lateInstrIndexes[id], i)
	}
	for i := range msgs.elems {
		id, err := msgs.identity(i)
		if err != nil {
			continue
		}
		if _, ok := provCount[id]; !ok {
			continue
		}
		lateCount[id]++
		lateMsgIndexes[id] = append(lateMsgIndexes[id], i)
	}
	extraPerID := extraOccurrences(lateCount, filteredCount)
	if len(extraPerID) == 0 {
		return instr, msgs
	}
	provByID := provenanceByIdentity(provenance)
	toRemoveInstr := map[int]struct{}{}
	toRemoveMsgs := map[int]struct{}{}
	for id, extra := range extraPerID {
		expectedInstr, expectedMsgs := legacyExpectedIndices(provByID[id], instr.len())
		cands := make([]removalCandidate, 0, len(lateInstrIndexes[id])+len(lateMsgIndexes[id]))
		for _, idx := range lateInstrIndexes[id] {
			cands = append(cands, removalCandidate{region: regionInstructions, idx: idx, dist: nearestDistance(idx, expectedInstr, unmatchedRegionDistance)})
		}
		for _, idx := range lateMsgIndexes[id] {
			cands = append(cands, removalCandidate{region: regionMessages, idx: idx, dist: nearestDistance(idx, expectedMsgs, unmatchedRegionDistance)})
		}
		rankRemovalCandidates(cands)
		for i := 0; i < extra && i < len(cands); i++ {
			if cands[i].region == regionInstructions {
				toRemoveInstr[cands[i].idx] = struct{}{}
			} else {
				toRemoveMsgs[cands[i].idx] = struct{}{}
			}
		}
	}
	if len(toRemoveInstr) == 0 && len(toRemoveMsgs) == 0 {
		return instr, msgs
	}
	keptInstr := make([]bool, instr.len())
	for i := range instr.elems {
		if _, ok := toRemoveInstr[i]; !ok {
			keptInstr[i] = true
		}
	}
	keptMsgs := make([]bool, msgs.len())
	for i := range msgs.elems {
		if _, ok := toRemoveMsgs[i]; !ok {
			keptMsgs[i] = true
		}
	}
	return instr.retained(keptInstr), msgs.retained(keptMsgs)
}

// extraOccurrences returns how many occurrences of each identity exceed the
// legitimate filtered count.
func extraOccurrences(lateCount, filteredCount map[MessageIdentity]int) map[MessageIdentity]int {
	var out map[MessageIdentity]int
	for id, cnt := range lateCount {
		exp := filteredCount[id]
		if cnt <= exp {
			continue
		}
		if out == nil {
			out = map[MessageIdentity]int{}
		}
		out[id] = cnt - exp
	}
	return out
}

// checkRemainingProvIdentities fails closed when a legitimate same-identity
// occurrence disappeared during cleanup.
func checkRemainingProvIdentities(cleanedIn, frozenIn projectionInput, provenance []OverlayProvenance) error {
	provCount := provenanceCounts(provenance)
	filteredCount := countFilteredIdentities(cleanedIn, frozenIn, provCount)
	remainingCount := countCleanedIdentities(cleanedIn, provCount)
	for id, cnt := range remainingCount {
		exp := filteredCount[id]
		if cnt > exp {
			// Extra provenance identity remains beyond the legitimate user count;
			// the removal phase below drops it. Only a missing legitimate occurrence
			// is a failure, because that would mean user content was deleted.
			continue
		}
		if cnt < exp {
			return remainingIdentityLossError(cleanedIn, id, exp, cnt)
		}
	}
	return nil
}

// remainingIdentityLossError preserves the denial text of the authority the phase
// inspected, so existing operator-facing diagnostics stay byte-identical.
func remainingIdentityLossError(in projectionInput, id MessageIdentity, expected, got int) error {
	if in.itemAuthority {
		return fmt.Errorf("%w: prov identity %s expected %d legitimate occurrences after cleanup, got %d (legitimate deleted)", ErrProjectionFailed, id, expected, got)
	}
	return fmt.Errorf("%w: prov identity %s expected %d legitimate occurrences after cleanup, got %d", ErrProjectionFailed, id, expected, got)
}

// removeExtraProvIdentities drops the trailing duplicate copies of provenance
// identities beyond the legitimate filtered count.
func removeExtraProvIdentities(cleanedIn, frozenIn projectionInput, provenance []OverlayProvenance) projectionInput {
	provCount := provenanceCounts(provenance)
	filteredCount := countFilteredIdentities(cleanedIn, frozenIn, provCount)
	remainingCount := countCleanedIdentities(cleanedIn, provCount)
	if len(extraOccurrences(remainingCount, filteredCount)) == 0 {
		return cleanedIn
	}
	if cleanedIn.itemAuthority {
		cleanedIn.items = keepFirstItems(cleanedIn.items, provCount, filteredCount)
		return cleanedIn
	}
	cleanedIn.legacy.instr, cleanedIn.legacy.msgs = keepFirstLegacy(cleanedIn.legacy, provCount, filteredCount)
	return cleanedIn
}

// keepFirstItems keeps the earliest occurrences of each provenance identity up to
// the legitimate filtered count and drops the duplicates that follow.
func keepFirstItems(items itemTrajectory, provCount, keep map[MessageIdentity]int) itemTrajectory {
	kept := make([]bool, items.len())
	seen := map[MessageIdentity]int{}
	for i := range items.elems {
		kept[i] = true
		if items.elems[i].Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := items.identity(i)
		if err != nil {
			continue
		}
		if _, ok := provCount[id]; !ok {
			continue
		}
		if seen[id] >= keep[id] {
			kept[i] = false
		}
		seen[id]++
	}
	return items.retained(kept)
}

// keepFirstLegacy is keepFirstItems over both legacy regions, with the occurrence
// count spanning instructions first and then messages.
func keepFirstLegacy(cleaned legacyTrajectory, provCount, keep map[MessageIdentity]int) (identityTrajectory[aipapi.Message], identityTrajectory[aipapi.Message]) {
	seen := map[MessageIdentity]int{}
	keptInstr := make([]bool, cleaned.instr.len())
	for i := range cleaned.instr.elems {
		keptInstr[i] = true
		id, err := cleaned.instr.identity(i)
		if err != nil {
			continue
		}
		if _, ok := provCount[id]; !ok {
			continue
		}
		if seen[id] >= keep[id] {
			keptInstr[i] = false
		}
		seen[id]++
	}
	keptMsgs := make([]bool, cleaned.msgs.len())
	for i := range cleaned.msgs.elems {
		keptMsgs[i] = true
		id, err := cleaned.msgs.identity(i)
		if err != nil {
			continue
		}
		if _, ok := provCount[id]; !ok {
			continue
		}
		if seen[id] >= keep[id] {
			keptMsgs[i] = false
		}
		seen[id]++
	}
	return cleaned.instr.retained(keptInstr), cleaned.msgs.retained(keptMsgs)
}

// countCleanedIdentities counts identities in the trajectory being cleaned:
// message items for item authority, instructions and then messages for legacy.
func countCleanedIdentities(cleanedIn projectionInput, want map[MessageIdentity]int) map[MessageIdentity]int {
	if cleanedIn.itemAuthority {
		return countItemIdentities(cleanedIn.items, want)
	}
	return countLegacyIdentities(cleanedIn.legacy, want)
}

// countFilteredIdentities counts identities in the frozen baseline region that
// matches the cleaned call authority. Reassertion has always read the baseline
// through the authority under construction - filtered.Items for item authority and
// filtered.Instructions/filtered.Messages otherwise - so a baseline carrying the
// other authority contributes no occurrences, exactly as before.
func countFilteredIdentities(cleanedIn, frozenIn projectionInput, want map[MessageIdentity]int) map[MessageIdentity]int {
	if cleanedIn.itemAuthority {
		return countItemIdentities(frozenItems(frozenIn), want)
	}
	return countLegacyIdentities(frozenLegacy(frozenIn), want)
}

// frozenItems returns the frozen baseline's item trajectory. A baseline of the
// other authority carries no item region, so counts over it stay empty - exactly
// what the item phases observed when they read filtered.Items directly.
func frozenItems(frozenIn projectionInput) itemTrajectory {
	if !frozenIn.itemAuthority {
		return itemTrajectory{}
	}
	return frozenIn.items
}

// frozenLegacy returns the frozen baseline's legacy regions, empty when the
// baseline is item authority.
func frozenLegacy(frozenIn projectionInput) legacyTrajectory {
	if frozenIn.itemAuthority {
		return legacyTrajectory{}
	}
	return frozenIn.legacy
}

// provenanceByIdentity groups provenance entries by their injected identity.
func provenanceByIdentity(provenance []OverlayProvenance) map[MessageIdentity][]OverlayProvenance {
	grouped := make(map[MessageIdentity][]OverlayProvenance, len(provenance))
	for _, p := range provenance {
		grouped[p.InjectedIdentity] = append(grouped[p.InjectedIdentity], p)
	}
	return grouped
}

// itemExpectedIndices returns the item-space positions provenance recorded for one
// identity.
func itemExpectedIndices(entries []OverlayProvenance) []int {
	var out []int
	for _, p := range entries {
		expIdx := p.InjectedTrajectoryIndex
		if p.InjectedAuthority == "item" && p.InjectedItemIndex >= 0 {
			expIdx = p.InjectedItemIndex
		}
		if expIdx >= 0 {
			out = append(out, expIdx)
		}
	}
	return out
}

// legacyExpectedIndices splits one identity's recorded positions into the
// instruction and message regions.
func legacyExpectedIndices(entries []OverlayProvenance, instrLen int) (instrPositions, msgPositions []int) {
	for _, p := range entries {
		if p.InjectedAuthority == "legacy" && p.InjectedLegacyIsInstruction != nil && p.InjectedLegacyIndex != nil {
			if *p.InjectedLegacyIsInstruction {
				instrPositions = append(instrPositions, *p.InjectedLegacyIndex)
			} else {
				msgPositions = append(msgPositions, *p.InjectedLegacyIndex)
			}
			continue
		}
		// Fall back to the trajectory index, exactly as the scanning phase did.
		if p.InjectedTrajectoryIndex < instrLen {
			instrPositions = append(instrPositions, p.InjectedTrajectoryIndex)
		} else {
			msgPositions = append(msgPositions, p.InjectedTrajectoryIndex-instrLen)
		}
	}
	return instrPositions, msgPositions
}
