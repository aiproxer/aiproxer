package reasoninge2e

import "fmt"

// CheckPrefixRetention validates a plan prefix against a backend observation while
// modeling FIFO retention of artifact-producing turns (non-empty observed reasoning).
//
// maxArtifactTurns is the store bound (newest N artifact turns retained) and must be > 0.
// Use CheckPrefix for unbounded/eviction-blind validation.
//
// ModeDropped expects restoration only while retained; after eviction it expects
// absence. ModePreserved / ModeConflict / ModeNone keep CheckPrefix semantics.
// The immutable plan ExpectedBackend fields are never mutated.
func CheckPrefixRetention(plan Plan, obs BackendRequestObservation, maxArtifactTurns int) error {
	if maxArtifactTurns <= 0 {
		return fmt.Errorf(
			"reasoninge2e oracle: seed=%d policy=%s structural mismatch: invalid_max_artifact_turns value=%d",
			plan.Seed, plan.Policy.String(), maxArtifactTurns,
		)
	}
	want := plan.Turns()
	if len(obs.AssistantTurns) > len(want) {
		return fmt.Errorf(
			"reasoninge2e oracle: seed=%d policy=%s structural mismatch: turn_count got=%d want<=%d",
			plan.Seed, plan.Policy.String(), len(obs.AssistantTurns), len(want),
		)
	}
	histLen := len(obs.AssistantTurns)
	requestTurn := histLen
	requestTurnID := ""
	if requestTurn < len(want) {
		requestTurnID = want[requestTurn].ID
	}
	retained := retainedArtifactIDs(want, histLen, maxArtifactTurns)
	for i := range obs.AssistantTurns {
		_, ok := retained[want[i].ID]
		if err := checkTurnRetention(plan.Seed, requestTurnID, requestTurn, i, want[i], obs.AssistantTurns[i], ok); err != nil {
			return err
		}
	}
	return nil
}

func retainedArtifactIDs(turns []PlannedTurn, histLen, maxArtifactTurns int) map[string]struct{} {
	if histLen > len(turns) {
		histLen = len(turns)
	}
	var artifactIDs []string
	for i := 0; i < histLen; i++ {
		if len(turns[i].Observed.Reasoning) == 0 {
			continue
		}
		artifactIDs = append(artifactIDs, turns[i].ID)
	}
	if len(artifactIDs) > maxArtifactTurns {
		artifactIDs = artifactIDs[len(artifactIDs)-maxArtifactTurns:]
	}
	out := make(map[string]struct{}, len(artifactIDs))
	for _, id := range artifactIDs {
		out[id] = struct{}{}
	}
	return out
}

func expectedBackendWithRetention(want PlannedTurn, retained bool) AssistantTurn {
	base := cloneTurn(want.ExpectedBackend)
	if want.Mode == ModeDropped && !retained {
		base.Reasoning = nil
	}
	return base
}

func checkTurnRetention(
	seed uint64,
	requestTurnID string,
	requestTurn, historyTurn int,
	want PlannedTurn,
	got BackendTurnObservation,
	retained bool,
) error {
	adjusted := expectedBackendWithRetention(want, retained)
	state := "retained"
	if len(want.Observed.Reasoning) == 0 {
		state = "none"
	} else if !retained {
		state = "evicted"
	}
	prefix := fmt.Sprintf(
		"reasoninge2e oracle: seed=%d mode=%s request_turn_id=%s request_turn=%d history_turn_id=%s history_turn=%d artifact_state=%s",
		seed, want.Mode, requestTurnID, requestTurn, want.ID, historyTurn, state,
	)
	if got.TurnID != want.ID {
		return fmt.Errorf("%s structural mismatch: turn_id", prefix)
	}
	if got.VisibleText != adjusted.VisibleText {
		return fmt.Errorf("%s structural mismatch: visible_text", prefix)
	}
	if err := checkTool(prefix, adjusted.Tool, got.Tool); err != nil {
		return err
	}
	mode := want.Mode
	if mode == ModeDropped && !retained {
		mode = ModeNone
	}
	return checkReasoning(prefix, mode, adjusted.Reasoning, got.Reasoning)
}
