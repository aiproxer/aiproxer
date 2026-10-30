package hooks

import "cmp"

// StableParticipantLess is the design §17 intra-stage ordering: ascending order
// (priority), then ascending stable id, then ascending registration index tie-break.
func StableParticipantLess(orderA, orderB int, idA, idB string, regIdxA, regIdxB int) int {
	if c := cmp.Compare(orderA, orderB); c != 0 {
		return c
	}
	if c := cmp.Compare(idA, idB); c != 0 {
		return c
	}
	return cmp.Compare(regIdxA, regIdxB)
}
