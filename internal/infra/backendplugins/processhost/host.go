package processhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"slices"
	"sync"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Config configures a process supervisor.
type Config struct {
	Launcher Launcher
	Channel  ChannelFactory
	WorkDir  string
	AllowEnv []string
	// OutputBudget bounds the diagnostics a connector generation may emit.
	// Zero values select the package defaults.
	OutputBudget OutputBudget
}

type instanceState int

const (
	instancePending instanceState = iota
	instanceConfigured
	instanceFailed
)

// Host supervises lazy plugin process activation and peer-gated configuration.
type Host struct {
	cfg Config

	mu        sync.Mutex
	closing   bool
	nextGen   uint64
	slots     map[string]*generationSlot
	retiring  map[uint64]*generationSlot // detached generations still undergoing physical teardown
	instances map[string]*instanceRec
	// flights holds the in-flight shared launch for each ownership key. A flight
	// outlives any single caller but is owned by the activations waiting on it:
	// the last withdrawal cancels its construction lifetime.
	flights    map[string]*launchFlight
	launchNote func()
	// breachNote observes a diagnostic-budget breach before the generation is
	// invalidated. It is a diagnostic seam: production leaves it unset.
	breachNote func()
	// Admission and Add share mu with Close. A generation stays owned while
	// launching or detached for cleanup, not just while present in slots.
	generations   sync.WaitGroup
	flightWorkers sync.WaitGroup
	shutdown      context.Context
	cancel        context.CancelFunc
}

// launchFlight is one in-flight shared construction for an ownership key.
//
// A flight outlives any single activation: a caller that withdraws stops only
// its own wait while surviving peers keep the launch alive. The construction
// itself is owned by the activations interested in it, counted in waiters. The
// cancellation in cancel therefore belongs to the flight, not to any request,
// and bounds the launch while it has published no generation.
//
// Publication transfers ownership from the flight to the generation: the
// published slot's release path owns the child from then on, so a later
// withdrawal settles the slot instead of canceling the launch.
type launchFlight struct {
	key    string
	cancel context.CancelFunc
	done   chan struct{}

	// The fields below are guarded by Host.mu. finished is set in the same
	// critical section that publishes the slot (or in the finish publication
	// for an outcome without a slot), so abandonment and publication cannot
	// both win. slot and err are written before done is closed and read after
	// receiving from it.
	waiters   int
	finished  bool
	abandoned bool
	slot      *generationSlot
	err       error
}

type generationSlot struct {
	key        string
	model      ProcessModel
	generation uint64
	proc       Process
	lis        Listener
	conn       net.Conn
	peer       PeerIdentity
	drain      *pipeDrain
	failed     bool
	reaped     bool
	reapDone   chan struct{}
	release    func()
	// terminalCause is why this generation ended abnormally. It is bound to
	// the slot that produced it: a replacement generation under the same
	// ownership key never inherits or overwrites it.
	terminalCause error

	rpcMu sync.Mutex // serializes DialAndConfigure on the shared conn
}

type instanceRec struct {
	id         string
	slotKey    string
	generation uint64
	state      instanceState
	closing    bool
	flight     *launchFlight
	err        error
	// terminalCause is the cause of the generation this activation owns. It
	// is set when the generation is terminated by its diagnostic budget, so
	// the caller learns why the generation ended instead of observing a
	// transport death.
	terminalCause error
	// drain is the diagnostic drain of the generation this activation owns.
	// Its breach record is the linearization point of the budget outcome:
	// the drain sets it before it notifies the host, so a breach it has
	// already recorded is reportable even when the asynchronous
	// invalidation has not run yet.
	drain  *pipeDrain
	result ActivateResult
	done   chan struct{} // closed on configured or failed
}

func NewHost(cfg Config) *Host {
	if cfg.Launcher == nil {
		cfg.Launcher = NewPlatformLauncher()
	}
	if cfg.Channel == nil {
		cfg.Channel = NewPlatformChannel()
	}
	shutdown, cancel := context.WithCancel(context.Background())
	return &Host{
		cfg:       cfg,
		slots:     map[string]*generationSlot{},
		retiring:  map[uint64]*generationSlot{},
		instances: map[string]*instanceRec{},
		flights:   map[string]*launchFlight{},
		shutdown:  shutdown,
		cancel:    cancel,
	}
}

func (h *Host) SetLaunchProbe(fn func()) {
	h.mu.Lock()
	h.launchNote = fn
	h.mu.Unlock()
}

// SetBudgetBreachProbe installs a probe that runs when the diagnostic drain
// records a budget breach, before the generation is invalidated.
//
// The invalidation is asynchronous: the drain records the breach on its own
// worker and notifies the host afterwards. A probe is therefore how a caller
// observes the breach before that invalidation runs, or holds it while it
// inspects the generation. It is a diagnostic seam: production leaves it
// unset.
func (h *Host) SetBudgetBreachProbe(fn func()) {
	h.mu.Lock()
	h.breachNote = fn
	h.mu.Unlock()
}

// ActivateRequest configures one instance against a verified artifact.
type ActivateRequest struct {
	InstanceID  string
	Artifact    *trust.VerifiedArtifact
	Model       ProcessModel
	Sharing     SharingOptions
	FactoryKind string
	ConfigYAML  []byte
	Secrets     backendplugin.SecretBundle
	// LaunchEnv is captured non-secret execution context; default empty.
	LaunchEnv   map[string]string
	Policy      backendplugin.RuntimePolicy
	ExpectedUID *int
	ExpectedSID string
	// DialAndConfigure runs only after peer authentication. Secrets are supplied
	// here by the host only after peer success (never via LaunchSpec).
	DialAndConfigure func(ctx context.Context, conn net.Conn, peer PeerIdentity, generation uint64, secrets backendplugin.SecretBundle, configYAML []byte) error
}

type ActivateResult struct {
	Generation uint64
	PID        int
	Peer       PeerIdentity
	Conn       net.Conn
	Cleanup    func() error
}

func (h *Host) Activate(ctx context.Context, req ActivateRequest) (ActivateResult, error) {
	if len(req.LaunchEnv) > 0 && req.Model == ProcessModelSharedArtifact {
		return ActivateResult{}, ReasonEnvBootstrapRejected
	}
	if _, err := appendActivationEnv(nil, req.LaunchEnv); err != nil {
		return ActivateResult{}, err
	}
	req.LaunchEnv = maps.Clone(req.LaunchEnv)
	req.ConfigYAML = bytes.Clone(req.ConfigYAML)
	req.Policy.AllowedEnvNames = slices.Clone(req.Policy.AllowedEnvNames)
	secrets := make(map[string][]byte, len(req.Secrets.Values))
	for name, value := range req.Secrets.Values {
		secrets[name] = bytes.Clone(value)
	}
	req.Secrets.Values = secrets
	if req.Artifact == nil {
		return ActivateResult{}, ReasonArtifactRequired
	}
	if req.InstanceID == "" {
		return ActivateResult{}, fmt.Errorf("instance id required")
	}
	if req.Model != ProcessModelPerInstance && req.Model != ProcessModelSharedArtifact {
		return ActivateResult{}, ReasonProcessModelViolation
	}
	if req.Model == ProcessModelSharedArtifact && (!req.Sharing.IsolationDeclared || !req.Sharing.ConcurrencyDeclared) {
		return ActivateResult{}, ReasonProcessModelViolation
	}
	if req.DialAndConfigure == nil {
		return ActivateResult{}, ReasonConfigureBeforePeer
	}
	req.Policy.DisableTransportRetries = true

	digest := req.Artifact.DigestHex
	key := OwnershipKey(req.Model, digest, req.InstanceID)

	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return ActivateResult{}, ReasonShuttingDown
	}
	if inst, ok := h.instances[req.InstanceID]; ok {
		switch inst.state {
		case instanceConfigured:
			res := inst.result
			breached := inst.breached()
			key, generation := inst.slotKey, inst.generation
			h.mu.Unlock()
			if breached {
				// The connector outran its diagnostic budget: the generation
				// is terminated and must not be served.
				_ = h.invalidateGeneration(key, generation, ReasonConnectorOutputLimitExceeded)
				return ActivateResult{}, ReasonConnectorOutputLimitExceeded
			}
			return res, nil
		case instanceFailed:
			err := inst.err
			h.mu.Unlock()
			if err == nil {
				err = ReasonGenerationInvalidated
			}
			return ActivateResult{}, err
		case instancePending:
			done := inst.done
			h.mu.Unlock()
			select {
			case <-ctx.Done():
				return ActivateResult{}, ctx.Err()
			case <-done:
			}
			h.mu.Lock()
			state, err, res := inst.state, inst.err, inst.result
			breached := inst.breached()
			key, generation := inst.slotKey, inst.generation
			h.mu.Unlock()
			if state == instanceConfigured {
				if breached {
					_ = h.invalidateGeneration(key, generation, ReasonConnectorOutputLimitExceeded)
					return ActivateResult{}, ReasonConnectorOutputLimitExceeded
				}
				return res, nil
			}
			if err == nil {
				err = ReasonGenerationInvalidated
			}
			return ActivateResult{}, err
		}
	}

	inst := &instanceRec{
		id:      req.InstanceID,
		slotKey: key,
		state:   instancePending,
		done:    make(chan struct{}),
	}
	h.instances[req.InstanceID] = inst
	flight := h.flights[key]
	if flight == nil {
		flightCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		flight = &launchFlight{key: key, cancel: cancel, done: make(chan struct{})}
		h.flights[key] = flight
		f := flight
		h.flightWorkers.Go(func() {
			slot, err := h.ensureGeneration(flightCtx, key, req, f)
			if err == nil {
				h.settleUnownedSlot(key, slot)
			}
			h.mu.Lock()
			f.finished = true
			f.slot, f.err = slot, err
			if h.flights[key] == f {
				delete(h.flights, key)
			}
			close(f.done)
			h.mu.Unlock()
			if err != nil {
				cancel()
			}
		})
	}
	flight.waiters++
	inst.flight = flight
	h.mu.Unlock()

	res, err := h.activatePending(ctx, key, req, inst, flight)
	if err != nil {
		h.failInstance(inst, err)
		// This activation stopped waiting after the shared launch had already
		// published its generation: with no other owner, the generation is
		// unowned and must be settled here rather than left running.
		h.settleUnownedSlot(key, nil)
		return ActivateResult{}, err
	}
	return res, nil
}

func (h *Host) activatePending(ctx context.Context, key string, req ActivateRequest, inst *instanceRec, flight *launchFlight) (ActivateResult, error) {
	defer func() {
		h.mu.Lock()
		h.withdrawFlightLocked(inst)
		h.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return ActivateResult{}, ctx.Err()
	case <-flight.done:
	}
	h.mu.Lock()
	instanceErr := inst.err
	h.mu.Unlock()
	if instanceErr != nil {
		return ActivateResult{}, instanceErr
	}
	if flight.err != nil {
		return ActivateResult{}, flight.err
	}
	slot := flight.slot
	// A pending owner may have been withdrawn while launch/singleflight was
	// blocked. Keep its cleanup obligation until the slot exists; release only
	// an exact slot with no remaining owners (including pending shared owners).
	defer h.reapUnownedSlot(slot)

	h.mu.Lock()
	if h.closing || slot.failed || h.instances[inst.id] != inst || inst.state != instancePending {
		cause := recordBudgetCause(slot, ReasonGenerationInvalidated)
		h.mu.Unlock()
		if cause != nil {
			return ActivateResult{}, cause
		}
		return ActivateResult{}, ReasonGenerationInvalidated
	}
	inst.generation = slot.generation
	inst.slotKey = key
	// The drain of this generation is the linearization point of its budget
	// outcome: the activation keeps it so a cleanup can report a breach the
	// drain recorded before the asynchronous invalidation ran.
	inst.drain = slot.drain
	h.mu.Unlock()

	slot.rpcMu.Lock()
	defer slot.rpcMu.Unlock()

	h.mu.Lock()
	if h.closing || slot.failed || inst.state != instancePending {
		state := inst.state
		res := inst.result
		ferr := inst.err
		h.mu.Unlock()
		if state == instanceConfigured {
			return res, nil
		}
		if ferr != nil {
			return ActivateResult{}, ferr
		}
		return ActivateResult{}, ReasonGenerationInvalidated
	}
	// A breach the drain has already recorded ends the generation before any
	// instance is configured against it. Other instances may share this
	// process: admitting a new configuration callback after the breach would
	// deliver credentials and run connector-side effects for a generation that
	// is already terminated.
	breached := slot.drain != nil && slot.drain.exceeded()
	h.mu.Unlock()

	if breached {
		// Invalidate outside the host lock, so the reap keeps its ordering.
		_ = h.invalidateGeneration(key, slot.generation, ReasonConnectorOutputLimitExceeded)
		return ActivateResult{}, ReasonConnectorOutputLimitExceeded
	}

	if err := req.DialAndConfigure(ctx, slot.conn, slot.peer, slot.generation, req.Secrets, req.ConfigYAML); err != nil {
		_ = h.invalidateGeneration(key, slot.generation, ReasonGenerationInvalidated)
		h.mu.Lock()
		cause := recordBudgetCause(slot, ReasonGenerationInvalidated)
		h.mu.Unlock()
		if cause != nil {
			// The connector outran its diagnostic budget while it was being
			// configured: the budget, not the handshake, ended the generation.
			return ActivateResult{}, cause
		}
		return ActivateResult{}, fmt.Errorf("%w: %v", ReasonHandshakeFailed, err)
	}

	pid := 0
	if slot.proc != nil {
		pid = slot.proc.PID()
	}
	res := ActivateResult{
		Generation: slot.generation,
		PID:        pid,
		Peer:       slot.peer,
		Conn:       slot.conn,
		Cleanup: func() error {
			return h.closeInstance(inst.id, inst)
		},
	}
	// Configuration can finish after invalidation or shutdown. Publish its
	// result only while this exact instance and slot still own the generation.
	h.mu.Lock()
	if h.closing || slot.failed || h.instances[inst.id] != inst || inst.state != instancePending {
		cause := recordBudgetCause(slot, ReasonGenerationInvalidated)
		h.mu.Unlock()
		if cause != nil {
			return ActivateResult{}, cause
		}
		return ActivateResult{}, ReasonGenerationInvalidated
	}
	if slot.drain != nil && slot.drain.exceeded() {
		// The connector outran its diagnostic budget while it was being
		// configured. The breach decides the outcome even though the
		// configuration itself succeeded and the asynchronous invalidation
		// has not run yet: the generation is terminated and the activation
		// fails with the budget reason. The invalidation runs outside the
		// host lock, so the reap keeps its established ordering.
		h.mu.Unlock()
		_ = h.invalidateGeneration(key, slot.generation, ReasonConnectorOutputLimitExceeded)
		return ActivateResult{}, ReasonConnectorOutputLimitExceeded
	}
	inst.state = instanceConfigured
	inst.result = res
	close(inst.done)
	h.mu.Unlock()
	return res, nil
}

// settleUnownedSlot withdraws a generation an activation stopped waiting for.
//
// A shared launch keeps running after every caller's context ends, so the slot
// it publishes can be left with no pending or configured owner. Such a
// generation is unreachable until shutdown: keeping its child process,
// listener, RPC connection and drain workers alive would leak them for as long
// as the host runs. The published slot is preferred when known, so a withdrawal
// is never charged to an unrelated replacement generation.
func (h *Host) settleUnownedSlot(key string, published *generationSlot) {
	slot := published
	if slot == nil {
		// No slot reached this activation: the launch publishes one later and
		// settles it itself, or another generation now serves the key.
		h.mu.Lock()
		slot = h.slots[key]
		h.mu.Unlock()
		if slot == nil {
			return
		}
	}
	// reapUnownedSlot keeps a live slot that still has owners, so a concurrent
	// claim preserves the generation: ownership is decided by the instance maps
	// under the host lock, never by the caller that happened to withdraw.
	h.reapUnownedSlot(slot)
}

func (h *Host) failInstance(inst *instanceRec, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if inst.state != instancePending {
		return
	}
	inst.state = instanceFailed
	inst.err = err
	close(inst.done)
}

func (h *Host) withdrawFlightLocked(inst *instanceRec) {
	f := inst.flight
	if f == nil {
		return
	}
	inst.flight = nil
	f.waiters--
	if f.waiters == 0 && !f.finished {
		f.abandoned = true
		if h.flights[f.key] == f {
			delete(h.flights, f.key)
		}
		f.cancel()
	}
}

func (h *Host) ensureGeneration(ctx context.Context, key string, req ActivateRequest, flight *launchFlight) (*generationSlot, error) {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return nil, ReasonShuttingDown
	}
	if flight.abandoned || h.flights[key] != flight {
		h.mu.Unlock()
		return nil, ReasonGenerationInvalidated
	}
	if slot, ok := h.slots[key]; ok && !slot.failed && slot.proc != nil && slot.conn != nil {
		flight.finished = true
		h.mu.Unlock()
		return slot, nil
	}
	h.nextGen++
	gen := h.nextGen
	note := h.launchNote
	h.generations.Add(1)
	h.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(h.shutdown, cancel)
	release := func() {
		stop()
		cancel()
		h.generations.Done()
	}
	published := false
	defer func() {
		if !published {
			release()
		}
	}()
	if note != nil {
		note()
	}

	lis, inherited, err := h.cfg.Channel.Listen(ctx, gen)
	if err != nil {
		return nil, err
	}
	channelFD := 0
	if len(inherited) > 0 {
		channelFD = 3 // ExtraFiles[0] → FD 3 in child
	}
	var extras []string
	if ep, ok := lis.(LaunchEnvProvider); ok {
		extras = ep.LaunchEnv()
	}
	env, err := buildLaunchEnv(h.cfg.AllowEnv, channelFD, extras)
	if err == nil {
		env, err = appendActivationEnv(env, req.LaunchEnv)
	}
	if err != nil {
		_ = lis.Close()
		closeFiles(inherited)
		return nil, err
	}
	spec := LaunchSpec{
		Artifact:   req.Artifact,
		WorkDir:    h.cfg.WorkDir,
		Env:        env,
		Generation: gen,
		ExtraFiles: inherited,
	}
	proc, err := h.cfg.Launcher.Launch(ctx, spec)
	closeFiles(inherited)
	if err != nil {
		_ = lis.Close()
		return nil, err
	}
	acceptCtx, cancelAccept := context.WithCancel(ctx)
	defer cancelAccept()
	// Drain the connector pipes for the whole process lifetime, starting before
	// the peer is authenticated: an unread pipe fills up and blocks the
	// connector's next write, which stalls the connector mid-request. The
	// drain also bounds the diagnostics the connector may emit: a connector
	// that outruns the budget is terminated and its generation dropped, so
	// the host never serves a connector that floods it.
	drain := startPipeDrain(proc, h.cfg.OutputBudget, func() {
		// The breach is observed. The invalidation below is asynchronous, so
		// the probe runs first: a caller that must see the breach before the
		// generation is torn down can hold the invalidation here.
		h.mu.Lock()
		note := h.breachNote
		h.mu.Unlock()
		if note != nil {
			note()
		}
		cancelAccept()
		_ = h.invalidateGeneration(key, gen, ReasonConnectorOutputLimitExceeded)
	})
	if ul, ok := lis.(interface{ SetExpectedPID(int) }); ok {
		ul.SetExpectedPID(proc.PID())
	}
	if req.ExpectedUID != nil {
		if ul, ok := lis.(interface{ SetExpectedUID(int) }); ok {
			ul.SetExpectedUID(*req.ExpectedUID)
		}
	}
	if req.ExpectedSID != "" {
		if ul, ok := lis.(interface{ SetExpectedSID(string) }); ok {
			ul.SetExpectedSID(req.ExpectedSID)
		}
	}
	if ul, ok := lis.(interface{ SetJobFromProcess(Process) }); ok {
		ul.SetJobFromProcess(proc)
	}
	conn, peer, err := lis.Accept(acceptCtx)
	if err != nil {
		stopLaunch(proc, drain)
		_ = lis.Close()
		if drain.exceeded() {
			return nil, ReasonConnectorOutputLimitExceeded
		}
		return nil, err
	}
	if err := h.authorizePeer(proc, gen, peer, req.ExpectedUID, req.ExpectedSID); err != nil {
		_ = conn.Close()
		stopLaunch(proc, drain)
		_ = lis.Close()
		return nil, err
	}
	if drain.exceeded() {
		// The connector outran its diagnostic budget before it was
		// configured: do not serve this generation.
		_ = conn.Close()
		stopLaunch(proc, drain)
		_ = lis.Close()
		return nil, ReasonConnectorOutputLimitExceeded
	}

	slot := &generationSlot{
		key: key, model: req.Model, generation: gen, proc: proc, lis: lis, conn: conn, peer: peer,
		drain:   drain,
		release: release,
	}
	h.mu.Lock()
	// Close snapshots h.slots under this mutex before reaping. Publishing a slot
	// after that snapshot would escape the reaper and leak the child process,
	// listener, and RPC connection, so admission and publication share Close's
	// critical section and fail closed once shutdown starts.
	if h.closing || flight.abandoned || h.flights[key] != flight {
		h.mu.Unlock()
		_ = conn.Close()
		_ = lis.Close()
		stopLaunch(proc, drain)
		return nil, ReasonGenerationInvalidated
	}
	h.slots[key] = slot
	flight.finished = true
	published = true // physical cleanup now owns the generation admission
	h.mu.Unlock()
	if drain.exceeded() {
		// The connector outran its budget before the slot was published:
		// drop the generation instead of serving it.
		_ = h.invalidateGeneration(key, gen, ReasonConnectorOutputLimitExceeded)
		return nil, ReasonConnectorOutputLimitExceeded
	}
	return slot, nil
}

// stopLaunch tears down a launch that never became a published slot. The child
// is terminated first, then the drain workers are stopped and joined, then the
// child is reaped: os/exec closes the parent's pipe descriptors inside
// Cmd.Wait, so the drain must finish before the wait to keep the documented
// read-before-wait ordering.
func stopLaunch(proc Process, drain *pipeDrain) {
	if proc != nil {
		_ = proc.SignalKill()
	}
	drain.stop()
	if proc != nil {
		_ = proc.Close()
	}
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

func (h *Host) authorizePeer(proc Process, gen uint64, peer PeerIdentity, expectedUID *int, expectedSID string) error {
	if peer.Generation != gen {
		return ReasonStaleGeneration
	}
	if peer.PID <= 0 {
		return ReasonPeerRejected
	}
	if !proc.ContainsPID(peer.PID) {
		if peer.PID == proc.PID() {
			return ReasonPIDReuse
		}
		return ReasonPeerRejected
	}
	if expectedUID != nil && peer.UID != *expectedUID {
		return ReasonPeerRejected
	}
	if expectedSID != "" && peer.SID != expectedSID {
		return ReasonPeerRejected
	}
	return nil
}

func (h *Host) InvalidateGeneration(key string) error {
	return h.invalidateGeneration(key, 0, ReasonGenerationInvalidated)
}

// invalidateGeneration fails a generation and its pending instances. A
// generation whose connector outran its diagnostic budget reports that reason
// instead: the budget, not a generic invalidation, is the cause.
//
// A non-zero generation scopes the invalidation to that generation: the
// diagnostic budget is reported asynchronously, and by the time the report
// lands a newer generation may own the key. The stale report must not tear
// the newer generation down.
//
// The budget outcome is retained as the generation's terminal cause and
// reported through the invalidation and cleanup surfaces, so the caller learns
// why the generation ended instead of observing a transport death.
func (h *Host) invalidateGeneration(key string, generation uint64, reason error) error {
	h.mu.Lock()
	if slot := h.retiring[generation]; generation != 0 && slot != nil && slot.key == key {
		// The generation is already detached: its terminal cause was decided
		// by the invalidation that detached it. A late report must not
		// retroactively rewrite that decision, so it joins the teardown
		// without mutating the cause.
		cause := slot.terminalCause
		h.mu.Unlock()
		return joinCause(cause, h.reapSlot(slot))
	}
	slot := h.slots[key]
	if slot == nil {
		h.mu.Unlock()
		return nil
	}
	if generation != 0 && slot.generation != generation {
		h.mu.Unlock()
		return nil
	}
	// One authoritative decision: it drives both the reported cause and the
	// error every pending instance records, so a caller and its waiters can
	// never observe different terminal classifications.
	cause := recordBudgetCause(slot, reason)
	if cause != nil {
		reason = cause
	}
	h.detachSlotLocked(slot)
	for id, inst := range h.instances {
		if inst.slotKey != key {
			continue
		}
		if cause != nil {
			inst.terminalCause = cause
		}
		// The generation's outcome is decided here: the instance keeps the
		// cause decided now, and clearing the drain reference stops a later
		// breach from retroactively rewriting it.
		inst.drain = nil
		if inst.state == instancePending {
			inst.state = instanceFailed
			inst.err = reason
			select {
			case <-inst.done:
			default:
				close(inst.done)
			}
		}
		delete(h.instances, id)
	}
	h.mu.Unlock()
	return joinCause(cause, h.reapSlot(slot))
}

// recordBudgetCause retains the terminal cause of a generation and reports it.
//
// Only the diagnostic budget produces a terminal cause: an administrative
// invalidation is a requested lifecycle event, not a failure to report.
//
// The budget is recognised two ways: the invalidation names it, or the
// generation's drain has already recorded the breach. The drain's record is
// the linearization point of the budget outcome: the drain sets it before it
// notifies the host, and that notification is asynchronous, so a breach the
// drain has already seen is reportable before the invalidation runs.
//
// Detachment finalizes the decision. Once the slot is detached, this reports
// the cause decided when it was detached and never rewrites it, so a late
// report joins the teardown instead of retroactively changing the outcome.
//
// The cause is bound to the slot that produced it, so a replacement
// generation under the same ownership key never inherits or overwrites it.
//
// The caller must hold h.mu.
func recordBudgetCause(slot *generationSlot, reason error) error {
	if slot == nil {
		return nil
	}
	if slot.failed {
		return slot.terminalCause
	}
	if slot.terminalCause == nil &&
		(errors.Is(reason, ReasonConnectorOutputLimitExceeded) ||
			(slot.drain != nil && slot.drain.exceeded())) {
		slot.terminalCause = ReasonConnectorOutputLimitExceeded
	}
	return slot.terminalCause
}

// instanceTerminalCause returns the terminal cause of the generation an
// activation owns.
//
// The drain's breach record is the linearization point of the budget
// outcome: the drain sets it before it notifies the host, and that
// notification is asynchronous. A breach the drain has already recorded is
// reported here even when the invalidation has not run yet, so a cleanup
// cannot report success for a generation the connector outran.
//
// The cause stays bound to the generation that produced it: a replacement
// generation under the same ownership key never inherits or overwrites it.
//
// The caller must hold h.mu.
func (h *Host) instanceTerminalCause(inst *instanceRec) error {
	if inst == nil {
		return nil
	}
	if inst.terminalCause == nil && inst.breached() {
		inst.terminalCause = ReasonConnectorOutputLimitExceeded
	}
	return inst.terminalCause
}

// breached reports whether the generation an activation owns has already
// recorded a diagnostic-budget breach. The drain's record is the
// linearization point of the budget outcome: the drain sets it before it
// notifies the host, and that notification is asynchronous, so a breach it
// has already recorded is reportable before the invalidation runs.
//
// The caller must hold h.mu.
func (i *instanceRec) breached() bool {
	return i != nil && i.drain != nil && i.drain.exceeded()
}

// joinCause combines a terminal cause with a teardown error. The cause stays
// matchable with errors.Is when the teardown itself succeeded.
func joinCause(cause, err error) error {
	switch {
	case cause == nil:
		return err
	case err == nil:
		return cause
	default:
		return errors.Join(cause, err)
	}
}

// InvalidateProcessGeneration invalidates only this process generation and
// waits for its physical teardown, including when another caller detached it
// first. A completed or unknown generation is a no-op.
func (h *Host) InvalidateProcessGeneration(generation uint64) error {
	h.mu.Lock()
	var key string
	for k, s := range h.slots {
		if s.generation == generation {
			key = k
			break
		}
	}
	if slot := h.retiring[generation]; slot != nil {
		key = slot.key
	}
	h.mu.Unlock()
	if key == "" {
		return nil
	}
	return h.invalidateGeneration(key, generation, ReasonGenerationInvalidated)
}

// CloseInstance administratively closes the current instance with this ID.
// Returned activation cleanup callbacks instead carry the exact incarnation.
// Pending withdrawal is immediate; the activation retains the obligation to
// reap any subsequently published slot with no surviving owners before returning.
func (h *Host) CloseInstance(id string) error {
	return h.closeInstance(id, nil)
}

func (h *Host) closeInstance(id string, expected *instanceRec) error {
	h.mu.Lock()
	inst, ok := h.instances[id]
	if !ok || (expected != nil && inst != expected) {
		// The instance incarnation is gone. Its generation may still have
		// ended for a reason the caller must learn.
		cause := h.instanceTerminalCause(expected)
		h.mu.Unlock()
		return cause
	}
	if inst.closing {
		h.mu.Unlock()
		return nil
	}
	inst.closing = true
	if inst.state == instancePending {
		h.withdrawFlightLocked(inst)
		inst.state = instanceFailed
		inst.err = ReasonGenerationInvalidated
		close(inst.done)
	}
	delete(h.instances, id)
	key := inst.slotKey
	slot := h.slots[key]
	if slot != nil && slot.generation != inst.generation {
		slot = nil
	}
	cause := h.instanceTerminalCause(inst)
	if cause != nil && slot != nil && slot.terminalCause == nil {
		slot.terminalCause = cause
	}
	reap := slot != nil && !h.slotHasOwnersLocked(slot)
	if reap {
		// Last-member withdrawal and detachment must be atomic: otherwise
		// a new instance could join the slot while its reaper is starting.
		h.detachSlotLocked(slot)
	}
	// The activation's outcome is decided here: a later breach on the same
	// generation cannot rewrite what this cleanup reports.
	inst.drain = nil
	h.mu.Unlock()
	if reap {
		return joinCause(cause, h.reapSlot(slot))
	}
	return cause
}

func (h *Host) Close() error {
	h.mu.Lock()
	h.closing = true
	slots := make([]*generationSlot, 0, len(h.slots))
	for _, s := range h.slots {
		slots = append(slots, s)
	}
	for _, inst := range h.instances {
		// Freeze the activation's outcome at the shutdown decision: a breach
		// the drain has already recorded is preserved, and clearing the drain
		// reference stops a breach during the subsequent teardown from
		// retroactively rewriting what the retained cleanup reports.
		_ = h.instanceTerminalCause(inst)
		inst.drain = nil
		if inst.state == instancePending {
			inst.state = instanceFailed
			inst.err = ReasonShuttingDown
			select {
			case <-inst.done:
			default:
				close(inst.done)
			}
		}
	}
	h.instances = map[string]*instanceRec{}
	h.mu.Unlock()
	h.cancel()
	var first error
	for _, slot := range slices.Backward(slots) {
		if err := h.reapSlot(slot); err != nil && first == nil {
			first = err
		}
	}
	h.flightWorkers.Wait()
	h.generations.Wait()
	return first
}

// detachSlotLocked withdraws only this physical incarnation. Reaping an old
// slot must never unregister a replacement published under the same key.
func (h *Host) detachSlotLocked(slot *generationSlot) {
	slot.failed = true
	h.retiring[slot.generation] = slot
	if h.slots[slot.key] == slot {
		delete(h.slots, slot.key)
	}
}

// slotHasOwnersLocked counts physical leases, not retained failed records.
// Only pending admissions may own a slot before being assigned its generation.
func (h *Host) slotHasOwnersLocked(slot *generationSlot) bool {
	for _, inst := range h.instances {
		if inst.closing || inst.slotKey != slot.key {
			continue
		}
		switch inst.state {
		case instancePending:
			if inst.generation == 0 || inst.generation == slot.generation {
				return true
			}
		case instanceConfigured:
			if inst.generation == slot.generation {
				return true
			}
		}
	}
	return false
}

// reapUnownedSlot settles a withdrawn activation even if it lost ownership
// before launch published. New pending owners protect a shared slot too.
func (h *Host) reapUnownedSlot(slot *generationSlot) {
	h.mu.Lock()
	if !slot.failed {
		if h.slotHasOwnersLocked(slot) {
			h.mu.Unlock()
			return
		}
		// Capture the pre-detachment outcome before the decision is frozen.
		_ = recordBudgetCause(slot, ReasonGenerationInvalidated)
		h.detachSlotLocked(slot)
	}
	h.mu.Unlock()
	_ = h.reapSlot(slot)
}

func (h *Host) reapSlot(slot *generationSlot) error {
	h.mu.Lock()
	if slot.reaped {
		done := slot.reapDone
		h.mu.Unlock()
		<-done
		return nil
	}
	slot.reaped = true
	slot.reapDone = make(chan struct{})
	// Capture the pre-detachment outcome before the decision is frozen.
	_ = recordBudgetCause(slot, ReasonGenerationInvalidated)
	h.detachSlotLocked(slot)
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.retiring, slot.generation)
		close(slot.reapDone)
		h.mu.Unlock()
		if slot.release != nil {
			slot.release()
		}
	}()
	if slot.conn != nil {
		_ = slot.conn.Close()
	}
	if slot.lis != nil {
		_ = slot.lis.Close()
	}
	if slot.proc != nil {
		_ = slot.proc.SignalKill()
	}
	if slot.drain != nil {
		slot.drain.stop()
	}
	if slot.proc != nil {
		_ = slot.proc.Close()
	}
	return nil
}
