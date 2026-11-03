package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// MaxFrameBytes bounds one NDJSON message in either direction. The host already
// bounds the canonical request; this is the connector-owned private transport
// bound and must stay small enough to reject a damaged child early.
const MaxFrameBytes = 8 << 20

// StderrLimit is the historical raw-stderr retention bound. V1 retains no raw
// stderr window at all: a retained window edge can cut through a credential, so
// only a bounded byte-count status is retained.
const StderrLimit = 4 << 10

// Options describes one private sidecar child. Every value is connector-owned;
// nothing here comes from client request data.
type Options struct {
	Node            string
	Script          string
	Env             []string
	Dir             string
	Secrets         []string
	ShutdownTimeout time.Duration
}

// Process owns one private Node sidecar and its process group. One reader and
// one ordered writer goroutine serialize the NDJSON pipe; the child is never
// shared between conversations in V1.
type Process struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	lines   chan lineResult
	done    chan struct{}
	waitErr error
	waitMu  sync.Mutex

	sends      chan sendRequest
	writerDone chan struct{}
	// writeMu marks the single ordered writer's ownership while it is inside a
	// pipe write. Close never acquires it: it closes the pipe to release a
	// blocked write, so shutdown cannot be pinned behind this lock.
	writeMu sync.Mutex

	secrets []string
	stderr  *stderrBuffer
	wg      sync.WaitGroup

	closeOnce   sync.Once
	closeErr    error
	shutdownDur time.Duration
}

type lineResult struct {
	line []byte
	err  error
}

type sendRequest struct {
	data   []byte
	result chan error
}

// Start launches the private sidecar on inherited stdio pipes. It does not wait
// for the protocol hello; callers use AwaitHello with their own deadline.
func Start(ctx context.Context, opts Options) (*Process, error) {
	if opts.Node == "" || opts.Script == "" {
		return nil, errors.New("claudeagentmodel: sidecar node and script are required")
	}
	if !strings.HasSuffix(opts.Script, ".mjs") {
		return nil, errors.New("claudeagentmodel: sidecar script must be a bundled .mjs bridge")
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 2 * time.Second
	}
	cmd := exec.Command(opts.Node, opts.Script)
	cmd.Env = append([]string(nil), opts.Env...)
	cmd.Dir = opts.Dir
	configureProcessGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claudeagentmodel: sidecar start unavailable")
	}
	p := &Process{
		cmd:         cmd,
		stdin:       stdin,
		lines:       make(chan lineResult, 8),
		done:        make(chan struct{}),
		sends:       make(chan sendRequest),
		writerDone:  make(chan struct{}),
		secrets:     append([]string(nil), opts.Secrets...),
		stderr:      newStderrBuffer(StderrLimit),
		shutdownDur: opts.ShutdownTimeout,
	}
	p.wg.Go(func() { p.readLines(stdout) })
	p.wg.Go(func() { p.readStderr(stderr) })
	p.wg.Go(p.writeLoop)
	p.wg.Go(func() {
		err := cmd.Wait()
		p.waitMu.Lock()
		p.waitErr = err
		p.waitMu.Unlock()
		close(p.done)
	})
	return p, nil
}

// AwaitHello consumes the mandatory protocol greeting and proves the child is
// the expected bundle. Any first frame other than hello is a protocol failure.
func (p *Process) AwaitHello(ctx context.Context) (Hello, error) {
	line, err := p.recv(ctx)
	if err != nil {
		return Hello{}, p.startupError(err)
	}
	var hello Hello
	if err := json.Unmarshal(line, &hello); err != nil || hello.Kind != "hello" {
		return Hello{}, errors.New("claudeagentmodel: sidecar did not greet with a valid hello")
	}
	if hello.SDKVersion == "" || hello.Protocol == "" {
		return Hello{}, errors.New("claudeagentmodel: sidecar hello omitted identity")
	}
	return hello, nil
}

// Hello is the sidecar greeting, used to bind the running child to the pinned
// SDK/Node identity before any request is sent.
type Hello struct {
	Kind        string `json:"kind"`
	Protocol    string `json:"protocol"`
	SDKVersion  string `json:"sdk_version"`
	NodeVersion string `json:"node_version"`
}

// Send enqueues one bounded JSON frame for the single ordered writer. It is
// interruptible by child exit, writer retirement, or Close; use SendContext
// when the caller has its own cancellation bound.
func (p *Process) Send(value any) error {
	return p.SendContext(context.Background(), value)
}

// SendContext is Send with an explicit cancellation bound. A blocked pipe never
// holds an unbreakable lock: the call returns on context completion, child
// exit, writer retirement, or Close.
func (p *Process) SendContext(ctx context.Context, value any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > MaxFrameBytes {
		return errors.New("claudeagentmodel: sidecar frame exceeds bound")
	}
	data = append(data, '\n')
	req := sendRequest{data: data, result: make(chan error, 1)}
	select {
	case p.sends <- req:
	case <-p.writerDone:
		return errors.New("claudeagentmodel: sidecar pipe unavailable")
	case <-p.done:
		return p.exitError()
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.result:
		if err != nil {
			return errors.New("claudeagentmodel: sidecar pipe unavailable")
		}
		return nil
	case <-p.writerDone:
		return errors.New("claudeagentmodel: sidecar pipe unavailable")
	case <-p.done:
		return p.exitError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Process) writeLoop() {
	defer close(p.writerDone)
	for {
		select {
		case req := <-p.sends:
			p.writeMu.Lock()
			_, err := p.stdin.Write(req.data)
			p.writeMu.Unlock()
			req.result <- err
		case <-p.done:
			return
		}
	}
}

// Recv returns the next raw NDJSON line, bounded by MaxFrameBytes.
func (p *Process) Recv(ctx context.Context) ([]byte, error) {
	return p.recv(ctx)
}

func (p *Process) recv(ctx context.Context) ([]byte, error) {
	select {
	case result, ok := <-p.lines:
		if !ok {
			return nil, p.exitError()
		}
		if result.err != nil {
			return nil, result.err
		}
		return result.line, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Diagnostics returns a bounded, secret-free status for child stderr. Raw
// stderr content is never retained or exposed: a lossy window can cut through a
// credential before exact-match redaction, so the connector reports only bytes
// received. It never includes raw upstream payloads or credentials.
func (p *Process) Diagnostics() string {
	return redact(p.stderr.String(), p.secrets...)
}

// Sanitize bounds and redacts one diagnostic-bearing sidecar payload. Every
// error or warning frame passes through it before classification or emission.
func (p *Process) Sanitize(value string) string {
	return boundedText(redact(value, p.secrets...))
}

// Close gracefully shuts down the sidecar, then kills its process group if it
// does not exit within the configured grace. It is idempotent and joins the
// child before returning. The grace bound starts before the first control write
// so a child that stops reading stdin can never pin shutdown.
func (p *Process) Close() error {
	p.closeOnce.Do(func() {
		deadline := time.Now().Add(p.shutdownDur)
		p.sendWithin(map[string]any{"kind": "shutdown"}, deadline)
		// Closing our end unblocks a writer stuck in Write even when the child
		// never reads another byte.
		_ = p.stdin.Close()
		timer := time.NewTimer(max(0, time.Until(deadline)))
		defer timer.Stop()
		select {
		case <-p.done:
		case <-timer.C:
			if err := killProcessGroup(p.cmd); err != nil {
				p.closeErr = fmt.Errorf("claudeagentmodel: sidecar did not exit: %w", err)
			}
			<-p.done
		}
		// The leader is gone, but SDK descendants it spawned can still be alive
		// in the owned group. Retire the group after a clean leader exit too,
		// then observe that the group is actually gone before returning.
		if err := killProcessGroup(p.cmd); err != nil && p.closeErr == nil {
			p.closeErr = fmt.Errorf("claudeagentmodel: sidecar group cleanup failed: %w", err)
		}
		waitProcessGroupGone(p.cmd, 2*time.Second)
		p.wg.Wait()
	})
	return p.closeErr
}

// sendWithin makes one bounded best-effort control write. The absolute deadline
// bounds both enqueueing behind an existing blocked write and the write itself.
func (p *Process) sendWithin(value any, deadline time.Time) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	if len(data) > MaxFrameBytes {
		return
	}
	data = append(data, '\n')
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	req := sendRequest{data: data, result: make(chan error, 1)}
	select {
	case p.sends <- req:
	case <-timer.C:
		return
	case <-p.writerDone:
		return
	case <-p.done:
		return
	}
	select {
	case <-req.result:
	case <-timer.C:
	case <-p.writerDone:
	case <-p.done:
	}
}

// Kill terminates the sidecar process group without a graceful round trip.
func (p *Process) Kill() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	// Release a writer blocked on the closed pipe before killing the group.
	_ = p.stdin.Close()
	return killProcessGroup(p.cmd)
}

func (p *Process) startupError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	detail := p.Diagnostics()
	if strings.TrimSpace(detail) == "" {
		return errors.New("claudeagentmodel: sidecar unavailable")
	}
	return errors.New("claudeagentmodel: sidecar unavailable: " + detail)
}

func (p *Process) exitError() error {
	p.waitMu.Lock()
	err := p.waitErr
	p.waitMu.Unlock()
	if err == nil {
		return io.EOF
	}
	return errors.New("claudeagentmodel: sidecar exited unexpectedly")
}

func (p *Process) readLines(r io.ReadCloser) {
	defer func() { _ = r.Close() }()
	defer close(p.lines)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), MaxFrameBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line) == 0 {
			continue
		}
		select {
		case p.lines <- lineResult{line: line}:
		case <-p.done:
			return
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case p.lines <- lineResult{err: errors.New("claudeagentmodel: sidecar frame exceeds bound or is unreadable")}:
		case <-p.done:
		}
	}
}

func (p *Process) readStderr(r io.ReadCloser) {
	defer func() { _ = r.Close() }()
	// Drain to EOF so a normally noisy child never sees EPIPE; stderrBuffer
	// retains only the bounded, redacted diagnostic window.
	_, _ = io.Copy(p.stderr, r)
}

// stderrBuffer counts child stderr without retaining raw bytes. A retained
// window cannot be redacted safely once its edge cuts through a credential, so
// Diagnostics exposes only a bounded status.
type stderrBuffer struct {
	mu    sync.Mutex
	total int64
}

func newStderrBuffer(int) *stderrBuffer { return &stderrBuffer{} }

func (b *stderrBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	b.total += int64(len(data))
	b.mu.Unlock()
	return len(data), nil
}

func (b *stderrBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.total == 0 {
		return ""
	}
	return fmt.Sprintf("sidecar stderr: %d bytes received; raw content withheld", b.total)
}

var bearerPrefixes = []string{"Bearer ", "bearer ", "sk-ant-", "sk-", "CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_API_KEY="}

// redact removes explicit secret values and common bearer shapes from
// diagnostics. It is deliberately conservative: a false positive costs a
// diagnostic, a false negative costs a credential.
func redact(text string, secrets ...string) string {
	for _, secret := range secrets {
		if strings.TrimSpace(secret) == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	for _, prefix := range bearerPrefixes {
		for {
			index := strings.Index(text, prefix)
			if index < 0 {
				break
			}
			start := index + len(prefix)
			end := start
			for end < len(text) {
				c := text[end]
				if c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '"' || c == '\'' || c == ',' {
					break
				}
				end++
			}
			if end == start {
				break
			}
			text = text[:index] + "[redacted]" + text[end:]
		}
	}
	return text
}
