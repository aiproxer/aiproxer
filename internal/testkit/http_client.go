package testkit

import (
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
)

const localTestServerHTTPTimeout = 30 * time.Second

// LocalTestServerHTTPClient returns a short, time-bounded [http.Client] for tests that call
// [httptest.Server] or loopback URLs. Prefer this over the [http] package default client, which
// has no timeout. For outbound calls that should match production, use [IntegrationHTTPClient]
// (or pass an explicit [http.Client] from the test harness).
func LocalTestServerHTTPClient() *http.Client {
	return &http.Client{Timeout: localTestServerHTTPTimeout}
}

// IntegrationHTTPClient returns c when non-nil; otherwise it returns [httpclient.Standard]
// so integration tests match production outbound pooling and timeouts (not [http.DefaultClient]).
func IntegrationHTTPClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return httpclient.Standard()
}
