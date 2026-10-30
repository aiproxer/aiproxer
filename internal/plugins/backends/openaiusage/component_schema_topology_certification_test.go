package openaiusage_test

// Certification of the shipped OpenAI Chat/Responses provider-family inclusion
// schema against the strengthened component-schema topology validators.
//
// The review's claim is that the three structurally-knowable impossibilities are
// not OpenAI-specific: any schema whose accounting is driven by declared edges
// rather than by a component-name table must pass them for free. This file is
// the executable form of that claim. It:
//
//  1. validates the topology under the WIDEST possible gate, every rule at
//     once including the two that are deliberately report-only, so the claim is
//     not an artefact of which rules happen to be enforced today;
//  2. proves the outcome is component-name blind, by rebuilding an isomorphic
//     copy in which every component name is replaced by an opaque vendor name
//     and showing that topology inspection and publication are unchanged.
//
// The real-tariff publication/compilation and real-rater money halves of the
// original vectors (BuildTariffSnapshotWithSchemas publication of the actual
// frozen schema, NewReferenceRater compilation, intact 6-measure observation
// rated complete at exactly 74/0, and the renamed copy reproducing the same
// 74/0) are preserved by task 3.5 in the Enterprise tree:
// enterprise/internal/preservation/openai_topology_test.go.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaiusage"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// certNameBlindCopy rebuilds the production schema with every component name
// replaced by an opaque vendor name, preserving direction, unit, schema
// identity, kind and optionality exactly. The mapping is keyed on the full
// canonical key and assigned in sorted order, so it is deterministic and it
// keeps the two distinct cache-write identities (native detail and default
// inclusion) as two distinct names.
func certNameBlindCopy(t *testing.T) ([]sdkmetering.ComponentSchema, map[string]sdkmetering.ComponentKey) {
	t.Helper()
	production := openaiusage.NativeUsageInclusionSchemas()
	unique := make(map[string]sdkmetering.ComponentKey)
	for _, schema := range production {
		for _, relationship := range schema.Relationships {
			unique[relationship.Parent.CanonicalKey()] = relationship.Parent
			unique[relationship.Child.CanonicalKey()] = relationship.Child
		}
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	renamed := make(map[string]sdkmetering.ComponentKey, len(keys))
	for i, key := range keys {
		original := unique[key]
		blind := original
		blind.Component = fmt.Sprintf("vendor:cert:c%02d", i+1)
		renamed[key] = blind
	}

	out := make([]sdkmetering.ComponentSchema, 0, len(production))
	for _, schema := range production {
		clone := schema.Clone()
		for i := range clone.Relationships {
			parent := renamed[clone.Relationships[i].Parent.CanonicalKey()]
			child := renamed[clone.Relationships[i].Child.CanonicalKey()]
			clone.Relationships[i].Parent = parent
			clone.Relationships[i].Child = child
		}
		out = append(out, clone)
	}
	return out, renamed
}

func certAssertStructurePreserved(t *testing.T, production, blind []sdkmetering.ComponentSchema) {
	t.Helper()
	if len(production) != len(blind) {
		t.Fatalf("schema count = %d, want %d", len(blind), len(production))
	}
	for s := range production {
		if production[s].ID != blind[s].ID || production[s].Version != blind[s].Version {
			t.Fatalf("schema identity changed: %+v vs %+v", blind[s], production[s])
		}
		if len(production[s].Relationships) != len(blind[s].Relationships) {
			t.Fatalf("relationship count = %d, want %d", len(blind[s].Relationships), len(production[s].Relationships))
		}
		for r := range production[s].Relationships {
			got, want := blind[s].Relationships[r], production[s].Relationships[r]
			if got.Kind != want.Kind || got.Optional != want.Optional {
				t.Fatalf("edge %d kind/optional changed: %+v vs %+v", r, got, want)
			}
			if got.Parent.Direction != want.Parent.Direction || got.Child.Direction != want.Child.Direction {
				t.Fatalf("edge %d direction changed: %+v vs %+v", r, got, want)
			}
			if got.Parent.Unit != want.Parent.Unit || got.Child.Unit != want.Child.Unit {
				t.Fatalf("edge %d unit changed: %+v vs %+v", r, got, want)
			}
			if got.Parent.SchemaID != want.Parent.SchemaID || got.Child.SchemaID != want.Child.SchemaID {
				t.Fatalf("edge %d schema identity changed: %+v vs %+v", r, got, want)
			}
		}
	}
}

// TestNativeUsageInclusionSchemaPassesStrengthenedTopologyGate is the
// certification itself: the shipped production schema is validated under the
// strengthened validator, with no finding under ANY rule and a clean default
// publication gate.
func TestNativeUsageInclusionSchemaPassesStrengthenedTopologyGate(t *testing.T) {
	t.Parallel()
	schemas := openaiusage.NativeUsageInclusionSchemas()

	// Topology, under the widest gate this package can express: all three
	// rules at once, including the two that are report-only today. A schema that
	// only survives the current default is not certified.
	report := sdkmetering.InspectComponentSchemaTopology(schemas)
	if report.Len() != 0 {
		t.Fatalf("shipped production schema reported %d topology finding(s), want 0: %v", report.Len(), report.Findings)
	}
	if got := report.Rules(); len(got) != 0 {
		t.Fatalf("shipped production schema reported rules %v, want none", got)
	}
	if err := sdkmetering.ValidateComponentSchemasWithTopology(schemas, sdkmetering.SchemaTopologyAllRules); err != nil {
		t.Fatalf("shipped production schema must pass the widest topology gate: %v", err)
	}
	if err := sdkmetering.ValidateComponentSchemas(schemas); err != nil {
		t.Fatalf("shipped production schema must pass the default publication gate: %v", err)
	}
}

// TestNativeUsageInclusionSchemaTopologyIsComponentNameBlind proves the
// certification is not an artefact of the SDK's built-in component vocabulary.
//
// The same topology is republished with every component name replaced by an
// opaque vendor name. If topology inspection or publication branched on a
// component NAME, the renamed copy could not reproduce the production verdict;
// it must reproduce it exactly.
func TestNativeUsageInclusionSchemaTopologyIsComponentNameBlind(t *testing.T) {
	t.Parallel()
	production := openaiusage.NativeUsageInclusionSchemas()
	blind, _ := certNameBlindCopy(t)
	certAssertStructurePreserved(t, production, blind)

	// The renamed copy must contain no component name the SDK recognises, so
	// this really is a name-blind variant rather than a partial rename.
	for _, schema := range blind {
		for _, relationship := range schema.Relationships {
			for _, key := range []sdkmetering.ComponentKey{relationship.Parent, relationship.Child} {
				if !strings.HasPrefix(key.Component, "vendor:cert:") {
					t.Fatalf("component %q was not replaced by an opaque vendor name", key.Component)
				}
			}
		}
	}

	// Publication under the widest gate is identical.
	blindReport := sdkmetering.InspectComponentSchemaTopology(blind)
	productionReport := sdkmetering.InspectComponentSchemaTopology(production)
	if blindReport.Len() != productionReport.Len() {
		t.Fatalf("renamed schema reported %d finding(s), production reported %d; the analysis is not name-blind",
			blindReport.Len(), productionReport.Len())
	}
	if err := sdkmetering.ValidateComponentSchemasWithTopology(blind, sdkmetering.SchemaTopologyAllRules); err != nil {
		t.Fatalf("renamed isomorphic schema must pass the widest topology gate: %v", err)
	}
}

// TestNativeUsageInclusionSchemaRejectedByStrengthenedGateIsTyped pins the
// negative control: the certification above is not vacuous. A copy of the
// production schema with one edge turned into a shared containment descendant
// between two complete siblings is refused by the enforced rule with the typed
// topology sentinel, at publication time, before any rater runs.
func TestNativeUsageInclusionSchemaRejectedByStrengthenedGateIsTyped(t *testing.T) {
	t.Parallel()
	schemas := openaiusage.NativeUsageInclusionSchemas()
	// The two input partition children (text and audio) both gain a containment
	// path to one new descendant. Everything else, including every name, is the
	// production declaration.
	descendant := wireComponentKey(sdkmetering.DirectionInput, "vendor:cert:shared_descendant")
	broken := schemas[0].Clone()
	broken.Relationships = append(
		broken.Relationships,
		sdkmetering.ComponentRelationship{
			Kind: sdkmetering.RelationshipSubset, Parent: broken.Relationships[0].Child, Child: descendant,
		},
		sdkmetering.ComponentRelationship{
			Kind: sdkmetering.RelationshipSubset, Parent: broken.Relationships[1].Child, Child: descendant,
		},
	)
	brokenSet := []sdkmetering.ComponentSchema{broken}

	report := sdkmetering.InspectComponentSchemaTopology(brokenSet)
	if !report.Has(sdkmetering.SchemaTopologyCompleteSiblingSharedDescendant) {
		t.Fatalf("the negative control must trip the shared-descendant rule: %v", report.Findings)
	}
	err := sdkmetering.ValidateComponentSchemas(brokenSet)
	if !errors.Is(err, sdkmetering.ErrComponentSchemaTopology) {
		t.Fatalf("negative control error = %v, want ErrComponentSchemaTopology", err)
	}
	if !errors.Is(err, sdkmetering.ErrInvalidComponentSchema) {
		t.Fatalf("negative control error = %v, want it to also wrap ErrInvalidComponentSchema", err)
	}
	// The production schema it was derived from must remain clean, so the
	// rejection above is caused by the added edges and nothing else.
	if got := sdkmetering.InspectComponentSchemaTopology(schemas).Len(); got != 0 {
		t.Fatalf("production schema reported %d finding(s), want 0", got)
	}
}
