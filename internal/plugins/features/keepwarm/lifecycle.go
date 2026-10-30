package keepwarm

import (
	"context"
	"maps"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

type lifecycleState uint8

const (
	lifecycleRunning lifecycleState = iota
	lifecycleQuiescing
	lifecycleQuiesced
)

func (s lifecycleState) String() string {
	switch s {
	case lifecycleRunning:
		return "running"
	case lifecycleQuiescing:
		return "quiescing"
	case lifecycleQuiesced:
		return "quiesced"
	default:
		return "unknown"
	}
}

func (m *Manager) releaseObservationsLocked(observations []promptcache.Observation, controller promptcache.Controller) {
	for _, o := range observations {
		m.enqueueReleaseLocked(controller, o.Handle)
	}
}

func (m *Manager) enqueueReleaseLocked(controller promptcache.Controller, handle promptcache.Handle) {
	if controller == nil || len(handle) == 0 {
		return
	}
	job := releaseJob{controller: controller, handle: append(promptcache.Handle(nil), handle...)}
	if m.releaseClosed {
		// Late observations still release their local handles asynchronously
		// (pinned by TestManagerQuiesceRejectsLateArmAndWaitsForRelease). The
		// RPC is bounded by renew_timeout and the backend target store is itself
		// bounded, so an escaping best-effort release is harmless.
		go m.releaseOne(job)
		return
	}
	if !m.releaseStarted {
		m.releaseStarted = true
		m.releaseWG.Add(1)
		go m.releaseLoop()
	}
	m.releaseQueue = append(m.releaseQueue, job)
	select {
	case m.releaseWake <- struct{}{}:
	default:
	}
}

func (m *Manager) releaseLoop() {
	defer m.releaseWG.Done()
	for {
		m.mu.Lock()
		if len(m.releaseQueue) > 0 {
			job := m.releaseQueue[0]
			m.releaseQueue[0] = releaseJob{}
			m.releaseQueue = m.releaseQueue[1:]
			m.mu.Unlock()
			m.releaseOne(job)
			continue
		}
		if m.releaseClosed {
			m.mu.Unlock()
			return
		}
		wake := m.releaseWake
		m.mu.Unlock()
		<-wake
	}
}

func (m *Manager) releaseOne(job releaseJob) {
	ctx, cancel := context.WithTimeout(context.Background(), m.cfg.RenewTimeout)
	defer cancel()
	_ = job.controller.Release(ctx, promptcache.ReleaseRequest{Handle: job.handle})
}

func (m *Manager) metric(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metricLocked(name)
}

func (m *Manager) metricLocked(name string) {
	m.metrics[name]++
	if m.hooks.Metric != nil {
		m.hooks.Metric(name)
	}
}

type MetricsSnapshot struct {
	ActiveEpochs  int
	ActiveTargets int
	Events        map[string]uint64
}

func (m *Manager) Metrics() MetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	events := maps.Clone(m.metrics)
	targets := 0
	for _, epoch := range m.epochs {
		targets += len(epoch.targets)
	}
	return MetricsSnapshot{ActiveEpochs: len(m.epochs), ActiveTargets: targets, Events: events}
}

func (m *Manager) Quiesce(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	switch m.state {
	case lifecycleQuiesced:
		m.mu.Unlock()
		return nil
	case lifecycleQuiescing:
		done := m.quiesceDone
		m.mu.Unlock()
		return waitQuiesced(ctx, done)
	}
	m.state = lifecycleQuiescing
	m.quiesceDone = make(chan struct{})
	done := m.quiesceDone
	for a := range m.epochs {
		m.invalidateLocked(a, "quiesce")
	}
	if m.runCancel != nil {
		m.runCancel()
	}
	m.mu.Unlock()

	// Shutdown owns its own lifetime, not the first caller's request context.
	// This lets a timed-out caller return without leaving the generation in a
	// state where a later Close incorrectly observes an incomplete quiesce.
	go m.finishQuiesce(done)
	return waitQuiesced(ctx, done)
}

func waitQuiesced(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) finishQuiesce(done chan struct{}) {
	m.runWG.Wait()
	m.renewWG.Wait()

	m.mu.Lock()
	if !m.releaseClosed {
		m.releaseClosed = true
		select {
		case m.releaseWake <- struct{}{}:
		default:
		}
	}
	m.mu.Unlock()
	m.releaseWG.Wait()

	m.mu.Lock()
	m.state = lifecycleQuiesced
	close(done)
	m.mu.Unlock()
}

func (m *Manager) ActiveTargetCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.epochs {
		n += len(e.targets)
	}
	return n
}

func (m *Manager) ActiveEpochCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.epochs)
}

func (m *Manager) NextDue() (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out time.Time
	ok := false
	for _, e := range m.epochs {
		for _, t := range e.targets {
			if !ok || t.dueAt.Before(out) {
				out = t.dueAt
				ok = true
			}
		}
	}
	return out, ok
}
