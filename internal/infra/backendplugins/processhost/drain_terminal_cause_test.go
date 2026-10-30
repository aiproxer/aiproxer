package processhost

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	backendplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// dialOKInternal is the successful configure callback of the host tests.
func dialOKInternal(context.Context, net.Conn, PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
	return nil
}

// gatedFloodReader floods only after its gate opens, so a test can let the
// activation complete before the connector starts flooding. It never ends on
// its own: the host must terminate it.
type gatedFloodReader struct {
	gate      chan struct{}
	closed    chan struct{}
	openOnce  sync.Once
	closeOnce sync.Once
}

func newGatedFloodReader() *gatedFloodReader {
	return &gatedFloodReader{gate: make(chan struct{}), closed: make(chan struct{})}
}

// open starts the flood.
func (r *gatedFloodReader) open() { r.openOnce.Do(func() { close(r.gate) }) }

func (r *gatedFloodReader) Read(p []byte) (int, error) {
	select {
	case <-r.closed:
		return 0, io.EOF
	default:
	}
	select {
	case <-r.gate:
	case <-r.closed:
		return 0, io.EOF
	}
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func (r *gatedFloodReader) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return nil
}

// TestBudgetCause_PostActivationBreachIsReported proves the post-activation
// half of the budget contract: a connector that breaches its diagnostic
// budget after it was served is terminated, and the caller learns the budget
// reason through the cleanup surface instead of observing a transport death.
func TestBudgetCause_PostActivationBreachIsReported(t *testing.T) {
	t.Parallel()
	reader := newGatedFloodReader()
	reaped := make(chan struct{})
	launcher := &TestLauncher{PID: 9801, StdoutReader: reader, OnReap: func() { close(reaped) }}
	h := NewHost(Config{
		Launcher: launcher, Channel: &TestChannel{},
		// The first drained chunk outruns the burst allowance, so the
		// breach lands as soon as the connector starts flooding.
		OutputBudget: OutputBudget{BurstBytes: 1 << 10, BytesPerSecond: 1},
	})
	t.Cleanup(func() { _ = h.Close() })

	res, err := h.Activate(context.Background(), ActivateRequest{
		InstanceID:       "budget-after-activation",
		Artifact:         &trust.VerifiedArtifact{DigestHex: "budget-after-activation"},
		Model:            ProcessModelPerInstance,
		DialAndConfigure: dialOKInternal,
	})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}

	// The connector starts flooding only now, so the breach is observed
	// after the generation was served.
	reader.open()
	select {
	case <-reaped:
	case <-time.After(10 * time.Second):
		t.Fatal("the diagnostic budget did not terminate the flooding connector")
	}

	if err := res.Cleanup(); !errors.Is(err, ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("cleanup reported %v, want the diagnostic budget reason", err)
	}
}

// TestBudgetCause_ConfigureBreachReportsTheBudgetReason proves the
// configure-time half of the budget contract: a breach observed while the
// connector is being configured reports the budget reason, not the generic
// handshake failure.
func TestBudgetCause_ConfigureBreachReportsTheBudgetReason(t *testing.T) {
	t.Parallel()
	reader := newGatedFloodReader()
	reaped := make(chan struct{})
	launcher := &TestLauncher{PID: 9802, StdoutReader: reader, OnReap: func() { close(reaped) }}
	h := NewHost(Config{
		Launcher: launcher, Channel: &TestChannel{},
		OutputBudget: OutputBudget{BurstBytes: 1 << 10, BytesPerSecond: 1},
	})
	t.Cleanup(func() { _ = h.Close() })

	_, err := h.Activate(context.Background(), ActivateRequest{
		InstanceID: "budget-during-configure",
		Artifact:   &trust.VerifiedArtifact{DigestHex: "budget-during-configure"},
		Model:      ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			// The connector starts flooding while it is being configured, so
			// the budget terminates the generation under the configure.
			reader.open()
			select {
			case <-reaped:
			case <-time.After(10 * time.Second):
				return errors.New("the diagnostic budget did not terminate the flooding connector")
			}
			return errors.New("configure failed")
		},
	})
	if !errors.Is(err, ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("activate reported %v, want the diagnostic budget reason", err)
	}
	if errors.Is(err, ReasonHandshakeFailed) {
		t.Fatalf("activate reported the generic handshake failure: %v", err)
	}
}

// TestBudgetCause_CleanupBeforeInvalidationCallbackReportsTheCause proves the
// linearization rule of the budget outcome: the drain's breach record decides
// the cause, so a cleanup that runs before the asynchronous invalidation
// still reports the budget instead of success.
func TestBudgetCause_CleanupBeforeInvalidationCallbackReportsTheCause(t *testing.T) {
	t.Parallel()
	reader := newGatedFloodReader()
	breached := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseBreach := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseBreach)
	launcher := &TestLauncher{PID: 9804, StdoutReader: reader}
	h := NewHost(Config{
		Launcher: launcher, Channel: &TestChannel{},
		OutputBudget: OutputBudget{BurstBytes: 1 << 10, BytesPerSecond: 1},
	})
	t.Cleanup(func() { _ = h.Close() })
	// The probe holds the invalidation: the breach is recorded, but the
	// generation is not torn down by the budget before the cleanup runs.
	h.SetBudgetBreachProbe(func() {
		close(breached)
		<-release
	})

	res, err := h.Activate(context.Background(), ActivateRequest{
		InstanceID:       "budget-delayed-invalidation",
		Artifact:         &trust.VerifiedArtifact{DigestHex: "budget-delayed-invalidation"},
		Model:            ProcessModelPerInstance,
		DialAndConfigure: dialOKInternal,
	})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}

	reader.open()
	select {
	case <-breached:
	case <-time.After(10 * time.Second):
		t.Fatal("the diagnostic budget did not record the breach")
	}

	// The invalidation is still held, so the cause can only come from the
	// drain's breach record.
	if err := res.Cleanup(); !errors.Is(err, ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("cleanup reported %v, want the diagnostic budget reason", err)
	}
	releaseBreach()
}

// TestBudgetCause_SharedGenerationInstancesRetainTheCause proves that every
// activation of a shared generation retains the cause of that generation: one
// connector outran its budget, and every owner of the shared process learns
// it.
func TestBudgetCause_SharedGenerationInstancesRetainTheCause(t *testing.T) {
	t.Parallel()
	reader := newGatedFloodReader()
	reaped := make(chan struct{})
	launcher := &TestLauncher{PID: 9805, StdoutReader: reader, OnReap: func() { close(reaped) }}
	h := NewHost(Config{
		Launcher: launcher, Channel: &TestChannel{},
		OutputBudget: OutputBudget{BurstBytes: 1 << 10, BytesPerSecond: 1},
	})
	t.Cleanup(func() { _ = h.Close() })

	req := ActivateRequest{
		InstanceID:       "shared-budget-owner",
		Artifact:         &trust.VerifiedArtifact{DigestHex: "shared-budget"},
		Model:            ProcessModelSharedArtifact,
		Sharing:          SharingOptions{IsolationDeclared: true, ConcurrencyDeclared: true},
		DialAndConfigure: dialOKInternal,
	}
	first, err := h.Activate(context.Background(), req)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	other := req
	other.InstanceID = "shared-budget-peer"
	peer, err := h.Activate(context.Background(), other)
	if err != nil {
		t.Fatalf("activate peer: %v", err)
	}
	if peer.Generation != first.Generation {
		t.Fatalf("the owners did not share the generation: %d and %d", first.Generation, peer.Generation)
	}

	reader.open()
	select {
	case <-reaped:
	case <-time.After(10 * time.Second):
		t.Fatal("the diagnostic budget did not terminate the flooding connector")
	}

	for name, res := range map[string]ActivateResult{"owner": first, "peer": peer} {
		if err := res.Cleanup(); !errors.Is(err, ReasonConnectorOutputLimitExceeded) {
			t.Fatalf("%s cleanup reported %v, want the diagnostic budget reason", name, err)
		}
	}
}

// TestBudgetCause_ActivationFailsAfterRecordedBreach proves that a recorded
// breach decides the activation outcome even when the configuration itself
// succeeded: the drain's breach record is the linearization point, so the
// activation must not publish a served generation the connector outran.
func TestBudgetCause_ActivationFailsAfterRecordedBreach(t *testing.T) {
	t.Parallel()
	reader := newGatedFloodReader()
	breached := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseBreach := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseBreach)
	launcher := &TestLauncher{PID: 9806, StdoutReader: reader}
	h := NewHost(Config{
		Launcher: launcher, Channel: &TestChannel{},
		OutputBudget: OutputBudget{BurstBytes: 1 << 10, BytesPerSecond: 1},
	})
	t.Cleanup(func() { _ = h.Close() })
	// The probe holds the invalidation: the breach is recorded, but the
	// generation is not torn down by the budget before the activation
	// publishes its result.
	h.SetBudgetBreachProbe(func() {
		close(breached)
		<-release
	})

	_, err := h.Activate(context.Background(), ActivateRequest{
		InstanceID: "budget-breach-before-publication",
		Artifact:   &trust.VerifiedArtifact{DigestHex: "budget-breach-before-publication"},
		Model:      ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			// The connector floods while it is being configured.
			reader.open()
			select {
			case <-breached:
			case <-time.After(10 * time.Second):
				return errors.New("the diagnostic budget did not record the breach")
			}
			// The configuration itself succeeds.
			return nil
		},
	})
	if !errors.Is(err, ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("activate reported %v, want the diagnostic budget reason", err)
	}
	releaseBreach()
}

// TestBudgetCause_ConfiguredInstanceReuseFailsAfterRecordedBreach proves that
// a later activation cannot be served the generation whose budget was already
// breached: the recorded breach ends the generation even while the
// asynchronous invalidation is still pending.
func TestBudgetCause_ConfiguredInstanceReuseFailsAfterRecordedBreach(t *testing.T) {
	t.Parallel()
	reader := newGatedFloodReader()
	breached := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseBreach := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseBreach)
	launcher := &TestLauncher{PID: 9807, StdoutReader: reader}
	h := NewHost(Config{
		Launcher: launcher, Channel: &TestChannel{},
		OutputBudget: OutputBudget{BurstBytes: 1 << 10, BytesPerSecond: 1},
	})
	t.Cleanup(func() { _ = h.Close() })
	h.SetBudgetBreachProbe(func() {
		close(breached)
		<-release
	})

	res, err := h.Activate(context.Background(), ActivateRequest{
		InstanceID:       "budget-breach-before-reuse",
		Artifact:         &trust.VerifiedArtifact{DigestHex: "budget-breach-before-reuse"},
		Model:            ProcessModelPerInstance,
		DialAndConfigure: dialOKInternal,
	})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}

	// The connector floods after it was served, and the invalidation is held.
	reader.open()
	select {
	case <-breached:
	case <-time.After(10 * time.Second):
		t.Fatal("the diagnostic budget did not record the breach")
	}

	again, err := h.Activate(context.Background(), ActivateRequest{
		InstanceID:       "budget-breach-before-reuse",
		Artifact:         &trust.VerifiedArtifact{DigestHex: "budget-breach-before-reuse"},
		Model:            ProcessModelPerInstance,
		DialAndConfigure: dialOKInternal,
	})
	if !errors.Is(err, ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("reuse reported %v, want the diagnostic budget reason", err)
	}
	if again.Conn != nil {
		t.Fatal("the breached generation was served again")
	}
	releaseBreach()
	_ = res.Cleanup()
}

// TestBudgetCause_SharedGenerationAdmissionRejectsRecordedBreach proves that a
// new instance is never configured against a shared process whose budget was
// already breached: the configuration callback delivers credentials, so it
// must not run for a generation that is already terminated.
func TestBudgetCause_SharedGenerationAdmissionRejectsRecordedBreach(t *testing.T) {
	t.Parallel()
	reader := newGatedFloodReader()
	breached := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseBreach := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseBreach)
	launcher := &TestLauncher{PID: 9808, StdoutReader: reader}
	h := NewHost(Config{
		Launcher: launcher, Channel: &TestChannel{},
		OutputBudget: OutputBudget{BurstBytes: 1 << 10, BytesPerSecond: 1},
	})
	t.Cleanup(func() { _ = h.Close() })
	h.SetBudgetBreachProbe(func() {
		close(breached)
		<-release
	})

	first := ActivateRequest{
		InstanceID:       "shared-breach-owner",
		Artifact:         &trust.VerifiedArtifact{DigestHex: "shared-breach"},
		Model:            ProcessModelSharedArtifact,
		Sharing:          SharingOptions{IsolationDeclared: true, ConcurrencyDeclared: true},
		DialAndConfigure: dialOKInternal,
	}
	res, err := h.Activate(context.Background(), first)
	if err != nil {
		t.Fatalf("activate owner: %v", err)
	}

	// The connector floods after it was served, and the invalidation is held:
	// the generation is breached but still published.
	reader.open()
	select {
	case <-breached:
	case <-time.After(10 * time.Second):
		t.Fatal("the diagnostic budget did not record the breach")
	}

	// A new instance shares the artifact, so it would reuse the breached
	// generation if admission did not consult the drain's record.
	peer := first
	peer.InstanceID = "shared-breach-peer"
	var configured atomic.Int64
	peer.DialAndConfigure = func(context.Context, net.Conn, PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
		configured.Add(1)
		return nil
	}
	if _, err := h.Activate(context.Background(), peer); !errors.Is(err, ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("peer admission reported %v, want the diagnostic budget reason", err)
	}
	if configured.Load() != 0 {
		t.Fatal("the peer configuration callback ran against a generation whose budget was breached")
	}
	releaseBreach()
	_ = res.Cleanup()
}

// TestBudgetCause_LateReportDoesNotRewriteRetiringGeneration proves the
// precedence rule: the invalidation that detaches a generation decides its
// terminal cause, and a budget report that arrives while the physical teardown
// is still underway cannot retroactively rewrite it.
func TestBudgetCause_LateReportDoesNotRewriteRetiringGeneration(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		launcher := &TestLauncher{PID: 9809, OnReap: func() {
			close(entered)
			<-release
		}}
		h := NewHost(Config{Launcher: launcher, Channel: &TestChannel{}})
		defer func() { _ = h.Close() }()
		req := ActivateRequest{
			InstanceID:       "late-report-retiring",
			Artifact:         &trust.VerifiedArtifact{DigestHex: "late-report-retiring"},
			Model:            ProcessModelPerInstance,
			DialAndConfigure: dialOKInternal,
		}
		res, err := h.Activate(context.Background(), req)
		if err != nil {
			t.Fatalf("activate: %v", err)
		}
		key := OwnershipKey(req.Model, req.Artifact.DigestHex, req.InstanceID)

		// An administrative invalidation detaches the generation and the
		// physical teardown is still underway: the generation is retiring with
		// no decided budget cause.
		invalidated := make(chan error, 1)
		go func() { invalidated <- h.invalidateGeneration(key, res.Generation, ReasonGenerationInvalidated) }()
		<-entered

		// A late budget report arrives for the retiring generation. It joins
		// the teardown, so it is durably blocked until the teardown finishes.
		late := make(chan error, 1)
		go func() { late <- h.invalidateGeneration(key, res.Generation, ReasonConnectorOutputLimitExceeded) }()
		synctest.Wait()
		select {
		case err := <-late:
			t.Fatalf("the late report did not join the retiring teardown: %v", err)
		default:
		}

		close(release)
		if err := <-invalidated; err != nil {
			t.Fatalf("administrative invalidation reported %v", err)
		}
		if err := <-late; err != nil {
			t.Fatalf("the late report rewrote the retiring generation's cause: %v", err)
		}
		if err := res.Cleanup(); err != nil {
			t.Fatalf("the retiring generation reported %v after a late report", err)
		}
	})
}

// blockingKillLauncher holds the process termination so a test can record a
// budget breach between a generation's detachment and its drain shutdown.
type blockingKillLauncher struct {
	inner   Launcher
	entered chan struct{}
	release chan struct{}
}

func (l *blockingKillLauncher) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	proc, err := l.inner.Launch(ctx, spec)
	if err != nil || proc == nil {
		return proc, err
	}
	return &blockingKillProc{Process: proc, entered: l.entered, release: l.release}, nil
}

type blockingKillProc struct {
	Process
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingKillProc) SignalKill() error {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return p.Process.SignalKill()
}

// TestBudgetCause_DetachmentFinalizesCauseAgainstLateBreach proves the
// decision is final across every caller: a pending configuration that resumes
// after its generation was administratively detached reports the decided
// outcome, even when the drain records a breach during the teardown.
func TestBudgetCause_DetachmentFinalizesCauseAgainstLateBreach(t *testing.T) {
	t.Parallel()
	reader := newGatedFloodReader()
	breached := make(chan struct{})
	var breachOnce sync.Once
	killing := &blockingKillLauncher{
		inner:   &TestLauncher{PID: 9810, StdoutReader: reader},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	h := NewHost(Config{
		Launcher: killing,
		Channel:  &TestChannel{},
		// The first drained chunk outruns the burst allowance.
		OutputBudget: OutputBudget{BurstBytes: 1 << 10, BytesPerSecond: 1},
	})
	t.Cleanup(func() { _ = h.Close() })
	h.SetBudgetBreachProbe(func() { breachOnce.Do(func() { close(breached) }) })

	configureEntered := make(chan struct{})
	configureRelease := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := h.Activate(context.Background(), ActivateRequest{
			InstanceID: "detach-finalizes",
			Artifact:   &trust.VerifiedArtifact{DigestHex: "detach-finalizes"},
			Model:      ProcessModelPerInstance,
			DialAndConfigure: func(context.Context, net.Conn, PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
				close(configureEntered)
				<-configureRelease
				return errors.New("configure failed")
			},
		})
		finished <- err
	}()
	<-configureEntered

	// An administrative invalidation detaches the generation, finalizing its
	// cause as no cause at all.
	key := OwnershipKey(ProcessModelPerInstance, "detach-finalizes", "detach-finalizes")
	invalidated := make(chan error, 1)
	go func() { invalidated <- h.InvalidateGeneration(key) }()
	<-killing.entered // the teardown reached the termination, before the drain shutdown

	// The drain records a breach after the decision was finalized.
	reader.open()
	select {
	case <-breached:
	case <-time.After(10 * time.Second):
		t.Fatal("the diagnostic budget did not record the breach")
	}
	close(killing.release)
	if err := <-invalidated; err != nil {
		t.Fatalf("administrative invalidation reported %v", err)
	}

	// The pending configuration resumes and fails: the decided cause (none)
	// stands, so the failure stays a handshake failure.
	close(configureRelease)
	if err := <-finished; !errors.Is(err, ReasonHandshakeFailed) {
		t.Fatalf("the resumed activation reported %v, want the finalized outcome", err)
	}
}

// TestBudgetCause_InvalidationAndActivationAgree pins the observable contract
// of the single terminal decision: the invalidation result and the error the
// activation it fails observes are derived from the same cause, so they can
// never disagree.
func TestBudgetCause_InvalidationAndActivationAgree(t *testing.T) {
	t.Parallel()
	reader := newGatedFloodReader()
	breached := make(chan struct{})
	release := make(chan struct{})
	h := NewHost(Config{
		Launcher: &TestLauncher{PID: 9811, StdoutReader: reader},
		Channel:  &TestChannel{},
		// The first drained chunk outruns the burst allowance.
		OutputBudget: OutputBudget{BurstBytes: 1 << 10, BytesPerSecond: 1},
	})
	t.Cleanup(func() { _ = h.Close() })
	h.SetBudgetBreachProbe(func() { close(breached); <-release })

	configureEntered := make(chan struct{})
	configureRelease := make(chan struct{})
	first := make(chan error, 1)
	req := ActivateRequest{
		InstanceID: "decision-agrees",
		Artifact:   &trust.VerifiedArtifact{DigestHex: "decision-agrees"},
		Model:      ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			close(configureEntered)
			<-configureRelease
			return nil
		},
	}
	go func() { _, err := h.Activate(context.Background(), req); first <- err }()
	<-configureEntered

	// The connector floods: the breach is recorded, and its asynchronous
	// invalidation is held by the probe, so the generation is still live.
	reader.open()
	select {
	case <-breached:
	case <-time.After(10 * time.Second):
		t.Fatal("the diagnostic budget did not record the breach")
	}

	// One decision drives both surfaces.
	key := OwnershipKey(req.Model, req.Artifact.DigestHex, req.InstanceID)
	invalidated := h.InvalidateGeneration(key)
	close(configureRelease)
	close(release)
	activationErr := <-first

	if !errors.Is(invalidated, ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("invalidation reported %v, want the diagnostic budget reason", invalidated)
	}
	if !errors.Is(activationErr, invalidated) {
		t.Fatalf("the activation reported %v while the invalidation reported %v", activationErr, invalidated)
	}
}

// TestBudgetCause_ShutdownFinalizesCleanupAgainstLateBreach proves the
// shutdown decision is final for the cleanup surface: an administrative
// Close detaches the generation, and a budget breach the drain records during
// the subsequent teardown must not rewrite what the retained cleanup reports.
func TestBudgetCause_ShutdownFinalizesCleanupAgainstLateBreach(t *testing.T) {
	t.Parallel()
	reader := newGatedFloodReader()
	breached := make(chan struct{})
	var breachOnce sync.Once
	killing := &blockingKillLauncher{
		inner:   &TestLauncher{PID: 9812, StdoutReader: reader},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	h := NewHost(Config{
		Launcher: killing,
		Channel:  &TestChannel{},
		// The first drained chunk outruns the burst allowance.
		OutputBudget: OutputBudget{BurstBytes: 1 << 10, BytesPerSecond: 1},
	})
	t.Cleanup(func() { _ = h.Close() })
	h.SetBudgetBreachProbe(func() { breachOnce.Do(func() { close(breached) }) })

	res, err := h.Activate(context.Background(), ActivateRequest{
		InstanceID:       "shutdown-finalizes",
		Artifact:         &trust.VerifiedArtifact{DigestHex: "shutdown-finalizes"},
		Model:            ProcessModelPerInstance,
		DialAndConfigure: dialOKInternal,
	})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}

	// Administrative shutdown detaches the generation. The physical teardown
	// is held at the process termination, before the drain shutdown, so the
	// drain can still record a breach afterwards.
	closed := make(chan error, 1)
	go func() { closed <- h.Close() }()
	<-killing.entered

	// The drain records a breach after the shutdown decision was finalized.
	reader.open()
	select {
	case <-breached:
	case <-time.After(10 * time.Second):
		t.Fatal("the diagnostic budget did not record the breach")
	}
	close(killing.release)
	if err := <-closed; err != nil {
		t.Fatalf("shutdown reported %v", err)
	}

	// The shutdown was silent, and the late breach must not rewrite it.
	if err := res.Cleanup(); err != nil {
		t.Fatalf("cleanup reported %v after a late breach, want no cause", err)
	}
}

// TestBudgetCause_ShutdownPreservesPreShutdownBreach proves the other half of
// the shutdown freeze: a breach the drain recorded before the shutdown
// decision is preserved, so the retained cleanup still reports it.
func TestBudgetCause_ShutdownPreservesPreShutdownBreach(t *testing.T) {
	t.Parallel()
	reader := newGatedFloodReader()
	breached := make(chan struct{})
	release := make(chan struct{})
	h := NewHost(Config{
		Launcher: &TestLauncher{PID: 9813, StdoutReader: reader},
		Channel:  &TestChannel{},
		// The first drained chunk outruns the burst allowance.
		OutputBudget: OutputBudget{BurstBytes: 1 << 10, BytesPerSecond: 1},
	})
	t.Cleanup(func() { _ = h.Close() })
	// Hold the asynchronous invalidation so the generation is still live when
	// the shutdown runs: the breach is recorded, but nothing has detached yet.
	h.SetBudgetBreachProbe(func() { close(breached); <-release })

	res, err := h.Activate(context.Background(), ActivateRequest{
		InstanceID:       "shutdown-preserves",
		Artifact:         &trust.VerifiedArtifact{DigestHex: "shutdown-preserves"},
		Model:            ProcessModelPerInstance,
		DialAndConfigure: dialOKInternal,
	})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}

	// The connector floods: the breach is recorded before the shutdown.
	reader.open()
	select {
	case <-breached:
	case <-time.After(10 * time.Second):
		t.Fatal("the diagnostic budget did not record the breach")
	}

	closed := make(chan error, 1)
	go func() { closed <- h.Close() }()
	// The shutdown reap joins the drain; the held breach callback runs on its
	// own goroutine, so releasing it cannot deadlock the teardown.
	close(release)
	if err := <-closed; err != nil {
		t.Fatalf("shutdown reported %v", err)
	}

	if err := res.Cleanup(); !errors.Is(err, ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("cleanup reported %v, want the diagnostic budget reason", err)
	}
}

// TestBudgetCause_StaleReportDoesNotAffectTheReplacement proves the
// generation scope of the terminal cause: a stale report from a replaced
// generation never reaches the replacement that owns the same key.
func TestBudgetCause_StaleReportDoesNotAffectTheReplacement(t *testing.T) {
	t.Parallel()
	launcher := &TestLauncher{PID: 9803}
	h := NewHost(Config{Launcher: launcher, Channel: &TestChannel{}})
	t.Cleanup(func() { _ = h.Close() })
	req := ActivateRequest{
		InstanceID:       "budget-stale-report",
		Artifact:         &trust.VerifiedArtifact{DigestHex: "budget-stale-report"},
		Model:            ProcessModelPerInstance,
		DialAndConfigure: dialOKInternal,
	}
	first, err := h.Activate(context.Background(), req)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	key := OwnershipKey(req.Model, req.Artifact.DigestHex, req.InstanceID)

	// The budget terminates the first generation, and the reason is
	// reported through the invalidation surface.
	if err := h.invalidateGeneration(key, first.Generation, ReasonConnectorOutputLimitExceeded); !errors.Is(err, ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("the budget cause was not reported: %v", err)
	}

	// The replacement takes the ownership key.
	second, err := h.Activate(context.Background(), req)
	if err != nil {
		t.Fatalf("activate replacement: %v", err)
	}
	if second.Generation == first.Generation {
		t.Fatal("the replacement reused the terminated generation")
	}

	// A stale report for the replaced generation must not touch the
	// replacement.
	_ = h.invalidateGeneration(key, first.Generation, ReasonConnectorOutputLimitExceeded)

	// The replacement is still the live generation: a further activation
	// reuses it instead of launching a new connector.
	again, err := h.Activate(context.Background(), req)
	if err != nil {
		t.Fatalf("activate again: %v", err)
	}
	if again.Generation != second.Generation || launcher.Launches.Load() != 2 {
		t.Fatalf("the stale report dropped the replacement: gen %d launches %d",
			again.Generation, launcher.Launches.Load())
	}
	if err := second.Cleanup(); err != nil {
		t.Fatalf("the replacement inherited the stale cause: %v", err)
	}
	if err := first.Cleanup(); !errors.Is(err, ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("the replaced generation lost its cause: %v", err)
	}
}
