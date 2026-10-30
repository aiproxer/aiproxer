package reasoninge2e_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
)

func TestGenerateTranscriptPlan_sameSeedIdentical(t *testing.T) {
	t.Parallel()
	a, err := reasoninge2e.GenerateTranscriptPlan(reasoninge2e.MatrixModeCombined, 11, 20)
	if err != nil {
		t.Fatal(err)
	}
	b, err := reasoninge2e.GenerateTranscriptPlan(reasoninge2e.MatrixModeCombined, 11, 20)
	if err != nil {
		t.Fatal(err)
	}
	if a.StructuralTrace() != b.StructuralTrace() {
		t.Fatal("same seed/mode must yield identical structural trace")
	}
	if len(a.ScriptedTurns()) != len(b.ScriptedTurns()) || len(a.Plan().Turns()) != len(b.Plan().Turns()) {
		t.Fatal("same seed/mode must yield identical turn counts")
	}
	at, bt := a.Plan().Turns(), b.Plan().Turns()
	for i := range at {
		if at[i].Mode != bt[i].Mode || at[i].ID != bt[i].ID {
			t.Fatalf("turn %d mode/id mismatch", i)
		}
		if at[i].Observed.Streaming != bt[i].Observed.Streaming {
			t.Fatalf("turn %d streaming mismatch", i)
		}
		if (at[i].Observed.Tool == nil) != (bt[i].Observed.Tool == nil) {
			t.Fatalf("turn %d tool presence mismatch", i)
		}
		if (len(at[i].Observed.Reasoning) == 0) != (len(bt[i].Observed.Reasoning) == 0) {
			t.Fatalf("turn %d reasoning presence mismatch", i)
		}
	}
}

func TestGenerateTranscriptPlan_differentSeedDiffers(t *testing.T) {
	t.Parallel()
	a, err := reasoninge2e.GenerateTranscriptPlan(reasoninge2e.MatrixModeCombined, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	b, err := reasoninge2e.GenerateTranscriptPlan(reasoninge2e.MatrixModeCombined, 2, 20)
	if err != nil {
		t.Fatal(err)
	}
	if a.StructuralTrace() == b.StructuralTrace() {
		t.Fatal("different seeds must not share identical structural traces")
	}
}

func TestGenerateTranscriptPlan_defensiveCopies(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.GenerateTranscriptPlan(reasoninge2e.MatrixModeAlwaysReasonRandomClient, 5, 20)
	if err != nil {
		t.Fatal(err)
	}
	scripted := plan.ScriptedTurns()
	scripted[0].VisibleText = "MUTATED"
	scripted[0].ReasoningText = "MUTATED-REASON"
	decisions := plan.Decisions()
	decisions[0].ReasonCode = "MUTATED"
	turns := plan.Plan().Turns()
	turns[0].Observed.VisibleText = "MUTATED-VIS"
	again := plan.ScriptedTurns()
	if again[0].VisibleText == "MUTATED" || again[0].ReasoningText == "MUTATED-REASON" {
		t.Fatal("ScriptedTurns must return defensive copies")
	}
	if plan.Decisions()[0].ReasonCode == "MUTATED" {
		t.Fatal("Decisions must return defensive copies")
	}
	if plan.Plan().Turns()[0].Observed.VisibleText == "MUTATED-VIS" {
		t.Fatal("Plan turns must remain immutable via defensive copies")
	}
}

func TestGenerateTranscriptPlan_forcedCategories_randomBackendDropAll(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.GenerateTranscriptPlan(reasoninge2e.MatrixModeRandomBackendDropAll, 3, 20)
	if err != nil {
		t.Fatal(err)
	}
	assertForcedCategories(t, plan, true, false, true)
	for _, d := range plan.Decisions() {
		if d.HasReasoning && d.ClientDecision != "drop" {
			t.Fatalf("drop_all mode must drop reasoned turns; idx=%d got=%s", d.Index, d.ClientDecision)
		}
	}
}

func TestGenerateTranscriptPlan_forcedCategories_alwaysReasonRandomClient(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.GenerateTranscriptPlan(reasoninge2e.MatrixModeAlwaysReasonRandomClient, 4, 20)
	if err != nil {
		t.Fatal(err)
	}
	assertForcedCategories(t, plan, false, true, true)
	for _, d := range plan.Decisions() {
		if !d.HasReasoning {
			t.Fatalf("always_reason must reason every turn; idx=%d", d.Index)
		}
	}
}

func TestGenerateTranscriptPlan_forcedCategories_combined(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.GenerateTranscriptPlan(reasoninge2e.MatrixModeCombined, 9, 20)
	if err != nil {
		t.Fatal(err)
	}
	assertForcedCategories(t, plan, true, true, true)
}

func TestGenerateTranscriptPlan_streamAlternation(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.GenerateTranscriptPlan(reasoninge2e.MatrixModeCombined, 8, 20)
	if err != nil {
		t.Fatal(err)
	}
	turns := plan.Plan().Turns()
	var sawStream, sawNon bool
	for i, tr := range turns {
		want := i%2 == 1
		if tr.Observed.Streaming != want {
			t.Fatalf("turn %d streaming=%v want=%v", i, tr.Observed.Streaming, want)
		}
		if want {
			sawStream = true
		} else {
			sawNon = true
		}
	}
	if !sawStream || !sawNon {
		t.Fatal("must alternate streaming and non-streaming")
	}
}

func TestGenerateTranscriptPlan_independentRNGStreams(t *testing.T) {
	t.Parallel()
	// Include seed 0: previous retain-mutation code forced bits and broke independence.
	seeds := []uint64{0, 1, 2, 3, 5, 7, 10, 42, 99, 128}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("always_seed_%d", seed), func(t *testing.T) {
			t.Parallel()
			plan, err := reasoninge2e.GenerateTranscriptPlan(reasoninge2e.MatrixModeAlwaysReasonRandomClient, seed, 20)
			if err != nil {
				t.Fatal(err)
			}
			want := reasoninge2e.ClientRetainSequence(seed, 20)
			got := plan.Decisions()
			if len(want) != len(got) {
				t.Fatalf("retain len got=%d want=%d", len(got), len(want))
			}
			for i := range want {
				preserve := got[i].ClientDecision == "preserve"
				if want[i] != preserve {
					t.Fatalf("client stream independence broken at idx=%d want_preserve=%v got=%s", i, want[i], got[i].ClientDecision)
				}
			}
		})
		t.Run(fmt.Sprintf("combined_seed_%d", seed), func(t *testing.T) {
			t.Parallel()
			combined, err := reasoninge2e.GenerateTranscriptPlan(reasoninge2e.MatrixModeCombined, seed, 20)
			if err != nil {
				t.Fatal(err)
			}
			clientBits := reasoninge2e.ClientRetainSequence(seed, 20)
			for i, d := range combined.Decisions() {
				if !d.HasReasoning {
					if d.ClientDecision != "none" {
						t.Fatalf("unreasoned turn must be client none idx=%d got=%s", i, d.ClientDecision)
					}
					continue
				}
				preserve := d.ClientDecision == "preserve"
				if clientBits[i] != preserve {
					t.Fatalf("combined client independence broken at idx=%d", i)
				}
			}
		})
	}
}

func TestGenerateTranscriptPlan_finalTurnNeverTool(t *testing.T) {
	t.Parallel()
	modes := []reasoninge2e.MatrixMode{
		reasoninge2e.MatrixModeRandomBackendDropAll,
		reasoninge2e.MatrixModeAlwaysReasonRandomClient,
		reasoninge2e.MatrixModeCombined,
	}
	for _, mode := range modes {
		for seed := range uint64(64) {
			t.Run(fmt.Sprintf("%s/seed_%d", mode, seed), func(t *testing.T) {
				t.Parallel()
				plan, err := reasoninge2e.GenerateTranscriptPlan(mode, seed, 20)
				if err != nil {
					t.Fatal(err)
				}
				dec := plan.Decisions()
				if len(dec) == 0 {
					t.Fatal("empty decisions")
				}
				last := dec[len(dec)-1]
				if last.HasTool {
					t.Fatalf("final turn must not be tool: mode=%s seed=%d", mode, seed)
				}
				scripted := plan.ScriptedTurns()
				if scripted[len(scripted)-1].ToolID != "" {
					t.Fatal("final scripted turn must not carry tool id")
				}
			})
		}
	}
}

func TestDefaultMatrixCases_clientVarietyAndFinalNoTool(t *testing.T) {
	t.Parallel()
	cases := reasoninge2e.DefaultMatrixCases()
	if len(cases) != 64 {
		t.Fatalf("cases=%d want=64", len(cases))
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s/seed_%d", c.Mode, c.Seed), func(t *testing.T) {
			t.Parallel()
			plan, err := reasoninge2e.GenerateTranscriptPlan(c.Mode, c.Seed, 20)
			if err != nil {
				t.Fatal(err)
			}
			needPreserveDrop := c.Mode == reasoninge2e.MatrixModeAlwaysReasonRandomClient || c.Mode == reasoninge2e.MatrixModeCombined
			needUnreasoned := c.Mode != reasoninge2e.MatrixModeAlwaysReasonRandomClient
			assertForcedCategories(t, plan, needUnreasoned, needPreserveDrop, true)
			dec := plan.Decisions()
			if dec[len(dec)-1].HasTool {
				t.Fatal("default matrix final turn must not be tool")
			}
			want := reasoninge2e.ClientRetainSequence(c.Seed, 20)
			for i, d := range dec {
				if c.Mode == reasoninge2e.MatrixModeRandomBackendDropAll {
					if d.HasReasoning && d.ClientDecision != "drop" {
						t.Fatalf("drop_all must drop reasoned turns idx=%d got=%s", i, d.ClientDecision)
					}
					continue
				}
				if d.HasReasoning {
					preserve := d.ClientDecision == "preserve"
					if want[i] != preserve {
						t.Fatalf("default matrix client independence broken idx=%d", i)
					}
				}
			}
		})
	}
}

func TestGenerateTranscriptPlan_uniqueIDs(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.GenerateTranscriptPlan(reasoninge2e.MatrixModeCombined, 15, 20)
	if err != nil {
		t.Fatal(err)
	}
	vis := map[string]int{}
	tools := map[string]int{}
	for i, tr := range plan.Plan().Turns() {
		if tr.Observed.VisibleText == "" {
			t.Fatalf("empty visible id at %d", i)
		}
		if prev, ok := vis[tr.Observed.VisibleText]; ok {
			t.Fatalf("duplicate visible text idx=%d prev=%d", i, prev)
		}
		vis[tr.Observed.VisibleText] = i
		if tr.Observed.Tool != nil {
			if prev, ok := tools[tr.Observed.Tool.ID]; ok {
				t.Fatalf("duplicate tool id idx=%d prev=%d", i, prev)
			}
			tools[tr.Observed.Tool.ID] = i
		}
	}
}

func TestGenerateTranscriptPlan_traceAndErrorsContentSafe(t *testing.T) {
	t.Parallel()
	plan, err := reasoninge2e.GenerateTranscriptPlan(reasoninge2e.MatrixModeCombined, 99, 20)
	if err != nil {
		t.Fatal(err)
	}
	secrets := collectPayloadSecrets(plan)
	trace := plan.StructuralTrace()
	fail := reasoninge2e.FormatMatrixFail(plan, 3, "restoration_incomplete")
	replay := reasoninge2e.MatrixReplayCommand(plan.Mode(), plan.Seed())
	for _, s := range []string{trace, fail, replay} {
		for _, secret := range secrets {
			if secret != "" && strings.Contains(s, secret) {
				t.Fatalf("content leak of payload fragment in output")
			}
		}
	}
	for _, need := range []string{
		"mode=combined",
		"seed=99",
		"turn=",
		"idx=3",
		"reason_code=restoration_incomplete",
		"AIP_REASONING_E2E_MODE=",
		"AIP_REASONING_E2E_SEED=",
		"TestReasoningPreservationHTTP_RandomMatrix",
	} {
		if !strings.Contains(fail, need) {
			t.Fatalf("fail missing %q in %q", need, fail)
		}
	}
	if !strings.Contains(replay, "AIP_REASONING_E2E_SEED=99") {
		t.Fatalf("replay command missing seed: %s", replay)
	}
}

func assertForcedCategories(t *testing.T, plan reasoninge2e.TranscriptPlan, needUnreasoned, needPreserveDrop, needTool bool) {
	t.Helper()
	var reasoned, unreasoned, tool, noTool, preserve, drop, stream, nonstream bool
	for _, d := range plan.Decisions() {
		if d.HasReasoning {
			reasoned = true
		} else {
			unreasoned = true
		}
		if d.HasTool {
			tool = true
		} else {
			noTool = true
		}
		switch d.ClientDecision {
		case "preserve":
			preserve = true
		case "drop":
			drop = true
		}
		if d.Streaming {
			stream = true
		} else {
			nonstream = true
		}
	}
	if !reasoned {
		t.Fatal("missing reasoned category")
	}
	if needUnreasoned && !unreasoned {
		t.Fatal("missing unreasoned category")
	}
	if needPreserveDrop && (!preserve || !drop) {
		t.Fatalf("missing preserve/drop categories preserve=%v drop=%v", preserve, drop)
	}
	if needTool && (!tool || !noTool) {
		t.Fatalf("missing tool/no-tool categories tool=%v noTool=%v", tool, noTool)
	}
	if !stream || !nonstream {
		t.Fatal("missing streaming/nonstreaming categories")
	}
	if got := len(plan.Plan().Turns()); got != 20 {
		t.Fatalf("turn count=%d want=20", got)
	}
	if got := len(plan.ScriptedTurns()); got != 20 {
		t.Fatalf("scripted count=%d want=20", got)
	}
	if plan.Mode() == "" {
		t.Fatal("empty mode")
	}
}

func collectPayloadSecrets(plan reasoninge2e.TranscriptPlan) []string {
	var out []string
	for _, s := range plan.ScriptedTurns() {
		out = append(out, s.VisibleText, s.ReasoningText, s.ToolArgs)
	}
	for _, tr := range plan.Plan().Turns() {
		out = append(out, tr.Observed.VisibleText)
		for _, r := range tr.Observed.Reasoning {
			out = append(out, r.Text, r.Signature, string(r.Opaque))
		}
		if tr.Observed.Tool != nil {
			out = append(out, tr.Observed.Tool.ID, tr.Observed.Tool.Arguments, tr.Observed.Tool.Result)
		}
	}
	return out
}
