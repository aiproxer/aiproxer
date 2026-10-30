package reasoninge2e_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
)

// obsFromExpected builds a backend observation matching each planned turn's
// ExpectedBackend (eviction-blind). Used to prove retention-aware checks diverge.
func obsFromExpected(plan reasoninge2e.Plan, histLen int) reasoninge2e.BackendRequestObservation {
	turns := plan.Turns()
	out := make([]reasoninge2e.BackendTurnObservation, histLen)
	for i := range histLen {
		out[i] = reasoninge2e.BackendTurnObservation{
			TurnID:      turns[i].ID,
			VisibleText: turns[i].ExpectedBackend.VisibleText,
			Reasoning:   turns[i].ExpectedBackend.Reasoning,
			Tool:        turns[i].ExpectedBackend.Tool,
		}
	}
	return reasoninge2e.BackendRequestObservation{AssistantTurns: out}
}

func obsCustom(
	plan reasoninge2e.Plan,
	histLen int,
	reasoningByIndex map[int][]reasoninge2e.ReasoningBlock,
) reasoninge2e.BackendRequestObservation {
	turns := plan.Turns()
	out := make([]reasoninge2e.BackendTurnObservation, histLen)
	for i := range histLen {
		r := turns[i].ExpectedBackend.Reasoning
		if v, ok := reasoningByIndex[i]; ok {
			r = v
		}
		out[i] = reasoninge2e.BackendTurnObservation{
			TurnID:      turns[i].ID,
			VisibleText: turns[i].ExpectedBackend.VisibleText,
			Reasoning:   r,
			Tool:        turns[i].ExpectedBackend.Tool,
		}
	}
	return reasoninge2e.BackendRequestObservation{AssistantTurns: out}
}

func TestCheckPrefixRetention_bound2_droppedRestoredWhileRetained(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   101,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{VisibleText: "a", Reasoning: sampleReasoning("ra")},
			{VisibleText: "b", Reasoning: sampleReasoning("rb")},
			{VisibleText: "c", Reasoning: sampleReasoning("rc")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const bound = 2
	// Request turn 1: history=[A]; A is sole artifact → retained → restore.
	obs := obsFromExpected(plan, 1)
	if err := reasoninge2e.CheckPrefixRetention(plan, obs, bound); err != nil {
		t.Fatalf("retained dropped A must restore: %v", err)
	}
}

func TestCheckPrefixRetention_bound2_evictedDroppedExpectsAbsence(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   102,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{VisibleText: "a", Reasoning: sampleReasoning("ra")},
			{VisibleText: "b", Reasoning: sampleReasoning("rb")},
			{VisibleText: "c", Reasoning: sampleReasoning("rc")},
			{VisibleText: "d", Reasoning: sampleReasoning("rd")}, // next request turn for request_turn=3
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const bound = 2
	turns := plan.Turns()
	// Request turn 3: history=[A,B,C]; newest 2 artifacts are B,C → A evicted.
	// Eviction-blind ExpectedBackend still wants A's reasoning; that must FAIL retention check.
	blind := obsFromExpected(plan, 3)
	if err := reasoninge2e.CheckPrefix(plan, blind); err != nil {
		t.Fatalf("eviction-blind CheckPrefix should still expect A restore: %v", err)
	}
	if err := reasoninge2e.CheckPrefixRetention(plan, blind, bound); err == nil {
		t.Fatal("evicted dropped A with restored reasoning must fail retention oracle")
	} else {
		msg := err.Error()
		if !strings.Contains(msg, "artifact_state=evicted") {
			t.Fatalf("want artifact_state=evicted in %q", msg)
		}
		if !strings.Contains(msg, "request_turn=3") {
			t.Fatalf("want request_turn=3 in %q", msg)
		}
		if !strings.Contains(msg, "history_turn=0") {
			t.Fatalf("want history_turn=0 in %q", msg)
		}
		if !strings.Contains(msg, "history_turn_id="+turns[0].ID) {
			t.Fatalf("want history_turn_id=%s in %q", turns[0].ID, msg)
		}
		if !strings.Contains(msg, "request_turn_id="+turns[3].ID) {
			t.Fatalf("want request_turn_id=%s in %q", turns[3].ID, msg)
		}
		assertNoPayloadLeak(t, msg)
	}

	// Correct post-eviction observation: A absent, B+C restored.
	ok := obsCustom(plan, 3, map[int][]reasoninge2e.ReasoningBlock{
		0: nil,
	})
	if err := reasoninge2e.CheckPrefixRetention(plan, ok, bound); err != nil {
		t.Fatalf("A absent + B/C restored must pass: %v", err)
	}
}

func TestCheckPrefixRetention_bound2_retainedDroppedStillStrict(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   103,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{VisibleText: "a", Reasoning: sampleReasoning("ra")},
			{VisibleText: "b", Reasoning: sampleReasoning("rb")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const bound = 2
	turns := plan.Turns()

	t.Run("missing_restore_while_retained", func(t *testing.T) {
		t.Parallel()
		obs := obsCustom(plan, 2, map[int][]reasoninge2e.ReasoningBlock{0: nil})
		err := reasoninge2e.CheckPrefixRetention(plan, obs, bound)
		if err == nil {
			t.Fatal("missing restore for retained dropped turn must fail")
		}
		msg := err.Error()
		if !strings.Contains(msg, "restoration_incomplete") {
			t.Fatalf("want restoration_incomplete: %q", msg)
		}
		if !strings.Contains(msg, "artifact_state=retained") {
			t.Fatalf("want artifact_state=retained: %q", msg)
		}
		if !strings.Contains(msg, turns[0].ID) {
			t.Fatalf("want turn id: %q", msg)
		}
		assertNoPayloadLeak(t, msg)
	})

	t.Run("duplication_while_retained", func(t *testing.T) {
		t.Parallel()
		dup := append(sampleReasoning("ra"), sampleReasoning("extra")...)
		obs := obsCustom(plan, 2, map[int][]reasoninge2e.ReasoningBlock{0: dup})
		err := reasoninge2e.CheckPrefixRetention(plan, obs, bound)
		if err == nil {
			t.Fatal("duplication for retained dropped turn must fail")
		}
		if !strings.Contains(err.Error(), "reasoning_duplication") {
			t.Fatalf("want reasoning_duplication: %v", err)
		}
		assertNoPayloadLeak(t, err.Error())
	})

	t.Run("content_mismatch_while_retained", func(t *testing.T) {
		t.Parallel()
		obs := obsCustom(plan, 2, map[int][]reasoninge2e.ReasoningBlock{
			0: sampleReasoning("wrong"),
		})
		err := reasoninge2e.CheckPrefixRetention(plan, obs, bound)
		if err == nil {
			t.Fatal("content mismatch for retained dropped turn must fail")
		}
		if !strings.Contains(err.Error(), "restoration_content") {
			t.Fatalf("want restoration_content: %v", err)
		}
		assertNoPayloadLeak(t, err.Error())
	})
}

func TestCheckPrefixRetention_preservedSurvivesEviction(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   104,
		Policy: reasoninge2e.PreserveAllReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{VisibleText: "a", Reasoning: sampleReasoning("ra"), ClientMode: reasoninge2e.ModePreserved},
			{VisibleText: "b", Reasoning: sampleReasoning("rb"), ClientMode: reasoninge2e.ModeDropped},
			{VisibleText: "c", Reasoning: sampleReasoning("rc"), ClientMode: reasoninge2e.ModeDropped},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const bound = 2
	// After B+C push A out of FIFO, preserved A still carries client reasoning.
	obs := obsFromExpected(plan, 3)
	if err := reasoninge2e.CheckPrefixRetention(plan, obs, bound); err != nil {
		t.Fatalf("preserved A must still expect reasoning after eviction: %v", err)
	}
	// Absence of preserved A reasoning must fail even when artifact is evicted.
	missing := obsCustom(plan, 3, map[int][]reasoninge2e.ReasoningBlock{0: nil})
	err = reasoninge2e.CheckPrefixRetention(plan, missing, bound)
	if err == nil {
		t.Fatal("missing preserved reasoning must fail after eviction")
	}
	if !strings.Contains(err.Error(), "reasoning_count") && !strings.Contains(err.Error(), "preserved") {
		t.Fatalf("want preserved mismatch: %v", err)
	}
	assertNoPayloadLeak(t, err.Error())
}

func TestCheckPrefixRetention_noReasonTurnsDoNotConsumeBound(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   105,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{VisibleText: "a", Reasoning: sampleReasoning("ra")},
			{VisibleText: "plain"}, // ModeNone — must not consume artifact slot
			{VisibleText: "b", Reasoning: sampleReasoning("rb")},
			{VisibleText: "c", Reasoning: sampleReasoning("rc")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const bound = 2
	// Artifacts among history=[A,plain,B,C] are A,B,C; newest 2 are B,C → A evicted.
	// plain never counted.
	blind := obsFromExpected(plan, 4)
	if err := reasoninge2e.CheckPrefixRetention(plan, blind, bound); err == nil {
		t.Fatal("A must be evicted despite intervening no-reason turn")
	}
	ok := obsCustom(plan, 4, map[int][]reasoninge2e.ReasoningBlock{0: nil})
	if err := reasoninge2e.CheckPrefixRetention(plan, ok, bound); err != nil {
		t.Fatalf("A absent after eviction with no-reason gap must pass: %v", err)
	}
	// Before C arrives, history=[A,plain,B] has artifacts A,B only → both retained.
	early := obsFromExpected(plan, 3)
	if err := reasoninge2e.CheckPrefixRetention(plan, early, bound); err != nil {
		t.Fatalf("A+B retained before C: %v", err)
	}
}

func TestCheckPrefixRetention_rejectsNonPositiveBound(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   106,
		Policy: reasoninge2e.DropAllReasoning,
		Turns:  []reasoninge2e.TurnSpec{{VisibleText: "a", Reasoning: sampleReasoning("ra")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	obs := obsFromExpected(plan, 1)
	for _, bound := range []int{0, -1} {
		t.Run(fmt.Sprintf("bound_%d", bound), func(t *testing.T) {
			t.Parallel()
			err := reasoninge2e.CheckPrefixRetention(plan, obs, bound)
			if err == nil {
				t.Fatal("expected invalid_max_artifact_turns error")
			}
			msg := err.Error()
			if !strings.Contains(msg, "invalid_max_artifact_turns") {
				t.Fatalf("want invalid_max_artifact_turns: %q", msg)
			}
			if !strings.Contains(msg, "seed=106") {
				t.Fatalf("want seed: %q", msg)
			}
			assertNoPayloadLeak(t, msg)
		})
	}
}

func TestCheckPrefixRetention_conflictUnchangedByEviction(t *testing.T) {
	t.Parallel()
	alt := sampleReasoning("alt")
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   107,
		Policy: reasoninge2e.ConflictReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{VisibleText: "a", Reasoning: sampleReasoning("obs"), ConflictReasoning: alt},
			{VisibleText: "b", Reasoning: sampleReasoning("ob2"), ConflictReasoning: sampleReasoning("alt2")},
			{VisibleText: "c", Reasoning: sampleReasoning("ob3"), ConflictReasoning: sampleReasoning("alt3")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const bound = 2
	obs := obsFromExpected(plan, 3)
	if err := reasoninge2e.CheckPrefixRetention(plan, obs, bound); err != nil {
		t.Fatalf("conflict expectations must ignore eviction: %v", err)
	}
}
