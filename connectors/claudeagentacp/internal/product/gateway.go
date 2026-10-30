package product

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// GatewayConfig is the non-secret endpoint configuration of the gateway
// credential source. Header VALUES are secrets, delivered only through the
// opaque Configure secret bundle; this type carries the bounded yaml schema
// so service parsing and this env owner keep one definition.
type GatewayConfig struct {
	BaseURL string          `yaml:"base_url"`
	Headers []GatewayHeader `yaml:"headers"`
}

type GatewayHeader struct {
	Name string `yaml:"name"`
}

func (c *GatewayConfig) Empty() bool { return c.BaseURL == "" && len(c.Headers) == 0 }

// ValidateGatewayConfig enforces the endpoint policy before anything can
// resolve secrets or spawn: https always, loopback http, no userinfo,
// bounded case-insensitively unique header names holding values only as
// secret refs. Config validation and auth construction both call it so the
// fail-closed posture cannot be bypassed through either seam.
func ValidateGatewayConfig(cfg GatewayConfig) error {
	invalid := func(why string) error { return fmt.Errorf("claudeagentacp: gateway %s", why) }
	if len(cfg.BaseURL) > 4096 {
		return invalid("base_url exceeds the bounded length")
	}
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil || parsed.Host == "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname()))) {
		return invalid("base_url must be an absolute https URL, or http only for a loopback host")
	}
	if parsed.User != nil {
		return invalid("base_url must not carry user information")
	}
	if len(cfg.Headers) > 16 {
		return invalid("headers exceed the bounded count")
	}
	seen := map[string]bool{}
	for i, header := range cfg.Headers {
		if len(header.Name) == 0 || len(header.Name) > 128 || !isHTTPHeaderName(header.Name) {
			return invalid(fmt.Sprintf("headers[%d] is not a bounded header name", i))
		}
		lower := strings.ToLower(header.Name)
		if seen[lower] {
			return invalid(fmt.Sprintf("headers[%d] repeats an existing name", i))
		}
		seen[lower] = true
		// The pinned runtime sends its own Authorization bypass token and
		// replaces this header on the wire. Accepting it would lie about the
		// secret's effect, so it never configures; use any supported custom
		// header names instead.
		if lower == "authorization" {
			return fmt.Errorf("claudeagentacp: gateway headers[%d]: Authorization is overridden by the runtime's own authorization bypass; use a supported custom header name", i)
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isHTTPHeaderName accepts exactly the host secret-reference namespace —
// internal/core/config's backendBindingName (^`[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`).
// This is the single authoritative header-name policy: every name the connector
// accepts must remain provisionable through the documented stock-host secret
// path; RFC-style token names outside it (for example `X+Route`, `X~Route` or
// `X!Route`) reject here so operators are never offered a configuration their
// host cannot provision.
func isHTTPHeaderName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for i, c := range name {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if i == 0 && !alnum {
			return false
		}
		if !alnum && c != '_' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}
