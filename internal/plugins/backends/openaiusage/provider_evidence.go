package openaiusage

import (
	"encoding/json"
	"maps"
	"math"
	"strconv"
	"strings"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// ProviderEvidenceDraft maps OpenAI-family usage into the host-only V2 seam.
// The family parser is intentionally kept here: core sees only neutral
// measures and cannot infer provider fields or prices.
func ProviderEvidenceDraft(ev aipapi.Event, mapping, sourceKey string) coremetering.ProviderEvidenceDraft {
	draft := coremetering.ProviderUsageEvent(ev, mapping, sourceKey)
	draft.Measures = append(draft.Measures, NativeUsageMeasures(ev.RawUsageJSON)...)
	draft.Evidence = append(draft.Evidence, NativeUsageEvidence(ev.RawUsageJSON)...)
	if ev.CostPresent && ev.Accounting.Source == aipapi.UsageSourceProviderReported && ev.CostNanoUnits >= 0 && strings.TrimSpace(ev.Currency) != "" {
		amount, raw := providerCostAmount(ev)
		if amount != nil {
			draft.Charges = append(draft.Charges, sdkmetering.ReportedCharge{
				ChargeItemID: "cost:" + draft.SourceEventKey,
				Amount:       amount,
				Currency:     strings.TrimSpace(ev.Currency),
				Kind:         sdkmetering.ChargeKindAggregate,
			})
			// Keep the provider's bounded amount lexeme when the response
			// actually surfaced it. A nano-unit fallback is sufficient for the
			// charge amount but must not masquerade as the provider's raw value.
			if raw != "" {
				draft.Evidence = append(
					draft.Evidence,
					sdkmetering.SafeEvidenceField{Path: "$.cost.amount", Lexeme: raw, Present: true, Acquisition: sdkmetering.AcquisitionProviderResponse},
					sdkmetering.SafeEvidenceField{Path: "$.cost.currency", Lexeme: strings.TrimSpace(ev.Currency), Present: true, Acquisition: sdkmetering.AcquisitionProviderResponse},
				)
			}
		}
	}
	return draft
}

func providerCostAmount(ev aipapi.Event) (*sdkmetering.Decimal, string) {
	raw := strings.TrimSpace(providerCostRawFromUsageJSON(ev.RawUsageJSON))
	if raw != "" {
		amount, err := sdkmetering.ParseDecimal(raw)
		if err != nil || strings.HasPrefix(amount.Coefficient, "-") {
			return nil, ""
		}
		return &amount, raw
	}
	amount := sdkmetering.DecimalFromNanoUnits(ev.CostNanoUnits)
	return &amount, ""
}

// AnnotateProviderContext attaches identifiers that were actually surfaced by
// the provider response. Empty values remain absent; no request identifier is
// synthesized from a local call ID.
func AnnotateProviderContext(ev *aipapi.Event, providerRequestID, serviceContext string) {
	if ev == nil {
		return
	}
	ev.Accounting.ProviderRequestID = strings.TrimSpace(providerRequestID)
	ev.Accounting.ServiceContext = strings.TrimSpace(serviceContext)
}

type nativeUsageSpec struct {
	keys             []string
	nested           string
	component        string
	unit             string
	direction        sdkmetering.FlowDirection
	path             string
	integer          bool
	preserveWirePath bool
}

// nativeUsageSpecs contains fields used by OpenAI and compatible Responses /
// Chat endpoints. Aliases are accepted because compatible providers commonly
// expose the same quantity under prompt/input or completion/output names.
// No estimate or text-token conversion is performed.
var nativeUsageSpecs = []nativeUsageSpec{
	// The nested token-detail fields are the provider-family representations.
	// Keep them ahead of compatible flat aliases so a response carrying both
	// forms cannot silently select a conflicting synthetic alias.
	{nested: "prompt_tokens_details", keys: []string{"text_tokens"}, component: sdkmetering.ComponentTextToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_text_tokens", integer: true, preserveWirePath: true},
	{nested: "completion_tokens_details", keys: []string{"text_tokens"}, component: sdkmetering.ComponentTextToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_text_tokens", integer: true, preserveWirePath: true},
	{nested: "prompt_tokens_details", keys: []string{"audio_tokens"}, component: sdkmetering.ComponentAudioToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_audio_tokens", integer: true, preserveWirePath: true},
	{nested: "completion_tokens_details", keys: []string{"audio_tokens"}, component: sdkmetering.ComponentAudioToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_audio_tokens", integer: true, preserveWirePath: true},
	// The Chat CompletionUsage prompt_tokens_details also carries image input
	// tokens (prompt_tokens_details.image_tokens). They are token quantities,
	// not image counts, so they map onto the directional image_token component;
	// they must never be mapped onto the separate image/image-count component.
	// The documented completion_tokens_details has no image_tokens member, so no
	// output image token is fabricated. Keeping this nested field ahead of the
	// flat compatible aliases lets the provider-family value win a conflict,
	// and preserveWirePath retains the evidence at the exact provider location
	// $.usage.prompt_tokens_details.image_tokens (allowlisted by the SDK).
	{nested: "prompt_tokens_details", keys: []string{"image_tokens"}, component: sdkmetering.ComponentImageToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.prompt_tokens_details.image_tokens", integer: true, preserveWirePath: true},
	// The official Chat CompletionUsagePromptTokensDetails carries the standard
	// cache_write_tokens member ("The unadjusted number of prompt tokens written
	// to cache"); the Responses ResponseUsageInputTokensDetails carries the same
	// member. It is an input subset like cached_tokens, so it maps onto the
	// directional input cache_write_input_token component under the native detail
	// schema and retains its exact provider wire path. Both provider-family
	// spellings are declared; only one is ever present on a wire response.
	{nested: "prompt_tokens_details", keys: []string{"cache_write_tokens"}, component: sdkmetering.ComponentCacheWriteInputToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.prompt_tokens_details.cache_write_tokens", integer: true, preserveWirePath: true},
	{nested: "input_tokens_details", keys: []string{"text_tokens"}, component: sdkmetering.ComponentTextToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_text_tokens", integer: true, preserveWirePath: true},
	{nested: "output_tokens_details", keys: []string{"text_tokens"}, component: sdkmetering.ComponentTextToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_text_tokens", integer: true, preserveWirePath: true},
	{nested: "input_tokens_details", keys: []string{"audio_tokens"}, component: sdkmetering.ComponentAudioToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_audio_tokens", integer: true, preserveWirePath: true},
	{nested: "output_tokens_details", keys: []string{"audio_tokens"}, component: sdkmetering.ComponentAudioToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_audio_tokens", integer: true, preserveWirePath: true},
	{nested: "input_tokens_details", keys: []string{"images"}, component: sdkmetering.ComponentImage, unit: sdkmetering.UnitImage, direction: sdkmetering.DirectionInput, path: "$.usage.input_image_count", integer: true, preserveWirePath: true},
	{nested: "output_tokens_details", keys: []string{"images"}, component: sdkmetering.ComponentImage, unit: sdkmetering.UnitImage, direction: sdkmetering.DirectionOutput, path: "$.usage.output_image_count", integer: true, preserveWirePath: true},
	{nested: "input_tokens_details", keys: []string{"cache_write_tokens"}, component: sdkmetering.ComponentCacheWriteInputToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_tokens_details.cache_write_tokens", integer: true, preserveWirePath: true},
	{keys: []string{"input_image_tokens", "image_input_tokens", "prompt_image_tokens"}, component: sdkmetering.ComponentImageToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_image_tokens", integer: true},
	{keys: []string{"output_image_tokens", "image_output_tokens", "completion_image_tokens"}, component: sdkmetering.ComponentImageToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_image_tokens", integer: true},
	{keys: []string{"input_audio_tokens", "audio_input_tokens", "prompt_audio_tokens"}, component: sdkmetering.ComponentAudioToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_audio_tokens", integer: true, preserveWirePath: true},
	{keys: []string{"output_audio_tokens", "audio_output_tokens", "completion_audio_tokens"}, component: sdkmetering.ComponentAudioToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_audio_tokens", integer: true, preserveWirePath: true},
	{keys: []string{"input_video_tokens", "video_input_tokens", "prompt_video_tokens"}, component: sdkmetering.ComponentVideoToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_video_tokens", integer: true},
	{keys: []string{"output_video_tokens", "video_output_tokens", "completion_video_tokens"}, component: sdkmetering.ComponentVideoToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_video_tokens", integer: true},
	{keys: []string{"input_document_tokens", "document_input_tokens", "prompt_document_tokens"}, component: sdkmetering.ComponentDocumentToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionInput, path: "$.usage.input_document_tokens", integer: true},
	{keys: []string{"output_document_tokens", "document_output_tokens", "completion_document_tokens"}, component: sdkmetering.ComponentDocumentToken, unit: sdkmetering.UnitToken, direction: sdkmetering.DirectionOutput, path: "$.usage.output_document_tokens", integer: true},
	{keys: []string{"input_image_count", "image_input_count", "prompt_image_count"}, component: sdkmetering.ComponentImage, unit: sdkmetering.UnitImage, direction: sdkmetering.DirectionInput, path: "$.usage.input_image_count", integer: true},
	{keys: []string{"output_image_count", "image_output_count", "completion_image_count"}, component: sdkmetering.ComponentImage, unit: sdkmetering.UnitImage, direction: sdkmetering.DirectionOutput, path: "$.usage.output_image_count", integer: true},
	{keys: []string{"input_audio_seconds", "audio_input_seconds", "prompt_audio_seconds"}, component: sdkmetering.ComponentAudio, unit: sdkmetering.UnitSecond, direction: sdkmetering.DirectionInput, path: "$.usage.input_audio_seconds", integer: false},
	{keys: []string{"output_audio_seconds", "audio_output_seconds", "completion_audio_seconds"}, component: sdkmetering.ComponentAudio, unit: sdkmetering.UnitSecond, direction: sdkmetering.DirectionOutput, path: "$.usage.output_audio_seconds", integer: false},
	{keys: []string{"input_video_seconds", "video_input_seconds", "prompt_video_seconds"}, component: sdkmetering.ComponentVideo, unit: sdkmetering.UnitSecond, direction: sdkmetering.DirectionInput, path: "$.usage.input_video_seconds", integer: false},
	{keys: []string{"output_video_seconds", "video_output_seconds", "completion_video_seconds"}, component: sdkmetering.ComponentVideo, unit: sdkmetering.UnitSecond, direction: sdkmetering.DirectionOutput, path: "$.usage.output_video_seconds", integer: false},
	{keys: []string{"input_video_frames", "video_input_frames", "prompt_video_frames"}, component: sdkmetering.ComponentVideo, unit: sdkmetering.UnitFrame, direction: sdkmetering.DirectionInput, path: "$.usage.input_video_frames", integer: true},
	{keys: []string{"output_video_frames", "video_output_frames", "completion_video_frames"}, component: sdkmetering.ComponentVideo, unit: sdkmetering.UnitFrame, direction: sdkmetering.DirectionOutput, path: "$.usage.output_video_frames", integer: true},
	{keys: []string{"input_document_pages", "document_input_pages", "prompt_document_pages"}, component: sdkmetering.ComponentDocument, unit: sdkmetering.UnitPage, direction: sdkmetering.DirectionInput, path: "$.usage.input_document_pages", integer: true},
	{keys: []string{"output_document_pages", "document_output_pages", "completion_document_pages"}, component: sdkmetering.ComponentDocument, unit: sdkmetering.UnitPage, direction: sdkmetering.DirectionOutput, path: "$.usage.output_document_pages", integer: true},
	{keys: []string{"input_file_pages", "file_input_pages", "prompt_file_pages"}, component: sdkmetering.ComponentFile, unit: sdkmetering.UnitPage, direction: sdkmetering.DirectionInput, path: "$.usage.input_file_pages", integer: true},
	{keys: []string{"output_file_pages", "file_output_pages", "completion_file_pages"}, component: sdkmetering.ComponentFile, unit: sdkmetering.UnitPage, direction: sdkmetering.DirectionOutput, path: "$.usage.output_file_pages", integer: true},
	{keys: []string{"input_bytes", "request_bytes", "prompt_bytes"}, component: sdkmetering.ComponentFile, unit: sdkmetering.UnitByte, direction: sdkmetering.DirectionInput, path: "$.usage.input_bytes", integer: true},
	{keys: []string{"output_bytes", "response_bytes", "completion_bytes"}, component: sdkmetering.ComponentFile, unit: sdkmetering.UnitByte, direction: sdkmetering.DirectionOutput, path: "$.usage.output_bytes", integer: true},
}

// nativeUsageMethodRef and nativeUsageMalformedReason are bounded adapter
// identities carried by every native measure. The malformed reason is the
// typed unavailable status for a present provider-family field whose lexeme
// cannot be a valid quantity.
const (
	nativeUsageMethodRef       = "openai.usage.native.v2"
	nativeUsageMalformedReason = "malformed_native_usage"
)

// NativeUsageMeasures returns only provider-reported native units. A malformed,
// negative, fractional discrete or overflow flat compatible alias is omitted,
// preserving an unavailable quantity rather than coercing it to zero or text
// tokens. A present provider-family nested field is authoritative: when its
// lexeme is malformed it becomes an explicit unavailable measure instead of
// being silently dropped, and it claims its canonical component so a later
// compatible flat alias cannot stand in for the unusable provider value.
func NativeUsageMeasures(raw string) []sdkmetering.Measure {
	fields, details := usageFields(raw)
	if len(fields) == 0 {
		return nil
	}
	measures := make([]sdkmetering.Measure, 0, len(nativeUsageSpecs))
	for _, spec := range nativeUsageSpecs {
		value, _, presence := nativeSpecPresence(fields, details, spec)
		if presence == nativeFieldAbsent {
			continue
		}
		measure := nativeMeasureForKey(spec, value)
		if measure.Quality == sdkmetering.QualityUnavailable && spec.nested == "" {
			continue
		}
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

// nativeCountMaxDigits mirrors the SDK safe-evidence count bound: a
// non-negative integer count is limited to 19 canonical base-10 digits so it
// cannot exceed an unsigned 64-bit quantity. The adapter applies the same bound
// before it emits an observed count or retains a raw count lexeme, so a native
// measure and its evidence can never disagree with Observation.Validate.
const nativeCountMaxDigits = 19

// nativeCanonicalCount reports whether a present provider-family integer lexeme
// is exactly the canonical non-negative base-10 unsigned integer spelling the
// SDK safe-evidence count contract enforces. Signs, fractions, exponents,
// leading zeroes, grouping separators, underscores and overflow are rejected.
func nativeCanonicalCount(value string) bool {
	if value == "" || len(value) > nativeCountMaxDigits {
		return false
	}
	if len(value) > 1 && value[0] == '0' {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	if _, err := strconv.ParseUint(value, 10, 64); err != nil {
		return false
	}
	return true
}

// nativeObservedDecimal accepts one present provider lexeme for a spec and
// returns its canonical observed Decimal. A declared discrete integer count must
// be a canonical unsigned count before it may be observed; a permissively parsed
// decimal or scientific lexeme is unavailable rather than a canonicalized
// observed count whose original raw lexeme would fail Observation.Validate and
// discard the whole observation. A fractional/duration spec keeps the exact
// non-negative decimal grammar.
func nativeObservedDecimal(spec nativeUsageSpec, lexeme string) (sdkmetering.Decimal, bool) {
	if spec.integer {
		if !nativeCanonicalCount(lexeme) {
			return sdkmetering.Decimal{}, false
		}
		return sdkmetering.Decimal{Coefficient: lexeme, Scale: 0}, true
	}
	decimal, err := sdkmetering.ParseDecimal(lexeme)
	if err != nil || strings.HasPrefix(lexeme, "-") || strings.HasPrefix(decimal.Coefficient, "-") {
		return sdkmetering.Decimal{}, false
	}
	return decimal, true
}

// nativeMeasureForKey builds one native measure for a present provider value.
// The declared discrete integer grammar is enforced exactly as the evidence
// contract does; an unusable lexeme yields an explicit unavailable measure.
func nativeMeasureForKey(spec nativeUsageSpec, value json.RawMessage) sdkmetering.Measure {
	key := sdkmetering.ComponentKey{Direction: spec.direction, Component: spec.component, Unit: spec.unit, SchemaID: NativeUsageSchemaID}
	lexeme := strings.TrimSpace(string(value))
	decimal, ok := nativeObservedDecimal(spec, lexeme)
	if !ok {
		return sdkmetering.Measure{Key: key, Quality: sdkmetering.QualityUnavailable, MethodRef: nativeUsageMethodRef, Reason: nativeUsageMalformedReason}
	}
	return sdkmetering.Measure{Key: key, Value: &decimal, Quality: sdkmetering.QualityObserved, MethodRef: nativeUsageMethodRef}
}

// NativeUsageEvidence returns bounded safe lexemes for the same native fields
// retained as measures. Provider-family nested fields and selected aliases
// retain their exact allowlisted wire path; no synthetic decomposition path is
// substituted for the provider's field identity. A present-but-malformed
// provider-family nested field has no safe numeric lexeme to retain, but it
// still claims its canonical component so a later flat alias cannot silently
// stand in for it; its typed unavailable status is carried by the measure.
func NativeUsageEvidence(raw string) []sdkmetering.SafeEvidenceField {
	fields, details := usageFields(raw)
	if len(fields) == 0 {
		return nil
	}
	evidence := make([]sdkmetering.SafeEvidenceField, 0, len(nativeUsageSpecs))
	seen := make(map[string]struct{}, len(nativeUsageSpecs))
	for _, spec := range nativeUsageSpecs {
		value, providerKey, presence := nativeSpecPresence(fields, details, spec)
		if presence == nativeFieldAbsent {
			continue
		}
		componentKey := sdkmetering.ComponentKey{
			Direction: spec.direction, Component: spec.component, Unit: spec.unit, SchemaID: NativeUsageSchemaID,
		}.CanonicalKey()
		if _, ok := seen[componentKey]; ok {
			// The first provider-family field in nativeUsageSpecs wins. This
			// prevents a conflicting alias or alternate endpoint detail from
			// adding a second evidence lexeme for one canonical measure.
			continue
		}
		lexeme := strings.TrimSpace(string(value))
		if _, ok := nativeObservedDecimal(spec, lexeme); !ok {
			if spec.nested != "" {
				// A malformed provider-family nested field still claims its
				// canonical component so its own typed unavailable measure is
				// never supplemented by a flat alias lexeme.
				seen[componentKey] = struct{}{}
			}
			continue
		}
		seen[componentKey] = struct{}{}
		path := spec.path
		if spec.preserveWirePath {
			path = nativeEvidencePath(spec, providerKey)
		}
		evidence = append(evidence, sdkmetering.SafeEvidenceField{Path: path, Lexeme: lexeme, Present: true, Acquisition: sdkmetering.AcquisitionProviderResponse})
	}
	return evidence
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

func usageFields(raw string) (map[string]json.RawMessage, map[string]map[string]json.RawMessage) {
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

// nativeFieldPresence classifies whether a spec's provider key is actually
// present on the wire. An absent member and a JSON null member both carry no
// provider quantity and are indistinguishable here; a present member is
// authoritative for the spec's canonical component even when its lexeme is
// malformed.
type nativeFieldPresence uint8

const (
	nativeFieldAbsent nativeFieldPresence = iota
	nativeFieldPresent
)

// nativeSpecPresence returns the first present provider key for a spec. Flat
// compatible aliases read the merged top-level fields; nested provider-family
// fields read only their own detail object so an absent detail member never
// falls through to an unrelated top-level spelling.
func nativeSpecPresence(fields map[string]json.RawMessage, details map[string]map[string]json.RawMessage, spec nativeUsageSpec) (json.RawMessage, string, nativeFieldPresence) {
	lookup := fields
	if spec.nested != "" {
		lookup = details[spec.nested]
	}
	for _, key := range spec.keys {
		value, ok := lookup[key]
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(string(value))
		if trimmed == "" || trimmed == "null" {
			continue
		}
		return value, key, nativeFieldPresent
	}
	return nil, "", nativeFieldAbsent
}

func nativeEvidencePath(spec nativeUsageSpec, providerKey string) string {
	if spec.nested != "" {
		return "$.usage." + spec.nested + "." + providerKey
	}
	return "$.usage." + providerKey
}
