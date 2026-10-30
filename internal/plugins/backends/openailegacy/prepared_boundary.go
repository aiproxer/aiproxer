package openailegacy

import (
	"context"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/openai/openai-go/v3"
)

const openAILegacyPreparedInputMethod = "adapter:openailegacy.final_provider_payload.v1"

// observePreparedInput reports the SDK parameter tree after canonical
// translation and adapter rewrites. It deliberately walks provider fields
// directly; marshaling a request body just to meter it would create a
// payload-sized accounting allocation.
func observePreparedInput(ctx context.Context, params openai.ChatCompletionNewParams) {
	if !coremetering.PreparedInputObservationEnabled(ctx) {
		return
	}
	builder := coremetering.NewPreparedInputSummaryBuilder(openAILegacyPreparedInputMethod)
	for _, message := range params.Messages {
		observeChatMessage(&builder, message)
	}
	for _, tool := range params.Tools {
		if function := tool.GetFunction(); function != nil {
			builder.AddText(function.Name)
			if function.Description.Valid() {
				builder.AddText(function.Description.Value)
			}
			continue
		}
		if custom := tool.GetCustom(); custom != nil {
			builder.AddText(custom.Name)
			if custom.Description.Valid() {
				builder.AddText(custom.Description.Value)
			}
		}
	}
	coremetering.ObservePreparedInput(ctx, builder.Build())
}

func observeChatMessage(builder *coremetering.PreparedInputSummaryBuilder, message openai.ChatCompletionMessageParamUnion) {
	if builder == nil {
		return
	}
	content := message.GetContent().AsAny()
	switch value := content.(type) {
	case *string:
		if value != nil {
			builder.AddText(*value)
		}
	case *[]openai.ChatCompletionContentPartTextParam:
		if value != nil {
			for _, part := range *value {
				builder.AddText(part.Text)
			}
		}
	case *[]openai.ChatCompletionContentPartUnionParam:
		if value != nil {
			for _, part := range *value {
				observeChatContentPart(builder, part)
			}
		}
	case *[]openai.ChatCompletionAssistantMessageParamContentArrayOfContentPartUnion:
		if value != nil {
			for _, part := range *value {
				if text := part.GetText(); text != nil {
					builder.AddText(*text)
				}
				if refusal := part.GetRefusal(); refusal != nil {
					builder.AddText(*refusal)
				}
			}
		}
	}
	if assistant := message.OfAssistant; assistant != nil {
		if assistant.Refusal.Valid() {
			builder.AddText(assistant.Refusal.Value)
		}
		if assistant.Audio.ID != "" {
			builder.AddMedia(coremetering.MediaSummary{Kind: coremetering.MediaAudio, Count: 1, MIME: "audio"})
		}
		//nolint:staticcheck // legacy function_call wire field is still observed for usage evidence on received payloads; the SDK deprecation targets new sends, not reading provider responses
		if assistant.FunctionCall.Arguments != "" {
			builder.AddText(assistant.FunctionCall.Arguments)
		}
		for _, toolCall := range assistant.ToolCalls {
			if toolCall.OfFunction != nil {
				builder.AddText(toolCall.OfFunction.Function.Name)
				builder.AddText(toolCall.OfFunction.Function.Arguments)
			}
		}
	}
}

func observeChatContentPart(builder *coremetering.PreparedInputSummaryBuilder, part openai.ChatCompletionContentPartUnionParam) {
	if builder == nil {
		return
	}
	if text := part.GetText(); text != nil {
		builder.AddText(*text)
	}
	if image := part.GetImageURL(); image != nil {
		builder.AddMedia(coremetering.MediaSummary{Kind: coremetering.MediaImage, Count: 1, MIME: "image"})
	}
	if audio := part.GetInputAudio(); audio != nil {
		media := coremetering.MediaSummary{Kind: coremetering.MediaAudio, Count: 1, MIME: "audio/" + audio.Format}
		if audio.Data != "" {
			media.Bytes, media.BytesPresent = int64(len(audio.Data)), true
		}
		builder.AddMedia(media)
	}
	if file := part.GetFile(); file != nil {
		media := coremetering.MediaSummary{Kind: coremetering.MediaDocument, Count: 1, MIME: "application/octet-stream"}
		if file.FileData.Valid() {
			media.Bytes, media.BytesPresent = int64(len(file.FileData.Value)), true
		}
		if file.Filename.Valid() {
			builder.AddText(file.Filename.Value)
		}
		builder.AddMedia(media)
	}
}
