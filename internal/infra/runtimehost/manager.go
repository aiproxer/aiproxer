package runtimehost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Manager is the production generation publication and acquire surface (req 5.2-5.4, 10, 15).
// Manager also owns post-publish retirement scheduling (task 7.3): it fires
// one bounded background retirement per replaced generation, bounded by the
// finite retained-generation budget — never an unbounded worker map/pool.
type Manager struct {
	instanceID      string
	active          atomic.Pointer[Generation]
	nextID          atomic.Int64
	maxRetained     int
	mu              sync.Mutex
	retained        []*Generation
	clock           *ManualClock
	afterRetainHook atomic.Value // func(*Generation)
	shuttingDown    atomic.Bool

	policyMu      sync.Mutex
	cleanupPolicy CleanupPolicy
	observer      *ReloadObserver
}

// NewManager constructs a manager with a finite retained-generation budget (req 10.8).
// clock may be nil. A cryptographically random opaque instance ID is assigned once
// for this manager/process incarnation (task 3.6).
func NewManager(maxRetained int, clock *ManualClock) *Manager {
	return NewManagerWithInstanceID(maxRetained, clock, newRuntimeInstanceID())
}

// NewManagerWithInstanceID is the deterministic constructor/test seam for a fixed
// opaque runtime instance identity. Empty instanceID falls back to a random one.
func NewManagerWithInstanceID(maxRetained int, clock *ManualClock, instanceID string) *Manager {
	if maxRetained < 0 {
		maxRetained = 0
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		instanceID = newRuntimeInstanceID()
	}
	return &Manager{maxRetained: maxRetained, clock: clock, instanceID: instanceID}
}

// InstanceID returns the opaque process/manager incarnation identity.
func (m *Manager) InstanceID() string {
	if m == nil {
		return ""
	}
	return m.instanceID
}

func newRuntimeInstanceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Extremely unlikely; still produce a non-empty collision-resistant-ish id.
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}

// Prepare creates a prepared candidate with no generation-owned payload.
func (m *Manager) Prepare(label string) *Generation {
	return newGeneration(label, GenPrepared, nil)
}

// PrepareOwned creates a prepared candidate bound to generation-owned resources.
func (m *Manager) PrepareOwned(label string, owned OwnedCloser) *Generation {
	return newGeneration(label, GenPrepared, owned)
}

// PrepareRequestPlane creates a prepared candidate bound to an immutable
// request-plane publisher (owned close + Handler).
func (m *Manager) PrepareRequestPlane(label string, plane PublishedRequestPlane) *Generation {
	return newGenerationWithRequestPlane(label, GenPrepared, plane)
}

// BeginPrepare creates a preparing candidate (MarkPrepared before Publish).
func (m *Manager) BeginPrepare(label string, owned OwnedCloser) *Generation {
	return newGeneration(label, GenPreparing, owned)
}

// BeginPrepareRequestPlane creates a preparing candidate bound to an immutable
// request-plane publisher (MarkPrepared before Publish).
func (m *Manager) BeginPrepareRequestPlane(label string, plane PublishedRequestPlane) *Generation {
	return newGenerationWithRequestPlane(label, GenPreparing, plane)
}

// Active returns the current active generation pointer (may be nil).
func (m *Manager) Active() *Generation { return m.active.Load() }

// RetainedCount returns how many retired generations occupy the retention budget.
func (m *Manager) RetainedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.retained)
}

// HasOpenGenerations reports whether any active or retained generation is still
// non-closed. Used by process shutdown to avoid closing ProcessServices while a
// generation pin/lease remains (req 13.x).
func (m *Manager) HasOpenGenerations() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if g := m.active.Load(); g != nil && g.Lifecycle() != GenClosed {
		return true
	}
	for _, g := range m.retained {
		if g != nil && g.Lifecycle() != GenClosed {
			return true
		}
	}
	return false
}

// GenerationByID returns an active or retained generation with the given id, or nil.
// Used by terminal-work generation-bound provider resolution (task 3.6).
func (m *Manager) GenerationByID(id int64) *Generation {
	return m.GenerationByIdentity(m.instanceID, id)
}

// GenerationByIdentity returns an open generation only when both the manager
// instance ID and numeric generation ID match exactly (task 3.6 restart safety).
func (m *Manager) GenerationByIdentity(instanceID string, id int64) *Generation {
	if m == nil || id <= 0 {
		return nil
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" || instanceID != m.instanceID {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if g := m.active.Load(); g != nil && g.ID() == id && g.Lifecycle() != GenClosed {
		return g
	}
	for _, g := range m.retained {
		if g != nil && g.ID() == id && g.Lifecycle() != GenClosed {
			return g
		}
	}
	return nil
}

// ClockNow returns the manager clock instant, or zero if unset.
func (m *Manager) ClockNow() time.Time {
	if m.clock == nil {
		return time.Time{}
	}
	return m.clock.Now()
}

// SetAfterRetainHook installs a barrier hook between tryRetain and pointer recheck.
func (m *Manager) SetAfterRetainHook(fn func(*Generation)) {
	if fn == nil {
		m.afterRetainHook.Store((func(*Generation))(nil))
		return
	}
	m.afterRetainHook.Store(fn)
}

// Acquire loads the active generation with retain and pointer recheck (req 5.3-5.4, 10.2, 15.1).
func (m *Manager) Acquire() (*Lease, bool) {
	if m == nil || m.shuttingDown.Load() {
		return nil, false
	}
	for {
		if m.shuttingDown.Load() {
			return nil, false
		}
		g := m.active.Load()
		if g == nil {
			return nil, false
		}
		if !g.tryRetain() {
			if m.active.Load() == g {
				return nil, false
			}
			continue
		}
		if v := m.afterRetainHook.Load(); v != nil {
			if hook, ok := v.(func(*Generation)); ok && hook != nil {
				hook(g)
			}
		}
		if !m.shuttingDown.Load() && m.active.Load() == g {
			return &Lease{gen: g}, true
		}
		g.releaseRef()
		if m.shuttingDown.Load() {
			return nil, false
		}
	}
}

// Publish atomically swaps the active pointer after budget reservation (req 5.2, 5.9, 15.4).
// Retention rejection and host-shutdown rejection roll back the unpublished candidate
// exactly once after releasing Manager.mu so closers may re-enter status paths (req 10.9).
func (m *Manager) Publish(candidate *Generation) error {
	postSwap, err := m.commitPublish(candidate, nil)
	if err != nil {
		return err
	}
	postSwap.run()
	return nil
}

// commitPublish performs only the guarded generation swap. The caller must run
// the returned post-swap work after installing any state committed with the
// generation, so lifecycle observers cannot see a published generation paired
// with the previous source baseline.
func (m *Manager) commitPublish(candidate *Generation, committed *bool) (*publishPostSwap, error) {
	if candidate == nil {
		return nil, ErrNotPrepared
	}

	m.mu.Lock()
	var reject error
	switch {
	case m.shuttingDown.Load():
		reject = ErrHostShuttingDown
	default:
		switch candidate.Lifecycle() {
		case GenPrepared:
			// ok
		case GenPreparing:
			m.mu.Unlock()
			return nil, ErrNotPrepared
		case GenActive, GenRetiring, GenQuiescing, GenQuiesced, GenDrained, GenClosing, GenClosed:
			m.mu.Unlock()
			return nil, ErrAlreadyPublished
		default:
			m.mu.Unlock()
			return nil, ErrNotPrepared
		}

		if len(m.retained) >= m.maxRetained && m.active.Load() != nil {
			reject = ErrRetentionBlocked
		} else {
			prev := m.active.Load()
			var prevID int64
			if prev != nil {
				prevID = prev.ID()
			}
			id := m.nextID.Add(1)
			publishedAt := time.Now().UTC()
			if m.clock != nil {
				publishedAt = m.clock.Now()
			}
			if err := candidate.assignPublishWithInstance(id, prevID, m.instanceID, publishedAt); err != nil {
				m.mu.Unlock()
				return nil, err
			}

			prior := m.active.Swap(candidate)
			if committed != nil {
				*committed = true
			}
			if prior != nil {
				prior.markRetiring()
				m.retained = append(m.retained, prior)
			}
			m.mu.Unlock()
			return &publishPostSwap{manager: m, prior: prior, candidate: candidate}, nil
		}
	}
	m.mu.Unlock()

	cleanupErr := candidate.Discard()
	if cleanupErr != nil && !errors.Is(cleanupErr, ErrAlreadyClosed) {
		return nil, errors.Join(reject, cleanupErr)
	}
	return nil, reject
}

type publishPostSwap struct {
	once      sync.Once
	manager   *Manager
	prior     *Generation
	candidate *Generation
}

func (p *publishPostSwap) run() {
	if p == nil || p.manager == nil || p.candidate == nil {
		return
	}
	p.once.Do(func() {
		if p.prior != nil {
			go p.manager.scheduleRetire(p.prior)
		}
		// After the active swap so compile/compose failures cannot start work.
		_, observer := p.manager.retirementDeps()
		startPublishedWork(p.candidate, observer)
	})
}

func startPublishedWork(candidate *Generation, observer *ReloadObserver) {
	if candidate == nil {
		return
	}
	failed := false
	func() {
		defer func() {
			if recover() != nil {
				failed = true
			}
		}()
		starter, ok := candidate.RequestPlane().(PublishedWorkStarter)
		if !ok || starter == nil {
			return
		}
		if starter.StartPublished(context.Background()) != nil {
			failed = true
		}
	}()
	if failed && observer != nil {
		observer.ObserveLifecycle(context.Background(), "cleanup", string(LifecycleOutcomeCleanupFailed), 0)
	}
}

// SweepClosed drops closed generations from the retained budget set.
func (m *Manager) SweepClosed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	dst := m.retained[:0]
	for _, g := range m.retained {
		if g.Lifecycle() != GenClosed {
			dst = append(dst, g)
		}
	}
	m.retained = dst
}

// SetCleanupPolicy installs the post-drain cleanup retry budget used by both
// automatic post-publish retirement scheduling and RetireGeneration. Nil-safe.
func (m *Manager) SetCleanupPolicy(p CleanupPolicy) {
	if m == nil {
		return
	}
	m.policyMu.Lock()
	m.cleanupPolicy = p
	m.policyMu.Unlock()
}

// SetLifecycleObserver attaches optional reload lifecycle telemetry
// (quiesce/cleanup spans) used by retirement. Nil-safe.
func (m *Manager) SetLifecycleObserver(obs *ReloadObserver) {
	if m == nil {
		return
	}
	m.policyMu.Lock()
	m.observer = obs
	m.policyMu.Unlock()
}

func (m *Manager) HasLifecycleObserver() bool {
	if m == nil {
		return false
	}
	m.policyMu.Lock()
	defer m.policyMu.Unlock()
	return m.observer != nil
}

func (m *Manager) retirementDeps() (CleanupPolicy, *ReloadObserver) {
	if m == nil {
		return CleanupPolicy{}, nil
	}
	m.policyMu.Lock()
	defer m.policyMu.Unlock()
	return m.cleanupPolicy, m.observer
}

// RetireGeneration synchronously drives one generation's quiesce → drain →
// close cycle using the manager's cleanup policy/observer, deriving the
// QuiesceCloser solely from the generation (never an external collaborator).
// It is the sync retry/wait counterpart to automatic post-publish scheduling:
// callers may retry an exhausted-cleanup generation, wait for a specific
// generation's retirement to finish, or drive retirement during shutdown.
// Concurrent calls for the same generation are serialized by that
// generation's own context-aware retirement admission; unrelated generations
// retire independently.
func (m *Manager) RetireGeneration(ctx context.Context, g *Generation) (RetirementStatus, error) {
	if m == nil || g == nil {
		return RetirementStatus{}, nil
	}
	policy, observer := m.retirementDeps()
	return retireGeneration(ctx, g, policy, observer, true, nil)
}

// scheduleRetire is the one-goroutine-per-replaced-generation background
// retirement launched by Publish. It never blocks Publish and is bounded by
// the finite retained-generation budget (no unbounded worker map/pool).
// Async retirement quiesces then either closes immediately (already drained)
// or arms a post-drain close — it must not park in select-on-Drained, so a
// pinned prior generation cannot leak a long-lived retirement goroutine.
func (m *Manager) scheduleRetire(g *Generation) {
	if m == nil || g == nil {
		return
	}
	policy, observer := m.retirementDeps()
	_, _ = retireGeneration(context.Background(), g, policy, observer, false, m.SweepClosed)
}
