package geoip

import (
	"net/http"
	"net/netip"
	"strconv"
	"testing"

	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
)

type benchmarkLookup struct{}

func (benchmarkLookup) LookupCountry(netip.Addr) (string, bool, error) { return "US", true, nil }

func BenchmarkResolveClientIPDisabledEquivalent(b *testing.B) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	req, _ := http.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "198.51.100.10:443"
	h := Middleware(Input{}, next)
	w := discardResponseWriter{}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(w, req)
	}
}

func BenchmarkPolicyCIDROnly(b *testing.B) {
	policy, _ := coregeoip.Compile(coregeoip.CompileInput{
		Order: coregeoip.OrderDenyAllow,
		Allow: coregeoip.RuleConfig{CIDRs: []string{"198.51.100.0/24"}},
	})
	addr := netip.MustParseAddr("198.51.100.10")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = policy.Evaluate(addr, nil)
	}
}

func BenchmarkPolicyCountryLookup(b *testing.B) {
	policy, _ := coregeoip.Compile(coregeoip.CompileInput{
		Order: coregeoip.OrderDenyAllow,
		Deny:  coregeoip.RuleConfig{Countries: []string{"RU"}},
	})
	addr := netip.MustParseAddr("198.51.100.10")
	lookup := benchmarkLookup{}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = policy.Evaluate(addr, lookup)
	}
}

func BenchmarkTrustedForwardedResolution(b *testing.B) {
	req, _ := http.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "192.0.2.2:443"
	req.Header.Set("X-Forwarded-For", "198.51.100.10, 192.0.2.1")
	cfg := ResolverConfig{Source: SourceXForwardedFor, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = ResolveClientIP(req, cfg)
	}
}

func BenchmarkPolicyPrefixScaling(b *testing.B) {
	for _, count := range []int{1, 16, 64, 256} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			cidrs := make([]string, count)
			for i := range cidrs {
				cidrs[i] = "10." + strconv.Itoa(i%256) + ".0.0/16"
			}
			policy, _ := coregeoip.Compile(coregeoip.CompileInput{Order: coregeoip.OrderDenyAllow, Deny: coregeoip.RuleConfig{CIDRs: cidrs}})
			addr := netip.MustParseAddr("192.0.2.1")
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = policy.Evaluate(addr, nil)
			}
		})
	}
}

type discardResponseWriter struct{}

func (discardResponseWriter) Header() http.Header       { return make(http.Header) }
func (discardResponseWriter) Write([]byte) (int, error) { return 0, nil }
func (discardResponseWriter) WriteHeader(int)           {}
