package interleavedthinking

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

func TestRenderMemoPayload_HeaderAndTrim(t *testing.T) {
	t.Parallel()
	got := RenderMemoPayload("  plan: do the thing\n")
	want := SessionSteeringGuidanceHeader + "\nplan: do the thing"
	if got != want {
		t.Fatalf("RenderMemoPayload = %q, want %q", got, want)
	}
}

func TestMemoPutRequest_PolicyFields(t *testing.T) {
	t.Parallel()
	req := MemoPutRequest("memo body")
	if req.OverlayID != steering.OverlayID(MemoOverlayID) {
		t.Fatalf("OverlayID = %q, want %q", req.OverlayID, MemoOverlayID)
	}
	if req.Message.Role != aipapi.RoleUser {
		t.Fatalf("Role = %q, want user", req.Message.Role)
	}
	if !strings.Contains(req.Message.Text, SessionSteeringGuidanceHeader) {
		t.Fatalf("Text missing header: %q", req.Message.Text)
	}
	if req.Placement != steering.AfterIngressTail {
		t.Fatalf("Placement = %q, want after_ingress_tail", req.Placement)
	}
	if req.AnchorMissingPolicy != steering.StablePrefixFallback {
		t.Fatalf("AnchorMissingPolicy = %q, want stable_prefix_fallback", req.AnchorMissingPolicy)
	}
	if req.Reason != steering.ReasonCode(MemoSteeringReason) {
		t.Fatalf("Reason = %q, want %q", req.Reason, MemoSteeringReason)
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("PutRequest.Validate: %v", err)
	}
}

func TestIsMemoOverlay(t *testing.T) {
	t.Parallel()
	if !IsMemoOverlay(MemoOverlayID) {
		t.Fatalf("IsMemoOverlay(%q) = false, want true", MemoOverlayID)
	}
	if IsMemoOverlay("other-overlay") {
		t.Fatal("IsMemoOverlay(other-overlay) = true, want false")
	}
	if IsMemoOverlay("") {
		t.Fatal("IsMemoOverlay(empty) = true, want false")
	}
}
