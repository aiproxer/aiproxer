package geoip

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
)

const (
	MaxForwardedHeaderBytes = coregeoip.MaxForwardedHeaderBytes
	MaxForwardedHops        = coregeoip.MaxForwardedHops
)

// Source selects the authoritative client address source.
type Source string

const (
	SourceDirect        Source = "direct"
	SourceXForwardedFor Source = "x_forwarded_for"
	SourceForwarded     Source = "forwarded"
)

// ResolverConfig is immutable input for one request-plane generation.
type ResolverConfig struct {
	Source         Source
	TrustedProxies []netip.Prefix
}

// ResolveClientIP resolves a literal IP without DNS. Forwarding metadata is
// authoritative only when the immediate direct peer is trusted.
func ResolveClientIP(r *http.Request, cfg ResolverConfig) (netip.Addr, error) {
	if r == nil {
		return netip.Addr{}, fmt.Errorf("geoip: nil request")
	}
	direct, err := parseRemoteAddr(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("geoip: direct peer: %w", err)
	}
	source := cfg.Source
	if source == "" {
		source = SourceDirect
	}
	if source == SourceDirect || !isTrusted(direct, cfg.TrustedProxies) {
		return direct, nil
	}
	var values []string
	switch source {
	case SourceXForwardedFor:
		values = r.Header.Values("X-Forwarded-For")
	case SourceForwarded:
		values = r.Header.Values("Forwarded")
	default:
		return netip.Addr{}, fmt.Errorf("geoip: unsupported client-IP source %q", source)
	}
	joined, err := aggregateHeaderValues(values)
	if err != nil {
		return netip.Addr{}, err
	}
	if source == SourceXForwardedFor {
		return resolveXForwardedFor(joined, cfg.TrustedProxies)
	}
	return resolveForwarded(joined, cfg.TrustedProxies)
}

func parseRemoteAddr(raw string) (netip.Addr, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return netip.Addr{}, fmt.Errorf("empty RemoteAddr")
	}
	if addr, err := netip.ParseAddr(raw); err == nil {
		return addr.Unmap(), nil
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		if addr, err := netip.ParseAddr(host); err == nil {
			return addr.Unmap(), nil
		}
	}
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		if addr, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")); err == nil {
			return addr.Unmap(), nil
		}
	}
	return netip.Addr{}, fmt.Errorf("invalid literal address %q", raw)
}

func aggregateHeaderValues(values []string) (string, error) {
	if len(values) == 0 {
		return "", fmt.Errorf("geoip: authoritative forwarding header is missing")
	}
	total := len(values) - 1
	for _, value := range values {
		total += len(value)
		if total > MaxForwardedHeaderBytes {
			return "", fmt.Errorf("geoip: forwarding header exceeds %d bytes", MaxForwardedHeaderBytes)
		}
	}
	return strings.Join(values, ","), nil
}

func resolveXForwardedFor(raw string, trusted []netip.Prefix) (netip.Addr, error) {
	parts := strings.Split(raw, ",")
	if len(parts) == 0 || len(parts) > MaxForwardedHops {
		return netip.Addr{}, fmt.Errorf("geoip: X-Forwarded-For exceeds %d hops", MaxForwardedHops)
	}
	hops := make([]netip.Addr, 0, len(parts))
	for i, part := range parts {
		addr, err := parseAddressToken(part)
		if err != nil {
			return netip.Addr{}, fmt.Errorf("geoip: X-Forwarded-For hop %d: %w", i, err)
		}
		hops = append(hops, addr)
	}
	return firstUntrustedHop(hops, trusted)
}

func resolveForwarded(raw string, trusted []netip.Prefix) (netip.Addr, error) {
	elements, err := splitOutsideQuotes(raw, ',')
	if err != nil {
		return netip.Addr{}, fmt.Errorf("geoip: Forwarded: %w", err)
	}
	if len(elements) == 0 || len(elements) > MaxForwardedHops {
		return netip.Addr{}, fmt.Errorf("geoip: Forwarded exceeds %d hops", MaxForwardedHops)
	}
	hops := make([]netip.Addr, 0, len(elements))
	for i, element := range elements {
		addr, err := parseForwardedElement(element)
		if err != nil {
			return netip.Addr{}, fmt.Errorf("geoip: Forwarded element %d: %w", i, err)
		}
		hops = append(hops, addr)
	}
	return firstUntrustedHop(hops, trusted)
}

func firstUntrustedHop(hops []netip.Addr, trusted []netip.Prefix) (netip.Addr, error) {
	for _, hop := range slices.Backward(hops) {
		if !isTrusted(hop, trusted) {
			return hop, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("geoip: forwarding chain contains no untrusted client hop")
}

func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	addr = addr.Unmap()
	for _, prefix := range trusted {
		prefixAddr := prefix.Addr()
		bits := prefix.Bits()
		if prefixAddr.Is4In6() {
			prefixAddr = prefixAddr.Unmap()
			bits -= 96
			if bits < 0 {
				continue
			}
		}
		if netip.PrefixFrom(prefixAddr.Unmap(), bits).Masked().Contains(addr) {
			return true
		}
	}
	return false
}

func parseAddressToken(raw string) (netip.Addr, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return netip.Addr{}, fmt.Errorf("empty address")
	}
	if addr, err := netip.ParseAddr(raw); err == nil {
		return addr.Unmap(), nil
	}
	if strings.HasPrefix(raw, "[") {
		end := strings.IndexByte(raw, ']')
		if end < 0 || (len(raw) > end+1 && raw[end+1] != ':') {
			return netip.Addr{}, fmt.Errorf("invalid bracketed address %q", raw)
		}
		if addr, err := netip.ParseAddr(raw[1:end]); err == nil {
			return addr.Unmap(), nil
		}
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		if addr, err := netip.ParseAddr(host); err == nil {
			return addr.Unmap(), nil
		}
	}
	return netip.Addr{}, fmt.Errorf("invalid literal address %q", raw)
}

func parseForwardedElement(raw string) (netip.Addr, error) {
	parts, err := splitOutsideQuotes(raw, ';')
	if err != nil {
		return netip.Addr{}, err
	}
	var value string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		key, rawValue, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return netip.Addr{}, fmt.Errorf("malformed parameter")
		}
		if strings.EqualFold(strings.TrimSpace(key), "for") {
			if value != "" {
				return netip.Addr{}, fmt.Errorf("duplicate for parameter")
			}
			value, err = unquoteForwardedValue(strings.TrimSpace(rawValue))
			if err != nil {
				return netip.Addr{}, err
			}
		}
	}
	if value == "" {
		return netip.Addr{}, fmt.Errorf("missing for parameter")
	}
	if strings.EqualFold(value, "unknown") || strings.HasPrefix(value, "_") {
		return netip.Addr{}, fmt.Errorf("obfuscated or unknown for value")
	}
	return parseAddressToken(value)
}

func unquoteForwardedValue(value string) (string, error) {
	if len(value) < 2 || value[0] != '"' {
		return value, nil
	}
	if value[len(value)-1] != '"' {
		return "", fmt.Errorf("unterminated quoted value")
	}
	var b strings.Builder
	b.Grow(len(value) - 2)
	for i := 1; i < len(value)-1; i++ {
		if value[i] == '\\' {
			i++
			if i >= len(value)-1 {
				return "", fmt.Errorf("invalid quoted escape")
			}
		}
		b.WriteByte(value[i])
	}
	return b.String(), nil
}

func splitOutsideQuotes(raw string, delimiter byte) ([]string, error) {
	parts := []string{}
	start := 0
	quoted, escaped := false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if escaped {
			escaped = false
			continue
		}
		if quoted && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if c == delimiter && !quoted {
			part := strings.TrimSpace(raw[start:i])
			if part == "" {
				return nil, fmt.Errorf("empty list element")
			}
			parts = append(parts, part)
			start = i + 1
		}
	}
	if quoted || escaped {
		return nil, fmt.Errorf("unterminated quoted value")
	}
	part := strings.TrimSpace(raw[start:])
	if part == "" {
		return nil, fmt.Errorf("empty list element")
	}
	return append(parts, part), nil
}
