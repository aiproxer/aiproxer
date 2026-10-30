package frontendpipe_test

// Task 1.6 characterization: freeze frontendpipe response/keepalive behavior
// for the future large-payload lane (requirements 17, 18; design sections 1,
// 13 read-only, 16).
//
// What this proves with real existing seams only:
//   - StreamKeepaliveInterval is attached to the request context before body
//     processing (pipe.go ServeHTTP); a positive interval is visible to every
//     downstream stage via stream.KeepaliveIntervalFromContext, while zero
//     leaves the stream default (12s) in effect.
//   - PreRequestKeepalive/holdalive.Wait wraps streaming Execute only:
//     disabled emits no 103; enabled emits >=1 HTTP 103 on slow provider-open
//     while preserving the executor result; non-streaming Execute bypasses
//     holdalive entirely even when enabled.
//   - holdalive.Wait fast paths: nil writer or non-positive interval invokes
//     fn directly with no informational status.
//
// Reused, not duplicated here:
//   - Shared stage ordering incl. AfterDecode/WrapStream hook ownership
//     (pipe_ordering_test.go, pipe_hooks_test.go,
//     pipe_single_admission_characterization_test.go).
//   - holdalive 103/error/cancel-drain semantics (holdalive/wait_test.go,
//     holdalive/wait_internal_test.go).
//   - stdhttp projection of EffectivePreRequestKeepalive/StreamKeepaliveInterval
//     into frontend mounts (stdhttp/http_input_projection_test.go).
//
// What this explicitly does NOT claim (out of scope, needs future tasks):
//   - ExecuteLargeBody is not invoked through this path yet; the frozen
//     invariant is only that a future wire invoke must reuse the same
//     holdalive wrapper after one-way commit and must never emit keepalive
//     bytes before validation/assessment/commit (design 13).
//
// Test-only, no production diff.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/holdalive"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

type freezeBlockingExec struct {
	delay  time.Duration
	stream aipapi.EventStream
	calls  int
}

func (e *freezeBlockingExec) Execute(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
	e.calls++
	select {
	case <-time.After(e.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return e.stream, nil
}

func (e *freezeBlockingExec) CancelALeg(context.Context, aipapi.ALegCancelRequest) error {
	return nil
}
func (e *freezeBlockingExec) WallClock() func() time.Time { return nil }

type freezeStatusWriter struct {
	header   http.Header
	statuses []int
	flushes  int
	body     bytes.Buffer
}

func (w *freezeStatusWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *freezeStatusWriter) Write(p []byte) (int, error) { return w.body.Write(p) }
func (w *freezeStatusWriter) WriteHeader(s int)           { w.statuses = append(w.statuses, s) }
func (w *freezeStatusWriter) Flush()                      { w.flushes++ }

func (w *freezeStatusWriter) count(status int) int {
	n := 0
	for _, s := range w.statuses {
		if s == status {
			n++
		}
	}
	return n
}

func freezeKeepaliveSpec(exec aipsdk.ExecutorView, adm *orderingAdmission, log *orderingLog, streamMode bool, ka aipsdk.FrontendKeepaliveConfig, streamInterval time.Duration, onDecodeCtx func(context.Context), onWrite func()) frontendpipe.Spec[struct{}] {
	return frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:                    exec,
			FrontendID:              "keepalive-freeze",
			DecodeAdmission:         adm,
			DefaultRouteSelector:    "stub:default",
			RoutePrefixes:           routeselect.NewPrefixSet([]string{"stub"}),
			PreRequestKeepalive:     ka,
			StreamKeepaliveInterval: streamInterval,
		},
		Wire: &orderingWire{log: log},
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			if path == "/v1/create" {
				return frontendpipe.PathMatch{}, true
			}
			return frontendpipe.PathMatch{}, false
		},
		RouteFromBodyModel: true,
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			if onDecodeCtx != nil {
				onDecodeCtx(dctx.Ctx)
			}
			return &frontendpipe.Decoded{Call: orderingValidCall(), Stream: streamMode, RouteSelector: dctx.RouteSelector}, nil
		},
		BuildEncodeOpts: func(*frontendpipe.Decoded) struct{} { return struct{}{} },
		WriteStream: func(_ context.Context, w http.ResponseWriter, _ *aipapi.Call, es aipapi.EventStream, _ struct{}) error {
			if es != nil {
				_ = es.Close()
			}
			if onWrite != nil {
				onWrite()
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "stream-ok")
			return nil
		},
		WriteNonStream: func(_ context.Context, w http.ResponseWriter, _ *aipapi.Call, es aipapi.EventStream, _ struct{}) error {
			if es != nil {
				_ = es.Close()
			}
			if onWrite != nil {
				onWrite()
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "nonstream-ok")
			return nil
		},
	}
}

func freezeServeCreate(spec *frontendpipe.Spec[struct{}], w http.ResponseWriter) {
	req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{"model":"stub:body-model"}`))
	frontendpipe.ServeHTTP(spec, w, req)
}

func TestKeepaliveFreeze_StreamIntervalAttachedToDownstreamContext(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	adm := &orderingAdmission{log: log}
	var gotCtx context.Context
	spec := freezeKeepaliveSpec(exec, adm, log, false, aipsdk.FrontendKeepaliveConfig{}, 42*time.Second, func(ctx context.Context) {
		gotCtx = ctx
	}, nil)
	freezeServeCreate(&spec, httptest.NewRecorder())
	if gotCtx == nil {
		t.Fatal("decode never ran")
	}
	if got := stream.KeepaliveIntervalFromContext(gotCtx); got != 42*time.Second {
		t.Fatalf("keepalive interval=%v want 42s", got)
	}
}

func TestKeepaliveFreeze_ZeroStreamIntervalLeavesStreamDefault(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	adm := &orderingAdmission{log: log}
	var gotCtx context.Context
	spec := freezeKeepaliveSpec(exec, adm, log, false, aipsdk.FrontendKeepaliveConfig{}, 0, func(ctx context.Context) {
		gotCtx = ctx
	}, nil)
	freezeServeCreate(&spec, httptest.NewRecorder())
	if gotCtx == nil {
		t.Fatal("decode never ran")
	}
	if got := stream.KeepaliveIntervalFromContext(gotCtx); got != stream.DefaultRecoveryKeepaliveInterval {
		t.Fatalf("keepalive interval=%v want default %v", got, stream.DefaultRecoveryKeepaliveInterval)
	}
}

func TestKeepaliveFreeze_DisabledKeepaliveEmitsNo103OnSlowStreamingOpen(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &freezeBlockingExec{delay: 30 * time.Millisecond, stream: aipapi.NewFixedEventStream(nil)}
	adm := &orderingAdmission{log: log}
	wrote := false
	spec := freezeKeepaliveSpec(exec, adm, log, true, aipsdk.FrontendKeepaliveConfig{}, 0, nil, func() { wrote = true })
	w := &freezeStatusWriter{}
	freezeServeCreate(&spec, w)
	if exec.calls != 1 {
		t.Fatalf("exec calls=%d want 1", exec.calls)
	}
	if !wrote {
		t.Fatal("WriteStream never ran: executor result lost")
	}
	if len(w.statuses) != 1 || w.statuses[0] != http.StatusOK {
		t.Fatalf("statuses=%v want exactly [200]", w.statuses)
	}
	if w.body.String() != "stream-ok" {
		t.Fatalf("body=%q", w.body.String())
	}
}

func TestKeepaliveFreeze_EnabledKeepaliveEmits103OnSlowStreamingOpen(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &freezeBlockingExec{delay: 30 * time.Millisecond, stream: aipapi.NewFixedEventStream(nil)}
	adm := &orderingAdmission{log: log}
	wrote := false
	spec := freezeKeepaliveSpec(exec, adm, log, true, aipsdk.FrontendKeepaliveConfig{Enabled: true, Interval: time.Millisecond}, 0, nil, func() {
		wrote = true
	})
	w := &freezeStatusWriter{}
	freezeServeCreate(&spec, w)
	if exec.calls != 1 {
		t.Fatalf("exec calls=%d want 1", exec.calls)
	}
	if !wrote {
		t.Fatal("WriteStream never ran: executor result lost under keepalive")
	}
	if n := w.count(http.StatusProcessing); n < 1 {
		t.Fatalf("103 count=%d statuses=%v want >=1 before final 200", n, w.statuses)
	}
	if len(w.statuses) == 0 || w.statuses[len(w.statuses)-1] != http.StatusOK {
		t.Fatalf("statuses=%v want final 200", w.statuses)
	}
	if w.flushes != 0 {
		t.Fatal("Flush after 102 would commit a final 200")
	}
	if w.body.String() != "stream-ok" {
		t.Fatalf("body=%q", w.body.String())
	}
}

func TestKeepaliveFreeze_NonStreamExecuteBypassesHoldalive(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &freezeBlockingExec{delay: 30 * time.Millisecond, stream: aipapi.NewFixedEventStream(nil)}
	adm := &orderingAdmission{log: log}
	spec := freezeKeepaliveSpec(exec, adm, log, false, aipsdk.FrontendKeepaliveConfig{Enabled: true, Interval: time.Millisecond}, 0, nil, nil)
	w := &freezeStatusWriter{}
	freezeServeCreate(&spec, w)
	if exec.calls != 1 {
		t.Fatalf("exec calls=%d want 1", exec.calls)
	}
	if len(w.statuses) != 1 || w.statuses[0] != http.StatusOK {
		t.Fatalf("statuses=%v want exactly [200]: non-stream execute must bypass holdalive", w.statuses)
	}
	if w.body.String() != "nonstream-ok" {
		t.Fatalf("body=%q", w.body.String())
	}
}

func TestKeepaliveFreeze_HoldaliveFastPathsInvokeDirectly(t *testing.T) {
	t.Parallel()
	ran := 0
	fn := func(context.Context) (string, error) {
		ran++
		return "direct", nil
	}

	got, err := holdalive.Wait(context.Background(), nil, holdalive.Config{Enabled: true, Interval: time.Millisecond}, fn, nil)
	if err != nil || got != "direct" || ran != 1 {
		t.Fatalf("nil-writer fast path: got=%q err=%v ran=%d", got, err, ran)
	}

	w := &freezeStatusWriter{}
	got, err = holdalive.Wait(context.Background(), w, holdalive.Config{Enabled: true}, fn, nil)
	if err != nil || got != "direct" || ran != 2 {
		t.Fatalf("zero-interval fast path: got=%q err=%v ran=%d", got, err, ran)
	}
	if len(w.statuses) != 0 || w.flushes != 0 {
		t.Fatalf("fast path wrote statuses=%v flushes=%d", w.statuses, w.flushes)
	}
}
