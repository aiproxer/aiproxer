package stdhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	refanth "github.com/aiproxer/aiproxer/internal/refbackend/anthropicmessages"
)

// claudeEnvironmentBlock is the preamble Claude Code 2.1.322 sends in default
// (non-bare) mode: header line, lead line, then the bulleted environment facts.
const claudeEnvironmentBlock = "# Environment\n" +
	"You have been invoked in the following environment: \n" +
	" - Primary working directory: /home/user/project\n" +
	" - Is a git repository: true\n" +
	" - Platform: linux"

// Claim (#793): a default (non-bare) Claude Code turn, whose environment block
// arrives as a mid-history system-role message, completes through the real
// Anthropic frontend, canonical core and Anthropic Messages backend, and the
// provider receives that block as a system message at its original index with
// its exact text. Mutant: restore the backend's refusal of role "system", or
// lift the block into the top-level system field.
func TestAnthropicFrontendMidHistorySystem_NativeClaudeTurnServed(t *testing.T) {
	t.Parallel()
	stack, upstream := startMidHistorySystemStack(t)
	body, err := json.Marshal(map[string]any{
		"model": rpE2EAnthModel, "max_tokens": 64, "stream": true,
		"messages": []any{
			map[string]any{"role": "user", "content": "probe"},
			map[string]any{"role": "system", "content": claudeEnvironmentBlock},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, stack.proxyURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", rpE2EAnthKey)
	resp, err := stack.proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	stream, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(stream), "message_stop") {
		t.Fatalf("status %d, want a completed 200 stream; body=%s", resp.StatusCode, stream)
	}
	assertUpstreamKeepsSystemInPlace(t, upstream.last())
}

// TestAnthropicFrontendMidHistorySystem_CapturedClaudeShapeServed replays the
// wire shape captured from Claude Code 2.1.322 with Opus 5.5 in default
// (non-bare) mode: a top-level system prompt, a user turn carrying the
// per-turn reminders, and the environment block as the final mid-history
// system message. It proves the two system carriers stay distinct and in
// place.
func TestAnthropicFrontendMidHistorySystem_CapturedClaudeShapeServed(t *testing.T) {
	t.Parallel()
	stack, upstream := startMidHistorySystemStack(t)
	status, stream := postMidHistoryMessages(t, stack, map[string]any{
		"model": rpE2EAnthModel, "max_tokens": 64, "stream": true,
		"system": []any{
			map[string]any{"type": "text", "text": "stable instruction"},
			map[string]any{"type": "text", "text": "steering instruction"},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "<system-reminder>\n# gitStatus"},
				map[string]any{"type": "text", "text": "Reply with exactly OK"},
			}},
			map[string]any{"role": "system", "content": []any{
				map[string]any{"type": "text", "text": claudeEnvironmentBlock},
			}},
		},
	})
	if status != http.StatusOK || !strings.Contains(string(stream), "message_stop") {
		t.Fatalf("status %d, want a completed 200 stream; body=%s", status, stream)
	}

	captured := upstream.last()
	var req struct {
		System []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(captured, &req); err != nil {
		t.Fatalf("upstream body: %v", err)
	}
	// The canonical builder joins top-level instructions into one system block;
	// that existing behavior is not the claim. The environment block must not be
	// added to it.
	if len(req.System) != 1 || !strings.Contains(req.System[0].Text, "stable instruction") || !strings.Contains(req.System[0].Text, "steering instruction") {
		t.Fatalf("top-level system changed: %+v", req.System)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "user" {
		t.Fatalf("upstream messages changed: %+v", req.Messages)
	}
	if req.Messages[1].Role != "system" || len(req.Messages[1].Content) != 1 || req.Messages[1].Content[0].Text != claudeEnvironmentBlock {
		t.Fatalf("mid-history environment block moved or changed: %+v", req.Messages[1])
	}
}

// TestAnthropicFrontendMidHistorySystem_PlacementRefusedAsClientError proves a
// locally refused placement is a client-visible 400, never an opaque 500, and
// never reaches the upstream.
func TestAnthropicFrontendMidHistorySystem_PlacementRefusedAsClientError(t *testing.T) {
	t.Parallel()
	stack, upstream := startMidHistorySystemStack(t)
	cases := []struct {
		name        string
		messages    []any
		wantMessage string
	}{
		{
			name: "system first",
			messages: []any{
				map[string]any{"role": "system", "content": claudeEnvironmentBlock},
				map[string]any{"role": "user", "content": "probe"},
			},
			wantMessage: "cannot be the first message",
		},
		{
			name: "system followed by user",
			messages: []any{
				map[string]any{"role": "user", "content": "probe"},
				map[string]any{"role": "system", "content": claudeEnvironmentBlock},
				map[string]any{"role": "user", "content": "again"},
			},
			wantMessage: "must be followed by an assistant turn or end the request",
		},
		{
			name: "system after assistant",
			messages: []any{
				map[string]any{"role": "user", "content": "probe"},
				map[string]any{"role": "assistant", "content": "answer"},
				map[string]any{"role": "system", "content": claudeEnvironmentBlock},
			},
			wantMessage: "must follow a user message",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := postMidHistoryMessages(t, stack, map[string]any{
				"model": rpE2EAnthModel, "max_tokens": 64, "stream": true,
				"messages": tc.messages,
			})
			if status != http.StatusBadRequest {
				t.Fatalf("status %d, want 400; body=%s", status, body)
			}
			var wire struct {
				Type  string `json:"type"`
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatalf("error body: %v", err)
			}
			if wire.Type != "error" || wire.Error.Type != "invalid_request_error" {
				t.Fatalf("error body %s, want invalid_request_error", body)
			}
			if !strings.Contains(wire.Error.Message, tc.wantMessage) {
				t.Fatalf("error message %q, want it to contain %q", wire.Error.Message, tc.wantMessage)
			}
			if strings.Contains(wire.Error.Message, "internal error") {
				t.Fatalf("error message %q leaked the opaque internal error", wire.Error.Message)
			}
			if got := upstream.last(); len(got) != 0 {
				t.Fatalf("locally refused request reached upstream: %s", got)
			}
		})
	}
}

func postMidHistoryMessages(t *testing.T, stack *rpHTTPStack, body map[string]any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, stack.proxyURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", rpE2EAnthKey)
	resp, err := stack.proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// assertUpstreamKeepsSystemInPlace proves the provider-bound body keeps the
// environment block as a system message at its original index, and that it was
// neither hoisted into the top-level system field nor coerced onto a user turn.
func assertUpstreamKeepsSystemInPlace(t *testing.T, captured []byte) {
	t.Helper()
	var req struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(captured, &req); err != nil {
		t.Fatalf("upstream body: %v", err)
	}
	if len(req.System) > 0 && string(req.System) != "null" && string(req.System) != "[]" {
		t.Fatalf("environment block was hoisted into top-level system: %s", req.System)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("upstream messages %d, want 2 (history must keep every turn)", len(req.Messages))
	}
	if got := req.Messages[1].Role; got != "system" {
		t.Fatalf("upstream message 1 role %q, want system", got)
	}
	var blocks []map[string]any
	if err := json.Unmarshal(req.Messages[1].Content, &blocks); err != nil {
		t.Fatalf("upstream content: %v", err)
	}
	if len(blocks) != 1 || blocks[0]["text"] != claudeEnvironmentBlock {
		t.Fatalf("upstream system block changed: %v", blocks)
	}
}

// recordingEmulator captures the last upstream body for post-run assertions.
type recordingEmulator struct {
	mu   sync.Mutex
	body []byte
}

func (r *recordingEmulator) observe(body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.body = append([]byte(nil), body...)
}

func (r *recordingEmulator) last() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.body...)
}

// startMidHistorySystemStack builds an in-process host with the Anthropic
// frontend and Anthropic Messages backend against a recording emulator.
func startMidHistorySystemStack(t *testing.T) (*rpHTTPStack, *recordingEmulator) {
	t.Helper()
	return startMidHistorySystemStackWithUpstream(t, nil)
}

// startMidHistorySystemStackWithUpstream is startMidHistorySystemStack with an
// optional wrapper around the reference emulator, used to script upstream
// refusals while the rest of the stack stays real.
func startMidHistorySystemStackWithUpstream(t *testing.T, wrap func(http.Handler) http.Handler) (*rpHTTPStack, *recordingEmulator) {
	t.Helper()
	return startMidHistorySystemStackWithConfig(t, wrap, nil)
}

// startMidHistorySystemStackWithConfig is startMidHistorySystemStackWithUpstream
// with an optional edit of the generated proxy config, used to widen the route
// so a retryable failure would reach a second attempt.
func startMidHistorySystemStackWithConfig(t *testing.T, wrap func(http.Handler) http.Handler, editConfig func(string) string) (*rpHTTPStack, *recordingEmulator) {
	t.Helper()
	rec := &recordingEmulator{}
	upstream := refanth.NewHandler(refanth.Config{AllowMissingAPIKey: false, OnRequestBody: rec.observe})
	if wrap != nil {
		upstream = wrap(upstream)
	}
	emu := httptest.NewServer(upstream)
	t.Cleanup(emu.Close)
	cfgPath, err := writeReasoningPreservationConfigOptsErr(rpChatStackOpts{FeatureRow: rpFeatureRowOmit}, "anthropic", emu.URL, rpE2EAnthKey, rpE2EAnthModel)
	if err != nil {
		t.Fatal(err)
	}
	if editConfig != nil {
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, []byte(editConfig(string(raw))), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	proxy, cleanup, err := startRPBootstrapProxyErr(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	stack := &rpHTTPStack{proxyURL: proxy.URL, proxy: proxy, emulator: emu, cleanup: func() { proxy.Close(); cleanup() }}
	t.Cleanup(stack.cleanup)
	return stack, rec
}
