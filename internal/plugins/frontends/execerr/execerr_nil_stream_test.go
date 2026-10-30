package execerr_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// nilStreamOpenError builds the terminal error for a backend Open that
// returned no stream: a pre-output upstream failure naming the backend that
// opened nothing. It mirrors the runtime's failed-open shape without importing
// its unexported constructor.
func nilStreamOpenError() error {
	return &aipapi.UpstreamFailureError{
		Phase:        aipapi.PhasePreOutput,
		Recoverable:  true,
		Reason:       "backend open returned no stream",
		CandidateKey: "nilstream:m",
	}
}

// TestClassifyExecute_nilStreamOpenMapsToBackendUnavailable pins the wire
// mapping for a nil-stream backend open: no candidate could serve the request,
// so it maps to 502 backend unavailable like a hard capability rejection,
// with a fixed message that carries no upstream detail.
func TestClassifyExecute_nilStreamOpenMapsToBackendUnavailable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "nil_stream_open",
			err:  nilStreamOpenError(),
		},
		{
			name: "nil_stream_open_wrapped_with_upstream_detail",
			err:  fmt.Errorf("executor submit upstream=http://internal-host:9090/v1 token=sekret: %w", nilStreamOpenError()),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !runtime.IsNilStreamOpenError(tc.err) {
				t.Fatalf("predicate must recognize the nil-stream open failure: %v", tc.err)
			}
			out := execerr.ClassifyExecute(tc.err)
			if out.Kind != execerr.KindBackendUnavailable {
				t.Fatalf("kind: got %v want %v", out.Kind, execerr.KindBackendUnavailable)
			}
			if out.Status != http.StatusBadGateway {
				t.Fatalf("status: got %d want %d", out.Status, http.StatusBadGateway)
			}
			if out.Message != execerr.BackendUnavailableWireMessage {
				t.Fatalf("message: got %q want %q", out.Message, execerr.BackendUnavailableWireMessage)
			}
			if out.Err == nil || !errors.Is(out.Err, tc.err) {
				t.Fatalf("Err must preserve original error, got %v", out.Err)
			}
			if strings.Contains(out.Message, "internal-host") || strings.Contains(out.Message, "sekret") ||
				strings.Contains(out.Message, "nilstream") {
				t.Fatalf("message leaked wrapped detail: %q", out.Message)
			}
		})
	}
}

// TestClassifyExecute_nilStreamOpenDoesNotWiden pins that the new branch is
// narrow: cancellations, deadlines, lookalike upstream failures, and unrelated
// failures keep their existing mapping.
func TestClassifyExecute_nilStreamOpenDoesNotWiden(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
	}{
		{name: "nil", err: nil},
		{name: "unrelated", err: errors.New("some unrelated executor failure")},
		{name: "canceled", err: context.Canceled},
		{name: "deadline_exceeded", err: context.DeadlineExceeded},
		{
			name: "same_reason_wrong_phase",
			err: &aipapi.UpstreamFailureError{
				Phase:        aipapi.PhasePostOutput,
				Recoverable:  true,
				Reason:       "backend open returned no stream",
				CandidateKey: "nilstream:m",
			},
		},
		{
			name: "same_reason_not_recoverable",
			err: &aipapi.UpstreamFailureError{
				Phase:        aipapi.PhasePreOutput,
				Recoverable:  false,
				Reason:       "backend open returned no stream",
				CandidateKey: "nilstream:m",
			},
		},
		{
			name: "different_reason_same_shape",
			err: &aipapi.UpstreamFailureError{
				Phase:        aipapi.PhasePreOutput,
				Recoverable:  true,
				Reason:       "upstream",
				CandidateKey: "nilstream:m",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.err != nil && runtime.IsNilStreamOpenError(tc.err) {
				t.Fatalf("predicate must not match %s: %v", tc.name, tc.err)
			}
			out := execerr.ClassifyExecute(tc.err)
			if out.Kind == execerr.KindBackendUnavailable {
				t.Fatalf("%s: must not classify as backend unavailable: %+v", tc.name, out)
			}
		})
	}

	if runtime.IsNilStreamOpenError(nil) {
		t.Fatal("a nil error must not classify as a nil-stream open failure")
	}
	unrelated := errors.New("some unrelated executor failure")
	if out := execerr.ClassifyExecute(unrelated); out.Kind != execerr.KindInternalError ||
		out.Message != execerr.InternalWireMessage {
		t.Fatalf("unrelated error changed classification: kind=%v message=%q", out.Kind, out.Message)
	}
}

// TestShouldLogExecuteError pins the frontend logging policy: internal errors
// always log, nil-stream backend faults log so the backend fault keeps an
// error-level diagnostic, and ordinary capability rejections stay quiet.
func TestShouldLogExecuteError(t *testing.T) {
	t.Parallel()
	nilStreamOut := execerr.ClassifyExecute(nilStreamOpenError())
	if !execerr.ShouldLogExecuteError(nilStreamOut) {
		t.Fatal("nil-stream backend fault must keep its frontend error log")
	}
	if !execerr.ShouldLogExecuteError(execerr.ClassifyExecute(errors.New("boom"))) {
		t.Fatal("internal errors must log")
	}
	capabilityOut := execerr.ClassifyExecute(fmt.Errorf("route plan: %w", aipapi.ErrCapabilityReject))
	if capabilityOut.Kind != execerr.KindBackendUnavailable {
		t.Fatalf("capability reject must stay backend unavailable, got %v", capabilityOut.Kind)
	}
	if execerr.ShouldLogExecuteError(capabilityOut) {
		t.Fatal("ordinary capability rejections must not become error-level events")
	}
	if execerr.ShouldLogExecuteError(execerr.Outcome{}) {
		t.Fatal("an outcome without an error must not log")
	}
}
