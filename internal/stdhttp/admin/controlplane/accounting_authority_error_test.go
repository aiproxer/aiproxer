package controlplane

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
)

// The authority query error vocabulary lives in the carved open-source port;
// these sentinels are the identical values the usage-authority service wraps,
// so the wire classification is unchanged.
func TestWriteAuthorityQueryError_joinedTimeoutPrecedence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
	}{
		{
			name:       "timeout_plus_degraded_is_degraded",
			err:        errors.Join(usagelimit.ErrEvaluationTimeout, usagelimit.ErrDegraded),
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "degraded",
		},
		{
			name:       "timeout_plus_invalid_query_is_invalid_query",
			err:        errors.Join(usagelimit.ErrEvaluationTimeout, usagelimit.ErrInvalidQuery),
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_query",
		},
		{
			name:       "timeout_plus_unsupported_filter_is_unsupported_filter",
			err:        errors.Join(usagelimit.ErrEvaluationTimeout, usagelimit.ErrUnsupportedFilter),
			wantStatus: http.StatusBadRequest,
			wantError:  "unsupported_filter",
		},
		{
			name:       "timeout_alone_is_unavailable",
			err:        usagelimit.ErrEvaluationTimeout,
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "unavailable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rr := httptest.NewRecorder()
			writeAuthorityQueryError(rr, tc.err)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status %d want %d body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body["error"] != tc.wantError {
				t.Fatalf("error %q want %q", body["error"], tc.wantError)
			}
		})
	}
}
