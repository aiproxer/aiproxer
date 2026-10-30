package reasoningpreservation

import (
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type PlacedReasoning struct {
	BeforeNonReasoningPart int
	Part                   aipapi.Part
}

type TurnArtifact struct {
	ID             string
	Anchor         [32]byte
	SourceBackend  string
	SourceModel    string
	Reasoning      []PlacedReasoning
	CreatedAt      time.Time
	ReasoningBytes int
}
