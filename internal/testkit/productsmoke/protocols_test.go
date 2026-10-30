//go:build productsmoke

package productsmoke

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const (
	smokeBackendID = "smoke"
	smokeCanonical = "smoke/chat-a"
	smokeNative    = "smoke-native-a"
	smokeModel     = smokeBackendID + ":" + smokeCanonical
	smokeToolName  = "get_weather"

	openAIChatPath = "/v1/chat/completions"

	smokeSystemInstruction = "You are the product smoke client."
	smokeUserMessage       = "Say hello in a few words."
)

// chatMessage is one client message the journeys send.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls carries assistant tool calls for a second turn.
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
	// ToolCallID carries the matching id on a tool result message.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// chatToolCall is the client-side OpenAI legacy assistant tool call.
type chatToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function chatToolCallFunction `json:"function"`
}

// chatToolCallFunction is the function payload of an assistant tool call.
type chatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatTool is one client tool definition the journeys send.
type chatTool struct {
	Type     string             `json:"type"`
	Function chatToolDefinition `json:"function"`
}

// chatToolDefinition is the function payload of a client tool.
type chatToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// chatBody is the OpenAI legacy client request the journeys send.
type chatBody struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Messages []chatMessage `json:"messages"`
	Tools    []chatTool    `json:"tools,omitempty"`
}

func smokeMessages() []chatMessage {
	return []chatMessage{
		{Role: "system", Content: smokeSystemInstruction},
		{Role: "user", Content: smokeUserMessage},
	}
}

func smokeTools() []chatTool {
	return []chatTool{{
		Type: "function",
		Function: chatToolDefinition{
			Name:        smokeToolName,
			Description: "Look up the weather for a city.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"location": map[string]any{"type": "string"},
				},
			},
		},
	}}
}

func smokeScript() fakeScript {
	return fakeScript{
		Model:  smokeNative,
		Deltas: []string{"Hello ", "from the ", "fake provider."},
		Usage:  fakeUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
	}
}

func smokeRequestBody(stream bool) []byte {
	raw, err := json.Marshal(chatBody{
		Model:    smokeModel,
		Stream:   stream,
		Messages: smokeMessages(),
		Tools:    smokeTools(),
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// TestProduct_Protocols drives one frontend per subtest pair: stream and
// collect both witness content, usage, a legal terminal and the translated
// request at the provider. SystemOne is collect-only because the shipped
// frontend serves only non-streaming decisions (owner decision, task 4).
func TestProduct_Protocols(t *testing.T) {
	t.Run("openai-legacy", func(t *testing.T) {
		t.Run("stream", testOpenAILegacyStream)
		t.Run("collect", testOpenAILegacyCollect)
	})
	t.Run("openai-responses", func(t *testing.T) {
		t.Run("stream", testOpenAIResponsesStream)
		t.Run("collect", testOpenAIResponsesCollect)
	})
	t.Run("anthropic", func(t *testing.T) {
		t.Run("stream", testAnthropicStream)
		t.Run("collect", testAnthropicCollect)
	})
	t.Run("gemini", func(t *testing.T) {
		t.Run("stream", testGeminiStream)
		t.Run("collect", testGeminiCollect)
	})
	t.Run("openresponses", func(t *testing.T) {
		t.Run("stream", testOpenResponsesStream)
		t.Run("collect", testOpenResponsesCollect)
	})
	t.Run("systemone", func(t *testing.T) {
		t.Run("collect", testSystemOneCollect)
	})
}

func testOpenAILegacyStream(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	provider := newOpenAILegacyProvider(t, canary, smokeScript())
	h := startSUT(t, sutConfig{
		Canary:    canary,
		Frontends: []string{"openai-legacy"},
		Backends: []backendWiring{{
			ID:      smokeBackendID,
			Kind:    kindOpenAILegacyCompatible,
			BaseURL: provider.OpenAILegacyBaseURL(),
			Models:  []modelWiring{{Canonical: smokeCanonical, Native: smokeNative}},
		}},
	})
	wire := doJSON(t.Context(), h.BaseURL()+openAIChatPath, smokeRequestBody(true))
	failures := failureContext{sut: h, provider: provider, wire: wire}
	if err := checkOpenAIStream(wire, smokeScript(), h.evidence.redact); err != nil {
		failf(t, failures, "openai-legacy stream: %v", err)
	}
	if got := provider.RequestCount(); got != 1 {
		failf(t, failures, "provider received %d requests, want exactly 1 (no retries)", got)
	}
	if err := checkTranslatedChat(provider.Requests()[0], smokeMessages(), smokeTools(), h.evidence.redact); err != nil {
		failf(t, failures, "openai-legacy stream: %v", err)
	}
	t.Logf("openai-legacy stream passed: %d provider request(s), evidence %s", provider.RequestCount(), h.evidence.dir)
}

func testOpenAILegacyCollect(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	provider := newOpenAILegacyProvider(t, canary, smokeScript())
	h := startSUT(t, sutConfig{
		Canary:    canary,
		Frontends: []string{"openai-legacy"},
		Backends: []backendWiring{{
			ID:      smokeBackendID,
			Kind:    kindOpenAILegacyCompatible,
			BaseURL: provider.OpenAILegacyBaseURL(),
			Models:  []modelWiring{{Canonical: smokeCanonical, Native: smokeNative}},
		}},
	})
	wire := doJSON(t.Context(), h.BaseURL()+openAIChatPath, smokeRequestBody(false))
	failures := failureContext{sut: h, provider: provider, wire: wire}
	if err := checkOpenAICollect(wire, smokeScript(), h.evidence.redact); err != nil {
		failf(t, failures, "openai-legacy collect: %v", err)
	}
	if got := wireModel(wire); got != smokeModel {
		failf(t, failures, "openai-legacy collect: response model %q, want the client route %q", got, smokeModel)
	}
	if got := provider.RequestCount(); got != 1 {
		failf(t, failures, "provider received %d requests, want exactly 1 (no retries)", got)
	}
	if err := checkTranslatedChat(provider.Requests()[0], smokeMessages(), smokeTools(), h.evidence.redact); err != nil {
		failf(t, failures, "openai-legacy collect: %v", err)
	}
	t.Logf("openai-legacy collect passed: %d provider request(s), evidence %s", provider.RequestCount(), h.evidence.dir)
}

// usagePayload is the OpenAI legacy usage object.
type usagePayload struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (u usagePayload) String() string {
	return fmt.Sprintf("prompt=%d completion=%d total=%d", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
}

type chatToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role      string              `json:"role"`
			Content   string              `json:"content"`
			ToolCalls []chatToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usagePayload `json:"usage"`
}

type chatCompletion struct {
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usagePayload `json:"usage"`
}

// checkOpenAIStream observes the terminal event first, then compares it,
// reassembles the content and checks the scripted usage. Every excerpt of the
// observed wire is redacted before it is bounded.
func checkOpenAIStream(wire *clientWire, script fakeScript, redact redactor) error {
	if wire.Err != nil {
		return fmt.Errorf("client exchange failed: %w", wire.Err)
	}
	if wire.Status != http.StatusOK {
		return fmt.Errorf("status %d, want 200; body: %s", wire.Status, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if contentType := wire.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		return fmt.Errorf("content type %q, want text/event-stream", contentType)
	}
	frames, err := parseSSEFrames(wire.Body)
	if err != nil {
		return fmt.Errorf("parse SSE: %w", err)
	}
	payloads := dataPayloads(frames)
	if len(payloads) == 0 {
		return errors.New("no SSE data events observed")
	}
	if last := payloads[len(payloads)-1]; last != "[DONE]" {
		return fmt.Errorf("stream ended with %q, want the [DONE] terminal", excerpt(redact, last, wireFailureDetailExcerpt))
	}
	var (
		deltas      []string
		role        string
		terminal    *chatChunk
		terminalAt  = -1
		usage       *usagePayload
		usageChunks int
		observed    []chatToolCallDelta
		toolIndex   = map[int]int{}
	)
	chunks := make([]chatChunk, 0, len(payloads)-1)
	for i, payload := range payloads[:len(payloads)-1] {
		var chunk chatChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("chunk %d is not JSON: %v; payload: %s", i, err, excerpt(redact, payload, wireFailureDetailExcerpt))
		}
		if chunk.Object != "chat.completion.chunk" {
			return fmt.Errorf("chunk %d object %q, want chat.completion.chunk", i, chunk.Object)
		}
		if chunk.ID == "" {
			return fmt.Errorf("chunk %d has no id", i)
		}
		if chunk.Model == "" {
			return fmt.Errorf("chunk %d has no model", i)
		}
		if chunk.Usage != nil {
			usageChunks++
			usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			chunks = append(chunks, chunk)
			continue
		}
		if len(chunk.Choices) != 1 {
			return fmt.Errorf("chunk %d carried %d choices, want 1", i, len(chunk.Choices))
		}
		choice := chunk.Choices[0]
		if choice.Delta.Role != "" {
			if role != "" && role != choice.Delta.Role {
				return fmt.Errorf("chunk %d role %q after %q", i, choice.Delta.Role, role)
			}
			role = choice.Delta.Role
		}
		for _, tc := range choice.Delta.ToolCalls {
			if terminal != nil {
				return fmt.Errorf("tool call delta arrived after the terminal event at chunk %d", terminalAt)
			}
			index, seen := toolIndex[tc.Index]
			if !seen {
				index = len(observed)
				toolIndex[tc.Index] = index
				observed = append(observed, chatToolCallDelta{Index: tc.Index})
			}
			call := &observed[index]
			if tc.ID != "" {
				call.ID = tc.ID
			}
			if tc.Type != "" {
				call.Type = tc.Type
			}
			if tc.Function.Name != "" {
				call.Function.Name = tc.Function.Name
			}
			call.Function.Arguments += tc.Function.Arguments
		}
		if choice.Delta.Content != "" {
			if terminal != nil {
				return fmt.Errorf("content %q arrived after the terminal event at chunk %d", excerpt(redact, choice.Delta.Content, 80), terminalAt)
			}
			deltas = append(deltas, choice.Delta.Content)
		}
		if choice.FinishReason != nil {
			if terminal != nil {
				return fmt.Errorf("second terminal event at chunk %d after chunk %d", i, terminalAt)
			}
			terminal = &chunk
			terminalAt = i
		}
		chunks = append(chunks, chunk)
	}
	if terminal == nil {
		return errors.New("no terminal event observed: every chunk carried finish_reason=null")
	}
	if got, want := *terminal.Choices[0].FinishReason, script.finishReason(familyOpenAILegacy); got != want {
		return fmt.Errorf("terminal finish_reason %q, want %q", got, want)
	}
	// A protocol-legal terminal ends the choice stream: only usage-only chunks
	// (empty choices) may follow it before [DONE].
	for i := terminalAt + 1; i < len(chunks); i++ {
		if len(chunks[i].Choices) != 0 {
			return fmt.Errorf("chunk %d carried choices after the terminal event at chunk %d", i, terminalAt)
		}
	}
	if role != "assistant" {
		return fmt.Errorf("first observed role %q, want assistant", role)
	}
	if got, want := strings.Join(deltas, ""), script.content(); got != want {
		return fmt.Errorf("content %q, want %q", got, want)
	}
	if usageChunks != 1 {
		return fmt.Errorf("observed %d usage chunks, want exactly 1", usageChunks)
	}
	if usage == nil {
		return errors.New("terminal carried no usage")
	}
	if script.ToolCall != nil {
		if len(observed) != 1 {
			return fmt.Errorf("observed %d tool call(s), want exactly 1", len(observed))
		}
		call := observed[0]
		if call.ID != script.ToolCall.ID {
			return fmt.Errorf("tool call id %q, want %q", call.ID, script.ToolCall.ID)
		}
		if call.Type != "function" {
			return fmt.Errorf("tool call type %q, want function", call.Type)
		}
		if call.Function.Name != script.ToolCall.Name {
			return fmt.Errorf("tool call name %q, want %q", call.Function.Name, script.ToolCall.Name)
		}
		if call.Function.Arguments != script.ToolCall.Arguments {
			return fmt.Errorf("tool call arguments %q, want %q", excerpt(redact, call.Function.Arguments, wireFailureDetailExcerpt), script.ToolCall.Arguments)
		}
	} else if len(observed) != 0 {
		return fmt.Errorf("observed %d tool call(s), want none: this journey scripts text only", len(observed))
	}
	return checkUsage(*usage, script.Usage)
}

// checkOpenAICollect observes the terminal first, then compares content and
// usage. Every excerpt of the observed wire is redacted before it is bounded.
func checkOpenAICollect(wire *clientWire, script fakeScript, redact redactor) error {
	if wire.Err != nil {
		return fmt.Errorf("client exchange failed: %w", wire.Err)
	}
	if wire.Status != http.StatusOK {
		return fmt.Errorf("status %d, want 200; body: %s", wire.Status, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if contentType := wire.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		return fmt.Errorf("content type %q, want application/json", contentType)
	}
	var completion chatCompletion
	if err := json.Unmarshal(wire.Body, &completion); err != nil {
		return fmt.Errorf("decode response: %v; body: %s", err, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if completion.Object != "chat.completion" {
		return fmt.Errorf("object %q, want chat.completion", completion.Object)
	}
	if len(completion.Choices) != 1 {
		return fmt.Errorf("response carried %d choices, want 1", len(completion.Choices))
	}
	choice := completion.Choices[0]
	if choice.FinishReason == nil {
		return errors.New("no terminal finish_reason observed in the collected response")
	}
	if got, want := *choice.FinishReason, script.finishReason(familyOpenAILegacy); got != want {
		return fmt.Errorf("terminal finish_reason %q, want %q", got, want)
	}
	if choice.Message.Role != "assistant" {
		return fmt.Errorf("message role %q, want assistant", choice.Message.Role)
	}
	if got, want := choice.Message.Content, script.content(); got != want {
		return fmt.Errorf("content %q, want %q", got, want)
	}
	if completion.Usage == nil {
		return errors.New("collected response carries no usage")
	}
	return checkUsage(*completion.Usage, script.Usage)
}

func checkUsage(got usagePayload, want fakeUsage) error {
	wantPayload := usagePayload(want)
	if got != wantPayload {
		return fmt.Errorf("usage %s, want %s", got, wantPayload)
	}
	return nil
}

func wireModel(wire *clientWire) string {
	var completion chatCompletion
	if err := json.Unmarshal(wire.Body, &completion); err != nil {
		return ""
	}
	return completion.Model
}

// translatedChatTool is one provider-side OpenAI legacy tool declaration.
type translatedChatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// translatedChatMessage is the provider-side view of one OpenAI legacy chat
// message, including the tool-call and tool-result fields a second turn carries.
type translatedChatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// translatedChat is the provider-side view of the request the proxy sent.
type translatedChat struct {
	Model    string                  `json:"model"`
	Messages []translatedChatMessage `json:"messages"`
	Tools    []translatedChatTool    `json:"tools"`
}

// checkTranslatedChat asserts the OpenAI legacy chat request the provider
// actually received. Its excerpts are redacted before they are bounded.
func checkTranslatedChat(req providerRequest, wantMessages []chatMessage, wantTools []chatTool, redact redactor) error {
	return checkTranslatedOpenAIChat(req, smokeNative, openAIChatPath, wantMessages, wantTools, redact)
}

// checkTranslatedOpenAIChat asserts a chat request the provider received for a
// journey that may use a different client frontend. Its excerpts are redacted
// before they are bounded.
func checkTranslatedOpenAIChat(req providerRequest, wantModel, wantPath string, wantMessages []chatMessage, wantTools []chatTool, redact redactor) error {
	if err := checkProviderIdentity(req, wantPath); err != nil {
		return err
	}
	var got translatedChat
	if err := json.Unmarshal(req.Body, &got); err != nil {
		return fmt.Errorf("provider request body is not JSON: %v", err)
	}
	if got.Model != wantModel {
		return fmt.Errorf("provider saw model %q, want the translated native id %q", got.Model, wantModel)
	}
	if len(got.Messages) != len(wantMessages) {
		return fmt.Errorf("provider saw %d messages, want %d", len(got.Messages), len(wantMessages))
	}
	for i, want := range wantMessages {
		message := got.Messages[i]
		if message.Role != want.Role {
			return fmt.Errorf("message %d role %q, want %q", i, message.Role, want.Role)
		}
		var content string
		if err := json.Unmarshal(message.Content, &content); err != nil {
			return fmt.Errorf("message %d content is not a string: %v", i, err)
		}
		if content != want.Content {
			return fmt.Errorf("message %d content %q, want %q", i, content, want.Content)
		}
	}
	if len(wantTools) == 0 {
		return nil
	}
	return checkTranslatedTools(got.Tools, wantTools, redact)
}

// checkTranslatedTools asserts the provider received the client's tool
// declarations with their top-level parameter schema type and properties.
func checkTranslatedTools(got []translatedChatTool, wantTools []chatTool, redact redactor) error {
	if len(got) != len(wantTools) {
		return fmt.Errorf("provider saw %d tools, want %d", len(got), len(wantTools))
	}
	for i, want := range wantTools {
		tool := got[i]
		if tool.Type != want.Type {
			return fmt.Errorf("tool %d type %q, want %q", i, tool.Type, want.Type)
		}
		if tool.Function.Name != want.Function.Name {
			return fmt.Errorf("tool %d name %q, want %q", i, tool.Function.Name, want.Function.Name)
		}
		if tool.Function.Description != want.Function.Description {
			return fmt.Errorf("tool %d description %q, want %q", i, tool.Function.Description, want.Function.Description)
		}
		var parameters struct {
			Type       string `json:"type"`
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(tool.Function.Parameters, &parameters); err != nil {
			return fmt.Errorf("tool %d parameters are not JSON: %v", i, err)
		}
		wantType, ok := want.Function.Parameters["type"].(string)
		if !ok || wantType == "" {
			return fmt.Errorf("tool %d fixture parameters carry no top-level string type", i)
		}
		if parameters.Type != wantType {
			return fmt.Errorf("tool %d parameters type %q, want %q: %s", i, parameters.Type, wantType, excerpt(redact, string(tool.Function.Parameters), wireFailureDetailExcerpt))
		}
		location, ok := parameters.Properties["location"]
		if !ok || location.Type != "string" {
			return fmt.Errorf("tool %d parameters lost properties.location.type=string: %s", i, excerpt(redact, string(tool.Function.Parameters), wireFailureDetailExcerpt))
		}
	}
	return nil
}

// sseFrame is one server-sent event.
type sseFrame struct {
	Event string
	Data  string
}

// parseSSEFrames splits a bounded SSE transcript into dispatched frames. SSE
// dispatches an event only when a blank line terminates it, so pending data at
// end of stream was never dispatched by the server; it is reported as an
// explicit incomplete frame instead of being promoted to an observed event.
func parseSSEFrames(raw []byte) ([]sseFrame, error) {
	var frames []sseFrame
	var event string
	var data []string
	flush := func() {
		if len(data) == 0 {
			event = ""
			return
		}
		frames = append(frames, sseFrame{Event: event, Data: strings.Join(data, "\n")})
		event = ""
		data = nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			data = append(data, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(data) > 0 || event != "" {
		return nil, fmt.Errorf("incomplete SSE frame at end of stream: %d data line(s) were never dispatched by a blank line", len(data))
	}
	return frames, nil
}

func dataPayloads(frames []sseFrame) []string {
	out := make([]string, 0, len(frames))
	for _, frame := range frames {
		if frame.Data != "" {
			out = append(out, frame.Data)
		}
	}
	return out
}

// newCanary returns a unique credential value for one journey.
func newCanary(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("product smoke: generate canary: %v", err)
	}
	return "smoke-canary-" + hex.EncodeToString(buf)
}

// TestParseSSEFramesRequiresDispatchDelimiter proves an SSE event counts only
// when a blank line dispatched it: an undelimited terminal, with or without one
// trailing newline, must fail instead of being promoted to an observed event.
func TestParseSSEFramesRequiresDispatchDelimiter(t *testing.T) {
	t.Parallel()
	frames, err := parseSSEFrames([]byte("data: {\"ok\":true}\n\n"))
	if err != nil {
		t.Fatalf("complete dispatch rejected: %v", err)
	}
	if len(frames) != 1 || frames[0].Data != `{"ok":true}` {
		t.Fatalf("frames %+v, want one dispatched data payload", frames)
	}
	for _, raw := range []string{"data: [DONE]\n", "data: [DONE]"} {
		frames, err := parseSSEFrames([]byte(raw))
		if err == nil {
			t.Fatalf("undelimited %q was accepted as %+v", raw, frames)
		}
		if !strings.Contains(err.Error(), "incomplete SSE frame at end of stream") {
			t.Fatalf("undelimited %q error %q does not name the incomplete frame", raw, err)
		}
	}
}

// translatedChatRequestBody builds the provider-side request the smoke fixture
// is expected to become, with an overridable top-level parameter-schema type.
func translatedChatRequestBody(t *testing.T, schemaType string) []byte {
	t.Helper()
	messages := make([]map[string]any, 0, len(smokeMessages()))
	for _, message := range smokeMessages() {
		messages = append(messages, map[string]any{"role": message.Role, "content": message.Content})
	}
	tools := make([]map[string]any, 0, len(smokeTools()))
	for _, tool := range smokeTools() {
		tools = append(tools, map[string]any{
			"type": tool.Type,
			"function": map[string]any{
				"name":        tool.Function.Name,
				"description": tool.Function.Description,
				"parameters": map[string]any{
					"type":       schemaType,
					"properties": tool.Function.Parameters["properties"],
				},
			},
		})
	}
	raw, err := json.Marshal(map[string]any{"model": smokeNative, "messages": messages, "tools": tools})
	if err != nil {
		t.Fatalf("marshal provider request fixture: %v", err)
	}
	return raw
}

// TestCheckTranslatedChatAssertsParameterSchemaType proves the translated tool
// schema keeps the fixture's top-level object type, not only its properties.
func TestCheckTranslatedChatAssertsParameterSchemaType(t *testing.T) {
	t.Parallel()
	redact := func(s string) string { return s }
	wantMessages := smokeMessages()
	wantTools := smokeTools()
	control := providerRequest{
		Method:     http.MethodPost,
		Path:       openAIChatPath,
		KeyMatched: true,
		Body:       translatedChatRequestBody(t, "object"),
	}
	if err := checkTranslatedChat(control, wantMessages, wantTools, redact); err != nil {
		t.Fatalf("correct object parameter schema rejected: %v", err)
	}
	corrupt := control
	corrupt.Body = translatedChatRequestBody(t, "string")
	err := checkTranslatedChat(corrupt, wantMessages, wantTools, redact)
	if err == nil {
		t.Fatal("parameters.type=string was accepted while the fixture supplies object")
	}
	for _, want := range []string{"parameters type", `"string"`, `"object"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %s", err, want)
		}
	}
}
