package openresponsescompat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestRepresentableContentPartKinds_includeFileVideoExtension(t *testing.T) {
	t.Parallel()
	for _, k := range []aipapi.ContentPartKind{
		aipapi.ContentPartFileRef,
		aipapi.ContentPartVideoRef,
		aipapi.ContentPartExtension,
	} {
		if !representableContentPartKind(k) {
			t.Fatalf("content part kind %q must be representable on the pinned profile", k)
		}
	}
	for _, k := range []aipapi.ContentPartKind{
		aipapi.ContentPartAnnotation,
		aipapi.ContentPartAssistantRef,
		aipapi.ContentPartSummary,
		aipapi.ContentPartReasoning,
		aipapi.ContentPartJSON,
		aipapi.ContentPartToolResult,
	} {
		if representableContentPartKind(k) {
			t.Fatalf("content part kind %q must not be silently text-mapped on the pinned profile", k)
		}
	}
}

func TestConfig_DefaultCapabilitiesDoNotClaimAnnotations(t *testing.T) {
	t.Parallel()
	cfg := mustConfig(t, "inst", minimalYAML)
	if configHasCapability(cfg.Capabilities, aipapi.CapabilityAnnotations) {
		t.Fatal("default capabilities must not claim annotations (not fully representable on the pinned profile)")
	}
	if !configHasCapability(cfg.Capabilities, aipapi.CapabilityDocuments) {
		t.Fatal("default capabilities must claim documents (representable end-to-end as input_file)")
	}
	if configHasCapability(cfg.Capabilities, aipapi.CapabilityVideoInput) {
		t.Fatal("default capabilities must not claim video_input (requires explicit declaration)")
	}
}

func TestConfig_ExplicitUnsupportedCapabilityRejected(t *testing.T) {
	t.Parallel()
	if err := decodeConfigErr(t, "inst", minimalYAML+"capabilities: [ordered_items, streaming, annotations]\n"); err == nil {
		t.Fatal("expected explicit annotations capability claim to be rejected (not representable on the pinned profile)")
	}
	if err := decodeConfigErr(t, "inst", minimalYAML+"capabilities: [ordered_items, streaming, assistant_media_refs]\n"); err == nil {
		t.Fatal("expected explicit assistant_media_refs claim to be rejected without a response media surface")
	}
}

func TestConfig_ExplicitVideoInputDeclaredAccepted(t *testing.T) {
	t.Parallel()
	cfg := mustConfig(t, "inst", minimalYAML+"capabilities: [ordered_items, streaming, video_input, documents]\n")
	if !configHasCapability(cfg.Capabilities, aipapi.CapabilityVideoInput) {
		t.Fatalf("video_input missing from declared capabilities: %+v", cfg.Capabilities)
	}
	if !configHasCapability(cfg.Capabilities, aipapi.CapabilityDocuments) {
		t.Fatalf("documents missing from declared capabilities: %+v", cfg.Capabilities)
	}
}

func TestBackend_ZeroNetworkRejectionForUnrepresentableContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cfgExtra string
		mutate   func(c *aipapi.Call)
	}{
		{
			name:     "video_input_not_declared",
			cfgExtra: "",
			mutate: func(c *aipapi.Call) {
				c.Items[0].Content = []aipapi.ContentPart{{Kind: aipapi.ContentPartVideoRef, VideoRef: "https://x/v.mp4"}}
			},
		},
		{
			name:     "annotation_not_representable",
			cfgExtra: "",
			mutate: func(c *aipapi.Call) {
				c.Items[0].Content = []aipapi.ContentPart{{Kind: aipapi.ContentPartAnnotation, Annotation: &aipapi.AnnotationPart{Type: "url_citation"}}}
			},
		},
		{
			name:     "opaque_extensions_not_declared",
			cfgExtra: "capabilities: [streaming, tools, vision, documents, reasoning, parallel_tool_calls, ordered_items, assistant_phase, item_references, compaction]\n",
			mutate: func(c *aipapi.Call) {
				c.Items[0].Content = []aipapi.ContentPart{{
					Kind:      aipapi.ContentPartExtension,
					Extension: &aipapi.ExtensionContentPart{Type: "acme:part", Data: json.RawMessage(`{"type":"acme:part"}`)},
				}}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			be, obs := newObserverBackend(t, tc.cfgExtra, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, completeResourceJSON)
			})
			call := itemAuthorityCreateCall()
			tc.mutate(&call)
			if _, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}}); err == nil {
				t.Fatal("expected rejection before any upstream work")
			}
			if obs.count() != 0 {
				t.Fatalf("rejection caused %d upstream requests, want 0", obs.count())
			}
		})
	}
}

func TestBackend_FileVideoExtensionContentForwardedLosslessly(t *testing.T) {
	t.Setenv("MY_OR_KEY", "sk-doc-secret")
	// Declare video_input explicitly; defaults claim documents and opaque
	// extensions so file and extension content parts are representable. The
	// exact acme:part extension type is declared so content-part admission
	// matches the derived extension requirement.
	caps := "capabilities: [streaming, tools, vision, documents, video_input, reasoning, parallel_tool_calls, ordered_items, assistant_phase, item_references, compaction, opaque_extensions]\n" +
		"dialects:\n  extensions:\n    - namespace: acme\n      type: acme:part\n"
	be, obs := newObserverBackend(t, "api_key_env_var_root: MY_OR_KEY\n"+caps, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeResourceJSON)
	})

	call := aipapi.Call{
		Items: []aipapi.Item{{
			Kind:   aipapi.ItemKindMessage,
			ID:     "msg-1",
			Status: aipapi.ItemStatusCompleted,
			Role:   aipapi.RoleUser,
			Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "see"},
				{Kind: aipapi.ContentPartFileRef, FileRef: "https://x/report.pdf", FileData: "aGVsbG8=", FileName: "report.pdf"},
				{Kind: aipapi.ContentPartVideoRef, VideoRef: "https://x/v.mp4"},
				{
					Kind: aipapi.ContentPartExtension,
					Extension: &aipapi.ExtensionContentPart{
						Type: "acme:part",
						Data: json.RawMessage(`{"type":"acme:part","payload":{"k":1}}`),
					},
				},
			},
		}},
	}
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	events := drainManagedEvents(t, es)
	if !hasTextDelta(events, "The weather in Paris is sunny.") {
		t.Fatalf("missing text delta in %+v", events)
	}
	if obs.count() != 1 {
		t.Fatalf("request count = %d, want 1", obs.count())
	}
	body := string(obs.last(t).Body)
	for _, marker := range []string{
		`"input_file"`,
		`"file_url":"https://x/report.pdf"`,
		`"file_data":"aGVsbG8="`,
		`"input_video"`,
		`"video_url":"https://x/v.mp4"`,
		`"type":"acme:part"`,
		`"payload":{"k":1}`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("upstream wire missing %q:\n%s", marker, body)
		}
	}
}
