package runtime_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/require"
)

// panickingReplayBackend is a backend whose plugin-boundary capability resolver
// panics for this candidate only. The resolver runs while the candidate is
// evaluated, before the leg owns an attempt transaction. evaluated is closed
// first so a test can order the panic ahead of the winning leg's first event.
func panickingReplayBackend(t *testing.T, evaluated func()) execbackend.Backend {
	t.Helper()
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		ResolveReplaySupport: func(context.Context, aipapi.Call, routing.AttemptCandidate) aipapi.ReasoningReplaySupport {
			if evaluated != nil {
				evaluated()
			}
			panic("replay resolver panic")
		},
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			t.Error("open must not be reached for a candidate whose evaluation panicked")
			return nil, context.Canceled
		},
	}
}

// gatedWinnerBackend opens only after the losing leg evaluated its candidate
// and after the isolated-panic log was emitted. Without that ordering the
// winner can commit first: the losing leg is then cancelled before its
// candidate is evaluated, so it never panics, and nothing synchronizes the
// background log the assertion waits for.
func gatedWinnerBackend(events []aipapi.Event, evaluated <-chan struct{}, logged <-chan struct{}) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			for _, gate := range []<-chan struct{}{evaluated, logged} {
				select {
				case <-gate:
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(5 * time.Second):
					return nil, errors.New("winner gate: losing leg did not reach the expected stage")
				}
			}
			return aipapi.NewFixedEventStream(events), nil
		},
	}
}

func isolatedPanicLogger(observed *atomic.Bool, logged chan<- struct{}) *slog.Logger {
	var once sync.Once
	return slog.New(&slogWrapper{
		Handler: slog.NewTextHandler(io.Discard, nil),
		onLog: func(msg string) {
			if strings.Contains(strings.ToLower(msg), "parallel race leg") {
				observed.Store(true)
				once.Do(func() { close(logged) })
			}
		},
	})
}

// A backend capability resolver is plugin-boundary code. It is invoked while a
// parallel leg evaluates its candidate, which happens before the leg owns an
// attempt transaction and on a goroutine the HTTP recovery middleware cannot
// see. A panic there must stay inside the leg: the process must survive and the
// unaffected leg must still win.
func TestParallelRace_IsolatesPanicDuringCandidateEvaluation(t *testing.T) {
	t.Parallel()
	ex := runtime.TestExecutor()
	ex.Store = parallelStore(t)
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	var panicObserved atomic.Bool
	evaluated := make(chan struct{})
	logged := make(chan struct{})
	ex.Log = isolatedPanicLogger(&panicObserved, logged)
	ex.Backends = map[string]execbackend.Backend{
		"panicky": panickingReplayBackend(t, func() { close(evaluated) }),
		"winner":  gatedWinnerBackend(completionEvents("winner-response"), evaluated, logged),
	}

	stream, err := ex.Execute(t.Context(), parallelCall("panicky:model!winner:model"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	collected, err := aipapi.Collect(t.Context(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got := collected.Text.String(); got != "winner-response" {
		t.Fatalf("winner text: %q, want winner-response", got)
	}
	require.True(t, panicObserved.Load(),
		"expected the panicking leg to be logged as an isolated parallel race leg panic")
}
