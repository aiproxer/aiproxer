package openairesponses_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/limits"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDecodeCreate_limits(t *testing.T) {
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

	tests := []struct {
		name string
		body string
	}{
		{name: "too many metadata entries", body: fmt.Sprintf(`{"model":"m","input":"x","metadata":{%s}}`, repeatMetadata(limits.MaxMetadata+1))},
		{name: "oversized metadata session id", body: `{"model":"m","input":"x","metadata":{"` + sessionwire.MetaKeyAuthoritativeSessionID + `":"` + strings.Repeat("s", aipapi.MaxAuthoritativeSessionIDBytes+1) + `"}}`},
		{name: "oversized metadata resume token", body: `{"model":"m","input":"x","metadata":{"` + sessionwire.MetaKeyResumeToken + `":"` + strings.Repeat("r", aipapi.MaxResumeTokenBytes+1) + `"}}`},
		{name: "too many input items", body: fmt.Sprintf(`{"model":"m","input":[%s]}`, repeatObjects(limits.MaxMessages+1, `{"role":"user","content":"x"}`))},
		{name: "too many content blocks", body: fmt.Sprintf(`{"model":"m","input":[{"role":"user","content":[%s]}]}`, repeatObjects(limits.MaxParts+1, `{"type":"input_text","text":"x"}`))},
		{name: "too many tools", body: fmt.Sprintf(`{"model":"m","input":"x","tools":[%s]}`, repeatObjects(limits.MaxTools+1, `{"type":"function","name":"f","parameters":{}}`))},
		{name: "oversized tool parameters", body: `{"model":"m","input":"x","tools":[{"type":"function","name":"f","parameters":` + oversizedSchema + `}]}`},
		{name: "oversized function_call arguments", body: `{"model":"m","input":[{"type":"function_call","call_id":"c","name":"f","arguments":` + oversizedObject + `}]}`},
		{name: "oversized function_call_output output", body: `{"model":"m","input":[{"type":"function_call_output","call_id":"c","output":` + oversizedObject + `}]}`},
		{name: "oversized input_file file_data", body: `{"model":"m","input":[{"role":"user","content":[{"type":"input_file","filename":"a.txt","file_data":"` + strings.Repeat("a", limits.MaxBase64Data+1) + `"}]}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := openairesponses.DecodeCreateRequest([]byte(tt.body), openairesponses.DecodeOptions{RouteSelector: "stub:m"})
			if err == nil {
				t.Fatal("expected limit error")
			}
		})
	}
}

func TestDecodeCreate_headersOverrideMetadata(t *testing.T) {
	t.Parallel()
	body := `{"model":"m","input":"x","metadata":{"` + sessionwire.MetaKeyAuthoritativeSessionID + `":"meta-sid","` + sessionwire.MetaKeyResumeToken + `":"meta-tok"}}`
	h := http.Header{}
	h.Set(sessionwire.HeaderAuthoritativeSessionID, "hdr-sid")
	h.Set(sessionwire.HeaderResumeToken, "hdr-tok")
	d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{RouteSelector: "stub:m", Headers: h})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Session.AuthoritativeSessionID != "hdr-sid" || d.Call.Session.ResumeToken != "hdr-tok" {
		t.Fatalf("session: %+v", d.Call.Session)
	}
}

func TestDecodeCreate_validSmallLimitSurfaces(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(map[string]any{
		"model": "m",
		"input": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "x"},
				map[string]any{"type": "input_file", "filename": "a.txt", "file_data": "YWJj"},
			},
		}},
		"tools": []any{map[string]any{"type": "function", "name": "f", "parameters": map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:m"}); err != nil {
		t.Fatal(err)
	}
}
