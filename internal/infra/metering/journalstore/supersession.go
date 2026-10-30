package journalstore

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// validateSupersessionGraph checks same-stream target existence and cycle
// rejection for correction/replacement facts (requirements 6.6–6.7, D7).
// lookup returns a fact by FactID within the current store namespace.
func validateSupersessionGraph(fact metering.Fact, lookup func(factID string) (metering.Fact, bool), edges map[string][]string) error {
	if !fact.Kind.RequiresSupersedes() {
		return nil
	}
	from := strings.TrimSpace(fact.FactID)
	stream := strings.TrimSpace(fact.StreamID)
	targets := make([]string, 0, len(fact.Supersedes))
	for _, raw := range fact.Supersedes {
		id := strings.TrimSpace(raw)
		if id == "" {
			return fmt.Errorf("%w: empty target", ErrSupersessionTarget)
		}
		if id == from {
			return fmt.Errorf("%w: self-target %q", ErrSupersessionCycle, id)
		}
		target, ok := lookup(id)
		if !ok {
			return fmt.Errorf("%w: missing fact_id=%q", ErrSupersessionTarget, id)
		}
		if strings.TrimSpace(target.StreamID) != stream {
			return fmt.Errorf("%w: target %q stream %q != %q", ErrSupersessionTarget, id, target.StreamID, stream)
		}
		targets = append(targets, id)
	}
	if supersessionCreatesCycle(edges, from, targets) {
		return fmt.Errorf("%w: fact_id=%q", ErrSupersessionCycle, from)
	}
	return nil
}

func supersessionCreatesCycle(edges map[string][]string, from string, targets []string) bool {
	for _, to := range targets {
		if to == from || supersessionReaches(edges, to, from) {
			return true
		}
	}
	return false
}

func supersessionReaches(edges map[string][]string, start, goal string) bool {
	if start == "" || goal == "" {
		return false
	}
	seen := map[string]struct{}{}
	stack := []string{start}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == goal {
			return true
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		stack = append(stack, edges[n]...)
	}
	return false
}

func supersessionEdgesFromFacts(facts []storedFact) map[string][]string {
	edges := make(map[string][]string)
	for _, stored := range facts {
		f := stored.fact
		if !f.Kind.RequiresSupersedes() {
			continue
		}
		from := strings.TrimSpace(f.FactID)
		for _, raw := range f.Supersedes {
			id := strings.TrimSpace(raw)
			if id == "" {
				continue
			}
			edges[from] = append(edges[from], id)
		}
	}
	return edges
}
