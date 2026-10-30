package execerr_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// capabilityMultiRootError mimics a backend refusal that carries two roots
// (capability reject for the wire mapping, recoverable pre-output for
// failover), like the strict-adjacency refusal in openaicompat. It is not a
// *aipapi.RejectError, so aipapi.IsReject does not recognize it.
type capabilityMultiRootError struct{ cause error }

func (e *capabilityMultiRootError) Error() string {
	if e == nil || e.cause == nil {
		return "request cannot be served without reordering"
	}
	return e.cause.Error()
}

func (e *capabilityMultiRootError) Unwrap() []error {
	if e == nil || e.cause == nil {
		return []error{aipapi.ErrCapabilityReject}
	}
	return []error{aipapi.ErrCapabilityReject, e.cause}
}

// TestClassifyExecute_capabilityRejectNotARejectError pins the wire mapping
// for errors matching aipapi.ErrCapabilityReject that are NOT *RejectError
// (exact requirement mismatches, multi-root backend refusals, raw sentinel
// wraps). The executor surfaces these when no candidate can serve a request.
func TestClassifyExecute_capabilityRejectNotARejectError(t *testing.T) {
	t.Parallel()
	reqReject := &aipapi.RequirementsRejectError{
		MissingCaps: []aipapi.Capability{aipapi.CapabilityVision},
	}
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "requirements_reject",
			err:  reqReject,
		},
		{
			name: "requirements_reject_wrapped_with_upstream_detail",
			err:  fmt.Errorf("executor submit upstream=http://internal-host:9090/v1 token=sekret: %w", reqReject),
		},
		{
			name: "raw_capability_reject_sentinel_wrap",
			err:  fmt.Errorf("route plan: %w", aipapi.ErrCapabilityReject),
		},
		{
			name: "multi_root_backend_refusal",
			err:  &capabilityMultiRootError{cause: errors.New("refusing detail: item 3 interleaved")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
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
			if strings.Contains(out.Message, "internal-host") || strings.Contains(out.Message, "sekret") {
				t.Fatalf("message leaked wrapped detail: %q", out.Message)
			}
		})
	}
}
