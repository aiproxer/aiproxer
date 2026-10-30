package frontendpipe_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type readTrackingReader struct {
	r         io.Reader
	readCalls atomic.Int64
	bytesRead atomic.Int64
}

func (tr *readTrackingReader) Read(p []byte) (int, error) {
	tr.readCalls.Add(1)
	n, err := tr.r.Read(p)
	tr.bytesRead.Add(int64(n))
	return n, err
}

// TestTask12_6_LegacyResolver_NoBodyDoubleMaterialization verifies Requirement 22.4:
// When a custom route hook is configured, frontend candidate lane MUST decline
// at PreCapture Gate 5 before any capture/spool allocation, ensuring the body
// is never double-materialized.
func TestTask12_6_LegacyResolver_NoBodyDoubleMaterialization(t *testing.T) {
	t.Parallel()
	exec := &candidateGatesExec{}
	prof := &trackingCandidateProfile{}
	var records []frontendpipe.PreCaptureResult
	spec := newCandidateGatesSpec(exec, prof, &records)

	var resolverCalled bool
	spec.ResolveBodyRoute = func(_ []byte, _ frontendpipe.PathMatch) routing.ResolvedRoute {
		resolverCalled = true
		return routing.ExplicitRoute("legacy-route")
	}

	payload := `{"model":"test-model","prompt":"` + strings.Repeat("x", 2<<20) + `"}`
	tracker := &readTrackingReader{r: strings.NewReader(payload)}

	req := httptest.NewRequest(http.MethodPost, "/v1/create", tracker)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, resolverCalled, "custom body route hook should be invoked on canonical fallback path")

	// PreCapture must record exactly one Gate 5 decline
	require.Len(t, records, 1)
	assert.False(t, records[0].Candidate, "candidate must be false when custom route hook is configured")
	assert.Equal(t, frontendpipe.PreCaptureGateLegacyRouteResolver, records[0].Gate)
	assert.Equal(t, largebody.StaticWireReasonLegacyResolverConfigured, records[0].Reason)

	// Verify the body was consumed only once: tracker bytes read should not exceed payload length
	assert.Equal(t, int64(len(payload)), tracker.bytesRead.Load(),
		"body must be read exactly once by canonical path; candidate lane must not materialize a second body")
}

// TestTask12_6_LegacyResolver_NilResolver_CandidatePermitted verifies that
// when no custom route hook is configured, PreCapture does not decline on Gate 5.
func TestTask12_6_LegacyResolver_NilResolver_CandidatePermitted(t *testing.T) {
	t.Parallel()
	exec := &candidateGatesExec{}
	prof := &trackingCandidateProfile{}
	var records []frontendpipe.PreCaptureResult
	spec := newCandidateGatesSpec(exec, prof, &records)
	spec.ResolvePreAdmissionRoute = nil
	spec.ResolveBodyRoute = nil // No custom route hook

	payload := `{"model":"test-model","prompt":"` + strings.Repeat("x", 2<<20) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, records, 1)
	assert.True(t, records[0].Candidate, "candidate should be permitted when no custom route hook is configured")
	assert.Equal(t, frontendpipe.PreCaptureGateNone, records[0].Gate)
}
