package host_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"go.uber.org/goleak"
)

const sessionConcurrencyGuard = time.Second

// verifyNoGoroutineLeak snapshots current goroutines once, then polls
// goleak.Find until the teardown settles or a 5s deadline passes. gRPC conn
// teardown spawns short-lived addrConn/CallbackSerializer goroutines after
// the snapshot, and goleak's built-in retry window is too short on loaded CI
// runners. The assertion is unchanged: goroutines missing from the snapshot
// after the deadline still fail; only the settle patience grows.
func verifyNoGoroutineLeak(t *testing.T) {
	t.Helper()
	ignore := goleak.IgnoreCurrent()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := goleak.Find(ignore); err == nil {
			return
		}
		if time.Now().After(deadline) {
			goleak.VerifyNone(t, ignore)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestSession_CrossGenerationExecuteSerializesOnOneSession characterizes the
// existing Session lifecycle lock that a pooled Session would share across
// retained old-generation and new-generation execution.
//
//nolint:paralleltest // goleak VerifyNone conflicts with parallel tests
func TestSession_CrossGenerationExecuteSerializesOnOneSession(t *testing.T) {
	t.Cleanup(func() { verifyNoGoroutineLeak(t) })

	session, plugin := newCrossGenerationSession(t)

	old := &publicStream{
		ctx: context.Background(),
		frames: []backendplugin.ClientFrame{{
			Kind: backendplugin.ClientFrameStart, InstanceID: "cross-generation", Invocation: validInvocation(),
		}},
	}
	oldDone := make(chan error, 1)
	go func() { oldDone <- session.Execute(old) }()
	waitForSignal(t, plugin.executeStarted, "old-generation Execute")

	secondContextCalled := make(chan struct{})
	second := &contextProbeStream{
		publicStream: publicStream{
			ctx: context.Background(),
			frames: []backendplugin.ClientFrame{{
				Kind: backendplugin.ClientFrameStart, InstanceID: "cross-generation", Invocation: validInvocation(),
			}},
		},
		contextCalled: secondContextCalled,
	}
	secondAttempted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondAttempted)
		secondDone <- session.Execute(second)
	}()
	waitForSignal(t, secondAttempted, "new-generation Execute goroutine before Session.Execute")

	assertNoSignal(t, secondContextCalled, "new-generation Execute entered Session while old Execute was held")

	plugin.releaseExecute()
	if err := waitForResult(t, oldDone, "old-generation Execute"); err != nil {
		t.Fatalf("old-generation Execute: %v", err)
	}
	if err := waitForResult(t, secondDone, "new-generation Execute"); err != nil {
		t.Fatalf("new-generation Execute: %v", err)
	}
	if plugin.executeCalls.Load() != 2 {
		t.Fatalf("plugin Execute calls=%d, want 2 after the first call released", plugin.executeCalls.Load())
	}
}

// TestSession_CrossGenerationMetadataOverlapsHeldExecute characterizes the
// existing standard-host contract: server-side instance leases permit the
// metadata and auxiliary RPCs to overlap an active Execute on one Session.
//
//nolint:paralleltest // goleak VerifyNone conflicts with parallel tests
func TestSession_CrossGenerationMetadataOverlapsHeldExecute(t *testing.T) {
	t.Cleanup(func() { verifyNoGoroutineLeak(t) })

	session, plugin := newCrossGenerationSession(t)

	old := &publicStream{
		ctx: context.Background(),
		frames: []backendplugin.ClientFrame{{
			Kind: backendplugin.ClientFrameStart, InstanceID: "cross-generation", Invocation: validInvocation(),
		}},
	}
	oldDone := make(chan error, 1)
	go func() { oldDone <- session.Execute(old) }()
	waitForSignal(t, plugin.executeStarted, "held Execute")
	plugin.probeEnabled.Store(true)

	resolveDone := make(chan error, 1)
	listModelsDone := make(chan error, 1)
	countTokensDone := make(chan error, 1)
	finalizeBillingDone := make(chan error, 1)
	go func() {
		_, err := session.Resolve(context.Background(), nil)
		resolveDone <- err
	}()
	go func() {
		_, err := session.ListModels(context.Background(), 16)
		listModelsDone <- err
	}()
	go func() {
		_, err := session.CountTokens(context.Background(), backendplugin.CountTokensRequest{
			InstanceID: "cross-generation", ModelID: "model", Invocation: *validInvocation(),
		})
		countTokensDone <- err
	}()
	go func() {
		_, err := session.FinalizeBilling(context.Background(), backendplugin.FinalizeBillingRequest{
			InstanceID: "cross-generation", ALegID: "a-leg", BLegID: "b-leg", ModelID: "model", IdempotencyKey: "cross-generation-idem",
		})
		finalizeBillingDone <- err
	}()

	waitForSignal(t, plugin.resolveEntered, "Resolve alongside held Execute")
	waitForSignal(t, plugin.listModelsEntered, "ListModels alongside held Execute")
	waitForSignal(t, plugin.countTokensEntered, "CountTokens alongside held Execute")
	waitForSignal(t, plugin.finalizeBillingEntered, "FinalizeBilling alongside held Execute")
	assertNoSignal(t, plugin.executeReleased, "held Execute released before metadata and auxiliary operations completed")

	for name, done := range map[string]<-chan error{
		"Resolve":         resolveDone,
		"ListModels":      listModelsDone,
		"CountTokens":     countTokensDone,
		"FinalizeBilling": finalizeBillingDone,
	} {
		if err := waitForResult(t, done, name); err != nil {
			t.Fatalf("%s alongside held Execute: %v", name, err)
		}
	}

	plugin.releaseExecute()
	if err := waitForResult(t, oldDone, "held Execute"); err != nil {
		t.Fatalf("held Execute: %v", err)
	}
}

// TestSession_CrossGenerationCloseSerializesWithExecute characterizes the
// lifecycle guarantee that Close cannot tear down the transport below an
// active Execute.
//
//nolint:paralleltest // goleak VerifyNone conflicts with parallel tests
func TestSession_CrossGenerationCloseSerializesWithExecute(t *testing.T) {
	t.Cleanup(func() { verifyNoGoroutineLeak(t) })

	session, plugin := newCrossGenerationSession(t)

	old := &publicStream{
		ctx: context.Background(),
		frames: []backendplugin.ClientFrame{{
			Kind: backendplugin.ClientFrameStart, InstanceID: "cross-generation", Invocation: validInvocation(),
		}},
	}
	oldDone := make(chan error, 1)
	go func() { oldDone <- session.Execute(old) }()
	waitForSignal(t, plugin.executeStarted, "held Execute")

	closeAttempted := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		close(closeAttempted)
		closeDone <- session.Close(context.Background())
	}()
	waitForSignal(t, closeAttempted, "Close goroutine before Session.Close")
	assertNoSignal(t, plugin.closeStarted, "Close entered the configured instance while Execute was held")

	plugin.releaseExecute()
	if err := waitForResult(t, oldDone, "held Execute"); err != nil {
		t.Fatalf("held Execute: %v", err)
	}
	waitForSignal(t, plugin.closeStarted, "Close after Execute")
	if err := waitForResult(t, closeDone, "Close"); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func newCrossGenerationSession(t *testing.T) (*host.Session, *crossGenerationFake) {
	t.Helper()
	plugin := newCrossGenerationFake()
	conn := startPublicFake(t, plugin)
	session, _, err := host.DialConfiguredSession(
		context.Background(), conn, "cross-generation", "fake", nil, backendplugin.SecretBundle{},
		backendplugin.RuntimePolicy{DisableTransportRetries: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		plugin.releaseExecute()
		_ = session.Close(context.Background())
	})
	return session, plugin
}

func waitForSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	timer := time.NewTimer(sessionConcurrencyGuard)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func assertNoSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	timer := time.NewTimer(sessionConcurrencyGuard)
	defer timer.Stop()
	select {
	case <-signal:
		t.Fatal(failure)
	case <-timer.C:
	}
}

func waitForResult(t *testing.T, result <-chan error, operation string) error {
	t.Helper()
	timer := time.NewTimer(sessionConcurrencyGuard)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", operation)
		return errors.New("unreachable")
	}
}

type contextProbeStream struct {
	publicStream
	contextCalled chan struct{}
	contextOnce   sync.Once
}

func (s *contextProbeStream) Context() context.Context {
	s.contextOnce.Do(func() { close(s.contextCalled) })
	return s.ctx
}

type crossGenerationFake struct {
	executeStarted         chan struct{}
	executeRelease         chan struct{}
	executeReleased        chan struct{}
	closeStarted           chan struct{}
	resolveEntered         chan struct{}
	listModelsEntered      chan struct{}
	countTokensEntered     chan struct{}
	finalizeBillingEntered chan struct{}

	probeEnabled       atomic.Bool
	executeCalls       atomic.Int32
	closeCalls         atomic.Int32
	executeReleaseOnce sync.Once
	resolveOnce        sync.Once
	listOnce           sync.Once
	countOnce          sync.Once
	finalizeOnce       sync.Once
	closeOnce          sync.Once
}

func newCrossGenerationFake() *crossGenerationFake {
	return &crossGenerationFake{
		executeStarted:         make(chan struct{}),
		executeRelease:         make(chan struct{}),
		executeReleased:        make(chan struct{}),
		closeStarted:           make(chan struct{}),
		resolveEntered:         make(chan struct{}),
		listModelsEntered:      make(chan struct{}),
		countTokensEntered:     make(chan struct{}),
		finalizeBillingEntered: make(chan struct{}),
	}
}

func (f *crossGenerationFake) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorProxyOwnedSessionID,
		PluginID: "cross-generation-fake", Version: "v1",
		Features:  []backendplugin.Feature{{Name: backendplugin.FeatureOrderedItems}},
		Factories: []backendplugin.FactoryDescriptor{{Kind: "fake", StaticCapabilities: backendplugin.CapabilitySummary{Streaming: true}}},
	}, nil
}

func (f *crossGenerationFake) Configure(context.Context, backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	return (*crossGenerationInstance)(f), nil
}

func (f *crossGenerationFake) releaseExecute() {
	f.executeReleaseOnce.Do(func() { close(f.executeRelease) })
}

type crossGenerationInstance crossGenerationFake

func (f *crossGenerationInstance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	if f.probeEnabled.Load() {
		f.resolveOnce.Do(func() { close(f.resolveEntered) })
	}
	return backendplugin.ResolvedProfile{
		EvidenceSource:      "cross-generation-fake",
		Capabilities:        backendplugin.CapabilitySummary{Streaming: true},
		SupportsCountTokens: true, SupportsFinalizeBilling: true,
	}, nil
}

func (f *crossGenerationInstance) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	if f.probeEnabled.Load() {
		f.listOnce.Do(func() { close(f.listModelsEntered) })
	}
	return backendplugin.ListModelsResponse{InventorySource: "cross-generation-fake"}, nil
}

func (f *crossGenerationInstance) Execute(stream backendplugin.ExecuteStream) error {
	call := f.executeCalls.Add(1)
	frame, err := stream.Recv()
	if err != nil {
		return err
	}
	if frame.Kind != backendplugin.ClientFrameStart {
		return errors.New("cross-generation fake: expected start frame")
	}
	if call == 1 {
		close(f.executeStarted)
		select {
		case <-f.executeRelease:
			close(f.executeReleased)
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
	return stream.Send(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameTerminal,
		Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
	})
}

func (f *crossGenerationInstance) Close(context.Context) error {
	f.closeCalls.Add(1)
	f.closeOnce.Do(func() { close(f.closeStarted) })
	return nil
}

func (f *crossGenerationInstance) CountTokens(context.Context, backendplugin.CountTokensRequest) (backendplugin.CountTokensResponse, error) {
	if f.probeEnabled.Load() {
		f.countOnce.Do(func() { close(f.countTokensEntered) })
	}
	value := int64(7)
	return backendplugin.CountTokensResponse{InputTokens: &value, Presence: backendplugin.UsagePresence{InputTokens: true}, EvidenceQuality: "cross-generation-fake"}, nil
}

func (f *crossGenerationInstance) FinalizeBilling(context.Context, backendplugin.FinalizeBillingRequest) (backendplugin.FinalizeBillingResponse, error) {
	if f.probeEnabled.Load() {
		f.finalizeOnce.Do(func() { close(f.finalizeBillingEntered) })
	}
	value := int64(11)
	return backendplugin.FinalizeBillingResponse{Usage: backendplugin.UsageEvidence{TotalTokens: &value, Presence: backendplugin.UsagePresence{TotalTokens: true}}, EvidenceQuality: "cross-generation-fake"}, nil
}

var (
	_ backendplugin.Service            = (*crossGenerationFake)(nil)
	_ backendplugin.ConfiguredInstance = (*crossGenerationInstance)(nil)
	_ backendplugin.TokenCounter       = (*crossGenerationInstance)(nil)
	_ backendplugin.BillingFinalizer   = (*crossGenerationInstance)(nil)
)

var _ backendplugin.ExecuteStream = (*contextProbeStream)(nil)
