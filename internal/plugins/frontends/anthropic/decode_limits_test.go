package anthropic_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/limits"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDecodeMessage_limits(t *testing.T) {
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
		{name: "too many metadata entries", body: fmt.Sprintf(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"metadata":{%s}}`, repeatMetadata(limits.MaxMetadata+1))},
		{name: "oversized metadata session id", body: `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"metadata":{"` + sessionwire.MetaKeyAuthoritativeSessionID + `":"` + strings.Repeat("s", aipapi.MaxAuthoritativeSessionIDBytes+1) + `"}}`},
		{name: "oversized metadata resume token", body: `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"metadata":{"` + sessionwire.MetaKeyResumeToken + `":"` + strings.Repeat("r", aipapi.MaxResumeTokenBytes+1) + `"}}`},
		{name: "too many messages", body: fmt.Sprintf(`{"model":"m","max_tokens":1,"messages":[%s]}`, repeatObjects(limits.MaxMessages+1, `{"role":"user","content":"x"}`))},
		{name: "too many content blocks", body: fmt.Sprintf(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[%s]}]}`, repeatObjects(limits.MaxParts+1, `{"type":"text","text":"x"}`))},
		{name: "too many tools", body: fmt.Sprintf(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"tools":[%s]}`, repeatObjects(limits.MaxTools+1, `{"name":"f","input_schema":{}}`))},
		{name: "oversized input_schema", body: `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"tools":[{"name":"f","input_schema":` + oversizedSchema + `}]}`},
		{name: "oversized tool_use input", body: `{"model":"m","max_tokens":1,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"f","input":` + oversizedObject + `}]}]}`},
		{name: "oversized tool_result content", body: `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":` + oversizedObject + `}]}]}`},
		{name: "oversized image data", body: `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + strings.Repeat("a", limits.MaxBase64Data+1) + `"}}]}]}`},
		{name: "oversized document data", body: `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + strings.Repeat("a", limits.MaxBase64Data+1) + `"}}]}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := anthropic.DecodeMessageRequest([]byte(tt.body), anthropic.DecodeOptions{RouteSelector: "stub:m"})
			if err == nil {
				t.Fatal("expected limit error")
			}
		})
	}
}

func TestDecodeMessage_headersOverrideMetadata(t *testing.T) {
	t.Parallel()
	body := `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"metadata":{"` + sessionwire.MetaKeyAuthoritativeSessionID + `":"meta-sid","` + sessionwire.MetaKeyResumeToken + `":"meta-tok"}}`
	h := http.Header{}
	h.Set(sessionwire.HeaderAuthoritativeSessionID, "hdr-sid")
	h.Set(sessionwire.HeaderResumeToken, "hdr-tok")
	d, err := anthropic.DecodeMessageRequest([]byte(body), anthropic.DecodeOptions{RouteSelector: "stub:m", Headers: h})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Session.AuthoritativeSessionID != "hdr-sid" || d.Call.Session.ResumeToken != "hdr-tok" {
		t.Fatalf("session: %+v", d.Call.Session)
	}
}

func TestDecodeMessage_nonMapMetadataDoesNotSetSession(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "string", body: `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"metadata":"not-a-map"}`},
		{name: "array", body: `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"metadata":["not-a-map"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d, err := anthropic.DecodeMessageRequest([]byte(tt.body), anthropic.DecodeOptions{RouteSelector: "stub:m"})
			if err != nil {
				t.Fatal(err)
			}
			if d.Call.Session.AuthoritativeSessionID != "" || d.Call.Session.ResumeToken != "" {
				t.Fatalf("session: %+v", d.Call.Session)
			}
		})
	}
}

func TestDecodeMessage_validSmallLimitSurfaces(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(map[string]any{
		"model": "m", "max_tokens": 1,
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "x"},
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "YWJj"}},
		}}},
		"tools": []any{map[string]any{"name": "f", "input_schema": map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{RouteSelector: "stub:m"}); err != nil {
		t.Fatal(err)
	}
}
