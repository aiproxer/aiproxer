package geoip

import (
	"net/http"
	"net/netip"
	"testing"
)

func FuzzForwardedHeaderResolution(f *testing.F) {
	f.Add("for=198.51.100.10,for=192.0.2.1")
	f.Add(`for="[2001:db8::10]:443";proto=https`)
	f.Fuzz(func(t *testing.T, header string) {
		req, _ := http.NewRequest("GET", "http://example.test", nil)
		req.RemoteAddr = "192.0.2.2:443"
		req.Header.Set("Forwarded", header)
		_, _ = ResolveClientIP(req, ResolverConfig{
			Source:         SourceForwarded,
			TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
		})
	})
}
