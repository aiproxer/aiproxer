package backendplugin

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// phase1SidebandFixture is the frozen V1 protobuf boundary for finalizer and
// host-only accounting evidence. It protects field numbers, presence, and
// identity while V2 contracts are designed.
//
//go:embed testdata/phase1_v1_sideband_compatibility.json
var phase1SidebandFixture embed.FS

type phase1WireFixture struct {
	Payload    json.RawMessage `json:"payload"`
	WireSHA256 string          `json:"wire_sha256"`
}

type phase1SidebandFixtureDocument struct {
	FinalizeRequest  phase1WireFixture `json:"finalize_request"`
	FinalizeResponse phase1WireFixture `json:"finalize_response"`
	AccountingFrame  phase1WireFixture `json:"accounting_frame"`
	FieldNumbers     map[string]int    `json:"field_numbers"`
}

func readPhase1SidebandFixture(t *testing.T) phase1SidebandFixtureDocument {
	t.Helper()
	payload, err := phase1SidebandFixture.ReadFile("testdata/phase1_v1_sideband_compatibility.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture phase1SidebandFixtureDocument
	if err := json.Unmarshal(payload, &fixture); err != nil {
		t.Fatalf("decode sideband fixture: %v", err)
	}
	return fixture
}

func assertPhase1WirePayload(t *testing.T, name string, raw json.RawMessage, wantSHA string) {
	t.Helper()
	if len(raw) == 0 || !json.Valid(raw) {
		t.Fatalf("%s wire payload is empty or invalid JSON", name)
	}
	canonical := phase1CompactJSON(t, name, raw)
	sum := sha256.Sum256(canonical)
	got := hex.EncodeToString(sum[:])
	if got != wantSHA {
		t.Fatalf("%s wire SHA-256 = %s, want %s", name, got, wantSHA)
	}
}

func assertPhase1WireRoundTrip(t *testing.T, name string, raw json.RawMessage, message protoreflect.ProtoMessage) {
	t.Helper()
	encoded, err := protojson.MarshalOptions{UseProtoNames: false, EmitUnpopulated: false}.Marshal(message)
	if err != nil {
		t.Fatalf("marshal %s wire payload: %v", name, err)
	}
	if !bytes.Equal(phase1CompactJSON(t, name+" encoded", encoded), phase1CompactJSON(t, name, raw)) {
		t.Fatalf("%s wire payload changed on round-trip:\n got  %s\n want %s", name, encoded, raw)
	}
}

func phase1CompactJSON(t *testing.T, name string, raw []byte) []byte {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatalf("compact %s wire payload: %v", name, err)
	}
	return compact.Bytes()
}

func TestPhase1V1SidebandAndFinalizerFixtures(t *testing.T) {
	t.Parallel()
	fixture := readPhase1SidebandFixture(t)

	var requestWire backendpluginv1.FinalizeBillingRequest
	if err := protojson.Unmarshal(fixture.FinalizeRequest.Payload, &requestWire); err != nil {
		t.Fatalf("decode finalize request: %v", err)
	}
	request, err := FinalizeBillingRequestFromProto(&requestWire)
	if err != nil {
		t.Fatalf("finalize request conversion: %v", err)
	}
	wantRequest := FinalizeBillingRequest{
		InstanceID: "instance-phase1", ALegID: "a-phase1", BLegID: "b-primary", ModelID: "model-phase1", Reason: "terminal", IdempotencyKey: "bc_0123456789abcdef0123456789abcdef:b-primary",
	}
	if !reflect.DeepEqual(request, wantRequest) {
		t.Fatalf("finalize request = %+v, want %+v", request, wantRequest)
	}
	assertPhase1WirePayload(t, "finalize request", fixture.FinalizeRequest.Payload, fixture.FinalizeRequest.WireSHA256)
	assertPhase1WireRoundTrip(t, "finalize request", fixture.FinalizeRequest.Payload, &requestWire)

	var responseWire backendpluginv1.FinalizeBillingResponse
	if err := protojson.Unmarshal(fixture.FinalizeResponse.Payload, &responseWire); err != nil {
		t.Fatalf("decode finalize response: %v", err)
	}
	response, err := FinalizeBillingResponseFromProto(&responseWire)
	if err != nil {
		t.Fatalf("finalize response conversion: %v", err)
	}
	if response.EvidenceQuality != "provider_reported" || response.Usage.InputTokens == nil || *response.Usage.InputTokens != 123 || response.Usage.OutputTokens == nil || *response.Usage.OutputTokens != 45 || response.Usage.TotalTokens == nil || *response.Usage.TotalTokens != 170 {
		t.Fatalf("finalize response usage = %+v, quality=%q", response.Usage, response.EvidenceQuality)
	}
	if response.Usage.CacheReadTokens != nil || response.Usage.CacheWriteTokens != nil || response.Usage.ReasoningTokens != nil {
		t.Fatal("finalize response invented absent counters")
	}
	if response.Usage.Presence != (UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true}) {
		t.Fatalf("finalize response presence = %+v", response.Usage.Presence)
	}
	assertPhase1WirePayload(t, "finalize response", fixture.FinalizeResponse.Payload, fixture.FinalizeResponse.WireSHA256)
	assertPhase1WireRoundTrip(t, "finalize response", fixture.FinalizeResponse.Payload, &responseWire)

	var frameWire backendpluginv1.ExecuteServerFrame
	if err := protojson.Unmarshal(fixture.AccountingFrame.Payload, &frameWire); err != nil {
		t.Fatalf("decode accounting frame: %v", err)
	}
	frame, err := ServerFrameFromProto(&frameWire)
	if err != nil {
		t.Fatalf("accounting frame conversion: %v", err)
	}
	if frame.Kind != ServerFrameAccountingEvidence || frame.Sequence != 7 || frame.Accounting == nil {
		t.Fatalf("accounting frame identity = kind=%q sequence=%d evidence=%+v", frame.Kind, frame.Sequence, frame.Accounting)
	}
	if frame.Accounting.InputTokens == nil || *frame.Accounting.InputTokens != 123 || frame.Accounting.OutputTokens == nil || *frame.Accounting.OutputTokens != 45 {
		t.Fatalf("accounting frame quantities = %+v", frame.Accounting)
	}
	if frame.Accounting.Presence != (UsagePresence{InputTokens: true, OutputTokens: true}) || frame.Accounting.Source != AccountingSourceProviderReported || frame.Accounting.Authority != AccountingAuthorityAuthoritative || frame.Accounting.Plane != AccountingPlaneProviderBillable || frame.Accounting.DedupeKey != "provider-charge-phase1" {
		t.Fatalf("accounting frame evidence = %+v", frame.Accounting)
	}
	assertPhase1WirePayload(t, "accounting frame", fixture.AccountingFrame.Payload, fixture.AccountingFrame.WireSHA256)
	assertPhase1WireRoundTrip(t, "accounting frame", fixture.AccountingFrame.Payload, &frameWire)
}

func TestPhase1V1SidebandFieldNumbersRemainCompatible(t *testing.T) {
	t.Parallel()
	fixture := readPhase1SidebandFixture(t)
	for qualified, want := range fixture.FieldNumbers {
		messageName, fieldName, ok := strings.Cut(qualified, ".")
		if !ok || messageName == "" || fieldName == "" || strings.Contains(fieldName, ".") {
			t.Fatalf("invalid field-number key %q", qualified)
		}
		message := backendpluginv1.File_backendplugin_v1_backend_proto.Messages().ByName(protoreflect.Name(messageName))
		if message == nil {
			t.Fatalf("field-number fixture references unknown message %q", messageName)
		}
		field := message.Fields().ByName(protoreflect.Name(fieldName))
		if field == nil {
			t.Fatalf("field-number fixture references unknown field %s", qualified)
		}
		if int(field.Number()) != want {
			t.Fatalf("field %s = %d, want %d", qualified, field.Number(), want)
		}
	}
}

func TestPhase1V1SidebandPayloadsContainNoRawEconomicContent(t *testing.T) {
	t.Parallel()
	fixture := readPhase1SidebandFixture(t)
	for name, raw := range map[string]json.RawMessage{
		"finalize request":  fixture.FinalizeRequest.Payload,
		"finalize response": fixture.FinalizeResponse.Payload,
		"accounting frame":  fixture.AccountingFrame.Payload,
	} {
		text := strings.ToLower(string(raw))
		for _, forbidden := range []string{"prompt", "completion", "authorization", "secret", "credential", "raw_usage_json"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s fixture contains forbidden raw economic content marker %q", name, forbidden)
			}
		}
	}
}
