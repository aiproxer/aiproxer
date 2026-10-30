package aipapi_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNegotiate_lossless(t *testing.T) {
	t.Parallel()

	res := aipapi.Negotiate(
		[]aipapi.Capability{aipapi.CapabilityTools, aipapi.CapabilityStreaming},
		aipapi.NewBackendCaps(aipapi.CapabilityTools, aipapi.CapabilityStreaming, aipapi.CapabilityVision),
	)
	if res.Kind != aipapi.NegotiationLossless {
		t.Fatalf("got %s", res.Kind)
	}
	if res.Err() != nil {
		t.Fatalf("unexpected err: %v", res.Err())
	}
}

func TestNegotiate_downgradeReasoningAndParallelTools(t *testing.T) {
	t.Parallel()

	par := true
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools: []aipapi.ToolDef{{Name: "t"}},
		Options: aipapi.GenerationOptions{
			ReasoningEffort:   "high",
			ParallelToolCalls: &par,
		},
	}
	required := aipapi.RequiredCapabilities(call)

	backend := aipapi.NewBackendCaps(aipapi.CapabilityTools)
	res := aipapi.Negotiate(required, backend)
	if res.Kind != aipapi.NegotiationDowngrade {
		t.Fatalf("got %s missing=%v down=%v", res.Kind, res.Missing, res.Downgraded)
	}
	if len(res.Downgraded) == 0 {
		t.Fatal("expected downgraded list")
	}
}

func TestNegotiate_rejectMissingVision(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind:     aipapi.PartImageRef,
				ImageRef: "https://example.com/x.png",
			}},
		}},
	}
	required := aipapi.RequiredCapabilities(call)

	res := aipapi.Negotiate(required, aipapi.NewBackendCaps(aipapi.CapabilityTools))
	if res.Kind != aipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
	if len(res.Missing) == 0 {
		t.Fatal("expected missing capabilities")
	}

	err := res.Err()
	if err == nil {
		t.Fatal("expected reject error")
	}
	if !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("expected ErrCapabilityReject wrap: %v", err)
	}
	if !aipapi.IsReject(err) {
		t.Fatal("expected IsReject")
	}
}

func TestRequiredCapabilities_mapsCallShape(t *testing.T) {
	t.Parallel()

	par := true
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind:     aipapi.PartImageRef,
				ImageRef: "x",
			}},
		}},
		Tools: []aipapi.ToolDef{{Name: "t"}},
		Options: aipapi.GenerationOptions{
			ResponseMIMEType:  "application/json",
			ReasoningEffort:   "low",
			ParallelToolCalls: &par,
		},
	}

	got := aipapi.RequiredCapabilities(call)
	want := map[aipapi.Capability]struct{}{
		aipapi.CapabilityVision:            {},
		aipapi.CapabilityTools:             {},
		aipapi.CapabilityStructuredOutputs: {},
		aipapi.CapabilityReasoning:         {},
		aipapi.CapabilityParallelToolCalls: {},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for _, c := range got {
		if _, ok := want[c]; !ok {
			t.Fatalf("unexpected capability %q", c)
		}
	}
}

func TestNegotiate_nilBackendTreatsAsEmpty(t *testing.T) {
	t.Parallel()

	res := aipapi.Negotiate([]aipapi.Capability{aipapi.CapabilityTools}, nil)
	if res.Kind != aipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
}

func TestApplyNegotiatedDowngrades_stripsSoftCapabilities(t *testing.T) {
	t.Parallel()
	par := true
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Options: aipapi.GenerationOptions{
			ReasoningEffort:   "high",
			ParallelToolCalls: &par,
		},
	}
	res := aipapi.Negotiate(aipapi.RequiredCapabilities(call), aipapi.NewBackendCaps(aipapi.CapabilityStreaming))
	if res.Kind != aipapi.NegotiationDowngrade {
		t.Fatalf("got %s", res.Kind)
	}
	aipapi.ApplyNegotiatedDowngrades(&call, res)
	if call.Options.ReasoningEffort != "" {
		t.Fatalf("reasoning: %q", call.Options.ReasoningEffort)
	}
	if call.Options.ParallelToolCalls != nil {
		t.Fatal("expected parallel tool calls cleared")
	}
}

func TestRequiredCapabilities_scansInstructionsAndMessages(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Instructions: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind:     aipapi.PartImageRef,
				ImageRef: "https://example.com/x.png",
			}},
		}},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.FilePart("file-123", "application/pdf", "doc.pdf"),
			},
		}},
	}
	got := aipapi.RequiredCapabilities(call)
	if !slices.Contains(got, aipapi.CapabilityVision) {
		t.Fatalf("expected vision from instructions: %v", got)
	}
	if !slices.Contains(got, aipapi.CapabilityDocuments) {
		t.Fatalf("expected documents from messages: %v", got)
	}
}

func TestRequiredCapabilities_fileRefRequiresDocuments(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.FilePart("file-123", "application/pdf", "doc.pdf"),
			},
		}},
	}
	got := aipapi.RequiredCapabilities(call)
	if !slices.Contains(got, aipapi.CapabilityDocuments) {
		t.Fatalf("expected CapabilityDocuments in %v", got)
	}
}

func TestNegotiate_rejectMissingDocuments(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.FilePart("file-123", "application/pdf", "doc.pdf"),
			},
		}},
	}
	res := aipapi.Negotiate(aipapi.RequiredCapabilities(call), aipapi.NewBackendCaps(aipapi.CapabilityStreaming))
	if res.Kind != aipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
}
