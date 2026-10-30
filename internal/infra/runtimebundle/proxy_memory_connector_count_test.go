package runtimebundle

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	connectorfake "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// The process-boundary count of the identity-only reference compile (R11).
//
// The count is taken at the connector's ResolveProfile endpoint: the real
// gRPC server, the real host session, the real adapter bridge and the real
// root compiler. A count at the capability-resolver seam would not be the
// process boundary, and a fake whole port would not exercise the connector
// path at all.

// countingPluginServer is the real connector gRPC server with a counting
// ResolveProfile endpoint. Every other endpoint is the server's own.
type countingPluginServer struct {
	*backendplugin.GRPCServer
	resolveProfileCalls atomic.Int64
}

func (s *countingPluginServer) ResolveProfile(ctx context.Context, req *backendpluginv1.ResolveProfileRequest) (*backendpluginv1.ResolveProfileResponse, error) {
	s.resolveProfileCalls.Add(1)
	return s.GRPCServer.ResolveProfile(ctx, req)
}

func (s *countingPluginServer) count() int64 {
	return s.resolveProfileCalls.Load()
}

// connectorSessionFixture is one connector fixture: the counting gRPC server,
// the real host session and the real adapter bridge.
type connectorSessionFixture struct {
	server   *countingPluginServer
	session  *host.Session
	backend  execbackend.Backend
	instance string
}

// newConnectorSessionFixture starts the real connector gRPC server over a
// bufconn, configures the real host session and builds the real adapter
// bridge, so the capability resolution travels the connector's own
// session.Resolve path.
func newConnectorSessionFixture(t *testing.T, instanceID string) *connectorSessionFixture {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	offer := backendplugin.ProtocolOffer{Major: 1, Minor: 0, DisableTransportRetries: true}
	server := &countingPluginServer{GRPCServer: backendplugin.NewGRPCServer(offer, &connectorfake.FakeService{Mode: connectorfake.ModeValid})}
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, server)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() {
		gs.Stop()
		_ = lis.Close()
	})

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///connector", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := backendpluginv1.NewBackendPluginClient(conn)

	neg, err := client.Negotiate(context.Background(), &backendpluginv1.NegotiateRequest{
		HostMajor: 1, HostMinor: 0, DisableTransportRetries: true,
	})
	if err != nil {
		t.Fatalf("negotiate: %v", err)
	}
	if !neg.GetCompatible() {
		t.Fatalf("negotiate: incompatible: %s", neg.GetRejectReason())
	}
	if _, err := client.Configure(context.Background(), &backendpluginv1.ConfigureRequest{
		InstanceId:       instanceID,
		FactoryKind:      "fake",
		ConfigYaml:       []byte("k: v\n"),
		NegotiationToken: neg.GetNegotiationToken(),
		RuntimePolicy:    &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	sess := host.NewSessionForTesting(client, conn, instanceID, backendplugin.Negotiation{})
	profile, err := sess.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatalf("resolve profile: %v", err)
	}
	built := adapter.Build(sess, profile, adapter.Options{InstanceID: instanceID, RoutePrefixes: []string{"openai"}})
	return &connectorSessionFixture{server: server, session: sess, backend: built.Backend, instance: instanceID}
}

// TestProxyMemoryTargetIdentityCompileAddsNoConnectorProfileRPC pins the
// identity-only reference compile at the connector's process boundary (R11,
// design C3).
//
// The count is the number of ResolveProfile RPCs the connector's endpoint
// served. The capability path resolves the capabilities of the compiled
// candidates, so it costs one RPC per candidate; the identity-only path
// resolves no capability, so it costs none. The preflight compile and the
// runtime's own attempt-time resolution are separate stages and are counted
// separately.
func TestProxyMemoryTargetIdentityCompileAddsNoConnectorProfileRPC(t *testing.T) {
	fixture := newConnectorSessionFixture(t, "openai")
	ctx := context.Background()

	// The executor carries the real connector backend and the real
	// capability map, so the capability resolution travels the adapter
	// bridge into the connector session.
	exec := runtime.TestExecutor()
	exec.Backends = map[string]execbackend.Backend{fixture.instance: fixture.backend}
	exec.CapsResolver = capabilities.MapResolver{
		fixture.instance: func(ctx context.Context, cand routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps {
			return execbackend.EffectiveCaps(ctx, fixture.backend, call, cand)
		},
	}
	compiler := newProxyMemoryTargetCompiler().bind(exec)

	call := aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:openai/test-model"},
		Options: aipapi.GenerationOptions{
			ReasoningEffort: "low",
		},
	}

	// The capability path is the positive control: it resolves the
	// capabilities of the compiled candidate, so the connector serves one
	// profile RPC. The counter is live.
	beforeControl := fixture.server.count()
	var compiled int
	if err := compiler.CompileTarget(ctx, call, func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps) {
		compiled++
	}); err != nil {
		t.Fatalf("CompileTarget: %v", err)
	}
	if compiled != 1 {
		t.Fatalf("compiled candidates = %d, want 1", compiled)
	}
	afterControl := fixture.server.count()
	if afterControl <= beforeControl {
		t.Fatalf("connector profile RPCs = %d, want more than %d: the counter is not live", afterControl, beforeControl)
	}

	// The identity-only path resolves no capability: it adds no RPC.
	beforeIdentity := fixture.server.count()
	var identities int
	if err := compiler.CompileIdentity(ctx, call, func(backend, model string) {
		identities++
	}); err != nil {
		t.Fatalf("CompileIdentity: %v", err)
	}
	afterIdentity := fixture.server.count()
	if identities != 1 {
		t.Fatalf("compiled identities = %d, want 1", identities)
	}
	if afterIdentity != beforeIdentity {
		t.Fatalf("connector profile RPCs = %d, want %d: the identity-only compile must add no RPC", afterIdentity, beforeIdentity)
	}
}

// TestProxyMemoryTargetCapabilityPathCountStages reports the honest stage
// counts of one phase dispatch over a connector backend: the preflight
// compile resolves the capabilities of the compiled candidates, and the
// runtime resolves the capabilities of the attempt it opens. The identity
// path adds none.
//
// The count is the connector's ResolveProfile endpoint, so it is the process
// boundary and not a capability-resolver seam.
func TestProxyMemoryTargetCapabilityPathCountStages(t *testing.T) {
	fixture := newConnectorSessionFixture(t, "openai")
	ctx := context.Background()

	exec := runtime.TestExecutor()
	exec.Backends = map[string]execbackend.Backend{fixture.instance: fixture.backend}
	exec.CapsResolver = capabilities.MapResolver{
		fixture.instance: func(ctx context.Context, cand routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps {
			return execbackend.EffectiveCaps(ctx, fixture.backend, call, cand)
		},
	}
	compiler := newProxyMemoryTargetCompiler().bind(exec)

	call := aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:openai/test-model"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}

	before := fixture.server.count()
	if err := compiler.CompileTarget(ctx, call, func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps) {}); err != nil {
		t.Fatalf("CompileTarget: %v", err)
	}
	afterCapability := fixture.server.count()
	if err := compiler.CompileIdentity(ctx, call, func(backend, model string) {}); err != nil {
		t.Fatalf("CompileIdentity: %v", err)
	}
	afterIdentity := fixture.server.count()

	t.Logf("connector profile RPCs: session bootstrap=%d, capability preflight=%d, identity reference=%d",
		before, afterCapability-before, afterIdentity-afterCapability)
	if afterCapability-before != 1 {
		t.Fatalf("capability preflight RPCs = %d, want 1 (one per compiled candidate)", afterCapability-before)
	}
	if afterIdentity-afterCapability != 0 {
		t.Fatalf("identity reference RPCs = %d, want 0", afterIdentity-afterCapability)
	}
}

// proxyMemoryConnectorConfig keeps the configuration shape of the composed
// fixture reachable from this file.
var _ = config.ServerConfig{}
