package backendplugin

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// The host-bound V2 fixture is a test capability, not production behavior.
// Its own test proves the exact shape the host contract depends on: the
// advertised minor and feature, the explicit configuration source, the echoed
// transport B-leg pseudonym, and the absence of any money value.

// v2CaptureStream captures the frames one fake execute emits.
type v2CaptureStream struct {
	ctx    context.Context
	frames []backendplugin.ServerFrame
}

func (s *v2CaptureStream) Context() context.Context { return s.ctx }

func (s *v2CaptureStream) Send(frame backendplugin.ServerFrame) error {
	s.frames = append(s.frames, frame)
	return nil
}

// v2StartStream replays one start frame and then reports the end of the
// host-to-plugin direction.
type v2StartStream struct {
	*v2CaptureStream
	frame backendplugin.ClientFrame
	sent  bool
}

func (s *v2StartStream) Recv() (backendplugin.ClientFrame, error) {
	if s.sent {
		return backendplugin.ClientFrame{}, context.Canceled
	}
	s.sent = true
	return s.frame, nil
}

// v2StartFrame builds the start frame the host sends for one attempt.
func v2StartFrame(bLeg string) backendplugin.ClientFrame {
	text := "ping"
	return backendplugin.ClientFrame{
		Kind:       backendplugin.ClientFrameStart,
		InstanceID: "v2-probe",
		Invocation: &backendplugin.Invocation{
			RequestID: "v2-request", AttemptID: "v2-request-attempt",
			ALegID: "v2-aleg", BLegID: bLeg,
			CanonicalModelID: "fake/fake-model", NativeModelID: "fake-model",
			Messages: []backendplugin.Message{{
				Role:  backendplugin.RoleUser,
				Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
			}},
		},
	}
}

// v2ConfigureRequest builds a configure request with a compatible
// negotiation, so the fixture's own configuration rules are what is under
// test.
func v2ConfigureRequest(configYAML string) backendplugin.ConfigureRequest {
	return backendplugin.ConfigureRequest{
		InstanceID: "v2-probe", FactoryKind: "fake",
		ConfigYAML: []byte(configYAML),
		RuntimePolicy: backendplugin.RuntimePolicy{
			DisableTransportRetries: true,
		},
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2,
		},
	}
}

// v2Claims returns the typed V2 claims of the captured frames.
func v2Claims(frames []backendplugin.ServerFrame) []backendplugin.AccountingEvidenceV2 {
	var out []backendplugin.AccountingEvidenceV2
	for _, frame := range frames {
		if frame.AccountingV2 == nil {
			continue
		}
		out = append(out, *frame.AccountingV2)
	}
	return out
}

func TestFakeEconomicsV2_DefaultModeKeepsTheV1Contract(t *testing.T) {
	t.Parallel()
	desc, err := (&FakeService{Mode: ModeValid}).Describe(context.Background())
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if desc.ProtocolMinor >= backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2 {
		t.Fatalf("the default connector advertises minor %d; the default V1 behavior must be unchanged",
			desc.ProtocolMinor)
	}
	for _, f := range desc.Features {
		if f.Name == backendplugin.FeatureHostBoundEconomicEvidenceV2 {
			t.Fatal("the default connector must not advertise the host-bound economic evidence feature")
		}
	}
}

func TestFakeEconomicsV2_OptInAdvertisesTheHostBoundContract(t *testing.T) {
	t.Parallel()
	svc := &FakeService{Mode: ModeValid, EconomicsV2: true}
	desc, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if desc.ProtocolMinor != backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2 {
		t.Fatalf("advertised minor %d, want %d", desc.ProtocolMinor,
			backendplugin.ProtocolMinorHostBoundEconomicEvidenceV2)
	}
	found := false
	for _, f := range desc.Features {
		if f.Name == backendplugin.FeatureHostBoundEconomicEvidenceV2 {
			found = true
			if f.Required {
				t.Fatal("the host-bound feature must stay non-required")
			}
		}
	}
	if !found {
		t.Fatalf("the connector does not advertise %s", backendplugin.FeatureHostBoundEconomicEvidenceV2)
	}
}

func TestFakeEconomicsV2_ConfigurationSourceIsExplicit(t *testing.T) {
	t.Parallel()
	svc := &FakeService{Mode: ModeValid, EconomicsV2: true}
	if _, err := svc.Configure(context.Background(), v2ConfigureRequest("")); err == nil {
		t.Fatal("the V2 connector accepted a configuration without its explicit source")
	}
	instance, err := svc.Configure(context.Background(), v2ConfigureRequest(
		"economics_v2:\n  store_id: store\n  provider_account_key: key\n  provider_request_id: request\n"))
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if instance == nil {
		t.Fatal("configure returned no instance")
	}
	// The default V1 connector refuses the same explicit source: the opt-in
	// is declared in one place only.
	plain := &FakeService{Mode: ModeValid}
	if _, err := plain.Configure(context.Background(), v2ConfigureRequest(
		"economics_v2:\n  store_id: store\n  provider_account_key: key\n  provider_request_id: request\n")); err == nil {
		t.Fatal("the default V1 connector accepted a host-bound V2 source")
	}
}

func TestFakeEconomicsV2_ClaimCarriesIdentityAndQuantityOnly(t *testing.T) {
	t.Parallel()
	svc := &FakeService{Mode: ModeValid, EconomicsV2: true}
	instance, err := svc.Configure(context.Background(), v2ConfigureRequest(
		"economics_v2:\n  store_id: store\n  provider_account_key: key\n  provider_request_id: request\n  input_tokens: 7\n  output_tokens: 11\n"))
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	stream := &v2CaptureStream{ctx: context.Background()}
	if err := instance.Execute(&v2StartStream{v2CaptureStream: stream, frame: v2StartFrame("transport-pseudonym")}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	claims := v2Claims(stream.frames)
	if len(claims) != 1 {
		t.Fatalf("the claim count = %d, want exactly one", len(claims))
	}
	claim := claims[0].Observation
	if claim.Origin != metering.OriginProvider {
		t.Fatalf("the claim origin = %q, want %q", claim.Origin, metering.OriginProvider)
	}
	if claim.Subject.Kind != metering.SubjectBLeg {
		t.Fatalf("the claim subject kind = %q, want %q", claim.Subject.Kind, metering.SubjectBLeg)
	}
	if len(claim.Charges) != 0 {
		t.Fatalf("the claim carries %d charges; the fixture carries no money value", len(claim.Charges))
	}
	if len(claim.Measures) != 2 {
		t.Fatalf("the claim carries %d measures, want the reported input and output quantities", len(claim.Measures))
	}
	// The claim echoes the transport B-leg pseudonym the host supplied; the
	// host replaces it with the sealed B-leg.
	if claim.Subject.BLegID != "transport-pseudonym" {
		t.Fatalf("the claim B-leg = %q, want the echoed transport pseudonym", claim.Subject.BLegID)
	}
	if claim.Correlation.BLegID != claim.Subject.BLegID {
		t.Fatalf("the claim correlation B-leg = %q, want %q", claim.Correlation.BLegID, claim.Subject.BLegID)
	}
}

func TestFakeEconomicsV2_TrustConflictClaimsAForeignLeg(t *testing.T) {
	t.Parallel()
	svc := &FakeService{Mode: ModeValid, EconomicsV2: true}
	instance, err := svc.Configure(context.Background(), v2ConfigureRequest(
		"economics_v2:\n  store_id: store\n  provider_account_key: key\n  provider_request_id: request\n  attempt_identity_conflict: true\n"))
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	stream := &v2CaptureStream{ctx: context.Background()}
	if err := instance.Execute(&v2StartStream{v2CaptureStream: stream, frame: v2StartFrame("transport-pseudonym")}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	claims := v2Claims(stream.frames)
	if len(claims) != 1 {
		t.Fatalf("the claim count = %d, want exactly one", len(claims))
	}
	if claims[0].Observation.Subject.BLegID == "transport-pseudonym" {
		t.Fatal("the trust-conflict control echoed the transport pseudonym")
	}
	if !strings.Contains(claims[0].Observation.Subject.BLegID, "foreign") {
		t.Fatalf("the trust-conflict control claimed %q", claims[0].Observation.Subject.BLegID)
	}
}
