package reasoninge2e

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
)

// MatrixMode selects a precomputed random-matrix scenario family.
type MatrixMode string

const (
	// MatrixModeRandomBackendDropAll randomizes backend reason/no-reason/tool; client drops all.
	MatrixModeRandomBackendDropAll MatrixMode = "random_backend_drop_all"
	// MatrixModeAlwaysReasonRandomClient always emits reasoning; client preserve/drop is randomized.
	MatrixModeAlwaysReasonRandomClient MatrixMode = "always_reason_random_client"
	// MatrixModeCombined randomizes backend and client retention independently.
	MatrixModeCombined MatrixMode = "combined"
)

const (
	matrixBackendSalt = int64(0xBEA11D75EED)
	matrixClientSalt  = int64(0x4C11E475EED)
)

type backendKind int

const (
	backendReason backendKind = iota
	backendNoReason
	backendTool
)

// ScriptedBackendTurn is a content-bearing scripted assistant response for refbackends.
// HTTP drivers map this to emulator ScriptedTurn values; traces must not print these fields.
type ScriptedBackendTurn struct {
	VisibleText   string
	ReasoningText string
	ToolID        string
	ToolName      string
	ToolArgs      string
}

// DecisionRecord is one compact structural decision for a generated turn.
type DecisionRecord struct {
	Index          int
	TurnID         string
	BackendKind    string // reason | no_reason | tool
	ClientDecision string // preserve | drop | none
	Streaming      bool
	HasReasoning   bool
	HasTool        bool
	ReasonCode     string // structural label only; empty unless set by callers for failures
}

// TranscriptPlan is an immutable precomputed matrix scenario.
type TranscriptPlan struct {
	mode      MatrixMode
	seed      uint64
	turnCount int
	plan      Plan
	scripted  []ScriptedBackendTurn
	decisions []DecisionRecord
	trace     string
}

// GenerateTranscriptPlan precomputes a deterministic scenario from seed+mode+turnCount.
// It uses independent RNG streams for backend and client choices (no package-global RNG).
func GenerateTranscriptPlan(mode MatrixMode, seed uint64, turnCount int) (TranscriptPlan, error) {
	if turnCount <= 0 {
		return TranscriptPlan{}, fmt.Errorf("reasoninge2e matrix: turn_count must be > 0")
	}
	switch mode {
	case MatrixModeRandomBackendDropAll, MatrixModeAlwaysReasonRandomClient, MatrixModeCombined:
	default:
		return TranscriptPlan{}, fmt.Errorf("reasoninge2e matrix: unknown mode %q", mode)
	}

	kinds := drawBackendKinds(mode, seed, turnCount)
	retain := ClientRetainSequence(seed, turnCount)
	forceMatrixCategories(mode, kinds, retain)
	reserveFinalTurnNoTool(kinds)

	specs := make([]TurnSpec, turnCount)
	scripted := make([]ScriptedBackendTurn, turnCount)
	decisions := make([]DecisionRecord, turnCount)
	for i := range turnCount {
		streaming := i%2 == 1
		vis := fmt.Sprintf("vis-s%d-t%d", seed, i)
		reasonText := ""
		var tool *ToolExchange
		switch kinds[i] {
		case backendReason:
			reasonText = fmt.Sprintf("reason-s%d-t%d-payload", seed, i)
		case backendNoReason:
			// intentionally empty
		case backendTool:
			reasonText = fmt.Sprintf("reason-s%d-t%d-tool-payload", seed, i)
			tool = &ToolExchange{
				ID:        fmt.Sprintf("call-s%d-t%d", seed, i),
				Name:      "matrix_tool",
				Arguments: fmt.Sprintf(`{"k":"s%d-t%d"}`, seed, i),
				Result:    fmt.Sprintf(`{"ok":true,"t":%d}`, i),
			}
		}
		if mode == MatrixModeAlwaysReasonRandomClient && reasonText == "" {
			reasonText = fmt.Sprintf("reason-s%d-t%d-payload", seed, i)
		}
		var reasoning []ReasoningBlock
		if reasonText != "" {
			// Wire Chat observations carry dialect+text only; keep plan observed blocks
			// aligned so ClientEmulator.Record structural matching succeeds.
			reasoning = []ReasoningBlock{{
				Dialect: DialectOpenAIChatTextV1,
				Text:    reasonText,
			}}
		}
		clientMode := ModeNone
		clientDecision := "none"
		if len(reasoning) > 0 {
			if mode == MatrixModeRandomBackendDropAll {
				clientMode = ModeDropped
				clientDecision = "drop"
			} else if retain[i] {
				clientMode = ModePreserved
				clientDecision = "preserve"
			} else {
				clientMode = ModeDropped
				clientDecision = "drop"
			}
		}
		specs[i] = TurnSpec{
			VisibleText: vis,
			Reasoning:   reasoning,
			Tool:        cloneTool(tool),
			Streaming:   streaming,
			ClientMode:  clientMode,
		}
		st := ScriptedBackendTurn{VisibleText: vis, ReasoningText: reasonText}
		if tool != nil {
			st.ToolID = tool.ID
			st.ToolName = tool.Name
			st.ToolArgs = tool.Arguments
		}
		scripted[i] = st
		decisions[i] = DecisionRecord{
			Index:          i,
			TurnID:         fmt.Sprintf("turn-%d-%d", seed, i),
			BackendKind:    backendKindString(kinds[i]),
			ClientDecision: clientDecision,
			Streaming:      streaming,
			HasReasoning:   len(reasoning) > 0,
			HasTool:        tool != nil,
		}
	}

	policy := DropAllReasoning
	if mode != MatrixModeRandomBackendDropAll {
		policy = SeededPerTurnRetention
	}
	plan, err := BuildPlan(PlanConfig{Seed: seed, Policy: policy, Turns: specs})
	if err != nil {
		return TranscriptPlan{}, err
	}
	// Align decision turn IDs with BuildPlan IDs.
	built := plan.Turns()
	for i := range decisions {
		decisions[i].TurnID = built[i].ID
	}
	out := TranscriptPlan{
		mode:      mode,
		seed:      seed,
		turnCount: turnCount,
		plan:      plan,
		scripted:  scripted,
		decisions: decisions,
	}
	out.trace = buildStructuralTrace(out)
	return out, nil
}

// ClientRetainSequence returns the independent client preserve bits for seed (true=preserve).
// Exported for independence proofs; callers must not treat this as payload-bearing.
func ClientRetainSequence(seed uint64, n int) []bool {
	rng := rand.New(rand.NewPCG(uint64(int64(seed)^matrixClientSalt), 0)) //nolint:gosec // deterministic test plan
	out := make([]bool, n)
	for i := range n {
		out[i] = rng.IntN(2) == 1
	}
	return out
}

func drawBackendKinds(mode MatrixMode, seed uint64, n int) []backendKind {
	rng := rand.New(rand.NewPCG(uint64(int64(seed)^matrixBackendSalt), 0)) //nolint:gosec // deterministic test plan
	out := make([]backendKind, n)
	for i := range n {
		switch mode {
		case MatrixModeAlwaysReasonRandomClient:
			if rng.IntN(2) == 0 {
				out[i] = backendTool
			} else {
				out[i] = backendReason
			}
		default:
			out[i] = backendKind(rng.IntN(3))
		}
	}
	return out
}

func forceMatrixCategories(mode MatrixMode, kinds []backendKind, retain []bool) {
	n := len(kinds)
	if n == 0 {
		return
	}
	// Prefer non-final slots so reserveFinalTurnNoTool can keep the last turn tool-free.
	nonFinal := max(n-1, 1)
	setKind := func(idx int, k backendKind) {
		if idx >= 0 && idx < n {
			kinds[idx] = k
		}
	}
	hasKind := func(k backendKind) bool {
		return slices.Contains(kinds, k)
	}
	hasNoTool := func() bool {
		for _, x := range kinds {
			if x != backendTool {
				return true
			}
		}
		return false
	}
	toolSlot := 2 % nonFinal
	altSlot := 3 % nonFinal
	if altSlot == toolSlot && nonFinal > 1 {
		altSlot = (toolSlot + 1) % nonFinal
	}

	switch mode {
	case MatrixModeRandomBackendDropAll, MatrixModeCombined:
		if !hasKind(backendReason) {
			setKind(0%nonFinal, backendReason)
		}
		if !hasKind(backendNoReason) {
			setKind(1%nonFinal, backendNoReason)
		}
		if !hasKind(backendTool) {
			setKind(toolSlot, backendTool)
		}
		if !hasNoTool() {
			setKind(altSlot, backendReason)
		}
	case MatrixModeAlwaysReasonRandomClient:
		for i := range kinds {
			if kinds[i] == backendNoReason {
				kinds[i] = backendReason
			}
		}
		if !hasKind(backendTool) {
			setKind(toolSlot, backendTool)
		}
		if !hasNoTool() {
			setKind(altSlot, backendReason)
		}
	}
	_ = retain // client retain bits are never mutated; independence is preserved.
}

// reserveFinalTurnNoTool ensures the last turn is never a tool so every tool
// result is submitted on a subsequent request.
func reserveFinalTurnNoTool(kinds []backendKind) {
	n := len(kinds)
	if n == 0 {
		return
	}
	if kinds[n-1] == backendTool {
		kinds[n-1] = backendReason
	}
}

func backendKindString(k backendKind) string {
	switch k {
	case backendReason:
		return "reason"
	case backendNoReason:
		return "no_reason"
	case backendTool:
		return "tool"
	default:
		return "unknown"
	}
}

func buildStructuralTrace(p TranscriptPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mode=%s seed=%d turns=%d", p.mode, p.seed, p.turnCount)
	for _, d := range p.decisions {
		fmt.Fprintf(&b, " | i=%d id=%s bk=%s cl=%s stream=%t r=%t t=%t",
			d.Index, d.TurnID, d.BackendKind, d.ClientDecision, d.Streaming, d.HasReasoning, d.HasTool)
	}
	return b.String()
}

// Mode returns the matrix mode.
func (p TranscriptPlan) Mode() MatrixMode { return p.mode }

// Seed returns the scenario seed.
func (p TranscriptPlan) Seed() uint64 { return p.seed }

// TurnCount returns the configured turn count.
func (p TranscriptPlan) TurnCount() int { return p.turnCount }

// Plan returns the immutable reasoninge2e Plan (defensive turn copies via Plan methods).
func (p TranscriptPlan) Plan() Plan { return p.plan }

// ScriptedTurns returns defensive copies of scripted backend turns.
func (p TranscriptPlan) ScriptedTurns() []ScriptedBackendTurn {
	if len(p.scripted) == 0 {
		return nil
	}
	out := make([]ScriptedBackendTurn, len(p.scripted))
	copy(out, p.scripted)
	return out
}

// Decisions returns defensive copies of structural decision records.
func (p TranscriptPlan) Decisions() []DecisionRecord {
	if len(p.decisions) == 0 {
		return nil
	}
	out := make([]DecisionRecord, len(p.decisions))
	copy(out, p.decisions)
	return out
}

// StructuralTrace returns a compact content-safe decision trace.
func (p TranscriptPlan) StructuralTrace() string { return p.trace }

// MatrixCase is one default-matrix (mode, seed) pair.
type MatrixCase struct {
	Mode MatrixMode
	Seed uint64
}

// DefaultMatrixCases returns the exact 64-seed split: 16 drop-all, 16 always-reason, 32 combined.
// Always-reason and combined seeds are chosen so the independent client RNG stream
// naturally includes both preserve and drop (client bits are never mutated).
func DefaultMatrixCases() []MatrixCase {
	out := make([]MatrixCase, 0, 64)
	for seed := range 16 {
		out = append(out, MatrixCase{Mode: MatrixModeRandomBackendDropAll, Seed: uint64(seed)})
	}
	for _, seed := range selectSeedsWithClientVariety(MatrixModeAlwaysReasonRandomClient, 16, 20) {
		out = append(out, MatrixCase{Mode: MatrixModeAlwaysReasonRandomClient, Seed: seed})
	}
	for _, seed := range selectSeedsWithClientVariety(MatrixModeCombined, 32, 20) {
		out = append(out, MatrixCase{Mode: MatrixModeCombined, Seed: seed})
	}
	return out
}

func selectSeedsWithClientVariety(mode MatrixMode, want, turnCount int) []uint64 {
	out := make([]uint64, 0, want)
	for seed := uint64(0); len(out) < want; seed++ {
		if seed > 1_000_000 {
			break
		}
		if !seedHasClientVariety(mode, seed, turnCount) {
			continue
		}
		out = append(out, seed)
	}
	return out
}

func seedHasClientVariety(mode MatrixMode, seed uint64, turnCount int) bool {
	plan, err := GenerateTranscriptPlan(mode, seed, turnCount)
	if err != nil {
		return false
	}
	var preserve, drop, tool, noTool bool
	dec := plan.Decisions()
	if len(dec) == 0 || dec[len(dec)-1].HasTool {
		return false
	}
	for _, d := range dec {
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
	}
	if !tool || !noTool {
		return false
	}
	switch mode {
	case MatrixModeAlwaysReasonRandomClient, MatrixModeCombined:
		return preserve && drop
	default:
		return true
	}
}

// MatrixReplayCommand returns a single content-safe seed-replay command.
func MatrixReplayCommand(mode MatrixMode, seed uint64) string {
	return fmt.Sprintf(
		"AIP_REASONING_E2E_MODE=%s AIP_REASONING_E2E_SEED=%d go test -tags=precommit -run TestReasoningPreservationHTTP_RandomMatrix -count=1 ./internal/stdhttp/",
		mode, seed,
	)
}

// FormatMatrixFail builds a content-safe matrix failure line with replay command.
func FormatMatrixFail(plan TranscriptPlan, turnIndex int, reasonCode string) string {
	turnID := ""
	if turnIndex >= 0 && turnIndex < len(plan.decisions) {
		turnID = plan.decisions[turnIndex].TurnID
	}
	return fmt.Sprintf(
		"reasoninge2e matrix fail: mode=%s seed=%d turn=%s idx=%d reason_code=%s decisions=%d trace=%s replay=%s",
		plan.mode, plan.seed, turnID, turnIndex, reasonCode, len(plan.decisions), plan.trace, MatrixReplayCommand(plan.mode, plan.seed),
	)
}
