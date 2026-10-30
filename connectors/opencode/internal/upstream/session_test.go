package upstream

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestResolveSessionID_CorrelationID(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID: "client-sess-123",
		},
	}
	got := resolveSessionID(call)
	if got != "client-sess-123" {
		t.Fatalf("got = %q want client-sess-123", got)
	}

	callAuth := aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID:        "client-sess-123",
			AuthoritativeSessionID: "auth-sess-456",
		},
	}
	gotAuth := resolveSessionID(callAuth)
	if gotAuth != "auth-sess-456" {
		t.Fatalf("gotAuth = %q want auth-sess-456", gotAuth)
	}
}

func TestResolveSessionID_Metadata(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Session: aipapi.SessionRef{
			Metadata: map[string]string{
				HeaderOpenCodeSession: "meta-sess-789",
			},
		},
	}
	got := resolveSessionID(call)
	if got != "meta-sess-789" {
		t.Fatalf("got = %q want meta-sess-789", got)
	}
}

func TestResolveSessionID_CallIDFallback(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "call-uuid-999",
	}
	got := resolveSessionID(call)
	if got != "call-uuid-999" {
		t.Fatalf("got = %q want call-uuid-999", got)
	}
}

func TestResolveSessionID_MintFallback(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{}
	got := resolveSessionID(call)
	if !strings.HasPrefix(got, "aip-") {
		t.Fatalf("expected fallback session id to start with 'aip-', got %q", got)
	}
	if len(got) < 10 {
		t.Fatalf("fallback session id too short: %q", got)
	}
}

func TestSanitizeOpenAIPayload_OmenAlpha(t *testing.T) {
	t.Parallel()
	body := map[string]any{
		"model":            "omen-alpha",
		"reasoning":        "deep",
		"reasoning_effort": "high",
		"thinking":         map[string]any{"type": "enabled"},
		"temperature":      0.7,
	}

	sanitizeOpenAIPayload(body, "omen-alpha")

	for _, k := range []string{"reasoning", "reasoning_effort", "thinking"} {
		if _, exists := body[k]; exists {
			t.Fatalf("field %q should have been deleted from payload", k)
		}
	}
	if body["temperature"] != 0.7 {
		t.Fatalf("temperature was unexpectedly modified: %v", body["temperature"])
	}
}

func TestSanitizeOpenAIPayload_OmenAlphaWithPrefix(t *testing.T) {
	t.Parallel()
	body := map[string]any{
		"model":            "opencode-go/omen-alpha",
		"reasoning_effort": "low",
		"messages":         []any{"hello"},
	}

	sanitizeOpenAIPayload(body, "opencode-go/omen-alpha")

	if _, exists := body["reasoning_effort"]; exists {
		t.Fatal("reasoning_effort should have been deleted for opencode-go/omen-alpha")
	}
	if body["messages"] == nil {
		t.Fatal("messages was unexpectedly modified")
	}
}

func TestSanitizeOpenAIPayload_GLMPrefix(t *testing.T) {
	t.Parallel()
	body := map[string]any{
		"model":            "glm-5.1",
		"reasoning":        "true",
		"reasoning_effort": "medium",
	}

	sanitizeOpenAIPayload(body, "glm-5.1")

	if _, exists := body["reasoning"]; exists {
		t.Fatal("reasoning should have been deleted for glm-5.1")
	}
	if _, exists := body["reasoning_effort"]; exists {
		t.Fatal("reasoning_effort should have been deleted for glm-5.1")
	}
}

func TestSanitizeOpenAIPayload_OtherModelsPreserved(t *testing.T) {
	t.Parallel()
	body := map[string]any{
		"model":            "kimi-k2.7-code",
		"reasoning_effort": "medium",
	}

	sanitizeOpenAIPayload(body, "kimi-k2.7-code")

	if body["reasoning_effort"] != "medium" {
		t.Fatalf("reasoning_effort should have been preserved for kimi-k2.7-code, got %v", body["reasoning_effort"])
	}
}
