package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestPublicationClosedSentinel_ErrorsIs(t *testing.T) {
	t.Parallel()

	wrappedProducers := []struct {
		name string
		err  error
	}{
		{
			name: "executor_assemble_stream",
			err:  fmt.Errorf("runtime: %w", errPublicationClosed),
		},
		{
			name: "interleaved_open",
			err:  fmt.Errorf("interleaved open: %w", errPublicationClosed),
		},
		{
			name: "executor_recv_loop",
			err:  fmt.Errorf("recv loop: %w", errPublicationClosed),
		},
		{
			name: "turn_terminal",
			err:  fmt.Errorf("turn terminal: %w", errPublicationClosed),
		},
		{
			name: "continuation",
			err:  fmt.Errorf("continuation %w", errPublicationClosed),
		},
	}

	for _, tc := range wrappedProducers {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.err, errPublicationClosed) {
				t.Fatalf("expected errors.Is(err, errPublicationClosed) to be true for %s", tc.name)
			}
		})
	}
}

func TestPublicationClosedSentinel_ReadyDispose_WrappedSentinel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sess := &attemptSession{
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "aleg-1", BLegID: "bleg-1"},
		cand:     routing.AttemptCandidate{Key: "cand-1"},
	}
	ready := &readyAttempt{
		session: sess,
		state:   readyStatePrepared,
	}

	wrappedErr := fmt.Errorf("transport closed: %w", errPublicationClosed)
	ready.Dispose(ctx, wrappedErr)

	if !ready.IsConsumed() {
		t.Fatalf("expected ready attempt to be consumed/disposed")
	}

	outcome, ok := sess.terminal.Owner().Outcome()
	if !ok {
		t.Fatalf("expected outcome to be claimed")
	}
	if outcome.Command != sdkterminal.CommandSwallowedAttempt {
		t.Fatalf("expected command %s for wrapped errPublicationClosed, got %s",
			sdkterminal.CommandSwallowedAttempt, outcome.Command)
	}
}

func TestPublicationClosedSentinel_ReadyDispose_DecoyStringNotSwallowed(t *testing.T) {
	t.Parallel()

	decoys := []struct {
		name string
		err  error
	}{
		{
			name: "substring decoy",
			err:  errors.New("unrelated network drop: publication closed by peer"),
		},
		{
			name: "exact string distinct error instance",
			err:  errors.New("publication closed"),
		},
	}

	for _, tc := range decoys {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			sess := &attemptSession{
				terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
				bleg:     b2bua.BLegRecord{ALegID: "aleg-decoy", BLegID: "bleg-decoy"},
				cand:     routing.AttemptCandidate{Key: "cand-decoy"},
			}
			ready := &readyAttempt{
				session: sess,
				state:   readyStatePrepared,
			}

			ready.Dispose(ctx, tc.err)

			if !ready.IsConsumed() {
				t.Fatalf("expected ready attempt to be consumed/disposed")
			}

			outcome, ok := sess.terminal.Owner().Outcome()
			if !ok {
				t.Fatalf("expected outcome to be claimed")
			}
			// Decoy errors must NOT classify as SwallowedAttempt because they do not wrap errPublicationClosed.
			if outcome.Command == sdkterminal.CommandSwallowedAttempt {
				t.Fatalf("decoy error %q was erroneously classified as CommandSwallowedAttempt! Expected CommandBackendOpenFailure",
					tc.err.Error())
			}
			if outcome.Command != sdkterminal.CommandBackendOpenFailure {
				t.Fatalf("expected command %s for decoy error, got %s",
					sdkterminal.CommandBackendOpenFailure, outcome.Command)
			}
		})
	}
}
