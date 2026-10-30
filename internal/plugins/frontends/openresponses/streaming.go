package openresponses

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

const maxStreamingEvents = 100_000

type sseFlusher interface {
	Flush()
}

// sseResponseWriter is a response-writer seam for SSE streams. It sets the SSE
// headers exactly once, immediately before the first event bytes reach the
// underlying writer, and derives its committed state from the actual write
// result rather than from the success of the enclosing event. Once any bytes
// have been accepted (n > 0) or a write succeeded, the response is committed:
// callers must then only terminate/cleanup and must never attempt a JSON error
// response or header mutation. A failure with zero bytes accepted keeps the
// seam uncommitted so a normal JSON error is still possible.
type sseResponseWriter struct {
	w         http.ResponseWriter
	committed bool
}

func (s *sseResponseWriter) Header() http.Header {
	return s.w.Header()
}

func (s *sseResponseWriter) Write(p []byte) (int, error) {
	if !s.committed {
		h := s.w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		n, err := s.w.Write(p)
		if n > 0 || err == nil {
			s.committed = true
		}
		return n, err
	}
	return s.w.Write(p)
}

func (s *sseResponseWriter) Flush() {
	// net/http may commit the response when Flush is called even if no body
	// write reached the underlying writer. Keep the seam's state aligned with
	// that transport behavior so callers never attempt a JSON fallback after a
	// flush has committed headers.
	if !s.committed {
		s.committed = true
	}
	if f, ok := s.w.(sseFlusher); ok {
		f.Flush()
	}
}

func (h *Handler) serveStreaming(ctx context.Context, w http.ResponseWriter, stream aipapi.EventStream, decoded *DecodedCreate, responseID string, store aipcont.Store, scope aipcont.Scope, isReserved bool, owner continuationReservationOwner) {
	fallbackModel := ""
	if decoded != nil {
		fallbackModel = decoded.Model
	}
	h.serveStreamingWithModel(ctx, w, stream, decoded, fallbackModel, responseID, store, scope, isReserved, owner)
}

func (h *Handler) serveStreamingWithModel(ctx context.Context, w http.ResponseWriter, stream aipapi.EventStream, decoded *DecodedCreate, fallbackModel string, responseID string, store aipcont.Store, scope aipcont.Scope, isReserved bool, owner continuationReservationOwner, extraOpts ...aipapi.GenerationOptions) {
	if stream == nil {
		if isReserved && store != nil {
			cleanupContinuationReservation(store, scope, responseID)
		}
		writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
		return
	}
	var sm *proto.StateMachine
	terminalSuccess := false
	cleanupConsumed := false
	defer func() { _ = stream.Close() }()
	defer func() {
		if terminalSuccess || cleanupConsumed {
			return
		}
		if isReserved && store != nil {
			// Frontend-owned reservations are released on every non-terminal
			// exit, including validation/state-machine failures.
			cleanupContinuationReservation(store, scope, responseID)
			return
		}
		if owner != nil {
			// Recorder ownership was transferred, but no successful terminal
			// record exists. Release the reservation exactly once.
			safeReleaseContinuationReservation(owner)
		}
	}()

	if responseID == "" {
		ids := h.cfg.ResponseIDSource
		if ids == nil {
			ids = systemResponseIDSource{}
		}
		responseID = ids.NewResponseID()
	}
	clock := h.cfg.ResponseClock
	if clock == nil {
		clock = systemResponseClock{}
	}
	model := fallbackModel
	var storePtr *bool
	var callOpts aipapi.GenerationOptions
	var prevID string
	if decoded != nil {
		model = decoded.Model
		storePtr = &decoded.Store
		if decoded.Call != nil {
			callOpts = decoded.Call.Options
		}
		prevID = decoded.PreviousResponseID
	} else {
		storeVal := false
		storePtr = &storeVal
		if len(extraOpts) > 0 {
			callOpts = extraOpts[0]
		}
	}
	sm = proto.NewStateMachine(proto.EnvelopeMetadata{
		ResponseID:         responseID,
		PreviousResponseID: prevID,
		CreatedAt:          clock.Now(),
		Model:              model,
		Store:              storePtr,
	}, callOpts, effectiveProtocolLimits(h.cfg.ProtocolLimits))
	seam := &sseResponseWriter{w: w}
	sse := proto.NewSSEWriter(seam)
	committed := func() bool { return seam.committed }

	flush := func() {
		seam.Flush()
	}
	writeEvents := func(events []proto.StreamEvent) error {
		for _, event := range events {
			if err := sse.WriteEvent(event); err != nil {
				if !committed() {
					w.Header().Del("Content-Type")
					w.Header().Del("Cache-Control")
					w.Header().Del("Connection")
				}
				return err
			}
			flush()
		}
		return nil
	}

	failUncommitted := func() {
		if isReserved && store != nil {
			cleanupContinuationReservation(store, scope, responseID)
			isReserved = false
		}
		writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
	}

	last, err := driveStateMachine(ctx, stream, sm, driveOptions{
		limits:         streamingDriveLimits(),
		stopOnTerminal: true,
		sanitizeError:  true,
	}, func(_ aipapi.Event, events []proto.StreamEvent) error {
		if writeErr := writeEvents(events); writeErr != nil {
			if committed() {
				return errDriveCommittedWrite
			}
			return writeErr
		}
		return nil
	})
	if err == nil {
		terminalSuccess = last.Kind == aipapi.EventResponseFinished
		if writeErr := sse.WriteDONE(); writeErr == nil {
			flush()
		}
		return
	}
	if errors.Is(err, errDriveCommittedWrite) {
		return
	}
	if errors.Is(err, io.EOF) {
		if !committed() {
			failUncommitted()
		} else {
			_ = emitStreamFailure(sse, sm, flush)
		}
		return
	}
	if ctx.Err() != nil {
		if committed() && owner != nil {
			finalizeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			finalizeErr := safeFinalizeIncomplete(finalizeCtx, owner)
			cancel()
			if finalizeErr == nil {
				terminalSuccess = true
			} else {
				if !safeCleanupConsumed(owner) {
					safeReleaseContinuationReservation(owner)
				}
				cleanupConsumed = true
			}
		}
		return
	}
	if committed() {
		_ = emitStreamFailure(sse, sm, flush)
		return
	}
	if errors.Is(err, errDriveInvalidEnvelope) || errors.Is(err, errDriveTooManyEvents) {
		failUncommitted()
		return
	}
	status, typ, code, message := classifyExecutionError(err)
	if status == http.StatusBadGateway && typ == "server_error" {
		if isReserved && store != nil {
			cleanupContinuationReservation(store, scope, responseID)
			isReserved = false
		}
		writeWireError(w, status, typ, code, message)
		return
	}
	if isReserved && store != nil {
		cleanupContinuationReservation(store, scope, responseID)
		isReserved = false
	}
	writeWireError(w, status, typ, code, message)
}

func classifyCanonicalEventError(ev aipapi.Event) (int, string, string, string) {
	code := ev.ErrorCode
	if code == "" {
		code = "backend_error"
	}
	message := safeCanonicalErrorMessage(ev.ErrorMessage)
	switch code {
	case "invalid_request", "invalid_request_error", "context_length_exceeded":
		return http.StatusBadRequest, "invalid_request_error", code, message
	case "rate_limit_exceeded", "rate_limit_error":
		return http.StatusTooManyRequests, "rate_limit_error", code, message
	case "authentication_error":
		return http.StatusUnauthorized, "authentication_error", code, message
	case "permission_error":
		return http.StatusForbidden, "permission_error", code, message
	default:
		return http.StatusBadGateway, "server_error", code, message
	}
}

func emitStreamFailure(sse *proto.SSEWriter, sm *proto.StateMachine, flush func()) (err error) {
	doneAttempted := false
	defer func() {
		if doneAttempted {
			return
		}
		doneAttempted = true
		doneErr := sse.WriteDONE()
		if err == nil {
			err = doneErr
		}
		if doneErr == nil {
			flush()
		}
	}()

	if sm.State() == proto.StateTerminal {
		doneAttempted = true
		err = sse.WriteDONE()
		if err == nil {
			flush()
		}
		return err
	}
	events, err := sm.ProcessCanonicalEvent(aipapi.Event{
		Kind:         aipapi.EventError,
		ErrorCode:    "backend_error",
		ErrorMessage: "Backend execution failed",
	})
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := sse.WriteEvent(event); err != nil {
			return err
		}
		flush()
	}
	return nil
}
