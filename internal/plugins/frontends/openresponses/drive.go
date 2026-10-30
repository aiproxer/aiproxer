package openresponses

import (
	"context"
	"errors"
	"fmt"
	"io"

	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var (
	errDriveTooManyEvents     = errors.New("canonical response exceeded event limit")
	errDriveInvalidEnvelope   = errors.New("canonical response contained an invalid event")
	errDriveTooMuchText       = errors.New("canonical response exceeded text limit")
	errDriveTooMuchTool       = errors.New("canonical response exceeded tool argument limit")
	errDriveTooManyItems      = errors.New("canonical response exceeded item limit")
	errDriveNoTerminal        = errors.New("canonical response ended without terminal event")
	errDriveMultipleTerminal  = errors.New("canonical response contained multiple terminal events")
	errDriveSequenceInvalid   = errors.New("canonical response sequence was invalid")
	errDriveNilStream         = errors.New("nil canonical event stream")
	errDriveStreamCloseFailed = errors.New("canonical response stream close failed")
	errDriveBuildResource     = errors.New("failed to build response resource")
	errDriveBuildCompact      = errors.New("failed to build compact resource")
	errDriveCommittedWrite    = errors.New("openresponses: committed stream write failed")
	errDriveSessionWrite      = errors.New("openresponses: websocket session write failed")
)

type driveLimits struct {
	maxEvents    int
	maxItems     int
	maxTextBytes int
	maxToolBytes int
}

type driveOptions struct {
	limits              driveLimits
	stopOnTerminal      bool
	errorEventIsFailure bool
	checkContext        bool
	sanitizeError       bool
}

func collectDriveLimits() driveLimits {
	return driveLimits{
		maxEvents:    maxNonStreamingEvents,
		maxItems:     maxNonStreamingItems,
		maxTextBytes: maxNonStreamingTextBytes,
		maxToolBytes: maxNonStreamingToolBytes,
	}
}

func compactDriveLimits() driveLimits {
	return driveLimits{
		maxEvents:    maxCompactEvents,
		maxItems:     maxCompactItems,
		maxTextBytes: maxCompactTextBytes,
		maxToolBytes: maxCompactToolBytes,
	}
}

func streamingDriveLimits() driveLimits {
	return driveLimits{maxEvents: maxStreamingEvents}
}

func isCanonicalTerminal(ev aipapi.Event) bool {
	return ev.Kind == aipapi.EventError || ev.Kind == aipapi.EventResponseFinished
}

func driveStateMachine(
	ctx context.Context,
	stream aipapi.EventStream,
	sm *proto.StateMachine,
	opts driveOptions,
	onEvent func(aipapi.Event, []proto.StreamEvent) error,
) (aipapi.Event, error) {
	if stream == nil {
		return aipapi.Event{}, errDriveNilStream
	}
	var (
		textBytes    int
		toolBytes    int
		terminalSeen bool
		last         aipapi.Event
	)
	for eventCount := 0; eventCount < opts.limits.maxEvents; eventCount++ {
		if opts.checkContext {
			if err := ctx.Err(); err != nil {
				return last, err
			}
		}
		ev, err := stream.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Prefer cancellation/deadline over EOF so callers keep cleanup
				// semantics instead of treating a canceled close as a stream fault.
				if cerr := ctx.Err(); cerr != nil {
					return last, cerr
				}
				// EOF decision table (errorEventIsFailure, stopOnTerminal):
				// - terminalSeen && !stopOnTerminal → success (compact allows EOF after terminal)
				// - !terminalSeen && (errorEventIsFailure || !stopOnTerminal) → errDriveNoTerminal
				// - otherwise → raw io.EOF (streaming callers handle committed vs uncommitted)
				if terminalSeen && !opts.stopOnTerminal {
					return last, nil
				}
				if !terminalSeen && (opts.errorEventIsFailure || !opts.stopOnTerminal) {
					return last, errDriveNoTerminal
				}
			}
			return last, err
		}
		if err := aipapi.ValidateEventEnvelope(&ev); err != nil {
			return last, errDriveInvalidEnvelope
		}
		switch ev.Kind {
		case aipapi.EventTextDelta, aipapi.EventReasoningDelta:
			if opts.limits.maxTextBytes > 0 {
				textBytes += len(ev.Delta)
				if textBytes > opts.limits.maxTextBytes {
					return last, errDriveTooMuchText
				}
			}
		case aipapi.EventToolCallArgsDelta:
			if opts.limits.maxToolBytes > 0 {
				toolBytes += len(ev.Delta)
				if toolBytes > opts.limits.maxToolBytes {
					return last, errDriveTooMuchTool
				}
			}
		}
		if ev.Kind == aipapi.EventError {
			if ev.ErrorCode == "" {
				ev.ErrorCode = "backend_error"
			}
			if opts.sanitizeError {
				ev.ErrorMessage = safeCanonicalErrorMessage(ev.ErrorMessage)
			}
		}
		events, processErr := sm.ProcessCanonicalEvent(ev)
		if processErr != nil {
			if opts.errorEventIsFailure {
				return last, fmt.Errorf("%w: %w", errDriveSequenceInvalid, processErr)
			}
			return last, processErr
		}
		if opts.limits.maxItems > 0 && len(sm.Trajectory()) > opts.limits.maxItems {
			return last, errDriveTooManyItems
		}
		if onEvent != nil {
			if err := onEvent(ev, events); err != nil {
				return ev, err
			}
		}
		last = ev
		if isCanonicalTerminal(ev) {
			if terminalSeen {
				return last, errDriveMultipleTerminal
			}
			terminalSeen = true
			if opts.errorEventIsFailure && ev.Kind == aipapi.EventError {
				return last, aipapi.NewStreamError(ev.ErrorCode, ev.ErrorMessage)
			}
			if opts.stopOnTerminal {
				return last, nil
			}
		}
	}
	return last, errDriveTooManyEvents
}
