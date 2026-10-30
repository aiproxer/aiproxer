package tiktoken

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/infra/tokenizers/imageestimator"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	tiktokenlib "github.com/tiktoken-go/tokenizer"
)

func TestCounterImplementsLocalCounter(t *testing.T) {
	t.Parallel()

	var _ app.LocalCounter = (*Counter)(nil)
}

func TestCountTextExplicitCL100KBase(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	got, err := counter.CountText(context.Background(), app.CountTextInput{Model: "openai:cl100k_base", Text: "hello world"})
	if err != nil {
		t.Fatalf("CountText() error = %v", err)
	}

	assertMetadata(t, got, "cl100k_base", "openai:cl100k_base")
	if got.InputTokens != 2 {
		t.Fatalf("InputTokens = %d, want 2", got.InputTokens)
	}
	if got.OutputTokens != 0 {
		t.Fatalf("OutputTokens = %d, want 0", got.OutputTokens)
	}
	if got.TotalTokens != 2 {
		t.Fatalf("TotalTokens = %d, want 2", got.TotalTokens)
	}
	if !got.TotalTokensPresent {
		t.Fatal("TotalTokensPresent=false, want true for counted total")
	}
}

func TestCountTextEmptyMarksTotalPresent(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	got, err := counter.CountText(context.Background(), app.CountTextInput{Model: "cl100k_base", Text: ""})
	if err != nil {
		t.Fatalf("CountText() error = %v", err)
	}
	if got.TotalTokens != 0 {
		t.Fatalf("TotalTokens=%d want 0", got.TotalTokens)
	}
	if !got.TotalTokensPresent {
		t.Fatal("TotalTokensPresent=false; explicit zero count must remain present")
	}
}

func TestCountTextExplicitO200KBase(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	got, err := counter.CountText(context.Background(), app.CountTextInput{Model: "o200k_base", Text: "hello world"})
	if err != nil {
		t.Fatalf("CountText() error = %v", err)
	}

	assertMetadata(t, got, "o200k_base", "o200k_base")
	if got.InputTokens != 2 {
		t.Fatalf("InputTokens = %d, want 2", got.InputTokens)
	}
	if got.TotalTokens != 2 {
		t.Fatalf("TotalTokens = %d, want 2", got.TotalTokens)
	}
}

func TestGPT4OModelMapsToO200KBase(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	got, err := counter.CountText(context.Background(), app.CountTextInput{Model: "gpt-4o-mini", Text: "hello world"})
	if err != nil {
		t.Fatalf("CountText() error = %v", err)
	}

	assertMetadata(t, got, "o200k_base", "gpt-4o-mini")
	if got.InputTokens != 2 {
		t.Fatalf("InputTokens = %d, want 2", got.InputTokens)
	}
}

func TestConfiguredModelMappingOverridesBuiltInEncoding(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{
		DefaultEncoding: "cl100k_base",
		ModelMappings:   map[string]string{"gpt-4o-mini": "cl100k_base"},
	})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	got, err := counter.CountText(context.Background(), app.CountTextInput{Model: "gpt-4o-mini", Text: "hello world"})
	if err != nil {
		t.Fatalf("CountText() error = %v", err)
	}

	assertMetadata(t, got, "cl100k_base", "gpt-4o-mini")
}

func TestNewCounterUnsupportedMappedEncoding(t *testing.T) {
	t.Parallel()

	_, err := NewCounter(Config{DefaultEncoding: "cl100k_base", ModelMappings: map[string]string{"custom": "not_real"}})
	if !errors.Is(err, app.ErrLocalUnavailable) {
		t.Fatalf("NewCounter() error = %v, want ErrLocalUnavailable", err)
	}
}

func TestUnknownModelUsesDefaultEncodingAndRecordsFallback(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	got, err := counter.CountText(context.Background(), app.CountTextInput{Model: "custom-model", Text: "hello world"})
	if err != nil {
		t.Fatalf("CountText() error = %v", err)
	}

	assertMetadata(t, got, "cl100k_base", "custom-model")
	if len(got.Fallbacks) != 1 {
		t.Fatalf("fallback count = %d, want 1", len(got.Fallbacks))
	}
	if got.Fallbacks[0].Reason != app.FallbackReasonLocalDefaultEncoding {
		t.Fatalf("fallback reason = %q, want %q", got.Fallbacks[0].Reason, app.FallbackReasonLocalDefaultEncoding)
	}
	if got.Fallbacks[0].Message == "" {
		t.Fatal("fallback message is empty")
	}
}

func TestNewCounterUnsupportedDefaultEncoding(t *testing.T) {
	t.Parallel()

	_, err := NewCounter(Config{DefaultEncoding: "not_real"})
	if !errors.Is(err, app.ErrLocalUnavailable) {
		t.Fatalf("NewCounter() error = %v, want ErrLocalUnavailable", err)
	}
}

func TestCountTextInvalidExplicitEncodingDoesNotFallback(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	_, err = counter.CountText(context.Background(), app.CountTextInput{Model: "openai:not_real", Text: "hello world"})
	if !errors.Is(err, app.ErrLocalUnavailable) {
		t.Fatalf("CountText() error = %v, want ErrLocalUnavailable", err)
	}
}

func TestCountOutputSetsOutputTokensOnly(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	got, err := counter.CountOutput(context.Background(), app.CountOutputInput{Model: "cl100k_base", Text: "hello world"})
	if err != nil {
		t.Fatalf("CountOutput() error = %v", err)
	}

	assertMetadata(t, got, "cl100k_base", "cl100k_base")
	if got.InputTokens != 0 {
		t.Fatalf("InputTokens = %d, want 0", got.InputTokens)
	}
	if got.OutputTokens != 2 {
		t.Fatalf("OutputTokens = %d, want 2", got.OutputTokens)
	}
	if got.TotalTokens != 2 {
		t.Fatalf("TotalTokens = %d, want 2", got.TotalTokens)
	}
}

func TestCountCallTextOnlyAddsChatFraming(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello world")}}}}
	got, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: call})
	if err != nil {
		t.Fatalf("CountCall() error = %v", err)
	}

	textOnly, err := counter.CountText(context.Background(), app.CountTextInput{Model: "cl100k_base", Text: "hello world"})
	if err != nil {
		t.Fatalf("CountText() error = %v", err)
	}
	assertMetadata(t, got, "cl100k_base", "cl100k_base")
	if got.InputTokens <= textOnly.InputTokens {
		t.Fatalf("InputTokens = %d, want greater than raw text tokens %d", got.InputTokens, textOnly.InputTokens)
	}
	if got.TotalTokens != got.InputTokens {
		t.Fatalf("TotalTokens = %d, want InputTokens %d", got.TotalTokens, got.InputTokens)
	}
	again, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: call})
	if err != nil {
		t.Fatalf("second CountCall() error = %v", err)
	}
	if again.InputTokens != got.InputTokens {
		t.Fatalf("second InputTokens = %d, want deterministic %d", again.InputTokens, got.InputTokens)
	}
}

func TestCountCallIncludesInstructionsAndMessages(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	messagesOnly := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("summarize")}}}}
	withInstructions := messagesOnly
	withInstructions.Instructions = []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("answer tersely")}}}

	without, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: messagesOnly})
	if err != nil {
		t.Fatalf("CountCall(messagesOnly) error = %v", err)
	}
	with, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: withInstructions})
	if err != nil {
		t.Fatalf("CountCall(withInstructions) error = %v", err)
	}
	if with.InputTokens <= without.InputTokens {
		t.Fatalf("with instructions InputTokens = %d, want greater than %d", with.InputTokens, without.InputTokens)
	}
}

func TestCountCallRoleEstimatorChangesTokenCount(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	base := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("same content")}}}}
	changedRole := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.Role("custom-diagnostic-role"), Parts: []aipapi.Part{aipapi.TextPart("same content")}}}}

	baseCount, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: base})
	if err != nil {
		t.Fatalf("CountCall(base) error = %v", err)
	}
	changedRoleCount, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: changedRole})
	if err != nil {
		t.Fatalf("CountCall(changedRole) error = %v", err)
	}
	if baseCount.InputTokens == changedRoleCount.InputTokens {
		t.Fatalf("InputTokens did not change when role changed: %d", baseCount.InputTokens)
	}
}

func TestCountCallToolDefinitionIncreasesTokens(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	base := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("weather")}}}}
	withTool := base
	withTool.Tools = []aipapi.ToolDef{{
		Name:        "get_weather",
		Description: "Get weather for a city.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
	}}
	withTool.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "get_weather"}

	without, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: base})
	if err != nil {
		t.Fatalf("CountCall(base) error = %v", err)
	}
	with, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: withTool})
	if err != nil {
		t.Fatalf("CountCall(withTool) error = %v", err)
	}
	if with.InputTokens <= without.InputTokens {
		t.Fatalf("with tool InputTokens = %d, want greater than %d", with.InputTokens, without.InputTokens)
	}
}

func TestCountCallUnsupportedImageAndFileParts(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	for _, part := range []aipapi.Part{
		{Kind: aipapi.PartImageRef, ImageRef: "image-1", ImageMIME: "image/png"},
		aipapi.FilePart("file-1", "application/pdf", "doc.pdf"),
	} {
		call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{part}}}}
		_, err = counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: call})
		if !errors.Is(err, app.ErrLocalUnavailable) {
			t.Fatalf("CountCall(%s) error = %v, want ErrLocalUnavailable", part.Kind, err)
		}
	}
}

func TestCountCallImageDataURIUsesBaseTokensForLowAndAutoDetail(t *testing.T) {
	t.Parallel()

	for _, detail := range []string{"", "low", "auto"} {
		t.Run("detail_"+detail, func(t *testing.T) {
			t.Parallel()
			counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base", Image: ImageConfig{MaxDecodedBytes: 1024}})
			if err != nil {
				t.Fatalf("NewCounter() error = %v", err)
			}

			base := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("describe")}}}}
			withImage := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{
				aipapi.TextPart("describe"), imagePart(dataURIPNG(t, 64, 64), detail),
			}}}}

			baseCount, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: base})
			if err != nil {
				t.Fatalf("CountCall(base) error = %v", err)
			}
			got, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: withImage})
			if err != nil {
				t.Fatalf("CountCall(withImage) error = %v", err)
			}
			if got.InputTokens-baseCount.InputTokens != 85 {
				t.Fatalf("image tokens = %d, want 85", got.InputTokens-baseCount.InputTokens)
			}
		})
	}
}

func TestCountCallImageDataURIUsesHighDetailTiles(t *testing.T) {
	t.Parallel()

	// The decoded-size cap is not under test here; image/png output size varies
	// across Go releases, so leave ample headroom for the 1024x1024 fixture.
	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base", Image: ImageConfig{MaxDecodedBytes: 1 << 20}})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	base := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("describe")}}}}
	withImage := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{
		aipapi.TextPart("describe"), imagePart(dataURIPNG(t, 1024, 1024), "high"),
	}}}}

	baseCount, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: base})
	if err != nil {
		t.Fatalf("CountCall(base) error = %v", err)
	}
	got, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: withImage})
	if err != nil {
		t.Fatalf("CountCall(withImage) error = %v", err)
	}
	if got.InputTokens-baseCount.InputTokens != 765 {
		t.Fatalf("image tokens = %d, want 765", got.InputTokens-baseCount.InputTokens)
	}
}

func TestCountCallImageDataURIRejectsOversizedDecodedData(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base", Image: ImageConfig{MaxDecodedBytes: 8}})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{imagePart(dataURIPNG(t, 8, 8), "low")}}}}
	_, err = counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: call})
	if !errors.Is(err, app.ErrLocalUnavailable) {
		t.Fatalf("CountCall() error = %v, want ErrLocalUnavailable", err)
	}
}

func TestCountCallImageDataURIRejectsInvalidBase64AndNonImageData(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base", Image: ImageConfig{MaxDecodedBytes: 1024}})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	for _, ref := range []string{
		"data:image/png;base64,not base64",
		"data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not an image")),
	} {
		t.Run(ref, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{imagePart(ref, "low")}}}}
			_, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: call})
			if !errors.Is(err, app.ErrLocalUnavailable) {
				t.Fatalf("CountCall() error = %v, want ErrLocalUnavailable", err)
			}
		})
	}
}

func TestCountCallImageWithoutLocalBytesRequiresDefaultOptIn(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{imagePart("https://example.com/image.png", "low")}}}}
	_, err = counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: call})
	if !errors.Is(err, app.ErrLocalUnavailable) {
		t.Fatalf("CountCall() error = %v, want ErrLocalUnavailable", err)
	}
}

func TestCountCallImageWithoutLocalBytesUsesConfiguredDefault(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base", Image: ImageConfig{UseDefaultTokens: true, DefaultTokens: 123}})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	base := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("describe")}}}}
	withImage := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{
		aipapi.TextPart("describe"), imagePart("https://example.com/image.png", "high"),
	}}}}
	baseCount, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: base})
	if err != nil {
		t.Fatalf("CountCall(base) error = %v", err)
	}
	got, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: withImage})
	if err != nil {
		t.Fatalf("CountCall(withImage) error = %v", err)
	}
	if got.InputTokens-baseCount.InputTokens != 123 {
		t.Fatalf("image tokens = %d, want 123", got.InputTokens-baseCount.InputTokens)
	}
}

func TestCountCallUnknownModelRecordsFallback(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello world")}}}}
	got, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "custom-model", Call: call})
	if err != nil {
		t.Fatalf("CountCall() error = %v", err)
	}

	assertMetadata(t, got, "cl100k_base", "custom-model")
	if len(got.Fallbacks) != 1 {
		t.Fatalf("fallback count = %d, want 1", len(got.Fallbacks))
	}
	if got.Fallbacks[0].Reason != app.FallbackReasonLocalDefaultEncoding {
		t.Fatalf("fallback reason = %q, want %q", got.Fallbacks[0].Reason, app.FallbackReasonLocalDefaultEncoding)
	}
}

func TestCountCallToolFormattingDeterministicForParameterKeyOrder(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	callA := toolFormattingCall(json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"},"units":{"enum":["c","f"]}},"required":["city"]}`))
	callB := toolFormattingCall(json.RawMessage(`{"required":["city"],"properties":{"units":{"enum":["c","f"]},"city":{"type":"string"}},"type":"object"}`))

	gotA, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: callA})
	if err != nil {
		t.Fatalf("CountCall(callA) error = %v", err)
	}
	gotB, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: callB})
	if err != nil {
		t.Fatalf("CountCall(callB) error = %v", err)
	}
	if gotA.InputTokens != gotB.InputTokens {
		t.Fatalf("InputTokens differ for equivalent parameter objects: %d vs %d", gotA.InputTokens, gotB.InputTokens)
	}
}

func TestFormatToolDefOutputStableAndContainsCanonicalFields(t *testing.T) {
	t.Parallel()

	tool := aipapi.ToolDef{
		Name:        "get_weather",
		Description: "Get weather for a city.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"},"units":{"enum":["c","f"]}}}`),
	}

	got, err := formatToolDef(tool)
	if err != nil {
		t.Fatalf("formatToolDef() error = %v", err)
	}
	want := "namespace functions {\n" +
		"type get_weather = (_: {\"properties\":{\"city\":{\"type\":\"string\"},\"units\":{\"enum\":[\"c\",\"f\"]}},\"type\":\"object\"}) => any // Get weather for a city.\n" +
		"}\n"
	if got != want {
		t.Fatalf("formatToolDef() = %q, want %q", got, want)
	}
}

func TestCountCallRejectsInvalidJSONPart(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"bad"`)}}}}}
	_, err = counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: call})
	if !errors.Is(err, app.ErrLocalUnavailable) {
		t.Fatalf("CountCall() error = %v, want ErrLocalUnavailable", err)
	}
	if !strings.Contains(err.Error(), "invalid json") {
		t.Fatalf("CountCall() error = %q, want invalid json detail", err)
	}
}

func TestCountCallRejectsInvalidToolParameters(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("weather")}}},
		Tools:    []aipapi.ToolDef{{Name: "get_weather", Parameters: json.RawMessage(`{"bad"`)}},
	}
	_, err = counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: call})
	if !errors.Is(err, app.ErrLocalUnavailable) {
		t.Fatalf("CountCall() error = %v, want ErrLocalUnavailable", err)
	}
	if !strings.Contains(err.Error(), "invalid tool parameters json") {
		t.Fatalf("CountCall() error = %q, want invalid tool parameters detail", err)
	}
}

func TestCountCallUnsupportedToolResultPart(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call-1"}}}}}
	_, err = counter.CountCall(context.Background(), app.CountCallInput{Model: "gpt-4o", Call: call})
	if !errors.Is(err, app.ErrLocalUnavailable) {
		t.Fatalf("CountCall() error = %v, want ErrLocalUnavailable", err)
	}
}

func TestCountCall_countsReasoningParts(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}

	opaque := json.RawMessage(`{"data":"hidden"}`)
	if !json.Valid(opaque) {
		t.Fatal("opaque must be valid JSON")
	}
	baseline := aipapi.Call{Messages: []aipapi.Message{{
		Role:  aipapi.RoleAssistant,
		Parts: []aipapi.Part{aipapi.TextPart("answer")},
	}}}
	withReasoning := aipapi.Call{Messages: []aipapi.Message{{
		Role: aipapi.RoleAssistant,
		Parts: []aipapi.Part{
			{
				Kind: aipapi.PartReasoning,
				Reasoning: &aipapi.ReasoningPart{
					Dialect:   aipapi.ReasoningDialectAnthropicThinkingV1,
					Text:      "long reasoning token bait abcdefghijklmnop",
					Signature: "sig_token_bait",
					Opaque:    opaque,
				},
			},
			aipapi.TextPart("answer"),
		},
	}}}

	baseCount, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: baseline})
	if err != nil {
		t.Fatalf("baseline CountCall() error = %v", err)
	}
	got, err := counter.CountCall(context.Background(), app.CountCallInput{Model: "cl100k_base", Call: withReasoning})
	if err != nil {
		t.Fatalf("reasoning CountCall() error = %v (reasoning parts must be token-count inputs, not unsupported)", err)
	}
	if got.InputTokens <= baseCount.InputTokens {
		t.Fatalf("InputTokens=%d want > baseline %d (reasoning text/signature/opaque must increase token count)", got.InputTokens, baseCount.InputTokens)
	}
}

func imagePart(ref, detail string) aipapi.Part {
	content := json.RawMessage(nil)
	if detail != "" {
		content = json.RawMessage(`{"detail":"` + detail + `"}`)
	}
	return aipapi.Part{Kind: aipapi.PartImageRef, ImageRef: ref, ImageMIME: "image/png", Content: content}
}

func dataURIPNG(t *testing.T, width, height int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestCountCallHonorsCanceledContextDuringPartLoop(t *testing.T) {
	t.Parallel()

	codec := &cancelAfterCodec{remaining: 4}
	parts := make([]aipapi.Part, 0, 32)
	for range 32 {
		parts = append(parts, aipapi.TextPart("hello world"))
	}
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: parts}}}
	ctx, cancel := context.WithCancel(context.Background())
	codec.cancel = cancel

	_, err := countCallTokens(ctx, codec, counterImageEstimator(t), call)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("countCallTokens() error = %v, want context.Canceled", err)
	}
}

func toolFormattingCall(parameters json.RawMessage) aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("weather")}}},
		Tools: []aipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Get weather for a city.",
			Parameters:  parameters,
		}},
	}
}

type cancelAfterCodec struct {
	remaining int
	cancel    context.CancelFunc
}

func (c *cancelAfterCodec) GetName() string { return "cancel-after" }

func (c *cancelAfterCodec) Count(string) (int, error) {
	c.remaining--
	if c.remaining <= 0 && c.cancel != nil {
		c.cancel()
	}
	return 1, nil
}

func (c *cancelAfterCodec) Encode(string) ([]uint, []string, error) { return nil, nil, nil }

func (c *cancelAfterCodec) Decode([]uint) (string, error) { return "", nil }

func (c *cancelAfterCodec) ErrorMode() tiktokenlib.ErrorMode { return tiktokenlib.ErrorModeNone }

func (c *cancelAfterCodec) SetErrorMode(tiktokenlib.ErrorMode) {}

func TestCountTextHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = counter.CountText(ctx, app.CountTextInput{Model: "cl100k_base", Text: "hello world"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CountText() error = %v, want context.Canceled", err)
	}
}

func assertMetadata(t *testing.T, got app.CountResult, wantEncoding, wantModelUsed string) {
	t.Helper()
	if got.Accounting.Source != aipapi.UsageSourceLocalTokenizer {
		t.Fatalf("source = %q, want %q", got.Accounting.Source, aipapi.UsageSourceLocalTokenizer)
	}
	if got.Accounting.Authority != aipapi.UsageAuthorityEstimated {
		t.Fatalf("authority = %q, want %q", got.Accounting.Authority, aipapi.UsageAuthorityEstimated)
	}
	if got.Accounting.Tokenizer.Type != "tiktoken" {
		t.Fatalf("tokenizer type = %q, want tiktoken", got.Accounting.Tokenizer.Type)
	}
	if got.Accounting.Tokenizer.ID != wantEncoding {
		t.Fatalf("tokenizer id = %q, want %q", got.Accounting.Tokenizer.ID, wantEncoding)
	}
	if got.Accounting.Tokenizer.ModelUsed != wantModelUsed {
		t.Fatalf("model used = %q, want %q", got.Accounting.Tokenizer.ModelUsed, wantModelUsed)
	}
}

func counterImageEstimator(t *testing.T) imageestimator.Estimator {
	t.Helper()
	counter, err := NewCounter(Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatalf("NewCounter() error = %v", err)
	}
	return counter.imageEstimator
}
