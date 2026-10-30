package aipruntime_test

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// TestClose_ProductionBuild_PinnedStreamDeadlineRetry proves real Build + public
// Close retry/idempotency without reaching into host internals: an open Execute
// stream holds generation work so a short-deadline Close fails, facades stay
// usable, reload rejects after BeginShutdown, and releasing the stream lets
// retry succeed exactly once (Task 7.4 Host tests cover process-close internals).
func TestClose_ProductionBuild_PinnedStreamDeadlineRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := bpkit.MaterializeExampleConfig(t, filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	rt, err := aipruntime.Build(ctx, aipruntime.Options{ConfigPath: cfg, LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	view := rt.ExecutorView()
	if view == nil || !rt.Ready() {
		t.Fatal("runtime must be ready with ExecutorView before Close")
	}

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "dogfood-local:stub-default"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hold-pin-for-close")},
		}},
	}
	stream, err := view.Execute(ctx, call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	err = rt.Close(closeCtx)
	if err == nil {
		_ = stream.Close()
		t.Fatal("expected deadline while Execute stream keeps generation work alive")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		_ = stream.Close()
		t.Fatalf("err=%v want deadline", err)
	}
	if rt.ExecutorView() == nil || rt.ExecutorView() != view {
		_ = stream.Close()
		t.Fatal("ExecutorView must remain usable after deadline Close")
	}
	_ = rt.ReloadStatus()
	late := rt.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI, SafeActor: "after-deadline-close"})
	if late.Category != aipruntime.ResultCanceled {
		_ = stream.Close()
		t.Fatalf("reload after BeginShutdown category=%q want canceled", late.Category)
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("release stream: %v", err)
	}
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer retryCancel()
	if err := rt.Close(retryCtx); err != nil {
		t.Fatalf("retry Close after stream release: %v", err)
	}
	if rt.Ready() {
		t.Fatal("Ready must be false after successful Close")
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("successful Close must be idempotent: %v", err)
	}
	if rt.ExecutorView() == nil {
		t.Fatal("ExecutorView must survive successful Close")
	}
	if rt.ReloadControl() == nil {
		t.Fatal("ReloadControl must survive successful Close")
	}
}

func TestClose_RetryAfterHostFailureThenIdempotent(t *testing.T) {
	t.Parallel()
	host := &facadeFakeHost{closeErrs: []error{errors.New("tracing shutdown boom")}}
	rt := aipruntime.NewRuntimeWithHostForTest(host)

	err := rt.Close(context.Background())
	if err == nil || err.Error() != "tracing shutdown boom" {
		t.Fatalf("first Close err=%v", err)
	}
	if host.closeCalls.Load() != 1 {
		t.Fatalf("close calls=%d want 1", host.closeCalls.Load())
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if host.closeCalls.Load() != 2 {
		t.Fatalf("close calls=%d want 2", host.closeCalls.Load())
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
	if host.closeCalls.Load() != 2 {
		t.Fatalf("successful Close must not re-enter host: calls=%d", host.closeCalls.Load())
	}
}

func TestClose_Idempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := aipruntime.Build(ctx, aipruntime.Options{ConfigPath: repoConfigPath(t), LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !rt.Ready() {
		t.Fatal("runtime must be ready before Close")
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if rt.Ready() {
		t.Fatal("runtime must not report ready after Close")
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if rt.ExecutorView() == nil {
		t.Fatal("ExecutorView must survive Close")
	}
}

func TestClose_DeadlineLeavesFacadeRetryable(t *testing.T) {
	t.Parallel()
	blocked := make(chan struct{})
	host := &closeDeadlineHost{
		block: blocked,
		inner: &facadeFakeHost{
			ready: true,
			reload: func(context.Context, sdkreload.Trigger) sdkreload.Result {
				return sdkreload.Result{Category: sdkreload.ResultCanceled}
			},
		},
	}
	rt := aipruntime.NewRuntimeWithHostForTest(host)

	closeCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	err := rt.Close(closeCtx)
	if err == nil {
		t.Fatal("expected deadline while host Close blocked")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want deadline", err)
	}
	close(blocked)
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
	late := rt.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI})
	if late.Category != aipruntime.ResultCanceled {
		t.Fatalf("reload category=%q want canceled", late.Category)
	}
}

func TestClose_ConcurrentWithReloadStatusExecute(t *testing.T) {
	t.Parallel()
	for i := range 20 {
		ctx := context.Background()
		rt, err := aipruntime.Build(ctx, aipruntime.Options{ConfigPath: repoConfigPath(t), LogWriter: io.Discard})
		if err != nil {
			t.Fatalf("iter %d Build: %v", i, err)
		}
		view := rt.ExecutorView()
		var wg sync.WaitGroup
		wg.Add(4)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = rt.Close(cctx)
		}()
		go func() {
			defer wg.Done()
			_ = rt.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI, SafeActor: "race"})
		}()
		go func() {
			defer wg.Done()
			_ = rt.ReloadStatus()
			_ = rt.ReloadControl()
		}()
		go func() {
			defer wg.Done()
			if view != nil {
				_, _ = view.Execute(context.Background(), &aipapi.Call{})
			}
		}()
		wg.Wait()
		_ = rt.Close(context.Background())
		if rt.ExecutorView() == nil || rt.ReloadControl() == nil {
			t.Fatalf("iter %d facade handles must remain non-nil", i)
		}
	}
}

type closeDeadlineHost struct {
	inner   *facadeFakeHost
	block   chan struct{}
	entered atomic.Bool
}

func (h *closeDeadlineHost) ExecutorView() aipsdk.ExecutorView { return h.inner.ExecutorView() }
func (h *closeDeadlineHost) Ready() bool                       { return h.inner.Ready() }
func (h *closeDeadlineHost) Capabilities() aipruntime.HostCapabilities {
	return h.inner.Capabilities()
}
func (h *closeDeadlineHost) MeteringQuerier() metering.Querier { return h.inner.MeteringQuerier() }
func (h *closeDeadlineHost) ReadinessReport() controlplane.ReadinessReportReader {
	return h.inner.ReadinessReport()
}

func (h *closeDeadlineHost) RefreshSnapshots(ctx context.Context) error {
	return h.inner.RefreshSnapshots(ctx)
}

func (h *closeDeadlineHost) Reload(ctx context.Context, t sdkreload.Trigger) sdkreload.Result {
	return h.inner.Reload(ctx, t)
}
func (h *closeDeadlineHost) Status() sdkreload.Status { return h.inner.Status() }
func (h *closeDeadlineHost) Close(ctx context.Context) error {
	if !h.entered.Swap(true) {
		select {
		case <-h.block:
			return nil
		case <-ctx.Done():
			h.entered.Store(false)
			return ctx.Err()
		}
	}
	return h.inner.Close(ctx)
}
