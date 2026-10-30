package reasoninge2e_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
)

func sampleReasoning(text string) []reasoninge2e.ReasoningBlock {
	return []reasoninge2e.ReasoningBlock{{
		Dialect:   "openai.chat.reasoning_text.v1",
		Text:      text,
		Signature: "sig-" + text,
		Opaque:    []byte(`{"k":"` + text + `"}`),
	}}
}

func TestBuildPlan_preserveAll_keepsReasoning(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   42,
		Policy: reasoninge2e.PreserveAllReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{VisibleText: "a1", Reasoning: sampleReasoning("r1")},
			{VisibleText: "a2"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	turns := plan.Turns()
	if len(turns) != 2 {
		t.Fatalf("turns: %d", len(turns))
	}
	if turns[0].Mode != reasoninge2e.ModePreserved {
		t.Fatalf("mode0: %s", turns[0].Mode)
	}
	if turns[1].Mode != reasoninge2e.ModeNone {
		t.Fatalf("mode1: %s", turns[1].Mode)
	}
	if !reflect.DeepEqual(turns[0].Submitted.Reasoning, turns[0].Observed.Reasoning) {
		t.Fatal("preserve must keep observed reasoning in submitted")
	}
	if !reflect.DeepEqual(turns[0].ExpectedBackend.Reasoning, turns[0].Observed.Reasoning) {
		t.Fatal("preserve expected backend must equal observed once")
	}
	if len(turns[1].ExpectedBackend.Reasoning) != 0 {
		t.Fatal("no-reasoning turn must not insert reasoning on backend")
	}
}

func TestBuildPlan_dropAll_stripsSubmitted_expectsRestore(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   7,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{VisibleText: "a1", Reasoning: sampleReasoning("secret-think")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := plan.Turns()[0]
	if tr.Mode != reasoninge2e.ModeDropped {
		t.Fatalf("mode: %s", tr.Mode)
	}
	if len(tr.Submitted.Reasoning) != 0 {
		t.Fatal("drop must clear submitted reasoning")
	}
	if !reflect.DeepEqual(tr.ExpectedBackend.Reasoning, tr.Observed.Reasoning) {
		t.Fatal("drop expects exact restoration on backend")
	}
}

func TestBuildPlan_conflict_usesAlternateUntouched(t *testing.T) {
	t.Parallel()
	alt := sampleReasoning("client-alt")
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   3,
		Policy: reasoninge2e.ConflictReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{
				VisibleText:       "a1",
				Reasoning:         sampleReasoning("observed"),
				ConflictReasoning: alt,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := plan.Turns()[0]
	if tr.Mode != reasoninge2e.ModeConflict {
		t.Fatalf("mode: %s", tr.Mode)
	}
	if !reflect.DeepEqual(tr.Submitted.Reasoning, alt) {
		t.Fatal("conflict submitted must use alternate reasoning")
	}
	if !reflect.DeepEqual(tr.ExpectedBackend.Reasoning, alt) {
		t.Fatal("conflict expected backend must leave client reasoning untouched")
	}
	if reflect.DeepEqual(tr.ExpectedBackend.Reasoning, tr.Observed.Reasoning) {
		t.Fatal("conflict must not restore observed over client")
	}
}

func TestBuildPlan_seededPerTurn_deterministicAndVaries(t *testing.T) {
	t.Parallel()
	baseTurns := make([]reasoninge2e.TurnSpec, 0, 17)
	for range 16 {
		baseTurns = append(baseTurns, reasoninge2e.TurnSpec{
			VisibleText: "t",
			Reasoning:   sampleReasoning("r"),
		})
	}
	baseTurns = append(baseTurns, reasoninge2e.TurnSpec{VisibleText: "plain"})

	var mixSeed uint64
	var a reasoninge2e.Plan
	for seed := uint64(1); seed < 10_000; seed++ {
		plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
			Seed: seed, Policy: reasoninge2e.SeededPerTurnRetention, Turns: baseTurns,
		})
		if err != nil {
			t.Fatal(err)
		}
		var preserved, dropped int
		for _, tr := range plan.Turns() {
			if tr.Observed.VisibleText == "plain" {
				if tr.Mode != reasoninge2e.ModeNone {
					t.Fatalf("plain mode: %s", tr.Mode)
				}
				continue
			}
			switch tr.Mode {
			case reasoninge2e.ModePreserved:
				preserved++
			case reasoninge2e.ModeDropped:
				dropped++
			default:
				t.Fatalf("seeded mode %s", tr.Mode)
			}
		}
		if preserved > 0 && dropped > 0 {
			mixSeed = seed
			a = plan
			break
		}
	}
	if mixSeed == 0 {
		t.Fatal("could not find seeded mix within search window")
	}
	b, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed: mixSeed, Policy: reasoninge2e.SeededPerTurnRetention, Turns: baseTurns,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !samePlanModes(a, b) {
		t.Fatal("same seed must produce identical retention modes")
	}
	var differ bool
	for seed := mixSeed + 1; seed < mixSeed+10_000; seed++ {
		c, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
			Seed: seed, Policy: reasoninge2e.SeededPerTurnRetention, Turns: baseTurns,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !samePlanModes(a, c) {
			differ = true
			break
		}
	}
	if !differ {
		t.Fatal("different seeds must differ in seeded retention pattern")
	}
}

func TestBuildPlan_uniqueTurnIDs_andToolCopy(t *testing.T) {
	t.Parallel()
	opaque := []byte(`{"x":1}`)
	tool := &reasoninge2e.ToolExchange{
		ID: "call_1", Name: "get_weather", Arguments: `{"city":"NYC"}`, Result: `{"ok":true}`,
	}
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   11,
		Policy: reasoninge2e.PreserveAllReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "with-tool",
			Reasoning:   []reasoninge2e.ReasoningBlock{{Dialect: "d", Text: "t", Opaque: opaque}},
			Tool:        tool,
			Streaming:   true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := plan.Turns()[0]
	if tr.ID == "" || !strings.Contains(tr.ID, "11") {
		t.Fatalf("turn id: %q", tr.ID)
	}
	if !tr.Observed.Streaming {
		t.Fatal("streaming flag lost")
	}
	if tr.Observed.Tool == nil || tr.Observed.Tool.ID != "call_1" {
		t.Fatalf("tool: %+v", tr.Observed.Tool)
	}
	opaque[0] = 'Z'
	tool.ID = "mutated"
	if tr.Observed.Tool.ID != "call_1" {
		t.Fatal("tool must be defensively copied from TurnSpec")
	}
	if string(tr.Observed.Reasoning[0].Opaque) != `{"x":1}` {
		t.Fatal("opaque must be defensively copied from TurnSpec")
	}
}

func TestPlan_defensiveCopies_onAccessors(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   1,
		Policy: reasoninge2e.PreserveAllReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "v",
			Reasoning:   sampleReasoning("r"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	turns := plan.Turns()
	turns[0].Observed.VisibleText = "mut"
	turns[0].Observed.Reasoning[0].Text = "mut"
	turns[0].Observed.Reasoning[0].Opaque[0] = 'X'

	again := plan.Turns()
	if again[0].Observed.VisibleText != "v" {
		t.Fatal("Turns() must return defensive copies")
	}
	if again[0].Observed.Reasoning[0].Text != "r" {
		t.Fatal("reasoning text must not alias")
	}
	if again[0].Observed.Reasoning[0].Opaque[0] == 'X' {
		t.Fatal("opaque must not alias")
	}

	obs := plan.ObservedTranscript()
	obs[0].VisibleText = "x"
	if plan.ObservedTranscript()[0].VisibleText != "v" {
		t.Fatal("ObservedTranscript must defensive-copy")
	}
	sub := plan.SubmittedTranscript()
	sub[0].VisibleText = "y"
	if plan.SubmittedTranscript()[0].VisibleText != "v" {
		t.Fatal("SubmittedTranscript must defensive-copy")
	}
}

func TestBuildPlan_conflictRequiresAlternate(t *testing.T) {
	t.Parallel()
	_, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   1,
		Policy: reasoninge2e.ConflictReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "a",
			Reasoning:   sampleReasoning("r"),
		}},
	})
	if err == nil {
		t.Fatal("expected error when ConflictReasoning missing alternate")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "sig-") {
		t.Fatalf("error must not include payloads: %v", err)
	}
}

func samePlanModes(a, b reasoninge2e.Plan) bool {
	at, bt := a.Turns(), b.Turns()
	if len(at) != len(bt) {
		return false
	}
	for i := range at {
		if at[i].Mode != bt[i].Mode || at[i].ID != bt[i].ID {
			return false
		}
		if !reflect.DeepEqual(at[i].Submitted.Reasoning, bt[i].Submitted.Reasoning) {
			return false
		}
	}
	return true
}

func TestReasoningBlock_opaqueIndependence(t *testing.T) {
	t.Parallel()
	src := []byte(`{"a":1}`)
	blocks := []reasoninge2e.ReasoningBlock{{Opaque: src}}
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed: 2, Policy: reasoninge2e.PreserveAllReasoning,
		Turns: []reasoninge2e.TurnSpec{{VisibleText: "v", Reasoning: blocks}},
	})
	if err != nil {
		t.Fatal(err)
	}
	src[2] = '9'
	got := plan.Turns()[0].Observed.Reasoning[0].Opaque
	if bytes.Equal(got, src) || string(got) != `{"a":1}` {
		t.Fatalf("opaque alias: %q", got)
	}
}
