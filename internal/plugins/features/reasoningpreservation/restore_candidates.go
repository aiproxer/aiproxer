package reasoningpreservation

import (
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// restoreCandidate is the shared classification result used by both restore and poll.
// It preserves exact restore semantics: missing artifact ID is a state error,
// unsupported dialect is flagged for unrepresentable policy, and classification
// is performed once via ClassifyAssistantTurns.
type restoreCandidate struct {
	MsgIndex       int
	Artifact       TurnArtifact
	ArtifactID     string
	Classification Classification
	Unsupported    bool
}

// restorableArtifacts returns the ordered supported ClassMissing artifacts
// sharing the same classification as restore. It is the poll-facing filtered
// view of collectRestoreCandidates.
func restorableArtifacts(call *aipapi.Call, artifacts []TurnArtifact, support aipapi.ReasoningReplaySupport) ([]TurnArtifact, error) {
	_, candidates, err := collectRestoreCandidates(call, artifacts, support)
	if err != nil {
		return nil, err
	}
	var out []TurnArtifact
	for _, c := range candidates {
		if c.Unsupported {
			continue
		}
		out = append(out, c.Artifact)
	}
	return out, nil
}

// collectRestoreCandidates classifies assistant turns and builds the ordered
// candidate list for ClassMissing. It returns the full classified slice and the
// candidate list with Unsupported flagged per dialect support. Missing artifact ID
// or assistant/classified length mismatch is returned as a state-error sentinel.
func collectRestoreCandidates(call *aipapi.Call, artifacts []TurnArtifact, support aipapi.ReasoningReplaySupport) (classified []ClassifiedTurn, candidates []restoreCandidate, err error) {
	if call == nil {
		return nil, nil, fmt.Errorf("%s: call is required", ID)
	}
	artifactsCloned := cloneArtifacts(artifacts)
	if err := validateArtifacts(artifactsCloned); err != nil {
		return nil, nil, err
	}
	classified, err = ClassifyAssistantTurns(call.Messages, artifactsCloned)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]TurnArtifact, len(artifactsCloned))
	for _, a := range artifactsCloned {
		byID[a.ID] = a
	}
	supported := dialectSet(support.Dialects)
	assistantIdx := 0
	for i, m := range call.Messages {
		if m.Role != aipapi.RoleAssistant {
			continue
		}
		if assistantIdx >= len(classified) {
			return nil, nil, fmt.Errorf("%s: classified length mismatch", ID)
		}
		c := classified[assistantIdx]
		assistantIdx++
		if c.Classification != ClassMissing {
			continue
		}
		art, ok := byID[c.ArtifactID]
		if !ok {
			return nil, nil, fmt.Errorf("%s: missing artifact %q", ID, c.ArtifactID)
		}
		unsupported := false
		for _, pr := range art.Reasoning {
			if pr.Part.Reasoning == nil {
				continue
			}
			d := aipapi.NormalizeReasoningDialect(pr.Part.Reasoning.Dialect)
			if _, ok := supported[d]; !ok {
				unsupported = true
				break
			}
		}
		candidates = append(candidates, restoreCandidate{
			MsgIndex:       i,
			Artifact:       art,
			ArtifactID:     c.ArtifactID,
			Classification: c.Classification,
			Unsupported:    unsupported,
		})
	}
	return classified, candidates, nil
}
