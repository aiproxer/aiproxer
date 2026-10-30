package geminigenerate

import (
	"context"
	"math"
	"strings"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"google.golang.org/genai"
)

const geminiPreparedInputMethod = "adapter:geminigenerate.final_provider_payload.v1"

// observePreparedInput walks the final genai parameter tree after canonical
// translation (including base64 decoding and role/default insertion).  It
// records bounded properties directly and never marshals the provider body.
func observePreparedInput(ctx context.Context, params StreamParams) {
	if !coremetering.PreparedInputObservationEnabled(ctx) {
		return
	}
	builder := coremetering.NewPreparedInputSummaryBuilder(geminiPreparedInputMethod)
	if params.Config != nil {
		observeGenAIContent(&builder, params.Config.SystemInstruction)
		for _, tool := range params.Config.Tools {
			for _, declaration := range tool.FunctionDeclarations {
				if declaration == nil {
					continue
				}
				builder.AddText(declaration.Name)
				builder.AddText(declaration.Description)
			}
		}
	}
	for _, content := range params.Contents {
		observeGenAIContent(&builder, content)
	}
	coremetering.ObservePreparedInput(ctx, builder.Build())
}

func observeGenAIContent(builder *coremetering.PreparedInputSummaryBuilder, content *genai.Content) {
	if builder == nil || content == nil {
		return
	}
	for _, part := range content.Parts {
		observeGenAIPart(builder, part)
	}
}

func observeGenAIPart(builder *coremetering.PreparedInputSummaryBuilder, part *genai.Part) {
	if builder == nil || part == nil {
		return
	}
	// Thought text is provider reasoning, not customer-visible input.  Do not
	// turn an opaque/hidden reasoning field into a local token claim.
	if !part.Thought {
		builder.AddText(part.Text)
	}
	if part.InlineData != nil {
		builder.AddMedia(genAIMediaSummary(part.InlineData.MIMEType, int64(len(part.InlineData.Data)), true, part.VideoMetadata))
	} else if part.FileData != nil {
		builder.AddMedia(genAIMediaSummary(part.FileData.MIMEType, 0, false, part.VideoMetadata))
	}
	if part.FunctionCall != nil {
		builder.AddText(part.FunctionCall.Name)
	}
	if part.FunctionResponse != nil {
		builder.AddText(part.FunctionResponse.Name)
	}
	if part.ExecutableCode != nil {
		builder.AddText(part.ExecutableCode.Code)
	}
	if part.CodeExecutionResult != nil {
		builder.AddText(part.CodeExecutionResult.Output)
	}
	// Server-side tool arguments/results are structured maps.  Their exact
	// provider units remain unavailable; names are still bounded visible facts.
	if part.ToolCall != nil {
		builder.AddText(string(part.ToolCall.ToolType))
	}
	if part.ToolResponse != nil {
		builder.AddText(string(part.ToolResponse.ToolType))
	}
}

func genAIMediaSummary(mime string, bytes int64, bytesPresent bool, video *genai.VideoMetadata) coremetering.MediaSummary {
	media := coremetering.MediaSummary{Kind: genAIMediaKind(mime), Count: 1, MIME: mime, Bytes: bytes, BytesPresent: bytesPresent}
	if video == nil {
		return media
	}
	if video.EndOffset > video.StartOffset {
		duration := video.EndOffset - video.StartOffset
		if millis := duration.Milliseconds(); millis >= 0 {
			media.DurationMillis = millis
			media.DurationPresent = true
			if video.FPS != nil && *video.FPS > 0 && !math.IsNaN(*video.FPS) && !math.IsInf(*video.FPS, 0) {
				frames := duration.Seconds() * *video.FPS
				if frames >= 0 && frames <= float64(1<<60) {
					media.Frames = int64(frames)
					media.FramesPresent = true
				}
			}
		}
	}
	return media
}

func genAIMediaKind(mime string) coremetering.MediaKind {
	switch {
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "image/"):
		return coremetering.MediaImage
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "audio/"):
		return coremetering.MediaAudio
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "video/"):
		return coremetering.MediaVideo
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "text/"), strings.EqualFold(strings.TrimSpace(mime), "application/pdf"):
		return coremetering.MediaDocument
	default:
		return coremetering.MediaFile
	}
}
