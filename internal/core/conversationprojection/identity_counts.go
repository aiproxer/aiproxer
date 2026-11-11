package conversationprojection

import "github.com/aiproxer/aiproxer/pkg/aipapi"

// countIdentities returns the occurrences of want's identities in one trajectory.
// Elements whose identity cannot be derived are skipped, matching every counting
// phase of reassertion.
func countIdentities[T any](t identityTrajectory[T], want map[MessageIdentity]int) map[MessageIdentity]int {
	counts := make(map[MessageIdentity]int, len(want))
	for i := range t.elems {
		id, err := t.identity(i)
		if err != nil {
			continue
		}
		if _, ok := want[id]; ok {
			counts[id]++
		}
	}
	return counts
}

// countItemIdentities counts message items only: non-message items carry no
// message identity, and every item phase of reassertion skips them.
func countItemIdentities(t itemTrajectory, want map[MessageIdentity]int) map[MessageIdentity]int {
	counts := make(map[MessageIdentity]int, len(want))
	for i := range t.elems {
		if t.elems[i].Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := t.identity(i)
		if err != nil {
			continue
		}
		if _, ok := want[id]; ok {
			counts[id]++
		}
	}
	return counts
}

// countLegacyIdentities counts want's identities over the whole legacy trajectory,
// instructions first and then messages.
func countLegacyIdentities(t legacyTrajectory, want map[MessageIdentity]int) map[MessageIdentity]int {
	counts := countIdentities(t.instr, want)
	for id, n := range countIdentities(t.msgs, want) {
		counts[id] += n
	}
	return counts
}

// traversable reports whether the input describes at least one trajectory region,
// which is what a caller-supplied frozen baseline must have to prove lineage.
func (in projectionInput) traversable() bool {
	if in.itemAuthority {
		return in.items.len() > 0
	}
	return in.legacy.instr.len() > 0 || in.legacy.msgs.len() > 0
}

// provenanceCounts returns how many provenance entries carry each injected
// identity.
func provenanceCounts(provenance []OverlayProvenance) map[MessageIdentity]int {
	counts := make(map[MessageIdentity]int, len(provenance))
	for _, p := range provenance {
		counts[p.InjectedIdentity]++
	}
	return counts
}
