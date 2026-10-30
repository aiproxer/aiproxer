package openresponses

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

var scenarioIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// allScenarioCases is the single authoritative scenario registry.
func allScenarioCases() []scenarioCase {
	out := responseScenarioCases()
	out = append(out, sseScenarioCases()...)
	out = append(out, wsScenarioCases()...)
	out = append(out, clientScenarioCases()...)
	out = append(out, compactScenarioCases()...)
	return out
}

// TestExecuteAllScenarioCases executes every declared scenario case, so each
// declarative scenario ID has live evidence.
func TestExecuteAllScenarioCases(t *testing.T) {
	t.Parallel()
	all := allScenarioCases()
	for _, tc := range all {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			if tc.run != nil {
				tc.run(t)
				return
			}
			if tc.parse != nil {
				data := mustReadScenario(t, tc.fixture)
				tc.parse(t, data)
				return
			}
			t.Fatalf("scenario %s has no execution", tc.id)
		})
	}
}

// TestScenarioRegistry_DeclarativeIDs pins every scenario ID used across the client
// emulator tests to the kebab-case declarative registry. Each ID must be unique.
func TestScenarioRegistry_DeclarativeIDs(t *testing.T) {
	t.Parallel()
	registered := map[string]ScenarioDescriptor{}
	register := func(sd ScenarioDescriptor) {
		if err := sd.Validate(); err != nil {
			t.Errorf("invalid scenario %q: %v", sd.ID, err)
			return
		}
		if _, dup := registered[sd.ID]; dup {
			t.Errorf("duplicate scenario ID %q", sd.ID)
			return
		}
		registered[sd.ID] = sd
	}

	for _, tc := range allScenarioCases() {
		register(ScenarioDescriptor{ID: tc.id, Kind: tc.kind, Description: tc.description})
	}

	if len(registered) < 12 {
		t.Fatalf("scenario registry too small: %d", len(registered))
	}
	for id := range registered {
		if !scenarioIDPattern.MatchString(id) {
			t.Errorf("scenario ID %q violates kebab-case", id)
		}
	}
}

// TestEveryScenarioFixture_Linked verifies every scenario fixture under testdata/scenarios
// is referenced by at least one declared scenario case (no orphan fixtures, no empty state).
func TestEveryScenarioFixture_Linked(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir("testdata/scenarios")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no scenario fixtures present")
	}
	used := map[string]bool{}
	for _, tc := range allScenarioCases() {
		if tc.fixture != "" {
			used[tc.fixture] = true
		}
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") && !strings.HasSuffix(e.Name(), ".sse") {
			continue
		}
		if !used[e.Name()] {
			t.Errorf("scenario fixture %s not linked to any scenario case", e.Name())
		}
	}
}

// TestScenarioIDsNeverEmpty asserts descriptions and IDs are never empty.
func TestScenarioIDsNeverEmpty(t *testing.T) {
	t.Parallel()
	for _, tc := range allScenarioCases() {
		if strings.TrimSpace(tc.id) == "" {
			t.Error("scenario with empty id")
		}
		if strings.TrimSpace(tc.description) == "" {
			t.Errorf("scenario %s has empty description", tc.id)
		}
		if tc.parse == nil && tc.run == nil {
			t.Errorf("scenario %s has no execution", tc.id)
		}
	}
}

// coverageScenarios is the authoritative non-empty list used by the coverage gate.
func coverageScenarios() []string {
	out := make([]string, 0, 48)
	for _, tc := range allScenarioCases() {
		out = append(out, tc.id)
	}
	return out
}

func TestCoverageScenarios_Reported(t *testing.T) {
	t.Parallel()
	list := coverageScenarios()
	if len(list) == 0 {
		t.Fatal("coverage scenario list must not be empty")
	}
	fmt.Printf("refclient/openresponses coverage scenarios: %d (%s)\n", len(list), strings.Join(list, ", "))
}
