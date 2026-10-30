package host_test

import (
	"context"
	"strings"
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type effortValuesService struct {
	profile backendplugin.ResolvedProfile
}

func (s effortValuesService) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorReasoningEffortValues,
		PluginID: "effort-probe", Version: "v1",
		Features:  []backendplugin.Feature{{Name: backendplugin.FeatureReasoningEffortValues}},
		Factories: []backendplugin.FactoryDescriptor{{Kind: "fake", StaticCapabilities: backendplugin.CapabilitySummary{Streaming: true}}},
	}, nil
}

func (s effortValuesService) Configure(context.Context, backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	return effortValuesInstance(s), nil
}

type effortValuesInstance struct {
	profile backendplugin.ResolvedProfile
}

func (i effortValuesInstance) Resolve(_ context.Context, modelID *string) (backendplugin.ResolvedProfile, error) {
	out := i.profile
	if modelID == nil {
		out.ReasoningEffortValues = nil
		return out, nil
	}
	if out.ReasoningEffortValues != nil {
		// Echo the requested model so mismatch tests can substitute.
		if out.ReasoningEffortValues.ModelID == "echo" {
			out.ReasoningEffortValues = &backendplugin.ReasoningEffortValuesV1{
				ModelID:    *modelID,
				Supported:  append([]string(nil), i.profile.ReasoningEffortValues.Supported...),
				Exhaustive: i.profile.ReasoningEffortValues.Exhaustive,
			}
		}
	}
	return out, nil
}

func (i effortValuesInstance) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}

func (i effortValuesInstance) Execute(backendplugin.ExecuteStream) error { return nil }
func (i effortValuesInstance) Close(context.Context) error               { return nil }

func dialEffortProbe(t *testing.T, pluginOffer backendplugin.ProtocolOffer, svc backendplugin.Service) (*host.Session, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(grpcServer, backendplugin.NewGRPCServer(pluginOffer, svc))
	go func() { _ = grpcServer.Serve(lis) }()
	// Use the public dial path so the host offer/negotiation is real.
	conn, err := lis.Dial()
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sess, _, err := host.DialConfiguredSession(context.Background(), conn, "probe", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatalf("DialConfiguredSession: %v", err)
	}
	return sess, func() {
		_ = sess.Close(context.Background())
		grpcServer.Stop()
		_ = lis.Close()
	}
}

func TestEffortValuesHostRoundTrip(t *testing.T) {
	t.Parallel()
	pluginOffer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorReasoningEffortValues,
		DisableTransportRetries: true,
		Features:                []backendplugin.Feature{{Name: backendplugin.FeatureReasoningEffortValues}},
	}
	svc := effortValuesService{profile: backendplugin.ResolvedProfile{
		Capabilities:          backendplugin.CapabilitySummary{Streaming: true},
		ReasoningEffortValues: &backendplugin.ReasoningEffortValuesV1{ModelID: "echo", Supported: []string{"high"}, Exhaustive: true},
	}}
	sess, cleanup := dialEffortProbe(t, pluginOffer, svc)
	defer cleanup()
	if !backendplugin.ReasoningEffortValuesNegotiated(sess.Negotiation()) {
		t.Fatalf("effort values not negotiated: %+v", sess.Negotiation())
	}
	model := "model-m"
	got, err := sess.Resolve(context.Background(), &model)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ReasoningEffortValues == nil || got.ReasoningEffortValues.ModelID != model {
		t.Fatalf("expected echoed effort facts for %q, got %+v", model, got.ReasoningEffortValues)
	}
	sup, decided := backendplugin.ClassifyEffortSupport(got.ReasoningEffortValues, model, "high")
	if !decided || !sup {
		t.Fatalf("expected supported, got %v %v", sup, decided)
	}
	if sup, decided := backendplugin.ClassifyEffortSupport(got.ReasoningEffortValues, model, "none"); !decided || sup {
		t.Fatalf("expected unsupported exhaustive negative, got %v %v", sup, decided)
	}
	// Model-agnostic query leaves the field absent.
	agnostic, err := sess.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatalf("agnostic Resolve: %v", err)
	}
	if agnostic.ReasoningEffortValues != nil {
		t.Fatalf("agnostic resolve must leave effort values absent")
	}
}

func TestEffortValuesLegacyPeerStaysUnknown(t *testing.T) {
	t.Parallel()
	// Plugin predates the contract: minor 10, no feature. Even if its
	// service misbehaves and returns facts, the server suppresses and the
	// host strips them, so the caller sees Unknown with normal inference.
	pluginOffer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2,
		DisableTransportRetries: true,
		Features:                []backendplugin.Feature{{Name: backendplugin.FeatureHostBoundEconomicEvidenceV2}},
	}
	svc := effortValuesService{profile: backendplugin.ResolvedProfile{
		Capabilities:          backendplugin.CapabilitySummary{Streaming: true},
		ReasoningEffortValues: &backendplugin.ReasoningEffortValuesV1{ModelID: "model-m", Supported: []string{"high"}, Exhaustive: true},
	}}
	sess, cleanup := dialEffortProbe(t, pluginOffer, svc)
	defer cleanup()
	if backendplugin.ReasoningEffortValuesNegotiated(sess.Negotiation()) {
		t.Fatalf("legacy peer must not negotiate effort values")
	}
	model := "model-m"
	got, err := sess.Resolve(context.Background(), &model)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ReasoningEffortValues != nil {
		t.Fatalf("unnegotiated facts must be stripped, got %+v", got.ReasoningEffortValues)
	}
	if !got.Capabilities.Streaming {
		t.Fatalf("ordinary traffic must keep working after strip")
	}
}

func TestEffortValuesInvalidServiceRejected(t *testing.T) {
	t.Parallel()
	pluginOffer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorReasoningEffortValues,
		DisableTransportRetries: true,
		Features:                []backendplugin.Feature{{Name: backendplugin.FeatureReasoningEffortValues}},
	}
	svc := effortValuesService{profile: backendplugin.ResolvedProfile{
		Capabilities:          backendplugin.CapabilitySummary{Streaming: true},
		ReasoningEffortValues: &backendplugin.ReasoningEffortValuesV1{ModelID: "m", Supported: []string{"low", "low"}, Exhaustive: true},
	}}
	sess, cleanup := dialEffortProbe(t, pluginOffer, svc)
	defer cleanup()
	model := "m"
	_, err := sess.Resolve(context.Background(), &model)
	if err == nil {
		t.Fatalf("invalid duplicate declaration must be rejected")
	}
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument && !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected InvalidArgument/duplicate, got %v", err)
	}
}

// rawMisbehavingPeerClient is a misbehaving raw gRPC peer: it returns wire
// bytes directly without the SDK server's suppression or validation, so the
// host must strip unsolicited declarations before conversion.
type rawMisbehavingPeerClient struct {
	backendpluginv1.BackendPluginClient
	wire *backendpluginv1.ResolvedProfile
}

func (c *rawMisbehavingPeerClient) ResolveProfile(context.Context, *backendpluginv1.ResolveProfileRequest, ...grpc.CallOption) (*backendpluginv1.ResolveProfileResponse, error) {
	return &backendpluginv1.ResolveProfileResponse{Profile: c.wire}, nil
}

func (c *rawMisbehavingPeerClient) CloseInstance(context.Context, *backendpluginv1.CloseInstanceRequest, ...grpc.CallOption) (*backendpluginv1.CloseInstanceResponse, error) {
	return &backendpluginv1.CloseInstanceResponse{}, nil
}

// TestEffortValuesUnnegotiatedMalformedStrippedBeforeDecode proves the host
// strips unsolicited facts before conversion: a malformed unnegotiated
// declaration must not fail legacy ResolveProfile.
func TestEffortValuesUnnegotiatedMalformedStrippedBeforeDecode(t *testing.T) {
	t.Parallel()
	// Duplicate values are valid proto but fail DTO validation. Build a fresh
	// wire profile per subcase: the host strips by clearing the wire field,
	// so sharing one pointer would let the first strip hide the second case.
	malformedFor := func() *backendpluginv1.ResolvedProfile {
		return &backendpluginv1.ResolvedProfile{
			Capabilities:          &backendpluginv1.CapabilitySummary{Streaming: true},
			ReasoningEffortValues: &backendpluginv1.ReasoningEffortValuesV1{ModelId: "m", Supported: []string{"low", "low"}, Exhaustive: true},
		}
	}
	legacy := backendplugin.Negotiation{
		Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2,
		EnabledFeatures: []string{backendplugin.FeatureHostBoundEconomicEvidenceV2},
	}
	if backendplugin.ReasoningEffortValuesNegotiated(legacy) {
		t.Fatalf("legacy negotiation must not enable effort values")
	}
	sess := host.NewSessionForTesting(&rawMisbehavingPeerClient{wire: malformedFor()}, nil, "raw-peer", legacy)
	model := "m"
	got, err := sess.Resolve(context.Background(), &model)
	if err != nil {
		t.Fatalf("unnegotiated malformed declaration must be stripped, got err %v", err)
	}
	if got.ReasoningEffortValues != nil {
		t.Fatalf("unnegotiated facts must be stripped, got %+v", got.ReasoningEffortValues)
	}
	if !got.Capabilities.Streaming {
		t.Fatalf("ordinary traffic must keep working after strip")
	}
	// Positive control: the same wire bytes are malformed when negotiated,
	// so the failure is the strip, not an accidentally valid fixture.
	negotiated := backendplugin.Negotiation{
		Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorReasoningEffortValues,
		EnabledFeatures: []string{backendplugin.FeatureReasoningEffortValues},
	}
	sessNeg := host.NewSessionForTesting(&rawMisbehavingPeerClient{wire: malformedFor()}, nil, "raw-peer", negotiated)
	if _, err := sessNeg.Resolve(context.Background(), &model); err == nil {
		t.Fatalf("negotiated malformed declaration must be rejected")
	}
}
