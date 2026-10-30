package processhost

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func ownershipRequest(model ProcessModel) ActivateRequest {
	return ActivateRequest{
		InstanceID: "owner", Artifact: &trust.VerifiedArtifact{DigestHex: "owner"}, Model: model,
		Sharing: SharingOptions{IsolationDeclared: true, ConcurrencyDeclared: true},
		DialAndConfigure: func(context.Context, net.Conn, PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			return nil
		},
	}
}

func activateOwner(t *testing.T, h *Host, req ActivateRequest) ActivateResult {
	t.Helper()
	res, err := h.Activate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// #1337: the old slot is already failed when a replacement is published;
// finishing its reap must not unregister that replacement from shutdown.
func TestReapSlot_OldSlotCannotUnregisterReplacement(t *testing.T) {
	t.Parallel()
	launcher := &TestLauncher{}
	h := NewHost(Config{Launcher: launcher, Channel: &TestChannel{}})
	t.Cleanup(func() { _ = h.Close() })
	req := ownershipRequest(ProcessModelPerInstance)
	old := activateOwner(t, h, req)
	key := OwnershipKey(req.Model, req.Artifact.DigestHex, req.InstanceID)
	h.mu.Lock()
	oldSlot := h.slots[key]
	oldSlot.failed = true
	delete(h.instances, req.InstanceID)
	h.mu.Unlock()
	current := activateOwner(t, h, req)
	currentProc, ok := launcher.LastProcess.(*testProc)
	if !ok {
		t.Fatalf("unexpected process %T", launcher.LastProcess)
	}
	if current.Generation == old.Generation {
		t.Fatal("replacement reused the failed generation")
	}
	if err := h.reapSlot(oldSlot); err != nil {
		t.Fatal(err)
	}
	if err := h.InvalidateProcessGeneration(current.Generation); err != nil {
		t.Fatal(err)
	}
	if !currentProc.Killed() || !currentProc.Waited() {
		t.Fatal("old reap unregistered the replacement: invalidation missed it")
	}
	if got := activateOwner(t, h, req); got.Generation == current.Generation {
		t.Fatal("old reap unregistered the replacement: invalidation missed it")
	}
}

// Cleanup is incarnation-scoped even if an ID is reused within the same
// shared process generation, not just after a process restart.
func TestCleanup_OldInstanceCannotCloseReusedID(t *testing.T) {
	t.Parallel()
	for _, model := range []ProcessModel{ProcessModelPerInstance, ProcessModelSharedArtifact} {
		t.Run(string(model), func(t *testing.T) {
			h := NewHost(Config{Launcher: &TestLauncher{}, Channel: &TestChannel{}})
			t.Cleanup(func() { _ = h.Close() })
			req := ownershipRequest(model)
			var configured atomic.Int64
			req.DialAndConfigure = func(context.Context, net.Conn, PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
				configured.Add(1)
				return nil
			}
			old := activateOwner(t, h, req)
			if model == ProcessModelSharedArtifact {
				other := req
				other.InstanceID = "other-owner"
				activateOwner(t, h, other)
			}
			if err := old.Cleanup(); err != nil {
				t.Fatal(err)
			}
			current := activateOwner(t, h, req)
			before := configured.Load()
			if err := old.Cleanup(); err != nil {
				t.Fatal(err)
			}
			again := activateOwner(t, h, req)
			if again.Conn != current.Conn || configured.Load() != before {
				t.Fatal("stale cleanup released the replacement")
			}
		})
	}
}

// The old handshake resumes only after G2 has configured successfully.
// Neither a late failure nor a late success may disturb or resurrect G1.
func TestHandshake_CompletionCannotAffectReplacement(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := NewHost(Config{Launcher: &TestLauncher{}, Channel: &TestChannel{}})
				defer func() { _ = h.Close() }()
				req := ownershipRequest(ProcessModelPerInstance)
				entered, release := make(chan uint64, 1), make(chan struct{})
				oldReq := req
				oldReq.DialAndConfigure = func(_ context.Context, _ net.Conn, _ PeerIdentity, gen uint64, _ backendplugin.SecretBundle, _ []byte) error {
					entered <- gen
					<-release
					if fail {
						return errors.New("old handshake failed")
					}
					return nil
				}
				finished := make(chan error, 1)
				go func() { _, err := h.Activate(context.Background(), oldReq); finished <- err }()
				gen := <-entered
				if err := h.InvalidateProcessGeneration(gen); err != nil {
					t.Fatal(err)
				}
				current := activateOwner(t, h, req)
				close(release)
				if err := <-finished; err == nil {
					t.Error("invalidated handshake returned success")
				}
				again := activateOwner(t, h, req)
				if again.Generation != current.Generation || again.Conn != current.Conn {
					t.Fatal("old handshake invalidated the replacement")
				}
			})
		})
	}
}

// Shutdown must join a detached generation whose physical cleanup is still
// running, while also reaping its replacement exactly once.
func TestClose_JoinsDetachedCleanupAndReplacement(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var reaps atomic.Int64
		launcher := &TestLauncher{OnReap: func() {
			if reaps.Add(1) == 1 {
				close(entered)
				<-release
			}
		}}
		h := NewHost(Config{Launcher: launcher, Channel: &TestChannel{}})
		req := ownershipRequest(ProcessModelPerInstance)
		old := activateOwner(t, h, req)
		invalidated := make(chan struct{})
		go func() { _ = h.InvalidateProcessGeneration(old.Generation); close(invalidated) }()
		<-entered
		current := activateOwner(t, h, req)
		if current.Generation == old.Generation {
			t.Fatal("failed generation reused")
		}
		closed := make(chan struct{})
		go func() { _ = h.Close(); close(closed) }()
		synctest.Wait()
		select {
		case <-closed:
			t.Error("Close returned while detached cleanup was still running")
		default:
		}
		close(release)
		<-invalidated
		<-closed
		if reaps.Load() != 2 {
			t.Fatalf("want both generations reaped once, got %d", reaps.Load())
		}
	})
}

// A launched connector may never dial. Shutdown must cancel that admission,
// release its pipes and reap it rather than leave an unreachable child behind.
func TestClose_CancelsUnpublishedPeerAccept(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		launcher := &TestLauncher{}
		h := NewHost(Config{Launcher: launcher, Channel: waitingPeerChannel{entered: entered}})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		finished := make(chan error, 1)
		go func() { _, err := h.Activate(ctx, ownershipRequest(ProcessModelPerInstance)); finished <- err }()
		<-entered
		closed := make(chan struct{})
		go func() { _ = h.Close(); close(closed) }()
		synctest.Wait()
		select {
		case err := <-finished:
			if err == nil {
				t.Error("unpublished activation succeeded during shutdown")
			}
		default:
			t.Error("shutdown left peer acceptance blocked")
			cancel() // unwind even when the regression is present
		}
		<-closed
		cancel()
		synctest.Wait()
		proc, ok := launcher.LastProcess.(*testProc)
		if !ok {
			t.Fatalf("unexpected process %T", launcher.LastProcess)
		}
		if !proc.Killed() || !proc.Waited() {
			t.Fatal("shutdown did not terminate and reap the unpublished connector")
		}
	})
}

type waitingPeerChannel struct{ entered chan struct{} }

// #1337: a failed launch record is diagnostic state, not a process lease.
func TestSharedOwnership_FailedLaunchDoesNotRetainLaterProcess(t *testing.T) {
	t.Parallel()
	for _, withdraw := range []bool{false, true} {
		t.Run(map[bool]string{false: "configured-cleanup", true: "pending-withdrawal"}[withdraw], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				launchErr := errors.New("first launch failed")
				launcher := &TestLauncher{Fail: launchErr}
				h := NewHost(Config{Launcher: launcher, Channel: &TestChannel{}})
				defer func() { _ = h.Close() }()
				req := ownershipRequest(ProcessModelSharedArtifact)
				if _, err := h.Activate(context.Background(), req); !errors.Is(err, launchErr) {
					t.Fatalf("first launch: %v", err)
				}
				launcher.Fail = nil
				req.InstanceID = "later-owner"
				if withdraw {
					entered, release := make(chan struct{}), make(chan struct{})
					launcher.OnLaunch = func(LaunchSpec) { close(entered); <-release }
					finished := make(chan error, 1)
					go func() { _, err := h.Activate(context.Background(), req); finished <- err }()
					<-entered
					if err := h.CloseInstance(req.InstanceID); err != nil {
						t.Fatal(err)
					}
					close(release)
					if err := <-finished; !errors.Is(err, ReasonGenerationInvalidated) {
						t.Fatalf("withdrawn activation: %v", err)
					}
				} else {
					res := activateOwner(t, h, req)
					if err := res.Cleanup(); err != nil {
						t.Fatal(err)
					}
				}
				proc, ok := launcher.LastProcess.(*testProc)
				if !ok {
					t.Fatalf("unexpected process %T", launcher.LastProcess)
				}
				if !proc.Killed() || !proc.Waited() {
					t.Fatal("failed launch record retained an otherwise unowned process")
				}
			})
		})
	}
}

func TestSharedOwnership_PendingConfigureRetainsProcess(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		launcher := &TestLauncher{}
		h := NewHost(Config{Launcher: launcher, Channel: &TestChannel{}})
		defer func() { _ = h.Close() }()
		req := ownershipRequest(ProcessModelSharedArtifact)
		first := activateOwner(t, h, req)
		entered, release := make(chan struct{}), make(chan struct{})
		req.InstanceID = "pending-owner"
		req.DialAndConfigure = func(context.Context, net.Conn, PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			close(entered)
			<-release
			return nil
		}
		result, finished := make(chan ActivateResult, 1), make(chan error, 1)
		go func() {
			res, err := h.Activate(context.Background(), req)
			result <- res
			finished <- err
		}()
		<-entered
		if err := first.Cleanup(); err != nil {
			t.Fatal(err)
		}
		proc, ok := launcher.LastProcess.(*testProc)
		if !ok {
			t.Fatalf("unexpected process %T", launcher.LastProcess)
		}
		if proc.Killed() || proc.Waited() {
			t.Error("configured owner cleanup reaped a genuinely pending shared owner's process")
		}
		close(release)
		res := <-result
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
		if res.Generation != first.Generation {
			t.Fatal("pending owner did not retain the shared generation")
		}
		if err := res.Cleanup(); err != nil {
			t.Fatal(err)
		}
		if !proc.Killed() || !proc.Waited() {
			t.Fatal("last shared owner failed to reap its process")
		}
	})
}

// A withdrawal before publication must be settled by the activation, without
// relying on global shutdown. Other shared owners must keep their generation.
func TestCloseInstance_PendingLaunchSettlesOwnership(t *testing.T) {
	t.Parallel()
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "last-owner", true: "shared-survivor"}[shared], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				launcher := &TestLauncher{OnLaunch: func(LaunchSpec) { close(entered); <-release }}
				h := NewHost(Config{Launcher: launcher, Channel: &TestChannel{}})
				defer func() { _ = h.Close() }()
				req := ownershipRequest(ProcessModelPerInstance)
				if shared {
					req.Model = ProcessModelSharedArtifact
				}
				finished := make(chan error, 1)
				go func() { _, err := h.Activate(context.Background(), req); finished <- err }()
				<-entered
				otherResult := make(chan ActivateResult, 1)
				otherErr := make(chan error, 1)
				if shared {
					other := req
					other.InstanceID = "survivor"
					go func() {
						res, err := h.Activate(context.Background(), other)
						otherResult <- res
						otherErr <- err
					}()
					synctest.Wait() // second owner is registered and waiting on the launch
				}
				if err := h.CloseInstance(req.InstanceID); err != nil {
					t.Fatal(err)
				}
				close(release)
				if err := <-finished; !errors.Is(err, ReasonGenerationInvalidated) {
					t.Fatalf("withdrawn activation: %v", err)
				}
				proc, ok := launcher.LastProcess.(*testProc)
				if !ok {
					t.Fatalf("unexpected process %T", launcher.LastProcess)
				}
				if !shared {
					if !proc.Killed() || !proc.Waited() {
						t.Fatal("withdrawn pending activation left an unowned process alive")
					}
					return
				}
				res := <-otherResult
				if err := <-otherErr; err != nil {
					t.Fatal(err)
				}
				if proc.Killed() || launcher.Launches.Load() != 1 {
					t.Fatal("withdrawal destroyed the surviving shared owner's process")
				}
				if err := res.Cleanup(); err != nil {
					t.Fatal(err)
				}
				if !proc.Killed() || !proc.Waited() {
					t.Fatal("last shared owner did not reap its process")
				}
			})
		})
	}
}

// A stale callback for detached G1 must join G1's cleanup, not return early
// or invalidate G2, which now owns the same routing key.
func TestInvalidateProcessGeneration_DetachedCallbackJoinsOnlyItsGeneration(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var reaps atomic.Int64
		launcher := &TestLauncher{OnReap: func() {
			if reaps.Add(1) == 1 {
				close(entered)
				<-release
			}
		}}
		h := NewHost(Config{Launcher: launcher, Channel: &TestChannel{}})
		defer func() { _ = h.Close() }()
		req := ownershipRequest(ProcessModelPerInstance)
		old := activateOwner(t, h, req)
		first := make(chan struct{})
		go func() { _ = h.InvalidateProcessGeneration(old.Generation); close(first) }()
		<-entered
		current := activateOwner(t, h, req)
		proc, ok := launcher.LastProcess.(*testProc)
		if !ok {
			t.Fatalf("unexpected process %T", launcher.LastProcess)
		}
		second := make(chan struct{})
		go func() { _ = h.InvalidateProcessGeneration(old.Generation); close(second) }()
		synctest.Wait()
		select {
		case <-second:
			t.Error("concurrent invalidation returned before its detached reap completed")
		default:
		}
		close(release)
		<-first
		<-second
		if err := h.InvalidateProcessGeneration(old.Generation); err != nil {
			t.Fatal(err)
		}
		if proc.Killed() || activateOwner(t, h, req).Generation != current.Generation {
			t.Fatal("stale callback disturbed the replacement generation")
		}
		if err := current.Cleanup(); err != nil {
			t.Fatal(err)
		}
		if !proc.Killed() || !proc.Waited() {
			t.Fatal("replacement's own cleanup did not reap it")
		}
	})
}

func (c waitingPeerChannel) Listen(ctx context.Context, generation uint64) (Listener, []*os.File, error) {
	base := &TestChannel{}
	lis, files, err := base.Listen(ctx, generation)
	return waitingPeerListener{Listener: lis, entered: c.entered}, files, err
}

type waitingPeerListener struct {
	Listener
	entered chan struct{}
}

func (l waitingPeerListener) Accept(ctx context.Context) (net.Conn, PeerIdentity, error) {
	close(l.entered)
	<-ctx.Done()
	return nil, PeerIdentity{}, ctx.Err()
}
