package openrouterwire_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/openrouterwire"
)

func TestCaptureBodyFields_passthrough(t *testing.T) {
	t.Parallel()
	body := map[string]json.RawMessage{
		"provider":        json.RawMessage(`{"order":["OpenAI"]}`),
		"models":          json.RawMessage(`["openai/gpt-4o","anthropic/claude-3.5-sonnet"]`),
		"route":           json.RawMessage(`"fallback"`),
		"plugins":         json.RawMessage(`[{"id":"web"}]`),
		"prediction":      json.RawMessage(`{"type":"content","content":"hello"}`),
		"debug":           json.RawMessage(`true`),
		"service_tier":    json.RawMessage(`"default"`),
		"session_id":      json.RawMessage(`"sess-abc"`),
		"user":            json.RawMessage(`"user-123"`),
		"response_format": json.RawMessage(`{"type":"json_object"}`),
		"reasoning":       json.RawMessage(`{"effort":"high"}`),
		"model":           json.RawMessage(`"openai/gpt-4o"`),
		"messages":        json.RawMessage(`[]`),
	}
	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureBodyFields(body, ext)

	want := map[string]string{
		openrouterwire.ExtProvider:       `{"order":["OpenAI"]}`,
		openrouterwire.ExtModels:         `["openai/gpt-4o","anthropic/claude-3.5-sonnet"]`,
		openrouterwire.ExtRoute:          `"fallback"`,
		openrouterwire.ExtPlugins:        `[{"id":"web"}]`,
		openrouterwire.ExtPrediction:     `{"type":"content","content":"hello"}`,
		openrouterwire.ExtDebug:          `true`,
		openrouterwire.ExtServiceTier:    `"default"`,
		openrouterwire.ExtSessionID:      `"sess-abc"`,
		openrouterwire.ExtUser:           `"user-123"`,
		openrouterwire.ExtResponseFormat: `{"type":"json_object"}`,
		openrouterwire.ExtReasoning:      `{"effort":"high"}`,
	}
	for key, wantVal := range want {
		got, ok := ext[key]
		if !ok {
			t.Errorf("missing ext %q", key)
			continue
		}
		if string(got) != wantVal {
			t.Errorf("ext[%q] = %s, want %s", key, got, wantVal)
		}
	}

	if _, ok := ext["model"]; ok {
		t.Error("should not capture 'model' field (handled by standard decode)")
	}
	if _, ok := ext["messages"]; ok {
		t.Error("should not capture 'messages' field")
	}
}

func TestCaptureBodyFields_ignoresNull(t *testing.T) {
	t.Parallel()
	body := map[string]json.RawMessage{
		"provider": json.RawMessage(`null`),
		"route":    json.RawMessage(``),
	}
	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureBodyFields(body, ext)
	if len(ext) != 0 {
		t.Errorf("expected empty ext, got %d entries", len(ext))
	}
}

func TestCaptureHeaders(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("HTTP-Referer", "https://myapp.com")
	h.Set("X-OpenRouter-Title", "MyApp")
	h.Set("X-OpenRouter-Categories", "ai,chat")
	h.Set("X-OpenRouter-Metadata", `{"session":"abc"}`)

	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureHeaders(h, ext)

	if openrouterwire.GetString(ext, openrouterwire.ExtHTTPReferer) != "https://myapp.com" {
		t.Errorf("referer: %s", ext[openrouterwire.ExtHTTPReferer])
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtTitle) != "MyApp" {
		t.Errorf("title: %s", ext[openrouterwire.ExtTitle])
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtCategories) != "ai,chat" {
		t.Errorf("categories: %s", ext[openrouterwire.ExtCategories])
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtMetadataHeader) != `{"session":"abc"}` {
		t.Errorf("metadata: %s", ext[openrouterwire.ExtMetadataHeader])
	}
}

func TestCaptureHeaders_xTitleFallback(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("X-Title", "FallbackTitle")

	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureHeaders(h, ext)

	if openrouterwire.GetString(ext, openrouterwire.ExtTitle) != "FallbackTitle" {
		t.Errorf("title: %s", ext[openrouterwire.ExtTitle])
	}
}

func TestCaptureHeaders_xTitlePrefersFull(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("X-OpenRouter-Title", "Preferred")
	h.Set("X-Title", "Fallback")

	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureHeaders(h, ext)

	if openrouterwire.GetString(ext, openrouterwire.ExtTitle) != "Preferred" {
		t.Errorf("title: %s (expected Preferred over Fallback)", ext[openrouterwire.ExtTitle])
	}
}

func TestCaptureHeaders_blankModernTitleFallsBackToXTitle(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("X-OpenRouter-Title", "   ")
	h.Set("X-Title", "LegacyTitle")

	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureHeaders(h, ext)

	if openrouterwire.GetString(ext, openrouterwire.ExtTitle) != "LegacyTitle" {
		t.Errorf("title: %s (expected LegacyTitle when modern title blank)", ext[openrouterwire.ExtTitle])
	}
}

func TestCaptureHeaders_blankBothTitles(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("X-OpenRouter-Title", " ")
	h.Set("X-Title", "")

	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureHeaders(h, ext)

	if openrouterwire.GetString(ext, openrouterwire.ExtTitle) != "" {
		t.Errorf("expected no title, got %s", ext[openrouterwire.ExtTitle])
	}
}

func TestCaptureHeaders_rejectsInvalidRefererAndTitle(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("HTTP-Referer", "not-a-url")
	h.Set("X-OpenRouter-Title", "Bad\nTitle")
	h.Set("X-OpenRouter-Categories", "ai,chat")
	h.Set("X-OpenRouter-Metadata", `{"ok":true}`)

	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureHeaders(h, ext)

	if openrouterwire.GetString(ext, openrouterwire.ExtHTTPReferer) != "" {
		t.Fatalf("invalid referer must be omitted, got %q", openrouterwire.GetString(ext, openrouterwire.ExtHTTPReferer))
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtTitle) != "" {
		t.Fatalf("invalid title must be omitted, got %q", openrouterwire.GetString(ext, openrouterwire.ExtTitle))
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtCategories) != "ai,chat" {
		t.Fatalf("categories unchanged: %q", openrouterwire.GetString(ext, openrouterwire.ExtCategories))
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtMetadataHeader) != `{"ok":true}` {
		t.Fatalf("metadata unchanged: %q", openrouterwire.GetString(ext, openrouterwire.ExtMetadataHeader))
	}
}

func TestCaptureHeaders_invalidModernTitleFallsBackToValidXTitle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		modern string
		legacy string
		want   string
	}{
		{
			name:   "invalid_modern_valid_legacy",
			modern: "Bad\nTitle",
			legacy: "LegacyOK",
			want:   "LegacyOK",
		},
		{
			name:   "valid_modern_ignores_legacy",
			modern: "ModernOK",
			legacy: "LegacyIgnored",
			want:   "ModernOK",
		},
		{
			name:   "invalid_both_omit",
			modern: "Bad\nModern",
			legacy: "Bad\nLegacy",
			want:   "",
		},
		{
			name:   "overlong_modern_valid_legacy",
			modern: strings.Repeat("m", 257),
			legacy: "FromLegacy",
			want:   "FromLegacy",
		},
		{
			name:   "blank_modern_valid_legacy",
			modern: "   ",
			legacy: "BlankModernFallback",
			want:   "BlankModernFallback",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			if tc.modern != "" {
				h.Set("X-OpenRouter-Title", tc.modern)
			}
			if tc.legacy != "" {
				h.Set("X-Title", tc.legacy)
			}
			ext := make(map[string]json.RawMessage)
			openrouterwire.CaptureHeaders(h, ext)
			if got := openrouterwire.GetString(ext, openrouterwire.ExtTitle); got != tc.want {
				t.Fatalf("title=%q want %q", got, tc.want)
			}
		})
	}
}

func TestCaptureHeaders_rejectsOverlongAndControlReferer(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("HTTP-Referer", "https://example.com/\x00")
	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureHeaders(h, ext)
	if openrouterwire.GetString(ext, openrouterwire.ExtHTTPReferer) != "" {
		t.Fatal("control-char referer must be omitted")
	}
}

func TestCaptureHeaders_rejectsOverlongTitleFallback(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("X-Title", strings.Repeat("t", 257))
	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureHeaders(h, ext)
	if openrouterwire.GetString(ext, openrouterwire.ExtTitle) != "" {
		t.Fatal("overlong X-Title fallback must be omitted")
	}
}

func TestCaptureExtraBodyFields_capturesUnknown(t *testing.T) {
	t.Parallel()
	body := map[string]json.RawMessage{
		"model":                json.RawMessage(`"nvidia/test"`),
		"messages":             json.RawMessage(`[]`),
		"stream":               json.RawMessage(`true`),
		"chat_template_kwargs": json.RawMessage(`{"enable_thinking":true}`),
		"custom_field":         json.RawMessage(`42`),
		"provider":             json.RawMessage(`{"order":["NVIDIA"]}`),
		"temperature":          json.RawMessage(`0.7`),
	}
	known := map[string]bool{
		"model":       true,
		"messages":    true,
		"stream":      true,
		"temperature": true,
	}
	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureExtraBodyFields(body, ext, known)

	if string(ext[openrouterwire.ExtraBodyExtPrefix+"chat_template_kwargs"]) != `{"enable_thinking":true}` {
		t.Errorf("expected chat_template_kwargs, got %s", ext[openrouterwire.ExtraBodyExtPrefix+"chat_template_kwargs"])
	}
	if string(ext[openrouterwire.ExtraBodyExtPrefix+"custom_field"]) != `42` {
		t.Errorf("expected custom_field, got %s", ext[openrouterwire.ExtraBodyExtPrefix+"custom_field"])
	}
	if _, ok := ext[openrouterwire.ExtraBodyExtPrefix+"model"]; ok {
		t.Error("should not capture known field 'model'")
	}
	if _, ok := ext[openrouterwire.ExtraBodyExtPrefix+"messages"]; ok {
		t.Error("should not capture known field 'messages'")
	}
	if _, ok := ext[openrouterwire.ExtraBodyExtPrefix+"temperature"]; ok {
		t.Error("should not capture known field 'temperature'")
	}
	if _, ok := ext[openrouterwire.ExtraBodyExtPrefix+"provider"]; ok {
		t.Error("should not capture OpenRouter passthrough field 'provider'")
	}
}

func TestCaptureExtraBodyFields_ignoresNullAndEmpty(t *testing.T) {
	t.Parallel()
	body := map[string]json.RawMessage{
		"null_field":  json.RawMessage(`null`),
		"empty_field": json.RawMessage(``),
		"valid_field": json.RawMessage(`"hello"`),
	}
	known := map[string]bool{}
	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureExtraBodyFields(body, ext, known)

	if _, ok := ext[openrouterwire.ExtraBodyExtPrefix+"null_field"]; ok {
		t.Error("should not capture null field")
	}
	if _, ok := ext[openrouterwire.ExtraBodyExtPrefix+"empty_field"]; ok {
		t.Error("should not capture empty field")
	}
	if string(ext[openrouterwire.ExtraBodyExtPrefix+"valid_field"]) != `"hello"` {
		t.Errorf("expected valid_field, got %s", ext[openrouterwire.ExtraBodyExtPrefix+"valid_field"])
	}
}

func TestCaptureExtraBodyFields_nilExtDoesNotPanic(t *testing.T) {
	t.Parallel()
	body := map[string]json.RawMessage{"custom_field": json.RawMessage(`42`)}
	openrouterwire.CaptureExtraBodyFields(body, nil, nil)
}

func TestCaptureExtraBodyFields_rejectsUnsafeFieldNames(t *testing.T) {
	t.Parallel()
	body := map[string]json.RawMessage{
		"safe_field":          json.RawMessage(`"ok"`),
		"unsafe.nested":       json.RawMessage(`"no"`),
		"unsafe[bracket]":     json.RawMessage(`"no"`),
		"1_starts_with_digit": json.RawMessage(`"no"`),
	}
	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureExtraBodyFields(body, ext, nil)

	if string(ext[openrouterwire.ExtraBodyExtPrefix+"safe_field"]) != `"ok"` {
		t.Fatalf("safe_field = %s", ext[openrouterwire.ExtraBodyExtPrefix+"safe_field"])
	}
	for _, key := range []string{"unsafe.nested", "unsafe[bracket]", "1_starts_with_digit"} {
		if _, ok := ext[openrouterwire.ExtraBodyExtPrefix+key]; ok {
			t.Fatalf("captured unsafe extra body field %q", key)
		}
	}
}

func TestCaptureExtraBodyFields_boundsCapturedFields(t *testing.T) {
	t.Parallel()
	body := make(map[string]json.RawMessage, openrouterwire.MaxExtraBodyFields+1)
	for i := range openrouterwire.MaxExtraBodyFields + 1 {
		body["field_"+string(rune('a'+i))] = json.RawMessage(`true`)
	}
	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureExtraBodyFields(body, ext, nil)

	if len(ext) > openrouterwire.MaxExtraBodyFields {
		t.Fatalf("captured %d fields, want at most %d", len(ext), openrouterwire.MaxExtraBodyFields)
	}
}

func TestCaptureExtraBodyFields_rejectsOversizedValue(t *testing.T) {
	t.Parallel()
	tooLarge := make([]byte, openrouterwire.MaxExtraBodyFieldValueBytes+1)
	for i := range tooLarge {
		tooLarge[i] = 'x'
	}
	body := map[string]json.RawMessage{"safe_field": tooLarge}
	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureExtraBodyFields(body, ext, nil)

	if _, ok := ext[openrouterwire.ExtraBodyExtPrefix+"safe_field"]; ok {
		t.Fatal("captured oversized extra body value")
	}
}

func TestGetString_empty(t *testing.T) {
	t.Parallel()
	ext := map[string]json.RawMessage{}
	if got := openrouterwire.GetString(ext, "missing"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestGetRaw_nil(t *testing.T) {
	t.Parallel()
	ext := map[string]json.RawMessage{}
	if got := openrouterwire.GetRaw(ext, "missing"); got != nil {
		t.Errorf("expected nil, got %s", got)
	}
}
