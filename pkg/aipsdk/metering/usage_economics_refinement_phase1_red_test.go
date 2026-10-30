package metering

import (
	_ "embed"
	"encoding/json"
	"testing"
)

//go:embed testdata/refinement_phase1_multimodal_vectors.json
var refinementPhase1MultimodalVectors []byte

type refinementPhase1Vector struct {
	ID            string            `json:"id"`
	Media         string            `json:"media"`
	Direction     string            `json:"direction"`
	Unit          string            `json:"unit"`
	ProviderValue string            `json:"provider_value"`
	CustomerValue string            `json:"customer_value"`
	Qualifiers    map[string]string `json:"qualifiers"`
	BillingCallID string            `json:"billing_call_id"`
	ALegID        string            `json:"a_leg_id"`
	BLegIDs       []string          `json:"b_leg_ids"`
	Terminal      bool              `json:"terminal"`
	Revision      int               `json:"revision"`
	Supersedes    []string          `json:"supersedes"`
}

// TestRefinementPhase1MultimodalFixtureInventory is an inventory-only fixture
// check. It makes the Phase 1 acceptance vectors concrete before the V2 DTOs
// exist and preserves native media units, including the normative video input
// token vector. It does not certify economic behavior: parent Tasks 2.1-2.5,
// 3.1-3.4 and 4.1-4.2 must provide the V2 identity, normalization and durable
// revision seams before these vectors can execute against production contracts.
func TestRefinementPhase1MultimodalFixtureInventory(t *testing.T) {
	t.Parallel()

	var vectors []refinementPhase1Vector
	if err := json.Unmarshal(refinementPhase1MultimodalVectors, &vectors); err != nil {
		t.Fatalf("decode multimodal fixture: %v", err)
	}
	want := map[string]struct {
		media     string
		direction string
		unit      string
	}{
		"image-input-provider-bound-resize":      {media: "image", direction: "input", unit: "image"},
		"image-output-provider-origin-transcode": {media: "image", direction: "output", unit: "image"},
		"audio-input-provider-bound-duration":    {media: "audio", direction: "input", unit: "second"},
		"audio-output-provider-origin-duration":  {media: "audio", direction: "output", unit: "second"},
		"video-input-provider-native-tokens":     {media: "video", direction: "input", unit: UnitToken},
		"video-output-generated-seconds":         {media: "video", direction: "output", unit: UnitSecond},
		"document-input-provider-bound-pages":    {media: "document", direction: "input", unit: "page"},
		"document-output-provider-origin-pages":  {media: "document", direction: "output", unit: "page"},
		"multimodal-derived-aggregate":           {media: "mixed", direction: "none", unit: "aggregate"},
		"same-a-leg-resumed-billing-call":        {media: "continuation", direction: "none", unit: "call"},
		"preterminal-revision":                   {media: "audio", direction: "output", unit: "second"},
		"late-correction-after-terminal":         {media: "audio", direction: "output", unit: "second"},
	}
	got := make(map[string]struct{}, len(vectors))
	byID := make(map[string]refinementPhase1Vector, len(vectors))
	for _, vector := range vectors {
		if _, duplicate := got[vector.ID]; duplicate {
			t.Fatalf("duplicate fixture vector %q", vector.ID)
		}
		got[vector.ID] = struct{}{}
		byID[vector.ID] = vector
		expect, ok := want[vector.ID]
		if !ok {
			t.Fatalf("unexpected fixture vector %q", vector.ID)
		}
		if vector.Media != expect.media || vector.Direction != expect.direction || vector.Unit != expect.unit {
			t.Fatalf("vector %q identity = (%q, %q, %q), want (%q, %q, %q)", vector.ID, vector.Media, vector.Direction, vector.Unit, expect.media, expect.direction, expect.unit)
		}
		if vector.ProviderValue == "" || vector.CustomerValue == "" || len(vector.Qualifiers) == 0 {
			t.Fatalf("vector %q lacks provider/customer values or transform qualifiers", vector.ID)
		}
		if vector.BillingCallID == "" || vector.ALegID == "" || len(vector.BLegIDs) == 0 || vector.Revision < 1 {
			t.Fatalf("vector %q lacks call/leg lineage or revision", vector.ID)
		}
		if vector.ID == "late-correction-after-terminal" && len(vector.Supersedes) != 1 {
			t.Fatalf("late correction %q must supersede exactly one preterminal observation", vector.ID)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("fixture vectors = %d, want %d", len(got), len(want))
	}

	// The media vectors must retain both sides of each transform. Equal values
	// would erase the provider-bound/customer-bound distinction this fixture is
	// intended to make visible. These are the original phase-1 media vectors; the
	// inventory deliberately does not invent additional provider semantics.
	for _, id := range []string{
		"image-input-provider-bound-resize", "image-output-provider-origin-transcode",
		"audio-input-provider-bound-duration", "audio-output-provider-origin-duration",
		"video-input-provider-native-tokens", "video-output-generated-seconds",
		"document-input-provider-bound-pages", "document-output-provider-origin-pages",
	} {
		vector := byID[id]
		if vector.ProviderValue == vector.CustomerValue {
			t.Fatalf("transformed media vector %q collapsed provider/customer values to %q", id, vector.ProviderValue)
		}
		boundary := vector.Qualifiers["boundary"]
		if boundary != "customer_ingress" && boundary != "customer_egress" {
			t.Fatalf("transformed media vector %q boundary = %q, want customer ingress or egress", id, boundary)
		}
		if vector.Qualifiers["transform"] == "" {
			t.Fatalf("transformed media vector %q has no transform qualifier", id)
		}
	}

	videoInput := byID["video-input-provider-native-tokens"]
	videoOutput := byID["video-output-generated-seconds"]
	if videoInput.Direction != "input" || videoInput.Unit != UnitToken || videoInput.Qualifiers["tokenizer"] != "provider_native" {
		t.Fatalf("video input = direction:%q unit:%q tokenizer:%q, want input/token/provider_native", videoInput.Direction, videoInput.Unit, videoInput.Qualifiers["tokenizer"])
	}
	if videoOutput.Direction != "output" || videoOutput.Unit != UnitSecond || videoOutput.Qualifiers["generation"] != "provider_generated" {
		t.Fatalf("video output = direction:%q unit:%q generation:%q, want output/second/provider_generated", videoOutput.Direction, videoOutput.Unit, videoOutput.Qualifiers["generation"])
	}
	if inputRate, outputRate := videoInput.Qualifiers["rate"], videoOutput.Qualifiers["rate"]; inputRate == "" || outputRate == "" || inputRate == outputRate {
		t.Fatalf("video rate qualifiers = input:%q output:%q, want distinct non-empty rates", inputRate, outputRate)
	}

	// Pre-terminal and late-correction records are two revisions of the same
	// B-leg economic identity. Execution terminality changes, but call/leg
	// lineage remains stable and the correction explicitly supersedes revision 1.
	pre := byID["preterminal-revision"]
	late := byID["late-correction-after-terminal"]
	if pre.BillingCallID != late.BillingCallID || pre.ALegID != late.ALegID || len(pre.BLegIDs) != 1 || len(late.BLegIDs) != 1 || pre.BLegIDs[0] != late.BLegIDs[0] {
		t.Fatalf("revision lineage changed across terminal correction: pre=%+v late=%+v", pre, late)
	}
	if pre.Terminal || pre.Revision != 1 {
		t.Fatalf("preterminal revision = terminal:%v revision:%d, want false/1", pre.Terminal, pre.Revision)
	}
	if !late.Terminal || late.Revision != 2 || len(late.Supersedes) != 1 || late.Supersedes[0] != pre.ID {
		t.Fatalf("late correction = terminal:%v revision:%d supersedes:%v, want true/2/[preterminal-revision]", late.Terminal, late.Revision, late.Supersedes)
	}
}
