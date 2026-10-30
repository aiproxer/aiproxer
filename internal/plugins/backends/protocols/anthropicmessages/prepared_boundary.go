package anthropicmessages

import (
	"context"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/anthropics/anthropic-sdk-go"
)

const anthropicPreparedInputMethod = "adapter:anthropicmessages.final_provider_payload.v1"

// observePreparedInput walks the final Anthropic SDK parameter tree.  The
// adapter has already normalized roles, merged messages, decoded no payloads,
// and applied its defaults at this point.  Reading the tree directly keeps
// accounting bounded and avoids a payload-sized marshal/copy.
func observePreparedInput(ctx context.Context, params anthropic.MessageNewParams) {
	if !coremetering.PreparedInputObservationEnabled(ctx) {
		return
	}
	builder := coremetering.NewPreparedInputSummaryBuilder(anthropicPreparedInputMethod)
	for _, block := range params.System {
		builder.AddText(block.Text)
	}
	for _, message := range params.Messages {
		for _, block := range message.Content {
			observeAnthropicContentBlock(&builder, block)
		}
	}
	for _, tool := range params.Tools {
		if tool.OfTool == nil {
			continue
		}
		builder.AddText(tool.OfTool.Name)
		if tool.OfTool.Description.Valid() {
			builder.AddText(tool.OfTool.Description.Value)
		}
	}
	coremetering.ObservePreparedInput(ctx, builder.Build())
}

func observeAnthropicContentBlock(builder *coremetering.PreparedInputSummaryBuilder, block anthropic.ContentBlockParamUnion) {
	if builder == nil {
		return
	}
	switch {
	case block.OfText != nil:
		builder.AddText(block.OfText.Text)
	case block.OfImage != nil:
		builder.AddMedia(anthropicMediaSummary(coremetering.MediaImage, block.OfImage.Source.GetData(), block.OfImage.Source.GetMediaType()))
	case block.OfDocument != nil:
		builder.AddMedia(anthropicMediaSummary(coremetering.MediaDocument, block.OfDocument.Source.GetData(), block.OfDocument.Source.GetMediaType()))
		observeAnthropicDocumentContent(builder, block.OfDocument.Source.OfContent)
	case block.OfToolUse != nil:
		// Tool input is provider-visible but may be an arbitrary object.  Keep
		// its name bounded and leave exact tool-query economics unavailable.
		builder.AddText(block.OfToolUse.Name)
	case block.OfToolResult != nil:
		for _, content := range block.OfToolResult.Content {
			observeAnthropicToolResultContent(builder, content)
		}
	case block.OfSearchResult != nil:
		for _, content := range block.OfSearchResult.Content {
			builder.AddText(content.Text)
		}
		// Thinking and redacted-thinking blocks intentionally contribute no text:
		// hidden reasoning remains unavailable local evidence.
	}
}

func observeAnthropicToolResultContent(builder *coremetering.PreparedInputSummaryBuilder, content anthropic.ToolResultBlockParamContentUnion) {
	if builder == nil {
		return
	}
	switch {
	case content.OfText != nil:
		builder.AddText(content.OfText.Text)
	case content.OfImage != nil:
		builder.AddMedia(anthropicMediaSummary(coremetering.MediaImage, content.OfImage.Source.GetData(), content.OfImage.Source.GetMediaType()))
	case content.OfDocument != nil:
		builder.AddMedia(anthropicMediaSummary(coremetering.MediaDocument, content.OfDocument.Source.GetData(), content.OfDocument.Source.GetMediaType()))
		observeAnthropicDocumentContent(builder, content.OfDocument.Source.OfContent)
	case content.OfSearchResult != nil:
		for _, item := range content.OfSearchResult.Content {
			builder.AddText(item.Text)
		}
	}
}

func observeAnthropicDocumentContent(builder *coremetering.PreparedInputSummaryBuilder, source *anthropic.ContentBlockSourceParam) {
	if builder == nil || source == nil {
		return
	}
	content := &source.Content
	if content.OfString.Valid() {
		builder.AddText(content.OfString.Value)
	}
	for _, item := range content.OfContentBlockSourceContent {
		if item.OfText != nil {
			builder.AddText(item.OfText.Text)
		}
		if item.OfImage != nil {
			builder.AddMedia(anthropicMediaSummary(coremetering.MediaImage, item.OfImage.Source.GetData(), item.OfImage.Source.GetMediaType()))
		}
	}
}

func anthropicMediaSummary(kind coremetering.MediaKind, data, mime *string) coremetering.MediaSummary {
	media := coremetering.MediaSummary{Kind: kind, Count: 1}
	if data != nil {
		media.Bytes = int64(len(*data))
		media.BytesPresent = true
	}
	if mime != nil {
		media.MIME = *mime
	}
	return media
}
