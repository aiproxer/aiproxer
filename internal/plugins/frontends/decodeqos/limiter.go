package decodeqos

import (
	"context"
	"errors"
	"sync"
)

var (
	// ErrOverweight is returned when weight exceeds the limiter's max inflight byte budget.
	ErrOverweight = errors.New("decodeqos: weight exceeds inflight byte budget")
	// ErrInvalidWeight is returned when weight is negative.
	ErrInvalidWeight = errors.New("decodeqos: negative weight")
)

// Admission bounds concurrent frontend protocol decode/materialization work and
// weighted in-flight decode bytes while decode is active.
type Admission interface {
	TryAcquire(ctx context.Context, weight int64) (release func(), ok bool, err error)
	Acquire(ctx context.Context, weight int64) (release func(), err error)
}

// Limiter bounds concurrent frontend protocol decode/materialization and weighted
// in-flight decode bytes while decode is active. Body ReadAll and JSON preflight
// run before admission.
type Limiter struct {
	maxConcurrent    int
	maxInflightBytes int64

	mu            sync.Mutex
	cond          *sync.Cond
	inflightCount int
	inflightBytes int64
}

// New returns nil when maxConcurrent <= 0 or maxInflightBytes <= 0 so callers can keep
// zero-value behavior unlimited. Standard distribution wiring always passes positive finite values.
func New(maxConcurrent int, maxInflightBytes int64) *Limiter {
	if maxConcurrent <= 0 || maxInflightBytes <= 0 {
		return nil
	}
	l := &Limiter{
		maxConcurrent:    maxConcurrent,
		maxInflightBytes: maxInflightBytes,
	}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// TryAcquire attempts to reserve decode capacity without waiting behind saturated work.
func (l *Limiter) TryAcquire(ctx context.Context, weight int64) (func(), bool, error) {
	if l == nil {
		return func() {}, true, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if weight < 0 {
		return nil, false, ErrInvalidWeight
	}
	if weight > l.maxInflightBytes {
		return nil, false, ErrOverweight
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Recheck under the mutex: ctx may have canceled while waiting for l.mu.
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !l.canAdmitLocked(weight) {
		return nil, false, nil
	}
	l.admitLocked(weight)
	return l.releaseFunc(weight), true, nil
}

// Acquire waits until decode capacity is available or ctx is canceled.
func (l *Limiter) Acquire(ctx context.Context, weight int64) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	if weight < 0 {
		return nil, ErrInvalidWeight
	}
	if weight > l.maxInflightBytes {
		return nil, ErrOverweight
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	stop := context.AfterFunc(ctx, func() {
		l.mu.Lock()
		l.cond.Broadcast()
		l.mu.Unlock()
	})
	defer stop()

	for !l.canAdmitLocked(weight) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		l.cond.Wait()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	l.admitLocked(weight)
	return l.releaseFunc(weight), nil
}

func (l *Limiter) canAdmitLocked(weight int64) bool {
	if l.inflightCount >= l.maxConcurrent {
		return false
	}
	if weight == 0 {
		return true
	}
	// Overflow-safe: reject when inflightBytes + weight would exceed max.
	if l.inflightBytes > l.maxInflightBytes-weight {
		return false
	}
	return true
}

func (l *Limiter) admitLocked(weight int64) {
	l.inflightCount++
	l.inflightBytes += weight
}

func (l *Limiter) releaseFunc(weight int64) func() {
	return sync.OnceFunc(func() {
		l.mu.Lock()
		l.inflightCount--
		l.inflightBytes -= weight
		l.cond.Broadcast()
		l.mu.Unlock()
	})
}
