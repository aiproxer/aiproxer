package reasoninge2e_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
)

func TestFailtrace_omitsSensitivePayloads(t *testing.T) {
	t.Parallel()
	secretText := "SECRET-REASONING-PAYLOAD"
	secretSig := "sig-SECRET-XYZ"
	secretOpaque := "opaque-SECRET-BYTES"
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   99,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{{
			VisibleText: "visible",
			Reasoning: []reasoninge2e.ReasoningBlock{{
				Dialect:   "openai.chat.reasoning_text.v1",
				Text:      secretText,
				Signature: secretSig,
				Opaque:    []byte(secretOpaque),
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	msg := reasoninge2e.FormatFail(plan, plan.Turns()[0].ID, reasoninge2e.ModeDropped, "restoration_incomplete", "count_got=0 count_want=1")
	for _, needle := range []string{secretText, secretSig, secretOpaque, "visible"} {
		if strings.Contains(msg, needle) {
			t.Fatalf("failtrace leaked %q in %q", needle, msg)
		}
	}
	for _, need := range []string{"seed=99", "policy=drop_all", "mode=dropped", "field=restoration_incomplete", "turn="} {
		if !strings.Contains(msg, need) {
			t.Fatalf("failtrace missing %q in %q", need, msg)
		}
	}
}

func TestFormatRetentionDiag_extractsFields(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("reasoninge2e oracle: seed=1 mode=dropped request_turn_id=turn-req history_turn_id=turn-hist request_turn=3 history_turn=0 artifact_state=evicted structural mismatch: unexpected_reasoning_insertion")
	got := reasoninge2e.FormatRetentionDiag(err)
	for _, need := range []string{
		"request_turn_id=turn-req",
		"history_turn_id=turn-hist",
		"request_turn=3",
		"history_turn=0",
		"artifact_state=evicted",
	} {
		if !strings.Contains(got, need) {
			t.Fatalf("missing %q in %q", need, got)
		}
	}
	if strings.Contains(got, "request_turn=request_turn_id") {
		t.Fatalf("request_turn= must not swallow request_turn_id: %q", got)
	}
	if reasoninge2e.FormatRetentionDiag(nil) != "" {
		t.Fatal("nil err must yield empty diag")
	}
}

func TestFormatRetentionDiag_composedWithMatrixAndSoakFail(t *testing.T) {
	t.Parallel()
	tp, err := reasoninge2e.GenerateTranscriptPlan(reasoninge2e.MatrixModeCombined, 7, 4)
	if err != nil {
		t.Fatal(err)
	}
	plan := tp.Plan()
	turns := plan.Turns()
	// Force a dropped reasoned prefix long enough to evict under bound=2.
	dropPlan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   208,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{VisibleText: "a", Reasoning: sampleReasoning("ra")},
			{VisibleText: "b", Reasoning: sampleReasoning("rb")},
			{VisibleText: "c", Reasoning: sampleReasoning("rc")},
			{VisibleText: "d", Reasoning: sampleReasoning("rd")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	dropTurns := dropPlan.Turns()
	blind := reasoninge2e.BackendRequestObservation{
		AssistantTurns: []reasoninge2e.BackendTurnObservation{
			{TurnID: dropTurns[0].ID, VisibleText: "a", Reasoning: sampleReasoning("ra")},
			{TurnID: dropTurns[1].ID, VisibleText: "b", Reasoning: sampleReasoning("rb")},
			{TurnID: dropTurns[2].ID, VisibleText: "c", Reasoning: sampleReasoning("rc")},
		},
	}
	oracleErr := reasoninge2e.CheckPrefixRetention(dropPlan, blind, 2)
	if oracleErr == nil {
		t.Fatal("expected eviction oracle error")
	}

	const idx = 3
	code := "unexpected_reasoning_insertion"
	matrixLine := reasoninge2e.FormatMatrixFail(tp, idx, code) + reasoninge2e.FormatRetentionDiag(oracleErr)
	soakLine := reasoninge2e.FormatSoakFail(tp, idx, code) + reasoninge2e.FormatRetentionDiag(oracleErr)
	wantReqID := dropTurns[3].ID
	wantHistID := dropTurns[0].ID
	for _, line := range []string{matrixLine, soakLine} {
		for _, need := range []string{
			"request_turn_id=" + wantReqID,
			"history_turn_id=" + wantHistID,
			"request_turn=3",
			"history_turn=0",
			"artifact_state=evicted",
			"reason_code=" + code,
		} {
			if !strings.Contains(line, need) {
				t.Fatalf("composed fail missing %q in %q", need, line)
			}
		}
		// Matrix/soak fail lines carry the current request turn id from transcript plan idx.
		if !strings.Contains(line, " turn="+turns[idx].ID) {
			t.Fatalf("composed fail missing current request context in %q", line)
		}
		assertNoPayloadLeak(t, line)
	}
}

func TestCheckPrefix_partialHistory(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   3,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{VisibleText: "a1", Reasoning: sampleReasoning("r1")},
			{VisibleText: "a2", Reasoning: sampleReasoning("r2")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	turns := plan.Turns()
	obs := reasoninge2e.BackendRequestObservation{
		AssistantTurns: []reasoninge2e.BackendTurnObservation{{
			TurnID:      turns[0].ID,
			VisibleText: turns[0].ExpectedBackend.VisibleText,
			Reasoning:   turns[0].ExpectedBackend.Reasoning,
		}},
	}
	if err := reasoninge2e.CheckPrefix(plan, obs); err != nil {
		t.Fatalf("prefix check: %v", err)
	}
	if err := reasoninge2e.Check(plan, obs); err == nil {
		t.Fatal("full Check must reject incomplete history")
	}
}
