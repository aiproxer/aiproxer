package aipapi_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestAdmission_OpaqueExtensionContentPartNegotiation(t *testing.T) {
	t.Parallel()
	call := callWith(itemWith(extContentPart("acme:part", `{"type":"acme:part"}`)))
	required := aipapi.RequiredCapabilities(call)

	// Without CapabilityOpaqueExtensions the backend must hard-reject before work.
	without := aipapi.Negotiate(required, aipapi.NewBackendCaps(aipapi.CapabilityOrderedItems, aipapi.CapabilityDocuments))
	if without.Kind != aipapi.NegotiationReject {
		t.Fatalf("expected reject without opaque_extensions, got %s", without.Kind)
	}
	// With CapabilityOpaqueExtensions admission is lossless.
	with := aipapi.Negotiate(required, aipapi.NewBackendCaps(aipapi.CapabilityOrderedItems, aipapi.CapabilityOpaqueExtensions))
	if with.Kind != aipapi.NegotiationLossless {
		t.Fatalf("expected lossless with opaque_extensions, got %s", with.Kind)
	}
}

func TestAdmission_FileAndVideoContentPartNegotiation(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{Items: []aipapi.Item{itemWith(
		aipapi.ContentPart{Kind: aipapi.ContentPartFileRef, FileRef: "https://x/report.pdf"},
		aipapi.ContentPart{Kind: aipapi.ContentPartVideoRef, VideoRef: "https://x/v.mp4"},
	)}}
	required := aipapi.RequiredCapabilities(call)
	caps := aipapi.NewBackendCaps()
	for _, c := range required {
		caps[c] = struct{}{}
	}
	if res := aipapi.Negotiate(required, caps); res.Kind != aipapi.NegotiationLossless {
		t.Fatalf("expected lossless with full caps, got %s missing=%v", res.Kind, res.Missing)
	}
	// Dropping video_input must hard-reject (video is not soft-downgradable).
	noVideo := aipapi.NewBackendCaps(aipapi.CapabilityOrderedItems, aipapi.CapabilityDocuments)
	if res := aipapi.Negotiate(required, noVideo); res.Kind != aipapi.NegotiationReject {
		t.Fatalf("expected reject without video_input, got %s", res.Kind)
	}
}

func TestAdmission_InlineFileDataRequiresDocuments(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{Items: []aipapi.Item{itemWith(
		aipapi.ContentPart{Kind: aipapi.ContentPartFileRef, FileData: "aGVsbG8="},
	)}}
	required := aipapi.RequiredCapabilities(call)
	hasDocs := false
	for _, c := range required {
		if c == aipapi.CapabilityDocuments {
			hasDocs = true
		}
	}
	if !hasDocs {
		t.Fatalf("inline file_data call must require documents capability, got %v", required)
	}
	res := aipapi.Negotiate(required, aipapi.NewBackendCaps(aipapi.CapabilityOrderedItems))
	if res.Kind != aipapi.NegotiationReject {
		t.Fatalf("expected reject without documents, got %s", res.Kind)
	}
}

func TestJSONRoundTrip_ContentPartExtensionAndFileData(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{Items: []aipapi.Item{itemWith(
		aipapi.ContentPart{Kind: aipapi.ContentPartFileRef, FileData: "aGVsbG8=", FileName: "minimal.pdf"},
		extContentPart("acme:part", `{"type":"acme:part","payload":{"k":1}}`),
	)}}
	raw, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	var back aipapi.Call
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got := back.Items[0].Content
	if got[0].FileData != "aGVsbG8=" || got[0].FileName != "minimal.pdf" {
		t.Fatalf("file_data json round-trip mismatch: %+v", got[0])
	}
	if got[1].Kind != aipapi.ContentPartExtension || got[1].Extension == nil ||
		got[1].Extension.Type != "acme:part" || string(got[1].Extension.Data) != `{"type":"acme:part","payload":{"k":1}}` {
		t.Fatalf("extension json round-trip mismatch: %+v", got[1])
	}
}
