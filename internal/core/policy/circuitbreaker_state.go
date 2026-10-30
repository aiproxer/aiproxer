package policy

import (
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// CircuitBreakerState is a shared failure/blockedUntil observation store for
// routing candidate keys, decoupled from any one generation's threshold and
// open-duration policy. Multiple [CircuitBreakerPolicy] views may observe and
// record against the same state while each applies its own generation-scoped
// threshold/open-duration (design "Health policy reload": reloadable request-
// plane health policy over compatible process-shared observation state).
type CircuitBreakerState struct {
	mu      sync.Mutex
	keys    map[string]*cbState
	maxKeys int
}

// CircuitBreakerStateOptions configures [NewCircuitBreakerState].
type CircuitBreakerStateOptions struct {
	// MaxTrackedKeys caps distinct candidate keys retained in memory; zero uses
	// defaultMaxTrackedCircuitKeys.
	MaxTrackedKeys int
}

// NewCircuitBreakerState constructs a process-shared observation store.
func NewCircuitBreakerState(opts CircuitBreakerStateOptions) *CircuitBreakerState {
	mk := opts.MaxTrackedKeys
	if mk <= 0 {
		mk = defaultMaxTrackedCircuitKeys
	}
	return &CircuitBreakerState{keys: make(map[string]*cbState), maxKeys: mk}
}

func (s *CircuitBreakerState) recordSuccess(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.keys[key]
	if st == nil {
		return
	}
	st.consecutiveFailures = 0
	st.blockedUntil = time.Time{}
}

func (s *CircuitBreakerState) recordFailure(key string, threshold int, openFor time.Duration, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys[key] == nil {
		s.ensureRoomForNewKeyLocked(key)
		s.keys[key] = &cbState{}
	}
	st := s.keys[key]
	st.consecutiveFailures++
	if st.consecutiveFailures >= threshold {
		st.blockedUntil = now.Add(openFor)
	}
}

// unhealthyKeys reports keys open under the caller's generation threshold.
// A key is unhealthy when its shared blockedUntil is still active, or when
// consecutiveFailures already meets this generation's threshold (so a stricter
// overlapping generation observes open without waiting for a lenient writer to
// set blockedUntil).
func (s *CircuitBreakerState) unhealthyKeys(now time.Time, threshold int) map[string]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]struct{})
	for k, st := range s.keys {
		if st == nil {
			continue
		}
		if now.Before(st.blockedUntil) {
			out[k] = struct{}{}
			continue
		}
		if threshold > 0 && st.consecutiveFailures >= threshold {
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

func (s *CircuitBreakerState) ensureRoomForNewKeyLocked(key string) {
	limit := s.maxKeys
	if limit <= 0 {
		limit = defaultMaxTrackedCircuitKeys
	}
	for s.keys[key] == nil && len(s.keys) >= limit {
		if !s.evictOneIdleLocked() {
			s.evictLowestPressureLocked()
		}
	}
}

func (s *CircuitBreakerState) evictOneIdleLocked() bool {
	for k, st := range s.keys {
		if st != nil && st.consecutiveFailures == 0 && st.blockedUntil.IsZero() {
			delete(s.keys, k)
			return true
		}
	}
	return false
}

func (s *CircuitBreakerState) evictLowestPressureLocked() {
	victim := ""
	for k, st := range s.keys {
		if st == nil {
			delete(s.keys, k)
			return
		}
		if victim == "" {
			victim = k
			continue
		}
		vSt := s.keys[victim]
		if st.consecutiveFailures < vSt.consecutiveFailures ||
			(st.consecutiveFailures == vSt.consecutiveFailures && k < victim) {
			victim = k
		}
	}
	if victim != "" {
		delete(s.keys, victim)
	}
}

// CircuitBreakerPolicy evaluates and records outcomes against a shared
// [CircuitBreakerState] using its own threshold/open-duration/clock. One
// policy view exists per config generation so request-plane health policy can
// reload while observation state remains process-shared for compatible
// backend identities (req 7.4, 9.1; design "Health policy reload").
type CircuitBreakerPolicy struct {
	state     *CircuitBreakerState
	threshold int
	openFor   time.Duration
	now       func() time.Time
}

// NewCircuitBreakerPolicy builds a generation-scoped policy view over state.
// A nil state gets a fresh private store (single-generation compatibility).
func NewCircuitBreakerPolicy(state *CircuitBreakerState, opts CircuitBreakerOptions) *CircuitBreakerPolicy {
	if state == nil {
		state = NewCircuitBreakerState(CircuitBreakerStateOptions{})
	}
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
	return &CircuitBreakerPolicy{state: state, threshold: th, openFor: d, now: now}
}

// OnRoutingAttemptOutcome feeds consecutive failure tracking using this
// policy's threshold/open-duration against the shared observation state.
func (p *CircuitBreakerPolicy) OnRoutingAttemptOutcome(candidateKey string, outcome aipapi.AttemptOutcome) {
	if p == nil || candidateKey == "" {
		return
	}
	switch outcome {
	case aipapi.AttemptSuccess:
		p.state.recordSuccess(candidateKey)
	case aipapi.AttemptSurfacedFailure, aipapi.AttemptSwallowedFailure:
		p.state.recordFailure(candidateKey, p.threshold, p.openFor, p.now())
	default:
	}
}

// UnhealthyCandidateKeys implements [CandidateHealth] over the shared state
// using this policy's failure threshold.
func (p *CircuitBreakerPolicy) UnhealthyCandidateKeys() map[string]struct{} {
	if p == nil {
		return nil
	}
	return p.state.unhealthyKeys(p.now(), p.threshold)
}

var (
	_ CandidateHealth           = (*CircuitBreakerPolicy)(nil)
	_ RoutingAttemptOutcomeSink = (*CircuitBreakerPolicy)(nil)
)
