package completion

import (
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// OutcomeKind is the typed gate decision (design §6).
type OutcomeKind int

const (
	// OutcomePassOriginal leaves the current buffered completion unchanged for downstream gates / emit.
	OutcomePassOriginal OutcomeKind = iota
	// OutcomeReplace substitutes the completion with Events (must end with EventResponseFinished when stream completed).
	OutcomeReplace
	// OutcomeReplayOriginal resets the working buffer to the original completion snapshot (design §17 incomplete replay).
	OutcomeReplayOriginal
	// OutcomeReject fails the response path with Err (surfaced like hook rejection).
	OutcomeReject
)

// Outcome is the result of one gate invocation.
type Outcome struct {
	Kind OutcomeKind
	// Events is the replacement stream when Kind == OutcomeReplace.
	Events []aipapi.Event
	// Err is required when Kind == OutcomeReject.
	Err error
}

// Validate checks invariants for the outcome kind.
func (o Outcome) Validate() error {
	switch o.Kind {
	case OutcomePassOriginal, OutcomeReplayOriginal:
		if len(o.Events) != 0 {
			return errors.New("completion: pass original or replay original outcome must not set events")
		}
		if o.Err != nil {
			return errors.New("completion: pass original or replay original outcome must not set err")
		}
	case OutcomeReplace:
		if len(o.Events) == 0 {
			return errors.New("completion: replace outcome requires non-empty events")
		}
		if o.Err != nil {
			return errors.New("completion: replace outcome must not set err")
		}
	case OutcomeReject:
		if o.Err == nil {
			return errors.New("completion: reject outcome requires err")
		}
		if len(o.Events) != 0 {
			return errors.New("completion: reject outcome must not set events")
		}
	default:
		return errors.New("completion: unknown outcome kind")
	}
	return nil
}

// ReplaceOutcome builds an OutcomeReplace. Events must be non-empty; callers should end with EventResponseFinished.
func ReplaceOutcome(events []aipapi.Event) Outcome {
	return Outcome{Kind: OutcomeReplace, Events: events}
}

// RejectOutcome builds an OutcomeReject.
func RejectOutcome(err error) Outcome {
	return Outcome{Kind: OutcomeReject, Err: err}
}

// PassOriginalOutcome returns OutcomePassOriginal.
func PassOriginalOutcome() Outcome {
	return Outcome{Kind: OutcomePassOriginal}
}

// ReplayOriginalOutcome returns OutcomeReplayOriginal.
func ReplayOriginalOutcome() Outcome {
	return Outcome{Kind: OutcomeReplayOriginal}
}

// LastEventKind returns the Kind of the last event, or empty string if none.
func LastEventKind(events []aipapi.Event) aipapi.EventKind {
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1].Kind
}
