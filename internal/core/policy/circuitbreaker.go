package policy

import (
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	DefaultFailureThreshold      = 5
	DefaultOpenDuration          = 30 * time.Second
	defaultMaxTrackedCircuitKeys = 10000
)

type CircuitBreakerOptions struct {
	FailureThreshold int
	OpenDuration     time.Duration
	Now              func() time.Time
	// MaxTrackedKeys caps distinct candidate keys retained in memory; zero uses defaultMaxTrackedCircuitKeys.
	// When full, idle keys (no streak, not blocked) are evicted first, then the lowest-pressure key.
	MaxTrackedKeys int
}

type CircuitBreaker struct {
	mu sync.Mutex

	threshold int
	openFor   time.Duration
	now       func() time.Time
	maxKeys   int

	keys map[string]*cbState
}

type cbState struct {
	consecutiveFailures int
	blockedUntil        time.Time
}

func NewCircuitBreaker(opts CircuitBreakerOptions) *CircuitBreaker {
	th := opts.FailureThreshold
	if th < 1 {
		th = DefaultFailureThreshold
	}
	d := opts.OpenDuration
	if d <= 0 {
		d = DefaultOpenDuration
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	mk := opts.MaxTrackedKeys
	if mk == 0 {
		mk = defaultMaxTrackedCircuitKeys
	}
	return &CircuitBreaker{
		threshold: th,
		openFor:   d,
		now:       now,
		maxKeys:   mk,
		keys:      make(map[string]*cbState),
	}
}

// OnRoutingAttemptOutcome feeds consecutive failure tracking for routing candidate keys.
// Semantics (including swallowed vs surfaced): see docs/routing-health-circuit-breaker.md.
func (cb *CircuitBreaker) OnRoutingAttemptOutcome(candidateKey string, outcome aipapi.AttemptOutcome) {
	if cb == nil || candidateKey == "" {
		return
	}
	switch outcome {
	case aipapi.AttemptSuccess:
		cb.recordSuccess(candidateKey)
	case aipapi.AttemptSurfacedFailure, aipapi.AttemptSwallowedFailure:
		cb.recordFailure(candidateKey)
	default:
	}
}

func (cb *CircuitBreaker) recordSuccess(key string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	st := cb.keys[key]
	if st == nil {
		return
	}
	st.consecutiveFailures = 0
	st.blockedUntil = time.Time{}
}

func (cb *CircuitBreaker) recordFailure(key string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.keys[key] == nil {
		cb.ensureRoomForNewKeyLocked(key)
		cb.keys[key] = &cbState{}
	}
	st := cb.keys[key]
	st.consecutiveFailures++
	if st.consecutiveFailures >= cb.threshold {
		st.blockedUntil = cb.now().Add(cb.openFor)
	}
}

func (cb *CircuitBreaker) UnhealthyCandidateKeys() map[string]struct{} {
	if cb == nil {
		return nil
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	now := cb.now()
	out := make(map[string]struct{})
	for k, st := range cb.keys {
		if st == nil {
			continue
		}
		if now.Before(st.blockedUntil) {
			out[k] = struct{}{}
			continue
		}
		if !st.blockedUntil.IsZero() {
			st.consecutiveFailures = 0
			st.blockedUntil = time.Time{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (cb *CircuitBreaker) ensureRoomForNewKeyLocked(key string) {
	limit := cb.maxKeys
	if limit <= 0 {
		limit = defaultMaxTrackedCircuitKeys
	}
	for cb.keys[key] == nil && len(cb.keys) >= limit {
		if !cb.evictOneIdleLocked() {
			cb.evictLowestPressureLocked()
		}
	}
}

func (cb *CircuitBreaker) evictOneIdleLocked() bool {
	for k, st := range cb.keys {
		if st != nil && st.consecutiveFailures == 0 && st.blockedUntil.IsZero() {
			delete(cb.keys, k)
			return true
		}
	}
	return false
}

func (cb *CircuitBreaker) evictLowestPressureLocked() {
	victim := ""
	for k, st := range cb.keys {
		if st == nil {
			delete(cb.keys, k)
			return
		}
		if victim == "" {
			victim = k
			continue
		}
		vSt := cb.keys[victim]
		if st.consecutiveFailures < vSt.consecutiveFailures ||
			(st.consecutiveFailures == vSt.consecutiveFailures && k < victim) {
			victim = k
		}
	}
	if victim != "" {
		delete(cb.keys, victim)
	}
}
