package claudeagentacp_test

import (
	"context"
	"net"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/connectors/claudeagentacp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// Runs model and identity admission regressions through module validation.
// Node is an explicit connector-development prerequisite, not a host dependency.
func TestNode_ModelAndIdentityAdmission(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--test", "adapter-extension/identity.test.mjs")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Node model/identity contracts (Node >=22 required): %v\n%s", err, output)
	}
}

func TestDescribe_DistinctLocalAgentFactory(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if d.PluginID != "com.aiproxer.backend.claudeagentacp" || len(d.Factories) != 1 {
		t.Fatalf("descriptor=%+v", d)
	}
	f := d.Factories[0]
	if f.Kind != "claudeagentacp" || !reflect.DeepEqual(f.RoutePrefixes, []string{"claudeagentacp"}) || f.Description != "Local Claude Agent ACP connector; model namespace anthropic/" {
		t.Fatalf("route identity=%+v", f)
	}
	if f.CredentialMode != backendplugin.CredentialModeStatic || f.AccessScope != backendplugin.AccessScopeLocalOnly || f.ProcessSharing != backendplugin.ProcessSharingPerInstance {
		t.Fatalf("posture=%+v", f)
	}
	// The connector advertises the proxy-owned session id and the optional
	// graded effort-values feature: the host injects the session id only for
	// a connector that declares it, and graded facts ride ResolveProfile only
	// when both sides negotiate the effort feature.
	wantFeatures := []backendplugin.Feature{{Name: backendplugin.FeatureProxyOwnedSessionID}, {Name: backendplugin.FeatureReasoningEffortValues}}
	if f.StaticCapabilities != (backendplugin.CapabilitySummary{Streaming: true, Reasoning: true}) || f.TransportCapabilities != (backendplugin.TransportCapabilitySummary{}) || !f.SupportsDynamicInventory || !reflect.DeepEqual(d.Features, wantFeatures) || d.ProtocolMinor < backendplugin.ProtocolMinorProxyOwnedSessionID {
		t.Fatalf("unexpected certified assembly capabilities: %+v", d)
	}
}

// The connector must negotiate the proxy-owned session id with a stock host
// offer: the host injects the session id only for a connector that enables the
// feature, and that session id is the conversation identity of the agent
// process.
func TestNegotiation_EnablesProxyOwnedSessionID(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	host := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorAccountingEvidenceV2,
		Features:                []backendplugin.Feature{{Name: backendplugin.FeatureProxyOwnedSessionID}},
		DisableTransportRetries: true,
	}
	neg, err := backendplugin.Negotiate(host, backendplugin.ProtocolOffer{
		Major: d.ProtocolMajor, Minor: d.ProtocolMinor, Features: d.Features,
		DisableTransportRetries: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !backendplugin.ProxyOwnedSessionIDSupported(neg) {
		t.Fatalf("the negotiated ABI cannot carry the proxy-owned session id: %+v", neg)
	}
}

func TestConfigure_RejectsLiteralCredentials(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"", "claudeagentacp", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			inst, err := service.New().Configure(t.Context(), backendplugin.ConfigureRequest{
				FactoryKind: kind, InstanceID: "agent",
				ConfigYAML: []byte("api_key: never-echo-this\n"),
				Secrets:    backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("never-echo-this")}},
			})
			if inst != nil || err == nil || strings.Contains(err.Error(), "never-echo-this") {
				t.Fatalf("instance=%v err=%v", inst, err)
			}
			if kind == "anthropic" {
				if !strings.Contains(err.Error(), "unexpected factory kind") {
					t.Fatal(err)
				}
			} else if !strings.Contains(err.Error(), "invalid configuration") {
				t.Fatalf("missing remediation: %v", err)
			}
		})
	}
}

// Exercise real ABI rejection without runtime credentials or provider requests.
func TestServiceConformance_NegotiateDescribeAndFailClosedConfigure(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	svc := service.New()
	d, err := svc.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(backendplugin.ProtocolOffer{
		Major: d.ProtocolMajor, Minor: d.ProtocolMinor, Features: d.Features, DisableTransportRetries: true,
	}, svc))
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); _ = lis.Close(); <-done })
	conn, err := grpc.NewClient("passthrough:///test", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := backendpluginv1.NewBackendPluginClient(conn)
	neg, err := client.Negotiate(ctx, &backendpluginv1.NegotiateRequest{HostMajor: 1, HostMinor: d.ProtocolMinor, DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	if !neg.GetCompatible() || neg.GetNegotiationToken() == "" {
		t.Fatalf("negotiation=%+v", neg)
	}
	described, err := client.Describe(ctx, &backendpluginv1.DescribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := backendplugin.PluginDescriptorToProto(d)
	if err != nil {
		t.Fatal(err)
	}
	if described.GetDescriptor_().GetPluginId() != want.GetPluginId() || described.GetDescriptor_().GetFactories()[0].GetKind() != "claudeagentacp" || described.GetDescriptor_().GetFactories()[0].GetDescription() != "Local Claude Agent ACP connector; model namespace anthropic/" {
		t.Fatalf("wire descriptor=%+v", described)
	}
	configured, err := client.Configure(ctx, backendplugin.ConfigureRequestToProto(backendplugin.ConfigureRequest{
		InstanceID: "agent", FactoryKind: "claudeagentacp", NegotiationToken: neg.GetNegotiationToken(),
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	}))
	if configured != nil || err == nil || !strings.Contains(err.Error(), "api_key secret required") {
		t.Fatalf("configure=%v err=%v", configured, err)
	}
	if profile, err := client.ResolveProfile(ctx, &backendpluginv1.ResolveProfileRequest{InstanceId: "agent"}); err == nil || profile != nil {
		t.Fatalf("rejected instance became resolvable: profile=%v err=%v", profile, err)
	}
}
