package frontendpipe

// Task 1 (large-payload-lifecycle-correctness): both frontendpipe execution
// seams are transfer-or-dispose. An error-bearing stream is closed, the result
// cleared, and the ORIGINAL error returned in direct and waited modes for both
// the canonical and wire representations (Requirements 2.3, 2.4). Cancellation
// abandonment disposes the eventual stream exactly once (Requirements 2.1, 2.2).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

type ownershipStream struct {
	closed   atomic.Int32
	closeErr error
}

func (s *ownershipStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *ownershipStream) Close() error {
	s.closed.Add(1)
	return s.closeErr
}

func (s *ownershipStream) closeCount() int32 {
	return s.closed.Load()
}

type ownershipExecutor struct {
	fn func(context.Context, *aipapi.Call) (aipapi.EventStream, error)
}

func (e *ownershipExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	return e.fn(ctx, call)
}

func (e *ownershipExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error {
	return nil
}

func (e *ownershipExecutor) WallClock() func() time.Time { return nil }

type ownershipWireExecutor struct {
	fn func(context.Context, largebody.Assessment, largebody.Source) (largebody.ExecutionResult, error)
}

func (e *ownershipWireExecutor) ExecuteLargeBody(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
	return e.fn(ctx, accepted, src)
}

func ownershipTestConfig(exec aipsdk.ExecutorView, log *slog.Logger, keepalive aipsdk.FrontendKeepaliveConfig) Config {
	return Config{
		Exec:                exec,
		Log:                 log,
		PreRequestKeepalive: keepalive,
	}
}

func ownershipLogSink() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func TestLargePayloadLifecycle_ErrorBearingStreamDisposed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		waited  bool
		wire    bool
		boomErr string
	}{
		{name: "canonical direct", boomErr: "canonical direct produce failure"},
		{name: "canonical waited", waited: true, boomErr: "canonical waited produce failure"},
		{name: "wire direct", wire: true, boomErr: "wire direct produce failure"},
		{name: "wire waited", wire: true, waited: true, boomErr: "wire waited produce failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			boom := errors.New(tt.boomErr)
			stream := &ownershipStream{}
			log, _ := ownershipLogSink()
			keepalive := aipsdk.FrontendKeepaliveConfig{}
			if tt.waited {
				keepalive = aipsdk.FrontendKeepaliveConfig{Enabled: true, Interval: time.Millisecond}
			}
			if tt.wire {
				cfg := ownershipTestConfig(nil, log, keepalive)
				var w http.ResponseWriter
				if tt.waited {
					w = httptest.NewRecorder()
				}
				got, err := cfg.executeLargeBody(context.Background(), w,
					&ownershipWireExecutor{
						fn: func(context.Context, largebody.Assessment, largebody.Source) (largebody.ExecutionResult, error) {
							return largebody.ExecutionResult{Stream: stream}, boom
						},
					}, largebody.Assessment{}, nil, tt.waited)
				if !errors.Is(err, boom) {
					t.Fatalf("err=%v want %v", err, boom)
				}
				if got.Stream != nil {
					t.Fatalf("error-bearing wire stream was handed to the caller: %v", got.Stream)
				}
			} else {
				cfg := ownershipTestConfig(&ownershipExecutor{
					fn: func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
						return stream, boom
					},
				}, log, keepalive)
				var w http.ResponseWriter
				if tt.waited {
					w = httptest.NewRecorder()
				}
				got, err := cfg.execute(context.Background(), w, &aipapi.Call{}, tt.waited)
				if !errors.Is(err, boom) {
					t.Fatalf("err=%v want %v", err, boom)
				}
				if got != nil {
					t.Fatalf("error-bearing stream was handed to the caller: %v", got)
				}
			}
			if stream.closeCount() != 1 {
				t.Fatalf("error-bearing stream closes=%d want exactly 1", stream.closeCount())
			}
		})
	}
}

func TestLargePayloadLifecycle_CanonicalNilStreamErrorPassesThrough(t *testing.T) {
	t.Parallel()
	boom := errors.New("canonical nil-stream failure")
	log, _ := ownershipLogSink()
	for _, streamMode := range []bool{false, true} {
		cfg := ownershipTestConfig(&ownershipExecutor{
			fn: func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
				return nil, boom
			},
		}, log, aipsdk.FrontendKeepaliveConfig{Enabled: true, Interval: time.Millisecond})
		got, err := cfg.execute(context.Background(), httptest.NewRecorder(), &aipapi.Call{}, streamMode)
		if !errors.Is(err, boom) {
			t.Fatalf("stream=%v err=%v want %v", streamMode, err, boom)
		}
		if got != nil {
			t.Fatalf("stream=%v got=%v want nil", streamMode, got)
		}
	}
}

func TestLargePayloadLifecycle_CancelAbandonmentDisposesEventualStream(t *testing.T) {
	t.Parallel()

	for _, wire := range []bool{false, true} {
		name := "canonical"
		if wire {
			name = "wire"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			stream := &ownershipStream{}
			started := make(chan struct{})
			log, _ := ownershipLogSink()
			keepalive := aipsdk.FrontendKeepaliveConfig{Enabled: true, Interval: time.Hour}
			returned := make(chan error, 1)
			if wire {
				cfg := ownershipTestConfig(nil, log, keepalive)
				go func() {
					got, err := cfg.executeLargeBody(ctx, httptest.NewRecorder(),
						&ownershipWireExecutor{
							fn: func(ctx context.Context, _ largebody.Assessment, _ largebody.Source) (largebody.ExecutionResult, error) {
								close(started)
								<-ctx.Done()
								return largebody.ExecutionResult{Stream: stream}, nil
							},
						}, largebody.Assessment{}, nil, true)
					if got.Stream != nil {
						t.Errorf("abandoned wire stream was handed to the caller: %v", got.Stream)
					}
					returned <- err
				}()
			} else {
				cfg := ownershipTestConfig(&ownershipExecutor{
					fn: func(ctx context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
						close(started)
						<-ctx.Done()
						return stream, nil
					},
				}, log, keepalive)
				go func() {
					got, err := cfg.execute(ctx, httptest.NewRecorder(), &aipapi.Call{}, true)
					if got != nil {
						t.Errorf("abandoned stream was handed to the caller: %v", got)
					}
					returned <- err
				}()
			}
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("executor did not start")
			}
			cancel()
			select {
			case err := <-returned:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err=%v want context.Canceled", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("execute did not return after cancellation")
			}
			if stream.closeCount() != 1 {
				t.Fatalf("abandoned stream closes=%d want exactly 1", stream.closeCount())
			}
		})
	}
}

func TestLargePayloadLifecycle_CleanupFailurePreservesOriginalError(t *testing.T) {
	t.Parallel()

	for _, wire := range []bool{false, true} {
		name := "canonical"
		if wire {
			name = "wire"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			boom := errors.New("original produce failure")
			cleanupErr := errors.New("owned stream close failed")
			stream := &ownershipStream{closeErr: cleanupErr}
			log, buf := ownershipLogSink()
			if wire {
				cfg := ownershipTestConfig(nil, log, aipsdk.FrontendKeepaliveConfig{})
				_, err := cfg.executeLargeBody(context.Background(), nil,
					&ownershipWireExecutor{
						fn: func(context.Context, largebody.Assessment, largebody.Source) (largebody.ExecutionResult, error) {
							return largebody.ExecutionResult{Stream: stream}, boom
						},
					}, largebody.Assessment{}, nil, false)
				if !errors.Is(err, boom) {
					t.Fatalf("err=%v want original %v", err, boom)
				}
			} else {
				cfg := ownershipTestConfig(&ownershipExecutor{
					fn: func(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
						return stream, boom
					},
				}, log, aipsdk.FrontendKeepaliveConfig{})
				_, err := cfg.execute(context.Background(), nil, &aipapi.Call{}, false)
				if !errors.Is(err, boom) {
					t.Fatalf("err=%v want original %v", err, boom)
				}
			}
			if stream.closeCount() != 1 {
				t.Fatalf("error-bearing stream closes=%d want exactly 1", stream.closeCount())
			}
			if !strings.Contains(buf.String(), "close failed") {
				t.Fatalf("cleanup failure was not reported with fixed diagnostics; log=%q", buf.String())
			}
		})
	}
}
