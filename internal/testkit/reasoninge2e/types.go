package reasoninge2e

import "fmt"

// RetentionPolicy controls how the client materializes previously observed assistant reasoning.
type RetentionPolicy int

const (
	// PreserveAllReasoning keeps every observed reasoning block in the submitted transcript.
	PreserveAllReasoning RetentionPolicy = iota
	// DropAllReasoning strips all observed reasoning from the submitted transcript.
	DropAllReasoning
	// SeededPerTurnRetention keeps or drops each turn's reasoning using a seeded PRNG.
	SeededPerTurnRetention
	// ConflictReasoning submits alternate reasoning that differs from the observed blocks.
	ConflictReasoning
)

func (p RetentionPolicy) String() string {
	switch p {
	case PreserveAllReasoning:
		return "preserve_all"
	case DropAllReasoning:
		return "drop_all"
	case SeededPerTurnRetention:
		return "seeded_per_turn"
	case ConflictReasoning:
		return "conflict"
	default:
		return fmt.Sprintf("RetentionPolicy(%d)", int(p))
	}
}

// RetentionMode is the planned fate of one assistant turn's reasoning.
type RetentionMode string

const (
	// ModeNone means the observed turn had no reasoning; nothing may be inserted later.
	ModeNone RetentionMode = "none"
	// ModePreserved means the client still carries the observed reasoning.
	ModePreserved RetentionMode = "preserved"
	// ModeDropped means the client omitted observed reasoning (restore candidate).
	ModeDropped RetentionMode = "dropped"
	// ModeConflict means the client submitted conflicting reasoning that must stay untouched.
	ModeConflict RetentionMode = "conflict"
)

// ReasoningBlock is one ordered reasoning payload on an assistant turn.
type ReasoningBlock struct {
	Dialect   string
	Text      string
	Signature string
	Opaque    []byte
}

// ToolExchange is an optional tool call plus result attached to an assistant turn.
type ToolExchange struct {
	ID        string
	Name      string
	Arguments string
	Result    string
}

// AssistantTurn is one assistant turn in an observed or submitted transcript.
// Streaming is plan/client metadata (how the turn was produced); Check does not
// read it from BackendTurnObservation.
type AssistantTurn struct {
	ID          string
	VisibleText string
	Reasoning   []ReasoningBlock
	Tool        *ToolExchange
	Streaming   bool
}

// TurnSpec is the explicit seed input for one observed assistant turn.
type TurnSpec struct {
	VisibleText       string
	Reasoning         []ReasoningBlock
	ConflictReasoning []ReasoningBlock
	Tool              *ToolExchange
	Streaming         bool
	// ClientMode, when non-empty, overrides policy-derived retention for this turn.
	ClientMode RetentionMode
}

// PlanConfig is the explicit deterministic plan input.
type PlanConfig struct {
	Seed   uint64
	Policy RetentionPolicy
	Turns  []TurnSpec
}

// PlannedTurn is the precomputed expectation for one assistant turn.
type PlannedTurn struct {
	ID              string
	Mode            RetentionMode
	Observed        AssistantTurn
	Submitted       AssistantTurn
	ExpectedBackend AssistantTurn
}

// Plan is a precomputed deterministic transcript plan.
type Plan struct {
	Seed   uint64
	Policy RetentionPolicy
	turns  []PlannedTurn
}

// BackendTurnObservation is one historical assistant turn as seen on a backend-bound request.
// It has no Streaming field: stream vs non-stream wire shape is out of oracle scope.
type BackendTurnObservation struct {
	TurnID      string
	VisibleText string
	Reasoning   []ReasoningBlock
	Tool        *ToolExchange
}

// BackendRequestObservation is the oracle input for one backend-bound request.
type BackendRequestObservation struct {
	AssistantTurns []BackendTurnObservation
}
