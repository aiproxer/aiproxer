package acp

import (
	"errors"
	"io"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestClassifyPreOutputError_RecoverableHTTP proves transport/HTTP 5xx and rate
// limit failures before canonical output are wrapped as recoverable pre-output
// so the core may fail over to another candidate (Task 8.5 / FeatureFailover).
func TestClassifyPreOutputError_RecoverableHTTP(t *testing.T) {
	t.Parallel()
	for _, status := range []int{500, 502, 503, 504, 429} {
		err := classifyPreOutputError(&httpStatusError{Op: "session/prompt", Status: status, Detail: "boom"})
		if !aipapi.IsRecoverablePreOutput(err) {
			t.Fatalf("HTTP %d classify = terminal, want recoverable pre-output (%v)", status, err)
		}
	}
}

// TestClassifyPreOutputError_TerminalAuthValidation proves terminal auth and
// validation failures (HTTP 4xx, JSON-RPC rejections) stay terminal so an
// invalid request never silently retries against another candidate.
func TestClassifyPreOutputError_TerminalAuthValidation(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 401, 403, 404, 422} {
		err := classifyPreOutputError(&httpStatusError{Op: "session/prompt", Status: status, Detail: "nope"})
		if aipapi.IsRecoverablePreOutput(err) {
			t.Fatalf("HTTP %d classify = recoverable, want terminal", status)
		}
	}
	if err := classifyPreOutputError(&RPCError{Method: "session/prompt", Code: -32602, Message: "invalid params"}); aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("JSON-RPC rejection classify = recoverable, want terminal (%v)", err)
	}
}

// TestClassifyPreOutputError_TransportFailureRecoverable proves network/transport
// and protocol failures before canonical output are recoverable failover
// candidates.
func TestClassifyPreOutputError_TransportFailureRecoverable(t *testing.T) {
	t.Parallel()
	cases := []error{
		errors.New("dial tcp: connection refused"),
		io.ErrUnexpectedEOF,
		errors.New("read: connection reset by peer"),
	}
	for _, cause := range cases {
		err := classifyPreOutputError(cause)
		if !aipapi.IsRecoverablePreOutput(err) {
			t.Fatalf("transport failure %v classify = terminal, want recoverable pre-output", cause)
		}
	}
}

func TestClassifyPreOutputError_NilAndCausePreserved(t *testing.T) {
	t.Parallel()
	if err := classifyPreOutputError(nil); err != nil {
		t.Fatalf("classifyPreOutputError(nil) = %v, want nil", err)
	}
	cause := errors.New("dial timeout")
	err := classifyPreOutputError(cause)
	if !errors.Is(err, aipapi.ErrRecoverablePreOutput) {
		t.Fatalf("wrapped error does not unwrap to ErrRecoverablePreOutput: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("wrapped error lost its cause chain: %v", err)
	}
	// HTTP 4xx keeps its status identity for diagnostics.
	hse := classifyPreOutputError(&httpStatusError{Op: "session/prompt", Status: 401, Detail: "invalid key"})
	if _, ok := errors.AsType[*httpStatusError](hse); !ok {
		t.Fatalf("terminal HTTP error lost its typed status: %v", hse)
	}
}
