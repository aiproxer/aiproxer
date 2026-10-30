package configreload_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	mgmtreload "github.com/aiproxer/aiproxer/internal/stdhttp/admin/configreload"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func doBearerStatus(t *testing.T, h *mgmtreload.Handler, authValue string, _ int) int {
	t.Helper()
	srv := httptest.NewServer(h.Mux())
	t.Cleanup(srv.Close)
	req, err := http.NewRequest(http.MethodGet, srv.URL+mgmtreload.StatusPath, nil)
	require.NoError(t, err)
	if authValue != "" {
		req.Header.Set("Authorization", authValue)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { assert.NoError(t, res.Body.Close()) }()
	return res.StatusCode
}

func TestManagement_BearerAuthSemantics(t *testing.T) {
	t.Parallel()

	const secret = "test-management-secret-0123456789"

	tests := []struct {
		name      string
		authValue string
		want      int
	}{
		{name: "correct token", authValue: "Bearer " + secret, want: http.StatusOK},
		{name: "correct token lowercase prefix", authValue: "bearer " + secret, want: http.StatusOK},
		{name: "wrong same length", authValue: "Bearer " + secret[:len(secret)-1] + "X", want: http.StatusUnauthorized},
		{name: "wrong shorter", authValue: "Bearer short", want: http.StatusUnauthorized},
		{name: "wrong longer", authValue: "Bearer " + secret + "-extra-suffix", want: http.StatusUnauthorized},
		{name: "missing header", authValue: "", want: http.StatusUnauthorized},
		{name: "malformed scheme", authValue: "Token " + secret, want: http.StatusUnauthorized},
		{name: "empty bearer value", authValue: "Bearer ", want: http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			coord := newFakeCoordinator("/fixed/startup/config.yaml", func(context.Context, sdkreload.Trigger) sdkreload.Result {
				return sdkreload.Result{Category: sdkreload.ResultPublished}
			})
			h, err := mgmtreload.NewHandler(mgmtreload.Options{
				Address:     "127.0.0.1:0",
				AuthMode:    mgmtreload.AuthModeBearer,
				BearerToken: secret,
			}, coord)
			require.NoError(t, err)
			assert.Equal(t, tc.want, doBearerStatus(t, h, tc.authValue, tc.want))
		})
	}
}
