package frontendpipe_test

// PR #1064 review finding F1 (RED): an observer panic between the wire
// executor return and the transferred-stream guard registration strands the
// accepted execution stream. This test injects a panic from
// OnStageDuration("execution", ...) and requires the transferred stream to be
// disposed, the source reclaimed through it, and the panic to propagate.
// It FAILS on the current code (guard registered after the observer call).

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
)

type pr1064F1PanicObserver struct {
	sentinel error
}

func (o *pr1064F1PanicObserver) OnPipelineStage(largebody.PipelineStage) {}
func (o *pr1064F1PanicObserver) OnDecline(string)                        {}
func (o *pr1064F1PanicObserver) OnCapture(string, largebody.StorageKind) {}
func (o *pr1064F1PanicObserver) OnReplay()                               {}
func (o *pr1064F1PanicObserver) OnRewrite()                              {}
func (o *pr1064F1PanicObserver) OnActiveSpoolBytes(uint64, int64)        {}
func (o *pr1064F1PanicObserver) OnStageDuration(stage string, _ time.Duration) {
	if stage == "execution" {
		panic(o.sentinel)
	}
}

func TestPR1064_F1_ExecutionObserverPanicDisposesTransferredStream(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("pr1064 f1 execution observer panic")
	tracker := &handoffTrackingStream{}
	h := newHandoffHarness(func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
		tracker.attach(src)
		return largebody.ExecutionResult{Stream: tracker}, nil
	}, func(cfg *frontendpipe.LargePayloadConfig) {
		cfg.Diagnostics = &pr1064F1PanicObserver{sentinel: sentinel}
	})

	payload := buildJSONPayload(1200 * 1024)
	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	panicked := serveHandoffRecovered(&h.spec, rec, req)
	if panicked == nil || panicked != any(sentinel) {
		t.Fatalf("panic=%v want the execution-observer sentinel to propagate unchanged", panicked)
	}
	if got := tracker.closeCount(); got != 1 {
		t.Fatalf("transferred stream closes=%d want exactly 1 despite the observer panic (stream stranded)", got)
	}
	if !handoffSourceClosed(t, *h.capturedSrc) {
		t.Fatal("transferred-stream cleanup must reclaim the owned source on observer-panic unwinding")
	}
	if *h.decodeCalls != 0 {
		t.Fatalf("Spec.Decode calls=%d want 0 (no canonical fallback after commitment)", *h.decodeCalls)
	}
}
