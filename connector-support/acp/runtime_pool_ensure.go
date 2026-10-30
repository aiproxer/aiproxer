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

// EnsureProcess returns a live transport and session id for the runtime
// identified by key, reusing an existing initialized process when present, or
// spawning a fresh subprocess via strategy.Spawn and running strategy.Handshake
// otherwise. A stale or uninitialized runtime is killed first so goroutines and
// pipes from any prior process exit cleanly before a new one is spawned.
//
// Concurrent ensures for the same RuntimeKey are serialized on that key alone
// (not ProcessConfigKey): the pool slot is shared, and parallel flights for
// different process configs would otherwise thrash KillRuntime/SetProcess in an
// unbounded retry loop. After each flight, the caller re-checks the live pool;
// if a peer's config won the slot, Forget clears the flight entry and the loop
// retries until this caller's ProcessConfigKey is live or ctx is cancelled.
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

		_, err, _ := p.ensureGroup.Do(skey, func() (any, error) {
			// Re-check after acquiring the singleflight lock.
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
		})
		if err != nil {
			return EnsureProcessResult{}, err
		}

		// Prefer the live pool state over the shared flight value: another config
		// may have owned the RuntimeKey flight, or KillRuntime may have cleared it
		// after Do returned.
		if res, ok := p.ensureResultIfReady(key, strategy.ProcessConfigKey); ok {
			return res, nil
		}

		// Drop the flight entry so the next Do re-enters spawn/handshake instead of
		// waiting on / returning a completed peer result that does not match us.
		p.ensureGroup.Forget(skey)
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
