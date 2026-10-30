package stdhttp

import (
	"os"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// Env-only static-credential contract (requirements 2.1, 5.4): the e2e
	// harnesses in this package boot full hosts in-process, so their provider
	// credentials come from the host environment, never from config files.
	// Fixed fake keys shared by every stack (including parallel matrix
	// subtests, which cannot use per-test Setenv); no test in this package
	// asserts their absence.
	_ = os.Setenv("OPENAI_API_KEY", "sk-test-reasoning-e2e")
	_ = os.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-reasoning-e2e")
	_ = os.Setenv("GEMINI_API_KEY", "gemini-test-reasoning-e2e")
	goleak.VerifyTestMain(
		m,
		// Transitive dependency (e.g. via gRPC/OpenTelemetry exporters) starts this worker at init.
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
		// gRPC background transports and callback serializers from test stub connections.
		goleak.IgnoreAnyFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),
		goleak.IgnoreAnyFunction("google.golang.org/grpc.(*addrConn).resetTransportAndUnlock"),
		goleak.IgnoreAnyFunction("google.golang.org/grpc.(*addrConn).connect"),
	)
}
