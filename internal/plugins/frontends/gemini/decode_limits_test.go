package gemini_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/limits"
)

func TestDecodeGenerateContent_limits(t *testing.T) {
	t.Parallel()
	repeatObjects := func(n int, obj string) string { return strings.TrimSuffix(strings.Repeat(obj+",", n), ",") }
	oversizedObject := `{"x":"` + strings.Repeat("a", limits.MaxRawJSONPayload) + `"}`
	oversizedSchema := `{"x":"` + strings.Repeat("a", limits.MaxToolSchema) + `"}`

	tests := []struct{ name, body string }{
		{name: "too many contents", body: fmt.Sprintf(`{"contents":[%s]}`, repeatObjects(limits.MaxMessages+1, `{"role":"user","parts":[{"text":"x"}]}`))},
		{name: "too many parts", body: fmt.Sprintf(`{"contents":[{"role":"user","parts":[%s]}]}`, repeatObjects(limits.MaxParts+1, `{"text":"x"}`))},
		{name: "too many tools", body: fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"text":"x"}]}],"tools":[%s]}`, repeatObjects(limits.MaxTools+1, `{"functionDeclarations":[]}`))},
		{name: "too many functionDeclarations", body: fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"text":"x"}]}],"tools":[{"functionDeclarations":[%s]}]}`, repeatObjects(limits.MaxTools+1, `{"name":"f","parameters":{}}`))},
		{name: "oversized parameters", body: `{"contents":[{"role":"user","parts":[{"text":"x"}]}],"tools":[{"functionDeclarations":[{"name":"f","parameters":` + oversizedSchema + `}]}]}`},
		{name: "oversized functionCall args", body: `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f","args":` + oversizedObject + `}}]}]}`},
		{name: "oversized functionResponse response", body: `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"f","response":` + oversizedObject + `}}]}]}`},
		{name: "oversized inlineData data", body: `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"` + strings.Repeat("a", limits.MaxBase64Data+1) + `"}}]}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := gemini.DecodeGenerateContentRequest([]byte(tt.body), gemini.DecodeOptions{RouteSelector: "stub:m", Model: "m"})
			if err == nil {
				t.Fatal("expected limit error")
			}
		})
	}
}

func TestDecodeGenerateContent_validSmallLimitSurfaces(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{
			map[string]any{"text": "x"},
			map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "YWJj"}},
		}}},
		"tools": []any{map[string]any{"functionDeclarations": []any{map[string]any{"name": "f", "parameters": map[string]any{"type": "object"}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{RouteSelector: "stub:m", Model: "m"}); err != nil {
		t.Fatal(err)
	}
}
