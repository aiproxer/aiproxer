package proxymemory

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

const (
	// captureOrder places the capture transform and observer after the
	// canonical content stages (design §9).
	captureOrder = 1000
	// capturePendingCap bounds the pending-turn map (design §9).
	capturePendingCap = 1024
	// capturePendingTTL is the lifetime of a pending turn whose attempts never
	// release successfully (design §9).
	capturePendingTTL = 10 * time.Minute
	// captureQueueCap bounds the writer queue (design §9).
	captureQueueCap = 256
	// captureWriteTimeout bounds one store write (design §9).
	captureWriteTimeout = 5 * time.Second
)

// capturePendingKey binds one turn to its request trace and A-leg. The
// transform and the observer of one turn derive the same key (design §9).
func capturePendingKey(traceID, aLegID string) string { return traceID + "\x00" + aLegID }

// pendingTurn is the request history of one turn awaiting a released
// response, plus the owner and activity identity it carries into the writer.
type pendingTurn struct {
	owner        Owner
	label        string
	aLegID       string
	entries      []Entry
	at           time.Time
	sourceSeq    int64
	erasureEpoch int64
}

// captureJob is one released turn queued for the writer: the pending history
// concatenated with the released assistant output, and the activity time.
type captureJob struct {
	owner        Owner
	label        string
	aLegID       string
	entries      []Entry
	activityMS   int64
	sourceSeq    int64
	erasureEpoch int64
}

type writerJob struct {
	firstTurn *FirstTurnJob
	capture   *captureJob
}

// Capture is the design §9 request transform, stream observer factory, pending
// map and single-writer queue of proxy memory capture. It is fail-open: the
// transform never mutates the call and never returns an error, observers never
// fail a request, and a full queue or a stopped writer drops the capture
// without blocking. All state is bounded and no captured content reaches a log
// or a metric label.
type Capture struct {
	cfg       Config
	store     Store
	metrics   *Metrics
	decisions *DecisionCache
	now       func() time.Time
	log       *slog.Logger

	mu      sync.Mutex
	pending map[string]pendingTurn

	queue      chan writerJob
	done       chan struct{}
	writerDone chan struct{}
	startOnce  sync.Once
	stopOnce   sync.Once
	started    atomic.Bool

	// stopMu guards the shutdown state shared with the writer goroutine and
	// the queue publication. Stop publishes stopCtx and the stopped flag
	// before it closes done, so the drain path that observed done always sees
	// them; cancelWrites cancels the writer's context root the moment that
	// budget expires (design §9: drain "until the stop ctx expires"), which
	// bounds the store write already in flight. The same lock linearizes the
	// queue publication with the stop: a job is either queued before the stop
	// — and therefore drained — or refused.
	stopMu       sync.Mutex
	stopCtx      context.Context
	cancelWrites context.CancelFunc
	stopped      bool
}

var (
	_ request.Transform              = (*Capture)(nil)
	_ response.StreamObserverFactory = (*Capture)(nil)
)

// NewCapture constructs the capture plane over the store seam. now is the
// injectable clock (time.Now when nil), log receives content-free degradation
// records (slog.Default when nil) and decisions is the process-owned cache of
// immutable first-turn decisions; a nil cache gets a bounded private instance.
func NewCapture(cfg Config, store Store, m *Metrics, now func() time.Time, log *slog.Logger, decisions *DecisionCache) *Capture {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	if decisions == nil {
		decisions = NewDecisionCache(0)
	}
	if cfg.RecallTimeout <= 0 {
		cfg.RecallTimeout = defaultRecallTimeout
	}
	return &Capture{
		cfg:        cfg,
		store:      store,
		metrics:    m,
		decisions:  decisions,
		now:        now,
		log:        log,
		pending:    make(map[string]pendingTurn),
		queue:      make(chan writerJob, captureQueueCap),
		done:       make(chan struct{}),
		writerDone: make(chan struct{}),
	}
}

// Transform returns the request-wide capture transform (design §9).
func (c *Capture) Transform() request.Transform { return c }

// ObserverFactory returns the final-stream observer factory (design §9).
func (c *Capture) ObserverFactory() response.StreamObserverFactory { return c }

// ID is the capture handler identity, shared by both planes so a phase 1 or
// phase 2 auxiliary request can suppress capture exactly (design §7.2).
func (c *Capture) ID() string { return CaptureHandlerID }

// Order places capture after the canonical content stages (design §9).
func (c *Capture) Order() int { return captureOrder }

// FailureMode keeps capture fail-open: a capture defect never fails a request.
func (c *Capture) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

// Handle reads one bounded admission snapshot, applies only an immutable frozen
// first-turn decision, and snapshots eligible request history into the pending
// map (design §9). It never waits for a write and always fails open.
//
// The decision is eligible before it reaches the model-bound request: a
// decision the bounded writer queue refused is dropped before it can become
// durable, so it never authorizes an injection, and a decision whose durable
// write failed is never replayed on a later request.
func (c *Capture) Handle(ctx context.Context, call *aipapi.Call, meta request.RequestMeta, _ request.Services) error {
	if ctx == nil {
		ctx = context.Background()
	}
	principal, _, ok := CheckEligible(c.cfg, meta.Scope)
	if !ok {
		c.metrics.Inc(EventInjectIneligible)
		return nil
	}
	if !HasSession(meta.Session) {
		c.metrics.Inc(EventInjectNoSession)
		c.metrics.Inc(EventCaptureNoSession)
		return nil
	}

	firstTurn := meta.Session.AuthoritativeFirstTurn
	candidate := Project{}
	if firstTurn {
		candidate, _ = DeriveProject(call)
	}
	cached, cacheStatus := c.decisions.GetStatus(principal, meta.Session.ALegID)
	cacheFound := cacheStatus != DecisionAbsent
	cacheQueued := cacheStatus == DecisionQueued
	query := AdmissionQuery{
		Principal:              principal,
		ALegID:                 meta.Session.ALegID,
		AuthoritativeFirstTurn: firstTurn,
		Candidate:              candidate,
	}
	if cacheFound {
		query.CachedProject = &cached.Project
	}
	readCtx, cancel := context.WithTimeout(ctx, c.cfg.RecallTimeout)
	snapshot, err := c.store.ReadAdmission(readCtx, query)
	cancel()
	if err != nil {
		// A missing decision is omitted, never substituted: the request
		// continues without recall and the failure stays observable.
		if firstTurn {
			c.decisions.Put(FirstTurnJob{
				Principal: principal, ALegID: meta.Session.ALegID, Project: candidate,
				Decision: FirstTurnDecision{Outcome: FirstTurnUnavailable},
			})
		}
		c.unavailableDecision(err)
		return nil
	}

	project := snapshot.Project
	var decision FirstTurnDecision
	available := false
	queued := false
	persisted := false
	// fresh marks a decision frozen by this request. A replayed decision
	// describes an earlier observation: the current store is healthy, so it
	// must not report a storage failure.
	fresh := false
	// A cached decision is replayable only under the same epoch and project,
	// and only while it is not in its terminal failed state: a failed decision
	// never becomes durable, so it authorizes neither an injection nor a
	// capture.
	cacheValid := cacheFound && cached.EpochKnown && cached.ErasureEpoch == snapshot.ErasureEpoch &&
		cached.Project.Key == project.Key && cacheStatus != DecisionFailed
	switch {
	case snapshot.Decision != nil:
		// A durable decision is the store's authority: it is used even when
		// the local cache entry is in its terminal failed state, because the
		// current durable read wins over any local state.
		decision, available = *snapshot.Decision, true
		c.decisions.Put(FirstTurnJob{
			Principal: principal, ALegID: meta.Session.ALegID, Project: project,
			Decision: decision, ErasureEpoch: snapshot.ErasureEpoch, EpochKnown: true,
		})
	case cacheValid:
		// The same decision under the same epoch: replay it verbatim.
		decision, available = cached.Decision, true
		if cacheQueued {
			queued = true
		} else if firstTurn {
			// The cached decision was never published to the writer queue: the
			// authoritative first turn publishes it now.
			persisted = true
			queued = c.admitFirstTurn(cached)
		}
	case firstTurn:
		fresh = true
		decision = freezeDecision(c.cfg, project, snapshot)
		job := FirstTurnJob{
			Principal:    principal,
			ALegID:       meta.Session.ALegID,
			Project:      project,
			Decision:     decision,
			ErasureEpoch: snapshot.ErasureEpoch,
			EpochKnown:   true,
		}
		c.decisions.Put(job)
		available = true
		persisted = true
		queued = c.admitFirstTurn(job)
	default:
		c.metrics.Inc(EventInjectDecisionUnavailable)
	}

	if persisted && !queued {
		// The frozen decision was dropped before it could become durable, so
		// it never authorized an injection: the request continues without
		// recall and the omission stays observable.
		available = false
		c.metrics.Inc(EventInjectDecisionUnavailable)
	}
	if available {
		c.applyDecision(call, decision, fresh)
	}
	if persisted && !queued {
		// The frozen decision was dropped: this turn continues without recall
		// and authorizes no capture.
		return nil
	}
	if project.Key == "" {
		return nil
	}
	// A capture needs durable project authority: a persisted binding, or the
	// first turn's own admitted decision. A replayed decision that is not
	// persisted yet never authorizes a capture.
	if !snapshot.Bound && (!queued || (!firstTurn && (!cacheValid || !cacheQueued))) {
		// Only an already persisted binding or an admitted first-turn job may
		// authorize capture for an unbound A-leg.
		return nil
	}
	if snapshot.NextBSeq < 0 || snapshot.NextBSeq >= 1<<63-1 {
		c.metrics.Inc(EventCaptureError)
		c.log.Warn("proxy-memory capture admission failed", "event", EventCaptureError, "error_kind", "invalid_sequence")
		return nil
	}
	seq := max(int64(1), snapshot.NextBSeq+1)
	owner := Owner{TenantID: principal.TenantID, PrincipalID: principal.PrincipalID, ProjectKey: project.Key}
	c.storePending(capturePendingKey(meta.TraceID, meta.Session.ALegID), pendingTurn{
		owner:        owner,
		sourceSeq:    seq,
		label:        project.Label,
		aLegID:       meta.Session.ALegID,
		entries:      RenderHistory(call),
		at:           c.now(),
		erasureEpoch: snapshot.ErasureEpoch,
	})
	return nil
}

// admitFirstTurn queues the frozen decision for durable persistence. The queue
// is bounded: a full or stopped queue drops the decision, and the cached entry
// stops authorizing capture. The publication and the cached state change in
// one step, so a consumer failure can never be overwritten by this producer.
func (c *Capture) admitFirstTurn(job FirstTurnJob) bool {
	return c.decisions.Admit(job, func() bool {
		if c.publish(writerJob{firstTurn: &job}) {
			return true
		}
		c.recordDroppedDecision()
		return false
	})
}

func freezeDecision(cfg Config, project Project, snapshot AdmissionSnapshot) FirstTurnDecision {
	if project.Key == "" {
		return FirstTurnDecision{Outcome: FirstTurnNoProject}
	}
	if snapshot.SummaryUnavailable {
		return FirstTurnDecision{Outcome: FirstTurnUnavailable}
	}
	if snapshot.Summary == "" {
		return FirstTurnDecision{Outcome: FirstTurnNoSummary}
	}
	return FirstTurnDecision{
		Outcome:        FirstTurnMatched,
		Injection:      RenderInjection(cfg, snapshot.Summary, project.Key),
		SummaryVersion: snapshot.SummaryVersion,
	}
}

// applyDecision applies one frozen decision to the request and counts its
// outcome. fresh marks a decision this request froze: only a fresh
// unavailable decision reports a storage failure, because a replayed
// unavailable decision describes an earlier observation and the current store
// read succeeded.
func (c *Capture) applyDecision(call *aipapi.Call, decision FirstTurnDecision, fresh bool) {
	switch decision.Outcome {
	case FirstTurnMatched:
		if call == nil || decision.Injection == "" {
			c.metrics.Inc(EventInjectDecisionUnavailable)
			return
		}
		injectMemory(call, decision.Injection)
		c.metrics.Inc(EventInjectMatched)
	case FirstTurnNoSummary:
		c.metrics.Inc(EventInjectNoSummary)
	case FirstTurnNoProject:
		c.metrics.Inc(EventInjectNoProject)
	case FirstTurnUnavailable:
		c.metrics.Inc(EventInjectDecisionUnavailable)
		if fresh {
			c.metrics.Inc(EventInjectError)
			c.metrics.Inc(EventStoreUnavailable)
		}
	default:
		c.metrics.Inc(EventInjectDecisionUnavailable)
	}
}

func injectMemory(call *aipapi.Call, injection string) {
	if call.HasItemAuthority() {
		memoryItem := aipapi.Item{
			Kind:    aipapi.ItemKindMessage,
			Role:    aipapi.RoleSystem,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: injection}},
		}
		call.Items = append([]aipapi.Item{memoryItem}, call.Items...)
		return
	}
	memoryInstruction := aipapi.Message{
		Role:  aipapi.RoleSystem,
		Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: injection}},
	}
	call.Instructions = append([]aipapi.Message{memoryInstruction}, call.Instructions...)
}

func (c *Capture) unavailableDecision(err error) {
	c.metrics.Inc(EventInjectDecisionUnavailable)
	c.metrics.Inc(EventInjectError)
	c.metrics.Inc(EventStoreUnavailable)
	c.log.Warn("proxy-memory admission unavailable", "event", EventInjectError, "error_kind", captureErrorKind(err))
}

func (c *Capture) recordDroppedDecision() {
	c.metrics.Inc(EventInjectDroppedQueue)
	c.log.Warn("proxy-memory first-turn decision dropped", "event", EventInjectDroppedQueue)
}

// publish enqueues one writer job without blocking. The publication is
// linearized with the shutdown: Stop marks the writer stopped under the same
// lock, so a job is either queued before the stop — and therefore drained —
// or refused. A full queue refuses the job as well.
func (c *Capture) publish(job writerJob) bool {
	c.stopMu.Lock()
	defer c.stopMu.Unlock()
	if c.stopped {
		return false
	}
	select {
	case c.queue <- job:
		return true
	default:
		return false
	}
}

// storePending inserts one turn under the bounded pending map: expired turns
// are evicted first, and a new key at the cap evicts the oldest turn. Every
// eviction is a dropped pending capture and is counted without content
// (Requirements 1.6, 3.5; VISION "no hidden degradation").
func (c *Capture) storePending(key string, turn pendingTurn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpiredLocked(turn.at)
	if _, exists := c.pending[key]; !exists {
		for len(c.pending) >= capturePendingCap {
			c.evictOldestLocked()
		}
	}
	c.pending[key] = turn
}

// evictExpiredLocked drops every turn older than capturePendingTTL.
func (c *Capture) evictExpiredLocked(now time.Time) {
	cutoff := now.Add(-capturePendingTTL)
	for key, turn := range c.pending {
		if turn.at.Before(cutoff) {
			delete(c.pending, key)
			c.metrics.Inc(EventCaptureDroppedPending)
		}
	}
}

// evictOldestLocked drops the pending turn with the earliest activity time.
func (c *Capture) evictOldestLocked() {
	var (
		oldestKey string
		oldestAt  time.Time
	)
	first := true
	for key, turn := range c.pending {
		if first || turn.at.Before(oldestAt) {
			oldestKey, oldestAt, first = key, turn.at, false
		}
	}
	if first {
		return
	}
	delete(c.pending, oldestKey)
	c.metrics.Inc(EventCaptureDroppedPending)
}

// Open returns one observer per B-leg of a pending turn (design §9). The
// lookup never removes the turn, so a retry, failover or parallel branch gets
// its own observer over the same history; an absent turn yields a no-op
// observer.
func (c *Capture) Open(_ context.Context, meta response.StreamMeta, _ response.Services) (response.StreamObserver, error) {
	key := capturePendingKey(meta.TraceID, meta.ALegID)
	c.mu.Lock()
	turn, ok := c.pending[key]
	c.mu.Unlock()
	if !ok {
		return captureNoopObserver{}, nil
	}
	return &captureObserver{
		capture: c,
		key:     key,
		turn:    turn,
		acc:     &AssistantAccumulator{},
	}, nil
}

// Start starts the single writer goroutine over ctx. It is idempotent.
func (c *Capture) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.startOnce.Do(func() {
		c.started.Store(true)
		go c.run(ctx)
	})
	return nil
}

// Stop closes the done channel (never the queue), then waits for the writer to
// drain the queued jobs, or for ctx. That ctx is also the drain budget of
// design §9: "drain what is already queued until the stop ctx expires". When
// it expires, Stop cancels the writer's context root before returning, so the
// store write that is in flight right now is cancelled with the budget instead
// of continuing under its own five-second limit, and the writer's drain path
// stops dequeuing. A finish after Stop is dropped without panicking; the queue
// channel is never closed (Requirement 3.5). Stop is idempotent and safe
// before Start.
func (c *Capture) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.stopOnce.Do(func() {
		c.stopMu.Lock()
		c.stopCtx = ctx
		// The stopped flag is published before done is closed, so a queue
		// publication is either queued before the stop — and therefore drained
		// — or refused. A closed done channel racing a ready buffered send
		// would admit work no writer is left to process.
		c.stopped = true
		c.stopMu.Unlock()
		close(c.done)
	})
	if !c.started.Load() {
		return nil
	}
	select {
	case <-c.writerDone:
		return nil
	case <-ctx.Done():
		c.cancelWriter()
		return ctx.Err()
	}
}

// cancelWriter cancels the writer's context root, which bounds the store write
// that is in flight when the shutdown budget expires. It is a no-op when the
// writer never started.
func (c *Capture) cancelWriter() {
	c.stopMu.Lock()
	cancelWrites := c.cancelWrites
	c.stopMu.Unlock()
	if cancelWrites != nil {
		cancelWrites()
	}
}

// run is the single writer loop. A closed done channel drains what is already
// queued until the stop ctx expires and exits (design §9); cancelling the
// lifecycle ctx passed to Start ends the writer as well, so a retired
// generation leaves no owned goroutine behind. The loop prefers lifecycle
// cancellation and done over a ready queue so no job dequeued after either can
// escape the drain budget. The context root is cancelled by Stop when that
// budget expires — bounding the write already in flight — and released on the
// normal path.
func (c *Capture) run(ctx context.Context) {
	defer close(c.writerDone)
	writeRoot, cancelWrites := context.WithCancel(ctx)
	defer cancelWrites()
	c.stopMu.Lock()
	c.cancelWrites = cancelWrites
	c.stopMu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			c.drain(ctx, writeRoot)
			return
		default:
		}
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			c.drain(ctx, writeRoot)
			return
		case job := <-c.queue:
			c.writeJob(writeRoot, nil, job)
		}
	}
}

// drain writes the jobs already queued when Stop closed done for as long as the
// stop budget lasts (design §9: "drain what is already queued until the stop
// ctx expires; return"). Admission stops at the first sign of an exhausted
// budget — a cancelled stop ctx, an exhausted absolute stop deadline, or a
// cancelled lifecycle ctx — so nothing is dequeued after the deadline even
// when the stop ctx's own cancellation is not yet observable. Every drained
// write carries the absolute stop deadline, so no store I/O starts, or
// outlives, that deadline.
func (c *Capture) drain(ctx context.Context, writeRoot context.Context) {
	stopCtx := c.stopBudget()
	stopDeadline, hasStopDeadline := stopCtx.Deadline()
	for {
		if stopCtx.Err() != nil || ctx.Err() != nil {
			return
		}
		if hasStopDeadline && !time.Now().Before(stopDeadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case job := <-c.queue:
			c.writeJob(writeRoot, stopCtx, job)
		default:
			return
		}
	}
}

// stopBudget returns the context passed to the first Stop. Stop publishes it
// before closing done, so the drain path that observed done always sees it.
// The fallback keeps an unreachable call shaped safely.
func (c *Capture) stopBudget() context.Context {
	c.stopMu.Lock()
	defer c.stopMu.Unlock()
	if c.stopCtx == nil {
		return context.Background()
	}
	return c.stopCtx
}

// captureWriteDeadline returns the absolute deadline of one store write: five
// seconds from now while running, clipped to an earlier stop deadline while
// draining (design §9). The stop deadline is absolute, so encoding a drained
// job cannot push its write past the stop budget.
func captureWriteDeadline(budget context.Context) time.Time {
	deadline := time.Now().Add(captureWriteTimeout)
	if budget == nil {
		return deadline
	}
	if stopDeadline, ok := budget.Deadline(); ok && stopDeadline.Before(deadline) {
		return stopDeadline
	}
	return deadline
}

// write encodes one queued job and upserts it, counting the outcome. Encoding
// (redaction, NUL removal and newest-first truncation) happens here, off the
// request path (design §9); logs carry bounded counts and kinds only. budget
// is nil while running — the write gets the five-second per-write limit — or
// the stop ctx while draining, which clips the write's absolute deadline to
// the stop deadline. A job whose write context is already dead before any
// store I/O is refused as a whole: the store is never invoked with an expired
// context, and nothing is counted because no write started.
func (c *Capture) write(ctx context.Context, budget context.Context, job captureJob) {
	transcript, truncated, err := EncodeTranscript(job.entries, c.cfg.MaxTranscriptBytes)
	if err != nil {
		c.metrics.Inc(EventCaptureError)
		c.log.Warn("proxy-memory capture failed", "event", EventCaptureError, "error_kind", captureErrorKind(err))
		return
	}
	if truncated {
		c.metrics.Inc(EventCaptureTruncated)
		c.log.Warn("proxy-memory capture truncated",
			"event", EventCaptureTruncated,
			"limit_bytes", c.cfg.MaxTranscriptBytes,
			"encoded_bytes", len(transcript))
	}
	writeCtx, cancel := context.WithDeadline(ctx, captureWriteDeadline(budget))
	defer cancel()
	if writeCtx.Err() != nil {
		// The budget — or the lifecycle — was exhausted before any store I/O:
		// the job is refused without a store call and without counting a
		// failure that never started.
		return
	}
	outcome, err := c.store.UpsertSession(writeCtx, SessionCapture{
		Owner:        job.owner,
		ProjectLabel: job.label,
		ALegID:       job.aLegID,
		Transcript:   transcript,
		ActivityMS:   job.activityMS,
		SourceSeq:    job.sourceSeq,
		ErasureEpoch: job.erasureEpoch,
	})
	if err != nil {
		c.metrics.Inc(EventCaptureError)
		c.log.Warn("proxy-memory capture failed", "event", EventCaptureError, "error_kind", captureErrorKind(err))
		return
	}
	switch outcome {
	case UpsertStored:
		c.metrics.Inc(EventCaptureRecorded)
	case UpsertOwnerMismatch:
		c.metrics.Inc(EventCaptureOwnerMismatch)
	case UpsertForgotten:
		c.metrics.Inc(EventCaptureForgotten)
	case UpsertUnbound:
		// The authoritative first-turn decision never committed, so the
		// capture has no binding to learn from. The capture is dropped, never
		// repaired: a missing binding cannot be reconstructed from a capture.
		c.metrics.Inc(EventCaptureUnbound)
		c.log.Debug("proxy-memory capture refused without a durable first-turn binding",
			"event", EventCaptureUnbound)
	case UpsertStale:
		c.metrics.Inc(EventCaptureStale)
		c.log.Debug("proxy-memory capture refused as stale", "event", EventCaptureStale)
	}
}

func (c *Capture) writeJob(ctx context.Context, budget context.Context, job writerJob) {
	switch {
	case job.firstTurn != nil:
		c.writeFirstTurn(ctx, budget, *job.firstTurn)
	case job.capture != nil:
		c.write(ctx, budget, *job.capture)
	}
}

func (c *Capture) writeFirstTurn(ctx context.Context, budget context.Context, job FirstTurnJob) {
	writeCtx, cancel := context.WithDeadline(ctx, captureWriteDeadline(budget))
	defer cancel()
	if !job.EpochKnown || writeCtx.Err() != nil {
		c.decisions.MarkWriteFailed(job)
		return
	}
	committed, err := c.store.CommitFirstTurn(writeCtx, job)
	if err != nil {
		c.decisions.MarkWriteFailed(job)
		c.metrics.Inc(EventInjectError)
		c.log.Warn("proxy-memory first-turn decision write failed",
			"event", EventInjectError, "error_kind", captureErrorKind(err))
		return
	}
	if !committed {
		c.decisions.MarkWriteFailed(job)
		c.metrics.Inc(EventInjectDecisionDropped)
		c.log.Debug("proxy-memory first-turn decision dropped", "event", EventInjectDecisionDropped)
	}
}

// captureErrorKind classifies a write failure into a bounded log value; the
// raw error never reaches a log record because it can embed store content.
func captureErrorKind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "store_error"
	}
}

// captureObserver accumulates one B-leg's released output over its pending
// turn. A finish that is not OutcomeSuccessReleased drops the accumulator and
// records nothing (Requirement 3.4).
type captureObserver struct {
	capture *Capture
	key     string
	turn    pendingTurn
	acc     *AssistantAccumulator
}

// Observe never returns an error and never blocks (design §9).
func (o *captureObserver) Observe(_ context.Context, ev aipapi.Event) error {
	o.acc.Observe(ev)
	return nil
}

// Finish records the turn only when the runtime released a successful
// response. It deletes the pending entry and enqueues without blocking; a
// full queue or a stopped writer drops the capture without failing anything
// (Requirements 3.4, 3.5).
func (o *captureObserver) Finish(_ context.Context, outcome response.StreamOutcome) error {
	if outcome != response.OutcomeSuccessReleased {
		return nil
	}
	o.capture.record(o.key, o.turn, o.acc.Entries())
	return nil
}

// record builds the capture job, clears the pending entry and enqueues the job
// without blocking. The entries hold string references only, so this costs
// O(entries) (design §9).
func (c *Capture) record(key string, turn pendingTurn, assistant []Entry) {
	job := captureJob{
		owner:        turn.owner,
		label:        turn.label,
		aLegID:       turn.aLegID,
		entries:      slices.Concat(turn.entries, assistant),
		activityMS:   c.now().UnixMilli(),
		sourceSeq:    turn.sourceSeq,
		erasureEpoch: turn.erasureEpoch,
	}
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
	if !c.publish(writerJob{capture: &job}) {
		// A stopped writer or a full queue drops the capture without failing
		// anything (Requirements 3.4, 3.5).
		c.metrics.Inc(EventCaptureDroppedQueue)
		c.log.Warn("proxy-memory capture dropped", "event", EventCaptureDroppedQueue)
	}
}

// captureNoopObserver is the fail-open observer for a turn that has no pending
// entry: every call succeeds and records nothing.
type captureNoopObserver struct{}

func (captureNoopObserver) Observe(context.Context, aipapi.Event) error { return nil }

func (captureNoopObserver) Finish(context.Context, response.StreamOutcome) error { return nil }
