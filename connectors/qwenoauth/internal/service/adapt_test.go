package service_test

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/qwenoauth/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

var uuidRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestAdaptRequestBody_Golden(t *testing.T) {
	initialBodyJSON := `{
		"model": "qwen-coder-plus",
		"stream": true,
		"messages": [
			{
				"role": "system",
				"content": "You are a helpful coding assistant."
			},
			{
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "Analyze this screenshot:"
					},
					{
						"type": "image_url",
						"image_url": {
							"url": "https://example.com/screenshot.png",
							"detail": "high"
						}
					}
				]
			},
			{
				"role": "assistant",
				"content": "I see the code editor."
			}
		]
	}`

	var body map[string]any
	if err := json.Unmarshal([]byte(initialBodyJSON), &body); err != nil {
		t.Fatalf("unmarshal initial body: %v", err)
	}

	inv := backendplugin.Invocation{
		RequestID:           "req-prompt-uuid-987",
		ProxyOwnedSessionID: "sess_proxy_12345",
		SafeMetadata: map[string]string{
			"client_app": "aip-tester", // unrelated metadata key — must NOT be in top-level metadata
		},
	}
	call := aipapi.Call{}

	if err := service.AdaptRequestBody(body, inv, call); err != nil {
		t.Fatalf("AdaptRequestBody failed: %v", err)
	}

	// 1. Check top-level vl_high_resolution_images
	vlHighRes, ok := body["vl_high_resolution_images"].(bool)
	if !ok || !vlHighRes {
		t.Fatalf("expected vl_high_resolution_images: true, got %#v", body["vl_high_resolution_images"])
	}

	// 2. Check top-level metadata: must have camelCase sessionId and promptId ONLY
	metadata, ok := body["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected top-level metadata map, got %#v", body["metadata"])
	}
	if metadata["sessionId"] != "sess_proxy_12345" {
		t.Fatalf("expected metadata sessionId 'sess_proxy_12345', got %#v", metadata["sessionId"])
	}
	if metadata["promptId"] != "req-prompt-uuid-987" {
		t.Fatalf("expected metadata promptId 'req-prompt-uuid-987', got %#v", metadata["promptId"])
	}

	// Hard negative: must NOT contain snake_case session_id or arbitrary safe metadata
	if _, hasSnake := metadata["session_id"]; hasSnake {
		t.Fatalf("metadata must NOT contain snake_case session_id: %#v", metadata)
	}
	if _, hasUnrelated := metadata["client_app"]; hasUnrelated {
		t.Fatalf("metadata must NOT contain unrelated SafeMetadata: %#v", metadata)
	}
	if len(metadata) != 2 {
		t.Fatalf("expected exactly 2 metadata keys (sessionId, promptId), got %d: %#v", len(metadata), metadata)
	}

	// 3. Check messages
	msgs, ok := body["messages"].([]map[string]any)
	if !ok {
		t.Fatalf("expected messages to be []map[string]any, got %T", body["messages"])
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	// Message 0: system message string normalized to list of parts AND cache_control injected on last part
	sysParts, ok := msgs[0]["content"].([]map[string]any)
	if !ok || len(sysParts) != 1 {
		t.Fatalf("expected system message content to be 1-element slice, got %#v", msgs[0]["content"])
	}
	if sysParts[0]["type"] != "text" || sysParts[0]["text"] != "You are a helpful coding assistant." {
		t.Fatalf("unexpected system text part: %#v", sysParts[0])
	}
	cacheControl, ok := sysParts[0]["cache_control"].(map[string]any)
	if !ok || cacheControl["type"] != "ephemeral" {
		t.Fatalf("expected system last part to have cache_control: {type: ephemeral}, got %#v", sysParts[0]["cache_control"])
	}

	// Message 1: user message with text + image_url preserved
	userParts, ok := msgs[1]["content"].([]map[string]any)
	if !ok || len(userParts) != 2 {
		t.Fatalf("expected user message content to have 2 parts, got %#v", msgs[1]["content"])
	}
	if userParts[0]["type"] != "text" || userParts[0]["text"] != "Analyze this screenshot:" {
		t.Fatalf("unexpected user part 0: %#v", userParts[0])
	}
	if userParts[1]["type"] != "image_url" {
		t.Fatalf("expected user part 1 type image_url, got %#v", userParts[1]["type"])
	}
	imgURL, ok := userParts[1]["image_url"].(map[string]any)
	if !ok || imgURL["url"] != "https://example.com/screenshot.png" {
		t.Fatalf("image_url object not preserved: %#v", userParts[1]["image_url"])
	}
	if imgURL["detail"] != "high" {
		t.Fatalf("image_url detail not preserved: %#v", imgURL["detail"])
	}

	// Message 2: assistant message string normalized to typed text part
	astParts, ok := msgs[2]["content"].([]map[string]any)
	if !ok || len(astParts) != 1 {
		t.Fatalf("expected assistant message content to be 1-element slice, got %#v", msgs[2]["content"])
	}
	if astParts[0]["type"] != "text" || astParts[0]["text"] != "I see the code editor." {
		t.Fatalf("unexpected assistant part: %#v", astParts[0])
	}
	if _, hasCache := astParts[0]["cache_control"]; hasCache {
		t.Fatalf("assistant message should not have cache_control: %#v", astParts[0])
	}
}

func TestAdaptRequestBody_GeneratesPromptUUIDWhenMissing(t *testing.T) {
	initialBody := map[string]any{
		"model": "qwen-max",
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "Hello world",
			},
		},
	}
	inv := backendplugin.Invocation{
		ProxyOwnedSessionID: "sess_test",
	}
	call := aipapi.Call{}
	if err := service.AdaptRequestBody(initialBody, inv, call); err != nil {
		t.Fatalf("AdaptRequestBody: %v", err)
	}

	meta, ok := initialBody["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata map, got %#v", initialBody["metadata"])
	}
	if meta["sessionId"] != "sess_test" {
		t.Fatalf("expected sessionId 'sess_test', got %#v", meta["sessionId"])
	}
	promptID, ok := meta["promptId"].(string)
	if !ok || !uuidRegex.MatchString(promptID) {
		t.Fatalf("expected valid UUID for promptId when omitted, got %#v", meta["promptId"])
	}
}

func TestAdaptRequestBody_NoSystemMessage(t *testing.T) {
	initialBody := map[string]any{
		"model": "qwen-max",
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "Hello world",
			},
		},
	}
	inv := backendplugin.Invocation{}
	call := aipapi.Call{}
	if err := service.AdaptRequestBody(initialBody, inv, call); err != nil {
		t.Fatalf("AdaptRequestBody: %v", err)
	}

	msgs, ok := initialBody["messages"].([]map[string]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %#v", initialBody["messages"])
	}
	parts, ok := msgs[0]["content"].([]map[string]any)
	if !ok || len(parts) != 1 || parts[0]["text"] != "Hello world" {
		t.Fatalf("unexpected parts: %#v", msgs[0]["content"])
	}
	if _, hasCache := parts[0]["cache_control"]; hasCache {
		t.Fatalf("user message should not have cache_control")
	}
	if initialBody["vl_high_resolution_images"] != true {
		t.Fatalf("expected vl_high_resolution_images: true")
	}
}
