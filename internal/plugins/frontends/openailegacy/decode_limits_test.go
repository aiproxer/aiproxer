package openailegacy_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/limits"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDecodeChat_limits(t *testing.T) {
	t.Parallel()
	repeatObjects := func(n int, obj string) string { return strings.TrimSuffix(strings.Repeat(obj+",", n), ",") }
	repeatMetadata := func(n int) string {
		pairs := make([]string, 0, n)
		for i := range n {
			pairs = append(pairs, fmt.Sprintf(`"k%d":"v"`, i))
		}
		return strings.Join(pairs, ",")
	}
	oversizedObject := `{"x":"` + strings.Repeat("a", limits.MaxRawJSONPayload) + `"}`
	oversizedSchema := `{"x":"` + strings.Repeat("a", limits.MaxToolSchema) + `"}`

	tests := []struct{ name, body string }{
		{name: "too many metadata entries", body: fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"x"}],"metadata":{%s}}`, repeatMetadata(limits.MaxMetadata+1))},
		{name: "oversized metadata session id", body: `{"model":"m","messages":[{"role":"user","content":"x"}],"metadata":{"` + sessionwire.MetaKeyAuthoritativeSessionID + `":"` + strings.Repeat("s", aipapi.MaxAuthoritativeSessionIDBytes+1) + `"}}`},
		{name: "oversized metadata resume token", body: `{"model":"m","messages":[{"role":"user","content":"x"}],"metadata":{"` + sessionwire.MetaKeyResumeToken + `":"` + strings.Repeat("r", aipapi.MaxResumeTokenBytes+1) + `"}}`},
		{name: "too many messages", body: fmt.Sprintf(`{"model":"m","messages":[%s]}`, repeatObjects(limits.MaxMessages+1, `{"role":"user","content":"x"}`))},
		{name: "too many content blocks", body: fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[%s]}]}`, repeatObjects(limits.MaxParts+1, `{"type":"text","text":"x"}`))},
		{name: "too many tools", body: fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[%s]}`, repeatObjects(limits.MaxTools+1, `{"type":"function","function":{"name":"f","parameters":{}}}`))},
		{name: "oversized function parameters", body: `{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"f","parameters":` + oversizedSchema + `}}]}`},
		{name: "oversized tool_calls", body: `{"model":"m","messages":[{"role":"assistant","tool_calls":[` + oversizedObject + `]}]}`},
		{name: "oversized function_call", body: `{"model":"m","messages":[{"role":"assistant","function_call":` + oversizedObject + `}]}`},
		{name: "oversized file_data", body: `{"model":"m","messages":[{"role":"user","content":[{"type":"file","file":{"filename":"a.txt","file_data":"` + strings.Repeat("a", limits.MaxBase64Data+1) + `"}}]}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := openailegacy.DecodeChatRequest([]byte(tt.body), openailegacy.DecodeOptions{RouteSelector: "stub:m"})
			if err == nil {
				t.Fatal("expected limit error")
			}
		})
	}
}

func TestDecodeChat_headersOverrideMetadata(t *testing.T) {
	t.Parallel()
	body := `{"model":"m","messages":[{"role":"user","content":"x"}],"metadata":{"` + sessionwire.MetaKeyAuthoritativeSessionID + `":"meta-sid","` + sessionwire.MetaKeyResumeToken + `":"meta-tok"}}`
	h := http.Header{}
	h.Set(sessionwire.HeaderAuthoritativeSessionID, "hdr-sid")
	h.Set(sessionwire.HeaderResumeToken, "hdr-tok")
	d, err := openailegacy.DecodeChatRequest([]byte(body), openailegacy.DecodeOptions{RouteSelector: "stub:m", Headers: h})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Session.AuthoritativeSessionID != "hdr-sid" || d.Call.Session.ResumeToken != "hdr-tok" {
		t.Fatalf("session: %+v", d.Call.Session)
	}
}

func TestDecodeChat_validSmallLimitSurfaces(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(map[string]any{
		"model": "m",
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "x"},
			map[string]any{"type": "file", "file": map[string]any{"filename": "a.txt", "file_data": "YWJj"}},
		}}},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "f", "parameters": map[string]any{"type": "object"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:m"}); err != nil {
		t.Fatal(err)
	}
}
