package auxiliary

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// PollState distinguishes non-blocking background job inspection outcomes.
type PollState uint8

const (
	PollPending PollState = iota
	PollCompleted
	PollFailed
	PollNotFound
)

// PollResult is the non-blocking inspection result for a background job.
type PollResult struct {
	State PollState
	// Collected is a defensive copy only when State == PollCompleted.
	Collected aipapi.Collected
	// Err is the terminal job failure when State == PollFailed.
	Err error
}

// BackgroundPoller is the optional non-blocking inspection capability for
// background auxiliary collections. It is separate from BackgroundClient so
// external implementations of BackgroundClient remain source-compatible without
// implementing Poll.
type BackgroundPoller interface {
	Poll(ctx context.Context, id JobID) (PollResult, error)
}
