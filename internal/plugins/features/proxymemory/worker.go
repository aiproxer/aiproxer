package proxymemory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

const (
	// workerPurgeLimit bounds one purge step (design §10 step 1).
	workerPurgeLimit = 500
	// workerLeaseOwnerBytes is the entropy of one generation's lease owner
	// (design §10: 16 random bytes, hex).
	workerLeaseOwnerBytes = 16
	// workerClaimLimit is the one-row claim batch of both phases (design §10:
	// claim one session or bank at a time, so a lease always covers the item
	// its auxiliary attempt runs under).
	workerClaimLimit = 1
)

// workerPhase1CoalesceKey is the scheduler identity of one phase 1 collection
// (design §10 step 2.3): the generation's lease owner, the claimed A-leg and
// its revision. It names one committed continuity transaction, so a duplicate
// submission of the same claim coalesces instead of billing twice.
func workerPhase1CoalesceKey(leaseOwner, aLegID string, revision int64) string {
	return "proxy-memory/p1/" + leaseOwner + "/" + aLegID + "/" + strconv.FormatInt(revision, 10)
}

// workerPhase2CoalesceKey is the scheduler identity of one phase 2 collection
// (design §10 step 3.3): the generation's lease owner, the bank's opaque
// project key and its claimed dirty sequence.
func workerPhase2CoalesceKey(leaseOwner, projectKey string, dirtySeq int64) string {
	return "proxy-memory/p2/" + leaseOwner + "/" + projectKey + "/" + strconv.FormatInt(dirtySeq, 10)
}

// Worker is the proxy-memory background worker (design §10). It runs one
// generation's tick loop: each tick purges expired rows, extracts up to
// Phase1Batch idle sessions and consolidates up to Phase2Batch dirty banks
// through the private auxiliary client, always one leased claim at a time.
// The worker owns no store wiring and no views; the store and the auxiliary
// client are injected by the composition step.
//
// Lifecycle: Start generates the generation's lease owner and starts the loop;
// the first Tick runs after one PollInterval, never at Start. QuiescePhase is
// true, so a retired generation stops extracting; SafeUnderCandidateOverlap is
// true because every claim and publish is fenced by the store leases.
type Worker struct {
	cfg     Config
	store   Store
	aux     auxiliary.BackgroundClient
	metrics *Metrics
	now     func() time.Time
	log     *slog.Logger

	// newTicker builds the loop's tick source. It is a seam so tests can drive
	// ticks without wall-clock sleeps; production builds time.NewTicker.
	newTicker func(time.Duration) workerTicker

	// mu guards the whole lifecycle state — lifecycle, loopCancel and the
	// leaseOwner Start publishes — as one mutex-guarded state machine, with no
	// separate once or atomic fields. Stop reads the state under this same
	// lock every time it runs, so it is a latch-free operation: a Stop can
	// neither be consumed by a Start that has not happened yet nor lost to
	// one that races it.
	mu         sync.Mutex
	lifecycle  workerLifecycle
	loopCancel context.CancelFunc

	// leaseOwner fences every claim and names the generation in auxiliary
	// coalesce keys. Start publishes it under mu before the loop starts, and
	// only the loop reads it afterwards.
	leaseOwner string

	done chan struct{}
}

// workerLifecycle is the worker's single lifecycle state machine, guarded by
// Worker.mu:
//
//	workerLifecycleIdle --Start--> workerLifecycleRunning
//
// Start moves an idle worker to running once; while a loop has been launched
// (running, even after it exited) Start is idempotent and the generation keeps
// its lease owner. Stop is not a latch: it cancels whichever loop the state
// publishes at the time it runs, so a Stop issued before Start finds nothing
// to cancel and every later Stop observes the cancel Start published.
type workerLifecycle int

const (
	// workerLifecycleIdle is a worker whose loop has never been launched.
	workerLifecycleIdle workerLifecycle = iota
	// workerLifecycleRunning is a worker whose loop has been launched; the
	// loop may already have exited.
	workerLifecycleRunning
)

// workerTicker is the tick source of the loop. *time.Ticker does not expose
// its channel through an interface, so the loop consumes this minimal shape.
type workerTicker interface {
	ticks() <-chan time.Time
	stop()
}

// realWorkerTicker adapts *time.Ticker to the workerTicker seam.
type realWorkerTicker struct {
	ticker *time.Ticker
}

func (t realWorkerTicker) ticks() <-chan time.Time { return t.ticker.C }
func (t realWorkerTicker) stop()                   { t.ticker.Stop() }

// newRealWorkerTicker builds the production tick source (design §10:
// time.NewTicker(cfg.PollInterval)).
func newRealWorkerTicker(d time.Duration) workerTicker {
	return realWorkerTicker{ticker: time.NewTicker(d)}
}

// NewWorker constructs the worker over the store and auxiliary seams. now is
// the injectable clock (time.Now when nil) and log receives content-free
// degradation records (slog.Default when nil).
func NewWorker(cfg Config, store Store, aux auxiliary.BackgroundClient, m *Metrics, now func() time.Time, log *slog.Logger) *Worker {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Worker{
		cfg:       cfg,
		store:     store,
		aux:       aux,
		metrics:   m,
		now:       now,
		log:       log,
		newTicker: newRealWorkerTicker,
		done:      make(chan struct{}),
	}
}

// Start generates this generation's lease owner (16 random bytes, lowercase
// hex) and starts the tick loop over ctx. The first Tick runs after one
// cfg.PollInterval, never at Start, because lifecycles start while the
// generation is still a candidate that may be rolled back (design §10). Start
// is idempotent: once a loop has been launched the generation keeps its
// identity and lease owner, and the interval guard keeps a misconfigured
// interval from panicking time.NewTicker inside the loop goroutine.
func (w *Worker) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lifecycle != workerLifecycleIdle {
		return nil
	}
	// A non-positive interval would panic time.NewTicker in the loop
	// goroutine, where no recovery could save the process. The composition
	// validates the config first; this guard keeps the failure a returned
	// error even when a caller skips that validation.
	if w.cfg.PollInterval <= 0 {
		return fmt.Errorf("%s: worker poll interval must be positive", ID)
	}
	leaseOwner, err := newWorkerLeaseOwner()
	if err != nil {
		return err
	}
	loopCtx, cancel := context.WithCancel(ctx)
	w.loopCancel = cancel
	w.leaseOwner = leaseOwner
	w.lifecycle = workerLifecycleRunning
	go func() {
		defer cancel()
		w.run(loopCtx)
	}()
	return nil
}

// QuiescePhase opts the worker into PhaseQuiesce stop semantics: a retired
// generation stops extracting under its stale configuration (design §10,
// §14.3; precedent keepwarmGenerationLifecycle).
func (w *Worker) QuiescePhase() bool { return true }

// SafeUnderCandidateOverlap reports that two worker generations may overlap:
// the store leases fence every claim, completion and publish (design §10,
// §14.3).
func (w *Worker) SafeUnderCandidateOverlap() bool { return true }

// Stop cancels the loop context — which makes an in-flight auxiliary Await
// observe ctx.Err() and stops the loop — and waits until the loop exited or
// ctx is done (design §10, §14.3). Stop is idempotent and safe before Start.
//
// Lifecycle decision (design §10 Start/Stop): the worker keeps one
// mutex-guarded state, and Stop is a latch-free operation on it — it cancels
// whatever loop the state publishes and joins it. A Stop issued before any
// Start therefore finds no loop and returns immediately, and the cancel a
// later Start publishes is observed by the next Stop; the cancellation can
// never be consumed permanently, and a Stop that races Start either cancels
// the published loop or is followed by a Stop that does. Stopping does not
// prevent a later Start from launching a loop; the retired generation stops
// with the same cancel/join path whenever Stop is called again.
func (w *Worker) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	running := w.lifecycle == workerLifecycleRunning
	cancel := w.loopCancel
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if !running {
		return nil
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run is the generation's tick loop: one Tick per tick of the injected tick
// source. The loop exits when the lifecycle context ends or Stop cancels the
// loop context (design §10).
func (w *Worker) run(ctx context.Context) {
	defer close(w.done)
	ticker := w.newTicker(w.cfg.PollInterval)
	defer ticker.stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.ticks():
			w.Tick(ctx)
		}
	}
}

// Tick runs one worker iteration in design §10's order: purge, then up to
// Phase1Batch phase 1 claims, then up to Phase2Batch phase 2 claims. It never
// panics outward: a panic from any step is recovered, counted as worker_panic
// and logged without content. Tick is exported for tests.
func (w *Worker) Tick(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			// The recovered value can embed store or model content, so it is
			// classified, never logged.
			w.metrics.Inc(EventWorkerPanic)
			w.log.Warn("proxy-memory worker recovered from panic", "event", EventWorkerPanic)
		}
	}()
	w.purge(ctx)
	w.phase1(ctx)
	w.phase2(ctx)
}

// purge runs the tick's maintenance step (design §10 step 1): aged sessions,
// aged extractions, fenced sessions and retired bindings are deleted in one
// bounded call, and the returned row total is counted as phase1_purged
// (Requirements 4.8, 5.2). A purge failure is logged with a bounded kind and
// leaves the rest of the tick intact.
func (w *Worker) purge(ctx context.Context) {
	now := w.now()
	deleted, err := w.store.Purge(ctx, now.UnixMilli(),
		now.Add(-w.cfg.MaxSessionAge).UnixMilli(),
		now.Add(-w.cfg.MaxUnusedAge).UnixMilli(),
		workerPurgeLimit)
	if err != nil {
		w.log.Warn("proxy-memory purge failed", "error_kind", workerErrorKind(err))
		return
	}
	w.countPurged(deleted)
}

// countPurged records the purge step's row total. EventPhase1Purged counts
// rows removed, so the returned total is added to the counter; Inc, the closed
// single-event API, cannot express a row total. A zero-row purge counts
// nothing, which also keeps the series absent until it has a value.
func (w *Worker) countPurged(deleted int) {
	if w.metrics == nil || deleted <= 0 {
		return
	}
	w.metrics.events.WithLabelValues(EventPhase1Purged).Add(float64(deleted))
}

// claimQuery builds one session claim under this generation's lease (design
// §10 step 2): the idle window, the maximum session age, and a lease that
// always outlives the item's AuxTimeout.
func (w *Worker) claimQuery(now time.Time) ClaimQuery {
	return ClaimQuery{
		NowMS:        now.UnixMilli(),
		IdleBeforeMS: now.Add(-w.cfg.MinIdle).UnixMilli(),
		NotBeforeMS:  now.Add(-w.cfg.MaxSessionAge).UnixMilli(),
		LeaseUntilMS: now.Add(w.cfg.Lease).UnixMilli(),
		LeaseOwner:   w.leaseOwner,
		Limit:        workerClaimLimit,
	}
}

// bankClaimQuery builds one bank claim under this generation's lease. The bank
// candidate query of §12.3 bounds only the retry time and the lease: banks
// carry no idle window.
func (w *Worker) bankClaimQuery(now time.Time) ClaimQuery {
	return ClaimQuery{
		NowMS:        now.UnixMilli(),
		LeaseUntilMS: now.Add(w.cfg.Lease).UnixMilli(),
		LeaseOwner:   w.leaseOwner,
		Limit:        workerClaimLimit,
	}
}

// phase1 runs up to Phase1Batch extraction attempts, claiming one session at a
// time and processing it before the next claim (design §10 step 2). A claim
// failure is logged and counted as phase1_failed, the only bounded phase 1
// failure event, and ends the phase for this tick.
func (w *Worker) phase1(ctx context.Context) {
	for range w.cfg.Phase1Batch {
		now := w.now()
		claimed, err := w.store.ClaimSessions(ctx, w.claimQuery(now))
		if err != nil {
			w.metrics.Inc(EventPhase1Failed)
			w.log.Warn("proxy-memory phase 1 claim failed", "event", EventPhase1Failed, "error_kind", workerErrorKind(err))
			return
		}
		if len(claimed) == 0 {
			return
		}
		w.processSession(ctx, claimed[0], w.claimDeadline(now))
	}
}

// processSession runs one phase 1 attempt (design §10 step 2): build the
// stage-one request, collect the auxiliary reply under the owner's internal
// scope, parse it, and complete or fail the claim.
func (w *Worker) processSession(ctx context.Context, s ClaimedSession, deadline time.Time) {
	target, err := w.claimTarget(ctx, s.Owner, PhaseExtraction, deadline)
	if err != nil {
		w.failSession(ctx, s, err)
		return
	}
	req, err := BuildPhase1Request(w.cfg, Phase1Input{
		SessionRef:   SessionRef(s.ALegID),
		ProjectLabel: s.ProjectLabel,
		Transcript:   s.Transcript,
	})
	if err != nil {
		w.failSession(ctx, s, err)
		return
	}
	req.Call.Route.Selector = target.route
	req.Call.Options.ReasoningEffort = target.effort
	// The phase's target is compiled and checked before the dispatch: the
	// final compiled actual target is the only target the policy may be
	// checked against, and a phase is never dispatched against an unchecked
	// one (design §1 and §3; Requirements 1.4 and 1.5).
	if err := enforceCompiledTarget(ctx, w.cfg, PhaseExtraction, req.Call); err != nil {
		w.countModelPolicyRefusal(err)
		w.failSession(ctx, s, err)
		return
	}
	collected, err := w.collect(ctx, s.Owner, req, workerPhase1CoalesceKey(w.leaseOwner, s.ALegID, s.Revision), deadline)
	if err != nil {
		if errors.Is(err, errInvalidAuxResult) {
			w.failSessionInvalidResult(ctx, s, collected.Receipt, err)
			return
		}
		w.failSession(ctx, s, err)
		return
	}
	out, err := ParsePhase1(collected.Text)
	if err != nil {
		w.failSessionInvalidResult(ctx, s, collected.Receipt, err)
		return
	}
	extraction := Extraction{
		Owner:           s.Owner,
		ALegID:          s.ALegID,
		SessionRef:      SessionRef(s.ALegID),
		RawMemory:       out.RawMemory,
		RolloutSummary:  out.RolloutSummary,
		RolloutSlug:     out.RolloutSlug,
		SourceUpdatedMS: s.LastActivityMS,
		GeneratedAtMS:   w.now().UnixMilli(),
	}
	if err := w.store.CompleteSession(ctx, s, extraction); err != nil {
		if errors.Is(err, ErrLeaseLost) {
			// Another instance owns the claim now; its result stands and this
			// attempt is neither a success nor a failure (design §10 step 2.5).
			w.metrics.Inc(EventPhase1LeaseLost)
			return
		}
		// The completion rolled back. Route the non-lease failure through the
		// failure path (design §10 step 2.6, Requirement 4.5) so a transient
		// commit failure clears the claim and retries at now+RetryBackoff
		// instead of waiting out the lease.
		w.failSession(ctx, s, err)
		return
	}
	w.metrics.Inc(EventPhase1Succeeded)
}

// countModelPolicyRefusal counts the model-policy refusals that carry a
// bounded classification. The refusal is already counted on the phase's
// ordinary failure path; this counter makes the policy's own reason visible
// without parsing a message, and it stays one bounded event series.
func (w *Worker) countModelPolicyRefusal(err error) {
	if w == nil || err == nil {
		return
	}
	if errors.Is(err, ErrReasoningCapabilityRefused) {
		w.metrics.Inc(EventModelPolicyRefusedReasoning)
	}
}

// failSession completes one failed phase 1 attempt: the transcript stays, the
// next attempt is scheduled at now+RetryBackoff, and the attempt is counted as
// phase1_failed (design §10 step 2.6, Requirement 4.5). It is the single
// phase 1 failure path — a failed collection, parse or completion all land
// here — and it never recurses: when the FailSession write itself fails, the
// attempt is still counted through the same counter exactly once and the
// bounded log records the classification, so the claim is left to its lease
// expiry. A lease lost while recording the failure is counted as
// phase1_lease_lost and is not a failure.
func (w *Worker) failSession(ctx context.Context, s ClaimedSession, cause error) {
	if err := w.store.FailSession(ctx, s, w.now().Add(w.cfg.RetryBackoff).UnixMilli()); err != nil {
		if errors.Is(err, ErrLeaseLost) {
			w.metrics.Inc(EventPhase1LeaseLost)
			return
		}
		w.log.Warn("proxy-memory phase 1 failure could not be recorded", "event", EventPhase1Failed, "error_kind", workerErrorKind(err))
	}
	w.metrics.Inc(EventPhase1Failed)
	w.log.Warn("proxy-memory phase 1 failed", "event", EventPhase1Failed, "error_kind", workerErrorKind(cause))
}

// failSessionInvalidResult records the phase 1 memory-processing outcome for a
// completed collection the parser or validator rejected. A linked receipt
// becomes one immutable `invalid_result` fact in the same store transaction
// that conditionally releases this still-live claim; a missing receipt keeps
// the existing usage-only failure path and never fabricates a link.
//
// The write is lexical and bounded by the same settlement reserve the claim
// deadline already uses. A failed or conflicting write is a bounded
// degradation: no durable evidence is claimed, no second failure write runs,
// and the claim is left to its lease expiry. The paid operation is unchanged.
func (w *Worker) failSessionInvalidResult(ctx context.Context, s ClaimedSession, receipt CollectionReceipt, cause error) {
	if !receipt.Linked() {
		// Usage-only compatibility: release the claim through the legacy path,
		// but make the loss of the durable join an explicit, content-free
		// degradation. Nothing is fabricated and no billing requirement is
		// imposed on a usage-only host.
		w.metrics.Inc(EventPhase1InvalidResultUnlinked)
		w.log.Warn("proxy-memory phase 1 invalid result has no host receipt",
			"event", EventPhase1InvalidResultUnlinked, "error_kind", "unlinked")
		w.failSession(ctx, s, cause)
		return
	}
	if w.cfg.MaxUnusedAge <= 0 || w.cfg.Lease <= 0 {
		w.failSession(ctx, s, cause)
		return
	}
	now := w.now()
	outcome, err := w.recordFailure(ctx, FailureEvidence{
		Owner:         s.Owner,
		Phase:         FailurePhaseExtraction,
		StoreID:       receipt.StoreID,
		BillingCallID: receipt.BillingCallID,
		ClaimID:       s.ALegID,
		ClaimRevision: s.Revision,
		LeaseOwner:    s.LeaseOwner,
		LeaseUntilMS:  s.LeaseUntilMS,
		ErasureEpoch:  s.ErasureEpoch,
		RetryAtMS:     now.Add(w.cfg.RetryBackoff).UnixMilli(),
		Kind:          FailureKindInvalidResult,
		CompletedAtMS: receipt.CompletedAt.UnixMilli(),
		ExpiresAtMS:   receipt.CompletedAt.Add(w.cfg.MaxUnusedAge).UnixMilli(),
		RecordedAtMS:  now.UnixMilli(),
	})
	if err != nil {
		w.metrics.Inc(EventPhase1Failed)
		w.log.Warn("proxy-memory phase 1 invalid-result evidence could not be committed",
			"event", EventPhase1Failed, "error_kind", workerErrorKind(err))
		return
	}
	switch outcome {
	case FailureRecorded, FailureDuplicate:
		w.metrics.Inc(EventPhase1Failed)
		w.log.Warn("proxy-memory phase 1 invalid result",
			"event", EventPhase1Failed, "error_kind", "invalid_result")
	case FailureRecordedStale, FailureErased:
		w.metrics.Inc(EventPhase1LeaseLost)
		w.log.Warn("proxy-memory phase 1 invalid-result evidence is fenced",
			"event", EventPhase1LeaseLost, "error_kind", "invalid_result_stale")
	case FailureExpired:
		w.metrics.Inc(EventPhase1Failed)
		w.log.Warn("proxy-memory phase 1 invalid-result receipt expired",
			"event", EventPhase1Failed, "error_kind", "invalid_result_expired")
	default:
		w.metrics.Inc(EventPhase1Failed)
		w.log.Warn("proxy-memory phase 1 invalid-result evidence outcome is unknown",
			"event", EventPhase1Failed, "error_kind", "invalid_result_outcome")
	}
}

// failBankInvalidResult is the phase 2 half of the linked invalid-result path.
// The fact carries the claimed bank version and dirty sequence, so the store
// can distinguish this live claim from a successor.
func (w *Worker) failBankInvalidResult(ctx context.Context, b ClaimedBank, receipt CollectionReceipt, cause error) {
	if !receipt.Linked() {
		w.metrics.Inc(EventPhase2InvalidResultUnlinked)
		w.log.Warn("proxy-memory phase 2 invalid result has no host receipt",
			"event", EventPhase2InvalidResultUnlinked, "error_kind", "unlinked")
		w.failBank(ctx, b, cause)
		return
	}
	if w.cfg.MaxUnusedAge <= 0 || w.cfg.Lease <= 0 {
		w.failBank(ctx, b, cause)
		return
	}
	now := w.now()
	outcome, err := w.recordFailure(ctx, FailureEvidence{
		Owner:         b.Owner,
		Phase:         FailurePhaseConsolidation,
		StoreID:       receipt.StoreID,
		BillingCallID: receipt.BillingCallID,
		ClaimID:       b.Owner.ProjectKey,
		ClaimRevision: b.Version,
		ClaimSeq:      b.DirtySeq,
		LeaseOwner:    b.LeaseOwner,
		LeaseUntilMS:  b.LeaseUntilMS,
		ErasureEpoch:  b.ErasureEpoch,
		RetryAtMS:     now.Add(w.cfg.RetryBackoff).UnixMilli(),
		Kind:          FailureKindInvalidResult,
		CompletedAtMS: receipt.CompletedAt.UnixMilli(),
		ExpiresAtMS:   receipt.CompletedAt.Add(w.cfg.MaxUnusedAge).UnixMilli(),
		RecordedAtMS:  now.UnixMilli(),
	})
	if err != nil {
		w.metrics.Inc(EventPhase2Failed)
		w.log.Warn("proxy-memory phase 2 invalid-result evidence could not be committed",
			"event", EventPhase2Failed, "error_kind", workerErrorKind(err))
		return
	}
	switch outcome {
	case FailureRecorded, FailureDuplicate:
		w.metrics.Inc(EventPhase2Failed)
		w.log.Warn("proxy-memory phase 2 invalid result",
			"event", EventPhase2Failed, "error_kind", "invalid_result")
	case FailureRecordedStale, FailureErased:
		w.metrics.Inc(EventPhase2LeaseLost)
		w.log.Warn("proxy-memory phase 2 invalid-result evidence is fenced",
			"event", EventPhase2LeaseLost, "error_kind", "invalid_result_stale")
	case FailureExpired:
		w.metrics.Inc(EventPhase2Failed)
		w.log.Warn("proxy-memory phase 2 invalid-result receipt expired",
			"event", EventPhase2Failed, "error_kind", "invalid_result_expired")
	default:
		w.metrics.Inc(EventPhase2Failed)
		w.log.Warn("proxy-memory phase 2 invalid-result evidence outcome is unknown",
			"event", EventPhase2Failed, "error_kind", "invalid_result_outcome")
	}
}

// recordFailure runs the one feature-store failure transaction under a lexical
// timeout: min(one second, Lease/10), the same reserve the collection deadline
// already withholds. Parent cancellation wins; there is no background context,
// WithoutCancel, goroutine or second lifecycle owner. A non-positive reserve is
// refused explicitly instead of becoming an unlimited timeout.
func (w *Worker) recordFailure(ctx context.Context, evidence FailureEvidence) (FailureOutcome, error) {
	reserve := min(time.Second, w.cfg.Lease/10)
	if reserve <= 0 {
		return "", ErrFailureInvalid
	}
	if err := evidence.Validate(); err != nil {
		return "", err
	}
	settleCtx, cancel := context.WithTimeout(ctx, reserve)
	defer cancel()
	return w.store.RecordPhaseFailure(settleCtx, evidence)
}

// phase2 runs up to Phase2Batch consolidation attempts, claiming one bank at a
// time and processing it before the next claim (design §10 step 3). A claim
// failure is logged and counted as phase2_failed, the only bounded phase 2
// failure event, and ends the phase for this tick.
func (w *Worker) phase2(ctx context.Context) {
	for range w.cfg.Phase2Batch {
		now := w.now()
		claimed, err := w.store.ClaimBanks(ctx, w.bankClaimQuery(now))
		if err != nil {
			w.metrics.Inc(EventPhase2Failed)
			w.log.Warn("proxy-memory phase 2 claim failed", "event", EventPhase2Failed, "error_kind", workerErrorKind(err))
			return
		}
		if len(claimed) == 0 {
			return
		}
		w.processBank(ctx, claimed[0], w.claimDeadline(now))
	}
}

var (
	// errPhase2NoteBudget marks a phase 2 attempt whose pending notes the
	// input budget cannot carry and which has no other durable progress to
	// make: the attempt would buy an inference without consolidating
	// anything.
	errPhase2NoteBudget = errors.New("proxy-memory: pending note cannot fit phase 2 input; increase the configured input budget")
	// errPhase2ExtractionBudget marks a phase 2 attempt with no useful
	// extraction, note or removal progress. A single extraction that cannot
	// fit is never acknowledged or skipped to reach a later extraction.
	errPhase2ExtractionBudget = errors.New("proxy-memory: extraction cannot fit phase 2 input; increase the configured input budget")
)

// phase2ProgressFailure reports the bounded failure of a phase 2 attempt that
// must not be published.
//
// A publication must make progress. Three things count:
//
//   - At least one pending note was carried; it is consumed by the
//     publication.
//   - At least one outstanding extraction revision was carried.
//   - A legitimate removal: a previously consolidated summary has disappeared
//     from the consolidation window, so the published memory can drop its
//     reference to it.
//
// Replaying an already consolidated extraction is not progress. It changes
// nothing, and counting it would let a permanently unprocessable note drive
// paid consolidations for as long as the bank stays claimable (the residual
// note keeps it dirty after every publication).
//
// The builder carries an oldest prefix of whole extraction pairs. A budget-
// excluded tail is not a publication blocker if this attempt carries genuine
// progress: PublishBank acknowledges only the carried revisions, and the tail
// stays durably outstanding. Store-level cap exclusions obey the same rule.
func phase2ProgressFailure(b ClaimedBank, batch ConsolidationBatch, included Phase2Included) error {
	consolidated := make(map[string]struct{}, len(b.ConsolidatedNames))
	for _, name := range b.ConsolidatedNames {
		consolidated[name] = struct{}{}
	}
	window := make(map[string]struct{}, len(batch.WindowNames))
	for _, name := range batch.WindowNames {
		window[name] = struct{}{}
	}
	// The builder selects whole extraction pairs. Count those exact revisions,
	// not rendered names, which can collide for distinct A-legs. Only these
	// revisions may be acknowledged, regardless of the excluded batch tail.
	carriedNew := len(included.Extractions)
	removed := 0
	for name := range consolidated {
		if _, ok := window[name]; !ok {
			removed++
		}
	}
	if len(included.NoteIDs) > 0 || carriedNew > 0 || removed > 0 {
		return nil
	}
	if len(batch.Notes) > 0 {
		return errPhase2NoteBudget
	}
	return errPhase2ExtractionBudget
}

// processBank runs one phase 2 attempt (design §10 step 3): read the
// consolidation window, publish an empty memory when nothing is left, or build
// and collect a consolidation and publish the parsed result.
func (w *Worker) processBank(ctx context.Context, b ClaimedBank, deadline time.Time) {
	target, err := w.claimTarget(ctx, b.Owner, PhaseConsolidation, deadline)
	if err != nil {
		w.failBank(ctx, b, err)
		return
	}
	now := w.now()
	sinceMS := now.Add(-w.cfg.MaxUnusedAge).UnixMilli()
	batch, err := w.store.ConsolidationInput(ctx, b, sinceMS, w.cfg.MaxRawMemories)
	if err != nil {
		w.failBank(ctx, b, err)
		return
	}
	if len(batch.Extractions) == 0 && len(batch.Notes) == 0 {
		retained := retainedConsolidationNames(b, batch.WindowNames, Phase2Included{})
		// A drained batch is not an empty memory window. Keep the existing
		// documents when all their identities are still live. Actual removals
		// from a nonempty window go through the model below.
		if len(batch.WindowNames) == 0 || len(retained) == len(b.ConsolidatedNames) {
			// Nothing is left in the selection window: publish the retained
			// identity set so the bank settles instead of dirtying every tick
			// (Requirement 5.6). The store advances the consolidated sequence only
			// because no in-window extraction remains outstanding.
			publication := Publication{
				ConsolidatedNames: retained,
				WindowSinceMS:     sinceMS,
				PublishedAtMS:     w.now().UnixMilli(),
			}
			if len(batch.WindowNames) > 0 {
				publication.MemoryMD = b.MemoryMD
				publication.MemorySummary = b.MemorySummary
			}
			if err := w.store.PublishBank(ctx, b, publication); err != nil {
				w.failPublish(ctx, b, err)
				return
			}
			w.metrics.Inc(EventPhase2PublishedEmpty)
			return
		}
	}
	req, included, err := BuildPhase2Request(w.cfg, Phase2Input{
		CurrentSummary:  b.MemorySummary,
		CurrentMemoryMD: b.MemoryMD,
		ProjectLabel:    b.ProjectLabel,
		Extractions:     batch.Extractions,
		WindowNames:     batch.WindowNames,
		Notes:           batch.Notes,
		PreviousNames:   b.ConsolidatedNames,
	})
	if err != nil {
		w.failBank(ctx, b, err)
		return
	}
	req.Call.Route.Selector = target.route
	req.Call.Options.ReasoningEffort = target.effort
	if included.Truncated {
		// The builder left out notes and/or the newest extractions. The
		// omission is counted and logged as soon as the input is fixed, ahead
		// of the collection and the parse, so a failing attempt still proves
		// exactly what input the model was asked to work on and every path
		// counts it exactly once (Requirement 5.9; no hidden degradation).
		// The record is content-free: bounded counts only.
		w.metrics.Inc(EventPhase2Truncated)
		w.log.Warn("proxy-memory phase 2 input truncated",
			"event", EventPhase2Truncated,
			"included_notes", len(included.NoteIDs),
			"included_extractions", len(included.SummaryNames))
	}
	if err := phase2ProgressFailure(b, batch, included); err != nil {
		// The request carries no outstanding extraction revision, pending
		// note or legitimate removal. In particular, an oversized oldest
		// extraction cannot be skipped to reach newer content. All excluded
		// work stays pending; retry backoff and bounded failure classification
		// expose the budget problem.
		w.failBank(ctx, b, err)
		return
	}
	// The phase's target is compiled and checked before the dispatch: the
	// final compiled actual target is the only target the policy may be
	// checked against, and a phase is never dispatched against an unchecked
	// one (design §1 and §3; Requirements 1.4 and 1.5).
	if err := enforceCompiledTarget(ctx, w.cfg, PhaseConsolidation, req.Call); err != nil {
		w.countModelPolicyRefusal(err)
		w.failBank(ctx, b, err)
		return
	}
	collected, err := w.collect(ctx, b.Owner, req, workerPhase2CoalesceKey(w.leaseOwner, b.Owner.ProjectKey, b.DirtySeq), deadline)
	if err != nil {
		if errors.Is(err, errInvalidAuxResult) {
			w.failBankInvalidResult(ctx, b, collected.Receipt, err)
			return
		}
		w.failBank(ctx, b, err)
		return
	}
	out, err := ParsePhase2(collected.Text)
	if err != nil {
		w.failBankInvalidResult(ctx, b, collected.Receipt, err)
		return
	}
	publication := Publication{
		MemoryMD:          out.MemoryMD,
		MemorySummary:     out.MemorySummary,
		ConsolidatedNames: retainedConsolidationNames(b, batch.WindowNames, included),
		ConsumedNoteIDs:   included.NoteIDs,
		Acknowledged:      included.Extractions,
		WindowSinceMS:     sinceMS,
		PublishedAtMS:     w.now().UnixMilli(),
	}
	if err := w.store.PublishBank(ctx, b, publication); err != nil {
		w.failPublish(ctx, b, err)
		return
	}
	w.metrics.Inc(EventPhase2Published)
}

// retainedConsolidationNames builds the summary names one publication records
// as consolidated: everything the request carried, plus the previously
// consolidated summaries that are still in the consolidation window but were
// left out of this prompt. The window is the identity set, not the selected
// batch: an extraction that the per-attempt cap or the input budget left out is
// still in the memory folder and its content was already consolidated.
//
// Omitting an extraction from one prompt is not the same as deleting it. The
// request's CHANGES section compares against the window, so the model is not
// told it was removed. Dropping its name from the publication instead would
// make the next attempt treat the same extraction as new, and an extraction
// that is permanently too large would then refuse every later consolidation —
// including the ones whose notes and extractions do fit.
//
// A previously consolidated summary that has left the window is not retained:
// that publication removes its reference.
func retainedConsolidationNames(b ClaimedBank, windowNames []string, included Phase2Included) []string {
	inWindow := make(map[string]struct{}, len(windowNames))
	for _, name := range windowNames {
		inWindow[name] = struct{}{}
	}
	names := make([]string, 0, len(included.SummaryNames)+len(b.ConsolidatedNames))
	seen := make(map[string]struct{}, len(included.SummaryNames)+len(b.ConsolidatedNames))
	for _, name := range included.SummaryNames {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, name := range b.ConsolidatedNames {
		if _, ok := inWindow[name]; !ok {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

// failBank completes one failed phase 2 attempt: the published memory stays,
// the next attempt is scheduled at now+RetryBackoff, and the attempt is
// counted as phase2_failed (design §10 step 3.5, Requirement 5.5). It is the
// single phase 2 failure path — a failed input read, build, collection, parse
// or publication all land here — and it never recurses: when the FailBank
// write itself fails, the attempt is still counted through the same counter
// exactly once and the bounded log records the classification, so the bank is
// left to its lease expiry. A lease lost while recording the failure is
// counted as phase2_lease_lost and is not a failure.
func (w *Worker) failBank(ctx context.Context, b ClaimedBank, cause error) {
	if err := w.store.FailBank(ctx, b, w.now().Add(w.cfg.RetryBackoff).UnixMilli()); err != nil {
		if errors.Is(err, ErrLeaseLost) {
			w.metrics.Inc(EventPhase2LeaseLost)
			return
		}
		w.log.Warn("proxy-memory phase 2 failure could not be recorded", "event", EventPhase2Failed, "error_kind", workerErrorKind(err))
	}
	w.metrics.Inc(EventPhase2Failed)
	w.log.Warn("proxy-memory phase 2 failed", "event", EventPhase2Failed, "error_kind", workerErrorKind(cause))
}

// failPublish routes a failed PublishBank into the tick's phase 2 failure path
// (design §10 steps 3.2 and 3.5, Requirements 5.5/5.6), covering both the
// empty-bank and the consolidated publication. A lost lease counts as
// phase2_lease_lost and is never compensated — the loss is crash recovery, so
// the winner's result stands — while every other error schedules the bank's
// next attempt through FailBank at now+RetryBackoff. failBank keeps the
// bounded log and the lease-expiry fallback when even that write fails, so
// this never recurses and the attempt is counted exactly once.
func (w *Worker) failPublish(ctx context.Context, b ClaimedBank, err error) {
	if errors.Is(err, ErrLeaseLost) {
		w.metrics.Inc(EventPhase2LeaseLost)
		return
	}
	w.failBank(ctx, b, err)
}

// claimDeadline includes time spent acquiring the lease, and reserves a small
// part of it for result settlement. Translating the remaining duration to a
// local deadline preserves the injected bookkeeping clock used by this worker.
func (w *Worker) claimDeadline(claimedAt time.Time) time.Time {
	reserve := min(time.Second, w.cfg.Lease/10)
	remaining := claimedAt.Add(w.cfg.Lease).Sub(w.now()) - reserve
	return time.Now().Add(min(w.cfg.AuxTimeout, remaining))
}

// errInvalidAuxResult marks a completed auxiliary collection whose result the
// feature rejected (a parse failure or a no-tools validation failure). It is
// distinct from a failed collection, a provider-terminal error and a submit
// refusal: only this classification may become `invalid_result` evidence.
var errInvalidAuxResult = errors.New("proxy-memory: auxiliary result is invalid")

// workerCollection is one completed collection plus the host operation
// receipt the scheduler retained with it. Receipt is zero when the composed
// client has no receipt capability.
type workerCollection struct {
	Text    string
	Receipt CollectionReceipt
}

// collect bounds submission, queue wait and execution by the claim deadline.
// It attributes the job to its owner, awaits the no-tools result and its host
// receipt from one job snapshot, and always forgets the job after a successful
// submission (design §10, §14.3).
func (w *Worker) collect(ctx context.Context, o Owner, req auxiliary.Request, coalesceKey string, deadline time.Time) (workerCollection, error) {
	var out workerCollection
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := bounded.Err(); err != nil {
		return out, err
	}
	submitCtx := scope.WithScope(bounded, ownerScope(o))
	// A shorter parent deadline must bound both Await and detached execution.
	deadline, _ = submitCtx.Deadline()
	id, err := w.aux.SubmitCollect(submitCtx, req, auxiliary.SubmitOptions{
		CoalesceKey:    coalesceKey,
		Timeout:        w.cfg.AuxTimeout,
		Deadline:       deadline,
		MaxOutputBytes: w.cfg.MaxOutputBytes,
	})
	if err != nil {
		return out, fmt.Errorf("submit auxiliary collection: %w", err)
	}
	defer w.aux.Forget(id)
	var collected aipapi.Collected
	if receiptClient, ok := w.aux.(ReceiptCollector); ok {
		collected, out.Receipt, err = receiptClient.AwaitCollection(submitCtx, id)
	} else {
		collected, err = w.aux.Await(submitCtx, id)
	}
	if err != nil {
		return out, fmt.Errorf("await auxiliary collection: %w", err)
	}
	if calls := collected.OrderedToolCalls(); len(calls) > 0 {
		return out, fmt.Errorf("%w: auxiliary collection returned tool calls", errInvalidAuxResult)
	}
	if collected.TerminalError != nil {
		return out, errors.New("auxiliary collection reported a terminal error")
	}
	out.Text = collected.Text.String()
	return out, nil
}

// ownerScope builds the internal principal scope of one worker auxiliary
// request (design §10 step 2.2, Requirements 4.7/5.8): the owner principal as
// a service subject, the owner's tenant when it has one, and OriginInternal.
// An internal origin is never memory-eligible, so a worker request can never
// recurse into its own capture or injection.
func ownerScope(o Owner) scope.PrincipalScopeView {
	tenant := scope.Unknown()
	if o.TenantID != "" {
		tenant = scope.Known(o.TenantID)
	}
	return scope.PrincipalScopeView{
		SubjectKind: scope.SubjectService,
		PrincipalID: scope.Known(o.PrincipalID),
		TenantID:    tenant,
		Origin:      scope.OriginInternal,
	}
}

// workerErrorKind classifies a failure into a bounded log value. The raw error
// never reaches a log record: store and auxiliary errors can embed content.
func workerErrorKind(err error) string {
	switch {
	case errors.Is(err, errPhase2NoteBudget):
		return "note_input_budget"
	case errors.Is(err, errPhase2ExtractionBudget):
		return "extraction_input_budget"
	// A cancelled or expired compilation keeps its identity through the
	// refusal, so it stays a cancellation and not a target defect.
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrTargetCompilerUnavailable):
		return "target_unavailable"
	case errors.Is(err, ErrTargetCompilationFailed):
		return "target_invalid"
	case errors.Is(err, ErrLeaseLost):
		return "lease_lost"
	case errors.Is(err, ErrFailureConflict):
		return "conflict"
	case errors.Is(err, ErrFailureInvalid):
		return "invalid_evidence"
	default:
		return "error"
	}
}

// newWorkerLeaseOwner generates one generation's claim lease owner: 16 random
// bytes in lowercase hex (design §10). A generation that cannot generate one
// must not claim anything, so the error fails Start.
func newWorkerLeaseOwner() (string, error) {
	var buf [workerLeaseOwnerBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("%s: worker lease owner: %w", ID, err)
	}
	return hex.EncodeToString(buf[:]), nil
}
