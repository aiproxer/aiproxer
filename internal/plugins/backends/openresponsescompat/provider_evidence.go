package openresponsescompat

import (
	"encoding/json"
	"maps"
	"math"
	"strings"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// providerEvidenceDraft maps the generic OpenResponses usage object without
// importing any provider SDK. Provider-specific connectors can use the same
// neutral wire shape while keeping their registration and SDK dependencies
// outside this generic backend package.
func providerEvidenceDraft(ev aipapi.Event, mapping, sourceKey string) coremetering.ProviderEvidenceDraft {
	draft := coremetering.ProviderUsageEvent(ev, mapping, sourceKey)
	draft.Measures = append(draft.Measures, nativeUsageMeasures(ev.RawUsageJSON)...)
	draft.Evidence = append(draft.Evidence, nativeUsageEvidence(ev.RawUsageJSON)...)
	return draft
}

func annotateProviderContext(ev *aipapi.Event, providerRequestID string) {
	if ev == nil {
		return
	}
	ev.Accounting.ProviderRequestID = strings.TrimSpace(providerRequestID)
}

type nativeUsageField struct {
	nested    string
	keys      []string
	component string
	unit      string
	direction sdkmetering.FlowDirection
	path      string
	integer   bool
}

// These are only fields surfaced by OpenResponses-compatible usage objects.
// Direction and native units remain explicit; no text-token or aggregate
// inference is performed for a malformed or ambiguous field.
var nativeUsageFields = []nativeUsageField{
	{keys: []string{"input_image_tokens", "image_input_tokens", "prompt_image_tokens"}, component: sdkmetering.ComponentImageToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_image_tokens", integer: true},
	{keys: []string{"output_image_tokens", "image_output_tokens", "completion_image_tokens"}, component: sdkmetering.ComponentImageToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_image_tokens", integer: true},
	{keys: []string{"input_audio_tokens", "audio_input_tokens", "prompt_audio_tokens"}, component: sdkmetering.ComponentAudioToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_audio_tokens", integer: true},
	{keys: []string{"output_audio_tokens", "audio_output_tokens", "completion_audio_tokens"}, component: sdkmetering.ComponentAudioToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_audio_tokens", integer: true},
	{keys: []string{"input_video_tokens", "video_input_tokens", "prompt_video_tokens"}, component: sdkmetering.ComponentVideoToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_video_tokens", integer: true},
	{keys: []string{"output_video_tokens", "video_output_tokens", "completion_video_tokens"}, component: sdkmetering.ComponentVideoToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_video_tokens", integer: true},
	{keys: []string{"input_document_tokens", "document_input_tokens", "prompt_document_tokens"}, component: sdkmetering.ComponentDocumentToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_document_tokens", integer: true},
	{keys: []string{"output_document_tokens", "document_output_tokens", "completion_document_tokens"}, component: sdkmetering.ComponentDocumentToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_document_tokens", integer: true},
	{keys: []string{"input_image_count", "image_input_count", "prompt_image_count"}, component: sdkmetering.ComponentImage, unit: sdkmetering.UnitImage, direction: sdkmetering.DirectionInput, path: "$.usage.input_image_count", integer: true},
	{keys: []string{"output_image_count", "image_output_count", "completion_image_count"}, component: sdkmetering.ComponentImage, unit: sdkmetering.UnitImage, direction: sdkmetering.DirectionOutput, path: "$.usage.output_image_count", integer: true},
	{keys: []string{"input_audio_seconds", "audio_input_seconds", "prompt_audio_seconds"}, component: sdkmetering.ComponentAudio, unit: sdkmetering.UnitSecond, direction: sdkmetering.DirectionInput, path: "$.usage.input_audio_seconds"},
	{keys: []string{"output_audio_seconds", "audio_output_seconds", "completion_audio_seconds"}, component: sdkmetering.ComponentAudio, unit: sdkmetering.UnitSecond, direction: sdkmetering.DirectionOutput, path: "$.usage.output_audio_seconds"},
	{keys: []string{"input_video_seconds", "video_input_seconds", "prompt_video_seconds"}, component: sdkmetering.ComponentVideo, unit: sdkmetering.UnitSecond, direction: sdkmetering.DirectionInput, path: "$.usage.input_video_seconds"},
	{keys: []string{"output_video_seconds", "video_output_seconds", "completion_video_seconds"}, component: sdkmetering.ComponentVideo, unit: sdkmetering.UnitSecond, direction: sdkmetering.DirectionOutput, path: "$.usage.output_video_seconds"},
	{keys: []string{"input_video_frames", "video_input_frames", "prompt_video_frames"}, component: sdkmetering.ComponentVideo, unit: sdkmetering.UnitFrame, direction: sdkmetering.DirectionInput, path: "$.usage.input_video_frames", integer: true},
	{keys: []string{"output_video_frames", "video_output_frames", "completion_video_frames"}, component: sdkmetering.ComponentVideo, unit: sdkmetering.UnitFrame, direction: sdkmetering.DirectionOutput, path: "$.usage.output_video_frames", integer: true},
	{keys: []string{"input_document_pages", "document_input_pages", "prompt_document_pages"}, component: sdkmetering.ComponentDocument, unit: sdkmetering.UnitPage, direction: sdkmetering.DirectionInput, path: "$.usage.input_document_pages", integer: true},
	{keys: []string{"output_document_pages", "document_output_pages", "completion_document_pages"}, component: sdkmetering.ComponentDocument, unit: sdkmetering.UnitPage, direction: sdkmetering.DirectionOutput, path: "$.usage.output_document_pages", integer: true},
	{keys: []string{"input_file_pages", "file_input_pages", "prompt_file_pages"}, component: sdkmetering.ComponentFile, unit: sdkmetering.UnitPage, direction: sdkmetering.DirectionInput, path: "$.usage.input_file_pages", integer: true},
	{keys: []string{"output_file_pages", "file_output_pages", "completion_file_pages"}, component: sdkmetering.ComponentFile, unit: sdkmetering.UnitPage, direction: sdkmetering.DirectionOutput, path: "$.usage.output_file_pages", integer: true},
	{keys: []string{"input_bytes", "request_bytes", "prompt_bytes"}, component: sdkmetering.ComponentFile, unit: sdkmetering.UnitByte, direction: sdkmetering.DirectionInput, path: "$.usage.input_bytes", integer: true},
	{keys: []string{"output_bytes", "response_bytes", "completion_bytes"}, component: sdkmetering.ComponentFile, unit: sdkmetering.UnitByte, direction: sdkmetering.DirectionOutput, path: "$.usage.output_bytes", integer: true},
	{nested: "input_tokens_details", keys: []string{"text_tokens"}, component: sdkmetering.ComponentTextToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_text_tokens", integer: true},
	{nested: "output_tokens_details", keys: []string{"text_tokens"}, component: sdkmetering.ComponentTextToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_text_tokens", integer: true},
	{nested: "input_tokens_details", keys: []string{"audio_tokens"}, component: sdkmetering.ComponentAudioToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_audio_tokens", integer: true},
	{nested: "output_tokens_details", keys: []string{"audio_tokens"}, component: sdkmetering.ComponentAudioToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_audio_tokens", integer: true},
	{nested: "input_tokens_details", keys: []string{"images"}, component: sdkmetering.ComponentImage, unit: sdkmetering.UnitImage, direction: sdkmetering.DirectionInput, path: "$.usage.input_image_count", integer: true},
	{nested: "output_tokens_details", keys: []string{"images"}, component: sdkmetering.ComponentImage, unit: sdkmetering.UnitImage, direction: sdkmetering.DirectionOutput, path: "$.usage.output_image_count", integer: true},
}

func nativeUsageMeasures(raw string) []sdkmetering.Measure {
	fields, details := decodeUsageFields(raw)
	if len(fields) == 0 {
		return nil
	}
	measures := make([]sdkmetering.Measure, 0, len(nativeUsageFields))
	for _, spec := range nativeUsageFields {
		value, ok := usageField(fields, details, spec)
		if !ok {
			continue
		}
		lexeme := strings.TrimSpace(string(value))
		decimal, err := sdkmetering.ParseDecimal(lexeme)
		if err != nil || strings.HasPrefix(lexeme, "-") || strings.HasPrefix(decimal.Coefficient, "-") || (spec.integer && decimal.Scale != 0) {
			continue
		}
		measure := sdkmetering.Measure{Key: sdkmetering.ComponentKey{Direction: spec.direction, Component: spec.component, Unit: spec.unit, SchemaID: "openresponses.usage.v2"}, Value: &decimal, Quality: sdkmetering.QualityObserved, MethodRef: "openresponses.usage.native.v2"}
		duplicate := false
		for _, prior := range measures {
			if prior.Key.CanonicalKey() == measure.Key.CanonicalKey() {
				duplicate = true
				break
			}
		}
		if !duplicate {
			measures = append(measures, measure)
		}
	}
	return measures
}

func nativeUsageEvidence(raw string) []sdkmetering.SafeEvidenceField {
	fields, details := decodeUsageFields(raw)
	if len(fields) == 0 {
		return nil
	}
	out := make([]sdkmetering.SafeEvidenceField, 0, len(nativeUsageFields))
	seen := make(map[string]struct{}, len(nativeUsageFields))
	for _, spec := range nativeUsageFields {
		value, providerKey, ok := usageFieldWithKey(fields, details, spec)
		if !ok {
			continue
		}
		lexeme := strings.TrimSpace(string(value))
		decimal, err := sdkmetering.ParseDecimal(lexeme)
		if err != nil || strings.HasPrefix(lexeme, "-") || strings.HasPrefix(decimal.Coefficient, "-") || (spec.integer && decimal.Scale != 0) {
			continue
		}
		path := spec.path
		if providerKey != "" {
			path = "$.usage."
			if spec.nested != "" {
				path += spec.nested + "."
			}
			path += providerKey
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, sdkmetering.SafeEvidenceField{Path: path, Lexeme: lexeme, Present: true, Acquisition: sdkmetering.AcquisitionProviderResponse})
	}
	return out
}

// mapCapacityHint returns base+extra for a make(map) size hint without letting
// the int size computation overflow. A wrapped hint would be negative and make
// panic; the base length is always a safe lower bound.
func mapCapacityHint(base, extra int) int {
	if extra <= 0 || base > math.MaxInt-extra {
		return base
	}
	return base + extra
}

func decodeUsageFields(raw string) (map[string]json.RawMessage, map[string]map[string]json.RawMessage) {
	var root map[string]json.RawMessage
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &root) != nil {
		return nil, nil
	}
	fields := make(map[string]json.RawMessage, mapCapacityHint(len(root), 8))
	details := make(map[string]map[string]json.RawMessage, 4)
	maps.Copy(fields, root)
	for _, nested := range []string{"prompt_tokens_details", "input_tokens_details", "completion_tokens_details", "output_tokens_details"} {
		var child map[string]json.RawMessage
		if value, ok := root[nested]; ok && json.Unmarshal(value, &child) == nil {
			details[nested] = child
			for key, value := range child {
				if _, exists := fields[key]; !exists {
					fields[key] = value
				}
			}
		}
	}
	return fields, details
}

func usageField(fields map[string]json.RawMessage, details map[string]map[string]json.RawMessage, spec nativeUsageField) (json.RawMessage, bool) {
	value, _, ok := usageFieldWithKey(fields, details, spec)
	return value, ok
}

func usageFieldWithKey(fields map[string]json.RawMessage, details map[string]map[string]json.RawMessage, spec nativeUsageField) (json.RawMessage, string, bool) {
	if spec.nested != "" {
		return firstUsageFieldWithKey(details[spec.nested], spec.keys)
	}
	return firstUsageFieldWithKey(fields, spec.keys)
}

func firstUsageFieldWithKey(fields map[string]json.RawMessage, keys []string) (json.RawMessage, string, bool) {
	for _, key := range keys {
		value, ok := fields[key]
		if !ok || strings.TrimSpace(string(value)) == "" || strings.TrimSpace(string(value)) == "null" {
			continue
		}
		return value, key, true
	}
	return nil, "", false
}
