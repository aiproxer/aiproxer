package interleavedthinking

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

// MemoOverlayID is the stable conversation-view overlay ID that carries the
// latest thinker memo for an A-leg. Each capture replaces the payload and
// re-resolves the anchor to the current ingress tail.
const MemoOverlayID = "interleaved-thinking-memo"

// MemoSteeringReason is the bounded content-free reason recorded on the memo
// steering overlay and its diagnostics.
const MemoSteeringReason = "interleaved_thinking_memo"

// RenderMemoPayload renders the model-visible user-message text persisted for
// a captured memo. The header keeps prior injections unambiguous without any
// protocol-specific markers.
func RenderMemoPayload(memo string) string {
	return SessionSteeringGuidanceHeader + "\n" + strings.TrimSpace(memo)
}

// MemoPutRequest builds the trusted steering mutation that persists or
// replaces the thinker memo overlay for an A-leg. Placement resolves
// after_ingress_tail against the accepted ingress trajectory frozen at turn
// preparation; the anchor therefore stays fixed while later history appends
// behind it (cache-stable).
func MemoPutRequest(memo string) steering.PutRequest {
	return steering.PutRequest{
		OverlayID: steering.OverlayID(MemoOverlayID),
		Message: steering.Message{
			Role: aipapi.RoleUser,
			Text: RenderMemoPayload(memo),
		},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              steering.ReasonCode(MemoSteeringReason),
	}
}

// IsMemoOverlay reports whether id identifies the thinker-memo steering
// overlay, so generic orchestration can filter it without knowing the policy.
func IsMemoOverlay(id string) bool {
	return id == MemoOverlayID
}
