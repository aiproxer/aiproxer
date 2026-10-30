package runtime

import "context"

// ALegRetirementCleanup builds the published generation's A-leg retirement
// cleanup: the prompt-cache session of the retired A-leg ends and its
// conversation view is deleted.
//
// The cleanup is bounded and never fails its caller: a retirement
// notification must not break the request path that retired the A-leg.
func ALegRetirementCleanup(promptCache PromptCacheMaintenance, conversationViewTagger ConversationViewTagger) func(string) {
	return func(aLegID string) {
		if promptCache != nil {
			promptCache.EndSession(aLegID)
		}
		if deleter, ok := conversationViewTagger.(interface {
			DeleteALeg(context.Context, string) error
		}); ok {
			_ = deleter.DeleteALeg(context.Background(), aLegID)
		}
	}
}
