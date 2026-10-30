package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"testing/synctest"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type admissionSpec struct{ negotiatedOptionsSpec }

func (admissionSpec) PromptAdmissionMethod() string      { return "_test/admit" }
func (admissionSpec) PromptAdmissionMetadataKey() string { return "testAdmission" }
func (admissionSpec) ValidatePromptAdmission(sessionID, nonce string, previous, response json.RawMessage) error {
	var got struct {
		SessionID, CheckpointID string
		Sequence                int
	}
	if json.Unmarshal(response, &got) != nil || got.SessionID != sessionID || got.CheckpointID != nonce || got.Sequence != 1 {
		return errors.New("bad admission")
	}
	if len(previous) > 0 {
		return errors.New("replayed admission")
	}
	return nil
}

type admissionTransport struct {
	*optionsTransport
	bad      bool
	metadata json.RawMessage
}

func (t *admissionTransport) CallUnary(ctx context.Context, body []byte, status int) ([]byte, error) {
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	if req.Method != "_test/admit" {
		return t.optionsTransport.CallUnary(ctx, body, status)
	}
	var params map[string]string
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return nil, err
	}
	if t.bad {
		params["checkpointId"] = "wrong"
	}
	result, _ := json.Marshal(map[string]any{"sessionId": params["sessionId"], "checkpointId": params["checkpointId"], "sequence": 1})
	t.metadata = result
	return fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result), nil
}

func (t *admissionTransport) CallPromptStream(ctx context.Context, body []byte) (io.ReadCloser, error) {
	var req struct {
		Params struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		}
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	if !bytes.Equal(req.Params.Meta["testAdmission"], t.metadata) {
		return nil, errors.New("client metadata escaped or admission missing")
	}
	return t.optionsTransport.CallPromptStream(ctx, body)
}

func TestPromptAdmission_ExactOptionsThenOwnedMetadataAndRetainedState(t *testing.T) {
	transport := &admissionTransport{optionsTransport: &optionsTransport{newOptions: advertisedOptions("opus", "low", "low")}}
	protocol := NewACPProtocol(admissionSpec{}, nil)
	sid, err := protocol.Handshake(t.Context(), transport, "sonnet", "")
	if err != nil {
		t.Fatal(err)
	}
	call := newTurnCall("admission")
	call.Extensions = map[string]json.RawMessage{"testAdmission": json.RawMessage(`{"client":true}`)}
	body, _, err := protocol.BindSession(transport, sid).SendPrompt(t.Context(), "sonnet", "hello", call)
	if err != nil {
		t.Fatal(err)
	}
	_ = body.Close()
	if len(transport.calls) == 0 || transport.calls[len(transport.calls)-1] != "effort=medium" {
		t.Fatalf("options not applied before prompt: %v", transport.calls)
	}
	if transport.prompts != 1 {
		t.Fatalf("prompts=%d", transport.prompts)
	}
	// A fresh handle still sees the previous accepted admission, not client data.
	_, _, err = protocol.BindSession(transport, sid).SendPrompt(t.Context(), "sonnet", "again", call)
	if !isPromptAdmissionFailure(err) || transport.prompts != 1 {
		t.Fatalf("replay escaped: %v prompts=%d", err, transport.prompts)
	}
}

func TestPromptAdmission_FailureIsWithdrawalBeforePrompt(t *testing.T) {
	transport := &admissionTransport{optionsTransport: &optionsTransport{newOptions: advertisedOptions("opus", "low", "low")}, bad: true}
	protocol := NewACPProtocol(admissionSpec{}, nil)
	sid, err := protocol.Handshake(t.Context(), transport, "sonnet", "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = protocol.BindSession(transport, sid).SendPrompt(t.Context(), "sonnet", "hello", &aipapi.Call{})
	if !isPromptAdmissionFailure(err) || transport.prompts != 0 {
		t.Fatalf("bad identity sent prompt: %v", err)
	}
	if errors.Is(err, io.EOF) {
		t.Fatal("unexpected EOF")
	}
}

func TestPromptAdmission_AmbiguityKillsAndResetsBeforeClaimRelease(t *testing.T) {
	starter := &optionsProcessStarter{t: t, admission: true}
	engine := NewSubprocessBackend(SubprocessBackendConfig{Protocol: NewACPProtocol(admissionSpec{}, nil), Workspace: WorkspacePolicy{DefaultDir: t.TempDir()}, ProcessStarter: starter})
	t.Cleanup(func() {
		_ = engine.Close()
		for _, done := range starter.done {
			<-done
		}
	})
	call := newTurnCall("identity-withdrawal")
	call.Extensions = map[string]json.RawMessage{"acp.model": json.RawMessage(`"sonnet"`)}
	stream, err := engine.Open(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, err = stream.Recv(t.Context())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Open(t.Context(), call); !isPromptAdmissionFailure(err) {
		t.Fatalf("replay admitted: %v", err)
	}
	backend, ok := engine.(*subprocessBackend)
	if !ok {
		t.Fatal("unexpected subprocess engine")
	}
	key := backend.runtimeKey(backend.workspace.DefaultDir, "sonnet", CallClientSession(call))
	rt := backend.pool.Get(key)
	if rt == nil || rt.HasProcess() || rt.IsInUse() || rt.HistoryState() != (historyState{}) {
		t.Fatal("ambiguous runtime/history survived claim release")
	}
}

func TestPromptAdmission_RemotePromptRefusalWithdrawsBeforeStreamRelease(t *testing.T) {
	starter := &optionsProcessStarter{t: t, admission: true, rejectPrompt: true}
	engine := NewSubprocessBackend(SubprocessBackendConfig{Protocol: NewACPProtocol(admissionSpec{}, nil), Workspace: WorkspacePolicy{DefaultDir: t.TempDir()}, ProcessStarter: starter})
	t.Cleanup(func() {
		_ = engine.Close()
		for _, done := range starter.done {
			<-done
		}
	})
	call := newTurnCall("remote-refusal")
	call.Extensions = map[string]json.RawMessage{"acp.model": json.RawMessage(`"sonnet"`)}
	stream, err := engine.Open(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	sawError := false
	for {
		ev, err := stream.Recv(t.Context())
		if ev.Kind == aipapi.EventError {
			sawError = true
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !sawError {
		t.Fatal("missing remote refusal")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	backend, ok := engine.(*subprocessBackend)
	if !ok {
		t.Fatal("unexpected subprocess engine")
	}
	rt := backend.pool.Get(backend.runtimeKey(backend.workspace.DefaultDir, "sonnet", CallClientSession(call)))
	if rt == nil || rt.HasProcess() || rt.IsInUse() || rt.HistoryState() != (historyState{}) {
		t.Fatal("remote admission refusal left reusable runtime")
	}
}

func TestPromptAdmission_CancelUnblocksFullPipeAndJoinsWithdrawal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proc := newFakeProcess(t)
		transport := newStdioTransport(proc, nil)
		defer func() { _ = transport.Close() }()
		transport.sessionOptions.current.Store(&sessionOptionState{sessionID: "s"})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var err error
		go func() { _, err = admitPrompt(ctx, newClientFromTransport(transport, nil), "s", admissionSpec{}) }()
		synctest.Wait() // blocked write; there is deliberately no stdin reader
		cancel()
		synctest.Wait()
		if !isPromptAdmissionFailure(err) || !errors.Is(err, context.Canceled) || !transport.closed.Load() || transport.sessionOptions.current.Load() != nil {
			t.Fatalf("cancellation did not withdraw and join: %v", err)
		}
	})
}
