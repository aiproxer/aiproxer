package selfdefense

import (
	"net/http"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	geoipingress "github.com/aiproxer/aiproxer/internal/stdhttp/geoip"
)

// discardWriter reuses one header map across requests, the way a real
// http.response reuses its handler header, so the reported allocations belong to
// the gate rather than to the benchmark stub.
type discardWriter struct{ header http.Header }

func newDiscardWriter() *discardWriter {
	return &discardWriter{header: make(http.Header)}
}

func (w *discardWriter) Header() http.Header     { return w.header }
func (*discardWriter) Write([]byte) (int, error) { return 0, nil }
func (*discardWriter) WriteHeader(int)           {}

func benchmarkGate() Input {
	return Input{
		Policy: &ingressdefense.Policy{
			Enabled:           true,
			AuthFailures:      5,
			FailureWindow:     time.Minute,
			InitialQuarantine: time.Minute,
			MaxQuarantine:     time.Hour,
		},
		Resolver:        geoipingress.ResolverConfig{Source: geoipingress.SourceDirect},
		ImpossiblePaths: true,
		Now:             func() time.Time { return time.Unix(0, 0).UTC() },
	}
}

// BenchmarkMiddlewareDirectPeer measures the common direct-IP/no-state path the
// design requires to stay cheap: one literal address parse, one fixed matcher
// pass, no quarantine lookup and no adaptive write, followed by a delegation.
func BenchmarkMiddlewareDirectPeer(b *testing.B) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	req, _ := http.NewRequest("GET", "http://example.test/v1/chat/completions", nil)
	req.RemoteAddr = "198.51.100.10:443"
	h := Middleware(benchmarkGate(), next)
	w := newDiscardWriter()
	b.ReportAllocs()
	for range b.N {
		h.ServeHTTP(w, req)
	}
}

// BenchmarkMiddlewareDirectPeerImpossiblePath measures the same path with a
// matched impossible-path target, so the refusal branch cost is visible beside the
// delegation branch.
func BenchmarkMiddlewareDirectPeerImpossiblePath(b *testing.B) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	req, _ := http.NewRequest("GET", "http://example.test/.git/config", nil)
	req.RemoteAddr = "198.51.100.10:443"
	// State is absent so the benchmark measures the deterministic refusal and its
	// matcher pass without adaptive-state contention.
	h := Middleware(benchmarkGate(), next)
	w := newDiscardWriter()
	b.ReportAllocs()
	for range b.N {
		h.ServeHTTP(w, req)
	}
}
