package taskrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type OutputMode uint8

const (
	Stream OutputMode = iota
	Capture
)

type Request struct {
	Argv           []string
	Dir            string
	Env            []string
	ClearEnv       bool
	Timeout        time.Duration
	Context        context.Context
	Output         OutputMode
	StreamOut      io.Writer
	StreamErr      io.Writer
	StdoutLimit    int
	StderrLimit    int
	AggregateLimit int
	HeadLimit      int
	TailLimit      int
	Label          string
	Redactions     []string
	RestrictAdmin  bool
	// KillGroupOnExit kills the command's process group after cmd.Wait returns,
	// on every outcome, not only on deadline cancellation. It is opt-in for a
	// caller whose leader may exit normally while descendants must not outlive
	// it (for example a test binary that panics before t.Cleanup runs).
	// Default false keeps existing callers unchanged. On Windows the job object
	// already kills surviving members when its handle closes; see
	// windowsProcess.killGroup.
	KillGroupOnExit bool
}

type Kind string

const (
	Success          Kind = "success"
	ChildFailure     Kind = "child_failure"
	DeadlineExceeded Kind = "deadline_exceeded"
	StartFailure     Kind = "start_failure"
	CleanupFailure   Kind = "cleanup_failure"
	InvalidRequest   Kind = "invalid_request"
)

type CleanupResult struct {
	Attempted bool
	Err       error
}

type Result struct {
	Kind            Kind
	ExitCode        int
	Label           string
	Dir             string
	DurationClass   string
	Elapsed         time.Duration
	Accounting      ProcessAccounting
	AccountingErr   error
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
	Cleanup         CleanupResult
	Err             error
}

const (
	defaultStreamLimit = 64 * 1024
	defaultAggregate   = 256 * 1024
	defaultHead        = 32 * 1024
	defaultTail        = 32 * 1024

	// processWaitDelay bounds how long os/exec waits for a command's output
	// pipes to close after the process exits. A descendant that survived the
	// process-group kill can keep the write end of a capture or stream pipe
	// open indefinitely, and without this bound cmd.Wait would never return.
	// One second matches the ACP stdio transport's bound: far longer than a
	// healthy pipe drain needs, short enough to keep teardown bounded.
	processWaitDelay = time.Second
)

func Run(ctx context.Context, req Request) (result Result) {
	started := time.Now()
	result = Result{Label: req.Label}
	defer func() {
		if result.Elapsed == 0 {
			result.Elapsed = time.Since(started)
		}
	}()
	if ctx == nil {
		return invalid(result, "nil caller context")
	}
	if req.Context != nil {
		ctx = req.Context
	}
	if err := normalizeWorkingDirectory(&req); err != nil {
		return invalid(result, err.Error())
	}
	if err := validate(&req); err != nil {
		return invalid(result, err.Error())
	}
	if err := ctx.Err(); err != nil {
		result.Kind, result.Err = DeadlineExceeded, err
		result.DurationClass = durationClass(time.Since(started), req.Timeout)
		return result
	}
	result.Dir = req.Dir
	limits := limitsFor(req)
	env, err := childEnvironment(req)
	if err != nil {
		return invalid(result, err.Error())
	}
	childCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	cmd := exec.Command(req.Argv[0], req.Argv[1:]...)
	cmd.Dir = req.Dir
	cmd.Env = env
	cmd.WaitDelay = processWaitDelay
	adapter, err := newProcessAdapter(cmd, req.RestrictAdmin)
	if err != nil {
		return fail(result, StartFailure, err)
	}

	var outBuf, errBuf *boundedBuffer
	if req.Output == Capture {
		budget := &captureBudget{remaining: limits.aggregate}
		outBuf = newBoundedBuffer(limits.stdout, limits.head, limits.tail, budget)
		errBuf = newBoundedBuffer(limits.stderr, limits.head, limits.tail, budget)
		// Let os/exec own the capture pipes too. Wait then waits for its own
		// copy goroutines and applies WaitDelay when a descendant keeps a pipe
		// open; draining before Wait would never observe a leader that exited
		// successfully while a descendant held the pipes.
		cmd.Stdout, cmd.Stderr = outBuf, errBuf
	} else {
		cmd.Stdout = writerOrDiscard(req.StreamOut)
		cmd.Stderr = writerOrDiscard(req.StreamErr)
	}
	if err := adapter.start(); err != nil {
		_ = adapter.close()
		return fail(result, StartFailure, err)
	}
	if startupErr := adapter.startupCleanupError(); startupErr != nil {
		result.Cleanup.Attempted = true
		result.Cleanup.Err = startupErr
	}

	result = waitForProcess(childCtx, cmd, adapter, result, started, req.Timeout, req.KillGroupOnExit)
	if req.Output == Capture {
		result.Stdout, result.StdoutTruncated = outBuf.Bytes(), outBuf.Truncated()
		result.Stderr, result.StderrTruncated = errBuf.Bytes(), errBuf.Truncated()
	}
	redactResult(&result, req.Redactions)
	return result
}

type outputLimits struct{ stdout, stderr, head, tail, aggregate int }

func limitsFor(req Request) outputLimits {
	limits := outputLimits{req.StdoutLimit, req.StderrLimit, req.HeadLimit, req.TailLimit, req.AggregateLimit}
	if limits.stdout == 0 {
		limits.stdout = defaultStreamLimit
	}
	if limits.stderr == 0 {
		limits.stderr = defaultStreamLimit
	}
	if limits.head == 0 {
		limits.head = defaultHead
	}
	if limits.tail == 0 {
		limits.tail = defaultTail
	}
	if limits.aggregate == 0 {
		limits.aggregate = defaultAggregate
	}
	return limits
}

func validate(req *Request) error {
	if len(req.Argv) == 0 || req.Argv[0] == "" {
		return errors.New("argv must contain an executable")
	}
	if req.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if req.Output != Stream && req.Output != Capture {
		return errors.New("invalid output mode")
	}
	if req.Dir != "" {
		info, err := os.Stat(req.Dir)
		if err != nil {
			return fmt.Errorf("stat cwd: %w", err)
		}
		if !info.IsDir() {
			return errors.New("cwd is not a directory")
		}
	}
	if req.StdoutLimit < 0 || req.StderrLimit < 0 || req.AggregateLimit < 0 || req.HeadLimit < 0 || req.TailLimit < 0 {
		return errors.New("output limits cannot be negative")
	}
	if req.Output == Capture && (req.StdoutLimit > 0 && req.HeadLimit+req.TailLimit > req.StdoutLimit || req.StderrLimit > 0 && req.HeadLimit+req.TailLimit > req.StderrLimit) {
		return errors.New("head and tail limits exceed stream limit")
	}
	for _, value := range req.Env {
		if !strings.Contains(value, "=") || strings.SplitN(value, "=", 2)[0] == "" {
			return fmt.Errorf("invalid environment entry %q", value)
		}
	}
	return nil
}

func normalizeWorkingDirectory(req *Request) error {
	if req.Dir == "" {
		return nil
	}
	abs, err := filepath.Abs(req.Dir)
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	req.Dir = abs
	return nil
}

func childEnvironment(req Request) ([]string, error) {
	var env []string
	if !req.ClearEnv {
		env = append(env, os.Environ()...)
	}
	for _, override := range req.Env {
		key, _, _ := strings.Cut(override, "=")
		found := false
		for i, current := range env {
			if strings.HasPrefix(current, key+"=") {
				env[i], found = override, true
				break
			}
		}
		if !found {
			env = append(env, override)
		}
	}
	return env, nil
}

func writerOrDiscard(writer io.Writer) io.Writer {
	if writer == nil {
		return io.Discard
	}
	return writer
}

func invalid(result Result, message string) Result {
	result.Kind, result.Err = InvalidRequest, errors.New(message)
	return result
}

func fail(result Result, kind Kind, err error) Result {
	result.Kind, result.Err = kind, err
	return result
}

// waitForProcess coordinates process termination, output completion, and timeout handling.
// os/exec owns the output copy goroutines in both modes: Wait returns only after the
// command exits and its pipes drain, and WaitDelay bounds a descendant that keeps a
// pipe open. Starting Wait immediately therefore observes a leader that exits while a
// descendant holds the pipes instead of consuming the caller's whole timeout first.
// On deadline expiration or cancellation, the process adapter is killed so Wait can
// finish; WaitDelay bounds the remaining pipe closure. With killGroupOnExit, the
// adapter's process group is killed after cmd.Wait has reaped the leader, so
// descendants cannot survive a leader that exited on its own.
func waitForProcess(ctx context.Context, cmd *exec.Cmd, adapter processAdapter, result Result, started time.Time, timeout time.Duration, killGroupOnExit bool) Result {
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	var waitErr error
	var killedAt time.Time
	select {
	case waitErr = <-waitDone:
	case <-ctx.Done():
		result.Kind = DeadlineExceeded
		result.Err = ctx.Err()
		result.Cleanup.Attempted = true
		killedAt = time.Now()
		result.Cleanup.Err = adapter.kill()
		waitErr = <-waitDone
	}
	if killGroupOnExit {
		result.Cleanup.Attempted = true
		if err := adapter.killGroup(); err != nil {
			result.Cleanup.Err = errors.Join(result.Cleanup.Err, err)
		}
	}
	if cause := waitDelayCause(waitErr, killedAt); cause != nil {
		result.Cleanup.Attempted = true
		if result.Cleanup.Err == nil {
			result.Cleanup.Err = cause
		}
	}
	result.Accounting, result.AccountingErr = adapter.accounting()
	if closeErr := adapter.close(); closeErr != nil && result.Cleanup.Err == nil {
		result.Cleanup.Err = closeErr
	}
	if result.Kind == "" {
		result.ExitCode = exitCode(waitErr)
		switch {
		case waitErr == nil:
			result.Kind = Success
		case errors.Is(waitErr, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success():
			// The command exited successfully; Wait failed only because its
			// output pipes stayed open past WaitDelay. Keep the successful exit
			// status and let the recorded cleanup error classify the run.
			result.Kind = Success
		default:
			result.Kind = ChildFailure
			result.Err = waitErr
		}
	}
	if result.Kind == DeadlineExceeded || result.Kind == ChildFailure {
		result.ExitCode = exitCode(waitErr)
	}
	if result.Cleanup.Err != nil && result.Kind == Success {
		result.Kind = CleanupFailure
	}
	result.Elapsed = time.Since(started)
	result.DurationClass = durationClass(result.Elapsed, timeout)
	return result
}

// waitDelayCause reports the cleanup failure when cmd.Wait returned only because
// WaitDelay closed the output pipes. os/exec returns exec.ErrWaitDelay only when
// the command itself exited successfully; after a kill it reports the exit error
// and drops that signal, so a wait that consumed the whole bound after the kill
// is not direct evidence: a stalled scheduler or a slow process termination can
// consume the same time. That fallback therefore reports a suspected
// output-pipe timeout and wraps exec.ErrWaitDelay so callers keep one
// classification for both branches.
func waitDelayCause(waitErr error, killedAt time.Time) error {
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		return waitErr
	}
	if killedAt.IsZero() || time.Since(killedAt) < processWaitDelay {
		return nil
	}
	return fmt.Errorf("suspected output-pipe timeout: the wait consumed the full %s delay after the kill, so the output pipes may have stayed open: %w", processWaitDelay, exec.ErrWaitDelay)
}

func durationClass(elapsed, timeout time.Duration) string {
	if elapsed < timeout/3 {
		return "fast"
	}
	if elapsed >= timeout*8/10 {
		return "near_deadline"
	}
	return "normal"
}

func exitCode(err error) int {
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode()
	}
	return 0
}

func redactResult(result *Result, secrets []string) {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		result.Stdout = bytes.ReplaceAll(result.Stdout, []byte(secret), []byte("[REDACTED]"))
		result.Stderr = bytes.ReplaceAll(result.Stderr, []byte(secret), []byte("[REDACTED]"))
	}
}

type captureBudget struct {
	mu        sync.Mutex
	remaining int
}

type boundedBuffer struct {
	limit, head, tail int
	budget            *captureBudget
	headBuf, tailBuf  []byte
	total             int
	truncated         bool
}

func newBoundedBuffer(limit, head, tail int, budget *captureBudget) *boundedBuffer {
	return &boundedBuffer{
		limit:  limit,
		head:   head,
		tail:   tail,
		budget: budget,
	}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.total += len(p)
	if b.total > b.limit {
		b.truncated = true
	}
	b.budget.mu.Lock()
	allowed := min(len(p), b.budget.remaining)
	b.budget.remaining -= allowed
	b.budget.mu.Unlock()
	if allowed == 0 {
		b.truncated = true
		return len(p), nil
	}
	head := min(b.head-len(b.headBuf), allowed)
	b.headBuf = append(b.headBuf, p[:head]...)
	remaining := p[head:allowed]
	tailCapacity := min(b.tail, b.limit-len(b.headBuf))
	if len(remaining) > 0 && tailCapacity > 0 {
		b.tailBuf = append(b.tailBuf, remaining...)
		if len(b.tailBuf) > tailCapacity {
			b.tailBuf = b.tailBuf[len(b.tailBuf)-tailCapacity:]
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte {
	return append(append([]byte(nil), b.headBuf...), b.tailBuf...)
}
func (b *boundedBuffer) Truncated() bool { return b.truncated }
