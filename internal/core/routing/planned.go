package routing

import "net/url"

// PlannedLeaf is one concrete backend:model leaf of a selector plan. Key is
// the leaf's stable diagnostic identity (Primary.String()); Backend and Model
// are the routing identities used for price and tariff resolution. Backend is
// empty for model-only selectors. Params is the leaf's route generation query
// (for example max_output_tokens=8000) as a read-only view of the selector
// AST: consumers must never mutate it, and every occurrence carries its own
// value so multiplicity and per-leaf authority survive the shared traversal.
type PlannedLeaf struct {
	Key     string
	Backend string
	Model   string
	Params  url.Values
}

// PlannedLeaves enumerates every planned leaf occurrence of a selector in
// execution-plan traversal order: each failover alternative left to right,
// then each weighted branch in order (an embedded parallel group contributes
// its branches in order), and each plain parallel branch in order. Duplicate
// leaves are retained exactly once per occurrence — multiplicity is
// load-bearing for quote maxima (two identical parallel branches cost twice) —
// and the list is never treated as a set or deduplicated. A nil selector
// yields no leaves.
//
// It is a pure read-only projection of the selector AST: it reads no runtime
// state and never mutates the selector, so quote, admission, and reference
// composition can share one traversal and cannot diverge.
func PlannedLeaves(sel *Selector) []PlannedLeaf {
	if sel == nil {
		return nil
	}
	var out []PlannedLeaf
	add := func(p Primary) {
		out = append(out, PlannedLeaf{Key: p.String(), Backend: p.Backend, Model: p.Model, Params: p.Params})
	}
	for _, alt := range sel.Alternatives {
		switch {
		case alt.Primary != nil:
			add(*alt.Primary)
		case alt.Weighted != nil:
			for _, branch := range alt.Weighted.Branches {
				if branch.Parallel != nil {
					for _, parallelBranch := range branch.Parallel.Branches {
						add(parallelBranch.Target)
					}
					continue
				}
				add(branch.Target)
			}
		case alt.Parallel != nil:
			for _, branch := range alt.Parallel.Branches {
				add(branch.Target)
			}
		}
	}
	return out
}
