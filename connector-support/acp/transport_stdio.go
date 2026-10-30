package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Process abstracts a subprocess for stdio JSON-RPC transport. Tests inject
// fake implementations; production code uses osProcess (transport_stdio_os.go).
type Process interface {
	PID() int
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() error
	Kill() error
}

// ProcessStarter spawns a Process from a command, working directory, and environment.
type ProcessStarter interface {
	Start(cmd []string, cwd string, env []string) (Process, error)
}

var _ Transport = (*stdioTransport)(nil)

const maxStdioLineBytes = 1024 * 1024

// An unsolicited server request has no caller context. Bound its response so
// an idle peer that stops reading stdin cannot strand the sole stdout owner.
const outOfTurnResponseTimeout = 5 * time.Second

// stdioTransport implements Transport over newline-delimited JSON-RPC on a
// subprocess stdin/stdout. A single reader goroutine demultiplexes stdout lines
// into unary responses (matched by JSON-RPC id) and prompt-stream lines
// (notifications, server requests, terminal prompt response).
//
// Concurrency: one reader goroutine started in newStdioTransport; CallUnary and
// CallPromptStream may be called from different goroutines but not concurrently
// with each other (the ACP client serializes handshake then prompt). SendJSONRPC
// and Close are safe for concurrent use. The reader goroutine owns stdout; all
// stdin writes are serialized under writeMu. The closed flag is atomic so Close
// never blocks on writeMu — critical because writeStdin can block on a full
// stdin pipe if the agent isn't reading, and Close must be able to kill the
// process and close stdin to unblock that write.
type stdioTransport struct {
	writeMu   sync.Mutex
	proc      Process
	stdin     io.WriteCloser
	log       *slog.Logger
	closed    atomic.Bool
	closeInit sync.Once
	closeDone chan struct{}
	closeErrM sync.Mutex
	closeErr  error
	options   TransportOptions // installed before the reader starts; never mutated

	// Published/withdrawn atomically with transport lifetime. The runtime claim
	// exclusively owns option exchanges; the reader never mutates this state.
	sessionOptions sessionOptionStore

	pendingMu sync.Mutex
	pending   map[string]chan []byte // keyed by trimmed JSON id bytes

	promptMu     sync.Mutex
	promptReader *lineChannelReader // non-nil while a prompt stream is active
	promptID     string             // trimmed JSON id of active session/prompt
}

// pendingKey trims whitespace from a JSON-RPC id for map key comparison.
func pendingKey(id json.RawMessage) string {
	return string(bytes.TrimSpace(id))
}

// NewStdioTransport wraps an already-started Process as a Transport. A reader
// goroutine is launched immediately and runs until stdout EOF or Close. A
// stderr drain goroutine prevents the agent from blocking on a full stderr pipe.
func NewStdioTransport(proc Process, log *slog.Logger) *stdioTransport {
	return newStdioTransport(proc, log)
}

// NewStdioTransportWithOptions installs immutable lifetime policy before the
// reader starts. The caller owns the already-started process through Close.
func NewStdioTransportWithOptions(proc Process, log *slog.Logger, options TransportOptions) Transport {
	return newStdioTransportWithOptions(proc, log, options)
}

func newStdioTransport(proc Process, log *slog.Logger) *stdioTransport {
	return newStdioTransportWithOptions(proc, log, TransportOptions{})
}

func newStdioTransportWithOptions(proc Process, log *slog.Logger, options TransportOptions) *stdioTransport {
	t := &stdioTransport{
		proc:    proc,
		stdin:   proc.Stdin(),
		log:     log,
		pending: make(map[string]chan []byte),
		options: options,
	}
	go t.readLoop()
	go t.drainStderr()
	return t
}

// readLoop reads stdout lines until EOF or scanner error, routing each line to
// the appropriate consumer (pending unary channel, prompt stream, or auto-respond
// for server requests with no active prompt). Exits cleanly on Close (stdin close
// causes the agent to exit, which causes stdout EOF).
func (t *stdioTransport) readLoop() {
	scanner := bufio.NewScanner(t.proc.Stdout())
	scanner.Buffer(make([]byte, 0, 64*1024), maxStdioLineBytes)
	for scanner.Scan() {
		raw := scanner.Bytes()
		// Copy because scanner reuses the buffer.
		line := make([]byte, len(raw))
		copy(line, raw)
		if err := t.routeLine(line); err != nil {
			if t.log != nil {
				t.log.Debug("acp stdio: route error", "error", err)
			}
		}
	}
	if err := scanner.Err(); err != nil && t.log != nil {
		t.log.Debug("acp stdio: scanner ended", "error", err)
	}
	t.cleanupReadLoop()
}

// routeLine classifies one JSON-RPC line and dispatches it.
func (t *stdioTransport) routeLine(line []byte) error {
	var probe struct {
		ID     json.RawMessage `json:"id,omitempty"`
		Method string          `json:"method,omitempty"`
		Params json.RawMessage `json:"params,omitempty"`
		Result json.RawMessage `json:"result,omitempty"`
		Error  *rpcErrorBody   `json:"error,omitempty"`
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	if err := dec.Decode(&probe); err != nil {
		return fmt.Errorf("acp: decode stdio line: %w", err)
	}

	hasID := len(bytes.TrimSpace(probe.ID)) > 0
	hasResult := len(probe.Result) > 0
	hasError := probe.Error != nil
	hasMethod := probe.Method != ""

	// Response: has id, no method, and result or error.
	if hasID && !hasMethod && (hasResult || hasError) {
		key := pendingKey(probe.ID)

		// Try pending unary first.
		t.pendingMu.Lock()
		ch, ok := t.pending[key]
		if ok {
			delete(t.pending, key)
		}
		t.pendingMu.Unlock()
		if ok {
			// Non-blocking send protects readLoop liveness: a blocking send
			// here would stall all stdout demultiplexing. The channel is
			// buffer-1 with a single matched response, so the default is not
			// reached in normal flow — it only guards against an invariant
			// regression (duplicate send or reused channel).
			select {
			case ch <- line:
			default:
			}
			return nil
		}

		// Try active prompt terminal response.
		t.promptMu.Lock()
		reader := t.promptReader
		isPromptResp := key == t.promptID
		t.promptMu.Unlock()
		if isPromptResp && reader != nil {
			reader.enqueue(line)
			reader.finish()
			t.promptMu.Lock()
			t.promptReader = nil
			t.promptID = ""
			t.promptMu.Unlock()
			return nil
		}
		// Stray response: drop.
		return nil
	}

	// Notification or server request: has method.
	if hasMethod {
		t.promptMu.Lock()
		reader := t.promptReader
		t.promptMu.Unlock()
		if reader != nil {
			reader.enqueue(line)
			return nil
		}
		// No active prompt: auto-respond to server requests (headless proxy).
		if hasID {
			var reply []byte
			var err error
			if !t.options.RefuseOutOfTurnRequests && t.options.AutoApproveOutOfTurnPermissions && probe.Method == "session/request_permission" {
				reply, err = replyServerRequestJSON(probe.ID, AutoApprovePermissionRequest(probe.Params))
			} else if !t.options.RefuseOutOfTurnRequests && !t.options.AutoApproveOutOfTurnPermissions {
				reply, err = replyServerRequestJSON(probe.ID, map[string]any{})
			} else if probe.Method == "session/request_permission" {
				reply, err = replyServerRequestJSON(probe.ID, map[string]any{
					"outcome": map[string]any{"outcome": "cancelled"},
				})
			} else {
				reply, err = replyServerRequestErrorJSON(probe.ID, -32601, "method not found")
			}
			if err != nil {
				return fmt.Errorf("acp: auto-respond marshal: %w", err)
			}
			reply = append(reply, '\n')
			ctx, cancel := context.WithTimeout(context.Background(), outOfTurnResponseTimeout)
			err = t.writeStdinContext(ctx, reply)
			cancel()
			if err != nil {
				return fmt.Errorf("acp: auto-respond write: %w", err)
			}
		}
		// Notifications with no active prompt are dropped.
		return nil
	}

	// Unknown line shape: drop.
	return nil
}

// drainStderr consumes the agent's stderr until EOF. If the agent writes a lot
// to stderr and nobody reads it, the OS pipe buffer fills and the agent blocks.
// Lines are logged at Debug level. The goroutine exits when stderr closes (on
// Close, Kill closes stderrW) or EOF.
//
// Note: os/exec docs say "it is incorrect to call Wait before all reads from the
// pipe have completed." We intentionally race drainStderr with proc.Wait() in
// Close because: (1) stderr is only Debug-level diagnostics, not protocol data;
// (2) Kill forces the process to exit, causing stderr EOF shortly after; (3) the
// worst case is missing the final stderr line, which is acceptable. A fully safe
// alternative would drain stderr to EOF before Wait, but that would block Close
// if the agent doesn't exit promptly.
func (t *stdioTransport) drainStderr() {
	scanner := bufio.NewScanner(t.proc.Stderr())
	scanner.Buffer(make([]byte, 0, 4*1024), 64*1024)
	for scanner.Scan() {
		if t.log != nil {
			t.log.Debug("acp stdio: agent stderr", "line", scanner.Text())
		}
	}
}

// cleanupReadLoop closes all pending channels and the prompt reader so waiters
// unblock promptly when stdout ends.
func (t *stdioTransport) cleanupReadLoop() {
	t.sessionOptions.close()
	t.pendingMu.Lock()
	for key, ch := range t.pending {
		close(ch)
		delete(t.pending, key)
	}
	t.pendingMu.Unlock()

	t.promptMu.Lock()
	reader := t.promptReader
	t.promptReader = nil
	t.promptID = ""
	t.promptMu.Unlock()
	if reader != nil {
		reader.finish()
	}
}

func (t *stdioTransport) writeStdin(data []byte) error {
	if t.closed.Load() {
		return fmt.Errorf("acp: stdio transport closed")
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	// Re-check under the lock: Close may have run between the atomic check and
	// acquiring writeMu. This is the window where Close closes stdin, which will
	// cause the Write to return an error rather than block.
	if t.closed.Load() {
		return fmt.Errorf("acp: stdio transport closed")
	}
	_, err := t.stdin.Write(data)
	return err
}

// writeStdinContext withdraws an ambiguous transport if cancellation interrupts
// a write (including a wait for writeMu). Close deliberately never takes writeMu.
func (t *stdioTransport) writeStdinContext(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		_ = t.Close()
	})
	defer func() {
		if !stop() {
			<-done
		}
	}()
	err := t.writeStdin(data)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// CallUnary writes one JSON-RPC request and waits for the matching response.
// For notifications (no id), it writes and returns immediately. The expectStatus
// parameter is ignored (stdio has no HTTP status).
func (t *stdioTransport) CallUnary(ctx context.Context, body []byte, _ int) ([]byte, error) {
	var req struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("acp: stdio unary: decode request: %w", err)
	}
	key := pendingKey(req.ID)

	line, err := appendNewline(body)
	if err != nil {
		if key == "" {
			return nil, fmt.Errorf("acp: stdio notification: %w", err)
		}
		return nil, fmt.Errorf("acp: stdio unary: %w", err)
	}

	// Notification (no id): write and return immediately.
	if key == "" {
		if err := t.writeStdinContext(ctx, line); err != nil {
			return nil, fmt.Errorf("acp: stdio notification: write: %w", err)
		}
		return nil, nil
	}

	// Register pending response channel.
	ch := make(chan []byte, 1)
	t.pendingMu.Lock()
	if t.closed.Load() {
		t.pendingMu.Unlock()
		return nil, fmt.Errorf("acp: stdio transport closed")
	}
	if _, exists := t.pending[key]; exists {
		t.pendingMu.Unlock()
		return nil, fmt.Errorf("acp: stdio: duplicate request id %q", key)
	}
	t.pending[key] = ch
	t.pendingMu.Unlock()

	if err := t.writeStdinContext(ctx, line); err != nil {
		t.pendingMu.Lock()
		delete(t.pending, key)
		t.pendingMu.Unlock()
		return nil, fmt.Errorf("acp: stdio unary: write: %w", err)
	}

	// Wait for response or cancellation.
	select {
	case raw, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("acp: stdio: transport closed during unary call")
		}
		return raw, nil
	case <-ctx.Done():
		t.pendingMu.Lock()
		delete(t.pending, key)
		t.pendingMu.Unlock()
		return nil, ctx.Err()
	}
}

// CallPromptStream writes session/prompt and returns an io.ReadCloser whose Read
// yields NDJSON lines (notifications, server requests, terminal response) from
// stdout. Only one prompt stream may be active at a time per transport.
func (t *stdioTransport) CallPromptStream(ctx context.Context, body []byte) (io.ReadCloser, error) {
	var req struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("acp: stdio prompt: decode request: %w", err)
	}
	key := pendingKey(req.ID)
	if key == "" {
		return nil, fmt.Errorf("acp: stdio prompt: request has no id")
	}

	if t.closed.Load() {
		return nil, fmt.Errorf("acp: stdio transport closed")
	}

	line, err := appendNewline(body)
	if err != nil {
		return nil, fmt.Errorf("acp: stdio prompt: %w", err)
	}

	reader := newLineChannelReader()

	t.promptMu.Lock()
	if t.promptReader != nil {
		t.promptMu.Unlock()
		return nil, fmt.Errorf("acp: stdio: a prompt stream is already active")
	}
	t.promptReader = reader
	t.promptID = key
	t.promptMu.Unlock()

	if err := t.writeStdinContext(ctx, line); err != nil {
		t.promptMu.Lock()
		t.promptReader = nil
		t.promptID = ""
		t.promptMu.Unlock()
		reader.finish()
		return nil, fmt.Errorf("acp: stdio prompt: write: %w", err)
	}

	return reader, nil
}

// SendJSONRPC writes an arbitrary JSON-RPC line to stdin (e.g. a server-request
// response or a notification).
func (t *stdioTransport) SendJSONRPC(ctx context.Context, body []byte) error {
	line, err := appendNewline(body)
	if err != nil {
		return err
	}
	return t.writeStdinContext(ctx, line)
}

func appendNewline(b []byte) ([]byte, error) {
	if len(b) >= maxStdioLineBytes {
		return nil, fmt.Errorf("acp: stdio line exceeds %d bytes", maxStdioLineBytes)
	}
	out := make([]byte, len(b)+1)
	copy(out, b)
	out[len(b)] = '\n'
	return out, nil
}

// Close kills the subprocess, closes stdin, and unblocks any active prompt reader.
// Safe to call multiple times. Close does NOT acquire writeMu — this is deliberate
// so that it can proceed even when writeStdin is blocked on a full stdin pipe.
// Closing stdin (after killing the process) will unblock the stuck write.
func (t *stdioTransport) Close() error {
	t.closeInit.Do(func() { t.closeDone = make(chan struct{}) })
	if !t.closed.CompareAndSwap(false, true) {
		<-t.closeDone
		return t.CleanupError()
	}
	defer close(t.closeDone)
	t.sessionOptions.close()

	// Capture cleanup errors via the logger rather than discarding with `_`.
	// Concurrent shutdown: a Kill error here means the process is likely already
	// dead (or in a state where the OS won't accept the kill); the readLoop and
	// drainStderr goroutines will still observe stdout/stderr EOF when the OS
	// reaps the process, so they have a defined exit. Logging preserves the
	// signal for production diagnostics without changing goroutine lifecycle.
	if err := t.proc.Kill(); err != nil && t.log != nil {
		t.log.Debug("acp stdio: proc kill during shutdown", "error", err)
	}
	if err := t.proc.Wait(); err != nil {
		if errors.Is(err, ErrCleanupIncomplete) {
			t.setCleanupError(err)
		}
		if t.log != nil {
			t.log.Debug("acp stdio: proc wait during shutdown", "error", err)
		}
	}
	if t.stdin != nil {
		if err := t.stdin.Close(); err != nil && t.log != nil {
			t.log.Debug("acp stdio: stdin close during shutdown", "error", err)
		}
	}

	t.promptMu.Lock()
	reader := t.promptReader
	t.promptReader = nil
	t.promptID = ""
	t.promptMu.Unlock()
	if reader != nil {
		reader.finish()
	}
	return t.CleanupError()
}

// setCleanupError records the cleanup failure this transport observed.
func (t *stdioTransport) setCleanupError(err error) {
	t.closeErrM.Lock()
	t.closeErr = err
	t.closeErrM.Unlock()
}

// CleanupError returns the cleanup failure this transport observed, or nil when
// cleanup has not failed. A transport can be closed without the pool driving
// the retirement (request cancellation closes it), so the pool reads this
// outcome to refuse a runtime whose cleanup is already known to be incomplete.
func (t *stdioTransport) CleanupError() error {
	t.closeErrM.Lock()
	defer t.closeErrM.Unlock()
	return t.closeErr
}

// lineChannelReader implements io.ReadCloser by serving complete NDJSON lines
// (each followed by '\n') from a channel. When the channel is closed or Close is
// called, Read returns io.EOF. It is safe for a single goroutine to call Read.
type lineChannelReader struct {
	ch     chan []byte
	done   chan struct{}
	once   sync.Once
	buf    bytes.Buffer
	closed atomic.Bool
	// betweenSelects is an optional test seam invoked between the buffered
	// fast path and the blocking receive. A line and a finish that arrive in
	// that window leave both select cases ready at once; the seam lets a test
	// exercise that state deterministically. Production code leaves it nil.
	betweenSelects func()
}

func newLineChannelReader() *lineChannelReader {
	return &lineChannelReader{
		ch:   make(chan []byte, 64),
		done: make(chan struct{}),
	}
}

// enqueue writes a line to the channel. Called by the transport's reader goroutine.
// If the reader has been finished/closed, the select on r.done unblocks without panic
// (r.ch is never closed, so there is no send-on-closed-channel risk).
func (r *lineChannelReader) enqueue(line []byte) {
	select {
	case r.ch <- line:
	case <-r.done:
	}
}

// finish signals the reader to stop by closing the done channel. This unblocks
// both Read (via select on done) and enqueue (via select on done) without closing
// r.ch, which would panic on a subsequent send from the readLoop goroutine.
// r.ch is left open and will be garbage-collected when unreferenced.
func (r *lineChannelReader) finish() {
	r.once.Do(func() {
		close(r.done)
	})
}

func (r *lineChannelReader) Read(p []byte) (int, error) {
	// Serve from buffer first (partial line from a previous Read).
	if r.buf.Len() > 0 {
		return r.buf.Read(p)
	}
	// Prioritize buffered lines over the done signal so that finish() — called
	// after enqueuing the terminal response — doesn't cause EOF to preempt
	// lines still in the channel. Without this, bufio.Scanner stops on the
	// first EOF and never reads remaining buffered lines.
	select {
	case line, ok := <-r.ch:
		if !ok {
			return 0, io.EOF
		}
		return r.serveLine(line, p)
	default:
	}
	if r.betweenSelects != nil {
		r.betweenSelects()
	}
	select {
	case line, ok := <-r.ch:
		if !ok {
			return 0, io.EOF
		}
		return r.serveLine(line, p)
	case <-r.done:
		// A line can become buffered between the two selects, after finish()
		// has already closed done. Both cases are then ready at once, and the
		// runtime may pick the finish: drain the line the caller was promised
		// before reporting EOF. finish() runs after the last enqueue, so
		// anything delivered here predates the end of the stream.
		select {
		case line, ok := <-r.ch:
			if !ok {
				return 0, io.EOF
			}
			return r.serveLine(line, p)
		default:
			return 0, io.EOF
		}
	}
}

// serveLine buffers one received line with its NDJSON terminator and serves it.
func (r *lineChannelReader) serveLine(line []byte, p []byte) (int, error) {
	r.buf.Write(line)
	r.buf.WriteByte('\n')
	return r.buf.Read(p)
}

func (r *lineChannelReader) Close() error {
	if r.closed.Swap(true) {
		return nil
	}
	r.once.Do(func() {
		close(r.done)
	})
	return nil
}
