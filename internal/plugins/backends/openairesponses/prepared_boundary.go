package openairesponses

import (
	"context"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/openai/openai-go/v3/responses"
)

const openAIResponsesPreparedInputMethod = "adapter:openairesponses.final_provider_payload.v1"

// observePreparedInput walks the already-built Responses SDK parameter tree at
// the final adapter boundary. It includes adapter defaults and transforms by
// reading provider fields directly, without marshaling or retaining payloads.
func observePreparedInput(ctx context.Context, params responses.ResponseNewParams) {
	if !coremetering.PreparedInputObservationEnabled(ctx) {
		return
	}
	builder := coremetering.NewPreparedInputSummaryBuilder(openAIResponsesPreparedInputMethod)
	if params.Instructions.Valid() {
		builder.AddText(params.Instructions.Value)
	}
	if params.Input.OfString.Valid() {
		builder.AddText(params.Input.OfString.Value)
	}
	for _, item := range params.Input.OfInputItemList {
		observeResponseInputItem(&builder, item)
	}
	for _, tool := range params.Tools {
		if name := tool.GetName(); name != nil {
			builder.AddText(*name)
		}
		if description := tool.GetDescription(); description != nil {
			builder.AddText(*description)
		}
	}
	coremetering.ObservePreparedInput(ctx, builder.Build())
}

func observeResponseInputItem(builder *coremetering.PreparedInputSummaryBuilder, item responses.ResponseInputItemUnionParam) {
	if builder == nil {
		return
	}
	if message := item.OfMessage; message != nil {
		if message.Content.OfString.Valid() {
			builder.AddText(message.Content.OfString.Value)
		}
		for _, content := range message.Content.OfInputItemContentList {
			observeResponseInputContent(builder, content)
		}
	}
	if message := item.OfInputMessage; message != nil {
		for _, content := range message.Content {
			observeResponseInputContent(builder, content)
		}
	}
	if message := item.OfOutputMessage; message != nil {
		for _, content := range message.Content {
			if text := content.GetText(); text != nil {
				builder.AddText(*text)
			}
			if refusal := content.GetRefusal(); refusal != nil {
				builder.AddText(*refusal)
			}
		}
	}
	if call := item.OfFunctionCall; call != nil {
		builder.AddText(call.Name)
		builder.AddText(call.Arguments)
	}
	if call := item.OfCustomToolCall; call != nil {
		builder.AddText(call.Name)
		builder.AddText(call.Input)
	}
	if output := item.OfFunctionCallOutput; output != nil && output.Output.OfString.Valid() {
		builder.AddText(output.Output.OfString.Value)
	}
	// Reasoning items intentionally contribute no text here. Hidden reasoning
	// remains an explicit unavailable local economic field, even when an opaque
	// replay envelope is present in the SDK params.
}

func observeResponseInputContent(builder *coremetering.PreparedInputSummaryBuilder, content responses.ResponseInputContentUnionParam) {
	if builder == nil {
		return
	}
	if text := content.GetText(); text != nil {
		builder.AddText(*text)
	}
	if imageURL := content.GetImageURL(); imageURL != nil {
		builder.AddMedia(coremetering.MediaSummary{Kind: coremetering.MediaImage, Count: 1, MIME: "image"})
	}
	if fileData := content.GetFileData(); fileData != nil {
		media := coremetering.MediaSummary{Kind: coremetering.MediaDocument, Count: 1, MIME: "application/octet-stream", Bytes: int64(len(*fileData)), BytesPresent: true}
		builder.AddMedia(media)
	}
	if fileURL := content.GetFileURL(); fileURL != nil {
		builder.AddMedia(coremetering.MediaSummary{Kind: coremetering.MediaDocument, Count: 1, MIME: "application/octet-stream"})
	}
}
