package pluginreg

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain guards this package. The OpenCensus ignore handles a transitive global worker.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(
		m,
		// OpenCensus registers a global stats worker via init; not owned by this package.
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
		// Backend-factory tests spin up httptest.Server instances and exercise outbound
		// *http.Client transports whose idle persistConn read/write loops linger
		// asynchronously after the server closes (default 90s IdleConnTimeout). The
		// goroutines' top frame is internal/poll.runtime_pollWait, so match anywhere in
		// the stack with IgnoreAnyFunction. Canonical goleak treatment for net/http
		// transport idle connections; deterministic on Windows where CI is Linux-only.
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
	)
}
