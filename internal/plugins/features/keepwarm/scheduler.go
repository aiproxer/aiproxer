package keepwarm

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// RunDue dispatches all currently due work, bounded by concurrency and budgets.
// It is the deterministic fake-clock seam and uses the same bounded renewal
// execution path as the production scheduler workers.
func (m *Manager) RunDue(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		job := m.claimDue(ctx, true)
		if job == nil {
			return
		}
		go func(j *renewJob) {
			defer m.renewWG.Done()
			defer m.finishJob()
			m.execute(ctx, j)
		}(job)
	}
}

// Start owns one scheduler loop and a bounded worker pool for this generation.
// It is lazy: a default-on manager with no eligible target owns no goroutines.
func (m *Manager) Start() {
	m.mu.Lock()
	m.autoStart = true
	if len(m.epochs) > 0 {
		m.ensureStartedLocked()
	}
	m.mu.Unlock()
}

func (m *Manager) ensureStartedLocked() {
	if m.started || m.state != lifecycleRunning {
		return
	}
	m.started = true
	m.runCtx, m.runCancel = context.WithCancel(context.Background())
	m.jobCh = make(chan *renewJob, m.cfg.MaxConcurrentRenewals)
	m.wakeCh = make(chan struct{}, 1)
	m.runWG.Add(1)
	go m.schedulerLoop()
	for i := 0; i < m.cfg.MaxConcurrentRenewals; i++ {
		m.runWG.Add(1)
		go m.workerLoop()
	}
}

func (m *Manager) signalWake() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.signalWakeLocked()
}

func (m *Manager) signalWakeLocked() {
	wake := m.wakeCh
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (m *Manager) schedulerLoop() {
	defer m.runWG.Done()
	for {
		m.dispatchToWorkers()
		m.mu.Lock()
		ctx := m.runCtx
		wake := m.wakeCh
		due, ok := m.nextWakeLocked()
		m.mu.Unlock()
		if ctx == nil {
			return
		}
		var timer *time.Timer
		var timerC <-chan time.Time
		if ok {
			d := max(due.Sub(m.clock.Now()), 0)
			timer = time.NewTimer(d)
			timerC = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-wake:
			if timer != nil {
				timer.Stop()
			}
		case <-timerC:
		}
	}
}

func (m *Manager) dispatchToWorkers() {
	for {
		m.mu.Lock()
		ctx := m.runCtx
		jobCh := m.jobCh
		m.mu.Unlock()
		if ctx == nil || jobCh == nil {
			return
		}
		job := m.claimDue(ctx, false)
		if job == nil {
			return
		}
		select {
		case jobCh <- job:
		case <-ctx.Done():
			m.finishJob()
			return
		}
	}
}

func (m *Manager) workerLoop() {
	defer m.runWG.Done()
	for {
		m.mu.Lock()
		ctx := m.runCtx
		jobCh := m.jobCh
		m.mu.Unlock()
		if ctx == nil || jobCh == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case job := <-jobCh:
			if job == nil {
				return
			}
			m.execute(ctx, job)
			m.finishJob()
		}
	}
}

func (m *Manager) finishJob() {
	m.mu.Lock()
	if m.running > 0 {
		m.running--
	}
	m.mu.Unlock()
	m.signalWake()
}

func (m *Manager) nextWakeLocked() (time.Time, bool) {
	var next time.Time
	ok := false
	for _, epoch := range m.epochs {
		if !ok || epoch.stopAt.Before(next) {
			next, ok = epoch.stopAt, true
		}
	}
	m.discardStaleHeapEntriesLocked()
	if len(m.dueHeap) > 0 && (!ok || m.dueHeap[0].due.Before(next)) {
		return m.dueHeap[0].due, true
	}
	return next, ok
}

func (m *Manager) discardStaleHeapEntriesLocked() {
	for len(m.dueHeap) > 0 {
		entry := m.dueHeap[0]
		epoch := m.epochs[entry.aLegID]
		if epoch != nil && epoch.revision == entry.epoch {
			target := epoch.targets[entry.targetKey]
			if target != nil && target.revision == entry.targetRev && target.dueAt.Equal(entry.due) {
				return
			}
		}
		heap.Pop(&m.dueHeap)
	}
}

type renewJob struct {
	aLeg      string
	epoch     EpochRevision
	key       string
	targetRev TargetRevision
	target    *targetState
	operation string
	ctx       context.Context
}

func (m *Manager) claimDue(ctx context.Context, trackRenewWait bool) *renewJob {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != lifecycleRunning {
		return nil
	}
	now := m.clock.Now()
	for aLeg, epoch := range m.epochs {
		// A refresh-cap hit only retires the epoch once no refresh is in flight:
		// the cap bounds new work, it must never cancel the last allowed renewal.
		if !now.Before(epoch.stopAt) || (epoch.refreshes >= m.cfg.MaxRefreshesPerIdleEpoch && !epochHasInFlight(epoch)) {
			m.invalidateLocked(aLeg, "exhausted")
		}
	}
	if m.running >= m.cfg.MaxConcurrentRenewals {
		return nil
	}
	for {
		m.discardStaleHeapEntriesLocked()
		if len(m.dueHeap) == 0 || now.Before(m.dueHeap[0].due) {
			return nil
		}
		entry, ok := heap.Pop(&m.dueHeap).(scheduleEntry)
		if !ok {
			continue
		}
		epoch := m.epochs[entry.aLegID]
		if epoch == nil || epoch.revision != entry.epoch {
			continue
		}
		target := epoch.targets[entry.targetKey]
		if target == nil || target.revision != entry.targetRev || target.inFlight {
			continue
		}
		// Recheck hard and soft budgets immediately before each dispatch, not
		// only at claimDue entry: a burst of due targets must not exceed the
		// refresh cap, and the provider-token budget must fit the next call.
		if epoch.refreshes >= m.cfg.MaxRefreshesPerIdleEpoch {
			// The popped target is not in flight (checked above); retire just this
			// target. Any in-flight sibling keeps its context until it finishes.
			m.removeTargetLocked(epoch, entry.targetKey)
			m.enqueueReleaseLocked(target.controller, target.observation.Handle)
			m.deleteEmptyEpochLocked(entry.aLegID, epoch)
			continue
		}
		if m.cfg.MaxProviderTokensPerIdleEpoch != nil && !m.epochsWithinBudgetLocked(epoch, target.estimatedTokens) {
			m.removeTargetLocked(epoch, entry.targetKey)
			m.enqueueReleaseLocked(target.controller, target.observation.Handle)
			m.metricLocked("budget_exhausted")
			m.deleteEmptyEpochLocked(entry.aLegID, epoch)
			continue
		}
		if target.observation.Timing.ExpiresAt != nil && !now.Before(*target.observation.Timing.ExpiresAt) {
			m.removeTargetLocked(epoch, entry.targetKey)
			m.enqueueReleaseLocked(target.controller, target.observation.Handle)
			m.metricLocked("expired")
			m.deleteEmptyEpochLocked(entry.aLegID, epoch)
			continue
		}
		ctx2, cancel := context.WithCancel(ctx)
		target.cancel = cancel
		target.inFlight = true
		m.running++
		if trackRenewWait {
			// RunDue has no worker-pool WaitGroup. Register its job while
			// holding m.mu so Quiesce cannot begin Wait before this Add.
			m.renewWG.Add(1)
		}
		epoch.refreshes++
		if m.cfg.MaxProviderTokensPerIdleEpoch != nil {
			// Reserve only while the provider call is in flight. Committed spend
			// is monotonic and is never refunded when the target is retired.
			target.reservedTokens = target.estimatedTokens
			epoch.providerReserved += target.reservedTokens
		}
		op := m.nextOperationLocked(epoch.revision, target.sequence)
		target.operationID = op
		return &renewJob{aLeg: entry.aLegID, epoch: epoch.revision, key: entry.targetKey, targetRev: target.revision, target: target, operation: op, ctx: ctx2}
	}
}

func epochHasInFlight(epoch *idleEpoch) bool {
	if epoch == nil {
		return false
	}
	for _, target := range epoch.targets {
		if target.inFlight {
			return true
		}
	}
	return false
}

func (m *Manager) epochsWithinBudgetLocked(epoch *idleEpoch, estimate int64) bool {
	if m.cfg.MaxProviderTokensPerIdleEpoch == nil {
		return true
	}
	return estimate >= 0 && epoch.providerSpent+epoch.providerReserved <= *m.cfg.MaxProviderTokensPerIdleEpoch-estimate
}

func (m *Manager) nextOperationLocked(epoch EpochRevision, seq uint64) string {
	n := m.operationSeq.Add(1)
	return fmt.Sprintf("keepwarm:%s:%d:%d:%d", m.namespace, epoch, seq, n)
}

func (m *Manager) execute(parent context.Context, job *renewJob) {
	m.mu.Lock()
	timeout := m.cfg.RenewTimeout
	target := job.target
	callContext := job.ctx
	m.mu.Unlock()
	if callContext == nil {
		callContext = parent
	}
	ctx, cancel := context.WithTimeout(callContext, timeout)
	defer cancel()
	resp, err := target.controller.Renew(ctx, promptcache.RenewRequest{Handle: append(promptcache.Handle(nil), target.observation.Handle...), OperationID: job.operation})
	if err == nil {
		err = resp.Validate()
	}
	record := &RenewalRecord{OperationID: job.operation, ALegID: job.aLeg, TargetID: target.observation.TargetID, BackendID: target.backend, ModelID: target.model, Status: resp.Result.Status, Accounting: resp.Accounting, Err: err}
	m.apply(job, resp, err, record)
	if m.hooks.Accounting != nil {
		if accountingErr := m.deliverAccounting(parent, timeout, *record); accountingErr != nil {
			record.Err = errors.Join(record.Err, accountingErr)
			m.metric("accounting_error")
		}
	}
}

func (m *Manager) apply(job *renewJob, response promptcache.RenewResponse, err error, record *RenewalRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	epoch := m.epochs[job.aLeg]
	target := (*targetState)(nil)
	if epoch != nil {
		target = epoch.targets[job.key]
	}
	if epoch == nil || epoch.revision != job.epoch || target == nil || target.revision != job.targetRev || !target.inFlight {
		record.Stale = true
		m.metricLocked("stale_result")
		return
	}
	target.inFlight = false
	target.cancel = nil
	if m.cfg.MaxProviderTokensPerIdleEpoch != nil {
		if target.reservedTokens > 0 {
			epoch.providerReserved -= target.reservedTokens
			if epoch.providerReserved < 0 {
				epoch.providerReserved = 0
			}
		}
		actual := target.estimatedTokens
		if response.Accounting != nil && response.Accounting.TotalTokens != nil && *response.Accounting.TotalTokens >= 0 {
			actual = *response.Accounting.TotalTokens
		}
		epoch.providerSpent += actual
		target.estimatedTokens = actual
		target.reservedTokens = 0
	}
	if err != nil {
		m.removeTargetLocked(epoch, job.key)
		m.enqueueReleaseLocked(target.controller, target.observation.Handle)
		m.metricLocked("control_error")
		m.deleteEmptyEpochLocked(job.aLeg, epoch)
		return
	}
	switch response.Result.Status {
	case promptcache.Renewed, promptcache.StillResident:
		m.rescheduleRenewedLocked(job, epoch, target, response)
	case promptcache.ColdRecreated:
		m.rescheduleColdRecreatedLocked(job, epoch, target, response)
	case promptcache.Stale, promptcache.Unsupported, promptcache.ControlFailed:
		m.removeTargetLocked(epoch, job.key)
		m.enqueueReleaseLocked(target.controller, target.observation.Handle)
		m.metricLocked(string(response.Result.Status))
	default:
		m.removeTargetLocked(epoch, job.key)
		m.enqueueReleaseLocked(target.controller, target.observation.Handle)
		m.metricLocked("control_error")
	}
	// A refresh-cap hit retires the epoch only once every already-admitted
	// refresh has completed: completing one target must never cancel a sibling
	// that is still in flight. The dispatch side enforces the same rule.
	if len(epoch.targets) == 0 || (epoch.refreshes >= m.cfg.MaxRefreshesPerIdleEpoch && !epochHasInFlight(epoch)) {
		m.invalidateLocked(job.aLeg, "exhausted")
	}
}

func (m *Manager) rescheduleRenewedLocked(job *renewJob, epoch *idleEpoch, target *targetState, response promptcache.RenewResponse) {
	if response.Result.Observation == nil {
		m.removeTargetLocked(epoch, job.key)
		m.enqueueReleaseLocked(target.controller, target.observation.Handle)
		m.metricLocked("no_schedule")
		m.deleteEmptyEpochLocked(job.aLeg, epoch)
		return
	}
	next := *response.Result.Observation
	due, ok, reason := m.scheduleLocked(next, target.backend, target.model, m.clock.Now(), epoch.revision)
	if !ok {
		m.removeTargetLocked(epoch, job.key)
		m.enqueueReleaseLocked(target.controller, target.observation.Handle)
		m.metricLocked(reason)
		m.deleteEmptyEpochLocked(job.aLeg, epoch)
		return
	}
	oldHandle := target.observation.Handle
	target.observation = next
	target.revision++
	target.dueAt = due
	heap.Push(&m.dueHeap, scheduleEntry{due: due, sequence: target.sequence, aLegID: job.aLeg, epoch: epoch.revision, targetKey: job.key, targetRev: target.revision})
	if string(oldHandle) != string(next.Handle) {
		m.enqueueReleaseLocked(target.controller, oldHandle)
	}
	m.metricLocked(string(response.Result.Status))
}

func (m *Manager) rescheduleColdRecreatedLocked(job *renewJob, epoch *idleEpoch, target *targetState, response promptcache.RenewResponse) {
	epoch.coldRecreates++
	if !m.cfg.ContinueAfterColdRecreate || epoch.coldRecreates > m.cfg.MaxColdRecreatesPerIdleEpoch || response.Result.Observation == nil {
		m.removeTargetLocked(epoch, job.key)
		m.enqueueReleaseLocked(target.controller, target.observation.Handle)
		m.metricLocked("cold_recreated")
		m.deleteEmptyEpochLocked(job.aLeg, epoch)
		return
	}
	next := *response.Result.Observation
	due, ok, reason := m.scheduleLocked(next, target.backend, target.model, m.clock.Now(), epoch.revision)
	if !ok {
		m.removeTargetLocked(epoch, job.key)
		m.enqueueReleaseLocked(target.controller, target.observation.Handle)
		m.metricLocked(reason)
		m.deleteEmptyEpochLocked(job.aLeg, epoch)
		return
	}
	target.observation = next
	target.revision++
	target.dueAt = due
	heap.Push(&m.dueHeap, scheduleEntry{due: due, sequence: target.sequence, aLegID: job.aLeg, epoch: epoch.revision, targetKey: job.key, targetRev: target.revision})
}

func (m *Manager) deleteEmptyEpochLocked(aLegID string, epoch *idleEpoch) {
	if epoch != nil && len(epoch.targets) == 0 && m.epochs[aLegID] == epoch {
		delete(m.epochs, aLegID)
	}
}

func (m *Manager) removeTargetLocked(epoch *idleEpoch, key string) *targetState {
	target := epoch.targets[key]
	if target == nil {
		return nil
	}
	delete(epoch.targets, key)
	if m.cfg.MaxProviderTokensPerIdleEpoch != nil && target.reservedTokens > 0 {
		epoch.providerReserved -= target.reservedTokens
		if epoch.providerReserved < 0 {
			epoch.providerReserved = 0
		}
		target.reservedTokens = 0
	}
	return target
}
