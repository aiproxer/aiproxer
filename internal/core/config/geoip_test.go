package config

import (
	"testing"
	"time"

	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
)

func TestCompileGeoIPConfigDisabledIsZeroRequestPolicy(t *testing.T) {
	t.Parallel()

	compiled, err := CompileGeoIP(GeoIPConfig{})
	if err != nil {
		t.Fatalf("CompileGeoIP: %v", err)
	}
	if compiled.Enabled() {
		t.Fatal("disabled GeoIP config compiled as enabled")
	}
	if compiled.Policy() != nil {
		t.Fatal("disabled GeoIP config published a policy")
	}
}

func TestCompileGeoIPConfigValidatesPolicyAndTrustedProxySource(t *testing.T) {
	t.Parallel()

	compiled, err := CompileGeoIP(GeoIPConfig{
		Enabled: true,
		Order:   string(coregeoip.OrderDenyAllow),
		Deny:    GeoIPRuleConfig{Countries: []string{"ru"}},
		Allow:   GeoIPRuleConfig{CIDRs: []string{"203.0.113.10"}},
		ClientIP: GeoIPClientConfig{
			Source:         ClientIPSourceXForwardedFor,
			TrustedProxies: []string{"192.0.2.0/24"},
		},
	})
	if err != nil {
		t.Fatalf("CompileGeoIP: %v", err)
	}
	if !compiled.Enabled() || compiled.Policy() == nil {
		t.Fatal("expected enabled compiled policy")
	}
	if got := len(compiled.TrustedProxies()); got != 1 {
		t.Fatalf("trusted proxy count = %d, want 1", got)
	}
	if got := compiled.ClientIPSource(); got != ClientIPSourceXForwardedFor {
		t.Fatalf("client source = %q, want %q", got, ClientIPSourceXForwardedFor)
	}
}

func TestCompileGeoIPConfigRejectsInvalidStaticValues(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  GeoIPConfig
	}{
		{name: "order", cfg: GeoIPConfig{Enabled: true, Order: "first_match"}},
		{name: "forwarded without trust", cfg: GeoIPConfig{Enabled: true, Order: "deny_allow", ClientIP: GeoIPClientConfig{Source: "forwarded"}}},
		{name: "bad trusted proxy", cfg: GeoIPConfig{Enabled: true, Order: "deny_allow", ClientIP: GeoIPClientConfig{Source: "forwarded", TrustedProxies: []string{"proxy.example"}}}},
		{name: "local managed update", cfg: GeoIPConfig{Database: GeoIPDBConfig{Source: GeoIPDatabaseSourceLocal, LocalPath: "country.mmdb", Update: GeoIPUpdateConfig{Enabled: true}}}},
		{name: "managed local path", cfg: GeoIPConfig{Database: GeoIPDBConfig{Source: GeoIPDatabaseSourceManaged, LocalPath: "country.mmdb"}}},
		{name: "short update interval", cfg: GeoIPConfig{Database: GeoIPDBConfig{Source: GeoIPDatabaseSourceManaged, Update: GeoIPUpdateConfig{Interval: "5h"}}}},
		{name: "bad update interval", cfg: GeoIPConfig{Database: GeoIPDBConfig{Source: GeoIPDatabaseSourceManaged, Update: GeoIPUpdateConfig{Interval: "not-a-duration"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CompileGeoIP(tc.cfg); err == nil {
				t.Fatal("CompileGeoIP succeeded, want validation error")
			}
		})
	}
}

func TestCompileGeoIPConfigDefaultsAndLimits(t *testing.T) {
	t.Parallel()

	compiled, err := CompileGeoIP(GeoIPConfig{
		Database: GeoIPDBConfig{Source: GeoIPDatabaseSourceManaged},
	})
	if err != nil {
		t.Fatalf("CompileGeoIP: %v", err)
	}
	if got := compiled.UpdateInterval(); got != 24*time.Hour {
		t.Fatalf("default update interval = %s, want 24h", got)
	}
	if coregeoip.MaxForwardedHeaderBytes != 16<<10 || coregeoip.MaxForwardedHops != 32 ||
		coregeoip.MaxDatabaseDownloadBytes != 128<<20 || coregeoip.DatabaseOperationTimeout != 2*time.Minute {
		t.Fatal("GeoIP safety limits changed from normative v1 values")
	}
}
