package conformance

// ACP matrix cells execute against the relocated connector architecture.
//
// origin/main relocated the ACP HTTP prompt-turn adapter out of
// internal/plugins/backends/acp into the shared connector-support/acp module
// plus the executable connector module connectors/acp. The root module must not
// require connector modules (TestRootGoMod_NoConnectorModules) and production
// core must not import connector modules (hybrid-backend rules), so the
// conformance harness cannot link the ACP protocol adapter in-process. Instead
// it builds the actual aip-backend-acp executable (once per test binary) and
// drives each ACP backend through the backendplugin host adapter APIs
// (adapter.DialConfiguredSession + adapter.Build): the connector process is the
// real relocated connector, configured with the cell's observing origin as
// base_url, and the host adapter builds the execbackend.Backend exactly like the
// production composition. No ACP protocol code is duplicated in the harness.
//
// The process build/launch/configure machinery is shared with the OpenRouter and
// NVIDIA connector columns through the generalized connector-host harness
// (connector_host.go); this file keeps the ACP entrypoint the base harness
// selector consumes.
//
// Each ACP backend owns a dedicated connector process. The process is shut down
// via tb.Cleanup, so parallel conformance cells stay isolated and no connector
// process outlives its test.

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
)

// acpConnectorBackend launches a dedicated aip-backend-acp connector process
// configured against originURL and returns the host-built execbackend.Backend.
func acpConnectorBackend(tb testing.TB, originURL string) execbackend.Backend {
	tb.Helper()
	return connectorHostBackend(tb, BackendACP, originURL)
}
