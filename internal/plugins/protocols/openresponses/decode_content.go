package openresponses

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/jsonpresence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Standard content part type discriminators in official OpenResponses 2026-04-24.
var standardContentPartTypes = map[string]bool{
	"input_text":  true,
	"output_text": true,
	"text":        true,
	"input_image": true,
	"input_file":  true,
	"input_video": true,
	"refusal":     true,
}

// decodeExtensionContentPart binds identity only to the trusted wire
// discriminator. Payload metadata is opaque extension data, not identity.
func decodeExtensionContentPart(wireType string, raw []byte) *aipapi.ExtensionContentPart {
	ext := &aipapi.ExtensionContentPart{
		Namespace: aipapi.DeriveExtensionNamespace(wireType),
		Type:      wireType,
		Data:      cloneBytes(raw),
	}
	return ext
}

// decodeContentParts parses wire content array into canonical []aipapi.ContentPart.
func decodeContentParts(raw []byte) ([]aipapi.ContentPart, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}

	if len(trimmed) > 0 && trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, fmt.Errorf("%w: content string shorthand must be a valid string", ErrDecodeFailed)
		}
		return []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}}, nil
	}

	var rawParts []json.RawMessage
	if err := json.Unmarshal(trimmed, &rawParts); err != nil {
		return nil, fmt.Errorf("%w: content must be array of parts or a string", ErrDecodeFailed)
	}

	var parts []aipapi.ContentPart
	for i, rp := range rawParts {
		var wPart WireContentPart
		if err := json.Unmarshal(rp, &wPart); err != nil {
			return nil, fmt.Errorf("content[%d]: %w", i, err)
		}

		if !standardContentPartTypes[wPart.Type] && !isPrefixedType(wPart.Type) {
			return nil, fmt.Errorf("%w: content part %q", ErrUnknownDiscriminator, wPart.Type)
		}

		cp := aipapi.ContentPart{}
		switch wPart.Type {
		case "input_text", "output_text", "text":
			cp.Kind = aipapi.ContentPartText
			cp.Text = wPart.Text

		case "input_image":
			cp.Kind = aipapi.ContentPartImageRef
			if jsonpresence.IsPresentNonNullJSON(wPart.ImageURL) {
				trimmedImg := bytes.TrimSpace(wPart.ImageURL)
				if len(trimmedImg) > 0 && trimmedImg[0] == '"' {
					var imgStr string
					if err := json.Unmarshal(trimmedImg, &imgStr); err != nil {
						return nil, fmt.Errorf("content[%d]: %w: input_image image_url must be a valid string", i, ErrDecodeFailed)
					}
					cp.ImageRef = imgStr
				} else if len(trimmedImg) > 0 && trimmedImg[0] == '{' {
					var imgObj struct {
						URL string `json:"url"`
					}
					if err := json.Unmarshal(trimmedImg, &imgObj); err != nil {
						return nil, fmt.Errorf("content[%d]: %w: input_image image_url object is invalid", i, ErrDecodeFailed)
					}
					if imgObj.URL == "" {
						return nil, fmt.Errorf("content[%d]: %w: input_image image_url object is missing url", i, ErrDecodeFailed)
					}
					cp.ImageRef = imgObj.URL
				}
			}

		case "input_file":
			cp.Kind = aipapi.ContentPartFileRef
			if jsonpresence.IsPresentNonNullJSON(wPart.FileID) {
				// The pinned 2026-04-24 InputFileContentParam shape carries only
				// filename, file_data, and file_url. A non-null file_id cannot be
				// represented by the canonical file_ref part, so admitting it would
				// silently drop the file reference before the upstream backend.
				return nil, fmt.Errorf("content[%d]: %w: input_file field file_id is not part of the pinned 2026-04-24 profile", i, ErrDecodeFailed)
			}
			if jsonpresence.IsPresentNonNullJSON(wPart.FileURL) {
				var fileURL string
				if err := json.Unmarshal(wPart.FileURL, &fileURL); err != nil {
					return nil, fmt.Errorf("content[%d]: %w: input_file file_url must be a string", i, ErrDecodeFailed)
				}
				cp.FileRef = fileURL
			}
			if jsonpresence.IsPresentNonNullJSON(wPart.FileData) {
				var fileData string
				if err := json.Unmarshal(wPart.FileData, &fileData); err != nil {
					return nil, fmt.Errorf("content[%d]: %w: input_file file_data must be a string", i, ErrDecodeFailed)
				}
				cp.FileData = fileData
			}
			cp.FileName = wPart.Filename

		case "input_video":
			cp.Kind = aipapi.ContentPartVideoRef
			if jsonpresence.IsPresentNonNullJSON(wPart.VideoData) {
				// The pinned 2026-04-24 InputVideoContent shape carries only
				// video_url. A non-null video_data cannot be represented by the
				// canonical video_ref part, so admitting it would silently drop
				// the video data before the upstream backend.
				return nil, fmt.Errorf("content[%d]: %w: input_video field video_data is not part of the pinned 2026-04-24 profile", i, ErrDecodeFailed)
			}
			if jsonpresence.IsPresentNonNullJSON(wPart.VideoURL) {
				var videoURL string
				if err := json.Unmarshal(wPart.VideoURL, &videoURL); err != nil {
					return nil, fmt.Errorf("content[%d]: %w: input_video video_url must be a string", i, ErrDecodeFailed)
				}
				cp.VideoRef = videoURL
			}

		case "refusal":
			cp.Kind = aipapi.ContentPartRefusal
			cp.Refusal = wPart.Refusal

		default:
			// Vendor-prefixed custom content part: preserve the bounded
			// structured payload opaquely. It is never stringified to text.
			cp.Kind = aipapi.ContentPartExtension
			cp.Extension = decodeExtensionContentPart(wPart.Type, rp)
		}

		parts = append(parts, cp)
	}

	return parts, nil
}
