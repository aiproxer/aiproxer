package catalog

import (
	"testing"
)

func TestParseModelsResponse_openAIList(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"object": "list",
		"data": [
			{"id": "gpt-5.4", "object": "model"},
			{"id": "claude-sonnet-4-6", "object": "model"}
		]
	}`)
	entries, err := ParseModelsResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].RawID != "gpt-5.4" || entries[1].RawID != "claude-sonnet-4-6" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseModelsResponse_openCodeGoList(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"object":"list",
		"data":[
			{"id":"minimax-m3","object":"model","created":1782378346,"owned_by":"opencode"},
			{"id":"kimi-k2.7-code","object":"model","created":1782378346,"owned_by":"opencode"},
			{"id":"qwen3.7-plus","object":"model","created":1782378346,"owned_by":"opencode"}
		]
	}`)
	entries, err := ParseModelsResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].RawID != "minimax-m3" || entries[1].RawID != "kimi-k2.7-code" || entries[2].RawID != "qwen3.7-plus" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseModelsResponse_extendedMetadata(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"data": [
			{
				"id": "gpt-5.4",
				"endpoint": "https://opencode.ai/zen/v1/responses",
				"ai_sdk_package": "@ai-sdk/openai",
				"name": "GPT 5.4"
			},
			{
				"id": "claude-sonnet-4-6",
				"endpoint": "https://opencode.ai/zen/v1/messages",
				"ai_sdk_package": "@ai-sdk/anthropic",
				"display_name": "Claude Sonnet 4.6"
			},
			{
				"id": "gemini-3.1-pro",
				"endpoint": "https://opencode.ai/zen/v1/models/gemini-3.1-pro:generateContent",
				"ai_sdk_package": "@ai-sdk/google",
				"name": "Gemini 3.1 Pro"
			}
		]
	}`)
	entries, err := ParseModelsResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].DisplayName != "GPT 5.4" {
		t.Fatalf("entry[0] = %+v", entries[0])
	}
	if entries[1].DisplayName != "Claude Sonnet 4.6" {
		t.Fatalf("entry[1] = %+v", entries[1])
	}
	if entries[0].Endpoint == "" || entries[0].AISDKPackage == "" {
		t.Fatalf("entry[0] = %+v", entries[0])
	}
}

func TestParseModelsResponse_skipsEmptyIDs(t *testing.T) {
	t.Parallel()

	body := []byte(`{"data":[{"id":""},{"id":"glm-5.2"}]}`)
	entries, err := ParseModelsResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].RawID != "glm-5.2" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseModelsResponse_skipsSchemaPlaceholderIDs(t *testing.T) {
	t.Parallel()

	body := []byte(`{"data":[{"id":"string","created":"int","object":"string","owned_by":"string"},{"id":"kimi-k2.7-code"}]}`)
	entries, err := ParseModelsResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].RawID != "kimi-k2.7-code" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseModelsResponse_empty(t *testing.T) {
	t.Parallel()

	_, err := ParseModelsResponse([]byte(`{"data":[]}`))
	if err == nil {
		t.Fatal("expected error for empty model list")
	}
}
