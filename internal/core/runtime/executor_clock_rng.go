package runtime

import (
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

type attemptBudget struct {
	mu       sync.Mutex
	max      int
	used     int
	failures *candidateFailureHistory
}

func (b *attemptBudget) getFailures() *candidateFailureHistory {
	if b == nil {
		return &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
	}
	if b.failures == nil {
		b.failures = &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
	}
	return b.failures
}

func (b *attemptBudget) tryAcquire() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used >= b.max {
		return false
	}
	b.used++
	return true
}

// release refunds a previously acquired slot. It guards against underflow so it is
// safe on failure paths where the preceding tryAcquire may or may not have run.
func (b *attemptBudget) release() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used > 0 {
		b.used--
	}
}

// usedNow returns the current number of acquired slots under the mutex. Use this
// instead of reading the guarded used field directly so callers (including
// tests) avoid data races on the attempt budget.
func (b *attemptBudget) usedNow() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// exhausted reports whether every attempt slot is already acquired. It reads
// used and max under the budget mutex, matching tryAcquire's refusal condition
// exactly; a nil budget is unlimited, exactly like tryAcquire.
func (b *attemptBudget) exhausted() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used >= b.max
}

func (e *Executor) effectiveMaxAttempts() int {
	if e == nil || e.MaxAttempts <= 0 {
		return 3
	}
	return e.MaxAttempts
}

type lockedRng struct {
	mu   sync.Mutex
	base routing.Rng
}

func (l *lockedRng) Intn(n int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.base.Intn(n)
}

var _ routing.Rng = (*lockedRng)(nil)

func (e *Executor) now() time.Time {
	if e != nil && e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Executor) WallClock() func() time.Time {
	if e == nil {
		return nil
	}
	return e.Now
}

func (e *Executor) rng() routing.Rng {
	if e.Rand != nil {
		e.rngOnce.Do(func() {
			e.lockedRand = &lockedRng{base: e.Rand}
		})
		return e.lockedRand
	}
	return routing.NewSeededRng(1)
}

func (e *Executor) mergePlannerHealth() map[string]struct{} {
	if e == nil || e.CandidateHealth == nil {
		return nil
	}
	return e.CandidateHealth.UnhealthyCandidateKeys()
}
