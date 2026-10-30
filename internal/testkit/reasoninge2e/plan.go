package reasoninge2e

import (
	"fmt"
	"math/rand/v2"
)

// BuildPlan precomputes a deterministic transcript plan from an explicit seed and policy.
func BuildPlan(cfg PlanConfig) (Plan, error) {
	if err := validatePolicy(cfg); err != nil {
		return Plan{}, err
	}
	rng := rand.New(rand.NewPCG(cfg.Seed, 0)) //nolint:gosec // deterministic test plan, not crypto
	out := make([]PlannedTurn, 0, len(cfg.Turns))
	for i, spec := range cfg.Turns {
		id := fmt.Sprintf("turn-%d-%d", cfg.Seed, i)
		observed := AssistantTurn{
			ID:          id,
			VisibleText: spec.VisibleText,
			Reasoning:   cloneBlocks(spec.Reasoning),
			Tool:        cloneTool(spec.Tool),
			Streaming:   spec.Streaming,
		}
		mode, submittedReasoning, err := materialize(cfg.Policy, observed.Reasoning, spec.ConflictReasoning, rng, spec.ClientMode)
		if err != nil {
			return Plan{}, fmt.Errorf("reasoninge2e: seed=%d turn=%s mode_plan: %w", cfg.Seed, id, err)
		}
		submitted := AssistantTurn{
			ID:          id,
			VisibleText: spec.VisibleText,
			Reasoning:   submittedReasoning,
			Tool:        cloneTool(spec.Tool),
			Streaming:   spec.Streaming,
		}
		expected := expectedBackend(mode, observed, submitted)
		out = append(out, PlannedTurn{
			ID:              id,
			Mode:            mode,
			Observed:        observed,
			Submitted:       submitted,
			ExpectedBackend: expected,
		})
	}
	return Plan{Seed: cfg.Seed, Policy: cfg.Policy, turns: out}, nil
}

func validatePolicy(cfg PlanConfig) error {
	switch cfg.Policy {
	case PreserveAllReasoning, DropAllReasoning, SeededPerTurnRetention, ConflictReasoning:
	default:
		return fmt.Errorf("reasoninge2e: unknown retention policy %d", int(cfg.Policy))
	}
	if cfg.Policy == ConflictReasoning {
		for i, spec := range cfg.Turns {
			if len(spec.Reasoning) == 0 {
				continue
			}
			if len(spec.ConflictReasoning) == 0 {
				return fmt.Errorf("reasoninge2e: seed=%d turn_index=%d conflict policy requires ConflictReasoning", cfg.Seed, i)
			}
		}
	}
	return nil
}

func materialize(
	policy RetentionPolicy,
	observed []ReasoningBlock,
	conflict []ReasoningBlock,
	rng *rand.Rand,
	clientMode RetentionMode,
) (RetentionMode, []ReasoningBlock, error) {
	if clientMode != "" {
		return materializeClientMode(clientMode, observed, conflict)
	}
	if len(observed) == 0 {
		return ModeNone, nil, nil
	}
	switch policy {
	case PreserveAllReasoning:
		return ModePreserved, cloneBlocks(observed), nil
	case DropAllReasoning:
		return ModeDropped, nil, nil
	case SeededPerTurnRetention:
		if rng.IntN(2) == 0 {
			return ModeDropped, nil, nil
		}
		return ModePreserved, cloneBlocks(observed), nil
	case ConflictReasoning:
		return ModeConflict, cloneBlocks(conflict), nil
	default:
		return "", nil, fmt.Errorf("unknown policy")
	}
}

func materializeClientMode(
	clientMode RetentionMode,
	observed []ReasoningBlock,
	conflict []ReasoningBlock,
) (RetentionMode, []ReasoningBlock, error) {
	switch clientMode {
	case ModeNone:
		if len(observed) != 0 {
			return "", nil, fmt.Errorf("client mode none requires empty observed reasoning")
		}
		return ModeNone, nil, nil
	case ModeDropped:
		if len(observed) == 0 {
			return ModeNone, nil, nil
		}
		return ModeDropped, nil, nil
	case ModePreserved:
		if len(observed) == 0 {
			return ModeNone, nil, nil
		}
		return ModePreserved, cloneBlocks(observed), nil
	case ModeConflict:
		if len(observed) == 0 {
			return ModeNone, nil, nil
		}
		if len(conflict) == 0 {
			return "", nil, fmt.Errorf("client mode conflict requires ConflictReasoning")
		}
		return ModeConflict, cloneBlocks(conflict), nil
	default:
		return "", nil, fmt.Errorf("unknown client mode %q", clientMode)
	}
}

func expectedBackend(mode RetentionMode, observed, submitted AssistantTurn) AssistantTurn {
	base := AssistantTurn{
		ID:          observed.ID,
		VisibleText: observed.VisibleText,
		Tool:        cloneTool(observed.Tool),
		Streaming:   observed.Streaming,
	}
	switch mode {
	case ModeNone:
		return base
	case ModePreserved, ModeDropped:
		base.Reasoning = cloneBlocks(observed.Reasoning)
		return base
	case ModeConflict:
		base.Reasoning = cloneBlocks(submitted.Reasoning)
		return base
	default:
		return base
	}
}

// Turns returns defensive copies of planned turns.
func (p Plan) Turns() []PlannedTurn {
	if len(p.turns) == 0 {
		return nil
	}
	out := make([]PlannedTurn, len(p.turns))
	for i := range p.turns {
		out[i] = clonePlannedTurn(p.turns[i])
	}
	return out
}

// ObservedTranscript returns a defensive copy of the immutable observed assistant turns.
func (p Plan) ObservedTranscript() []AssistantTurn {
	if len(p.turns) == 0 {
		return nil
	}
	out := make([]AssistantTurn, len(p.turns))
	for i := range p.turns {
		out[i] = cloneTurn(p.turns[i].Observed)
	}
	return out
}

// SubmittedTranscript returns a defensive copy of the materialized client-submitted turns.
func (p Plan) SubmittedTranscript() []AssistantTurn {
	if len(p.turns) == 0 {
		return nil
	}
	out := make([]AssistantTurn, len(p.turns))
	for i := range p.turns {
		out[i] = cloneTurn(p.turns[i].Submitted)
	}
	return out
}

func clonePlannedTurn(in PlannedTurn) PlannedTurn {
	return PlannedTurn{
		ID:              in.ID,
		Mode:            in.Mode,
		Observed:        cloneTurn(in.Observed),
		Submitted:       cloneTurn(in.Submitted),
		ExpectedBackend: cloneTurn(in.ExpectedBackend),
	}
}

func cloneTurn(in AssistantTurn) AssistantTurn {
	return AssistantTurn{
		ID:          in.ID,
		VisibleText: in.VisibleText,
		Reasoning:   cloneBlocks(in.Reasoning),
		Tool:        cloneTool(in.Tool),
		Streaming:   in.Streaming,
	}
}

func cloneBlocks(in []ReasoningBlock) []ReasoningBlock {
	if len(in) == 0 {
		return nil
	}
	out := make([]ReasoningBlock, len(in))
	for i := range in {
		out[i] = ReasoningBlock{
			Dialect:   in[i].Dialect,
			Text:      in[i].Text,
			Signature: in[i].Signature,
			Opaque:    cloneBytes(in[i].Opaque),
		}
	}
	return out
}

func cloneTool(in *ToolExchange) *ToolExchange {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}

func cloneBytes(in []byte) []byte {
	if len(in) == 0 {
		if in == nil {
			return nil
		}
		return []byte{}
	}
	out := make([]byte, len(in))
	copy(out, in)
	return out
}
