package keepwarm

import (
	"container/heap"
	"context"
	"fmt"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// Clock is injectable so scheduler tests never need wall-clock sleeps.
type Clock interface{ Now() time.Time }

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time {
	if f == nil {
		return time.Now().UTC()
	}
	return f()
}

type (
	EpochRevision  uint64
	TargetRevision uint64
)

type ArmInput struct {
	ALegID              string
	BLegID              string
	CommittedSuccessful bool
	ToolEvents          []aipapi.ToolEvent
	Observations        []promptcache.Observation
	BackendInstanceID   string
	CanonicalModelID    string
	Controller          promptcache.Controller
}

type ArmResult struct {
	Armed       bool
	Reason      string
	Epoch       EpochRevision
	TargetCount int
}

type RenewalRecord struct {
	OperationID string
	ALegID      string
	TargetID    promptcache.TargetID
	BackendID   string
	ModelID     string
	Status      promptcache.RenewStatus
	Accounting  *promptcache.AccountingEvidence
	Err         error
	Stale       bool
}

type Hooks struct {
	// Accounting receives provider-billable maintenance evidence even when the
	// scheduling result is stale. The manager never merges it into foreground usage.
	// The context is bounded by the manager renewal timeout and canceled with the
	// scheduler/RunDue parent context.
	Accounting func(context.Context, RenewalRecord) error
	// Metric receives only bounded reason/result names, never target or session IDs.
	Metric func(name string)
}

type targetState struct {
	observation     promptcache.Observation
	controller      promptcache.Controller
	revision        TargetRevision
	dueAt           time.Time
	sequence        uint64
	backend         string
	model           string
	inFlight        bool
	cancel          context.CancelFunc
	operationID     string
	estimatedTokens int64
	reservedTokens  int64
}

type idleEpoch struct {
	aLegID           string
	revision         EpochRevision
	armedAt          time.Time
	stopAt           time.Time
	refreshes        int
	coldRecreates    int
	providerSpent    int64
	providerReserved int64
	targets          map[string]*targetState
}

type releaseJob struct {
	controller promptcache.Controller
	handle     promptcache.Handle
}

type scheduleEntry struct {
	due       time.Time
	sequence  uint64
	aLegID    string
	epoch     EpochRevision
	targetKey string
	targetRev TargetRevision
}

type scheduleHeap []scheduleEntry

func (h scheduleHeap) Len() int { return len(h) }

func (h scheduleHeap) Less(i, j int) bool {
	if h[i].due.Equal(h[j].due) {
		return h[i].sequence < h[j].sequence
	}
	return h[i].due.Before(h[j].due)
}

func (h scheduleHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *scheduleHeap) Push(value any) {
	if entry, ok := value.(scheduleEntry); ok {
		*h = append(*h, entry)
	}
}

func (h *scheduleHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

// Manager owns provider-neutral state for one immutable runtime generation.
// Scheduling, renewal execution, release cleanup, and observability live in
// focused companion files so this type remains the domain aggregate rather
// than an everything-object.
type Manager struct {
	namespace string
	mu        sync.Mutex
	cfg       Config
	clock     Clock
	hooks     Hooks
	epochs    map[string]*idleEpoch
	nextRev   uint64
	nextSeq   uint64
	state     lifecycleState
	disabled  map[string]bool
	running   int
	renewWG   sync.WaitGroup

	runCtx    context.Context
	runCancel context.CancelFunc
	jobCh     chan *renewJob
	wakeCh    chan struct{}
	runWG     sync.WaitGroup
	started   bool
	autoStart bool

	releaseQueue   []releaseJob
	releaseWake    chan struct{}
	releaseStarted bool
	releaseClosed  bool
	releaseWG      sync.WaitGroup
	quiesceDone    chan struct{}

	operationSeq atomic.Uint64
	metrics      map[string]uint64
	dueHeap      scheduleHeap
}

func NewManager(cfg Config, clock Clock, hooks Hooks) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	namespace, err := newManagerNamespace()
	if err != nil {
		return nil, fmt.Errorf("keepwarm: generate operation namespace: %w", err)
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &Manager{
		namespace: namespace,
		cfg:       cfg, clock: clock, hooks: hooks,
		epochs: make(map[string]*idleEpoch), disabled: make(map[string]bool),
		releaseWake: make(chan struct{}, 1),
		metrics:     make(map[string]uint64),
	}, nil
}

func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *Manager) SetSessionDisabled(aLegID string, disabled bool) {
	m.mu.Lock()
	if disabled {
		m.disabled[aLegID] = true
		m.invalidateLocked(aLegID, "disabled")
	} else {
		delete(m.disabled, aLegID)
	}
	m.mu.Unlock()
	m.signalWake()
}

// BeginForegroundTurn synchronously detaches the idle epoch and cancels its
// provider contexts. It intentionally does not wait for release RPCs.
func (m *Manager) BeginForegroundTurn(aLegID string) {
	m.mu.Lock()
	m.invalidateLocked(aLegID, "foreground")
	m.mu.Unlock()
	m.signalWake()
}

// EndSession invalidates maintenance and removes the generation-local policy
// shadow for the A-leg. The process policy store is forgotten by the runtime
// orchestrator alongside this call.
func (m *Manager) EndSession(aLegID string) {
	m.mu.Lock()
	delete(m.disabled, aLegID)
	m.invalidateLocked(aLegID, "session_end")
	m.mu.Unlock()
	m.signalWake()
}

func (m *Manager) invalidateLocked(aLegID, cause string) {
	epoch := m.epochs[aLegID]
	if epoch == nil {
		return
	}
	delete(m.epochs, aLegID)
	for _, target := range epoch.targets {
		if target.cancel != nil {
			target.cancel()
		}
		m.enqueueReleaseLocked(target.controller, target.observation.Handle)
	}
	if cause != "" {
		m.metricLocked("cancel_" + cause)
	}
}

func (m *Manager) ArmFromCommittedTurn(input ArmInput) ArmResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != lifecycleRunning {
		m.releaseObservationsLocked(input.Observations, input.Controller)
		m.metricLocked("generation_quiescing")
		return ArmResult{Reason: "generation_quiescing"}
	}
	if !m.cfg.Enabled {
		m.releaseObservationsLocked(input.Observations, input.Controller)
		m.metricLocked("disabled_global")
		return ArmResult{Reason: "disabled_global"}
	}
	if m.disabled[input.ALegID] {
		m.releaseObservationsLocked(input.Observations, input.Controller)
		m.metricLocked("disabled_session")
		return ArmResult{Reason: "disabled_session"}
	}
	if !input.CommittedSuccessful {
		m.releaseObservationsLocked(input.Observations, input.Controller)
		m.metricLocked("uncommitted")
		return ArmResult{Reason: "uncommitted"}
	}
	if !hasFinishedOSCommand(input.ToolEvents) {
		m.releaseObservationsLocked(input.Observations, input.Controller)
		m.metricLocked("no_os_command")
		return ArmResult{Reason: "no_os_command"}
	}
	if input.ALegID == "" || input.BLegID == "" || input.Controller == nil {
		m.releaseObservationsLocked(input.Observations, input.Controller)
		m.metricLocked("invalid_lineage")
		return ArmResult{Reason: "invalid_lineage"}
	}

	// A new committed arm replaces any older epoch for the same A-leg. The
	// foreground gate normally already did this; replacement remains fail-closed.
	m.invalidateLocked(input.ALegID, "arm_replacement")
	now := m.clock.Now()
	rev, ok := m.nextRevisionLocked()
	if !ok {
		m.releaseObservationsLocked(input.Observations, input.Controller)
		m.metricLocked("revision_exhausted")
		return ArmResult{Reason: "revision_exhausted"}
	}
	epoch := &idleEpoch{aLegID: input.ALegID, revision: rev, armedAt: now, stopAt: now.Add(m.cfg.MaxIdleDuration), targets: make(map[string]*targetState)}
	for _, observation := range input.Observations {
		if observation.ALegID != input.ALegID || observation.BLegID != input.BLegID || observation.BackendInstanceID != input.BackendInstanceID || !observation.Renewable || observation.Handle.Validate(true) != nil || observation.Validate() != nil {
			// A handle from a different backend lineage can only be released by
			// the controller that issued it; the committed controller must not
			// release foreign handles. Same-lineage skips still release.
			if observation.BackendInstanceID == input.BackendInstanceID {
				m.enqueueReleaseLocked(input.Controller, observation.Handle)
			}
			continue
		}
		due, ok, reason := m.scheduleLocked(observation, input.BackendInstanceID, input.CanonicalModelID, now, rev)
		if !ok {
			m.enqueueReleaseLocked(input.Controller, observation.Handle)
			m.metricLocked(reason)
			continue
		}
		estimate := int64(0)
		if m.cfg.MaxProviderTokensPerIdleEpoch != nil {
			if observation.Evidence.TotalTokens == nil {
				m.enqueueReleaseLocked(input.Controller, observation.Handle)
				m.metricLocked("budget_unknown")
				continue
			}
			estimate = *observation.Evidence.TotalTokens
			if estimate < 0 || !m.epochsWithinBudgetLocked(epoch, estimate) {
				m.enqueueReleaseLocked(input.Controller, observation.Handle)
				m.metricLocked("budget_exhausted")
				continue
			}
		}
		key := string(observation.TargetID) + "\x00" + string(observation.GenerationID)
		if existing, exists := epoch.targets[key]; exists {
			if string(existing.observation.Handle) != string(observation.Handle) {
				m.enqueueReleaseLocked(input.Controller, observation.Handle)
			}
			continue
		}
		if m.activeTargetCountLocked(epoch) >= m.cfg.MaxActiveTargets {
			oldEpoch, oldKey, old := m.latestDueTargetLocked(epoch)
			if old == nil || !due.Before(old.dueAt) {
				m.enqueueReleaseLocked(input.Controller, observation.Handle)
				m.metricLocked("capacity")
				continue
			}
			m.removeTargetLocked(oldEpoch, oldKey)
			m.enqueueReleaseLocked(old.controller, old.observation.Handle)
			m.deleteEmptyEpochLocked(oldEpoch.aLegID, oldEpoch)
			m.metricLocked("capacity")
		}
		seq := m.nextSequenceLocked()
		target := &targetState{observation: observation, controller: input.Controller, revision: 1, dueAt: due, sequence: seq, backend: input.BackendInstanceID, model: input.CanonicalModelID, estimatedTokens: estimate}
		epoch.targets[key] = target
		heap.Push(&m.dueHeap, scheduleEntry{due: due, sequence: seq, aLegID: input.ALegID, epoch: rev, targetKey: key, targetRev: target.revision})
	}
	if len(epoch.targets) == 0 {
		m.metricLocked("no_eligible_target")
		return ArmResult{Reason: "no_eligible_target"}
	}
	m.epochs[input.ALegID] = epoch
	if m.autoStart {
		m.ensureStartedLocked()
	}
	m.signalWakeLocked()
	m.metricLocked("armed")
	return ArmResult{Armed: true, Epoch: epoch.revision, TargetCount: len(epoch.targets)}
}

func (m *Manager) scheduleLocked(o promptcache.Observation, backend, model string, now time.Time, rev EpochRevision) (time.Time, bool, string) {
	if o.Timing.ExpiresAt != nil {
		expires := o.Timing.ExpiresAt.UTC()
		if !expires.After(now) {
			return time.Time{}, false, "expired"
		}
		window := expires.Sub(o.Timing.ObservedAt)
		if window <= 0 {
			return time.Time{}, false, "unsafe_window"
		}
		lead := min(max(window/10, 15*time.Second), 5*time.Minute)
		spread := min(lead/4, 30*time.Second)
		return expires.Add(-lead).Add(-deterministicSpread(spread, rev, m.nextSeq+1)), true, ""
	}
	if override, ok := m.cfg.heuristic(backend, model); ok {
		return o.Timing.ObservedAt.Add(override.Interval), true, ""
	}
	return time.Time{}, false, "no_schedule"
}

func deterministicSpread(max time.Duration, rev EpochRevision, seq uint64) time.Duration {
	if max <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%d:%d", rev, seq)
	return time.Duration(uint64(h.Sum32()) % uint64(max+1))
}

func (m *Manager) nextRevisionLocked() (EpochRevision, bool) {
	if m.nextRev == ^uint64(0) {
		return 0, false
	}
	m.nextRev++
	return EpochRevision(m.nextRev), true
}

func (m *Manager) nextSequenceLocked() uint64 {
	m.nextSeq++
	return m.nextSeq
}

func (m *Manager) activeTargetCountLocked(candidate *idleEpoch) int {
	count := 0
	for _, epoch := range m.epochs {
		count += len(epoch.targets)
	}
	if candidate != nil && m.epochs[candidate.aLegID] != candidate {
		count += len(candidate.targets)
	}
	return count
}

func (m *Manager) latestDueTargetLocked(candidate *idleEpoch) (*idleEpoch, string, *targetState) {
	var latestEpoch *idleEpoch
	var latestKey string
	var latest *targetState
	consider := func(epoch *idleEpoch) {
		if epoch == nil {
			return
		}
		for key, target := range epoch.targets {
			if target.inFlight {
				continue
			}
			if latest == nil || target.dueAt.After(latest.dueAt) || (target.dueAt.Equal(latest.dueAt) && target.sequence > latest.sequence) {
				latestEpoch, latestKey, latest = epoch, key, target
			}
		}
	}
	for _, epoch := range m.epochs {
		consider(epoch)
	}
	if candidate != nil && m.epochs[candidate.aLegID] != candidate {
		consider(candidate)
	}
	return latestEpoch, latestKey, latest
}
