package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestTask33TurnTerminalBaseOwnsALegEndExactlyOnceAcrossCompetingFinishPaths(t *testing.T) {
	t.Parallel()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	const aLegID = "task33-base-end"
	aLeg := coord.StartALeg(aLegID)
	turn := newTurnTerminalWithALeg(aLeg, aLegEndBase)
	attempt := newAttemptSession(attemptSessionInput{
		bleg: b2bua.BLegRecord{ALegID: aLegID, BLegID: "b-task33", Seq: 1},
		cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
	})
	snap := func() coreterm.AccumulatorSnapshot {
		return coreterm.NewAccumulatorSnapshot(nil, false)
	}

	results := make(chan coreterm.Result, 2)
	var wg sync.WaitGroup
	for _, cmd := range []sdkterminal.Command{sdkterminal.CommandClose, sdkterminal.CommandEOF} {
		wg.Go(func() {
			results <- testTurnTerminalize(context.Background(), turn, cmd, attempt, snap, nil)
		})
	}
	wg.Wait()
	close(results)
	var winner coreterm.Result
	for result := range results {
		if result.Won {
			winner = result
		}
	}
	if !winner.Won || turn.requestTerminal().Owner().State() != sdkterminal.StateReleased {
		t.Fatalf("terminal winner=%+v request state=%q, want one released winner", winner, turn.requestTerminal().Owner().State())
	}
	if !turn.endALeg(aLegEndBase) {
		t.Fatal("base owner must end A-leg at the terminal boundary")
	}
	if turn.endALeg(aLegEndBase) {
		t.Fatal("competing base finish path must not end A-leg twice")
	}
	if replacement := coord.StartALeg(aLegID); replacement == aLeg {
		t.Fatal("A-leg remains registered after exactly-once end")
	}
}

func TestTask33TurnTerminalOuterInterleavedOwnershipSuppressesBaseEnd(t *testing.T) {
	t.Parallel()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	const aLegID = "task33-outer-end"
	aLeg := coord.StartALeg(aLegID)
	turn := newTurnTerminalWithALeg(aLeg, aLegEndBase)
	if !turn.deferALegEndToOuter() {
		t.Fatal("interleaved construction must transfer A-leg end ownership to outer wrapper")
	}
	attempt := newAttemptSession(attemptSessionInput{
		bleg: b2bua.BLegRecord{ALegID: aLegID, BLegID: "b-task33", Seq: 1},
		cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
	})
	result := testTurnTerminalize(context.Background(), turn, sdkterminal.CommandClose, attempt,
		func() coreterm.AccumulatorSnapshot { return coreterm.NewAccumulatorSnapshot(nil, false) }, nil)
	if !result.Won || turn.requestTerminal().Owner().State() != sdkterminal.StateReleased {
		t.Fatalf("outer terminal result=%+v request state=%q, want released winner", result, turn.requestTerminal().Owner().State())
	}
	if turn.endALeg(aLegEndBase) {
		t.Fatal("base terminal must not end an A-leg owned by outer interleaved wrapper")
	}
	if !turn.endALeg(aLegEndOuter) {
		t.Fatal("outer owner must end A-leg at the combined boundary")
	}
	if turn.endALeg(aLegEndOuter) {
		t.Fatal("outer competing finish path must not end A-leg twice")
	}
	if replacement := coord.StartALeg(aLegID); replacement == aLeg {
		t.Fatal("A-leg remains registered after outer exactly-once end")
	}
}

func TestTask33TurnTerminalSharedOuterALegAuthorityAcrossThinkerAndExecutor(t *testing.T) {
	t.Parallel()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	const aLegID = "task33-shared-outer-end"
	aLeg := coord.StartALeg(aLegID)
	thinker := newTurnTerminalWithALeg(aLeg, aLegEndBase)
	if !thinker.deferALegEndToOuter() {
		t.Fatal("thinker construction must transfer A-leg ownership to outer wrapper")
	}
	executor := newTurnTerminalWithSharedALeg(thinker)
	ends := make(chan bool, 2)
	go func() { ends <- thinker.endALeg(aLegEndOuter) }()
	go func() { ends <- executor.endALeg(aLegEndOuter) }()
	wins := 0
	for range 2 {
		if <-ends {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("thinker/executor shared outer end winners=%d, want exactly one", wins)
	}
	if replacement := coord.StartALeg(aLegID); replacement == aLeg {
		t.Fatal("shared A-leg remains registered after outer end")
	}
}
