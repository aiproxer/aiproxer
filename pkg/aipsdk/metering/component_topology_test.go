package metering_test

// Table-driven tests for the three structurally-knowable component-schema
// topology rules.
//
// Each rule is exercised twice over: once with a minimal reproducer that must
// be reported, and once with a NEAR-MISS legal graph that shares most of the
// reproducer's shape and must still be accepted. The near-misses are the load-
// bearing half: a rule that cannot tell its own reproducer from a legal
// neighbour is not a usable publication gate.
//
// Every case also pins the ENFORCEMENT MODE of its rule, because the modes
// differ per rule and a future change to the enforced set must be a deliberate,
// visible edit rather than a silent one.

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// topologyKey builds a schema-qualified key so no case accidentally depends on
// the SDK's built-in component vocabulary or on its direction/unit tables.
func topologyKey(direction metering.FlowDirection, component string) metering.ComponentKey {
	return metering.ComponentKey{
		Direction: direction,
		Component: component,
		Unit:      metering.UnitToken,
		SchemaID:  "schema:topology:v1",
	}
}

func topologyEdge(kind metering.RelationshipKind, parent, child metering.ComponentKey, optional bool) metering.ComponentRelationship {
	return metering.ComponentRelationship{Kind: kind, Parent: parent, Child: child, Optional: optional}
}

func topologySchema(relationships ...metering.ComponentRelationship) []metering.ComponentSchema {
	return []metering.ComponentSchema{{ID: "schema:topology:v1", Version: "1", Relationships: relationships}}
}

// topologyCase is one table row: the graph, the rule it exercises, and whether
// that rule must report on it.
type topologyCase struct {
	name       string
	rule       metering.SchemaTopologyRule
	schemas    []metering.ComponentSchema
	wantReport bool
}

// assertTopologyCases runs a table and additionally pins the rule's mode in the
// shipped default gate, so a promoted or demoted rule fails here rather than in
// an unrelated package.
func assertTopologyCases(t *testing.T, wantEnforced bool, cases []topologyCase) {
	t.Helper()
	rule := cases[0].rule
	if got := metering.EnforcedComponentSchemaTopologyRules().Contains(rule); got != wantEnforced {
		t.Fatalf("rule %q enforced by the default gate = %t, want %t", rule, got, wantEnforced)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			report := metering.InspectComponentSchemaTopology(tc.schemas)
			if got := report.Has(tc.rule); got != tc.wantReport {
				t.Fatalf("rule %q reported = %t, want %t; findings=%v", tc.rule, got, tc.wantReport, report.Findings)
			}

			// The non-fatal surface must always agree with the rule verdict,
			// independently of which rules the caller chooses to enforce.
			onlyRule := metering.SchemaTopologyRuleNone.With(tc.rule)
			onlyErr := report.ErrFor(onlyRule)
			if tc.wantReport && onlyErr == nil {
				t.Fatalf("rule %q must produce a non-nil ErrFor error when reported: findings=%v", tc.rule, report.Findings)
			}
			if !tc.wantReport && onlyErr != nil {
				t.Fatalf("rule %q must not produce an ErrFor error: %v", tc.rule, onlyErr)
			}
			if tc.wantReport {
				if !errors.Is(onlyErr, metering.ErrComponentSchemaTopology) {
					t.Fatalf("rule %q error must wrap ErrComponentSchemaTopology: %v", tc.rule, onlyErr)
				}
				if !errors.Is(onlyErr, metering.ErrInvalidComponentSchema) {
					t.Fatalf("rule %q error must also wrap ErrInvalidComponentSchema: %v", tc.rule, onlyErr)
				}
			}

			// The shipped default gate enforces exactly the enforced subset.
			err := metering.ValidateComponentSchemas(tc.schemas)
			if wantEnforced && tc.wantReport {
				if !errors.Is(err, metering.ErrComponentSchemaTopology) {
					t.Fatalf("enforced rule %q must reject the schema at publication: %v", tc.rule, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("schema must be accepted by the default gate: %v", err)
			}
		})
	}
}

// TestSchemaTopologyDirectionMismatch pins rule 1: a containment edge must not
// span a different economic direction.
//
// The near-misses are the reason this rule is reported rather than enforced. A
// cross-direction subset and a cross-direction partition are both legal
// published shapes whose meaning is "ignore this edge", and pinned regressions
// depend on that. A cross-direction TRANSFORM is legal for a different reason:
// a transform is a unit derivation, not containment, so direction never applies
// to it at all.
func TestSchemaTopologyDirectionMismatch(t *testing.T) {
	t.Parallel()
	inputParent := topologyKey(metering.DirectionInput, "vendor:dir_parent")
	outputChild := topologyKey(metering.DirectionOutput, "vendor:dir_child")
	sameDirectionChild := topologyKey(metering.DirectionInput, "vendor:dir_same")
	noneParent := topologyKey(metering.DirectionNone, "vendor:dir_none")
	noneChild := topologyKey(metering.DirectionNone, "vendor:dir_none_child")

	cases := []topologyCase{
		{
			name:       "cross_direction_subset_is_reported",
			rule:       metering.SchemaTopologyDirectionMismatch,
			schemas:    topologySchema(topologyEdge(metering.RelationshipSubset, inputParent, outputChild, false)),
			wantReport: true,
		},
		{
			name:       "cross_direction_partition_is_reported",
			rule:       metering.SchemaTopologyDirectionMismatch,
			schemas:    topologySchema(topologyEdge(metering.RelationshipPartition, inputParent, outputChild, true)),
			wantReport: true,
		},
		{
			name:       "cross_direction_aggregate_is_reported",
			rule:       metering.SchemaTopologyDirectionMismatch,
			schemas:    topologySchema(topologyEdge(metering.RelationshipAggregate, inputParent, outputChild, false)),
			wantReport: true,
		},
		{
			name: "near_miss_same_direction_subset_is_legal",
			rule: metering.SchemaTopologyDirectionMismatch,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipSubset, inputParent, sameDirectionChild, false),
			),
			wantReport: false,
		},
		{
			name: "near_miss_cross_direction_transform_is_legal",
			rule: metering.SchemaTopologyDirectionMismatch,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipTransform, inputParent, outputChild, false),
			),
			wantReport: false,
		},
		{
			name: "near_miss_none_to_none_subset_is_legal",
			rule: metering.SchemaTopologyDirectionMismatch,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipSubset, noneParent, noneChild, false),
			),
			wantReport: false,
		},
	}
	assertTopologyCases(t, false, cases)
}

// TestSchemaTopologyCompleteSiblingContains pins rule 2: two COMPLETE siblings of
// the same parent must not be containment-connected.
//
// The minimal reproducer is exactly the three-node graph named in the review:
// n1 partition n2, n1 partition n3, n2 subset n3. The near-misses each remove
// one ingredient, so a rule that fires on any two complete siblings regardless
// of structure fails them.
func TestSchemaTopologyCompleteSiblingContains(t *testing.T) {
	t.Parallel()
	n1 := topologyKey(metering.DirectionInput, "vendor:cs_parent")
	n2 := topologyKey(metering.DirectionInput, "vendor:cs_left")
	n3 := topologyKey(metering.DirectionInput, "vendor:cs_right")
	leaf := topologyKey(metering.DirectionInput, "vendor:cs_leaf")
	otherParent := topologyKey(metering.DirectionInput, "vendor:cs_other_parent")

	cases := []topologyCase{
		{
			name: "minimal_reproducer_sibling_contains_sibling",
			rule: metering.SchemaTopologyCompleteSiblingContains,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipPartition, n1, n2, false),
				topologyEdge(metering.RelationshipPartition, n1, n3, false),
				topologyEdge(metering.RelationshipSubset, n2, n3, false),
			),
			wantReport: true,
		},
		{
			name: "reproducer_holds_for_aggregate_members_and_optional_flags",
			rule: metering.SchemaTopologyCompleteSiblingContains,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipAggregate, n1, n2, false),
				topologyEdge(metering.RelationshipPartition, n1, n3, true),
				topologyEdge(metering.RelationshipSubset, n2, n3, false),
			),
			wantReport: true,
		},
		{
			name: "reproducer_holds_across_a_longer_containment_chain",
			rule: metering.SchemaTopologyCompleteSiblingContains,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipPartition, n1, n2, false),
				topologyEdge(metering.RelationshipPartition, n1, n3, false),
				topologyEdge(metering.RelationshipSubset, n2, leaf, false),
				topologyEdge(metering.RelationshipSubset, leaf, n3, false),
			),
			wantReport: true,
		},
		{
			name: "near_miss_sibling_contains_a_non_sibling_leaf_is_legal",
			rule: metering.SchemaTopologyCompleteSiblingContains,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipPartition, n1, n2, false),
				topologyEdge(metering.RelationshipPartition, n1, n3, false),
				topologyEdge(metering.RelationshipSubset, n2, leaf, false),
			),
			wantReport: false,
		},
		{
			name: "near_miss_transform_between_siblings_is_legal",
			rule: metering.SchemaTopologyCompleteSiblingContains,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipPartition, n1, n2, false),
				topologyEdge(metering.RelationshipPartition, n1, n3, false),
				topologyEdge(metering.RelationshipTransform, n2, n3, false),
			),
			wantReport: false,
		},
		{
			name: "near_miss_single_complete_child_is_legal",
			rule: metering.SchemaTopologyCompleteSiblingContains,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipPartition, n1, n2, false),
				topologyEdge(metering.RelationshipSubset, n2, n3, false),
			),
			wantReport: false,
		},
		{
			name: "near_miss_containment_across_parents_is_legal",
			rule: metering.SchemaTopologyCompleteSiblingContains,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipPartition, n1, n2, false),
				topologyEdge(metering.RelationshipPartition, otherParent, n3, false),
				topologyEdge(metering.RelationshipSubset, n2, n3, false),
			),
			wantReport: false,
		},
	}
	assertTopologyCases(t, true, cases)
}

// TestSchemaTopologyCompleteSiblingSharedDescendant pins rule 3: two COMPLETE
// siblings of the same parent must not share a declared containment descendant.
//
// The rule needs four nodes, which is precisely why the generated three-node
// space never exercises it. The near-misses are the shapes the review explicitly
// keeps legal: nested complete partitions, a subset under one complete branch,
// redundant equivalent containment paths inside one branch, a transform-only
// convergence, and shared complete ownership between parents that are not
// siblings.
func TestSchemaTopologyCompleteSiblingSharedDescendant(t *testing.T) {
	t.Parallel()
	n1 := topologyKey(metering.DirectionInput, "vendor:sd_parent")
	n2 := topologyKey(metering.DirectionInput, "vendor:sd_left")
	n3 := topologyKey(metering.DirectionInput, "vendor:sd_right")
	shared := topologyKey(metering.DirectionInput, "vendor:sd_shared")
	leafL := topologyKey(metering.DirectionInput, "vendor:sd_leaf_left")
	leafR := topologyKey(metering.DirectionInput, "vendor:sd_leaf_right")
	farLeaf := topologyKey(metering.DirectionInput, "vendor:sd_far_leaf")
	otherParent := topologyKey(metering.DirectionInput, "vendor:sd_other_parent")

	cases := []topologyCase{
		{
			name: "minimal_reproducer_shared_subset_descendant",
			rule: metering.SchemaTopologyCompleteSiblingSharedDescendant,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipPartition, n1, n2, false),
				topologyEdge(metering.RelationshipPartition, n1, n3, false),
				topologyEdge(metering.RelationshipSubset, n2, shared, false),
				topologyEdge(metering.RelationshipSubset, n3, shared, false),
			),
			wantReport: true,
		},
		{
			name: "reproducer_shares_a_complete_descendant",
			rule: metering.SchemaTopologyCompleteSiblingSharedDescendant,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipAggregate, n1, n2, true),
				topologyEdge(metering.RelationshipPartition, n1, n3, false),
				topologyEdge(metering.RelationshipAggregate, n2, shared, false),
				topologyEdge(metering.RelationshipAggregate, n3, shared, false),
			),
			wantReport: true,
		},
		{
			name: "reproducer_shares_a_descendant_only_via_a_longer_branch",
			rule: metering.SchemaTopologyCompleteSiblingSharedDescendant,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipPartition, n1, n2, false),
				topologyEdge(metering.RelationshipPartition, n1, n3, false),
				topologyEdge(metering.RelationshipSubset, n2, leafL, false),
				topologyEdge(metering.RelationshipSubset, leafL, shared, false),
				topologyEdge(metering.RelationshipSubset, n3, farLeaf, false),
				topologyEdge(metering.RelationshipSubset, farLeaf, shared, false),
			),
			wantReport: true,
		},
		{
			name: "near_miss_nested_complete_partition_is_legal",
			rule: metering.SchemaTopologyCompleteSiblingSharedDescendant,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipPartition, n1, n2, false),
				topologyEdge(metering.RelationshipPartition, n1, n3, false),
				topologyEdge(metering.RelationshipPartition, n2, shared, false),
			),
			wantReport: false,
		},
		{
			name: "near_miss_subset_under_one_complete_branch_is_legal",
			rule: metering.SchemaTopologyCompleteSiblingSharedDescendant,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipPartition, n1, n2, false),
				topologyEdge(metering.RelationshipPartition, n1, n3, false),
				topologyEdge(metering.RelationshipSubset, n2, leafL, false),
				topologyEdge(metering.RelationshipSubset, n3, leafR, false),
			),
			wantReport: false,
		},
		{
			name: "near_miss_redundant_equivalent_paths_in_one_branch_are_legal",
			rule: metering.SchemaTopologyCompleteSiblingSharedDescendant,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipPartition, n1, n2, false),
				topologyEdge(metering.RelationshipPartition, n1, n3, false),
				topologyEdge(metering.RelationshipSubset, n2, shared, false),
				topologyEdge(metering.RelationshipSubset, shared, farLeaf, false),
				topologyEdge(metering.RelationshipSubset, n2, farLeaf, false),
			),
			wantReport: false,
		},
		{
			name: "near_miss_transform_convergence_is_legal",
			rule: metering.SchemaTopologyCompleteSiblingSharedDescendant,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipPartition, n1, n2, false),
				topologyEdge(metering.RelationshipPartition, n1, n3, false),
				topologyEdge(metering.RelationshipTransform, n2, shared, false),
				topologyEdge(metering.RelationshipTransform, n3, shared, false),
			),
			wantReport: false,
		},
		{
			name: "near_miss_shared_complete_ownership_across_non_sibling_parents_is_legal",
			rule: metering.SchemaTopologyCompleteSiblingSharedDescendant,
			schemas: topologySchema(
				topologyEdge(metering.RelationshipPartition, n1, n2, false),
				topologyEdge(metering.RelationshipPartition, otherParent, n3, false),
				topologyEdge(metering.RelationshipPartition, n2, shared, false),
				topologyEdge(metering.RelationshipPartition, n3, shared, false),
			),
			wantReport: false,
		},
	}
	assertTopologyCases(t, true, cases)
}

// TestSchemaTopologyRulesAreIndependent pins that the three rules do not shadow
// each other, so promoting or demoting one never silently changes another's
// verdict on the same graph.
func TestSchemaTopologyRulesAreIndependent(t *testing.T) {
	t.Parallel()
	n1 := topologyKey(metering.DirectionInput, "vendor:ind_parent")
	n2 := topologyKey(metering.DirectionInput, "vendor:ind_left")
	n3 := topologyKey(metering.DirectionInput, "vendor:ind_right")
	shared := topologyKey(metering.DirectionInput, "vendor:ind_shared")
	outSibling := topologyKey(metering.DirectionOutput, "vendor:ind_out")

	// A cross-direction complete sibling plus a containment connection: only
	// the direction rule and the containment rule may speak.
	mixed := topologySchema(
		topologyEdge(metering.RelationshipPartition, n1, n2, false),
		topologyEdge(metering.RelationshipPartition, n1, outSibling, false),
		topologyEdge(metering.RelationshipSubset, n2, outSibling, false),
	)
	mixedReport := metering.InspectComponentSchemaTopology(mixed)
	if !mixedReport.Has(metering.SchemaTopologyDirectionMismatch) {
		t.Fatalf("direction rule must report: %v", mixedReport.Findings)
	}
	if !mixedReport.Has(metering.SchemaTopologyCompleteSiblingContains) {
		t.Fatalf("containment rule must report: %v", mixedReport.Findings)
	}
	if mixedReport.Has(metering.SchemaTopologyCompleteSiblingSharedDescendant) {
		t.Fatalf("shared-descendant rule must stay silent: %v", mixedReport.Findings)
	}
	// Both complete-structure rules are enforced, so this graph is refused at
	// publication even though the direction rule alone would only report it.
	if err := metering.ValidateComponentSchemas(mixed); !errors.Is(err, metering.ErrComponentSchemaTopology) {
		t.Fatalf("graph with enforced complete-structure findings must be refused: %v", err)
	}
	// Narrowing the gate to the direction rule alone still refuses this graph,
	// because the graph genuinely trips the direction rule too. Independence is
	// shown the other way round: a graph whose ONLY finding is a complete-structure
	// one must be ACCEPTED by a direction-only gate, which is what proves the two
	// rules can be enabled and disabled separately.
	containmentOnly := topologySchema(
		topologyEdge(metering.RelationshipPartition, n1, n2, false),
		topologyEdge(metering.RelationshipPartition, n1, n3, false),
		topologyEdge(metering.RelationshipSubset, n2, n3, false),
	)
	containmentOnlyReport := metering.InspectComponentSchemaTopology(containmentOnly)
	if !containmentOnlyReport.Has(metering.SchemaTopologyCompleteSiblingContains) {
		t.Fatalf("containment rule must report: %v", containmentOnlyReport.Findings)
	}
	if containmentOnlyReport.Has(metering.SchemaTopologyDirectionMismatch) {
		t.Fatalf("direction rule must stay silent: %v", containmentOnlyReport.Findings)
	}
	if err := metering.ValidateComponentSchemasWithTopology(containmentOnly, metering.SchemaTopologyRuleSet{}.With(metering.SchemaTopologyDirectionMismatch)); err != nil {
		t.Fatalf("direction-only gate must accept a containment-only finding: %v", err)
	}
	if err := metering.ValidateComponentSchemas(containmentOnly); !errors.Is(err, metering.ErrComponentSchemaTopology) {
		t.Fatalf("default gate must refuse a containment-only finding: %v", err)
	}

	// A shared descendant reached by a cross-direction edge: the reachability is
	// real, so the shared-descendant rule reports even though the connecting
	// edges also trip the direction rule.
	sharedDescendant := topologySchema(
		topologyEdge(metering.RelationshipPartition, n1, n2, false),
		topologyEdge(metering.RelationshipPartition, n1, n3, false),
		topologyEdge(metering.RelationshipSubset, n2, shared, false),
		topologyEdge(metering.RelationshipSubset, n3, shared, false),
	)
	sharedReport := metering.InspectComponentSchemaTopology(sharedDescendant)
	if !sharedReport.Has(metering.SchemaTopologyCompleteSiblingSharedDescendant) {
		t.Fatalf("shared-descendant rule must report: %v", sharedReport.Findings)
	}
	if !errors.Is(metering.ValidateComponentSchemas(sharedDescendant), metering.ErrComponentSchemaTopology) {
		t.Fatal("an enforced shared-descendant finding must reject publication")
	}
}

// TestSchemaTopologyReportIsDeclarationOrderInvariant pins that a report is a
// property of the declared GRAPH, not of the order the edges were written in.
// A schema set is serialized and re-read, so a report that depended on
// declaration order could not be compared against a published snapshot.
func TestSchemaTopologyReportIsDeclarationOrderInvariant(t *testing.T) {
	t.Parallel()
	n1 := topologyKey(metering.DirectionInput, "schema:topology:v1:n1")
	n2 := topologyKey(metering.DirectionInput, "schema:topology:v1:n2")
	n3 := topologyKey(metering.DirectionInput, "schema:topology:v1:n3")
	shared := topologyKey(metering.DirectionInput, "schema:topology:v1:n4")
	rel := []metering.ComponentRelationship{
		topologyEdge(metering.RelationshipPartition, n1, n2, false),
		topologyEdge(metering.RelationshipPartition, n1, n3, false),
		topologyEdge(metering.RelationshipSubset, n2, shared, false),
		topologyEdge(metering.RelationshipSubset, n3, shared, false),
	}
	forward := metering.InspectComponentSchemaTopology(topologySchema(rel...))

	for seed := range 8 {
		reversed := make([]metering.ComponentRelationship, len(rel))
		for i := range rel {
			j := (i + seed) % len(rel)
			reversed[i] = rel[j]
		}
		got := metering.InspectComponentSchemaTopology(topologySchema(reversed...))
		if got.Len() != forward.Len() {
			t.Fatalf("rotation %d: finding count = %d, want %d", seed, got.Len(), forward.Len())
		}
		for i := range forward.Findings {
			if got.Findings[i] != forward.Findings[i] {
				t.Fatalf("rotation %d: finding[%d] = %+v, want %+v", seed, i, got.Findings[i], forward.Findings[i])
			}
		}
	}
}

// TestSchemaTopologyRuleSetIsImmutableValue pins the rule-set value semantics, so
// widening one caller's gate can never leak into another's.
func TestSchemaTopologyRuleSetIsImmutableValue(t *testing.T) {
	t.Parallel()
	widened := metering.SchemaTopologyRuleNone.With(metering.SchemaTopologyDirectionMismatch)
	if metering.SchemaTopologyRuleNone.Contains(metering.SchemaTopologyDirectionMismatch) {
		t.Fatal("With must not mutate the receiver")
	}
	if !widened.Contains(metering.SchemaTopologyDirectionMismatch) {
		t.Fatal("With must include the requested rule")
	}
	if got := len(widened.Rules()); got != 1 {
		t.Fatalf("widened rule count = %d, want 1", got)
	}
	if got := metering.SchemaTopologyAllRules.Rules(); len(got) != len(metering.AllSchemaTopologyRules()) {
		t.Fatalf("all-rule count = %d, want %d", len(got), len(metering.AllSchemaTopologyRules()))
	}
	if got := len(metering.SchemaTopologyRuleNone.Rules()); got != 0 {
		t.Fatalf("none rule count = %d, want 0", got)
	}
	// An unknown rule is never a member, so a typo cannot silently widen a gate.
	if metering.SchemaTopologyRule("not-a-rule").IsKnown() {
		t.Fatal("an unknown rule must not report as known")
	}
	if metering.SchemaTopologyAllRules.With("not-a-rule").Contains("not-a-rule") {
		t.Fatal("With must ignore an unknown rule")
	}
}

// TestSchemaTopologyPreservesLegacyPublicationIdentity pins the backward-
// compatibility promise at the level the review cares about: the structural
// analysis adds no new rejection to nil, empty or plainly legal schema sets, and
// a non-enforced rule never turns a previously published schema into an error.
func TestSchemaTopologyPreservesLegacyPublicationIdentity(t *testing.T) {
	t.Parallel()
	for _, schemas := range [][]metering.ComponentSchema{
		nil,
		{},
		{{ID: "schema:topology:empty", Version: "1"}},
	} {
		if err := metering.ValidateComponentSchemas(schemas); err != nil {
			t.Fatalf("legacy schema set %+v must stay valid: %v", schemas, err)
		}
		if got := metering.InspectComponentSchemaTopology(schemas).Len(); got != 0 {
			t.Fatalf("legacy schema set %+v reported %d findings, want 0", schemas, got)
		}
		if err := metering.ValidateComponentSchemasWithTopology(schemas, metering.SchemaTopologyAllRules); err != nil {
			t.Fatalf("legacy schema set %+v must survive the widest gate: %v", schemas, err)
		}
	}
}
