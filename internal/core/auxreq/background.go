package auxreq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

var (
	// ErrQueueFull means the bounded scheduler cannot admit another distinct job.
	ErrQueueFull = errors.New("auxreq: background queue is full")
	// ErrSchedulerClosed means no new work is admitted after process shutdown starts.
	ErrSchedulerClosed = errors.New("auxreq: background scheduler is closed")
	// ErrInvalidCoalesceKey prevents uncommitted or otherwise unkeyed work from
	// entering the billable background scheduler.
	ErrInvalidCoalesceKey = errors.New("auxreq: empty coalescing key")
	// ErrInvalidJobID means Await was given an empty identifier.
	ErrInvalidJobID = errors.New("auxreq: empty job id")
	// ErrJobNotFound means a result was forgotten or evicted from bounded retention.
	ErrJobNotFound = errors.New("auxreq: job result not found")
	// ErrResultTooLarge means collection exceeded the scheduler's result byte bound.
	ErrResultTooLarge = errors.New("auxreq: collected result exceeds configured bound")
)

const (
	defaultBackgroundWorkers     = 1
	defaultBackgroundQueue       = 32
	defaultBackgroundResults     = 128
	defaultBackgroundResultTTL   = 10 * time.Minute
	defaultBackgroundJobTimeout  = time.Minute
	defaultBackgroundResultBytes = 8 << 20
)

// SchedulerConfig bounds all process-local background state. Zero values use
// conservative defaults; negative values are rejected.
type SchedulerConfig struct {
	Workers        int
	QueueCapacity  int
	MaxResults     int
	ResultTTL      time.Duration
	JobTimeout     time.Duration
	MaxResultBytes int
	// Now supplies scheduler bookkeeping time. Production uses time.Now; tests
	// may provide a deterministic clock without sleeping.
	Now func() time.Time
}

// BackgroundSchedulerConfig is an additive name for callers that prefer the
// capability's full name.
type BackgroundSchedulerConfig = SchedulerConfig

type backgroundJob struct {
	id             auxiliary.JobID
	key            string
	req            auxiliary.Request
	runner         ExecutorRunner
	pin            genpin.Pin
	timeout        time.Duration
	deadline       time.Time
	maxOutputBytes int
	done           chan struct{}
	release        sync.Once
	// expiry ends the job when its absolute deadline passes while it still
	// waits in the queue. It is armed under the scheduler mutex when the job
	// is published and stopped when the job reaches a terminal state, so a
	// deadline is never decided by an early check alone.
	expiry *time.Timer

	// The fields below are protected by BackgroundScheduler.mu.
	finished       bool
	forgotten      bool
	result         *aipapi.Collected
	err            error
	completedAt    time.Time
	completedOrder uint64
	scope          scope.PrincipalScopeView
	hasScope       bool
	// receipt is the immutable host operation receipt the runtime executor
	// published for this job, retained with the result under the same
	// scheduler lock and bounded lifetime.
	receipt execctx.ExecutionReceipt

	// sponsorship is the operator-declared funding decision captured at
	// submission. The worker context is derived from the process root, so the
	// decision is carried explicitly instead of being inherited from the
	// submitting context.
	sponsorship    execctx.Sponsorship
	hasSponsorship bool
}

// BackgroundScheduler owns a fixed worker pool and a bounded result registry
// for process-scoped auxiliary collection. It is intentionally not a generic
// task runner: jobs contain only a canonical auxiliary request and a captured
// ExecutorRunner.
type BackgroundScheduler struct {
	root   context.Context
	cancel context.CancelFunc
	runner func() ExecutorRunner
	cfg    SchedulerConfig
	queue  chan *backgroundJob

	mu        sync.Mutex
	closed    bool
	jobs      map[auxiliary.JobID]*backgroundJob
	byKey     map[string]auxiliary.JobID
	sequence  atomic.Uint64
	completed atomic.Uint64
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

func (s *BackgroundScheduler) now() time.Time {
	if s != nil && s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

// NewBackgroundScheduler creates a process-owned collector. The runner
// provider is consulted synchronously on each accepted direct submission;
// workers use the captured runner and never acquire a later generation. Use
// BindRunner when a generation snapshot needs an immutable client view.
func NewBackgroundScheduler(root context.Context, runner func() ExecutorRunner, cfg SchedulerConfig) (*BackgroundScheduler, error) {
	if cfg.Workers < 0 || cfg.QueueCapacity < 0 || cfg.MaxResults < 0 || cfg.MaxResultBytes < 0 || cfg.ResultTTL < 0 || cfg.JobTimeout < 0 {
		return nil, fmt.Errorf("auxreq: scheduler bounds must be non-negative")
	}
	if cfg.Workers == 0 {
		cfg.Workers = defaultBackgroundWorkers
	}
	if cfg.QueueCapacity == 0 {
		cfg.QueueCapacity = defaultBackgroundQueue
	}
	if cfg.MaxResults == 0 {
		cfg.MaxResults = defaultBackgroundResults
	}
	if cfg.ResultTTL == 0 {
		cfg.ResultTTL = defaultBackgroundResultTTL
	}
	if cfg.JobTimeout == 0 {
		cfg.JobTimeout = defaultBackgroundJobTimeout
	}
	if cfg.MaxResultBytes == 0 {
		cfg.MaxResultBytes = defaultBackgroundResultBytes
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if root == nil {
		root = context.Background()
	}
	ctx, cancel := context.WithCancel(root)
	s := &BackgroundScheduler{
		root:   ctx,
		cancel: cancel,
		runner: runner,
		cfg:    cfg,
		queue:  make(chan *backgroundJob, cfg.QueueCapacity),
		jobs:   make(map[auxiliary.JobID]*backgroundJob),
		byKey:  make(map[string]auxiliary.JobID),
	}
	s.wg.Add(cfg.Workers)
	for range cfg.Workers {
		go s.worker()
	}
	return s, nil
}

// NewBackgroundClient is a convenience constructor with the SDK capability's
// name. It preserves the concrete scheduler for callers that need Close.
func NewBackgroundClient(root context.Context, runner func() ExecutorRunner, cfg SchedulerConfig) (*BackgroundScheduler, error) {
	return NewBackgroundScheduler(root, runner, cfg)
}

// BindRunner returns a generation-bound view over the process-owned scheduler.
// The view contains no worker, queue, or result state of its own. Its runner is
// immutable for the lifetime of the view; Await and Forget continue to use the
// scheduler's process-owned result registry.
func (s *BackgroundScheduler) BindRunner(runner ExecutorRunner) auxiliary.BackgroundClient {
	return boundBackgroundClient{scheduler: s, runner: runner}
}

type boundBackgroundClient struct {
	scheduler *BackgroundScheduler
	runner    ExecutorRunner
}

func (c boundBackgroundClient) SubmitCollect(ctx context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	if c.runner == nil {
		return "", auxiliary.ErrNotConfigured
	}
	if c.scheduler == nil {
		return "", ErrSchedulerClosed
	}
	return c.scheduler.submitCollect(ctx, req, opts, c.runner, true)
}

func (c boundBackgroundClient) Await(ctx context.Context, id auxiliary.JobID) (aipapi.Collected, error) {
	if c.scheduler == nil {
		return aipapi.Collected{}, ErrSchedulerClosed
	}
	return c.scheduler.Await(ctx, id)
}

func (c boundBackgroundClient) Forget(id auxiliary.JobID) {
	if c.scheduler != nil {
		c.scheduler.Forget(id)
	}
}

func (c boundBackgroundClient) AwaitWithReceipt(ctx context.Context, id auxiliary.JobID) (aipapi.Collected, ExecutionReceipt, error) {
	if c.scheduler == nil {
		return aipapi.Collected{}, ExecutionReceipt{}, ErrSchedulerClosed
	}
	return c.scheduler.AwaitWithReceipt(ctx, id)
}

func (c boundBackgroundClient) Poll(ctx context.Context, id auxiliary.JobID) (auxiliary.PollResult, error) {
	if c.scheduler == nil {
		return auxiliary.PollResult{}, ErrSchedulerClosed
	}
	return c.scheduler.Poll(ctx, id)
}

func (s *BackgroundScheduler) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.root.Done():
			s.drainCanceled()
			return
		case job := <-s.queue:
			if job != nil {
				s.run(job)
			}
		}
	}
}

// drainCanceled releases jobs that were still buffered when an externally
// canceled process root stopped admission. Multiple workers may race here;
// each queue item is received once and finish is idempotent.
func (s *BackgroundScheduler) drainCanceled() {
	for {
		select {
		case job := <-s.queue:
			if job != nil {
				s.finish(job, nil, execctx.ExecutionReceipt{}, fmt.Errorf("%w: %w", ErrSchedulerClosed, s.root.Err()))
			}
		default:
			return
		}
	}
}

func (s *BackgroundScheduler) run(job *backgroundJob) {
	if err := s.root.Err(); err != nil {
		s.finish(job, nil, execctx.ExecutionReceipt{}, fmt.Errorf("%w: %w", ErrSchedulerClosed, err))
		return
	}
	ctx, cancel := context.WithTimeout(s.root, job.timeout)
	defer cancel()
	if !job.deadline.IsZero() {
		var cancelDeadline context.CancelFunc
		ctx, cancelDeadline = context.WithDeadline(ctx, job.deadline)
		defer cancelDeadline()
	}
	if err := ctx.Err(); err != nil {
		s.finish(job, nil, execctx.ExecutionReceipt{}, err)
		return
	}
	ctx = workerAttributionContext(ctx, job.req, job.scope, job.hasScope)
	if job.hasSponsorship {
		ctx = execctx.WithSponsorship(ctx, job.sponsorship)
	}
	var collected *aipapi.Collected
	var receipt execctx.ExecutionReceipt
	var runErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			runErr = fmt.Errorf("auxreq: background runner panic: %v", recovered)
		}
		s.finish(job, collected, receipt, runErr)
	}()
	stream, receipt, err := (Client{}).streamWithRunnerReceipt(ctx, job.req, job.runner, false)
	if err == nil {
		var collectedValue aipapi.Collected
		effective := s.cfg.MaxResultBytes
		if job.maxOutputBytes > 0 && job.maxOutputBytes < effective {
			effective = job.maxOutputBytes
		}
		defaultLimits := aipapi.DefaultCollectLimits()
		limits := aipapi.CollectLimits{
			MaxTextBytes:             effective,
			MaxReasoningBytes:        effective,
			MaxToolArgsTotalBytes:    effective,
			MaxWarnings:              defaultLimits.MaxWarnings,
			MaxAssistantMediaParts:   defaultLimits.MaxAssistantMediaParts,
			MaxAggregatePayloadBytes: effective,
		}
		collectedValue, runErr = aipapi.CollectWithLimits(ctx, stream, limits)
		if runErr != nil && errors.Is(runErr, aipapi.ErrCollectLimitExceeded) {
			runErr = fmt.Errorf("%w: %w: %w", ErrResultTooLarge, auxiliary.ErrResultTooLarge, runErr)
		}
		collected = &collectedValue
		if runErr == nil && estimateCollectedBytes(collected) > effective {
			runErr = fmt.Errorf("%w: %w", ErrResultTooLarge, auxiliary.ErrResultTooLarge)
		}
	} else if stream != nil {
		_ = stream.Close()
		runErr = err
	} else {
		runErr = err
	}
}

func workerAttributionContext(ctx context.Context, req auxiliary.Request, parent scope.PrincipalScopeView, hasScope bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if hasScope {
		parent.Origin = scope.OriginInternal
		if req.ParentTraceID != "" {
			parent.ParentTraceID = scope.Known(req.ParentTraceID)
		}
		ctx = scope.WithScope(ctx, parent)
	}
	return ctx
}

// SubmitCollect synchronously captures the current executor and generation
// pin, then transfers both into the bounded queue. Parent cancellation after
// this handoff does not cancel worker execution.
func (s *BackgroundScheduler) SubmitCollect(ctx context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return s.submitCollect(ctx, req, opts, nil, false)
}

// submitCollect performs common admission and ownership transfer for direct
// and generation-bound clients. A bound client supplies the runner already;
// direct clients resolve their provider only after the coalescing fast path.
func (s *BackgroundScheduler) submitCollect(ctx context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions, boundRunner ExecutorRunner, bound bool) (auxiliary.JobID, error) {
	if bound && boundRunner == nil {
		return "", auxiliary.ErrNotConfigured
	}
	if s == nil {
		return "", ErrSchedulerClosed
	}
	if ctx == nil {
		return "", aipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.deadlinePassed(opts.Deadline) {
		return "", context.DeadlineExceeded
	}
	if req.Call == nil {
		return "", fmt.Errorf("auxreq: nil call: %w", aipapi.ErrInvalidCall)
	}
	key := strings.TrimSpace(opts.CoalesceKey)
	if key == "" {
		return "", ErrInvalidCoalesceKey
	}
	// Resolve an existing committed job before touching the runner provider or
	// generation retainer. The second lookup below remains necessary because a
	// concurrent submit may reserve this key after this fast-path unlocks.
	s.mu.Lock()
	s.cleanupLocked(s.now())
	if s.closed {
		s.mu.Unlock()
		return "", ErrSchedulerClosed
	}
	if id, ok := s.byKey[key]; ok {
		// The coalescing path is a publication boundary of its own: the
		// caller's context and the absolute deadline are revalidated here,
		// because the preflight check above already ran before the lock.
		err := ctx.Err()
		if err == nil && s.deadlinePassed(opts.Deadline) {
			err = context.DeadlineExceeded
		}
		s.mu.Unlock()
		if err != nil {
			return "", err
		}
		safeInvokeOnCoalesced(opts.OnCoalesced, true)
		return id, nil
	}
	s.mu.Unlock()
	run := boundRunner
	if !bound {
		if s.runner == nil {
			return "", auxiliary.ErrNotConfigured
		}
		run = s.runner()
		if run == nil {
			return "", auxiliary.ErrNotConfigured
		}
	}

	var pin genpin.Pin
	if ret, ok := genpin.FromContext(ctx); ok && ret != nil {
		var retained bool
		pin, retained = ret.Retain(genpin.KindAsync)
		if !retained || pin == nil {
			return "", fmt.Errorf("auxreq: runtime generation pin retain failed: %w", aipapi.ErrInvalidCall)
		}
	}
	releaseOnFailure := true
	defer func() {
		if releaseOnFailure && pin != nil {
			pin.Release()
		}
	}()

	request := cloneRequest(req)
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = s.cfg.JobTimeout
	}
	job := &backgroundJob{
		key:            key,
		req:            request,
		runner:         run,
		pin:            pin,
		timeout:        timeout,
		deadline:       opts.Deadline,
		maxOutputBytes: opts.MaxOutputBytes,
		done:           make(chan struct{}),
	}
	if parent, ok := scope.ScopeFromContext(ctx); ok {
		job.scope = parent
		job.hasScope = true
	}
	// The operator-declared funding decision is captured here, at
	// submission: the worker context is derived from the process root, so an
	// inherited context value would never reach the execution.
	if decl, ok := execctx.SponsorshipFromContext(ctx); ok {
		job.sponsorship = decl
		job.hasSponsorship = true
	}

	id, admitted, err := s.publishJob(ctx, job)
	if admitted {
		releaseOnFailure = false
		safeInvokeOnCoalesced(opts.OnCoalesced, false)
	} else if err == nil && id != "" {
		safeInvokeOnCoalesced(opts.OnCoalesced, true)
	}
	if errors.Is(err, ErrQueueFull) {
		err = fmt.Errorf("%w: %w", auxiliary.ErrQueueSaturated, err)
	} else if err != nil && !errors.Is(err, auxiliary.ErrQueueSaturated) && !errors.Is(err, ErrSchedulerClosed) && !errors.Is(err, auxiliary.ErrNotConfigured) && !errors.Is(err, ErrInvalidCoalesceKey) && !errors.Is(err, aipapi.ErrInvalidCall) && !errors.Is(err, aipapi.ErrNilContext) && !callerBoundError(err) {
		// Preserve typed taxonomy for unknown submit failures; wrap with ErrSubmitFailed.
		err = fmt.Errorf("%w: %w", auxiliary.ErrSubmitFailed, err)
	}
	return id, err
}

// callerBoundError reports whether err is the caller's own bound — its
// cancellation or its absolute deadline — rather than a scheduler failure. Such
// an error keeps its identity instead of being relabeled a submit failure.
func callerBoundError(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// deadlinePassed reports whether the absolute deadline has passed on the
// scheduler's clock. A zero deadline never passes: it preserves the detached
// job lifetime.
func (s *BackgroundScheduler) deadlinePassed(deadline time.Time) bool {
	if deadline.IsZero() {
		return false
	}
	return !s.now().Before(deadline)
}

// safeInvokeOnCoalesced invokes the optional coalescing observer outside any lock
// and recovers from panics so an SDK consumer cannot crash the scheduler.
func safeInvokeOnCoalesced(fn func(bool), coalesced bool) {
	if fn == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	fn(coalesced)
}

// publishJob commits one job to the bounded queue. It is the publication
// boundary: the caller's context and the job's absolute deadline are
// revalidated here, because resolving the runner, acquiring the generation
// pin and cloning the request can all outlive the bound the caller asked for.
// A job that reaches the queue also gets its expiry timer armed, so a
// deadline that passes while the job waits for a worker ends the job without a
// worker.
func (s *BackgroundScheduler) publishJob(ctx context.Context, job *backgroundJob) (auxiliary.JobID, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(s.now())
	if s.closed {
		return "", false, ErrSchedulerClosed
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if s.deadlinePassed(job.deadline) {
		return "", false, context.DeadlineExceeded
	}
	if id, ok := s.byKey[job.key]; ok {
		return id, false, nil
	}
	job.id = s.nextID()
	s.jobs[job.id], s.byKey[job.key] = job, job.id
	select {
	case s.queue <- job:
		s.armExpiryLocked(job)
		return job.id, true, nil
	default:
		delete(s.jobs, job.id)
		if s.byKey[job.key] == job.id {
			delete(s.byKey, job.key)
		}
		return "", false, fmt.Errorf("%w: %w", auxiliary.ErrQueueSaturated, ErrQueueFull)
	}
}

// armExpiryLocked starts the timer that ends a queued job when its absolute
// deadline passes. The timer is the only mechanism that can end a job that no
// worker has picked up: a worker may be busy for longer than the deadline, and
// the deadline bounds the queue residence as much as the execution. The
// remaining duration is measured on the scheduler's clock, which is the clock
// the deadline is compared against.
func (s *BackgroundScheduler) armExpiryLocked(job *backgroundJob) {
	if job.deadline.IsZero() {
		return
	}
	remaining := max(job.deadline.Sub(s.now()), 0)
	job.expiry = time.AfterFunc(remaining, func() { s.expire(job) })
}

// expire ends a job whose absolute deadline passed while it waited in the
// queue. The job becomes terminal without a worker, its generation pin is
// released exactly once, and its queue slot is freed so an expired job cannot
// saturate the queue for healthy submissions. A job a worker already owns is
// left alone: that worker's execution context carries the same deadline and
// ends the job there.
func (s *BackgroundScheduler) expire(job *backgroundJob) {
	if job == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		// The shutdown drain owns the queued jobs.
		s.mu.Unlock()
		return
	}
	queued := s.removeQueuedLocked(job)
	s.mu.Unlock()
	if !queued {
		return
	}
	s.finish(job, nil, execctx.ExecutionReceipt{}, context.DeadlineExceeded)
}

// removeQueuedLocked takes one job out of the queue so its capacity becomes
// reusable and reports whether the job was still queued. A channel cannot drop
// an element, so the queue is drained and refilled with the entries that are
// still live; a worker that takes an entry during the scan keeps it, and an
// entry the scan does not find is already running. The caller must hold the
// scheduler mutex: it keeps publishers from sending while the queue is
// refilled.
func (s *BackgroundScheduler) removeQueuedLocked(target *backgroundJob) bool {
	var live []*backgroundJob
	for {
		select {
		case job := <-s.queue:
			if job == target {
				s.refillQueueLocked(live)
				return true
			}
			live = append(live, job)
		default:
			s.refillQueueLocked(live)
			return false
		}
	}
}

// refillQueueLocked puts the drained live entries back. The queue held at
// most its capacity entries, so the refill always fits.
func (s *BackgroundScheduler) refillQueueLocked(live []*backgroundJob) {
	for _, job := range live {
		s.queue <- job
	}
}

func (s *BackgroundScheduler) nextID() auxiliary.JobID {
	return auxiliary.JobID(fmt.Sprintf("aux-%d", s.sequence.Add(1)))
}

func cloneRequest(req auxiliary.Request) auxiliary.Request {
	out := req
	if req.Call != nil {
		call := aipapi.CloneCall(*req.Call)
		out.Call = &call
	}
	if len(req.DisablePlugins) > 0 {
		out.DisablePlugins = append([]string(nil), req.DisablePlugins...)
	}
	return out
}

func (s *BackgroundScheduler) finish(job *backgroundJob, result *aipapi.Collected, receipt execctx.ExecutionReceipt, err error) {
	if job == nil {
		return
	}
	s.mu.Lock()
	if job.finished {
		s.mu.Unlock()
		return
	}
	job.finished = true
	job.err = err
	job.completedAt = s.now()
	job.completedOrder = s.completed.Add(1)
	if job.expiry != nil {
		// The job is terminal: its expiry timer has nothing left to end.
		job.expiry.Stop()
		job.expiry = nil
	}
	job.receipt = receipt
	if err == nil && result != nil {
		job.result = aipapi.CloneCollected(result)
	}
	if !job.forgotten {
		s.jobs[job.id] = job
		if err == nil || job.err != nil {
			s.byKey[job.key] = job.id
		}
		s.cleanupLocked(job.completedAt)
	}
	s.mu.Unlock()
	job.releasePin()
	close(job.done)
}

func (j *backgroundJob) releasePin() {
	j.release.Do(func() {
		if j.pin != nil {
			j.pin.Release()
		}
	})
}

// Await waits for a bounded result without inheriting the worker's lifetime.
func (s *BackgroundScheduler) Await(ctx context.Context, id auxiliary.JobID) (out aipapi.Collected, err error) {
	out, _, err = s.await(ctx, id)
	return out, err
}

// AwaitWithReceipt returns the collected result, the job's host operation
// receipt and the job error from one locked snapshot. It is the internal
// receipt-aware sibling of Await; the public SDK contract is unchanged.
func (s *BackgroundScheduler) AwaitWithReceipt(ctx context.Context, id auxiliary.JobID) (out aipapi.Collected, receipt ExecutionReceipt, err error) {
	return s.await(ctx, id)
}

func (s *BackgroundScheduler) await(ctx context.Context, id auxiliary.JobID) (out aipapi.Collected, receipt ExecutionReceipt, err error) {
	if s == nil {
		return out, receipt, ErrSchedulerClosed
	}
	if ctx == nil {
		return out, receipt, aipapi.ErrNilContext
	}
	if strings.TrimSpace(string(id)) == "" {
		return out, receipt, ErrInvalidJobID
	}
	s.mu.Lock()
	s.cleanupLocked(s.now())
	job, ok := s.jobs[id]
	if !ok {
		s.mu.Unlock()
		return out, receipt, ErrJobNotFound
	}
	done := job.done
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return out, receipt, ctx.Err()
	case <-done:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok = s.jobs[id]
	if !ok || job.forgotten {
		return out, receipt, ErrJobNotFound
	}
	if !job.finished {
		return out, receipt, errors.New("auxreq: job completion state unavailable")
	}
	receipt = ExecutionReceipt{Execution: job.receipt, CompletedAt: job.completedAt}
	if job.err != nil {
		return out, receipt, job.err
	}
	if job.result == nil {
		return out, receipt, errors.New("auxreq: completed job has no result")
	}
	aipapi.CloneCollectedInto(&out, job.result)
	return out, receipt, nil
}

// Poll inspects a background job without blocking. It distinguishes pending,
// completed, failed, and not-found/expired states, clones completed results
// defensively, and does not consume or forget the job.
func (s *BackgroundScheduler) Poll(ctx context.Context, id auxiliary.JobID) (auxiliary.PollResult, error) {
	if s == nil {
		return auxiliary.PollResult{}, ErrSchedulerClosed
	}
	if ctx == nil {
		return auxiliary.PollResult{}, aipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return auxiliary.PollResult{}, err
	}
	if strings.TrimSpace(string(id)) == "" {
		return auxiliary.PollResult{}, ErrInvalidJobID
	}
	s.mu.Lock()
	s.cleanupLocked(s.now())
	job, ok := s.jobs[id]
	if !ok {
		s.mu.Unlock()
		return auxiliary.PollResult{State: auxiliary.PollNotFound}, nil
	}
	finished := job.finished
	jobErr := job.err
	resultPtr := job.result
	s.mu.Unlock()
	if !finished {
		return auxiliary.PollResult{State: auxiliary.PollPending}, nil
	}
	if jobErr != nil {
		return auxiliary.PollResult{State: auxiliary.PollFailed, Err: jobErr}, nil
	}
	if resultPtr == nil {
		return auxiliary.PollResult{State: auxiliary.PollFailed, Err: errors.New("auxreq: completed job has no result")}, nil
	}
	var collected aipapi.Collected
	aipapi.CloneCollectedInto(&collected, resultPtr)
	return auxiliary.PollResult{State: auxiliary.PollCompleted, Collected: collected}, nil
}

// Forget removes a result (or prevents a pending result from being retained)
// from the bounded registry. Active work still owns its submit-time pin until
// terminal worker completion.
func (s *BackgroundScheduler) Forget(id auxiliary.JobID) {
	if s == nil || strings.TrimSpace(string(id)) == "" {
		return
	}
	s.mu.Lock()
	job, ok := s.jobs[id]
	if ok {
		job.forgotten = true
		delete(s.jobs, id)
		if s.byKey[job.key] == id {
			delete(s.byKey, job.key)
		}
	}
	s.mu.Unlock()
}

// Close stops admission, cancels worker contexts, joins all workers, and
// drains any jobs that were still buffered so every retained pin is released.
func (s *BackgroundScheduler) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		s.mu.Unlock()
		s.wg.Wait()
		for {
			select {
			case job := <-s.queue:
				if job != nil {
					s.finish(job, nil, execctx.ExecutionReceipt{}, ErrSchedulerClosed)
				}
			default:
				s.closeErr = nil
				return
			}
		}
	})
	return s.closeErr
}

func (s *BackgroundScheduler) cleanupLocked(now time.Time) {
	for id, job := range s.jobs {
		if !job.finished || job.completedAt.IsZero() || now.Sub(job.completedAt) < s.cfg.ResultTTL {
			continue
		}
		job.forgotten = true
		delete(s.jobs, id)
		if s.byKey[job.key] == id {
			delete(s.byKey, job.key)
		}
	}
	completed := 0
	for _, job := range s.jobs {
		if job.finished {
			completed++
		}
	}
	for completed > s.cfg.MaxResults {
		var oldestID auxiliary.JobID
		var oldest time.Time
		var oldestOrder uint64
		for id, job := range s.jobs {
			if !job.finished || job.forgotten {
				continue
			}
			if oldestID == "" || job.completedAt.Before(oldest) || (job.completedAt.Equal(oldest) && job.completedOrder < oldestOrder) {
				oldestID, oldest = id, job.completedAt
				oldestOrder = job.completedOrder
			}
		}
		if oldestID == "" {
			return
		}
		job := s.jobs[oldestID]
		job.forgotten = true
		delete(s.jobs, oldestID)
		if s.byKey[job.key] == oldestID {
			delete(s.byKey, job.key)
		}
		completed--
	}
}

func estimateCollectedBytes(c *aipapi.Collected) int {
	if c == nil {
		return 0
	}
	n := c.Text.Len() + c.Reasoning.Len()
	for id, b := range c.ToolArgs {
		n += len(id)
		if b != nil {
			n += b.Len()
		}
	}
	for id, name := range c.ToolNames {
		n += len(id) + len(name)
	}
	for _, warning := range c.Warnings {
		n += len(warning)
	}
	for _, part := range c.AssistantMedia {
		n += len(part.Content)
	}
	return n
}

var (
	_ auxiliary.BackgroundClient = (*BackgroundScheduler)(nil)
	_ auxiliary.BackgroundClient = boundBackgroundClient{}
	_ auxiliary.BackgroundPoller = (*BackgroundScheduler)(nil)
	_ auxiliary.BackgroundPoller = boundBackgroundClient{}
	_ ReceiptAwaiter             = (*BackgroundScheduler)(nil)
	_ ReceiptAwaiter             = boundBackgroundClient{}
)
