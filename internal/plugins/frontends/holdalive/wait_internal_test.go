package holdalive

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type recordingWriter struct{ statuses []int }

func (w *recordingWriter) Header() http.Header         { return http.Header{} }
func (w *recordingWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *recordingWriter) WriteHeader(statusCode int) {
	w.statuses = append(w.statuses, statusCode)
}

// When fn ignores ctx, Wait must not block forever on the post-cancel drain: after
// the drain grace it returns ctx.Err() while fn keeps running (buffered done channel
// absorbs the late send, so the goroutine does not leak).
//
//nolint:paralleltest // stubs package-level cancelDrainGrace
func TestWait_contextCancelWithCtxIgnoringFnReturnsAfterDrainGrace(t *testing.T) {
	orig := cancelDrainGrace
	cancelDrainGrace = 20 * time.Millisecond
	stopCh := make(chan struct{})
	t.Cleanup(func() {
		close(stopCh)
		cancelDrainGrace = orig
	})

	ctx, cancel := context.WithCancel(context.Background())
	fnStarted := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		_, err := Wait(ctx, &recordingWriter{}, Config{Enabled: true, Interval: time.Hour},
			func(context.Context) (string, error) {
				close(fnStarted)
				<-stopCh // ignores ctx during test run, terminates on test cleanup
				return "", nil
			}, nil)
		returned <- err
	}()

	<-fnStarted
	cancel()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait blocked indefinitely on fn that ignores ctx")
	}
}
