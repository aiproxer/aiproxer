package auxiliary

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// JobID identifies one bounded process-local background auxiliary collection.
// Job IDs do not imply durable task semantics and are only valid for the owning
// process scheduler's retention window.
type JobID string

// SubmitOptions controls admission of a background collection. CoalesceKey must
// identify a committed continuity transaction; empty keys are rejected so a
// pre-open preview cannot become a billable scheduler job.
// OnCoalesced is an optional synchronous observer invoked before return:
// true if an existing job for the key was returned (coalesced), false if newly admitted.
// It is content-free, must not retain references, must not block, and must not
// panic: the scheduler recovers panics from the callback so an SDK consumer
// cannot crash the scheduler. Additive field: old callers with zero value (nil)
// remain source-compatible; new callers may set it.
// MaxOutputBytes is an optional per-job pre-collection byte bound for the
// aggregated result (Text, Reasoning, ToolArgs). Zero preserves existing
// scheduler/default behavior; a positive value is clamped to the scheduler's
// MaxResultBytes defense-in-depth bound and is enforced with CollectWithLimits
// before builder growth so a misbehaving compressor cannot allocate far above
// the feature hard/config limit. Bounded defaults for warnings/media remain.
type SubmitOptions struct {
	CoalesceKey string
	Timeout     time.Duration
	// Deadline optionally bounds queue residence plus execution. Zero preserves
	// the detached job lifetime; a nonzero deadline is independent of caller
	// cancellation, and an expired queued job must not invoke the runner.
	Deadline       time.Time
	OnCoalesced    func(coalesced bool)
	MaxOutputBytes int
}

// BackgroundClient is the narrow asynchronous auxiliary collection surface.
// It deliberately has no callback, arbitrary task, or durable queue API.
type BackgroundClient interface {
	SubmitCollect(ctx context.Context, req Request, opts SubmitOptions) (JobID, error)
	Await(ctx context.Context, id JobID) (aipapi.Collected, error)
	Forget(id JobID)
}

// DisabledBackgroundClient rejects background work when the feature is not
// composed into a process.
type DisabledBackgroundClient struct{}

func (DisabledBackgroundClient) SubmitCollect(context.Context, Request, SubmitOptions) (JobID, error) {
	return "", ErrNotConfigured
}

func (DisabledBackgroundClient) Await(context.Context, JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, ErrNotConfigured
}

func (DisabledBackgroundClient) Forget(JobID) {}

var _ BackgroundClient = DisabledBackgroundClient{}
