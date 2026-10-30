package openairesponses

import (
	"encoding/json"
	"strings"
	"testing"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaiusage"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/openai/openai-go/v3/responses"
)

const nativeEconomicsResponseJSON = `{
  "id": "resp_native_economics",
  "object": "response",
  "created_at": 1719900000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [
    {
      "type": "message",
      "id": "msg_native_economics",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "see"},
        {"type": "input_image", "image_url": "https://cdn.example.com/out.png"},
        {"type": "input_file", "file_id": "file-output-1"}
      ]
    }
  ],
  "usage": {
    "input_tokens": 4,
    "output_tokens": 6,
    "total_tokens": 10,
    "input_image_tokens": 23,
    "output_image_count": 2,
    "input_audio_seconds": 1.5,
    "output_video_frames": 12,
    "input_document_pages": 3,
    "output_bytes": 4096,
    "cost": 0.00125
  }
}`

func nativeEconomicsResponse(t *testing.T, raw string) responses.Response {
	t.Helper()
	var resp responses.Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestProviderEvidence_ResponseNativeMediaCostAndAssistantOutput(t *testing.T) {
	t.Parallel()
	resp := nativeEconomicsResponse(t, nativeEconomicsResponseJSON)
	usage := usageFromResponse(resp)
	if usage == nil {
		t.Fatal("final provider usage was not emitted")
	}
	openaiusage.AnnotateProviderContext(usage, resp.ID, string(resp.ServiceTier))
	draft := openaiusage.ProviderEvidenceDraft(*usage, "openai.responses.v2", "openai.responses.usage:"+resp.ID)

	want := map[string]string{
		"input/image_token/token": "23/0",
		"output/image/image":      "2/0",
		"input/audio/second":      "15/1",
		"output/video/frame":      "12/0",
		"input/document/page":     "3/0",
		"output/file/byte":        "4096/0",
	}
	for _, measure := range draft.Measures {
		if measure.Key.SchemaID != "openai.usage.v2" {
			continue
		}
		key := string(measure.Key.Direction) + "/" + measure.Key.Component + "/" + measure.Key.Unit
		value, ok := want[key]
		if !ok {
			continue
		}
		if measure.Value == nil || measure.Value.CanonicalString() != value {
			t.Errorf("native measure %q = %+v, want %s", key, measure.Value, value)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("native measures missing = %+v; all measures = %+v", want, draft.Measures)
	}
	if len(draft.Charges) != 1 || draft.Charges[0].Amount == nil || draft.Charges[0].Amount.CanonicalString() != "125/5" {
		t.Fatalf("provider-reported cost = %+v, want exact 125/5 aggregate", draft.Charges)
	}
	var costAmount bool
	for _, field := range draft.Evidence {
		if field.Path == "$.cost.amount" && field.Present && field.Lexeme == "0.00125" {
			costAmount = true
		}
	}
	if !costAmount {
		t.Fatalf("raw provider cost lexeme was not retained: %+v", draft.Evidence)
	}
	if draft.Authority != sdkmetering.AuthorityObservedClaim || draft.Acquisition != sdkmetering.AcquisitionProviderResponse {
		t.Fatalf("provider authority/provenance = %q/%q", draft.Authority, draft.Acquisition)
	}

	events, err := CompletionEvents(resp)
	if err != nil {
		t.Fatal(err)
	}
	var imageRef, fileRef bool
	for _, event := range events {
		switch event.Kind {
		case aipapi.EventAssistantImageRef:
			imageRef = event.AssistantRef == "https://cdn.example.com/out.png"
		case aipapi.EventAssistantFileRef:
			fileRef = event.AssistantRef == "file-output-1"
		}
	}
	if !imageRef || !fileRef {
		t.Fatalf("assistant output refs were not preserved: %+v", events)
	}
}

func TestProviderEvidence_ResponseNativeMediaOnlyUsageIsEmitted(t *testing.T) {
	t.Parallel()
	const raw = `{
  "id": "resp_native_only",
  "object": "response",
  "created_at": 1719900000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [],
  "usage": {
    "input_image_tokens": 23,
    "output_audio_seconds": 1.5
  }
}`
	resp := nativeEconomicsResponse(t, raw)
	usage := usageFromResponse(resp)
	if usage == nil {
		t.Fatal("native-only Responses usage was dropped")
	}
	measures := openaiusage.NativeUsageMeasures(usage.RawUsageJSON)
	if len(measures) != 2 {
		t.Fatalf("native-only measures = %d, want 2: %+v", len(measures), measures)
	}
}

func TestProviderEvidence_AssistantMediaRefsAloneDoNotInventUsage(t *testing.T) {
	t.Parallel()
	const raw = `{
  "id": "resp_media_refs_only",
  "object": "response",
  "created_at": 1719900000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [{
    "type": "message",
    "id": "msg_media_refs_only",
    "status": "completed",
    "role": "assistant",
    "content": [
      {"type": "input_image", "image_url": "https://cdn.example.com/out.png"},
      {"type": "input_file", "file_id": "file-output-1"}
    ]
  }]
}`
	resp := nativeEconomicsResponse(t, raw)
	events, err := CompletionEvents(resp)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == aipapi.EventUsageDelta {
			t.Fatalf("assistant media refs invented provider usage: %+v", event)
		}
	}
	stream := openaiusage.NewProviderEvidenceStream(events, "openai.responses.v2")
	binder, ok := stream.(coremetering.ProviderEvidenceBinder)
	if !ok {
		t.Fatal("Responses evidence stream does not expose its binder")
	}
	binder.BindEconomicEvidence(coremetering.ObservationIdentity{StoreID: "store", BLegID: "b-leg"})
	source, ok := stream.(interface {
		DrainEconomicObservations() []sdkmetering.Observation
	})
	if !ok {
		t.Fatal("Responses evidence stream does not expose observation source")
	}
	observations := source.DrainEconomicObservations()
	if len(observations) != 0 {
		t.Fatalf("assistant media refs invented provider observations: %+v", observations)
	}
}

func TestSDKStreamProviderEvidence_BindsFinalAndLateCorrection(t *testing.T) {
	t.Parallel()
	resp := nativeEconomicsResponse(t, nativeEconomicsResponseJSON)
	var union responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(`{"type":"response.completed","sequence_number":1,"response":`+nativeEconomicsResponseJSON+`}`), &union); err != nil {
		t.Fatal(err)
	}
	s := &sdkStream{
		pending:                stream.NewPendingEventQueue(0),
		ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer(),
	}
	if err := s.handleUnion(union); err != nil {
		t.Fatal(err)
	}
	if got := s.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("unbound final provider evidence escaped: %d", len(got))
	}
	s.BindEconomicEvidence(coremetering.ObservationIdentity{
		StoreID: "store", RequestID: "request", CallID: "call", BillingCallID: "billing",
		ALegID: "a-leg", BLegID: "b-leg", AttemptID: "attempt",
	})
	initial := s.DrainEconomicObservations()
	if len(initial) != 1 {
		t.Fatalf("final provider observations = %d, want 1", len(initial))
	}
	if initial[0].Version != sdkmetering.ObservationVersionV2 || initial[0].Subject.BLegID != "b-leg" || initial[0].Subject.ProviderRequestID != resp.ID {
		t.Fatalf("final provider lineage = %+v", initial[0])
	}
	if initial[0].Semantics != sdkmetering.SemanticsCumulative {
		t.Fatalf("final provider semantics = %q, want cumulative", initial[0].Semantics)
	}

	correctedRaw := strings.Replace(nativeEconomicsResponseJSON, `"input_tokens": 4`, `"input_tokens": 5`, 1)
	corrected := nativeEconomicsResponse(t, correctedRaw)
	correctedUsage := usageFromResponse(corrected)
	if correctedUsage == nil {
		t.Fatal("corrected provider usage was not emitted")
	}
	openaiusage.AnnotateProviderContext(correctedUsage, corrected.ID, string(corrected.ServiceTier))
	s.Add(openaiusage.ProviderEvidenceDraft(*correctedUsage, "openai.responses.v2", "openai.responses.usage:"+resp.ID))
	late := s.DrainEconomicObservations()
	if len(late) != 1 || late[0].Revision != 2 || late[0].Semantics != sdkmetering.SemanticsReplacement {
		t.Fatalf("late provider correction = %+v, want revision 2 replacement", late)
	}
	if len(late[0].Supersedes) != 1 || late[0].Supersedes[0].ObservationID != initial[0].ID {
		t.Fatalf("late provider supersession = %+v, want %q", late[0].Supersedes, initial[0].ID)
	}
}

func TestProviderCapabilitiesExplicitlyOmitAssistantMediaRefs(t *testing.T) {
	t.Parallel()
	backend := New(Config{BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"})
	if _, ok := backend.Caps[aipapi.CapabilityAssistantMediaRefs]; ok {
		t.Fatal("OpenAI Responses must not advertise assistant media refs without a negotiated response-media surface")
	}
	negotiated := aipapi.Negotiate(
		[]aipapi.Capability{aipapi.CapabilityAssistantMediaRefs},
		backend.Caps,
	)
	if negotiated.Kind != aipapi.NegotiationReject {
		t.Fatalf("assistant media capability negotiation = %q, want reject", negotiated.Kind)
	}
	if len(negotiated.Missing) != 1 || negotiated.Missing[0] != aipapi.CapabilityAssistantMediaRefs {
		t.Fatalf("missing assistant media capability = %v", negotiated.Missing)
	}
}
