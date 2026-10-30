package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// compile-time contract: stdioTransport satisfies Transport.
func TestStdioTransport_ImplementsTransport(t *testing.T) {
	t.Parallel()
	var _ Transport = (*stdioTransport)(nil)
}

// The exported discovery constructor must install refusal before reading even
// the first unsolicited request. The old constructor retains empty replies.
func TestNewStdioTransportWithOptions_StartupRefusal(t *testing.T) {
	for _, strict := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			proc := newFakeProcess(t)
			go func() {
				proc.writeStdout(`{"jsonrpc":"2.0","id":9,"method":"session/request_permission","params":{}}`)
				proc.writeStdout(`{"jsonrpc":"2.0","id":10,"method":"unknown"}`)
			}()
			var transport Transport
			if strict {
				transport = NewStdioTransportWithOptions(proc, nil, TransportOptions{RefuseOutOfTurnRequests: true})
			} else {
				transport = NewStdioTransport(proc, nil)
			}
			defer func() { _ = transport.Close() }()
			decoder := json.NewDecoder(proc.stdinR)
			for _, id := range []string{"9", "10"} {
				var reply rpcResponse
				if err := decoder.Decode(&reply); err != nil || string(reply.ID) != id {
					t.Fatalf("startup reply: %+v err=%v", reply, err)
				}
				if !strict && string(reply.Result) != `{}` {
					t.Fatal("default transport changed")
				}
				if strict && ((id == "9" && string(reply.Result) != `{"outcome":{"outcome":"cancelled"}}`) || (id == "10" && (reply.Error == nil || reply.Error.Code != -32601))) {
					t.Fatalf("unsafe startup response: %+v", reply)
				}
			}
		})
	}
}

// fakeProcess implements Process for tests using in-memory pipes.
type fakeProcess struct {
	pid     int
	stdinR  *io.PipeReader
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	stderrR *io.PipeReader
	stderrW *io.PipeWriter
	mu      sync.Mutex
	killed  bool
	waitErr error
}

var nextFakePID atomic.Int64

func newFakeProcess(t *testing.T) *fakeProcess {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	return &fakeProcess{
		pid:     int(nextFakePID.Add(1)),
		stdinR:  stdinR,
		stdinW:  stdinW,
		stdoutR: stdoutR,
		stdoutW: stdoutW,
		stderrR: stderrR,
		stderrW: stderrW,
	}
}

func (p *fakeProcess) PID() int              { return p.pid }
func (p *fakeProcess) Stdin() io.WriteCloser { return p.stdinW }
func (p *fakeProcess) Stdout() io.ReadCloser { return p.stdoutR }
func (p *fakeProcess) Stderr() io.ReadCloser { return p.stderrR }

func (p *fakeProcess) Wait() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (p *fakeProcess) Kill() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	_ = p.stdinW.Close()
	_ = p.stdoutW.Close()
	_ = p.stderrW.Close()
	return nil
}

// writeStdout writes a line to the fake process stdout (simulating agent output).
func (p *fakeProcess) writeStdout(line string) {
	_, _ = p.stdoutW.Write([]byte(line + "\n"))
}

func (p *fakeProcess) closeStdout() {
	_ = p.stdoutW.Close()
}

type signaledStdin struct {
	io.WriteCloser
	started chan struct{}
}

func (w signaledStdin) Write(b []byte) (int, error) {
	close(w.started)
	return w.WriteCloser.Write(b)
}

// Transport methods must honor cancellation even when the agent never reads stdin.
// Mutant: perform the write before registering cancellation (the main implementation).
func TestStdioTransport_CancellationUnblocksWritesAndQuiesces(t *testing.T) {
	for _, method := range []string{"unary", "notification", "prompt", "send"} {
		t.Run(method, func(t *testing.T) {
			proc := newFakeProcess(t)
			started := make(chan struct{})
			tr := &stdioTransport{proc: proc, stdin: signaledStdin{proc.Stdin(), started}, pending: make(map[string]chan []byte)}
			readDone, stderrDone := make(chan struct{}), make(chan struct{})
			go func() { defer close(readDone); tr.readLoop() }()
			go func() { defer close(stderrDone); tr.drainStderr() }()
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			t.Cleanup(func() {
				cancel()
				_ = tr.Close()
				<-readDone
				<-stderrDone
			})
			go func() {
				body := []byte(`{"jsonrpc":"2.0","id":1,"method":"session/new"}`)
				var err error
				switch method {
				case "unary":
					_, err = tr.CallUnary(ctx, body, 200)
				case "notification":
					_, err = tr.CallUnary(ctx, []byte(`{"jsonrpc":"2.0","method":"session/cancel"}`), 200)
				case "prompt":
					var reader io.ReadCloser
					reader, err = tr.CallPromptStream(ctx, body)
					if reader != nil {
						_ = reader.Close()
					}
				case "send":
					err = tr.SendJSONRPC(ctx, body)
				}
				done <- err
			}()
			<-started // The in-memory pipe has no reader, so this write cannot complete.
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("blocked %s: %v", method, err)
				}
			case <-time.After(time.Second):
				_ = tr.Close()
				<-done
				t.Fatal("cancelled write remained blocked")
			}
			<-readDone
			<-stderrDone
			if !proc.killed || len(tr.pending) != 0 || tr.promptReader != nil || tr.promptID != "" {
				t.Fatal("cancelled write retained process or transport state")
			}
		})
	}
}

// Claim: an unsolicited server response cannot strand the stdout owner when
// stdin has no reader. Source: PR #1006 adversarial re-review, finding 1.
// Mutant: routeLine writes the response without a deadline.
func TestStdioTransport_OutOfTurnBlockedResponseQuiesces(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proc := newFakeProcess(t)
		started := make(chan struct{})
		tr := &stdioTransport{proc: proc, stdin: signaledStdin{proc.Stdin(), started}, pending: make(map[string]chan []byte)}
		readDone, stderrDone := make(chan struct{}), make(chan struct{})
		go func() { defer close(readDone); tr.readLoop() }()
		go func() { defer close(stderrDone); tr.drainStderr() }()
		t.Cleanup(func() {
			_ = tr.Close()
			<-readDone
			<-stderrDone
		})
		proc.writeStdout(`{"jsonrpc":"2.0","id":99,"method":"session/request_permission"}`)
		<-started // io.Pipe has no stdin reader; even the small reply blocks.
		select {
		case <-readDone:
		case <-time.After(6 * time.Second):
			t.Fatal("out-of-turn response stranded the stdout reader")
		}
		<-stderrDone
		proc.mu.Lock()
		killed := proc.killed
		proc.mu.Unlock()
		if !killed || !tr.closed.Load() {
			t.Fatal("response timeout did not withdraw the owned process/transport")
		}
		if err := tr.SendJSONRPC(t.Context(), []byte(`{"jsonrpc":"2.0","method":"later"}`)); err == nil {
			t.Fatal("withdrawn transport accepted a later write")
		}
	})
}

// readStdin reads one line from the fake process stdin (simulating what the agent received).
func (p *fakeProcess) readStdin() (string, error) {
	scanner := bufio.NewScanner(p.stdinR)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStdioLineBytes)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return scanner.Text(), nil
}

func TestStdioTransport_CallUnary_WritesRequestAndReturnsMatchedResponse(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	// Agent goroutine: read the initialize request, respond.
	go func() {
		req, err := proc.readStdin()
		if err != nil {
			return
		}
		var parsed struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal([]byte(req), &parsed); err != nil {
			return
		}
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      parsed.ID,
			"result":  map[string]any{"protocolVersion": 1},
		}
		b, _ := json.Marshal(resp)
		proc.writeStdout(string(b))
	}()

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := tr.CallUnary(ctx, body, 200)
	if err != nil {
		t.Fatalf("CallUnary: %v", err)
	}
	if !bytes.Contains(got, []byte(`"protocolVersion"`)) {
		t.Fatalf("response missing protocolVersion: %s", got)
	}
	_ = tr.Close()
}

func TestStdioTransport_CallUnary_IgnoresNotificationsBeforeResponse(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	go func() {
		// Read the request.
		req, err := proc.readStdin()
		if err != nil {
			return
		}
		var parsed struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal([]byte(req), &parsed)

		// Send a session/update notification first.
		notif := map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params":  map[string]any{},
		}
		b, _ := json.Marshal(notif)
		proc.writeStdout(string(b))

		// Then send the response.
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      parsed.ID,
			"result":  map[string]any{},
		}
		b, _ = json.Marshal(resp)
		proc.writeStdout(string(b))
	}()

	body := []byte(`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := tr.CallUnary(ctx, body, 200)
	if err != nil {
		t.Fatalf("CallUnary: %v", err)
	}
	if !bytes.Contains(got, []byte(`"result"`)) {
		t.Fatalf("expected response with result: %s", got)
	}
	_ = tr.Close()
}

func TestStdioTransport_CallUnary_NotificationNoIDWritesAndReturns(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	go func() {
		// Read the notification.
		_, _ = proc.readStdin()
		proc.closeStdout()
	}()

	body := []byte(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := tr.CallUnary(ctx, body, 204)
	if err != nil {
		t.Fatalf("CallUnary notification: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty body for notification, got %s", got)
	}
	_ = tr.Close()
}

func TestStdioTransport_CallPromptStream_ForwardsLines(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	go func() {
		// Read the prompt request to get the id.
		req, err := proc.readStdin()
		if err != nil {
			return
		}
		var parsed struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal([]byte(req), &parsed)

		// Send a session/update notification.
		notif := map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId": "s1",
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": "hello"},
				},
			},
		}
		b, _ := json.Marshal(notif)
		proc.writeStdout(string(b))

		// Send terminal response.
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      parsed.ID,
			"result":  map[string]any{"stopReason": "end_turn"},
		}
		b, _ = json.Marshal(resp)
		proc.writeStdout(string(b))
	}()

	body := []byte(`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := tr.CallPromptStream(ctx, body)
	if err != nil {
		t.Fatalf("CallPromptStream: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })

	// Read all lines from the stream.
	all, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !strings.Contains(string(all), "agent_message_chunk") {
		t.Fatalf("missing notification in stream: %s", all)
	}
	if !strings.Contains(string(all), "end_turn") {
		t.Fatalf("missing terminal response in stream: %s", all)
	}
	_ = tr.Close()
}

func TestStdioTransport_SendJSONRPC_WritesSingleLine(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	go func() {
		line, err := proc.readStdin()
		if err != nil {
			return
		}
		if !strings.Contains(line, `"method":"test/send"`) {
			t.Errorf("unexpected stdin content: %s", line)
		}
		proc.closeStdout()
	}()

	body := []byte(`{"jsonrpc":"2.0","id":99,"method":"test/send","params":{}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.SendJSONRPC(ctx, body); err != nil {
		t.Fatalf("SendJSONRPC: %v", err)
	}
	_ = tr.Close()
}

func TestStdioTransport_RejectsConcurrentPromptStream(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	// Drain stdin so CallPromptStream's write doesn't block.
	go func() { _, _ = io.Copy(io.Discard, proc.stdinR) }()

	body := []byte(`{"jsonrpc":"2.0","id":5,"method":"session/prompt","params":{}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc1, err := tr.CallPromptStream(ctx, body)
	if err != nil {
		t.Fatalf("first CallPromptStream: %v", err)
	}
	t.Cleanup(func() { _ = rc1.Close() })

	_, err = tr.CallPromptStream(ctx, body)
	if err == nil {
		t.Fatal("expected error for concurrent prompt stream")
	}
	if !strings.Contains(err.Error(), "already active") {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = tr.Close()
}

func TestStdioTransport_CloseUnblocksPendingUnary(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	// Drain stdin so CallUnary's write succeeds and it blocks waiting for a
	// response on the pending channel (the scenario we want to test).
	go func() { _, _ = io.Copy(io.Discard, proc.stdinR) }()

	// Start a unary call that never gets a response.
	body := []byte(`{"jsonrpc":"2.0","id":7,"method":"initialize","params":{}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type result struct {
		data []byte
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		data, err := tr.CallUnary(ctx, body, 200)
		resCh <- result{data, err}
	}()

	// Wait for the goroutine to register the pending channel by polling for
	// the key's existence. This avoids a racy time.Sleep that could be flaky
	// on slow CI.
	waitPendingRegistered(t, tr, "7")

	// Close kills the process, closes stdout, causing readLoop to exit and
	// cleanup pending channels (which unblocks the waiting CallUnary).
	_ = tr.Close()

	select {
	case res := <-resCh:
		if res.err == nil {
			t.Fatal("expected error from closed transport during unary call")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CallUnary did not unblock after Close")
	}
}

func TestStdioTransport_CloseUnblocksPromptReader(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	// Drain stdin so CallPromptStream's write doesn't block.
	go func() { _, _ = io.Copy(io.Discard, proc.stdinR) }()

	body := []byte(`{"jsonrpc":"2.0","id":8,"method":"session/prompt","params":{}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rc, err := tr.CallPromptStream(ctx, body)
	if err != nil {
		t.Fatalf("CallPromptStream: %v", err)
	}

	// Close the transport — should unblock the prompt reader.
	_ = tr.Close()

	// Read should return EOF promptly.
	all, err := io.ReadAll(rc)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAll after Close: %v", err)
	}
	_ = all
}

func TestNewClientFromTransport_UsesProvidedTransport(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())
	c := newClientFromTransport(tr, slog.Default())
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.t != tr {
		t.Fatal("client transport mismatch")
	}
	_ = tr.Close()
}

func TestAppendNewline_Boundary(t *testing.T) {
	t.Parallel()
	okBody := make([]byte, maxStdioLineBytes-1)
	got, err := appendNewline(okBody)
	if err != nil {
		t.Fatalf("appendNewline largest accepted: %v", err)
	}
	if len(got) != maxStdioLineBytes || got[len(got)-1] != '\n' {
		t.Fatalf("appendNewline largest: len=%d last=%q", len(got), got[len(got)-1])
	}

	_, err = appendNewline(make([]byte, maxStdioLineBytes))
	if err == nil {
		t.Fatal("expected error for at-limit body")
	}
	_, err = appendNewline(make([]byte, maxStdioLineBytes+1))
	if err == nil {
		t.Fatal("expected error for over-limit body")
	}
}

func TestStdioTransport_SendJSONRPC_RejectsOversizedNoWrite(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	wrote := make(chan struct{})
	go func() {
		_, _ = proc.readStdin()
		close(wrote)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := tr.SendJSONRPC(ctx, make([]byte, maxStdioLineBytes))
	if err == nil {
		t.Fatal("expected oversized SendJSONRPC error")
	}
	select {
	case <-wrote:
		t.Fatal("unexpected stdin write on oversized SendJSONRPC")
	case <-time.After(50 * time.Millisecond):
	}
	_ = tr.Close()
}

func TestStdioTransport_CallUnary_RejectsOversizedNoPending(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	wrote := make(chan struct{})
	go func() {
		_, _ = proc.readStdin()
		close(wrote)
	}()

	body := oversizedJSONRPCBody(t, "42")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := tr.CallUnary(ctx, body, 200)
	if err == nil {
		t.Fatal("expected oversized CallUnary error")
	}
	tr.pendingMu.Lock()
	pendingLen := len(tr.pending)
	tr.pendingMu.Unlock()
	if pendingLen != 0 {
		t.Fatalf("pending leaked: %d entries", pendingLen)
	}
	select {
	case <-wrote:
		t.Fatal("unexpected stdin write on oversized CallUnary")
	case <-time.After(50 * time.Millisecond):
	}
	_ = tr.Close()
}

func TestStdioTransport_CallPromptStream_RejectsOversizedNoPrompt(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	wrote := make(chan struct{})
	go func() {
		_, _ = proc.readStdin()
		close(wrote)
	}()

	body := oversizedJSONRPCBody(t, "7")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := tr.CallPromptStream(ctx, body)
	if err == nil {
		t.Fatal("expected oversized CallPromptStream error")
	}
	if rc != nil {
		t.Fatal("expected nil reader on rejection")
	}
	tr.promptMu.Lock()
	reader := tr.promptReader
	promptID := tr.promptID
	tr.promptMu.Unlock()
	if reader != nil || promptID != "" {
		t.Fatalf("prompt state leaked: reader=%v id=%q", reader != nil, promptID)
	}
	select {
	case <-wrote:
		t.Fatal("unexpected stdin write on oversized CallPromptStream")
	case <-time.After(50 * time.Millisecond):
	}
	_ = tr.Close()
}

func oversizedJSONRPCBody(t *testing.T, id string) []byte {
	t.Helper()
	prefix := "{\"jsonrpc\":\"2.0\",\"id\":" + id + ",\"method\":\"session/prompt\",\"params\":{\"p\":\""
	suffix := "\"}}"
	need := max(maxStdioLineBytes-len(prefix)-len(suffix)+1, 1)
	return []byte(prefix + strings.Repeat("a", need) + suffix)
}

// killFunc adapts Process.Kill for t.Cleanup which expects func().
func killFunc(p Process) func() {
	return func() { _ = p.Kill() }
}

// waitPendingRegistered polls the transport's pending map until key is present
// or the deadline expires. This replaces a racy time.Sleep with deterministic
// synchronization.
func waitPendingRegistered(t *testing.T, tr *stdioTransport, key string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tr.pendingMu.Lock()
		_, ok := tr.pending[key]
		tr.pendingMu.Unlock()
		if ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("pending channel for key %q was not registered within 3s", key)
}
