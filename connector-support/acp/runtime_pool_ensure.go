package acp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// SpawnHandshakeStrategy parameterizes the shared process-ensuring flow so the
// ACP CLI subprocess backends and the Codex app-server backend can reuse common
// reuse/spawn/register scaffolding while keeping their distinct handshake and
// command-building protocols.
//
// Spawn returns the command (first element used as a fallback executable path
// for PID-reuse hardening in SetProcess), the started process, and the transport.
// Handshake runs the vendor-specific handshake over the transport and returns the
// session/thread id to store in the runtime pool. Both callbacks may wrap their
// errors with vendor-specific context so callers see protocol-specific messages.
type SpawnHandshakeStrategy struct {
	// Spawn produces a fresh subprocess + transport. It must not perform the
	// handshake; that is Handshake's responsibility, so the shared scaffolding can
	// close the transport uniformly on handshake failure.
	Spawn func() (cmd []string, proc Process, transport Transport, err error)

	// Handshake runs the vendor-specific initialize/session-or-thread exchange over
	// the transport and returns the session/thread id to register in the pool.
	Handshake func(ctx context.Context, transport Transport) (sessionID string, err error)

	// Log receives debug lifecycle messages; may be nil.
	Log *slog.Logger

	// LogPrefix prefixes debug log messages (e.g. "acp subprocess", "codex app-server").
	LogPrefix string

	// ProcessConfigKey identifies the process-scoped configuration requested by
	// the current call. A mismatch prevents reuse of the existing subprocess.
	ProcessConfigKey string
}

// EnsureProcessResult is the outcome of a shared ensure-process call. The
// transport is the live transport for the runtime; callers wrap it in their own
// vendor-specific client. SessionID is the ACP session id or Codex thread id.
type EnsureProcessResult struct {
	Transport Transport
	SessionID string
}

// ensureFlight is one in-progress process construction per serialized key. The
// elected leader writes err and closes done; followers only read it after done
// closes. The leader runs the construction in its own goroutine, so cancelling
// a follower detaches that follower alone.
type ensureFlight struct {
	done chan struct{}
	err  error
}

// EnsureProcess returns a live transport and session id for the runtime
// identified by key, reusing an existing initialized process when present, or
// spawning a fresh subprocess via strategy.Spawn and running strategy.Handshake
// otherwise. A stale or uninitialized runtime is killed first so goroutines and
// pipes from any prior process exit cleanly before a new one is spawned.
//
// Concurrent ensures for the same RuntimeKey are serialized on that key alone
// (not ProcessConfigKey): the pool slot is shared, and parallel constructions
// for different process configs would otherwise thrash KillRuntime/SetProcess in
// an unbounded retry loop. The first caller becomes the flight leader and runs
// the construction synchronously in its own goroutine; peers join as followers.
// After each flight, the caller re-checks the live pool; if a peer's config won
// the slot, the loop retries until this caller's ProcessConfigKey is live or ctx
// is cancelled. A follower may abandon its wait on ctx cancellation, but the
// leader's construction and handshake stay attached to the leader rather than
// running in a detached goroutine.
//
// On handshake failure the freshly created transport is closed (single-handling
// rule: the close error is logged at Debug, the originating handshake error is
// returned). On success the process/transport/sessionID are registered via
// SetProcess so the pool owns their lifecycle (idle reaping, stale kill, PID-reuse
// hardening).
func (p *RuntimePool) EnsureProcess(ctx context.Context, key RuntimeKey, strategy SpawnHandshakeStrategy) (EnsureProcessResult, error) {
	// Serialize on RuntimeKey: one ensure at a time for the shared pool slot.
	skey := fmt.Sprintf("%s\x00%s\x00%s", key.Workspace, key.Model, key.ClientSession)

	for {
		if err := ctx.Err(); err != nil {
			return EnsureProcessResult{}, err
		}
		if res, ok := p.ensureResultIfReady(key, strategy.ProcessConfigKey); ok {
			return res, nil
		}

		flight, leader := p.beginEnsureFlight(skey)
		if leader {
			_, err := p.ensureProcessFlight(ctx, key, strategy)
			p.finishEnsureFlight(skey, flight, err)
			if err != nil {
				return EnsureProcessResult{}, err
			}
		} else {
			select {
			case <-flight.done:
			case <-ctx.Done():
				return EnsureProcessResult{}, ctx.Err()
			}
			if flight.err != nil {
				return EnsureProcessResult{}, flight.err
			}
		}

		// Prefer the live pool state over the flight's value: KillRuntime may
		// have cleared the runtime after the flight returned, and a follower
		// may have joined a flight for a different ProcessConfigKey.
		if res, ok := p.ensureResultIfReady(key, strategy.ProcessConfigKey); ok {
			return res, nil
		}

		// The completed flight is no longer the map entry, so the next loop
		// iteration re-checks the pool and elects a new leader when this
		// caller's config still is not live.
	}
}

// ensureProcessFlight performs one construction for a serialized key. It runs
// synchronously in the elected leader's goroutine; followers never call it.
func (p *RuntimePool) ensureProcessFlight(ctx context.Context, key RuntimeKey, strategy SpawnHandshakeStrategy) (EnsureProcessResult, error) {
	// Re-check after election: a peer may have initialized the slot between the
	// caller's readiness check and the flight election.
	if res, ok := p.ensureResultIfReady(key, strategy.ProcessConfigKey); ok {
		return res, nil
	}

	// Kill any stale process. KillRuntimeContext closes the old
	// transport before killing, so the prior process's readLoop/drainStderr
	// goroutines exit cleanly, and the wait is bounded by the caller's
	// context: a process-tree cleanup that never confirms extinction must
	// not hold the request forever.
	if err := p.KillRuntimeContext(ctx, key); err != nil {
		return EnsureProcessResult{}, err
	}

	// Capacity is reserved before the spawn: the bound is on live agent
	// processes, so it is enforced where a process is created.
	release, err := p.reserveSpawn(ctx, key)
	if err != nil {
		return EnsureProcessResult{}, err
	}
	defer func() {
		if release != nil {
			release()
		}
	}()

	cmd, proc, transport, err := strategy.Spawn()
	if err != nil {
		if proc != nil || transport != nil {
			p.retireUnpublished(key, proc, transport, "")
		}
		return EnsureProcessResult{}, err
	}
	cmdFirst := ""
	if len(cmd) > 0 {
		cmdFirst = cmd[0]
	}
	if err := p.registerProcess(ctx, key, proc, transport, "", strategy.ProcessConfigKey, cmdFirst, false); err != nil {
		return EnsureProcessResult{}, err
	}
	// Registration transfers the construction reservation to a counted
	// process slot, including the entire handshake and failed cleanup.
	release()
	release = nil

	sid, err := strategy.Handshake(ctx, transport)
	if err != nil {
		return EnsureProcessResult{}, errors.Join(err, p.KillRuntimeContext(ctx, key))
	}
	if err := ctx.Err(); err != nil {
		return EnsureProcessResult{}, errors.Join(err, p.KillRuntimeContext(ctx, key))
	}
	rt := p.Get(key)
	rt.mu.Lock()
	if rt.retiring != nil || rt.proc == nil || rt.quarantined {
		rt.mu.Unlock()
		return EnsureProcessResult{}, errors.New("acp: process retired during handshake")
	}
	rt.spawning = false
	rt.initialized = true
	rt.sessionID = sid
	rt.mu.Unlock()
	return EnsureProcessResult{Transport: transport, SessionID: sid}, nil
}

// beginEnsureFlight returns the active construction for skey, creating it and
// electing the caller as leader when none is active.
func (p *RuntimePool) beginEnsureFlight(skey string) (*ensureFlight, bool) {
	p.ensureMu.Lock()
	defer p.ensureMu.Unlock()
	if flight := p.ensureFlights[skey]; flight != nil {
		return flight, false
	}
	flight := &ensureFlight{done: make(chan struct{})}
	p.ensureFlights[skey] = flight
	return flight, true
}

// finishEnsureFlight publishes the leader's result to its followers and removes
// the flight. Publication closes done under ensureMu, so a joining caller can
// never observe a flight that is absent from the map but not yet complete.
func (p *RuntimePool) finishEnsureFlight(skey string, flight *ensureFlight, err error) {
	p.ensureMu.Lock()
	defer p.ensureMu.Unlock()
	flight.err = err
	close(flight.done)
	if p.ensureFlights[skey] == flight {
		delete(p.ensureFlights, skey)
	}
}

// joinEnsureFlights waits for constructions admitted before the call. Shutdown
// uses it so a leader's construction, publication and refused-child cleanup are
// observed instead of outliving Close.
func (p *RuntimePool) joinEnsureFlights() {
	p.ensureMu.Lock()
	flights := make([]*ensureFlight, 0, len(p.ensureFlights))
	for _, flight := range p.ensureFlights {
		flights = append(flights, flight)
	}
	p.ensureMu.Unlock()
	for _, flight := range flights {
		<-flight.done
	}
}

// ensureResultIfReady returns the live pool transport when the runtime is
// initialized for wantConfig; otherwise ok is false.
func (p *RuntimePool) ensureResultIfReady(key RuntimeKey, wantConfig string) (EnsureProcessResult, bool) {
	rt := p.Get(key)
	if rt == nil || !rt.HasProcess() || !rt.IsInitialized() || rt.ProcessConfig() != wantConfig {
		return EnsureProcessResult{}, false
	}
	// A runtime whose cleanup is already known to be incomplete is not live:
	// refusing it here keeps a closed transport out of the caller's hands.
	if rt.cleanupOutcome() != nil {
		return EnsureProcessResult{}, false
	}
	t := rt.Transport()
	if t == nil {
		return EnsureProcessResult{}, false
	}
	return EnsureProcessResult{Transport: t, SessionID: rt.SessionID()}, true
}
