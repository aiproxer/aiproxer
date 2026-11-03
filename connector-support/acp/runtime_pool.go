package acp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// ErrCleanupIncomplete means process-tree extinction was not confirmed. Owners
// must retain lifecycle state and refuse reuse, even if the direct child exited.
var ErrCleanupIncomplete = errors.New("acp: process cleanup incomplete")

// RuntimeKey identifies one ACP subprocess pool entry by workspace, model, and
// client session.
type RuntimeKey struct {
	Workspace     string
	Model         string
	ClientSession string
}

// SubprocessRuntime holds the state for one live ACP subprocess connection.
// The runtime retains process-scoped connection and turn state.
type SubprocessRuntime struct {
	mu sync.Mutex
	// Retirement has a process-scoped join point even after proc/transport are
	// withdrawn. Never hold mu while waiting for Kill/Wait or another retirement.
	retiring   chan struct{}
	cleanupErr error
	// Keep the captured handles as well as the error if extinction is unconfirmed.
	failedRetirement *DetachedRuntime

	key       RuntimeKey
	proc      Process
	identity  ProcessIdentity
	transport Transport

	// sessionID is the ACP session id from session/new, or empty if not yet
	// initialized (or if the session was reset).
	sessionID string

	// lastActivity is updated on each prompt turn for idle reaping decisions.
	lastActivity time.Time

	// inUse is true while a prompt turn is active; prevents idle reaping.
	inUse bool

	// initialized is true after a successful handshake (initialize + authenticate).
	initialized bool
	// spawning prevents reaping or reuse while the registered child handshakes.
	spawning bool

	// processConfig identifies the process-scoped configuration used when the
	// current subprocess was spawned (empty for protocols without variants).
	processConfig string

	// historyState tracks the conversation prefix for divergence detection.
	historyState historyState

	// staleEpoch names the current claim/timer generation of the runtime. A
	// claim and every new timer bump it, so an expired callback whose epoch
	// no longer matches the runtime knows that a later turn took it, and
	// retires nothing.
	staleEpoch uint64

	// withdrawn marks a runtime whose turn ended without a clean completion.
	// Its live process must never be reused: the next claim retires it before
	// the turn starts. The mark is set without blocking, so a claim never
	// waits on the physical retirement.
	withdrawn bool

	// quarantined marks a slot whose last retirement did not confirm process
	// extinction. The slot stays owned by the pool, is never reused, and is
	// never deleted: the unconfirmed process tree may still be alive, so the
	// evidence must survive the slot.
	quarantined bool

	// deleted marks a slot that was removed from the pool after a confirmed
	// retirement. A claim that observes it re-resolves the key instead of
	// claiming a detached slot.
	deleted bool

	// claimReleased is closed when the active turn claim ends. It is installed
	// by the claim and consumed by the release, so a waiter observes exactly
	// one release and never a stale one.
	claimReleased chan struct{}

	// sessionInstructions records the canonical instructions the live ACP
	// session was created with (delivered as _meta.systemPrompt by opting
	// connectors). Empty means the session carries no authoritative
	// instructions. Read and written under the turn claim only. The record is
	// process-bound: it is cleared when the process is published and when it
	// is detached, so it never describes a process that did not receive the
	// instructions.
	sessionInstructions string
}

// historyState tracks the message count and prefix hash for divergence detection.
type historyState struct {
	messageCount int
	prefixHash   string
}

// RuntimePoolConfig configures the subprocess lifecycle pool.
type RuntimePoolConfig struct {
	// IdleTimeout is how long a runtime may sit idle before it's eligible for
	// reaping on the next acquire. Zero disables idle reaping.
	IdleTimeout time.Duration
	// StaleKillDelay is how long after a prompt turn completes before the stale
	// kill timer fires. Zero disables stale kills.
	StaleKillDelay time.Duration
	// ClaimWait bounds how long a turn waits for a concurrent turn to release
	// the runtime, or for a retirement in flight to finish, before the claim
	// is refused. Zero refuses immediately.
	ClaimWait time.Duration
	// MaxRuntimes bounds how many live agent processes one pool may hold. A
	// claim that would exceed the bound waits for a slot to free and is
	// refused explicitly when the bound is reached. Zero is unbounded.
	MaxRuntimes int
	// Log is optional for debug logging.
	Log *slog.Logger
}

// RuntimePool manages a pool of ACP subprocesses keyed by workspace+model+session.
// It handles idle reaping, stale killing with PID-reuse hardening, and concurrent
// access safety.
type RuntimePool struct {
	cfg   RuntimePoolConfig
	log   *slog.Logger
	mu    sync.Mutex
	pools map[RuntimeKey]*SubprocessRuntime
	// Rejected publications remain pool-owned until their cleanup is confirmed.
	retirements map[*SubprocessRuntime]RuntimeKey
	// One construction reservation bridges the capacity check and registration.
	// After registration the slot counts through handshake and retirement.
	spawnGate chan struct{}

	// staleKillTimers tracks active stale-kill timers per runtime for cancellation.
	staleKillMu     sync.Mutex
	staleKillTimers map[RuntimeKey]staleKillTimer
	// staleKillBoundary is an optional test seam invoked by the stale-kill
	// reservation between its eligibility decision and the detachment, while
	// the runtime lock is held. It lets a test force a successor claim at that
	// boundary. Production code leaves it nil.
	staleKillBoundary func()
	// staleKillScheduleBoundary is an optional test seam invoked by the
	// stale-kill scheduling transaction between its ownership decision and its
	// timer publication, while the pool lock and the runtime lock are held. It
	// lets a test observe that the transaction keeps a successor claim and
	// release out until the timer is published. Production code leaves it nil.
	staleKillScheduleBoundary func()

	// capacityChanged is closed and replaced whenever the pool's live process
	// count may have changed, so a capacity waiter can observe the change
	// without polling.
	capacityMu      sync.Mutex
	capacityChanged chan struct{}

	// ensureMu guards ensureFlights. One flight per serialized RuntimeKey
	// joins concurrent EnsureProcess callers; the elected leader runs the
	// construction synchronously, while followers wait on its done channel and
	// may abandon that wait by cancelling their context.
	ensureMu      sync.Mutex
	ensureFlights map[string]*ensureFlight
}

// NewRuntimePool creates a new subprocess lifecycle pool.
// closeTransportQuietly closes transport, logging close errors via the pool
// logger. Lifecycle-cleanup paths have no primary error to attach, so the
// single-handling rule routes the close error to Debug instead of discarding
// with `_`. Safe to call with a nil transport or nil log.
func closeTransportQuietly(log *slog.Logger, key RuntimeKey, phase string, transport Transport) error {
	if transport == nil {
		return nil
	}
	err := transport.Close()
	if err != nil && log != nil {
		log.Debug("acp pool: transport close failed", "key", key, "phase", phase, "error", err)
	}
	if errors.Is(err, ErrCleanupIncomplete) {
		return err
	}
	return nil
}

// killAndWaitQuietly performs Kill+Wait on proc, logging each non-nil result via
// the pool logger. Same lifecycle-cleanup rationale as closeTransportQuietly.
// Sequential order: Kill first (terminate), Wait second (reap, closes pipes).
func killAndWaitQuietly(log *slog.Logger, key RuntimeKey, phase string, proc Process) error {
	if proc == nil {
		return nil
	}
	if err := proc.Kill(); err != nil && log != nil {
		log.Debug("acp pool: proc kill failed", "key", key, "phase", phase, "error", err)
	}
	err := proc.Wait()
	if err != nil && log != nil {
		log.Debug("acp pool: proc wait failed", "key", key, "phase", phase, "error", err)
	}
	if errors.Is(err, ErrCleanupIncomplete) {
		return err
	}
	return nil
}

func NewRuntimePool(cfg RuntimePoolConfig) *RuntimePool {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &RuntimePool{
		cfg:             cfg,
		log:             log,
		pools:           make(map[RuntimeKey]*SubprocessRuntime),
		retirements:     make(map[*SubprocessRuntime]RuntimeKey),
		spawnGate:       make(chan struct{}, 1),
		staleKillTimers: make(map[RuntimeKey]staleKillTimer),
		capacityChanged: make(chan struct{}),
		ensureFlights:   make(map[string]*ensureFlight),
	}
}

// staleKillTimer is one armed stale-kill timer, the runtime epoch that armed
// it and the runtime incarnation it belongs to. The epoch lets an expired
// callback tell whether the runtime still belongs to the turn that armed the
// timer; the incarnation keeps two runtimes that share a key and an epoch
// apart, so a callback can never remove another runtime's timer.
type staleKillTimer struct {
	epoch uint64
	rt    *SubprocessRuntime
	timer *time.Timer
}

// Acquire returns the runtime for key, creating a fresh entry if none exists.
// If the existing runtime's process is idle past IdleTimeout, it is killed and
// replaced with a fresh slot. The caller must call Release when done with the
// runtime (typically after the prompt turn completes).
func (p *RuntimePool) Acquire(key RuntimeKey) (*SubprocessRuntime, error) {
	p.mu.Lock()
	rt, exists := p.pools[key]
	if !exists {
		rt = &SubprocessRuntime{key: key}
		p.pools[key] = rt
	}
	p.mu.Unlock()

	if p.cfg.IdleTimeout > 0 {
		if replaced := p.tryReapIdle(key, rt); replaced != nil {
			rt = replaced
		}
	}
	rt.mu.Lock()
	err := rt.cleanupOutcomeLocked()
	rt.mu.Unlock()
	// Capacity management never blocks the request: the eviction retires the
	// victim in a pool-owned goroutine, so a request for an unrelated
	// conversation never waits for another runtime's process tree.
	p.enforceBound(key, 0)
	return rt, err
}

// tryReapIdle checks if the runtime is idle past the timeout and retires its
// process. The slot is kept: the retirement runs in a pool-owned goroutine and
// the slot keeps its cleanup evidence, so an unconfirmed extinction quarantines
// the slot instead of losing it. The transcript state is reset because the
// reaped process no longer represents the conversation.
func (p *RuntimePool) tryReapIdle(key RuntimeKey, rt *SubprocessRuntime) *SubprocessRuntime {
	rt.mu.Lock()
	if rt.retiring != nil {
		rt.mu.Unlock()
		return rt
	}
	if rt.inUse || rt.spawning || rt.quarantined || rt.deleted || rt.cleanupOutcomeLocked() != nil {
		rt.mu.Unlock()
		return nil
	}
	if rt.proc == nil {
		rt.mu.Unlock()
		return nil
	}
	if rt.lastActivity.IsZero() {
		rt.mu.Unlock()
		return nil
	}
	if time.Since(rt.lastActivity) < p.cfg.IdleTimeout {
		rt.mu.Unlock()
		return nil
	}
	// The detachment drops the transcript marker with the reaped process: it
	// no longer represents the conversation.
	detached, _ := detachRuntimeLocked(rt)
	done := make(chan struct{})
	rt.retiring = done
	rt.mu.Unlock()

	p.cancelStaleKill(key)
	p.retireAsync(key, rt, detached, done, false)
	if p.log != nil {
		p.log.Debug("acp: idle runtime reaped", "key", key)
	}
	return rt
}

// SetProcess attaches a spawned process and transport to the runtime and marks
// it as initialized. Called after a successful subprocess spawn + handshake.
// processConfig identifies the process-scoped configuration variant, and
// cmdFirstArg is the first element of the spawn command (e.g. "cursor-agent"),
// used as a fallback for the executable path in PID-reuse hardening.
func (p *RuntimePool) SetProcess(key RuntimeKey, proc Process, transport Transport, sessionID, processConfig, cmdFirstArg string) {
	p.SetProcessContext(context.Background(), key, proc, transport, sessionID, processConfig, cmdFirstArg)
}

// SetProcessContext attaches a spawned process and transport to the runtime
// and marks it as initialized. Called after a successful subprocess spawn +
// handshake. processConfig identifies the process-scoped configuration variant,
// and cmdFirstArg is the first element of the spawn command (e.g.
// "cursor-agent"), used as a fallback for the executable path in PID-reuse
// hardening.
//
// The wait for a retirement in flight is bounded by ctx: a process-tree
// cleanup that never confirms extinction must not hold the request forever.
func (p *RuntimePool) SetProcessContext(ctx context.Context, key RuntimeKey, proc Process, transport Transport, sessionID, processConfig, cmdFirstArg string) {
	_ = p.registerProcess(ctx, key, proc, transport, sessionID, processConfig, cmdFirstArg, true)
}

// registerProcess transfers ownership before a handshake can block. Refused
// publication transfers the child to its own retirement, not the current slot.
func (p *RuntimePool) registerProcess(ctx context.Context, key RuntimeKey, proc Process, transport Transport, sessionID, processConfig, cmdFirstArg string, initialized bool) error {
	refuse := func(err error) error {
		p.retireUnpublished(key, proc, transport, cmdFirstArg)
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return refuse(err)
		}
		p.mu.Lock()
		rt, exists := p.pools[key]
		if !exists {
			rt = &SubprocessRuntime{key: key}
			p.pools[key] = rt
		}
		p.mu.Unlock()

		rt.mu.Lock()
		if pending := rt.retiring; pending != nil {
			rt.mu.Unlock()
			select {
			case <-pending:
			case <-ctx.Done():
				return refuse(ctx.Err())
			}
			continue // idle reaping may have replaced the canonical slot
		}
		if rt.deleted || rt.quarantined || rt.cleanupOutcomeLocked() != nil {
			err := rt.cleanupOutcomeLocked()
			rt.mu.Unlock()
			return refuse(errors.Join(errors.New("acp: runtime unavailable for publication"), err))
		}
		if err := ctx.Err(); err != nil {
			rt.mu.Unlock()
			return refuse(err)
		}
		rt.proc = proc
		rt.identity = captureProcessIdentity(proc, cmdFirstArg)
		rt.transport = transport
		rt.sessionID = sessionID
		// A newly published process has no ACP session yet, so it carries no
		// authoritative instructions. The turn that creates the session
		// records them after the handshake.
		rt.sessionInstructions = ""
		rt.initialized = initialized
		rt.spawning = !initialized
		rt.processConfig = processConfig
		rt.lastActivity = time.Now()
		rt.mu.Unlock()
		return nil
	}
}

// retireUnpublished owns a refused child independently of the reusable slot.
// Cancellation is a request outcome; only cleanup failure quarantines this owner.
func (p *RuntimePool) retireUnpublished(key RuntimeKey, proc Process, transport Transport, cmdFirstArg string) {
	rt := &SubprocessRuntime{key: key, retiring: make(chan struct{})}
	detached := DetachedRuntime{proc: proc, transport: transport}
	if proc != nil {
		detached.identity = captureProcessIdentity(proc, cmdFirstArg)
	}
	p.mu.Lock()
	p.retirements[rt] = key
	p.mu.Unlock()
	p.retireAsync(key, rt, detached, rt.retiring, false)
}

// MarkInUse marks the runtime as actively processing a prompt turn.
func (p *RuntimePool) MarkInUse(key RuntimeKey) {
	p.mu.Lock()
	rt := p.pools[key]
	p.mu.Unlock()
	if rt == nil {
		return
	}
	rt.mu.Lock()
	rt.inUse = true
	// Marking the runtime in use supersedes its stale-kill timer, so an
	// expired callback cannot retire the process this turn is about to use.
	rt.staleEpoch++
	p.cancelStaleKill(key)
	rt.mu.Unlock()
}

// claimOutcome describes why a claim attempt did not acquire the runtime.
type claimOutcome int

const (
	// claimAcquired: the runtime is claimed by the caller.
	claimAcquired claimOutcome = iota
	// claimHeld: a live turn still holds the runtime. It will release it, so a
	// bounded wait can observe the release.
	claimHeld
	// claimRetiring: a retirement is in flight for the runtime. It will finish,
	// so a bounded wait can observe its completion.
	claimRetiring
	// claimUnusable: the runtime cannot serve a turn (a failed cleanup). No
	// release will ever arrive for it, so waiting cannot help.
	claimUnusable
	claimCanceled
)

// ClaimForTurn atomically claims the runtime for key for a new prompt turn and
// returns the runtime to use with a busy flag. It is the race-free replacement
// for the Open flow's former "MarkInUse then maybe KillRuntime" sequence.
//
// A single stdio subprocess cannot carry two concurrent turns, and killing an
// in-use transport would corrupt the in-flight turn. ClaimForTurn therefore
// refuses (busy=true, no claim) when another turn still holds the runtime, so
// the caller fails the conflicting request explicitly instead of killing the
// peer. It first waits up to the pool's configured ClaimWait for that turn to
// release the runtime, so the ordinary teardown of a finished turn does not
// fail the next turn of the same conversation.
//
// On a successful claim it cancels the stale-kill timer and, when the live
// child was spawned with a different process-scoped config than wantConfig
// (e.g. Codex model_verbosity), kills it and resets transcript state so the
// next spawn receives a full replay. A withdrawn runtime (a turn that ended
// without a clean completion) is retired the same way, so its ambiguous
// process is never reused. Because the claim is atomic, no peer can begin
// streaming between the in-use check and the kill, so the reset only ever
// runs on the idle (claiming) path. wantConfig empty (ACP-session protocols)
// never restarts. The caller must Release(key) when the turn completes.
func (p *RuntimePool) ClaimForTurn(ctx context.Context, key RuntimeKey, wantConfig string) (*SubprocessRuntime, bool) {
	rt, outcome := p.claimForTurn(ctx, key, wantConfig)
	return rt, outcome != claimAcquired
}

// claimForTurn claims the runtime for key and reports the precise outcome so
// the caller can distinguish a scheduling conflict from an unusable runtime.
// The wait is bounded by the pool's ClaimWait and observes ctx cancellation:
// a cancelled request never acquires a claim.
func (p *RuntimePool) claimForTurn(ctx context.Context, key RuntimeKey, wantConfig string) (*SubprocessRuntime, claimOutcome) {
	wait := p.cfg.ClaimWait
	deadline := time.Now().Add(wait)
	for {
		if ctx.Err() != nil {
			return nil, claimCanceled
		}
		rt, outcome := p.tryClaimForTurn(key, wantConfig)
		switch outcome {
		case claimAcquired:
			if ctx.Err() != nil {
				p.Release(key)
				return rt, claimCanceled
			}
			return rt, claimAcquired
		case claimUnusable:
			return rt, claimUnusable
		}
		if wait <= 0 {
			return rt, outcome
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return rt, outcome
		}
		signal, retry := rt.claimWaitSignal(outcome)
		if retry {
			// The holder released between the attempt and the signal read.
			continue
		}
		if signal == nil {
			// The holder never signalled its release, so no release can be
			// observed. Do not wait for one.
			return rt, outcome
		}
		timer := time.NewTimer(remaining)
		select {
		case <-signal:
			timer.Stop()
		case <-timer.C:
			return rt, outcome
		case <-ctx.Done():
			timer.Stop()
			return rt, claimCanceled
		}
	}
}

// tryClaimForTurn attempts one claim and reports why it did not acquire the
// runtime.
func (p *RuntimePool) tryClaimForTurn(key RuntimeKey, wantConfig string) (*SubprocessRuntime, claimOutcome) {
	for {
		p.mu.Lock()
		rt, exists := p.pools[key]
		if !exists {
			rt = &SubprocessRuntime{key: key}
			p.pools[key] = rt
		}
		p.mu.Unlock()

		rt.mu.Lock()
		if rt.deleted {
			// The slot was retired and removed while this claim resolved it.
			rt.mu.Unlock()
			continue
		}
		if rt.inUse || rt.spawning {
			rt.mu.Unlock()
			return rt, claimHeld
		}
		if rt.retiring != nil {
			rt.mu.Unlock()
			return rt, claimRetiring
		}
		if rt.quarantined || rt.cleanupOutcomeLocked() != nil {
			rt.mu.Unlock()
			return rt, claimUnusable
		}
		withdrawn := rt.withdrawn
		rt.inUse = true
		// The claim supersedes the runtime's stale-kill timer: an expired
		// callback must not retire the process this turn is about to use.
		rt.staleEpoch++
		rt.claimReleased = make(chan struct{})
		hasProc := rt.proc != nil && rt.initialized
		currentCfg := rt.processConfig
		// The cancellation runs under the same lock that bumps the
		// generation, so no timer can be armed between the two.
		p.cancelStaleKill(key)
		rt.mu.Unlock()

		if withdrawn || (hasProc && currentCfg != wantConfig) {
			// The live child must not be reused: withdraw it, or reset it for a
			// different process-scoped configuration, so the next spawn receives a
			// complete replay. The retirement runs in a pool-owned goroutine, so
			// this claim never blocks on a process-tree cleanup.
			p.detachLocked(rt, key)
			fresh := p.Get(key)
			if fresh != nil {
				fresh.mu.Lock()
				unusable := fresh.quarantined || fresh.cleanupOutcomeLocked() != nil
				fresh.mu.Unlock()
				if unusable {
					// The runtime cannot serve turns. End the claim so the
					// refusal never leaves it held.
					p.Release(key)
					return fresh, claimUnusable
				}
				return fresh, claimAcquired
			}
		}
		return rt, claimAcquired
	}
}

// detachLocked detaches the runtime's live incarnation and starts its
// retirement. The caller must own the slot: it holds the turn claim, or it
// reserved the slot for eviction.
func (p *RuntimePool) detachLocked(rt *SubprocessRuntime, key RuntimeKey) bool {
	rt.mu.Lock()
	if rt.retiring != nil {
		rt.mu.Unlock()
		return false
	}
	detached, ok := detachRuntimeLocked(rt)
	if !ok {
		rt.mu.Unlock()
		return false
	}
	done := make(chan struct{})
	rt.retiring = done
	rt.mu.Unlock()
	p.retireAsync(key, rt, detached, done, false)
	return true
}

// claimWaitSignal returns the channel that closes when the blocking condition
// observed by a claim attempt ends: the turn release for a held runtime, the
// retirement completion for a retiring runtime. The second result reports that
// the condition already ended and the attempt should be repeated.
func (r *SubprocessRuntime) claimWaitSignal(outcome claimOutcome) (chan struct{}, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch outcome {
	case claimHeld:
		if r.claimReleased != nil {
			return r.claimReleased, false
		}
		// A claim taken without a release signal can never be observed.
		return nil, !r.inUse
	case claimRetiring:
		if r.retiring != nil {
			return r.retiring, false
		}
		return nil, true
	default:
		return nil, false
	}
}

// DetachedRuntime is one live process incarnation detached from its runtime
// slot. Only the owner of a detached runtime may retire it, so a departing turn
// can never retire the process a successor published under the same key.
type DetachedRuntime struct {
	proc      Process
	transport Transport
	identity  ProcessIdentity
	// done closes when the retirement of this incarnation completes.
	done chan struct{}
}

// detachRuntimeLocked removes the runtime's live incarnation from the slot.
// The caller must hold rt.mu and must retire the returned handles.
//
// Everything the slot records about the live process dies with it. The
// transcript marker records the messages the live agent process already
// received, and the session instructions record the system instructions its
// ACP session was created with; neither describes a replacement process, so a
// successor receives the full transcript and re-delivers its instructions
// instead of inheriting a stale claim that it already has them.
func detachRuntimeLocked(rt *SubprocessRuntime) (DetachedRuntime, bool) {
	if rt.proc == nil && rt.transport == nil {
		return DetachedRuntime{}, false
	}
	detached := DetachedRuntime{proc: rt.proc, transport: rt.transport, identity: rt.identity}
	rt.proc = nil
	rt.transport = nil
	rt.initialized = false
	rt.spawning = false
	rt.sessionID = ""
	rt.sessionInstructions = ""
	rt.processConfig = ""
	rt.withdrawn = false
	rt.historyState = historyState{}
	return detached, true
}

// DetachRuntime detaches the runtime's live incarnation from its slot and
// marks the slot as retiring. The slot keeps no process, so no successor can
// reuse the detached one, and the slot is not claimable until the retirement
// completes. The physical cleanup runs in a pool-owned goroutine, so the
// calling turn never blocks on a process-tree cleanup that may not finish.
//
// The detachment is the retirement reservation: it happens under the runtime
// lock, so a concurrent claim either wins the slot first or observes the
// retiring mark.
func (p *RuntimePool) DetachRuntime(key RuntimeKey) (DetachedRuntime, bool) {
	p.mu.Lock()
	rt := p.pools[key]
	p.mu.Unlock()
	if rt == nil {
		return DetachedRuntime{}, false
	}
	rt.mu.Lock()
	if rt.retiring != nil {
		rt.mu.Unlock()
		return DetachedRuntime{}, false
	}
	detached, ok := detachRuntimeLocked(rt)
	if !ok {
		rt.mu.Unlock()
		return DetachedRuntime{}, false
	}
	done := make(chan struct{})
	rt.retiring = done
	detached.done = done
	rt.mu.Unlock()

	p.retireAsync(key, rt, detached, done, false)
	return detached, true
}

// retireAsync retires a detached incarnation in a pool-owned goroutine. The
// slot keeps its retiring mark until the retirement completes, so no peer can
// reuse the detached process, and a retirement that does not confirm process
// extinction quarantines the slot instead of releasing it.
//
// release removes the slot from the pool once the retirement is confirmed. It
// is used by capacity eviction, where the slot's process is gone for good; a
// retirement that fails never releases the slot, because its cleanup evidence
// must survive.
func (p *RuntimePool) retireAsync(key RuntimeKey, rt *SubprocessRuntime, detached DetachedRuntime, done chan struct{}, release bool) {
	if detached.done == nil {
		detached.done = done
	}
	go func() {
		err := p.retireDetached(key, detached)
		rt.mu.Lock()
		if err != nil {
			rt.cleanupErr = errors.Join(rt.cleanupErr, err)
			rt.quarantined = true
			rt.failedRetirement = &detached
		}
		rt.retiring = nil
		close(done)
		rt.mu.Unlock()
		if err != nil {
			if p.log != nil {
				p.log.Error("acp pool: retirement did not confirm process extinction", "key", key, "error", err)
			}
		} else if release || p.cfg.MaxRuntimes > 0 {
			// Release may have run while cleanup was pending. Revisit the
			// empty slot now; claimed and quarantined owners remain protected.
			p.removeRetiredSlot(key, rt)
		}
		if err == nil {
			p.mu.Lock()
			delete(p.retirements, rt)
			p.mu.Unlock()
		}
		p.signalCapacity()
	}()
}

// removeRetiredSlot removes a confirmed-retired slot from the pool. A slot
// that is still claimed stays owned: the running turn needs it.
func (p *RuntimePool) removeRetiredSlot(key RuntimeKey, rt *SubprocessRuntime) {
	rt.mu.Lock()
	if rt.inUse || rt.spawning || rt.quarantined || rt.proc != nil || rt.transport != nil || rt.retiring != nil || rt.cleanupOutcomeLocked() != nil {
		rt.mu.Unlock()
		return
	}
	rt.deleted = true
	rt.mu.Unlock()

	p.mu.Lock()
	if current, ok := p.pools[key]; ok && current == rt {
		delete(p.pools, key)
	}
	p.mu.Unlock()
}

// retireDetached performs the physical cleanup of one detached incarnation.
// It never touches the slot, so it cannot retire a successor's process.
func (p *RuntimePool) retireDetached(key RuntimeKey, detached DetachedRuntime) error {
	proc, transport, identity := detached.proc, detached.transport, detached.identity
	if proc == nil {
		// Orphaned-runtime edge case: the process is already gone, close the
		// cached transport.
		return closeTransportQuietly(p.log, key, "orphan", transport)
	}
	// PID-reuse hardening: verify the process is still the one we started. If
	// the PID was reused, the original process already exited and signalling
	// would target the wrong process.
	if identity.PID > 0 && !stillSameProcess(proc, identity) {
		if p.log != nil {
			p.log.Debug("acp: stale kill skipped — process identity mismatch", "key", key, "pid", identity.PID)
		}
		// An opt-in lifecycle owner may join its original process handle and
		// cached cleanup outcome without signalling the recycled numeric PID.
		if owner, ok := proc.(interface{ WaitCleanup() error }); ok {
			// The captured instance can still be alive with a replaced or
			// unlinked executable: the executable key changes while the PID and
			// the start time stay the same. Joining such an owner without first
			// asking it to stop would block forever on a healthy supervisor, so
			// request shutdown through the identity-proven instance. A recycled
			// PID is never signalled.
			if sameProcessInstance(proc, identity) {
				if err := proc.Kill(); err != nil && p.log != nil {
					p.log.Debug("acp: replaced-executable owner shutdown requested", "key", key, "pid", identity.PID, "error", err)
				}
			}
			if err := owner.WaitCleanup(); errors.Is(err, ErrCleanupIncomplete) {
				return err
			}
		}
		return nil
	}
	if transport != nil {
		return closeTransportQuietly(p.log, key, "kill", transport)
	}
	return killAndWaitQuietly(p.log, key, "kill", proc)
}

// RetireRuntime detaches the runtime's live incarnation and retires exactly
// that process, joining the cleanup.
//
// The retirement targets the incarnation this call detached, so a departing
// turn can never retire the process a successor published under the same key.
// The join keeps the fail-closed lifecycle: the process tree is confirmed
// extinct before the caller releases the turn claim.
func (p *RuntimePool) RetireRuntime(key RuntimeKey) error {
	p.mu.Lock()
	rt := p.pools[key]
	p.mu.Unlock()
	if rt == nil {
		return nil
	}
	p.cancelStaleKill(key)

	rt.mu.Lock()
	if pending := rt.retiring; pending != nil {
		rt.mu.Unlock()
		<-pending
		rt.mu.Lock()
		err := rt.cleanupErr
		rt.mu.Unlock()
		return err
	}
	detached, ok := detachRuntimeLocked(rt)
	if !ok {
		rt.mu.Unlock()
		return nil
	}
	done := make(chan struct{})
	rt.retiring = done
	rt.mu.Unlock()

	p.retireAsync(key, rt, detached, done, false)
	<-done
	rt.mu.Lock()
	err := rt.cleanupErr
	rt.mu.Unlock()
	return err
}

// MarkWithdrawn marks the runtime for key as withdrawn: the turn that held it
// ended without a clean completion, so its live process must never be reused.
// The mark never blocks, and the transcript state is cleared because the
// withdrawn process no longer represents the conversation. The next claim
// retires the process before the turn starts.
func (p *RuntimePool) MarkWithdrawn(key RuntimeKey) {
	p.mu.Lock()
	rt := p.pools[key]
	p.mu.Unlock()
	if rt == nil {
		return
	}
	rt.mu.Lock()
	rt.withdrawn = true
	rt.historyState = historyState{}
	rt.mu.Unlock()
}

// Release marks the runtime as no longer in use, updates lastActivity, and
// schedules a stale-kill timer if configured. It is idempotent with respect to
// the claim signal: only the release that ends the active claim closes it.
//
// The release captures the runtime's current generation under the runtime
// lock. That generation owns the arming, and the runtime the release ended
// owns the slot: a release that a later claim or a replaced slot superseded
// must not arm a timer, because such a timer would retire a process another
// turn owns.
func (p *RuntimePool) Release(key RuntimeKey) {
	p.mu.Lock()
	rt := p.pools[key]
	p.mu.Unlock()
	if rt == nil {
		return
	}
	rt.mu.Lock()
	rt.inUse = false
	rt.lastActivity = time.Now()
	released := rt.claimReleased
	rt.claimReleased = nil
	token := rt.staleEpoch
	rt.mu.Unlock()
	if released != nil {
		close(released)
	}
	// A refused cold start owns no process and must not accumulate as an
	// eviction candidate. Quarantined or retiring owners are never pruned.
	if p.cfg.MaxRuntimes > 0 {
		p.removeRetiredSlot(key, rt)
	}
	p.signalCapacity()

	if p.cfg.StaleKillDelay > 0 {
		p.scheduleStaleKill(key, rt, token)
	}
}

// enforceBound retires one idle runtime when the pool would hold more live
// agent processes than the bound allows, counting the processes the caller
// still needs. The victim is selected and reserved under the runtime lock, so
// a concurrent claim either wins the slot first or observes the retiring mark:
// an active turn can never lose its process to the eviction.
//
// A retirement that does not confirm process extinction keeps its slot: the
// evidence and the possibly-live process tree stay owned by the pool, and the
// slot counts against the bound until the cleanup is confirmed.
func (p *RuntimePool) enforceBound(keep RuntimeKey, need int) {
	max := p.cfg.MaxRuntimes
	if max <= 0 {
		return
	}
	if p.liveProcessCount()+need <= max {
		return
	}
	victim, ok := p.reserveIdleVictim(keep)
	if !ok {
		// Every other slot is busy or quarantined. The bound is exceeded only
		// until those turns finish.
		return
	}
	p.retireAsync(victim.key, victim.rt, victim.detached, victim.done, true)
}

// evictionVictim is one runtime reserved for capacity eviction.
type evictionVictim struct {
	key      RuntimeKey
	rt       *SubprocessRuntime
	detached DetachedRuntime
	done     chan struct{}
}

// reserveIdleVictim selects an idle runtime and detaches its live incarnation
// under the runtime lock. The reservation is atomic with the selection: no
// claim can acquire the runtime between the idle check and the detachment.
func (p *RuntimePool) reserveIdleVictim(keep RuntimeKey) (evictionVictim, bool) {
	p.mu.Lock()
	type candidate struct {
		key      RuntimeKey
		rt       *SubprocessRuntime
		activity time.Time
	}
	candidates := make([]candidate, 0, len(p.pools))
	for k, rt := range p.pools {
		if k == keep {
			continue
		}
		candidates = append(candidates, candidate{key: k, rt: rt})
	}
	p.mu.Unlock()
	for i := range candidates {
		rt := candidates[i].rt
		rt.mu.Lock()
		candidates[i].activity = rt.lastActivity
		rt.mu.Unlock()
	}
	slices.SortFunc(candidates, func(a, b candidate) int { return a.activity.Compare(b.activity) })
	for _, c := range candidates {
		rt := c.rt
		rt.mu.Lock()
		if rt.inUse || rt.spawning || rt.proc == nil || rt.retiring != nil || rt.withdrawn || rt.quarantined || rt.deleted {
			rt.mu.Unlock()
			continue
		}
		detached, _ := detachRuntimeLocked(rt)
		done := make(chan struct{})
		rt.retiring = done
		detached.done = done
		rt.mu.Unlock()
		return evictionVictim{key: c.key, rt: rt, detached: detached, done: done}, true
	}
	return evictionVictim{}, false
}

// liveProcessCount returns the number of slots that may still hold a live
// agent process: a slot with a process, a slot whose retirement is in flight,
// and a quarantined slot whose process extinction was never confirmed.
func (p *RuntimePool) liveProcessCount() int {
	p.mu.Lock()
	runtimes := make([]*SubprocessRuntime, 0, len(p.pools))
	for _, rt := range p.pools {
		runtimes = append(runtimes, rt)
	}
	for rt := range p.retirements {
		runtimes = append(runtimes, rt)
	}
	p.mu.Unlock()
	count := 0
	for _, rt := range runtimes {
		rt.mu.Lock()
		live := rt.proc != nil || rt.retiring != nil || rt.quarantined
		rt.mu.Unlock()
		if live {
			count++
		}
	}
	return count
}

// signalCapacity wakes the capacity waiters: a retirement completed or a turn
// released its slot.
func (p *RuntimePool) signalCapacity() {
	p.capacityMu.Lock()
	close(p.capacityChanged)
	p.capacityChanged = make(chan struct{})
	p.capacityMu.Unlock()
}

// capacitySignal returns the channel closed when the pool's live process count
// may have changed.
func (p *RuntimePool) capacitySignal() chan struct{} {
	p.capacityMu.Lock()
	defer p.capacityMu.Unlock()
	return p.capacityChanged
}

// reserveSpawn holds the pool-wide construction permit until the started child
// is registered. Thus another key cannot pass capacity while Spawn is in flight.
// Once registered, the child counts through handshake, use and retirement.
func (p *RuntimePool) reserveSpawn(ctx context.Context, key RuntimeKey) (func(), error) {
	if p.cfg.MaxRuntimes <= 0 {
		// Legacy/unbounded pools need no construction permit. Do not turn
		// their zero ClaimWait into a new cross-key admission refusal.
		return func() {}, ctx.Err()
	}
	if p.cfg.ClaimWait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.cfg.ClaimWait)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case p.spawnGate <- struct{}{}:
	default:
		if p.cfg.ClaimWait <= 0 {
			return nil, errors.New("acp: runtime spawn capacity reached")
		}
		select {
		case p.spawnGate <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	release := func() { <-p.spawnGate }
	if err := p.reserveCapacity(ctx, key); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// reserveCapacity waits for room for one more live agent process. Callers that
// spawn must hold spawnGate through registration. It retires
// idle runtimes when the bound is reached and waits, bounded by the pool's
// ClaimWait and the caller's context, for a running turn to finish or for a
// retirement to complete. When no slot frees in time the caller is refused
// explicitly: the bound is never exceeded silently.
func (p *RuntimePool) reserveCapacity(ctx context.Context, key RuntimeKey) error {
	max := p.cfg.MaxRuntimes
	if max <= 0 {
		return nil
	}
	deadline := time.Now().Add(p.cfg.ClaimWait)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The change signal is captured before the eviction: a retirement that
		// completes between the check and the wait must still wake the waiter.
		signal := p.capacitySignal()
		p.enforceBound(key, 1)
		if p.liveProcessCount()+1 <= max {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("acp: runtime capacity %d reached", max)
		}
		timer := time.NewTimer(remaining)
		select {
		case <-signal:
			timer.Stop()
		case <-timer.C:
			return fmt.Errorf("acp: runtime capacity %d reached", max)
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// Get returns the runtime for key without side effects, or nil if none exists.
func (p *RuntimePool) Get(key RuntimeKey) *SubprocessRuntime {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pools[key]
}

// KillRuntime kills the process for the given key and resets the runtime state.
// Safe to call even if the process has already exited. The transport is closed
// (which kills the process, closes stdin, and unblocks goroutines) unless
// PID-reuse hardening detects the original process has already exited and the
// PID may have been reused — in that case, killing would target the wrong process.
// KillRuntime kills the process for the given key and resets the runtime state.
// Safe to call even if the process has already exited. The transport is closed
// (which kills the process, closes stdin, and unblocks goroutines) unless
// PID-reuse hardening detects the original process has already exited and the
// PID may have been reused — in that case, killing would target the wrong process.
//
// The caller must own the slot: it holds the turn claim, it reserved the slot
// for eviction, or it is the pool shutting down. A departing turn uses
// [RuntimePool.DetachRuntime] instead, so it can never retire the process a
// successor published under the same key.
func (p *RuntimePool) KillRuntime(key RuntimeKey) error {
	return p.KillRuntimeContext(context.Background(), key)
}

// KillRuntimeContext retires the runtime's live process and waits for the
// retirement, bounded by ctx. A context that ends first leaves the retirement
// running in the background: the slot stays marked as retiring and is never
// reused.
func (p *RuntimePool) KillRuntimeContext(ctx context.Context, key RuntimeKey) error {
	p.mu.Lock()
	rt := p.pools[key]
	p.mu.Unlock()
	if rt == nil {
		return nil
	}
	p.cancelStaleKill(key)

	rt.mu.Lock()
	if pending := rt.retiring; pending != nil {
		rt.mu.Unlock()
		return waitRetirement(ctx, pending, rt)
	}
	if rt.cleanupErr != nil {
		err := rt.cleanupErr
		rt.mu.Unlock()
		return err
	}
	detached, ok := detachRuntimeLocked(rt)
	if !ok {
		rt.mu.Unlock()
		return nil
	}
	done := make(chan struct{})
	rt.retiring = done
	rt.mu.Unlock()

	p.retireAsync(key, rt, detached, done, false)
	return waitRetirement(ctx, done, rt)
}

// waitRetirement waits for a retirement to complete and reports its outcome.
func waitRetirement(ctx context.Context, done chan struct{}, rt *SubprocessRuntime) error {
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	rt.mu.Lock()
	err := rt.cleanupErr
	rt.mu.Unlock()
	return err
}

func (rt *SubprocessRuntime) finishRetirement(done chan struct{}) {
	rt.mu.Lock()
	rt.retiring = nil
	close(done)
	rt.mu.Unlock()
}

// cleanupOutcomeLocked returns the cleanup failure known for this runtime. It
// combines the pool's own sticky outcome with a failure the transport already
// observed: a transport can be closed without the pool driving the retirement
// (request cancellation closes it), and that outcome must still refuse reuse.
// The caller must hold rt.mu.
func (rt *SubprocessRuntime) cleanupOutcomeLocked() error {
	if rt.cleanupErr != nil {
		return rt.cleanupErr
	}
	if rt.transport != nil {
		if owner, ok := rt.transport.(interface{ CleanupError() error }); ok {
			return owner.CleanupError()
		}
	}
	return nil
}

// cleanupOutcome returns the cleanup failure known for this runtime.
func (rt *SubprocessRuntime) cleanupOutcome() error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.cleanupOutcomeLocked()
}

// Close kills all runtimes in the pool and clears it. Used during shutdown.
func (p *RuntimePool) Close() error {
	// Join admitted constructions before snapshotting lifecycle state. A
	// construction that finishes during shutdown either publishes to the pool or
	// hands its refused child to a retirement, and Close must observe both. The
	// leader stays attached to its own caller, so an abandoned construction
	// cannot outlive the shutdown that joined it.
	p.joinEnsureFlights()

	p.mu.Lock()
	keys := make([]RuntimeKey, 0, len(p.pools))
	for k := range p.pools {
		keys = append(keys, k)
	}
	retained := make([]*SubprocessRuntime, 0, len(p.retirements))
	for rt := range p.retirements {
		retained = append(retained, rt)
	}
	p.mu.Unlock()

	// One owner may retain its descendants indefinitely on OS cleanup failure.
	// Start every retirement before joining so it cannot starve healthy peers.
	// Close owns and joins one worker per snapshotted runtime, without pool locks.
	errs := make([]error, len(keys)+len(retained))
	var retiring sync.WaitGroup
	for i, k := range keys {
		retiring.Go(func() { errs[i] = p.KillRuntime(k) })
	}
	for i, rt := range retained {
		retiring.Go(func() {
			rt.mu.Lock()
			pending := rt.retiring
			rt.mu.Unlock()
			if pending != nil {
				errs[len(keys)+i] = waitRetirement(context.Background(), pending, rt)
			} else {
				errs[len(keys)+i] = rt.cleanupOutcome()
			}
		})
	}
	retiring.Wait()
	if err := errors.Join(errs...); err != nil {
		return err
	}

	p.mu.Lock()
	p.pools = make(map[RuntimeKey]*SubprocessRuntime)
	p.mu.Unlock()

	p.staleKillMu.Lock()
	for k, armed := range p.staleKillTimers {
		armed.timer.Stop()
		delete(p.staleKillTimers, k)
	}
	p.staleKillMu.Unlock()
	return nil
}

// scheduleStaleKill arms a timer to retire the runtime after StaleKillDelay.
//
// The scheduling is one transaction. It verifies that the releasing runtime is
// still the pool's canonical slot for the key, that the release generation the
// release carries is still the runtime's current generation, and that the
// runtime is eligible to be retired when the timer fires: it has a live
// process, no retirement is in flight and its cleanup was confirmed. Only
// then does it replace the armed timer.
//
// The transaction holds the pool lock, the runtime lock and the timer lock
// together, in that order, so a claim or a release that supersedes the owning
// release cannot interleave between the ownership decision and the timer
// publication. It either runs before the transaction, and the transaction
// observes its superseded generation, or it runs after, and it observes the
// published timer. A scheduler that published outside the transaction would
// cancel the successor's timer and replace it with its own, leaving the
// successor's process without a stale-kill timer at all.
//
// The owner is the runtime the release ended, not the runtime the key resolves
// to now: a release that a replaced slot superseded owns nothing, even when
// the replacement happens to carry the same numeric generation.
func (p *RuntimePool) scheduleStaleKill(key RuntimeKey, owner *SubprocessRuntime, token uint64) {
	p.mu.Lock()
	rt, ok := p.pools[key]
	if !ok || rt != owner {
		// The pool no longer serves the releasing runtime under the key.
		p.mu.Unlock()
		return
	}
	rt.mu.Lock()
	if rt.staleEpoch != token {
		// A later claim superseded this release.
		rt.mu.Unlock()
		p.mu.Unlock()
		return
	}
	if rt.retiring != nil || rt.quarantined || rt.proc == nil {
		// The runtime cannot be retired when the timer fires: a retirement
		// is already in flight, its cleanup was not confirmed, or it has no
		// live process. Arming a timer for it would only produce a skip.
		rt.mu.Unlock()
		p.mu.Unlock()
		return
	}
	rt.staleEpoch++
	epoch := rt.staleEpoch
	if p.staleKillScheduleBoundary != nil {
		p.staleKillScheduleBoundary()
	}
	// The previous timer and the new one are replaced under one lock, so the
	// successor's timer cannot be cancelled by a publication that started
	// before it.
	p.staleKillMu.Lock()
	if armed, ok := p.staleKillTimers[key]; ok {
		armed.timer.Stop()
	}
	timer := time.AfterFunc(p.cfg.StaleKillDelay, func() {
		p.retireStaleRuntime(key, rt, epoch)
	})
	p.staleKillTimers[key] = staleKillTimer{epoch: epoch, rt: rt, timer: timer}
	p.staleKillMu.Unlock()
	rt.mu.Unlock()
	p.mu.Unlock()
}

// retireStaleRuntime retires the runtime's live incarnation when the stale
// timer of the given epoch fires.
//
// The reservation is one operation under the runtime lock: a successor claim
// either wins the slot first — the reservation observes the claim and retires
// nothing — or observes the retiring mark, so the timer owns exactly the
// incarnation it detached and never the process a successor published under
// the same key. A superseded epoch owns neither: its turn already ended.
//
// The retirement runs in a pool-owned goroutine, so a process tree that never
// confirms its extinction cannot block the timer, and an unconfirmed
// extinction keeps the slot quarantined with its cleanup evidence.
func (p *RuntimePool) retireStaleRuntime(key RuntimeKey, armed *SubprocessRuntime, epoch uint64) {
	p.mu.Lock()
	rt := p.pools[key]
	p.mu.Unlock()
	if rt != armed {
		// The pool no longer serves this runtime under the key; the timer
		// belongs to a slot that was replaced.
		p.disarmStaleKill(key, armed, epoch)
		return
	}

	detached, done, reason := p.reserveStaleRetirement(rt, epoch)
	if reason != "" {
		p.logStaleSkip(key, reason)
		p.disarmStaleKill(key, armed, epoch)
		return
	}

	// The fired timer is no longer armed. The bookkeeping follows the
	// reservation so the eligibility check and the detachment stay atomic.
	p.disarmStaleKill(key, armed, epoch)
	p.retireAsync(key, rt, detached, done, false)
}

// reserveStaleRetirement reserves the runtime's live incarnation for the stale
// timer of the given epoch.
//
// The epoch check, the eligibility checks, the detachment and the retiring
// mark are one reservation under the runtime lock. No caller can split the
// decision from the detachment: a successor claim either wins the slot first
// or observes the retiring mark, and a superseded epoch never detaches
// anything.
//
// The returned reason is empty when the reservation succeeded.
func (p *RuntimePool) reserveStaleRetirement(rt *SubprocessRuntime, epoch uint64) (DetachedRuntime, chan struct{}, string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.staleEpoch != epoch {
		return DetachedRuntime{}, nil, "epoch superseded"
	}
	switch {
	case rt.retiring != nil:
		return DetachedRuntime{}, nil, "retirement in flight"
	case rt.inUse:
		return DetachedRuntime{}, nil, "runtime in use"
	case rt.quarantined || rt.cleanupErr != nil:
		// A slot whose cleanup was not confirmed keeps its process tree: the
		// retirement that failed owns it.
		return DetachedRuntime{}, nil, "runtime quarantined"
	case rt.proc == nil:
		return DetachedRuntime{}, nil, "no live process"
	}
	if p.staleKillBoundary != nil {
		p.staleKillBoundary()
	}
	detached, ok := detachRuntimeLocked(rt)
	if !ok {
		return DetachedRuntime{}, nil, "no live process"
	}
	done := make(chan struct{})
	rt.retiring = done
	return detached, done, ""
}

// logStaleSkip reports why the stale timer left a runtime alone. The timer
// skips a retirement on purpose, and the reason stays observable for the
// operator.
func (p *RuntimePool) logStaleSkip(key RuntimeKey, reason string) {
	if p.log != nil {
		p.log.Debug("acp: stale kill skipped", "key", key, "reason", reason)
	}
}

// disarmStaleKill removes the fired timer for key, but only when it is still
// the timer that armed the given epoch of the given runtime. A timer armed
// after this one, or one that belongs to another runtime under the same key,
// keeps its entry and stays armed.
func (p *RuntimePool) disarmStaleKill(key RuntimeKey, rt *SubprocessRuntime, epoch uint64) {
	p.staleKillMu.Lock()
	armed, ok := p.staleKillTimers[key]
	if ok && armed.epoch == epoch && armed.rt == rt {
		delete(p.staleKillTimers, key)
	}
	p.staleKillMu.Unlock()
}

// cancelStaleKill stops the stale-kill timer for key if one is armed.
func (p *RuntimePool) cancelStaleKill(key RuntimeKey) {
	p.staleKillMu.Lock()
	armed, ok := p.staleKillTimers[key]
	if ok {
		armed.timer.Stop()
		delete(p.staleKillTimers, key)
	}
	p.staleKillMu.Unlock()
}

// String returns a debug-friendly representation of the pool state.
func (k RuntimeKey) String() string {
	return fmt.Sprintf("{workspace=%s model=%s session=%s}", k.Workspace, k.Model, k.ClientSession)
}

// SessionID returns the ACP session id for the runtime, or empty if not initialized.
func (r *SubprocessRuntime) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionID
}

// RotateSession records a replacement ACP session id with the instructions
// it was created with, after an instruction-driven rotation. Callers must
// hold the turn claim; history reset is a separate explicit step.
func (r *SubprocessRuntime) RotateSession(sessionID, instructions string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessionID = sessionID
	r.sessionInstructions = instructions
}

// SessionInstructions returns the instructions the live session was created
// with, or empty when it carries none.
func (r *SubprocessRuntime) SessionInstructions() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionInstructions
}

// IsInitialized returns true if the runtime has completed a successful handshake.
func (r *SubprocessRuntime) IsInitialized() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.initialized
}

// IsInUse returns true while a prompt turn is actively streaming on this
// runtime. The pool uses it to refuse killing a child another turn still holds.
func (r *SubprocessRuntime) IsInUse() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inUse
}

// ProcessConfig returns the process-scoped configuration used for the live
// subprocess, or empty when no process has been initialized.
func (r *SubprocessRuntime) ProcessConfig() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.processConfig
}

// HasProcess returns true if the runtime has a live subprocess.
func (r *SubprocessRuntime) HasProcess() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proc != nil
}

// Transport returns the transport for the runtime, or nil if the process has
// not been spawned or has been killed/reaped. The transport and process are
// always set together by SetProcess and cleared together by KillRuntime/reap,
// so HasProcess() == true implies Transport() != nil.
func (r *SubprocessRuntime) Transport() Transport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.transport
}

// HistoryState returns the current history state for divergence detection.
func (r *SubprocessRuntime) HistoryState() historyState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.historyState
}

// FreshHistoryState returns a zero history state representing a fresh agent
// process with no prior conversation. Cross-package callers use it as the
// starting state when no runtime entry exists yet; same-package callers can
// use the historyState zero value directly.
func FreshHistoryState() historyState { return historyState{} }

// SetHistoryState updates the history state after a successful prompt turn.
func (r *SubprocessRuntime) SetHistoryState(state historyState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.historyState = state
}

// ResetHistoryState marks the runtime as having no transcript known by the
// current process. Callers use this when a process-scoped configuration change
// forces a fresh subprocess and full transcript replay.
func (r *SubprocessRuntime) ResetHistoryState() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.historyState = historyState{}
}
