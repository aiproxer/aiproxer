package reasoningpreservation

import (
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type RestoreResult struct {
	Mutated       bool
	RestoredCount int
	RestoredBytes int
	Exclude       bool
	ReasonCode    string
	Outcomes      []SafeOutcome
}

type RestoreInput struct {
	Action            string
	OnUnrepresentable string
	OnStateError      string
	Call              *aipapi.Call
	Artifacts         []TurnArtifact
	ReplaySupport     aipapi.ReasoningReplaySupport
	Eligible          bool
}

func RestoreMissingReasoning(in RestoreInput) (RestoreResult, error) {
	if in.Call == nil {
		return RestoreResult{}, fmt.Errorf("%s: call is required", ID)
	}
	// Unmatched / ineligible candidates stay fully inert: no restore and no outcomes.
	if !in.Eligible {
		return RestoreResult{}, nil
	}
	artifacts := cloneArtifacts(in.Artifacts)
	if in.Action == ActionObserve {
		if err := validateArtifacts(artifacts); err != nil {
			return RestoreResult{ReasonCode: "state_error", Outcomes: []SafeOutcome{OutcomeStateError}}, nil
		}
		classified, err := ClassifyAssistantTurns(in.Call.Messages, artifacts)
		if err != nil {
			return RestoreResult{ReasonCode: "state_error", Outcomes: []SafeOutcome{OutcomeStateError}}, nil
		}
		return RestoreResult{Outcomes: outcomesFromClassifications(classified, nil)}, nil
	}
	if in.Action != ActionRestore {
		return RestoreResult{}, fmt.Errorf("%s: unknown action %q", ID, in.Action)
	}
	classified, candidates, err := collectRestoreCandidates(in.Call, artifacts, in.ReplaySupport)
	if err != nil {
		return applyStateErrorPolicy(in.OnStateError)
	}
	return restoreWithCandidates(in, classified, candidates)
}

func restoreWithCandidates(in RestoreInput, classified []ClassifiedTurn, candidates []restoreCandidate) (RestoreResult, error) {
	if len(candidates) == 0 {
		return RestoreResult{Outcomes: outcomesFromClassifications(classified, nil)}, nil
	}
	for _, c := range candidates {
		if c.Unsupported {
			res, err := applyUnrepresentablePolicy(in.OnUnrepresentable)
			if res.ReasonCode != "" && len(res.Outcomes) == 0 {
				res.Outcomes = []SafeOutcome{OutcomeUnrepresentable}
			}
			return res, err
		}
	}
	work := aipapi.CloneCall(*in.Call)
	restoredCount := 0
	restoredBytes := 0
	restoredIDs := map[string]struct{}{}
	for _, c := range candidates {
		msg := work.Messages[c.MsgIndex]
		newParts, bytes, err := insertPlacements(msg.Parts, c.Artifact.Reasoning)
		if err != nil {
			return applyStateErrorPolicy(in.OnStateError)
		}
		work.Messages[c.MsgIndex].Parts = newParts
		restoredCount++
		restoredBytes += bytes
		restoredIDs[c.Artifact.ID] = struct{}{}
	}
	if err := work.Validate(); err != nil {
		return applyStateErrorPolicy(in.OnStateError)
	}
	in.Call.Messages = work.Messages
	return RestoreResult{
		Mutated:       true,
		RestoredCount: restoredCount,
		RestoredBytes: restoredBytes,
		Outcomes:      outcomesFromClassifications(classified, restoredIDs),
	}, nil
}

func outcomesFromClassifications(classified []ClassifiedTurn, restoredIDs map[string]struct{}) []SafeOutcome {
	out := make([]SafeOutcome, 0, len(classified))
	for _, c := range classified {
		switch c.Classification {
		case ClassMissing:
			if _, ok := restoredIDs[c.ArtifactID]; ok {
				out = append(out, OutcomeRestored)
			} else {
				out = append(out, OutcomeMissing)
			}
		case ClassPreserved:
			out = append(out, OutcomePreserved)
		case ClassConflicting:
			out = append(out, OutcomeConflicting)
		case ClassAmbiguous:
			out = append(out, OutcomeAmbiguous)
		case ClassUnmatched:
			out = append(out, OutcomeUnmatched)
		}
	}
	return out
}

func applyUnrepresentablePolicy(policy string) (RestoreResult, error) {
	switch policy {
	case PolicyReject, "":
		return RestoreResult{Exclude: true, ReasonCode: "unrepresentable_replay", Outcomes: []SafeOutcome{OutcomeUnrepresentable}}, nil
	case PolicyLogSkip:
		return RestoreResult{ReasonCode: "unrepresentable", Outcomes: []SafeOutcome{OutcomeUnrepresentable}}, nil
	default:
		return RestoreResult{}, fmt.Errorf("%s: unknown on_unrepresentable policy", ID)
	}
}

func applyStateErrorPolicy(policy string) (RestoreResult, error) {
	switch policy {
	case PolicyReject:
		return RestoreResult{Exclude: true, ReasonCode: "state_error", Outcomes: []SafeOutcome{OutcomeStateError}}, nil
	case PolicyLogSkip, "":
		return RestoreResult{ReasonCode: "state_error", Outcomes: []SafeOutcome{OutcomeStateError}}, nil
	default:
		return RestoreResult{}, fmt.Errorf("%s: unknown on_state_error policy", ID)
	}
}

func dialectSet(dialects []aipapi.ReasoningDialect) map[aipapi.ReasoningDialect]struct{} {
	out := make(map[aipapi.ReasoningDialect]struct{}, len(dialects))
	for _, d := range aipapi.NormalizeReasoningDialects(dialects) {
		out[d] = struct{}{}
	}
	return out
}

func insertPlacements(parts []aipapi.Part, placements []PlacedReasoning) ([]aipapi.Part, int, error) {
	nonReasoning := make([]aipapi.Part, 0, len(parts))
	for _, p := range parts {
		if p.Kind == aipapi.PartReasoning {
			continue
		}
		nonReasoning = append(nonReasoning, clonePart(p))
	}
	n := len(nonReasoning)
	byIndex := make(map[int][]aipapi.Part)
	bytes := 0
	for _, pr := range placements {
		if pr.BeforeNonReasoningPart < 0 || pr.BeforeNonReasoningPart > n {
			return nil, 0, fmt.Errorf("%s: placement out of range", ID)
		}
		if pr.Part.Kind != aipapi.PartReasoning || pr.Part.Reasoning == nil {
			return nil, 0, fmt.Errorf("%s: invalid placed reasoning", ID)
		}
		cp := clonePart(pr.Part)
		byIndex[pr.BeforeNonReasoningPart] = append(byIndex[pr.BeforeNonReasoningPart], cp)
		bytes += aipapi.ReasoningPayloadBytes(cp.Reasoning)
	}
	out := make([]aipapi.Part, 0, len(nonReasoning)+len(placements))
	for i := 0; i <= n; i++ {
		out = append(out, byIndex[i]...)
		if i < n {
			out = append(out, nonReasoning[i])
		}
	}
	return out, bytes, nil
}
