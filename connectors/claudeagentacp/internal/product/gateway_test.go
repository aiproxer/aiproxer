package product

import (
	"strings"
	"testing"
)

func TestGatewayConfig_AcceptsPolicyRows(t *testing.T) {
	for _, url := range []string{
		"https://claude-gw.example.com",
		"https://claude-gw.example.com/v1/messages",
		"http://127.0.0.1:18911",
		"http://localhost:18911",
		"http://[::1]:18911",
	} {
		if err := ValidateGatewayConfig(GatewayConfig{BaseURL: url}); err != nil {
			t.Fatalf("accepted endpoint %q: %v", url, err)
		}
	}
	if err := ValidateGatewayConfig(GatewayConfig{BaseURL: "https://gw.example.com", Headers: []GatewayHeader{{Name: "X-Route"}, {Name: "X-Bearer-Gateway"}}}); err != nil {
		t.Fatal(err)
	}
	// The runtime replaces a configured Authorization with its own bypass
	// token, so configuration must reject it with operator guidance.
	for _, name := range []string{"Authorization", "authorization", "AUTHORIZATION"} {
		if err := ValidateGatewayConfig(GatewayConfig{BaseURL: "https://gw.example.com", Headers: []GatewayHeader{{Name: name}}}); err == nil || !strings.Contains(err.Error(), "supported custom header name") {
			t.Fatalf("Authorization name %q must reject: %v", name, err)
		}
	}
	if err := ValidateGatewayConfig(GatewayConfig{BaseURL: "https://gw.example.com", Headers: []GatewayHeader{{Name: "X-A=C"}}}); err == nil {
		t.Fatal("accepted header with space")
	}
}

func TestGatewayConfig_RejectsPolicyViolations(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"empty", ""},
		{"plain-hostname", "gw.example.com"},
		{"plain-http", "http://gw.example.com"},
		{"userinfo-basic", "https://user:pass@gw.example.com"},
		{"userinfo-none", "https://user@gw.example.com"},
		{"fragment", "https://gw.example.com#token"},
		{"ftp", "ftp://gw.example.com"},
		{"non-loopback-http", "http://10.0.0.1:8080"},
		{"quoted-host", "http://bad host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateGatewayConfig(GatewayConfig{BaseURL: tc.url}); err == nil {
				t.Fatalf("accepted %q", tc.url)
			} else if !strings.HasPrefix(err.Error(), "claudeagentacp: gateway") {
				t.Fatalf("error text: %v", err)
			}
		})
	}
	for _, name := range []string{"", "with space", "with\ttab", "with\nnewline", strings.Repeat("a", 129)} {
		cfg := GatewayConfig{BaseURL: "https://gw.example.com", Headers: []GatewayHeader{{Name: name}}}
		if err := ValidateGatewayConfig(cfg); err == nil {
			t.Fatalf("accepted header name %q", name)
		}
	}
	// Single authoritative namespace: names the connector accepts must be
	// exactly the host secret-reference grammar
	// (internal/core/config backendBindingName ^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$);
	// RFC token names the host cannot provision reject here.
	for _, name := range []string{"X-Route", "X_Route.v1", "x-gateway-2", "A", "8-Start"} {
		if err := ValidateGatewayConfig(GatewayConfig{BaseURL: "https://gw.example.com", Headers: []GatewayHeader{{Name: name}}}); err != nil {
			t.Fatalf("host-provisionable name %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"X+Route", "X~Route", "X!Route", "_proto", "!bang"} {
		if err := ValidateGatewayConfig(GatewayConfig{BaseURL: "https://gw.example.com", Headers: []GatewayHeader{{Name: name}}}); err == nil {
			t.Fatalf("name %q is not host-provisionable and must reject", name)
		}
	}
	for _, tc := range []struct {
		name    string
		headers []GatewayHeader
	}{
		{"dupe", []GatewayHeader{{Name: "X-Route"}, {Name: "x-rOute"}}},
		{"empty-list", nil},
	} {
		cfg := GatewayConfig{BaseURL: "https://gw.example.com", Headers: tc.headers}
		if tc.name == "dupe" {
			if err := ValidateGatewayConfig(cfg); err == nil {
				t.Fatalf("accepted %s", tc.name)
			}
		}
	}
	overs := []GatewayHeader{}
	for i := range 17 {
		overs = append(overs, GatewayHeader{Name: nameFor(i)})
	}
	if err := ValidateGatewayConfig(GatewayConfig{BaseURL: "https://gw.example.com", Headers: overs}); err == nil {
		t.Fatal("accepted 17 headers")
	}
}

func nameFor(i int) string { return "H" + string(rune('A'+i%26)) }
