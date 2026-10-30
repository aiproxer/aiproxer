package openresponses

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func contentMessage(role string, parts ...map[string]any) map[string]any {
	return map[string]any{
		"type":    "message",
		"role":    role,
		"content": parts,
	}
}

func TestDecode_InputFileAndInputVideoTyped(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"input": []any{
			contentMessage(
				"user",
				map[string]any{"type": "input_file", "file_url": "https://x/report.pdf", "filename": "report.pdf"},
				map[string]any{"type": "input_file", "file_data": "aGVsbG8=", "filename": "minimal.pdf"},
				map[string]any{"type": "input_video", "video_url": "https://x/v.mp4"},
			),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, call, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	content := call.Items[0].Content
	if len(content) != 3 {
		t.Fatalf("expected 3 content parts, got %d", len(content))
	}
	if content[0].Kind != aipapi.ContentPartFileRef ||
		content[0].FileRef != "https://x/report.pdf" || content[0].FileName != "report.pdf" {
		t.Fatalf("file_url part mismatch: %+v", content[0])
	}
	if content[1].Kind != aipapi.ContentPartFileRef ||
		content[1].FileData != "aGVsbG8=" || content[1].FileName != "minimal.pdf" {
		t.Fatalf("file_data part mismatch: %+v", content[1])
	}
	if content[2].Kind != aipapi.ContentPartVideoRef || content[2].VideoRef != "https://x/v.mp4" {
		t.Fatalf("input_video part mismatch: %+v", content[2])
	}
}

func TestContentPart_InputFileVideoLosslessRoundTrip(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Items: []aipapi.Item{{
			Kind:   aipapi.ItemKindMessage,
			ID:     "msg-1",
			Status: aipapi.ItemStatusCompleted,
			Role:   aipapi.RoleUser,
			Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartFileRef, FileRef: "https://x/report.pdf", FileMIME: "application/pdf", FileName: "report.pdf"},
				{Kind: aipapi.ContentPartFileRef, FileData: "aGVsbG8=", FileName: "minimal.pdf"},
				{Kind: aipapi.ContentPartVideoRef, VideoRef: "https://x/v.mp4", VideoMIME: "video/mp4"},
			},
		}},
	}
	encoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	lower := strings.ToLower(string(encoded))
	for _, marker := range []string{`"input_file"`, `"file_url":"https://x/report.pdf"`, `"file_data":"agvsbg8="`, `"input_video"`, `"video_url":"https://x/v.mp4"`} {
		if !strings.Contains(lower, marker) {
			t.Fatalf("encoded wire missing %q:\n%s", marker, encoded)
		}
	}
	_, reDecoded, err := DecodeRequest(encoded)
	if err != nil {
		t.Fatalf("DecodeRequest of re-encoded: %v", err)
	}
	got := reDecoded.Items[0].Content
	if got[0].FileRef != "https://x/report.pdf" || got[0].FileName != "report.pdf" {
		t.Fatalf("file_url round-trip mismatch: %+v", got[0])
	}
	if got[1].FileData != "aGVsbG8=" || got[1].FileName != "minimal.pdf" {
		t.Fatalf("file_data round-trip mismatch: %+v", got[1])
	}
	if got[2].VideoRef != "https://x/v.mp4" {
		t.Fatalf("input_video round-trip mismatch: %+v", got[2])
	}
}

func TestDecode_RejectsUnpinnedInputFileFileID(t *testing.T) {
	t.Parallel()
	// The pinned 2026-04-24 InputFileContentParam shape carries only filename,
	// file_data, and file_url. A non-null file_id is not part of the profile and
	// must be rejected before canonical construction instead of silently dropped.
	for _, payload := range []map[string]any{
		{"type": "input_file", "file_id": "file-abc"},
		{"type": "input_file", "file_id": "file-abc", "file_url": "https://x/report.pdf"},
		{"type": "input_file", "file_id": 123},
	} {
		body, err := json.Marshal(map[string]any{
			"model": "gpt-4o",
			"input": []any{contentMessage("user", payload)},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := DecodeRequest(body); err == nil {
			t.Fatalf("expected rejection of input_file file_id (payload %v)", payload)
		} else if !errors.Is(err, ErrDecodeFailed) {
			t.Fatalf("input_file file_id rejection must classify as decode failure, got %v", err)
		}
	}
}

func TestDecode_RejectsUnpinnedInputVideoVideoData(t *testing.T) {
	t.Parallel()
	// The pinned 2026-04-24 InputVideoContent shape carries only video_url. A
	// non-null video_data is not part of the profile and must be rejected before
	// canonical construction instead of silently dropped.
	for _, payload := range []map[string]any{
		{"type": "input_video", "video_data": "aGVsbG8="},
		{"type": "input_video", "video_data": "aGVsbG8=", "video_url": "https://x/v.mp4"},
		{"type": "input_video", "video_data": 42},
	} {
		body, err := json.Marshal(map[string]any{
			"model": "gpt-4o",
			"input": []any{contentMessage("user", payload)},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := DecodeRequest(body); err == nil {
			t.Fatalf("expected rejection of input_video video_data (payload %v)", payload)
		} else if !errors.Is(err, ErrDecodeFailed) {
			t.Fatalf("input_video video_data rejection must classify as decode failure, got %v", err)
		}
	}
}

func TestDecode_NullUnpinnedFieldsAccepted(t *testing.T) {
	t.Parallel()
	// Explicit null carries no data, so it is treated as absent per the codebase
	// jsonpresence convention. The pinned fields still round-trip losslessly.
	body, err := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"input": []any{contentMessage(
			"user",
			map[string]any{"type": "input_file", "file_id": nil, "file_url": "https://x/report.pdf", "filename": "report.pdf"},
			map[string]any{"type": "input_video", "video_data": nil, "video_url": "https://x/v.mp4"},
		)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, call, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	content := call.Items[0].Content
	if len(content) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(content))
	}
	if content[0].Kind != aipapi.ContentPartFileRef || content[0].FileRef != "https://x/report.pdf" {
		t.Fatalf("input_file part mismatch after null file_id: %+v", content[0])
	}
	if content[1].Kind != aipapi.ContentPartVideoRef || content[1].VideoRef != "https://x/v.mp4" {
		t.Fatalf("input_video part mismatch after null video_data: %+v", content[1])
	}
}

func TestDecode_PrefixedCustomContentPartPreservedNotStringified(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"input": []any{contentMessage(
			"user",
			map[string]any{"type": "acme:input_file", "file_url": "https://x/f", "meta": map[string]any{"k": 1}},
		)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, call, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	cp := call.Items[0].Content[0]
	if cp.Kind != aipapi.ContentPartExtension {
		t.Fatalf("expected opaque extension content part, got kind %q (text=%q)", cp.Kind, cp.Text)
	}
	if cp.Extension == nil || cp.Extension.Type != "acme:input_file" {
		t.Fatalf("extension type mismatch: %+v", cp.Extension)
	}
	if !json.Valid(cp.Extension.Data) {
		t.Fatalf("extension data must be valid structured JSON, got %q", cp.Extension.Data)
	}
	// The structured payload must be preserved (never flattened into a text
	// string): the meta object and file_url must be present verbatim.
	raw := string(cp.Extension.Data)
	if !strings.Contains(raw, `"meta":{"k":1}`) && !strings.Contains(raw, `"meta": {"k": 1}`) {
		t.Fatalf("extension structured payload lost: %s", raw)
	}
}

func TestContentPart_ExtensionContentPartLosslessRoundTrip(t *testing.T) {
	t.Parallel()
	wirePart := map[string]any{"type": "acme:input_file", "file_url": "https://x/f", "meta": map[string]any{"k": 1}}
	body, err := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"input": []any{contentMessage("user", wirePart)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, call, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	encoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var out struct {
		Input []struct {
			Content []json.RawMessage `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("decode re-encoded request: %v", err)
	}
	if len(out.Input) != 1 || len(out.Input[0].Content) != 1 {
		t.Fatalf("unexpected re-encoded shape: %s", encoded)
	}
	var rePart map[string]json.RawMessage
	if err := json.Unmarshal(out.Input[0].Content[0], &rePart); err != nil {
		t.Fatalf("re-encoded part not structured JSON: %v", err)
	}
	var typ string
	if err := json.Unmarshal(rePart["type"], &typ); err != nil || typ != "acme:input_file" {
		t.Fatalf("re-encoded part type: %q err=%v", typ, err)
	}
	if got := string(rePart["file_url"]); got != `"https://x/f"` {
		t.Fatalf("re-encoded file_url: %s", got)
	}
	if got := string(rePart["meta"]); got != `{"k":1}` {
		t.Fatalf("re-encoded meta payload: %s", got)
	}
}

func TestDecode_RejectsUnprefixedInventedContentDiscriminator(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"input": []any{contentMessage(
			"user",
			map[string]any{"type": "fancy_new_content_part", "value": 1},
		)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeRequest(body); err == nil {
		t.Fatal("expected rejection of unknown unprefixed content discriminator")
	}
}
