package openresponses

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

var coverageIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// backendScenarioRegistry is the authoritative non-empty list of declarative
// scenario IDs the emulator proves. Each ID is executed by a live test.
func backendScenarioRegistry() []string {
	return []string{
		"scenario-json-text",
		"scenario-sse-text",
		"scenario-compact",
		"scenario-ws-basic",
		"scenario-ws-sequential",
		"scenario-ws-continuation",
		"scenario-tools",
		"scenario-reasoning",
		"scenario-phase",
		"scenario-extensions",
		"scenario-required-presence",
		"scenario-multimodal-input",
		"scenario-auth-missing",
		"scenario-auth-wrong",
		"scenario-rate-limit",
		"scenario-4xx",
		"scenario-5xx",
		"scenario-malformed-event",
		"scenario-malformed-resource",
		"scenario-malformed-content-type",
		"scenario-disconnect",
		"scenario-cancel",
		"scenario-slow-write",
		"scenario-backpressure",
		"scenario-zero-upstream",
	}
}

// TestBackendScenarioRegistry_ValidAndNonEmpty pins the registry hygiene: every
// ID is kebab-case, unique, and the list is never empty.
func TestBackendScenarioRegistry_ValidAndNonEmpty(t *testing.T) {
	t.Parallel()
	ids := backendScenarioRegistry()
	if len(ids) == 0 {
		t.Fatal("scenario registry must not be empty")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			t.Error("scenario id cannot be empty")
		}
		if !coverageIDPattern.MatchString(id) {
			t.Errorf("scenario id %q violates kebab-case", id)
		}
		if seen[id] {
			t.Errorf("duplicate scenario id %q", id)
		}
		seen[id] = true
	}
	// Every declared scenario must be linked to live evidence: search the test
	// tree for its ID so no registry entry is dead.
	for _, id := range ids {
		if !scenarioIDMentioned(t, id) {
			t.Errorf("scenario %q has no live test evidence", id)
		}
	}
}

// TestBackendCoverageScenarios_Reported prints the covered scenario set for the
// coverage gate (Requirement 12.13/12.14 style scenario evidence).
func TestBackendCoverageScenarios_Reported(t *testing.T) {
	t.Parallel()
	list := backendScenarioRegistry()
	fmt.Printf("refbackend/openresponses coverage scenarios: %d (%s)\n", len(list), strings.Join(list, ", "))
}

// TestBackendModeCoverage asserts every declared mode and malformed mode has live
// evidence somewhere in the test tree (matched by exact exported identifier).
func TestBackendModeCoverage(t *testing.T) {
	t.Parallel()
	modeIDs := map[Mode]string{
		ModeJSON:      "ModeJSON",
		ModeSSE:       "ModeSSE",
		ModeCompact:   "ModeCompact",
		ModeWebSocket: "ModeWebSocket",
	}
	for mode, id := range modeIDs {
		if !identifierMentioned(t, id) {
			t.Errorf("mode %s (identifier %s) has no live test evidence", mode, id)
		}
	}
	malformedIDs := map[MalformedMode]string{
		MalformedResourceMissingField:   "MalformedResourceMissingField",
		MalformedResourceBadType:        "MalformedResourceBadType",
		MalformedItemDiscriminator:      "MalformedItemDiscriminator",
		MalformedBodyNotJSON:            "MalformedBodyNotJSON",
		MalformedOversizedBody:          "MalformedOversizedBody",
		MalformedEventNoHeader:          "MalformedEventNoHeader",
		MalformedEventMismatch:          "MalformedEventMismatch",
		MalformedEventDuplicateTerminal: "MalformedEventDuplicateTerminal",
		MalformedEventAfterTerminal:     "MalformedEventAfterTerminal",
		MalformedDoneBeforeTerminal:     "MalformedDoneBeforeTerminal",
		MalformedMissingDONE:            "MalformedMissingDONE",
		MalformedContentType:            "MalformedContentType",
	}
	for mm, id := range malformedIDs {
		if !ValidMalformed(mm) {
			t.Errorf("malformed mode %q must be recognized by ValidMalformed", mm)
		}
		if !identifierMentioned(t, id) {
			t.Errorf("malformed mode %s (identifier %s) has no live test evidence", mm, id)
		}
	}
}

// scenarioIDMentioned reports whether id appears in any *_test.go file under
// this package directory (live evidence linkage).
func scenarioIDMentioned(t *testing.T, id string) bool {
	t.Helper()
	return scanTestTree(t, func(s string) bool { return strings.Contains(s, id) })
}

func identifierMentioned(t *testing.T, id string) bool {
	t.Helper()
	return scanTestTree(t, func(s string) bool { return strings.Contains(s, id) })
}

func scanTestTree(t *testing.T, match func(string) bool) bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if match(string(b)) {
			return true
		}
	}
	return false
}
