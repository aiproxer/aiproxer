package runtimehost

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// Coordinator serializes explicit reload attempts (design Reload Coordinator; req 1.4, 3.x, 11.x, 13.x).
// It orchestrates the gate, runner, state owner, and observer without
// directly implementing detailed source/load/classification/compile branches
// (req 6.4); source is kept only for FixedSourcePath (AbsolutePath), never
// for reload execution.
type Coordinator struct {
	source   StableConfigSource
	mgr      *Manager
	timeout  time.Duration
	observer *ReloadObserver
	gate     *attemptGate
	runner   *attemptRunner
	state    *ReloadState

	attempts atomic.Int64
}

func closeUnusedSourceOwner(slot *configsource.SourceOwnerSlot) {
	if slot != nil {
		_ = slot.Close(context.Background())
	}
}

// NewCoordinator constructs a production serialized reload coordinator.
func NewCoordinator(deps CoordinatorDeps) (*Coordinator, error) {
	if deps.Source == nil {
		return nil, fmt.Errorf("runtimehost: nil StableConfigSource")
	}
	if deps.Loader == nil {
		return nil, fmt.Errorf("runtimehost: nil EffectiveLoader")
	}
	if deps.Compile == nil {
		return nil, fmt.Errorf("runtimehost: nil CandidateCompiler")
	}
	if deps.Manager == nil {
		return nil, fmt.Errorf("runtimehost: nil Manager")
	}
	if deps.ActiveSourceOwner != nil && !deps.ActiveSourceOwner.ValidFor(deps.ActiveSource) {
		return nil, fmt.Errorf("runtimehost: mismatched active source owner")
	}
	timeout := deps.Timeout
	if timeout <= 0 {
		timeout = DefaultReloadTimeout
	}
	gate := newAttemptGate()
	c := &Coordinator{
		source:   deps.Source,
		mgr:      deps.Manager,
		timeout:  timeout,
		observer: deps.Observer,
		gate:     gate,
	}
	initial := reloadStateInitial{
		ActiveEffective: deps.ActiveEffective,
		ActiveSource:    deps.ActiveSource,
	}
	if active := deps.Manager.Active(); active != nil {
		initial.InitialResult = sdkreload.Result{
			Category:         sdkreload.ResultPublished,
			ActiveGeneration: active.ID(),
		}
		if meta := active.Status().Meta; meta.PublicFingerprint != "" {
			initial.ModelGeneration = meta.PublicFingerprint
		}
	}
	stateOwner := configsource.NewSourceOwnerSlot(nil)
	if deps.ActiveSourceOwner != nil {
		deps.ActiveSourceOwner.MoveTo(stateOwner)
	}
	initial.ActiveSourceOwner = stateOwner
	if deps.Observer != nil {
		stateOwner.SetCleanupObserver(func(error) {
			deps.Observer.ObserveLifecycle(context.Background(), "cleanup", string(LifecycleOutcomeCleanupFailed), 0)
		})
	}
	c.state = newReloadState(initial)
	c.runner = newAttemptRunner(attemptRunnerDeps{
		Source:   deps.Source,
		Loader:   deps.Loader,
		Classify: deps.Classify,
		Compile:  deps.Compile,
		Manager:  deps.Manager,
		Observer: deps.Observer,
		Gate:     gate,
		State:    c.state,
	})
	gate.SetShutdownFinalizer(c.state.requestCloseActiveSource)
	gate.SetShutdownWaiter(c.state.waitForSourceClose)
	return c, nil
}

// BeginShutdown rejects new attempts, cancels the host-owned reload context
// (including coalesced follow-ups), signals manager shutdown, and prevents late
// publication (req 1.9, 11.9, 13.7). It does not wait for rollback; callers that
// must close process services afterward should use [Coordinator.WaitForIdle].
func (c *Coordinator) BeginShutdown() {
	if c == nil {
		return
	}
	if c.gate != nil {
		c.gate.BeginShutdown()
	}
	if c.mgr != nil {
		c.mgr.BeginShutdown()
	}
}

// WaitForIdle blocks until no reload attempt (including coalesced follow-up) is
// in flight, or until ctx is done. It is safe after [Coordinator.BeginShutdown]
// and does not take request-path locks (req 13.7, 15.1).
func (c *Coordinator) WaitForIdle(ctx context.Context) error {
	if c == nil || c.gate == nil {
		return nil
	}
	return c.gate.WaitForIdle(ctx)
}

// Status returns a bounded safe snapshot (req 13.1-13.2, 14.1, 14.8).
// The snapshot is secret-safe and must not include filesystem paths.
// Management adapters that need the fixed startup source should call
// FixedSourcePath and map it into transport DTOs only
// (canonical Status stays path-free).
func (c *Coordinator) Status() sdkreload.Status {
	if c == nil {
		return sdkreload.Status{}
	}
	// Gate snapshot first so gate and state locks are never held together.
	var gateSnap attemptGateSnapshot
	if c.gate != nil {
		gateSnap = c.gate.Snapshot()
	}
	var activeID int64
	retained := 0
	pressure := false
	if c.mgr != nil {
		if g := c.mgr.Active(); g != nil {
			activeID = g.ID()
		}
		snap := c.mgr.ObservabilitySnapshot()
		retained = snap.Retired
		pressure = snap.RetentionWouldBlock
	}
	return c.state.Snapshot(reloadStatusInput{
		ActiveGeneration:    activeID,
		Busy:                gateSnap.Busy,
		PendingSignal:       gateSnap.PendingSignal,
		CoalescedSignals:    gateSnap.CoalescedSignals,
		RetainedGenerations: retained,
		RetentionPressure:   pressure,
	})
}

// Reload runs one serialized attempt. API callers receive Busy when an attempt is
// active; SIGHUP coalesces into at most one pending follow-up (req 11.4-11.6).
// The attempt uses a host-owned timeout independent of API client cancel (req 12.9).
func (c *Coordinator) Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result {
	if c == nil {
		return sdkreload.Result{Category: sdkreload.ResultInternalFailed, ReasonCategory: "nil-coordinator"}
	}
	if c.gate == nil {
		return sdkreload.Result{Category: sdkreload.ResultInternalFailed, ReasonCategory: "nil-attempt-gate"}
	}
	if c.shuttingDown() {
		return c.terminal(sdkreload.Result{
			Category:       sdkreload.ResultCanceled,
			ReasonCategory: configreload.StageShutdown,
		})
	}

	admission := c.gate.TryStart(ctx, trigger)
	switch admission.Kind {
	case admissionRejectedShutdown:
		return c.terminal(sdkreload.Result{
			Category:       sdkreload.ResultCanceled,
			ReasonCategory: configreload.StageShutdown,
		})
	case admissionBusyAPI:
		return sdkreload.Result{
			Category:         sdkreload.ResultBusy,
			ActiveGeneration: c.activeGenerationID(),
			ReasonCategory:   configreload.StageBusy,
		}
	case admissionPendingHUP, admissionCoalescedHUP:
		return sdkreload.Result{
			Category:         sdkreload.ResultBusy,
			ActiveGeneration: c.activeGenerationID(),
			ReasonCategory:   configreload.StageCoalesce,
			CoalescedSignals: admission.CoalescedSignals,
		}
	case admissionAdmitted:
		// proceed
	default:
		return c.terminal(sdkreload.Result{
			Category:       sdkreload.ResultInternalFailed,
			ReasonCategory: "unknown-admission",
		})
	}

	lease := admission.Lease
	if lease == nil {
		return c.terminal(sdkreload.Result{
			Category:       sdkreload.ResultInternalFailed,
			ReasonCategory: "nil-lease",
		})
	}
	// Gate-owned cleanup: abandon the current lease variable on any exit path
	// (including panic outside runner.Run). After Complete advances to a
	// follow-up, lease is reassigned so this defer targets the active token.
	// After a normal final Complete, Abandon is an idempotent no-op.
	defer func() { lease.Abandon() }()

	hostCtx, cancel := context.WithTimeout(lease.Context(), c.timeout)
	start := time.Now()
	outcome := c.runner.Run(hostCtx, c.state.ActiveInput(trigger, c.attempts.Add(1), c.activeGenerationID()))
	cancel()
	first := c.state.Apply(outcome, reloadTerminalMeta{
		Trigger:    trigger,
		Duration:   time.Since(start),
		RecordedAt: time.Now().UTC(),
	})
	closeUnusedSourceOwner(outcome.SourceOwnerSlot)
	c.refreshGauges()

	for {
		// Manager-only shutdown must fold into the gate so Complete clears
		// pending follow-up and rejects further progression.
		if c.mgr != nil && c.mgr.ShuttingDown() {
			c.gate.BeginShutdown()
		}
		fin := lease.Complete()
		switch fin.Kind {
		case finishReleasedIdle, finishAlreadyCompleted:
			return first
		case finishFollowUpClaimed:
			if fin.FollowUpLease == nil {
				return first
			}
			lease = fin.FollowUpLease
			followCtx, followCancel := context.WithTimeout(lease.Context(), c.timeout)
			followTrigger := sdkreload.Trigger{
				Kind:       sdkreload.TriggerSIGHUP,
				AcceptedAt: time.Now().UTC(),
				SafeActor:  "coalesced-sighup",
			}
			followStart := time.Now()
			followOutcome := c.runner.Run(followCtx, c.state.ActiveInput(followTrigger, c.attempts.Add(1), c.activeGenerationID()))
			followCancel()
			// CoalescedSignals must be set on the result before Apply so the
			// terminal state and its history entry agree (req 6.8).
			followOutcome.Result.CoalescedSignals = fin.CoalescedSignals
			c.state.Apply(followOutcome, reloadTerminalMeta{
				Trigger:    followTrigger,
				Duration:   time.Since(followStart),
				RecordedAt: time.Now().UTC(),
			})
			closeUnusedSourceOwner(followOutcome.SourceOwnerSlot)
			c.refreshGauges()
			// loop: another SIGHUP may have reserved pending during follow-up
		default:
			return first
		}
	}
}

// refreshGauges invokes observer/Manager side effects strictly after the
// state lock has been released by Apply (req 6.4).
func (c *Coordinator) refreshGauges() {
	if c.observer != nil {
		c.observer.RefreshGauges(c.mgr)
	}
}

func (c *Coordinator) terminal(res sdkreload.Result) sdkreload.Result {
	if res.ActiveGeneration == 0 {
		res.ActiveGeneration = c.activeGenerationID()
	}
	return res
}

// shuttingDown reports gate-owned shutdown truth plus Manager shutdown where
// currently required for late-publication checks.
func (c *Coordinator) shuttingDown() bool {
	if c == nil {
		return true
	}
	if c.gate != nil && c.gate.shuttingDown() {
		return true
	}
	return c.mgr != nil && c.mgr.ShuttingDown()
}

// activeGenerationID reports the currently published generation ID, or 0 if
// none is published. Manager owns its own synchronization; Coordinator holds
// no lock of its own.
func (c *Coordinator) activeGenerationID() int64 {
	if c.mgr == nil {
		return 0
	}
	if g := c.mgr.Active(); g != nil {
		return g.ID()
	}
	return 0
}
