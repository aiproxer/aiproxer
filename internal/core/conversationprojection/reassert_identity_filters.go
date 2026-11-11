package conversationprojection

import "github.com/aiproxer/aiproxer/pkg/aipapi"

// filterByNeverSet removes reintroduced never_backend content and, for item
// authority, the item_references that target the removed concrete IDs.
func filterByNeverSet(in projectionInput, neverSet map[MessageIdentity]struct{}) projectionInput {
	if in.itemAuthority {
		return filterItemsByNeverSet(in, neverSet)
	}
	in.legacy.instr = filterMessagesByNeverSet(in.legacy.instr, neverSet)
	in.legacy.msgs = filterMessagesByNeverSet(in.legacy.msgs, neverSet)
	return in
}

// filterItemsByNeverSet drops excluded message items and every item_reference
// whose target ID was removed, keeping the derived identities in step.
func filterItemsByNeverSet(in projectionInput, neverSet map[MessageIdentity]struct{}) projectionInput {
	// First pass: collect concrete IDs of removed never_backend messages for
	// item_reference dependency cleanup (phase 2 of Project).
	removedIDs := make(map[string]struct{})
	for i := range in.items.elems {
		it := &in.items.elems[i]
		if it.Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := in.items.identity(i)
		if err != nil {
			continue
		}
		if _, ok := neverSet[id]; ok && it.ID != "" {
			removedIDs[it.ID] = struct{}{}
		}
	}
	kept := make([]bool, in.items.len())
	for i := range in.items.elems {
		it := &in.items.elems[i]
		kept[i] = true
		if it.Kind == aipapi.ItemKindItemReference && it.Reference != nil {
			if _, removed := removedIDs[it.Reference.ID]; removed {
				kept[i] = false
			}
		}
	}
	in.items = in.items.retained(kept)
	kept = make([]bool, in.items.len())
	for i := range in.items.elems {
		it := &in.items.elems[i]
		kept[i] = true
		if it.Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := in.items.identity(i)
		if err != nil {
			continue
		}
		if _, ok := neverSet[id]; ok {
			kept[i] = false
		}
	}
	in.items = in.items.retained(kept)
	return in
}

// filterMessagesByNeverSet drops excluded messages, keeping unidentifiable
// messages exactly as the identity-scanning version did.
func filterMessagesByNeverSet(in identityTrajectory[aipapi.Message], neverSet map[MessageIdentity]struct{}) identityTrajectory[aipapi.Message] {
	kept := make([]bool, in.len())
	for i := range in.elems {
		kept[i] = true
		id, err := in.identity(i)
		if err != nil {
			continue
		}
		if _, ok := neverSet[id]; ok {
			kept[i] = false
		}
	}
	return in.retained(kept)
}
