package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/streampump"
)

// IsInboundServerRequest reports whether probe is a JSON-RPC server-initiated
// request: it has a non-empty method, a non-nil id, and no result/error fields.
// methods in exclude are treated as notifications/responses even when they
// carry an id (e.g. ACP's session/update). Codex passes nil to apply no
// exclusions.
func IsInboundServerRequest(probe map[string]any, exclude []string) bool {
	if probe == nil {
		return false
	}
	method, ok := probe["method"].(string)
	if !ok || method == "" {
		return false
	}
	if slices.Contains(exclude, method) {
		return false
	}
	if probe["result"] != nil || probe["error"] != nil {
		return false
	}
	return probe["id"] != nil
}

// NDJSONStreamStrategy encapsulates the protocol-specific behavior that
// NDJSONStreamBase delegates to its concrete stream (ACP promptStream,
// Codex codexStream). Implementations keep provider wire details inside their
// own backend adapter package.
type NDJSONStreamStrategy interface {
	// Label returns the protocol label used in scan/decode error wrapping
	// (e.g. "acp", "codex").
	Label() string
	// IsServerRequest reports whether probe is an inbound JSON-RPC server
	// request. Implementations typically delegate to IsInboundServerRequest
	// with their exclusion list.
	IsServerRequest(probe map[string]any) bool
	// HandleServerRequest processes an inbound server request, returning a
	// fully-wrapped error to terminate the stream. Some implementations send a
	// JSON-RPC error response and return nil to continue streaming.
	HandleServerRequest(ctx context.Context, probe map[string]any) error
	// MapLine maps a non-server-request NDJSON line to canonical events.
	// Implementations handle terminal responses, turn/start responses, and
	// notifications. Returns nil events to continue without enqueuing.
	// ctx is the stream context (not the per-Recv ctx) so trace values
	// propagate and cancellation aligns with Close/parent.
	MapLine(ctx context.Context, line string, probe map[string]any) ([]aipapi.Event, error)
	// OnCancel performs protocol-specific cancellation when Cancel is invoked
	// or when a Recv observes an already-canceled caller context (e.g. ACP
	// sends a cancel-session RPC; Codex cancels the stream context).
	OnCancel()
}

// NDJSONStreamBase owns the shared NDJSON stream mechanics: scanner, body,
// pending queue, response/message-start framing, EOF/unexpected-EOF behavior,
// and close/cancel lifecycle. Concrete streams embed it and supply an
// NDJSONStreamStrategy.
//
// Concurrency: one goroutine calls Recv at a time. Close may run concurrently
// with Recv blocked on scanner.Scan or network I/O; Close cancels the stream
// context and closes the response body so Scan unblocks.
type NDJSONStreamBase struct {
	mu sync.Mutex

	body    io.ReadCloser
	scanner *bufio.Scanner

	pending         streampump.PendingEventQueue
	responseStarted bool
	messageStarted  bool
	after           bool
	closed          bool
	// Admission-protected turns cannot certify completion from transport EOF.
	// Set during stream construction only; legacy protocols retain EOF framing.
	requireTerminalResponse bool

	ctx    context.Context
	cancel context.CancelFunc

	strategy NDJSONStreamStrategy
}

// NewNDJSONStreamBase constructs a base bound to parent (WithCancel), body, and
// the given strategy. maxPending is forwarded to the pending queue (0 = no
// cap). The scanner buffer matches the prior ACP/Codex configuration.
func NewNDJSONStreamBase(parent context.Context, body io.ReadCloser, maxPending int, strategy NDJSONStreamStrategy) *NDJSONStreamBase {
	ctx, cancel := context.WithCancel(parent)
	b := &NDJSONStreamBase{
		body:     body,
		ctx:      ctx,
		cancel:   cancel,
		pending:  streampump.NewPendingEventQueue(maxPending),
		strategy: strategy,
	}
	b.scanner = bufio.NewScanner(body)
	b.scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return b
}

// StreamContext returns the stream-lifetime context (derived from the Open
// parent). Strategies use it for outbound RPCs whose trace values should
// propagate from the parent and whose cancellation aligns with Close/parent.
func (b *NDJSONStreamBase) StreamContext() context.Context { return b.ctx }

// CancelStreamContext cancels the stream-lifetime context. Strategies that
// only need context cancellation (e.g. Codex) call this from OnCancel.
func (b *NDJSONStreamBase) CancelStreamContext() { b.cancel() }

// Close cancels the stream context and closes the response body. Idempotent.
func (b *NDJSONStreamBase) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	b.cancel()
	if b.body != nil {
		return b.body.Close()
	}
	return nil
}

// Cancel is the aipapi.ManagedEventStream hook. It delegates to the strategy's
// OnCancel and reports provider-mode cancellation.
func (b *NDJSONStreamBase) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	b.strategy.OnCancel()
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

func (b *NDJSONStreamBase) ensureResponseStartedLocked() error {
	if b.responseStarted {
		return nil
	}
	if err := b.pending.Push(aipapi.Event{Kind: aipapi.EventResponseStarted}); err != nil {
		return err
	}
	b.responseStarted = true
	return nil
}

func (b *NDJSONStreamBase) ensureMessageStartedLocked() error {
	if b.messageStarted {
		return nil
	}
	if err := b.pending.Push(aipapi.Event{Kind: aipapi.EventMessageStarted}); err != nil {
		return err
	}
	b.messageStarted = true
	return nil
}

func (b *NDJSONStreamBase) enqueueEventsLocked(evs []aipapi.Event) error {
	for _, e := range evs {
		switch e.Kind {
		case aipapi.EventTextDelta, aipapi.EventReasoningDelta:
			if err := b.ensureResponseStartedLocked(); err != nil {
				return err
			}
			if err := b.ensureMessageStartedLocked(); err != nil {
				return err
			}
		case aipapi.EventError, aipapi.EventResponseFinished, aipapi.EventWarning:
			if err := b.ensureResponseStartedLocked(); err != nil {
				return err
			}
		default:
		}
		if err := b.pending.Push(e); err != nil {
			return err
		}
		if e.Kind == aipapi.EventResponseFinished {
			b.after = true
		}
	}
	return nil
}

// PushPendingLocked appends ev to the pending queue under the base mutex.
// Wrappers that inject canonical events from outside the base (e.g. a wrapper
// flushing tool summaries at EOF) must use this rather than touching pending
// directly, so the queue stays protected by b.mu even if the base's locking
// discipline broadens later.
func (b *NDJSONStreamBase) PushPendingLocked(ev aipapi.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pending.Push(ev)
}

// Recv returns the next canonical event. See [aipapi.EventStream] cancellation
// notes: the per-call ctx is honored at entry and forwarded to server-request
// handlers; line mapping uses the stream context. Scan does not observe the
// per-call ctx—use Close (or parent cancellation that closes the body) to
// unblock a blocked Scan.
func (b *NDJSONStreamBase) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		b.strategy.OnCancel()
		_ = b.Close()
		return aipapi.Event{}, err
	}
	for {
		b.mu.Lock()
		if ev, ok := b.pending.PopFront(); ok {
			b.mu.Unlock()
			return ev, nil
		}
		if b.after {
			b.mu.Unlock()
			return aipapi.Event{}, io.EOF
		}
		b.mu.Unlock()

		if !b.scanner.Scan() {
			if err := b.scanner.Err(); err != nil {
				b.mu.Lock()
				preOutput := !b.responseStarted
				b.mu.Unlock()
				if preOutput {
					return aipapi.Event{}, recoverablePreOutput(fmt.Errorf("%s: scan stream: %w", b.strategy.Label(), err))
				}
				return aipapi.Event{}, fmt.Errorf("%s: scan stream: %w", b.strategy.Label(), err)
			}
			b.mu.Lock()
			if !b.responseStarted {
				b.mu.Unlock()
				return aipapi.Event{}, recoverablePreOutput(io.ErrUnexpectedEOF)
			}
			if !b.after {
				if b.requireTerminalResponse {
					b.after = true
					b.mu.Unlock()
					return aipapi.Event{}, io.ErrUnexpectedEOF
				}
				if err := b.ensureResponseStartedLocked(); err != nil {
					b.mu.Unlock()
					return aipapi.Event{}, err
				}
				if err := b.pending.Push(aipapi.Event{Kind: aipapi.EventResponseFinished}); err != nil {
					b.mu.Unlock()
					return aipapi.Event{}, err
				}
				b.after = true
				b.mu.Unlock()
				continue
			}
			b.mu.Unlock()
			return aipapi.Event{}, io.EOF
		}
		line := strings.TrimSpace(b.scanner.Text())
		if line == "" {
			continue
		}

		var probe map[string]any
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			b.mu.Lock()
			preOutput := !b.responseStarted
			b.mu.Unlock()
			if preOutput {
				return aipapi.Event{}, recoverablePreOutput(fmt.Errorf("%s: decode inbound line: %w", b.strategy.Label(), err))
			}
			return aipapi.Event{}, fmt.Errorf("%s: decode inbound line: %w", b.strategy.Label(), err)
		}

		if b.strategy.IsServerRequest(probe) {
			if err := b.strategy.HandleServerRequest(ctx, probe); err != nil {
				return aipapi.Event{}, err
			}
			continue
		}

		evs, err := b.strategy.MapLine(b.ctx, line, probe)
		if err != nil {
			return aipapi.Event{}, err
		}
		if len(evs) == 0 {
			continue
		}
		b.mu.Lock()
		if err := b.enqueueEventsLocked(evs); err != nil {
			b.mu.Unlock()
			return aipapi.Event{}, err
		}
		b.mu.Unlock()
	}
}
