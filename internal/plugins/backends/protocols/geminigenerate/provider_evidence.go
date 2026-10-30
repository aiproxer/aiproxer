package geminigenerate

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"google.golang.org/genai"
)

func geminiEvidenceDraft(ev aipapi.Event, usage *genai.GenerateContentResponseUsageMetadata) coremetering.ProviderEvidenceDraft {
	draft := coremetering.ProviderUsageEvent(ev, "gemini.generate.v2", "gemini.generate.usage:stream")
	draft.Measures = append(draft.Measures, geminiNativeMeasures(usage)...)
	draft.Evidence = append(draft.Evidence, geminiNativeEvidence(usage)...)
	return draft
}

type geminiModality struct {
	component string
	value     int64
}

func geminiNativeMeasures(u *genai.GenerateContentResponseUsageMetadata) []sdkmetering.Measure {
	if u == nil {
		return nil
	}
	// Aggregate repeated modality entries before constructing measures. This
	// avoids duplicate ComponentKeys while retaining every provider count.
	input := make(map[string]int64)
	output := make(map[string]int64)
	cache := make(map[string]int64)
	tool := make(map[string]int64)
	collect := func(dst map[string]int64, entries []*genai.ModalityTokenCount) {
		for _, entry := range entries {
			if entry == nil || entry.TokenCount < 0 {
				continue
			}
			component, ok := geminiModalityComponent(entry.Modality)
			if !ok {
				continue
			}
			dst[component] += int64(entry.TokenCount)
		}
	}
	collect(input, u.PromptTokensDetails)
	collect(output, u.CandidatesTokensDetails)
	collect(cache, u.CacheTokensDetails)
	collect(tool, u.ToolUsePromptTokensDetails)
	measures := make([]sdkmetering.Measure, 0, len(input)+len(output)+len(cache)+len(tool)+2)
	appendMap := func(values map[string]int64, direction sdkmetering.FlowDirection, dimension string) {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, component := range keys {
			value := sdkmetering.Decimal{Coefficient: strconv.FormatInt(values[component], 10)}
			dimensions := []sdkmetering.Dimension{{Name: "modality", Value: component}}
			if dimension != "" {
				dimensions = append(dimensions, sdkmetering.Dimension{Name: "evidence_plane", Value: dimension})
			}
			measures = append(measures, sdkmetering.Measure{
				Key:   sdkmetering.ComponentKey{Direction: direction, Component: component, Unit: sdkmetering.UnitToken, SchemaID: "gemini.usage.v2", Dimensions: dimensions},
				Value: &value, Quality: sdkmetering.QualityObserved, MethodRef: "gemini.usage.modality.v2",
			})
		}
	}
	appendMap(input, sdkmetering.DirectionInput, "prompt")
	appendMap(output, sdkmetering.DirectionOutput, "candidates")
	appendMap(cache, sdkmetering.DirectionInput, "cache")
	// The generated SDK type has no presence bit for scalar counters. Only a
	// non-zero value can be distinguished from an omitted field; the detail
	// list is emitted independently below and must not imply an aggregate.
	if u.ToolUsePromptTokenCount > 0 {
		value := sdkmetering.Decimal{Coefficient: strconv.FormatInt(int64(u.ToolUsePromptTokenCount), 10)}
		measures = append(measures, sdkmetering.Measure{
			Key:   sdkmetering.ComponentKey{Direction: sdkmetering.DirectionInput, Component: "grounded_tool_token", Unit: sdkmetering.UnitToken, SchemaID: "gemini.usage.v2"},
			Value: &value, Quality: sdkmetering.QualityObserved, MethodRef: "gemini.usage.grounded_tool.v2",
		})
	}
	appendMap(tool, sdkmetering.DirectionInput, "grounded_tool")
	return measures
}

func geminiNativeEvidence(u *genai.GenerateContentResponseUsageMetadata) []sdkmetering.SafeEvidenceField {
	if u == nil {
		return nil
	}
	raw, err := json.Marshal(u)
	if err != nil {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	result := make([]sdkmetering.SafeEvidenceField, 0, 8)
	appendField := func(name, path string) {
		value, ok := fields[name]
		if !ok || strings.TrimSpace(string(value)) == "" || strings.TrimSpace(string(value)) == "null" {
			return
		}
		result = append(result, sdkmetering.SafeEvidenceField{Path: path, Lexeme: strings.TrimSpace(string(value)), Present: true, Acquisition: sdkmetering.AcquisitionProviderResponse})
	}
	appendField("toolUsePromptTokenCount", "$.usage.tool_use_prompt_tokens")
	return result
}

func geminiModalityComponent(modality genai.MediaModality) (string, bool) {
	switch modality {
	case genai.MediaModalityText:
		return sdkmetering.ComponentTextToken, true
	case genai.MediaModalityImage:
		return sdkmetering.ComponentImageToken, true
	case genai.MediaModalityAudio:
		return sdkmetering.ComponentAudioToken, true
	case genai.MediaModalityVideo:
		return sdkmetering.ComponentVideoToken, true
	case genai.MediaModalityDocument:
		return sdkmetering.ComponentDocumentToken, true
	default:
		return "", false
	}
}
