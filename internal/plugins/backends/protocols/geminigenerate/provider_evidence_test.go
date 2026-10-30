package geminigenerate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"google.golang.org/genai"
)

func TestGeminiNativeMeasuresPreserveModalityDirectionCacheReasoningAndGroundedTool(t *testing.T) {
	t.Parallel()
	u := &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:           20,
		CandidatesTokenCount:       8,
		ThoughtsTokenCount:         3,
		CachedContentTokenCount:    4,
		ToolUsePromptTokenCount:    2,
		TotalTokenCount:            33,
		PromptTokensDetails:        []*genai.ModalityTokenCount{{Modality: genai.MediaModalityText, TokenCount: 10}, {Modality: genai.MediaModalityImage, TokenCount: 10}},
		CandidatesTokensDetails:    []*genai.ModalityTokenCount{{Modality: genai.MediaModalityText, TokenCount: 8}},
		CacheTokensDetails:         []*genai.ModalityTokenCount{{Modality: genai.MediaModalityImage, TokenCount: 4}},
		ToolUsePromptTokensDetails: []*genai.ModalityTokenCount{{Modality: genai.MediaModalityText, TokenCount: 2}},
	}
	measures := geminiNativeMeasures(u)
	if len(measures) != 6 {
		t.Fatalf("native Gemini measures = %d, want 6", len(measures))
	}
	hasInputImage, hasOutputText, hasCachedImage, hasGrounded := false, false, false, false
	for _, measure := range measures {
		switch {
		case measure.Key.Component == sdkmetering.ComponentImageToken && measure.Key.Direction == sdkmetering.DirectionInput && dimensionValue(measure.Key, "evidence_plane") == "prompt":
			hasInputImage = true
		case measure.Key.Component == sdkmetering.ComponentTextToken && measure.Key.Direction == sdkmetering.DirectionOutput:
			hasOutputText = true
		case measure.Key.Component == sdkmetering.ComponentImageToken && dimensionValue(measure.Key, "evidence_plane") == "cache":
			hasCachedImage = true
		case measure.Key.Component == "grounded_tool_token":
			hasGrounded = true
		}
	}
	if !hasInputImage || !hasOutputText || !hasCachedImage || !hasGrounded {
		t.Fatalf("missing modality/plane measures: %+v", measures)
	}
	if ev := usageEvent(&genai.GenerateContentResponse{UsageMetadata: u}); ev == nil || !ev.UsagePresence.CacheReadTokens || !ev.UsagePresence.ReasoningTokens || ev.Accounting.Source != aipapi.UsageSourceProviderReported {
		t.Fatalf("usage event lost cache/reasoning/provider presence: %+v", ev)
	}
}

func TestGeminiEvidenceDraftWithTrafficTypeRetainsSingleServiceContext(t *testing.T) {
	t.Parallel()
	u := &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 2, CandidatesTokenCount: 1, TotalTokenCount: 3, TrafficType: "on_demand"}
	ev := usageEvent(&genai.GenerateContentResponse{UsageMetadata: u})
	if ev == nil || ev.Accounting.ServiceContext != "on_demand" {
		t.Fatalf("usage event service context=%+v", ev)
	}
	b := coremetering.NewProviderEvidenceBuffer()
	b.Add(geminiEvidenceDraft(*ev, u))
	b.BindEconomicEvidence(coremetering.ObservationIdentity{StoreID: "store", BLegID: "b-leg"})
	observations := b.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("Gemini service-context observations=%d, want 1", len(observations))
	}
	count := 0
	for _, field := range observations[0].Evidence {
		if field.Path == "$.provider_schema.service_context" {
			count++
			if field.Lexeme != "on_demand" {
				t.Fatalf("service context lexeme=%q", field.Lexeme)
			}
		}
	}
	if count != 1 {
		t.Fatalf("service context evidence count=%d, want 1", count)
	}
}

func TestGeminiZeroUsageMetadataRetainsPresenceWithoutInventingPrice(t *testing.T) {
	t.Parallel()
	ev := usageEvent(&genai.GenerateContentResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{}})
	if ev == nil || !ev.UsagePresence.InputTokens || !ev.UsagePresence.OutputTokens || !ev.UsagePresence.TotalTokens {
		t.Fatalf("zero provider usage must remain an explicit observation: %+v", ev)
	}
	if ev.CostPresent || ev.Accounting.Source != aipapi.UsageSourceProviderReported {
		t.Fatalf("Gemini usage must not invent monetary evidence: %+v", ev)
	}
}

func TestGeminiCacheOnlyUsageRetainsCachePresence(t *testing.T) {
	t.Parallel()
	ev := usageEvent(&genai.GenerateContentResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{CachedContentTokenCount: 4}})
	if ev == nil || !ev.UsagePresence.CacheReadTokens || ev.CacheReadTokens != 4 {
		t.Fatalf("cache-only Gemini usage lost cache evidence: %+v", ev)
	}
}

func TestGeminiNegativeUsageFieldRemainsUnavailable(t *testing.T) {
	t.Parallel()
	ev := usageEvent(&genai.GenerateContentResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: -1, CandidatesTokenCount: 2, TotalTokenCount: 2}})
	if ev == nil || ev.UsagePresence.InputTokens || ev.InputTokens != 0 {
		t.Fatalf("negative Gemini input became provider evidence: %+v", ev)
	}
}

func dimensionValue(key sdkmetering.ComponentKey, name string) string {
	for _, dimension := range key.Dimensions {
		if dimension.Name == name {
			return dimension.Value
		}
	}
	return ""
}

func TestGeminiNativeMeasuresPreserveEveryReportedModalityAndDirection(t *testing.T) {
	t.Parallel()
	u := &genai.GenerateContentResponseUsageMetadata{
		PromptTokensDetails: []*genai.ModalityTokenCount{
			{Modality: genai.MediaModalityText, TokenCount: 3},
			{Modality: genai.MediaModalityImage, TokenCount: 5},
			{Modality: genai.MediaModalityAudio, TokenCount: 7},
			{Modality: genai.MediaModalityVideo, TokenCount: 11},
			{Modality: genai.MediaModalityDocument, TokenCount: 13},
		},
		CandidatesTokensDetails: []*genai.ModalityTokenCount{
			{Modality: genai.MediaModalityText, TokenCount: 2},
			{Modality: genai.MediaModalityImage, TokenCount: 4},
			{Modality: genai.MediaModalityAudio, TokenCount: 6},
			{Modality: genai.MediaModalityVideo, TokenCount: 8},
			{Modality: genai.MediaModalityDocument, TokenCount: 10},
		},
		CacheTokensDetails: []*genai.ModalityTokenCount{
			{Modality: genai.MediaModalityImage, TokenCount: 17},
		},
		ToolUsePromptTokenCount: 19,
		ToolUsePromptTokensDetails: []*genai.ModalityTokenCount{
			{Modality: genai.MediaModalityAudio, TokenCount: 23},
		},
	}

	measures := geminiNativeMeasures(u)
	want := []struct {
		direction sdkmetering.FlowDirection
		component string
		plane     string
		value     string
	}{
		{sdkmetering.DirectionInput, sdkmetering.ComponentTextToken, "prompt", "3"},
		{sdkmetering.DirectionInput, sdkmetering.ComponentImageToken, "prompt", "5"},
		{sdkmetering.DirectionInput, sdkmetering.ComponentAudioToken, "prompt", "7"},
		{sdkmetering.DirectionInput, sdkmetering.ComponentVideoToken, "prompt", "11"},
		{sdkmetering.DirectionInput, sdkmetering.ComponentDocumentToken, "prompt", "13"},
		{sdkmetering.DirectionOutput, sdkmetering.ComponentTextToken, "candidates", "2"},
		{sdkmetering.DirectionOutput, sdkmetering.ComponentImageToken, "candidates", "4"},
		{sdkmetering.DirectionOutput, sdkmetering.ComponentAudioToken, "candidates", "6"},
		{sdkmetering.DirectionOutput, sdkmetering.ComponentVideoToken, "candidates", "8"},
		{sdkmetering.DirectionOutput, sdkmetering.ComponentDocumentToken, "candidates", "10"},
		{sdkmetering.DirectionInput, sdkmetering.ComponentImageToken, "cache", "17"},
		{sdkmetering.DirectionInput, sdkmetering.ComponentAudioToken, "grounded_tool", "23"},
		{sdkmetering.DirectionInput, "grounded_tool_token", "", "19"},
	}
	if len(measures) != len(want) {
		t.Fatalf("Gemini native measures = %d, want %d: %+v", len(measures), len(want), measures)
	}
	for _, expected := range want {
		var found bool
		for _, measure := range measures {
			if measure.Key.Direction != expected.direction || measure.Key.Component != expected.component ||
				dimensionValue(measure.Key, "evidence_plane") != expected.plane {
				continue
			}
			if measure.Value == nil || measure.Value.Coefficient != expected.value {
				t.Fatalf("Gemini %s/%s/%s value = %+v, want %s", expected.direction, expected.component, expected.plane, measure.Value, expected.value)
			}
			if measure.Key.Unit != sdkmetering.UnitToken || measure.Key.SchemaID != "gemini.usage.v2" || measure.Quality != sdkmetering.QualityObserved {
				t.Fatalf("Gemini %s/%s has non-native identity/quality: %+v", expected.direction, expected.component, measure)
			}
			found = true
			break
		}
		if !found {
			t.Fatalf("missing Gemini native measure %s/%s/%s", expected.direction, expected.component, expected.plane)
		}
	}
}

func TestGeminiAbsentGroundedToolDoesNotInventNativeMeasure(t *testing.T) {
	t.Parallel()
	measures := geminiNativeMeasures(&genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: 12, CandidatesTokenCount: 4, TotalTokenCount: 16,
	})
	for _, measure := range measures {
		if measure.Key.Component == "grounded_tool_token" {
			t.Fatalf("absent Gemini grounded-tool field became a zero measure: %+v", measure)
		}
	}
}

func TestGeminiDetailsOnlyJSONPreservesNativeModalityWithoutScalarAggregate(t *testing.T) {
	t.Parallel()
	const payload = `{"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":4,"totalTokenCount":16,"toolUsePromptTokensDetails":[{"modality":"AUDIO","tokenCount":23}]}}`
	var response genai.GenerateContentResponse
	if err := json.Unmarshal([]byte(payload), &response); err != nil {
		t.Fatalf("decode Gemini details-only usage JSON: %v", err)
	}
	if response.UsageMetadata == nil {
		t.Fatal("details-only Gemini usage JSON lost usage metadata")
	}

	measures := geminiNativeMeasures(response.UsageMetadata)
	var detailedAudio, scalarAggregate bool
	for _, measure := range measures {
		if measure.Key.Direction == sdkmetering.DirectionInput && measure.Key.Component == sdkmetering.ComponentAudioToken && dimensionValue(measure.Key, "evidence_plane") == "grounded_tool" {
			if measure.Value == nil || measure.Value.Coefficient != "23" {
				t.Fatalf("Gemini details-only AUDIO measure = %+v, want 23 tokens", measure)
			}
			detailedAudio = true
		}
		if measure.Key.Component == "grounded_tool_token" {
			scalarAggregate = true
		}
	}
	if !detailedAudio {
		t.Fatalf("Gemini details-only AUDIO evidence was lost: %+v", measures)
	}
	if scalarAggregate {
		t.Fatalf("Gemini details-only usage fabricated a zero grounded-tool aggregate: %+v", measures)
	}
}

func TestGeminiTotalFallbackDoesNotAttributeGroundedToolToOutput(t *testing.T) {
	t.Parallel()
	ev := usageEvent(&genai.GenerateContentResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: 11, ToolUsePromptTokenCount: 8, TotalTokenCount: 19,
	}})
	if ev == nil {
		t.Fatal("usage event is nil")
	}
	if ev.OutputTokens != 0 {
		t.Fatalf("grounded-tool tokens became output tokens: %+v", ev)
	}
}

func TestGeminiPreparedVideoPreservesDurationAndFramesWithoutInferringResolution(t *testing.T) {
	t.Parallel()
	fps := 24.0
	params := StreamParams{
		Contents: []*genai.Content{
			{
				Parts: []*genai.Part{
					{
						InlineData: &genai.Blob{Data: []byte("video-bytes"), MIMEType: "video/mp4"},
						VideoMetadata: &genai.VideoMetadata{
							StartOffset: 125 * time.Millisecond,
							EndOffset:   10*time.Second + 125*time.Millisecond,
							FPS:         &fps,
						},
						// Gemini exposes a tokenization hint, not pixel dimensions. It must
						// not be converted into a fabricated width/height qualifier.
						MediaResolution: &genai.PartMediaResolution{
							Level: genai.PartMediaResolutionLevelMediaResolutionHigh,
						},
					},
				},
			},
		},
	}

	var observed coremetering.PreparedInputSummary
	ctx := coremetering.WithPreparedInputObserver(context.Background(), func(summary coremetering.PreparedInputSummary) {
		observed = summary
	})
	observePreparedInput(ctx, params)
	if len(observed.Media) != 1 {
		t.Fatalf("prepared Gemini media = %#v, want one video", observed.Media)
	}
	media := observed.Media[0]
	if media.Kind != coremetering.MediaVideo || !media.BytesPresent || media.Bytes != int64(len("video-bytes")) {
		t.Fatalf("prepared Gemini video bytes/kind = %#v", media)
	}
	if !media.DurationPresent || media.DurationMillis != 10_000 {
		t.Fatalf("prepared Gemini video duration = %#v, want 10000ms", media)
	}
	if !media.FramesPresent || media.Frames != 240 {
		t.Fatalf("prepared Gemini video frames = %#v, want 240", media)
	}
	if media.WidthPresent || media.HeightPresent {
		t.Fatalf("Gemini resolution hint became fabricated dimensions: %#v", media)
	}
}

func TestGeminiPreparedMediaKindsRetainNativeBoundaryBytes(t *testing.T) {
	t.Parallel()
	params := StreamParams{Contents: []*genai.Content{{Parts: []*genai.Part{
		{InlineData: &genai.Blob{Data: []byte("image"), MIMEType: "image/png"}},
		{InlineData: &genai.Blob{Data: []byte("audio"), MIMEType: "audio/wav"}},
		{InlineData: &genai.Blob{Data: []byte("video"), MIMEType: "video/mp4"}},
		{InlineData: &genai.Blob{Data: []byte("document"), MIMEType: "application/pdf"}},
		{InlineData: &genai.Blob{Data: []byte("file"), MIMEType: "application/octet-stream"}},
	}}}}

	var observed coremetering.PreparedInputSummary
	ctx := coremetering.WithPreparedInputObserver(context.Background(), func(summary coremetering.PreparedInputSummary) {
		observed = summary
	})
	observePreparedInput(ctx, params)
	want := []struct {
		kind  coremetering.MediaKind
		bytes int64
	}{
		{coremetering.MediaImage, 5},
		{coremetering.MediaAudio, 5},
		{coremetering.MediaVideo, 5},
		{coremetering.MediaDocument, 8},
		{coremetering.MediaFile, 4},
	}
	if len(observed.Media) != len(want) {
		t.Fatalf("prepared Gemini media count = %d, want %d: %#v", len(observed.Media), len(want), observed.Media)
	}
	for i, expected := range want {
		media := observed.Media[i]
		if media.Kind != expected.kind || !media.BytesPresent || media.Bytes != expected.bytes || media.Count != 1 {
			t.Fatalf("prepared Gemini media[%d] = %#v, want %s/%d bytes", i, media, expected.kind, expected.bytes)
		}
	}
}

func TestGeminiOutputMediaReferencesDoNotInventUsageOrStorageCharges(t *testing.T) {
	t.Parallel()
	s := &genaiStream{ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer()}
	s.BindEconomicEvidence(coremetering.ObservationIdentity{StoreID: "store", BillingCallID: "billing", BLegID: "b-leg"})
	resp := &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
		{FileData: &genai.FileData{FileURI: "gs://bucket/generated.aac", MIMEType: "audio/aac"}},
		{FileData: &genai.FileData{FileURI: "gs://bucket/generated.mp4", MIMEType: "video/mp4"}},
	}}}}}
	if err := s.handleResponse(resp); err != nil {
		t.Fatalf("handle response: %v", err)
	}
	events := stream.DrainPending(&s.pending)
	seen := map[string]bool{}
	for _, event := range events {
		if event.Kind == aipapi.EventAssistantFileRef {
			seen[event.AssistantMIME] = true
		}
	}
	if !seen["audio/aac"] || !seen["video/mp4"] {
		t.Fatalf("Gemini output references lost modality MIME: events=%+v", events)
	}
	if observations := s.DrainEconomicObservations(); len(observations) != 0 {
		t.Fatalf("Gemini output URI invented provider usage/storage evidence: %+v", observations)
	}
}

func TestGeminiProviderEvidenceIsCumulativeTerminalCheckpointAndLateCorrection(t *testing.T) {
	t.Parallel()
	rawStream := newGenaiStream(func(yield func(*genai.GenerateContentResponse, error) bool) {
		yield(&genai.GenerateContentResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount: 12, CandidatesTokenCount: 4, TotalTokenCount: 16,
			PromptTokensDetails: []*genai.ModalityTokenCount{{Modality: genai.MediaModalityImage, TokenCount: 12}},
		}}, nil)
	}, "gemini", 0)
	s, ok := rawStream.(*genaiStream)
	if !ok {
		t.Fatalf("Gemini stream is %T, want *genaiStream", rawStream)
	}
	s.BindEconomicEvidence(coremetering.ObservationIdentity{
		StoreID: "store", RequestID: "request", CallID: "call", BillingCallID: "billing",
		ALegID: "a-leg", BLegID: "b-leg", AttemptID: "attempt", AttemptSeq: 1,
	})

	var sawFinished bool
	for {
		event, err := s.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("receive initial Gemini stream: %v", err)
		}
		if event.Kind == aipapi.EventResponseFinished {
			sawFinished = true
		}
	}
	if !sawFinished {
		t.Fatal("Gemini stream did not emit terminal response checkpoint")
	}
	first := s.DrainEconomicObservations()
	if len(first) != 1 || first[0].Revision != 1 || first[0].Semantics != sdkmetering.SemanticsCumulative {
		t.Fatalf("initial Gemini evidence = %+v", first)
	}
	if first[0].Subject.BLegID != "b-leg" || first[0].Subject.BillingCallID != "billing" {
		t.Fatalf("Gemini evidence lost trusted B-leg identity: %+v", first[0].Subject)
	}

	// A provider correction can arrive after execution terminal; it appends a
	// replacement revision while leaving execution terminal and B-leg identity
	// untouched.
	if err := s.handleResponse(&genai.GenerateContentResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: 13, CandidatesTokenCount: 4, TotalTokenCount: 17,
		PromptTokensDetails: []*genai.ModalityTokenCount{{Modality: genai.MediaModalityImage, TokenCount: 13}},
	}}); err != nil {
		t.Fatalf("handle late Gemini correction: %v", err)
	}
	late := s.DrainEconomicObservations()
	if len(late) != 1 || late[0].Revision != 2 || late[0].Semantics != sdkmetering.SemanticsReplacement {
		t.Fatalf("late Gemini correction = %+v", late)
	}
	if len(late[0].Supersedes) != 1 || late[0].Supersedes[0].ObservationID != first[0].ID || late[0].Subject.BLegID != "b-leg" {
		t.Fatalf("late Gemini correction lineage = %+v", late[0])
	}
	if err := s.handleResponse(&genai.GenerateContentResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: 13, CandidatesTokenCount: 4, TotalTokenCount: 17,
		PromptTokensDetails: []*genai.ModalityTokenCount{{Modality: genai.MediaModalityImage, TokenCount: 13}},
	}}); err != nil {
		t.Fatalf("handle repeated Gemini correction: %v", err)
	}
	if replay := s.DrainEconomicObservations(); len(replay) != 0 {
		t.Fatalf("unchanged late Gemini correction was not deduplicated: %+v", replay)
	}
}
