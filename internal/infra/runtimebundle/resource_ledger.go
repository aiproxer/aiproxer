package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/internal/core/configreload"
)

// ClosePhase classifies when a generation-owned cleanup runs (design Resource Ledger).
type ClosePhase uint8

const (
	PhasePrepare  ClosePhase = iota + 1 // rollback after fallible prepare work
	PhaseActivate                       // rollback after commit-safe activate work
	PhaseQuiesce                        // stop admission-independent workers after retirement
	PhaseClose                          // release clients/backends/transports after drain
	PhasePublish                        // start admission-independent workers after host publication
)

type ledgerLifeState uint8

const (
	ledgerLifeOpen ledgerLifeState = iota
	ledgerLifeQuiesced
	ledgerLifeClosed
	ledgerLifeRolledBack
)

// ResourceLedger tracks candidate/generation-owned resources for reverse-order
// rollback, quiesce, and close. Sole generation-resource phase owner; never owns ProcessServices.
type ResourceLedger struct {
	mu                                                                     sync.Mutex
	cond                                                                   *sync.Cond
	entries                                                                []*ledgerEntry
	state                                                                  ledgerLifeState
	preparing, activating, publishing, quiescing, rollingBack, closing     bool
	prepareDone, activateDone, publishDone, quiesceDone                    bool
	prepareErr, activateErr, publishErr, quiesceErr, rollbackErr, closeErr error
	sealed                                                                 bool // late acquisitions cleaned immediately outside mu
	prepared                                                               atomic.Bool
}

type ledgerEntry struct {
	name                                 string
	phase                                ClosePhase
	start, stop                          func(context.Context) error
	mu                                   sync.Mutex
	cond                                 *sync.Cond
	cleaning, cleanedOK, terminalClaimed bool
	cleanErr                             error
	startAttempted, started              atomic.Bool
}

func NewResourceLedger() *ResourceLedger {
	l := &ResourceLedger{}
	l.cond = sync.NewCond(&l.mu)
	return l
}

func (l *ResourceLedger) ensureCond() {
	if l.cond == nil {
		l.cond = sync.NewCond(&l.mu)
	}
}

func waitWhile(cond *sync.Cond, active *bool) {
	for *active {
		cond.Wait()
	}
}

func (l *ResourceLedger) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

func (l *ResourceLedger) Add(name string, phase ClosePhase, closeFn func(context.Context) error) {
	if l == nil || closeFn == nil {
		return
	}
	l.AddAction(name, phase, nil, closeFn)
}

// AddClose registers a no-arg closer; cleanup runs only via Rollback/Quiesce/Close.
func (l *ResourceLedger) AddClose(name string, phase ClosePhase, closeFn func() error) {
	if l == nil || closeFn == nil {
		return
	}
	l.addEntry(name, phase, nil, func(context.Context) error { return closeFn() })
}

func (l *ResourceLedger) AddAction(name string, phase ClosePhase, start, stop func(context.Context) error) {
	if l == nil || (start == nil && stop == nil) {
		return
	}
	l.addEntry(name, phase, start, stop)
}

func (l *ResourceLedger) addEntry(name string, phase ClosePhase, start, stop func(context.Context) error) *ledgerEntry {
	e := &ledgerEntry{name: name, phase: phase, start: start, stop: stop}
	e.cond = sync.NewCond(&e.mu)
	l.mu.Lock()
	l.ensureCond()
	immediate := l.sealed || ((l.quiesceDone || l.quiescing || l.state == ledgerLifeQuiesced ||
		l.state == ledgerLifeClosed || l.state == ledgerLifeRolledBack) && phase == PhaseQuiesce)
	if !immediate {
		l.entries = append(l.entries, e)
		l.mu.Unlock()
		return e
	}
	l.mu.Unlock()
	if stop != nil && start == nil { // accept-or-immediately-close for close-only races
		_ = e.claimAndStop(context.Background(), true)
	}
	return e
}

// claimAndStop runs stop at most once on success. terminal=true permanently claims
// the entry even on failure; terminal=false leaves failed entries retryable on Close.
func (e *ledgerEntry) claimAndStop(ctx context.Context, terminal bool) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.cond == nil {
		e.cond = sync.NewCond(&e.mu)
	}
	for e.cleaning {
		e.cond.Wait()
	}
	if e.cleanedOK {
		e.mu.Unlock()
		return nil
	}
	if e.terminalClaimed {
		err := e.cleanErr
		e.mu.Unlock()
		return err
	}
	e.cleaning = true
	e.mu.Unlock()
	err := safeLedgerStop(ctx, e)
	e.mu.Lock()
	e.cleaning = false
	if err == nil {
		e.cleanedOK, e.cleanErr = true, nil
	} else if terminal {
		e.terminalClaimed, e.cleanErr = true, err
	} else {
		e.cleanErr = err
	}
	e.cond.Broadcast()
	e.mu.Unlock()
	return err
}

func (l *ResourceLedger) copyEntries() []*ledgerEntry {
	l.mu.Lock()
	entries := append([]*ledgerEntry(nil), l.entries...)
	l.mu.Unlock()
	return entries
}

func (l *ResourceLedger) waitPhaseLocked(waitQuiescing, waitClosing bool) {
	for {
		busy := l.preparing || l.activating || l.publishing || l.rollingBack
		if waitQuiescing {
			busy = busy || l.quiescing
		}
		if waitClosing {
			busy = busy || l.closing
		}
		if !busy {
			return
		}
		l.cond.Wait()
	}
}

func (l *ResourceLedger) lifeErr(st ledgerLifeState) error {
	switch st {
	case ledgerLifeClosed:
		return l.closeErr
	case ledgerLifeRolledBack:
		return l.rollbackErr
	case ledgerLifeQuiesced:
		return l.quiesceErr
	default:
		return nil
	}
}

func (l *ResourceLedger) cachedLifeErrLocked(states ...ledgerLifeState) (bool, error) {
	for _, st := range states {
		if l.state == st {
			return true, l.lifeErr(st)
		}
	}
	return false, nil
}

func (l *ResourceLedger) resolveInFlightErr(base error) error {
	if l.state == ledgerLifeClosed || l.state == ledgerLifeRolledBack {
		return l.lifeErr(l.state)
	}
	return base
}

func (l *ResourceLedger) execStartPhase(ctx context.Context, phase ClosePhase, done *bool, phaseErr *error, busy *bool, markPrepared bool) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	l.ensureCond()
	l.waitPhaseLocked(true, true)
	if *done {
		err := *phaseErr
		l.mu.Unlock()
		return err
	}
	if blocked, err := l.startBlockedLocked(); blocked {
		l.mu.Unlock()
		return err
	}
	*busy = true
	l.mu.Unlock()
	// An unexpected unwind never produced a normal result, so the phase flag must
	// be released before it propagates. A stranded flag would block waitPhaseLocked
	// for every later lifecycle call and hide the panic's outcome from callers.
	completed := false
	defer func() {
		if completed {
			return
		}
		l.mu.Lock()
		*busy = false
		l.cond.Broadcast()
		l.mu.Unlock()
	}()
	resultErr := l.runStartsGuarded(ctx, phase)
	l.mu.Lock()
	*done, *phaseErr = true, resultErr
	if resultErr == nil && markPrepared {
		l.prepared.Store(true)
	}
	*busy = false
	l.cond.Broadcast()
	l.mu.Unlock()
	completed = true
	return resultErr
}

// runStartsGuarded converts an unexpected panic in this phase machinery into the
// existing bounded publish-start error. Publish is the only post-commit phase:
// its generation is already the active request plane, so an unexpected unwind
// must not escape and must not retry already attempted starts. Precommit phases
// keep propagating the original value, because fabricating success or a cached
// result would hide a rollback-capable failure.
func (l *ResourceLedger) runStartsGuarded(ctx context.Context, phase ClosePhase) (resultErr error) {
	if phase == PhasePublish {
		defer func() {
			if recover() != nil {
				resultErr = errors.New("runtimebundle: publish start failed")
			}
		}()
	}
	return l.runStarts(ctx, phase)
}

// Prepare runs PhasePrepare start hooks in acquisition order (req). Failure does not auto-rollback.
func (l *ResourceLedger) Prepare(ctx context.Context) error {
	return l.execStartPhase(ctx, PhasePrepare, &l.prepareDone, &l.prepareErr, &l.preparing, true)
}

// Activate runs PhaseActivate start hooks; commit-safe and bounded (req).
func (l *ResourceLedger) Activate(ctx context.Context) error {
	return l.execStartPhase(ctx, PhaseActivate, &l.activateDone, &l.activateErr, &l.activating, false)
}

// Publish runs PhasePublish start hooks after the generation is the active
// published request plane. CompileCandidate/CompileGeneration must not call it.
func (l *ResourceLedger) Publish(ctx context.Context) error {
	return l.execStartPhase(ctx, PhasePublish, &l.publishDone, &l.publishErr, &l.publishing, false)
}

func (l *ResourceLedger) startBlockedLocked() (bool, error) {
	switch l.state {
	case ledgerLifeClosed:
		return true, l.closeErr
	case ledgerLifeRolledBack:
		return true, l.rollbackErr
	case ledgerLifeQuiesced:
		return true, l.quiesceErr
	}
	if l.quiesceDone {
		return true, l.quiesceErr
	}
	if l.sealed {
		if l.closeErr != nil {
			return true, l.closeErr
		}
		return true, l.rollbackErr
	}
	return false, nil
}

func (l *ResourceLedger) runStarts(ctx context.Context, phase ClosePhase) error {
	ctx = ctxOrBackground(ctx)
	for _, e := range l.copyEntries() {
		if e.phase != phase || e.start == nil {
			continue
		}
		e.startAttempted.Store(true)
		if err := safeLedgerStart(ctx, e); err != nil {
			return safeLedgerStartError{cause: err}
		}
		e.started.Store(true)
	}
	return nil
}

type safeLedgerStartError struct{ cause error }

func (safeLedgerStartError) Error() string   { return "runtimebundle: ledger start failed" }
func (e safeLedgerStartError) Unwrap() error { return e.cause }

func safeLedgerStart(ctx context.Context, e *ledgerEntry) (err error) {
	if e == nil || e.start == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = errors.New("runtimebundle: lifecycle start failed")
		}
	}()
	return e.start(ctx)
}

// beginStopPhase locks, waits, and returns cached/in-flight/done outcomes. Caller holds l.mu on proceed.
func (l *ResourceLedger) beginStopPhase(waitQ, waitC bool, terminal []ledgerLifeState, inFlight *bool, baseErr *error, done func() (bool, error)) (proceed bool, err error) {
	if l == nil {
		return false, nil
	}
	l.mu.Lock()
	l.ensureCond()
	l.waitPhaseLocked(waitQ, waitC)
	if ok, e := l.cachedLifeErrLocked(terminal...); ok {
		l.mu.Unlock()
		return false, e
	}
	if inFlight != nil && *inFlight {
		waitWhile(l.cond, inFlight)
		err = l.resolveInFlightErr(*baseErr)
		l.mu.Unlock()
		return false, err
	}
	if done != nil {
		if ok, e := done(); ok {
			l.mu.Unlock()
			return false, e
		}
	}
	return true, nil
}

// Rollback closes all entries in reverse order; caches terminal result (req 3.4).
func (l *ResourceLedger) Rollback(ctx context.Context) error {
	proceed, err := l.beginStopPhase(true, true, []ledgerLifeState{ledgerLifeRolledBack, ledgerLifeClosed}, nil, nil, nil)
	if !proceed {
		return err
	}
	l.rollingBack, l.sealed = true, true
	l.mu.Unlock()
	err = l.stopReverse(ctx, nil, true)
	l.mu.Lock()
	l.rollbackErr, l.state, l.quiesceDone = err, ledgerLifeRolledBack, true
	l.rollingBack = false
	l.cond.Broadcast()
	l.mu.Unlock()
	return err
}

// Quiesce runs PhaseQuiesce cleanups at most once in reverse order (req 10.5).
func (l *ResourceLedger) Quiesce(ctx context.Context) error {
	proceed, err := l.beginStopPhase(false, true, []ledgerLifeState{ledgerLifeClosed, ledgerLifeRolledBack}, &l.quiescing, &l.quiesceErr,
		func() (bool, error) {
			if l.quiesceDone || l.state == ledgerLifeQuiesced {
				return true, l.quiesceErr
			}
			return false, nil
		})
	if !proceed {
		return err
	}
	l.quiescing = true
	l.mu.Unlock()
	err = l.stopReverse(ctx, func(e *ledgerEntry) bool { return e.phase == PhaseQuiesce }, true)
	l.mu.Lock()
	l.quiesceErr, l.quiesceDone = err, true
	if l.state == ledgerLifeOpen {
		l.state = ledgerLifeQuiesced
	}
	l.quiescing = false
	l.cond.Broadcast()
	l.mu.Unlock()
	return err
}

// Close runs generation cleanup; retryable on failure. After Quiesce skips PhaseQuiesce.
func (l *ResourceLedger) Close(ctx context.Context) error {
	proceed, err := l.beginStopPhase(true, false, []ledgerLifeState{ledgerLifeClosed, ledgerLifeRolledBack}, &l.closing, &l.closeErr, nil)
	if !proceed {
		return err
	}
	wasQuiesced := l.state == ledgerLifeQuiesced || l.quiesceDone
	l.closing, l.sealed = true, true
	l.mu.Unlock()
	if wasQuiesced {
		err = l.stopReverse(ctx, func(e *ledgerEntry) bool { return e.phase != PhaseQuiesce }, false)
	} else {
		err = l.stopReverse(ctx, nil, false)
	}
	l.mu.Lock()
	l.closeErr = err
	if err == nil {
		l.state, l.quiesceDone = ledgerLifeClosed, true
	}
	l.closing = false
	l.cond.Broadcast()
	l.mu.Unlock()
	return err
}

func (l *ResourceLedger) stopReverse(ctx context.Context, match func(*ledgerEntry) bool, terminal bool) error {
	ctx = ctxOrBackground(ctx)
	entries := l.copyEntries()
	var out error
	for _, e := range slices.Backward(entries) {
		if match != nil && !match(e) {
			continue
		}
		if e.start != nil && !e.startAttempted.Load() {
			continue
		}
		if err := e.claimAndStop(ctx, terminal); err != nil {
			out = errors.Join(out, fmt.Errorf("runtimebundle: ledger close %q: %w", e.name, err))
		}
	}
	return out
}

func safeLedgerStop(ctx context.Context, e *ledgerEntry) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("runtimebundle: ledger close panic: %s", configreload.SanitizePanicValue(recovered))
		}
	}()
	if e == nil || e.stop == nil {
		return nil
	}
	ctx = ctxOrBackground(ctx)
	return e.stop(ctx)
}
