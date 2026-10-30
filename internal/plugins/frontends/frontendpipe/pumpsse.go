package frontendpipe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// PumpSSE wraps es with recovery keepalive, pumps canonical events through handle until
// it returns done or an error, and closes es on exit (joining close errors).
func PumpSSE(ctx context.Context, w http.ResponseWriter, es aipapi.EventStream, eofWithoutDone error, handle func(aipapi.Event) (done bool, err error)) (err error) {
	return PumpSSEWithFailure(ctx, w, es, eofWithoutDone, handle, nil)
}

// PumpSSEWithFailure reports receive and encoding failures through the wire
// adapter before returning the original error for operator diagnostics. It never
// emits a second terminal for a Close failure after successful completion.
func PumpSSEWithFailure(ctx context.Context, w http.ResponseWriter, es aipapi.EventStream, eofWithoutDone error, handle func(aipapi.Event) (done bool, err error), failure func(error) error) (err error) {
	completed := false
	var owned aipapi.EventStream
	// Register cleanup first so failure delivery runs before this potentially
	// blocking Close. Cleanup stays synchronous and has exactly one owner.
	defer func() {
		if owned != nil {
			if cerr := owned.Close(); cerr != nil {
				err = errors.Join(err, fmt.Errorf("stream: close event stream: %w", cerr))
			}
		}
	}()
	defer func() {
		if err != nil && !completed && failure != nil && ctx.Err() == nil {
			err = errors.Join(err, failure(err))
		}
	}()
	ka, err := stream.WrapRecoveryKeepaliveInterval(es, stream.KeepaliveIntervalFromContext(ctx))
	if err != nil {
		return err
	}
	es = ka
	owned = es

	fl, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("stream: ResponseWriter is not a Flusher")
	}

	for {
		var ev aipapi.Event
		ev, err = es.Recv(ctx)
		if errors.Is(err, io.EOF) {
			if eofWithoutDone != nil {
				return eofWithoutDone
			}
			return fmt.Errorf("stream: ended without terminal event")
		}
		if err != nil {
			return err
		}
		if ev.Kind == aipapi.EventWarning && ev.WarningCode == stream.KeepaliveEventCode {
			if _, err = io.WriteString(w, ": keepalive\n\n"); err != nil {
				return err
			}
			fl.Flush()
			continue
		}
		if ev.Kind == aipapi.EventError {
			return aipapi.NewStreamError(ev.ErrorCode, ev.ErrorMessage)
		}
		var done bool
		done, err = handle(ev)
		completed = done && err == nil
		if done || err != nil {
			return err
		}
	}
}
