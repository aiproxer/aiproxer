package runtime_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// panickingReplayBackend is a backend whose plugin-boundary capability resolver
// panics for this candidate only. The resolver runs while the candidate is
// evaluated, before the leg owns an attempt transaction.
func panickingReplayBackend(t *testing.T) execbackend.Backend {
	t.Helper()
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		ResolveReplaySupport: func(context.Context, aipapi.Call, routing.AttemptCandidate) aipapi.ReasoningReplaySupport {
			panic("replay resolver panic")
		},
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			t.Error("open must not be reached for a candidate whose evaluation panicked")
			return nil, context.Canceled
		},
	}
}

func isolatedPanicLogger(observed *atomic.Bool) *slog.Logger {
	return slog.New(&slogWrapper{
		Handler: slog.NewTextHandler(io.Discard, nil),
		onLog: func(msg string) {
			if strings.Contains(strings.ToLower(msg), "parallel race leg") {
				observed.Store(true)
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
	ex.Log = isolatedPanicLogger(&panicObserved)
	ex.Backends = map[string]execbackend.Backend{
		"panicky": panickingReplayBackend(t),
		"winner":  parallelBackend(completionEvents("winner-response")),
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
	if !panicObserved.Load() {
		t.Fatal("expected the panicking leg to be logged as an isolated parallel race leg panic")
	}
}
