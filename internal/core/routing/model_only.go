package routing

import (
	"fmt"
	"slices"
	"strings"
)

// ApplyModelOnlyBackends sets Primary.Backend to defaultBackend wherever it is empty.
// defaultBackend must be non-empty; otherwise this is a no-op.
func ApplyModelOnlyBackends(sel *Selector, defaultBackend string) {
	if sel == nil {
		return
	}
	defaultBackend = strings.TrimSpace(defaultBackend)
	if defaultBackend == "" {
		return
	}
	for i := range sel.Alternatives {
		applyModelOnlyToAlt(&sel.Alternatives[i], defaultBackend)
	}
}

func applyModelOnlyToAlt(a *FailoverAlt, backend string) {
	switch {
	case a.Primary != nil && strings.TrimSpace(a.Primary.Backend) == "":
		a.Primary.Backend = backend
	case a.Weighted != nil:
		for j := range a.Weighted.Branches {
			applyModelOnlyToWeightedBranch(&a.Weighted.Branches[j], backend)
		}
	case a.Parallel != nil:
		for j := range a.Parallel.Branches {
			if strings.TrimSpace(a.Parallel.Branches[j].Target.Backend) == "" {
				a.Parallel.Branches[j].Target.Backend = backend
			}
		}
	}
}

// SelectorHasEmptyBackend reports whether any primary still has an empty backend id.
func SelectorHasEmptyBackend(sel *Selector) bool {
	if sel == nil {
		return false
	}
	for _, alt := range sel.Alternatives {
		if alt.Primary != nil && strings.TrimSpace(alt.Primary.Backend) == "" {
			return true
		}
		if alt.Weighted != nil {
			if slices.ContainsFunc(alt.Weighted.Branches, weightedBranchHasEmptyBackend) {
				return true
			}
		}
		if alt.Parallel != nil {
			for _, b := range alt.Parallel.Branches {
				if strings.TrimSpace(b.Target.Backend) == "" {
					return true
				}
			}
		}
	}
	return false
}

func applyModelOnlyToWeightedBranch(b *WeightedBranch, backend string) {
	if b == nil {
		return
	}
	if b.Parallel != nil {
		for j := range b.Parallel.Branches {
			if strings.TrimSpace(b.Parallel.Branches[j].Target.Backend) == "" {
				b.Parallel.Branches[j].Target.Backend = backend
			}
		}
		return
	}
	if strings.TrimSpace(b.Target.Backend) == "" {
		b.Target.Backend = backend
	}
}

func defaultBackendFromWeightedBranch(b WeightedBranch) (string, error) {
	if b.Parallel != nil {
		if len(b.Parallel.Branches) == 0 {
			return "", fmt.Errorf("routing default_route parallel branch must name a backend")
		}
		backend := strings.TrimSpace(b.Parallel.Branches[0].Target.Backend)
		if backend == "" {
			return "", fmt.Errorf("routing default_route parallel branch must name a backend")
		}
		return backend, nil
	}
	backend := strings.TrimSpace(b.Target.Backend)
	if backend == "" {
		return "", fmt.Errorf("routing default_route weighted branch must name a backend")
	}
	return backend, nil
}

func weightedBranchHasEmptyBackend(b WeightedBranch) bool {
	if b.Parallel != nil {
		for _, leg := range b.Parallel.Branches {
			if strings.TrimSpace(leg.Target.Backend) == "" {
				return true
			}
		}
		return false
	}
	return strings.TrimSpace(b.Target.Backend) == ""
}

// BackendIDsReferenced returns every literal (non-model-only) backend ID
// embedded across sel's failover alternatives, weighted branches, and
// parallel branches, in selector order. Used to validate a candidate default
// route or alias replacement against the candidate backend set before
// publication (req 9.2). A nil selector returns nil.
func BackendIDsReferenced(sel *Selector) []string {
	var out []string
	for _, leaf := range routeLeaves(sel) {
		appendBackendID(&out, leaf.Backend)
	}
	return out
}

// routeLeaves returns every concrete backend:model leaf of sel, in selector
// order: the failover alternatives, the weighted branches (including a parallel
// group embedded in a weighted branch) and the parallel branches.
//
// It is the complete traversal of a parsed selector. Every validator that has
// to inspect the leaves of a route uses it, so no leaf can escape a check by
// hiding in a nested branch.
func routeLeaves(sel *Selector) []Primary {
	if sel == nil {
		return nil
	}
	var out []Primary
	for _, alt := range sel.Alternatives {
		switch {
		case alt.Primary != nil:
			out = append(out, *alt.Primary)
		case alt.Weighted != nil:
			for _, b := range alt.Weighted.Branches {
				if b.Parallel != nil {
					for _, leg := range b.Parallel.Branches {
						out = append(out, leg.Target)
					}
					continue
				}
				out = append(out, b.Target)
			}
		case alt.Parallel != nil:
			for _, b := range alt.Parallel.Branches {
				out = append(out, b.Target)
			}
		}
	}
	return out
}

func appendBackendID(out *[]string, backend string) {
	backend = strings.TrimSpace(backend)
	if backend != "" {
		*out = append(*out, backend)
	}
}

// DefaultBackendFromRouteSelector extracts the backend id from a configured default route
// (for example "openai-responses:gpt-4o-mini"). The route must contain at least one primary
// with a non-empty backend (not model-only).
func DefaultBackendFromRouteSelector(defaultRoute string) (string, error) {
	defaultRoute = strings.TrimSpace(defaultRoute)
	if defaultRoute == "" {
		return "", nil
	}
	sel, err := Parse(defaultRoute)
	if err != nil {
		return "", fmt.Errorf("routing default_route: %w", err)
	}
	for _, alt := range sel.Alternatives {
		if alt.Primary != nil {
			b := strings.TrimSpace(alt.Primary.Backend)
			if b == "" {
				return "", fmt.Errorf("routing default_route must not be model-only (missing backend)")
			}
			return b, nil
		}
		if alt.Weighted != nil && len(alt.Weighted.Branches) > 0 {
			b, err := defaultBackendFromWeightedBranch(alt.Weighted.Branches[0])
			if err != nil {
				return "", err
			}
			return b, nil
		}
		if alt.Parallel != nil && len(alt.Parallel.Branches) > 0 {
			b := strings.TrimSpace(alt.Parallel.Branches[0].Target.Backend)
			if b == "" {
				return "", fmt.Errorf("routing default_route parallel branch must name a backend")
			}
			return b, nil
		}
	}
	return "", fmt.Errorf("routing default_route has no primary")
}
