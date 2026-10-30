package semantic_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestBaselineScenarioCorpus(t *testing.T) {
	t.Parallel()

	scenarios := semantic.BaselineScenarioCorpus()
	if len(scenarios) == 0 {
		t.Fatalf("expected non-empty baseline scenario corpus")
	}

	seen := make(map[semantic.ScenarioID]bool)
	for _, sc := range scenarios {
		if sc.ID == "" {
			t.Fatalf("scenario descriptor has empty ID")
		}
		if seen[sc.ID] {
			t.Fatalf("duplicate scenario ID %q", sc.ID)
		}
		seen[sc.ID] = true
	}
}

func TestSelectScenariosForSubject_Frontend(t *testing.T) {
	t.Parallel()

	corpus := semantic.BaselineScenarioCorpus()
	feSubject := semantic.SubjectDescriptor{
		ID:   "fe-openresponses",
		Kind: semantic.KindFrontend,
		Capabilities: []aipapi.Capability{
			aipapi.CapabilityStreaming,
			aipapi.CapabilityTools,
		},
	}

	positive, negative := semantic.SelectScenariosForSubject(feSubject, corpus)

	if !slices.Contains(positive, "text-baseline") {
		t.Fatalf("expected text-baseline in positive scenarios")
	}
	if !slices.Contains(positive, "text-streaming") {
		t.Fatalf("expected text-streaming in positive scenarios")
	}
	if !slices.Contains(positive, "tools-execution") {
		t.Fatalf("expected tools-execution in positive scenarios")
	}

	// Terminal-semantics promotion: a streaming+tools frontend selects every
	// terminal scenario positive.
	for _, id := range []semantic.ScenarioID{"terminal-clean-text", "terminal-clean-tool-call", "terminal-output-limit", "terminal-upstream-error", "terminal-usage-reported", "terminal-content-filter", "terminal-empty-turn", "terminal-error-taxonomy"} {
		if !slices.Contains(positive, id) {
			t.Fatalf("expected %q in positive scenarios", id)
		}
	}

	// vision-input requires Vision capability, which feSubject lacks -> must be negative
	if !slices.Contains(negative, "vision-input") {
		t.Fatalf("expected vision-input in negative scenarios for subject lacking Vision capability")
	}
}

func TestSelectScenariosForSubject_BackendFamily(t *testing.T) {
	t.Parallel()

	corpus := semantic.BaselineScenarioCorpus()
	beSubject := semantic.SubjectDescriptor{
		ID:   "openai-responses-compatible",
		Kind: semantic.KindBackendFamily,
		Capabilities: []aipapi.Capability{
			aipapi.CapabilityStreaming,
			aipapi.CapabilityTools,
			aipapi.CapabilityVision,
			aipapi.CapabilityStructuredOutputs,
		},
	}

	positive, negative := semantic.SelectScenariosForSubject(beSubject, corpus)

	if !slices.Contains(positive, "vision-input") {
		t.Fatalf("expected vision-input in positive scenarios for Vision-capable backend family")
	}
	if !slices.Contains(positive, "structured-output") {
		t.Fatalf("expected structured-output in positive scenarios")
	}

	// reasoning-output requires Reasoning capability -> must be negative
	if !slices.Contains(negative, "reasoning-output") {
		t.Fatalf("expected reasoning-output in negative scenarios")
	}

	// Terminal-semantics promotion: every terminal scenario is positive for a
	// streaming backend family (tools declared, so the tool-call row applies).
	for _, id := range []semantic.ScenarioID{"terminal-clean-text", "terminal-clean-tool-call", "terminal-output-limit", "terminal-upstream-error", "terminal-usage-reported", "terminal-content-filter", "terminal-empty-turn", "terminal-error-taxonomy"} {
		if !slices.Contains(positive, id) {
			t.Fatalf("expected %q in positive scenarios", id)
		}
	}
}

func TestSelectScenariosForSubject_ConnectorAndProfile(t *testing.T) {
	t.Parallel()

	corpus := semantic.BaselineScenarioCorpus()

	connSubject := semantic.SubjectDescriptor{
		ID:           "acp-connector",
		Kind:         semantic.KindConnector,
		Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming},
	}
	posConn, negConn := semantic.SelectScenariosForSubject(connSubject, corpus)
	if !slices.Contains(posConn, "text-streaming") {
		t.Fatalf("expected text-streaming in positive connector scenarios")
	}
	if len(negConn) == 0 {
		t.Fatalf("expected non-empty negative connector scenarios")
	}
	// Terminal-semantics promotion: a streaming-only connector selects the
	// capability-free terminal rows positive and the tools row negative.
	for _, id := range []semantic.ScenarioID{"terminal-clean-text", "terminal-output-limit", "terminal-upstream-error", "terminal-usage-reported", "terminal-content-filter", "terminal-empty-turn", "terminal-error-taxonomy"} {
		if !slices.Contains(posConn, id) {
			t.Fatalf("expected %q in positive connector scenarios", id)
		}
	}
	if !slices.Contains(negConn, "terminal-clean-tool-call") {
		t.Fatalf("expected terminal-clean-tool-call in negative connector scenarios without Tools capability")
	}
	// A scenario the subject cannot satisfy is still listed exactly once, in
	// the negative set rather than dropped.
	if len(posConn)+len(negConn) != len(corpus) {
		t.Fatalf("selection covers %d of %d corpus scenarios", len(posConn)+len(negConn), len(corpus))
	}

	profSubject := semantic.SubjectDescriptor{
		ID:           "provider-profile-0001",
		Kind:         semantic.KindProviderProfile,
		Capabilities: []aipapi.Capability{},
	}
	posProf, negProf := semantic.SelectScenariosForSubject(profSubject, corpus)
	if !slices.Contains(posProf, "text-baseline") {
		t.Fatalf("expected text-baseline in positive profile scenarios")
	}
	if !slices.Contains(negProf, "text-streaming") {
		t.Fatalf("expected text-streaming in negative profile scenarios when subject lacks Streaming capability")
	}
}

func TestSelectScenariosForSubject_DialectsAndExtensions(t *testing.T) {
	t.Parallel()

	corpus := semantic.BaselineScenarioCorpus()

	// Subject WITH item_reference dialect and extension
	subjectWithDialect := semantic.SubjectDescriptor{
		ID:           "backend-openresponses",
		Kind:         semantic.KindBackendFamily,
		Capabilities: []aipapi.Capability{aipapi.CapabilityItemReferences},
		Dialects: aipapi.DialectSupport{
			ItemDialects:   []aipapi.DialectRequirement{{Kind: "item", Dialect: "item_reference"}},
			ExtensionTypes: []aipapi.ExtensionRequirement{{Namespace: "com.example", Type: "custom"}},
		},
	}

	pos, neg := semantic.SelectScenariosForSubject(subjectWithDialect, corpus)

	if !slices.Contains(pos, "item-reference-dialect") {
		t.Fatalf("expected item-reference-dialect in positive scenarios for subject declaring item_reference dialect")
	}
	if !slices.Contains(pos, "opaque-extension-type") {
		t.Fatalf("expected opaque-extension-type in positive scenarios for subject declaring com.example/custom extension")
	}
	if !slices.Contains(neg, "reasoning-replay-dialect") {
		t.Fatalf("expected reasoning-replay-dialect in negative scenarios for subject lacking reasoning_replay dialect")
	}

	// Subject WITHOUT item_reference dialect
	subjectNoDialect := semantic.SubjectDescriptor{
		ID:   "backend-openai-legacy",
		Kind: semantic.KindBackendFamily,
	}

	posNo, negNo := semantic.SelectScenariosForSubject(subjectNoDialect, corpus)
	if !slices.Contains(negNo, "item-reference-dialect") {
		t.Fatalf("expected item-reference-dialect in negative scenarios for subject lacking item_reference dialect")
	}
	if slices.Contains(posNo, "item-reference-dialect") {
		t.Fatalf("unexpected item-reference-dialect in positive scenarios")
	}
}

func TestCertification_ValidateReleaseReady(t *testing.T) {
	t.Parallel()

	corpus := semantic.BaselineScenarioCorpus()
	subject := semantic.SubjectDescriptor{
		ID:           "sub-1",
		Kind:         semantic.KindBackendFamily,
		Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming},
	}
	expectedPos, expectedNeg := semantic.SelectScenariosForSubject(subject, corpus)

	valid := semantic.Certification{
		SubjectID:           subject.ID,
		SubjectKind:         subject.Kind,
		Capabilities:        subject.Capabilities,
		Passed:              expectedPos,
		Negative:            expectedNeg,
		Executed:            true,
		ExecutedScenarioIDs: append(append([]semantic.ScenarioID(nil), expectedPos...), expectedNeg...),
		HarnessCalls:        1,
	}

	if err := valid.ValidateReleaseReady(); err != nil {
		t.Fatalf("unexpected error for valid certification: %v", err)
	}

	rawJSON, err := valid.MarshalJSON()
	if err != nil {
		t.Fatalf("failed to marshal certification: %v", err)
	}
	if len(rawJSON) == 0 {
		t.Fatalf("empty JSON serialization")
	}

	var unmarshaled semantic.Certification
	if err := json.Unmarshal(rawJSON, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal certification JSON: %v", err)
	}
	if unmarshaled.SubjectID != "sub-1" || len(unmarshaled.Passed) != len(expectedPos) || len(unmarshaled.Negative) != len(expectedNeg) {
		t.Fatalf("unmarshaled certification mismatch: %+v", unmarshaled)
	}

	// Test Mutations
	mutations := []struct {
		name   string
		mutate func(c *semantic.Certification)
	}{
		{
			name: "Empty SubjectID",
			mutate: func(c *semantic.Certification) {
				c.SubjectID = ""
			},
		},
		{
			name: "Invalid SubjectKind",
			mutate: func(c *semantic.Certification) {
				c.SubjectKind = "invalid_kind"
			},
		},
		{
			name: "Unknown Scenario in Passed",
			mutate: func(c *semantic.Certification) {
				c.Passed = append(c.Passed, "unknown-scenario-xyz")
			},
		},
		{
			name: "Unknown Scenario in Negative",
			mutate: func(c *semantic.Certification) {
				c.Negative = append(c.Negative, "unknown-scenario-xyz")
			},
		},
		{
			name: "Duplicate Scenario in Passed",
			mutate: func(c *semantic.Certification) {
				if len(c.Passed) > 0 {
					c.Passed = append(c.Passed, c.Passed[0])
				}
			},
		},
		{
			name: "Duplicate Scenario in Negative",
			mutate: func(c *semantic.Certification) {
				if len(c.Negative) > 0 {
					c.Negative = append(c.Negative, c.Negative[0])
				}
			},
		},
		{
			name: "Scenario Overlap Between Passed and Negative",
			mutate: func(c *semantic.Certification) {
				if len(c.Passed) > 0 {
					c.Negative = append(c.Negative, c.Passed[0])
				}
			},
		},
		{
			name: "Missing Positive Scenario",
			mutate: func(c *semantic.Certification) {
				if len(c.Passed) > 0 {
					c.Passed = c.Passed[1:]
				}
			},
		},
		{
			name: "Missing Negative Scenario",
			mutate: func(c *semantic.Certification) {
				if len(c.Negative) > 0 {
					c.Negative = c.Negative[1:]
				}
			},
		},
		{
			name: "Failed Scenarios Present",
			mutate: func(c *semantic.Certification) {
				c.Failed = []semantic.ScenarioFailure{
					{ScenarioID: "tools-execution", Reason: "unhandled tool call"},
				}
			},
		},
	}

	for _, m := range mutations {
		mutated := valid
		// Make deep copy of slices
		mutated.Passed = append([]semantic.ScenarioID(nil), valid.Passed...)
		mutated.Negative = append([]semantic.ScenarioID(nil), valid.Negative...)
		mutated.Failed = append([]semantic.ScenarioFailure(nil), valid.Failed...)
		m.mutate(&mutated)

		if err := mutated.ValidateReleaseReady(); err == nil {
			t.Fatalf("expected mutation %q to fail ValidateReleaseReady, but it passed", m.name)
		}
	}
}
