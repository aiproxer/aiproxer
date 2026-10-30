package auxiliary

import "errors"

// ErrNotConfigured means no auxiliary client is bound for this execution snapshot.
var ErrNotConfigured = errors.New("aipsdk/auxiliary: auxiliary client not configured")

// ErrAuxDepthExceeded means nested auxiliary calls exceeded the runtime limit.
var ErrAuxDepthExceeded = errors.New("aipsdk/auxiliary: auxiliary recursion depth exceeded")

// ErrQueueSaturated means the bounded background queue cannot admit another distinct job.
var ErrQueueSaturated = errors.New("aipsdk/auxiliary: background queue saturated")

// ErrSubmitFailed is a generic non-queue/non-admission submit failure.
// Unknown submit errors are wrapped with this sentinel for typed classification.
var ErrSubmitFailed = errors.New("aipsdk/auxiliary: submit failed")

// ErrResultTooLarge means a background collection exceeded the configured per-job
// or scheduler byte bound before allocation. It is returned via Await/Poll (PollFailed)
// and is inspectable with errors.Is. Zero MaxOutputBytes preserves existing behavior.
var ErrResultTooLarge = errors.New("aipsdk/auxiliary: result too large")
