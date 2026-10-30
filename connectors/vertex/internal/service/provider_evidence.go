package service

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

type vertexEvidenceDraftResult struct {
	Measures []sdkmetering.Measure
	Evidence []sdkmetering.SafeEvidenceField
}

func vertexEvidenceDraft(ev aipapi.Event, usage *VertexUsageMetadata, _ string) vertexEvidenceDraftResult {
	draft := vertexEvidenceDraftResult{}
	if usage == nil && ev.RawUsageJSON != "" {
		var parsed VertexUsageMetadata
		if json.Unmarshal([]byte(ev.RawUsageJSON), &parsed) == nil {
			usage = &parsed
		}
	}
	draft.Measures = append(draft.Measures, vertexNativeMeasures(usage)...)
	if ev.Accounting.ServiceContext != "" {
		draft.Evidence = append(draft.Evidence, sdkmetering.SafeEvidenceField{
			Path: "$.provider_schema.service_context", Lexeme: ev.Accounting.ServiceContext,
			Present: true, Acquisition: sdkmetering.AcquisitionProviderResponse,
		})
	}
	return draft
}

func vertexNativeMeasures(u *VertexUsageMetadata) []sdkmetering.Measure {
	if u == nil {
		return nil
	}
	input := make(map[string]int64)
	output := make(map[string]int64)
	cache := make(map[string]int64)
	tool := make(map[string]int64)
	collect := func(dst map[string]int64, invalid map[string]bool, entries []VertexModalityTokenCount) {
		for _, entry := range entries {
			if entry.TokenCount < 0 {
				continue
			}
			component, ok := vertexModalityComponent(entry.Modality)
			if ok {
				if invalid[component] {
					continue
				}
				value := int64(entry.TokenCount)
				prior := dst[component]
				if prior > int64(^uint64(0)>>1)-value {
					// Do not retain a partial aggregate when the provider supplied
					// more modality counts than the exact contract can represent.
					delete(dst, component)
					invalid[component] = true
					continue
				}
				dst[component] = prior + value
			}
		}
	}
	collect(input, make(map[string]bool), u.PromptTokensDetails)
	collect(output, make(map[string]bool), u.CandidatesTokensDetails)
	collect(cache, make(map[string]bool), u.CacheTokensDetails)
	collect(tool, make(map[string]bool), u.ToolUsePromptTokensDetails)
	measures := make([]sdkmetering.Measure, 0, len(input)+len(output)+len(cache)+len(tool)+2)
	appendMap := func(values map[string]int64, direction sdkmetering.FlowDirection, plane string) {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, component := range keys {
			value := sdkmetering.Decimal{Coefficient: strconv.FormatInt(values[component], 10)}
			measures = append(measures, sdkmetering.Measure{
				Key:   sdkmetering.ComponentKey{Direction: direction, Component: component, Unit: sdkmetering.UnitToken, SchemaID: "vertex.usage.v2", Dimensions: []sdkmetering.Dimension{{Name: "modality", Value: component}, {Name: "evidence_plane", Value: plane}}},
				Value: &value, Quality: sdkmetering.QualityObserved, MethodRef: "vertex.usage.modality.v2",
			})
		}
	}
	appendMap(input, sdkmetering.DirectionInput, "prompt")
	appendMap(output, sdkmetering.DirectionOutput, "candidates")
	appendMap(cache, sdkmetering.DirectionInput, "cache")
	if toolPresent := u.groundedToolPresent || u.ToolUsePromptTokenCount != 0; toolPresent && u.ToolUsePromptTokenCount >= 0 {
		value := sdkmetering.Decimal{Coefficient: strconv.FormatInt(int64(u.ToolUsePromptTokenCount), 10)}
		measures = append(measures, sdkmetering.Measure{
			Key:   sdkmetering.ComponentKey{Direction: sdkmetering.DirectionInput, Component: "grounded_tool_token", Unit: sdkmetering.UnitToken, SchemaID: "vertex.usage.v2"},
			Value: &value, Quality: sdkmetering.QualityObserved, MethodRef: "vertex.usage.grounded_tool.v2",
		})
	}
	appendMap(tool, sdkmetering.DirectionInput, "grounded_tool")
	return measures
}

func vertexModalityComponent(modality string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(modality)) {
	case "TEXT", "MODALITY_TEXT":
		return sdkmetering.ComponentTextToken, true
	case "IMAGE", "MODALITY_IMAGE":
		return sdkmetering.ComponentImageToken, true
	case "AUDIO", "MODALITY_AUDIO":
		return sdkmetering.ComponentAudioToken, true
	case "VIDEO", "MODALITY_VIDEO":
		return sdkmetering.ComponentVideoToken, true
	case "DOCUMENT", "MODALITY_DOCUMENT":
		return sdkmetering.ComponentDocumentToken, true
	default:
		return "", false
	}
}

func vertexUsageRawJSON(usage *VertexUsageMetadata) string {
	if usage == nil {
		return ""
	}
	b, err := json.Marshal(usage)
	if err != nil {
		return ""
	}
	return string(b)
}
