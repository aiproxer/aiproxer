package runtime

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/submission"
)

func (e *Executor) billingEnabled() bool {
	return e != nil && (e.BillingLegObserver != nil || e.hasTerminalSink())
}

func (e *Executor) observeBillingLeg(ctx context.Context, record usageevidence.CallLegUsageRecord) {
	if e == nil || e.BillingLegObserver == nil {
		return
	}
	_ = safety.Call(safety.BoundaryStream, "billing_leg_observer", func() error {
		e.BillingLegObserver.ObserveBillingLeg(ctx, record)
		return nil
	})
}

func (e *Executor) callFinalizeBilling(ctx context.Context, in execbackend.BillingFinalizationInput) (aipapi.Event, error) {
	result, err := e.callFinalizeBillingResult(ctx, in)
	return result.Usage, err
}

func (e *Executor) callFinalizeBillingResult(ctx context.Context, in execbackend.BillingFinalizationInput) (execbackend.BillingFinalizationResult, error) {
	if e == nil || e.Backends == nil {
		return execbackend.BillingFinalizationResult{}, fmt.Errorf("executor finalizer: no backends")
	}
	backendID := strings.TrimSpace(in.Backend)
	be, ok := e.Backends[backendID]
	if !ok || (be.FinalizeBilling == nil && be.FinalizeBillingV2 == nil) {
		return execbackend.BillingFinalizationResult{}, fmt.Errorf("executor finalizer: backend %q does not support FinalizeBilling", backendID)
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingFinalizeTimeout)
	defer cancel()
	in.Backend = backendID
	result, err := safety.CallValue(safety.BoundaryBackend, "backend_finalize_billing", func() (execbackend.BillingFinalizationResult, error) {
		if be.FinalizeBillingV2 != nil {
			return be.FinalizeBillingV2(persistCtx, in)
		}
		ev, err := be.FinalizeBilling(persistCtx, in)
		return execbackend.BillingFinalizationResult{Usage: ev}, err
	})
	if err != nil {
		if e.Log != nil {
			e.Log.DebugContext(persistCtx, "billing FinalizeBilling", "error", err)
		}
		return execbackend.BillingFinalizationResult{}, err
	}
	if result.Usage.Kind != aipapi.EventUsageDelta {
		return execbackend.BillingFinalizationResult{}, fmt.Errorf("executor finalizer: invalid event kind %q", result.Usage.Kind)
	}
	if len(result.EconomicEvidence) > usageevidence.MaxCallLegEvidenceObservations {
		return execbackend.BillingFinalizationResult{}, fmt.Errorf("executor finalizer: economic evidence exceeds limit")
	}
	for i := range result.EconomicEvidence {
		if err := result.EconomicEvidence[i].Observation.Validate(); err != nil {
			return execbackend.BillingFinalizationResult{}, fmt.Errorf("executor finalizer: invalid economic evidence %d", i)
		}
	}
	return result, nil
}

type billingCallState struct {
	callID       usageevidence.BillingCallID
	submissionID string

	mu sync.Mutex

	// scope is the trusted customer scope frozen at exposure admission. It
	// lets terminal handoffs carry customer identity on detached contexts
	// long after the request context is gone.
	scope scope.PrincipalScopeView
	// scopeFrozen records that a trusted scope was frozen for this call. The
	// fact is independent of the stored view: a present scope whose fields are
	// all unknown is not represented by the stored view at all, and the
	// durable identity must tell that case apart from an absent scope.
	scopeFrozen bool

	allocated map[string]int // BLegID -> actual AttemptSeq
	frozen    []string
	hasFrozen bool
	legTimes  []billingLegTiming

	finalizeMu sync.Mutex
	finalize   map[string]*finalizeCacheEntry
}

func newBillingCallState(callID usageevidence.BillingCallID) *billingCallState {
	return &billingCallState{
		callID:    callID,
		allocated: make(map[string]int),
		finalize:  make(map[string]*finalizeCacheEntry),
	}
}

// freezeScope records the trusted customer scope once, at exposure
// admission. Later calls never override it: terminal handoffs must observe
// the frozen admission scope, not a re-resolved one.
//
// The freeze records its own presence fact. The stored view keeps only a scope
// whose principal is known, so a scope that was frozen with an unknown
// principal is present but not represented by the view; the presence fact
// therefore comes from the freeze itself and is never inferred from the
// stored view.
func (s *billingCallState) freezeScope(sc scope.PrincipalScopeView) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scopeFrozen {
		return
	}
	s.scopeFrozen = true
	if !sc.PrincipalID.IsKnown() {
		return
	}
	s.scope = sc.Clone()
}

// frozenScope returns the admission-frozen customer scope, or zero when the
// call never admitted one.
func (s *billingCallState) frozenScope() scope.PrincipalScopeView {
	if s == nil {
		return scope.PrincipalScopeView{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scope.Clone()
}

// frozenScopePresent reports whether a trusted scope was frozen for this call.
// The fact is independent of the stored view: a present scope whose fields
// are all unknown is not represented by the stored view at all.
func (s *billingCallState) frozenScopePresent() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scopeFrozen
}

func (s *billingCallState) ensureSubmissionID(id string) error {
	if s == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.submissionID != "" && s.submissionID != id {
		return fmt.Errorf("%w: billing call already belongs to submission %q", submission.ErrScopeMismatch, s.submissionID)
	}
	s.submissionID = id
	return nil
}

func (s *billingCallState) submissionIdentity() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submissionID
}

func billingSubmissionID(s *billingCallState) string {
	return s.submissionIdentity()
}

func (s *billingCallState) noteAllocatedBLeg(bLegID string, seq int) {
	if s == nil {
		return
	}
	bLegID = strings.TrimSpace(bLegID)
	if bLegID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasFrozen {
		return
	}
	if s.allocated == nil {
		s.allocated = make(map[string]int)
	}
	s.allocated[bLegID] = seq
}

func (s *billingCallState) freezeAllocatedBLegs() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasFrozen {
		return append([]string(nil), s.frozen...)
	}
	ids := make([]string, 0, len(s.allocated))
	for id := range s.allocated {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	s.frozen = append([]string(nil), ids...)
	s.hasFrozen = true
	return ids
}

func (s *billingCallState) noteLegTimes(started, finished time.Time) {
	if s == nil || started.IsZero() || finished.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasFrozen {
		return
	}
	s.legTimes = append(s.legTimes, billingLegTiming{startedAt: started, finishedAt: finished})
}

func (s *billingCallState) timingBounds(now time.Time) (time.Time, time.Time) {
	if s == nil {
		return now, now
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var started, finished time.Time
	for _, leg := range s.legTimes {
		if !leg.startedAt.IsZero() && (started.IsZero() || leg.startedAt.Before(started)) {
			started = leg.startedAt
		}
		if !leg.finishedAt.IsZero() && (finished.IsZero() || leg.finishedAt.After(finished)) {
			finished = leg.finishedAt
		}
	}
	if started.IsZero() {
		started = now
	}
	if finished.IsZero() {
		finished = now
	}
	if finished.Before(started) {
		finished = started
	}
	return started, finished
}

type billingLegTiming struct {
	startedAt  time.Time
	finishedAt time.Time
}

type finalizeCacheEntry struct {
	done   chan struct{}
	result execbackend.BillingFinalizationResult
	ok     bool
}

func finalizeCacheKey(in execbackend.BillingFinalizationInput) string {
	if id := strings.TrimSpace(in.BLegID); id != "" {
		return id
	}
	return strings.TrimSpace(in.ALegID) + "|" + strings.TrimSpace(in.Backend) + "|" + strings.TrimSpace(in.Model)
}

func (s *billingCallState) finalizeOnce(ctx context.Context, in execbackend.BillingFinalizationInput, finalizeFn func(context.Context, execbackend.BillingFinalizationInput) (aipapi.Event, error)) (aipapi.Event, bool) {
	result, ok := s.finalizeOnceWithEvidence(ctx, in, func(ctx context.Context, in execbackend.BillingFinalizationInput) (execbackend.BillingFinalizationResult, error) {
		ev, err := finalizeFn(ctx, in)
		return execbackend.BillingFinalizationResult{Usage: ev}, err
	})
	return result.Usage, ok
}

func (s *billingCallState) finalizeOnceWithEvidence(ctx context.Context, in execbackend.BillingFinalizationInput, finalizeFn func(context.Context, execbackend.BillingFinalizationInput) (execbackend.BillingFinalizationResult, error)) (execbackend.BillingFinalizationResult, bool) {
	if s == nil {
		return execbackend.BillingFinalizationResult{}, false
	}
	key := finalizeCacheKey(in)
	if key == "" {
		result, err := finalizeFn(ctx, in)
		return result, err == nil && result.Usage.Kind == aipapi.EventUsageDelta
	}

	s.finalizeMu.Lock()
	if s.finalize == nil {
		s.finalize = make(map[string]*finalizeCacheEntry)
	}
	entry, ok := s.finalize[key]
	if ok {
		s.finalizeMu.Unlock()
		select {
		case <-entry.done:
		case <-ctx.Done():
			return execbackend.BillingFinalizationResult{}, false
		}
		return entry.result, entry.ok
	}

	entry = &finalizeCacheEntry{done: make(chan struct{})}
	s.finalize[key] = entry
	s.finalizeMu.Unlock()

	defer close(entry.done)
	result, err := finalizeFn(ctx, in)
	entry.result = result
	entry.ok = err == nil && result.Usage.Kind == aipapi.EventUsageDelta

	return entry.result, entry.ok
}

const billingFinalizeTimeout = 2 * time.Second

var billingHandoffTimeout = 2 * time.Minute
