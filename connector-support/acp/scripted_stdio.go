package acp

import (
	"bufio"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
)

// ScriptedStdioAgent is a deterministic in-process ACP stdio peer for connector
// tests. It answers initialize/authenticate/session/new and, on session/prompt,
// emits text + thought chunks then a successful prompt result.
//
// SessionModels, when set, is reported as models.availableModels in session/new
// responses so tests can exercise ACP model enumeration.
type ScriptedStdioAgent struct {
	pid int

	stdinR  *io.PipeReader
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	stderrR *io.PipeReader
	stderrW *io.PipeWriter

	// SessionModels advertises available models in session/new responses.
	SessionModels []ModelInfo
	// SessionCurrentModel is reported as models.currentModelId.
	SessionCurrentModel string

	mu       sync.Mutex
	killed   bool
	waitErr  error
	onceStop sync.Once
	done     chan struct{}

	CancelSeen atomic.Bool
	PromptSeen atomic.Bool
}

var nextScriptedPID atomic.Int64

// NewScriptedStdioAgent starts a background JSON-RPC loop on pipes.
func NewScriptedStdioAgent() *ScriptedStdioAgent {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	a := &ScriptedStdioAgent{
		pid:     int(nextScriptedPID.Add(1)),
		stdinR:  stdinR,
		stdinW:  stdinW,
		stdoutR: stdoutR,
		stdoutW: stdoutW,
		stderrR: stderrR,
		stderrW: stderrW,
		done:    make(chan struct{}),
	}
	go a.loop()
	return a
}

func (a *ScriptedStdioAgent) PID() int              { return a.pid }
func (a *ScriptedStdioAgent) Stdin() io.WriteCloser { return a.stdinW }
func (a *ScriptedStdioAgent) Stdout() io.ReadCloser { return a.stdoutR }
func (a *ScriptedStdioAgent) Stderr() io.ReadCloser { return a.stderrR }

func (a *ScriptedStdioAgent) Wait() error {
	<-a.done
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.waitErr
}

func (a *ScriptedStdioAgent) Kill() error {
	a.mu.Lock()
	a.killed = true
	a.mu.Unlock()
	a.onceStop.Do(func() {
		_ = a.stdinW.Close()
		_ = a.stdoutW.Close()
		_ = a.stderrW.Close()
		close(a.done)
	})
	return nil
}

// ScriptedStarter returns a fresh ScriptedStdioAgent per Start so concurrent
// configure/execute cases do not share a single drained pipe pair.
type ScriptedStarter struct {
	// Agent, when non-nil, is returned once (tests that assert PromptSeen).
	// When nil, Start allocates a new agent each call.
	Agent *ScriptedStdioAgent
	// SessionModels is copied onto every agent Start allocates (including the
	// one-shot Agent) so enumeration tests observe deterministic models.
	SessionModels []ModelInfo
	// SessionCurrentModel is reported as models.currentModelId.
	SessionCurrentModel string
	used                atomic.Bool
}

func (s *ScriptedStarter) Start([]string, string, []string) (Process, error) {
	if s == nil {
		return NewScriptedStdioAgent(), nil
	}
	apply := func(a *ScriptedStdioAgent) *ScriptedStdioAgent {
		if len(s.SessionModels) > 0 {
			a.SessionModels = append([]ModelInfo(nil), s.SessionModels...)
		}
		if s.SessionCurrentModel != "" {
			a.SessionCurrentModel = s.SessionCurrentModel
		}
		return a
	}
	if s.Agent != nil && s.used.CompareAndSwap(false, true) {
		return apply(s.Agent), nil
	}
	return apply(NewScriptedStdioAgent()), nil
}

func (a *ScriptedStdioAgent) loop() {
	defer a.onceStop.Do(func() { close(a.done) })
	sc := bufio.NewScanner(a.stdinR)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sessionID := "scripted-session-1"
	for sc.Scan() {
		line := sc.Text()
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			a.writeResult(req.ID, map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{
					"loadSession": false,
				},
				"authMethods": []any{map[string]any{"id": "cursor_login"}},
			})
		case "authenticate":
			a.writeResult(req.ID, map[string]any{})
		case "session/new":
			available := make([]any, 0, len(a.SessionModels))
			for _, m := range a.SessionModels {
				available = append(available, map[string]any{
					"modelId":     m.ModelID,
					"name":        m.Name,
					"description": m.Description,
				})
			}
			a.writeResult(req.ID, map[string]any{
				"sessionId": sessionID,
				"models": map[string]any{
					"availableModels": available,
					"currentModelId":  a.SessionCurrentModel,
				},
			})
		case "session/prompt":
			a.PromptSeen.Store(true)
			a.writeNotify("session/update", map[string]any{
				"sessionId": sessionID,
				"update": map[string]any{
					"sessionUpdate": "agent_thought_chunk",
					"content":       map[string]any{"type": "text", "text": "think"},
				},
			})
			a.writeNotify("session/update", map[string]any{
				"sessionId": sessionID,
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": "ok-from-scripted-acp"},
				},
			})
			a.writeResult(req.ID, map[string]any{"stopReason": "end_turn"})
		case "session/cancel":
			a.CancelSeen.Store(true)
			if len(req.ID) > 0 && string(req.ID) != "null" {
				a.writeResult(req.ID, map[string]any{})
			}
		default:
			if len(req.ID) > 0 && string(req.ID) != "null" {
				a.writeResult(req.ID, map[string]any{})
			}
		}
	}
}

func (a *ScriptedStdioAgent) writeResult(id json.RawMessage, result any) {
	if len(id) == 0 {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  result,
	})
	_, _ = a.stdoutW.Write(append(payload, '\n'))
}

func (a *ScriptedStdioAgent) writeNotify(method string, params any) {
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	_, _ = a.stdoutW.Write(append(payload, '\n'))
}
