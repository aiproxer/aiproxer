package openaiusage

import (
	"math"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestUsageFieldsCapacityHintDoesNotOverflow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		base  int
		extra int
		want  int
	}{
		{name: "normal", base: 3, extra: 8, want: 11},
		{name: "zero base", base: 0, extra: 8, want: 8},
		{name: "exact boundary", base: math.MaxInt - 8, extra: 8, want: math.MaxInt},
		{name: "just past boundary stays safe", base: math.MaxInt - 7, extra: 8, want: math.MaxInt - 7},
		{name: "maximum base stays safe", base: math.MaxInt, extra: 8, want: math.MaxInt},
		{name: "no extra", base: 5, extra: 0, want: 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mapCapacityHint(tc.base, tc.extra)
			if got != tc.want {
				t.Fatalf("mapCapacityHint(%d, %d) = %d, want %d", tc.base, tc.extra, got, tc.want)
			}
			if got < 0 {
				t.Fatalf("mapCapacityHint(%d, %d) overflowed to %d", tc.base, tc.extra, got)
			}
		})
	}
}

func TestNativeUsageMeasuresPreserveMultimodalDirectionAndUnits(t *testing.T) {
	t.Parallel()
	raw := `{"input_image_tokens":0,"output_image_tokens":7,"input_audio_seconds":1.5,"output_video_frames":12,"input_document_pages":2,"output_bytes":4096}`
	measures := NativeUsageMeasures(raw)
	if len(measures) != 6 {
		t.Fatalf("native measures = %d, want 6", len(measures))
	}
	want := map[string]struct {
		direction sdkmetering.FlowDirection
		unit      string
		value     string
		scale     uint8
	}{
		sdkmetering.ComponentImageToken:             {sdkmetering.DirectionInput, sdkmetering.UnitToken, "0", 0},
		sdkmetering.ComponentImageToken + ":output": {sdkmetering.DirectionOutput, sdkmetering.UnitToken, "7", 0},
		sdkmetering.ComponentAudio:                  {sdkmetering.DirectionInput, sdkmetering.UnitSecond, "15", 1},
		sdkmetering.ComponentVideo + ":output":      {sdkmetering.DirectionOutput, sdkmetering.UnitFrame, "12", 0},
		sdkmetering.ComponentDocument:               {sdkmetering.DirectionInput, sdkmetering.UnitPage, "2", 0},
		sdkmetering.ComponentFile + ":output":       {sdkmetering.DirectionOutput, sdkmetering.UnitByte, "4096", 0},
	}
	for _, measure := range measures {
		key := measure.Key.Component
		if measure.Key.Direction == sdkmetering.DirectionOutput {
			key += ":output"
		}
		expected, ok := want[key]
		if !ok {
			t.Errorf("unexpected measure key: %+v", measure.Key)
			continue
		}
		if measure.Key.Direction != expected.direction || measure.Key.Unit != expected.unit || measure.Value == nil || measure.Value.Coefficient != expected.value || measure.Value.Scale != expected.scale {
			t.Errorf("measure %s = %+v, want direction=%s unit=%s value=%s scale=%d", key, measure, expected.direction, expected.unit, expected.value, expected.scale)
		}
	}
}

func TestNativeUsageMeasuresRejectMalformedNegativeAndFractionalDiscreteValues(t *testing.T) {
	t.Parallel()
	raw := `{"input_image_tokens":-1,"output_video_frames":1.2,"input_audio_seconds":"oops","output_image_count":1e999}`
	if got := NativeUsageMeasures(raw); len(got) != 0 {
		t.Fatalf("invalid native values should be unavailable, got %d measures", len(got))
	}
}

func TestNativeUsageEvidenceRetainsPresentZeroOncePerSafePath(t *testing.T) {
	t.Parallel()
	raw := `{"input_image_tokens":0,"output_image_tokens":7,"input_audio_seconds":0}`
	evidence := NativeUsageEvidence(raw)
	if len(evidence) != 3 {
		t.Fatalf("native evidence = %d, want 3", len(evidence))
	}
	for _, field := range evidence {
		if !field.Present || field.Lexeme == "" {
			t.Errorf("evidence lost presence/lexeme: %+v", field)
		}
	}
}

func TestNativeUsageEvidenceRejectsMalformedNegativeAndFractionalDiscreteValues(t *testing.T) {
	t.Parallel()
	raw := `{"input_image_tokens":-1,"output_video_frames":1.2,"input_audio_seconds":"oops","output_image_count":1e999}`
	if got := NativeUsageEvidence(raw); len(got) != 0 {
		t.Fatalf("invalid native evidence should be unavailable, got %d fields", len(got))
	}
}

func TestNativeUsageMeasuresMapsOpenResponsesDetailDirections(t *testing.T) {
	t.Parallel()
	raw := `{"input_tokens_details":{"text_tokens":4,"audio_tokens":3,"images":0},"output_tokens_details":{"text_tokens":6,"audio_tokens":5,"images":2}}`
	measures := NativeUsageMeasures(raw)
	if len(measures) != 6 {
		t.Fatalf("detail measures = %d, want 6", len(measures))
	}
	want := map[string]string{
		"input/text_token":   "4",
		"output/text_token":  "6",
		"input/audio_token":  "3",
		"output/audio_token": "5",
		"input/image":        "0",
		"output/image":       "2",
	}
	for _, measure := range measures {
		key := string(measure.Key.Direction) + "/" + measure.Key.Component
		if got := want[key]; got == "" {
			t.Errorf("unexpected detail measure: %+v", measure.Key)
		} else if measure.Value == nil || measure.Value.Coefficient != got {
			t.Errorf("detail measure %s = %+v, want %s", key, measure.Value, got)
		}
	}
}

func TestNativeUsageMeasuresMapsChatCompletionAudioDetailDirections(t *testing.T) {
	raw := `{"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"cached_tokens":0},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4,"reasoning_tokens":0}}`
	measures := NativeUsageMeasures(raw)
	want := map[string]string{
		"input/text_token":   "8",
		"output/text_token":  "5",
		"input/audio_token":  "3",
		"output/audio_token": "4",
	}
	for _, measure := range measures {
		key := string(measure.Key.Direction) + "/" + measure.Key.Component
		if expected, ok := want[key]; ok {
			if measure.Value == nil || measure.Value.Coefficient != expected {
				t.Errorf("Chat detail measure %s = %+v, want %s", key, measure.Value, expected)
			}
			delete(want, key)
		}
	}
	if len(measures) != 4 {
		t.Fatalf("Chat detail measures = %d, want 4: %+v", len(measures), measures)
	}
	if len(want) != 0 {
		t.Fatalf("Chat detail measures missing: %v (all=%+v)", want, measures)
	}
}

// TestNativeUsageMeasuresMapsChatNestedImageTokens pins the provider-family
// Chat prompt_tokens_details.image_tokens field as a directional input image
// token measure. The OpenAI Chat CompletionUsage contract exposes image_tokens
// alongside text_tokens/audio_tokens in prompt_tokens_details (image input
// tokens present in the prompt), so the native image quantity is a token
// measure, never an image count. The documented completion_tokens_details has
// no image_tokens member, so no output image token measure may be fabricated.
func TestNativeUsageMeasuresMapsChatNestedImageTokens(t *testing.T) {
	raw := `{"prompt_tokens":13,"completion_tokens":9,"total_tokens":22,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"image_tokens":2},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}`
	measures := NativeUsageMeasures(raw)
	want := map[string]string{
		"input/text_token":   "8",
		"output/text_token":  "5",
		"input/audio_token":  "3",
		"output/audio_token": "4",
		"input/image_token":  "2",
	}
	got := make(map[string]string, len(measures))
	for _, measure := range measures {
		key := string(measure.Key.Direction) + "/" + measure.Key.Component
		if measure.Value == nil {
			t.Fatalf("measure %s has no value", key)
		}
		got[key] = measure.Value.Coefficient
		if measure.Key.Component == sdkmetering.ComponentImageToken && measure.Key.Unit != sdkmetering.UnitToken {
			t.Fatalf("image token measure %s unit = %q, want %q", key, measure.Key.Unit, sdkmetering.UnitToken)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("Chat nested image measures = %v, want %v", got, want)
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Errorf("Chat nested image measure %s = %q, want %q (all=%v)", key, got[key], expected, got)
		}
	}
	if _, present := got["output/image_token"]; present {
		t.Fatalf("completion_tokens_details must not fabricate an output image token: %v", got)
	}
	if _, present := got["input/image"]; present {
		t.Fatalf("image tokens must not be confused with an image count: %v", got)
	}
}

// TestNativeUsageMeasuresChatNestedImageTokenPresence pins the absent/null/
// explicit-zero image token shape policy. Absent and null are not an observable
// quantity; an explicit zero is a present, complete zero-valued measure.
func TestNativeUsageMeasuresChatNestedImageTokenPresence(t *testing.T) {
	absent := `{"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3}}`
	if got := nativeImageTokenMeasures(absent); len(got) != 0 {
		t.Fatalf("absent image_tokens produced %v, want none", got)
	}
	null := `{"prompt_tokens_details":{"image_tokens":null}}`
	if got := nativeImageTokenMeasures(null); len(got) != 0 {
		t.Fatalf("null image_tokens produced %v, want none", got)
	}
	zero := `{"prompt_tokens_details":{"image_tokens":0}}`
	if got := nativeImageTokenMeasures(zero); got["input/image_token"] != "0" {
		t.Fatalf("explicit zero image_tokens = %v, want input/image_token 0", got)
	}
}

// TestNativeUsageEvidenceChatNestedImagePrecedenceAndWirePath proves the nested
// provider-family image token field wins over a conflicting flat compatible
// alias and that the evidence lexeme is retained at the exact provider wire
// path $.usage.prompt_tokens_details.image_tokens rather than a substitute
// canonical location.
func TestNativeUsageEvidenceChatNestedImagePrecedenceAndWirePath(t *testing.T) {
	raw := `{"input_image_tokens":99,"prompt_tokens_details":{"image_tokens":2}}`
	if got := nativeImageTokenMeasures(raw); got["input/image_token"] != "2" {
		t.Fatalf("nested image_tokens must win over flat alias, got %v (%+v)", got, NativeUsageMeasures(raw))
	}
	assertEvidencePathsAndLexemes(t, NativeUsageEvidence(raw), map[string]string{
		"$.usage.prompt_tokens_details.image_tokens": "2",
	})
}

// TestNativeUsageMeasuresChatMalformedNestedImageTokensAreUnavailable pins the
// present-but-malformed nested image token policy: a present string, negative,
// fractional or overflowing image_tokens value is an explicit unavailable
// measure rather than a silently dropped quantity, so a child-only rating
// cannot falsely complete over the remaining text/audio children.
func TestNativeUsageMeasuresChatMalformedNestedImageTokensAreUnavailable(t *testing.T) {
	for _, raw := range []string{
		`{"prompt_tokens_details":{"image_tokens":"oops"}}`,
		`{"prompt_tokens_details":{"image_tokens":-1}}`,
		`{"prompt_tokens_details":{"image_tokens":2.5}}`,
		`{"prompt_tokens_details":{"image_tokens":1e999}}`,
	} {
		var imageMeasures []sdkmetering.Measure
		for _, measure := range NativeUsageMeasures(raw) {
			if measure.Key.Component == sdkmetering.ComponentImageToken {
				imageMeasures = append(imageMeasures, measure)
			}
		}
		if len(imageMeasures) != 1 {
			t.Fatalf("malformed image_tokens %s produced %d image measures, want one unavailable: %+v", raw, len(imageMeasures), NativeUsageMeasures(raw))
		}
		measure := imageMeasures[0]
		if measure.Key.Direction != sdkmetering.DirectionInput {
			t.Fatalf("malformed image_tokens %s measure key = %+v, want input image_token", raw, measure.Key)
		}
		if measure.Quality != sdkmetering.QualityUnavailable || measure.Value != nil {
			t.Fatalf("malformed image_tokens %s measure = %+v, want unavailable with no value", raw, measure)
		}
		if got := NativeUsageEvidence(raw); len(got) != 0 {
			t.Fatalf("malformed image_tokens %s produced evidence %+v, want none", raw, got)
		}
	}
}

// TestNativeUsageMeasuresChatMalformedNestedImageBeatsFlatAlias proves an
// invalid nested provider-family image token field is not silently supplanted
// by a valid compatible flat alias for the same canonical image token
// component. The nested field is the authoritative provider representation, so
// its malformed presence stays an explicit unavailable measure.
func TestNativeUsageMeasuresChatMalformedNestedImageBeatsFlatAlias(t *testing.T) {
	raw := `{"input_image_tokens":5,"prompt_tokens_details":{"image_tokens":"oops"}}`
	measures := NativeUsageMeasures(raw)
	if len(measures) != 1 {
		t.Fatalf("nested malformed + flat alias produced %d measures, want one unavailable: %+v", len(measures), measures)
	}
	measure := measures[0]
	if measure.Quality != sdkmetering.QualityUnavailable || measure.Value != nil {
		t.Fatalf("nested malformed must stay unavailable, got %+v", measure)
	}
	if got := NativeUsageEvidence(raw); len(got) != 0 {
		t.Fatalf("nested malformed + flat alias produced evidence %+v, want none", got)
	}
}

// TestNativeUsageIntegerCountLexemesShareCanonicalGrammar pins the R7-C1B
// reviewer follow-up invariant that the adapter's native measure and native
// evidence paths agree on exactly the SDK safe-evidence count grammar before
// either emits an observed quantity or retains a raw lexeme. For every declared
// discrete provider-family detail field (not only the image branch), exponent
// notation, a trailing-fraction zero and a value beyond the canonical 19-digit
// unsigned count bound are unavailable with no evidence lexeme; the canonical
// count shapes remain observed with their exact lexeme.
func TestNativeUsageIntegerCountLexemesShareCanonicalGrammar(t *testing.T) {
	details := []struct {
		name      string
		detail    string
		key       string
		component string
	}{
		{name: "text", detail: "prompt_tokens_details", key: "text_tokens", component: sdkmetering.ComponentTextToken},
		{name: "audio", detail: "prompt_tokens_details", key: "audio_tokens", component: sdkmetering.ComponentAudioToken},
		{name: "image", detail: "prompt_tokens_details", key: "image_tokens", component: sdkmetering.ComponentImageToken},
		{name: "cache_write", detail: "prompt_tokens_details", key: "cache_write_tokens", component: sdkmetering.ComponentCacheWriteInputToken},
	}
	nonCanonical := []struct {
		name   string
		value  string
		lexeme string
	}{
		{name: "scientific", value: `1e3`, lexeme: "1e3"},
		{name: "trailing_fraction_zero", value: `2.0`, lexeme: "2.0"},
		{name: "twenty_digit_overflow", value: `99999999999999999999`, lexeme: "99999999999999999999"},
	}
	canonical := []struct {
		name  string
		value string
	}{
		{name: "zero", value: "0"},
		{name: "small", value: "7"},
		{name: "nineteen_digit_max", value: "9999999999999999999"},
	}
	for _, detail := range details {
		for _, tc := range nonCanonical {
			t.Run(detail.name+"/"+tc.name, func(t *testing.T) {
				raw := `{"` + detail.detail + `":{"` + detail.key + `":` + tc.value + `}}`
				measures := NativeUsageMeasures(raw)
				if len(measures) != 1 {
					t.Fatalf("non-canonical %s %s produced %d measures, want one unavailable: %+v", detail.name, tc.name, len(measures), measures)
				}
				measure := measures[0]
				if measure.Key.Component != detail.component || measure.Key.Direction != sdkmetering.DirectionInput {
					t.Fatalf("non-canonical %s %s key = %+v, want input %s", detail.name, tc.name, measure.Key, detail.component)
				}
				if measure.Quality != sdkmetering.QualityUnavailable || measure.Value != nil {
					t.Fatalf("non-canonical %s %s measure = %+v, want unavailable with no value", detail.name, tc.name, measure)
				}
				for _, field := range NativeUsageEvidence(raw) {
					if field.Lexeme == tc.lexeme {
						t.Fatalf("non-canonical %s lexeme %q reached evidence: %+v", detail.name, tc.lexeme, field)
					}
				}
				if got := NativeUsageEvidence(raw); len(got) != 0 {
					t.Fatalf("non-canonical %s %s produced evidence %+v, want none", detail.name, tc.name, got)
				}
			})
		}
		for _, tc := range canonical {
			t.Run(detail.name+"/canonical_"+tc.name, func(t *testing.T) {
				raw := `{"` + detail.detail + `":{"` + detail.key + `":` + tc.value + `}}`
				measures := NativeUsageMeasures(raw)
				if len(measures) != 1 || measures[0].Quality != sdkmetering.QualityObserved || measures[0].Value == nil || measures[0].Value.Coefficient != tc.value {
					t.Fatalf("canonical %s %s measures = %+v, want observed %s", detail.name, tc.value, measures, tc.value)
				}
				assertEvidencePathsAndLexemes(t, NativeUsageEvidence(raw), map[string]string{
					"$.usage." + detail.detail + "." + detail.key: tc.value,
				})
			})
		}
	}
}

func nativeImageTokenMeasures(raw string) map[string]string {
	out := make(map[string]string)
	for _, measure := range NativeUsageMeasures(raw) {
		if measure.Key.Component != sdkmetering.ComponentImageToken || measure.Value == nil {
			continue
		}
		out[string(measure.Key.Direction)+"/"+measure.Key.Component] = measure.Value.Coefficient
	}
	return out
}

func TestNativeUsageEvidenceChatNestedAudioPreservesWirePathAndLexeme(t *testing.T) {
	raw := `{"input_audio_tokens":99,"output_audio_tokens":98,"prompt_tokens_details":{"audio_tokens":3},"completion_tokens_details":{"audio_tokens":4}}`
	assertDirectionalAudioMeasures(t, NativeUsageMeasures(raw), map[string]string{"input": "3", "output": "4"})
	evidence := NativeUsageEvidence(raw)
	want := map[string]string{
		"$.usage.prompt_tokens_details.audio_tokens":     "3",
		"$.usage.completion_tokens_details.audio_tokens": "4",
	}
	assertEvidencePathsAndLexemes(t, evidence, want)
}

func TestNativeUsageEvidenceResponsesNestedAudioWinsConflictingTopLevelAlias(t *testing.T) {
	raw := `{"input_audio_tokens":91,"output_audio_tokens":92,"input_tokens_details":{"audio_tokens":13},"output_tokens_details":{"audio_tokens":14}}`
	assertDirectionalAudioMeasures(t, NativeUsageMeasures(raw), map[string]string{"input": "13", "output": "14"})
	evidence := NativeUsageEvidence(raw)
	want := map[string]string{
		"$.usage.input_tokens_details.audio_tokens":  "13",
		"$.usage.output_tokens_details.audio_tokens": "14",
	}
	assertEvidencePathsAndLexemes(t, evidence, want)
}

func TestNativeUsageEvidenceAudioAliasPreservesSelectedWireLexeme(t *testing.T) {
	raw := `{"audio_input_tokens":8,"prompt_audio_tokens":7,"completion_audio_tokens":4}`
	assertDirectionalAudioMeasures(t, NativeUsageMeasures(raw), map[string]string{"input": "8", "output": "4"})
	evidence := NativeUsageEvidence(raw)
	want := map[string]string{
		"$.usage.audio_input_tokens":      "8",
		"$.usage.completion_audio_tokens": "4",
	}
	assertEvidencePathsAndLexemes(t, evidence, want)
}

func assertEvidencePathsAndLexemes(t *testing.T, evidence []sdkmetering.SafeEvidenceField, want map[string]string) {
	t.Helper()
	got := make(map[string]string, len(evidence))
	for _, field := range evidence {
		if _, duplicate := got[field.Path]; duplicate {
			t.Fatalf("duplicate evidence path %q: %+v", field.Path, evidence)
		}
		if !field.Present {
			t.Errorf("evidence path %q is not present", field.Path)
		}
		got[field.Path] = field.Lexeme
	}
	if len(got) != len(want) {
		t.Fatalf("evidence paths = %v, want %v", got, want)
	}
	for path, expected := range want {
		if got[path] != expected {
			t.Errorf("evidence %q = %q, want lexeme %q (all=%v)", path, got[path], expected, got)
		}
	}
}

func assertDirectionalAudioMeasures(t *testing.T, measures []sdkmetering.Measure, want map[string]string) {
	t.Helper()
	got := make(map[string]string, len(measures))
	for _, measure := range measures {
		if measure.Key.Component != sdkmetering.ComponentAudioToken {
			continue
		}
		if measure.Value == nil {
			t.Fatalf("audio measure %q has no value", measure.Key.Direction)
		}
		key := string(measure.Key.Direction)
		if _, duplicate := got[key]; duplicate {
			t.Fatalf("duplicate audio direction %q: %+v", key, measures)
		}
		got[key] = measure.Value.Coefficient
	}
	if len(got) != len(want) {
		t.Fatalf("audio measures = %v, want %v", got, want)
	}
	for direction, expected := range want {
		if got[direction] != expected {
			t.Errorf("audio %s measure = %q, want %q", direction, got[direction], expected)
		}
	}
}

func TestNativeUsageMeasuresChatAliasesDoNotDuplicateDirectionalAudio(t *testing.T) {
	raw := `{"input_audio_tokens":9,"audio_input_tokens":8,"prompt_audio_tokens":7,"output_audio_tokens":6,"audio_output_tokens":5,"completion_audio_tokens":4}`
	measures := NativeUsageMeasures(raw)
	if len(measures) != 2 {
		t.Fatalf("conflicting audio aliases produced %d measures, want one per direction: %+v", len(measures), measures)
	}
	want := map[string]string{"input": "9", "output": "6"}
	for _, measure := range measures {
		if measure.Value == nil || measure.Value.Coefficient != want[string(measure.Key.Direction)] {
			t.Errorf("conflicting alias measure = %+v, want first aliases %v", measure, want)
		}
		delete(want, string(measure.Key.Direction))
	}
	if len(want) != 0 {
		t.Fatalf("conflicting alias directions missing: %v", want)
	}
}

func TestNativeUsageMeasuresChatNullAndZeroAudioPresence(t *testing.T) {
	nullRaw := `{"prompt_tokens_details":{"audio_tokens":null},"completion_tokens_details":null}`
	if got := NativeUsageMeasures(nullRaw); len(got) != 0 {
		t.Fatalf("null/absent Chat audio fields produced measures: %+v", got)
	}
	if got := NativeUsageEvidence(nullRaw); len(got) != 0 {
		t.Fatalf("null/absent Chat audio fields produced evidence: %+v", got)
	}
	zeroRaw := `{"prompt_tokens_details":{"audio_tokens":0},"completion_tokens_details":{"audio_tokens":0}}`
	measures := NativeUsageMeasures(zeroRaw)
	if len(measures) != 2 {
		t.Fatalf("zero Chat audio fields produced %d measures, want 2: %+v", len(measures), measures)
	}
	for _, measure := range measures {
		if measure.Value == nil || measure.Value.Coefficient != "0" {
			t.Errorf("zero Chat audio measure = %+v, want present zero", measure)
		}
	}
	assertEvidencePathsAndLexemes(t, NativeUsageEvidence(zeroRaw), map[string]string{
		"$.usage.prompt_tokens_details.audio_tokens":     "0",
		"$.usage.completion_tokens_details.audio_tokens": "0",
	})
}

func TestProviderEvidenceDraftChatDetailsKeepAggregateAndChildrenDistinct(t *testing.T) {
	event := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   11,
		OutputTokens:  9,
		TotalTokens:   20,
		UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
		RawUsageJSON:  `{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}`,
		Accounting: aipapi.UsageAccountingMetadata{
			Source:    aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	draft := ProviderEvidenceDraft(event, "openai.chat.v2", "openai.chat:details")
	want := map[string]string{
		"input/input_token":   "11",
		"output/output_token": "9",
		"none/total_token":    "20",
		"input/text_token":    "8",
		"output/text_token":   "5",
		"input/audio_token":   "3",
		"output/audio_token":  "4",
	}
	got := make(map[string]string, len(draft.Measures))
	for _, measure := range draft.Measures {
		key := string(measure.Key.Direction) + "/" + measure.Key.Component
		if _, duplicate := got[key]; duplicate {
			t.Fatalf("duplicate aggregate/child measure %q: %+v", key, draft.Measures)
		}
		if measure.Value == nil {
			t.Fatalf("measure %q has no value", key)
		}
		got[key] = measure.Value.Coefficient
	}
	if len(got) != len(want) {
		t.Fatalf("Chat aggregate/child measure count = %d, want %d: %+v", len(got), len(want), got)
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Errorf("Chat aggregate/child measure %q = %q, want %q (all=%+v)", key, got[key], expected, got)
		}
	}
}

func TestProviderEvidenceDraftRetainsGenuineProviderCostAndRawLexeme(t *testing.T) {
	t.Parallel()
	event := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		CostNanoUnits: 140_000,
		Currency:      "USD",
		CostPresent:   true,
		RawUsageJSON:  `{"input_tokens":3,"cost":0.00014}`,
		Accounting: aipapi.UsageAccountingMetadata{
			Source: aipapi.UsageSourceProviderReported,
		},
	}
	draft := ProviderEvidenceDraft(event, "openai.chat.v2", "openai.chat:usage")
	if len(draft.Charges) != 1 || draft.Charges[0].Amount == nil {
		t.Fatalf("provider cost charge = %+v, want one amount", draft.Charges)
	}
	if got := draft.Charges[0].Amount.CanonicalString(); got != "14/5" {
		t.Fatalf("provider cost amount = %q, want 14/5", got)
	}
	if got := draft.Charges[0].Currency; got != "USD" {
		t.Fatalf("provider cost currency = %q, want USD", got)
	}
	var amount, currency sdkmetering.SafeEvidenceField
	for _, field := range draft.Evidence {
		switch field.Path {
		case "$.cost.amount":
			amount = field
		case "$.cost.currency":
			currency = field
		}
	}
	if amount.Lexeme != "0.00014" || !amount.Present || currency.Lexeme != "USD" || !currency.Present {
		t.Fatalf("provider cost evidence = amount=%+v currency=%+v", amount, currency)
	}
}

func TestProviderEvidenceDraftRejectsMalformedOrNegativeProviderCost(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"cost":-1}`, `{"cost":1e100}`, `{"cost":"not-a-number"}`} {
		event := aipapi.Event{
			Kind:          aipapi.EventUsageDelta,
			CostNanoUnits: 1,
			Currency:      "USD",
			CostPresent:   true,
			RawUsageJSON:  raw,
			Accounting: aipapi.UsageAccountingMetadata{
				Source: aipapi.UsageSourceProviderReported,
			},
		}
		draft := ProviderEvidenceDraft(event, "openai.chat.v2", "openai.chat:usage")
		if len(draft.Charges) != 0 {
			t.Errorf("raw cost %s produced charge %+v", raw, draft.Charges)
		}
	}
}
