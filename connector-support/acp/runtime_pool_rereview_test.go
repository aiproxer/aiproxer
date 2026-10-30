package acp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Source: PR #1323 re-review. Mutant: let an empty refused slot win eviction.
func TestCapacity_RecoversAfterRejectedConversation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{MaxRuntimes: 1, ClaimWait: time.Second})
		t.Cleanup(func() { _ = pool.Close() })
		a, b, c := RuntimeKey{ClientSession: "a"}, RuntimeKey{ClientSession: "b"}, RuntimeKey{ClientSession: "c"}
		old := &closeTrackingTransport{}
		pool.SetProcess(a, newFakeProcess(t), old, "a", "", "agent")
		pool.ClaimForTurn(t.Context(), a, "")
		pool.ClaimForTurn(t.Context(), b, "")
		strategy := SpawnHandshakeStrategy{
			Spawn: func() ([]string, Process, Transport, error) {
				return nil, newFakeProcess(t), &closeTrackingTransport{}, nil
			},
			Handshake: func(context.Context, Transport) (string, error) { return "new", nil },
		}
		if _, err := pool.EnsureProcess(t.Context(), b, strategy); err == nil {
			t.Fatal("B exceeded the active process limit")
		}
		pool.Release(b)
		time.Sleep(time.Nanosecond) // virtual ordering: B is strictly older than idle A
		pool.Release(a)
		pool.ClaimForTurn(t.Context(), c, "")
		res, err := pool.EnsureProcess(t.Context(), c, strategy)
		if err != nil || res.SessionID != "new" || old.closeCount() != 1 {
			t.Fatalf("C did not reclaim A after B's refusal: result=%+v error=%v retired=%d", res, err, old.closeCount())
		}
	})
}

// Source: re-review idle-reaper race. Hold the existing stale-timer lock to
// pause the reaper after its eligibility check. A claim must either precede
// that atomic reservation (and keep its process) or observe retirement.
func TestIdleReap_ReservationPrecedesTheUnlockedCleanup(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{IdleTimeout: time.Second})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "idle"}
	transport := &closeTrackingTransport{}
	pool.SetProcess(key, newFakeProcess(t), transport, "session", "", "agent")
	rt := pool.Get(key)
	rt.mu.Lock()
	rt.lastActivity = time.Now().Add(-time.Hour)
	rt.historyState.messageCount = 1
	rt.mu.Unlock()
	pool.staleKillMu.Lock()
	locked := true
	defer func() {
		if locked {
			pool.staleKillMu.Unlock()
		}
	}()
	finished := make(chan struct{})
	go func() { pool.tryReapIdle(key, rt); close(finished) }()
	waitFor(t, 0, 0, "idle eligibility evaluated", func() bool {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		return rt.historyState.messageCount == 0
	})
	// tryClaimForTurn would cancel the same stale timer on success. Claim
	// directly under the slot lock to force exactly that admission boundary.
	rt.mu.Lock()
	claimed := rt.retiring == nil
	if claimed {
		rt.inUse = true
	}
	rt.mu.Unlock()
	pool.staleKillMu.Unlock()
	locked = false
	<-finished
	if claimed && (!rt.HasProcess() || transport.closeCount() != 0) {
		t.Fatal("reaper detached the incarnation claimed after its eligibility check")
	}
}

// staleSkipSignal is a slog handler that reports the first stale-kill skip
// with the given reason. It gives a test a deterministic signal for a
// background decision instead of a timing window.
type staleSkipSignal struct {
	reason string
	fired  chan struct{}
	once   sync.Once
}

func newStaleSkipSignal(reason string) *staleSkipSignal {
	return &staleSkipSignal{reason: reason, fired: make(chan struct{})}
}

func (s *staleSkipSignal) Enabled(context.Context, slog.Level) bool { return true }

func (s *staleSkipSignal) Handle(_ context.Context, record slog.Record) error {
	reason := ""
	record.Attrs(func(a slog.Attr) bool {
		if a.Key == "reason" {
			reason = a.Value.String()
		}
		return true
	})
	if record.Message == "acp: stale kill skipped" && reason == s.reason {
		s.once.Do(func() { close(s.fired) })
	}
	return nil
}

func (s *staleSkipSignal) WithAttrs([]slog.Attr) slog.Handler { return s }

func (s *staleSkipSignal) WithGroup(string) slog.Handler { return s }

// Source: #1336 (found in the #1323 re-review). The stale timer must reserve
// the incarnation it retires under the runtime lock. Take the stale-timer lock
// and fire the timer: the callback waits for that lock, so it is paused at a
// known point and the successor claim below lands exactly there. The timer
// must already own the incarnation at that point, so the claim can never
// receive the retired process.
func TestStaleKill_ReservationPrecedesTheSuccessorClaim(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{
		// The timer is fired explicitly below, so its own delay only has to
		// keep it from firing before the test takes the stale-timer lock.
		StaleKillDelay: time.Hour,
		ClaimWait:      5 * time.Second,
	})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "stale"}
	transport := &closeTrackingTransport{}
	pool.SetProcess(key, newFakeProcess(t), transport, "session", "", "agent")
	pool.Release(key) // arms the stale timer

	// Pause the stale timer at its boundary: the callback waits for the
	// stale-timer lock after it reserved the incarnation.
	pool.staleKillMu.Lock()
	locked := true
	defer func() {
		if locked {
			pool.staleKillMu.Unlock()
		}
	}()
	armed, ok := pool.staleKillTimers[key]
	if !ok {
		t.Fatal("the release did not arm the stale timer")
	}
	armed.timer.Reset(0) // fire the timer now; the callback blocks on the lock above

	rt := pool.Get(key)
	waitFor(t, 0, 0, "the stale timer reserves the incarnation it retires before it releases the slot", func() bool {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		return rt.retiring != nil
	})

	// The successor claims the slot across the timer's boundary: it either
	// waits for the retirement in flight or it claims the slot the timer
	// already emptied. It must never receive the retired incarnation.
	claimed := make(chan bool, 1)
	go func() {
		_, busy := pool.ClaimForTurn(t.Context(), key, "")
		claimed <- busy
	}()
	pool.staleKillMu.Unlock()
	locked = false

	// The claim resolves once the retirement completes.
	if busy := <-claimed; busy {
		t.Fatal("the successor claim was refused after the stale retirement completed")
	}
	if rt.HasProcess() {
		t.Fatal("the successor claim received the incarnation the stale timer retired")
	}
	if got := transport.closeCount(); got != 1 {
		t.Fatalf("stale transport close count = %d, want the retired incarnation only", got)
	}
	if err := rt.cleanupOutcome(); err != nil {
		t.Fatalf("the stale retirement contaminated the slot: %v", err)
	}
}

// Source: #1336. The other schedule: the successor claim wins the slot first,
// so the stale timer must observe the claim and retire nothing.
func TestStaleKill_DoesNotRetireAClaimedRuntime(t *testing.T) {
	signal := newStaleSkipSignal("runtime in use")
	pool := NewRuntimePool(RuntimePoolConfig{
		StaleKillDelay: 20 * time.Millisecond,
		Log:            slog.New(signal),
	})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "claimed"}
	transport := &closeTrackingTransport{}
	pool.SetProcess(key, newFakeProcess(t), transport, "session", "", "agent")
	if _, busy := pool.ClaimForTurn(t.Context(), key, ""); busy {
		t.Fatal("the successor claim must succeed")
	}

	// The claim cancels an armed timer, so the test arms one afterwards: the
	// stale path then runs against the claimed slot.
	claimed := pool.Get(key)
	claimed.mu.Lock()
	token := claimed.staleEpoch
	claimed.mu.Unlock()
	pool.scheduleStaleKill(key, claimed, token)

	select {
	case <-signal.fired:
	case <-time.After(2 * time.Second):
		if rt := pool.Get(key); rt != nil && !rt.HasProcess() {
			t.Fatal("the stale timer retired the incarnation a successor claim holds")
		}
		t.Fatal("the stale timer did not report that it skipped the claimed runtime")
	}

	rt := pool.Get(key)
	if rt == nil {
		t.Fatal("the claimed runtime left the pool")
	}
	if !rt.HasProcess() {
		t.Fatal("the stale timer retired the incarnation a successor claim holds")
	}
	if got := transport.closeCount(); got != 0 {
		t.Fatalf("the stale timer closed the claimed runtime's transport: close count = %d", got)
	}
	if err := rt.cleanupOutcome(); err != nil {
		t.Fatalf("the stale timer contaminated the claimed slot: %v", err)
	}
}

// Source: #1336 (review). The transcript marker is process-bound: the stale
// timer destroys the process, so the marker must die with it. Otherwise the
// successor process receives an incremental tail and loses the earlier
// conversation context.
func TestStaleKill_RetirementInvalidatesTheTranscriptMarker(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{StaleKillDelay: 20 * time.Millisecond})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "stale"}
	pool.SetProcess(key, newFakeProcess(t), &closeTrackingTransport{}, "session", "", "agent")

	// A completed turn commits the transcript marker of the live process.
	messages := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("first requirement")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("first answer")}},
	}
	rt := pool.Get(key)
	rt.SetHistoryState(newHistoryState(messages))
	pool.Release(key)

	waitFor(t, 0, 0, "the stale timer retires the process", func() bool {
		return !rt.HasProcess()
	})

	if state := rt.HistoryState(); state != (historyState{}) {
		t.Fatalf("history state = %+v, want the marker of the retired process dropped", state)
	}
	result := (&TranscriptHistoryCoordinator{}).ComputeHistoryAndUserMessage(messages, rt.HistoryState())
	if !strings.Contains(result.UserMessage, "first requirement") {
		t.Fatalf("the successor transcript lost the earlier context: %q", result.UserMessage)
	}
}

// Source: #1336 (review). An expired timer belongs to the turn that armed it.
// After a successor claimed and released the runtime, the expired callback
// must neither retire the successor's process nor cancel the successor's
// timer.
func TestStaleKill_ExpiredTimerDoesNotRetireTheSuccessor(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{
		StaleKillDelay: time.Hour,
		ClaimWait:      5 * time.Second,
	})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "stale"}
	pool.SetProcess(key, newFakeProcess(t), &closeTrackingTransport{}, "session", "", "agent")
	pool.Release(key) // arms the first timer

	rt := pool.Get(key)
	rt.mu.Lock()
	expiredEpoch := rt.staleEpoch
	rt.mu.Unlock()

	// The successor claims the slot, publishes its own process and releases.
	// The claim cancels the expired timer, and the release arms a new one.
	if _, busy := pool.ClaimForTurn(t.Context(), key, ""); busy {
		t.Fatal("the successor claim must succeed")
	}
	pool.DetachRuntime(key)
	successor := &closeTrackingTransport{}
	pool.SetProcess(key, newFakeProcess(t), successor, "session-2", "", "agent")
	pool.Release(key)

	// The expired callback resumes after the successor's turn.
	pool.retireStaleRuntime(key, rt, expiredEpoch)

	if !rt.HasProcess() {
		t.Fatal("the expired timer retired the successor's process")
	}
	if got := successor.closeCount(); got != 0 {
		t.Fatalf("the expired timer closed the successor's transport: close count = %d", got)
	}
	pool.staleKillMu.Lock()
	armed, ok := pool.staleKillTimers[key]
	pool.staleKillMu.Unlock()
	if !ok {
		t.Fatal("the expired timer cancelled the successor's timer")
	}
	if armed.epoch == expiredEpoch {
		t.Fatal("the successor's timer kept the expired timer's epoch")
	}
}

// Source: #1336 (review). A claim supersedes the runtime's stale-kill timer.
// The claim bumps the runtime's epoch, so an expired callback that resumes
// after the claim — while the successor's turn is still running — owns
// nothing and retires nothing.
func TestStaleKill_ClaimSupersedesTheArmedTimer(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{
		StaleKillDelay: time.Hour,
		ClaimWait:      5 * time.Second,
	})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "stale"}
	transport := &closeTrackingTransport{}
	pool.SetProcess(key, newFakeProcess(t), transport, "session", "", "agent")
	pool.Release(key) // arms the timer

	rt := pool.Get(key)
	rt.mu.Lock()
	armedEpoch := rt.staleEpoch
	rt.mu.Unlock()

	// The successor claims the slot: the claim supersedes the armed timer.
	if _, busy := pool.ClaimForTurn(t.Context(), key, ""); busy {
		t.Fatal("the successor claim must succeed")
	}
	rt.mu.Lock()
	claimedEpoch := rt.staleEpoch
	rt.mu.Unlock()
	if claimedEpoch == armedEpoch {
		t.Fatal("the claim did not supersede the armed timer's epoch")
	}

	// The expired callback resumes while the successor's turn still runs.
	pool.retireStaleRuntime(key, rt, armedEpoch)

	if !rt.HasProcess() {
		t.Fatal("the expired timer retired the claimed runtime's process")
	}
	if got := transport.closeCount(); got != 0 {
		t.Fatalf("the expired timer closed the claimed runtime's transport: close count = %d", got)
	}
}

// Source: #1336 (review). A retirement in flight is reported as such: the
// detached process leaves the slot without a process, so the reason must not
// be reported as a missing process.
func TestStaleKill_ReportsRetirementInFlight(t *testing.T) {
	signal := newStaleSkipSignal("retirement in flight")
	pool := NewRuntimePool(RuntimePoolConfig{
		StaleKillDelay: time.Hour,
		Log:            slog.New(signal),
	})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "stale"}
	transport := newBlockedTransport()
	// The blocked retirement must be released even when the test fails, or
	// the pool shutdown would wait for it forever.
	release := sync.OnceFunc(transport.unblock)
	t.Cleanup(release)
	pool.SetProcess(key, newFakeProcess(t), transport, "session", "", "agent")
	pool.Release(key) // arms the stale timer

	// A retirement that cannot complete is in flight: the slot holds no
	// process, and the retirement has not finished.
	pool.DetachRuntime(key)
	<-transport.entered

	pool.staleKillMu.Lock()
	armed, ok := pool.staleKillTimers[key]
	pool.staleKillMu.Unlock()
	if !ok {
		t.Fatal("the release did not arm the stale timer")
	}
	armed.timer.Reset(0)

	select {
	case <-signal.fired:
	case <-time.After(2 * time.Second):
		t.Fatal("the stale timer did not report the retirement in flight")
	}
	release()
}

// Source: #1336 (review). The reservation is one operation: the epoch check,
// the eligibility checks, the detachment and the retiring mark share one
// runtime-lock hold, so no caller can split the decision from the detachment.
func TestReserveStaleRetirement_RejectsASupersededEpoch(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{})
	rt := &SubprocessRuntime{}
	rt.staleEpoch = 7
	rt.proc = newFakeProcess(t)
	rt.initialized = true

	if _, _, reason := pool.reserveStaleRetirement(rt, 6); reason == "" {
		t.Fatal("the reservation accepted a superseded epoch")
	}
	if !rt.HasProcess() {
		t.Fatal("the superseded reservation took the process")
	}
	detached, done, reason := pool.reserveStaleRetirement(rt, 7)
	if reason != "" || detached.proc == nil || done == nil {
		t.Fatalf("the reservation rejected the current epoch: %q", reason)
	}
	if rt.HasProcess() {
		t.Fatal("the reservation kept the process in the slot")
	}
}

// Source: #1336 (review). A slot whose cleanup was not confirmed keeps its
// process tree: the retirement that failed owns it, so the stale timer must
// not detach it again.
func TestReserveStaleRetirement_LeavesAQuarantinedSlotAlone(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{})
	rt := &SubprocessRuntime{}
	rt.staleEpoch = 1
	rt.proc = newFakeProcess(t)
	rt.initialized = true
	rt.quarantined = true

	_, _, reason := pool.reserveStaleRetirement(rt, 1)
	if reason != "runtime quarantined" {
		t.Fatalf("reservation reason = %q, want the quarantined slot", reason)
	}
	if !rt.HasProcess() {
		t.Fatal("the reservation detached the quarantined slot's process")
	}
}

// Source: #1336 (review). A release that a later claim superseded must not
// arm a timer: such a timer would retire the process the claim owns.
func TestStaleKill_ObsoleteReleaseDoesNotArmATimer(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{StaleKillDelay: time.Hour})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "stale"}
	transport := &closeTrackingTransport{}
	pool.SetProcess(key, newFakeProcess(t), transport, "session", "", "agent")
	pool.Release(key) // arms the first timer

	rt := pool.Get(key)
	rt.mu.Lock()
	obsolete := rt.staleEpoch
	rt.mu.Unlock()

	// The successor claims the slot: the epoch moves on, and the claim
	// cancels the armed timer.
	if _, busy := pool.ClaimForTurn(t.Context(), key, ""); busy {
		t.Fatal("the successor claim must succeed")
	}

	// The obsolete release resumes and arms its timer.
	pool.scheduleStaleKill(key, rt, obsolete)

	// No timer may be armed for the claimed runtime: the obsolete release
	// does not own it.
	pool.staleKillMu.Lock()
	_, armed := pool.staleKillTimers[key]
	pool.staleKillMu.Unlock()
	if armed {
		t.Fatal("the obsolete release armed a timer for the claimed runtime")
	}
	if !rt.HasProcess() {
		t.Fatal("the claimed runtime lost its process")
	}
}

// Source: #1336 (review). Two runtimes can share a key and a numeric epoch.
// An expired callback of the replaced runtime must not remove the
// replacement's timer.
func TestStaleKill_ReplacedRuntimeKeepsItsTimer(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{StaleKillDelay: time.Hour})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "stale"}
	pool.SetProcess(key, newFakeProcess(t), &closeTrackingTransport{}, "session", "", "agent")
	pool.Release(key) // arms the first timer

	armed := pool.Get(key)
	armed.mu.Lock()
	epoch := armed.staleEpoch
	armed.mu.Unlock()

	// The pool replaces the slot, and the replacement arms a timer that
	// carries the same epoch as the replaced runtime's timer.
	replacement := &SubprocessRuntime{key: key}
	replacement.staleEpoch = epoch
	pool.mu.Lock()
	pool.pools[key] = replacement
	pool.mu.Unlock()
	pool.staleKillMu.Lock()
	pool.staleKillTimers[key] = staleKillTimer{
		epoch: epoch,
		rt:    replacement,
		timer: time.NewTimer(time.Hour),
	}
	pool.staleKillMu.Unlock()

	// The replaced runtime's expired callback resumes.
	pool.retireStaleRuntime(key, armed, epoch)

	pool.staleKillMu.Lock()
	entry, ok := pool.staleKillTimers[key]
	pool.staleKillMu.Unlock()
	if !ok || entry.rt != replacement {
		t.Fatal("the expired callback removed the replacement runtime's timer")
	}
}

// Source: #1336 (review). The instruction record is process-bound. A retired
// process's authoritative instructions must not survive on the slot, or the
// successor process would be told it already received instructions it never
// saw.
func TestRetirement_ClearsTheSessionInstructions(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "instructions"}
	pool.SetProcess(key, newFakeProcess(t), &closeTrackingTransport{}, "session", "", "agent")
	rt := pool.Get(key)
	rt.RotateSession("session", "steer this")
	if got := rt.SessionInstructions(); got != "steer this" {
		t.Fatalf("session instructions = %q, want the installed instructions", got)
	}

	if err := pool.KillRuntime(key); err != nil {
		t.Fatalf("retire: %v", err)
	}
	if got := rt.SessionInstructions(); got != "" {
		t.Fatalf("session instructions = %q after the retirement, want empty", got)
	}

	// The slot remains reusable: the next process starts with no instructions.
	pool.SetProcess(key, newFakeProcess(t), &closeTrackingTransport{}, "session", "", "agent")
	if !pool.Get(key).HasProcess() {
		t.Fatal("the slot is not reusable after a confirmed retirement")
	}
	if got := pool.Get(key).SessionInstructions(); got != "" {
		t.Fatalf("the replacement process inherited session instructions %q", got)
	}
}

// Source: #1336 (review). A runtime that cannot be retired when the timer
// fires gets no timer: the arming would only produce a skip.
func TestStaleKill_UnretirableRuntimeGetsNoTimer(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{StaleKillDelay: time.Hour})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "stale"}

	// The slot exists, but it has no live process to retire.
	rt := &SubprocessRuntime{key: key}
	pool.mu.Lock()
	pool.pools[key] = rt
	pool.mu.Unlock()

	rt.mu.Lock()
	token := rt.staleEpoch
	rt.mu.Unlock()
	pool.scheduleStaleKill(key, rt, token)

	pool.staleKillMu.Lock()
	_, armed := pool.staleKillTimers[key]
	pool.staleKillMu.Unlock()
	if armed {
		t.Fatal("the pool armed a timer for a runtime that cannot be retired")
	}
}

// Source: #1336 (review). The scheduling transaction is atomic: a scheduler
// that published its timer outside the transaction would cancel the successor's
// timer and replace it with its own, leaving the successor's process without a
// stale-kill timer. The transaction keeps the successor's claim and release out
// until the timer is published, and the successor's timer stays authoritative.
func TestStaleKill_ObsoleteSchedulerKeepsTheSuccessorTimer(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{StaleKillDelay: time.Hour})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "stale"}
	pool.SetProcess(key, newFakeProcess(t), &closeTrackingTransport{}, "session", "", "agent")
	rt := pool.Get(key)
	rt.mu.Lock()
	token := rt.staleEpoch
	rt.mu.Unlock()

	// The scheduler's transaction pauses between its ownership decision and its
	// timer publication. The seam asserts the transaction owns both locks: a
	// claim can neither run nor publish a timer between the decision and the
	// publication that follows.
	reached := make(chan struct{})
	var paused atomic.Bool
	pool.staleKillScheduleBoundary = func() {
		if !paused.CompareAndSwap(false, true) {
			return
		}
		if pool.mu.TryLock() {
			pool.mu.Unlock()
			t.Error("the scheduling transaction does not hold the pool lock")
		}
		if rt.mu.TryLock() {
			rt.mu.Unlock()
			t.Error("the scheduling transaction does not hold the runtime lock")
		}
		close(reached)
	}
	scheduled := make(chan struct{})
	go func() {
		defer close(scheduled)
		pool.scheduleStaleKill(key, rt, token)
	}()

	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("the scheduler did not reach its publication boundary")
	}

	// The successor's turn runs against the paused scheduler. It cannot proceed
	// while the transaction owns the slot, so it completes after the
	// publication, and its own release arms the authoritative timer.
	if _, busy := pool.ClaimForTurn(t.Context(), key, ""); busy {
		t.Fatal("the successor claim must succeed")
	}
	pool.Release(key)
	<-scheduled

	// The successor's timer must be the authoritative one.
	rt.mu.Lock()
	epoch := rt.staleEpoch
	rt.mu.Unlock()
	pool.staleKillMu.Lock()
	armed, ok := pool.staleKillTimers[key]
	pool.staleKillMu.Unlock()
	if !ok {
		t.Fatal("the successor's timer is not armed")
	}
	if armed.epoch != epoch {
		t.Fatalf("the successor's timer is not authoritative: armed generation = %d, want %d", armed.epoch, epoch)
	}
	if armed.rt != rt {
		t.Fatal("the successor's timer belongs to another runtime")
	}
}

// Source: #1336 (review). Two runtimes can share a key and a numeric
// generation. A release that a replaced slot superseded owns nothing: it must
// not schedule a timer on the replacement, and it must not touch the
// replacement's own timer.
func TestStaleKill_ObsoleteRuntimeDoesNotScheduleOnItsReplacement(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{StaleKillDelay: time.Hour})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "stale"}
	pool.SetProcess(key, newFakeProcess(t), &closeTrackingTransport{}, "session", "", "agent")
	pool.Release(key) // arms the first timer

	obsolete := pool.Get(key)
	obsolete.mu.Lock()
	generation := obsolete.staleEpoch
	obsolete.mu.Unlock()
	pool.staleKillMu.Lock()
	original, ok := pool.staleKillTimers[key]
	pool.staleKillMu.Unlock()
	if !ok {
		t.Fatal("the release armed no timer")
	}

	// The pool serves a replacement runtime under the key. It carries the same
	// numeric generation, so the generation alone cannot tell the two apart.
	replacement := &SubprocessRuntime{key: key}
	replacement.staleEpoch = generation
	replacement.proc = newFakeProcess(t)
	pool.mu.Lock()
	pool.pools[key] = replacement
	pool.mu.Unlock()

	// The obsolete release resumes and schedules its timer.
	pool.scheduleStaleKill(key, obsolete, generation)

	// The replacement owns the slot: the obsolete release schedules nothing,
	// and the timer that was already armed stays armed.
	pool.staleKillMu.Lock()
	armed, ok := pool.staleKillTimers[key]
	pool.staleKillMu.Unlock()
	if !ok {
		t.Fatal("the obsolete release removed the armed timer")
	}
	if armed.timer != original.timer || armed.epoch != original.epoch {
		t.Fatal("the obsolete release scheduled a timer on the replacement runtime")
	}
}

// Source: #1336 (review). End-to-end: a process that received authoritative
// instructions is retired, the replacement starts without them, and a later
// turn requests them again. The replacement must receive them through a new
// authoritative session, not have them silently stripped from the prompt.
func TestOpen_RetiredProcessInstructionsAreRedelivered(t *testing.T) {
	proto := &sysPromptTurnProtocol{}
	proto.model = "agent"
	backend := NewSubprocessBackend(SubprocessBackendConfig{
		Protocol:       proto,
		Workspace:      WorkspacePolicy{DefaultDir: os.TempDir()},
		Pool:           RuntimePoolConfig{},
		ProcessStarter: &fakeStarter{proc: newFakeProcess(t)},
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = backend.Close() })

	// Process A receives the instructions at creation.
	openTurn(t, backend, "sp-retire", []string{"steer this"})

	// The process is retired: the slot survives, its process-bound state does
	// not.
	engine, ok := backend.(*subprocessBackend)
	if !ok {
		t.Fatalf("unexpected subprocess engine %T", backend)
	}
	pool := engine.pool
	pool.mu.Lock()
	var key RuntimeKey
	for k := range pool.pools {
		key = k
	}
	pool.mu.Unlock()
	if err := pool.KillRuntime(key); err != nil {
		t.Fatalf("retire process A: %v", err)
	}

	// Process B starts without instructions.
	openTurn(t, backend, "sp-retire", nil)

	// The same instructions are requested again: they must be delivered to
	// process B through a fresh authoritative session.
	openTurn(t, backend, "sp-retire", []string{"steer this"})

	proto.mu.Lock()
	defer proto.mu.Unlock()
	if len(proto.created) != 1 || proto.created[0] != "steer this" {
		t.Fatalf("only the first process is created with instructions, got %v", proto.created)
	}
	if len(proto.rotated) != 1 || proto.rotated[0] != "steer this" {
		t.Fatalf("the replacement must receive the instructions through a rotated session, got %v", proto.rotated)
	}
	last := proto.sentCalls[len(proto.sentCalls)-1]
	if len(last.Instructions) != 0 {
		t.Fatal("instructions delivered through rotation must be stripped from the prompt")
	}
}

// Source: #1336 (review). The reservation and the successor claim meet at one
// boundary: the seam pauses the reservation between its decision and its
// detachment, and the claim that lands there must not win the slot.
func TestStaleKill_SuccessorClaimAtTheReservationBoundary(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{ClaimWait: 5 * time.Second})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "stale"}
	// The retirement cannot complete while the test holds it, so the retiring
	// mark stays set and the claim outcome does not depend on scheduling.
	transport := newBlockedTransport()
	release := sync.OnceFunc(transport.unblock)
	t.Cleanup(release)
	pool.SetProcess(key, newFakeProcess(t), transport, "session", "", "agent")
	rt := pool.Get(key)
	rt.mu.Lock()
	epoch := rt.staleEpoch
	rt.mu.Unlock()

	// The seam pauses the reservation between the decision and the detachment.
	reached := make(chan struct{})
	proceed := make(chan struct{})
	pool.staleKillBoundary = func() {
		close(reached)
		<-proceed
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		pool.retireStaleRuntime(key, rt, epoch)
	}()
	<-reached

	// The successor claims the slot at the boundary. It cannot win the slot
	// while the reservation holds the runtime lock.
	claimed := make(chan claimOutcome, 1)
	go func() {
		_, outcome := pool.tryClaimForTurn(key, "")
		claimed <- outcome
	}()
	close(proceed)
	<-done

	if outcome := <-claimed; outcome == claimAcquired {
		t.Fatal("the successor claim won the slot at the reservation boundary")
	}
	// The reservation retired exactly the incarnation it captured.
	select {
	case <-transport.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the reservation did not retire the incarnation it captured")
	}
}

// Source: #1336 (review). A timer that belongs to a slot the pool replaced
// retires nothing: the callback checks the armed runtime before it acts.
func TestStaleKill_ReplacedSlotIsNotRetired(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{StaleKillDelay: time.Hour})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "stale"}
	transport := &closeTrackingTransport{}
	pool.SetProcess(key, newFakeProcess(t), transport, "session", "", "agent")
	pool.Release(key)

	armed := pool.Get(key)
	armed.mu.Lock()
	epoch := armed.staleEpoch
	armed.mu.Unlock()

	// The pool replaces the slot under the same key, and the replacement
	// carries a process and the same epoch.
	replacement := &SubprocessRuntime{key: key}
	replacement.staleEpoch = epoch
	replacement.proc = newFakeProcess(t)
	replacement.initialized = true
	pool.mu.Lock()
	pool.pools[key] = replacement
	pool.mu.Unlock()

	// The expired callback resumes: it belongs to the replaced slot.
	pool.retireStaleRuntime(key, armed, epoch)

	if !replacement.HasProcess() {
		t.Fatal("the expired timer retired the replacement slot's process")
	}
	if got := transport.closeCount(); got != 0 {
		t.Fatalf("the expired timer closed the replaced slot's transport: close count = %d", got)
	}
}

// Source: #1336 (review). The reservation and the successor claim race for
// the slot. A claim that returns must keep its process: the stale timer must
// never retire a process after the claim returned. The race is repeated so a
// reservation that split its decision from its detachment cannot pass
// unnoticed.
func TestStaleKill_ClaimNeverLosesItsProcessToTheTimer(t *testing.T) {
	const iterations = 1000
	for i := range iterations {
		pool := NewRuntimePool(RuntimePoolConfig{ClaimWait: 5 * time.Second})
		key := RuntimeKey{ClientSession: "race"}
		transport := &closeTrackingTransport{}
		pool.SetProcess(key, newFakeProcess(t), transport, "session", "", "agent")
		rt := pool.Get(key)
		rt.mu.Lock()
		epoch := rt.staleEpoch
		rt.mu.Unlock()

		// The reservation runs beside the claim, so a claim that lands between
		// the reservation's decision and its detachment must still keep its
		// process. The close count is read as soon as the claim returns: a
		// retirement that closes the transport afterwards is the defect this
		// guards.
		start := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			<-start
			pool.retireStaleRuntime(key, rt, epoch)
		}()
		close(start)
		pool.ClaimForTurn(t.Context(), key, "")
		closedAtClaim := transport.closeCount()
		<-done

		// A retirement that started owns the process tree: the pool joins it
		// before the next iteration.
		rt.mu.Lock()
		pending := rt.retiring
		rt.mu.Unlock()
		if pending != nil {
			<-pending
		}
		if transport.closeCount() > closedAtClaim {
			t.Fatalf("iteration %d: the stale timer retired the process the successor claim owned", i)
		}
	}
}

// Source: #1336. A stale retirement that does not confirm process extinction
// must keep its slot quarantined with the cleanup evidence.
func TestStaleKill_UnconfirmedCleanupKeepsTheSlot(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{StaleKillDelay: 20 * time.Millisecond})
	t.Cleanup(func() { _ = pool.Close() })
	key := RuntimeKey{ClientSession: "stale"}
	proc := newFakeProcess(t)
	proc.waitErr = ErrCleanupIncomplete
	pool.SetProcess(key, proc, nil, "session", "", "agent")
	pool.Release(key)

	waitFor(t, 0, 0, "the stale retirement is recorded", func() bool {
		rt := pool.Get(key)
		if rt == nil {
			return false
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		return rt.quarantined
	})
	rt := pool.Get(key)
	if rt == nil {
		t.Fatal("the quarantined runtime lost its pool entry")
	}
	if !errors.Is(rt.cleanupOutcome(), ErrCleanupIncomplete) {
		t.Fatal("the quarantined runtime lost its cleanup evidence")
	}
	rt.mu.Lock()
	failed := rt.failedRetirement
	rt.mu.Unlock()
	if failed == nil || failed.proc != proc {
		t.Fatal("the quarantined runtime lost the process handle of the failed retirement")
	}
	if err := pool.Close(); !errors.Is(err, ErrCleanupIncomplete) {
		t.Fatalf("shutdown lost cleanup evidence: %v", err)
	}
}

// Source: capacity includes a registered child's entire handshake, not just
// initialized/reusable processes. Failed cleanup must keep that capacity too.
func TestCapacity_HandshakeAndUnconfirmedCleanupConsumeCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{MaxRuntimes: 1, ClaimWait: time.Second})
		a, b := RuntimeKey{ClientSession: "handshaking"}, RuntimeKey{ClientSession: "next"}
		entered, finish := make(chan struct{}), make(chan struct{})
		result := make(chan error, 1)
		proc := newFakeProcess(t)
		proc.waitErr = ErrCleanupIncomplete
		go func() {
			_, err := pool.EnsureProcess(t.Context(), a, SpawnHandshakeStrategy{
				Spawn: func() ([]string, Process, Transport, error) { return nil, proc, nil, nil },
				Handshake: func(context.Context, Transport) (string, error) {
					close(entered)
					<-finish
					return "", errors.New("handshake failed")
				},
			})
			result <- err
		}()
		<-entered
		starts := 0
		tryNext := func() error {
			_, err := pool.EnsureProcess(t.Context(), b, SpawnHandshakeStrategy{
				Spawn: func() ([]string, Process, Transport, error) {
					starts++
					return nil, nil, nil, errors.New("unexpected spawn")
				},
			})
			return err
		}
		if err := tryNext(); err == nil || starts != 0 {
			t.Fatal("handshake did not consume capacity")
		}
		close(finish)
		if err := <-result; !errors.Is(err, ErrCleanupIncomplete) {
			t.Fatalf("cleanup evidence lost: %v", err)
		}
		if err := tryNext(); err == nil || starts != 0 {
			t.Fatal("unconfirmed cleanup released capacity")
		}
		if err := pool.Close(); !errors.Is(err, ErrCleanupIncomplete) {
			t.Fatalf("shutdown lost cleanup evidence: %v", err)
		}
	})
}

// Source: callers must receive cancellation, not a runtime-conflict error.
func TestOpen_ClaimCancellationPreservesContextError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := NewSubprocessBackend(SubprocessBackendConfig{
			Protocol:       &fakeTurnProtocol{model: "agent"},
			Workspace:      WorkspacePolicy{DefaultDir: os.TempDir()},
			Pool:           RuntimePoolConfig{ClaimWait: time.Second},
			ProcessStarter: &fakeStarter{proc: newFakeProcess(t)},
		})
		t.Cleanup(func() { _ = backend.Close() })
		first, err := backend.Open(t.Context(), newTurnCall("session"))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() { _, err := backend.Open(ctx, newTurnCall("session")); result <- err }()
		synctest.Wait()
		cancel()
		_ = first.Close()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation became conflict: %v", err)
		}
	})
}

// Source: hard max_runtimes contract. Mutant: check capacity without reserving it.
func TestCapacity_ConcurrentColdStartsHaveOneSpawnAuthorization(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{MaxRuntimes: 1, ClaimWait: time.Second})
		t.Cleanup(func() { _ = pool.Close() })
		entered := make(chan struct{}, 2)
		unblock := make(chan struct{})
		strategy := SpawnHandshakeStrategy{
			Spawn: func() ([]string, Process, Transport, error) {
				entered <- struct{}{}
				<-unblock
				return nil, newFakeProcess(t), &closeTrackingTransport{}, nil
			},
			Handshake: func(context.Context, Transport) (string, error) { return "session", nil },
		}
		results := make(chan error, 2)
		for _, id := range []string{"a", "b"} {
			key := RuntimeKey{ClientSession: id}
			pool.ClaimForTurn(t.Context(), key, "")
			go func() { _, err := pool.EnsureProcess(t.Context(), key, strategy); results <- err }()
		}
		synctest.Wait()
		starts := len(entered)
		close(unblock)
		first, second := <-results, <-results
		if starts != 1 || (first == nil) == (second == nil) {
			t.Fatalf("spawn authorizations=%d errors=(%v, %v), want one spawn and one refusal", starts, first, second)
		}
	})
}

// Zero-valued pool settings preserve legacy, unbounded cross-key admission.
func TestCapacity_UnboundedPoolDoesNotSerializeColdStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{})
		t.Cleanup(func() { _ = pool.Close() })
		entered := make(chan struct{}, 2)
		unblock := make(chan struct{})
		result := make(chan error, 2)
		for _, id := range []string{"a", "b"} {
			go func() {
				_, err := pool.EnsureProcess(t.Context(), RuntimeKey{ClientSession: id}, SpawnHandshakeStrategy{
					Spawn: func() ([]string, Process, Transport, error) {
						entered <- struct{}{}
						<-unblock
						return nil, newFakeProcess(t), &closeTrackingTransport{}, nil
					},
					Handshake: func(context.Context, Transport) (string, error) { return "session", nil },
				})
				result <- err
			}()
		}
		synctest.Wait()
		starts := len(entered)
		close(unblock)
		first, second := <-result, <-result
		if starts != 2 || first != nil || second != nil {
			t.Fatalf("unbounded cold starts=%d errors=(%v, %v)", starts, first, second)
		}
	})
}

// Source: caller cancellation must never become runtime-conflict or ownership.
func TestClaim_AlreadyCanceledDoesNotAcquire(t *testing.T) {
	pool := NewRuntimePool(RuntimePoolConfig{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	key := RuntimeKey{ClientSession: "cancelled"}
	_, busy := pool.ClaimForTurn(ctx, key, "")
	if !busy {
		pool.Release(key)
		t.Fatal("already-cancelled request acquired a claim")
	}
}

// Source: canceled publication cannot contaminate a healthy slot. Mutant:
// synchronously close the refused child, then store ctx.Err as sticky cleanup.
func TestPublication_CancellationRetainsCleanupWithoutPoisoningSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{})
		t.Cleanup(func() { _ = pool.Close() })
		key := RuntimeKey{ClientSession: "publication"}
		old, refused := newBlockedTransport(), newBlockedTransport()
		pool.SetProcess(key, newFakeProcess(t), old, "old", "", "agent")
		detached, _ := pool.DetachRuntime(key)
		<-old.entered
		ctx, cancel := context.WithCancel(t.Context())
		returned := make(chan struct{})
		go func() {
			pool.SetProcessContext(ctx, key, newFakeProcess(t), refused, "new", "", "agent")
			close(returned)
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		prompt := false
		select {
		case <-returned:
			prompt = true
		default:
		}
		if prompt && pool.liveProcessCount() != 2 {
			t.Errorf("refused child lost ownership before cleanup: count=%d, want 2", pool.liveProcessCount())
		}
		old.unblock()
		refused.unblock()
		<-returned
		<-detached.done
		synctest.Wait()
		if !prompt {
			t.Error("publication cancellation blocked on physical cleanup")
		}
		if rt := pool.Get(key); rt != nil && rt.cleanupOutcome() != nil {
			t.Fatalf("clean cancellation poisoned the slot: %v", rt.cleanupOutcome())
		}
		if _, busy := pool.ClaimForTurn(t.Context(), key, ""); busy {
			t.Fatal("clean cancellation made the slot unusable")
		}
	})
}

// Source: a release concurrent with cancellation must not admit the cancelled waiter.
func TestClaim_CancelAndReleaseDoesNotAcquire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{ClaimWait: time.Second})
		key := RuntimeKey{ClientSession: "held"}
		pool.ClaimForTurn(t.Context(), key, "")
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan bool, 1)
		go func() { _, busy := pool.ClaimForTurn(ctx, key, ""); result <- busy }()
		synctest.Wait()
		cancel()
		pool.Release(key)
		if !<-result {
			t.Fatal("cancelled waiter acquired the released slot")
		}
		if _, busy := pool.ClaimForTurn(t.Context(), key, ""); busy {
			t.Fatal("cancelled waiter left the slot held")
		}
	})
}

func TestClaim_CancelAndRetirementDoesNotAcquire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{ClaimWait: time.Second})
		t.Cleanup(func() { _ = pool.Close() })
		key := RuntimeKey{ClientSession: "retiring"}
		transport := newBlockedTransport()
		pool.SetProcess(key, newFakeProcess(t), transport, "session", "", "agent")
		pool.DetachRuntime(key)
		<-transport.entered
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan bool, 1)
		go func() { _, busy := pool.ClaimForTurn(ctx, key, ""); result <- busy }()
		synctest.Wait()
		cancel()
		transport.unblock()
		if !<-result {
			t.Fatal("cancelled waiter acquired the retired slot")
		}
	})
}

func TestPublication_UnconfirmedRefusedChildStaysOwned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool := NewRuntimePool(RuntimePoolConfig{MaxRuntimes: 1, ClaimWait: time.Second})
		key := RuntimeKey{ClientSession: "publication"}
		proc := newFakeProcess(t)
		proc.waitErr = ErrCleanupIncomplete
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		pool.SetProcessContext(ctx, key, proc, nil, "", "", "agent")
		synctest.Wait()
		if pool.liveProcessCount() != 1 {
			t.Fatal("unconfirmed refused child released capacity")
		}
		if rt := pool.Get(key); rt != nil && rt.cleanupOutcome() != nil {
			t.Fatal("refused child contaminated reusable slot")
		}
		if err := pool.Close(); !errors.Is(err, ErrCleanupIncomplete) {
			t.Fatalf("pool lost refused cleanup evidence: %v", err)
		}
	})
}
