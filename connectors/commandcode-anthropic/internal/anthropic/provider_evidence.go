package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// usageFields retains wire presence for each Anthropic-compatible counter. A
// present zero is meaningful; a missing or malformed field stays unavailable.
type usageFields struct {
	InputTokens              *int                          `json:"input_tokens"`
	OutputTokens             *int                          `json:"output_tokens"`
	CacheCreationInputTokens *int                          `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int                          `json:"cache_read_input_tokens"`
	ReasoningTokens          *int                          `json:"reasoning_tokens"`
	ThinkingTokens           *int                          `json:"thinking_tokens"`
	TotalTokens              *int                          `json:"total_tokens"`
	CacheCreation            *anthropicCacheCreationFields `json:"cache_creation"`
	ServerToolUse            *anthropicServerToolUseFields `json:"server_tool_use"`
	ServiceTier              string                        `json:"service_tier"`
}

type anthropicCacheCreationFields struct {
	Ephemeral5mInputTokens *int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens *int `json:"ephemeral_1h_input_tokens"`
}

type anthropicServerToolUseFields struct {
	WebFetchRequests  *int `json:"web_fetch_requests"`
	WebSearchRequests *int `json:"web_search_requests"`
}

func commandcodeUsageEvent(usage *usageFields, requestID string) *aipapi.Event {
	if usage == nil {
		return nil
	}
	input, inputPresent := nonNegativeInt(usage.InputTokens)
	output, outputPresent := nonNegativeInt(usage.OutputTokens)
	cacheRead, cacheReadPresent := nonNegativeInt(usage.CacheReadInputTokens)
	cacheWrite, cacheWritePresent := nonNegativeInt(usage.CacheCreationInputTokens)
	reasoning, reasoningPresent := nonNegativeInt(usage.ReasoningTokens)
	if !reasoningPresent {
		reasoning, reasoningPresent = nonNegativeInt(usage.ThinkingTokens)
	}
	total, totalPresent := nonNegativeInt(usage.TotalTokens)
	presence := aipapi.UsagePresence{
		InputTokens: inputPresent, OutputTokens: outputPresent,
		CacheReadTokens: cacheReadPresent, CacheWriteTokens: cacheWritePresent,
		ReasoningTokens: reasoningPresent, TotalTokens: totalPresent,
	}
	if !presence.Any() {
		return nil
	}
	return &aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: input, OutputTokens: output,
		CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite,
		ReasoningTokens: reasoning, TotalTokens: total, UsagePresence: presence,
		RawUsageJSON: marshalUsageFields(usage),
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority:         aipapi.UsageAuthorityAuthoritative,
			ProviderRequestID: strings.TrimSpace(requestID),
			ServiceContext:    strings.TrimSpace(usage.ServiceTier),
			DedupeKey:         "commandcode.anthropic.usage:stream",
			// CommandCode Anthropic serves the Anthropic Messages protocol
			// at /v1/messages with the anthropic-version header. In that
			// usage object input_tokens counts the prompt tokens that were
			// neither read from cache nor written to cache, and the cached
			// and the written prompt subsets are reported as the separate
			// cache_read_input_tokens and cache_creation_input_tokens
			// counters, so both relations are declared with the counters
			// they qualify.
			InputTokensExcludeCacheRead:  true,
			InputTokensExcludeCacheWrite: true,
		},
	}
}

func nonNegativeInt(value *int) (int, bool) {
	if value == nil || *value < 0 {
		return 0, false
	}
	return *value, true
}

func annotateProviderUsageEvents(events []aipapi.Event, providerRequestID string) {
	for i := range events {
		if events[i].Kind != aipapi.EventUsageDelta {
			continue
		}
		accounting := events[i].Accounting
		accounting.Plane = aipapi.UsagePlaneProviderBillable
		accounting.Source = aipapi.UsageSourceProviderReported
		accounting.Authority = aipapi.UsageAuthorityAuthoritative
		if strings.TrimSpace(accounting.ProviderRequestID) == "" {
			accounting.ProviderRequestID = strings.TrimSpace(providerRequestID)
		}
		accounting.DedupeKey = "commandcode.anthropic.usage:stream"
		// Non-stream usage carries the same Anthropic Messages input
		// semantics as the streamed snapshots: input_tokens excludes the
		// cached and the written prompt subsets.
		accounting.InputTokensExcludeCacheRead = true
		accounting.InputTokensExcludeCacheWrite = true
		events[i].Accounting = accounting
	}
}

// mergeAnthropicUsageEvent joins partial message_start/message_delta snapshots
// for the V1 bridge. The canonical stream still emits each wire-shaped event;
// the sideband is flushed once at terminal so a delta cannot conflict with or
// replace the input/cache fields carried by message_start.
func mergeAnthropicUsageEvent(previous, next aipapi.Event) aipapi.Event {
	out := previous
	out.Kind = next.Kind
	copyCounter := func(present bool, value int, dst *int, dstPresent *bool) {
		if !present {
			return
		}
		*dst = value
		*dstPresent = true
	}
	copyCounter(next.UsagePresence.InputTokens, next.InputTokens, &out.InputTokens, &out.UsagePresence.InputTokens)
	copyCounter(next.UsagePresence.OutputTokens, next.OutputTokens, &out.OutputTokens, &out.UsagePresence.OutputTokens)
	copyCounter(next.UsagePresence.CacheReadTokens, next.CacheReadTokens, &out.CacheReadTokens, &out.UsagePresence.CacheReadTokens)
	copyCounter(next.UsagePresence.CacheWriteTokens, next.CacheWriteTokens, &out.CacheWriteTokens, &out.UsagePresence.CacheWriteTokens)
	copyCounter(next.UsagePresence.ReasoningTokens, next.ReasoningTokens, &out.ReasoningTokens, &out.UsagePresence.ReasoningTokens)
	copyCounter(next.UsagePresence.TotalTokens, next.TotalTokens, &out.TotalTokens, &out.UsagePresence.TotalTokens)
	if strings.TrimSpace(next.Accounting.ProviderRequestID) != "" {
		out.Accounting.ProviderRequestID = strings.TrimSpace(next.Accounting.ProviderRequestID)
	}
	if strings.TrimSpace(next.Accounting.ProviderChargeID) != "" {
		out.Accounting.ProviderChargeID = strings.TrimSpace(next.Accounting.ProviderChargeID)
	}
	if strings.TrimSpace(next.Accounting.ProviderAccountKey) != "" {
		out.Accounting.ProviderAccountKey = strings.TrimSpace(next.Accounting.ProviderAccountKey)
	}
	if strings.TrimSpace(next.Accounting.ServiceContext) != "" {
		out.Accounting.ServiceContext = strings.TrimSpace(next.Accounting.ServiceContext)
	}
	if strings.TrimSpace(next.Accounting.DedupeKey) != "" {
		out.Accounting.DedupeKey = strings.TrimSpace(next.Accounting.DedupeKey)
	}
	if strings.TrimSpace(next.RawUsageJSON) != "" {
		out.RawUsageJSON = next.RawUsageJSON
	}
	// Exclusion is sticky across partial snapshots: every contributing
	// snapshot declares the Anthropic Messages input semantics, so the
	// merged cumulative snapshot keeps the declaration regardless of merge
	// order. The two relations are independent: neither is derived from the
	// other.
	out.Accounting.InputTokensExcludeCacheRead = out.Accounting.InputTokensExcludeCacheRead || next.Accounting.InputTokensExcludeCacheRead
	out.Accounting.InputTokensExcludeCacheWrite = out.Accounting.InputTokensExcludeCacheWrite || next.Accounting.InputTokensExcludeCacheWrite
	return out
}

func marshalUsageFields(usage *usageFields) string {
	if usage == nil {
		return ""
	}
	raw, err := json.Marshal(usage)
	if err != nil {
		return ""
	}
	return string(raw)
}

func commandcodeNativeMeasures(raw string) []sdkmetering.Measure {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var usage usageFields
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		return nil
	}
	measures := make([]sdkmetering.Measure, 0, 4)
	appendCount := func(component, name string, value *int) {
		if value == nil || *value < 0 {
			return
		}
		decimal := sdkmetering.Decimal{Coefficient: strconv.Itoa(*value)}
		measures = append(measures, sdkmetering.Measure{
			Key:   sdkmetering.ComponentKey{Direction: sdkmetering.DirectionInput, Component: component, Unit: sdkmetering.UnitToken, SchemaID: "anthropic.usage.v2", Dimensions: []sdkmetering.Dimension{{Name: "cache_lifetime", Value: name}}},
			Value: &decimal, Quality: sdkmetering.QualityObserved, MethodRef: "anthropic.usage.cache_lifetime.v1",
		})
	}
	if usage.CacheCreation != nil {
		appendCount(sdkmetering.ComponentCacheWriteInputToken, "5m", usage.CacheCreation.Ephemeral5mInputTokens)
		appendCount(sdkmetering.ComponentCacheWriteInputToken, "1h", usage.CacheCreation.Ephemeral1hInputTokens)
	}
	appendTool := func(name string, value *int) {
		if value == nil || *value < 0 {
			return
		}
		decimal := sdkmetering.Decimal{Coefficient: strconv.Itoa(*value)}
		measures = append(measures, sdkmetering.Measure{
			Key:   sdkmetering.ComponentKey{Direction: sdkmetering.DirectionNone, Component: sdkmetering.ComponentToolQuery, Unit: sdkmetering.UnitCount, SchemaID: "anthropic.usage.v2", Dimensions: []sdkmetering.Dimension{{Name: "tool", Value: name}}},
			Value: &decimal, Quality: sdkmetering.QualityObserved, MethodRef: "anthropic.usage.server_tool.v1",
		})
	}
	if usage.ServerToolUse != nil {
		appendTool("web_fetch", usage.ServerToolUse.WebFetchRequests)
		appendTool("web_search", usage.ServerToolUse.WebSearchRequests)
	}
	return measures
}

type providerSliceStream struct {
	mu     sync.Mutex
	events []aipapi.Event
	index  int
	closed bool
	*backendplugin.UsageEvidenceBuffer
}

func newProviderSliceStream(events []aipapi.Event) *providerSliceStream {
	s := &providerSliceStream{events: append([]aipapi.Event(nil), events...), UsageEvidenceBuffer: backendplugin.NewUsageEvidenceBuffer()}
	var cumulative aipapi.Event
	seenUsage := false
	for _, event := range events {
		if event.Kind == aipapi.EventUsageDelta {
			if seenUsage {
				cumulative = mergeAnthropicUsageEvent(cumulative, event)
			} else {
				cumulative = event
				seenUsage = true
			}
		}
	}
	if seenUsage {
		s.AddUsageEvent(cumulative, "commandcode.anthropic.usage:stream")
	}
	return s
}

func (s *providerSliceStream) Recv(context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.index >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	if event.Kind == aipapi.EventUsageDelta && s.AccountingEvidenceEnabled() {
		event.Accounting.DedupeKey = ""
	}
	return event, nil
}

func (s *providerSliceStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *providerSliceStream) Cancel(_ context.Context, _ aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly, Err: s.Close()}
}
