package stdhttp

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

type stackGeoIPLookup struct{}

func (stackGeoIPLookup) LookupCountry(netip.Addr) (string, bool, error) {
	return "RU", true, nil
}

func TestStackHTTPHandlerGeoIPDenialIsBeforeDownstreamAndKeepsSecurityHeaders(t *testing.T) {
	t.Parallel()

	policy, err := coregeoip.Compile(coregeoip.CompileInput{
		Order: coregeoip.OrderDenyAllow,
		Deny:  coregeoip.RuleConfig{Countries: []string{"RU"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	downstreamCalls := 0
	h := stackHTTPHandler(stackHTTPInput{
		Cfg:      &config.Config{},
		Log:      testkit.DiscardLogger(),
		TraceGen: diag.NewTraceIDGenerator(),
		Security: HTTPSecurityInput{GeoIP: GeoIPSecurityInput{
			Policy:   policy,
			Lookup:   stackGeoIPLookup{},
			Resolver: GeoIPResolverConfig{Source: "direct"},
		}},
		Inner: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { downstreamCalls++ }),
	})
	req := httptest.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "198.51.100.10:443"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || rec.Body.String() != "Forbidden\n" {
		t.Fatalf("response = %d %q, want generic 403", rec.Code, rec.Body.String())
	}
	if downstreamCalls != 0 {
		t.Fatalf("downstream calls = %d, want 0", downstreamCalls)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %v, want nosniff", rec.Header())
	}
}
