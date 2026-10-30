package runtimehost

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/configreload"
)

type QuiesceCloser interface {
	OwnedCloser
	Quiesce(ctx context.Context) error
}

type retireAdmission struct {
	mu   sync.Mutex
	held bool
	wait chan struct{}
}

func newRetireAdmission() retireAdmission {
	return retireAdmission{wait: make(chan struct{})}
}

func (a *retireAdmission) acquire(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		a.mu.Lock()
		if !a.held {
			a.held = true
			a.mu.Unlock()
			return nil
		}
		wait := a.wait
		a.mu.Unlock()
		select {
		case <-wait:
			continue
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (a *retireAdmission) release() {
	a.mu.Lock()
	a.held = false
	woken := a.wait
	a.wait = make(chan struct{})
	a.mu.Unlock()
	close(woken)
}

func retireGeneration(ctx context.Context, g *Generation, policy CleanupPolicy, observer *ReloadObserver, waitDrain bool, afterClose func()) (RetirementStatus, error) {
	if g == nil {
		return RetirementStatus{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := g.retireAdmit.acquire(ctx); err != nil {
		return RetirementStatus{GenerationID: g.ID()}, err
	}
	defer g.retireAdmit.release()

	status := RetirementStatus{GenerationID: g.ID()}
	st := g.Lifecycle()
	if st == GenClosed {
		return status, ErrAlreadyClosed
	}

	owned := generationQuiesceCloser(g)
	var out error

	if st == GenRetiring { // the last lease can drain first: Retiring -> Drained
		if err := g.BeginQuiesce(); err != nil && g.Lifecycle() == GenRetiring {
			return status, errors.Join(out, err)
		}
		st = g.Lifecycle()
	}
	switch st {
	case GenQuiescing:
		out = errors.Join(out, runQuiesce(ctx, owned, observer, &status))
		if err := g.MarkQuiesced(); err != nil {
			out = errors.Join(out, err)
		}
	case GenDrained:
		out = errors.Join(out, runQuiesce(ctx, owned, observer, &status))
	case GenQuiesced, GenClosing:
	default:
		return status, ErrIllegalTransition
	}

	if !waitDrain {
		return finishOrArmAsync(g, policy, observer, owned != nil, afterClose, status, out)
	}

	priorArm := g.takePostDrainClose()
	select {
	case <-g.Drained():
	case <-ctx.Done():
		rearm := priorArm
		if rearm == nil && generationQuiesceCloser(g) != nil {
			rearm = func() {
				_, _ = finishRetireClose(context.Background(), g, policy, observer, RetirementStatus{GenerationID: g.ID()}, nil)
				if afterClose != nil {
					afterClose()
				}
			}
		}
		if rearm != nil {
			g.armPostDrainClose(rearm)
		}
		return status, errors.Join(out, ctx.Err())
	}

	stOut, err := finishRetireClose(ctx, g, policy, observer, status, out)
	if afterClose != nil {
		afterClose()
	}
	return stOut, err
}

func finishOrArmAsync(g *Generation, policy CleanupPolicy, observer *ReloadObserver, hasQuiesceCloser bool, afterClose func(), status RetirementStatus, out error) (RetirementStatus, error) {
	if !hasQuiesceCloser {
		return status, out
	}
	if g.Lifecycle() == GenDrained || g.Lifecycle() == GenClosing {
		stOut, err := finishRetireClose(context.Background(), g, policy, observer, status, out)
		if afterClose != nil {
			afterClose()
		}
		return stOut, err
	}
	g.armPostDrainClose(func() {
		_, _ = finishRetireClose(context.Background(), g, policy, observer, RetirementStatus{GenerationID: g.ID()}, nil)
		if afterClose != nil {
			afterClose()
		}
	})
	return status, out
}

func finishRetireClose(ctx context.Context, g *Generation, policy CleanupPolicy, observer *ReloadObserver, status RetirementStatus, out error) (RetirementStatus, error) {
	if g.Lifecycle() == GenDrained {
		if err := g.BeginClose(); err != nil {
			return status, errors.Join(out, err)
		}
	}
	if g.Lifecycle() == GenClosing {
		out = errors.Join(out, runCleanup(ctx, g, policy, observer, &status))
	}
	return status, out
}

func generationQuiesceCloser(g *Generation) QuiesceCloser {
	if plane := g.RequestPlane(); plane != nil {
		return plane
	}
	g.payloadMu.Lock()
	owned := g.owned
	g.payloadMu.Unlock()
	if qc, ok := owned.(QuiesceCloser); ok {
		return qc
	}
	return nil
}

func runQuiesce(ctx context.Context, owned QuiesceCloser, observer *ReloadObserver, status *RetirementStatus) error {
	if owned == nil {
		return nil
	}
	start := time.Now()
	if err := safeQuiesce(ctx, owned); err != nil {
		status.Outcome = LifecycleOutcomeQuiesceFailed
		status.Err = err
		if observer != nil {
			observer.ObserveLifecycle(ctx, "quiesce", string(LifecycleOutcomeQuiesceFailed), time.Since(start))
		}
		return err
	}
	if observer != nil {
		observer.ObserveLifecycle(ctx, "quiesce", "ok", time.Since(start))
	}
	return nil
}

func runCleanup(ctx context.Context, g *Generation, policy CleanupPolicy, observer *ReloadObserver, status *RetirementStatus) error {
	maxAttempts := policy.maxAttempts()
	var closeErr error
	start := time.Now()
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		status.Attempts = attempt
		closeErr = safeClose(g)
		if closeErr == nil || errors.Is(closeErr, ErrAlreadyClosed) {
			closeErr = nil
			break
		}
		if g.Lifecycle() == GenClosed {
			closeErr = nil
			break
		}
	}
	if closeErr != nil {
		status.Outcome = LifecycleOutcomeCleanupFailed
		status.Err = closeErr
		if observer != nil {
			observer.ObserveLifecycle(ctx, "cleanup", string(LifecycleOutcomeCleanupFailed), time.Since(start))
		}
		return closeErr
	}
	if status.Outcome == "" {
		status.Outcome = LifecycleOutcomeOK
		if observer != nil {
			observer.ObserveLifecycle(ctx, "cleanup", "ok", time.Since(start))
		}
	}
	return nil
}

func safeQuiesce(ctx context.Context, owned QuiesceCloser) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError("quiesce", configreload.SanitizePanicValue(recovered))
		}
	}()
	return owned.Quiesce(ctx)
}

func safeClose(g *Generation) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError("cleanup", configreload.SanitizePanicValue(recovered))
		}
	}()
	return g.Close()
}
