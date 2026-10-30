package openresponses

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"
)

// VirtualClock provides deterministic time for test execution.
type VirtualClock interface {
	Now() time.Time
	Advance(d time.Duration)
	Set(t time.Time)
}

// TestVirtualClock implements VirtualClock backed by deterministic time.
type TestVirtualClock struct {
	mu  sync.RWMutex
	now time.Time
}

func NewVirtualClock(initial time.Time) *TestVirtualClock {
	if initial.IsZero() {
		initial = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	}
	return &TestVirtualClock{now: initial}
}

func (c *TestVirtualClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *TestVirtualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *TestVirtualClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// RequestObservation captures an observed HTTP/transport request for test assertions.
type RequestObservation struct {
	ID        string      `json:"id"`
	Method    string      `json:"method"`
	URLPath   string      `json:"url_path"`
	Headers   http.Header `json:"headers"`
	Body      []byte      `json:"body,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
	Redacted  bool        `json:"redacted"`
}

// BoundedCapture manages a thread-safe, bounded slice of RequestObservations with redaction support.
type BoundedCapture struct {
	mu            sync.RWMutex
	maxSize       int
	observations  []RequestObservation
	overflowCount int
	redactKeys    map[string]bool
}

func NewBoundedCapture(maxSize int, redactHeaders []string) *BoundedCapture {
	if maxSize <= 0 {
		maxSize = 100
	}
	keys := make(map[string]bool)
	for _, h := range redactHeaders {
		keys[strings.ToLower(strings.TrimSpace(h))] = true
	}
	return &BoundedCapture{
		maxSize:    maxSize,
		redactKeys: keys,
	}
}

func (b *BoundedCapture) Capture(obs RequestObservation) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if obs.Body != nil {
		obs.Body = append([]byte(nil), obs.Body...)
	}

	if obs.Headers != nil {
		clonedHeaders := make(http.Header)
		for k, vals := range obs.Headers {
			kLower := strings.ToLower(k)
			if len(b.redactKeys) > 0 && b.redactKeys[kLower] {
				clonedHeaders.Set(k, "[REDACTED]")
				obs.Redacted = true
			} else {
				valCopy := make([]string, len(vals))
				copy(valCopy, vals)
				clonedHeaders[k] = valCopy
			}
		}
		obs.Headers = clonedHeaders
	}

	if len(b.observations) >= b.maxSize {
		b.overflowCount++
		return fmt.Errorf("bounded capture limit reached (%d items), overflow count %d", b.maxSize, b.overflowCount)
	}

	b.observations = append(b.observations, obs)
	return nil
}

func (b *BoundedCapture) Observations() []RequestObservation {
	b.mu.RLock()
	defer b.mu.RUnlock()
	cp := make([]RequestObservation, len(b.observations))
	for i, obs := range b.observations {
		item := obs
		if obs.Body != nil {
			item.Body = append([]byte(nil), obs.Body...)
		}
		if obs.Headers != nil {
			item.Headers = obs.Headers.Clone()
		}
		cp[i] = item
	}
	return cp
}

func (b *BoundedCapture) OverflowCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.overflowCount
}

func (b *BoundedCapture) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.observations)
}

// CleanupContract specifies idempotent teardown rules.
type CleanupContract interface {
	Close() error
	IsClosed() bool
}

type TestCleanup struct {
	mu      sync.Mutex
	closed  bool
	onClose func() error
}

func NewTestCleanup(onClose func() error) *TestCleanup {
	return &TestCleanup{onClose: onClose}
}

func (c *TestCleanup) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil // Idempotent: second close is no-op
	}
	c.closed = true
	if c.onClose != nil {
		return c.onClose()
	}
	return nil
}

func (c *TestCleanup) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// ScriptStep defines one request/response interaction in a scripted test.
type ScriptStep struct {
	Name            string            `json:"name"`
	MatchMethod     string            `json:"match_method"`
	MatchPath       string            `json:"match_path"`
	ResponseStatus  int               `json:"response_status"`
	ResponseBody    []byte            `json:"response_body"`
	ResponseHeaders map[string]string `json:"response_headers"`
	SimulateDelay   time.Duration     `json:"simulate_delay"`
	SimulateError   error             `json:"-"`
}

// ScriptContract manages execution of a sequence of ScriptSteps.
type ScriptContract struct {
	mu        sync.RWMutex
	steps     []ScriptStep
	nextIndex int
}

func cloneScriptStep(step ScriptStep) ScriptStep {
	cp := step
	if step.ResponseBody != nil {
		cp.ResponseBody = append([]byte(nil), step.ResponseBody...)
	}
	if step.ResponseHeaders != nil {
		hMap := make(map[string]string, len(step.ResponseHeaders))
		maps.Copy(hMap, step.ResponseHeaders)
		cp.ResponseHeaders = hMap
	}
	return cp
}

func NewScriptContract(steps []ScriptStep) *ScriptContract {
	cp := make([]ScriptStep, len(steps))
	for i, s := range steps {
		cp[i] = cloneScriptStep(s)
	}
	return &ScriptContract{steps: cp}
}

func (s *ScriptContract) NextStep() (ScriptStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextIndex >= len(s.steps) {
		return ScriptStep{}, errors.New("script exhausted: no remaining steps")
	}
	step := s.steps[s.nextIndex]
	s.nextIndex++
	return cloneScriptStep(step), nil
}

func (s *ScriptContract) Remaining() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.steps) - s.nextIndex
}

// TestEvidenceDescriptor describes scenario evidence linking for testkit validation.
type TestEvidenceDescriptor struct {
	ScenarioID    string   `json:"scenario_id"`
	Outcome       string   `json:"outcome"`
	TestArtifacts []string `json:"test_artifacts"`
}

func (e TestEvidenceDescriptor) Validate() error {
	if strings.TrimSpace(e.ScenarioID) == "" {
		return errors.New("scenario ID cannot be empty string")
	}
	switch e.Outcome {
	case "lossless", "documented_deterministic_projection", "rejected_before_network", "out_of_scope":
		// Valid outcomes
	default:
		return fmt.Errorf("unknown evidence outcome: %q", e.Outcome)
	}
	if len(e.TestArtifacts) == 0 {
		return errors.New("test evidence must link at least one artifact")
	}
	return nil
}

// Harness Interfaces for OpenResponses testing modes.
// Note: These define contract interfaces ONLY; full behavior is implemented in Phase 8.

type DirectWireHarness interface {
	RunDirectWireScenario(ctx context.Context, scenarioID string) error
}

type FrontendStubHarness interface {
	RunFrontendStubScenario(ctx context.Context, scenarioID string) error
}

type BackendEmulatorHarness interface {
	RunBackendEmulatorScenario(ctx context.Context, scenarioID string) error
}

type FullBlackBoxHarness interface {
	RunFullBlackBoxScenario(ctx context.Context, scenarioID string) error
}
