package geoip

import (
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestResolveClientIPDirectIgnoresForwardingHeaders(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "192.0.2.10:443"
	req.Header.Set("X-Forwarded-For", "198.51.100.10")
	req.Header.Set("Forwarded", "for=198.51.100.10")

	got, err := ResolveClientIP(req, ResolverConfig{Source: SourceDirect})
	if err != nil {
		t.Fatalf("ResolveClientIP: %v", err)
	}
	if got != netip.MustParseAddr("192.0.2.10") {
		t.Fatalf("address = %s, want direct peer", got)
	}
}

func TestResolveClientIPDirectSupportsHostOnlyAndMappedIPv4(t *testing.T) {
	t.Parallel()

	for _, remote := range []string{"::ffff:192.0.2.10", "[2001:db8::10]:443", "2001:db8::10"} {
		req, _ := http.NewRequest("GET", "http://example.test", nil)
		req.RemoteAddr = remote
		got, err := ResolveClientIP(req, ResolverConfig{Source: SourceDirect})
		if err != nil {
			t.Errorf("remote %q: ResolveClientIP: %v", remote, err)
			continue
		}
		if strings.HasPrefix(remote, "::ffff") && got != netip.MustParseAddr("192.0.2.10") {
			t.Errorf("remote %q: address = %s, want unmapped IPv4", remote, got)
		}
	}
}

func TestResolveClientIPRejectsMalformedDirectPeer(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "proxy.example:443"
	if _, err := ResolveClientIP(req, ResolverConfig{Source: SourceDirect}); err == nil {
		t.Fatal("ResolveClientIP succeeded for hostname, want error")
	}
}

func TestResolveClientIPTrustedXFFWalksRightToLeft(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "192.0.2.2:443"
	req.Header.Add("X-Forwarded-For", "198.51.100.10, 192.0.2.1")
	got, err := ResolveClientIP(req, ResolverConfig{
		Source:         SourceXForwardedFor,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
	})
	if err != nil {
		t.Fatalf("ResolveClientIP: %v", err)
	}
	if got != netip.MustParseAddr("198.51.100.10") {
		t.Fatalf("address = %s, want client", got)
	}
}

func TestResolveClientIPUntrustedPeerIgnoresSpoofedXFF(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "203.0.113.10:443"
	req.Header.Set("X-Forwarded-For", "198.51.100.10, 192.0.2.1")
	got, err := ResolveClientIP(req, ResolverConfig{
		Source:         SourceXForwardedFor,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
	})
	if err != nil {
		t.Fatalf("ResolveClientIP: %v", err)
	}
	if got != netip.MustParseAddr("203.0.113.10") {
		t.Fatalf("address = %s, want direct peer", got)
	}
}

func TestResolveClientIPRepeatedXFFFieldsShareBounds(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "192.0.2.2:443"
	req.Header.Add("X-Forwarded-For", "198.51.100.10")
	req.Header.Add("X-Forwarded-For", "192.0.2.1")
	got, err := ResolveClientIP(req, ResolverConfig{
		Source:         SourceXForwardedFor,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
	})
	if err != nil || got != netip.MustParseAddr("198.51.100.10") {
		t.Fatalf("repeated XFF result = %s, err=%v", got, err)
	}

	req.Header = make(http.Header)
	req.RemoteAddr = "192.0.2.2:443"
	for range MaxForwardedHops + 1 {
		req.Header.Add("X-Forwarded-For", "198.51.100.10")
	}
	if _, err := ResolveClientIP(req, ResolverConfig{
		Source:         SourceXForwardedFor,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
	}); err == nil {
		t.Fatal("oversized hop chain succeeded, want error")
	}
}

func TestResolveClientIPForwardedQuotedBracketedIPv6(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "192.0.2.2:443"
	req.Header.Add("Forwarded", `for="[2001:db8::10]:1234";proto=https`)
	got, err := ResolveClientIP(req, ResolverConfig{
		Source:         SourceForwarded,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
	})
	if err != nil {
		t.Fatalf("ResolveClientIP: %v", err)
	}
	if got != netip.MustParseAddr("2001:db8::10") {
		t.Fatalf("address = %s, want IPv6 client", got)
	}
}

func TestResolveClientIPMalformedAuthoritativeHeaderFailsClosed(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest("GET", "http://example.test", nil)
	req.RemoteAddr = "192.0.2.2:443"
	req.Header.Set("Forwarded", "for=unknown")
	if _, err := ResolveClientIP(req, ResolverConfig{
		Source:         SourceForwarded,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
	}); err == nil {
		t.Fatal("ResolveClientIP succeeded for unknown Forwarded node")
	}
}
