//go:build productsmoke

package productsmoke

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const (
	openAIResponsesPath   = "/v1/responses"
	openResponsesPath     = "/openresponses/v1/responses"
	anthropicMessagesPath = "/v1/messages"
	systemOneClientPath   = "/v1/systemone"
	geminiGeneratePrefix  = "/v1beta/models/"

	openAIResponsesFrontend = "openai-responses"
	anthropicFrontend       = "anthropic"
	geminiFrontend          = "gemini"
	openResponsesFrontend   = "openresponses"
	systemOneFrontend       = "systemone"

	kindSystemOneCompatible = "custom-systemone-compatible"
)

// startFamilySUT starts the proxy with the OpenAI legacy frontend (readiness
// probes need it) plus the frontend under test, routed to exactly one journey
// backend. Readiness keeps using its own dedicated backend and route, so the
// journey provider records only the client request under test.
func startFamilySUT(t *testing.T, canary, frontend, kind, baseURL string) *sut {
	t.Helper()
	return startSUT(t, sutConfig{
		Canary:    canary,
		Frontends: []string{"openai-legacy", frontend},
		Backends: []backendWiring{{
			ID:      smokeBackendID,
			Kind:    kind,
			BaseURL: baseURL,
			Models:  []modelWiring{{Canonical: smokeCanonical, Native: smokeNative}},
		}},
		DefaultRoute: smokeModel,
	})
}

// singleProviderRequest requires exactly one upstream request and returns it.
func singleProviderRequest(t *testing.T, fc failureContext, provider *fakeProvider) providerRequest {
	t.Helper()
	if got := provider.RequestCount(); got != 1 {
		failf(t, fc, "provider received %d requests, want exactly 1 (no retries)", got)
	}
	return provider.Requests()[0]
}

// openAIWireToolBody is the flat function-tool shape shared by the OpenAI
// Responses and OpenResponses frontends. The translation to the provider's
// nested chat tool shape is what the journey asserts.
func openAIWireToolBody() map[string]any {
	tool := smokeTools()[0]
	return map[string]any{
		"type":        "function",
		"name":        tool.Function.Name,
		"description": tool.Function.Description,
		"parameters":  tool.Function.Parameters,
	}
}

func openAIResponsesRequestBody(stream bool) []byte {
	raw, err := json.Marshal(map[string]any{
		"model":        smokeModel,
		"instructions": smokeSystemInstruction,
		"input":        smokeUserMessage,
		"tools":        []map[string]any{openAIWireToolBody()},
		"stream":       stream,
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func openResponsesRequestBody(stream bool) []byte {
	raw, err := json.Marshal(map[string]any{
		"model":        smokeModel,
		"instructions": smokeSystemInstruction,
		"input":        smokeUserMessage,
		"tools":        []map[string]any{openAIWireToolBody()},
		"store":        false,
		"stream":       stream,
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func anthropicMessagesRequestBody(stream bool) []byte {
	tool := smokeTools()[0]
	raw, err := json.Marshal(map[string]any{
		"model":      smokeModel,
		"max_tokens": 64,
		"system":     smokeSystemInstruction,
		"messages": []map[string]any{{
			"role":    "user",
			"content": smokeUserMessage,
		}},
		"tools": []map[string]any{{
			"name":         tool.Function.Name,
			"description":  tool.Function.Description,
			"input_schema": tool.Function.Parameters,
		}},
		"stream": stream,
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func geminiGenerateRequestBody() []byte {
	tool := smokeTools()[0]
	raw, err := json.Marshal(map[string]any{
		"systemInstruction": map[string]any{
			"parts": []map[string]any{{"text": smokeSystemInstruction}},
		},
		"contents": []map[string]any{{
			"role":  "user",
			"parts": []map[string]any{{"text": smokeUserMessage}},
		}},
		"tools": []map[string]any{{
			"functionDeclarations": []map[string]any{{
				"name":        tool.Function.Name,
				"description": tool.Function.Description,
				"parameters":  tool.Function.Parameters,
			}},
		}},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// systemOneRequestBody is the client decision request. Its state and questions
// are extracted again for the provider-side assertion, so the expected
// translation is exactly what the journey sent.
func systemOneRequestBody() []byte {
	raw, err := json.Marshal(map[string]any{
		"model": smokeModel,
		"state": map[string]any{
			"subject":    "product smoke",
			"facts":      []string{"alpha", "beta"},
			"confidence": 0.5,
		},
		"questions": map[string]any{
			"ship_it": map[string]any{
				"type":         "noul",
				"instructions": "Decide whether to ship the slice.",
				"criteria": map[string]any{
					"true":  "Ready to ship.",
					"false": "Not ready.",
				},
			},
			"next_step": map[string]any{
				"type":         "choice",
				"instructions": "Pick the next step.",
				"criteria": map[string]any{
					"ship": "Ship the slice.",
					"hold": "Hold for more work.",
				},
			},
			"quality": map[string]any{
				"type":     "score",
				"criteria": []string{"poor", "fair", "good"},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func systemOneTranslatedRequest(t *testing.T) (state, questions json.RawMessage) {
	t.Helper()
	var body struct {
		State     json.RawMessage `json:"state"`
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(systemOneRequestBody(), &body); err != nil {
		t.Fatalf("product smoke: decode system one request factory: %v", err)
	}
	return body.State, body.Questions
}

// agentUsage is the Responses-style usage object both the OpenAI Responses and
// OpenResponses wires expose.
type agentUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

func agentUsageOf(usage fakeUsage) agentUsage {
	return agentUsage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
	}
}

// agentResource is the completed response resource shared by the OpenAI
// Responses and OpenResponses protocols.
type agentResource struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	Status string `json:"status"`
	Model  string `json:"model"`
	Output []struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Status  string `json:"status"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage *agentUsage `json:"usage"`
}

// agentStreamEvent is one Responses-style SSE payload.
type agentStreamEvent struct {
	Type     string        `json:"type"`
	Response agentResource `json:"response"`
	Delta    string        `json:"delta"`
}

// agentText reads the assistant text out of a completed resource, requiring the
// exact message shape a scripted text answer produces.
func agentText(res agentResource) (string, error) {
	if len(res.Output) != 1 {
		return "", fmt.Errorf("response carried %d output items, want exactly 1 message", len(res.Output))
	}
	item := res.Output[0]
	if item.Type != "message" {
		return "", fmt.Errorf("output item type %q, want message", item.Type)
	}
	if item.Role != "assistant" {
		return "", fmt.Errorf("message role %q, want assistant", item.Role)
	}
	if item.Status != "completed" {
		return "", fmt.Errorf("message status %q, want completed", item.Status)
	}
	var b strings.Builder
	for i, part := range item.Content {
		if part.Type != "output_text" {
			return "", fmt.Errorf("content part %d type %q, want output_text", i, part.Type)
		}
		b.WriteString(part.Text)
	}
	if b.Len() == 0 {
		return "", errors.New("message carried no output_text content")
	}
	return b.String(), nil
}

// checkAgentStream observes the terminal event first, then its value, then the
// content and usage. It serves both Responses-style clients: OpenAI Responses
// and OpenResponses emit the same `response.completed` terminal and `[DONE]`
// dispatch delimiter for this journey.
func checkAgentStream(wire *clientWire, script fakeScript, redact redactor) error {
	if wire.Err != nil {
		return fmt.Errorf("no protocol-legal terminal: client exchange failed: %w", wire.Err)
	}
	if wire.Status != http.StatusOK {
		return fmt.Errorf("no protocol-legal terminal: status %d, want 200; body: %s", wire.Status, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if contentType := wire.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		return fmt.Errorf("no protocol-legal terminal: content type %q, want text/event-stream", contentType)
	}
	frames, err := parseSSEFrames(wire.Body)
	if err != nil {
		return fmt.Errorf("parse SSE: %w", err)
	}
	type dataEvent struct {
		name string
		data string
	}
	var events []dataEvent
	for _, frame := range frames {
		if frame.Data != "" {
			events = append(events, dataEvent{name: frame.Event, data: frame.Data})
		}
	}
	if len(events) == 0 {
		return errors.New("no protocol-legal terminal: no SSE data events observed")
	}
	if last := events[len(events)-1].data; last != "[DONE]" {
		return fmt.Errorf("no protocol-legal terminal: stream ended with %q, want the [DONE] terminal", excerpt(redact, last, wireFailureDetailExcerpt))
	}
	payloads := events[:len(events)-1]
	parsed := make([]agentStreamEvent, 0, len(payloads))
	types := make([]string, 0, len(payloads))
	terminalAt := -1
	for i, event := range payloads {
		var payload agentStreamEvent
		if err := json.Unmarshal([]byte(event.data), &payload); err != nil {
			return fmt.Errorf("event %d is not JSON: %v; payload: %s", i, err, excerpt(redact, event.data, wireFailureDetailExcerpt))
		}
		if payload.Type == "" {
			return fmt.Errorf("event %d carries no type", i)
		}
		if event.name != payload.Type {
			return fmt.Errorf("event %d is named %q but carries payload type %q", i, event.name, payload.Type)
		}
		types = append(types, payload.Type)
		switch payload.Type {
		case "response.completed", "response.incomplete", "response.failed":
			if terminalAt >= 0 {
				return fmt.Errorf("second terminal event %q at event %d after event %d", payload.Type, i, terminalAt)
			}
			terminalAt = i
		}
		parsed = append(parsed, payload)
	}
	if terminalAt < 0 {
		return fmt.Errorf("no protocol-legal terminal observed: no response.completed event in %d event(s) (%s)", len(payloads), eventSummary(types))
	}
	if terminalAt != len(parsed)-1 {
		return fmt.Errorf("event %q followed the terminal at event %d", parsed[terminalAt+1].Type, terminalAt)
	}
	terminal := parsed[terminalAt]
	if terminal.Type != "response.completed" {
		return fmt.Errorf("terminal event %q, want response.completed", terminal.Type)
	}
	if terminal.Response.Object != "response" {
		return fmt.Errorf("terminal response object %q, want response", terminal.Response.Object)
	}
	if terminal.Response.Status != "completed" {
		return fmt.Errorf("terminal response status %q, want completed", terminal.Response.Status)
	}
	text, err := agentText(terminal.Response)
	if err != nil {
		return fmt.Errorf("terminal response: %w", err)
	}
	var deltas []string
	for i := 0; i < terminalAt; i++ {
		if parsed[i].Type == "response.output_text.delta" {
			deltas = append(deltas, parsed[i].Delta)
		}
	}
	if len(deltas) == 0 {
		return errors.New("terminal was observed but no response.output_text.delta events preceded it")
	}
	if got, want := strings.Join(deltas, ""), text; got != want {
		return fmt.Errorf("streamed content %q, terminal content %q", got, want)
	}
	if got, want := text, script.content(); got != want {
		return fmt.Errorf("content %q, want %q", got, want)
	}
	if terminal.Response.Usage == nil {
		return errors.New("terminal response carries no usage")
	}
	if got, want := *terminal.Response.Usage, agentUsageOf(script.Usage); got != want {
		return fmt.Errorf("usage input=%d output=%d total=%d, want input=%d output=%d total=%d",
			got.InputTokens, got.OutputTokens, got.TotalTokens, want.InputTokens, want.OutputTokens, want.TotalTokens)
	}
	return nil
}

// checkAgentCollect observes the complete response, then its status, content
// and usage, for both Responses-style collect clients.
func checkAgentCollect(wire *clientWire, script fakeScript, redact redactor) error {
	if wire.Err != nil {
		return fmt.Errorf("no complete JSON terminal: client exchange failed: %w", wire.Err)
	}
	if wire.Status != http.StatusOK {
		return fmt.Errorf("no complete JSON terminal: status %d, want 200; body: %s", wire.Status, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if contentType := wire.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		return fmt.Errorf("no complete JSON terminal: content type %q, want application/json", contentType)
	}
	var res agentResource
	if err := json.Unmarshal(wire.Body, &res); err != nil {
		return fmt.Errorf("no complete JSON terminal: decode response: %v; body: %s", err, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if res.Object != "response" {
		return fmt.Errorf("response object %q, want response", res.Object)
	}
	if res.Status != "completed" {
		return fmt.Errorf("response status %q, want completed", res.Status)
	}
	text, err := agentText(res)
	if err != nil {
		return err
	}
	if got, want := text, script.content(); got != want {
		return fmt.Errorf("content %q, want %q", got, want)
	}
	if res.Usage == nil {
		return errors.New("collected response carries no usage")
	}
	if got, want := *res.Usage, agentUsageOf(script.Usage); got != want {
		return fmt.Errorf("usage input=%d output=%d total=%d, want input=%d output=%d total=%d",
			got.InputTokens, got.OutputTokens, got.TotalTokens, want.InputTokens, want.OutputTokens, want.TotalTokens)
	}
	return nil
}

func eventSummary(types []string) string {
	if len(types) == 0 {
		return "no events"
	}
	return strings.Join(types, ", ")
}

// anthropicUsageWire is the Anthropic usage object.
type anthropicUsageWire struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func anthropicUsageOf(usage fakeUsage) anthropicUsageWire {
	return anthropicUsageWire{InputTokens: usage.PromptTokens, OutputTokens: usage.CompletionTokens}
}

type anthropicStreamPayload struct {
	Type    string `json:"type"`
	Message struct {
		Role  string             `json:"role"`
		Usage anthropicUsageWire `json:"usage"`
	} `json:"message"`
	Delta struct {
		Type       string  `json:"type"`
		Text       string  `json:"text"`
		StopReason *string `json:"stop_reason"`
	} `json:"delta"`
	Usage anthropicUsageWire `json:"usage"`
}

// checkAnthropicStream observes message_stop first, then the stop reason, the
// text content and the final usage.
func checkAnthropicStream(wire *clientWire, script fakeScript, redact redactor) error {
	if wire.Err != nil {
		return fmt.Errorf("no protocol-legal terminal: client exchange failed: %w", wire.Err)
	}
	if wire.Status != http.StatusOK {
		return fmt.Errorf("no protocol-legal terminal: status %d, want 200; body: %s", wire.Status, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if contentType := wire.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		return fmt.Errorf("no protocol-legal terminal: content type %q, want text/event-stream", contentType)
	}
	frames, err := parseSSEFrames(wire.Body)
	if err != nil {
		return fmt.Errorf("parse SSE: %w", err)
	}
	type event struct {
		name    string
		payload anthropicStreamPayload
	}
	events := make([]event, 0, len(frames))
	types := make([]string, 0, len(frames))
	terminalAt := -1
	deltaAt := -1
	for i, frame := range frames {
		if frame.Data == "" {
			continue
		}
		var payload anthropicStreamPayload
		if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
			return fmt.Errorf("event %d is not JSON: %v; payload: %s", i, err, excerpt(redact, frame.Data, wireFailureDetailExcerpt))
		}
		if payload.Type == "" {
			return fmt.Errorf("event %d carries no type", i)
		}
		if frame.Event != payload.Type {
			return fmt.Errorf("event %d is named %q but carries payload type %q", i, frame.Event, payload.Type)
		}
		types = append(types, payload.Type)
		switch payload.Type {
		case "message_stop":
			if terminalAt >= 0 {
				return fmt.Errorf("second message_stop terminal at event %d after event %d", i, terminalAt)
			}
			terminalAt = i
		case "message_delta":
			if deltaAt >= 0 {
				return fmt.Errorf("second message_delta at event %d after event %d", i, deltaAt)
			}
			deltaAt = i
		}
		events = append(events, event{name: frame.Event, payload: payload})
	}
	if terminalAt < 0 {
		return fmt.Errorf("no protocol-legal terminal observed: no message_stop event in %d event(s) (%s)", len(events), eventSummary(types))
	}
	if terminalAt != len(events)-1 {
		return fmt.Errorf("event %q followed the message_stop terminal at event %d", events[terminalAt+1].name, terminalAt)
	}
	if deltaAt < 0 {
		return errors.New("no message_delta carried the terminal stop reason before message_stop")
	}
	if deltaAt != terminalAt-1 {
		return fmt.Errorf("message_delta was event %d, want the event immediately before message_stop at %d", deltaAt, terminalAt)
	}
	stop := events[deltaAt].payload.Delta.StopReason
	if stop == nil {
		return errors.New("message_delta carried no stop_reason terminal")
	}
	if got, want := *stop, script.finishReason(familyAnthropicMessages); got != want {
		return fmt.Errorf("terminal stop_reason %q, want %q", got, want)
	}
	var text strings.Builder
	for i := 0; i < deltaAt; i++ {
		payload := events[i].payload
		if payload.Type != "content_block_delta" {
			continue
		}
		if payload.Delta.Type != "text_delta" {
			continue
		}
		text.WriteString(payload.Delta.Text)
	}
	if text.Len() == 0 {
		return errors.New("terminal was observed but no text_delta content preceded it")
	}
	if got, want := text.String(), script.content(); got != want {
		return fmt.Errorf("content %q, want %q", got, want)
	}
	if got, want := events[deltaAt].payload.Usage, anthropicUsageOf(script.Usage); got != want {
		return fmt.Errorf("usage input=%d output=%d, want input=%d output=%d", got.InputTokens, got.OutputTokens, want.InputTokens, want.OutputTokens)
	}
	return nil
}

type anthropicMessageWire struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason *string            `json:"stop_reason"`
	Usage      anthropicUsageWire `json:"usage"`
}

// checkAnthropicCollect observes the complete message, then its stop reason,
// content and usage.
func checkAnthropicCollect(wire *clientWire, script fakeScript, redact redactor) error {
	if wire.Err != nil {
		return fmt.Errorf("no complete JSON terminal: client exchange failed: %w", wire.Err)
	}
	if wire.Status != http.StatusOK {
		return fmt.Errorf("no complete JSON terminal: status %d, want 200; body: %s", wire.Status, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if contentType := wire.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		return fmt.Errorf("no complete JSON terminal: content type %q, want application/json", contentType)
	}
	var message anthropicMessageWire
	if err := json.Unmarshal(wire.Body, &message); err != nil {
		return fmt.Errorf("no complete JSON terminal: decode message: %v; body: %s", err, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if message.Type != "message" {
		return fmt.Errorf("message type %q, want message", message.Type)
	}
	if message.Role != "assistant" {
		return fmt.Errorf("message role %q, want assistant", message.Role)
	}
	if message.StopReason == nil {
		return errors.New("collected message carries no stop_reason terminal")
	}
	if got, want := *message.StopReason, script.finishReason(familyAnthropicMessages); got != want {
		return fmt.Errorf("terminal stop_reason %q, want %q", got, want)
	}
	var text strings.Builder
	for _, part := range message.Content {
		if part.Type != "text" {
			return fmt.Errorf("content block type %q, want text", part.Type)
		}
		text.WriteString(part.Text)
	}
	if text.Len() == 0 {
		return errors.New("collected message carries no text content")
	}
	if got, want := text.String(), script.content(); got != want {
		return fmt.Errorf("content %q, want %q", got, want)
	}
	if got, want := message.Usage, anthropicUsageOf(script.Usage); got != want {
		return fmt.Errorf("usage input=%d output=%d, want input=%d output=%d", got.InputTokens, got.OutputTokens, want.InputTokens, want.OutputTokens)
	}
	return nil
}

// geminiUsageWire is the Gemini usageMetadata object.
type geminiUsageWire struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

func geminiUsageOf(usage fakeUsage) geminiUsageWire {
	return geminiUsageWire{
		PromptTokenCount:     usage.PromptTokens,
		CandidatesTokenCount: usage.CompletionTokens,
		TotalTokenCount:      usage.TotalTokens,
	}
}

type geminiChunk struct {
	Candidates []struct {
		Content struct {
			Role  string `json:"role"`
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *geminiUsageWire `json:"usageMetadata"`
}

// checkGeminiStream observes the finishReason terminal first, then content and
// usage.
func checkGeminiStream(wire *clientWire, script fakeScript, redact redactor) error {
	if wire.Err != nil {
		return fmt.Errorf("no protocol-legal terminal: client exchange failed: %w", wire.Err)
	}
	if wire.Status != http.StatusOK {
		return fmt.Errorf("no protocol-legal terminal: status %d, want 200; body: %s", wire.Status, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if contentType := wire.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		return fmt.Errorf("no protocol-legal terminal: content type %q, want text/event-stream", contentType)
	}
	frames, err := parseSSEFrames(wire.Body)
	if err != nil {
		return fmt.Errorf("parse SSE: %w", err)
	}
	chunks := make([]geminiChunk, 0, len(frames))
	for i, frame := range frames {
		if frame.Data == "" {
			continue
		}
		var chunk geminiChunk
		if err := json.Unmarshal([]byte(frame.Data), &chunk); err != nil {
			return fmt.Errorf("chunk %d is not JSON: %v; payload: %s", i, err, excerpt(redact, frame.Data, wireFailureDetailExcerpt))
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		return errors.New("no protocol-legal terminal: no SSE data chunks observed")
	}
	terminalAt := -1
	for i, chunk := range chunks {
		if len(chunk.Candidates) > 0 && chunk.Candidates[0].FinishReason != "" {
			if terminalAt >= 0 {
				return fmt.Errorf("second finishReason terminal at chunk %d after chunk %d", i, terminalAt)
			}
			terminalAt = i
		}
	}
	if terminalAt < 0 {
		return fmt.Errorf("no protocol-legal terminal observed: no finishReason in %d chunk(s)", len(chunks))
	}
	terminal := chunks[terminalAt]
	if len(terminal.Candidates) != 1 {
		return fmt.Errorf("terminal chunk carried %d candidates, want 1", len(terminal.Candidates))
	}
	// Gemini reports its own finish-reason vocabulary: a clean scripted turn is
	// STOP, not the upstream chat backend's "stop".
	if got, want := terminal.Candidates[0].FinishReason, "STOP"; got != want {
		return fmt.Errorf("terminal finishReason %q, want %q", got, want)
	}
	for i := terminalAt + 1; i < len(chunks); i++ {
		if len(chunks[i].Candidates) > 0 {
			return fmt.Errorf("candidate content followed the terminal at chunk %d", i)
		}
	}
	var text strings.Builder
	for i := 0; i < terminalAt; i++ {
		for _, candidate := range chunks[i].Candidates {
			for j, part := range candidate.Content.Parts {
				if part.Thought {
					return fmt.Errorf("chunk %d part %d is reasoning content, want assistant text", i, j)
				}
				text.WriteString(part.Text)
			}
		}
	}
	if text.Len() == 0 {
		return errors.New("terminal was observed but no text content preceded it")
	}
	if got, want := text.String(), script.content(); got != want {
		return fmt.Errorf("content %q, want %q", got, want)
	}
	var usage *geminiUsageWire
	for i := terminalAt + 1; i < len(chunks); i++ {
		if chunks[i].UsageMetadata == nil {
			continue
		}
		if usage != nil {
			return fmt.Errorf("second usageMetadata chunk at chunk %d", i)
		}
		usage = chunks[i].UsageMetadata
	}
	if usage == nil {
		return errors.New("terminal was observed but no usageMetadata chunk followed it")
	}
	wantUsage := geminiUsageOf(script.Usage)
	if *usage != wantUsage {
		return fmt.Errorf("usage prompt=%d candidates=%d total=%d, want prompt=%d candidates=%d total=%d",
			usage.PromptTokenCount, usage.CandidatesTokenCount, usage.TotalTokenCount,
			wantUsage.PromptTokenCount, wantUsage.CandidatesTokenCount, wantUsage.TotalTokenCount)
	}
	return nil
}

// checkGeminiCollect observes the complete generateContent response, then its
// finishReason, content and usage.
func checkGeminiCollect(wire *clientWire, script fakeScript, redact redactor) error {
	if wire.Err != nil {
		return fmt.Errorf("no complete JSON terminal: client exchange failed: %w", wire.Err)
	}
	if wire.Status != http.StatusOK {
		return fmt.Errorf("no complete JSON terminal: status %d, want 200; body: %s", wire.Status, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if contentType := wire.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		return fmt.Errorf("no complete JSON terminal: content type %q, want application/json", contentType)
	}
	var chunk geminiChunk
	if err := json.Unmarshal(wire.Body, &chunk); err != nil {
		return fmt.Errorf("no complete JSON terminal: decode response: %v; body: %s", err, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if len(chunk.Candidates) != 1 {
		return fmt.Errorf("response carried %d candidates, want 1", len(chunk.Candidates))
	}
	candidate := chunk.Candidates[0]
	if candidate.FinishReason == "" {
		return errors.New("collected response carries no finishReason terminal")
	}
	if got, want := candidate.FinishReason, "STOP"; got != want {
		return fmt.Errorf("terminal finishReason %q, want %q", got, want)
	}
	if candidate.Content.Role != "model" {
		return fmt.Errorf("candidate role %q, want model", candidate.Content.Role)
	}
	var text strings.Builder
	for i, part := range candidate.Content.Parts {
		if part.Thought {
			return fmt.Errorf("part %d is reasoning content, want assistant text", i)
		}
		text.WriteString(part.Text)
	}
	if text.Len() == 0 {
		return errors.New("collected response carries no text content")
	}
	if got, want := text.String(), script.content(); got != want {
		return fmt.Errorf("content %q, want %q", got, want)
	}
	if chunk.UsageMetadata == nil {
		return errors.New("collected response carries no usageMetadata")
	}
	wantUsage := geminiUsageOf(script.Usage)
	if *chunk.UsageMetadata != wantUsage {
		return fmt.Errorf("usage prompt=%d candidates=%d total=%d, want prompt=%d candidates=%d total=%d",
			chunk.UsageMetadata.PromptTokenCount, chunk.UsageMetadata.CandidatesTokenCount, chunk.UsageMetadata.TotalTokenCount,
			wantUsage.PromptTokenCount, wantUsage.CandidatesTokenCount, wantUsage.TotalTokenCount)
	}
	return nil
}

// checkProviderIdentity asserts the request reached the expected provider path
// with the canary credential.
func checkProviderIdentity(req providerRequest, path string) error {
	if req.Method != http.MethodPost {
		return fmt.Errorf("provider saw method %s, want POST", req.Method)
	}
	if req.Path != path {
		return fmt.Errorf("provider saw path %q, want %q", req.Path, path)
	}
	if !req.KeyMatched {
		return fmt.Errorf("provider did not receive the canary API key in %s", req.KeyHeader)
	}
	return nil
}

// translatedAnthropic is the provider-side view of a Messages API request.
type translatedAnthropic struct {
	Model    string          `json:"model"`
	System   json.RawMessage `json:"system"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	} `json:"tools"`
}

// anthropicContentText accepts both content shapes the SDK emits: a plain
// string and an array of text blocks.
func anthropicContentText(raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", errors.New("no content")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", err
		}
		return text, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, block := range blocks {
		if block.Type != "text" {
			return "", fmt.Errorf("block %d type %q, want text", i, block.Type)
		}
		b.WriteString(block.Text)
	}
	if b.Len() == 0 {
		return "", errors.New("no text blocks")
	}
	return b.String(), nil
}

// checkTranslatedAnthropic asserts the Messages request the provider received:
// the translated native model, the system instruction, the user turn and the
// tool declaration.
func checkTranslatedAnthropic(req providerRequest, wantModel, wantPath, wantSystem, wantUser string, wantTools []chatTool, redact redactor) error {
	if err := checkProviderIdentity(req, wantPath); err != nil {
		return err
	}
	var got translatedAnthropic
	if err := json.Unmarshal(req.Body, &got); err != nil {
		return fmt.Errorf("provider request body is not JSON: %v", err)
	}
	if got.Model != wantModel {
		return fmt.Errorf("provider saw model %q, want the translated native id %q", got.Model, wantModel)
	}
	system, err := anthropicContentText(got.System)
	if err != nil {
		return fmt.Errorf("provider system instruction: %v", err)
	}
	if system != wantSystem {
		return fmt.Errorf("provider system instruction %q, want %q", system, wantSystem)
	}
	if len(got.Messages) != 1 {
		return fmt.Errorf("provider saw %d messages, want 1", len(got.Messages))
	}
	if role := got.Messages[0].Role; role != "user" {
		return fmt.Errorf("provider message role %q, want user", role)
	}
	user, err := anthropicContentText(got.Messages[0].Content)
	if err != nil {
		return fmt.Errorf("provider user message: %v", err)
	}
	if user != wantUser {
		return fmt.Errorf("provider user message %q, want %q", user, wantUser)
	}
	if len(got.Tools) != len(wantTools) {
		return fmt.Errorf("provider saw %d tools, want %d", len(got.Tools), len(wantTools))
	}
	for i, want := range wantTools {
		tool := got.Tools[i]
		if tool.Name != want.Function.Name {
			return fmt.Errorf("tool %d name %q, want %q", i, tool.Name, want.Function.Name)
		}
		if tool.Description != want.Function.Description {
			return fmt.Errorf("tool %d description %q, want %q", i, tool.Description, want.Function.Description)
		}
		var schema struct {
			Type       string `json:"type"`
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			return fmt.Errorf("tool %d input_schema is not JSON: %v", i, err)
		}
		wantType, ok := want.Function.Parameters["type"].(string)
		if !ok || wantType == "" {
			return fmt.Errorf("tool %d fixture parameters carry no top-level string type", i)
		}
		if schema.Type != wantType {
			return fmt.Errorf("tool %d input_schema type %q, want %q: %s", i, schema.Type, wantType, excerpt(redact, string(tool.InputSchema), wireFailureDetailExcerpt))
		}
		location, ok := schema.Properties["location"]
		if !ok || location.Type != "string" {
			return fmt.Errorf("tool %d input_schema lost properties.location.type=string: %s", i, excerpt(redact, string(tool.InputSchema), wireFailureDetailExcerpt))
		}
	}
	return nil
}

// checkTranslatedSystemOne asserts the decision request the provider received:
// the translated native model, the raw state and the ordered questions.
func checkTranslatedSystemOne(req providerRequest, wantModel string, wantState, wantQuestions json.RawMessage, redact redactor) error {
	if err := checkProviderIdentity(req, systemOneUpstreamSuffix); err != nil {
		return err
	}
	var got struct {
		Model     string          `json:"model"`
		State     json.RawMessage `json:"state"`
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(req.Body, &got); err != nil {
		return fmt.Errorf("provider request body is not JSON: %v", err)
	}
	if got.Model != wantModel {
		return fmt.Errorf("provider saw model %q, want the translated native id %q", got.Model, wantModel)
	}
	if err := checkJSONEqual("state", got.State, wantState, redact); err != nil {
		return err
	}
	if err := checkJSONEqual("questions", got.Questions, wantQuestions, redact); err != nil {
		return err
	}
	return nil
}

// checkJSONEqual compares two JSON documents semantically.
func checkJSONEqual(field string, got, want json.RawMessage, redact redactor) error {
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		return fmt.Errorf("%s is not JSON: %v", field, err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		return fmt.Errorf("expected %s is not JSON: %v", field, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		return fmt.Errorf("%s %s, want %s", field,
			excerpt(redact, string(got), wireFailureBodyExcerpt),
			excerpt(redact, string(want), wireFailureBodyExcerpt))
	}
	return nil
}

// systemOneAnswer is the client-side view of one decision answer.
type systemOneAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul"`
	Choice        string             `json:"choice"`
	Score         *float64           `json:"score"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    *float64           `json:"confidence"`
	Legend        map[string]string  `json:"legend"`
}

// checkSystemOneCollect observes the complete decision response (its terminal),
// then the typed answers and usage.
func checkSystemOneCollect(wire *clientWire, script fakeScript, redact redactor) error {
	if wire.Err != nil {
		return fmt.Errorf("no complete JSON terminal: client exchange failed: %w", wire.Err)
	}
	if wire.Status != http.StatusOK {
		return fmt.Errorf("no complete JSON terminal: status %d, want 200; body: %s", wire.Status, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if contentType := wire.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		return fmt.Errorf("no complete JSON terminal: content type %q, want application/json", contentType)
	}
	var response struct {
		Model   string                     `json:"model"`
		Answers map[string]systemOneAnswer `json:"answers"`
		Usage   struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(wire.Body, &response); err != nil {
		return fmt.Errorf("no complete JSON terminal: decode decision response: %v; body: %s", err, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if len(response.Answers) != 3 {
		return fmt.Errorf("decision response carried %d answers, want the complete set of 3", len(response.Answers))
	}
	ship, ok := response.Answers["ship_it"]
	if !ok {
		return errors.New("decision response is missing the ship_it answer terminal")
	}
	if ship.Type != "noul" || ship.Noul == nil || *ship.Noul != 0.75 {
		return fmt.Errorf("ship_it answer %+v, want type=noul noul=0.75", ship)
	}
	next, ok := response.Answers["next_step"]
	if !ok {
		return errors.New("decision response is missing the next_step answer terminal")
	}
	if next.Type != "choice" || next.Choice != "ship" {
		return fmt.Errorf("next_step answer %+v, want type=choice choice=ship", next)
	}
	if next.Probabilities["ship"] != 0.7 || next.Probabilities["hold"] != 0.3 {
		return fmt.Errorf("next_step probabilities %v, want ship=0.7 hold=0.3", next.Probabilities)
	}
	if next.Confidence == nil || *next.Confidence != 0.7 {
		return fmt.Errorf("next_step confidence %v, want 0.7", next.Confidence)
	}
	quality, ok := response.Answers["quality"]
	if !ok {
		return errors.New("decision response is missing the quality answer terminal")
	}
	if quality.Type != "score" || quality.Score == nil || *quality.Score != 2 {
		return fmt.Errorf("quality answer %+v, want type=score score=2", quality)
	}
	if quality.Probabilities["0"] != 0.1 || quality.Probabilities["1"] != 0.2 || quality.Probabilities["2"] != 0.7 {
		return fmt.Errorf("quality probabilities %v, want 0=0.1 1=0.2 2=0.7", quality.Probabilities)
	}
	if quality.Legend["0"] != "poor" || quality.Legend["1"] != "fair" || quality.Legend["2"] != "good" {
		return fmt.Errorf("quality legend %v, want 0=poor 1=fair 2=good", quality.Legend)
	}
	if response.Model != script.Model {
		return fmt.Errorf("decision response model %q, want %q", response.Model, script.Model)
	}
	if got, want := response.Usage.InputTokens, script.Usage.PromptTokens; got != want {
		return fmt.Errorf("usage input_tokens=%d, want %d", got, want)
	}
	if got, want := response.Usage.OutputTokens, script.Usage.CompletionTokens; got != want {
		return fmt.Errorf("usage output_tokens=%d, want %d", got, want)
	}
	return nil
}

// testOpenAIResponsesStream drives the Responses frontend to an OpenAI legacy
// chat backend: a cross-protocol pairing.
func testOpenAIResponsesStream(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	script := smokeScript()
	provider := newOpenAILegacyProvider(t, canary, script)
	h := startFamilySUT(t, canary, openAIResponsesFrontend, kindOpenAILegacyCompatible, provider.OpenAILegacyBaseURL())
	wire := doJSON(t.Context(), h.BaseURL()+openAIResponsesPath, openAIResponsesRequestBody(true))
	failures := failureContext{sut: h, provider: provider, wire: wire}
	if err := checkAgentStream(wire, script, h.evidence.redact); err != nil {
		failf(t, failures, "openai-responses stream: %v", err)
	}
	request := singleProviderRequest(t, failures, provider)
	if err := checkTranslatedOpenAIChat(request, smokeNative, openAIChatPath, smokeMessages(), smokeTools(), h.evidence.redact); err != nil {
		failf(t, failures, "openai-responses stream: %v", err)
	}
	t.Logf("openai-responses stream passed: %d provider request(s), evidence %s", provider.RequestCount(), h.evidence.dir)
}

// testOpenAIResponsesCollect drives the Responses frontend to an OpenAI legacy
// chat backend: a cross-protocol pairing.
func testOpenAIResponsesCollect(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	script := smokeScript()
	provider := newOpenAILegacyProvider(t, canary, script)
	h := startFamilySUT(t, canary, openAIResponsesFrontend, kindOpenAILegacyCompatible, provider.OpenAILegacyBaseURL())
	wire := doJSON(t.Context(), h.BaseURL()+openAIResponsesPath, openAIResponsesRequestBody(false))
	failures := failureContext{sut: h, provider: provider, wire: wire}
	if err := checkAgentCollect(wire, script, h.evidence.redact); err != nil {
		failf(t, failures, "openai-responses collect: %v", err)
	}
	request := singleProviderRequest(t, failures, provider)
	if err := checkTranslatedOpenAIChat(request, smokeNative, openAIChatPath, smokeMessages(), smokeTools(), h.evidence.redact); err != nil {
		failf(t, failures, "openai-responses collect: %v", err)
	}
	t.Logf("openai-responses collect passed: %d provider request(s), evidence %s", provider.RequestCount(), h.evidence.dir)
}

// testAnthropicStream drives the Anthropic frontend to an Anthropic backend:
// the same-family pairing.
func testAnthropicStream(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	script := smokeScript()
	provider := newAnthropicMessagesProvider(t, canary, script)
	h := startFamilySUT(t, canary, anthropicFrontend, kindAnthropicCompatible, provider.AnthropicBaseURL())
	wire := doJSON(t.Context(), h.BaseURL()+anthropicMessagesPath, anthropicMessagesRequestBody(true))
	failures := failureContext{sut: h, provider: provider, wire: wire}
	if err := checkAnthropicStream(wire, script, h.evidence.redact); err != nil {
		failf(t, failures, "anthropic stream: %v", err)
	}
	request := singleProviderRequest(t, failures, provider)
	if err := checkTranslatedAnthropic(request, smokeNative, anthropicMessagesPath, smokeSystemInstruction, smokeUserMessage, smokeTools(), h.evidence.redact); err != nil {
		failf(t, failures, "anthropic stream: %v", err)
	}
	t.Logf("anthropic stream passed: %d provider request(s), evidence %s", provider.RequestCount(), h.evidence.dir)
}

func testAnthropicCollect(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	script := smokeScript()
	provider := newAnthropicMessagesProvider(t, canary, script)
	h := startFamilySUT(t, canary, anthropicFrontend, kindAnthropicCompatible, provider.AnthropicBaseURL())
	wire := doJSON(t.Context(), h.BaseURL()+anthropicMessagesPath, anthropicMessagesRequestBody(false))
	failures := failureContext{sut: h, provider: provider, wire: wire}
	if err := checkAnthropicCollect(wire, script, h.evidence.redact); err != nil {
		failf(t, failures, "anthropic collect: %v", err)
	}
	request := singleProviderRequest(t, failures, provider)
	if err := checkTranslatedAnthropic(request, smokeNative, anthropicMessagesPath, smokeSystemInstruction, smokeUserMessage, smokeTools(), h.evidence.redact); err != nil {
		failf(t, failures, "anthropic collect: %v", err)
	}
	t.Logf("anthropic collect passed: %d provider request(s), evidence %s", provider.RequestCount(), h.evidence.dir)
}

func geminiURL(h *sut, method string) string {
	url := h.BaseURL() + geminiGeneratePrefix + smokeModel + ":" + method
	if method == "streamGenerateContent" {
		url += "?alt=sse"
	}
	return url
}

// testGeminiStream drives the Gemini frontend to an Anthropic backend: a
// cross-protocol pairing.
func testGeminiStream(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	script := smokeScript()
	provider := newAnthropicMessagesProvider(t, canary, script)
	h := startFamilySUT(t, canary, geminiFrontend, kindAnthropicCompatible, provider.AnthropicBaseURL())
	wire := doJSON(t.Context(), geminiURL(h, "streamGenerateContent"), geminiGenerateRequestBody())
	failures := failureContext{sut: h, provider: provider, wire: wire}
	if err := checkGeminiStream(wire, script, h.evidence.redact); err != nil {
		failf(t, failures, "gemini stream: %v", err)
	}
	request := singleProviderRequest(t, failures, provider)
	if err := checkTranslatedAnthropic(request, smokeNative, anthropicMessagesPath, smokeSystemInstruction, smokeUserMessage, smokeTools(), h.evidence.redact); err != nil {
		failf(t, failures, "gemini stream: %v", err)
	}
	t.Logf("gemini stream passed: %d provider request(s), evidence %s", provider.RequestCount(), h.evidence.dir)
}

// testGeminiCollect drives the Gemini frontend to an Anthropic backend: a
// cross-protocol pairing.
func testGeminiCollect(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	script := smokeScript()
	provider := newAnthropicMessagesProvider(t, canary, script)
	h := startFamilySUT(t, canary, geminiFrontend, kindAnthropicCompatible, provider.AnthropicBaseURL())
	wire := doJSON(t.Context(), geminiURL(h, "generateContent"), geminiGenerateRequestBody())
	failures := failureContext{sut: h, provider: provider, wire: wire}
	if err := checkGeminiCollect(wire, script, h.evidence.redact); err != nil {
		failf(t, failures, "gemini collect: %v", err)
	}
	request := singleProviderRequest(t, failures, provider)
	if err := checkTranslatedAnthropic(request, smokeNative, anthropicMessagesPath, smokeSystemInstruction, smokeUserMessage, smokeTools(), h.evidence.redact); err != nil {
		failf(t, failures, "gemini collect: %v", err)
	}
	t.Logf("gemini collect passed: %d provider request(s), evidence %s", provider.RequestCount(), h.evidence.dir)
}

// testOpenResponsesStream drives the OpenResponses frontend to an OpenAI legacy
// chat backend: a cross-protocol pairing.
func testOpenResponsesStream(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	script := smokeScript()
	provider := newOpenAILegacyProvider(t, canary, script)
	h := startFamilySUT(t, canary, openResponsesFrontend, kindOpenAILegacyCompatible, provider.OpenAILegacyBaseURL())
	wire := doJSON(t.Context(), h.BaseURL()+openResponsesPath, openResponsesRequestBody(true))
	failures := failureContext{sut: h, provider: provider, wire: wire}
	if err := checkAgentStream(wire, script, h.evidence.redact); err != nil {
		failf(t, failures, "openresponses stream: %v", err)
	}
	request := singleProviderRequest(t, failures, provider)
	if err := checkTranslatedOpenAIChat(request, smokeNative, openAIChatPath, smokeMessages(), smokeTools(), h.evidence.redact); err != nil {
		failf(t, failures, "openresponses stream: %v", err)
	}
	t.Logf("openresponses stream passed: %d provider request(s), evidence %s", provider.RequestCount(), h.evidence.dir)
}

// testOpenResponsesCollect drives the OpenResponses frontend to an OpenAI
// legacy chat backend: a cross-protocol pairing.
func testOpenResponsesCollect(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	script := smokeScript()
	provider := newOpenAILegacyProvider(t, canary, script)
	h := startFamilySUT(t, canary, openResponsesFrontend, kindOpenAILegacyCompatible, provider.OpenAILegacyBaseURL())
	wire := doJSON(t.Context(), h.BaseURL()+openResponsesPath, openResponsesRequestBody(false))
	failures := failureContext{sut: h, provider: provider, wire: wire}
	if err := checkAgentCollect(wire, script, h.evidence.redact); err != nil {
		failf(t, failures, "openresponses collect: %v", err)
	}
	request := singleProviderRequest(t, failures, provider)
	if err := checkTranslatedOpenAIChat(request, smokeNative, openAIChatPath, smokeMessages(), smokeTools(), h.evidence.redact); err != nil {
		failf(t, failures, "openresponses collect: %v", err)
	}
	t.Logf("openresponses collect passed: %d provider request(s), evidence %s", provider.RequestCount(), h.evidence.dir)
}

// testSystemOneCollect drives the SystemOne frontend to the decision-wire
// backend. The shipped SystemOne frontend only serves non-streaming decisions,
// so there is deliberately no stream journey (owner decision, task 4).
func testSystemOneCollect(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	script := fakeScript{
		Model:  smokeNative,
		Deltas: []string{"decision"},
		Usage:  fakeUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
	}
	provider := newSystemOneProvider(t, canary, script)
	h := startFamilySUT(t, canary, systemOneFrontend, kindSystemOneCompatible, provider.SystemOneBaseURL())
	wire := doJSON(t.Context(), h.BaseURL()+systemOneClientPath, systemOneRequestBody())
	failures := failureContext{sut: h, provider: provider, wire: wire}
	if err := checkSystemOneCollect(wire, script, h.evidence.redact); err != nil {
		failf(t, failures, "systemone collect: %v", err)
	}
	request := singleProviderRequest(t, failures, provider)
	state, questions := systemOneTranslatedRequest(t)
	if err := checkTranslatedSystemOne(request, smokeNative, state, questions, h.evidence.redact); err != nil {
		failf(t, failures, "systemone collect: %v", err)
	}
	t.Logf("systemone collect passed: %d provider request(s), evidence %s", provider.RequestCount(), h.evidence.dir)
}
