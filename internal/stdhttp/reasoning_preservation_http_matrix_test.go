//go:build precommit

package stdhttp_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	refchat "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
)

// TestReasoningPreservationHTTP_RandomMatrix exercises the exact 64×20 precomputed
// OpenAI Chat matrix (16 random_backend_drop_all + 16 always_reason_random_client +
// 32 combined) through BuildHost/stdhttp/real adapters/refbackend.
//
// Seeds run sequentially (no t.Parallel across full stacks). Each seed owns one
// isolated proxy+emulator stack and 20 HTTP turns. Assumed budget: under the
// default 10m go-test timeout for the full 1280-turn matrix; single-seed replay
// via AIP_REASONING_E2E_MODE + AIP_REASONING_E2E_SEED.
func TestReasoningPreservationHTTP_RandomMatrix(t *testing.T) {
	cases := selectReasoningMatrixCases(t)
	if os.Getenv("AIP_REASONING_E2E_MODE") == "" && os.Getenv("AIP_REASONING_E2E_SEED") == "" {
		if len(cases) != 64 {
			t.Fatalf("matrix case count=%d want=64", len(cases))
		}
		var dropAll, always, combined int
		for _, c := range cases {
			switch c.Mode {
			case reasoninge2e.MatrixModeRandomBackendDropAll:
				dropAll++
			case reasoninge2e.MatrixModeAlwaysReasonRandomClient:
				always++
			case reasoninge2e.MatrixModeCombined:
				combined++
			}
		}
		if dropAll != 16 || always != 16 || combined != 32 {
			t.Fatalf("matrix split drop=%d always=%d combined=%d want 16/16/32", dropAll, always, combined)
		}
	}

	const turnsPerSeed = 20
	var totalHTTP atomic.Int64
	for _, c := range cases {
		name := fmt.Sprintf("%s/seed_%d", c.Mode, c.Seed)
		ok := t.Run(name, func(t *testing.T) {
			n, err := executeReasoningMatrixSeed(c.Mode, c.Seed, turnsPerSeed, 3*time.Minute, matrixFail)
			if err != nil {
				t.Fatal(err)
			}
			totalHTTP.Add(int64(n))
		})
		if !ok {
			return
		}
	}
	if !t.Failed() && os.Getenv("AIP_REASONING_E2E_MODE") == "" && os.Getenv("AIP_REASONING_E2E_SEED") == "" {
		if got := totalHTTP.Load(); got != int64(64*turnsPerSeed) {
			t.Fatalf("total ledger HTTP turns=%d want=%d", got, 64*turnsPerSeed)
		}
	}
}

func selectReasoningMatrixCases(t *testing.T) []reasoninge2e.MatrixCase {
	t.Helper()
	cases := reasoninge2e.DefaultMatrixCases()
	modeEnv := strings.TrimSpace(os.Getenv("AIP_REASONING_E2E_MODE"))
	seedEnv := strings.TrimSpace(os.Getenv("AIP_REASONING_E2E_SEED"))
	if modeEnv == "" && seedEnv == "" {
		return cases
	}
	if modeEnv == "" || seedEnv == "" {
		t.Fatalf("replay requires both AIP_REASONING_E2E_MODE and AIP_REASONING_E2E_SEED")
	}
	seed, err := strconv.ParseUint(seedEnv, 10, 64)
	if err != nil {
		t.Fatalf("AIP_REASONING_E2E_SEED parse: %v", err)
	}
	mode := reasoninge2e.MatrixMode(modeEnv)
	filtered := make([]reasoninge2e.MatrixCase, 0, 1)
	for _, c := range cases {
		if c.Mode == mode && c.Seed == seed {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) != 1 {
		t.Fatalf("replay filter mode=%s seed=%d matched=%d", mode, seed, len(filtered))
	}
	return filtered
}

// matrixFailFunc formats a content-safe failure for one seed/turn.
type matrixFailFunc func(tp reasoninge2e.TranscriptPlan, idx int, reasonCode string, err error) string

// executeReasoningMatrixSeed runs one isolated full-HTTP seed and returns the
// OracleLedger request count. Safe for soak worker goroutines (no testing.T).
func executeReasoningMatrixSeed(
	mode reasoninge2e.MatrixMode,
	seed uint64,
	turnCount int,
	perSeedTimeout time.Duration,
	fail matrixFailFunc,
) (int, error) {
	if fail == nil {
		fail = matrixFail
	}
	if perSeedTimeout <= 0 {
		perSeedTimeout = 3 * time.Minute
	}
	tp, err := reasoninge2e.GenerateTranscriptPlan(mode, seed, turnCount)
	if err != nil {
		return 0, fmt.Errorf("matrix plan generate mode=%s seed=%d: %v", mode, seed, err)
	}
	plan := tp.Plan()
	scripted := toRefchatScripted(tp.ScriptedTurns())
	validators := chatRestoreValidatorsPerTurnStream(plan, turnCount, rpHarnessMaxTurnsPerSession)
	stack, err := startReasoningPreservationChatStackErr("restore", scripted, validators...)
	if err != nil {
		return 0, errors.New(fail(tp, 0, "stack_bootstrap", err))
	}
	defer stack.cleanup()

	cli := &reasoninge2e.ChatWireClient{
		BaseURL:    stack.proxyURL + "/v1",
		APIKey:     rpE2EFakeKey,
		HTTPClient: stack.proxy.Client(),
		Model:      rpE2EModel,
	}
	emu := reasoninge2e.NewClientEmulator(plan)
	tools := matrixToolsIfNeeded(tp)
	ctx, cancel := context.WithTimeout(context.Background(), perSeedTimeout)
	defer cancel()

	turns := plan.Turns()
	for i := range turnCount {
		prompt := fmt.Sprintf("user-s%d-t%d", seed, i)
		msgs, err := emu.MaterializeChatRequest(prompt)
		if err != nil {
			return 0, errors.New(fail(tp, i, "client_materialize", err))
		}
		stream := turns[i].Observed.Streaming
		resp, err := cli.PostChatCompletion(ctx, stream, msgs, tools)
		if err != nil {
			return 0, errors.New(fail(tp, i, "http_transport", err))
		}
		if resp.Status != 200 {
			return 0, errors.New(fail(tp, i, "http_status", fmt.Errorf("status=%d", resp.Status)))
		}
		if stream {
			if err := assertMatrixStreamWire(resp); err != nil {
				return 0, errors.New(fail(tp, i, "stream_framing", err))
			}
		} else if strings.Contains(strings.ToLower(resp.ContentType), "text/event-stream") {
			return 0, errors.New(fail(tp, i, "nonstream_content_type", fmt.Errorf("unexpected SSE")))
		}
		if err := emu.Record(reasoninge2e.ChatResponseFromTurn(resp)); err != nil {
			return 0, errors.New(fail(tp, i, "client_record", err))
		}
		wantTool := turns[i].Observed.Tool
		if wantTool != nil {
			if resp.Tool == nil || resp.Tool.ID != wantTool.ID || resp.Tool.Name != wantTool.Name || resp.Tool.Arguments != wantTool.Arguments {
				return 0, errors.New(fail(tp, i, "tool_observe", fmt.Errorf("tool structural mismatch")))
			}
		} else if resp.Tool != nil {
			return 0, errors.New(fail(tp, i, "tool_presence", fmt.Errorf("unexpected tool")))
		}
		if err := stack.ledger.Err(); err != nil {
			return 0, errors.New(fail(tp, i, "oracle_ledger", err))
		}
	}
	if stack.ledger.Count() != turnCount {
		return 0, errors.New(fail(tp, turnCount-1, "oracle_count", fmt.Errorf("got=%d want=%d", stack.ledger.Count(), turnCount)))
	}
	if err := stack.ledger.Err(); err != nil {
		return 0, errors.New(fail(tp, turnCount-1, "oracle_ledger", err))
	}
	return stack.ledger.Count(), nil
}

func toRefchatScripted(in []reasoninge2e.ScriptedBackendTurn) []refchat.ScriptedTurn {
	out := make([]refchat.ScriptedTurn, len(in))
	for i := range in {
		out[i] = refchat.ScriptedTurn{
			VisibleText: in[i].VisibleText,
			Reasoning:   in[i].ReasoningText,
			ToolID:      in[i].ToolID,
			ToolName:    in[i].ToolName,
			ToolArgs:    in[i].ToolArgs,
		}
	}
	return out
}

func matrixToolsIfNeeded(tp reasoninge2e.TranscriptPlan) []map[string]any {
	for _, s := range tp.ScriptedTurns() {
		if s.ToolID != "" {
			return []map[string]any{{
				"type": "function",
				"function": map[string]any{
					"name": "matrix_tool",
					"parameters": map[string]any{
						"type":       "object",
						"properties": map[string]any{"k": map[string]any{"type": "string"}},
					},
				},
			}}
		}
	}
	return nil
}

func assertMatrixStreamWire(resp reasoninge2e.ChatTurnResponse) error {
	if !strings.Contains(strings.ToLower(resp.ContentType), "text/event-stream") {
		return fmt.Errorf("content_type")
	}
	if !strings.Contains(string(resp.RawBody), "[DONE]") {
		return fmt.Errorf("missing_done")
	}
	return nil
}

func matrixFail(tp reasoninge2e.TranscriptPlan, idx int, reasonCode string, err error) string {
	// Keep reason codes / structural err text only; never append raw bodies.
	code := reasonCode
	if err != nil {
		msg := err.Error()
		// Prefer already content-safe oracle/client codes when present.
		if _, after, ok := strings.Cut(msg, "structural mismatch:"); ok {
			code = strings.TrimSpace(after)
			if j := strings.IndexAny(code, " \t"); j >= 0 {
				code = code[:j]
			}
		}
	}
	return reasoninge2e.FormatMatrixFail(tp, idx, code) + reasoninge2e.FormatRetentionDiag(err)
}
