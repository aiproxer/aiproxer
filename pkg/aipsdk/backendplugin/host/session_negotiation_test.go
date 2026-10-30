package host_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

// hostOfferCapture records the host's real negotiate request as it travels on
// the wire. The capture is the actual host offer, not a copy of it: the host
// builds the offer inside its own dial path, and the probe reads the request
// the host sent.
type hostOfferCapture struct {
	mu       sync.Mutex
	major    uint32
	minor    uint32
	features []string
	required map[string]bool
}

func (c *hostOfferCapture) record(req *backendpluginv1.NegotiateRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.major = req.GetHostMajor()
	c.minor = req.GetHostMinor()
	c.features = nil
	c.required = map[string]bool{}
	for _, f := range req.GetHostFeatures() {
		c.features = append(c.features, f.GetName())
		c.required[f.GetName()] = f.GetRequired()
	}
}

func (c *hostOfferCapture) snapshot() (uint32, uint32, []string, map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.major, c.minor, slices.Clone(c.features), c.required
}

// negotiationProbeService is the minimal plugin service the negotiation
// probe needs. The probe never executes an attempt: it only proves what the
// host offers and what both sides negotiate.
type negotiationProbeService struct{}

func (negotiationProbeService) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2,
		PluginID: "negotiation-probe", Version: "v1",
		Features:  []backendplugin.Feature{{Name: backendplugin.FeatureHostBoundEconomicEvidenceV2}},
		Factories: []backendplugin.FactoryDescriptor{{Kind: "fake", StaticCapabilities: backendplugin.CapabilitySummary{Streaming: true}}},
	}, nil
}

func (negotiationProbeService) Configure(context.Context, backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	return negotiationProbeInstance{}, nil
}

type negotiationProbeInstance struct{}

func (negotiationProbeInstance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{EvidenceSource: "negotiation-probe"}, nil
}

func (negotiationProbeInstance) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}

func (negotiationProbeInstance) Execute(backendplugin.ExecuteStream) error { return nil }

func (negotiationProbeInstance) Close(context.Context) error { return nil }

// dialNegotiationProbe dials the real host against a plugin that advertises
// pluginOffer, and returns the session plus a cleanup that releases it.
func dialNegotiationProbe(
	t *testing.T,
	pluginOffer backendplugin.ProtocolOffer,
	capture *hostOfferCapture,
) (*host.Session, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(
		func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			if info.FullMethod == backendpluginv1.BackendPlugin_Negotiate_FullMethodName {
				if negotiate, ok := req.(*backendpluginv1.NegotiateRequest); ok {
					capture.record(negotiate)
				}
			}
			return handler(ctx, req)
		},
	))
	backendpluginv1.RegisterBackendPluginServer(
		grpcServer, backendplugin.NewGRPCServer(pluginOffer, negotiationProbeService{}),
	)
	go func() { _ = grpcServer.Serve(lis) }()

	conn, err := lis.Dial()
	if err != nil {
		grpcServer.Stop()
		_ = lis.Close()
		t.Fatalf("dial probe: %v", err)
	}
	sess, _, err := host.DialConfiguredSession(
		context.Background(), conn, "probe", "fake", nil,
		backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true},
	)
	if err != nil {
		_ = conn.Close()
		grpcServer.Stop()
		_ = lis.Close()
		t.Fatalf("DialConfiguredSession: %v", err)
	}
	cleanup := func() {
		_ = sess.Close(context.Background())
		_ = conn.Close()
		grpcServer.Stop()
		_ = lis.Close()
	}
	return sess, cleanup
}

// TestHostOfferNegotiatesHostBoundEconomicEvidenceV2 proves that the host
// itself offers the host-bound economic evidence contract. Before the fix the
// host offered protocol minor 9 and never advertised the feature, so minor 10
// was unreachable for every peer and no connector claim could ever be bound.
func TestHostOfferNegotiatesHostBoundEconomicEvidenceV2(t *testing.T) {
	t.Parallel()
	capture := &hostOfferCapture{}
	pluginOffer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2,
		DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureAccountingEvidenceV2},
			{Name: backendplugin.FeatureHostBoundEconomicEvidenceV2},
		},
	}
	sess, cleanup := dialNegotiationProbe(t, pluginOffer, capture)
	defer cleanup()

	major, minor, features, required := capture.snapshot()
	if major != 1 {
		t.Fatalf("host offered major %d, want 1", major)
	}
	if minor != backendplugin.ProtocolMinorReasoningEffortValues {
		t.Fatalf("host offered minor %d, want %d: the latest contract is unreachable for every peer",
			minor, backendplugin.ProtocolMinorReasoningEffortValues)
	}
	if !slices.Contains(features, backendplugin.FeatureHostBoundEconomicEvidenceV2) {
		t.Fatalf("host offer does not advertise %s: %v",
			backendplugin.FeatureHostBoundEconomicEvidenceV2, features)
	}
	if required[backendplugin.FeatureHostBoundEconomicEvidenceV2] {
		t.Fatalf("host offer marks %s required: a minor-9 peer must stay compatible",
			backendplugin.FeatureHostBoundEconomicEvidenceV2)
	}

	neg := sess.Negotiation()
	if neg.NegotiatedMinor != backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2 {
		t.Fatalf("negotiated minor %d, want %d (plugin predates effort values)", neg.NegotiatedMinor,
			backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2)
	}
	if !backendplugin.HostBoundEconomicEvidenceV2Negotiated(neg) {
		t.Fatalf("host-bound economic evidence V2 not negotiated: enabled=%v", neg.EnabledFeatures)
	}
}

// TestHostOfferKeepsMinorNinePeerCompatible is the backward-compatibility
// control: a peer that predates the host-bound contract still negotiates the
// older minor and keeps the V2 accounting evidence feature.
func TestHostOfferKeepsMinorNinePeerCompatible(t *testing.T) {
	t.Parallel()
	capture := &hostOfferCapture{}
	pluginOffer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorAccountingEvidenceV2,
		DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureAccountingEvidenceV2},
		},
	}
	sess, cleanup := dialNegotiationProbe(t, pluginOffer, capture)
	defer cleanup()

	neg := sess.Negotiation()
	if !neg.Compatible {
		t.Fatalf("minor-9 peer became incompatible: %s", neg.RejectReason)
	}
	if neg.NegotiatedMinor != backendplugin.ProtocolMinorAccountingEvidenceV2 {
		t.Fatalf("negotiated minor %d, want %d", neg.NegotiatedMinor,
			backendplugin.ProtocolMinorAccountingEvidenceV2)
	}
	if !backendplugin.AccountingEvidenceV2Negotiated(neg) {
		t.Fatalf("minor-9 peer lost the V2 accounting evidence feature: %v", neg.EnabledFeatures)
	}
	if backendplugin.HostBoundEconomicEvidenceV2Negotiated(neg) {
		t.Fatalf("a minor-9 peer must never be host-bound: %v", neg.EnabledFeatures)
	}
}

// TestHostBoundEconomicEvidenceV2RequiresMinorTen pins the minor floor of the
// contract: a peer that requires the host-bound feature cannot negotiate it
// against a minor-9 host.
func TestHostBoundEconomicEvidenceV2RequiresMinorTen(t *testing.T) {
	t.Parallel()
	hostOffer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorAccountingEvidenceV2,
		DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureHostBoundEconomicEvidenceV2, Required: true},
		},
	}
	pluginOffer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2,
		DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureHostBoundEconomicEvidenceV2, Required: true},
		},
	}
	if _, err := backendplugin.Negotiate(hostOffer, pluginOffer); !errors.Is(err, backendplugin.ErrIncompatibleMinor) {
		t.Fatalf("minor-9 host offer with a required host-bound feature: err=%v, want ErrIncompatibleMinor", err)
	}
}
