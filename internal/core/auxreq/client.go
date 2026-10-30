package auxreq

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// ExecutorRunner is satisfied by [*runtime.Executor] for auxiliary delegation.
type ExecutorRunner interface {
	Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error)
}

// Client implements [auxiliary.Client] by delegating to the runtime executor (design §7).
type Client struct {
	exec func() ExecutorRunner
}

// NewClient wraps a lazily resolved executor pointer so [runtimebundle.Build] can bind
// the snapshot before the executor value exists.
func NewClient(exec func() ExecutorRunner) auxiliary.Client {
	if exec == nil {
		return auxiliary.DisabledClient{}
	}
	return Client{exec: exec}
}

func (c Client) Stream(ctx context.Context, req auxiliary.Request) (aipapi.EventStream, error) {
	if req.Call == nil {
		return nil, fmt.Errorf("auxreq: nil call: %w", aipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return nil, aipapi.ErrNilContext
	}
	run := c.exec()
	if run == nil {
		return nil, auxiliary.ErrNotConfigured
	}
	return c.streamWithRunner(ctx, req, run, true)
}

// streamWithRunner executes against a runner captured by the caller. When
// retainPin is true, Stream owns a newly acquired request-generation pin until
// the returned stream terminates. BackgroundScheduler passes false because it
// transfers its submit-time pin to the worker job and releases it after
// collection, never attempting a replacement-generation retain.
func (c Client) streamWithRunner(ctx context.Context, req auxiliary.Request, run ExecutorRunner, retainPin bool) (aipapi.EventStream, error) {
	stream, _, err := c.streamWithRunnerReceipt(ctx, req, run, retainPin)
	return stream, err
}

// streamWithRunnerReceipt is the single canonical auxiliary invocation. It
// performs the existing child-context, detached-policy, scope and pin setup and
// then prefers the lexical optional receipt capability of the runner. An
// Execute-only runner still works and returns an unavailable receipt.
func (c Client) streamWithRunnerReceipt(ctx context.Context, req auxiliary.Request, run ExecutorRunner, retainPin bool) (aipapi.EventStream, execctx.ExecutionReceipt, error) {
	if req.Call == nil {
		return nil, execctx.ExecutionReceipt{}, fmt.Errorf("auxreq: nil call: %w", aipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return nil, execctx.ExecutionReceipt{}, aipapi.ErrNilContext
	}
	if run == nil {
		return nil, execctx.ExecutionReceipt{}, auxiliary.ErrNotConfigured
	}
	childCtx, ok := execctx.IncAuxiliaryDepth(ctx)
	if !ok {
		return nil, execctx.ExecutionReceipt{}, auxiliary.ErrAuxDepthExceeded
	}
	// The auxiliary role travels on a core-owned context carrier, so the
	// executor can capture it once as a typed frozen fact. The label lives in
	// the client-extensible Call.Extensions map as well, but that copy is
	// observability only: a request-part hook may delete it, and the frozen
	// fact may not be dropped.
	childCtx = execctx.WithAuxiliaryRole(childCtx, req.Role)
	if len(req.DisablePlugins) > 0 {
		childCtx = execctx.WithSuppressedPluginIDs(childCtx, req.DisablePlugins)
	}
	if req.SessionMode == auxiliary.SessionModeDetached {
		var err error
		childCtx, err = withDetachedPolicy(childCtx, req)
		if err != nil {
			return nil, execctx.ExecutionReceipt{}, err
		}
	}
	// Preserve parent principal/scope attribution and mark the derived origin separately so
	// auxiliary/internal requests stay correlated to the authoritative parent snapshot without
	// inheriting client origin authority (requirement 4.4). ScopeFromContext returns an owned
	// clone, so we mutate it directly; WithScope clones again on store.
	if derived, ok := scope.ScopeFromContext(ctx); ok {
		derived.Origin = scope.OriginInternal
		if req.ParentTraceID != "" {
			derived.ParentTraceID = scope.Known(req.ParentTraceID)
		}
		childCtx = scope.WithScope(childCtx, derived)
	}
	work := aipapi.CloneCall(*req.Call)
	work.ID = childAuxTraceID(req.ParentTraceID)
	if work.Extensions == nil {
		work.Extensions = map[string]json.RawMessage{}
	}
	encodeLineage(&work, req)

	var pin genpin.Pin
	if retainPin {
		if ret, ok := genpin.FromContext(ctx); ok && ret != nil {
			p, retained := ret.Retain(genpin.KindAsync)
			if !retained || p == nil {
				return nil, execctx.ExecutionReceipt{}, fmt.Errorf("auxreq: runtime generation pin retain failed: %w", aipapi.ErrInvalidCall)
			}
			pin = p
		}
	}

	// The bound is rechecked immediately before the runner is invoked. The
	// preparation above — cloning the call, deriving the scope, retaining the
	// generation pin — can outlive the caller's deadline, and an executor
	// that receives an already-canceled context may still start provider
	// work, so the refusal happens here and not only at the entry.
	if err := childCtx.Err(); err != nil {
		if pin != nil {
			pin.Release()
		}
		return nil, execctx.ExecutionReceipt{}, err
	}

	var (
		stream  aipapi.EventStream
		receipt execctx.ExecutionReceipt
		err     error
	)
	if receiptRun, ok := run.(receiptRunner); ok {
		stream, receipt, err = receiptRun.ExecuteWithReceipt(childCtx, &work)
	} else {
		stream, err = run.Execute(childCtx, &work)
	}
	if err != nil {
		if pin != nil {
			pin.Release()
		}
		return nil, receipt, err
	}
	if pin == nil {
		return stream, receipt, nil
	}
	return &pinnedEventStream{inner: stream, pin: pin}, receipt, nil
}

func (c Client) Collect(ctx context.Context, req auxiliary.Request) (aipapi.Collected, error) {
	s, err := c.Stream(ctx, req)
	if err != nil {
		return aipapi.Collected{}, err
	}
	return aipapi.Collect(ctx, s)
}

// pinnedEventStream releases a generation pin exactly once on terminal Recv
// outcome (EOF/error) or Close. Concurrent Close+Recv cannot underflow.
type pinnedEventStream struct {
	inner aipapi.EventStream
	pin   genpin.Pin
	once  sync.Once
}

func (s *pinnedEventStream) release() {
	s.once.Do(func() {
		if s.pin != nil {
			s.pin.Release()
		}
	})
}

func (s *pinnedEventStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s == nil || s.inner == nil {
		s.release()
		return aipapi.Event{}, io.EOF
	}
	ev, err := s.inner.Recv(ctx)
	if err != nil {
		s.release()
	}
	return ev, err
}

func (s *pinnedEventStream) Close() error {
	var err error
	if s != nil && s.inner != nil {
		err = s.inner.Close()
	}
	s.release()
	return err
}

func childAuxTraceID(parent string) string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%s-aux-fallback", parent)
	}
	suffix := hex.EncodeToString(buf[:])
	if parent != "" {
		return parent + "-aux-" + suffix
	}
	return "aux-" + suffix
}

func encodeLineage(call *aipapi.Call, req auxiliary.Request) {
	// Small JSON blob for observability and future traffic legs; not authoritative for routing.
	type lineage struct {
		Role          string `json:"role,omitempty"`
		Visibility    string `json:"visibility,omitempty"`
		ParentTraceID string `json:"parent_trace_id,omitempty"`
		ParentALegID  string `json:"parent_a_leg_id,omitempty"`
		ParentBLegID  string `json:"parent_b_leg_id,omitempty"`
	}
	ln := lineage{
		Role:          req.Role,
		Visibility:    req.Visibility,
		ParentTraceID: req.ParentTraceID,
		ParentALegID:  req.ParentALegID,
		ParentBLegID:  req.ParentBLegID,
	}
	raw, err := json.Marshal(ln)
	if err != nil {
		return
	}
	const extKey = "aip.aux.lineage.v1"
	call.Extensions[extKey] = raw
}

var (
	_ auxiliary.Client   = Client{}
	_ aipapi.EventStream = (*pinnedEventStream)(nil)
)
