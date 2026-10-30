package config

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
)

const defaultGeoIPEdition = "dbip-country-lite"

// CompiledGeoIP is the immutable generation projection produced by pure config
// compilation. Database lifecycle is intentionally not represented here.
type CompiledGeoIP struct {
	enabled        bool
	policy         *coregeoip.Policy
	clientIPSource ClientIPSource
	trustedProxies []netip.Prefix
	databaseSource GeoIPDatabaseSource
	updateInterval time.Duration
}

// CompileGeoIP validates static GeoIP configuration without opening files,
// constructing services, or performing network I/O.
func CompileGeoIP(in GeoIPConfig) (*CompiledGeoIP, error) {
	clientSource := ClientIPSource(strings.ToLower(strings.TrimSpace(string(in.ClientIP.Source))))
	if clientSource == "" {
		clientSource = ClientIPSourceDirect
	}
	switch clientSource {
	case ClientIPSourceDirect, ClientIPSourceXForwardedFor, ClientIPSourceForwarded:
	default:
		return nil, fmt.Errorf("access.geoip.client_ip.source: invalid source %q", in.ClientIP.Source)
	}
	trusted, err := compilePrefixes("access.geoip.client_ip.trusted_proxies", in.ClientIP.TrustedProxies)
	if err != nil {
		return nil, err
	}
	if (clientSource == ClientIPSourceXForwardedFor || clientSource == ClientIPSourceForwarded) && len(trusted) == 0 {
		return nil, fmt.Errorf("access.geoip.client_ip.trusted_proxies: at least one CIDR is required for source %q", clientSource)
	}

	policy, err := compileGeoIPPolicy(in)
	if err != nil {
		return nil, err
	}
	databaseSource, updateInterval, err := validateGeoIPDatabase(in.Database)
	if err != nil {
		return nil, err
	}
	return &CompiledGeoIP{
		enabled:        in.Enabled,
		policy:         policy,
		clientIPSource: clientSource,
		trustedProxies: trusted,
		databaseSource: databaseSource,
		updateInterval: updateInterval,
	}, nil
}

func compileGeoIPPolicy(in GeoIPConfig) (*coregeoip.Policy, error) {
	if !in.Enabled {
		if strings.TrimSpace(in.Order) == "" && len(in.Allow.Countries) == 0 && len(in.Allow.CIDRs) == 0 &&
			len(in.Deny.Countries) == 0 && len(in.Deny.CIDRs) == 0 {
			return nil, nil
		}
	}
	order := strings.TrimSpace(strings.ToLower(in.Order))
	if order == "" {
		return nil, fmt.Errorf("access.geoip.order: required when GeoIP policy rules are configured or enforcement is enabled")
	}
	policy, err := coregeoip.Compile(coregeoip.CompileInput{
		Order: coregeoip.Order(order),
		Allow: coregeoip.RuleConfig{Countries: append([]string(nil), in.Allow.Countries...), CIDRs: append([]string(nil), in.Allow.CIDRs...)},
		Deny:  coregeoip.RuleConfig{Countries: append([]string(nil), in.Deny.Countries...), CIDRs: append([]string(nil), in.Deny.CIDRs...)},
	})
	if err != nil {
		return nil, fmt.Errorf("access.geoip: %w", err)
	}
	if !in.Enabled {
		return nil, nil
	}
	return policy, nil
}

// compilePrefixes parses a list of IPv4/IPv6 addresses and CIDRs into
// normalized, masked prefixes. Bare addresses become host prefixes and
// IPv4-mapped IPv6 values are unmapped consistently.
func compilePrefixes(field string, raw []string) ([]netip.Prefix, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]netip.Prefix, 0, len(raw))
	for i, value := range raw {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%s[%d]: empty value", field, i)
		}
		if strings.Contains(value, "/") {
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				return nil, fmt.Errorf("%s[%d]: invalid CIDR %q: %w", field, i, value, err)
			}
			addr := prefix.Addr()
			bits := prefix.Bits()
			if addr.Is4In6() {
				addr = addr.Unmap()
				bits -= 96
				if bits < 0 {
					return nil, fmt.Errorf("%s[%d]: invalid mapped IPv6 prefix %q", field, i, value)
				}
			}
			out = append(out, netip.PrefixFrom(addr.Unmap(), bits).Masked())
			continue
		}
		addr, err := netip.ParseAddr(value)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: invalid address %q: %w", field, i, value, err)
		}
		addr = addr.Unmap()
		bits := 128
		if addr.Is4() {
			bits = 32
		}
		out = append(out, netip.PrefixFrom(addr, bits))
	}
	return out, nil
}

func validateGeoIPDatabase(in GeoIPDBConfig) (GeoIPDatabaseSource, time.Duration, error) {
	source := GeoIPDatabaseSource(strings.ToLower(strings.TrimSpace(string(in.Source))))
	if source == "" {
		if strings.TrimSpace(in.Edition) != "" || strings.TrimSpace(in.Directory) != "" || strings.TrimSpace(in.LocalPath) != "" || in.Update.Enabled || strings.TrimSpace(in.Update.Interval) != "" {
			return "", 0, fmt.Errorf("access.geoip.database.source: required when database fields are configured")
		}
		return "", 0, nil
	}
	if source != GeoIPDatabaseSourceManaged && source != GeoIPDatabaseSourceLocal {
		return "", 0, fmt.Errorf("access.geoip.database.source: want managed or local, got %q", in.Source)
	}
	if strings.TrimSpace(in.Edition) != "" && strings.TrimSpace(in.Edition) != defaultGeoIPEdition {
		return "", 0, fmt.Errorf("access.geoip.database.edition: unsupported edition %q", in.Edition)
	}
	if source == GeoIPDatabaseSourceLocal {
		if strings.TrimSpace(in.LocalPath) == "" {
			return "", 0, fmt.Errorf("access.geoip.database.local_path: required for local source")
		}
		if in.Update.Enabled || strings.TrimSpace(in.Update.Interval) != "" {
			return "", 0, fmt.Errorf("access.geoip.database.update: managed updater settings are not valid for local source")
		}
		if strings.TrimSpace(in.Directory) != "" {
			return "", 0, fmt.Errorf("access.geoip.database.directory: only valid for managed source")
		}
		return source, 0, nil
	}
	if strings.TrimSpace(in.LocalPath) != "" {
		return "", 0, fmt.Errorf("access.geoip.database.local_path: only valid for local source")
	}
	interval := coregeoip.DefaultUpdateInterval
	if raw := strings.TrimSpace(in.Update.Interval); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return "", 0, fmt.Errorf("access.geoip.database.update.interval: must be a positive duration")
		}
		interval = parsed
	}
	if interval < coregeoip.MinUpdateInterval || interval > coregeoip.MaxUpdateInterval {
		return "", 0, fmt.Errorf("access.geoip.database.update.interval: must be between %s and %s", coregeoip.MinUpdateInterval, coregeoip.MaxUpdateInterval)
	}
	return source, interval, nil
}

// Enabled reports whether request enforcement is enabled in this projection.
func (c *CompiledGeoIP) Enabled() bool { return c != nil && c.enabled }

// Policy returns the immutable policy pointer, or nil when the request wrapper
// must be omitted.
func (c *CompiledGeoIP) Policy() *coregeoip.Policy {
	if c == nil {
		return nil
	}
	return c.policy
}

func (c *CompiledGeoIP) ClientIPSource() ClientIPSource {
	if c == nil {
		return ClientIPSourceDirect
	}
	return c.clientIPSource
}

// TrustedProxies returns a defensive copy.
func (c *CompiledGeoIP) TrustedProxies() []netip.Prefix {
	if c == nil || c.trustedProxies == nil {
		return nil
	}
	return append([]netip.Prefix(nil), c.trustedProxies...)
}

func (c *CompiledGeoIP) DatabaseSource() GeoIPDatabaseSource {
	if c == nil {
		return ""
	}
	return c.databaseSource
}

func (c *CompiledGeoIP) UpdateInterval() time.Duration {
	if c == nil {
		return 0
	}
	return c.updateInterval
}
