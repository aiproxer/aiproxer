// Package billsem is a self-contained, test-only reference implementation of
// component-schema quantity semantics.
//
// It is deliberately independent of production billing code: it imports only
// the Go standard library and the public pkg/aipsdk/metering DTOs. It exists so
// that a second, independently written oracle can later falsify the production
// evaluator. It is not wired into any runtime path.
//
// The solver is deterministic: every map iteration is performed through a
// sorted key order, so neither Go map ordering nor relationship declaration
// order influence the result.
package billsem

import (
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// ErrInvalidInput marks genuinely malformed input (for example a schema that
// fails the public Validate, or an ObsExact state without a value). Semantic
// quantity problems are reported as classifications, never as errors.
var ErrInvalidInput = errors.New("billsem: invalid input")

// ObsState is the exact, three-valued observation state of one component in one
// reduction scope. The three states are DISTINCT:
//
//   - ObsAbsent:      the component was not reported. Exact(0) is NOT absent.
//   - ObsExact:       a reported, complete, exact rational quantity.
//   - ObsUnavailable: reported but not usable/unknown. Never becomes zero.
type ObsState int

const (
	ObsAbsent ObsState = iota
	ObsExact
	ObsUnavailable
)

// Evidence carries per-scope observation states and, for ObsExact entries, the
// reported rational value. Scopes never share evidence.
type Evidence struct {
	// States maps scope -> canonical component key -> observation state.
	States map[string]map[string]ObsState
	// Values maps scope -> canonical component key -> exact quantity. It must
	// contain an entry for every ObsExact state.
	Values map[string]map[string]*big.Rat
}

// NewEvidence allocates both maps so callers never have to handle nil maps.
func NewEvidence() Evidence {
	return Evidence{
		States: make(map[string]map[string]ObsState),
		Values: make(map[string]map[string]*big.Rat),
	}
}

// Class is the reported classification of one node in one scope. The constants
// are listed in classification precedence order.
type Class int

const (
	// ClassInactive means there is no evidence anywhere in its complete region.
	ClassInactive Class = iota
	// ClassContradicted means lower > upper after the fixed point.
	ClassContradicted
	// ClassIncomplete means active but a required member is missing/unavailable.
	ClassIncomplete
	// ClassAmbiguous means the node has more than one complete-coverage owner.
	ClassAmbiguous
	// ClassRepresented means an exact represented quantity is available.
	ClassRepresented
)

// String renders a Class for diagnostics and canonical serialization.
func (c Class) String() string {
	switch c {
	case ClassInactive:
		return "inactive"
	case ClassContradicted:
		return "contradicted"
	case ClassIncomplete:
		return "incomplete"
	case ClassAmbiguous:
		return "ambiguous"
	case ClassRepresented:
		return "represented"
	default:
		return fmt.Sprintf("class(%d)", int(c))
	}
}

// NodeResult is the solved state of one component in one scope.
type NodeResult struct {
	Key         string
	Lower       *big.Rat
	Upper       *big.Rat // nil means +infinity
	UpperFinite bool
	Class       Class
	// Represented is non-nil only when Class == ClassRepresented.
	Represented *big.Rat
	// Absent and Unavailable preserve the observation distinction even though
	// both share the [0, +inf) interval.
	Absent      bool
	Unavailable bool
}

// ScopeResult is the solved state of one scope. Nodes may be iterated
// deterministically via SortedKeys.
type ScopeResult struct {
	Scope               string
	Nodes               map[string]NodeResult
	Contradicted        []string
	Incomplete          []string
	Ambiguous           []string
	UnknownIntersection [][2]string
}

// SortedKeys returns map keys in stable sorted order.
func SortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Solve is the single entry point. It is deterministic and does not mutate its
// inputs.
func Solve(schemas []metering.ComponentSchema, ev Evidence) ([]ScopeResult, error) {
	if err := metering.ValidateComponentSchemas(schemas); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	g := buildGraph(schemas)
	scopes := evidenceScopes(ev)
	out := make([]ScopeResult, 0, len(scopes))
	for _, scope := range scopes {
		sr, err := solveScope(scope, ev, g)
		if err != nil {
			return nil, err
		}
		out = append(out, sr)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Rat helpers.
// ---------------------------------------------------------------------------

func ratInt(i int64) *big.Rat { return new(big.Rat).SetInt64(i) }

func cloneRat(r *big.Rat) *big.Rat {
	if r == nil {
		return nil
	}
	return new(big.Rat).Set(r)
}

func clamp0(r *big.Rat) *big.Rat {
	if r.Sign() < 0 {
		return ratInt(0)
	}
	return new(big.Rat).Set(r)
}

// ---------------------------------------------------------------------------
// Graph construction.
// ---------------------------------------------------------------------------

type containmentEdge struct {
	parent   string
	child    string
	optional bool
}

type graph struct {
	// allKeys is every component key declared by any relationship (including
	// transform and cross-direction/unit edges that carry no containment).
	allKeys map[string]struct{}
	// subsets holds the meaningful P --subset--> C edges.
	subsets []containmentEdge
	// complete holds the meaningful complete-coverage children of each parent
	// (partition and aggregate are synonyms). Children are sorted by key.
	complete map[string][]containmentEdge
	// parentList is the sorted set of complete-coverage parents.
	parentList []string
	// completeOwners maps a child to the distinct complete-coverage parents.
	completeOwners map[string]map[string]struct{}
	// containChildren / containParents are the meaningful containment adjacency
	// (subset + complete) used for anisotropy and intersection analysis.
	containChildren map[string]map[string]struct{}
	containParents  map[string]map[string]struct{}
}

func buildGraph(schemas []metering.ComponentSchema) *graph {
	g := &graph{
		allKeys:         map[string]struct{}{},
		complete:        map[string][]containmentEdge{},
		completeOwners:  map[string]map[string]struct{}{},
		containChildren: map[string]map[string]struct{}{},
		containParents:  map[string]map[string]struct{}{},
	}
	seen := map[string]struct{}{}
	for _, schema := range schemas {
		for _, r := range schema.Relationships {
			parent := r.Parent.CanonicalKey()
			child := r.Child.CanonicalKey()
			g.allKeys[parent] = struct{}{}
			g.allKeys[child] = struct{}{}

			complete := r.Kind == metering.RelationshipPartition || r.Kind == metering.RelationshipAggregate
			subset := r.Kind == metering.RelationshipSubset
			if !complete && !subset {
				continue // transform and anything else are never traversed.
			}
			if r.Parent.Direction != r.Child.Direction || r.Parent.Unit != r.Child.Unit {
				continue // not a meaningful containment edge.
			}
			dedup := r.Kind.String() + "\x00" + parent + "\x00" + child
			if _, ok := seen[dedup]; ok {
				continue
			}
			seen[dedup] = struct{}{}

			if g.containChildren[parent] == nil {
				g.containChildren[parent] = map[string]struct{}{}
			}
			g.containChildren[parent][child] = struct{}{}
			if g.containParents[child] == nil {
				g.containParents[child] = map[string]struct{}{}
			}
			g.containParents[child][parent] = struct{}{}

			if complete {
				g.complete[parent] = append(g.complete[parent], containmentEdge{
					parent:   parent,
					child:    child,
					optional: r.Optional,
				})
				if g.completeOwners[child] == nil {
					g.completeOwners[child] = map[string]struct{}{}
				}
				g.completeOwners[child][parent] = struct{}{}
			} else {
				g.subsets = append(g.subsets, containmentEdge{parent: parent, child: child})
			}
		}
	}
	for parent := range g.complete {
		children := g.complete[parent]
		sort.Slice(children, func(i, j int) bool { return children[i].child < children[j].child })
		g.complete[parent] = children
	}
	g.parentList = SortedKeys(g.complete)
	sort.Slice(g.subsets, func(i, j int) bool {
		if g.subsets[i].parent != g.subsets[j].parent {
			return g.subsets[i].parent < g.subsets[j].parent
		}
		return g.subsets[i].child < g.subsets[j].child
	})
	return g
}

func evidenceScopes(ev Evidence) []string {
	set := map[string]struct{}{}
	for scope := range ev.States {
		set[scope] = struct{}{}
	}
	for scope := range ev.Values {
		set[scope] = struct{}{}
	}
	return SortedKeys(set)
}

// ---------------------------------------------------------------------------
// Per-scope solving.
// ---------------------------------------------------------------------------

type nodeState struct {
	key   string
	state ObsState
	value *big.Rat // own exact value, when state == ObsExact
	lower *big.Rat // >= 0
	upper *big.Rat // nil means +infinity
}

func solveScope(scope string, ev Evidence, g *graph) (ScopeResult, error) {
	keys := map[string]struct{}{}
	for k := range g.allKeys {
		keys[k] = struct{}{}
	}
	for k := range ev.States[scope] {
		keys[k] = struct{}{}
	}
	for k := range ev.Values[scope] {
		keys[k] = struct{}{}
	}

	nodes := make(map[string]*nodeState, len(keys))
	for _, k := range SortedKeys(keys) {
		state := ObsAbsent
		if m := ev.States[scope]; m != nil {
			if s, ok := m[k]; ok {
				state = s
			}
		}
		n := &nodeState{key: k, state: state, lower: ratInt(0)}
		if state == ObsExact {
			var v *big.Rat
			if m := ev.Values[scope]; m != nil {
				v = m[k]
			}
			if v == nil {
				return ScopeResult{}, fmt.Errorf("%w: scope %q key %q: exact state requires a value", ErrInvalidInput, scope, k)
			}
			n.value = cloneRat(v)
			n.lower = clamp0(n.value)
			n.upper = cloneRat(n.value)
		}
		nodes[k] = n
	}

	represented := deriveRepresented(nodes, g)
	iterateBounds(nodes, g, represented)

	sr := ScopeResult{Scope: scope, Nodes: make(map[string]NodeResult, len(nodes))}
	for _, k := range SortedKeys(nodes) {
		n := nodes[k]
		contradicted := n.upper != nil && n.lower.Cmp(n.upper) > 0
		ambiguous := len(g.completeOwners[k]) > 1

		class := ClassInactive
		switch {
		case contradicted:
			class = ClassContradicted
		case ambiguous:
			class = ClassAmbiguous
		case incompleteNode(n, k, g, represented):
			class = ClassIncomplete
		case represented[k] != nil:
			class = ClassRepresented
		case active(n):
			class = ClassIncomplete
		}

		nr := NodeResult{
			Key:         k,
			Lower:       cloneRat(n.lower),
			Upper:       cloneRat(n.upper),
			UpperFinite: n.upper != nil,
			Class:       class,
			Absent:      n.state == ObsAbsent,
			Unavailable: n.state == ObsUnavailable,
		}
		if class == ClassRepresented {
			nr.Represented = cloneRat(represented[k])
		}
		sr.Nodes[k] = nr
		if contradicted {
			sr.Contradicted = append(sr.Contradicted, k)
		}
		if class == ClassIncomplete {
			sr.Incomplete = append(sr.Incomplete, k)
		}
		if ambiguous {
			sr.Ambiguous = append(sr.Ambiguous, k)
		}
	}
	sr.UnknownIntersection = unknownIntersections(nodes, g)
	return sr, nil
}

func active(n *nodeState) bool {
	return n.state != ObsAbsent || n.lower.Sign() > 0 || n.upper != nil
}

// incompleteNode reports whether a node's complete coverage cannot be proven:
// its own evidence is Unavailable, or a required complete-coverage member has
// no exact representation (missing, unavailable, or itself incomplete).
func incompleteNode(n *nodeState, k string, g *graph, represented map[string]*big.Rat) bool {
	if n.state == ObsUnavailable {
		return true
	}
	for _, e := range g.complete[k] {
		if e.optional {
			continue
		}
		if represented[e.child] == nil {
			return true
		}
	}
	return false
}

// raiseLower tightens n.lower to cand (clamped at zero) and reports a change.
func raiseLower(n *nodeState, cand *big.Rat) bool {
	if cand == nil {
		return false
	}
	c := clamp0(cand)
	if c.Cmp(n.lower) > 0 {
		n.lower = c
		return true
	}
	return false
}

// lowerUpper tightens n.upper to cand and reports a change. A nil cand is
// +infinity and never tightens.
func lowerUpper(n *nodeState, cand *big.Rat) bool {
	if cand == nil {
		return false
	}
	if n.upper != nil && n.upper.Cmp(cand) <= 0 {
		return false
	}
	n.upper = cloneRat(cand)
	return true
}

// edgeLocalBounds returns the bound interval a complete-coverage equation sees
// for one child edge. An ABSENT OPTIONAL member contributes an edge-local zero;
// it never becomes a global zero. Every other state is seen through its global
// bounds (Absent required and Unavailable both remain [0, +inf)).
func edgeLocalBounds(e containmentEdge, n *nodeState) (low, up *big.Rat, upFinite bool) {
	if n.state == ObsAbsent && e.optional {
		return ratInt(0), ratInt(0), true
	}
	return n.lower, n.upper, n.upper != nil
}

// iterateBounds runs the subset and complete-coverage propagations to a fixed
// point. Lower bounds only increase and upper bounds only decrease, so the
// iteration is monotone and terminates.
//
// The child-directed conservation rules are applied only to children with a
// known exact representation (own Exact evidence or an exact complete-coverage
// derivation). Applying them to a missing/unavailable child would synthesize a
// value out of absence, which the spec forbids (absence is never a global zero).
func iterateBounds(nodes map[string]*nodeState, g *graph, represented map[string]*big.Rat) {
	edgeCount := len(g.subsets)
	for _, children := range g.complete {
		edgeCount += len(children)
	}
	maxSweeps := 4*(len(nodes)+edgeCount) + 64

	for range maxSweeps {
		changed := false

		for _, e := range g.subsets {
			if raiseLower(nodes[e.parent], nodes[e.child].lower) {
				changed = true
			}
			if lowerUpper(nodes[e.child], nodes[e.parent].upper) {
				changed = true
			}
		}

		for _, parent := range g.parentList {
			children := g.complete[parent]
			if len(children) == 0 {
				continue
			}
			lows := make([]*big.Rat, len(children))
			ups := make([]*big.Rat, len(children))
			upFinite := make([]bool, len(children))
			sumLow := ratInt(0)
			sumUp := ratInt(0)
			allUpFinite := true
			for i, e := range children {
				l, u, f := edgeLocalBounds(e, nodes[e.child])
				lows[i], ups[i], upFinite[i] = l, u, f
				sumLow.Add(sumLow, l)
				if f {
					sumUp.Add(sumUp, u)
				} else {
					allUpFinite = false
				}
			}
			if raiseLower(nodes[parent], sumLow) {
				changed = true
			}
			if allUpFinite && lowerUpper(nodes[parent], sumUp) {
				changed = true
			}

			for i := range children {
				child := children[i].child
				if represented[child] == nil {
					// Unknown/absent/unavailable term: never derive a value.
					continue
				}
				if parentUpper := nodes[parent].upper; parentUpper != nil {
					otherLow := ratInt(0)
					for j := range children {
						if j != i {
							otherLow.Add(otherLow, lows[j])
						}
					}
					cand := new(big.Rat).Sub(parentUpper, otherLow)
					if lowerUpper(nodes[child], cand) {
						changed = true
					}
				}
				allOthersFinite := true
				otherUp := ratInt(0)
				for j := range children {
					if j == i {
						continue
					}
					if upFinite[j] {
						otherUp.Add(otherUp, ups[j])
					} else {
						allOthersFinite = false
					}
				}
				if allOthersFinite {
					cand := new(big.Rat).Sub(nodes[parent].lower, otherUp)
					if raiseLower(nodes[child], cand) {
						changed = true
					}
				}
			}
		}

		if !changed {
			return
		}
	}
}

// deriveRepresented computes the exact represented quantity, tracked separately
// from bounds. A node is represented only when its own evidence is Exact, or
// when it has a complete-coverage derivation whose required members are all
// represented (absent optional members contribute edge-local zero) and it is
// not itself ambiguously owned. A collapsed subset interval NEVER promotes a
// node here.
func deriveRepresented(nodes map[string]*nodeState, g *graph) map[string]*big.Rat {
	represented := make(map[string]*big.Rat, len(nodes))
	for k, n := range nodes {
		if n.state == ObsExact {
			represented[k] = cloneRat(n.value)
		}
	}
	edgeCount := 0
	for _, children := range g.complete {
		edgeCount += len(children)
	}
	maxSweeps := 4*(len(nodes)+edgeCount) + 64

	for range maxSweeps {
		changed := false
		for _, parent := range g.parentList {
			if represented[parent] != nil {
				continue
			}
			n := nodes[parent]
			if n.state == ObsUnavailable {
				continue
			}
			if len(g.completeOwners[parent]) > 1 {
				continue
			}
			children := g.complete[parent]
			if len(children) == 0 {
				continue
			}
			sum := ratInt(0)
			ok := true
			any := false
			for _, e := range children {
				if nodes[e.child].state == ObsAbsent && e.optional {
					any = true
					continue
				}
				rv := represented[e.child]
				if rv == nil {
					ok = false
					break
				}
				sum.Add(sum, rv)
				any = true
			}
			if ok && any {
				represented[parent] = sum
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return represented
}

// ---------------------------------------------------------------------------
// Unknown intersection.
// ---------------------------------------------------------------------------

func reachable(start string, adjacency map[string]map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for next := range adjacency[cur] {
			if _, seen := out[next]; seen {
				continue
			}
			out[next] = struct{}{}
			queue = append(queue, next)
		}
	}
	return out
}

func reportedPositive(n *nodeState) bool {
	return n.state == ObsExact && n.value != nil && n.value.Sign() > 0
}

// unknownIntersections reports pairs of distinct present-positive nodes that
// share a containment ancestor, are not ancestor/descendant of each other, and
// are not separated into distinct branches by a validated complete partition.
// It never affects the contradiction set or completeness verdicts.
func unknownIntersections(nodes map[string]*nodeState, g *graph) [][2]string {
	keys := SortedKeys(nodes)
	ancestors := make(map[string]map[string]struct{}, len(keys))
	descendants := make(map[string]map[string]struct{}, len(keys))
	for _, k := range keys {
		ancestors[k] = reachable(k, g.containParents)
		descendants[k] = reachable(k, g.containChildren)
	}

	var out [][2]string
	for i := range len(keys) {
		for j := i + 1; j < len(keys); j++ {
			u, v := keys[i], keys[j]
			if !reportedPositive(nodes[u]) || !reportedPositive(nodes[v]) {
				continue
			}
			if _, ok := ancestors[u][v]; ok {
				continue // v is an ancestor of u
			}
			if _, ok := ancestors[v][u]; ok {
				continue // u is an ancestor of v
			}
			common := false
			separated := false
			for w := range ancestors[u] {
				if _, ok := ancestors[v][w]; !ok {
					continue
				}
				common = true
				if wn := nodes[w]; wn.upper != nil && wn.lower.Cmp(wn.upper) > 0 {
					continue // not a validated partition
				}
				if separatedAt(w, u, v, g, descendants) {
					separated = true
					break
				}
			}
			if common && !separated {
				out = append(out, [2]string{u, v})
			}
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a][0] != out[b][0] {
			return out[a][0] < out[b][0]
		}
		return out[a][1] < out[b][1]
	})
	return out
}

// separatedAt reports whether u and v fall under different complete-coverage
// children of w, i.e. w partitions them into distinct branches.
func separatedAt(w, u, v string, g *graph, descendants map[string]map[string]struct{}) bool {
	under := func(target string) (string, bool) {
		for _, e := range g.complete[w] {
			child := e.child
			if child == target {
				return child, true
			}
			if _, ok := descendants[child][target]; ok {
				return child, true
			}
		}
		return "", false
	}
	cu, oku := under(u)
	cv, okv := under(v)
	return oku && okv && cu != cv
}
