// Package leglifecycle owns A-leg scoped cancellation and B-leg teardown policy.
package leglifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var ErrALegCanceled = errors.New("leglifecycle: a-leg canceled")

type CancelKind = aipapi.CancelKind

const (
	CancelExplicit    = aipapi.CancelExplicit
	CancelClientGone  = aipapi.CancelClientGone
	CancelContextDone = aipapi.CancelContextDone
	CancelRaceLoser   = aipapi.CancelRaceLoser
)

type CancelCause = aipapi.CancelCause

type CancelMode = aipapi.CancelMode

const (
	CancelModeNone      = aipapi.CancelModeNone
	CancelModeProvider  = aipapi.CancelModeProvider
	CancelModeTransport = aipapi.CancelModeTransport
	CancelModeCloseOnly = aipapi.CancelModeCloseOnly
)

type CancelResult = aipapi.CancelResult

type BLegAttempt = aipapi.ManagedEventStream

type BLegHandle struct {
	ID      string
	Attempt BLegAttempt
}

type LaunchCommitResult struct {
	Canceled bool
	Cause    CancelCause
}

type launchEntry struct {
	cancel context.CancelFunc
}

type LaunchPermit struct {
	aLeg    *ALeg
	bLegID  string
	entry   *launchEntry
	cancel  context.CancelFunc
	settled atomic.Bool
}

func (p *LaunchPermit) Commit(handle BLegAttempt) (LaunchCommitResult, error) {
	if p == nil || p.aLeg == nil {
		return LaunchCommitResult{}, nil
	}
	if p.settled.Swap(true) {
		return LaunchCommitResult{}, nil
	}
	a := p.aLeg
	a.mu.Lock()
	if cur, ok := a.launches[p.bLegID]; ok && cur == p.entry {
		delete(a.launches, p.bLegID)
	}
	if a.canceled {
		cause := a.cause
		a.mu.Unlock()
		p.cancel()
		return LaunchCommitResult{Canceled: true, Cause: cause}, nil
	}
	if handle != nil {
		if a.blegs == nil {
			a.blegs = make(map[string]BLegAttempt)
		}
		a.blegs[p.bLegID] = handle
	}
	a.mu.Unlock()
	return LaunchCommitResult{}, nil
}

func (p *LaunchPermit) Abort() {
	if p == nil || p.aLeg == nil {
		return
	}
	if p.settled.Swap(true) {
		return
	}
	a := p.aLeg
	a.mu.Lock()
	if cur, ok := a.launches[p.bLegID]; ok && cur == p.entry {
		delete(a.launches, p.bLegID)
	}
	a.mu.Unlock()
	p.cancel()
}

type CloseOnlyAttempt struct {
	Closer interface{ Close() error }
}

func (a CloseOnlyAttempt) Cancel(context.Context, CancelCause) CancelResult {
	return CancelResult{Mode: CancelModeCloseOnly}
}

func (a CloseOnlyAttempt) Close() error {
	if a.Closer == nil {
		return nil
	}
	return a.Closer.Close()
}

type CoordinatorConfig struct {
	CancelTimeout time.Duration
}

// DefaultCancelTimeout bounds B-leg Cancel when CoordinatorConfig.CancelTimeout is unset.
const DefaultCancelTimeout = 2 * time.Second

type Coordinator struct {
	mu    sync.Mutex
	cfg   CoordinatorConfig
	alegs map[string]*ALeg
}

func NewCoordinator(cfg CoordinatorConfig) *Coordinator {
	return &Coordinator{cfg: cfg, alegs: map[string]*ALeg{}}
}

func (c *Coordinator) ensureALegsLocked() {
	if c != nil && c.alegs == nil {
		c.alegs = map[string]*ALeg{}
	}
}

func (c *Coordinator) StartALeg(id string) *ALeg {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureALegsLocked()
	if a := c.alegs[id]; a != nil {
		return a
	}
	a := &ALeg{id: id, coordinator: c, launches: map[string]*launchEntry{}, blegs: map[string]BLegAttempt{}}
	c.alegs[id] = a
	return a
}

func (c *Coordinator) CancelALeg(ctx context.Context, id string, cause CancelCause) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	c.ensureALegsLocked()
	a := c.alegs[id]
	if a == nil {
		a = &ALeg{id: id, coordinator: c, launches: map[string]*launchEntry{}, blegs: map[string]BLegAttempt{}}
		c.alegs[id] = a
	}
	c.mu.Unlock()
	return a.Cancel(ctx, cause)
}

func (c *Coordinator) EndALeg(id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.alegs, id)
	c.mu.Unlock()
}

type ALeg struct {
	id          string
	coordinator *Coordinator

	mu       sync.Mutex
	canceled bool
	cause    CancelCause
	launches map[string]*launchEntry
	blegs    map[string]BLegAttempt
}

func (a *ALeg) BeginBLegLaunch(parent context.Context, bLegID string) (context.Context, *LaunchPermit, error) {
	if a == nil {
		if parent == nil {
			parent = context.Background()
		}
		return parent, nil, nil
	}
	if parent == nil {
		parent = context.Background()
	}
	openCtx, cancel := context.WithCancel(parent)
	a.mu.Lock()
	if a.canceled {
		a.mu.Unlock()
		cancel()
		return nil, nil, ErrALegCanceled
	}
	if a.launches == nil {
		a.launches = make(map[string]*launchEntry)
	}
	if a.blegs == nil {
		a.blegs = make(map[string]BLegAttempt)
	}
	if old, exists := a.launches[bLegID]; exists {
		old.cancel()
	}
	entry := &launchEntry{cancel: cancel}
	a.launches[bLegID] = entry
	a.mu.Unlock()
	return openCtx, &LaunchPermit{aLeg: a, bLegID: bLegID, entry: entry, cancel: cancel}, nil
}

func (a *ALeg) RegisterBLeg(ctx context.Context, h BLegHandle) error {
	if a == nil {
		return nil
	}
	if h.Attempt == nil {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		return nil
	}
	a.mu.Lock()
	if a.canceled {
		cause := a.cause
		a.mu.Unlock()
		cleanupErr := cancelAndClose(ctx, a.cancelTimeout(), h.Attempt, cause)
		if cleanupErr != nil {
			return errors.Join(ErrALegCanceled, cleanupErr)
		}
		return ErrALegCanceled
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			a.mu.Unlock()
			cleanupErr := cancelAndClose(ctx, a.cancelTimeout(), h.Attempt, CancelCause{Kind: CancelContextDone})
			if cleanupErr != nil {
				return errors.Join(err, cleanupErr)
			}
			return err
		}
	}
	if a.blegs == nil {
		a.blegs = make(map[string]BLegAttempt)
	}
	a.blegs[h.ID] = h.Attempt
	a.mu.Unlock()
	return nil
}

func (a *ALeg) Cancel(ctx context.Context, cause CancelCause) error {
	if a == nil {
		return nil
	}
	if cause.Kind == "" {
		cause.Kind = CancelContextDone
	}
	a.mu.Lock()
	if a.canceled {
		a.mu.Unlock()
		return nil
	}
	a.canceled = true
	a.cause = cause
	launches := make([]context.CancelFunc, 0, len(a.launches))
	for _, l := range a.launches {
		if l != nil && l.cancel != nil {
			launches = append(launches, l.cancel)
		}
	}
	a.launches = map[string]*launchEntry{}
	blegs := make([]BLegAttempt, 0, len(a.blegs))
	for _, b := range a.blegs {
		blegs = append(blegs, b)
	}
	a.blegs = map[string]BLegAttempt{}
	a.mu.Unlock()

	for _, cancel := range launches {
		cancel()
	}

	if len(blegs) == 0 {
		return nil
	}
	if len(blegs) == 1 {
		cleanupErr := cancelAndClose(ctx, a.cancelTimeout(), blegs[0], cause)
		if cleanupErr != nil {
			return fmt.Errorf("leglifecycle: cancel and close b-legs: %w", cleanupErr)
		}
		return nil
	}

	timeout := a.cancelTimeout()
	errs := make([]error, len(blegs))
	var wg sync.WaitGroup
	wg.Add(len(blegs))
	for i, b := range blegs {
		go func(i int, b BLegAttempt) {
			defer wg.Done()
			errs[i] = cancelAndClose(ctx, timeout, b, cause)
		}(i, b)
	}
	wg.Wait()

	var cleanupErr error
	for _, err := range errs {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if cleanupErr != nil {
		return fmt.Errorf("leglifecycle: cancel and close b-legs: %w", cleanupErr)
	}
	return nil
}

func (a *ALeg) Err() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.canceled {
		return ErrALegCanceled
	}
	return nil
}

func (a *ALeg) End() {
	if a == nil || a.coordinator == nil {
		return
	}
	a.coordinator.EndALeg(a.id)
}

func (a *ALeg) ReleaseBLeg(id string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	delete(a.blegs, id)
	if l, ok := a.launches[id]; ok {
		delete(a.launches, id)
		if l != nil && l.cancel != nil {
			l.cancel()
		}
	}
	a.mu.Unlock()
}

func (a *ALeg) cancelTimeout() time.Duration {
	if a == nil || a.coordinator == nil {
		return 0
	}
	return a.coordinator.cfg.CancelTimeout
}

func cancelAndClose(parent context.Context, timeout time.Duration, b BLegAttempt, cause CancelCause) error {
	if b == nil {
		return nil
	}
	if cause.Kind == "" {
		cause.Kind = CancelContextDone
	}
	ctx := parent
	if ctx == nil {
		ctx = context.Background()
	}
	effectiveTimeout := timeout
	if effectiveTimeout <= 0 {
		effectiveTimeout = DefaultCancelTimeout
	}
	if deadline, ok := ctx.Deadline(); ok {
		effectiveTimeout = min(effectiveTimeout, max(0, time.Until(deadline)))
	}
	var cleanupErr error
	res, closeErr := BoundedCancelAndClose(ctx, effectiveTimeout,
		func(cancelCtx context.Context) CancelResult { return b.Cancel(cancelCtx, cause) },
		b.Close,
		nil,
	)
	if res.Err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("cancel b-leg: %w", res.Err))
	}
	if closeErr != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("close b-leg: %w", closeErr))
	}
	return cleanupErr
}

// BoundedCancelAndClose requires Close to promptly return and unblock Cancel.
func BoundedCancelAndClose(
	parent context.Context,
	timeout time.Duration,
	cancelFn func(context.Context) CancelResult,
	closeFn func() error,
	force <-chan struct{},
) (CancelResult, error) {
	cancelCtx, cancelCancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancelCancel()
	cancelDone := make(chan CancelResult, 1)
	go func() { cancelDone <- cancelFn(cancelCtx) }()
	select {
	case cancelResult := <-cancelDone:
		return cancelResult, closeFn()
	case <-force:
	case <-cancelCtx.Done():
	}
	closeErr := closeFn()
	cancelResult := <-cancelDone
	if cancelResult.Err == nil {
		cancelResult.Err = context.DeadlineExceeded
	} else if !errors.Is(cancelResult.Err, context.DeadlineExceeded) {
		cancelResult.Err = errors.Join(cancelResult.Err, context.DeadlineExceeded)
	}
	return cancelResult, closeErr
}
