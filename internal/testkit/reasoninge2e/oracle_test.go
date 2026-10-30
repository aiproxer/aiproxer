package reasoninge2e_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
)

func TestOracle_positive_restoreDropPreserveNoneConflict(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   5,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{
				VisibleText: "drop-me",
				Reasoning:   sampleReasoning("hidden"),
				Tool: &reasoninge2e.ToolExchange{
					ID: "call_a", Name: "lookup", Arguments: `{"q":1}`, Result: `{"v":2}`,
				},
			},
			{VisibleText: "plain"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	obs := reasoninge2e.BackendRequestObservation{
		AssistantTurns: []reasoninge2e.BackendTurnObservation{
			{
				TurnID:      plan.Turns()[0].ID,
				VisibleText: "drop-me",
				Reasoning:   sampleReasoning("hidden"),
				Tool: &reasoninge2e.ToolExchange{
					ID: "call_a", Name: "lookup", Arguments: `{"q":1}`, Result: `{"v":2}`,
				},
			},
			{TurnID: plan.Turns()[1].ID, VisibleText: "plain"},
		},
	}
	if err := reasoninge2e.Check(plan, obs); err != nil {
		t.Fatalf("positive check: %v", err)
	}
}

func TestOracle_positive_preserveNoDuplication(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   6,
		Policy: reasoninge2e.PreserveAllReasoning,
		Turns:  []reasoninge2e.TurnSpec{{VisibleText: "p", Reasoning: sampleReasoning("keep")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := plan.Turns()[0]
	obs := reasoninge2e.BackendRequestObservation{
		AssistantTurns: []reasoninge2e.BackendTurnObservation{{
			TurnID: tr.ID, VisibleText: "p", Reasoning: sampleReasoning("keep"),
		}},
	}
	if err := reasoninge2e.Check(plan, obs); err != nil {
		t.Fatal(err)
	}
}

func TestOracle_positive_conflictUntouched(t *testing.T) {
	t.Parallel()
	alt := sampleReasoning("alt")
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   8,
		Policy: reasoninge2e.ConflictReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "c", Reasoning: sampleReasoning("obs"), ConflictReasoning: alt,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := plan.Turns()[0]
	obs := reasoninge2e.BackendRequestObservation{
		AssistantTurns: []reasoninge2e.BackendTurnObservation{{
			TurnID: tr.ID, VisibleText: "c", Reasoning: alt,
		}},
	}
	if err := reasoninge2e.Check(plan, obs); err != nil {
		t.Fatal(err)
	}
}

func TestOracle_fail_missingRestoration(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   9,
		Policy: reasoninge2e.DropAllReasoning,
		Turns:  []reasoninge2e.TurnSpec{{VisibleText: "d", Reasoning: sampleReasoning("need-restore")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := plan.Turns()[0]
	err = reasoninge2e.Check(plan, reasoninge2e.BackendRequestObservation{
		AssistantTurns: []reasoninge2e.BackendTurnObservation{{
			TurnID: tr.ID, VisibleText: "d",
		}},
	})
	if err == nil {
		t.Fatal("expected missing restoration error")
	}
	assertSafeOracleErr(t, err, plan.Seed, tr.ID, string(tr.Mode))
}

func TestOracle_fail_insertionOnNone(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   10,
		Policy: reasoninge2e.PreserveAllReasoning,
		Turns:  []reasoninge2e.TurnSpec{{VisibleText: "plain"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := plan.Turns()[0]
	err = reasoninge2e.Check(plan, reasoninge2e.BackendRequestObservation{
		AssistantTurns: []reasoninge2e.BackendTurnObservation{{
			TurnID: tr.ID, VisibleText: "plain", Reasoning: sampleReasoning("inserted"),
		}},
	})
	if err == nil {
		t.Fatal("expected insertion error")
	}
	assertSafeOracleErr(t, err, plan.Seed, tr.ID, string(tr.Mode))
}

func TestOracle_fail_duplicationWhenPreserved(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   12,
		Policy: reasoninge2e.PreserveAllReasoning,
		Turns:  []reasoninge2e.TurnSpec{{VisibleText: "p", Reasoning: sampleReasoning("once")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := plan.Turns()[0]
	dup := append(sampleReasoning("once"), sampleReasoning("once")...)
	err = reasoninge2e.Check(plan, reasoninge2e.BackendRequestObservation{
		AssistantTurns: []reasoninge2e.BackendTurnObservation{{
			TurnID: tr.ID, VisibleText: "p", Reasoning: dup,
		}},
	})
	if err == nil {
		t.Fatal("expected duplication error")
	}
	assertSafeOracleErr(t, err, plan.Seed, tr.ID, string(tr.Mode))
}

func TestOracle_fail_conflictRewritten(t *testing.T) {
	t.Parallel()
	alt := sampleReasoning("alt")
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   13,
		Policy: reasoninge2e.ConflictReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "c", Reasoning: sampleReasoning("obs"), ConflictReasoning: alt,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := plan.Turns()[0]
	err = reasoninge2e.Check(plan, reasoninge2e.BackendRequestObservation{
		AssistantTurns: []reasoninge2e.BackendTurnObservation{{
			TurnID: tr.ID, VisibleText: "c", Reasoning: sampleReasoning("obs"),
		}},
	})
	if err == nil {
		t.Fatal("expected conflict rewrite error")
	}
	assertSafeOracleErr(t, err, plan.Seed, tr.ID, string(tr.Mode))
}

func TestOracle_fail_toolMismatch(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   14,
		Policy: reasoninge2e.PreserveAllReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "t",
			Tool:        &reasoninge2e.ToolExchange{ID: "call_1", Name: "fn", Arguments: `{}`, Result: `ok`},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := plan.Turns()[0]
	err = reasoninge2e.Check(plan, reasoninge2e.BackendRequestObservation{
		AssistantTurns: []reasoninge2e.BackendTurnObservation{{
			TurnID: tr.ID, VisibleText: "t",
			Tool: &reasoninge2e.ToolExchange{ID: "call_2", Name: "fn", Arguments: `{}`, Result: `ok`},
		}},
	})
	if err == nil {
		t.Fatal("expected tool mismatch")
	}
	assertSafeOracleErr(t, err, plan.Seed, tr.ID, string(tr.Mode))
}

func TestOracle_fail_turnCountMismatch(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   15,
		Policy: reasoninge2e.PreserveAllReasoning,
		Turns:  []reasoninge2e.TurnSpec{{VisibleText: "a"}, {VisibleText: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = reasoninge2e.Check(plan, reasoninge2e.BackendRequestObservation{
		AssistantTurns: []reasoninge2e.BackendTurnObservation{{TurnID: "x", VisibleText: "a"}},
	})
	if err == nil {
		t.Fatal("expected turn count mismatch")
	}
	msg := err.Error()
	if !strings.Contains(msg, "seed=15") {
		t.Fatalf("seed missing: %v", err)
	}
	if !strings.Contains(msg, "policy=preserve_all") {
		t.Fatalf("policy label missing: %v", err)
	}
	if strings.Contains(msg, "mode=") {
		t.Fatalf("turn_count must use policy= not mode=: %v", err)
	}
	assertNoPayloadLeak(t, msg)
}

func TestOracle_fail_wrongContent_droppedAndPreserved(t *testing.T) {
	t.Parallel()
	t.Run("dropped", func(t *testing.T) {
		t.Parallel()
		plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
			Seed:   21,
			Policy: reasoninge2e.DropAllReasoning,
			Turns:  []reasoninge2e.TurnSpec{{VisibleText: "d", Reasoning: sampleReasoning("need-restore")}},
		})
		if err != nil {
			t.Fatal(err)
		}
		tr := plan.Turns()[0]
		err = reasoninge2e.Check(plan, reasoninge2e.BackendRequestObservation{
			AssistantTurns: []reasoninge2e.BackendTurnObservation{{
				TurnID: tr.ID, VisibleText: "d", Reasoning: sampleReasoning("wrong-restore"),
			}},
		})
		if err == nil {
			t.Fatal("expected restoration_content error")
		}
		if !strings.Contains(err.Error(), "restoration_content") {
			t.Fatalf("reason code: %v", err)
		}
		assertSafeOracleErr(t, err, plan.Seed, tr.ID, string(tr.Mode))
	})
	t.Run("preserved", func(t *testing.T) {
		t.Parallel()
		plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
			Seed:   22,
			Policy: reasoninge2e.PreserveAllReasoning,
			Turns:  []reasoninge2e.TurnSpec{{VisibleText: "p", Reasoning: sampleReasoning("keep")}},
		})
		if err != nil {
			t.Fatal(err)
		}
		tr := plan.Turns()[0]
		err = reasoninge2e.Check(plan, reasoninge2e.BackendRequestObservation{
			AssistantTurns: []reasoninge2e.BackendTurnObservation{{
				TurnID: tr.ID, VisibleText: "p", Reasoning: sampleReasoning("mutated-keep"),
			}},
		})
		if err == nil {
			t.Fatal("expected preserved_content error")
		}
		if !strings.Contains(err.Error(), "preserved_content") {
			t.Fatalf("reason code: %v", err)
		}
		assertSafeOracleErr(t, err, plan.Seed, tr.ID, string(tr.Mode))
	})
}

func TestOracle_fail_droppedExcessIsDuplication(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   23,
		Policy: reasoninge2e.DropAllReasoning,
		Turns:  []reasoninge2e.TurnSpec{{VisibleText: "d", Reasoning: sampleReasoning("hidden")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := plan.Turns()[0]
	dup := append(sampleReasoning("hidden"), sampleReasoning("extra")...)
	err = reasoninge2e.Check(plan, reasoninge2e.BackendRequestObservation{
		AssistantTurns: []reasoninge2e.BackendTurnObservation{{
			TurnID: tr.ID, VisibleText: "d", Reasoning: dup,
		}},
	})
	if err == nil {
		t.Fatal("expected duplication error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "reasoning_duplication") {
		t.Fatalf("want reasoning_duplication: %v", err)
	}
	if strings.Contains(msg, "restoration_incomplete") {
		t.Fatalf("excess must not be labeled incomplete: %v", err)
	}
	assertSafeOracleErr(t, err, plan.Seed, tr.ID, string(tr.Mode))
}

func TestOracle_multiBlock_order(t *testing.T) {
	t.Parallel()
	blocks := []reasoninge2e.ReasoningBlock{
		{Dialect: "d1", Text: "first", Signature: "sig-first", Opaque: []byte(`{"o":"1"}`)},
		{Dialect: "d2", Text: "second", Signature: "sig-second", Opaque: []byte(`{"o":"2"}`)},
	}
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   24,
		Policy: reasoninge2e.PreserveAllReasoning,
		Turns:  []reasoninge2e.TurnSpec{{VisibleText: "m", Reasoning: blocks}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := plan.Turns()[0]
	t.Run("exact_order", func(t *testing.T) {
		t.Parallel()
		obs := reasoninge2e.BackendRequestObservation{
			AssistantTurns: []reasoninge2e.BackendTurnObservation{{
				TurnID: tr.ID, VisibleText: "m",
				Reasoning: []reasoninge2e.ReasoningBlock{
					{Dialect: "d1", Text: "first", Signature: "sig-first", Opaque: []byte(`{"o":"1"}`)},
					{Dialect: "d2", Text: "second", Signature: "sig-second", Opaque: []byte(`{"o":"2"}`)},
				},
			}},
		}
		if err := reasoninge2e.Check(plan, obs); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("swapped_order", func(t *testing.T) {
		t.Parallel()
		obs := reasoninge2e.BackendRequestObservation{
			AssistantTurns: []reasoninge2e.BackendTurnObservation{{
				TurnID: tr.ID, VisibleText: "m",
				Reasoning: []reasoninge2e.ReasoningBlock{
					{Dialect: "d2", Text: "second", Signature: "sig-second", Opaque: []byte(`{"o":"2"}`)},
					{Dialect: "d1", Text: "first", Signature: "sig-first", Opaque: []byte(`{"o":"1"}`)},
				},
			}},
		}
		err := reasoninge2e.Check(plan, obs)
		if err == nil {
			t.Fatal("expected order mismatch")
		}
		if !strings.Contains(err.Error(), "preserved_content") {
			t.Fatalf("reason code: %v", err)
		}
		assertSafeOracleErr(t, err, plan.Seed, tr.ID, string(tr.Mode))
	})
}

func assertSafeOracleErr(t *testing.T, err error, seed uint64, turnID, mode string) {
	t.Helper()
	msg := err.Error()
	if !strings.Contains(msg, "seed=") && !strings.Contains(msg, "seed "+itoa(seed)) {
		// accept either seed=N or formatted seed
		if !strings.Contains(msg, itoa(seed)) {
			t.Fatalf("seed missing in %q", msg)
		}
	}
	if turnID != "" && !strings.Contains(msg, turnID) {
		t.Fatalf("turn id missing in %q", msg)
	}
	if mode != "" && !strings.Contains(msg, mode) {
		t.Fatalf("mode missing in %q", msg)
	}
	assertNoPayloadLeak(t, msg)
}

func assertNoPayloadLeak(t *testing.T, msg string) {
	t.Helper()
	leaks := []string{
		"need-restore", "wrong-restore", "hidden", "inserted", "once", "alt", "obs", "keep",
		"mutated-keep", "extra", "first", "second", "sig-", `{"k":`, `{"q":`, `{"o":`, "secret",
	}
	for _, leak := range leaks {
		if strings.Contains(msg, leak) {
			t.Fatalf("payload leak %q in %q", leak, msg)
		}
	}
}

func itoa(u uint64) string {
	if u == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for u > 0 {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
	}
	return string(b[i:])
}
