package toolpolicy

import "slices"

// MaterializeSorted returns policies ordered by Order, ID, then registration index.
func MaterializeSorted(policies []Policy) []Policy {
	if len(policies) == 0 {
		return nil
	}
	// Single policy: no stable-sort index slice; common for one policy per request snapshot.
	if len(policies) == 1 {
		return []Policy{policies[0]}
	}
	h := slices.Clone(policies)
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		pa, pb := h[a], h[b]
		if pa == nil || pb == nil {
			switch {
			case pa == nil && pb == nil:
				return a - b
			case pa == nil:
				return 1
			default:
				return -1
			}
		}
		if pa.Order() != pb.Order() {
			return pa.Order() - pb.Order()
		}
		if pa.ID() < pb.ID() {
			return -1
		}
		if pa.ID() > pb.ID() {
			return 1
		}
		return a - b
	})
	out := make([]Policy, len(h))
	for i, old := range idx {
		out[i] = h[old]
	}
	return out
}
