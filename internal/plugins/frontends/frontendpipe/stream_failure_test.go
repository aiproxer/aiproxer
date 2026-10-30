package frontendpipe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// First-event serialization failure must also take the explicit error path,
// without leaking the encoder's raw diagnostic. Arrival is not successful output.
func TestPumpSSE_FirstSerializationFailureIsReported(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	s := aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "first"}})
	err := frontendpipe.PumpSSEWithFailure(t.Context(), w, s, nil, func(aipapi.Event) (bool, error) {
		_, err := json.Marshal(math.Inf(1))
		return false, err
	}, func(err error) error {
		return json.NewEncoder(w).Encode(map[string]string{"message": execerr.ClassifyExecute(err).Message})
	})
	if err == nil || !strings.Contains(w.Body.String(), `"message":"internal error"`) {
		t.Fatalf("missing serialized error: err=%v body=%s", err, w.Body.String())
	}
}

func TestPumpSSE_CloseFailureAfterSuccessDoesNotEmitSecondTerminal(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("cleanup failed")
	s := &scriptedStream{evs: []aipapi.Event{{Kind: aipapi.EventResponseFinished}}, closeErr: closeErr}
	failed := false
	err := frontendpipe.PumpSSEWithFailure(t.Context(), httptest.NewRecorder(), s, nil,
		func(aipapi.Event) (bool, error) { return true, nil },
		func(error) error { failed = true; return nil })
	if !errors.Is(err, closeErr) || failed || s.closeCount() != 1 {
		t.Fatalf("close failure must be returned without a second terminal: err=%v reported=%v closes=%d", err, failed, s.closeCount())
	}
}

func TestWirePipeline_MetadataThenFailureHasTerminalError(t *testing.T) {
	t.Parallel()
	for _, responses := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat", true: "responses"}[responses], func(t *testing.T) {
			t.Parallel()
			executed := false
			spec := newKeepaliveTestSpec(keepaliveSpecOptions{
				streamMode: true,
				executeLargeFunc: func(context.Context, largebody.Assessment, largebody.Source) (largebody.ExecutionResult, error) {
					executed = true
					return largebody.ExecutionResult{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventError, ErrorCode: "provider_error", ErrorMessage: "secret-provider-diagnostic"},
					})}, nil
				},
				writeStreamFunc: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
					if responses {
						return openairesponses.WireWriteStreamSSE(ctx, w, rc, es, false)
					}
					return openailegacy.WireWriteStreamSSE(ctx, w, rc, es, false)
				},
			})
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(buildJSONPayload(1200*1024)))
			r.Header.Set("Content-Type", "application/json")
			frontendpipe.ServeHTTP(&spec, w, r)
			body := w.Body.String()
			if !executed || !strings.Contains(body, `"message":"internal error"`) || strings.Contains(body, "secret-provider-diagnostic") {
				t.Fatalf("wire execution=%v, missing or unsafe failure: %s", executed, body)
			}
			if responses && !strings.Contains(body, "event: response.failed\n") {
				t.Fatalf("missing response.failed: %s", body)
			}
			if !responses && !strings.HasSuffix(body, "data: [DONE]\n\n") {
				t.Fatalf("missing DONE: %s", body)
			}
		})
	}
}
