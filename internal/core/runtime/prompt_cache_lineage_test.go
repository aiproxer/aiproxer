package runtime

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// lineageSpyMaintenance captures the successful-turn handoff so a test can
// assert exactly which observations the keep-warm owner was allowed to arm.
type lineageSpyMaintenance struct {
	turns []PromptCacheCommittedTurn
}

func (s *lineageSpyMaintenance) BeginRealTurn(string) {}
func (s *lineageSpyMaintenance) EndSession(string)    {}

func (s *lineageSpyMaintenance) ArmCommittedTurn(turn PromptCacheCommittedTurn) {
	s.turns = append(s.turns, turn)
}

// staticObservationSource hands the maintenance boundary one producer-declared
// observation without the adapter buffer's own shape validation, so the
// lineage reconciliation is exercised on its own.
type staticObservationSource struct {
	observations []promptcache.Observation
}

func (s *staticObservationSource) DrainPromptCacheObservations() []promptcache.Observation {
	out := s.observations
	s.observations = nil
	return out
}

func commitLineageTurn(t *testing.T, observation promptcache.Observation) *lineageSpyMaintenance {
	t.Helper()
	spy := &lineageSpyMaintenance{}
	pipeline := newResponsePipeline()
	pipeline.promptCacheMaintenance = spy
	pipeline.committedTools = osToolLike()
	attempt := newAttemptSession(attemptSessionInput{})
	attempt.bleg.BLegID = "b-leg-trusted"
	attempt.cand.Key = "connector:model-x"
	attempt.cand.Primary.Backend = "connector"
	attempt.cand.Primary.Model = "model-x"
	attempt.promptCacheSource = &staticObservationSource{observations: []promptcache.Observation{observation}}
	attempt.promptCacheController = &fixedResultController{}
	// Same cleanup and real response-to-maintenance boundary as production.
	attempt.discardSidebandState()
	pipeline.commitSuccessfulTurn(recvTurnFacts{aLegID: "a-leg-trusted"}, attempt, true)
	return spy
}

func osToolLike() []aipapi.ToolEvent {
	return []aipapi.ToolEvent{{Kind: aipapi.ToolEventFinished, ToolCallID: "t", ToolName: "bash", Category: aipapi.ToolCategoryOSCommand}}
}

func lineageObservation(aLeg, bLeg, backend string) promptcache.Observation {
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	return promptcache.Observation{
		ALegID: aLeg, BLegID: bLeg, BackendInstanceID: backend,
		TargetID: "target-1", GenerationID: "generation-1",
		Lifecycle: promptcache.LifecycleSlidingExpiry,
		Timing:    promptcache.Timing{ObservedAt: now, ExpiresAt: &expires},
		Renewable: true, Handle: promptcache.Handle("live-handle"),
	}
}

// TestCommitSuccessfulTurnRejectsConflictingObservationLineage pins the
// no-laundering rule at the real caller boundary: a producer-declared
// observation that claims another session or another backend instance must
// never be rewritten into the current attempt's facts, so the keep-warm owner
// is never asked to arm it.
func TestCommitSuccessfulTurnRejectsConflictingObservationLineage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		obs  promptcache.Observation
	}{
		{"foreign_backend", lineageObservation("a-leg-trusted", "b-leg-trusted", "other-backend")},
		{"foreign_session_legs", lineageObservation("other-tenant-session", "other-turn", "connector")},
		{"foreign_b_leg", lineageObservation("a-leg-trusted", "route:other-model", "connector")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spy := commitLineageTurn(t, tc.obs)
			if len(spy.turns) != 0 {
				t.Fatalf("conflicting observation became trusted and armed: %+v", spy.turns)
			}
		})
	}
}

// TestCommitSuccessfulTurnBindsValidatedConnectorPseudonym proves the two
// allowed origins: a native producer that already reports the trusted B-leg,
// and the exact transport pseudonym the host supplied to that connector. In
// both cases the armed observation carries the trusted attempt lineage while
// target/handle identity is preserved, and empty fields are filled from the
// attempt rather than invented.
func TestCommitSuccessfulTurnBindsValidatedConnectorPseudonym(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		obs         promptcache.Observation
		wantALeg    string
		wantBLeg    string
		wantBackend string
	}{
		{"native_real_b_leg", lineageObservation("a-leg-trusted", "b-leg-trusted", "connector"), "a-leg-trusted", "b-leg-trusted", "connector"},
		{"connector_pseudonym", lineageObservation("a-leg-trusted", "connector:model-x", "connector"), "a-leg-trusted", "b-leg-trusted", "connector"},
		{"empty_fields_filled", lineageObservation("", "", ""), "a-leg-trusted", "b-leg-trusted", "connector"},
		{"empty_b_leg_filled", lineageObservation("a-leg-trusted", "", "connector"), "a-leg-trusted", "b-leg-trusted", "connector"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spy := commitLineageTurn(t, tc.obs)
			if len(spy.turns) != 1 {
				t.Fatalf("armed turns = %d, want 1", len(spy.turns))
			}
			turn := spy.turns[0]
			if turn.ALegID != "a-leg-trusted" || turn.BLegID != "b-leg-trusted" || turn.BackendInstanceID != "connector" {
				t.Fatalf("trusted turn lineage = %+v", turn)
			}
			if len(turn.Observations) != 1 {
				t.Fatalf("armed observations = %d, want 1", len(turn.Observations))
			}
			armed := turn.Observations[0]
			if armed.ALegID != tc.wantALeg || armed.BLegID != tc.wantBLeg || armed.BackendInstanceID != tc.wantBackend {
				t.Fatalf("armed lineage = %+v, want %s/%s/%s", armed, tc.wantALeg, tc.wantBLeg, tc.wantBackend)
			}
			if armed.TargetID != tc.obs.TargetID || armed.GenerationID != tc.obs.GenerationID || string(armed.Handle) != string(tc.obs.Handle) {
				t.Fatalf("target/handle identity changed: %+v", armed)
			}
		})
	}
}
