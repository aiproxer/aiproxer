package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// One process-boundary sentinel proves the pool, protocol and actual stdio
// transport preserve/reset options together, including failure withdrawal.
type optionsProcessStarter struct {
	t            *testing.T
	procs        []*fakeProcess
	done         []chan []string
	admission    bool
	rejectPrompt bool
}

func (s *optionsProcessStarter) Start(_ []string, _ string, _ []string) (Process, error) {
	proc := newFakeProcess(s.t)
	done := make(chan []string, 1)
	s.procs = append(s.procs, proc)
	s.done = append(s.done, done)
	go func() {
		var calls []string
		sequence := 0
		defer func() { done <- calls }()
		fake := &optionsTransport{newOptions: advertisedOptions("opus", "low", "low")}
		scanner := bufio.NewScanner(proc.stdinR)
		for scanner.Scan() {
			body := scanner.Bytes()
			var req rpcRequest
			if err := json.Unmarshal(body, &req); err != nil {
				calls = append(calls, "decode failed")
				return
			}
			var response []byte
			if req.Method == "_test/admit" && s.admission {
				sequence++
				var params map[string]string
				if json.Unmarshal(req.Params, &params) != nil {
					return
				}
				calls = append(calls, "admission")
				response = fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%s,"result":{"sessionId":%q,"checkpointId":%q,"sequence":%d}}`, req.ID, params["sessionId"], params["checkpointId"], sequence)
			} else if req.Method == "session/prompt" {
				calls = append(calls, "prompt")
				response = fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`, req.ID)
				if s.rejectPrompt {
					response = fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32603,"message":"admission refused"}}`, req.ID)
				}
			} else {
				var err error
				response, err = fake.CallUnary(context.Background(), body, 200)
				if err != nil {
					calls = append(calls, "exchange failed")
					return
				}
				if req.Method == "session/set_config_option" {
					calls = append(calls, fake.calls[len(fake.calls)-1])
				}
			}
			if _, err := proc.stdoutW.Write(append(response, '\n')); err != nil {
				return
			}
		}
	}()
	return proc, nil
}

func TestSessionOptions_PooledReuseAndFailureReplacesProcess(t *testing.T) {
	starter := &optionsProcessStarter{t: t}
	engine := NewSubprocessBackend(SubprocessBackendConfig{
		Protocol:  NewACPProtocol(negotiatedOptionsSpec{}, nil),
		Workspace: WorkspacePolicy{DefaultDir: t.TempDir()}, ProcessStarter: starter,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		_ = engine.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		for _, done := range starter.done {
			select {
			case <-done:
			case <-cleanupCtx.Done():
				t.Error("fake agent did not terminate")
			}
		}
	})
	call := newTurnCall("pooled")
	call.Extensions = map[string]json.RawMessage{"acp.model": json.RawMessage(`"sonnet"`)}
	for _, effort := range []string{"high", "", "max", ""} {
		call.Options.ReasoningEffort = effort
		stream, err := engine.Open(ctx, call)
		if effort == "max" {
			if !errors.Is(err, aipapi.ErrCapabilityReject) {
				t.Fatalf("invalid effort: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for {
			_, err := stream.Recv(ctx)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				_ = stream.Close()
				t.Fatal(err)
			}
		}
		if err := stream.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if len(starter.procs) != 2 {
		t.Fatalf("spawn count=%d, want reuse followed by replacement", len(starter.procs))
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	wants := [][]string{
		{"model=sonnet", "effort=high", "prompt", "effort=medium", "prompt"},
		{"model=sonnet", "effort=medium", "prompt"},
	}
	for i, done := range starter.done {
		select {
		case got := <-done:
			if !reflect.DeepEqual(got, wants[i]) {
				t.Fatalf("process %d calls=%v, want %v", i, got, wants[i])
			}
			// Preserve completion for cleanup's ownership check.
			done <- got
		case <-ctx.Done():
			t.Fatal("agent shutdown timed out")
		}
		starter.procs[i].mu.Lock()
		killed := starter.procs[i].killed
		starter.procs[i].mu.Unlock()
		if !killed {
			t.Fatalf("process %d not killed", i)
		}
	}
}
