package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var errSysPromptTest = errors.New("sysprompt test rotation failure")

func instructionCall(instructions []string, messages []aipapi.Message) *aipapi.Call {
	call := &aipapi.Call{
		ID:      "sysprompt",
		Session: aipapi.SessionRef{ClientSessionID: "s"},
	}
	for _, text := range instructions {
		call.Instructions = append(call.Instructions, aipapi.Message{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart(text)},
		})
	}
	call.Messages = messages
	return call
}

// Non-text parts are skipped even when they carry text, so authoritative
// delivery and prompt flattening cannot disagree about system content.
func TestSystemInstructionText_JoinsInstructionsAndSystemMessages(t *testing.T) {
	t.Parallel()
	call := instructionCall([]string{"op one", "", "op two"}, []aipapi.Message{
		{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("sys one"), {Kind: aipapi.PartToolResult, Text: "tool echo"}}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}},
	})
	if got, want := systemInstructionText(call), "op one\n\nop two\n\nsys one"; got != want {
		t.Fatalf("instructions = %q, want %q", got, want)
	}
}

func TestSystemInstructionText_Empty(t *testing.T) {
	t.Parallel()
	if got := systemInstructionText(nil); got != "" {
		t.Fatalf("nil call = %q, want empty", got)
	}
	call := instructionCall(nil, []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
	})
	if got := systemInstructionText(call); got != "" {
		t.Fatalf("no instructions = %q, want empty", got)
	}
}

func TestMergeSessionSystemPrompt_InjectsAppend(t *testing.T) {
	t.Parallel()
	base := json.RawMessage(`{"claudeCode":{"options":{"tools":[]}}}`)
	merged := mergeSessionSystemPrompt(base, "do the thing")
	var decoded struct {
		ClaudeCode map[string]any `json:"claudeCode"`
		System     struct {
			Append string `json:"append"`
		} `json:"systemPrompt"`
	}
	if err := json.Unmarshal(merged, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.System.Append != "do the thing" {
		t.Fatalf("append = %q", decoded.System.Append)
	}
	if len(decoded.ClaudeCode) == 0 {
		t.Fatal("base meta must be preserved")
	}
}

func TestMergeSessionSystemPrompt_InvalidBaseFallsBack(t *testing.T) {
	t.Parallel()
	merged := mergeSessionSystemPrompt(json.RawMessage(`{broken`), "do the thing")
	var decoded struct {
		System struct {
			Append string `json:"append"`
		} `json:"systemPrompt"`
	}
	if err := json.Unmarshal(merged, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.System.Append != "do the thing" {
		t.Fatalf("append = %q", decoded.System.Append)
	}
}

// A JSON null base is the document's explicit "no value": it decodes into a
// nil map without an error, so merging the append text must fall back to the
// minimal object instead of assigning into that nil map. Every spelling of
// the null document behaves the same way.
//
// Mutants: decode directly into the initialized map (the assignment then
// panics on a nil map), and treat null as a base to preserve (the agent
// preset is silently dropped).
func TestMergeSessionSystemPrompt_NullBaseFallsBack(t *testing.T) {
	t.Parallel()
	for _, base := range []string{`null`, " \t\nnull \n"} {
		merged := mergeSessionSystemPrompt(json.RawMessage(base), "do the thing")
		var decoded struct {
			System struct {
				Append string `json:"append"`
			} `json:"systemPrompt"`
		}
		if err := json.Unmarshal(merged, &decoded); err != nil {
			t.Fatalf("null base %q: %v", base, err)
		}
		if decoded.System.Append != "do the thing" {
			t.Fatalf("null base %q: append = %q", base, decoded.System.Append)
		}
	}
}

func TestStripSystemInstructions_RemovesSystemContent(t *testing.T) {
	t.Parallel()
	call := instructionCall([]string{"op"}, []aipapi.Message{
		{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("sys")}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("yo")}},
	})
	stripped := stripSystemInstructions(call)
	if len(stripped.Instructions) != 0 {
		t.Fatalf("instructions not stripped: %#v", stripped.Instructions)
	}
	for _, m := range stripped.Messages {
		if m.Role == aipapi.RoleSystem {
			t.Fatalf("system message survives strip: %#v", m)
		}
	}
	if len(stripped.Messages) != 2 {
		t.Fatalf("user/assistant messages must survive, got %d", len(stripped.Messages))
	}
	if len(call.Instructions) != 1 || len(call.Messages) != 3 {
		t.Fatal("original call must be unmutated")
	}
}

func TestValidSessionSystemPrompt_Bound(t *testing.T) {
	t.Parallel()
	if !validSessionSystemPrompt("do the thing") {
		t.Fatal("ordinary text must be valid")
	}
	if validSessionSystemPrompt("") || validSessionSystemPrompt(" \t\n ") {
		t.Fatal("blank text must be invalid")
	}
	if !validSessionSystemPrompt(strings.Repeat("x", maxSessionSystemPromptBytes)) {
		t.Fatal("text at the bound must be valid")
	}
	if validSessionSystemPrompt(strings.Repeat("x", maxSessionSystemPromptBytes+1)) {
		t.Fatal("over-bound text must be invalid")
	}
}

// sysPromptOptionsTransport captures session/new _meta for rotation tests,
// reusing the embedded stdio option-state contract.
type sysPromptOptionsTransport struct {
	optionsTransport
	metas []json.RawMessage
}

func (t *sysPromptOptionsTransport) CallUnary(ctx context.Context, body []byte, expect int) ([]byte, error) {
	var probe struct {
		Method string `json:"method"`
		Params struct {
			Meta json.RawMessage `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, err
	}
	if probe.Method == "session/new" {
		t.metas = append(t.metas, probe.Params.Meta)
	}
	return t.optionsTransport.CallUnary(ctx, body, expect)
}

type sysPromptSpec struct {
	negotiatedOptionsSpec
	text string
}

func (s sysPromptSpec) SessionSystemPrompt(call *aipapi.Call) (string, bool) {
	_ = call
	if s.text == "" {
		return "", false
	}
	return s.text, true
}

func sessionNewAppend(t *testing.T, meta json.RawMessage) string {
	t.Helper()
	var decoded struct {
		System struct {
			Append string `json:"append"`
		} `json:"systemPrompt"`
	}
	if err := json.Unmarshal(meta, &decoded); err != nil {
		t.Fatalf("session/new _meta is not an object: %v", err)
	}
	return decoded.System.Append
}

func TestHandshakeWithSystemPrompt_SendsAppendMeta(t *testing.T) {
	t.Parallel()
	transport := &sysPromptOptionsTransport{optionsTransport: optionsTransport{
		newOptions: advertisedOptions("sonnet", "medium", "medium"),
	}}
	proto := NewACPProtocol(sysPromptSpec{text: "steer this"}, nil)
	withPrompt, ok := proto.(SystemPromptSessionProtocol)
	if !ok {
		t.Fatal("protocol must expose the system-prompt handshake")
	}
	sid, err := withPrompt.HandshakeWithSystemPrompt(t.Context(), transport, "sonnet", "/ws", "steer this")
	if err != nil {
		t.Fatal(err)
	}
	if sid == "" {
		t.Fatal("empty session id")
	}
	if len(transport.metas) != 1 {
		t.Fatalf("want one session/new, got %d", len(transport.metas))
	}
	if got := sessionNewAppend(t, transport.metas[0]); got != "steer this" {
		t.Fatalf("append = %q", got)
	}
}

func TestHandshake_LegacyPathSendsNoSystemPrompt(t *testing.T) {
	t.Parallel()
	transport := &sysPromptOptionsTransport{optionsTransport: optionsTransport{
		newOptions: advertisedOptions("sonnet", "medium", "medium"),
	}}
	proto := NewACPProtocol(sysPromptSpec{text: "steer this"}, nil)
	if _, err := proto.Handshake(t.Context(), transport, "sonnet", "/ws"); err != nil {
		t.Fatal(err)
	}
	if len(transport.metas) != 1 {
		t.Fatalf("want one session/new, got %d", len(transport.metas))
	}
	if len(bytes.TrimSpace(transport.metas[0])) == 0 {
		return // legacy handshake omits _meta entirely: no injection.
	}
	var decoded map[string]any
	if err := json.Unmarshal(transport.metas[0], &decoded); err != nil {
		t.Fatal(err)
	}
	if _, present := decoded["systemPrompt"]; present {
		t.Fatalf("legacy handshake must not inject systemPrompt: %s", transport.metas[0])
	}
}

func TestRotateSessionSystemPrompt_RepublishesOptions(t *testing.T) {
	t.Parallel()
	transport := &sysPromptOptionsTransport{optionsTransport: optionsTransport{
		newOptions: advertisedOptions("sonnet", "medium", "medium"),
	}}
	proto := NewACPProtocol(sysPromptSpec{text: "v2"}, nil)
	withPrompt, ok := proto.(SystemPromptSessionProtocol)
	if !ok {
		t.Fatal("protocol must expose the system-prompt handshake")
	}
	first, err := withPrompt.HandshakeWithSystemPrompt(t.Context(), transport, "sonnet", "/ws", "v1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := withPrompt.RotateSessionSystemPrompt(t.Context(), transport, "sonnet", "v2")
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || second == "" {
		t.Fatal("rotation must return session ids")
	}
	if len(transport.metas) != 2 {
		t.Fatalf("want two session/new calls, got %d", len(transport.metas))
	}
	if got := sessionNewAppend(t, transport.metas[1]); got != "v2" {
		t.Fatalf("rotated append = %q", got)
	}
	var modelExchanged bool
	for _, c := range transport.calls {
		if c == "model=sonnet" {
			modelExchanged = true
		}
	}
	if !modelExchanged {
		t.Fatalf("rotation must re-exchange the model option, got %v", transport.calls)
	}
}

// sysPromptTurnProtocol extends the Open-level fake with the system-prompt
// opt-in surfaces, recording delivery decisions for drift assertions.
type sysPromptTurnProtocol struct {
	fakeTurnProtocol
	mu         sync.Mutex
	created    []string
	rotated    []string
	rotateErr  error
	sentCalls  []*aipapi.Call
	sentModels []string
}

func (p *sysPromptTurnProtocol) SessionSystemPrompt(call *aipapi.Call) (string, bool) {
	text := systemInstructionText(call)
	if !validSessionSystemPrompt(text) {
		return "", false
	}
	return text, true
}

func (p *sysPromptTurnProtocol) HandshakeWithSystemPrompt(_ context.Context, _ Transport, _, _ string, sysPrompt string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.created = append(p.created, sysPrompt)
	return "session-created", nil
}

func (p *sysPromptTurnProtocol) RotateSessionSystemPrompt(_ context.Context, _ Transport, _ string, sysPrompt string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rotateErr != nil {
		return "", p.rotateErr
	}
	p.rotated = append(p.rotated, sysPrompt)
	return "session-rotated", nil
}

func (p *sysPromptTurnProtocol) BindSession(transport Transport, sessionID string) SubprocessProtocolSession {
	return &sysPromptTurnSession{proto: p, transport: transport, sessionID: sessionID}
}

type sysPromptTurnSession struct {
	proto     *sysPromptTurnProtocol
	transport Transport
	sessionID string
}

func (s *sysPromptTurnSession) SendPrompt(_ context.Context, _ string, _ string, call *aipapi.Call) (io.ReadCloser, int64, error) {
	s.proto.mu.Lock()
	defer s.proto.mu.Unlock()
	s.proto.sentCalls = append(s.proto.sentCalls, call)
	return io.NopCloser(strings.NewReader("")), 1, nil
}

func (s *sysPromptTurnSession) BuildStream(_ context.Context, _ io.ReadCloser, _ int64, pool *RuntimePool, key RuntimeKey, _ int) (aipapi.ManagedEventStream, error) {
	return &fakeTurnStream{pool: pool, key: key}, nil
}

func openTurn(t *testing.T, backend SubprocessEngine, session string, instructions []string) {
	t.Helper()
	call := newTurnCall(session)
	for _, text := range instructions {
		call.Instructions = append(call.Instructions, aipapi.Message{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart(text)},
		})
	}
	stream, err := backend.Open(context.Background(), call)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpen_SystemPromptDeliveredAtCreationAndStripped(t *testing.T) {
	t.Parallel()
	proto := &sysPromptTurnProtocol{}
	proto.model = "agent"
	proc := newFakeProcess(t)
	backend := NewSubprocessBackend(SubprocessBackendConfig{
		Protocol:       proto,
		Workspace:      WorkspacePolicy{DefaultDir: os.TempDir()},
		Pool:           RuntimePoolConfig{},
		ProcessStarter: &fakeStarter{proc: proc},
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = backend.Close() })

	openTurn(t, backend, "sp1", []string{"steer this"})

	proto.mu.Lock()
	defer proto.mu.Unlock()
	if len(proto.created) != 1 || proto.created[0] != "steer this" {
		t.Fatalf("creation must carry instructions, got %v", proto.created)
	}
	if len(proto.rotated) != 0 {
		t.Fatalf("no rotation expected on first turn, got %v", proto.rotated)
	}
	if len(proto.sentCalls) != 1 {
		t.Fatalf("want one prompt, got %d", len(proto.sentCalls))
	}
	if len(proto.sentCalls[0].Instructions) != 0 {
		t.Fatal("delivered instructions must be stripped from prompt blocks")
	}
}

func TestOpen_SystemPromptRotatesOnDrift(t *testing.T) {
	t.Parallel()
	proto := &sysPromptTurnProtocol{}
	proto.model = "agent"
	proc := newFakeProcess(t)
	backend := NewSubprocessBackend(SubprocessBackendConfig{
		Protocol:       proto,
		Workspace:      WorkspacePolicy{DefaultDir: os.TempDir()},
		Pool:           RuntimePoolConfig{},
		ProcessStarter: &fakeStarter{proc: proc},
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = backend.Close() })

	openTurn(t, backend, "sp2", []string{"v1"})
	openTurn(t, backend, "sp2", []string{"v1"})
	openTurn(t, backend, "sp2", []string{"v2"})

	proto.mu.Lock()
	defer proto.mu.Unlock()
	if len(proto.rotated) != 1 || proto.rotated[0] != "v2" {
		t.Fatalf("drift must rotate once with v2, got %v", proto.rotated)
	}
	for i, sent := range proto.sentCalls {
		if len(sent.Instructions) != 0 {
			t.Fatalf("turn %d: delivered instructions must be stripped", i)
		}
	}
}

func TestOpen_SystemPromptRotationFailureFallsBack(t *testing.T) {
	t.Parallel()
	proto := &sysPromptTurnProtocol{rotateErr: errSysPromptTest}
	proto.model = "agent"
	proc := newFakeProcess(t)
	backend := NewSubprocessBackend(SubprocessBackendConfig{
		Protocol:       proto,
		Workspace:      WorkspacePolicy{DefaultDir: os.TempDir()},
		Pool:           RuntimePoolConfig{},
		ProcessStarter: &fakeStarter{proc: proc},
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = backend.Close() })

	openTurn(t, backend, "sp3", []string{"v1"})
	openTurn(t, backend, "sp3", []string{"v2"})

	proto.mu.Lock()
	defer proto.mu.Unlock()
	if len(proto.sentCalls) != 2 {
		t.Fatalf("want two prompts, got %d", len(proto.sentCalls))
	}
	if len(proto.sentCalls[1].Instructions) == 0 {
		t.Fatal("failed rotation must fall back to flattened instructions, keeping the turn servable")
	}
}
