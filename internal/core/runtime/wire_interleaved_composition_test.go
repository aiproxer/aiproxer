package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type mockAcceptLargeBodyAssessor struct{}

func (m *mockAcceptLargeBodyAssessor) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	return largebody.Assessment{
		Decision: largebody.AssessmentDecisionAccept,
		Reason:   largebody.DeclineReasonNone,
	}, nil
}

func (m *mockAcceptLargeBodyAssessor) LargeBodyStaticDisposition(profileID string) (largebody.StaticWireDisposition, largebody.StaticWireReason) {
	return largebody.StaticWireNeedsRequestAssessment, largebody.StaticWireReasonNone
}

// TestWireInterleaved_DeclinesWhenInterleavedEnabled proves that wire fast-path is explicitly declined
// when interleaved thinking is enabled on the executor (Item 4).
func TestWireInterleaved_DeclinesWhenInterleavedEnabled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	proof := largebody.Proof{
		ProfileID: "openai_chat_v1",
		Delivery:  aipapi.DeliveryModeStreaming,
	}

	// 1. Without interleaved thinking configured: assessor is reached and accepts.
	exDisabled := TestExecutor()
	exDisabled.LargeBodyAssessor = &mockAcceptLargeBodyAssessor{}
	exDisabled.InterleavedProcessor = nil

	if exDisabled.interleavedEnabled() {
		t.Fatalf("expected interleavedEnabled to be false when Processor is nil")
	}

	dispDisabled, reasonDisabled := exDisabled.LargeBodyStaticDisposition("openai_chat_v1")
	if dispDisabled != largebody.StaticWireNeedsRequestAssessment || reasonDisabled != largebody.StaticWireReasonNone {
		t.Fatalf("expected StaticWireNeedsRequestAssessment, got disp=%v reason=%v", dispDisabled, reasonDisabled)
	}

	assDisabled, err := exDisabled.AssessLargeBody(ctx, proof)
	if err != nil {
		t.Fatalf("unexpected assessment error: %v", err)
	}
	if !assDisabled.Accepted() {
		t.Fatalf("expected Accept when interleaved is disabled, got decision=%v reason=%v", assDisabled.Decision, assDisabled.Reason)
	}

	// 2. With interleaved thinking configured: static disposition immediately blocks to canonical,
	// and AssessLargeBody declines with DeclineReasonAuthorityBlocker.
	exEnabled := TestExecutor()
	exEnabled.LargeBodyAssessor = &mockAcceptLargeBodyAssessor{}
	exEnabled.InterleavedProcessor = &testInterleavedProcessorAdapter{}

	if !exEnabled.interleavedEnabled() {
		t.Fatalf("expected interleavedEnabled to be true when Processor is set")
	}

	dispEnabled, reasonEnabled := exEnabled.LargeBodyStaticDisposition("openai_chat_v1")
	if dispEnabled != largebody.StaticWireDefinitelyCanonical || reasonEnabled != largebody.StaticWireReasonStaticBlocker {
		t.Fatalf("expected (DefinitelyCanonical, StaticBlocker), got disp=%v reason=%v", dispEnabled, reasonEnabled)
	}

	assEnabled, err := exEnabled.AssessLargeBody(ctx, proof)
	if err != nil {
		t.Fatalf("unexpected assessment error: %v", err)
	}
	if !assEnabled.Declined() {
		t.Fatalf("expected Decline when interleaved is enabled, got decision=%v", assEnabled.Decision)
	}
	if assEnabled.Reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker, got %v", assEnabled.Reason)
	}
}
