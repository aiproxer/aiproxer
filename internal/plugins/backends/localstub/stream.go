package localstub

import (
	"context"
	"errors"

	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// errPostTextStream is returned after client-visible text when StreamErrorAfterTextDelta is set.
var errPostTextStream = errors.New("local-stub: simulated stream failure after text delta")

const stubToolCallID = "stub-tool-1"

// canonicalEvents returns the ordered canonical stream for one completed assistant turn.
func canonicalEvents(cfg Config) []aipapi.Event {
	evs := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: cfg.Text},
	}
	if cfg.ToolName != "" {
		args := cfg.ToolArgs
		if args == "" {
			args = DefaultToolArgs
		}
		evs = append(
			evs,
			aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: stubToolCallID, ToolName: cfg.ToolName},
			aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: stubToolCallID, Delta: args},
			aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: stubToolCallID},
		)
	}
	evs = append(
		evs,
		aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: cfg.InputTokens, OutputTokens: cfg.OutputTokens},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)
	return evs
}

// eventStreamForConfig returns a fixed canonical stream or a truncated-error stream for tests.
func eventStreamForConfig(cfg Config) aipapi.ManagedEventStream {
	if !cfg.StreamErrorAfterTextDelta {
		return aipapi.NewFixedEventStream(canonicalEvents(cfg))
	}
	evs := canonicalEvents(cfg)
	prefix := make([]aipapi.Event, 0, len(evs))
	for _, e := range evs {
		prefix = append(prefix, e)
		if e.Kind == aipapi.EventTextDelta {
			break
		}
	}
	return &errorAfterPrefixStream{events: prefix}
}

type errorAfterPrefixStream struct {
	events []aipapi.Event
	i      int
}

func (s *errorAfterPrefixStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	if s.i >= len(s.events) {
		return aipapi.Event{}, errPostTextStream
	}
	e := s.events[s.i]
	s.i++
	return e, nil
}

func (s *errorAfterPrefixStream) Close() error { return nil }

func (s *errorAfterPrefixStream) Cancel(context.Context, leglifecycle.CancelCause) leglifecycle.CancelResult {
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeNone}
}
