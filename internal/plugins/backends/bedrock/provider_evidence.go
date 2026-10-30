package bedrock

import (
	"sort"
	"strconv"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// bedrockEvidenceDraft preserves the token usage and the documented cache TTL
// breakdown surfaced by ConverseStream. Cache details remain input cache-write
// subcomponents; no provider price or request debit is inferred.
func bedrockEvidenceDraft(ev aipapi.Event, usage *types.TokenUsage) coremetering.ProviderEvidenceDraft {
	draft := coremetering.ProviderUsageEvent(ev, "bedrock.converse.v2", "bedrock.converse:stream")
	if usage == nil || len(usage.CacheDetails) == 0 {
		return draft
	}
	byTTL := make(map[string]int64, len(usage.CacheDetails))
	invalid := make(map[string]bool, len(usage.CacheDetails))
	for _, detail := range usage.CacheDetails {
		ttl := string(detail.Ttl)
		if ttl != string(types.CacheTTLFiveMinutes) && ttl != string(types.CacheTTLOneHour) {
			continue
		}
		if invalid[ttl] || detail.InputTokens == nil || *detail.InputTokens < 0 {
			if detail.InputTokens != nil && *detail.InputTokens < 0 {
				invalid[ttl] = true
				delete(byTTL, ttl)
			}
			continue
		}
		value := int64(*detail.InputTokens)
		prior := byTTL[ttl]
		if prior > int64(^uint64(0)>>1)-value {
			invalid[ttl] = true
			delete(byTTL, ttl)
			continue
		}
		byTTL[ttl] = prior + value
	}
	keys := make([]string, 0, len(byTTL))
	for ttl := range byTTL {
		keys = append(keys, ttl)
	}
	sort.Strings(keys)
	for _, ttl := range keys {
		value := sdkmetering.Decimal{Coefficient: strconv.FormatInt(byTTL[ttl], 10)}
		draft.Measures = append(draft.Measures, sdkmetering.Measure{
			Key: sdkmetering.ComponentKey{
				Direction: sdkmetering.DirectionInput, Component: sdkmetering.ComponentCacheWriteInputToken,
				Unit: sdkmetering.UnitToken, SchemaID: sdkmetering.DefaultInclusionSchemaID,
				Dimensions: []sdkmetering.Dimension{{Name: "cache_lifetime", Value: ttl}},
			},
			Value: &value, Quality: sdkmetering.QualityObserved, MethodRef: "bedrock.converse.cache_lifetime.v1",
		})
	}
	return draft
}
