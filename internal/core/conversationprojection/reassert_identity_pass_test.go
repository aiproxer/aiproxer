package conversationprojection_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// identityPassFixture is one conversation-view state a projection and reassertion
// must resolve: a 100-message legacy history, 256 never_backend identities that
// match nothing (the steady state of a conversation view whose exclusions name
// older content), and two active 4 KiB overlays - one stable prefix and one
// anchored at the ingress tail.
func identityPassFixture(tb testing.TB) (aipapi.Call, conversationprojection.Snapshot) {
	tb.Helper()
	var call aipapi.Call
	for i := range 100 {
		role := aipapi.RoleUser
		if i%2 == 1 {
			role = aipapi.RoleAssistant
		}
		call.Messages = append(call.Messages, textMessage(role, fmt.Sprintf("history-%d", i)))
	}
	snap := conversationprojection.Snapshot{StateRevision: 1}
	for i := range 256 {
		snap.NeverBackend = append(snap.NeverBackend, conversationprojection.Tag{
			Identity: conversationprojection.MessageIdentity(fmt.Sprintf("v1:%064x", i+7)),
			Reason:   "never_backend",
		})
	}
	anchors, err := conversationprojection.ComputeCallAnchors(call)
	if err != nil {
		tb.Fatalf("compute anchors: %v", err)
	}
	tail := anchors[len(anchors)-1]
	snap.Steering = []conversationprojection.Overlay{
		{
			OverlayID:           "stable",
			Revision:            1,
			SlotOrdinal:         1,
			Active:              true,
			Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleSystem, Text: overlayPayload("policy:stay-serde")},
			Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementStablePrefix},
			AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
		},
		{
			OverlayID:           "tail",
			Revision:            2,
			SlotOrdinal:         2,
			Active:              true,
			Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleUser, Text: overlayPayload("note:answer-in-json")},
			Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementAfterMessage, Anchor: &tail},
			AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
		},
	}
	return call, snap
}

// overlayPayload pads a stable prefix to exactly overlayPayloadBytes so the
// fixture carries the production-shaped 4 KiB overlay payloads the benchmark
// claims, not the short placeholders this comment used to describe.
func overlayPayload(prefix string) string {
	const overlayPayloadBytes = 4096
	if len(prefix) > overlayPayloadBytes {
		return prefix[:overlayPayloadBytes]
	}
	return prefix + strings.Repeat(" ", overlayPayloadBytes-len(prefix))
}

// BenchmarkReassert_ProductionShape measures the final reassertion on the shape a
// real conversation view produces: a 100-message history, 256 persisted
// exclusions, a 4 KiB stable-prefix overlay, and a 4 KiB overlay anchored at the
// ingress tail. The existing overlap benchmarks vary one dimension each; this one
// is the combination the runtime pays once per candidate attempt open.
func BenchmarkReassert_ProductionShape(b *testing.B) {
	call, snap := identityPassFixture(b)
	projected, evidence, err := conversationprojection.Project(call, snap)
	if err != nil {
		b.Fatalf("project setup: %v", err)
	}
	filtered, err := conversationprojection.FilterNeverBackend(call, snap)
	if err != nil {
		b.Fatalf("filter setup: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		out, _, err := conversationprojection.Reassert(projected, snap, evidence.Provenance, filtered)
		if err != nil {
			b.Fatalf("reassert: %v", err)
		}
		if len(out.Instructions) != 1 || len(out.Messages) != 101 {
			b.Fatalf("rebuilt trajectory %d instructions / %d messages, want 1 / 101", len(out.Instructions), len(out.Messages))
		}
	}
}
