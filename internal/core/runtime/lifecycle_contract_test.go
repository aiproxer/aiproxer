package runtime_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestExecutor_AlegCancellationCancelsManagedBLegBeforeClose(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	inner := &managedBlockingStream{ready: make(chan struct{})}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"managed": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return inner, nil
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "managed-cancel"},
		Route:   aipapi.RouteIntent{Selector: "managed:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := stream.Recv(ctx)
		done <- err
	}()
	<-inner.ready
	cancel()
	err = <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv err = %v want context.Canceled", err)
	}
	if got, want := inner.calls(), []string{"cancel:context_done", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("managed B-leg calls = %v want %v", got, want)
	}
}

func TestExecutor_AlegCancellationPersistsAuthoritativeFinalBilling(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rec := &billingMarkerRecorder{}
	var finalIn execbackend.BillingFinalizationInput
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.SecureSessionRecorder = rec
	ex.Backends = map[string]execbackend.Backend{
		"managed": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &managedBlockingStream{ready: make(chan struct{})}, nil
			},
			FinalizeBilling: func(ctx context.Context, in execbackend.BillingFinalizationInput) (aipapi.Event, error) {
				if err := ctx.Err(); err != nil {
					return aipapi.Event{}, err
				}
				finalIn = in
				return aipapi.Event{
					Kind:         aipapi.EventUsageDelta,
					InputTokens:  2,
					OutputTokens: 3,
					TotalTokens:  5,
					CostSource:   "provider_reported",
					RawUsageJSON: `{"provider":"final"}`,
				}, nil
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "managed-final-billing-cancel"},
		Route:   aipapi.RouteIntent{Selector: "managed:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = stream.Recv(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv err = %v want context.Canceled", err)
	}

	if finalIn.ALegID != call.Session.ALegID || finalIn.BLegID == "" || finalIn.Reason != "context canceled" {
		t.Fatalf("finalizer input = %+v, call A-leg=%q", finalIn, call.Session.ALegID)
	}
	got, ok := rec.usage()
	if !ok {
		t.Fatal("expected final billing usage to be persisted")
	}
	if got.CostSource != "provider_reported" || got.InputTokens != 2 || got.OutputTokens != 3 || got.TotalTokens != 5 {
		t.Fatalf("expected authoritative final usage, got %+v", got)
	}
	if got.RawUsageJSON != `{"provider":"final"}` {
		t.Fatalf("raw usage = %q", got.RawUsageJSON)
	}
	if rec.usageCount() != 1 {
		t.Fatalf("usage events = %d want 1; cancellation must not emit estimate plus final billing", rec.usageCount())
	}
}

func TestExecutor_AlegCancellationBlocksRecvPhaseReplacementBeforeOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lc := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.ALegLifecycle = lc
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return &recoverableBeforeOutputStream{}, nil
			},
		},
		"waste": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "cancel-before-replacement"},
		Route:   aipapi.RouteIntent{Selector: "bad:m|waste:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if err := lc.CancelALeg(context.Background(), call.Session.ALegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv(context.Background())
	if !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("Recv err = %v want ErrALegCanceled", err)
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("opens = %d want 1; replacement opened after A-leg cancellation", got)
	}
}

func TestExecutor_RegisterBLegFailureDoesNotDoubleCancelStream(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lc := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	inner := &managedBlockingStream{ready: make(chan struct{})}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.ALegLifecycle = lc
	ex.Backends = map[string]execbackend.Backend{
		"managed": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if call.Session.ALegID == "" {
					return nil, errors.New("missing a-leg id")
				}
				if err := lc.CancelALeg(ctx, call.Session.ALegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
					return nil, err
				}
				return inner, nil
			},
		},
	}
	_, err = ex.Execute(context.Background(), &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "register-cancel-once"},
		Route:   aipapi.RouteIntent{Selector: "managed:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	})
	if !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("Execute err = %v want ErrALegCanceled", err)
	}
	if got, want := inner.calls(), []string{"cancel:explicit", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("managed B-leg calls = %v want %v", got, want)
	}
}

type managedBlockingStream struct {
	ready chan struct{}
	once  sync.Once
	mu    sync.Mutex
	log   []string
}

type recoverableBeforeOutputStream struct{}

func (s *recoverableBeforeOutputStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	return aipapi.Event{}, aipapi.RecoverablePreOutputError(errors.New("temporary"))
}

func (s *recoverableBeforeOutputStream) Close() error { return nil }

func (s *recoverableBeforeOutputStream) Cancel(context.Context, leglifecycle.CancelCause) leglifecycle.CancelResult {
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeProvider}
}

func (m *managedBlockingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	m.once.Do(func() { close(m.ready) })
	<-ctx.Done()
	return aipapi.Event{}, ctx.Err()
}

func (m *managedBlockingStream) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, "close")
	return nil
}

func (m *managedBlockingStream) Cancel(_ context.Context, cause leglifecycle.CancelCause) leglifecycle.CancelResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, "cancel:"+string(cause.Kind))
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeProvider}
}

func (m *managedBlockingStream) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.log...)
}

func (m *managedBlockingStream) drain() {
	for {
		if _, err := m.Recv(context.Background()); errors.Is(err, io.EOF) {
			return
		}
	}
}

type billingMarkerRecorder struct {
	mu   sync.Mutex
	last app.StreamEventRecordInput
	ok   bool
	n    int
}

func (r *billingMarkerRecorder) RecordClientTurnAfterGate(context.Context, app.ClientTurnRecordInput) error {
	return nil
}

func (r *billingMarkerRecorder) RecordPostHookStreamEvent(_ context.Context, in app.StreamEventRecordInput) error {
	if in.IsUsageEvent {
		r.mu.Lock()
		r.last = in
		r.ok = true
		r.n++
		r.mu.Unlock()
	}
	return nil
}

func (r *billingMarkerRecorder) AppendTranscript(context.Context, domain.TranscriptItem) error {
	return nil
}
func (r *billingMarkerRecorder) AppendAudit(context.Context, domain.AuditItem) error { return nil }
func (r *billingMarkerRecorder) AddUsage(context.Context, domain.UsageDelta) error   { return nil }
func (r *billingMarkerRecorder) TouchActivity(context.Context, domain.SessionID, time.Time, domain.ActivitySource) error {
	return nil
}

func (r *billingMarkerRecorder) usage() (app.StreamEventRecordInput, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last, r.ok
}

func (r *billingMarkerRecorder) usageCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}
