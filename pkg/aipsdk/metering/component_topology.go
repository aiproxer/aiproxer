package metering

import (
	"errors"
	"fmt"
	"slices"
)

// ErrComponentSchemaTopology is wrapped by every component-schema topology
// finding. It is a separate sentinel from ErrInvalidComponentSchema so a caller
// can accept a graph that is structurally describable but topologically
// incompatible, and report it, without confusing it with a malformed edge.
var ErrComponentSchemaTopology = errors.New("metering: invalid component schema topology")

// SchemaTopologyRule names one evidence-independent, structurally-knowable
// impossibility in a declared containment graph. Every rule is decidable from
// the declared edges alone: no observation, tariff or price is consulted, so a
// rule finding is a property of the published schema, not of a rated call.
type SchemaTopologyRule string

const (
	// SchemaTopologyDirectionMismatch rejects a containment edge whose parent
	// and child carry different economic directions. Aggregate, partition and
	// subset all assert that one quantity is contained in another, which is
	// only a meaningful claim within a single flow; an edge that crosses from
	// input to output (or to/from none) can never be discharged. Unit equality
	// is already required for every non-transform edge, so direction is the
	// remaining identity gap on the same seam.
	//
	// This rule is reported, not enforced by default: a cross-direction edge is
	// a legal, if inert, way to declare a provider vocabulary entry on the far
	// side of a flow, and the historical behaviour is to ignore such an edge
	// rather than refuse the whole schema. See EnforcedComponentSchemaTopologyRules.
	SchemaTopologyDirectionMismatch SchemaTopologyRule = "direction-mismatch"

	// SchemaTopologyCompleteSiblingContains rejects two complete
	// (aggregate/partition) siblings of the same parent where one transitively
	// contains the other. A complete coverage asserts the disjoint additive sum
	// parent = sum(children); a complete child that also contains its complete
	// sibling double-declares that sibling inside the same sum, so the declared
	// disjointness cannot be satisfied by any quantities.
	//
	// This rule is reported, not enforced by default: it rejects 80 of the 343
	// generated three-node topologies, and the order-invariance regression
	// enumerates all 343 unconditionally. See EnforcedComponentSchemaTopologyRules.
	SchemaTopologyCompleteSiblingContains SchemaTopologyRule = "complete-sibling-contains-sibling"

	// SchemaTopologyCompleteSiblingSharedDescendant rejects two complete
	// (aggregate/partition) siblings of the same parent whose declared
	// containment descendant sets intersect: a node is reachable under both
	// branches. Each branch's additive sum therefore claims a share of the same
	// descendant, and a single declared quantity cannot satisfy both disjoint
	// sums.
	//
	// Reachability follows containment edges only (aggregate, partition and
	// subset). A transform is a separately governed unit derivation and is
	// never containment, so a transform-reachable node does not intersect.
	SchemaTopologyCompleteSiblingSharedDescendant SchemaTopologyRule = "complete-sibling-shared-descendant"
)

// AllSchemaTopologyRules returns every rule this package can report, in a
// stable order. It exists so a caller can enumerate the rule space without
// depending on the order of declaration.
func AllSchemaTopologyRules() []SchemaTopologyRule {
	return []SchemaTopologyRule{
		SchemaTopologyDirectionMismatch,
		SchemaTopologyCompleteSiblingContains,
		SchemaTopologyCompleteSiblingSharedDescendant,
	}
}

// IsKnown reports whether r is a rule this package can report.
func (r SchemaTopologyRule) IsKnown() bool {
	return slices.Contains(AllSchemaTopologyRules(), r)
}

// SchemaTopologyRuleSet is an immutable set of topology rules. The zero value is
// the empty set, which reports nothing and enforces nothing.
type SchemaTopologyRuleSet struct {
	rules []SchemaTopologyRule
}

// SchemaTopologyRuleNone is the empty rule set: inspect only, enforce nothing.
var SchemaTopologyRuleNone = SchemaTopologyRuleSet{}

// SchemaTopologyAllRules is the full rule set. Enforcing it rejects every
// topology this package can report.
var SchemaTopologyAllRules = SchemaTopologyRuleSet{rules: AllSchemaTopologyRules()}

// With returns a set containing r in addition to the receiver's rules. The
// receiver is not modified.
func (s SchemaTopologyRuleSet) With(r SchemaTopologyRule) SchemaTopologyRuleSet {
	if r.IsKnown() && !s.Contains(r) {
		next := make([]SchemaTopologyRule, 0, len(s.rules)+1)
		next = append(next, s.rules...)
		next = append(next, r)
		return SchemaTopologyRuleSet{rules: next}
	}
	return s
}

// Contains reports whether r is a member of the set. An unknown rule is never
// a member.
func (s SchemaTopologyRuleSet) Contains(r SchemaTopologyRule) bool {
	return slices.Contains(s.rules, r)
}

// Rules returns a copy of the set's members in AllSchemaTopologyRules order.
func (s SchemaTopologyRuleSet) Rules() []SchemaTopologyRule {
	out := make([]SchemaTopologyRule, 0, len(s.rules))
	for _, rule := range AllSchemaTopologyRules() {
		if s.Contains(rule) {
			out = append(out, rule)
		}
	}
	return out
}

// EnforcedComponentSchemaTopologyRules returns the rules
// ValidateComponentSchemas currently rejects. It is a function rather than a
// variable so a caller cannot mutate package state, and it is the single place
// that decides publication-time enforcement.
func EnforcedComponentSchemaTopologyRules() SchemaTopologyRuleSet {
	return SchemaTopologyRuleSet{rules: enforcedSchemaTopologyRules()}
}

// SchemaTopologyFinding is one reported structural incompatibility. Every field
// is a canonical component key or a schema id, never a caller-owned pointer, so
// a finding is safe to log, persist or compare after the inspected slice is
// released.
type SchemaTopologyFinding struct {
	// Rule is the structural impossibility that was found.
	Rule SchemaTopologyRule
	// SchemaID names the schema that declared the offending edges. A finding
	// is always attributable to one declared schema, even when the two
	// interacting edges are declared in different schemas.
	SchemaID string
	// Parent is the canonical key of the common parent of the interacting
	// complete siblings. It is empty for SchemaTopologyDirectionMismatch,
	// which is an edge-local property and names no second edge.
	Parent string
	// Left is the canonical key of the first interacting complete child, or
	// the edge's child for SchemaTopologyDirectionMismatch.
	Left string
	// Right is the canonical key of the second interacting complete child, or
	// the edge's parent for SchemaTopologyDirectionMismatch.
	Right string
	// Shared is the canonical key of the node reachable under both branches.
	// It is only set for SchemaTopologyCompleteSiblingSharedDescendant.
	Shared string
	// Detail is a human-readable rendering of the finding.
	Detail string
}

func (f SchemaTopologyFinding) String() string { return f.Detail }

// SchemaTopologyReport is the complete, non-fatal result of inspecting a schema
// set for structurally-knowable impossibilities. A report is always returned in
// full: enforcement is a separate, explicit decision so that a caller can
// surface every finding at once instead of learning about them one rejected
// schema at a time.
type SchemaTopologyReport struct {
	// Findings is every structural incompatibility found, ordered by
	// AllSchemaTopologyRules order and then by parent/left/right/shared so the
	// report is deterministic for a given input.
	Findings []SchemaTopologyFinding
}

// Len returns the number of findings.
func (r SchemaTopologyReport) Len() int { return len(r.Findings) }

// Has reports whether any finding carries rule.
func (r SchemaTopologyReport) Has(rule SchemaTopologyRule) bool {
	return slices.ContainsFunc(r.Findings, func(f SchemaTopologyFinding) bool { return f.Rule == rule })
}

// Rules returns the distinct rules present in the report, in
// AllSchemaTopologyRules order.
func (r SchemaTopologyReport) Rules() []SchemaTopologyRule {
	out := make([]SchemaTopologyRule, 0, len(r.Findings))
	for _, rule := range AllSchemaTopologyRules() {
		if r.Has(rule) {
			out = append(out, rule)
		}
	}
	return out
}

// For returns only the findings carrying rule.
func (r SchemaTopologyReport) For(rule SchemaTopologyRule) []SchemaTopologyFinding {
	var out []SchemaTopologyFinding
	for _, finding := range r.Findings {
		if finding.Rule == rule {
			out = append(out, finding)
		}
	}
	return out
}

// Err returns a single error describing the whole report, or nil when the
// report is empty. The returned error wraps ErrComponentSchemaTopology and
// ErrInvalidComponentSchema, so it is catchable either as its own class or as
// the general malformed-schema class.
func (r SchemaTopologyReport) Err() error { return r.ErrFor(SchemaTopologyAllRules) }

// ErrFor returns a single error describing the findings whose rule is a member
// of enforced, or nil when no such finding exists. Rules outside enforced are
// still reported by Len and Has, so a caller can widen enforcement without
// re-inspecting.
func (r SchemaTopologyReport) ErrFor(enforced SchemaTopologyRuleSet) error {
	selected := make([]SchemaTopologyFinding, 0, len(r.Findings))
	for _, finding := range r.Findings {
		if enforced.Contains(finding.Rule) {
			selected = append(selected, finding)
		}
	}
	if len(selected) == 0 {
		return nil
	}
	details := make([]string, 0, len(selected))
	for _, finding := range selected {
		details = append(details, finding.Detail)
	}
	return fmt.Errorf("%w: %w: %d topology finding(s): %v", ErrInvalidComponentSchema, ErrComponentSchemaTopology, len(selected), details)
}

// schemaTopologyGraph is the containment projection the complete-sibling rules
// need. Nodes are canonical component keys; edges exist only for containment
// kinds, so a transform never contributes reachability.
type schemaTopologyGraph struct {
	// children is the containment adjacency, parent canonical key to child
	// canonical keys in first-declared order.
	children map[string][]string
	// complete is the complete-coverage adjacency, parent canonical key to
	// complete child canonical keys in first-declared order.
	complete map[string][]string
	// schemaOf records which schema declared the first edge between an ordered
	// parent/child pair, so a finding can name one owning schema.
	schemaOf map[string]string
	// direction is the declared economic direction per node.
	direction map[string]FlowDirection
}

func isContainmentKind(kind RelationshipKind) bool {
	switch kind {
	case RelationshipAggregate, RelationshipPartition, RelationshipSubset:
		return true
	default:
		return false
	}
}

func isCompleteKind(kind RelationshipKind) bool {
	return kind == RelationshipAggregate || kind == RelationshipPartition
}

// buildSchemaTopologyGraph projects a schema set onto the containment graph the
// topology rules reason about. It is total: it never rejects, and it never
// reorders or mutates caller-owned slices.
func buildSchemaTopologyGraph(schemas []ComponentSchema) *schemaTopologyGraph {
	graph := &schemaTopologyGraph{
		children:  make(map[string][]string),
		complete:  make(map[string][]string),
		schemaOf:  make(map[string]string),
		direction: make(map[string]FlowDirection),
	}
	for _, schema := range schemas {
		for _, relationship := range schema.Relationships {
			parent := relationship.Parent.CanonicalKey()
			child := relationship.Child.CanonicalKey()
			graph.direction[parent] = relationship.Parent.Direction
			graph.direction[child] = relationship.Child.Direction
			if !isContainmentKind(relationship.Kind) {
				continue
			}
			graph.children[parent] = append(graph.children[parent], child)
			if isCompleteKind(relationship.Kind) {
				graph.complete[parent] = append(graph.complete[parent], child)
			}
			if _, seen := graph.schemaOf[parent+"\x00"+child]; !seen {
				graph.schemaOf[parent+"\x00"+child] = schema.ID
			}
		}
	}
	return graph
}

// schemaOwning returns the id of a schema that declared the first edge between
// two keys, or the empty string when no such edge exists.
func (g *schemaTopologyGraph) schemaOwning(parent, child string) string {
	return g.schemaOf[parent+"\x00"+child]
}

// inspectSchemaTopology projects schemas and returns every finding of the three
// structurally-knowable rules, in deterministic order. It performs no
// enforcement and returns no error.
func inspectSchemaTopology(schemas []ComponentSchema) SchemaTopologyReport {
	graph := buildSchemaTopologyGraph(schemas)
	report := SchemaTopologyReport{}
	report.Findings = append(report.Findings, inspectSchemaTopologyDirection(schemas)...)
	report.Findings = append(report.Findings, inspectSchemaTopologyCompleteSiblings(graph)...)
	slices.SortStableFunc(report.Findings, compareSchemaTopologyFindings)
	return report
}

// compareSchemaTopologyFindings orders findings by rule, then parent, then the
// two interacting children, then the shared descendant. A report for a given
// input is therefore byte-identical across runs and declaration orders.
func compareSchemaTopologyFindings(a, b SchemaTopologyFinding) int {
	if a.Rule != b.Rule {
		if a.Rule < b.Rule {
			return -1
		}
		return 1
	}
	for _, pair := range [][2]string{
		{a.SchemaID, b.SchemaID},
		{a.Parent, b.Parent},
		{a.Left, b.Left},
		{a.Right, b.Right},
		{a.Shared, b.Shared},
	} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// inspectSchemaTopologyDirection reports every containment edge whose parent and
// child declare different economic directions. The edge is reported in declared
// order but the report is sorted by the caller, so the result is stable.
func inspectSchemaTopologyDirection(schemas []ComponentSchema) []SchemaTopologyFinding {
	var findings []SchemaTopologyFinding
	for _, schema := range schemas {
		for _, relationship := range schema.Relationships {
			if !isContainmentKind(relationship.Kind) {
				continue
			}
			if relationship.Parent.Direction == relationship.Child.Direction {
				continue
			}
			parent := relationship.Parent.CanonicalKey()
			child := relationship.Child.CanonicalKey()
			findings = append(findings, SchemaTopologyFinding{
				Rule:     SchemaTopologyDirectionMismatch,
				SchemaID: schema.ID,
				Left:     child,
				Right:    parent,
				Detail: fmt.Sprintf("schema %q: %s relationship %s -> %s spans directions %q and %q",
					schema.ID, relationship.Kind, child, parent,
					relationship.Child.Direction, relationship.Parent.Direction),
			})
		}
	}
	return findings
}

// inspectSchemaTopologyCompleteSiblings reports the two complete-sibling rules
// in a single pass per parent.
//
// For one parent and its complete children, the walk visits every node
// reachable from each complete child through containment edges. A node reached
// from two distinct complete children is reported once as a shared descendant.
// A node reached from a complete child that is itself a complete child of the
// same parent is reported as a containment instead, because that is the
// stronger and more specific statement. Reporting one finding per offending node
// keeps the report bounded by the node count rather than by the number of
// sibling pairs.
func inspectSchemaTopologyCompleteSiblings(graph *schemaTopologyGraph) []SchemaTopologyFinding {
	parents := make([]string, 0, len(graph.complete))
	for parent := range graph.complete {
		parents = append(parents, parent)
	}
	slices.Sort(parents)

	var findings []SchemaTopologyFinding
	for _, parent := range parents {
		children := graph.complete[parent]
		if len(children) < 2 {
			// A single complete child can neither be compared with a sibling nor
			// intersect with one, so there is nothing to decide for this parent.
			continue
		}
		isCompleteChild := make(map[string]struct{}, len(children))
		for _, child := range children {
			isCompleteChild[child] = struct{}{}
		}
		// reachers records, per reachable node, every complete child that reaches
		// it. Collecting the whole set and taking the two lexicographically
		// smallest members at report time makes the finding independent of the
		// order the complete children were declared in.
		reachers := make(map[string][]string)
		for _, child := range children {
			for _, node := range containmentDescendants(graph, child) {
				reachers[node] = append(reachers[node], child)
			}
		}

		sharedNodes := make([]string, 0, len(reachers))
		for node, owners := range reachers {
			if len(owners) < 2 {
				continue
			}
			if _, isSibling := isCompleteChild[node]; isSibling {
				// A complete child reached from another complete child is the
				// containment case, which is reported against the reachability
				// walk below rather than as a shared descendant.
				continue
			}
			sharedNodes = append(sharedNodes, node)
		}
		slices.Sort(sharedNodes)
		for _, node := range sharedNodes {
			owners := reachers[node]
			slices.Sort(owners)
			findings = append(findings, sharedDescendantFinding(graph, parent, owners[0], owners[1], node))
		}

		for _, left := range children {
			for _, right := range containmentDescendants(graph, left) {
				if _, isSibling := isCompleteChild[right]; !isSibling || right == left {
					continue
				}
				findings = append(findings, completeSiblingContainmentFinding(graph, parent, left, right))
			}
		}
	}
	return findings
}

func completeSiblingContainmentFinding(graph *schemaTopologyGraph, parent, left, right string) SchemaTopologyFinding {
	return SchemaTopologyFinding{
		Rule:     SchemaTopologyCompleteSiblingContains,
		SchemaID: graph.schemaOwning(parent, left),
		Parent:   parent,
		Left:     left,
		Right:    right,
		Detail: fmt.Sprintf("schema %q: complete children %s and %s of parent %s are containment-connected, so their disjoint additive sum cannot be satisfied",
			graph.schemaOwning(parent, left), left, right, parent),
	}
}

func sharedDescendantFinding(graph *schemaTopologyGraph, parent, left, right, shared string) SchemaTopologyFinding {
	return SchemaTopologyFinding{
		Rule:     SchemaTopologyCompleteSiblingSharedDescendant,
		SchemaID: graph.schemaOwning(parent, left),
		Parent:   parent,
		Left:     left,
		Right:    right,
		Shared:   shared,
		Detail: fmt.Sprintf("schema %q: complete children %s and %s of parent %s both contain %s, so their disjoint additive sum double-declares it",
			graph.schemaOwning(parent, left), left, right, parent, shared),
	}
}

// containmentDescendants returns every node reachable from node through
// containment edges, excluding node itself. The walk is cycle-safe, so it stays
// total on a graph the acyclicity check has not yet run against.
func containmentDescendants(graph *schemaTopologyGraph, node string) []string {
	visited := make(map[string]struct{})
	queue := make([]string, 0, len(graph.children[node]))
	queue = append(queue, graph.children[node]...)
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if _, seen := visited[next]; seen {
			continue
		}
		visited[next] = struct{}{}
		queue = append(queue, graph.children[next]...)
	}
	out := make([]string, 0, len(visited))
	for descendant := range visited {
		out = append(out, descendant)
	}
	slices.Sort(out)
	return out
}

// InspectComponentSchemaTopology reports every structurally-knowable
// impossibility in a bounded schema set without enforcing any of them. The
// result is the non-fatal form of the same analysis
// ValidateComponentSchemas performs, so a caller can certify, warn or reject.
//
// The analysis is decidable from the declared edges alone: it never consults an
// observation, a tariff or a price, and it is therefore stable for a given set
// of declarations regardless of what is later rated against it. A report is
// deterministic and its order does not depend on declaration order.
//
// Shared complete ownership, where one node is a complete member of more than
// one parent, is deliberately NOT reported here. It is a per-observation
// classification rather than a static impossibility, and it stays with the
// rater.
func InspectComponentSchemaTopology(schemas []ComponentSchema) SchemaTopologyReport {
	return inspectSchemaTopology(schemas)
}

// ValidateComponentSchemasWithTopology is ValidateComponentSchemas plus an
// explicit choice of which topology rules to enforce. The structural, unit,
// optionality, duplicate, ambiguity, acyclicity and bound checks always run and
// are unaffected by enforced; only the topology rules are selected.
//
// A caller that wants the widest possible gate passes SchemaTopologyAllRules. A
// caller that must accept already-published inert schemas passes the rule subset
// it can live with, and can still consult
// InspectComponentSchemaTopology for the findings it chose not to enforce.
func ValidateComponentSchemasWithTopology(schemas []ComponentSchema, enforced SchemaTopologyRuleSet) error {
	if err := validateComponentSchemaStructure(schemas); err != nil {
		return err
	}
	return inspectSchemaTopology(schemas).ErrFor(enforced)
}
