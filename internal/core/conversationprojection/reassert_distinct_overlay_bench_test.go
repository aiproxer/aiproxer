package conversationprojection_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// benchSnapshotDistinctOverlays returns a steering snapshot with n active
// overlays whose payloads differ, which is how real steering messages reach
// the projection: one overlay per slot, each with its own text. The existing
// 64-overlay fixture reuses one payload, so every overlay shares one message
// identity and it cannot exercise per-identity work.
func benchSnapshotDistinctOverlays(n, textBytes int) conversationprojection.Snapshot {
	steering := make([]conversationprojection.Overlay, 0, n)
	for i := range n {
		prefix := fmt.Sprintf("overlay-%04d:", i)
		text := prefix + strings.Repeat("a", textBytes-len(prefix))
		steering = append(steering, conversationprojection.Overlay{
			OverlayID:           fmt.Sprintf("ov-%d", i),
			Revision:            1,
			SlotOrdinal:         uint64(i + 1),
			Active:              true,
			Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleSystem, Text: text},
			Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementStablePrefix},
			AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
		})
	}
	return conversationprojection.Snapshot{StateRevision: 1, Steering: steering}
}

// BenchmarkReassert_DistinctOverlays covers the final conversation-view
// reassertion that runs once per candidate attempt open with the frozen
// request-local snapshot. 64 overlays is the conversation-view contract
// maximum (MaxActiveOverlays); each overlay carries a distinct 4 KiB payload.
func BenchmarkReassert_DistinctOverlays(b *testing.B) {
	for _, overlays := range []int{1, 4, 16, 64} {
		b.Run(fmt.Sprintf("overlays=%d", overlays), func(b *testing.B) {
			base := benchCallWithMessages(10)
			snap := benchSnapshotDistinctOverlays(overlays, 4096)
			projected, ev, err := conversationprojection.Project(base, snap)
			if err != nil {
				b.Fatalf("project setup: %v", err)
			}
			filtered, err := conversationprojection.FilterNeverBackend(base, snap)
			if err != nil {
				b.Fatalf("filter setup: %v", err)
			}
			b.ReportAllocs()
			for b.Loop() {
				out, _, err := conversationprojection.Reassert(projected, snap, ev.Provenance, filtered)
				if err != nil {
					b.Fatalf("reassert: %v", err)
				}
				if len(out.Instructions) != overlays {
					b.Fatalf("instructions %d want %d", len(out.Instructions), overlays)
				}
			}
		})
	}
}
