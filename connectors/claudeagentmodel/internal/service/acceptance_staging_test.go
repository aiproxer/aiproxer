package service

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestStagedAcceptance_FrontendToolLoop drives the production seams end to end:
// the real aipstd host, the real OpenAI Chat, Anthropic Messages, OpenAI
// Responses (legacy and item-authority) and Gemini frontends, the real gRPC
// backend-plugin transport, the real connector service/engine and the real
// private sidecar protocol. Streaming and collected loops prove text and JSON
// results while replaying the assistant prefix each client actually saw. Only
// the SDK/model boundary is synthetic (the SDK-shaped fixture) because a paid
// live account is not part of the merge gate. The production sidecar glue
// itself is proved separately by the Node production-wiring suite.
func TestStagedAcceptance_FrontendToolLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("staged acceptance builds binaries")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("the connector manifest declares linux/amd64")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to stage the sidecar bundle")
	}
	stage := stageConnectorPlugin(t)
	address := reserveAddress(t)
	configPath := writeHostConfig(t, stage, address)
	server := startHost(t, configPath)
	defer stopHost(t, server)
	base := "http://" + address

	client := &http.Client{Timeout: 60 * time.Second}

	t.Run("openai chat collected", func(t *testing.T) {
		body := chatWire(false, chatToolCatalogueA, `[{"role":"user","content":"read the file"}]`)
		response := postJSON(t, client, base+"/v1/chat/completions", body, nil)
		var parsed chatCompletion
		if err := json.Unmarshal([]byte(response), &parsed); err != nil {
			t.Fatalf("%v: %s", err, response)
		}
		if len(parsed.Choices) != 1 || parsed.Choices[0].FinishReason != "tool_calls" {
			t.Fatalf("response=%s", response)
		}
		calls := parsed.Choices[0].Message.ToolCalls
		if len(calls) != 1 || calls[0].ID == "" || calls[0].Function.Name != "Read" ||
			calls[0].Function.Arguments != `{"file_path":"x"}` {
			t.Fatalf("tool calls=%+v", calls)
		}
	})

	t.Run("openai chat stream", func(t *testing.T) {
		body := chatWire(true, chatToolCatalogueA, `[{"role":"user","content":"read the file"}]`)
		stream := postJSON(t, client, base+"/v1/chat/completions", body, nil)
		events := sseEvents(t, stream)
		id, name, args := chatStreamToolCall(events)
		if id == "" || name != "Read" || args != `{"file_path":"x"}` {
			t.Fatalf("stream=%s", stream)
		}
		if lastChatFinish(events) != "tool_calls" {
			t.Fatalf("stream=%s", stream)
		}
	})

	t.Run("openai chat collected continuation", func(t *testing.T) {
		id := chatFirstCallID(t, client, base, false, chatToolCatalogueA, "read the file")
		continuation := chatWire(false, chatToolCatalogueA, chatContinuationMessages(id, "contents"))
		resumed := postJSON(t, client, base+"/v1/chat/completions", continuation, nil)
		var parsed chatCompletion
		if err := json.Unmarshal([]byte(resumed), &parsed); err != nil {
			t.Fatalf("%v: %s", err, resumed)
		}
		if len(parsed.Choices) != 1 || parsed.Choices[0].FinishReason != "stop" ||
			!strings.Contains(parsed.Choices[0].Message.Content, "got:contents") {
			t.Fatalf("continuation=%s", resumed)
		}
	})

	t.Run("openai chat stream continuation", func(t *testing.T) {
		id := chatFirstCallID(t, client, base, true, chatToolCatalogueA, "read the file")
		continuation := chatWire(true, chatToolCatalogueA, chatContinuationMessages(id, "contents"))
		resumed := postJSON(t, client, base+"/v1/chat/completions", continuation, nil)
		events := sseEvents(t, resumed)
		if !strings.Contains(chatStreamContent(events), "got:contents") || lastChatFinish(events) != "stop" {
			t.Fatalf("continuation=%s", resumed)
		}
	})

	t.Run("openai chat structured result continuation", func(t *testing.T) {
		id := chatFirstCallID(t, client, base, false, chatToolCatalogueA, "read the file")
		continuation := chatWire(false, chatToolCatalogueA, chatContinuationMessages(id, `{\"ok\":true,\"error\":null}`))
		resumed := postJSON(t, client, base+"/v1/chat/completions", continuation, nil)
		var parsed chatCompletion
		if err := json.Unmarshal([]byte(resumed), &parsed); err != nil {
			t.Fatalf("%v: %s", err, resumed)
		}
		if len(parsed.Choices) != 1 || parsed.Choices[0].FinishReason != "stop" ||
			!strings.Contains(parsed.Choices[0].Message.Content, `got:{"ok":true,"error":null}`) {
			t.Fatalf("structured continuation=%s", resumed)
		}
	})

	t.Run("anthropic messages collected", func(t *testing.T) {
		body := anthropicWire(false, anthropicToolCatalogueB, `[{"role":"user","content":"apply the patch"}]`)
		response := postJSON(t, client, base+"/v1/messages", body, map[string]string{"anthropic-version": "2023-06-01"})
		var parsed anthropicMessage
		if err := json.Unmarshal([]byte(response), &parsed); err != nil {
			t.Fatalf("%v: %s", err, response)
		}
		if parsed.StopReason != "tool_use" {
			t.Fatalf("response=%s", response)
		}
		found := false
		for _, block := range parsed.Content {
			if block.Type == "tool_use" {
				found = true
				if block.ID == "" || block.Name != "functions.apply_patch" || string(block.Input) != `{"file_path":"x"}` {
					t.Fatalf("tool_use=%+v", block)
				}
			}
		}
		if !found {
			t.Fatalf("response=%s", response)
		}
	})

	t.Run("anthropic messages stream", func(t *testing.T) {
		body := anthropicWire(true, anthropicToolCatalogueB, `[{"role":"user","content":"apply the patch"}]`)
		stream := postJSON(t, client, base+"/v1/messages", body, map[string]string{"anthropic-version": "2023-06-01"})
		events := sseEvents(t, stream)
		id, name := anthropicStreamToolUse(events)
		if id == "" || name != "functions.apply_patch" {
			t.Fatalf("stream=%s", stream)
		}
		if !anthropicStreamStop(events, "tool_use") {
			t.Fatalf("stream=%s", stream)
		}
	})

	t.Run("anthropic messages collected continuation", func(t *testing.T) {
		id := anthropicFirstCallID(t, client, base, false, anthropicToolCatalogueB, "apply the patch")
		continuation := anthropicWire(false, anthropicToolCatalogueB, anthropicContinuationMessages(id, "contents"))
		resumed := postJSON(t, client, base+"/v1/messages", continuation, map[string]string{"anthropic-version": "2023-06-01"})
		var parsed anthropicMessage
		if err := json.Unmarshal([]byte(resumed), &parsed); err != nil {
			t.Fatalf("%v: %s", err, resumed)
		}
		if parsed.StopReason != "end_turn" || !strings.Contains(anthropicText(parsed), "got:contents") {
			t.Fatalf("continuation=%s", resumed)
		}
	})

	t.Run("anthropic messages stream continuation", func(t *testing.T) {
		id := anthropicFirstCallID(t, client, base, true, anthropicToolCatalogueB, "apply the patch")
		continuation := anthropicWire(true, anthropicToolCatalogueB, anthropicContinuationMessages(id, "contents"))
		resumed := postJSON(t, client, base+"/v1/messages", continuation, map[string]string{"anthropic-version": "2023-06-01"})
		events := sseEvents(t, resumed)
		if !anthropicStreamStop(events, "end_turn") || !strings.Contains(anthropicStreamText(events), "got:contents") {
			t.Fatalf("continuation=%s", resumed)
		}
	})

	t.Run("anthropic messages structured result continuation", func(t *testing.T) {
		structured := `{\"ok\":true,\"error\":null}`
		id := anthropicFirstCallID(t, client, base, false, anthropicToolCatalogueB, "apply the patch")
		continuation := anthropicWire(false, anthropicToolCatalogueB, anthropicContinuationMessages(id, structured))
		resumed := postJSON(t, client, base+"/v1/messages", continuation, map[string]string{"anthropic-version": "2023-06-01"})
		var parsed anthropicMessage
		if err := json.Unmarshal([]byte(resumed), &parsed); err != nil {
			t.Fatalf("%v: %s", err, resumed)
		}
		if parsed.StopReason != "end_turn" || !strings.Contains(anthropicText(parsed), `got:{"ok":true,"error":null}`) {
			t.Fatalf("structured continuation=%s", resumed)
		}
	})

	t.Run("openai responses legacy collected text", func(t *testing.T) {
		assertResponsesLoop(t, client, base+"/v1/responses", false, "legacy responses collected", "contents")
	})
	t.Run("openai responses legacy stream text", func(t *testing.T) {
		assertResponsesLoop(t, client, base+"/v1/responses", true, "legacy responses stream", "contents")
	})
	t.Run("openai responses legacy stream codex call then text", func(t *testing.T) {
		// Codex 0.166 replays the assistant message item after the function_call
		// item even though the model emitted the text first. The continuation
		// must still release the parked native turn instead of starting a new
		// runtime that repeats the tool call.
		assertResponsesLoopOrdered(t, client, base+"/v1/responses", true, "legacy responses codex order", "contents", responsesCodexContinuationWire)
	})
	t.Run("openai responses legacy collected json", func(t *testing.T) {
		assertResponsesLoop(t, client, base+"/v1/responses", false, "legacy responses json", `{"ok":true,"error":null}`)
	})
	t.Run("openai responses legacy stream json", func(t *testing.T) {
		assertResponsesLoop(t, client, base+"/v1/responses", true, "legacy responses stream json", `{"ok":true,"error":null}`)
	})

	t.Run("openai responses items collected text", func(t *testing.T) {
		assertResponsesLoop(t, client, base+"/openresponses/v1/responses", false, "item responses collected", "contents")
	})
	t.Run("openai responses items stream text", func(t *testing.T) {
		assertResponsesLoop(t, client, base+"/openresponses/v1/responses", true, "item responses stream", "contents")
	})
	t.Run("openai responses items collected json", func(t *testing.T) {
		assertResponsesLoop(t, client, base+"/openresponses/v1/responses", false, "item responses json", `{"ok":true,"error":null}`)
	})
	t.Run("openai responses items stream json", func(t *testing.T) {
		assertResponsesLoop(t, client, base+"/openresponses/v1/responses", true, "item responses stream json", `{"ok":true,"error":null}`)
	})

	t.Run("gemini collected text", func(t *testing.T) {
		assertGeminiLoop(t, client, base, false, "gemini collected", `{"result":"contents"}`)
	})
	t.Run("gemini stream text", func(t *testing.T) {
		assertGeminiLoop(t, client, base, true, "gemini stream", `{"result":"contents"}`)
	})
	t.Run("gemini collected json", func(t *testing.T) {
		assertGeminiLoop(t, client, base, false, "gemini json", `{"ok":true,"error":null}`)
	})
	t.Run("gemini stream json", func(t *testing.T) {
		assertGeminiLoop(t, client, base, true, "gemini stream json", `{"ok":true,"error":null}`)
	})
}

type chatCompletion struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

type anthropicMessage struct {
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
}

func chatFirstCallID(t *testing.T, client *http.Client, base string, stream bool, catalogue, prompt string) string {
	t.Helper()
	body := chatWire(stream, catalogue, `[{"role":"user","content":"`+prompt+`"}]`)
	response := postJSON(t, client, base+"/v1/chat/completions", body, nil)
	if stream {
		id, _, _ := chatStreamToolCall(sseEvents(t, response))
		if id == "" {
			t.Fatalf("stream=%s", response)
		}
		return id
	}
	var parsed chatCompletion
	if err := json.Unmarshal([]byte(response), &parsed); err != nil {
		t.Fatalf("%v: %s", err, response)
	}
	if len(parsed.Choices) != 1 || len(parsed.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("response=%s", response)
	}
	return parsed.Choices[0].Message.ToolCalls[0].ID
}

func anthropicFirstCallID(t *testing.T, client *http.Client, base string, stream bool, catalogue, prompt string) string {
	t.Helper()
	body := anthropicWire(stream, catalogue, `[{"role":"user","content":"`+prompt+`"}]`)
	response := postJSON(t, client, base+"/v1/messages", body, map[string]string{"anthropic-version": "2023-06-01"})
	if stream {
		id, _ := anthropicStreamToolUse(sseEvents(t, response))
		if id == "" {
			t.Fatalf("stream=%s", response)
		}
		return id
	}
	var parsed anthropicMessage
	if err := json.Unmarshal([]byte(response), &parsed); err != nil {
		t.Fatalf("%v: %s", err, response)
	}
	for _, block := range parsed.Content {
		if block.Type == "tool_use" {
			return block.ID
		}
	}
	t.Fatalf("response=%s", response)
	return ""
}

func chatContinuationMessages(id, result string) string {
	return `[
		{"role":"user","content":"read the file"},
		{"role":"assistant","content":"let me look","tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"Read","arguments":"{\"file_path\":\"x\"}"}}]},
		{"role":"tool","tool_call_id":"` + id + `","content":"` + result + `"}
	]`
}

func anthropicContinuationMessages(id, result string) string {
	return `[
		{"role":"user","content":"apply the patch"},
		{"role":"assistant","content":[{"type":"text","text":"let me look"},{"type":"tool_use","id":"` + id + `","name":"functions.apply_patch","input":{"file_path":"x"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"` + result + `"}]}
	]`
}

const chatToolCatalogueA = `[{"type":"function","function":{"name":"Read","description":"read a file","parameters":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}}}]`

const anthropicToolCatalogueB = `[{"name":"functions.apply_patch","description":"apply a patch","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}}]`

func chatWire(stream bool, tools, messages string) string {
	mode := "false"
	if stream {
		mode = "true"
	}
	return `{"model":"claude-model:anthropic/sonnet","stream":` + mode + `,"messages":` + messages + `,"tools":` + tools + `}`
}

func anthropicWire(stream bool, tools, messages string) string {
	mode := "false"
	if stream {
		mode = "true"
	}
	return `{"model":"claude-model:anthropic/sonnet","max_tokens":256,"stream":` + mode + `,"messages":` + messages + `,"tools":` + tools + `}`
}

func postJSON(t *testing.T, client *http.Client, url, body string, headers map[string]string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, payload)
	}
	return string(payload)
}

func sseEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var events []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("invalid SSE payload %q: %v", payload, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func chatStreamToolCall(events []map[string]any) (string, string, string) {
	var gotID, gotName, gotArgs strings.Builder
	for _, event := range events {
		choices, _ := event["choices"].([]any)
		for _, raw := range choices {
			choice, _ := raw.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			calls, _ := delta["tool_calls"].([]any)
			for _, rawCall := range calls {
				call, _ := rawCall.(map[string]any)
				if value, ok := call["id"].(string); ok {
					gotID.WriteString(value)
				}
				fn, _ := call["function"].(map[string]any)
				if value, ok := fn["name"].(string); ok {
					gotName.WriteString(value)
				}
				if value, ok := fn["arguments"].(string); ok {
					gotArgs.WriteString(value)
				}
			}
		}
	}
	return gotID.String(), gotName.String(), gotArgs.String()
}

func chatStreamContent(events []map[string]any) string {
	var content strings.Builder
	for _, event := range events {
		choices, _ := event["choices"].([]any)
		for _, raw := range choices {
			choice, _ := raw.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			if value, ok := delta["content"].(string); ok {
				content.WriteString(value)
			}
		}
	}
	return content.String()
}

func lastChatFinish(events []map[string]any) string {
	finish := ""
	for _, event := range events {
		choices, _ := event["choices"].([]any)
		for _, raw := range choices {
			choice, _ := raw.(map[string]any)
			if value, ok := choice["finish_reason"].(string); ok && value != "" {
				finish = value
			}
		}
	}
	return finish
}

func anthropicStreamToolUse(events []map[string]any) (string, string) {
	for _, event := range events {
		if event["type"] != "content_block_start" {
			continue
		}
		block, _ := event["content_block"].(map[string]any)
		if block["type"] == "tool_use" {
			id, _ := block["id"].(string)
			name, _ := block["name"].(string)
			return id, name
		}
	}
	return "", ""
}

func anthropicStreamText(events []map[string]any) string {
	var text strings.Builder
	for _, event := range events {
		if event["type"] != "content_block_delta" {
			continue
		}
		delta, _ := event["delta"].(map[string]any)
		if value, ok := delta["text"].(string); ok {
			text.WriteString(value)
		}
	}
	return text.String()
}

func anthropicText(message anthropicMessage) string {
	var text strings.Builder
	for _, block := range message.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

func anthropicStreamStop(events []map[string]any, stop string) bool {
	for _, event := range events {
		if event["type"] != "message_delta" {
			continue
		}
		delta, _ := event["delta"].(map[string]any)
		if delta["stop_reason"] == stop {
			return true
		}
	}
	return false
}

func anthropicSSEStop(t *testing.T, body, stop string) bool {
	t.Helper()
	return anthropicStreamStop(sseEvents(t, body), stop)
}

// responsesWire carries what both OpenAI Responses frontends accept: the
// legacy openairesponses decoder and the item-authoritative openresponses
// decoder. input_items are message/function_call/function_call_output items.
func responsesWire(stream bool, inputItems []any) string {
	body := map[string]any{
		"model":  "claude-model:anthropic/sonnet",
		"stream": stream,
		"store":  false,
		"input":  inputItems,
		"tools":  []any{responsesToolDeclaration()},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func responsesToolDeclaration() map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "Read",
		"description": "read a file",
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
			"required":   []any{"file_path"},
		},
	}
}

func responsesWireUserItem(prompt string) map[string]any {
	return map[string]any{
		"type":    "message",
		"role":    "user",
		"content": []any{map[string]any{"type": "input_text", "text": prompt}},
	}
}

func responsesContinuationWire(stream bool, prompt, callID, result string) string {
	items := []any{
		responsesWireUserItem(prompt),
		map[string]any{
			"type":    "message",
			"role":    "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": "let me look"}},
		},
		map[string]any{
			"type":      "function_call",
			"call_id":   callID,
			"name":      "Read",
			"arguments": `{"file_path":"x"}`,
		},
		map[string]any{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  result,
		},
	}
	return responsesWire(stream, items)
}

// responsesCodexContinuationWire reproduces the exact item order Codex 0.166
// sends after a turn that carried assistant text and a function_call: the
// assistant message item follows the function_call item, not the other way
// around.
func responsesCodexContinuationWire(stream bool, prompt, callID, result string) string {
	items := []any{
		responsesWireUserItem(prompt),
		map[string]any{
			"type":      "function_call",
			"call_id":   callID,
			"name":      "Read",
			"arguments": `{"file_path":"x"}`,
		},
		map[string]any{
			"type":    "message",
			"role":    "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": "let me look"}},
		},
		map[string]any{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  result,
		},
	}
	return responsesWire(stream, items)
}

type responsesBody struct {
	Output []responsesItem `json:"output"`
}

type responsesItem struct {
	Type      string          `json:"type"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Content   []responsesText `json:"content"`
}

type responsesText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func responsesFunctionCall(t *testing.T, body string) (string, string, string) {
	t.Helper()
	var parsed responsesBody
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	for _, item := range parsed.Output {
		if item.Type == "function_call" {
			return item.CallID, item.Name, item.Arguments
		}
	}
	t.Fatalf("no function_call item: %s", body)
	return "", "", ""
}

func responsesAssistantText(body string) string {
	var parsed responsesBody
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ""
	}
	var text strings.Builder
	for _, item := range parsed.Output {
		for _, part := range item.Content {
			if part.Type == "output_text" {
				text.WriteString(part.Text)
			}
		}
	}
	return text.String()
}

func responsesStreamFunctionCall(t *testing.T, events []map[string]any) (string, string, string) {
	t.Helper()
	for _, event := range events {
		if event["type"] != "response.output_item.done" {
			continue
		}
		item, _ := event["item"].(map[string]any)
		if item == nil {
			continue
		}
		if item["type"] != "function_call" {
			continue
		}
		id, _ := item["call_id"].(string)
		name, _ := item["name"].(string)
		args, _ := item["arguments"].(string)
		if id != "" {
			return id, name, args
		}
	}
	t.Fatalf("stream carried no function_call item: %v", events)
	return "", "", ""
}

func responsesStreamText(events []map[string]any) string {
	var text strings.Builder
	for _, event := range events {
		if event["type"] != "response.output_text.delta" {
			continue
		}
		if delta, ok := event["delta"].(string); ok {
			text.WriteString(delta)
		}
	}
	return text.String()
}

// assertResponsesLoop proves one fresh tool call and its exact continuation
// through either production Responses frontend, streaming or collected. The
// continuation repeats the assistant prefix the client actually saw; a lost or
// altered prefix fails the connector's ordered proof. Only the exact client
// serialization order changes, never the content.
func assertResponsesLoop(t *testing.T, client *http.Client, url string, stream bool, prompt, result string) {
	t.Helper()
	assertResponsesLoopOrdered(t, client, url, stream, prompt, result, responsesContinuationWire)
}

// assertResponsesLoopOrdered is the same proof with the continuation item order
// selected by the caller, so client-specific serializations can be exercised.
func assertResponsesLoopOrdered(t *testing.T, client *http.Client, url string, stream bool, prompt, result string, continuationWire func(bool, string, string, string) string) {
	t.Helper()
	fresh := postJSON(t, client, url, responsesWire(stream, []any{responsesWireUserItem(prompt)}), nil)
	var id, name, args string
	if stream {
		events := sseEvents(t, fresh)
		id, name, args = responsesStreamFunctionCall(t, events)
		if text := responsesStreamText(events); !strings.Contains(text, "let me look") {
			t.Fatalf("fresh stream lost the assistant prefix: %s", fresh)
		}
	} else {
		id, name, args = responsesFunctionCall(t, fresh)
		if text := responsesAssistantText(fresh); !strings.Contains(text, "let me look") {
			t.Fatalf("fresh response lost the assistant prefix: %s", fresh)
		}
	}
	if id == "" || name != "Read" || args != `{"file_path":"x"}` {
		t.Fatalf("fresh tool call id=%q name=%q args=%q body=%s", id, name, args, fresh)
	}

	resumed := postJSON(t, client, url, continuationWire(stream, prompt, id, result), nil)
	want := "got:" + result
	if stream {
		if text := responsesStreamText(sseEvents(t, resumed)); !strings.Contains(text, want) {
			t.Fatalf("continuation lost result %q: %s", want, resumed)
		}
		return
	}
	if text := responsesAssistantText(resumed); !strings.Contains(text, want) {
		t.Fatalf("continuation lost result %q: %s", want, resumed)
	}
}

func geminiToolDeclaration() map[string]any {
	return map[string]any{
		"functionDeclarations": []any{map[string]any{
			"name":        "Read",
			"description": "read a file",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
				"required":   []any{"file_path"},
			},
		}},
	}
}

func geminiWire(contents []any) string {
	encoded, err := json.Marshal(map[string]any{
		"contents": contents,
		"tools":    []any{geminiToolDeclaration()},
	})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func geminiInitialWire(prompt string) string {
	return geminiWire([]any{map[string]any{
		"role":  "user",
		"parts": []any{map[string]any{"text": prompt}},
	}})
}

func geminiContinuationWire(prompt, result string) string {
	if !json.Valid([]byte(result)) {
		panic("result must be valid JSON")
	}
	return geminiWire([]any{
		map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": prompt}},
		},
		map[string]any{
			"role": "model",
			"parts": []any{
				map[string]any{"text": "let me look"},
				map[string]any{"functionCall": map[string]any{
					"name": "Read",
					"args": map[string]any{"file_path": "x"},
				}},
			},
		},
		map[string]any{
			"role": "user",
			"parts": []any{map[string]any{"functionResponse": map[string]any{
				"name":     "Read",
				"response": json.RawMessage(result),
			}}},
		},
	})
}

type geminiBody struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text         string `json:"text"`
				FunctionCall struct {
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				} `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

func geminiText(body string) string {
	var parsed geminiBody
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ""
	}
	var text strings.Builder
	for _, candidate := range parsed.Candidates {
		for _, part := range candidate.Content.Parts {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

func geminiStreamText(events []map[string]any) string {
	var text strings.Builder
	for _, event := range events {
		candidates, _ := event["candidates"].([]any)
		for _, raw := range candidates {
			candidate, _ := raw.(map[string]any)
			content, _ := candidate["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			for _, rawPart := range parts {
				part, _ := rawPart.(map[string]any)
				if value, ok := part["text"].(string); ok {
					text.WriteString(value)
				}
			}
		}
	}
	return text.String()
}

func geminiStreamFunctionCall(t *testing.T, events []map[string]any) (string, string) {
	t.Helper()
	for _, event := range events {
		candidates, _ := event["candidates"].([]any)
		for _, raw := range candidates {
			candidate, _ := raw.(map[string]any)
			content, _ := candidate["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			for _, rawPart := range parts {
				part, _ := rawPart.(map[string]any)
				call, _ := part["functionCall"].(map[string]any)
				if call == nil {
					continue
				}
				name, _ := call["name"].(string)
				args, _ := json.Marshal(call["args"])
				return name, string(args)
			}
		}
	}
	t.Fatalf("stream carried no functionCall part: %v", events)
	return "", ""
}

// assertGeminiLoop proves one fresh name-only tool call and its exact
// continuation. The continuation repeats the model's actual assistant text
// before the name-only call; the connector must match name, arguments and the
// ordered prefix without inventing an opaque call id.
func assertGeminiLoop(t *testing.T, client *http.Client, base string, stream bool, prompt, result string) {
	t.Helper()
	path := "/v1beta/models/gemini-2.0-flash:generateContent"
	if stream {
		path = "/v1beta/models/gemini-2.0-flash:streamGenerateContent?alt=sse"
	}
	fresh := postJSON(t, client, base+path, geminiInitialWire(prompt), nil)
	if stream {
		events := sseEvents(t, fresh)
		name, args := geminiStreamFunctionCall(t, events)
		if name != "Read" || args != `{"file_path":"x"}` {
			t.Fatalf("fresh name=%q args=%q body=%s", name, args, fresh)
		}
		if text := geminiStreamText(events); !strings.Contains(text, "let me look") {
			t.Fatalf("fresh stream lost the assistant prefix: %s", fresh)
		}
	} else {
		var parsed geminiBody
		if err := json.Unmarshal([]byte(fresh), &parsed); err != nil {
			t.Fatalf("%v: %s", err, fresh)
		}
		found := false
		for _, candidate := range parsed.Candidates {
			for _, part := range candidate.Content.Parts {
				if part.FunctionCall.Name != "Read" || string(part.FunctionCall.Args) != `{"file_path":"x"}` {
					continue
				}
				found = true
			}
		}
		if !found || !strings.Contains(geminiText(fresh), "let me look") {
			t.Fatalf("fresh response lost the name-only call or prefix: %s", fresh)
		}
	}

	resumed := postJSON(t, client, base+path, geminiContinuationWire(prompt, result), nil)
	want := "got:" + result
	if stream {
		if text := geminiStreamText(sseEvents(t, resumed)); !strings.Contains(text, want) {
			t.Fatalf("continuation lost result %q: %s", want, resumed)
		}
		return
	}
	if text := geminiText(resumed); !strings.Contains(text, want) {
		t.Fatalf("continuation lost result %q: %s", want, resumed)
	}
}

// stageConnectorPlugin builds the connector binary and stages it with the
// SDK-shaped sidecar fixture and a closed manifest. Only the executable hash is
// manifest-bound; companions are copied from the real sidecar directory.
func stageConnectorPlugin(t *testing.T) string {
	t.Helper()
	moduleRoot := moduleRoot(t)
	repoRoot := filepath.Clean(filepath.Join(moduleRoot, "..", ".."))
	stage := t.TempDir()
	pluginDir := filepath.Join(stage, "claudeagentmodel")
	binDir := filepath.Join(pluginDir, "bin")
	sidecarDir := filepath.Join(pluginDir, "sidecar")
	for _, dir := range []string{binDir, sidecarDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(binDir, "aip-backend-claudeagentmodel")
	runBuild(t, moduleRoot, binary, "./cmd/aip-backend-claudeagentmodel")
	// The SDK-shaped fixture is staged behind a tiny entry shim so independent
	// synthetic conversations use distinct opaque tool ids. The private
	// protocol and the connector glue are the real ones.
	if err := copyFile(filepath.Join(moduleRoot, "internal", "bridge", "testdata", "tool-sidecar.mjs"), filepath.Join(sidecarDir, "fixture.mjs")); err != nil {
		t.Fatal(err)
	}
	shim := "process.env.FAKE_MODE = 'unique_ids'\nawait import('./fixture.mjs')\n"
	if err := os.WriteFile(filepath.Join(sidecarDir, "bridge.mjs"), []byte(shim), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"session.mjs", "tool-server.mjs", "package.json"} {
		if err := copyFile(filepath.Join(moduleRoot, "sidecar", name), filepath.Join(sidecarDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := fileDigest(binary)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema":             "aiproxer.backendplugin.manifest/v1",
		"plugin_id":          PluginID,
		"version":            "0.1.0",
		"build_id":           "s2-acceptance",
		"executable":         "bin/aip-backend-claudeagentmodel",
		"sha256":             digest,
		"protocol_major":     1,
		"protocol_min_minor": 0,
		"protocol_max_minor": 11,
		"platforms":          []map[string]string{{"os": "linux", "arch": "amd64"}},
		"exports": []map[string]any{{
			"kind":            FactoryKind,
			"experimental":    true,
			"credential_mode": "oauth_user",
			"access_scope":    "local_only",
			"process_sharing": "per_instance",
			"execution_class": "agent_runtime",
		}},
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.backendplugin.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	// The staged "Claude Code CLI" is a local version probe; the SDK boundary is
	// the protocol fixture above and never reaches a real account.
	fakeCLI := filepath.Join(stage, "claude")
	if err := os.WriteFile(fakeCLI, []byte("#!/bin/sh\necho 2.1.311\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hostBinary := filepath.Join(stage, "aipstd")
	runBuild(t, repoRoot, hostBinary, "./cmd/aipstd")
	return stage
}

func writeHostConfig(t *testing.T, stage, address string) string {
	t.Helper()
	workspace := t.TempDir()
	config := fmt.Sprintf(`server:
  address: %q
access:
  mode: single_user
routing:
  default_route: "claude-model:anthropic/sonnet"
plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: openresponses
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
    - id: openai-responses
      enabled: true
      config: {}
  backend_discovery:
    enabled: true
    paths: [%q]
    instances:
      claude-model:
        kind: claudeagentmodel
        execution_env: [HOME, PATH, TMPDIR]
  backends:
    - id: claude-model
      kind: claudeagentmodel
      enabled: true
      config:
        executable: %q
        default_workspace: %q
        credential_source: host_login
        admin_policy: honor
        max_runtimes: 8
        pending_ttl_seconds: 120
`, address, filepath.Join(stage, "claudeagentmodel"), filepath.Join(stage, "claude"), workspace)
	path := filepath.Join(stage, "host.yaml")
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func startHost(t *testing.T, configPath string) *exec.Cmd {
	t.Helper()
	stage := filepath.Dir(configPath)
	command := exec.Command(filepath.Join(stage, "aipstd"), "--config", configPath, "serve")
	command.Env = append(os.Environ(), "HOME="+stage)
	var logs bytes.Buffer
	command.Stdout = &logs
	command.Stderr = &logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("host log:\n%s", logs.String())
		}
	})
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", hostAddress(t, configPath), 200*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return command
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = command.Process.Kill()
	_ = command.Wait()
	t.Fatalf("host did not start: %s", logs.String())
	return nil
}

func stopHost(t *testing.T, command *exec.Cmd) {
	t.Helper()
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		<-done
	}
}

func hostAddress(t *testing.T, configPath string) string {
	t.Helper()
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		value, ok := strings.CutPrefix(trimmed, "address:")
		if !ok {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	t.Fatalf("staged config has no address")
	return ""
}

func reserveAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func runBuild(t *testing.T, dir, output, pkg string) {
	t.Helper()
	command := exec.Command("go", "build", "-o", output, pkg)
	command.Dir = dir
	command.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v: %s", pkg, err, combined)
	}
}

func copyFile(source, target string) error {
	body, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, body, 0o644)
}

func fileDigest(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
