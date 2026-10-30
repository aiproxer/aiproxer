//go:build productsmoke

package productsmoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// wireFamily is the upstream wire protocol a fake provider speaks.
type wireFamily string

const (
	familyOpenAILegacy      wireFamily = "openai-legacy"
	familyAnthropicMessages wireFamily = "anthropic-messages"
	familySystemOne         wireFamily = "system-one"
)

const (
	openAIChatCompletionsSuffix = "/chat/completions"
	anthropicMessagesSuffix     = "/v1/messages"
	systemOneUpstreamSuffix     = "/systemone"
)

// fakeUsage is the scripted upstream token usage.
type fakeUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// fakeToolCall scripts one OpenAI chat tool call. The provider emits the call
// start with its id and name, then ArgumentDeltas as function.arguments
// fragments, and finishes the stream with finish_reason=tool_calls.
type fakeToolCall struct {
	ID        string
	Name      string
	Arguments string
	// ArgumentDeltas splits Arguments across streamed argument fragments. Empty
	// emits the whole Arguments value as one delta.
	ArgumentDeltas []string
}

// argumentDeltas returns the scripted argument fragments, defaulting to the
// complete arguments in one fragment.
func (c fakeToolCall) argumentDeltas() []string {
	if len(c.ArgumentDeltas) > 0 {
		return c.ArgumentDeltas
	}
	return []string{c.Arguments}
}

// fakeScript is the scripted upstream answer.
type fakeScript struct {
	// Model is the upstream model id echoed in wire payloads.
	Model string
	// Deltas are the streamed text deltas; their join is the full content.
	Deltas []string
	Usage  fakeUsage
	// FinishReason defaults to "stop" for OpenAI and "end_turn" for Anthropic,
	// and to "tool_calls" when ToolCall is set.
	FinishReason string
	// ToolCall scripts a streaming OpenAI chat tool call instead of text. It is
	// deliberately OpenAI-chat-only: the continuity journey runs on that wire.
	ToolCall *fakeToolCall
	// OmitTerminal scripts a response without its wire terminal: no finish
	// chunk or DONE for a stream, no finish_reason/stop_reason for a collected
	// response, and no answers for the System One decision wire. It exists for
	// the non-vacuity probes that prove a journey's terminal assertion is live;
	// every committed journey leaves it false.
	OmitTerminal bool
	// TruncateBody scripts a collected response whose JSON stops mid-object, so
	// the client receives no complete JSON terminal. It exists for the same
	// non-vacuity probes; every committed journey leaves it false.
	TruncateBody bool
}

func (s fakeScript) content() string { return strings.Join(s.Deltas, "") }

func (s fakeScript) finishReason(family wireFamily) string {
	if s.FinishReason != "" {
		return s.FinishReason
	}
	if family == familyAnthropicMessages {
		return "end_turn"
	}
	if s.ToolCall != nil {
		return "tool_calls"
	}
	return "stop"
}

// providerFault selects a scripted upstream failure.
type providerFault string

const (
	faultNone                providerFault = ""
	faultHTTPErrorBeforeBody providerFault = "http-error-before-body"
	faultAbortAfterEvents    providerFault = "abort-after-stream-events"
	faultStallUntilCancelled providerFault = "stall-until-cancelled"
)

const (
	providerBodyLimit = 1 << 20
	stallBound        = 30 * time.Second
)

// errProviderBodyIncomplete marks a request body the fake provider could not
// read to the end. Its bytes are never persisted, because the discarded suffix
// can complete a credential whose prefix remains in the retained bytes.
var errProviderBodyIncomplete = errors.New("provider request body exceeded the capture bound")

// providerRequest is one recorded upstream request.
type providerRequest struct {
	Method string
	Path   string
	Header http.Header
	// Body is the retained request body. It is empty when BodyOmitted is set.
	Body []byte
	// BodyOmitted explains why an incomplete body was not retained; it replaces
	// the body in every rendered request log.
	BodyOmitted string
	KeyHeader   string
	KeyMatched  bool
	At          time.Time
}

// readProviderBody reads at most one byte past the provider capture bound, so an
// oversized body is recognised explicitly instead of being silently truncated.
// A body that is incomplete for any reason is not retained: the discarded bytes
// may complete a credential whose prefix stays in the retained bytes, and
// exact-match redaction cannot recognise that fragment. It returns the safe
// reason to record in place of the body.
func readProviderBody(r io.Reader) ([]byte, string, error) {
	raw, readErr := io.ReadAll(io.LimitReader(r, providerBodyLimit+1))
	if len(raw) > providerBodyLimit {
		return nil,
			fmt.Sprintf("request body exceeded the %d-byte provider capture bound; the received prefix was discarded because a credential may straddle the cut", providerBodyLimit),
			fmt.Errorf("%w: more than %d bytes", errProviderBodyIncomplete, providerBodyLimit)
	}
	if readErr != nil {
		return nil,
			fmt.Sprintf("request body could not be read to the end (%v); the received prefix was discarded because a credential may straddle the cut", readErr),
			readErr
	}
	return raw, "", nil
}

// fakeProvider is an httptest.Server that speaks one upstream wire family,
// records every request, checks the canary API key and injects scripted faults.
type fakeProvider struct {
	t      *testing.T
	family wireFamily
	script fakeScript
	canary string
	srv    *httptest.Server

	mu          sync.Mutex
	requests    []providerRequest
	fault       providerFault
	abortAfter  int
	cancelled   chan struct{}
	cancelOnce  sync.Once
	cancelledAt time.Time
}

func newOpenAILegacyProvider(t *testing.T, canary string, script fakeScript) *fakeProvider {
	return newFakeProvider(t, familyOpenAILegacy, canary, script)
}

func newAnthropicMessagesProvider(t *testing.T, canary string, script fakeScript) *fakeProvider {
	return newFakeProvider(t, familyAnthropicMessages, canary, script)
}

func newSystemOneProvider(t *testing.T, canary string, script fakeScript) *fakeProvider {
	return newFakeProvider(t, familySystemOne, canary, script)
}

func newFakeProvider(t *testing.T, family wireFamily, canary string, script fakeScript) *fakeProvider {
	t.Helper()
	if script.Model == "" {
		t.Fatalf("product smoke: fake provider script needs a model")
	}
	p := &fakeProvider{
		t:         t,
		family:    family,
		script:    script,
		canary:    canary,
		cancelled: make(chan struct{}),
	}
	// Bind a harness-owned port and hold it: a provider must never share an
	// address with a concurrent test process that reserved one with ":0".
	p.srv = httptest.NewUnstartedServer(http.HandlerFunc(p.handle))
	_ = p.srv.Listener.Close()
	p.srv.Listener = listenSmokeLoopback(t)
	p.srv.Start()
	t.Cleanup(func() {
		p.srv.CloseClientConnections()
		p.srv.Close()
	})
	return p
}

// BaseURL is the httptest origin. Backend wiring appends the family's API
// prefix itself so the config stays explicit.
func (p *fakeProvider) BaseURL() string { return p.srv.URL }

// OpenAILegacyBaseURL is the OpenAI-compatible base URL, including /v1.
func (p *fakeProvider) OpenAILegacyBaseURL() string { return p.srv.URL + "/v1" }

// AnthropicBaseURL is the Anthropic API origin; the adapter appends /v1/messages.
func (p *fakeProvider) AnthropicBaseURL() string { return p.srv.URL }

// SystemOneBaseURL is the System One API origin; the adapter appends /systemone.
func (p *fakeProvider) SystemOneBaseURL() string { return p.srv.URL }

// SetFault configures a scripted failure. abortAfter applies to
// faultAbortAfterEvents and counts already-written stream events.
func (p *fakeProvider) SetFault(fault providerFault, abortAfter int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fault = fault
	p.abortAfter = abortAfter
}

// SetScript replaces the scripted answer for later requests. A request already
// in flight keeps the script it started with.
func (p *fakeProvider) SetScript(script fakeScript) {
	if script.Model == "" {
		p.t.Fatalf("product smoke: fake provider script needs a model")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.script = script
}

func (p *fakeProvider) scriptSnapshot() fakeScript {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.script
}

func (p *fakeProvider) currentFault() (providerFault, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fault, p.abortAfter
}

func (p *fakeProvider) record(rec providerRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, rec)
}

func (p *fakeProvider) Requests() []providerRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]providerRequest, len(p.requests))
	copy(out, p.requests)
	return out
}

func (p *fakeProvider) RequestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

// Cancelled closes once a handler observed its request context cancelled.
func (p *fakeProvider) Cancelled() <-chan struct{} { return p.cancelled }

// CancelledAt reports when cancellation was observed, if it was.
func (p *fakeProvider) CancelledAt() (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cancelledAt, !p.cancelledAt.IsZero()
}

func (p *fakeProvider) markCancelled() {
	p.cancelOnce.Do(func() {
		p.mu.Lock()
		p.cancelledAt = time.Now()
		p.mu.Unlock()
		close(p.cancelled)
	})
}

// RequestLog renders every recorded request for evidence.
func (p *fakeProvider) RequestLog() string {
	requests := p.Requests()
	var b strings.Builder
	fmt.Fprintf(&b, "fake provider %s recorded %d request(s)\n", p.family, len(requests))
	for i, req := range requests {
		fmt.Fprintf(&b, "\n--- request %d: %s %s (api key header %s, matched=%t) ---\n",
			i+1, req.Method, req.Path, req.KeyHeader, req.KeyMatched)
		for _, key := range []string{"Content-Type", "Accept", "Authorization", "X-Api-Key", "Anthropic-Version"} {
			if values := req.Header.Values(key); len(values) > 0 {
				fmt.Fprintf(&b, "%s: %s\n", key, strings.Join(values, ", "))
			}
		}
		if req.BodyOmitted != "" {
			fmt.Fprintf(&b, "body not retained: %s\n", req.BodyOmitted)
		} else {
			fmt.Fprintf(&b, "%s\n", prettyJSON(req.Body))
		}
	}
	return b.String()
}

func prettyJSON(raw []byte) string {
	var out json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return string(raw)
	}
	indented, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(indented)
}

func (p *fakeProvider) handle(w http.ResponseWriter, r *http.Request) {
	body, bodyOmitted, readErr := readProviderBody(r.Body)
	keyHeader, keyMatched := p.checkKey(r.Header)
	p.record(providerRequest{
		Method:      r.Method,
		Path:        r.URL.Path,
		Header:      r.Header.Clone(),
		Body:        body,
		BodyOmitted: bodyOmitted,
		KeyHeader:   keyHeader,
		KeyMatched:  keyMatched,
		At:          time.Now(),
	})
	if readErr != nil {
		http.Error(w, "scripted provider: "+readErr.Error(), http.StatusBadRequest)
		return
	}
	if !strings.HasSuffix(r.URL.Path, p.pathSuffix()) {
		http.Error(w, "scripted provider: unexpected path", http.StatusNotFound)
		return
	}
	if !keyMatched {
		p.writeErrorBody(w, http.StatusUnauthorized, "scripted provider: wrong or missing api key")
		return
	}
	if len(body) == 0 || !json.Valid(body) {
		p.writeErrorBody(w, http.StatusBadRequest, "scripted provider: request body is not valid JSON")
		return
	}
	fault, abortAfter := p.currentFault()
	script := p.scriptSnapshot()
	switch fault {
	case faultHTTPErrorBeforeBody:
		p.writeErrorBody(w, http.StatusServiceUnavailable, "scripted upstream failure before any body")
		return
	case faultStallUntilCancelled:
		p.stallUntilCancelled(w, r, script)
		return
	case faultAbortAfterEvents:
		p.writeStream(w, r, abortAfter, script)
		return
	}
	// The System One decision wire has no stream mode: every call is a
	// non-streaming decision evaluation.
	if p.family == familySystemOne || !wantsStream(body) {
		p.writeCollect(w, body, script)
		return
	}
	p.writeStream(w, r, 0, script)
}

func (p *fakeProvider) pathSuffix() string {
	switch p.family {
	case familyAnthropicMessages:
		return anthropicMessagesSuffix
	case familySystemOne:
		return systemOneUpstreamSuffix
	default:
		return openAIChatCompletionsSuffix
	}
}

// checkKey verifies the canary credential and names the header that carried it.
func (p *fakeProvider) checkKey(h http.Header) (string, bool) {
	if p.canary == "" {
		if p.family == familyAnthropicMessages {
			return "x-api-key", true
		}
		return "Authorization", true
	}
	if p.family == familyAnthropicMessages {
		if value := strings.TrimSpace(h.Get("X-Api-Key")); value != "" {
			return "X-Api-Key", value == p.canary
		}
	}
	value := strings.TrimSpace(h.Get("Authorization"))
	return "Authorization", strings.TrimPrefix(value, "Bearer ") == p.canary
}

func wantsStream(body []byte) bool {
	var probe struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &probe)
	return probe.Stream
}

func (p *fakeProvider) writeErrorBody(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if p.family == familyAnthropicMessages {
		_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":"api_error","message":%q}}`, message)
		return
	}
	_, _ = fmt.Fprintf(w, `{"error":{"message":%q,"type":"upstream_error","code":"scripted_failure"}}`, message)
}

// writeStream emits one scripted SSE response. When abortAfter is positive the
// connection is aborted just before the (abortAfter+1)th event would be written.
func (p *fakeProvider) writeStream(w http.ResponseWriter, r *http.Request, abortAfter int, script fakeScript) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	written := 0
	emit := func(event, data string) bool {
		if abortAfter > 0 && written == abortAfter {
			panic(http.ErrAbortHandler)
		}
		if err := writeSSE(w, event, data); err != nil {
			p.noteWriteFailure(r)
			return false
		}
		_ = rc.Flush()
		written++
		return true
	}
	if p.family == familyAnthropicMessages {
		p.writeAnthropicStream(emit, script)
		return
	}
	p.writeOpenAIStream(emit, script)
}

func (p *fakeProvider) writeOpenAIStream(emit func(event, data string) bool, script fakeScript) {
	chunk := func(delta map[string]any, finish *string, usage *fakeUsage) string {
		choices := []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}}
		payload := map[string]any{
			"id":      "chatcmpl-scripted",
			"object":  "chat.completion.chunk",
			"created": 1700000000,
			"model":   script.Model,
			"choices": choices,
		}
		if usage != nil {
			payload["usage"] = map[string]any{
				"prompt_tokens":     usage.PromptTokens,
				"completion_tokens": usage.CompletionTokens,
				"total_tokens":      usage.TotalTokens,
			}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			p.t.Errorf("product smoke: marshal scripted stream chunk: %v", err)
			return "{}"
		}
		return string(raw)
	}
	if !emit("", chunk(map[string]any{"role": "assistant"}, nil, nil)) {
		return
	}
	if script.ToolCall != nil {
		started := map[string]any{
			"index": 0,
			"id":    script.ToolCall.ID,
			"type":  "function",
			"function": map[string]any{
				"name": script.ToolCall.Name,
			},
		}
		if !emit("", chunk(map[string]any{"tool_calls": []any{started}}, nil, nil)) {
			return
		}
		for _, delta := range script.ToolCall.argumentDeltas() {
			args := map[string]any{"index": 0, "function": map[string]any{"arguments": delta}}
			if !emit("", chunk(map[string]any{"tool_calls": []any{args}}, nil, nil)) {
				return
			}
		}
		if script.OmitTerminal {
			return
		}
		finish := script.finishReason(p.family)
		if !emit("", chunk(map[string]any{}, &finish, &script.Usage)) {
			return
		}
		emit("", "[DONE]")
		return
	}
	for _, delta := range script.Deltas {
		if !emit("", chunk(map[string]any{"content": delta}, nil, nil)) {
			return
		}
	}
	if script.OmitTerminal {
		return
	}
	finish := script.finishReason(p.family)
	if !emit("", chunk(map[string]any{}, &finish, &script.Usage)) {
		return
	}
	emit("", "[DONE]")
}

func (p *fakeProvider) writeAnthropicStream(emit func(event, data string) bool, script fakeScript) {
	payload := func(v map[string]any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			p.t.Errorf("product smoke: marshal scripted anthropic event: %v", err)
			return "{}"
		}
		return string(raw)
	}
	if !emit("message_start", payload(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            "msg_scripted",
			"type":          "message",
			"role":          "assistant",
			"model":         script.Model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  script.Usage.PromptTokens,
				"output_tokens": 0,
			},
		},
	})) {
		return
	}
	if !emit("content_block_start", payload(map[string]any{
		"type":          "content_block_start",
		"index":         0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})) {
		return
	}
	for _, delta := range script.Deltas {
		if !emit("content_block_delta", payload(map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "text_delta", "text": delta},
		})) {
			return
		}
	}
	if !emit("content_block_stop", payload(map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	})) {
		return
	}
	if script.OmitTerminal {
		return
	}
	if !emit("message_delta", payload(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": script.finishReason(p.family), "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": script.Usage.CompletionTokens},
	})) {
		return
	}
	emit("message_stop", payload(map[string]any{"type": "message_stop"}))
}

func (p *fakeProvider) writeCollect(w http.ResponseWriter, requestBody []byte, script fakeScript) {
	w.Header().Set("Content-Type", "application/json")
	if script.TruncateBody {
		_, _ = fmt.Fprint(w, `{"id":"scripted","object":"chat.completion","choices":[`)
		return
	}
	switch p.family {
	case familyAnthropicMessages:
		var stopReason any = script.finishReason(p.family)
		if script.OmitTerminal {
			stopReason = nil
		}
		payload := map[string]any{
			"id":            "msg_scripted",
			"type":          "message",
			"role":          "assistant",
			"model":         script.Model,
			"content":       []map[string]any{{"type": "text", "text": script.content()}},
			"stop_reason":   stopReason,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  script.Usage.PromptTokens,
				"output_tokens": script.Usage.CompletionTokens,
			},
		}
		writeJSON(w, payload)
	case familySystemOne:
		p.writeSystemOneCollect(w, requestBody, script)
	default:
		var finishReason any = script.finishReason(p.family)
		if script.OmitTerminal {
			finishReason = nil
		}
		payload := map[string]any{
			"id":      "chatcmpl-scripted",
			"object":  "chat.completion",
			"created": 1700000000,
			"model":   script.Model,
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": script.content()},
				"finish_reason": finishReason,
			}},
			"usage": map[string]any{
				"prompt_tokens":     script.Usage.PromptTokens,
				"completion_tokens": script.Usage.CompletionTokens,
				"total_tokens":      script.Usage.TotalTokens,
			},
		}
		writeJSON(w, payload)
	}
}

// systemOneAnswers is the scripted System One answer set. The question ids and
// types are fixed by the journey's decision request, so the fake can answer
// without echoing any upstream content into the client checks.
var systemOneAnswers = map[string]any{
	"ship_it": map[string]any{
		"type": "noul",
		"noul": 0.75,
	},
	"next_step": map[string]any{
		"type":          "choice",
		"choice":        "ship",
		"probabilities": map[string]any{"ship": 0.7, "hold": 0.3},
		"confidence":    0.7,
	},
	"quality": map[string]any{
		"type":          "score",
		"score":         2,
		"probabilities": map[string]any{"0": 0.1, "1": 0.2, "2": 0.7},
	},
}

// writeSystemOneCollect answers a decision-wire call with the scripted model,
// answers and usage. Omitting the terminal drops the answers object entirely, so
// the frontend cannot produce a complete decision response.
func (p *fakeProvider) writeSystemOneCollect(w http.ResponseWriter, requestBody []byte, script fakeScript) {
	var request struct {
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(requestBody, &request); err != nil {
		p.writeErrorBody(w, http.StatusBadRequest, "scripted provider: decision body is not JSON")
		return
	}
	payload := map[string]any{
		"model": script.Model,
		"usage": map[string]any{
			"input_tokens":  script.Usage.PromptTokens,
			"output_tokens": script.Usage.CompletionTokens,
		},
	}
	if !script.OmitTerminal {
		answers := make(map[string]any, len(request.Questions))
		for id := range request.Questions {
			answer, ok := systemOneAnswers[id]
			if !ok {
				p.writeErrorBody(w, http.StatusBadRequest, "scripted provider: unscripted decision question")
				return
			}
			answers[id] = answer
		}
		payload["answers"] = answers
	}
	writeJSON(w, payload)
}

func writeJSON(w http.ResponseWriter, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "scripted provider: marshal response", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(raw)
}

// stallUntilCancelled opens the stream, sends nothing terminal, and waits for
// the request context to be cancelled.
func (p *fakeProvider) stallUntilCancelled(w http.ResponseWriter, r *http.Request, script fakeScript) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if p.family == familyAnthropicMessages {
		_ = writeSSE(w, "message_start", `{"type":"message_start","message":{"id":"msg_stall","type":"message","role":"assistant","model":"`+script.Model+`","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
	} else {
		_ = writeSSE(w, "", `{"id":"chatcmpl-stall","object":"chat.completion.chunk","created":1700000000,"model":"`+script.Model+`","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
	}
	_ = rc.Flush()
	select {
	case <-r.Context().Done():
		p.markCancelled()
	case <-time.After(stallBound):
	}
}

func (p *fakeProvider) noteWriteFailure(r *http.Request) {
	if r.Context().Err() != nil {
		p.markCancelled()
	}
}

func writeSSE(w http.ResponseWriter, event, data string) error {
	if event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

// scriptedOpenAIStreamBytes renders the exact scripted stream writeOpenAIStream
// emits, so a test can build a wire fixture without an HTTP exchange.
func scriptedOpenAIStreamBytes(script fakeScript) []byte {
	recorder := httptest.NewRecorder()
	provider := &fakeProvider{family: familyOpenAILegacy, script: script}
	provider.writeOpenAIStream(func(event, data string) bool {
		return writeSSE(recorder, event, data) == nil
	}, script)
	return recorder.Body.Bytes()
}

// TestFakeProviderWires proves the scripted providers themselves: both wire
// families, both delivery modes, the canary check and each fault. It never
// starts aipstd, so it runs without AIP_PRODUCT_SMOKE_BIN.
func TestFakeProviderWires(t *testing.T) {
	t.Parallel()
	script := fakeScript{
		Model:  "scripted-model",
		Deltas: []string{"alpha ", "beta"},
		Usage:  fakeUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	}
	t.Run("openai_stream", func(t *testing.T) {
		t.Parallel()
		provider := newOpenAILegacyProvider(t, "canary-value", script)
		resp, body, err := scriptedPost(t.Context(), provider.OpenAILegacyBaseURL()+openAIChatCompletionsSuffix, `{"model":"scripted-model","stream":true}`, "Authorization", "Bearer canary-value")
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d, want 200; body %s", resp.StatusCode, body)
		}
		frames, err := parseSSEFrames(body)
		if err != nil {
			t.Fatalf("parse sse: %v", err)
		}
		payloads := dataPayloads(frames)
		if len(payloads) < 4 {
			t.Fatalf("got %d data events, want at least 4: %s", len(payloads), body)
		}
		if got := payloads[len(payloads)-1]; got != "[DONE]" {
			t.Fatalf("last event %q, want [DONE]", got)
		}
		var joined strings.Builder
		for _, payload := range payloads[:len(payloads)-2] {
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				t.Fatalf("chunk is not JSON: %v", err)
			}
			for _, choice := range chunk.Choices {
				joined.WriteString(choice.Delta.Content)
			}
		}
		if got, want := joined.String(), script.content(); got != want {
			t.Fatalf("content %q, want %q", got, want)
		}
		if requests := provider.Requests(); len(requests) != 1 || !requests[0].KeyMatched {
			t.Fatalf("provider recorded %d requests, canary matched=%t", len(requests), requests[0].KeyMatched)
		}
	})

	t.Run("openai_collect", func(t *testing.T) {
		t.Parallel()
		provider := newOpenAILegacyProvider(t, "canary-value", script)
		resp, body, err := scriptedPost(t.Context(), provider.OpenAILegacyBaseURL()+openAIChatCompletionsSuffix, `{"model":"scripted-model","stream":false}`, "Authorization", "Bearer canary-value")
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d, want 200", resp.StatusCode)
		}
		var completion chatCompletion
		if err := json.Unmarshal(body, &completion); err != nil {
			t.Fatalf("decode completion: %v", err)
		}
		if got, want := completion.Choices[0].Message.Content, script.content(); got != want {
			t.Fatalf("content %q, want %q", got, want)
		}
		if completion.Usage == nil || completion.Usage.TotalTokens != script.Usage.TotalTokens {
			t.Fatalf("usage %+v, want total %d", completion.Usage, script.Usage.TotalTokens)
		}
	})

	t.Run("anthropic_stream", func(t *testing.T) {
		t.Parallel()
		provider := newAnthropicMessagesProvider(t, "canary-value", script)
		resp, body, err := scriptedPost(t.Context(), provider.AnthropicBaseURL()+anthropicMessagesSuffix, `{"model":"scripted-model","stream":true}`, "X-Api-Key", "canary-value")
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d, want 200; body %s", resp.StatusCode, body)
		}
		frames, err := parseSSEFrames(body)
		if err != nil {
			t.Fatalf("parse sse: %v", err)
		}
		var names []string
		var joined strings.Builder
		for _, frame := range frames {
			names = append(names, frame.Event)
			if frame.Event != "content_block_delta" {
				continue
			}
			var event struct {
				Delta struct {
					Text string `json:"text"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(frame.Data), &event); err != nil {
				t.Fatalf("delta is not JSON: %v", err)
			}
			joined.WriteString(event.Delta.Text)
		}
		wantNames := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
		if strings.Join(names, ",") != strings.Join(wantNames, ",") {
			t.Fatalf("event order %v, want %v", names, wantNames)
		}
		if got, want := joined.String(), script.content(); got != want {
			t.Fatalf("content %q, want %q", got, want)
		}
	})

	t.Run("anthropic_collect", func(t *testing.T) {
		t.Parallel()
		provider := newAnthropicMessagesProvider(t, "canary-value", script)
		resp, body, err := scriptedPost(t.Context(), provider.AnthropicBaseURL()+anthropicMessagesSuffix, `{"model":"scripted-model","stream":false}`, "X-Api-Key", "canary-value")
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d, want 200", resp.StatusCode)
		}
		var message struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			StopReason string `json:"stop_reason"`
		}
		if err := json.Unmarshal(body, &message); err != nil {
			t.Fatalf("decode message: %v", err)
		}
		if message.Type != "message" || message.StopReason != "end_turn" {
			t.Fatalf("message type %q stop reason %q, want message/end_turn", message.Type, message.StopReason)
		}
		if len(message.Content) != 1 || message.Content[0].Text != script.content() {
			t.Fatalf("content %+v, want %q", message.Content, script.content())
		}
	})

	t.Run("systemone_collect", func(t *testing.T) {
		t.Parallel()
		provider := newSystemOneProvider(t, "canary-value", script)
		request := `{"model":"scripted-model","state":{"facts":["alpha"]},"questions":{` +
			`"ship_it":{"type":"noul"},"next_step":{"type":"choice"},"quality":{"type":"score"}}}`
		resp, body, err := scriptedPost(t.Context(), provider.SystemOneBaseURL()+systemOneUpstreamSuffix, request, "Authorization", "Bearer canary-value")
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d, want 200; body %s", resp.StatusCode, body)
		}
		var decision struct {
			Model   string `json:"model"`
			Answers map[string]struct {
				Type   string   `json:"type"`
				Noul   *float64 `json:"noul"`
				Choice string   `json:"choice"`
				Score  *float64 `json:"score"`
			} `json:"answers"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &decision); err != nil {
			t.Fatalf("decode decision: %v", err)
		}
		if decision.Model != script.Model {
			t.Fatalf("model %q, want %q", decision.Model, script.Model)
		}
		if len(decision.Answers) != 3 {
			t.Fatalf("answers %d, want 3", len(decision.Answers))
		}
		if got := decision.Answers["ship_it"]; got.Type != "noul" || got.Noul == nil || *got.Noul != 0.75 {
			t.Fatalf("ship_it answer %+v, want noul=0.75", got)
		}
		if got := decision.Answers["next_step"]; got.Type != "choice" || got.Choice != "ship" {
			t.Fatalf("next_step answer %+v, want choice=ship", got)
		}
		if got := decision.Answers["quality"]; got.Type != "score" || got.Score == nil || *got.Score != 2 {
			t.Fatalf("quality answer %+v, want score=2", got)
		}
		if decision.Usage.InputTokens != script.Usage.PromptTokens || decision.Usage.OutputTokens != script.Usage.CompletionTokens {
			t.Fatalf("usage %+v, want input=%d output=%d", decision.Usage, script.Usage.PromptTokens, script.Usage.CompletionTokens)
		}
		if requests := provider.Requests(); len(requests) != 1 || !requests[0].KeyMatched {
			t.Fatalf("provider recorded %d requests, canary matched=%t", len(requests), requests[0].KeyMatched)
		}
	})

	t.Run("http_error_fault", func(t *testing.T) {
		t.Parallel()
		provider := newOpenAILegacyProvider(t, "canary-value", script)
		provider.SetFault(faultHTTPErrorBeforeBody, 0)
		resp, body, err := scriptedPost(t.Context(), provider.OpenAILegacyBaseURL()+openAIChatCompletionsSuffix, `{"model":"scripted-model","stream":true}`, "Authorization", "Bearer canary-value")
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status %d, want 503; body %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "before any body") {
			t.Fatalf("error body %s does not name the scripted failure", body)
		}
	})

	t.Run("abort_fault", func(t *testing.T) {
		t.Parallel()
		provider := newOpenAILegacyProvider(t, "canary-value", script)
		provider.SetFault(faultAbortAfterEvents, 2)
		resp, body, err := scriptedPost(t.Context(), provider.OpenAILegacyBaseURL()+openAIChatCompletionsSuffix, `{"model":"scripted-model","stream":true}`, "Authorization", "Bearer canary-value")
		if resp == nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("no readable 200 response before the abort: resp=%v err=%v", resp, err)
		}
		prefix := string(body)
		if !strings.Contains(prefix, `"role":"assistant"`) || !strings.Contains(prefix, "alpha ") {
			t.Fatalf("aborted stream carried no pre-abort events: %q (err=%v)", prefix, err)
		}
		if strings.Contains(prefix, "[DONE]") {
			t.Fatalf("aborted stream carried a terminal: %s", prefix)
		}
	})

	t.Run("stall_fault_observes_cancellation", func(t *testing.T) {
		t.Parallel()
		provider := newAnthropicMessagesProvider(t, "canary-value", script)
		provider.SetFault(faultStallUntilCancelled, 0)
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.AnthropicBaseURL()+anthropicMessagesSuffix, strings.NewReader(`{"model":"scripted-model","stream":true}`))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("X-Api-Key", "canary-value")
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		select {
		case <-provider.Cancelled():
		case <-time.After(2 * time.Second):
			t.Fatal("provider did not observe the cancelled request within 2s")
		}
		if _, ok := provider.CancelledAt(); !ok {
			t.Fatal("provider recorded no cancellation time")
		}
	})
}

// scriptedPost posts a JSON body with one extra header and returns the
// response and the bytes read from it.
func scriptedPost(ctx context.Context, url, body, header, value string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if header != "" {
		req.Header.Set(header, value)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, readErr := io.ReadAll(resp.Body)
	return resp, raw, readErr
}
