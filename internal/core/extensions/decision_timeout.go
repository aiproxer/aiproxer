package extensions

import (
	"context"
	"errors"
	"sync"
	"time"
)

// TimeoutBudgetSource is a frozen per-request source of evaluation timeouts for
// decision providers (requirement 6.3). The default source returns zero for every
// stage/provider so legacy extension behavior remains source- and behavior-compatible
// unless a configured provider/stage explicitly sets a budget. Implementations must be
// safe for concurrent reads; composition roots must treat a source as frozen for the
// lifetime of a request snapshot.
type TimeoutBudgetSource interface {
	TimeoutFor(stage, providerID string) time.Duration
}

// DefaultTimeoutBudgetSource returns a zero budget for every stage/provider,
// preserving legacy extension behavior (requirement 6.3, 10.5).
type DefaultTimeoutBudgetSource struct{}

// TimeoutFor returns zero for every stage/provider.
func (DefaultTimeoutBudgetSource) TimeoutFor(string, string) time.Duration { return 0 }

var _ TimeoutBudgetSource = DefaultTimeoutBudgetSource{}

// StaticTimeoutBudgetSource returns a fixed budget for every stage/provider. It is
// intended for tests and explicit configuration where one budget applies uniformly.
type StaticTimeoutBudgetSource struct {
	Budget time.Duration
}

// TimeoutFor returns the configured budget for every stage/provider.
func (s StaticTimeoutBudgetSource) TimeoutFor(string, string) time.Duration { return s.Budget }

var _ TimeoutBudgetSource = StaticTimeoutBudgetSource{}

// TimeoutResult carries the outcome of a bounded decision-provider call
// (requirements 6.1, 6.2, 6.3, 6.4). Exactly one of Value/Err is meaningful when
// TimedOut and ParentCanceled are both false.
type TimeoutResult[T any] struct {
	Value          T
	Err            error
	TimedOut       bool
	ParentCanceled bool
	// ProviderStillRunning reports that the helper returned because the parent or
	// evaluation deadline fired while the provider goroutine had not completed.
	ProviderStillRunning bool
	// GuardRejected reports that a ProviderTimeoutGuard rejected the call because
	// another bounded goroutine for this stage/provider is already running.
	GuardRejected bool
}

// ProviderTimeoutGuard bounds in-process providers that ignore context
// cancellation. It permits at most one bounded goroutine per stage/provider in a
// runtime snapshot, so an uncooperative provider cannot accumulate one leaked
// goroutine per request.
type ProviderTimeoutGuard struct {
	mu     sync.Mutex
	active map[string]int
}

// NewProviderTimeoutGuard returns an empty guard for one runtime snapshot.
func NewProviderTimeoutGuard() *ProviderTimeoutGuard {
	return &ProviderTimeoutGuard{active: make(map[string]int)}
}

func providerTimeoutKey(stage, providerID string) string { return stage + "\x00" + providerID }

// TryEnter reserves a bounded call slot for stage/provider. A nil guard permits
// the call. A false result means a bounded goroutine for the same stage/provider
// is already running.
func (g *ProviderTimeoutGuard) TryEnter(stage, providerID string) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active == nil {
		g.active = make(map[string]int)
	}
	key := providerTimeoutKey(stage, providerID)
	if g.active[key] > 0 {
		return false
	}
	g.active[key] = 1
	return true
}

func (g *ProviderTimeoutGuard) markStillRunning(stage, providerID string) bool {
	return g != nil
}

func (g *ProviderTimeoutGuard) finish(stage, providerID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := providerTimeoutKey(stage, providerID)
	if g.active[key] <= 1 {
		delete(g.active, key)
		return
	}
	g.active[key]--
}

// RunDecisionProviderWithDeadline invokes call with a child context that expires at
// the earlier of the parent deadline and the supplied deadline. It is the single
// source of truth for the bounding deadline: the same deadline value is used both to
// bound the provider (via context.WithDeadline) and, threaded explicitly through the
// bounded result by stage runners, to populate
// policydecision.Context.EvaluationDeadline on emitted evidence (requirement 6.3).
//
// deadline must be non-zero; callers derive it once (e.g. now+timeout) and reuse the
// same time.Time for both bounding and evidence projection so the provider's observed
// ctx.Deadline() equals the record's EvaluationDeadline exactly.
//
// When timeout is greater than zero, the helper runs call in a bounded goroutine
// against the derived child context and returns as soon as the call returns, the
// evaluation deadline expires, or the parent context is canceled. If the parent
// context is canceled or expires first, ParentCanceled is true and the original
// context error is returned in Err; no policy-denial evidence is implied. If the
// evaluation deadline expires while the parent is still active, TimedOut is true and
// Err is nil; the caller records a policy failure or fail-open skipped record
// according to the provider's configured failure behavior. Late provider results
// after timeout are ignored and never mutate live call or stream state.
func RunDecisionProviderWithDeadline[T any](ctx context.Context, deadline time.Time, call func(context.Context) (T, error)) TimeoutResult[T] {
	return RunDecisionProviderWithDeadlineGuarded[T](ctx, deadline, nil, "", "", call)
}

// RunDecisionProviderWithDeadlineGuarded behaves like
// [RunDecisionProviderWithDeadline] and also tracks provider goroutines that are
// still running after the helper returns. While a tracked goroutine remains active,
// the guard rejects future launches for the same stage/provider.
func RunDecisionProviderWithDeadlineGuarded[T any](ctx context.Context, deadline time.Time, guard *ProviderTimeoutGuard, stage, providerID string, call func(context.Context) (T, error)) TimeoutResult[T] {
	var zero T
	if ctx == nil {
		ctx = context.Background()
	}
	if call == nil || deadline.IsZero() {
		return TimeoutResult[T]{}
	}
	if err := ctx.Err(); err != nil {
		return TimeoutResult[T]{Value: zero, Err: err, ParentCanceled: true}
	}
	if !guard.TryEnter(stage, providerID) {
		return TimeoutResult[T]{TimedOut: true, GuardRejected: true}
	}
	childCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		defer guard.finish(stage, providerID)
		v, err := call(childCtx)
		done <- result{value: v, err: err}
	}()
	providerResult := func(r result) TimeoutResult[T] {
		// Parent cancellation takes precedence over provider completion and the
		// evaluation deadline (requirement 6.4).
		if ctx.Err() != nil {
			return TimeoutResult[T]{Value: zero, Err: ctx.Err(), ParentCanceled: true}
		}
		// A cooperative provider commonly returns ctx.Err() after the evaluation
		// deadline fires. Preserve that as an evaluation timeout, not an ordinary
		// provider failure. Successful results are accepted when the provider result
		// is already available, even if the deadline notification is also ready.
		if errors.Is(r.err, context.DeadlineExceeded) && childCtx.Err() != nil {
			return TimeoutResult[T]{Value: zero, TimedOut: true}
		}
		return TimeoutResult[T]{Value: r.value, Err: r.err}
	}

	select {
	case r := <-done:
		return providerResult(r)
	case <-childCtx.Done():
		if ctx.Err() != nil {
			select {
			case r := <-done:
				out := providerResult(r)
				out.Value = zero
				out.Err = ctx.Err()
				out.TimedOut = false
				out.ParentCanceled = true
				return out
			default:
			}
			stillRunning := guard.markStillRunning(stage, providerID)
			return TimeoutResult[T]{Value: zero, Err: ctx.Err(), ParentCanceled: true, ProviderStillRunning: stillRunning}
		}
		// If the provider result is already available, prefer it over the timeout
		// notification. Both channels can become ready at nearly the same time; this
		// avoids discarding a successful result just because the deadline was observed
		// first by select scheduling.
		select {
		case r := <-done:
			return providerResult(r)
		default:
		}
		stillRunning := guard.markStillRunning(stage, providerID)
		return TimeoutResult[T]{Value: zero, TimedOut: true, ProviderStillRunning: stillRunning}
	case <-ctx.Done():
		select {
		case r := <-done:
			out := providerResult(r)
			out.Value = zero
			out.Err = ctx.Err()
			out.TimedOut = false
			out.ParentCanceled = true
			return out
		default:
		}
		stillRunning := guard.markStillRunning(stage, providerID)
		return TimeoutResult[T]{Value: zero, Err: ctx.Err(), ParentCanceled: true, ProviderStillRunning: stillRunning}
	}
}

// RunDecisionProviderWithTimeout invokes call with a derived child context that
// expires at the earlier of the parent deadline and now+timeout (requirements 6.1,
// 6.2, 6.3, 6.4). The derived deadline is now+timeout.
//
// Legacy compatibility: when timeout is zero, no child context and no goroutine are
// created; call runs directly against ctx and the result is returned unwrapped. This
// preserves existing extension behavior and avoids the per-call goroutine wrapper
// (design Timeout Enforcement).
//
// Stage runners that need the same deadline value projected onto emitted evidence
// should derive the deadline once and call [RunDecisionProviderWithDeadline] instead,
// so the bounding deadline and the evidence EvaluationDeadline are exactly identical.
func RunDecisionProviderWithTimeout[T any](ctx context.Context, timeout time.Duration, call func(context.Context) (T, error)) TimeoutResult[T] {
	if ctx == nil {
		ctx = context.Background()
	}
	if call == nil {
		return TimeoutResult[T]{}
	}
	if timeout <= 0 {
		v, err := call(ctx)
		return TimeoutResult[T]{Value: v, Err: err}
	}
	return RunDecisionProviderWithDeadline[T](ctx, time.Now().Add(timeout), call)
}
