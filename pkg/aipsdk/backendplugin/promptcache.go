package backendplugin

import (
	"context"
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

type PromptCacheController interface {
	RenewPromptCache(context.Context, promptcache.RenewRequest) (promptcache.RenewResponse, error)
	ReleasePromptCache(context.Context, promptcache.ReleaseRequest) error
}
type PromptCacheObservationSource interface{ promptcache.ObservationSource }

func PromptCacheNegotiated(neg Negotiation) bool {
	return neg.Compatible && neg.NegotiatedMinor >= ProtocolMinorPromptCacheResidency && slices.Contains(neg.EnabledFeatures, FeaturePromptCacheResidency)
}
