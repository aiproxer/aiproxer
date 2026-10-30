package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	backendplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestNormalizePluginCleanupErr_KeepsBudgetCauseBesideTransportDeath proves
// that the runtime cleanup normalization drops only the transport-closure
// component of a composite cleanup error. An independent cause that shares
// the composite error — the connector output-budget terminal cause among them
// — must survive, or the budget outcome stays invisible in production.
func TestNormalizePluginCleanupErr_KeepsBudgetCauseBesideTransportDeath(t *testing.T) {
	t.Parallel()
	transport := status.Error(codes.Unavailable, "connector transport closed")
	composite := errors.Join(transport, processhost.ReasonConnectorOutputLimitExceeded)

	got := normalizePluginCleanupErr(composite)
	if !errors.Is(got, processhost.ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("normalization dropped the budget cause: %v", got)
	}
	if errors.Is(got, transport) {
		t.Fatalf("normalization kept the transport-closure component: %v", got)
	}

	// A wrapper around the joined error keeps the budget cause too: the
	// transport status is found inside the join, so a text-only classification
	// would drop the whole wrapper.
	wrapped := fmt.Errorf("connector cleanup: %w", composite)
	normalizedWrapped := normalizePluginCleanupErr(wrapped)
	if !errors.Is(normalizedWrapped, processhost.ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("normalization dropped the budget cause behind a wrapper: %v", normalizedWrapped)
	}
	// The wrapper's identity goes with the transport component it carried: a
	// discarded transport classification must not survive beside the cause.
	if errors.Is(normalizedWrapped, transport) {
		t.Fatalf("normalization kept the transport-closure component behind a wrapper: %v", normalizedWrapped)
	}
	doubleWrapped := fmt.Errorf("plugin cleanup: %w", wrapped)
	normalizedDouble := normalizePluginCleanupErr(doubleWrapped)
	if !errors.Is(normalizedDouble, processhost.ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("normalization dropped the budget cause behind a double wrapper: %v", normalizedDouble)
	}
	if errors.Is(normalizedDouble, transport) {
		t.Fatalf("normalization kept the transport-closure component behind a double wrapper: %v", normalizedDouble)
	}
	// A wrapper around the transport death alone stays harmless.
	if got := normalizePluginCleanupErr(fmt.Errorf("connector cleanup: %w", transport)); got != nil {
		t.Fatalf("a wrapper around the transport death must normalize to nil: %v", got)
	}

	// A cleanup that failed only because the transport died stays harmless.
	if got := normalizePluginCleanupErr(errors.Join(transport, status.Error(codes.Unavailable, "conn closed"))); got != nil {
		t.Fatalf("a wholly transport-closure cleanup must normalize to nil: %v", got)
	}
	if got := normalizePluginCleanupErr(transport); got != nil {
		t.Fatalf("a transport-closure cleanup must normalize to nil: %v", got)
	}
	// An unrelated cause survives on its own.
	if got := normalizePluginCleanupErr(errors.New("plugin cleanup failed")); got == nil {
		t.Fatal("an unrelated cleanup failure must survive normalization")
	}
}

// TestActivation_DescriptorRejectionRetainsBudgetCause proves that a budget
// termination observed while the activation's runtime descriptor is verified
// keeps its classification: the activation failure reports the budget reason
// instead of replacing it with the secondary transport failure.
func TestActivation_DescriptorRejectionRetainsBudgetCause(t *testing.T) {
	t.Parallel()
	reader := newGatedBudgetFloodReader()
	breached := make(chan struct{})
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 4242, StdoutReader: reader},
		Channel:  &processhost.TestChannel{},
		// The first drained chunk outruns the burst allowance.
		OutputBudget: processhost.OutputBudget{BurstBytes: 1 << 10, BytesPerSecond: 1},
	})
	t.Cleanup(func() { _ = host.Close() })
	host.SetBudgetBreachProbe(func() { close(breached) })

	var closed atomic.Int64
	dial := func(ctx context.Context, req DialSessionRequest) (ExecuteSession, backendplugin.ResolvedProfile, error) {
		fake := &bpkit.FakeService{Mode: bpkit.ModeValid}
		inst, err := fake.Configure(ctx, backendplugin.ConfigureRequest{
			InstanceID:  req.InstanceID,
			FactoryKind: req.FactoryKind,
			ConfigYAML:  req.ConfigYAML,
			Secrets:     req.Secrets,
			Negotiation: backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
				EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
			},
			RuntimePolicy: req.Policy,
		})
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		profile, err := inst.Resolve(ctx, nil)
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		return &budgetBreachDescriptorSession{
			ConfiguredInstance: inst,
			openGate:           reader.open,
			breached:           breached,
			closed:             &closed,
		}, profile, nil
	}
	export := ValidatedExport{
		Kind:     "openrouter",
		Artifact: &trust.VerifiedArtifact{DigestHex: "sha256:openrouter-artifact"},
		Model:    processhost.ProcessModelPerInstance,
		Manifest: inferenceActivationManifest(t),
	}
	input := inferencePhysicalInput()
	input.InstanceID = "or-budget"
	_, _, err := buildDiscoveredPhysical(context.Background(), host, export, input, dial, new(atomic.Uint64), nil)
	if err == nil {
		t.Fatal("a rejected runtime descriptor must fail the activation")
	}
	if !errors.Is(err, processhost.ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("the activation failure lost the budget cause: %v", err)
	}
}

// TestDiscoveredCleanup_KeepsBudgetCauseBesideTransportDeath exercises the
// production cleanup path end to end: the adapter cleanup fails with the gRPC
// transport death of a reaped connector, the activation cleanup carries the
// budget reason, and the runtime normalization must keep the budget cause
// instead of dropping the whole composite error.
func TestDiscoveredCleanup_KeepsBudgetCauseBesideTransportDeath(t *testing.T) {
	t.Parallel()
	reader := newGatedBudgetFloodReader()
	breached := make(chan struct{})
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 4343, StdoutReader: reader},
		Channel:  &processhost.TestChannel{},
		// The first drained chunk outruns the burst allowance.
		OutputBudget: processhost.OutputBudget{BurstBytes: 1 << 10, BytesPerSecond: 1},
	})
	t.Cleanup(func() { _ = host.Close() })
	host.SetBudgetBreachProbe(func() { close(breached) })

	var closed atomic.Int64
	dial := func(ctx context.Context, req DialSessionRequest) (ExecuteSession, backendplugin.ResolvedProfile, error) {
		fake := &bpkit.FakeService{Mode: bpkit.ModeValid}
		inst, err := fake.Configure(ctx, backendplugin.ConfigureRequest{
			InstanceID:  req.InstanceID,
			FactoryKind: req.FactoryKind,
			ConfigYAML:  req.ConfigYAML,
			Secrets:     req.Secrets,
			Negotiation: backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
				EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
			},
			RuntimePolicy: req.Policy,
		})
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		profile, err := inst.Resolve(ctx, nil)
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		return &transportDeathSession{ConfiguredInstance: inst, closed: &closed}, profile, nil
	}
	export := ValidatedExport{
		Kind:     "openrouter",
		Artifact: &trust.VerifiedArtifact{DigestHex: "sha256:openrouter-artifact"},
		Model:    processhost.ProcessModelPerInstance,
		Manifest: inferenceActivationManifest(t),
	}
	input := inferencePhysicalInput()
	input.InstanceID = "or-cleanup"
	_, cleanup, err := buildDiscoveredPhysical(context.Background(), host, export, input, dial, new(atomic.Uint64), nil)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}

	// The connector starts flooding after it was served, so the budget
	// terminates the generation the activation owns.
	reader.open()
	select {
	case <-breached:
	case <-time.After(10 * time.Second):
		t.Fatal("the diagnostic budget did not record the breach")
	}

	composite := cleanup()
	if !errors.Is(composite, processhost.ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("the composite cleanup lost the budget cause: %v", composite)
	}
	if !errors.Is(composite, status.Error(codes.Unavailable, "connector transport closed")) {
		t.Fatalf("the composite cleanup lost the transport failure: %v", composite)
	}
	normalized := normalizePluginCleanupErr(composite)
	if !errors.Is(normalized, processhost.ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("the runtime normalization dropped the budget cause: %v", normalized)
	}
}

// transportDeathSession fails its close with the gRPC transport death the
// host observes when a connector's generation was already reaped.
type transportDeathSession struct {
	backendplugin.ConfiguredInstance
	closed *atomic.Int64
}

func (s *transportDeathSession) Close(context.Context) error {
	s.closed.Add(1)
	return status.Error(codes.Unavailable, "connector transport closed")
}

// budgetBreachDescriptorSession reports a descriptor failure after the
// connector has outrun its diagnostic budget, so the activation's
// post-configure verification runs against a terminated generation.
type budgetBreachDescriptorSession struct {
	backendplugin.ConfiguredInstance
	openGate func()
	breached <-chan struct{}
	closed   *atomic.Int64
}

func (s *budgetBreachDescriptorSession) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	// The connector starts flooding while its descriptor is verified: the
	// budget terminates the generation under the verification.
	s.openGate()
	select {
	case <-s.breached:
	case <-time.After(10 * time.Second):
		return backendplugin.PluginDescriptor{}, errors.New("the diagnostic budget did not record the breach")
	}
	return backendplugin.PluginDescriptor{}, errors.New("runtime descriptor unavailable")
}

func (s *budgetBreachDescriptorSession) Close(ctx context.Context) error {
	s.closed.Add(1)
	return s.ConfiguredInstance.Close(ctx)
}

// gatedBudgetFloodReader floods a connector's stdout once its gate opens, so
// a test can let the activation complete before the connector starts
// flooding.
type gatedBudgetFloodReader struct {
	gate      chan struct{}
	closed    chan struct{}
	openOnce  sync.Once
	closeOnce sync.Once
}

func newGatedBudgetFloodReader() *gatedBudgetFloodReader {
	return &gatedBudgetFloodReader{gate: make(chan struct{}), closed: make(chan struct{})}
}

func (r *gatedBudgetFloodReader) open() { r.openOnce.Do(func() { close(r.gate) }) }

func (r *gatedBudgetFloodReader) Read(p []byte) (int, error) {
	select {
	case <-r.closed:
		return 0, io.EOF
	case <-r.gate:
	}
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func (r *gatedBudgetFloodReader) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return nil
}
