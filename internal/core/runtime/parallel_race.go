package runtime

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

const cancelLosersTimeout = 5 * time.Second

func (e *Executor) logParallelRacePanic(ctx context.Context, pe *safety.PanicError, msg string, o diag.AttrOpts) {
	if e != nil && e.Log != nil && pe != nil {
		attrs := diag.IsolatedCrashAttrs(ctx, pe, diag.CrashAttrOpts{AttrOpts: o})
		e.Log.LogAttrs(ctx, slog.LevelError, msg, diag.AppendIsolatedCrashStack(attrs, pe)...)
	}
}

type parallelLeg struct {
	billingCallState *billingCallState
	storeID          string
	requestID        string
	boundaryScope    scope.PrincipalScopeView
	billingCallID    usageevidence.BillingCallID
	boundary         *coremetering.BoundaryAccumulator
	cand             routing.AttemptCandidate
	bleg             b2bua.BLegRecord
	stream           aipapi.ManagedEventStream
	authority        authorityLifecycle
	delay            time.Duration
	startedAt        time.Time
	recvErr          error
	observedUsage    atomic.Value // aipapi.Event
	interleaved      interleavedstate.State
	turn             InterleavedTurn
	tx               *attemptTx
	ready            *readyAttempt
	managedByMain    bool
	mainDone         bool
}

func (e *Executor) releaseLosers(ctx context.Context, aScope *leglifecycle.ALeg, legs []*parallelLeg) error {
	var cleanupErr error
	for _, leg := range legs {
		usage, _ := leg.observedUsage.Load().(aipapi.Event)
		_outcome := aipapi.AttemptCancelled
		_reason := "parallel race loser"
		_detailErr := error(context.Canceled)
		if leg.recvErr != nil && !errors.Is(leg.recvErr, context.Canceled) && !errors.Is(leg.recvErr, context.DeadlineExceeded) {
			_outcome = aipapi.AttemptSwallowedFailure
			_reason = attemptReasonDetail(leg.recvErr)
			_detailErr = leg.recvErr
		}

		evidence := attemptEvidence{
			Command:       sdkterminal.CommandParallelLoser,
			ReleaseKind:   usagelimit.ReleaseKindLosing,
			LegOutcome:    usageevidence.LegOutcomeFailed,
			Usage:         usage,
			Err:           _detailErr,
			RecordOutcome: _outcome,
			RecordReason:  _reason,
			CancelCause: &aipapi.CancelCause{
				Kind:   aipapi.CancelRaceLoser,
				Detail: "parallel race loser",
			},
		}

		if leg.ready != nil {
			leg.ready.DisposeWithEvidence(ctx, IntentParallelLoser, evidence)
		} else if leg.tx != nil {
			tx := leg.tx
			if !tx.completed {
				evidence.TraceID = tx.reqFacts.traceID
				evidence.ALegID = tx.reqFacts.aLegID
				evidence.StartedAt = tx.openStartedAt
				res := tx.rollback(ctx, sdkterminal.CommandParallelLoser, evidence)
				if res.Result.Err != nil && !errors.Is(res.Result.Err, context.Canceled) {
					cleanupErr = errors.Join(cleanupErr, res.Result.Err)
				}
			}
		} else if leg.stream != nil || leg.authority.control != nil || leg.boundary != nil {
			sess := e.createSessionForParallelLeg(leg, aScope)
			res := sess.TerminalizeAttempt(ctx, IntentParallelLoser, evidence)
			if res.Result.Err != nil && !errors.Is(res.Result.Err, context.Canceled) {
				cleanupErr = errors.Join(cleanupErr, res.Result.Err)
			}
		}
	}
	return cleanupErr
}

type outcomeHandoff struct {
	outcome parallelArmOutcome
	ack     chan bool
}

type parallelDecisionSnapshot struct {
	outcomes []parallelArmOutcome
	fatalErr error
	canceled bool
}

func (e *Executor) reportParallelArmOutcome(
	parentCtx context.Context,
	raceCtx context.Context,
	outcomeCh chan<- outcomeHandoff,
	outcome parallelArmOutcome,
	tx *attemptTx,
	ready *readyAttempt,
) {
	ackCh := make(chan bool, 1)
	outcomeCh <- outcomeHandoff{outcome: outcome, ack: ackCh}
	accepted := <-ackCh
	if accepted {
		return
	}

	if ready == nil && (tx == nil || tx.completed) {
		return
	}

	cleanupCtx, cleanupCancel := detachedCleanupContext(parentCtx, cancelLosersTimeout)
	defer cleanupCancel()

	usage := outcome.observed
	_outcome := aipapi.AttemptCancelled
	_reason := "parallel race loser"
	_detailErr := error(context.Canceled)
	if outcome.failErr != nil && !errors.Is(outcome.failErr, context.Canceled) && !errors.Is(outcome.failErr, context.DeadlineExceeded) {
		_outcome = aipapi.AttemptSwallowedFailure
		_reason = attemptReasonDetail(outcome.failErr)
		_detailErr = outcome.failErr
	}
	evidence := attemptEvidence{
		Command:       sdkterminal.CommandParallelLoser,
		ReleaseKind:   usagelimit.ReleaseKindLosing,
		LegOutcome:    usageevidence.LegOutcomeFailed,
		Usage:         usage,
		Err:           _detailErr,
		RecordOutcome: _outcome,
		RecordReason:  _reason,
		CancelCause: &aipapi.CancelCause{
			Kind:   aipapi.CancelRaceLoser,
			Detail: "parallel race loser",
		},
	}

	if ready != nil {
		ready.DisposeWithEvidence(cleanupCtx, IntentParallelLoser, evidence)
	} else if tx != nil && !tx.completed {
		evidence.TraceID = tx.reqFacts.traceID
		evidence.ALegID = tx.reqFacts.aLegID
		evidence.StartedAt = tx.openStartedAt
		tx.rollback(cleanupCtx, sdkterminal.CommandParallelLoser, evidence)
	}
}

func (e *Executor) tryOpenParallelGroup(
	ctx context.Context,
	req openNextRequest,
	candidates []routing.AttemptCandidate,
	nextCycle *interleavedstate.CycleState,
	stickyBackendID string,
	stickyBinding bool,
) (openedAttempt, error) {
	var zero openedAttempt
	interleaved := req.interleaved
	cycleAdvance := nextCycle
	var maxHandicap time.Duration
	for _, c := range candidates {
		maxHandicap = max(maxHandicap, c.Handicap)
	}
	entries := make([]legEntry, len(candidates))
	for i, c := range candidates {
		entries[i] = legEntry{cand: c, delay: maxHandicap - c.Handicap}
	}
	slices.SortStableFunc(entries, func(a, b legEntry) int { return cmp.Compare(a.delay, b.delay) })
	if req.progress.budget != nil {
		var limited []legEntry
		for _, e := range entries {
			if !req.progress.budget.tryAcquire() {
				break
			}
			limited = append(limited, e)
		}
		if len(limited) == 0 {
			return zero, fmt.Errorf("executor: %w", aipapi.ErrMaxRouteAttempts)
		}
		entries = limited
	}
	if cycleAdvance != nil {
		interleaved.Cycle = *cycleAdvance
		req.interleaved = interleaved
	}

	raceCtx, raceCancel := context.WithCancel(ctx)
	defer raceCancel()
	fastForwardCh := make(chan struct{})
	var fastForwardOnce sync.Once
	broadcastFastForward := func() {
		fastForwardOnce.Do(func() { close(fastForwardCh) })
	}

	// Immutable facts snapshotted before the loop to achieve worker isolation
	frozenReqFacts := req.reqFacts
	frozenRouteFacts := req.routeFacts
	frozenInterleaved := req.interleaved

	var frozenTTFT *ttftBudget
	if req.progress.ttft != nil {
		req.progress.ttft.mu.Lock()
		frozenTTFT = &ttftBudget{
			start:         req.progress.ttft.start,
			global:        req.progress.ttft.global,
			done:          req.progress.ttft.done,
			leafDeadlines: maps.Clone(req.progress.ttft.leafDeadlines),
		}
		req.progress.ttft.mu.Unlock()
	}

	outcomeCh := make(chan outcomeHandoff, len(entries))
	var wg sync.WaitGroup

	for _, entry := range entries {
		wg.Add(1)
		go func(entry legEntry) {
			defer wg.Done()

			// The whole leg runs on its own goroutine, which the request-level
			// recovery middleware cannot observe: an unrecovered panic here
			// terminates the process instead of failing one attempt. Isolate
			// the entire leg, not only the backend open, because candidate
			// evaluation already invokes backend capability resolvers before
			// the attempt transaction exists. A leg that panics before
			// startAttemptTx owns nothing to roll back; one that panics after
			// it is terminalized exactly like an open failure.
			var tx *attemptTx
			defer func() {
				if r := recover(); r != nil {
					pe := safety.Capture(safety.BoundaryBackend, "parallel_race_leg", r)
					e.logParallelRacePanic(ctx, pe, "executor: isolated panic in parallel race leg", diag.AttrOpts{CallID: frozenReqFacts.traceID})
					if tx != nil && !tx.completed {
						rollbackAttemptTxOpenFailure(ctx, tx, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted)
					}
				}
			}()

			armCtx, armCancel := context.WithCancel(raceCtx)
			var ttftDeadline ttftContextDeadline
			if frozenTTFT != nil {
				workerTTFT := &ttftBudget{
					start:         frozenTTFT.start,
					global:        frozenTTFT.global,
					done:          frozenTTFT.done,
					leafDeadlines: maps.Clone(frozenTTFT.leafDeadlines),
				}
				armCtx, armCancel, ttftDeadline = workerTTFT.scopedContext(raceCtx, e.now(), entry.cand.Key, entry.cand.Primary.TTFTTimeout)
			}
			defer armCancel()

			localHist := candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
			if entry.delay > 0 {
				timer := time.NewTimer(entry.delay)
				select {
				case <-timer.C:
				case <-armCtx.Done():
					timer.Stop()
					e.reportParallelArmOutcome(ctx, raceCtx, outcomeCh, parallelArmOutcome{
						cand:    entry.cand,
						failErr: armCtx.Err(),
						delta:   parallelFailureDeltaFromHistory(localHist),
					}, nil, nil)
					return
				case <-fastForwardCh:
					timer.Stop()
				}
			}

			if err := armCtx.Err(); err != nil {
				e.reportParallelArmOutcome(ctx, raceCtx, outcomeCh, parallelArmOutcome{
					cand:    entry.cand,
					failErr: err,
					delta:   parallelFailureDeltaFromHistory(localHist),
				}, nil, nil)
				return
			}

			workerReqFacts := frozenReqFacts
			workerReqFacts.suppressThinker, workerReqFacts.suppressVisibleMemo = true, true

			plan := candidatePlan{
				cand:            entry.cand,
				stickyBackendID: stickyBackendID,
				stickyBinding:   stickyBinding,
				parallel:        true,
				parentCtx:       ctx,
			}

			evalOutcome, err := e.evaluateCandidate(armCtx, workerReqFacts, frozenRouteFacts, plan, frozenInterleaved)
			if err != nil {
				e.reportParallelArmOutcome(ctx, raceCtx, outcomeCh, parallelArmOutcome{
					cand:    entry.cand,
					failErr: err,
					delta:   parallelFailureDeltaFromHistory(localHist),
				}, nil, nil)
				return
			}
			if !evalOutcome.accepted {
				if evalOutcome.rejection.kind == rejectAdmission {
					if stickyBinding && entry.cand.Primary.Backend == stickyBackendID {
						localHist.AffinityReset = "admission_reject"
					}
				}
				e.reportParallelArmOutcome(ctx, raceCtx, outcomeCh, parallelArmOutcome{
					cand:      entry.cand,
					rejected:  true,
					rejection: cloneCandidateRejection(evalOutcome.rejection),
					delta:     parallelFailureDeltaFromHistory(localHist),
				}, nil, nil)
				return
			}

			tx, err = e.startAttemptTx(armCtx, workerReqFacts, frozenRouteFacts, entry.cand, nil, &localHist)
			if err != nil {
				e.reportParallelArmOutcome(ctx, raceCtx, outcomeCh, parallelArmOutcome{
					cand:    entry.cand,
					failErr: err,
					delta:   parallelFailureDeltaFromHistory(localHist),
				}, nil, nil)
				return
			}

			err = e.openAttemptTx(armCtx, tx, evalOutcome, plan)
			if tx.completed {
				e.reportParallelArmOutcome(ctx, raceCtx, outcomeCh, parallelArmOutcome{
					cand:      entry.cand,
					completed: true,
					bleg:      tx.bleg,
					delta:     parallelFailureDeltaFromHistory(localHist),
				}, nil, nil)
				return
			}
			if err != nil {
				rollbackAttemptTxOpenFailure(ctx, tx, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted)
				e.reportParallelArmOutcome(ctx, raceCtx, outcomeCh, parallelArmOutcome{
					cand:    entry.cand,
					failErr: err,
					bleg:    tx.bleg,
					delta:   parallelFailureDeltaFromHistory(localHist),
				}, nil, nil)
				return
			}
			ready := tx.HandoffReady(pendingSelectionEffects{
				interleaved: frozenInterleaved,
			})
			ready.setDefaultEvidence(usagelimit.ReleaseKindLosing, sdkterminal.CommandParallelLoser, usageevidence.LegOutcomeFailed)
			if err := tx.commitLaunchOrRegister(armCtx, ready, workerReqFacts.aScope); err != nil {
				e.reportParallelArmOutcome(ctx, raceCtx, outcomeCh, parallelArmOutcome{
					cand:    entry.cand,
					failErr: err,
					bleg:    tx.bleg,
					delta:   parallelFailureDeltaFromHistory(localHist),
				}, nil, nil)
				return
			}

			armLeg := &parallelLeg{
				billingCallState: frozenReqFacts.billingCallState,
				storeID:          frozenReqFacts.billingStoreID,
				requestID:        attemptRequestID(frozenReqFacts),
				boundaryScope:    frozenReqFacts.recvViews.Scope,
				billingCallID:    frozenReqFacts.billingCallID,
				boundary:         tx.boundary,
				cand:             entry.cand,
				bleg:             tx.bleg,
				stream:           tx.stream,
				authority:        tx.authLifecycle,
				delay:            entry.delay,
				interleaved:      frozenInterleaved,
				turn:             evalOutcome.turn,
				tx:               tx,
				ready:            ready,
				startedAt:        e.now(),
			}

			if err := armCtx.Err(); err != nil {
				observed := emptyOperatorUsageShell()
				armLeg.observedUsage.Store(observed)
				e.reportParallelArmOutcome(ctx, raceCtx, outcomeCh, parallelArmOutcome{
					cand:     entry.cand,
					failErr:  err,
					bleg:     tx.bleg,
					observed: observed,
					armLeg:   armLeg,
					delta:    parallelFailureDeltaFromHistory(localHist),
				}, tx, ready)
				return
			}

			var preBuf []aipapi.Event
			for {
				ev, err := tx.stream.Recv(armCtx)
				if err != nil {
					observed := operatorUsageOrShell(preBuf)
					armLeg.observedUsage.Store(observed)
					var localErr error
					if ttftDeadline.expired(armCtx, err) {
						if ttftDeadline.scope == ttftTimeoutGlobal {
							localErr = aipapi.ErrTTFTTimeout
						} else {
							localErr = ttftFailure(ttftDeadline.scope, entry.cand.Key)
						}
					} else {
						localErr = err
					}

					e.reportParallelArmOutcome(ctx, raceCtx, outcomeCh, parallelArmOutcome{
						cand:     entry.cand,
						failErr:  localErr,
						bleg:     tx.bleg,
						observed: observed,
						armLeg:   armLeg,
						delta:    parallelFailureDeltaFromHistory(localHist),
					}, tx, ready)
					return
				}
				preBuf = append(preBuf, ev)
				if isWinningEvent(ev) {
					observed := operatorUsageOrShell(preBuf)
					armLeg.observedUsage.Store(observed)

					e.reportParallelArmOutcome(ctx, raceCtx, outcomeCh, parallelArmOutcome{
						cand:  entry.cand,
						ready: ready,
						pending: pendingSelectionEffects{
							interleaved: frozenInterleaved,
						},
						bleg:      tx.bleg,
						observed:  observed,
						winnerBuf: preBuf,
						armLeg:    armLeg,
						turn:      evalOutcome.turn,
						delta:     parallelFailureDeltaFromHistory(localHist),
					}, tx, ready)
					return
				}
			}
		}(entry)
	}

	allDoneCh := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				e.logParallelRacePanic(ctx, safety.Capture(safety.BoundaryWorker, "parallel_race_waiter", r), "executor: isolated panic in parallel race waiter", diag.AttrOpts{CallID: req.reqFacts.traceID})
			}
		}()
		wg.Wait()
		close(allDoneCh)
	}()

	decisionCh := make(chan parallelDecisionSnapshot, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				e.logParallelRacePanic(ctx, safety.Capture(safety.BoundaryWorker, "parallel_race_coordinator", r), "executor: isolated panic in parallel race coordinator", diag.AttrOpts{CallID: req.reqFacts.traceID})
			}
		}()

		var (
			arrivalSeq       uint64
			accepting        = true
			published        = false
			acceptedOutcomes []parallelArmOutcome
			fatalErr         error
		)

		publishDecision := func(canceled bool) {
			if published {
				return
			}
			published = true
			accepting = false
			raceCancel()

			snapshotOutcomes := make([]parallelArmOutcome, len(acceptedOutcomes))
			copy(snapshotOutcomes, acceptedOutcomes)

			select {
			case decisionCh <- parallelDecisionSnapshot{
				outcomes: snapshotOutcomes,
				fatalErr: fatalErr,
				canceled: canceled,
			}:
			default:
			}
		}

		for {
			select {
			case handoff := <-outcomeCh:
				arrivalSeq++
				o := handoff.outcome
				o.arrival = arrivalSeq

				if accepting {
					if o.rejected || o.failErr != nil || o.completed {
						if o.cand.Handicap > 0 {
							broadcastFastForward()
						}
					}
					if isRoundAbortingFatalErr(o.failErr) {
						fatalErr = errors.Join(fatalErr, o.failErr)
						if handoff.ack != nil {
							handoff.ack <- true
						}
						acceptedOutcomes = append(acceptedOutcomes, o)
						publishDecision(false)
						continue
					}
					if isParallelFatalErr(o.failErr) {
						fatalErr = errors.Join(fatalErr, o.failErr)
					}

					if o.ready != nil && ctx.Err() == nil {
						// Atomically accept winner, append to outcomes, and publish decision snapshot
						if handoff.ack != nil {
							handoff.ack <- true
						}
						acceptedOutcomes = append(acceptedOutcomes, o)
						publishDecision(false)
						continue
					}

					// Pre-winner non-fatal failure / rejection / completed: accept ownership so reducer owns cleanup/deltas
					if handoff.ack != nil {
						handoff.ack <- true
					}
					acceptedOutcomes = append(acceptedOutcomes, o)

				} else {
					// Post-decision or post-cancellation outcome: reject ownership so worker self-cleans
					if handoff.ack != nil {
						handoff.ack <- false
					}
				}

			case <-ctx.Done():
				// Parent context canceled: publish abandoned snapshot immediately
				publishDecision(true)

			case <-allDoneCh:
				// All workers finished. Drain any remaining handoffs in outcomeCh.
				for {
					select {
					case handoff := <-outcomeCh:
						arrivalSeq++
						o := handoff.outcome
						o.arrival = arrivalSeq
						if accepting {
							if isParallelFatalErr(o.failErr) {
								fatalErr = errors.Join(fatalErr, o.failErr)
							}
							if o.ready != nil && ctx.Err() == nil {
								if handoff.ack != nil {
									handoff.ack <- true
								}
								acceptedOutcomes = append(acceptedOutcomes, o)
								publishDecision(false)
								continue
							}
							if handoff.ack != nil {
								handoff.ack <- true
							}
							acceptedOutcomes = append(acceptedOutcomes, o)
						} else {
							if handoff.ack != nil {
								handoff.ack <- false
							}
						}
					default:
						if !published {
							publishDecision(ctx.Err() != nil)
						}
						return
					}
				}
			}
		}
	}()

	var snapshot parallelDecisionSnapshot
	select {
	case snapshot = <-decisionCh:
	case <-ctx.Done():
		raceCancel()
		snapshot = <-decisionCh
	}

	reducer := newParallelRoundReducer(e, req, entries, candidates, cycleAdvance)
	reducer.losersDone = allDoneCh
	return reducer.Reduce(ctx, snapshot.outcomes)
}

func (e *Executor) reduceParallelOutcomes(
	ctx context.Context,
	req openNextRequest,
	entries []legEntry,
	candidates []routing.AttemptCandidate,
	cycleAdvance *interleavedstate.CycleState,
	collectedOutcomes []parallelArmOutcome,
) (openedAttempt, error) {
	reducer := newParallelRoundReducer(e, req, entries, candidates, cycleAdvance)
	return reducer.Reduce(ctx, collectedOutcomes)
}

type legEntry struct {
	cand  routing.AttemptCandidate
	delay time.Duration
}

// parallelRoundReducer owns serial shared progress mutation, winner selection,
// stable failure merging, and loser terminalization for a parallel execution round.
type parallelRoundReducer struct {
	e            *Executor
	req          openNextRequest
	entries      []legEntry
	candidates   []routing.AttemptCandidate
	cycleAdvance *interleavedstate.CycleState
	losersDone   <-chan struct{}
}

func newParallelRoundReducer(
	e *Executor,
	req openNextRequest,
	entries []legEntry,
	candidates []routing.AttemptCandidate,
	cycleAdvance *interleavedstate.CycleState,
) *parallelRoundReducer {
	return &parallelRoundReducer{
		e:            e,
		req:          req,
		entries:      entries,
		candidates:   candidates,
		cycleAdvance: cycleAdvance,
	}
}

func (r *parallelRoundReducer) Reduce(
	ctx context.Context,
	collectedOutcomes []parallelArmOutcome,
) (openedAttempt, error) {
	var zero openedAttempt

	cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
	defer cleanupCancel()

	var (
		winnerOut *parallelArmOutcome
		winnerBuf []aipapi.Event
		fatalErr  error
	)

	// 1. Winner Selection: Deterministic for same controlled arrival schedule.
	// Earliest arrival sequence number among ready outcomes wins.
	for i := range collectedOutcomes {
		o := &collectedOutcomes[i]
		if isParallelFatalErr(o.failErr) {
			fatalErr = errors.Join(fatalErr, o.failErr)
		}
		if o.ready != nil && o.failErr == nil {
			if winnerOut == nil || o.arrival < winnerOut.arrival {
				winnerOut = o
			}
		}
	}

	if winnerOut != nil {
		winnerBuf = winnerOut.winnerBuf
	}

	// Terminalize all loser / late ready arm attempts exactly once.
	for i := range collectedOutcomes {
		o := &collectedOutcomes[i]
		if o.ready != nil && o != winnerOut {
			o.ready.Dispose(cleanupCtx, errors.New("parallel race loser"))
		}
	}

	// 2. Build legs in stable entries order.
	legs := make([]parallelLeg, len(r.entries))
	for i, entry := range r.entries {
		legs[i] = parallelLeg{
			billingCallState: r.req.reqFacts.billingCallState,
			storeID:          r.req.reqFacts.billingStoreID,
			requestID:        attemptRequestID(r.req.reqFacts),
			boundaryScope:    r.req.reqFacts.recvViews.Scope,
			billingCallID:    r.req.reqFacts.billingCallID,
			cand:             entry.cand,
			delay:            entry.delay,
		}
		for _, o := range collectedOutcomes {
			if o.cand.Key == entry.cand.Key {
				if o.armLeg != nil {
					legs[i].stream = o.armLeg.stream
					legs[i].bleg = o.armLeg.bleg
					legs[i].authority = o.armLeg.authority
					legs[i].requestID = o.armLeg.requestID
					legs[i].boundaryScope = o.armLeg.boundaryScope
					legs[i].billingCallID = o.armLeg.billingCallID
					legs[i].boundary = o.armLeg.boundary
					legs[i].tx = o.armLeg.tx
					legs[i].ready = o.armLeg.ready
					legs[i].interleaved = o.armLeg.interleaved
					legs[i].startedAt = o.armLeg.startedAt
				}
				legs[i].recvErr = o.failErr
				if o.observed.Kind != "" {
					legs[i].observedUsage.Store(o.observed)
				}
				break
			}
		}
	}

	// 3. Handle Context Cancellation.
	if ctx.Err() != nil {
		if winnerOut != nil && winnerOut.ready != nil {
			winnerOut.ready.Dispose(cleanupCtx, ctx.Err())
		}
		r.releaseOpenedLegs(cleanupCtx, legs)
		return zero, ctx.Err()
	}

	// 4. Handle Fatal Errors (only when no ready winner exists).
	if fatalErr != nil && winnerOut == nil {
		r.releaseOpenedLegs(cleanupCtx, legs)
		return zero, fmt.Errorf("executor: parallel race aborted: %w", fatalErr)
	}

	// 5. Merge Exclusions and Candidate Failures in Stable Entries Order.
	if r.req.progress.excluded == nil {
		r.req.progress.excluded = make(map[string]struct{})
	}
	sh := r.req.progress.getFailures()

	for _, entry := range r.entries {
		var o *parallelArmOutcome
		for j := range collectedOutcomes {
			if collectedOutcomes[j].cand.Key == entry.cand.Key {
				o = &collectedOutcomes[j]
				break
			}
		}
		if o == nil {
			continue
		}

		if o.rejected {
			r.e.applyRejection(sh, o.cand, o.rejection)
			r.req.progress.excluded[o.cand.Key] = struct{}{}
		} else if o.completed {
			r.req.progress.excluded[o.cand.Key] = struct{}{}
		}

		if sh != nil {
			o.delta.applyToShared(sh)
		}
	}

	// 6. Winner Outcome: Commit effects, terminalize losers, install bridge stream, return openedAttempt.
	if winnerOut != nil && winnerOut.ready != nil && winnerOut.failErr == nil {
		winnerIdx := -1
		for i := range legs {
			if legs[i].cand.Key == winnerOut.cand.Key {
				winnerIdx = i
				break
			}
		}

		pending := winnerOut.ready.Pending()
		committedInterleaved := pending.interleaved
		if winnerIdx >= 0 {
			legs[winnerIdx].interleaved = committedInterleaved
		}
		committedInterleaved, err := r.e.advanceInterleavedCycle(ctx, r.req.reqFacts.aLegID, committedInterleaved, r.cycleAdvance)
		if err != nil {
			return zero, r.e.cleanUpParallelFailure(ctx, r.req, legs, winnerIdx, winnerOut, r.losersDone, err)
		}

		if winnerOut.cand.MarkedFirst {
			if err := r.e.Store.SetWeightedFirstConsumed(ctx, r.req.reqFacts.aLegID, true); err != nil {
				return zero, r.e.cleanUpParallelFailure(ctx, r.req, legs, winnerIdx, winnerOut, r.losersDone, err)
			}
		}

		var losers []*parallelLeg
		for i := range legs {
			if i == winnerIdx {
				continue
			}
			if (legs[i].stream != nil || legs[i].boundary != nil || legs[i].authority.control != nil) && (legs[i].ready != nil || legs[i].tx == nil || !legs[i].tx.completed) {
				legs[i].managedByMain = true
				losers = append(losers, &legs[i])
			}
		}

		var losersDone <-chan error
		if r.losersDone != nil || len(losers) > 0 {
			done := make(chan error, 1)
			losersDone = done
			go func() {
				var cleanupErr error
				if len(losers) > 0 {
					func() {
						defer func() {
							if rcv := recover(); rcv != nil {
								pe := safety.Capture(safety.BoundaryBackend, "parallel_cancel_losers", rcv)
								r.e.logParallelRacePanic(ctx, pe, "executor: isolated panic in parallel race loser cleanup", diag.AttrOpts{CallID: r.req.reqFacts.traceID})
								cleanupErr = errors.Join(cleanupErr, fmt.Errorf("parallel race loser cleanup panic"))
							}
						}()
						cancelCtx, cancel := detachedCleanupContext(ctx, cancelLosersTimeout)
						defer cancel()
						cleanupErr = r.e.releaseLosers(cancelCtx, r.req.reqFacts.aScope, losers)
					}()
				}
				if r.losersDone != nil {
					<-r.losersDone
				}
				done <- cleanupErr
				close(done)
			}()
		}

		winnerLeg := winnerOut.armLeg
		if winnerLeg == nil && winnerIdx >= 0 {
			winnerLeg = &legs[winnerIdx]
		}
		if winnerLeg == nil {
			winnerLeg = &parallelLeg{
				storeID:     r.req.reqFacts.billingStoreID,
				cand:        winnerOut.cand,
				bleg:        winnerOut.bleg,
				interleaved: committedInterleaved,
			}
		} else {
			winnerLeg.interleaved = committedInterleaved
		}

		winnerTurn := winnerOut.turn
		if winnerTurn == nil && winnerLeg != nil {
			winnerTurn = winnerLeg.turn
		}
		if err := r.e.commitInterleavedExecutorTurn(ctx, r.req.reqFacts.aLegID, winnerOut.cand.InterleavedRole, winnerTurn); err != nil {
			return zero, r.e.cleanUpParallelFailure(ctx, r.req, legs, winnerIdx, winnerOut, r.losersDone, err)
		}

		if err := winnerOut.ready.InstallBridgeStream(&parallelBridgeStream{
			winner:           winnerLeg,
			buf:              winnerBuf,
			losersDone:       losersDone,
			loserCleanupWait: cancelLosersTimeout,
		}); err != nil {
			return zero, r.e.cleanUpParallelFailure(ctx, r.req, legs, winnerIdx, winnerOut, r.losersDone, err)
		}
		return openedAttempt{
			ready:       winnerOut.ready,
			interleaved: committedInterleaved,
			turn:        winnerTurn,
		}, nil
	}

	// 7. No Winner (All Arms Failed): Terminalize failed legs, record ParallelFailure in stable order.
	var parallelFailure error
	var failedLegs []*parallelLeg
	for i := range legs {
		if legs[i].stream == nil {
			if legs[i].boundary != nil || legs[i].authority.control != nil {
				failedLegs = append(failedLegs, &legs[i])
			}
			parallelFailure = errors.Join(parallelFailure, fmt.Errorf("candidate %q did not open a stream", legs[i].cand.Key))
			continue
		}
		failure := legs[i].recvErr
		if failure == nil {
			failure = errors.New("parallel leg ended before winner")
		}
		parallelFailure = errors.Join(parallelFailure, fmt.Errorf("candidate %q failed before winner: %w", legs[i].cand.Key, failure))
		if legs[i].ready != nil || legs[i].tx == nil || !legs[i].tx.completed {
			failedLegs = append(failedLegs, &legs[i])
		}
	}
	if len(failedLegs) > 0 {
		if cerr := r.e.releaseLosers(cleanupCtx, r.req.reqFacts.aScope, failedLegs); cerr != nil {
			parallelFailure = errors.Join(parallelFailure, cerr)
		}
	}

	// Apply affinity reset in stable candidate order only on all-failure
	for _, entry := range r.entries {
		for j := range collectedOutcomes {
			if collectedOutcomes[j].cand.Key == entry.cand.Key {
				if collectedOutcomes[j].delta.affinityReset != "" {
					r.e.clearAffinityBinding(ctx, r.req.reqFacts.traceID, r.req.routeFacts.affinityKey, r.req.routeFacts.affinitySet, collectedOutcomes[j].delta.affinityReset)
				}
				break
			}
		}
	}

	if skipped := len(r.candidates) - len(legs); skipped > 0 {
		parallelFailure = errors.Join(parallelFailure, fmt.Errorf("parallel race skipped %d leg(s) due max-attempt budget", skipped))
	}
	if parallelFailure == nil {
		parallelFailure = errors.New("parallel race failed without winner")
	}
	failures := r.req.progress.getFailures()
	failures.ParallelFailure = fmt.Errorf("executor: parallel race arm failed: %w", parallelFailure)
	if r.req.progress.excluded == nil {
		r.req.progress.excluded = map[string]struct{}{}
	}
	for _, c := range r.candidates {
		r.req.progress.excluded[c.Key] = struct{}{}
	}
	return openedAttempt{interleaved: r.req.interleaved}, nil
}

func isRoundAbortingFatalErr(err error) bool {
	return errors.Is(err, leglifecycle.ErrALegCanceled)
}

func isParallelFatalErr(err error) bool {
	return errors.Is(err, aipapi.ErrMaxRouteAttempts) ||
		errors.Is(err, aipapi.ErrTTFTTimeout) ||
		errors.Is(err, leglifecycle.ErrALegCanceled)
}

func isWinningEvent(ev aipapi.Event) bool {
	return (ev.Kind == aipapi.EventTextDelta || ev.Kind == aipapi.EventReasoningDelta) && strings.TrimSpace(ev.Delta) != ""
}

func detachedCleanupContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	} else {
		parent = context.WithoutCancel(parent)
	}
	if timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, timeout)
}

func (r *parallelRoundReducer) releaseOpenedLegs(ctx context.Context, legs []parallelLeg) {
	var opened []*parallelLeg
	for i := range legs {
		if (legs[i].stream != nil || legs[i].boundary != nil || legs[i].authority.control != nil) && (legs[i].ready != nil || legs[i].tx == nil || !legs[i].tx.completed) {
			legs[i].managedByMain = true
			opened = append(opened, &legs[i])
		}
	}
	if len(opened) > 0 {
		_ = r.e.releaseLosers(ctx, r.req.reqFacts.aScope, opened)
	}
}

func rollbackAttemptTxOpenFailure(ctx context.Context, tx *attemptTx, rel usagelimit.ReleaseKind, outcome usageevidence.LegOutcome) {
	if tx == nil || tx.completed {
		return
	}
	tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, rel, outcome, nil, "")
}

func (e *Executor) cleanUpParallelFailure(ctx context.Context, req openNextRequest, legs []parallelLeg, winner int, wo *parallelArmOutcome, losersDone <-chan struct{}, err error) error {
	cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
	defer cleanupCancel()
	if wo != nil && wo.ready != nil {
		wo.ready.Dispose(cleanupCtx, err)
	}
	var toClean []*parallelLeg
	for i := range legs {
		if i != winner && (legs[i].stream != nil || legs[i].boundary != nil || legs[i].authority.control != nil) && (legs[i].ready != nil || legs[i].tx == nil || !legs[i].tx.completed) {
			toClean = append(toClean, &legs[i])
		}
	}
	cleanupErr := e.releaseLosers(cleanupCtx, req.reqFacts.aScope, toClean)
	if losersDone != nil {
		select {
		case <-losersDone:
		case <-cleanupCtx.Done():
			cleanupErr = errors.Join(cleanupErr, cleanupCtx.Err())
		}
	}
	return errors.Join(err, cleanupErr)
}

type parallelBridgeStream struct {
	winner           *parallelLeg
	buf              []aipapi.Event
	bufIdx           int
	finished         atomic.Bool
	closed           atomic.Bool
	losersDone       <-chan error
	loserCleanupWait time.Duration
}

func (s *parallelBridgeStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.finished.Load() {
		return aipapi.Event{}, io.EOF
	}
	idx := s.bufIdx
	if idx < len(s.buf) {
		ev := s.buf[idx]
		s.bufIdx++
		if ev.Kind == aipapi.EventResponseFinished {
			s.finished.Store(true)
		}
		return ev, nil
	}
	if s.winner.stream == nil {
		s.finished.Store(true)
		return aipapi.Event{}, io.EOF
	}
	ev, err := s.winner.stream.Recv(ctx)
	if err != nil {
		s.finished.Store(true)
		return aipapi.Event{}, err
	}
	if ev.Kind == aipapi.EventResponseFinished {
		s.finished.Store(true)
	}
	return ev, nil
}

func (s *parallelBridgeStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	if s.winner != nil && s.winner.stream != nil {
		s.finished.Store(true)
		return s.winner.stream.Cancel(ctx, cause)
	}
	return aipapi.CancelResult{}
}

func (s *parallelBridgeStream) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.finished.Store(true)
	var closeErr error
	if s.losersDone != nil {
		wait := cmp.Or(s.loserCleanupWait, cancelLosersTimeout)
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case err, ok := <-s.losersDone:
			if ok && err != nil {
				closeErr = errors.Join(closeErr, err)
			}
		case <-timer.C:
			closeErr = errors.Join(closeErr, fmt.Errorf("parallel race loser cleanup timed out after %s", wait))
		}
	}
	if s.winner != nil && s.winner.stream != nil {
		if err := s.winner.stream.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	return closeErr
}

// Wire evidence delegation (Requirement 4.3): the parallel bridge replaces
// the winner inner after a successful (already bound) open, so the existing
// live/checkpoint/terminal consumers drain through it. Without forwarding,
// the embedded winner stream's provider-owned evidence capabilities stay
// hidden and the winner attempt terminalizes without its bound native
// evidence while unbridged losers keep theirs. The bridge forwards the five
// evidence methods to the winner stream without taking queue, checkpoint,
// or drain ownership: no storage, no terminalization, no Close/Cancel/Recv
// change. The helpers are shared with the wire acquisition adapters, so
// bridge and chain semantics cannot drift.
func (s *parallelBridgeStream) winnerEvidenceStream() aipapi.ManagedEventStream {
	if s == nil || s.winner == nil {
		return nil
	}
	return s.winner.stream
}

func (s *parallelBridgeStream) BindEconomicEvidence(identity coremetering.ObservationIdentity) {
	if s == nil {
		return
	}
	delegateWireBindEconomicEvidence(s.winnerEvidenceStream(), identity)
}

func (s *parallelBridgeStream) DrainUsageEvidence() []aipapi.Event {
	if s == nil {
		return nil
	}
	return delegateWireDrainUsageEvidence(s.winnerEvidenceStream())
}

func (s *parallelBridgeStream) DrainEconomicObservations() []metering.Observation {
	if s == nil {
		return nil
	}
	return delegateWireDrainEconomicObservations(s.winnerEvidenceStream())
}

func (s *parallelBridgeStream) DrainEconomicEvidenceRecords() []execbackend.EconomicEvidence {
	if s == nil {
		return nil
	}
	return delegateWireDrainEconomicEvidenceRecords(s.winnerEvidenceStream())
}

func (s *parallelBridgeStream) AccountingEvidenceEnabled() bool {
	if s == nil {
		return false
	}
	return delegateWireAccountingEvidenceEnabled(s.winnerEvidenceStream())
}

var (
	_ coremetering.ProviderEvidenceBinder = (*parallelBridgeStream)(nil)
	_ aipapi.UsageEvidenceSource          = (*parallelBridgeStream)(nil)
	_ metering.ObservationSource          = (*parallelBridgeStream)(nil)
	_ execbackend.EconomicEvidenceSource  = (*parallelBridgeStream)(nil)
)

type parallelArmOutcome struct {
	cand      routing.AttemptCandidate
	ready     *readyAttempt
	failErr   error
	pending   pendingSelectionEffects
	rejected  bool
	rejection candidateRejection
	completed bool
	delta     parallelFailureDelta
	arrival   uint64
	observed  aipapi.Event
	bleg      b2bua.BLegRecord
	armLeg    *parallelLeg
	winnerBuf []aipapi.Event
	turn      InterleavedTurn
}

type parallelFailureDelta struct {
	transformCount   int
	unrepresentable  int
	nonTransform     bool
	capabilityReject aipapi.NegotiationResult
	transportReject  aipapi.TransportNegotiationResult
	admissionErr     error
	contextLimit     bool
	parallelFailure  error
	affinityReset    string
	// swallowedUpstreamFailure carries the typed upstream failure a worker
	// observed (a backend that opened no stream) across the worker/reducer
	// boundary, so the shared history keeps the backend identity and phase
	// instead of only the joined message.
	swallowedUpstreamFailure error
}

func cloneCandidateRejection(r candidateRejection) candidateRejection {
	res := candidateRejection{kind: r.kind}
	if admit, ok := r.detail.(aipapi.CandidateAdmissionResult); ok {
		cp := admit
		if len(admit.Capability.Missing) > 0 {
			cp.Capability.Missing = slices.Clone(admit.Capability.Missing)
		}
		res.detail = cp
	} else {
		res.detail = r.detail
	}
	return res
}

func parallelFailureDeltaFromHistory(h candidateFailureHistory) parallelFailureDelta {
	d := parallelFailureDelta{
		capabilityReject:         h.CapabilityReject,
		transportReject:          h.TransportReject,
		admissionErr:             h.AdmissionErr,
		contextLimit:             h.ContextLimit,
		parallelFailure:          h.ParallelFailure,
		affinityReset:            h.AffinityReset,
		swallowedUpstreamFailure: h.SwallowedUpstreamFailure,
	}
	if h.TransformExcludes != nil {
		h.TransformExcludes.mu.Lock()
		d.transformCount = h.TransformExcludes.count
		d.unrepresentable = h.TransformExcludes.unrepresentable
		d.nonTransform = h.TransformExcludes.nonTransform
		h.TransformExcludes.mu.Unlock()
	}
	if len(d.capabilityReject.Missing) > 0 {
		d.capabilityReject.Missing = slices.Clone(d.capabilityReject.Missing)
	}
	return d
}

func (d parallelFailureDelta) applyToShared(target *candidateFailureHistory) {
	if target == nil {
		return
	}
	if target.TransformExcludes == nil {
		target.TransformExcludes = &transformExcludeTracker{}
	}
	target.TransformExcludes.mu.Lock()
	target.TransformExcludes.count += d.transformCount
	target.TransformExcludes.unrepresentable += d.unrepresentable
	if d.nonTransform {
		target.TransformExcludes.nonTransform = true
	}
	target.TransformExcludes.mu.Unlock()

	if d.capabilityReject.Kind != "" {
		cp := d.capabilityReject
		if len(cp.Missing) > 0 {
			cp.Missing = slices.Clone(cp.Missing)
		}
		target.CapabilityReject = cp
	}
	if d.transportReject.Kind != "" {
		target.TransportReject = d.transportReject
	}
	if d.admissionErr != nil {
		target.AdmissionErr = d.admissionErr
	}
	if d.contextLimit {
		target.ContextLimit = true
	}
	if d.parallelFailure != nil {
		target.ParallelFailure = d.parallelFailure
	}
	if d.swallowedUpstreamFailure != nil {
		target.SwallowedUpstreamFailure = d.swallowedUpstreamFailure
	}
	if d.affinityReset != "" {
		target.AffinityReset = d.affinityReset
	}
}
